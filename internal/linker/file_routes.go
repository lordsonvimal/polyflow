// Package linker — M.0: file-based route synthesis.
// Runs once per service after per-file parsing, before the contract engine.
// Emits NodeTypeRoute and NodeTypeHTTPHandler nodes derived from filesystem
// structure alone (no call site required), and wires each via component_impl
// to the real handler function when the parse produced one.
package linker

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/deps"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// RouteConvention describes one framework's filesystem routing dialect.
// Conventions are data — adding a framework is a new table row + fixtures,
// never new traversal code.
type RouteConvention struct {
	// Framework is the unique internal name, used in synthesized node meta.
	// "next-pages" | "next-app" | "sveltekit" | "nuxt" | "nuxt-server" | "remix"
	Framework string
	// DetectDeps: convention activates iff any of these npm package names appear
	// in the service's resolved deps. Empty → never auto-activates.
	DetectDeps []string
	// RootDirs: candidate route roots relative to svcDir; first dir that contains
	// at least one indexed file wins.
	RootDirs []string
	// PageGlob / HandlerGlob: documentation only — actual classification is
	// performed by isPageFile / isHandlerFile (per-framework logic).
	PageGlob    string
	HandlerGlob string
}

// httpVerbSet is the set of exported function labels that identify HTTP verb
// handlers in Next.js app-router route.ts and SvelteKit +server.ts files.
var httpVerbSet = map[string]bool{
	"GET":     true,
	"POST":    true,
	"PUT":     true,
	"PATCH":   true,
	"DELETE":  true,
	"HEAD":    true,
	"OPTIONS": true,
}

// fileRouteConventions is the M.0 pinned dialect table.
// Two entries share the "nuxt" DetectDeps: nuxt (pages root) and
// nuxt-server (server/api root); both activate when nuxt is detected.
var fileRouteConventions = []RouteConvention{
	{
		Framework:   "next-pages",
		DetectDeps:  []string{"next"},
		RootDirs:    []string{"pages", "src/pages"},
		PageGlob:    "**/*.{tsx,ts,jsx,js}",
		HandlerGlob: "api/**/*.{tsx,ts,jsx,js}",
	},
	{
		Framework:   "next-app",
		DetectDeps:  []string{"next"},
		RootDirs:    []string{"app", "src/app"},
		PageGlob:    "**/page.{tsx,ts,jsx,js}",
		HandlerGlob: "**/route.{tsx,ts,jsx,js}",
	},
	{
		Framework:   "sveltekit",
		DetectDeps:  []string{"@sveltejs/kit"},
		RootDirs:    []string{"src/routes"},
		PageGlob:    "**/*+page.svelte",
		HandlerGlob: "**/*+server.{ts,js}",
	},
	{
		Framework:  "nuxt",
		DetectDeps: []string{"nuxt"},
		RootDirs:   []string{"pages", "src/pages"},
		PageGlob:   "**/*.vue",
	},
	{
		Framework:   "nuxt-server",
		DetectDeps:  []string{"nuxt"},
		RootDirs:    []string{"server/api"},
		HandlerGlob: "**/*.{ts,js}",
	},
	{
		Framework:   "remix",
		DetectDeps:  []string{"@remix-run/react", "@remix-run/node"},
		RootDirs:    []string{"app/routes"},
		PageGlob:    "**/*.{tsx,ts,jsx,js}",
	},
}

// FileRoutesResult contains the synthesized nodes, edges, and unresolved refs
// from SynthesizeFileRoutes.
type FileRoutesResult struct {
	Nodes      []graph.Node
	Edges      []graph.Edge
	Unresolved []graph.UnresolvedRef
}

