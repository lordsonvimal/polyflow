package factpipe

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	tssitter "github.com/smacker/go-tree-sitter/typescript/typescript"
	tsxsitter "github.com/smacker/go-tree-sitter/typescript/tsx"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/railsview"
)

// hub_pusher_js_consumer.go is the "pusher_js_subscribe_sites" hub provider
// (see hub.go) — the Tier FX FX.8 migration of the JS-dataflow half of
// internal/linker/pusher_js_consumer.go's retired EnrichPusherConsumersJS
// (SPA.7).
//
// What patterns/javascript/pusher_js_consumer.yaml's `.dl` needs is a plain
// "mint a subscriber node per resolved JS subscribe site, bridge it to a
// matching publisher" join — but *finding* those sites needs real,
// order-sensitive JS AST tracking no tree-sitter query can express in one
// match: a `new Pusher(...)` instance binding (including through
// `formal_parameters`) has to be correlated by plain identifier text against
// a LATER, textually-unrelated `.subscribe()`/`.bind()` call site, with a
// single-subscribe-per-file fallback when the LHS/receiver names don't line
// up. That is exactly this file's retained detectPusherJSSubs (ported
// verbatim from the retired Go, renamed pjcDetectSubs) — real Go dataflow
// tracking, the same class of "needs re-parsing, not just matching" hub.go's
// package doc describes for the ERB half.
//
// What this hub does NOT keep in Go: the ERB `pusherConfig` prop merge (now
// the pusher_wrapper_erb hub's pusher_react_prop_config fact, joined here in
// Go only because the retired pass's channel/event *override* logic —
// "literal wins, else ERB, independently per field" — is a handful of map
// lookups no `.dl` join can express without string-empty comparisons the
// engine's "no string operations" rule forbids), the producer-event bridge
// (now rules/javascript/pusher_js_consumer.dl's join against `node`/
// `node_meta` base facts), the node mint / edge / confidence / fan-out policy
// (now patterns/javascript/pusher_js_consumer.yaml's `emit:` block), and the
// dynamic-channel ledger (now an `unresolved:` block on the same emit spec).
func init() { RegisterHub("pusher_js_subscribe_sites", pusherJSSubscribeSitesHub) }

// Fact predicates this hub asserts. Both buckets carry the same 8 columns —
// (Svc, File, Line, Channel-or-Expr, Event, PropConfigLabel, EventsExtra,
// Label) — split into two predicates rather than a single row + a
// Dynamic="true"/"" flag column, because the "no string operations" `.dl`
// rule (docs/declarative-framework-pipeline-plan.md § the `.dl` extension)
// has no way to branch on whether a column is empty; the branch happens once,
// in Go, at fact-emission time.
const (
	pusherJSSubscribeResolvedPred = "pusher_js_subscribe_resolved"
	pusherJSSubscribeDynamicPred  = "pusher_js_subscribe_dynamic"
	// pusherJSSubscribeEventPred is one row per `.bind("evt", ...)` literal a
	// subscribe site carries (there can be more than one) — the fan-out
	// candidate set for the producer-event bridge, kept separate from the
	// display Event column (the subscribe site's first event, sorted) because
	// the bridge join needs ALL of them, the mint's Label needs only one.
	pusherJSSubscribeEventPred = "pusher_js_subscribe_event"
)

func pusherJSSubscribeSitesHub(nodes []graph.Node, files []string, _ string) []Fact {
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

	sortedFiles := append([]string(nil), files...)
	sort.Strings(sortedFiles)

	reactCfg := map[string]map[string]string{}
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
		pchWalkERBReactComponents(root, ruby, reactCfg, constVals, hashConstVals)
		release()
	}

	svc := ""
	if len(nodes) > 0 {
		svc = nodes[0].Service
	}

	var jsFiles []string
	for _, f := range sortedFiles {
		switch strings.ToLower(filepath.Ext(f)) {
		case ".js", ".jsx", ".mjs", ".es6", ".ts", ".tsx":
			jsFiles = append(jsFiles, f)
		}
	}

	var out []Fact
	for _, f := range jsFiles {
		if graph.IsTestFilePath(f) {
			continue
		}
		src, root, ok := pjcParseJS(f)
		if !ok || root == nil {
			continue
		}
		subs := pjcDetectSubs(root, src)
		if len(subs) == 0 {
			continue
		}
		base := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
		for _, s := range subs {
			if s.enclosingLabel == "" {
				s.enclosingLabel = base
			}

			channel, event, dynamic := s.channelLit, "", s.channelLit == ""
			if len(s.events) > 0 {
				event = s.events[0]
			}
			if dynamic && s.propConfig != "" {
				if cfg := reactCfg[s.enclosingLabel]; cfg != nil {
					if c := cfg["channel"]; c != "" {
						channel, dynamic = c, false
					}
					if event == "" {
						event = cfg["event"]
					}
				}
			}

			label := channel
			if dynamic {
				label = "(dynamic)"
			}
			if event != "" {
				label = strings.TrimSpace(label + " " + event)
			}
			propLabel := ""
			if s.propConfig != "" {
				propLabel = s.enclosingLabel
			}
			eventsExtra := ""
			if len(s.events) > 1 {
				eventsExtra = strings.Join(s.events, ",")
			}

			origin := Origin{Kind: OriginPrimitive, File: f, Line: s.line, Pattern: "pusher_subscribe_js"}
			if dynamic {
				out = append(out, Fact{
					Pred: pusherJSSubscribeDynamicPred,
					Args: []Atom{
						Str(svc), Str(f), Int(int64(s.line)), Str(s.channelExpr),
						Str(event), Str(propLabel), Str(eventsExtra), Str(label),
					},
					Origin: origin,
				})
			} else {
				out = append(out, Fact{
					Pred: pusherJSSubscribeResolvedPred,
					Args: []Atom{
						Str(svc), Str(f), Int(int64(s.line)), Str(channel),
						Str(event), Str(propLabel), Str(eventsExtra), Str(label),
					},
					Origin: origin,
				})
			}
			for _, ev := range s.events {
				out = append(out, Fact{
					Pred:   pusherJSSubscribeEventPred,
					Args:   []Atom{Str(svc), Str(f), Int(int64(s.line)), Str(ev)},
					Origin: origin,
				})
			}
		}
	}
	return out
}

