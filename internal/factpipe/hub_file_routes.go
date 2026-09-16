package factpipe

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// hub_file_routes.go is the "file_routes" hub provider (see hub.go) — the
// Tier FX FX.8.32 migration of internal/linker/file_routes.go's retired
// SynthesizeFileRoutes (Phase M.0).
//
// The roster's original caveat called this "actually node-minting" and
// disqualified it outright — that framing predates HubProvider (FX.8.44).
// Deciding which of six framework dialects (Next pages/app router,
// SvelteKit, Nuxt pages/server, Remix) applies, walking the filesystem for
// each dialect's route root, and mapping a file path to a route key are all
// real Go over the service's file list — exactly what a hub is for. Ported
// near-verbatim (`fr`-prefixed).
//
// Unlike every hub before it, this one does its OWN light npm-dependency
// presence check (reads svcPath's own package.json, dependencies +
// devDependencies key names only) instead of receiving a resolved
// []deps.Dependency list. The retired Go took a fully lockfile-resolved
// list (internal/deps.Resolve, which also walks UP to a workspace root
// when svcDir has no manifest of its own) — deliberately narrowed here:
// anyDepPresent only ever checked NAME presence, never a resolved version,
// and every one of this pass's six target frameworks (Next/SvelteKit/
// Nuxt/Remix) requires its own package.json at the service root for its
// own tooling to run at all, so the root-walkup case deps.Resolve exists
// for (a nested frontend under a non-JS service root, e.g. Rails +
// app/javascript) does not arise for a genuine file-router service. A
// fourth HubProvider parameter for one consumer's dependency list was
// judged not worth the generalization; revisit if a real corpus proves
// this narrowing wrong.
func init() { RegisterHub("file_routes", fileRoutesHub) }

// frHTTPVerbSet is the set of exported function labels that identify HTTP
// verb handlers in Next.js app-router route.ts and SvelteKit +server.ts.
var frHTTPVerbSet = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true,
	"DELETE": true, "HEAD": true, "OPTIONS": true,
}

// frRouteConvention mirrors the retired RouteConvention, ported verbatim.
type frRouteConvention struct {
	Framework   string
	DetectDeps  []string
	RootDirs    []string
	PageGlob    string
	HandlerGlob string
}

var frConventions = []frRouteConvention{
	{Framework: "next-pages", DetectDeps: []string{"next"}, RootDirs: []string{"pages", "src/pages"},
		PageGlob: "**/*.{tsx,ts,jsx,js}", HandlerGlob: "api/**/*.{tsx,ts,jsx,js}"},
	{Framework: "next-app", DetectDeps: []string{"next"}, RootDirs: []string{"app", "src/app"},
		PageGlob: "**/page.{tsx,ts,jsx,js}", HandlerGlob: "**/route.{tsx,ts,jsx,js}"},
	{Framework: "sveltekit", DetectDeps: []string{"@sveltejs/kit"}, RootDirs: []string{"src/routes"},
		PageGlob: "**/*+page.svelte", HandlerGlob: "**/*+server.{ts,js}"},
	{Framework: "nuxt", DetectDeps: []string{"nuxt"}, RootDirs: []string{"pages", "src/pages"}, PageGlob: "**/*.vue"},
	{Framework: "nuxt-server", DetectDeps: []string{"nuxt"}, RootDirs: []string{"server/api"}, HandlerGlob: "**/*.{ts,js}"},
	{Framework: "remix", DetectDeps: []string{"@remix-run/react", "@remix-run/node"}, RootDirs: []string{"app/routes"},
		PageGlob: "**/*.{tsx,ts,jsx,js}"},
}

const (
	frFileMintPred    = "fr_file_mint"    // (ID, File, Svc, Language, Basename)
	frRouteMintPred   = "fr_route_mint"   // (ID, Label, Svc, File, Language, Path, Framework, TargetID)
	frHandlerMintPred = "fr_handler_mint" // (ID, Label, Svc, File, Language, Path, Method, Framework, TargetID)
	frUnresolvedPred  = "fr_unresolved"   // (Svc, File, Name)
)

