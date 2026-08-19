package parser

import (
	"context"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Tier K.6 step 3 — the registration handshake, parser half.
//
// Tier 2 (resolveRubyQueueKeys) resolves a queue name that a *same-file* method
// returns as a literal. It deliberately gives up on the two shapes that make up
// a registration handshake, because neither is a literal anywhere in the file
// that publishes or consumes:
//
//	# orion — the server DECLARES the queue name in a REST response
//	amqp_progress_events_queue_name: vega_progress_events_queue(organization)
//
//	# orion-vega-agent — the agent READS it back by field name at runtime
//	def progress_events_queue_name
//	  CONFIG[organization_name]&.dig(:amqp_progress_events_queue_name)
//	end
//	channel.queue(Messaging::Publisher.progress_events_queue_name)
//
// The queue's *value* is per-organization and computed at runtime, so value
// matching is impossible; the only static token both repos share is the
// handshake **field symbol**. Joining on it is cross-service work and belongs to
// the linker (LinkAMQPHandshake). This file stamps the three same-file AST facts
// that join needs, and nothing more — it never invents a queue name:
//
//  1. `queue_key` on a queue-name method's function node, so the linker can
//     resolve a cross-file `vega_progress_events_queue(org)` reference that Tier
//     2's same-file registry cannot see.
//  2. `queue_method` on an amqp_field_pair node — the method the declaring side
//     assigns to the field.
//  3. `broker_field` on a key_dynamic publish/consume site whose queue-name
//     method digs a handshake symbol out of the registration config.
func stampRubyHandshakeFacts(file string, src []byte, nodes []graph.Node) {
	if len(nodes) == 0 {
		return
	}
	// Cheap gate, mirroring resolveRubyQueueKeys: only pay for the extra parse
	// when the file holds something this pass could stamp.
	//
	// The queue-name method arm is what distinguishes this gate from Tier 2's.
	// orion's lib/queue_names.rb declares no channel and publishes nothing —
	// it is a module of name builders called from elsewhere — so an AMQP-node
	// gate skips the one file that knows what the queue is actually called.
	relevant := false
	for i := range nodes {
		n := &nodes[i]
		if strings.HasPrefix(n.Meta["pattern"], "amqp_field") || rubyQueueKeyNode(n) ||
			(n.Type == graph.NodeTypeFunction && isRubyQueueMethodName(n.Label)) {
			relevant = true
			break
		}
	}
	if !relevant {
		return
	}

	p := sitter.NewParser()
	p.SetLanguage(rubysitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil || tree == nil {
		return
	}
	defer tree.Close()
	root := tree.RootNode()

	stampRubyQueueKeyMethods(root, src, nodes)
	stampRubyHandshakeFieldValues(root, src, nodes)
	stampRubyHandshakeDigRefs(root, src, nodes)
}

// isRubyQueueMethodName reports whether a method name is queue-related, using
// the same suffix test buildRubyQueueRegistry applies when deciding whether a
// method's string literals are queue keys.
func isRubyQueueMethodName(name string) bool {
	return strings.HasSuffix(name, "queue") || strings.HasSuffix(name, "queue_name")
}

// stampRubyQueueKeyMethods records, on each queue-name method's function node,
// the canonical queue key that method returns. The registry is the same one
// Tier 2 uses; the difference is that Tier 2 consumes it in-file and discards
// it, so a queue-name module (orion's lib/queue_names.rb, whose methods are
// called from controllers and workers in other files) left no trace in the
// graph at all. Publishing it as node meta is what lets the linker resolve a
// cross-file reference without a second parse of a file it does not own.
func stampRubyQueueKeyMethods(root *sitter.Node, src []byte, nodes []graph.Node) {
	registry := buildRubyQueueRegistry(root, src)
	if len(registry) == 0 {
		return
	}
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeFunction {
			continue
		}
		keys := registry[n.Label]
		// Exactly one key or it is not an answer: a method whose body offers
		// several literals is a branch, and picking one would fabricate the
		// queue a handshake resolves to (bug-class #12 — ledger, never guess).
		if len(keys) != 1 {
			continue
		}
		if n.Meta == nil {
			n.Meta = map[string]string{}
		}
		n.Meta["queue_key"] = keys[0]
	}
}

// stampRubyHandshakeFieldValues records the method a declaring side assigns to
// a handshake field — the `vega_progress_events_queue` in
// `amqp_progress_events_queue_name: vega_progress_events_queue(org)`. The
// pattern captures only the key symbol, because the value is an arbitrary
// expression that no tree-sitter query should try to classify.
func stampRubyHandshakeFieldValues(root *sitter.Node, src []byte, nodes []graph.Node) {
	// line → value method, for every `amqp_*_queue_name:` pair in the file.
	byLine := map[int]string{}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if n.Type() == "pair" {
			if k := n.ChildByFieldName("key"); k != nil {
				key := strings.Trim(string(src[k.StartByte():k.EndByte()]), ":")
				if strings.HasPrefix(key, "amqp_") && strings.HasSuffix(key, "_queue_name") {
					if v := n.ChildByFieldName("value"); v != nil {
						expr := string(src[v.StartByte():v.EndByte()])
						if m := rubyQueueMethodRef(expr); m != "" {
							byLine[int(k.StartPoint().Row)+1] = m
						}
					}
				}
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(root)
	if len(byLine) == 0 {
		return
	}
	for i := range nodes {
		n := &nodes[i]
		if n.Meta["pattern"] != "amqp_field_pair" {
			continue
		}
		if m := byLine[n.Line]; m != "" {
			n.Meta["queue_method"] = m
		}
	}
}

// stampRubyHandshakeDigRefs closes the reading side. A publish or consume site
// whose queue name is `Messaging::Publisher.progress_events_queue_name` is
// key_dynamic after Tier 2, because that method returns no literal — it digs
// the handshake field out of the config the registration response filled. This
// walks queue-name methods for exactly that dig and stamps the field symbol on
// every site referencing the method, which is the token the linker joins on.
func stampRubyHandshakeDigRefs(root *sitter.Node, src []byte, nodes []graph.Node) {
	fieldOf := buildRubyHandshakeDigRegistry(root, src)
	if len(fieldOf) == 0 {
		return
	}
	for i := range nodes {
		n := &nodes[i]
		if !rubyQueueKeyNode(n) {
			continue
		}
		method := rubyQueueMethodRef(n.Meta["key_dynamic_raw"])
		if method == "" {
			continue
		}
		if field := fieldOf[method]; field != "" {
			n.Meta["broker_field"] = field
		}
	}
}

// buildRubyHandshakeDigRegistry maps method name → the single handshake field
// symbol its body digs. A method that digs more than one field is not a queue
// accessor and is dropped rather than guessed at.
func buildRubyHandshakeDigRegistry(root *sitter.Node, src []byte) map[string]string {
	found := map[string]map[string]bool{}
	var walkMethods func(n *sitter.Node)
	walkMethods = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if n.Type() == "method" || n.Type() == "singleton_method" {
			name := ""
			if nn := n.ChildByFieldName("name"); nn != nil {
				name = string(src[nn.StartByte():nn.EndByte()])
			}
			if name != "" {
				for _, f := range collectRubyDigFields(n, src) {
					if found[name] == nil {
						found[name] = map[string]bool{}
					}
					found[name][f] = true
				}
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walkMethods(n.Child(i))
		}
	}
	walkMethods(root)

	out := make(map[string]string, len(found))
	for name, set := range found {
		if len(set) != 1 {
			continue
		}
		fields := make([]string, 0, 1)
		for f := range set {
			fields = append(fields, f)
		}
		sort.Strings(fields) // determinism is free here; map order never reaches output (#2)
		out[name] = fields[0]
	}
	return out
}

// collectRubyDigFields returns the `amqp_*_queue_name` symbols a method body
// digs. Nested definitions are not descended into — they register their own
// entries, exactly as collectRubyQueueKeys does.
func collectRubyDigFields(method *sitter.Node, src []byte) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if n != method && (n.Type() == "method" || n.Type() == "singleton_method") {
			return
		}
		if m := n.ChildByFieldName("method"); m != nil &&
			string(src[m.StartByte():m.EndByte()]) == "dig" {
			if args := n.ChildByFieldName("arguments"); args != nil {
				for i := 0; i < int(args.ChildCount()); i++ {
					c := args.Child(i)
					if c == nil {
						continue
					}
					var sym string
					switch c.Type() {
					case "simple_symbol":
						sym = strings.TrimPrefix(string(src[c.StartByte():c.EndByte()]), ":")
					case "string":
						// The string-keyed two-arg form: `dig("registration",
						// "amqp_queue_name")`. Only the field-name argument
						// matters; a plain outer key like "registration" is
						// harmless to test against the same suffix/prefix
						// check below since it will simply never match.
						sym = strings.Trim(string(src[c.StartByte():c.EndByte()]), `"'`)
					default:
						continue
					}
					if strings.HasPrefix(sym, "amqp_") && strings.HasSuffix(sym, "_queue_name") && !seen[sym] {
						seen[sym] = true
						out = append(out, sym)
					}
				}
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(method)
	return out
}
