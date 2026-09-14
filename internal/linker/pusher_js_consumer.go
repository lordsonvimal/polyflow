package linker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/lordsonvimal/polyflow/internal/railsview"
)

// EnrichPusherConsumersJS is SPA.7 — the JS half of the Pusher consumer side.
//
// `pusher_consumer_erb` (pusher_consumer.go) only recognises the orion
// `pusher_config(channel:, event:)` ERB-helper shape. Codebases that wire Pusher
// entirely in JS — `new Pusher(...)` / `window.pusher` then
// `channel = instance.subscribe(<name>)` and `channel.bind("<event>", cb)` — get
// no `subscriber` node at all, so a forward trace from a Rails `Pusher.trigger`
// dead-ends. This pass mints one `subscriber` node per JS subscribe site.
//
// The channel string is frequently runtime-built (`"#{Rails.env}.#{job}.#{id}"`
// handed down as a `pusherConfig` prop or a bare local); those subscribers carry
// `key_dynamic="true"` + a `pusher_channel_dynamic` ledger entry rather than a
// fabricated match — an honest witness, mirroring the producer side. The
// `.bind("update"|"notify"|…)` event literal is the one reliable join key, so
// when it agrees with a `pusher_trigger*` publisher's event a low-confidence
// `publishes` bridge is emitted (fan-out capped).
func EnrichPusherConsumersJS(
	nodes []graph.Node,
	serviceFiles map[string][]string,
) (newNodes []graph.Node, newEdges []graph.Edge, unresolved []graph.UnresolvedRef) {
	// ── gate: only run where the repo actually triggers Pusher server-side ──
	var pubs []*graph.Node
	hubFiles := map[string]bool{}
	for i := range nodes {
		p := nodes[i].Meta["pattern"]
		if strings.HasPrefix(p, "pusher_trigger") {
			pubs = append(pubs, &nodes[i])
			if nodes[i].File != "" {
				hubFiles[nodes[i].File] = true
			}
		}
	}
	if len(pubs) == 0 {
		return nil, nil, nil
	}

	// wrapper constants (CHANNELS[:x], FOLDER_REFRESH = "…") for the ERB bridge.
	constVals := map[string]map[string]string{}
	hashConstVals := map[string]map[string]map[string]string{}
	eventByMethod := map[string]map[string]string{}
	for f := range hubFiles {
		if fa := parseRubyFileAST(f); fa != nil {
			collectPusherWrapperFacts(fa, eventByMethod, constVals, hashConstVals)
			fa.release()
		}
	}

	fileNodeID := map[string]string{}
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeFile {
			fileNodeID[nodes[i].Service+"\x00"+nodes[i].File] = nodes[i].ID
		}
	}

	erbConfig := pusherPropConfigFromERB(serviceFiles, constVals, hashConstVals)

	// deterministic file order
	type jsEnt struct{ svc, file string }
	var jsFiles []jsEnt
	for svc, files := range serviceFiles {
		for _, f := range files {
			switch strings.ToLower(filepath.Ext(f)) {
			case ".js", ".jsx", ".mjs", ".es6", ".ts", ".tsx":
				jsFiles = append(jsFiles, jsEnt{svc, f})
			}
		}
	}
	sort.Slice(jsFiles, func(a, b int) bool { return jsFiles[a].file < jsFiles[b].file })

	seen := map[string]bool{}
	for _, ent := range jsFiles {
		if graph.IsTestFilePath(ent.file) {
			continue
		}
		src, root, _, ok := jsParse(ent.file)
		if !ok || root == nil {
			continue
		}
		rel := patterns.RelativizeToCwd(ent.file)
		subs := detectPusherJSSubs(root, src)
		if len(subs) == 0 {
			continue
		}
		fileID := fileNodeID[ent.svc+"\x00"+rel]

		base := strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
		for _, s := range subs {
			if s.enclosingLabel == "" {
				s.enclosingLabel = base
			}
			id := fmt.Sprintf("%s:%s:subscriber:pusher_subscribe_js:%d", ent.svc, rel, s.line)
			if seen[id] {
				continue
			}
			seen[id] = true

			chLit, event := s.channelLit, ""
			if len(s.events) > 0 {
				event = s.events[0]
			}
			dynamic := chLit == ""
			if dynamic && s.propConfig != "" {
				if cfg := erbConfig[s.enclosingLabel]; cfg != nil {
					if c := cfg["channel"]; c != "" {
						chLit, dynamic = c, false
					}
					if event == "" {
						event = cfg["event"]
					}
				}
			}

			meta := map[string]string{
				"pattern":      "pusher_subscribe_js",
				"package":      "pusher-js",
				"resolved_via": "pusher_consumer_js",
			}
			label := chLit
			if dynamic {
				meta["key_dynamic"] = "true"
				meta["key_dynamic_raw"] = s.channelExpr
				label = "(dynamic)"
			} else {
				meta["channel"] = chLit
			}
			if event != "" {
				meta["event"] = event
				label = strings.TrimSpace(label + " " + event)
			}
			if len(s.events) > 1 {
				meta["events"] = strings.Join(s.events, ",")
			}
			if s.propConfig != "" {
				meta["pusher_config"] = s.enclosingLabel
			}

			newNodes = append(newNodes, graph.Node{
				ID:       id,
				Type:     graph.NodeTypeSubscriber,
				Label:    label,
				Service:  ent.svc,
				File:     rel,
				Line:     s.line,
				Language: "javascript",
				Meta:     meta,
			})
			if fileID != "" {
				newEdges = append(newEdges, containsEdge(fileID, id))
			}
			if dynamic {
				unresolved = append(unresolved, graph.UnresolvedRef{
					Service: ent.svc, File: rel, Line: s.line,
					Name: s.channelExpr, Kind: "pusher_channel_dynamic",
				})
			}

			// ── event-literal bridge to a matching producer ──
			if event != "" {
				n := 0
				subEvents := map[string]bool{}
				for _, e := range s.events {
					subEvents[e] = true
				}
				for _, p := range pubs {
					if n >= 4 {
						break
					}
					pe := strings.Trim(p.Meta["event"], `"'`)
					if pe == "" || !subEvents[pe] {
						continue
					}
					via := "pusher_channel_event"
					if dynamic {
						via = "pusher_event_only"
					}
					newEdges = append(newEdges, graph.Edge{
						ID:         fmt.Sprintf("%s->%s:pusher_js_bridge", p.ID, id),
						From:       p.ID,
						To:         id,
						Type:       graph.EdgeTypePublishes,
						Confidence: graph.ConfidenceInferred,
						Meta:       map[string]string{"via": via, "event": pe},
					})
					n++
				}
			}
		}
	}

	sort.Slice(newNodes, func(a, b int) bool { return newNodes[a].ID < newNodes[b].ID })
	sort.Slice(newEdges, func(a, b int) bool {
		if newEdges[a].From != newEdges[b].From {
			return newEdges[a].From < newEdges[b].From
		}
		return newEdges[a].To < newEdges[b].To
	})
	return newNodes, newEdges, unresolved
}