func fileRoutesHub(nodes []graph.Node, files []string, svcPath string, _ []graph.LinkHint, _ graph.SchemaConfig) []Fact {
	if len(nodes) == 0 || svcPath == "" {
		return nil
	}
	svc := nodes[0].Service

	nodesInFile := map[string][]graph.Node{}
	for _, n := range nodes {
		if n.File != "" {
			nodesInFile[n.File] = append(nodesInFile[n.File], n)
		}
	}

	npmDeps := frReadPackageJSONDeps(svcPath)

	sorted := make([]string, len(files))
	copy(sorted, files)
	sort.Strings(sorted)

	var out []Fact
	mintedFile := map[string]bool{}
	mintFile := func(absFile string) string {
		id := fmt.Sprintf("%s:%s:file", svc, absFile)
		if !mintedFile[id] {
			mintedFile[id] = true
			out = append(out, Fact{
				Pred:   frFileMintPred,
				Args:   []Atom{Node(id), Str(absFile), Str(svc), Str(frLanguage(absFile)), Str(filepath.Base(absFile))},
				Origin: Origin{Kind: OriginPrimitive, File: absFile, Pattern: frFileMintPred},
			})
		}
		return id
	}
	ledger := func(absFile, relSlash string) {
		out = append(out, Fact{
			Pred:   frUnresolvedPred,
			Args:   []Atom{Str(svc), Str(absFile), Str(relSlash)},
			Origin: Origin{Kind: OriginPrimitive, File: absFile, Pattern: frUnresolvedPred},
		})
	}

	for _, conv := range frConventions {
		if !frAnyDepPresent(conv.DetectDeps, npmDeps) {
			continue
		}
		rootAbs := frFirstRootWithFiles(svcPath, conv.RootDirs, sorted)
		if rootAbs == "" {
			continue
		}
		for _, absFile := range sorted {
			rel, err := filepath.Rel(rootAbs, absFile)
			if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
				continue
			}
			relSlash := filepath.ToSlash(rel)

			if frIsPageFile(relSlash, conv.Framework) {
				frSynthPage(absFile, relSlash, svc, conv.Framework, mintFile, ledger, &out)
			} else if frIsHandlerFile(relSlash, conv.Framework) {
				frSynthHandler(absFile, relSlash, svc, conv.Framework, nodesInFile, mintFile, ledger, &out)
			}
		}
	}
	return out
}

func frSynthPage(absFile, relSlash, svc, framework string, mintFile func(string) string, ledger func(string, string), out *[]Fact) {
	routePath, ok := frFileToRoutePath(relSlash, framework)
	if !ok {
		ledger(absFile, relSlash)
		return
	}
	nodeID := fmt.Sprintf("fileroute:%s:%s:page", svc, absFile)
	fileNodeID := mintFile(absFile)
	*out = append(*out, Fact{
		Pred: frRouteMintPred,
		Args: []Atom{
			Node(nodeID), Str(routePath), Str(svc), Str(absFile), Str(frLanguage(absFile)),
			Str(routePath), Str(frCanonicalFramework(framework)), Node(fileNodeID),
		},
		Origin: Origin{Kind: OriginPrimitive, File: absFile, Pattern: frRouteMintPred},
	})
}

