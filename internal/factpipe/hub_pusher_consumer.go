package factpipe

import (
	"context"
	"os"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/railsview"
)

// hub_pusher_consumer.go is the "pusher_wrapper_erb" hub provider (see
// hub.go) — the Tier FX FX.8 migration of the Ruby half of the retired
// internal/linker/pusher_consumer.go's EnrichPusherConsumers.
//
// The pattern rules/ruby/pusher_consumer.dl needs is a plain "mint a
// subscriber node per resolved ERB call site" join — but resolving that call
// site needs two things no `.dl` rule or `facts:` tree-sitter query can
// supply:
//
//  1. The wrapper class's notify_*->event / CONST / CHANNELS[:x] table — a
//     real AST walk of an arbitrary Ruby class body (collectPusherWrapperFacts
//     in the retired Go, ported here verbatim as pchWrapperTables), not an
//     extraction verb (it's table-shaped derived state, not a value pulled
//     from one node).
//  2. The ERB file's embedded Ruby, in a form the ruby grammar can actually
//     parse. patterns/erb/*.yaml's `language: erb` frameworks parse .erb
//     files with the dedicated erb tree-sitter grammar, whose embedded-code
//     nodes are flat, childless leaves under that grammar — there is no
//     tree-sitter query that reaches a `pusher_config(channel:, event:)`
//     call's keyword-argument structure through it. internal/parser/erb.go's
//     base (non-factpipe) pass instead runs the *ruby* grammar directly
//     against railsview.SplitERB's virtualRuby view (ERB delimiters and
//     surrounding markup blanked to spaces, Ruby code kept at its original
//     byte/line position) — this hub provider does the same, using its own
//     os.ReadFile the same way the retired Go pass, and internal/factpipe's
//     other primitives (config_value, table_facts), already do real disk I/O
//     for a table a `.dl` rule then joins against.
//
// Gate: identical to the retired pass — this returns nil unless the graph
// already has a `pusher_trigger`/`pusher_trigger_async` publisher node
// (base, non-factpipe patterns/ruby/pusher.yaml mints those at parse time),
// so a repo with no server-side Pusher usage never re-parses every .erb file
// for nothing.
func init() { RegisterHub("pusher_wrapper_erb", pusherWrapperERBHub) }

// pusherSubscribeSitePred is the fact predicate this hub asserts:
// (Svc, File, Line, Pattern, ChannelSeg, Event, Label).
const pusherSubscribeSitePred = "pusher_subscribe_site"

// pusherReactPropConfigPred is the fact predicate this hub asserts for the
// react_component(..., pusherConfig: {...}) half (Tier FX 2026-09-15,
// internal/linker/pusher_js_consumer.go's retired pusherPropConfigFromERB):
// (Svc, Component, Channel, Event) — one row per component name, later ERB
// files winning on a repeat, matching the retired pass's plain
// `out[comp] = cfg` map-assignment semantics. pusher_js_consumer.yaml's `.dl`
// joins this against the pusher_js_subscribe_sites hub's dynamic sites by
// component/enclosing-class name; it cannot resolve a controller-built
// `@pusher_config` ivar, same honest gap as the retired Go.
const pusherReactPropConfigPred = "pusher_react_prop_config"