// pusherJSSub is one `<instance>.subscribe(<arg>)` site in a JS file.
type pusherJSSub struct {
	line           int
	channelLit     string   // "" when the channel is not a plain string literal
	channelExpr    string   // raw source text of the arg → key_dynamic_raw
	events         []string // literal args of `.bind("evt", …)` on the subscription
	propConfig     string   // the prop name when the arg is `<prop>.channel`
	enclosingLabel string   // PascalCase class name, else file basename — for the ERB bridge
	lhsBind        string   // assignment target the subscribe result is stored in
}

// detectPusherJSSubs walks a parsed JS/TS tree for Pusher subscribe/bind sites.
func detectPusherJSSubs(root *sitter.Node, src []byte) []pusherJSSub {
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
				if name := bindingTargetName(n, src); name != "" {
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
				collectParamIdents(n.NamedChild(i), src, func(id string) {
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
	var subs []pusherJSSub
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
						s := pusherJSSub{
							line:           int(n.StartPoint().Row) + 1,
							channelExpr:    txt(arg),
							enclosingLabel: enclosingComponentLabel(n, src),
						}
						s.channelLit, s.propConfig = classifyChannelArg(arg, src)
						s.lhsBind = bindTargetName(n, src)
						subs = append(subs, s)
					}
				case "bind", "bind_global":
					if args != nil && args.NamedChildCount() > 0 {
						if ev := pusherJSStringLit(args.NamedChild(0), src); ev != "" {
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

// bindingTargetName returns the identifier a node is assigned to (declarator
// name or assignment LHS), "" if it is used inline.
func bindingTargetName(n *sitter.Node, src []byte) string {
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

// bindTargetName returns the assignment/declarator target a subscribe call is
// stored in (`this.channel`, `subscription`), "" if used inline.
func bindTargetName(call *sitter.Node, src []byte) string { return bindingTargetName(call, src) }

// collectParamIdents yields every identifier bound by a parameter node.
func collectParamIdents(n *sitter.Node, src []byte, yield func(string)) {
	switch n.Type() {
	case "identifier", "shorthand_property_identifier_pattern":
		yield(n.Content(src))
	case "required_parameter", "optional_parameter":
		if p := n.ChildByFieldName("pattern"); p != nil {
			collectParamIdents(p, src, yield)
		}
	}
}

// classifyChannelArg reports the literal channel string (empty when not a plain
// literal) and, when the arg is `<prop>.channel`, the prop name.
func classifyChannelArg(arg *sitter.Node, src []byte) (lit, propConfig string) {
	switch arg.Type() {
	case "string", "template_string":
		return pusherJSStringLit(arg, src), ""
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
				return "", pusherConfigPropName(ot)
			}
		}
	}
	return "", ""
}

func pusherConfigPropName(objText string) string {
	// `this.props.pusherConfig` → pusherConfig ; `pusherConfig` → pusherConfig
	parts := strings.Split(objText, ".")
	return parts[len(parts)-1]
}

// stringLiteralValue returns a string/template literal's value, "" if it has an
// interpolation (a runtime value).
func pusherJSStringLit(n *sitter.Node, src []byte) string {
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

// enclosingComponentLabel finds the nearest class name, else the file basename.
func enclosingComponentLabel(n *sitter.Node, src []byte) string {
	for cur := n.Parent(); cur != nil; cur = cur.Parent() {
		if cur.Type() == "class_declaration" || cur.Type() == "class" {
			if nm := cur.ChildByFieldName("name"); nm != nil {
				return nm.Content(src)
			}
		}
	}
	return ""
}

// pusherPropConfigFromERB harvests `react_component("X", { pusherConfig: {
// channel:, event: } })` (or `pusherConfig: pusher_config(channel:, event:)`)
// call sites → component label → {channel,event,key}. Only inline literals and
// `PusherClient::` constants resolve; a controller-built `@pusher_config` ivar
// stays a no-op (honest — the strings are not statically present).
func pusherPropConfigFromERB(
	serviceFiles map[string][]string,
	constVals map[string]map[string]string,
	hashConstVals map[string]map[string]map[string]string,
) map[string]map[string]string {
	out := map[string]map[string]string{}
	var erb []string
	for _, files := range serviceFiles {
		for _, f := range files {
			if strings.HasSuffix(f, ".erb") && !graph.IsTestFilePath(f) {
				erb = append(erb, f)
			}
		}
	}
	sort.Strings(erb)

	for _, f := range erb {
		src, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		_, ruby := railsview.SplitERB(src)
		root, release := parsePusherRubySource(ruby)
		if root == nil {
			continue
		}
		var walk func(n *sitter.Node)
		walk = func(n *sitter.Node) {
			if n.Type() == "call" {
				if mn := n.ChildByFieldName("method"); mn != nil && mn.Content(ruby) == "react_component" {
					comp := ""
					if a0 := pusherArgAt(n, 0); a0 != nil && a0.Type() == "string" {
						comp = pusherStringLiteral(a0, ruby)
					}
					if hv := descendantPairValue(n, ruby, "pusherConfig"); comp != "" && hv != nil {
						if cfg := extractPusherConfigHash(hv, ruby, constVals, hashConstVals); len(cfg) > 0 {
							out[comp] = cfg
						}
					}
				}
			}
			for i := 0; i < int(n.NamedChildCount()); i++ {
				walk(n.NamedChild(i))
			}
		}
		walk(root)
		release()
	}
	return out
}

// descendantPairValue returns the value node of the first `pair` under n whose
// key matches (bare symbol, `:sym`, or `"str"`).
func descendantPairValue(n *sitter.Node, src []byte, key string) *sitter.Node {
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

// extractPusherConfigHash pulls channel/event/key out of either a hash literal
// or a `pusher_config(channel:, event:)` helper call.
func extractPusherConfigHash(
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
			if lit := pusherStringLiteral(cv, src); lit != "" {
				out["channel"] = lit
			} else {
				out["channel"] = pusherChannelFromString(cv, src) // interpolated → segment
			}
		} else {
			out["channel"] = pusherRefSegment(cv, src, constVals, hashConstVals)
		}
	}
	if ev := getPair(container, "event"); ev != nil {
		out["event"] = pusherRefLiteral(ev, src, constVals)
	}
	for k, val := range out {
		if val == "" {
			delete(out, k)
		}
	}
	return out
}

// parsePusherRubySource parses a raw Ruby byte slice (the virtual-Ruby view of
// an ERB template). Unlike rubyParse it takes bytes, not a path, and always
// owns the tree — the caller must call release.
//
// Retained here (moved from the now-retired pusher_consumer.go, Tier FX
// FX.8 2026-09-14 — its ERB scanning half migrated to
// internal/factpipe/hub_pusher_consumer.go's pusher_wrapper_erb hub
// provider) because this file's own pusherPropConfigFromERB still needs it:
// it stays hand-written Go, not a hub consumer, because its output only
// feeds this pass's JS-dataflow subscribe-site record (see this file's
// package doc) rather than minting anything on its own.
func parsePusherRubySource(src []byte) (root *sitter.Node, release func()) {
	p := sitter.NewParser()
	p.SetLanguage(rubysitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil || tree == nil {
		return nil, func() {}
	}
	return tree.RootNode(), func() { tree.Close() }
}

// pusherRefSegment reduces an ERB channel expression to the one stable segment
// both sides agree on: a bare string's literal middle segment, a
// `PusherClient::CONST` value, or a `PusherClient::CHANNELS[:sym]` lookup.
func pusherRefSegment(
	n *sitter.Node, src []byte,
	constVals map[string]map[string]string,
	hashConstVals map[string]map[string]map[string]string,
) string {
	if n == nil {
		return ""
	}
	switch n.Type() {
	case "string":
		return pusherChannelFromString(n, src)
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

// pusherRefLiteral resolves an ERB event expression to its string value: a
// literal, or a `PusherClient::CONST`.
func pusherRefLiteral(n *sitter.Node, src []byte, constVals map[string]map[string]string) string {
	if n == nil {
		return ""
	}
	switch n.Type() {
	case "string":
		return pusherStringLiteral(n, src)
	case "scope_resolution":
		scope := n.ChildByFieldName("scope")
		name := n.ChildByFieldName("name")
		if scope != nil && name != nil {
			return constVals[scope.Content(src)][name.Content(src)]
		}
	}
	return ""
}
