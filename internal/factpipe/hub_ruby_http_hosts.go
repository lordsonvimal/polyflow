package factpipe

import (
	"regexp"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// hub_ruby_http_hosts.go is the "ruby_http_hosts" hub provider (see hub.go)
// — the Tier FX migration of internal/linker/ruby_http_hosts.go's retired
// ResolveRubyHTTPHosts (Tier L).
//
// Scoped here the same way go_http_hosts was (see hub_go_http_hosts.go's own
// doc comment): originally parked on Tier VG's VG.7 as "generalize the value
// engine to Ruby," re-scoped after reading the retired 1306-line file.
// Unlike go_http_hosts, though, this pass is NOT a clean split of "real Go
// AST facts" vs "relational fixpoint" — read closely, only
// buildRubyHostRegistry's cross-file fold (a host-method name registered in
// more than one file, sticky-conflict on a differing env var, one extra L.1
// "delegate" hop) is a genuine whole-service fixpoint; resolveHostExpr (the
// per-call-site trace: local assignment / method-parameter → same-file
// caller argument, depth ≤ 3) never opens a second file — it is a bounded,
// single-file walk, structurally identical in kind to js_http_hosts (ruled
// "fits valuegraph.Engine, not a fixpoint" by this session's own VG.7
// survey). And pathTemplate/fillRubyParamHoles/rubyCallArgs are recursive
// STRING synthesis (building literal interpolated path text, substituting a
// call site's literal arguments into a host method's parameter holes) —
// exactly the kind of "string surgery" hub_rails_route_actions.go's doc
// comment says `derive:` is deliberately forbidden from doing, because a
// `.dl` join has no string-concatenation builtin to do it with either.
//
// So this hub, unlike go_http_hosts's, carries the ENTIRE retired algorithm
// (registry build + call-site resolution), ported near-verbatim
// (`rh`-prefixed) — including the cross-file registry fold, done here in Go
// rather than as `.dl` joins, because resolveHostExpr's recursive trace
// needs live O(1) map lookups against the FOLDED registry at multiple
// branch points per call site, not a value a rule can hand it once at the
// end. rules/ruby/ruby_http_hosts.dl is pure pass-through — the same
// precedent gorm_tables.go/hub_gorm_tables.go already established (see that
// hub's doc comment) for "the hub already resolved the winning value; there
// is nothing left to join."
func init() { RegisterHub("ruby_http_hosts", rubyHTTPHostsHub) }

const (
	rhHostEnvPred  = "ruby_host_env"  // (NodeID, Env, Raw)
	rhHostPathPred = "ruby_host_path" // (NodeID, Path)
)

func rubyHTTPHostsHub(nodes []graph.Node, files []string, _ string) []Fact {
	needsWork := false
	for i := range nodes {
		if rhDynamicHTTPNode(&nodes[i]) {
			needsWork = true
			break
		}
	}
	if !needsWork {
		return nil
	}

	registry := rhBuildHostRegistry(files)
	if len(registry) == 0 {
		return nil
	}

	fileCache := make(map[string]*rhFileAST)
	defer func() {
		for _, fa := range fileCache {
			if fa != nil && fa.release != nil {
				fa.release()
			}
		}
	}()

	var out []Fact
	add := func(pred string, args ...Atom) {
		out = append(out, Fact{Pred: pred, Args: args, Origin: Origin{Kind: OriginPrimitive, Pattern: pred}})
	}

	for i := range nodes {
		n := &nodes[i]
		if !rhDynamicHTTPNode(n) {
			continue
		}
		fa := fileCache[n.File]
		if fa == nil {
			fa = rhParseFileAST(n.File)
			fileCache[n.File] = fa
		}
		if fa == nil {
			continue
		}
		expr := rhStripKeywordLabel(n.Meta["key_dynamic_raw"])
		env, pathTmpl := fa.resolveHostExpr(expr, fa.enclosingMethod(n.Line), registry, 0)
		if env == "" {
			continue
		}
		add(rhHostEnvPred, Node(n.ID), Str(env), Str(`ENV.fetch("`+env+`")`))
		if p := rhClientPath(pathTmpl); p != "" {
			add(rhHostPathPred, Node(n.ID), Str(p))
		}
	}
	return out
}

// rhDynamicHTTPNode mirrors the retired rubyDynamicHTTPNode exactly.
func rhDynamicHTTPNode(n *graph.Node) bool {
	if n.Type != graph.NodeTypeHTTPClient || n.Language != "ruby" {
		return false
	}
	raw := n.Meta["key_dynamic_raw"]
	return n.Meta["key_dynamic"] == "true" && raw != "" && !strings.Contains(raw, "ENV.")
}

// ── registry: host-method name -> env var ───────────────────────────────────

type rhHostInfo struct {
	env    string
	path   string
	params []string
}

func rhBuildHostRegistry(files []string) map[string]rhHostInfo {
	type entry struct {
		info         rhHostInfo
		conflict     bool
		pathConflict bool
	}
	acc := make(map[string]*entry)
	fold := func(hm map[string]rhHostInfo) {
		for name, info := range hm {
			e := acc[name]
			if e == nil {
				acc[name] = &entry{info: info}
				continue
			}
			if e.info.env != info.env {
				e.conflict = true
			}
			if e.info.path != info.path {
				e.pathConflict = true
			}
		}
	}
	resolve := func() map[string]rhHostInfo {
		out := make(map[string]rhHostInfo, len(acc))
		for name, e := range acc {
			if e.conflict {
				continue
			}
			info := e.info
			if e.pathConflict {
				info.path = ""
				info.params = nil
			}
			out[name] = info
		}
		return out
	}

	var sorted []string
	for _, f := range files {
		if strings.HasSuffix(f, ".rb") {
			sorted = append(sorted, f)
		}
	}
	sort.Strings(sorted)

	asts := make([]*rhFileAST, 0, len(sorted))
	for _, f := range sorted {
		asts = append(asts, rhParseFileAST(f))
	}
	defer func() {
		for _, fa := range asts {
			if fa != nil {
				fa.release()
			}
		}
	}()

	for _, fa := range asts {
		if fa != nil {
			fold(fa.hostMethods())
		}
	}
	resolved := resolve()
	for _, fa := range asts {
		if fa == nil {
			continue
		}
		overlay := map[string]string{}
		for _, name := range fa.delegateHostNames() {
			if info, ok := resolved[name]; ok && info.env != "" {
				overlay[name] = info.env
			}
		}
		if len(overlay) > 0 {
			fold(fa.hostMethodsWith(overlay))
		}
	}
	return resolve()
}

// ── per-file AST + resolution ───────────────────────────────────────────────

type rhMethodInfo struct {
	name       string
	params     []string
	node       *sitter.Node
	start, end int
}

type rhFileAST struct {
	src                []byte
	release            func()
	root               *sitter.Node
	methods            []rhMethodInfo
	currentMethodGuard *sitter.Node
	symInlineActive    bool
}

func rhParseFileAST(file string) *rhFileAST {
	src, root, release, ok := rdReadAndParseRuby(file)
	if !ok {
		return nil
	}
	fa := &rhFileAST{src: src, root: root, release: release}
	fa.collectMethods()
	return fa
}

func (fa *rhFileAST) collectMethods() {
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "method" || n.Type() == "singleton_method" {
			mi := rhMethodInfo{
				node:  n,
				start: int(n.StartPoint().Row) + 1,
				end:   int(n.EndPoint().Row) + 1,
			}
			if nn := n.ChildByFieldName("name"); nn != nil {
				mi.name = nn.Content(fa.src)
			}
			if pn := n.ChildByFieldName("parameters"); pn != nil {
				for i := 0; i < int(pn.NamedChildCount()); i++ {
					c := pn.NamedChild(i)
					switch c.Type() {
					case "identifier":
						mi.params = append(mi.params, c.Content(fa.src))
					case "optional_parameter":
						if nm := c.ChildByFieldName("name"); nm != nil {
							mi.params = append(mi.params, nm.Content(fa.src))
						}
					}
				}
			}
			fa.methods = append(fa.methods, mi)
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(fa.root)
}

func (fa *rhFileAST) enclosingMethod(line int) *rhMethodInfo {
	var best *rhMethodInfo
	for i := range fa.methods {
		m := &fa.methods[i]
		if line < m.start || line > m.end {
			continue
		}
		if best == nil || (m.start >= best.start && m.end <= best.end) {
			best = m
		}
	}
	return best
}

func (fa *rhFileAST) hostMethods() map[string]rhHostInfo {
	return fa.hostMethodsWith(nil)
}

func (fa *rhFileAST) hostMethodsWith(overlay map[string]string) map[string]rhHostInfo {
	nameEnv := fa.fileNameEnv()
	for k, v := range overlay {
		if _, ok := nameEnv[k]; !ok {
			nameEnv[k] = v
		}
	}
	namePath := fa.fileNamePath()
	out := make(map[string]rhHostInfo)
	for i := range fa.methods {
		m := &fa.methods[i]
		if !rhHostishName(m.name) {
			continue
		}
		env := fa.exprEnv(m.node, nameEnv)
		if env == "" {
			continue
		}
		params := make(map[string]bool, len(m.params))
		for _, p := range m.params {
			params[p] = true
		}
		path, _ := fa.pathTemplate(m.node, namePath, params, 0)
		out[m.name] = rhHostInfo{env: env, path: path, params: m.params}
	}
	for name := range fa.attrNames() {
		if !rhHostishName(name) {
			continue
		}
		if _, taken := out[name]; taken {
			continue
		}
		if env := nameEnv[name]; env != "" {
			out[name] = rhHostInfo{env: env}
		}
	}
	return out
}

func (fa *rhFileAST) attrNames() map[string]bool {
	consts := fa.symbolArrayConsts()
	out := make(map[string]bool)
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "call" || n.Type() == "command" {
			if mn := n.ChildByFieldName("method"); mn != nil {
				switch mn.Content(fa.src) {
				case "attr_reader", "attr_accessor", "attr_writer":
					if args := n.ChildByFieldName("arguments"); args != nil {
						for i := 0; i < int(args.NamedChildCount()); i++ {
							c := args.NamedChild(i)
							if c.Type() == "splat_argument" {
								if cc := c.NamedChild(0); cc != nil && cc.Type() == "constant" {
									for _, s := range consts[cc.Content(fa.src)] {
										out[s] = true
									}
								}
								continue
							}
							if s := rhSymbolNodeName(c, fa.src); s != "" {
								out[s] = true
							}
						}
					}
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

func (fa *rhFileAST) symbolArrayConsts() map[string][]string {
	out := make(map[string][]string)
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "assignment" {
			left := n.ChildByFieldName("left")
			right := n.ChildByFieldName("right")
			if left != nil && right != nil && left.Type() == "constant" {
				r := right
				if r.Type() == "call" {
					if mn := r.ChildByFieldName("method"); mn != nil && mn.Content(fa.src) == "freeze" {
						if rc := r.ChildByFieldName("receiver"); rc != nil {
							r = rc
						} else if r.NamedChildCount() > 0 {
							r = r.NamedChild(0)
						}
					}
				}
				if r.Type() == "symbol_array" || r.Type() == "string_array" {
					var names []string
					for i := 0; i < int(r.NamedChildCount()); i++ {
						if s := rhSymbolNodeName(r.NamedChild(i), fa.src); s != "" {
							names = append(names, s)
						}
					}
					out[left.Content(fa.src)] = names
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

func (fa *rhFileAST) delegateHostNames() []string {
	var out []string
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "call" || n.Type() == "command" {
			if mn := n.ChildByFieldName("method"); mn != nil && mn.Content(fa.src) == "delegate" {
				if args := n.ChildByFieldName("arguments"); args != nil {
					hasTo := false
					var names []string
					for i := 0; i < int(args.NamedChildCount()); i++ {
						c := args.NamedChild(i)
						if c.Type() == "pair" {
							if k := c.ChildByFieldName("key"); k != nil &&
								strings.TrimSuffix(k.Content(fa.src), ":") == "to" {
								hasTo = true
							}
							continue
						}
						if s := rhSymbolNodeName(c, fa.src); s != "" && rhHostishName(s) {
							names = append(names, s)
						}
					}
					if hasTo {
						out = append(out, names...)
					}
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

func rhSymbolNodeName(n *sitter.Node, src []byte) string {
	if n == nil {
		return ""
	}
	switch n.Type() {
	case "simple_symbol":
		return strings.TrimPrefix(n.Content(src), ":")
	case "hash_key_symbol":
		return strings.TrimSuffix(n.Content(src), ":")
	case "bare_symbol":
		if c := n.NamedChild(0); c != nil {
			return c.Content(src)
		}
	case "string_content":
		return n.Content(src)
	}
	return ""
}

func (fa *rhFileAST) fileNamePath() map[string]string {
	uniq := make(map[string]*sitter.Node)
	conflict := make(map[string]bool)
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "assignment" {
			left := n.ChildByFieldName("left")
			right := n.ChildByFieldName("right")
			if left != nil && right != nil {
				switch left.Type() {
				case "identifier", "instance_variable":
					name := strings.TrimPrefix(left.Content(fa.src), "@")
					if prev, ok := uniq[name]; ok {
						if prev.Content(fa.src) != right.Content(fa.src) {
							conflict[name] = true
						}
					} else {
						uniq[name] = right
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(fa.root)

	names := make([]string, 0, len(uniq))
	for name := range uniq {
		names = append(names, name)
	}
	sort.Strings(names)

	namePath := make(map[string]string, len(uniq))
	for round := 0; round < 4; round++ {
		for _, name := range names {
			if conflict[name] {
				continue
			}
			if p, ok := fa.pathTemplate(uniq[name], namePath, nil, 0); ok {
				namePath[name] = p
			}
		}
	}
	for name := range conflict {
		delete(namePath, name)
	}
	return namePath
}

func (fa *rhFileAST) pathTemplate(n *sitter.Node, namePath map[string]string, params map[string]bool, depth int) (string, bool) {
	if n == nil || depth > 6 {
		return "", false
	}
	switch n.Type() {
	case "method", "singleton_method":
		return fa.pathTemplate(rhLastNamedChild(n.ChildByFieldName("body")), namePath, params, depth+1)

	case "body_statement", "begin_block", "parenthesized_statements":
		return fa.pathTemplate(rhLastNamedChild(n), namePath, params, depth+1)

	case "string":
		var b strings.Builder
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			switch c.Type() {
			case `"`, "'", "`":
				continue
			case "interpolation":
				if p, ok := fa.pathTemplate(rhLastNamedChild(c), namePath, params, depth+1); ok {
					b.WriteString(p)
				} else {
					b.WriteString("*")
				}
			default:
				b.WriteString(c.Content(fa.src))
			}
		}
		return b.String(), true

	case "identifier", "instance_variable":
		name := strings.TrimPrefix(n.Content(fa.src), "@")
		if p, ok := namePath[name]; ok {
			return p, true
		}
		if params[name] {
			return rhParamHole + name + rhParamHole, true
		}
		return "", false

	case "call", "method_call":
		if rhEnvReadVar(n, fa.src) != "" {
			return "", true
		}
		meth := n.ChildByFieldName("method")
		name := ""
		if meth != nil {
			name = meth.Content(fa.src)
		}
		switch name {
		case "to_s", "to_str", "freeze":
			return fa.pathTemplate(n.ChildByFieldName("receiver"), namePath, params, depth+1)
		case "URI", "Pathname":
			if args := n.ChildByFieldName("arguments"); args != nil &&
				args.NamedChildCount() == 1 && n.ChildByFieldName("receiver") == nil {
				return fa.pathTemplate(args.NamedChild(0), namePath, params, depth+1)
			}
		}
		return "", false
	}
	return "", false
}

func rhLastNamedChild(n *sitter.Node) *sitter.Node {
	if n == nil || n.NamedChildCount() == 0 {
		return nil
	}
	return n.NamedChild(int(n.NamedChildCount()) - 1)
}

func (fa *rhFileAST) fileNameEnv() map[string]string {
	type asn struct {
		name string
		rhs  *sitter.Node
	}
	var assigns []asn
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "assignment" {
			left := n.ChildByFieldName("left")
			right := n.ChildByFieldName("right")
			if left != nil && right != nil {
				switch left.Type() {
				case "identifier", "instance_variable":
					assigns = append(assigns, asn{strings.TrimPrefix(left.Content(fa.src), "@"), right})
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(fa.root)

	nameEnv := make(map[string]string)
	for round := 0; round < 4; round++ {
		changedAny := false
		for _, a := range assigns {
			if _, done := nameEnv[a.name]; done {
				continue
			}
			if env := fa.exprEnv(a.rhs, nameEnv); env != "" {
				nameEnv[a.name] = env
				changedAny = true
			}
		}
		if !changedAny {
			break
		}
	}
	return nameEnv
}

func (fa *rhFileAST) exprEnv(n *sitter.Node, nameEnv map[string]string) string {
	seen := make(map[string]bool)
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if n.Type() == "method" || n.Type() == "singleton_method" {
			if n != fa.currentMethodGuard {
				return
			}
		}
		if v := rhEnvReadVar(n, fa.src); v != "" {
			seen[v] = true
		}
		switch n.Type() {
		case "identifier", "instance_variable":
			if env, ok := nameEnv[strings.TrimPrefix(n.Content(fa.src), "@")]; ok {
				seen[env] = true
			}
		case "call", "method_call":
			if v := fa.envSymAccessor(n); v != "" {
				seen[v] = true
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	fa.currentMethodGuard = n
	walk(n)
	fa.currentMethodGuard = nil
	if len(seen) != 1 {
		return ""
	}
	for v := range seen {
		return v
	}
	return ""
}

func (fa *rhFileAST) envSymAccessor(n *sitter.Node) string {
	if fa.symInlineActive {
		return ""
	}
	if n.ChildByFieldName("receiver") != nil {
		return ""
	}
	mn := n.ChildByFieldName("method")
	args := n.ChildByFieldName("arguments")
	if mn == nil || args == nil || args.NamedChildCount() != 1 {
		return ""
	}
	sym := rhSymbolNodeName(args.NamedChild(0), fa.src)
	if sym == "" {
		return ""
	}
	fnName := mn.Content(fa.src)
	var mi *rhMethodInfo
	for i := range fa.methods {
		if fa.methods[i].name == fnName && len(fa.methods[i].params) == 1 {
			mi = &fa.methods[i]
			break
		}
	}
	if mi == nil {
		return ""
	}
	fa.symInlineActive = true
	transform, ok := fa.findEnvParamRead(mi.node, mi.params[0])
	fa.symInlineActive = false
	if !ok {
		return ""
	}
	return rhApplySymCase(sym, transform)
}

func (fa *rhFileAST) findEnvParamRead(method *sitter.Node, param string) (string, bool) {
	var found string
	var ok bool
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if ok || n == nil {
			return
		}
		if n != method && (n.Type() == "method" || n.Type() == "singleton_method") {
			return
		}
		var idx *sitter.Node
		switch n.Type() {
		case "element_reference":
			if obj := n.ChildByFieldName("object"); obj != nil && obj.Content(fa.src) == "ENV" &&
				n.NamedChildCount() >= 2 {
				idx = n.NamedChild(1)
			}
		case "call", "method_call":
			recv := n.ChildByFieldName("receiver")
			m := n.ChildByFieldName("method")
			if recv != nil && recv.Content(fa.src) == "ENV" && m != nil && m.Content(fa.src) == "fetch" {
				if a := n.ChildByFieldName("arguments"); a != nil && a.NamedChildCount() > 0 {
					idx = a.NamedChild(0)
				}
			}
		}
		if idx != nil {
			if chain, base := rhMethodChain(idx, fa.src); base == param {
				found, ok = strings.Join(chain, "."), true
				return
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(method)
	return found, ok
}

func rhMethodChain(n *sitter.Node, src []byte) ([]string, string) {
	var chain []string
	for n != nil {
		switch n.Type() {
		case "identifier":
			for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
				chain[i], chain[j] = chain[j], chain[i]
			}
			return chain, n.Content(src)
		case "call", "method_call":
			m := n.ChildByFieldName("method")
			r := n.ChildByFieldName("receiver")
			if m == nil || r == nil {
				return nil, ""
			}
			chain = append(chain, m.Content(src))
			n = r
		default:
			return nil, ""
		}
	}
	return nil, ""
}

func rhApplySymCase(sym, transform string) string {
	for _, op := range strings.Split(transform, ".") {
		switch op {
		case "upcase":
			sym = strings.ToUpper(sym)
		case "downcase":
			sym = strings.ToLower(sym)
		case "", "to_s", "to_str", "to_sym", "intern", "freeze":
		default:
			return ""
		}
	}
	return sym
}

// ── per-call-site resolution ─────────────────────────────────────────────

func (fa *rhFileAST) resolveHostExpr(expr string, method *rhMethodInfo, reg map[string]rhHostInfo, depth int) (string, string) {
	if depth > 3 || expr == "" {
		return "", ""
	}
	e := rhStripToS(expr)
	id := rhBareIdent(e)

	localBound := id != "" && method != nil &&
		(fa.assignmentRHS(method.node, id) != "" || rhParamIndex(method, id) >= 0)

	if !localBound {
		if m := rhFinalMethodName(e); m != "" {
			if info, ok := reg[m]; ok {
				return info.env, rhFillParamHoles(info, e)
			}
		}
	}
	if id == "" || method == nil {
		return "", ""
	}
	if rhs := fa.assignmentRHS(method.node, id); rhs != "" {
		if env, path := fa.resolveHostExpr(rhs, method, reg, depth+1); env != "" {
			return env, path
		}
	}
	if idx := rhParamIndex(method, id); idx >= 0 {
		for _, call := range fa.bareCallsTo(method.name) {
			arg := fa.positionalArg(call, idx)
			if arg == "" {
				continue
			}
			callerMethod := fa.enclosingMethod(int(call.StartPoint().Row) + 1)
			if callerMethod == method {
				continue
			}
			if env, path := fa.resolveHostExpr(arg, callerMethod, reg, depth+1); env != "" {
				return env, path
			}
		}
	}
	if localBound {
		if info, ok := reg[id]; ok {
			return info.env, info.path
		}
	}
	return "", ""
}

const rhParamHole = "\x00"

var reRhParamHole = regexp.MustCompile("\x00[^\x00]*\x00")

func rhFillParamHoles(info rhHostInfo, expr string) string {
	if !strings.Contains(info.path, rhParamHole) {
		return info.path
	}
	args := rhCallArgs(expr)
	out := info.path
	for i, p := range info.params {
		hole := rhParamHole + p + rhParamHole
		if !strings.Contains(out, hole) {
			continue
		}
		repl := "*"
		if i < len(args) {
			if t, ok := rhStringArgTemplate(args[i]); ok {
				repl = t
			}
		}
		out = strings.ReplaceAll(out, hole, repl)
	}
	return out
}

func rhCallArgs(expr string) []string {
	open := strings.IndexByte(expr, '(')
	if open < 0 {
		return nil
	}
	var (
		args  []string
		cur   strings.Builder
		depth int
		quote byte
	)
	for i := open; i < len(expr); i++ {
		c := expr[i]
		if quote != 0 {
			cur.WriteByte(c)
			if c == '\\' && i+1 < len(expr) {
				i++
				cur.WriteByte(expr[i])
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'', '`':
			quote = c
			cur.WriteByte(c)
		case '(', '[', '{':
			depth++
			if depth > 1 {
				cur.WriteByte(c)
			}
		case ')', ']', '}':
			depth--
			if depth == 0 {
				args = append(args, strings.TrimSpace(cur.String()))
				return args
			}
			cur.WriteByte(c)
		case ',':
			if depth == 1 {
				args = append(args, strings.TrimSpace(cur.String()))
				cur.Reset()
			} else {
				cur.WriteByte(c)
			}
		default:
			cur.WriteByte(c)
		}
	}
	return nil
}

var reRhInterp = regexp.MustCompile(`#\{[^}]*\}`)

func rhStringArgTemplate(arg string) (string, bool) {
	arg = strings.TrimSpace(arg)
	if len(arg) < 2 {
		return "", false
	}
	q := arg[0]
	if (q != '"' && q != '\'') || arg[len(arg)-1] != q {
		return "", false
	}
	return reRhInterp.ReplaceAllString(arg[1:len(arg)-1], "*"), true
}

func rhClientPath(tmpl string) string {
	if tmpl == "" {
		return ""
	}
	tmpl = strings.TrimSpace(reRhParamHole.ReplaceAllString(tmpl, "*"))
	if tmpl == "" {
		return ""
	}
	if !strings.HasPrefix(tmpl, "*") && !strings.HasPrefix(tmpl, "/") {
		tmpl = "/" + tmpl
	}
	if !strings.HasPrefix(tmpl, "*") {
		tmpl = "*" + tmpl
	}
	for _, seg := range strings.Split(strings.TrimPrefix(tmpl, "*"), "/") {
		if seg != "" && !strings.Contains(seg, "*") {
			return tmpl
		}
	}
	return ""
}

func (fa *rhFileAST) assignmentRHS(method *sitter.Node, id string) string {
	var found string
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if found != "" || n == nil {
			return
		}
		if n != method && (n.Type() == "method" || n.Type() == "singleton_method") {
			return
		}
		if n.Type() == "assignment" {
			left := n.ChildByFieldName("left")
			right := n.ChildByFieldName("right")
			if left != nil && right != nil && left.Type() == "identifier" &&
				left.Content(fa.src) == id {
				found = right.Content(fa.src)
				return
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(method)
	return found
}

func (fa *rhFileAST) bareCallsTo(name string) []*sitter.Node {
	if name == "" {
		return nil
	}
	var out []*sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		switch n.Type() {
		case "call", "command", "method_call":
			mn := n.ChildByFieldName("method")
			if mn != nil && mn.Content(fa.src) == name && n.ChildByFieldName("receiver") == nil {
				out = append(out, n)
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(fa.root)
	return out
}

func (fa *rhFileAST) positionalArg(call *sitter.Node, idx int) string {
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return ""
	}
	pos := 0
	for i := 0; i < int(args.NamedChildCount()); i++ {
		c := args.NamedChild(i)
		if c.Type() == "pair" || c.Type() == "hash" {
			continue
		}
		if pos == idx {
			return c.Content(fa.src)
		}
		pos++
	}
	return ""
}

// ── small helpers ───────────────────────────────────────────────────────────

func rhEnvReadVar(n *sitter.Node, src []byte) string {
	switch n.Type() {
	case "call", "method_call":
		recv := n.ChildByFieldName("receiver")
		meth := n.ChildByFieldName("method")
		if recv == nil || meth == nil || recv.Content(src) != "ENV" || meth.Content(src) != "fetch" {
			return ""
		}
		if args := n.ChildByFieldName("arguments"); args != nil && args.NamedChildCount() > 0 {
			return rhStringLiteral(args.NamedChild(0), src)
		}
	case "element_reference":
		obj := n.ChildByFieldName("object")
		if obj == nil || obj.Content(src) != "ENV" {
			return ""
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if c.Type() == "string" {
				return rhStringLiteral(c, src)
			}
		}
	}
	return ""
}

func rhStringLiteral(n *sitter.Node, src []byte) string {
	if n.Type() != "string" {
		return ""
	}
	var b strings.Builder
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		switch c.Type() {
		case `"`, "'", "`", "interpolation":
			continue
		default:
			b.WriteString(c.Content(src))
		}
	}
	return b.String()
}

var (
	reRhKeywordLabel = regexp.MustCompile(`^[a-z_]\w*:\s+(.+)$`)
	reRhBareIdent    = regexp.MustCompile(`^[a-z_]\w*[?!]?$`)
	reRhToS          = regexp.MustCompile(`\.(to_s|to_str|to_string|freeze)\s*$`)
)

func rhStripKeywordLabel(s string) string {
	s = strings.TrimSpace(s)
	if m := reRhKeywordLabel.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	return s
}

func rhStripToS(s string) string {
	s = strings.TrimSpace(s)
	for {
		next := reRhToS.ReplaceAllString(s, "")
		if next == s {
			return s
		}
		s = strings.TrimSpace(next)
	}
}

func rhBareIdent(e string) string {
	e = strings.TrimSpace(e)
	if reRhBareIdent.MatchString(e) {
		return e
	}
	return ""
}

func rhFinalMethodName(raw string) string {
	raw = strings.TrimSpace(raw)
	if i := strings.IndexByte(raw, '('); i >= 0 {
		raw = raw[:i]
	}
	if raw == "" || strings.ContainsAny(raw, "[]{} \t\n\"'") || strings.Contains(raw, "::") {
		return ""
	}
	if i := strings.LastIndexByte(raw, '.'); i >= 0 {
		raw = raw[i+1:]
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || !rhIsIdent(raw) {
		return ""
	}
	return raw
}

func rhIsIdent(s string) bool {
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		case (r == '?' || r == '!') && i == len(s)-1:
		default:
			return false
		}
	}
	return s != ""
}

// rhHostishName mirrors internal/linker's shared hostishName (defined in
// ruby_http_hosts.go, which this migration deletes — go_http_hosts.go's own
// hub already carries a Go-language copy of the same heuristic as
// ghHostishName; this is the Ruby-hub's).
func rhHostishName(name string) bool {
	l := strings.ToLower(name)
	for _, tok := range []string{"url", "uri", "host", "endpoint", "app", "base"} {
		if strings.Contains(l, tok) {
			return true
		}
	}
	return false
}

func rhParamIndex(m *rhMethodInfo, id string) int {
	for i, p := range m.params {
		if p == id {
			return i
		}
	}
	return -1
}
