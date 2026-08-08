package contract

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// EnrichRouteGroups stamps the reconstructed full prefix path into
// http_handler route nodes that are declared inside a router group.
//
// This is the G.3 meta-enrichment pass: a contextual node-join that a pure
// normalizer cannot perform because the group→variable binding sits outside
// the route call (gin: `api := r.Group("/v1")` then `api.GET("/users", h)`;
// chi: `r.Route("/admin", func(r chi.Router) { r.Get("/users", h) })`).
//
// The pass operates on a copy of the input slice. Nodes are identified by
// meta["pattern"] so it is safe to call with the full allNodes set.
//
// Scope: variable-scoped groups within a function/file (including nesting),
// plus (X.9) groups passed across a single function boundary to a registrar
// (`registerX(rg *gin.RouterGroup)`) — the caller's resolved prefix is seeded
// onto the callee's parameter so the callee's routes compose correctly. Groups
// passed through deeper indirection (a param of a param, dynamically built)
// remain unresolved; those routes surface via the contract-engine's unmatched
// policy on the consumer side (never a silent drop).
func EnrichRouteGroups(nodes []graph.Node) []graph.Node {
	// Deep-copy the slice so callers' Meta maps are not mutated.
	enriched := make([]graph.Node, len(nodes))
	for i, n := range nodes {
		if n.Meta != nil {
			copied := make(map[string]string, len(n.Meta))
			for k, v := range n.Meta {
				copied[k] = v
			}
			n.Meta = copied
		}
		enriched[i] = n
	}

	// ── Collect group descriptors grouped by file ──────────────────────────

	type ginGroup struct {
		varName  string // variable assigned: `api` in `api := r.Group(...)`
		prefix   string // the path prefix literal
		receiver string // parent router variable: `r` in `api := r.Group(...)`
		line     int    // declared at this line (for same-scope filtering)
	}
	type chiGroup struct {
		prefix  string // path prefix literal
		line    int    // call expression start line
		endLine int    // func_literal end line (from meta["end_line"])
	}

	ginGroupsByFile := map[string][]ginGroup{}
	chiGroupsByFile := map[string][]chiGroup{}

	// X.9 cross-function registrar facts (minted as NodeTypeVariable bookkeeping
	// nodes by gin_group_registrar_{func,call}). registrarFunc records, per
	// registrar function name, the file it is defined in and the *gin.RouterGroup
	// parameter name to seed. registrarCall records each call site passing a
	// group variable as the first argument.
	type registrarFunc struct {
		defFile string
		param   string
	}
	type registrarCall struct {
		callee string
		arg    string
		file   string
		line   int
	}
	registrarFuncs := map[string]registrarFunc{}
	var registrarCalls []registrarCall

	for i := range enriched {
		n := &enriched[i]
		if n.Type == graph.NodeTypeVariable {
			switch n.Meta["pattern"] {
			case "gin_group_registrar_func":
				name, param := n.Meta["name"], n.Meta["param"]
				if name == "" || param == "" {
					continue
				}
				// A registrar name defined in two files is a real ambiguity; keep
				// the lexicographically-first (file, param) deterministically (#2)
				// rather than depend on node order.
				if existing, ok := registrarFuncs[name]; ok {
					if n.File > existing.defFile ||
						(n.File == existing.defFile && param >= existing.param) {
						continue
					}
				}
				registrarFuncs[name] = registrarFunc{defFile: n.File, param: param}
			case "gin_group_registrar_call":
				callee, arg := n.Meta["callee"], n.Meta["arg"]
				if callee == "" || arg == "" {
					continue
				}
				// Test files wire routes only for the test's own assertions, not
				// the running service; excluding them avoids seeding a registrar
				// param from a throwaway `router.Group("/dsw")` in a _test.go.
				if graph.IsTestFilePath(n.File) {
					continue
				}
				registrarCalls = append(registrarCalls, registrarCall{
					callee: callee, arg: arg, file: n.File, line: n.Line,
				})
			}
			continue
		}
		// HH.3: the group nodes harvested below are route_group, not
		// http_handler. Both types are admitted because this loop is the single
		// place gin/chi groups are collected and retyping them must not silently
		// empty ginGroupsByFile — that would drop every nested route's prefix.
		if n.Type != graph.NodeTypeHTTPHandler && n.Type != graph.NodeTypeRouteGroup {
			continue
		}
		pat := n.Meta["pattern"]
		switch {
		case strings.HasPrefix(pat, "gin_route_group"):
			vn := n.Meta["var_name"]
			// An empty prefix is legitimate, not a failed capture: middleware-only
			// groups (`protected := v1.Group("")`) add no path segment but still
			// forward their parent's prefix to nested routes. Skipping them here
			// (the old `pfx == ""` guard did) breaks the chain — every route under
			// `protected` was treated as root-level and silently lost `/api/v1`.
			// Gate on var_name only; a real gin_route_group always carries a prefix
			// meta key (possibly ""), and an absent key means a malformed node.
			pfxRaw, hasPrefix := n.Meta["prefix"]
			if vn == "" || !hasPrefix {
				continue
			}
			pfx := stripQuotes(pfxRaw)
			ginGroupsByFile[n.File] = append(ginGroupsByFile[n.File], ginGroup{
				varName:  vn,
				prefix:   pfx,
				receiver: n.Meta["receiver"],
				line:     n.Line,
			})
		case strings.HasPrefix(pat, "chi_route_group"):
			pfx := stripQuotes(n.Meta["prefix"])
			if pfx == "" {
				continue
			}
			endLine := 0
			if v := n.Meta["end_line"]; v != "" {
				fmt.Sscanf(v, "%d", &endLine)
			}
			chiGroupsByFile[n.File] = append(chiGroupsByFile[n.File], chiGroup{
				prefix:  pfx,
				line:    n.Line,
				endLine: endLine,
			})
		}
	}

	// ── Resolve gin prefix chains per file ────────────────────────────────
	// Each group's full prefix = receiver-chain prefix + own prefix.
	// Groups whose receiver is not another known group are at the root level.
	// A `seed` (X.9) pre-binds group variables that were resolved in a caller's
	// file (a *gin.RouterGroup parameter): a seeded var is a known receiver, so
	// nested groups declared on top of it compose correctly.
	resolveGinPrefixes := func(groups []ginGroup, seed map[string]string) map[string]string {
		knownVars := make(map[string]bool, len(groups)+len(seed))
		for _, g := range groups {
			knownVars[g.varName] = true
		}
		pm := make(map[string]string, len(groups)+len(seed))
		for v, p := range seed {
			knownVars[v] = true
			pm[v] = p
		}

		// Iterative fixpoint: resolve groups bottom-up (stop when no progress).
		for iteration := 0; iteration <= len(groups); iteration++ {
			changed := false
			for _, g := range groups {
				if _, done := pm[g.varName]; done {
					continue
				}
				if knownVars[g.receiver] {
					// Receiver is another group: can only resolve once parent is done.
					if parentPrefix, ok := pm[g.receiver]; ok {
						pm[g.varName] = parentPrefix + g.prefix
						changed = true
					}
				} else {
					// Receiver is a root router (not a group): prefix chain starts here.
					pm[g.varName] = g.prefix
					changed = true
				}
			}
			if !changed {
				break
			}
		}
		return pm
	}

	ginPrefixByFile := map[string]map[string]string{} // file → varName → fullPrefix
	for file, groups := range ginGroupsByFile {
		ginPrefixByFile[file] = resolveGinPrefixes(groups, nil)
	}

	// ── Pass B (X.9): seed cross-function registrar parameters ────────────────
	// For each `registerX(groupVar, …)` call, resolve groupVar's prefix in the
	// caller's file and seed it onto registerX's *gin.RouterGroup parameter in
	// the file where registerX is defined. Registrars called from two different
	// prefixes are a real ambiguity: seed the lexicographically-first and skip
	// the rest, deterministically (#2). Then re-resolve each seeded file so any
	// group nested on the seeded parameter (`sub := rg.Group("/x")`) composes.
	sort.Slice(registrarCalls, func(a, b int) bool {
		if registrarCalls[a].file != registrarCalls[b].file {
			return registrarCalls[a].file < registrarCalls[b].file
		}
		return registrarCalls[a].line < registrarCalls[b].line
	})
	seedByFile := map[string]map[string]string{} // defFile → param → prefix
	for _, c := range registrarCalls {
		callerPrefixes, ok := ginPrefixByFile[c.file]
		if !ok {
			continue
		}
		fullPrefix, ok := callerPrefixes[c.arg]
		if !ok {
			continue // arg is not a resolvable group var in the caller — leave unseeded
		}
		rf, ok := registrarFuncs[c.callee]
		if !ok {
			continue // not a confirmed *gin.RouterGroup registrar — ignore the call
		}
		if seedByFile[rf.defFile] == nil {
			seedByFile[rf.defFile] = map[string]string{}
		}
		if prev, seeded := seedByFile[rf.defFile][rf.param]; seeded && prev <= fullPrefix {
			continue // keep lexicographically-first prefix on ambiguity
		}
		seedByFile[rf.defFile][rf.param] = fullPrefix
	}
	for file, seed := range seedByFile {
		ginPrefixByFile[file] = resolveGinPrefixes(ginGroupsByFile[file], seed)
	}

	// ── Stamp route nodes ─────────────────────────────────────────────────
	for i := range enriched {
		n := &enriched[i]
		if n.Type != graph.NodeTypeHTTPHandler {
			continue
		}
		pat := n.Meta["pattern"]
		// Group nodes are not stamped. Since HH.3 they are route_group and the
		// type check above already excluded them; this stays as the belt for a
		// group node that reaches here typed as a handler (a stored graph
		// indexed before HH.3, which this pass also runs over).
		if strings.HasPrefix(pat, "gin_route_group") || strings.HasPrefix(pat, "chi_route_group") {
			continue
		}

		currentPath := n.Meta["path"]
		router := n.Meta["router"]

		// Already composed. Composition reads meta["path"] and writes it back,
		// so running the pass over its own output — or over a node whose
		// composed path was persisted — would stack the prefix a second time
		// (`/api/v1/admin/api/v1/admin/users/:id`). meta["full_path"] equal to
		// meta["path"] is the marker that this node has been through the pass;
		// the guard makes the pass idempotent by construction rather than by
		// relying on every caller to feed it raw nodes.
		if fp := n.Meta["full_path"]; fp != "" && fp == currentPath {
			continue
		}

		// Gin enrichment: look up router variable in the file's prefix map.
		if pm, ok := ginPrefixByFile[n.File]; ok && router != "" {
			if fullPrefix, found := pm[router]; found {
				setPath(n, fullPrefix+currentPath)
				continue
			}
		}

		// Chi enrichment: line-range containment inside func_literal bodies.
		if chiGroups, ok := chiGroupsByFile[n.File]; ok {
			var containing []chiGroup
			for _, g := range chiGroups {
				if g.endLine > 0 && n.Line > g.line && n.Line <= g.endLine {
					containing = append(containing, g)
				}
			}
			if len(containing) > 0 {
				// Sort by line ascending so outermost group prefix comes first.
				sort.Slice(containing, func(a, b int) bool {
					return containing[a].line < containing[b].line
				})
				fullPrefix := ""
				for _, g := range containing {
					fullPrefix += g.prefix
				}
				setPath(n, fullPrefix+currentPath)
			}
		}
	}

	return enriched
}