// SynthesizeFileRoutes runs once per service after parsing, before the
// contract engine. It emits:
//   - NodeTypeRoute nodes for pages (meta: path, defined_by=file_convention)
//   - NodeTypeHTTPHandler nodes for API files (meta: method, path,
//     defined_by=file_convention)
//   - EdgeTypeComponentImpl edges: route/handler → real function node (or
//     the NodeTypeFile node when no function is found — never dangling)
//   - graph.UnresolvedRef{Kind: "route_convention_unresolved"} for special
//     constructs the dialect table cannot map (parallel routes, optional
//     catch-alls, verb-less route.ts files)
//
// Files are processed in sorted order for determinism (phases.md rule 2).
// Both next-pages and next-app activate when the "next" dep is present and
// their respective root dirs exist (fan-out, rule 1).
func SynthesizeFileRoutes(svcDir, service string, files []string,
	serviceDeps []deps.Dependency, nodesInFile func(string) []graph.Node) FileRoutesResult {

	// Build npm dep name set for convention detection.
	npmDeps := make(map[string]bool, len(serviceDeps))
	for _, d := range serviceDeps {
		if d.Ecosystem == deps.EcosystemNPM {
			npmDeps[d.Name] = true
		}
	}

	// Process files in sorted order (rule 2: determinism).
	sorted := make([]string, len(files))
	copy(sorted, files)
	sort.Strings(sorted)

	var result FileRoutesResult

	for _, conv := range fileRouteConventions {
		// Convention activates iff any DetectDep is present.
		if !anyDepPresent(conv.DetectDeps, npmDeps) {
			continue
		}

		// Find the first root dir that contains at least one indexed file.
		rootAbs := firstRootWithFiles(svcDir, conv.RootDirs, sorted)
		if rootAbs == "" {
			continue
		}

		for _, absFile := range sorted {
			rel, err := filepath.Rel(rootAbs, absFile)
			if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
				continue
			}
			relSlash := filepath.ToSlash(rel)

			if isPageFile(relSlash, conv.Framework) {
				r := synthPage(absFile, relSlash, service, conv.Framework, nodesInFile)
				result.Nodes = append(result.Nodes, r.Nodes...)
				result.Edges = append(result.Edges, r.Edges...)
				result.Unresolved = append(result.Unresolved, r.Unresolved...)
			} else if isHandlerFile(relSlash, conv.Framework) {
				r := synthHandler(absFile, relSlash, service, conv.Framework, nodesInFile)
				result.Nodes = append(result.Nodes, r.Nodes...)
				result.Edges = append(result.Edges, r.Edges...)
				result.Unresolved = append(result.Unresolved, r.Unresolved...)
			}
		}
	}
	return result
}

// ── page synthesis ────────────────────────────────────────────────────────────

func synthPage(absFile, relSlash, service, framework string, nodesInFile func(string) []graph.Node) FileRoutesResult {
	routePath, ok := fileToRoutePath(relSlash, framework)
	if !ok {
		return FileRoutesResult{Unresolved: []graph.UnresolvedRef{{
			Service: service,
			File:    absFile,
			Name:    relSlash,
			Kind:    "route_convention_unresolved",
		}}}
	}
	nodeID := fmt.Sprintf("fileroute:%s:%s:page", service, absFile)
	n := graph.Node{
		ID:       nodeID,
		Type:     graph.NodeTypeRoute,
		Label:    routePath,
		Service:  service,
		File:     absFile,
		Language: fileLanguage(absFile),
		Meta: map[string]string{
			"path":       routePath,
			"defined_by": "file_convention",
			"framework":  canonicalFramework(framework),
		},
	}
	var edges []graph.Edge
	// component_impl → file node (fallback; default-export detection is descoped M.0).
	// Emit the file node too so the FK target always exists, even for unparsed files
	// (.svelte, .vue) that LinkContainment never processes in M.0.
	fileNodeID := fmt.Sprintf("%s:%s:file", service, absFile)
	fileNode := mintFileNode(absFile, service)
	edges = append(edges, componentImplEdge(nodeID, fileNodeID))
	return FileRoutesResult{Nodes: []graph.Node{fileNode, n}, Edges: edges}
}

// ── handler synthesis ─────────────────────────────────────────────────────────