// --- pjcParseJS: read + parse one JS/TS/JSX/TSX file. ---

func pjcParseJS(file string) (src []byte, root *sitter.Node, ok bool) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, false
	}
	lang := tssitter.GetLanguage()
	if ext := strings.ToLower(filepath.Ext(file)); ext == ".tsx" || ext == ".jsx" {
		lang = tsxsitter.GetLanguage()
	}
	root, err = sitter.ParseCtx(context.Background(), src, lang)
	if err != nil || root == nil {
		return nil, nil, false
	}
	return src, root, true
}

// --- pjcSub / pjcDetectSubs: ported verbatim (renamed) from the retired
// internal/linker/pusher_js_consumer.go's pusherJSSub / detectPusherJSSubs —
// see this file's package doc for why the JS-dataflow tracking itself stays
// hand-written Go. ---

// pjcSub is one `<instance>.subscribe(<arg>)` site in a JS file.
type pjcSub struct {
	line           int
	channelLit     string   // "" when the channel is not a plain string literal
	channelExpr    string   // raw source text of the arg → key_dynamic_raw
	events         []string // literal args of `.bind("evt", …)` on the subscription
	propConfig     string   // the prop name when the arg is `<prop>.channel`
	enclosingLabel string   // PascalCase class name, else file basename — for the ERB bridge
	lhsBind        string   // assignment target the subscribe result is stored in
}

