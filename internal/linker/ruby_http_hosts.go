package linker

import (
	"context"
	"os"
	"regexp"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// ResolveRubyHTTPHosts is the Tier-L cross-file depth pass for Ruby HTTP
// clients. A real agent/client almost never posts to a literal URL — it posts
// to a *host method* (`server_api_url(...)`, `Connection.instance.update_job_status_url`)
// whose value is built from an environment variable (`ENV.fetch("SCE_HOST")`),
// usually defined in a mixin/singleton in a *different* file. The pattern layer
// captures only the opaque call-site token (`url`, `path: url`), so the dynamic
// producer lands in the ledger as an unactionable `config_not_found | url`.
//
// This pass rewrites that token to the concrete `ENV.fetch("VAR")` the host is
// built from, so the downstream config_resolve provider can either bind it to a
// checked-in `.env`/k8s/terraform value (a real `http_call` edge) or ledger a
// *named* deploy-secret miss (`config_not_found | ENV.fetch("SCE_HOST")`) that
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

	// Per-service registry: host-method name → env var (collision-aware).
	registry := make(map[string]map[string]string)
	for svc, files := range serviceFiles {
		if !svcNeeds[svc] {
			continue
		}
		registry[svc] = buildRubyHostEnvRegistry(files)
	}

	// A single Ruby file may be parsed twice (registry + value trace); cache the
	// parse for the value-trace phase, close everything at the end.
	fileCache := make(map[string]*rubyFileAST)
	defer func() {
		for _, fa := range fileCache {
			if fa != nil && fa.tree != nil {
				fa.tree.Close()
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
		env := fa.resolveHostExpr(expr, fa.enclosingMethod(n.Line), reg, 0)
		if env == "" {
			continue
		}
		n.Meta["key_dynamic_raw"] = `ENV.fetch("` + env + `")`
		n.Meta["host_resolved_via"] = "ruby_env_method"
		n.Meta["host_env_var"] = env
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

// buildRubyHostEnvRegistry scans every Ruby file in a service and returns a map
// from host-method name → env var, dropping any name defined in more than one
// file with conflicting env vars (a name collision is an honest ambiguity, left
// unresolved rather than guessed).
func buildRubyHostEnvRegistry(files []string) map[string]string {
	type entry struct {
		env      string
		conflict bool
	}
	acc := make(map[string]*entry)
	// Sort for deterministic first-writer-wins on identical env, stable output.
	sorted := append([]string(nil), files...)
	sort.Strings(sorted)
	for _, f := range sorted {
		if !isRubyFile(f) {
			continue
		}
		fa := parseRubyFileAST(f)
		if fa == nil {
			continue
		}
		for name, env := range fa.hostMethodEnv() {
			e := acc[name]
			if e == nil {
				acc[name] = &entry{env: env}
				continue
			}
			if e.env != env {
				e.conflict = true
			}
		}
		fa.tree.Close()
	}
	out := make(map[string]string, len(acc))
	for name, e := range acc {
		if !e.conflict {
			out[name] = e.env
		}
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
	tree    *sitter.Tree
	root    *sitter.Node
	methods []rubyMethodInfo
	// currentMethodGuard lets exprEnv descend into the one method node it was
	// handed while still refusing to cross into any *other* nested definition.
	currentMethodGuard *sitter.Node
}

func parseRubyFileAST(file string) *rubyFileAST {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	p := sitter.NewParser()
	p.SetLanguage(rubysitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil || tree == nil {
		return nil
	}
	fa := &rubyFileAST{src: src, tree: tree, root: tree.RootNode()}
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

// hostMethodEnv returns this file's host-method name → env var map. A method
// qualifies when its name looks host-ish (…url/uri/host/endpoint/app…) and its
// body derives a single env var — directly (ENV.fetch) or through a same-file
// ivar/attr assigned from one (Connection#service_base_url → @sce_host → ENV).
func (fa *rubyFileAST) hostMethodEnv() map[string]string {
	nameEnv := fa.fileNameEnv()
	out := make(map[string]string)
	for i := range fa.methods {
		m := &fa.methods[i]
		if !hostishName(m.name) {
			continue
		}
		if env := fa.exprEnv(m.node, nameEnv); env != "" {
			out[m.name] = env
		}
	}
	return out
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

// resolveHostExpr resolves a call-site URL expression to an env var via the
// registry, following (bounded) a local assignment or a method-parameter's
// same-file caller argument.
func (fa *rubyFileAST) resolveHostExpr(expr string, method *rubyMethodInfo, reg map[string]string, depth int) string {
	if depth > 3 || expr == "" {
		return ""
	}
	e := stripToS(expr)
	if m := finalMethodName(e); m != "" {
		if env, ok := reg[m]; ok {
			return env
		}
	}
	id := bareIdent(e)
	if id == "" || method == nil {
		return ""
	}
	// (2) local assignment inside the enclosing method.
	if rhs := fa.assignmentRHS(method.node, id); rhs != "" {
		if env := fa.resolveHostExpr(rhs, method, reg, depth+1); env != "" {
			return env
		}
	}
	// (3) method parameter → same-file caller argument.
	idx := paramIndex(method, id)
	if idx < 0 {
		return ""
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
		if env := fa.resolveHostExpr(arg, callerMethod, reg, depth+1); env != "" {
			return env
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
