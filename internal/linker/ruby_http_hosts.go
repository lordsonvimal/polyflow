package linker

import (
	"regexp"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// ResolveRubyHTTPHosts is the Tier-L cross-file depth pass for Ruby HTTP
// clients. A real agent/client almost never posts to a literal URL — it posts
// to a *host method* (`server_api_url(...)`, `Connection.instance.update_job_status_url`)
// whose value is built from an environment variable (`ENV.fetch("LYRA_HOST")`),
// usually defined in a mixin/singleton in a *different* file. The pattern layer
// captures only the opaque call-site token (`url`, `path: url`), so the dynamic
// producer lands in the ledger as an unactionable `config_not_found | url`.
//
// This pass rewrites that token to the concrete `ENV.fetch("VAR")` the host is
// built from, so the downstream config_resolve provider can either bind it to a
// checked-in `.env`/k8s/terraform value (a real `http_call` edge) or ledger a
// *named* deploy-secret miss (`config_not_found | ENV.fetch("LYRA_HOST")`) that
// tells a reviewer exactly which secret to consult — never a fabricated host
// (#12). It resolves three shapes, all bounded and same-file for the value
// trace (only the host-method → env registry is service-wide):
//
//  1. direct call        rest.get(path: Connection.instance.file_download_url)
//  2. local assignment   url = server_api_url(...).to_s; RestClient.post(url, …)
//  3. parameter → caller  def post(url,…); rest.post(path: url) ← post(Conn…url,…)
//
// Returns the mutated http_client nodes so the caller can re-persist them; the
// node metas are also mutated in place in the passed slice.
func ResolveRubyHTTPHosts(nodes []graph.Node, serviceFiles map[string][]string) []graph.Node {
	// Cheap gate: only pay the extra parses when at least one dynamic Ruby
	// http_client node actually needs resolving.
	svcNeeds := make(map[string]bool)
	for i := range nodes {
		if rubyDynamicHTTPNode(&nodes[i]) {
			svcNeeds[nodes[i].Service] = true
		}
	}
	if len(svcNeeds) == 0 {
		return nil
	}

	// Per-service registry: host-method name → {env var, path suffix}
	// (collision-aware).
	registry := make(map[string]map[string]rubyHostInfo)
	for svc, files := range serviceFiles {
		if !svcNeeds[svc] {
			continue
		}
		registry[svc] = buildRubyHostRegistry(files)
	}

	// A single Ruby file may be parsed twice (registry + value trace); cache the
	// parse for the value-trace phase, close everything at the end.
	fileCache := make(map[string]*rubyFileAST)
	defer func() {
		for _, fa := range fileCache {
			if fa != nil && fa.release != nil {
				fa.release()
			}
		}
	}()

	var changed []graph.Node
	for i := range nodes {
		n := &nodes[i]
		if !rubyDynamicHTTPNode(n) {
			continue
		}
		reg := registry[n.Service]
		if len(reg) == 0 {
			continue
		}
		fa := fileCache[n.File]
		if fa == nil {
			fa = parseRubyFileAST(n.File)
			fileCache[n.File] = fa
		}
		if fa == nil {
			continue
		}
		expr := stripKeywordLabel(n.Meta["key_dynamic_raw"])
		env, pathTmpl := fa.resolveHostExpr(expr, fa.enclosingMethod(n.Line), reg, 0)
		if env == "" {
			continue
		}
		n.Meta["key_dynamic_raw"] = `ENV.fetch("` + env + `")`
		n.Meta["host_resolved_via"] = "ruby_env_method"
		n.Meta["host_env_var"] = env
		// PR.3: the host walk above already traverses the literal path the host
		// method appends (`Connection#update_job_status_url` →
		// `"#{service_base_url}/job_items/update_job_status"`); it used to keep
		// only the env var and discard the path, which is why every Ruby agent
		// service resolved 0% of its client URLs. Keep the path too and the node
		// becomes an ordinary static producer the contract engine can join.
		//
		// Only nodes that actually gain a path stop being key_dynamic. That is a
		// real trade: such a node no longer reaches config_resolve, so a missing
		// deploy secret is no longer ledgered by *name* for it. A matched route
		// is worth more than a named host miss, and nodes whose path stays empty
		// keep the previous behaviour exactly.
		if p := rubyClientPath(pathTmpl); p != "" {
			n.Meta["path"] = p
			n.Meta["path_resolved_via"] = "ruby_host_method"
			delete(n.Meta, "key_dynamic")
		}
		changed = append(changed, *n)
	}
	return changed
}

// rubyDynamicHTTPNode reports whether n is a Ruby http_client whose URL went
// unresolved (key_dynamic) and is still an opaque token — not already an env
// expression a prior run captured.
func rubyDynamicHTTPNode(n *graph.Node) bool {
	if n.Type != graph.NodeTypeHTTPClient || n.Language != "ruby" {
		return false
	}
	raw := n.Meta["key_dynamic_raw"]
	return n.Meta["key_dynamic"] == "true" && raw != "" && !strings.Contains(raw, "ENV.")
}

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
	type entry struct {
		info         rubyHostInfo
		conflict     bool
		pathConflict bool
	}
	acc := make(map[string]*entry)
	// Sort for deterministic first-writer-wins on identical env, stable output.
	sorted := filterRubyFiles(files)
	sort.Strings(sorted)
	// Parse + extract per file in parallel; fold into acc serially in sorted
	// order so first-writer-wins stays deterministic.
	perFile := mapParallel(sorted, func(f string) map[string]rubyHostInfo {
		fa := parseRubyFileAST(f)
		if fa == nil {
			return nil
		}
		defer fa.release()
		return fa.hostMethods()
	})
	for _, hm := range perFile {
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
	nameEnv := fa.fileNameEnv()
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
	return out
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
	// Fixpoint: direct ENV first, then propagate through references. Bounded to
	// a few rounds — real config chains are 1–2 hops (@host→@base_url→method).
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

// resolveHostExpr resolves a call-site URL expression to an env var and the
// literal path the host method appends, via the registry, following (bounded) a
// local assignment or a method-parameter's same-file caller argument. The env
// var is the success signal: an empty env means unresolved, and the path is
// whatever came with it (possibly empty).
func (fa *rubyFileAST) resolveHostExpr(expr string, method *rubyMethodInfo, reg map[string]rubyHostInfo, depth int) (string, string) {
	if depth > 3 || expr == "" {
		return "", ""
	}
	e := stripToS(expr)
	if m := finalMethodName(e); m != "" {
		if info, ok := reg[m]; ok {
			return info.env, fillRubyParamHoles(info, e)
		}
	}
	id := bareIdent(e)
	if id == "" || method == nil {
		return "", ""
	}
	// (2) local assignment inside the enclosing method.
	if rhs := fa.assignmentRHS(method.node, id); rhs != "" {
		if env, path := fa.resolveHostExpr(rhs, method, reg, depth+1); env != "" {
			return env, path
		}
	}
	// (3) method parameter → same-file caller argument.
	idx := paramIndex(method, id)
	if idx < 0 {
		return "", ""
	}
	for _, call := range fa.bareCallsTo(method.name) {
		arg := fa.positionalArg(call, idx)
		if arg == "" {
			continue
		}
		callerMethod := fa.enclosingMethod(int(call.StartPoint().Row) + 1)
		if callerMethod == method {
			continue // a method calling itself — avoid a trivial loop
		}
		if env, path := fa.resolveHostExpr(arg, callerMethod, reg, depth+1); env != "" {
			return env, path
		}
	}
	return "", ""
}

// rubyParamHole brackets a host method's parameter name inside its path
// template. NUL cannot occur in Ruby source, so a hole can never collide with
// real path text. Holes the call site does not fill become "*".
const rubyParamHole = "\x00"

var reRubyParamHole = regexp.MustCompile("\x00[^\x00]*\x00")

// fillRubyParamHoles substitutes the literal arguments a call site passes into
// the parameter holes of a host method's path template
// (`server_api_url(endpoint)` → "*/\x00endpoint\x00", called as
// `server_api_url("client_api/v1/agents/register")` → "*/client_api/v1/agents/register").
func fillRubyParamHoles(info rubyHostInfo, expr string) string {
	if !strings.Contains(info.path, rubyParamHole) {
		return info.path
	}
	args := rubyCallArgs(expr)
	out := info.path
	for i, p := range info.params {
		hole := rubyParamHole + p + rubyParamHole
		if !strings.Contains(out, hole) {
			continue
		}
		repl := "*"
		if i < len(args) {
			if t, ok := rubyStringArgTemplate(args[i]); ok {
				repl = t
			}
		}
		out = strings.ReplaceAll(out, hole, repl)
	}
	return out
}

// rubyCallArgs splits the argument list of a call expression's *source text*
// into top-level arguments. The call site is only ever available here as text
// (it arrives via key_dynamic_raw), so this is a small bracket/quote-aware
// splitter rather than a parse — enough for the literal-endpoint shape it
// exists to serve, and it yields nothing rather than a wrong split otherwise.
func rubyCallArgs(expr string) []string {
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
	return nil // unbalanced — no split is better than a wrong one
}

var reRubyInterp = regexp.MustCompile(`#\{[^}]*\}`)

// rubyStringArgTemplate turns a quoted string argument's source text into a path
// template, each `#{…}` interpolation becoming "*". Reports false for anything
// that is not a plain quoted string.
func rubyStringArgTemplate(arg string) (string, bool) {
	arg = strings.TrimSpace(arg)
	if len(arg) < 2 {
		return "", false
	}
	q := arg[0]
	if (q != '"' && q != '\'') || arg[len(arg)-1] != q {
		return "", false
	}
	return reRubyInterp.ReplaceAllString(arg[1:len(arg)-1], "*"), true
}

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

// assignmentRHS returns the source text of the first `id = <rhs>` assignment in
// method's body, or "".
func (fa *rubyFileAST) assignmentRHS(method *sitter.Node, id string) string {
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

// bareCallsTo returns receiver-less call/command nodes invoking method `name`
// (i.e. `name(args)` — not `obj.name(args)`), the callers of a local method.
func (fa *rubyFileAST) bareCallsTo(name string) []*sitter.Node {
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

// positionalArg returns the source text of the idx-th positional argument of a
// call node (keyword pairs/hashes skipped), or "".
func (fa *rubyFileAST) positionalArg(call *sitter.Node, idx int) string {
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

var (
	reKeywordLabel = regexp.MustCompile(`^[a-z_]\w*:\s+(.+)$`)
	reBareIdent    = regexp.MustCompile(`^[a-z_]\w*[?!]?$`)
	reToS          = regexp.MustCompile(`\.(to_s|to_str|to_string|freeze)\s*$`)
)

// stripKeywordLabel turns `path: url` into `url`, leaving a bare expression
// untouched. The required space after the colon avoids matching `Foo::Bar`.
func stripKeywordLabel(s string) string {
	s = strings.TrimSpace(s)
	if m := reKeywordLabel.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	return s
}

// stripToS removes a trailing `.to_s`/`.freeze` conversion so the underlying
// call/identifier can be resolved.
func stripToS(s string) string {
	s = strings.TrimSpace(s)
	for {
		next := reToS.ReplaceAllString(s, "")
		if next == s {
			return s
		}
		s = strings.TrimSpace(next)
	}
}

// bareIdent returns e when it is a single local identifier, else "".
func bareIdent(e string) string {
	e = strings.TrimSpace(e)
	if reBareIdent.MatchString(e) {
		return e
	}
	return ""
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

// paramIndex returns the positional index of parameter id in method, or -1.
func paramIndex(m *rubyMethodInfo, id string) int {
	for i, p := range m.params {
		if p == id {
			return i
		}
	}
	return -1
}