// pjcDetectSubs walks a parsed JS/TS tree for Pusher subscribe/bind sites.
func pjcDetectSubs(root *sitter.Node, src []byte) []pjcSub {
	txt := func(n *sitter.Node) string {
		if n == nil {
			return ""
		}
		return n.Content(src)
	}
	endsWithPusher := func(s string) bool {
		return s == "Pusher" || s == "pusher" || strings.HasSuffix(s, ".Pusher") || strings.HasSuffix(s, ".pusher")
	}

	// pass 1 — names bound to a Pusher instance
	instances := map[string]bool{}
	var walk1 func(n *sitter.Node)
	walk1 = func(n *sitter.Node) {
		switch n.Type() {
		case "new_expression":
			if c := n.ChildByFieldName("constructor"); c != nil && endsWithPusher(txt(c)) {
				if name := pjcBindingTargetName(n, src); name != "" {
					instances[name] = true
				}
			}
		case "variable_declarator", "assignment_expression":
			var val *sitter.Node
			var name string
			if n.Type() == "variable_declarator" {
				val, name = n.ChildByFieldName("value"), txt(n.ChildByFieldName("name"))
			} else {
				val, name = n.ChildByFieldName("right"), txt(n.ChildByFieldName("left"))
			}
			if val != nil && val.Type() == "member_expression" && endsWithPusher(txt(val)) {
				if name != "" {
					instances[name] = true
				}
			}
		case "formal_parameters":
			for i := 0; i < int(n.NamedChildCount()); i++ {
				pjcCollectParamIdents(n.NamedChild(i), src, func(id string) {
					if id == "pusher" || id == "Pusher" {
						instances[id] = true
					}
				})
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk1(n.NamedChild(i))
		}
	}
	walk1(root)

	isInstanceExpr := func(obj *sitter.Node) bool {
		if obj == nil {
			return false
		}
		t := txt(obj)
		if obj.Type() == "identifier" && instances[t] {
			return true
		}
		if obj.Type() == "member_expression" && endsWithPusher(t) {
			return true
		}
		if obj.Type() == "new_expression" {
			if c := obj.ChildByFieldName("constructor"); c != nil && endsWithPusher(txt(c)) {
				return true
			}
		}
		return false
	}

	// pass 2 — subscribe sites + bind events
	type bindCall struct {
		recv  string
		event string
	}
	var subs []pjcSub
	var binds []bindCall
	var walk2 func(n *sitter.Node)
	walk2 = func(n *sitter.Node) {
		if n.Type() == "call_expression" {
			fn := n.ChildByFieldName("function")
			if fn != nil && fn.Type() == "member_expression" {
				prop := txt(fn.ChildByFieldName("property"))
				obj := fn.ChildByFieldName("object")
				args := n.ChildByFieldName("arguments")
				switch prop {
				case "subscribe":
					if isInstanceExpr(obj) && args != nil && args.NamedChildCount() > 0 {
						arg := args.NamedChild(0)
						s := pjcSub{
							line:           int(n.StartPoint().Row) + 1,
							channelExpr:    txt(arg),
							enclosingLabel: pjcEnclosingComponentLabel(n, src),
						}
						s.channelLit, s.propConfig = pjcClassifyChannelArg(arg, src)
						s.lhsBind = pjcBindingTargetName(n, src)
						subs = append(subs, s)
					}
				case "bind", "bind_global":
					if args != nil && args.NamedChildCount() > 0 {
						if ev := pjcStringLit(args.NamedChild(0), src); ev != "" {
							binds = append(binds, bindCall{recv: txt(obj), event: ev})
						}
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk2(n.NamedChild(i))
		}
	}
	walk2(root)

	// attach events: match `.bind` receiver to the subscribe target, else (single
	// subscribe in the file) take every bind.
	for i := range subs {
		seen := map[string]bool{}
		for _, b := range binds {
			if (subs[i].lhsBind != "" && b.recv == subs[i].lhsBind) || (len(subs) == 1) {
				if !seen[b.event] {
					seen[b.event] = true
					subs[i].events = append(subs[i].events, b.event)
				}
			}
		}
		sort.Strings(subs[i].events)
	}
	return subs
}

// pjcBindingTargetName returns the identifier a node is assigned to
// (declarator name or assignment LHS), "" if it is used inline.
func pjcBindingTargetName(n *sitter.Node, src []byte) string {
	for cur := n.Parent(); cur != nil; cur = cur.Parent() {
		switch cur.Type() {
		case "variable_declarator":
			if nm := cur.ChildByFieldName("name"); nm != nil {
				return nm.Content(src)
			}
			return ""
		case "assignment_expression":
			if l := cur.ChildByFieldName("left"); l != nil {
				return l.Content(src)
			}
			return ""
		case "statement_block", "program", "function_declaration", "method_definition", "arrow_function":
			return ""
		}
	}
	return ""
}

// pjcCollectParamIdents yields every identifier bound by a parameter node.
func pjcCollectParamIdents(n *sitter.Node, src []byte, yield func(string)) {
	switch n.Type() {
	case "identifier", "shorthand_property_identifier_pattern":
		yield(n.Content(src))
	case "required_parameter", "optional_parameter":
		if p := n.ChildByFieldName("pattern"); p != nil {
			pjcCollectParamIdents(p, src, yield)
		}
	}
}

// pjcClassifyChannelArg reports the literal channel string (empty when not a
// plain literal) and, when the arg is `<prop>.channel`, the prop name.
func pjcClassifyChannelArg(arg *sitter.Node, src []byte) (lit, propConfig string) {
	switch arg.Type() {
	case "string", "template_string":
		return pjcStringLit(arg, src), ""
	case "member_expression":
		prop := ""
		if p := arg.ChildByFieldName("property"); p != nil {
			prop = p.Content(src)
		}
		if prop == "channel" || prop == "channel_name" {
			obj := arg.ChildByFieldName("object")
			ot := ""
			if obj != nil {
				ot = obj.Content(src)
			}
			if strings.Contains(strings.ToLower(ot), "config") {
				return "", pjcConfigPropName(ot)
			}
		}
	}
	return "", ""
}

func pjcConfigPropName(objText string) string {
	// `this.props.pusherConfig` → pusherConfig ; `pusherConfig` → pusherConfig
	parts := strings.Split(objText, ".")
	return parts[len(parts)-1]
}

// pjcStringLit returns a string/template literal's value, "" if it has an
// interpolation (a runtime value).
func pjcStringLit(n *sitter.Node, src []byte) string {
	switch n.Type() {
	case "string":
		var b strings.Builder
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if c.Type() == "string_fragment" || c.Type() == "string_content" {
				b.WriteString(c.Content(src))
			}
		}
		if b.Len() == 0 {
			return strings.Trim(n.Content(src), `"'`)
		}
		return b.String()
	case "template_string":
		for i := 0; i < int(n.NamedChildCount()); i++ {
			if n.NamedChild(i).Type() == "template_substitution" {
				return "" // runtime
			}
		}
		return strings.Trim(n.Content(src), "`")
	}
	return ""
}

// pjcEnclosingComponentLabel finds the nearest class name, else "".
func pjcEnclosingComponentLabel(n *sitter.Node, src []byte) string {
	for cur := n.Parent(); cur != nil; cur = cur.Parent() {
		if cur.Type() == "class_declaration" || cur.Type() == "class" {
			if nm := cur.ChildByFieldName("name"); nm != nil {
				return nm.Content(src)
			}
		}
	}
	return ""
}