func synthHandler(absFile, relSlash, service, framework string, nodesInFile func(string) []graph.Node) FileRoutesResult {
	var result FileRoutesResult

	switch framework {
	case "next-pages":
		// pages/api/** → single handler, method="" (ALL).
		// Strip the leading "api/" and the file extension to get the route path.
		apiRel := strings.TrimPrefix(relSlash, "api/")
		// Strip extension before segment mapping ([id].ts → [id] → :id).
		apiRelNoExt := strings.TrimSuffix(apiRel, path.Ext(apiRel))
		apiRel = apiRelNoExt
		routePath, ok := nextSegmentPath(apiRel, false)
		if !ok {
			result.Unresolved = append(result.Unresolved, graph.UnresolvedRef{
				Service: service, File: absFile, Name: relSlash,
				Kind: "route_convention_unresolved",
			})
			return result
		}
		if !strings.HasPrefix(routePath, "/") {
			routePath = "/" + routePath
		}
		routePath = "/api" + routePath
		nodeID := fmt.Sprintf("fileroute:%s:%s:ALL", service, absFile)
		n := graph.Node{
			ID:       nodeID,
			Type:     graph.NodeTypeHTTPHandler,
			Label:    routePath,
			Service:  service,
			File:     absFile,
			Language: fileLanguage(absFile),
			Meta: map[string]string{
				"method":     "",
				"path":       routePath,
				"defined_by": "file_convention",
				"framework":  "next",
			},
		}
		result.Nodes = append(result.Nodes, mintFileNode(absFile, service), n)
		// component_impl → file node (no reliable default-export detection).
		fileNodeID := fmt.Sprintf("%s:%s:file", service, absFile)
		result.Edges = append(result.Edges, componentImplEdge(nodeID, fileNodeID))

	case "next-app", "sveltekit":
		// route.ts / +server.ts → one handler per exported HTTP verb.
		// The route path comes from the directory containing the file.
		dirRel := path.Dir(relSlash)
		if dirRel == "." {
			dirRel = ""
		}
		routePath, ok := nextSegmentPath(dirRel, true)
		if !ok {
			result.Unresolved = append(result.Unresolved, graph.UnresolvedRef{
				Service: service, File: absFile, Name: relSlash,
				Kind: "route_convention_unresolved",
			})
			return result
		}
		verbs := exportedVerbs(absFile, nodesInFile)
		if len(verbs) == 0 {
			// No exported verb found → ledger.
			result.Unresolved = append(result.Unresolved, graph.UnresolvedRef{
				Service: service, File: absFile, Name: relSlash,
				Kind: "route_convention_unresolved",
			})
			return result
		}
		for _, verb := range verbs {
			nodeID := fmt.Sprintf("fileroute:%s:%s:%s", service, absFile, verb)
			n := graph.Node{
				ID:       nodeID,
				Type:     graph.NodeTypeHTTPHandler,
				Label:    routePath,
				Service:  service,
				File:     absFile,
				Language: fileLanguage(absFile),
				Meta: map[string]string{
					"method":     verb,
					"path":       routePath,
					"defined_by": "file_convention",
					"framework":  canonicalFramework(framework),
				},
			}
			result.Nodes = append(result.Nodes, n)
			// component_impl → the parsed function node for this verb, or file node.
			target := verbFunctionNode(absFile, verb, nodesInFile)
			if target == "" {
				target = fmt.Sprintf("%s:%s:file", service, absFile)
				// Mint file node so FK target exists for unparsed files.
				result.Nodes = append(result.Nodes, mintFileNode(absFile, service))
			}
			result.Edges = append(result.Edges, componentImplEdge(nodeID, target))
		}

	case "nuxt-server":
		// server/api/**/*.get.ts → GET /api/<path>
		routePath, method, ok := nuxtServerPath(relSlash)
		if !ok {
			result.Unresolved = append(result.Unresolved, graph.UnresolvedRef{
				Service: service, File: absFile, Name: relSlash,
				Kind: "route_convention_unresolved",
			})
			return result
		}
		suffix := method
		if suffix == "" {
			suffix = "ALL"
		}
		nodeID := fmt.Sprintf("fileroute:%s:%s:%s", service, absFile, suffix)
		n := graph.Node{
			ID:       nodeID,
			Type:     graph.NodeTypeHTTPHandler,
			Label:    routePath,
			Service:  service,
			File:     absFile,
			Language: fileLanguage(absFile),
			Meta: map[string]string{
				"method":     method,
				"path":       routePath,
				"defined_by": "file_convention",
				"framework":  "nuxt",
			},
		}
		result.Nodes = append(result.Nodes, mintFileNode(absFile, service), n)
		fileNodeID := fmt.Sprintf("%s:%s:file", service, absFile)
		result.Edges = append(result.Edges, componentImplEdge(nodeID, fileNodeID))
	}
	return result
}