func frSynthHandler(absFile, relSlash, svc, framework string, nodesInFile map[string][]graph.Node,
	mintFile func(string) string, ledger func(string, string), out *[]Fact) {

	switch framework {
	case "next-pages":
		apiRel := strings.TrimPrefix(relSlash, "api/")
		apiRelNoExt := strings.TrimSuffix(apiRel, path.Ext(apiRel))
		routePath, ok := frNextSegmentPath(apiRelNoExt, false)
		if !ok {
			ledger(absFile, relSlash)
			return
		}
		if !strings.HasPrefix(routePath, "/") {
			routePath = "/" + routePath
		}
		routePath = "/api" + routePath
		nodeID := fmt.Sprintf("fileroute:%s:%s:ALL", svc, absFile)
		fileNodeID := mintFile(absFile)
		*out = append(*out, Fact{
			Pred: frHandlerMintPred,
			Args: []Atom{
				Node(nodeID), Str(routePath), Str(svc), Str(absFile), Str(frLanguage(absFile)),
				Str(routePath), Str(""), Str("next"), Node(fileNodeID),
			},
			Origin: Origin{Kind: OriginPrimitive, File: absFile, Pattern: frHandlerMintPred},
		})

	case "next-app", "sveltekit":
		dirRel := path.Dir(relSlash)
		if dirRel == "." {
			dirRel = ""
		}
		routePath, ok := frNextSegmentPath(dirRel, true)
		if !ok {
			ledger(absFile, relSlash)
			return
		}
		verbs := frExportedVerbs(nodesInFile[absFile])
		if len(verbs) == 0 {
			ledger(absFile, relSlash)
			return
		}
		for _, verb := range verbs {
			nodeID := fmt.Sprintf("fileroute:%s:%s:%s", svc, absFile, verb)
			target := frVerbFunctionNode(nodesInFile[absFile], verb)
			if target == "" {
				target = mintFile(absFile)
			}
			*out = append(*out, Fact{
				Pred: frHandlerMintPred,
				Args: []Atom{
					Node(nodeID), Str(routePath), Str(svc), Str(absFile), Str(frLanguage(absFile)),
					Str(routePath), Str(verb), Str(frCanonicalFramework(framework)), Node(target),
				},
				Origin: Origin{Kind: OriginPrimitive, File: absFile, Pattern: frHandlerMintPred},
			})
		}

	case "nuxt-server":
		routePath, method, ok := frNuxtServerPath(relSlash)
		if !ok {
			ledger(absFile, relSlash)
			return
		}
		suffix := method
		if suffix == "" {
			suffix = "ALL"
		}
		nodeID := fmt.Sprintf("fileroute:%s:%s:%s", svc, absFile, suffix)
		fileNodeID := mintFile(absFile)
		*out = append(*out, Fact{
			Pred: frHandlerMintPred,
			Args: []Atom{
				Node(nodeID), Str(routePath), Str(svc), Str(absFile), Str(frLanguage(absFile)),
				Str(routePath), Str(method), Str("nuxt"), Node(fileNodeID),
			},
			Origin: Origin{Kind: OriginPrimitive, File: absFile, Pattern: frHandlerMintPred},
		})
	}
}

// ── file classification (ported verbatim, fr-prefixed) ──────────────────

func frIsPageFile(relSlash, framework string) bool {
	base := path.Base(relSlash)
	ext := path.Ext(base)
	name := strings.TrimSuffix(base, ext)
	switch framework {
	case "next-pages":
		return frIsJSExt(ext) && !strings.HasPrefix(relSlash, "api/") && !strings.HasPrefix(name, "_")
	case "next-app":
		return frIsJSExt(ext) && name == "page"
	case "sveltekit":
		return strings.HasSuffix(base, "+page.svelte")
	case "nuxt":
		return ext == ".vue"
	case "nuxt-server":
		return false
	case "remix":
		return frIsJSExt(ext)
	}
	return false
}

func frIsHandlerFile(relSlash, framework string) bool {
	base := path.Base(relSlash)
	ext := path.Ext(base)
	name := strings.TrimSuffix(base, ext)
	switch framework {
	case "next-pages":
		return frIsJSExt(ext) && strings.HasPrefix(relSlash, "api/")
	case "next-app":
		return frIsJSExt(ext) && name == "route"
	case "sveltekit":
		return (name == "+server") && (ext == ".ts" || ext == ".js")
	case "nuxt":
		return false
	case "nuxt-server":
		return ext == ".ts" || ext == ".js"
	case "remix":
		return false
	}
	return false
}

