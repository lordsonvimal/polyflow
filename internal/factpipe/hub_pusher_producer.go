package factpipe

import (
	"fmt"
	"os"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// hub_pusher_producer.go is the "pusher_producer_sites" hub provider (see
// hub.go) — the Tier FX FX.8.11 migration of
// internal/linker/pusher_producer.go's EnrichPusherProducers.
//
// The wrapper-class table (notify_*->event / CONST / CHANNELS[:x]) this pass
// needs is exactly the one hub_pusher_consumer.go's pusher_wrapper_erb
// provider already builds (pchCollectWrapperFacts, reused verbatim, no
// duplication) — but the call-site discovery this pass does is a DIFFERENT
// AST shape than the ERB half: `<Class>.new(obj, <chan>).notify_x(...)`
// scattered across every .rb file (chained, assigned-to-a-local, or held on
// an ivar across methods of the same class — and, cross-file, via a mixin
// module whose holder class lives in a different file), never a keyword-arg
// call inside a template. That discovery walk stays real tree-sitter
// traversal — a `.dl` rule joins already-resolved facts, it does not decide
// which of three different Ruby binding shapes a channel argument reached
// through — so it lives here, not in rules/ruby/pusher_producer.dl.
//
// Gate: identical to pusher_wrapper_erb — nil unless the graph already has a
// pusher_trigger/pusher_trigger_async publisher node.
func init() { RegisterHub("pusher_producer_sites", pusherProducerSitesHub) }

// pusherProducerSitePred is the fact predicate this hub asserts:
// (Svc, File, Line, Method, Class, ChannelSeg, Event, Label) — one row per
// resolvable `notify_x`-style forwarding call site.
const pusherProducerSitePred = "pusher_producer_site"

func pusherProducerSitesHub(nodes []graph.Node, files []string, _ string, _ []graph.LinkHint, _ graph.SchemaConfig) []Fact {
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

	// hubFiles is keyed by graph.Node.File, not necessarily the exact same
	// string representation `files` (graphSoFar.Files, the raw whole-service
	// file list) uses — parsed independently rather than joined against the
	// rbFiles-keyed cache below by string equality, which silently dropped
	// every hubFiles hit (and this hub's entire output) on at least one real
	// corpus when tried.
	for file := range hubFiles {
		src, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		root, release := pchParseRuby(src)
		if root == nil {
			release()
			continue
		}
		pchCollectWrapperFacts(root, src, eventByMethod, constVals, hashConstVals)
		release()
	}
	if len(eventByMethod) == 0 {
		return nil
	}

	svc := ""
	if len(nodes) > 0 {
		svc = nodes[0].Service
	}

	rbFiles := make([]string, 0, len(files))
	for _, f := range files {
		if strings.HasSuffix(f, ".rb") {
			rbFiles = append(rbFiles, f)
		}
	}
	sort.Strings(rbFiles)

	// Parse every .rb file exactly once — pprCollectMixinHolders and the
	// call-site loop below both used to independently os.ReadFile +
	// pchParseRuby this exact same rbFiles list (2 full passes over every
	// Ruby file in the service for two different AST walks). release() is
	// deferred until both consumers are done with the shared trees.
	parsed := make([]pprParsedFile, 0, len(rbFiles))
	for _, file := range rbFiles {
		if graph.IsTestFilePath(file) {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		root, release := pchParseRuby(src)
		if root == nil {
			release()
			continue
		}
		parsed = append(parsed, pprParsedFile{file: file, src: src, root: root, release: release})
	}
	defer func() {
		for _, pf := range parsed {
			pf.release()
		}
	}()

	holderByModule := pprCollectMixinHolders(parsed, eventByMethod, constVals, hashConstVals)

	seen := map[string]bool{}
	var out []Fact
	emit := func(class, method, chanSeg, event, file string, line int) {
		if event == "" || chanSeg == "" {
			return
		}
		key := fmt.Sprintf("%s:%d:%s", file, line, method)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, Fact{
			Pred: pusherProducerSitePred,
			Args: []Atom{
				Str(svc), Str(file), Int(int64(line)), Str(method), Str(class),
				Str(chanSeg), Str(event), Str(chanSeg + " " + event),
			},
			Origin: Origin{Kind: OriginPrimitive, File: file, Line: line, Pattern: pusherProducerSitePred},
		})
	}

	for _, pf := range parsed {
		file, src, root := pf.file, pf.src, pf.root

		eventOf := func(className, methodName string, eventArg *sitter.Node) string {
			switch methodName {
			case "push", "trigger", "trigger_async":
				return pchLiteralOrConst(eventArg, src, className, constVals)
			default:
				return eventByMethod[className][methodName]
			}
		}

		for _, cs := range pprFindNewCallSites(root, src, eventByMethod) {
			chanSeg := pprResolveChannelSegment(cs.channelArg, src, cs.className, constVals, hashConstVals)
			if chanSeg == "" {
				continue
			}
			for _, m := range cs.methods {
				emit(cs.className, m.name, chanSeg, eventOf(cs.className, m.name, m.eventArg), file, m.line)
			}
		}

		if len(holderByModule) > 0 {
			pprWalkMixinModules(root, src, holderByModule, func(h pprMixinHolder, mc pprReceiverCall) {
				emit(h.className, mc.method, h.chanSeg, eventOf(h.className, mc.method, pchArgAt(mc.node, 1)), file, mc.line)
			})
		}
	}

	return out
}

// pprParsedFile is one .rb file parsed once and shared across
// pprCollectMixinHolders and the call-site loop in pusherProducerSitesHub —
// release must be called exactly once, after both consumers are done.
type pprParsedFile struct {
	file    string
	src     []byte
	root    *sitter.Node
	release func()
}

type pprNotifyCall struct {
	name     string
	line     int
	eventArg *sitter.Node
}

type pprNewCallSite struct {
	className  string
	channelArg *sitter.Node
	methods    []pprNotifyCall
}

// pprFindNewCallSites returns every `<Class>.new(obj, <chan>, ...)` in root
// that is followed (chained, or via a same-scope local var, or via a
// same-class ivar) by a call to a method known to forward to Pusher. Ported
// verbatim from the retired internal/linker/pusher_producer.go's
// findPusherNewCallSites.
func pprFindNewCallSites(root *sitter.Node, src []byte, eventByMethod map[string]map[string]string) []pprNewCallSite {
	var out []pprNewCallSite

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
			if newExpr, cls, chanArg := pprNewExpr(n, src, isPusherClass); newExpr != nil {
				site := pprNewCallSite{className: cls, channelArg: chanArg}

				// chained: parent call `.new(...).notify_x(...)`
				if p := n.Parent(); p != nil && p.Type() == "call" {
					if recv := p.ChildByFieldName("receiver"); recv != nil && recv.Equal(n) {
						if mn := p.ChildByFieldName("method"); mn != nil {
							m := mn.Content(src)
							if notifyish(cls, m) {
								site.methods = append(site.methods, pprNotifyCall{
									name: m, line: int(p.StartPoint().Row) + 1,
									eventArg: pchArgAt(p, 1),
								})
							}
						}
					}
				}

				// assigned: `lhs = Class.new(...)` then `lhs.notify_x(...)`
				if a := pprAscendToAssignment(n); a != nil {
					if lhs := a.ChildByFieldName("left"); lhs != nil {
						addMethod := func(mc pprReceiverCall) {
							if notifyish(cls, mc.method) {
								site.methods = append(site.methods, pprNotifyCall{
									name: mc.method, line: mc.line, eventArg: pchArgAt(mc.node, 1),
								})
							}
						}
						switch lhs.Type() {
						case "identifier":
							scope := pprEnclosingScopeNode(a)
							for _, mc := range pprFindReceiverCalls(scope, lhs.Content(src), src) {
								addMethod(mc)
							}
						case "instance_variable":
							ivar := lhs.Content(src)
							base := strings.TrimPrefix(ivar, "@")
							if body := pprEnclosingClassBody(a); body != nil {
								for _, mc := range pprFindIvarCalls(body, ivar, base, src) {
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
	walk(root)
	return out
}

// pprNewExpr reports whether call n is `<Class>.new(<obj>, <chan>, ...)` for
// a known Pusher wrapper class, returning the node, class name and the
// channel argument (2nd positional).
func pprNewExpr(n *sitter.Node, src []byte, isPusherClass func(string) bool) (*sitter.Node, string, *sitter.Node) {
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
	return n, cls, pchArgAt(n, 1)
}

func pprAscendToAssignment(n *sitter.Node) *sitter.Node {
	for cur := n.Parent(); cur != nil; cur = cur.Parent() {
		switch cur.Type() {
		case "assignment", "operator_assignment":
			return cur
		case "method", "class", "module", "do_block", "block":
			return nil
		}
	}
	return nil
}

// pprEnclosingClassBody returns the body of the nearest enclosing class/module.
func pprEnclosingClassBody(n *sitter.Node) *sitter.Node {
	for cur := n.Parent(); cur != nil; cur = cur.Parent() {
		if cur.Type() == "class" || cur.Type() == "module" {
			return cur.ChildByFieldName("body")
		}
	}
	return nil
}

type pprReceiverCall struct {
	method string
	line   int
	node   *sitter.Node
}

// pprFindIvarCalls returns every `@ivar.method(...)` or `base.method(...)`
// call anywhere in scope — the reader forms an ivar-held instance is invoked
// through (`@pusher.notify_x` directly, or `pusher.notify_x` via an
// `attr_reader :pusher`, which shares the ivar's bare name).
func pprFindIvarCalls(scope *sitter.Node, ivar, base string, src []byte) []pprReceiverCall {
	var out []pprReceiverCall
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
						out = append(out, pprReceiverCall{
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

func pprEnclosingScopeNode(n *sitter.Node) *sitter.Node {
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

func pprFindReceiverCalls(scope *sitter.Node, recvName string, src []byte) []pprReceiverCall {
	var out []pprReceiverCall
	if scope == nil {
		return out
	}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "call" {
			if r := n.ChildByFieldName("receiver"); r != nil && r.Type() == "identifier" && r.Content(src) == recvName {
				if mn := n.ChildByFieldName("method"); mn != nil {
					out = append(out, pprReceiverCall{
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

// pprResolveChannelSegment reduces a channel argument to the single stable
// segment both producer and ERB consumer agree on — the literal middle of
// `"#{Rails.env}.folder-status.#{id}"`, the value of `PusherClient::CHANNELS
// [:lro_update]`, or a bare `"folder-status"`. Distinct from
// hub_pusher_consumer.go's pchRefSegment: a bare `constant` reference here
// resolves against the CURRENT wrapper class's own constVals table (cls is
// known from the enclosing `.new` call), where the ERB side only ever sees
// explicitly-scoped `PusherClient::FOO` references.
func pprResolveChannelSegment(
	arg *sitter.Node, src []byte, cls string,
	constVals map[string]map[string]string,
	hashConstVals map[string]map[string]map[string]string,
) string {
	if arg == nil {
		return ""
	}
	switch arg.Type() {
	case "string":
		return pchChannelFromString(arg, src)
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

func pprConstNameOf(n *sitter.Node, src []byte) string {
	switch n.Type() {
	case "constant":
		return n.Content(src)
	case "scope_resolution":
		if nm := n.ChildByFieldName("name"); nm != nil {
			return nm.Content(src)
		}
	}
	return ""
}

// pprMixinHolder records that some class holds an ivar-bound PusherClient
// instance (`@pusher = PusherClient.new(_, "chan")`) AND `include`s a
// module — so `@pusher.notify_x` calls found in that module's body belong to
// `chanSeg`.
type pprMixinHolder struct {
	ivar, base, className, chanSeg string
}

// pprCollectMixinHolders scans every service file for `class C; include M;
// @ivar = <PusherClass>.new(...); end` shapes and returns module name ->
// holder. First wins on a name collision (deterministic — files are sorted).
func pprCollectMixinHolders(
	parsed []pprParsedFile,
	eventByMethod map[string]map[string]string,
	constVals map[string]map[string]string,
	hashConstVals map[string]map[string]map[string]string,
) map[string]pprMixinHolder {
	isPusherClass := func(name string) bool { _, ok := eventByMethod[name]; return ok }
	out := map[string]pprMixinHolder{}
	for _, pf := range parsed {
		src, root := pf.src, pf.root
		var walkClass func(n *sitter.Node)
		walkClass = func(n *sitter.Node) {
			if n.Type() == "class" {
				var mods []string
				var h pprMixinHolder
				var scan func(m *sitter.Node)
				scan = func(m *sitter.Node) {
					switch m.Type() {
					case "class", "module":
						if !m.Equal(n) {
							return
						}
					case "call":
						if mn := m.ChildByFieldName("method"); mn != nil && mn.Content(src) == "include" {
							if a := pchArgAt(m, 0); a != nil {
								if name := pprConstNameOf(a, src); name != "" {
									mods = append(mods, name)
								}
							}
						}
					case "assignment":
						l := m.ChildByFieldName("left")
						r := m.ChildByFieldName("right")
						if h.ivar == "" && l != nil && l.Type() == "instance_variable" && r != nil && r.Type() == "call" {
							if _, cls, chanArg := pprNewExpr(r, src, isPusherClass); cls != "" {
								if seg := pprResolveChannelSegment(chanArg, src, cls, constVals, hashConstVals); seg != "" {
									h.ivar = l.Content(src)
									h.base = strings.TrimPrefix(h.ivar, "@")
									h.className, h.chanSeg = cls, seg
								}
							}
						}
					}
					for i := 0; i < int(m.NamedChildCount()); i++ {
						scan(m.NamedChild(i))
					}
				}
				scan(n)
				if h.ivar != "" {
					for _, mod := range mods {
						if _, dup := out[mod]; !dup {
							out[mod] = h
						}
					}
				}
			}
			for i := 0; i < int(n.NamedChildCount()); i++ {
				walkClass(n.NamedChild(i))
			}
		}
		walkClass(root)
	}
	return out
}

// pprWalkMixinModules finds every `module M` in root whose name is a known
// holder and reports each `@ivar.notify_x` / `base.notify_x` call in its
// body.
func pprWalkMixinModules(root *sitter.Node, src []byte, holders map[string]pprMixinHolder, emit func(pprMixinHolder, pprReceiverCall)) {
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "module" {
			if nm := n.ChildByFieldName("name"); nm != nil {
				if h, ok := holders[nm.Content(src)]; ok {
					if body := n.ChildByFieldName("body"); body != nil {
						for _, mc := range pprFindIvarCalls(body, h.ivar, h.base, src) {
							emit(h, mc)
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
}
