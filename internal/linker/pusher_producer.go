package linker

import (
	"fmt"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// EnrichPusherProducers is Tier PU.2 — the producer half of Pusher realtime
// linking.
//
// The pattern pass (PU.1) matches the single server-side `.trigger(channel,
// event, data)` sink, which in every Rails app that wraps Pusher lives inside
// one private helper:
//
//	class PusherClient
//	  FOLDER_REFRESH = "folder_refresh"
//	  def initialize(object, channel, explicit_channel: false)
//	    @channel_name = explicit_channel ? channel : "#{Rails.env}.#{channel}.#{object.id}"
//	  end
//	  def notify_folder_refresh(id) = push({ folder_id: id }, FOLDER_REFRESH)
//	  def push(body, event_type)
//	    PusherClient.new_pusher_client.trigger(channel_name, event_type, body)
//	  end
//	end
//
// The trigger call's channel (`channel_name` ivar) and event (`event_type`
// param) are both runtime values at that one AST location, so the pattern node
// is `key_dynamic` and the contract engine drops it to a ledger entry. But the
// real producers are the ~20 `PusherClient.new(obj, <chan>).notify_x(...)`
// call sites scattered across models/services/jobs, and at each of those BOTH
// the channel segment and the event name ARE statically knowable — the channel
// from the `.new` argument (a literal or a `PusherClient::` constant), the
// event from which `notify_*` method the constant `push` forwards it to.
//
// This pass parses the wrapper class once to learn `notify_* -> event`, then
// re-parses every Ruby file to mint one `publisher` node per resolvable call
// site (pattern `pusher_trigger_forward`), attributed to its enclosing method
// with a `calls` edge. It never rewrites the wrapper's own shared node — that
// would be right for one caller and wrong for the other 19, the same reason
// ResolveRubyWrapperURLCallSites mints per-site rather than enriching in place.
// Call sites whose channel argument has no static segment (a bare local, a
// `"#{prefix}_x"` with no usable literal) are left to the wrapper's dynamic
// ledger entry.
func EnrichPusherProducers(nodes []graph.Node, serviceFiles map[string][]string) ([]graph.Node, []graph.Edge) {
	// ── locate the wrapper class(es): a file with a pusher_trigger* node ──
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
		return nil, nil
	}

	// className -> (notify method name -> event literal)
	eventByMethod := map[string]map[string]string{}
	// className -> const name -> string value  (FOLDER_REFRESH = "folder_refresh")
	constVals := map[string]map[string]string{}
	// className -> hash const name -> (symbol/string key -> value)  (CHANNELS = {...})
	hashConstVals := map[string]map[string]map[string]string{}

	for file := range hubFiles {
		fa := parseRubyFileAST(file)
		if fa == nil {
			continue
		}
		collectPusherWrapperFacts(fa, eventByMethod, constVals, hashConstVals)
		fa.release()
	}
	if len(eventByMethod) == 0 {
		return nil, nil
	}

	// enclosing-method line ranges, for the calls edge
	type fnRange struct {
		id            string
		line, endLine int
	}
	fnByFile := map[string][]fnRange{}
	for i := range nodes {
		n := &nodes[i]
		if (n.Type == graph.NodeTypeFunction || n.Type == graph.NodeTypeMethod) && n.EndLine > 0 {
			fnByFile[n.File] = append(fnByFile[n.File], fnRange{n.ID, n.Line, n.EndLine})
		}
	}
	enclosingFn := func(file string, line int) string {
		best, bestSpan := "", -1
		for _, r := range fnByFile[file] {
			if line < r.line || line > r.endLine {
				continue
			}
			if span := r.endLine - r.line; bestSpan == -1 || span < bestSpan {
				best, bestSpan = r.id, span
			}
		}
		return best
	}

	var newNodes []graph.Node
	var newEdges []graph.Edge

	svcFilesSorted := make([]string, 0)
	fileSvc := map[string]string{}
	for svc, files := range serviceFiles {
		for _, f := range files {
			if strings.HasSuffix(f, ".rb") {
				svcFilesSorted = append(svcFilesSorted, f)
				fileSvc[f] = svc
			}
		}
	}
	sort.Strings(svcFilesSorted)

	seen := map[string]bool{}
	for _, file := range svcFilesSorted {
		if graph.IsTestFilePath(file) {
			continue
		}
		fa := parseRubyFileAST(file)
		if fa == nil {
			continue
		}
		svc := fileSvc[file]
		relFile := patterns.RelativizeToCwd(file)

		for _, cs := range findPusherNewCallSites(fa, eventByMethod) {
			chanSeg := resolvePusherChannelSegment(cs.channelArg, fa.src, cs.className, constVals, hashConstVals)
			if chanSeg == "" {
				continue
			}
			for _, m := range cs.methods {
				event := ""
				switch m.name {
				case "push", "trigger", "trigger_async":
					event = pusherLiteralOrConst(m.eventArg, fa.src, cs.className, constVals)
				default:
					event = eventByMethod[cs.className][m.name]
				}
				if event == "" {
					continue
				}
				line := m.line
				id := fmt.Sprintf("%s:%s:publisher:pusher_trigger_forward:%s:%d", svc, relFile, m.name, line)
				if seen[id] {
					continue
				}
				seen[id] = true

				meta := map[string]string{
					"pattern":      "pusher_trigger_forward",
					"channel":      chanSeg,
					"event":        event,
					"package":      "pusher",
					"fn":           "trigger",
					"resolved_via": "pusher_producer_forward",
					"wrapper":      cs.className,
				}
				newNodes = append(newNodes, graph.Node{
					ID:       id,
					Type:     graph.NodeTypePublisher,
					Label:    chanSeg + " " + event,
					Service:  svc,
					File:     relFile,
					Line:     line,
					Language: "ruby",
					Meta:     meta,
				})
				if fn := enclosingFn(relFile, line); fn != "" {
					newEdges = append(newEdges, graph.Edge{
						ID:         fmt.Sprintf("%s->%s:pusher_producer_forward", fn, id),
						From:       fn,
						To:         id,
						Type:       graph.EdgeTypeCalls,
						Confidence: graph.ConfidenceInferred,
						Meta:       map[string]string{"via": "pusher_producer_forward"},
					})
				}
			}
		}
		fa.release()
	}

	sort.Slice(newNodes, func(a, b int) bool { return newNodes[a].ID < newNodes[b].ID })
	sort.Slice(newEdges, func(a, b int) bool {
		if newEdges[a].From != newEdges[b].From {
			return newEdges[a].From < newEdges[b].From
		}
		return newEdges[a].To < newEdges[b].To
	})
	return newNodes, newEdges
}