func pusherWrapperERBHub(nodes []graph.Node, files []string, _ string, _ []graph.LinkHint, _ graph.SchemaConfig) []Fact {
	hubFiles := map[string]bool{}
	for i := range nodes {
		switch nodes[i].Meta["pattern"] {
		case "pusher_trigger", "pusher_trigger_async":
			if nodes[i].File != "" {
				hubFiles[nodes[i].File] = true
			}
		}
	}
	if len(hubFiles) == 0 {
		return nil
	}

	eventByMethod := map[string]map[string]string{}
	constVals := map[string]map[string]string{}
	hashConstVals := map[string]map[string]map[string]string{}

	for file := range hubFiles {
		src, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		root, release := pchParseRuby(src)
		if root == nil {
			continue
		}
		pchCollectWrapperFacts(root, src, eventByMethod, constVals, hashConstVals)
		release()
	}
	if len(constVals) == 0 && len(hashConstVals) == 0 {
		return nil
	}

	svc := ""
	if len(nodes) > 0 {
		svc = nodes[0].Service
	}

	sortedFiles := append([]string(nil), files...)
	sort.Strings(sortedFiles)

	reactCfg := map[string]map[string]string{}
	var out []Fact
	for _, file := range sortedFiles {
		if !strings.HasSuffix(file, ".erb") || graph.IsTestFilePath(file) {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		_, ruby := railsview.SplitERB(src)
		root, release := pchParseRuby(ruby)
		if root == nil {
			continue
		}
		out = append(out, pchWalkERBCalls(root, ruby, file, svc, constVals, hashConstVals)...)
		pchWalkERBReactComponents(root, ruby, reactCfg, constVals, hashConstVals)
		release()
	}

	comps := make([]string, 0, len(reactCfg))
	for c := range reactCfg {
		comps = append(comps, c)
	}
	sort.Strings(comps)
	for _, comp := range comps {
		cfg := reactCfg[comp]
		out = append(out, Fact{
			Pred:   pusherReactPropConfigPred,
			Args:   []Atom{Str(svc), Str(comp), Str(cfg["channel"]), Str(cfg["event"])},
			Origin: Origin{Kind: OriginPrimitive, Pattern: pusherReactPropConfigPred},
		})
	}
	return out
}

// pchWalkERBReactComponents harvests `react_component("X", { pusherConfig: {
// channel:, event: } })` (or `pusherConfig: pusher_config(channel:, event:)`)
// call sites into dst, keyed by component label. Ported from the retired
// internal/linker/pusher_js_consumer.go's pusherPropConfigFromERB (Tier FX
// 2026-09-15) — reuses this file's own pchRefSegment/pchRefLiteral, since the
// channel/event resolution rules (literal string, PusherClient:: constant,
// CHANNELS[:x] hash lookup) are identical to the ERB-helper call shape
// pchWalkERBCalls already resolves.
func pchWalkERBReactComponents(
	root *sitter.Node, ruby []byte, dst map[string]map[string]string,
	constVals map[string]map[string]string,
	hashConstVals map[string]map[string]map[string]string,
) {
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "call" {
			if mn := n.ChildByFieldName("method"); mn != nil && mn.Content(ruby) == "react_component" {
				comp := ""
				if a0 := pchArgAt(n, 0); a0 != nil && a0.Type() == "string" {
					comp = pchStringLiteral(a0, ruby)
				}
				if hv := pchDescendantPairValue(n, ruby, "pusherConfig"); comp != "" && hv != nil {
					if cfg := pchExtractConfigHash(hv, ruby, constVals, hashConstVals); len(cfg) > 0 {
						dst[comp] = cfg
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
}

// pchDescendantPairValue returns the value node of the first `pair` under n
// whose key matches (bare symbol, `:sym`, or `"str"`).
func pchDescendantPairValue(n *sitter.Node, src []byte, key string) *sitter.Node {
	var found *sitter.Node
	var walk func(*sitter.Node)
	walk = func(m *sitter.Node) {
		if found != nil {
			return
		}
		if m.Type() == "pair" {
			if k := m.ChildByFieldName("key"); k != nil {
				kk := strings.Trim(strings.TrimSuffix(strings.TrimPrefix(k.Content(src), ":"), ":"), `"'`)
				if kk == key {
					found = m.ChildByFieldName("value")
					return
				}
			}
		}
		for i := 0; i < int(m.NamedChildCount()); i++ {
			walk(m.NamedChild(i))
		}
	}
	walk(n)
	return found
}

// pchExtractConfigHash pulls channel/event/key out of either a hash literal or
// a `pusher_config(channel:, event:)` helper call.
func pchExtractConfigHash(
	v *sitter.Node, src []byte,
	constVals map[string]map[string]string,
	hashConstVals map[string]map[string]map[string]string,
) map[string]string {
	out := map[string]string{}
	getPair := func(node *sitter.Node, key string) *sitter.Node {
		for i := 0; i < int(node.NamedChildCount()); i++ {
			p := node.NamedChild(i)
			if p.Type() != "pair" {
				continue
			}
			k := p.ChildByFieldName("key")
			if k == nil {
				continue
			}
			if strings.TrimSuffix(strings.TrimPrefix(k.Content(src), ":"), ":") == key {
				return p.ChildByFieldName("value")
			}
		}
		return nil
	}
	container := v
	if v.Type() == "call" {
		container = v.ChildByFieldName("arguments")
	}
	if container == nil {
		return out
	}
	if cv := getPair(container, "channel"); cv != nil {
		if cv.Type() == "string" {
			if lit := pchStringLiteral(cv, src); lit != "" {
				out["channel"] = lit
			} else {
				out["channel"] = pchChannelFromString(cv, src) // interpolated → segment
			}
		} else {
			out["channel"] = pchRefSegment(cv, src, constVals, hashConstVals)
		}
	}
	if ev := getPair(container, "event"); ev != nil {
		out["event"] = pchRefLiteral(ev, src, constVals)
	}
	for k, val := range out {
		if val == "" {
			delete(out, k)
		}
	}
	return out
}

// pchParseRuby parses a Ruby byte slice (a raw .rb file, or an ERB file's
// virtualRuby view) with the ruby tree-sitter grammar. The caller must call
// release once done with root.
func pchParseRuby(src []byte) (root *sitter.Node, release func()) {
	p := sitter.NewParser()
	p.SetLanguage(rubysitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil || tree == nil {
		return nil, func() {}
	}
	return tree.RootNode(), func() { tree.Close() }
}

// pchCollectWrapperFacts is collectPusherWrapperFacts, ported verbatim from
// the retired internal/linker/pusher_producer.go (both EnrichPusherProducers
// and EnrichPusherConsumers called the same function there). Reused as-is
// (not duplicated) by hub_pusher_producer.go's pusher_producer_sites hub,
// FX.8.11's own migration of EnrichPusherProducers.
func pchCollectWrapperFacts(
	root *sitter.Node, src []byte,
	eventByMethod map[string]map[string]string,
	constVals map[string]map[string]string,
	hashConstVals map[string]map[string]map[string]string,
) {
	var walkClass func(n *sitter.Node)
	walkClass = func(n *sitter.Node) {
		if n.Type() == "class" {
			nameNode := n.ChildByFieldName("name")
			if nameNode != nil {
				cls := nameNode.Content(src)
				body := n.ChildByFieldName("body")
				if body != nil {
					pchCollectClassBody(cls, body, src, eventByMethod, constVals, hashConstVals)
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walkClass(n.NamedChild(i))
		}
	}
	walkClass(root)
}

func pchCollectClassBody(
	cls string, body *sitter.Node, src []byte,
	eventByMethod map[string]map[string]string,
	constVals map[string]map[string]string,
	hashConstVals map[string]map[string]map[string]string,
) {
	if constVals[cls] == nil {
		constVals[cls] = map[string]string{}
	}
	if hashConstVals[cls] == nil {
		hashConstVals[cls] = map[string]map[string]string{}
	}
	if eventByMethod[cls] == nil {
		eventByMethod[cls] = map[string]string{}
	}

	var walkConst func(n *sitter.Node)
	walkConst = func(n *sitter.Node) {
		if n.Type() == "assignment" {
			l := n.ChildByFieldName("left")
			r := n.ChildByFieldName("right")
			if l != nil && r != nil && l.Type() == "constant" {
				name := l.Content(src)
				rr := r
				if rr.Type() == "call" { // `{...}.freeze`
					if recv := rr.ChildByFieldName("receiver"); recv != nil {
						rr = recv
					}
				}
				switch rr.Type() {
				case "string":
					constVals[cls][name] = pchStringLiteral(rr, src)
				case "hash":
					hashConstVals[cls][name] = pchHashLiterals(rr, src)
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walkConst(n.NamedChild(i))
		}
	}
	walkConst(body)

	for i := 0; i < int(body.NamedChildCount()); i++ {
		m := body.NamedChild(i)
		if m.Type() != "method" {
			continue
		}
		mn := m.ChildByFieldName("name")
		mb := m.ChildByFieldName("body")
		if mn == nil || mb == nil {
			continue
		}
		mName := mn.Content(src)
		params := map[string]bool{}
		if pn := m.ChildByFieldName("parameters"); pn != nil {
			for j := 0; j < int(pn.NamedChildCount()); j++ {
				c := pn.NamedChild(j)
				if c.Type() == "identifier" {
					params[c.Content(src)] = true
				} else if c.Type() == "optional_parameter" || c.Type() == "keyword_parameter" {
					if nm := c.ChildByFieldName("name"); nm != nil {
						params[nm.Content(src)] = true
					}
				}
			}
		}
		var found string
		var walkPush func(n *sitter.Node)
		walkPush = func(n *sitter.Node) {
			if found != "" {
				return
			}
			if n.Type() == "call" {
				if meth := n.ChildByFieldName("method"); meth != nil && meth.Content(src) == "push" {
					if args := n.ChildByFieldName("arguments"); args != nil && args.NamedChildCount() >= 2 {
						ev := args.NamedChild(1)
						if ev.Type() == "identifier" && params[ev.Content(src)] {
							// this IS the wrapper's own `push(body, event_type)`
						} else {
							found = pchLiteralOrConst(ev, src, cls, constVals)
						}
					}
				}
			}
			for i := 0; i < int(n.NamedChildCount()); i++ {
				walkPush(n.NamedChild(i))
			}
		}
		walkPush(mb)
		if found != "" {
			eventByMethod[cls][mName] = found
		}
	}
}

// pchWalkERBCalls is EnrichPusherConsumers' per-file walk, ported to emit
// Facts instead of minting graph.Node/graph.Edge directly. Pattern names
// ("pusher_subscribe_erb" / "pusher_subscribe_erb_channel") are unchanged
// from the retired Go so a downstream consumer keying on Meta["pattern"]
// (none currently do) would see the same values.
func pchWalkERBCalls(
	root *sitter.Node, ruby []byte, file, svc string,
	constVals map[string]map[string]string,
	hashConstVals map[string]map[string]map[string]string,
) []Fact {
	var out []Fact
	emit := func(pattern, chanSeg, event string, line int) {
		if chanSeg == "" {
			return
		}
		label := chanSeg
		if event != "" {
			label = chanSeg + " " + event
		}
		out = append(out, Fact{
			Pred: pusherSubscribeSitePred,
			Args: []Atom{
				Str(svc), Str(file), Int(int64(line)), Str(pattern),
				Str(chanSeg), Str(event), Str(label),
			},
			Origin: Origin{Kind: OriginPrimitive, File: file, Line: line, Pattern: pusherSubscribeSitePred},
		})
	}

	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "call" {
			if mn := n.ChildByFieldName("method"); mn != nil {
				switch mn.Content(ruby) {
				case "pusher_config":
					chArg := pchKeywordArg(n, ruby, "channel")
					chSeg := pchRefSegment(chArg, ruby, constVals, hashConstVals)
					if chSeg != "" {
						ev := pchRefLiteral(pchKeywordArg(n, ruby, "event"), ruby, constVals)
						emit("pusher_subscribe_erb", chSeg, ev, int(n.StartPoint().Row)+1)
					}
				case "render":
					if a0 := pchArgAt(n, 0); a0 != nil && a0.Type() == "string" &&
						pchStringLiteral(a0, ruby) == "shared/pusher" {
						chArg := pchKeywordArg(n, ruby, "pusher_channel")
						chSeg := pchRefSegment(chArg, ruby, constVals, hashConstVals)
						if chSeg != "" {
							emit("pusher_subscribe_erb_channel", chSeg, "", int(n.StartPoint().Row)+1)
						}
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return out
}

// --- small Ruby AST text helpers, ported from the retired
// internal/linker/pusher_producer.go / pusher_consumer.go. Duplicated rather
// than shared: those files stay hand-written Go (the producer side and the
// JS-dataflow half of the consumer side, see their package docs), and
// internal/factpipe cannot import internal/linker (linker already imports
// factpipe/pipeline) — see hub.go's package doc.

func pchArgAt(call *sitter.Node, i int) *sitter.Node {
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return nil
	}
	pos := 0
	for j := 0; j < int(args.NamedChildCount()); j++ {
		c := args.NamedChild(j)
		if c.Type() == "pair" || c.Type() == "hash_splat_argument" || c.Type() == "block_argument" {
			continue
		}
		if pos == i {
			return c
		}
		pos++
	}
	return nil
}

func pchKeywordArg(call *sitter.Node, src []byte, name string) *sitter.Node {
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return nil
	}
	for i := 0; i < int(args.NamedChildCount()); i++ {
		c := args.NamedChild(i)
		if c.Type() != "pair" {
			continue
		}
		k := c.ChildByFieldName("key")
		if k == nil {
			continue
		}
		key := strings.TrimSuffix(strings.TrimPrefix(k.Content(src), ":"), ":")
		if key == name {
			return c.ChildByFieldName("value")
		}
	}
	return nil
}

// pchRefSegment reduces an ERB channel expression to the one stable segment
// both producer and consumer agree on: a bare string's literal middle
// segment, a `PusherClient::CONST` value, or a `PusherClient::CHANNELS[:sym]`
// lookup.
func pchRefSegment(
	n *sitter.Node, src []byte,
	constVals map[string]map[string]string,
	hashConstVals map[string]map[string]map[string]string,
) string {
	if n == nil {
		return ""
	}
	switch n.Type() {
	case "string":
		return pchChannelFromString(n, src)
	case "scope_resolution":
		scope := n.ChildByFieldName("scope")
		name := n.ChildByFieldName("name")
		if scope != nil && name != nil {
			return constVals[scope.Content(src)][name.Content(src)]
		}
	case "element_reference":
		obj := n.ChildByFieldName("object")
		if obj == nil || obj.Type() != "scope_resolution" || n.NamedChildCount() < 2 {
			return ""
		}
		s := obj.ChildByFieldName("scope")
		nm := obj.ChildByFieldName("name")
		if s == nil || nm == nil {
			return ""
		}
		key := strings.TrimPrefix(strings.Trim(n.NamedChild(1).Content(src), `"'`), ":")
		if h := hashConstVals[s.Content(src)][nm.Content(src)]; h != nil {
			return h[key]
		}
	}
	return ""
}

// pchRefLiteral resolves an ERB event expression to its string value: a
// literal, or a `PusherClient::CONST`.
func pchRefLiteral(n *sitter.Node, src []byte, constVals map[string]map[string]string) string {
	if n == nil {
		return ""
	}
	switch n.Type() {
	case "string":
		return pchStringLiteral(n, src)
	case "scope_resolution":
		scope := n.ChildByFieldName("scope")
		name := n.ChildByFieldName("name")
		if scope != nil && name != nil {
			return constVals[scope.Content(src)][name.Content(src)]
		}
	}
	return ""
}

// pchLiteralOrConst resolves an event argument to its string value: a string
// literal, or a `FOO` / `Class::FOO` constant defined on the wrapper.
func pchLiteralOrConst(arg *sitter.Node, src []byte, cls string, constVals map[string]map[string]string) string {
	if arg == nil {
		return ""
	}
	switch arg.Type() {
	case "string":
		return pchStringLiteral(arg, src)
	case "constant":
		return constVals[cls][arg.Content(src)]
	case "scope_resolution":
		if nm := arg.ChildByFieldName("name"); nm != nil {
			return constVals[cls][nm.Content(src)]
		}
	}
	return ""
}

func pchStringLiteral(strNode *sitter.Node, src []byte) string {
	var b strings.Builder
	for i := 0; i < int(strNode.NamedChildCount()); i++ {
		c := strNode.NamedChild(i)
		if c.Type() == "string_content" {
			b.WriteString(c.Content(src))
		} else if c.Type() == "interpolation" {
			return "" // not a pure literal
		}
	}
	return b.String()
}

func pchHashLiterals(hash *sitter.Node, src []byte) map[string]string {
	out := map[string]string{}
	for i := 0; i < int(hash.NamedChildCount()); i++ {
		p := hash.NamedChild(i)
		if p.Type() != "pair" {
			continue
		}
		k := p.ChildByFieldName("key")
		v := p.ChildByFieldName("value")
		if k == nil || v == nil || v.Type() != "string" {
			continue
		}
		key := strings.TrimSuffix(strings.TrimPrefix(k.Content(src), ":"), ":")
		key = strings.Trim(key, `"'`)
		out[key] = pchStringLiteral(v, src)
	}
	return out
}

// pchChannelFromString extracts the literal segment(s) of a Ruby string,
// dropping interpolations and the `Rails.env` / trailing-id noise around
// them.
func pchChannelFromString(strNode *sitter.Node, src []byte) string {
	var lits []string
	for i := 0; i < int(strNode.NamedChildCount()); i++ {
		c := strNode.NamedChild(i)
		if c.Type() == "string_content" {
			lits = append(lits, c.Content(src))
		}
	}
	joined := strings.Join(lits, "")
	var segs []string
	for _, s := range strings.Split(joined, ".") {
		s = strings.Trim(s, " _")
		if s == "" || s == "env" {
			continue
		}
		segs = append(segs, s)
	}
	if len(segs) == 0 {
		return ""
	}
	best := segs[0]
	for _, s := range segs[1:] {
		if len(s) > len(best) {
			best = s
		}
	}
	return best
}