// ── file classification ───────────────────────────────────────────────────────

// isPageFile reports whether a file (relative to convention root, forward slashes)
// should produce a NodeTypeRoute.
func isPageFile(relSlash, framework string) bool {
	base := path.Base(relSlash)
	ext := path.Ext(base)
	name := strings.TrimSuffix(base, ext)
	switch framework {
	case "next-pages":
		// Pages: JS/TS files NOT under api/ and NOT starting with _ (Next.js internals).
		return isJSExt(ext) && !strings.HasPrefix(relSlash, "api/") && !strings.HasPrefix(name, "_")
	case "next-app":
		// Only files named "page" are pages.
		return isJSExt(ext) && name == "page"
	case "sveltekit":
		return strings.HasSuffix(base, "+page.svelte")
	case "nuxt":
		return ext == ".vue"
	case "nuxt-server":
		return false
	case "remix":
		return isJSExt(ext)
	}
	return false
}

// isHandlerFile reports whether a file should produce NodeTypeHTTPHandler node(s).
func isHandlerFile(relSlash, framework string) bool {
	base := path.Base(relSlash)
	ext := path.Ext(base)
	name := strings.TrimSuffix(base, ext)
	switch framework {
	case "next-pages":
		return isJSExt(ext) && strings.HasPrefix(relSlash, "api/")
	case "next-app":
		return isJSExt(ext) && name == "route"
	case "sveltekit":
		return (name == "+server") && (ext == ".ts" || ext == ".js")
	case "nuxt":
		return false
	case "nuxt-server":
		return ext == ".ts" || ext == ".js"
	case "remix":
		return false // remix page files can also export loader/action but handled in page pass
	}
	return false
}

// ── path mapping ─────────────────────────────────────────────────────────────

// fileToRoutePath maps a relative file path to the route key for page files.
func fileToRoutePath(relSlash, framework string) (string, bool) {
	switch framework {
	case "next-pages", "nuxt":
		return nextPagesPath(relSlash)
	case "next-app":
		// Path comes from the directory; relSlash is like "dashboard/page.tsx".
		return nextSegmentPath(path.Dir(relSlash), true)
	case "sveltekit":
		// relSlash like "blog/[slug]/+page.svelte" → dir = "blog/[slug]"
		return nextSegmentPath(path.Dir(relSlash), true)
	case "remix":
		return remixPath(relSlash)
	}
	return "", false
}

// nextPagesPath converts a next-pages / nuxt relative file path to a route.
// "about.tsx" → "/about", "posts/[id].tsx" → "/posts/:id",
// "[...slug].tsx" → "/*", "index.tsx" → "/".
func nextPagesPath(relSlash string) (string, bool) {
	// Strip extension.
	base := strings.TrimSuffix(relSlash, path.Ext(relSlash))
	segs := strings.Split(base, "/")
	return buildSegmentPath(segs, false)
}