// collectPusherWrapperFacts fills eventByMethod / constVals / hashConstVals for
// every class in fa that both defines a `pusher_trigger`-shaped call and holds
// `notify_*`-style forwarders to it.
func collectPusherWrapperFacts(
	fa *rubyFileAST,
	eventByMethod map[string]map[string]string,
	constVals map[string]map[string]string,
	hashConstVals map[string]map[string]map[string]string,
) {
	var walkClass func(n *sitter.Node)
	walkClass = func(n *sitter.Node) {
		if n.Type() == "class" {
			nameNode := n.ChildByFieldName("name")
			if nameNode != nil {
				cls := nameNode.Content(fa.src)
				body := n.ChildByFieldName("body")
				if body != nil {
					collectPusherClassBody(fa, cls, body, eventByMethod, constVals, hashConstVals)
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walkClass(n.NamedChild(i))
		}
	}
	walkClass(fa.root)
}

func collectPusherClassBody(
	fa *rubyFileAST,
	cls string,
	body *sitter.Node,
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

	// class-scope constants
	var walkConst func(n *sitter.Node)
	walkConst = func(n *sitter.Node) {
		if n.Type() == "assignment" {
			l := n.ChildByFieldName("left")
			r := n.ChildByFieldName("right")
			if l != nil && r != nil && l.Type() == "constant" {
				name := l.Content(fa.src)
				rr := r
				if rr.Type() == "call" { // `{...}.freeze`
					if recv := rr.ChildByFieldName("receiver"); recv != nil {
						rr = recv
					}
				}
				switch rr.Type() {
				case "string":
					constVals[cls][name] = pusherStringLiteral(rr, fa.src)
				case "hash":
					hashConstVals[cls][name] = pusherHashLiterals(rr, fa.src)
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walkConst(n.NamedChild(i))
		}
	}
	walkConst(body)

	// notify_* -> event, from `push(_, EVENT)` in each method body
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
		mName := mn.Content(fa.src)
		params := map[string]bool{}
		if pn := m.ChildByFieldName("parameters"); pn != nil {
			for j := 0; j < int(pn.NamedChildCount()); j++ {
				c := pn.NamedChild(j)
				if c.Type() == "identifier" {
					params[c.Content(fa.src)] = true
				} else if c.Type() == "optional_parameter" || c.Type() == "keyword_parameter" {
					if nm := c.ChildByFieldName("name"); nm != nil {
						params[nm.Content(fa.src)] = true
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
				if meth := n.ChildByFieldName("method"); meth != nil && meth.Content(fa.src) == "push" {
					if args := n.ChildByFieldName("arguments"); args != nil && args.NamedChildCount() >= 2 {
						ev := args.NamedChild(1)
						if ev.Type() == "identifier" && params[ev.Content(fa.src)] {
							// this IS the wrapper's own `push(body, event_type)`
						} else {
							found = pusherLiteralOrConst(ev, fa.src, cls, constVals)
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

type pusherNotifyCall struct {
	name     string
	line     int
	eventArg *sitter.Node // set for name in {push,trigger,trigger_async}
}

type pusherNewCallSite struct {
	className  string
	channelArg *sitter.Node
	methods    []pusherNotifyCall
}

// findPusherNewCallSites returns every `<Class>.new(obj, <chan>, ...)` in fa
// that is followed (chained, or via a same-scope local var) by a call to a
// method known to forward to Pusher.
func findPusherNewCallSites(fa *rubyFileAST, eventByMethod map[string]map[string]string) []pusherNewCallSite {
	var out []pusherNewCallSite

	isPusherClass := func(name string) bool { _, ok := eventByMethod[name]; return ok }
	notifyish := func(cls, m string) bool {
		if m == "push" || m == "trigger" || m == "trigger_async" {
			return true
		}
		_, ok := eventByMethod[cls][m]
		return ok
	}

	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "call" {
			if newExpr, cls, chanArg := pusherNewExpr(n, fa.src, isPusherClass); newExpr != nil {
				site := pusherNewCallSite{className: cls, channelArg: chanArg}

				// chained: parent call `.new(...).notify_x(...)`
				if p := n.Parent(); p != nil && p.Type() == "call" {
					if recv := p.ChildByFieldName("receiver"); recv != nil && recv.Equal(n) {
						if mn := p.ChildByFieldName("method"); mn != nil {
							m := mn.Content(fa.src)
							if notifyish(cls, m) {
								site.methods = append(site.methods, pusherNotifyCall{
									name: m, line: int(p.StartPoint().Row) + 1,
									eventArg: pusherArgAt(p, 1),
								})
							}
						}
					}
				}

				// assigned: `lhs = Class.new(...)` then `lhs.notify_x(...)`
				if a := ascendToAssignment(n); a != nil {
					if lhs := a.ChildByFieldName("left"); lhs != nil {
						addMethod := func(mc receiverCall) {
							if notifyish(cls, mc.method) {
								site.methods = append(site.methods, pusherNotifyCall{
									name: mc.method, line: mc.line, eventArg: pusherArgAt(mc.node, 1),
								})
							}
						}
						switch lhs.Type() {
						case "identifier":
							// same-scope local var
							scope := enclosingScopeNode(a)
							for _, mc := range findReceiverCalls(scope, lhs.Content(fa.src), fa.src) {
								addMethod(mc)
							}
						case "instance_variable":
							// ivar held across methods: `@pusher = Class.new(...)` in
							// one method, `@pusher.notify_x` / `pusher.notify_x`
							// (attr_reader) in others of the SAME class body. Cross-file
							// (module-mixed) holders are PU.2d, not handled here.
							ivar := lhs.Content(fa.src)
							base := strings.TrimPrefix(ivar, "@")
							if body := enclosingClassBody(a); body != nil {
								for _, mc := range findPusherIvarCalls(body, ivar, base, fa.src) {
									addMethod(mc)
								}
							}
						}
					}
				}

				if len(site.methods) > 0 && chanArg != nil {
					out = append(out, site)
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(fa.root)
	return out
}

// pusherNewExpr reports whether call n is `<Class>.new(<obj>, <chan>, ...)`
// for a known Pusher wrapper class, returning the node, class name and the
// channel argument (2nd positional).
func pusherNewExpr(n *sitter.Node, src []byte, isPusherClass func(string) bool) (*sitter.Node, string, *sitter.Node) {
	mn := n.ChildByFieldName("method")
	if mn == nil || mn.Content(src) != "new" {
		return nil, "", nil
	}
	recv := n.ChildByFieldName("receiver")
	if recv == nil {
		return nil, "", nil
	}
	var cls string
	switch recv.Type() {
	case "constant":
		cls = recv.Content(src)
	case "scope_resolution":
		if nm := recv.ChildByFieldName("name"); nm != nil {
			cls = nm.Content(src)
		}
	default:
		return nil, "", nil
	}
	if !isPusherClass(cls) {
		return nil, "", nil
	}
	return n, cls, pusherArgAt(n, 1)
}

// pusherArgAt returns the i-th positional argument of a call, skipping
// keyword-argument (`pair`) nodes.
func pusherArgAt(call *sitter.Node, i int) *sitter.Node {
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

func ascendToAssignment(n *sitter.Node) *sitter.Node {
	for cur := n.Parent(); cur != nil; cur = cur.Parent() {
		switch cur.Type() {
		case "assignment", "operator_assignment": // `x = ...` / `@x ||= ...`
			return cur
		case "method", "class", "module", "do_block", "block":
			return nil
		}
	}
	return nil
}

// enclosingClassBody returns the body of the nearest enclosing class/module.
func enclosingClassBody(n *sitter.Node) *sitter.Node {
	for cur := n.Parent(); cur != nil; cur = cur.Parent() {
		if cur.Type() == "class" || cur.Type() == "module" {
			return cur.ChildByFieldName("body")
		}
	}
	return nil
}

// findPusherIvarCalls returns every `@ivar.method(...)` or `base.method(...)`
// call anywhere in scope — the reader forms an ivar-held instance is invoked
// through (`@pusher.notify_x` directly, or `pusher.notify_x` via an
// `attr_reader :pusher`, which shares the ivar's bare name).
func findPusherIvarCalls(scope *sitter.Node, ivar, base string, src []byte) []receiverCall {
	var out []receiverCall
	if scope == nil {
		return out
	}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "call" {
			if r := n.ChildByFieldName("receiver"); r != nil {
				rc := r.Content(src)
				if (r.Type() == "instance_variable" && rc == ivar) ||
					(r.Type() == "identifier" && rc == base) {
					if mn := n.ChildByFieldName("method"); mn != nil {
						out = append(out, receiverCall{
							method: mn.Content(src),
							line:   int(n.StartPoint().Row) + 1,
							node:   n,
						})
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(scope)
	return out
}

func enclosingScopeNode(n *sitter.Node) *sitter.Node {
	for cur := n.Parent(); cur != nil; cur = cur.Parent() {
		switch cur.Type() {
		case "method", "singleton_method", "do_block", "block":
			if b := cur.ChildByFieldName("body"); b != nil {
				return b
			}
			return cur
		}
	}
	return n
}

type receiverCall struct {
	method string
	line   int
	node   *sitter.Node
}

func findReceiverCalls(scope *sitter.Node, recvName string, src []byte) []receiverCall {
	var out []receiverCall
	if scope == nil {
		return out
	}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "call" {
			if r := n.ChildByFieldName("receiver"); r != nil && r.Type() == "identifier" && r.Content(src) == recvName {
				if mn := n.ChildByFieldName("method"); mn != nil {
					out = append(out, receiverCall{
						method: mn.Content(src),
						line:   int(n.StartPoint().Row) + 1,
						node:   n,
					})
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(scope)
	return out
}

// resolvePusherChannelSegment reduces a channel argument to the single stable
// segment both producer and ERB consumer agree on — the literal middle of
// `"#{Rails.env}.folder-status.#{id}"`, the value of `PusherClient::CHANNELS
// [:lro_update]`, or a bare `"folder-status"`.
func resolvePusherChannelSegment(
	arg *sitter.Node, src []byte, cls string,
	constVals map[string]map[string]string,
	hashConstVals map[string]map[string]map[string]string,
) string {
	if arg == nil {
		return ""
	}
	switch arg.Type() {
	case "string":
		return pusherChannelFromString(arg, src)
	case "constant":
		return constVals[cls][arg.Content(src)]
	case "scope_resolution":
		nm := arg.ChildByFieldName("name")
		if nm == nil {
			return ""
		}
		return constVals[cls][nm.Content(src)]
	case "element_reference":
		obj := arg.ChildByFieldName("object")
		if obj == nil || arg.NamedChildCount() < 2 {
			return ""
		}
		var hashName string
		switch obj.Type() {
		case "constant":
			hashName = obj.Content(src)
		case "scope_resolution":
			if nm := obj.ChildByFieldName("name"); nm != nil {
				hashName = nm.Content(src)
			}
		}
		key := strings.TrimPrefix(strings.Trim(arg.NamedChild(1).Content(src), `"'`), ":")
		if h := hashConstVals[cls][hashName]; h != nil {
			return h[key]
		}
	}
	return ""
}

// pusherChannelFromString extracts the literal segment(s) of a Ruby string,
// dropping interpolations and the `Rails.env` / trailing-id noise around them.
func pusherChannelFromString(strNode *sitter.Node, src []byte) string {
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
	// the most specific (longest) literal segment is the channel identity
	best := segs[0]
	for _, s := range segs[1:] {
		if len(s) > len(best) {
			best = s
		}
	}
	return best
}

func pusherStringLiteral(strNode *sitter.Node, src []byte) string {
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

func pusherHashLiterals(hash *sitter.Node, src []byte) map[string]string {
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
		out[key] = pusherStringLiteral(v, src)
	}
	return out
}

// pusherLiteralOrConst resolves an event argument to its string value: a
// string literal, or a `FOO` / `Class::FOO` constant defined on the wrapper.
func pusherLiteralOrConst(arg *sitter.Node, src []byte, cls string, constVals map[string]map[string]string) string {
	if arg == nil {
		return ""
	}
	switch arg.Type() {
	case "string":
		return pusherStringLiteral(arg, src)
	case "constant":
		return constVals[cls][arg.Content(src)]
	case "scope_resolution":
		if nm := arg.ChildByFieldName("name"); nm != nil {
			return constVals[cls][nm.Content(src)]
		}
	}
	return ""
}
