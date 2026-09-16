package linker

import (
	"regexp"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// ── registry: host-method name → env var ────────────────────────────────────

// rubyHostInfo is everything a host method contributes to a client URL: the env
// var its host is read from, and the literal path suffix it appends. The path
// may carry parameter holes (see rubyParamHole) for a host method that takes its
// endpoint as an argument (`server_api_url("client_api/v1/agents/register")`);
// the call site fills them from the literal it passes.
type rubyHostInfo struct {
	env    string
	path   string
	params []string // positional parameter names, in order, for hole filling
}

// buildRubyHostRegistry scans every Ruby file in a service and returns a map
// from host-method name → env var + path suffix, dropping any name defined in
// more than one file with conflicting env vars (a name collision is an honest
// ambiguity, left unresolved rather than guessed). A name whose *path* differs
// between definitions keeps the env var and loses only the path — the host is
// still unambiguous, just the route is not.
func buildRubyHostRegistry(files []string) map[string]rubyHostInfo {
	// Sort for deterministic first-writer-wins on identical env, stable output.
	sorted := filterRubyFiles(files)
	sort.Strings(sorted)
	// Parse once; keep the ASTs for the L.1 delegate pass below.
	asts := mapParallel(sorted, parseRubyFileAST)
	defer func() {
		for _, fa := range asts {
			if fa != nil {
				fa.release()
			}
		}
	}()
	return buildRubyHostRegistryFromASTs(asts)
}

// buildRubyHostRegistryFromASTs is buildRubyHostRegistry's ASTs-already-parsed
// twin — callers that need the same files' ASTs for their own purposes (e.g.
// ruby_polymorphic_path.go) parse once and pass the result here instead of
// paying for a second parse+scan of every file. Caller owns release.
func buildRubyHostRegistryFromASTs(asts []*rubyFileAST) map[string]rubyHostInfo {
	type entry struct {
		info         rubyHostInfo
		conflict     bool
		pathConflict bool
	}
	acc := make(map[string]*entry)
	fold := func(hm map[string]rubyHostInfo) {
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
	resolve := func() map[string]rubyHostInfo {
		out := make(map[string]rubyHostInfo, len(acc))
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

	// Pass 1: direct host methods + attr-exposed env-derived host names.
	for _, fa := range asts {
		if fa != nil {
			fold(fa.hostMethods())
		}
	}
	// Pass 2 (L.1): a file that `delegate`s a host-ish name inherits that name's
	// env once pass 1 has resolved it from the defining file, so re-run just the
	// delegating files with the resolved name→env as an overlay. One extra
	// round; a name still ambiguous after pass 1 (conflict) is not carried.
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

type rubyMethodInfo struct {
	name       string
	params     []string // positional parameter names, in order
	node       *sitter.Node
	start, end int // 1-based line span
}

type rubyFileAST struct {
	src     []byte
	release func()
	root    *sitter.Node
	methods []rubyMethodInfo
	// currentMethodGuard lets exprEnv descend into the one method node it was
	// handed while still refusing to cross into any *other* nested definition.
	currentMethodGuard *sitter.Node
	// symInlineActive bounds envSymAccessor to a single inline (no re-entry).
	symInlineActive bool

	// XM.21: scan() replaces what used to be up to 7 independent full-tree
	// walks per file (fileNameEnv, fileNamePath, symbolArrayConsts, attrNames'
	// own walk, delegateHostNames, plus ruby_polymorphic_path.go's stringConsts
	// and delegatedNames — all matching against the same "assignment"/
	// "call"/"command" node shapes) with one walk that buffers the raw
	// matches; every one of those methods now post-processes a buffer instead
	// of re-walking fa.root, and is memoized so calling it twice (as
	// hostMethodsWith's two-pass registry build does for delegating files) is
	// a map read, not a second walk.
	scanned         bool
	varAssigns      []rubyAsn // identifier/instance_variable assignments
	constAssigns    []rubyAsn // constant assignments
	attrCalls       []*sitter.Node
	delegateCalls   []*sitter.Node
	bareCallsByName map[string][]*sitter.Node

	nameEnvCache      map[string]string
	namePathCache     map[string]string
	symConstsCache    map[string][]string
	stringConstsCache map[string]string
	attrNamesCache    map[string]bool
}

// rubyAsn is one `name = rhs`-shaped assignment found by scan().
type rubyAsn struct {
	name string
	rhs  *sitter.Node
}

func parseRubyFileAST(file string) *rubyFileAST {
	src, root, release, ok := rubyParse(file)
	if !ok {
		return nil
	}
	fa := &rubyFileAST{src: src, root: root, release: release}
	fa.collectMethods()
	return fa
}

// scan is fa's single top-level pass over its own AST, replacing the several
// independent full-tree walks the methods below used to each run on their
// own. Idempotent and memoized: called lazily by every method that needs its
// buffers, at most once per file.
func (fa *rubyFileAST) scan() {
	if fa.scanned {
		return
	}
	fa.scanned = true
	fa.bareCallsByName = map[string][]*sitter.Node{}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		switch n.Type() {
		case "assignment":
			left := n.ChildByFieldName("left")
			right := n.ChildByFieldName("right")
			if left != nil && right != nil {
				switch left.Type() {
				case "identifier", "instance_variable":
					name := strings.TrimPrefix(left.Content(fa.src), "@")
					fa.varAssigns = append(fa.varAssigns, rubyAsn{name, right})
				case "constant":
					fa.constAssigns = append(fa.constAssigns, rubyAsn{left.Content(fa.src), right})
				}
			}
		case "call", "command", "method_call":
			mn := n.ChildByFieldName("method")
			if mn == nil {
				break
			}
			name := mn.Content(fa.src)
			if n.Type() != "method_call" {
				switch name {
				case "attr_reader", "attr_accessor", "attr_writer":
					fa.attrCalls = append(fa.attrCalls, n)
				case "delegate":
					fa.delegateCalls = append(fa.delegateCalls, n)
				}
			}
			if n.ChildByFieldName("receiver") == nil {
				fa.bareCallsByName[name] = append(fa.bareCallsByName[name], n)
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(fa.root)
}

func (fa *rubyFileAST) collectMethods() {
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "method" || n.Type() == "singleton_method" {
			mi := rubyMethodInfo{
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
					// Positional params only (identifier / optional_parameter).
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

// enclosingMethod returns the innermost method whose line span contains line.
func (fa *rubyFileAST) enclosingMethod(line int) *rubyMethodInfo {
	var best *rubyMethodInfo
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

// hostMethods returns this file's host-method name → {env var, path} map. A
// method qualifies when its name looks host-ish (…url/uri/host/endpoint/app…)
// and its body derives a single env var — directly (ENV.fetch) or through a
// same-file ivar/attr assigned from one (Connection#service_base_url →
// @lyra_host → ENV). The env var remains the gate: a method with a path but no
// single env var is not a host method and is not registered.
func (fa *rubyFileAST) hostMethods() map[string]rubyHostInfo {
	return fa.hostMethodsWith(nil)
}

// hostMethodsWith is hostMethods with an optional name→env overlay merged into
// the same-file map before resolution. The two-pass registry build (L.1) uses
// it to re-run one file's host detection once a `delegate`d name's env is known
// from another file: `Connection#target_url` derives its host from a bare `url`
// that is `delegate :url, to: :config`, invisible until `Config#url`'s env has
// been resolved elsewhere.
func (fa *rubyFileAST) hostMethodsWith(overlay map[string]string) map[string]rubyHostInfo {
	nameEnv := fa.fileNameEnv()
	if len(overlay) > 0 {
		// fileNameEnv is memoized — copy before merging the overlay so the
		// cache isn't mutated by a delegating file's second (overlay) pass.
		merged := make(map[string]string, len(nameEnv)+len(overlay))
		for k, v := range nameEnv {
			merged[k] = v
		}
		for k, v := range overlay {
			if _, ok := merged[k]; !ok {
				merged[k] = v
			}
		}
		nameEnv = merged
	}
	namePath := fa.fileNamePath()
	out := make(map[string]rubyHostInfo)
	for i := range fa.methods {
		m := &fa.methods[i]
		if !hostishName(m.name) {
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
		out[m.name] = rubyHostInfo{env: env, path: path, params: m.params}
	}
	// L.1: a host-ish name exposed only through attr_reader/attr_accessor whose
	// backing ivar is env-derived (`Config`: `attr_accessor(*OPTION_VARS)` +
	// `@url = config_val(:url)` → ENV["URL"]). The env-derived requirement is the
	// guard: an attr backed by a DB column or a plain option leaves nameEnv
	// untouched and never registers.
	for name := range fa.attrNames() {
		if !hostishName(name) {
			continue
		}
		if _, taken := out[name]; taken {
			continue
		}
		if env := nameEnv[name]; env != "" {
			out[name] = rubyHostInfo{env: env}
		}
	}
	return out
}

// attrNames returns every symbol named by an `attr_reader`/`attr_accessor`/
// `attr_writer` call in the file, resolving a `*CONST` splat against a same-file
// `CONST = %i[…]` / `%w[…]` symbol-array literal.
func (fa *rubyFileAST) attrNames() map[string]bool {
	if fa.attrNamesCache != nil {
		return fa.attrNamesCache
	}
	fa.scan()
	consts := fa.symbolArrayConsts()
	out := make(map[string]bool)
	for _, n := range fa.attrCalls {
		args := n.ChildByFieldName("arguments")
		if args == nil {
			continue
		}
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
			if s := rubySymbolNodeName(c, fa.src); s != "" {
				out[s] = true
			}
		}
	}
	fa.attrNamesCache = out
	return out
}

// symbolArrayConsts maps a same-file `CONST = %i[a b c]` (optionally `.freeze`d)
// to its member names.
func (fa *rubyFileAST) symbolArrayConsts() map[string][]string {
	if fa.symConstsCache != nil {
		return fa.symConstsCache
	}
	fa.scan()
	out := make(map[string][]string)
	for _, a := range fa.constAssigns {
		// Peel a trailing `.freeze`.
		r := a.rhs
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
				if s := rubySymbolNodeName(r.NamedChild(i), fa.src); s != "" {
					names = append(names, s)
				}
			}
			out[a.name] = names
		}
	}
	fa.symConstsCache = out
	return out
}

// delegateHostNames returns the host-ish method names this file forwards with a
// bare `delegate :name, …, to: :target` (ActiveSupport). Only the name is
// needed: the two-pass registry looks it up against every service file's
// resolved host methods.
func (fa *rubyFileAST) delegateHostNames() []string {
	fa.scan()
	var out []string
	for _, n := range fa.delegateCalls {
		args := n.ChildByFieldName("arguments")
		if args == nil {
			continue
		}
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
			if s := rubySymbolNodeName(c, fa.src); s != "" && hostishName(s) {
				names = append(names, s)
			}
		}
		if hasTo {
			out = append(out, names...)
		}
	}
	return out
}

// symbolName returns the bare name of a `simple_symbol` (`:url` → "url"),
// `bare_symbol`, or `hash_key_symbol` node, or "".
func rubySymbolNodeName(n *sitter.Node, src []byte) string {
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

// fileNamePath is the path-carrying twin of fileNameEnv: it resolves every
// ivar/local name in the file to the literal path fragment it contributes to a
// URL, so an interpolation of that name inside a host method can be replaced by
// its text rather than a wildcard (Connection#service_base_url →
// "/service_api/v1", so update_job_status_url reads
// "/service_api/v1/job_items/update_job_status" rather than "*/job_items/…").
//
// A name assigned twice with different right-hand sides is dropped rather than
// guessed — this map feeds route text, and a wrong path is a fabricated edge.
func (fa *rubyFileAST) fileNamePath() map[string]string {
	if fa.namePathCache != nil {
		return fa.namePathCache
	}
	fa.scan()
	uniq := make(map[string]*sitter.Node)
	conflict := make(map[string]bool)
	for _, a := range fa.varAssigns {
		if prev, ok := uniq[a.name]; ok {
			if prev.Content(fa.src) != a.rhs.Content(fa.src) {
				conflict[a.name] = true
			}
		} else {
			uniq[a.name] = a.rhs
		}
	}

	names := make([]string, 0, len(uniq))
	for name := range uniq {
		names = append(names, name)
	}
	sort.Strings(names)

	// Fixpoint over a stable name order, overwriting each round rather than
	// freezing the first answer: on round 1 an interpolated name is not resolved
	// yet and reads as "*", which is not a conflict but an unfinished chain
	// (@service_base_url = "#{lyra_host}/service_api/v1" needs @lyra_host first).
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
	fa.namePathCache = namePath
	return namePath
}

// pathTemplate reconstructs the literal path fragment an expression contributes
// to a URL. Reports false when the expression is not path-shaped at all (a
// method call it cannot see through, a conditional, a computation) so the caller
// can fall back to a wildcard instead of inventing text.
//
// An env read returns ("", true) rather than false: `ENV.fetch("LYRA_HOST")` is a
// host, and a host contributes a real and *empty* path — that is what lets
// "#{lyra_host}/client_api/v1/agents/register" reduce to the bare route.
func (fa *rubyFileAST) pathTemplate(n *sitter.Node, namePath map[string]string, params map[string]bool, depth int) (string, bool) {
	if n == nil || depth > 6 {
		return "", false
	}
	switch n.Type() {
	case "method", "singleton_method":
		// Ruby's implicit return: the value is the body's last expression.
		return fa.pathTemplate(lastNamedChild(n.ChildByFieldName("body")), namePath, params, depth+1)

	case "body_statement", "begin_block", "parenthesized_statements":
		return fa.pathTemplate(lastNamedChild(n), namePath, params, depth+1)

	case "string":
		var b strings.Builder
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			switch c.Type() {
			case `"`, "'", "`":
				continue
			case "interpolation":
				if p, ok := fa.pathTemplate(lastNamedChild(c), namePath, params, depth+1); ok {
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
			return rubyParamHole + name + rubyParamHole, true
		}
		return "", false

	case "call", "method_call":
		if envReadVar(n, fa.src) != "" {
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
			// `URI("#{lyra_app}/#{endpoint}")` — a wrapper whose sole argument is
			// the URL. Only the receiver-less form; `URI.parse` is a different
			// node shape and is not worth a second case without a corpus for it.
			if args := n.ChildByFieldName("arguments"); args != nil &&
				args.NamedChildCount() == 1 && n.ChildByFieldName("receiver") == nil {
				return fa.pathTemplate(args.NamedChild(0), namePath, params, depth+1)
			}
		}
		return "", false
	}
	return "", false
}

// lastNamedChild returns n's final named child, or nil.
func lastNamedChild(n *sitter.Node) *sitter.Node {
	if n == nil || n.NamedChildCount() == 0 {
		return nil
	}
	return n.NamedChild(int(n.NamedChildCount()) - 1)
}

// fileNameEnv resolves every ivar/local name in the file to the env var it is
// (transitively, same-file) assigned from. Keyed by bare name (no leading @) so
// an attr_accessor reference resolves to its backing ivar's env.
func (fa *rubyFileAST) fileNameEnv() map[string]string {
	if fa.nameEnvCache != nil {
		return fa.nameEnvCache
	}
	fa.scan()
	nameEnv := make(map[string]string)
	// Fixpoint: direct ENV first, then propagate through references. Bounded to
	// a few rounds — real config chains are 1–2 hops (@host→@base_url→method).
	for round := 0; round < 4; round++ {
		changedAny := false
		for _, a := range fa.varAssigns {
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
	fa.nameEnvCache = nameEnv
	return nameEnv
}

// exprEnv returns the single env var an expression subtree derives, or "" when
// there is none or more than one (ambiguous → left unresolved). It counts both
// direct ENV.fetch/ENV[] reads and references to names already known env-derived.
func (fa *rubyFileAST) exprEnv(n *sitter.Node, nameEnv map[string]string) string {
	seen := make(map[string]bool)
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		// Do not descend into nested method defs — they own their own mapping.
		if n.Type() == "method" || n.Type() == "singleton_method" {
			if n != fa.currentMethodGuard {
				return
			}
		}
		if v := envReadVar(n, fa.src); v != "" {
			seen[v] = true
		}
		switch n.Type() {
		case "identifier", "instance_variable":
			if env, ok := nameEnv[strings.TrimPrefix(n.Content(fa.src), "@")]; ok {
				seen[env] = true
			}
		case "call", "method_call":
			// L.1.3: `config_val(:url)` where `def config_val(opt)` reads
			// `ENV[opt.to_s.upcase]` — inline one same-file accessor, substituting
			// the symbol argument.
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

// envSymAccessor resolves `helper(:sym)` — a receiver-less call whose sole
// argument is a symbol — when the same file defines `def helper(opt)` (exactly
// one positional param) with a body that reads `ENV[opt…]` / `ENV.fetch(opt…)`,
// optionally through `.to_s`/`.upcase`/`.downcase`. Returns the substituted env
// var name (`:url` via `opt.to_s.upcase` → "URL"), or "". Bounded to a single
// inline (no re-entry) so a helper that itself calls another accessor abstains.
func (fa *rubyFileAST) envSymAccessor(n *sitter.Node) string {
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
	sym := rubySymbolNodeName(args.NamedChild(0), fa.src)
	if sym == "" {
		return ""
	}
	fnName := mn.Content(fa.src)
	var mi *rubyMethodInfo
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
	return applySymCase(sym, transform)
}

// findEnvParamRead reports whether method reads `ENV[param…]` / `ENV.fetch(param…)`
// and returns the `.to_s`/`.upcase`/`.downcase` chain applied to param at that
// read (joined by "."). Does not descend into nested defs.
func (fa *rubyFileAST) findEnvParamRead(method *sitter.Node, param string) (string, bool) {
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
			if chain, base := rubyMethodChain(idx, fa.src); base == param {
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

// rubyMethodChain peels a `base.m1.m2` call expression into its trailing method
// names (outermost last) and the base identifier: `option.to_s.upcase` →
// (["to_s","upcase"], "option"). A non-identifier base yields ("", "").
func rubyMethodChain(n *sitter.Node, src []byte) ([]string, string) {
	var chain []string
	for n != nil {
		switch n.Type() {
		case "identifier":
			// Reverse: we collected outermost-first.
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

// applySymCase applies a `.to_s`/`.upcase`/`.downcase` chain to a symbol name.
func applySymCase(sym, transform string) string {
	for _, op := range strings.Split(transform, ".") {
		switch op {
		case "upcase":
			sym = strings.ToUpper(sym)
		case "downcase":
			sym = strings.ToLower(sym)
		case "", "to_s", "to_str", "to_sym", "intern", "freeze":
			// no-op on a bare name
		default:
			return "" // an op we don't model — abstain
		}
	}
	return sym
}

// rubyParamHole brackets a host method's parameter name inside its path
// template. NUL cannot occur in Ruby source, so a hole can never collide with
// real path text. Holes the call site does not fill become "*".
const rubyParamHole = "\x00"

var reRubyParamHole = regexp.MustCompile("\x00[^\x00]*\x00")

// rubyClientPath turns a resolved path template into the `path` meta a Ruby
// http_client node carries, or "" when it carries no routing information.
//
// The leading "*" is the host placeholder the Go side already emits for
// `fmt.Sprintf("%s/api/v1/x", base)`; the contract chain's dynamic_host_strip
// removes it so the client meets the handler's bare path. Emitting it is not
// cosmetic — a bare "/…" would read as root-relative and, under
// same_origin_relative, pin the call to its own service.
func rubyClientPath(tmpl string) string {
	if tmpl == "" {
		return ""
	}
	// Holes no call site filled are unknown segments, not literals.
	tmpl = strings.TrimSpace(reRubyParamHole.ReplaceAllString(tmpl, "*"))
	if tmpl == "" {
		return ""
	}
	if !strings.HasPrefix(tmpl, "*") && !strings.HasPrefix(tmpl, "/") {
		tmpl = "/" + tmpl
	}
	if !strings.HasPrefix(tmpl, "*") {
		tmpl = "*" + tmpl
	}
	// A template of nothing but host and wildcards would match every route in
	// every service; empty_path_guard would void it downstream anyway, and
	// dropping it here keeps the node's honest key_dynamic ledger entry.
	for _, seg := range strings.Split(strings.TrimPrefix(tmpl, "*"), "/") {
		if seg != "" && !strings.Contains(seg, "*") {
			return tmpl
		}
	}
	return ""
}

// bareCallsTo returns receiver-less call/command nodes invoking method `name`
// (i.e. `name(args)` — not `obj.name(args)`), the callers of a local method.
func (fa *rubyFileAST) bareCallsTo(name string) []*sitter.Node {
	if name == "" {
		return nil
	}
	fa.scan()
	return fa.bareCallsByName[name]
}

// ── small helpers ───────────────────────────────────────────────────────────

// envReadVar returns the env var name of an `ENV.fetch("X"…)` or `ENV["X"]`
// expression node, or "".
func envReadVar(n *sitter.Node, src []byte) string {
	switch n.Type() {
	case "call", "method_call":
		recv := n.ChildByFieldName("receiver")
		meth := n.ChildByFieldName("method")
		if recv == nil || meth == nil || recv.Content(src) != "ENV" || meth.Content(src) != "fetch" {
			return ""
		}
		if args := n.ChildByFieldName("arguments"); args != nil && args.NamedChildCount() > 0 {
			return rubyStringLiteral(args.NamedChild(0), src)
		}
	case "element_reference":
		obj := n.ChildByFieldName("object")
		if obj == nil || obj.Content(src) != "ENV" {
			return ""
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if c.Type() == "string" {
				return rubyStringLiteral(c, src)
			}
		}
	}
	return ""
}

// rubyStringLiteral returns a concrete (non-interpolated) string node's content,
// or "".
func rubyStringLiteral(n *sitter.Node, src []byte) string {
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

// finalMethodName extracts the final method identifier of a call-reference
// expression (`Connection.instance.file_download_url` → file_download_url;
// `server_api_url("…")` → server_api_url; bare `server_api_uri` → itself),
// or "" for anything that is not a clean method reference.
func finalMethodName(raw string) string {
	raw = strings.TrimSpace(raw)
	// Drop the argument list first — its contents (string literals, slashes)
	// are irrelevant to the method name and would otherwise fail the shape check.
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
	if raw == "" || !isRubyIdent(raw) {
		return ""
	}
	return raw
}

func isRubyIdent(s string) bool {
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

// hostishName reports whether a method name plausibly returns an HTTP host/URL.
func hostishName(name string) bool {
	l := strings.ToLower(name)
	for _, tok := range []string{"url", "uri", "host", "endpoint", "app", "base"} {
		if strings.Contains(l, tok) {
			return true
		}
	}
	return false
}
