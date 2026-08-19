package parser

import (
	"context"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// resolveRubyQueueKeys is the Tier-2 cross-reference pass for Ruby AMQP queue
// keys. The bunny/kicks patterns capture the queue-name *argument* verbatim,
// but in real apps it is almost never a literal at the call site — it is a
// method reference (`from_queue resolved_queue_name`,
// `channel.queue(QueueNames.cdr_progress_events_queue(org))`). The static
// KeyWalker correctly marks those `key_dynamic`, leaving `key_dynamic_raw`
// holding the method-reference expression.
//
// This pass builds a *same-file* registry of queue-name methods →
// canonical queue key(s), by extracting the string literals a queue-name
// method returns (including the literal branch of an `org ? … : "queue"`
// fallback and the literal argument of a `queue_name(org, "…")` builder), and
// then rewrites every channel/subscriber/publisher node whose
// `key_dynamic_raw` references such a method to carry the resolved
// `queue_name`. Scoping to the same file is deliberate: `resolved_queue_name`
// is redefined in every worker with a *different* queue, so a file-global
// name→key map would fan a worker out to every queue and manufacture false
// cross-service edges. Cross-file / runtime-only references (a queue name that
// only exists in a deploy secret or a registration handshake) are left
// `key_dynamic` for Tier 3 / OTel — never guessed.
func resolveRubyQueueKeys(file string, src []byte, nodes []graph.Node) {
	if len(nodes) == 0 {
		return
	}
	// Cheap gate: only pay for the extra parse when at least one node actually
	// needs resolving.
	needsWork := false
	for i := range nodes {
		if rubyQueueKeyNode(&nodes[i]) {
			needsWork = true
			break
		}
	}
	if !needsWork {
		return
	}

	p := sitter.NewParser()
	p.SetLanguage(rubysitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil || tree == nil {
		return
	}
	defer tree.Close()

	registry := buildRubyQueueRegistry(tree.RootNode(), src)
	if len(registry) == 0 {
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
		keys := registry[method]
		if len(keys) == 0 {
			continue
		}
		// Resolved: clear the dynamic markers and stamp the queue key(s).
		delete(n.Meta, "key_dynamic")
		delete(n.Meta, "key_dynamic_raw")
		n.Meta["key_resolved_via"] = "ruby_queue_method"
		if len(keys) == 1 {
			n.Meta["queue_name"] = keys[0]
			if n.Label == "dynamic" || n.Label == "" {
				n.Label = keys[0]
			}
		} else {
			n.Meta["key_candidates"] = contract.MarshalKeyCandidates(keys)
			n.Label = "branch_enum"
		}
	}
}

// rubyQueueKeyNode reports whether n is a channel/subscriber/publisher node
// whose queue key went unresolved (key_dynamic) and therefore is a candidate
// for method-reference resolution.
func rubyQueueKeyNode(n *graph.Node) bool {
	switch n.Type {
	case graph.NodeTypeChannel, graph.NodeTypeSubscriber, graph.NodeTypePublisher:
	default:
		return false
	}
	return n.Meta["key_dynamic"] == "true" && n.Meta["queue_name"] == "" &&
		n.Meta["key_dynamic_raw"] != ""
}

// rubyQueueMethodRef extracts the referenced method name from a captured
// queue-name expression: the last identifier segment, with any argument list
// stripped. Examples:
//
//	resolved_queue_name                          → resolved_queue_name
//	QUEUE_NAMES.cdr_progress_events_queue(org)   → cdr_progress_events_queue
//	Messaging::Publisher.progress_events_queue_name → progress_events_queue_name
//
// Returns "" for expressions that are not a bare method reference (e.g. a
// hash lookup, an interpolated string), which are left for Tier 3.
func rubyQueueMethodRef(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// An index access in the receiver chain (CONFIG[org].dig(:sym)) is a
	// runtime hash lookup, never a queue-name method reference — reject it so
	// it stays an honest key_dynamic miss (Tier 3 handles the :symbol form).
	if strings.ContainsAny(raw, "[]") {
		return ""
	}
	// Strip an argument list: take everything before the first '('.
	if i := strings.IndexByte(raw, '('); i >= 0 {
		raw = raw[:i]
	}
	// Take the last '.'-separated segment (receiver chain → final method).
	if i := strings.LastIndexByte(raw, '.'); i >= 0 {
		raw = raw[i+1:]
	}
	raw = strings.TrimSpace(raw)
	// Must be a plain method identifier (letters, digits, underscore, maybe a
	// trailing ? or !). Reject anything with brackets, spaces, ::, etc.
	if raw == "" || !isRubyMethodIdent(raw) {
		return ""
	}
	return raw
}

func isRubyMethodIdent(s string) bool {
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		case (r == '?' || r == '!') && i == len(s)-1:
		default:
			return false
		}
	}
	return true
}