// stripQuotes removes one pair of surrounding quotes (double, single, or
// backtick) from a captured prefix literal. The pattern matcher strips these
// at extraction time for known captures; this is defense in depth for
// third-party workspace patterns (and pre-fix cached graphs) whose prefix
// captures still carry the raw quoted source text.
func stripQuotes(s string) string {
	if len(s) >= 2 {
		switch {
		case s[0] == '"' && s[len(s)-1] == '"',
			s[0] == '\'' && s[len(s)-1] == '\'',
			s[0] == '`' && s[len(s)-1] == '`':
			return s[1 : len(s)-1]
		}
	}
	return s
}

// setPath updates meta["path"] on a node, initialising meta if needed.
//
// When composition actually changes the path, the composed form is also
// recorded as meta["full_path"] and folded into the label. Those two exist for
// the *persisted* graph rather than for matching: a handler stored as
// `GET /users/:id` with meta["router"]="admin" names a route nothing can call,
// and since nodes_fts indexes label (not meta), a search for the real
// `/api/v1/admin/users/:id` finds nothing. The label rewrite is what makes the
// route reachable; see persistComposedRoutes in internal/indexer.
//
// meta["path"] keeps composing in place because the contract engine keys on it.
// It is still not written back to the store: the stamp loop's full_path guard
// makes re-composition safe either way, but keeping the stored path as the raw
// route literal preserves the distinction between "what this call site writes"
// and "where it actually routes", which is the pair a reader needs.
func setPath(n *graph.Node, path string) {
	if n.Meta == nil {
		n.Meta = make(map[string]string)
	}
	old := n.Meta["path"]
	n.Meta["path"] = path
	if path == old {
		return
	}
	n.Meta["full_path"] = path
	// Handler labels are minted as `method + " " + path` (including the
	// trailing space when the route literal is empty, e.g. `camUsers.GET("")`).
	// Rewrite only on an exact match so a pattern that labels its nodes some
	// other way is left alone rather than silently reformatted.
	if method := n.Meta["method"]; n.Label == method+" "+old {
		n.Label = method + " " + path
	}
}