// ── path mapping (ported verbatim, fr-prefixed) ──────────────────────────

func frFileToRoutePath(relSlash, framework string) (string, bool) {
	switch framework {
	case "next-pages", "nuxt":
		return frNextPagesPath(relSlash)
	case "next-app":
		return frNextSegmentPath(path.Dir(relSlash), true)
	case "sveltekit":
		return frNextSegmentPath(path.Dir(relSlash), true)
	case "remix":
		return frRemixPath(relSlash)
	}
	return "", false
}

func frNextPagesPath(relSlash string) (string, bool) {
	base := strings.TrimSuffix(relSlash, path.Ext(relSlash))
	segs := strings.Split(base, "/")
	return frBuildSegmentPath(segs, false)
}

func frNextSegmentPath(dirPath string, allowGroups bool) (string, bool) {
	if dirPath == "" || dirPath == "." {
		return "/", true
	}
	segs := strings.Split(dirPath, "/")
	return frBuildSegmentPath(segs, allowGroups)
}

func frBuildSegmentPath(segs []string, allowGroups bool) (string, bool) {
	var out []string
	for _, seg := range segs {
		if seg == "" || seg == "index" || seg == "page" || seg == "+page" {
			continue
		}
		if strings.HasPrefix(seg, "[[") && strings.HasSuffix(seg, "]]") {
			return "", false
		}
		if strings.HasPrefix(seg, "@") {
			return "", false
		}
		if allowGroups && strings.HasPrefix(seg, "(") && strings.HasSuffix(seg, ")") {
			continue
		}
		if strings.HasPrefix(seg, "[...") && strings.HasSuffix(seg, "]") {
			out = append(out, "*")
			continue
		}
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

func frNuxtServerPath(relSlash string) (routePath, method string, ok bool) {
	base := strings.TrimSuffix(relSlash, path.Ext(relSlash))
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

func frRemixPath(relSlash string) (string, bool) {
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

// ── exported verb lookup ──────────────────────────────────────────────────

func frExportedVerbs(inFile []graph.Node) []string {
	var verbs []string
	seen := map[string]bool{}
	for _, n := range inFile {
		if n.Type == graph.NodeTypeFunction && frHTTPVerbSet[n.Label] && !seen[n.Label] {
			seen[n.Label] = true
			verbs = append(verbs, n.Label)
		}
	}
	sort.Strings(verbs)
	return verbs
}

func frVerbFunctionNode(inFile []graph.Node, verb string) string {
	for _, n := range inFile {
		if n.Type == graph.NodeTypeFunction && n.Label == verb {
			return n.ID
		}
	}
	return ""
}

// ── helpers ────────────────────────────────────────────────────────────────

func frAnyDepPresent(want []string, npmDeps map[string]bool) bool {
	for _, d := range want {
		if npmDeps[d] {
			return true
		}
	}
	return false
}

func frFirstRootWithFiles(svcDir string, rootDirs []string, sorted []string) string {
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

func frIsJSExt(ext string) bool {
	switch ext {
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".mts", ".es6":
		return true
	}
	return false
}

func frLanguage(absFile string) string {
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

func frCanonicalFramework(framework string) string {
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

// frReadPackageJSONDeps reads svcPath's OWN package.json (no workspace-root
// walk-up, unlike internal/deps.Resolve) and returns the union of its
// dependencies/devDependencies key names. See this file's doc comment for
// why the walk-up is deliberately not replicated here.
func frReadPackageJSONDeps(svcPath string) map[string]bool {
	data, err := os.ReadFile(filepath.Join(svcPath, "package.json"))
	if err != nil {
		return nil
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil
	}
	out := make(map[string]bool, len(pkg.Dependencies)+len(pkg.DevDependencies))
	for name := range pkg.Dependencies {
		out[name] = true
	}
	for name := range pkg.DevDependencies {
		out[name] = true
	}
	return out
}