// buildRubyQueueRegistry walks the file's method definitions and returns a map
// from queue-name method name → sorted, de-duplicated canonical queue keys.
// A method contributes keys when its name looks queue-related (ends in
// "queue"/"queue_name") — its returned string literals are the candidate
// queues — or when its body calls a `queue_name(receiver, "literal")` builder,
// which maps the *enclosing* method to that literal regardless of its name.
func buildRubyQueueRegistry(root *sitter.Node, src []byte) map[string][]string {
	reg := map[string]map[string]bool{}
	var walkMethods func(n *sitter.Node)
	walkMethods = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if n.Type() == "method" || n.Type() == "singleton_method" {
			name := ""
			if nameNode := n.ChildByFieldName("name"); nameNode != nil {
				name = string(src[nameNode.StartByte():nameNode.EndByte()])
			}
			if name != "" {
				keys := collectRubyQueueKeys(n, src, name)
				if len(keys) > 0 {
					if reg[name] == nil {
						reg[name] = map[string]bool{}
					}
					for _, k := range keys {
						reg[name][k] = true
					}
				}
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walkMethods(n.Child(i))
		}
	}
	walkMethods(root)

	out := make(map[string][]string, len(reg))
	for name, set := range reg {
		keys := make([]string, 0, len(set))
		for k := range set {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out[name] = keys
	}
	return out
}

// collectRubyQueueKeys extracts candidate canonical queue keys defined by a
// single method body. Two sources:
//  1. If the method name is queue-related, every concrete string literal it
//     returns (the literal branch of a ternary fallback, a bare return, etc.).
//  2. Any `queue_name(receiver, "literal")` builder call in the body — the
//     literal (parameterized) is a queue key regardless of the method name.
//
// Nested method definitions are not descended into (they contribute their own
// registry entries).
func collectRubyQueueKeys(method *sitter.Node, src []byte, methodName string) []string {
	queueish := strings.HasSuffix(methodName, "queue") || strings.HasSuffix(methodName, "queue_name")
	seen := map[string]bool{}
	var out []string
	add := func(lit string) {
		key := parameterizeQueueName(lit)
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, key)
	}

	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		// Do not cross into a nested definition.
		if n != method && (n.Type() == "method" || n.Type() == "singleton_method") {
			return
		}
		switch n.Type() {
		case "string":
			// A string literal that is itself a `dig(...)` argument is a hash
			// key into the registration config, not a queue-name literal —
			// exactly the shape `dig("registration", "amqp_queue_name")`
			// hands the handshake pass in stampRubyHandshakeDigRefs. Treating
			// it as a candidate queue key here manufactured a second, bogus
			// candidate ("registration") that always turned a real handshake
			// field into an unresolved ambiguity, even though only one string
			// in the pair is ever a plausible key.
			if queueish && !rubyIsDigArgument(n, src) {
				if lit, ok := rubyConcreteString(n, src); ok {
					add(lit)
				}
			}
		case "call", "method_call", "command":
			// queue_name(receiver, "literal") builder — the second positional
			// argument is the human queue name.
			if m := n.ChildByFieldName("method"); m != nil &&
				string(src[m.StartByte():m.EndByte()]) == "queue_name" {
				if lit, ok := rubyQueueNameBuilderArg(n, src); ok {
					add(lit)
				}
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(method)
	sort.Strings(out)
	return out
}

// rubyQueueNameBuilderArg returns the second positional string argument of a
// `queue_name(receiver, "name")` call, if present and concrete.
// rubyIsDigArgument reports whether node is a positional argument of a `dig`
// call — a hash-key lookup, not a value the enclosing method returns.
func rubyIsDigArgument(node *sitter.Node, src []byte) bool {
	args := node.Parent()
	if args == nil || args.Type() != "argument_list" {
		return false
	}
	call := args.Parent()
	if call == nil {
		return false
	}
	m := call.ChildByFieldName("method")
	return m != nil && string(src[m.StartByte():m.EndByte()]) == "dig"
}

func rubyQueueNameBuilderArg(call *sitter.Node, src []byte) (string, bool) {
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return "", false
	}
	var positionals []*sitter.Node
	for i := 0; i < int(args.ChildCount()); i++ {
		c := args.Child(i)
		if c == nil || !c.IsNamed() {
			continue
		}
		if c.Type() == "pair" || c.Type() == "hash" {
			continue // keyword args are not the queue name
		}
		positionals = append(positionals, c)
	}
	if len(positionals) < 2 {
		return "", false
	}
	return rubyConcreteString(positionals[1], src)
}

// rubyConcreteString reconstructs a Ruby string node's literal content,
// skipping interpolations. Returns ok=false for a pure-interpolation string
// (no concrete text to key on).
func rubyConcreteString(node *sitter.Node, src []byte) (string, bool) {
	if node.Type() != "string" {
		return "", false
	}
	var b strings.Builder
	hasContent := false
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case `"`, "'", "`":
			continue
		case "interpolation":
			continue
		default:
			b.WriteString(string(src[c.StartByte():c.EndByte()]))
			hasContent = true
		}
	}
	if !hasContent {
		return "", false
	}
	return b.String(), true
}

// parameterizeQueueName mirrors Rails' `String#parameterize.underscore`
// applied to queue names: lowercase, every run of non-alphanumeric characters
// collapses to a single "_", leading/trailing "_" trimmed. This makes the
// human form ("cdr progress events") and the wire form ("cdr_progress_events")
// converge on the same key.
func parameterizeQueueName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevSep := true // trims leading separators
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevSep = false
		} else if !prevSep {
			b.WriteByte('_')
			prevSep = true
		}
	}
	return strings.TrimRight(b.String(), "_")
}