// nextSegmentPath converts a directory path (no extension) to a route key,
// handling next-app / sveltekit dynamic segments and route groups.
// allowGroups=true strips (group) segments; false returns false on any unrecognized
// construct (call-site must ledger).
func nextSegmentPath(dirPath string, allowGroups bool) (string, bool) {
	if dirPath == "" || dirPath == "." {
		return "/", true
	}
	segs := strings.Split(dirPath, "/")
	return buildSegmentPath(segs, allowGroups)
}

// buildSegmentPath converts a slice of path segments to a route key.
// allowGroups controls whether (group) segments are silently stripped.
func buildSegmentPath(segs []string, allowGroups bool) (string, bool) {
	var out []string
	for _, seg := range segs {
		if seg == "" || seg == "index" || seg == "page" || seg == "+page" {
			continue
		}
		// Optional catch-all: [[...opt]] → ledger.
		if strings.HasPrefix(seg, "[[") && strings.HasSuffix(seg, "]]") {
			return "", false
		}
		// Parallel route: @modal → ledger.
		if strings.HasPrefix(seg, "@") {
			return "", false
		}
		// Route group: (marketing) → strip (next-app only).
		if allowGroups && strings.HasPrefix(seg, "(") && strings.HasSuffix(seg, ")") {
			continue
		}
		// Catch-all: [...slug] → *.
		if strings.HasPrefix(seg, "[...") && strings.HasSuffix(seg, "]") {
			out = append(out, "*")
			continue
		}
		// Dynamic: [id] → :id.
		if strings.HasPrefix(seg, "[") && strings.HasSuffix(seg, "]") {
			param := seg[1 : len(seg)-1]
			out = append(out, ":"+param)
			continue
		}
		out = append(out, seg)
	}
	if len(out) == 0 {
		return "/", true
	}
	return "/" + strings.Join(out, "/"), true
}

// nuxtServerPath maps a nuxt server/api file (relative to server/api/) to
// its route path and HTTP method.
// "items.get.ts" → ("/api/items", "GET", true)
// "items.ts"     → ("/api/items", "", true)  (method="" = ALL)
// "[id].get.ts"  → ("/api/:id", "GET", true)
func nuxtServerPath(relSlash string) (routePath, method string, ok bool) {
	// Strip extension.
	base := strings.TrimSuffix(relSlash, path.Ext(relSlash))
	// Check for HTTP method suffix: "items.get" → method=GET, base="items".
	for _, verb := range []string{".get", ".post", ".put", ".patch", ".delete", ".head", ".options"} {
		if strings.HasSuffix(strings.ToLower(base), verb) {
			method = strings.ToUpper(verb[1:])
			base = base[:len(base)-len(verb)]
			break
		}
	}
	segs := strings.Split(base, "/")
	var routeSegs []string
	for _, seg := range segs {
		if seg == "" || seg == "index" {
			continue
		}
		if strings.HasPrefix(seg, "[[") {
			return "", "", false
		}
		if strings.HasPrefix(seg, "@") {
			return "", "", false
		}
		if strings.HasPrefix(seg, "[...") && strings.HasSuffix(seg, "]") {
			routeSegs = append(routeSegs, "*")
			continue
		}
		if strings.HasPrefix(seg, "[") && strings.HasSuffix(seg, "]") {
			routeSegs = append(routeSegs, ":"+seg[1:len(seg)-1])
			continue
		}
		routeSegs = append(routeSegs, seg)
	}
	p := "/api"
	if len(routeSegs) > 0 {
		p += "/" + strings.Join(routeSegs, "/")
	}
	return p, method, true
}

// remixPath converts a remix routes file to a route key.
// "posts.$postId.tsx" → "/posts/:postId" (. = /, $x = :x).
// "_index.tsx" → "/" (root index route).
func remixPath(relSlash string) (string, bool) {
	base := strings.TrimSuffix(relSlash, path.Ext(relSlash))
	if base == "_index" || base == "index" {
		return "/", true
	}
	parts := strings.Split(base, ".")
	var segs []string
	for _, p := range parts {
		if p == "_index" || p == "index" {
			continue
		}
		// Pathless layout: _layout → skip segment.
		if strings.HasPrefix(p, "_") {
			continue
		}
		if strings.HasPrefix(p, "$") {
			segs = append(segs, ":"+p[1:])
		} else {
			segs = append(segs, p)
		}
	}
	if len(segs) == 0 {
		return "/", true
	}
	return "/" + strings.Join(segs, "/"), true
}

// ── exported verb lookup (rule 6: real parse path) ───────────────────────────

// exportedVerbs returns the HTTP verb labels (GET/POST/…) of function nodes
// found by nodesInFile for the given file, sorted for determinism.
func exportedVerbs(absFile string, nodesInFile func(string) []graph.Node) []string {
	var verbs []string
	seen := map[string]bool{}
	for _, n := range nodesInFile(absFile) {
		if n.Type == graph.NodeTypeFunction && httpVerbSet[n.Label] && !seen[n.Label] {
			seen[n.Label] = true
			verbs = append(verbs, n.Label)
		}
	}
	sort.Strings(verbs) // determinism
	return verbs
}

// verbFunctionNode returns the ID of the function node with label=verb in absFile,
// or "" if not found.
func verbFunctionNode(absFile, verb string, nodesInFile func(string) []graph.Node) string {
	for _, n := range nodesInFile(absFile) {
		if n.Type == graph.NodeTypeFunction && n.Label == verb {
			return n.ID
		}
	}
	return ""
}

// ── helpers ───────────────────────────────────────────────────────────────────

func anyDepPresent(deps []string, npmDeps map[string]bool) bool {
	for _, d := range deps {
		if npmDeps[d] {
			return true
		}
	}
	return false
}

// firstRootWithFiles returns the absolute path of the first RootDirs entry
// that has at least one file from sorted underneath it.
func firstRootWithFiles(svcDir string, rootDirs []string, sorted []string) string {
	for _, rd := range rootDirs {
		candidate := filepath.Join(svcDir, rd) + string(filepath.Separator)
		for _, f := range sorted {
			if strings.HasPrefix(f, candidate) {
				return filepath.Join(svcDir, rd)
			}
		}
	}
	return ""
}

func isJSExt(ext string) bool {
	switch ext {
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".mts", ".es6":
		return true
	}
	return false
}

// fileLanguage returns the polyflow language tag for a file extension.
func fileLanguage(absFile string) string {
	switch strings.ToLower(filepath.Ext(absFile)) {
	case ".ts", ".tsx", ".mts":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".es6":
		return "javascript"
	case ".svelte":
		return "svelte"
	case ".vue":
		return "vue"
	}
	return ""
}

// canonicalFramework maps internal framework IDs to their display names in node meta.
func canonicalFramework(framework string) string {
	switch framework {
	case "next-pages", "next-app":
		return "next"
	case "nuxt-server":
		return "nuxt"
	case "sveltekit":
		return "sveltekit"
	case "remix":
		return "remix"
	}
	return framework
}

// mintFileNode creates a NodeTypeFile node for absFile. Used to ensure the FK
// target for component_impl edges always exists, including for unparsed files
// (.svelte, .vue) that LinkContainment never processes in M.0.
func mintFileNode(absFile, service string) graph.Node {
	return graph.Node{
		ID:       fmt.Sprintf("%s:%s:file", service, absFile),
		Type:     graph.NodeTypeFile,
		Label:    absFile,
		Service:  service,
		File:     absFile,
		Language: fileLanguage(absFile),
		Meta:     map[string]string{"basename": filepath.Base(absFile)},
	}
}

// componentImplEdge creates a component_impl edge from the synthesized node to
// the real implementation node (function or file).
func componentImplEdge(fromID, toID string) graph.Edge {
	return graph.Edge{
		ID:         fmt.Sprintf("component_impl:%s->%s", fromID, toID),
		From:       fromID,
		To:         toID,
		Type:       graph.EdgeTypeComponentImpl,
		Confidence: graph.ConfidenceStatic,
		Meta:       map[string]string{"via": "file_convention"},
	}
}
