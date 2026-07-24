package indexer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// buildNextWorkspace creates a workspace fixture exercising every M.0 table row
// for next-pages and next-app, plus a fetch call for contract-linking acceptance.
func buildNextWorkspace(t *testing.T) *workspace.WorkspaceConfig {
	t.Helper()
	dir := t.TempDir()
	svc := filepath.Join(dir, "frontend")
	require.NoError(t, os.MkdirAll(svc, 0o755))

	writeFile(t, svc, "package.json", `{"dependencies": {"next": "^14.0.0"}}`)

	// next-pages rows ─────────────────────────────────────────────────────────
	mkdirWrite(t, svc, "pages/index.tsx", `export default function Home() {}`)
	mkdirWrite(t, svc, "pages/about.tsx", `export default function About() {}`)
	mkdirWrite(t, svc, "pages/posts/[id].tsx", `export default function Post() {}`)
	mkdirWrite(t, svc, "pages/[...slug].tsx", `export default function CatchAll() {}`)
	// pages/api → http_handler ALL /api/users/:id
	mkdirWrite(t, svc, "pages/api/users/[id].ts",
		`export default function handler(req, res) { res.json({}) }`)

	// next-app rows ───────────────────────────────────────────────────────────
	mkdirWrite(t, svc, "app/dashboard/page.tsx", `export default function Dashboard() {}`)
	// route group (marketing) stripped → /pricing
	mkdirWrite(t, svc, "app/(marketing)/pricing/page.tsx",
		`export default function Pricing() {}`)
	// route.ts → per-verb handlers (GET and POST)
	mkdirWrite(t, svc, "app/api/users/route.ts",
		`export async function GET(req) { return Response.json([]) }
export async function POST(req) { return Response.json({}) }`)
	// parallel route → ledger
	mkdirWrite(t, svc, "app/@modal/inbox/page.tsx",
		`export default function Modal() {}`)
	// optional catch-all → ledger
	mkdirWrite(t, svc, "app/[[...opt]]/page.tsx",
		`export default function Optional() {}`)

	// Second service (client) with a fetch call → acceptance test: http_call → ALL /api/users/:id.
	// Must be a separate service so the cross-service http contract rule fires
	// (same_service policy is skip_unless_meta:datastar for API calls).
	clientSvc := filepath.Join(dir, "client")
	require.NoError(t, os.MkdirAll(clientSvc, 0o755))
	writeFile(t, clientSvc, "package.json", `{"dependencies": {}}`)
	mkdirWrite(t, clientSvc, "lib/users.ts",
		`async function loadUser() {
  const r = await fetch("/api/users/42");
  return r.json();
}`)

	return &workspace.WorkspaceConfig{
		Name: "nexttest", Version: "1",
		Services: []workspace.Service{
			{Name: "frontend", Path: svc, Language: "javascript"},
			{Name: "client", Path: clientSvc, Language: "javascript"},
		},
	}
}

// openIndex runs the indexer and returns the resulting AdjacencyIndex.
func openIndex(t *testing.T, cfg *workspace.WorkspaceConfig, dbDir string, full bool) *graph.AdjacencyIndex {
	t.Helper()
	runIndexer(t, cfg, dbDir, full)
	store, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	idx, err := store.BuildIndex(context.Background())
	require.NoError(t, err)
	return idx
}

// TestSynthesizeFileRoutes_NextPages_And_NextApp exercises all M.0 table rows
// for Next.js (pages + app router) through the real indexer path (rule 6).
func TestSynthesizeFileRoutes_NextPages_And_NextApp(t *testing.T) {
	cfg := buildNextWorkspace(t)
	dbDir := t.TempDir()
	idx := openIndex(t, cfg, dbDir, true)

	// Collect synthesized nodes.
	routesByPath := map[string]*graph.Node{}
	handlersByKey := map[string]*graph.Node{} // "METHOD path"
	for _, n := range idx.Nodes {
		if n.Meta["defined_by"] != "file_convention" {
			continue
		}
		if n.Type == graph.NodeTypeRoute {
			routesByPath[n.Meta["path"]] = n
		}
		if n.Type == graph.NodeTypeHTTPHandler {
			key := n.Meta["method"] + " " + n.Meta["path"]
			handlersByKey[key] = n
		}
	}

	// next-pages: every table row.
	assert.Contains(t, routesByPath, "/", "next-pages index page")
	assert.Contains(t, routesByPath, "/about", "next-pages /about")
	assert.Contains(t, routesByPath, "/posts/:id", "next-pages dynamic /posts/:id")
	assert.Contains(t, routesByPath, "/*", "next-pages catch-all /*")

	// pages/api/users/[id].ts → method="" (ALL), path=/api/users/:id
	assert.Contains(t, handlersByKey, " /api/users/:id", "next-pages api handler (ALL, key=' /api/users/:id')")

	// next-app: route group stripped, per-verb handlers.
	assert.Contains(t, routesByPath, "/dashboard", "next-app page /dashboard")
	assert.Contains(t, routesByPath, "/pricing", "next-app route-group stripped to /pricing")
	assert.Contains(t, handlersByKey, "GET /api/users", "next-app GET handler")
	assert.Contains(t, handlersByKey, "POST /api/users", "next-app POST handler")

	// Fan-out (rule 1): two handlers from the same route.ts.
	assert.GreaterOrEqual(t, len(handlersByKey), 2, "fan-out: multiple handlers from one route.ts")

	// Acceptance: fetch("/api/users/42") → http_call → ALL /api/users/:id handler.
	allHandler, hasHandler := handlersByKey[" /api/users/:id"]
	if hasHandler {
		var found bool
		for _, e := range idx.AllEdges() {
			if e.Type == graph.EdgeTypeHTTPCall && e.To == allHandler.ID {
				found = true
				break
			}
		}
		assert.True(t, found,
			"acceptance: fetch('/api/users/42') must link to ALL /api/users/:id via http_call")
	} else {
		t.Log("SKIP acceptance assertion: ALL handler not synthesized (dep or root-dir detection issue)")
	}

	// component_impl edges must exist for every synthesized node (never dangling).
	for _, n := range handlersByKey {
		var hasImpl bool
		for _, e := range idx.OutEdges[n.ID] {
			if e.Type == graph.EdgeTypeComponentImpl && e.Meta["via"] == "file_convention" {
				hasImpl = true
				break
			}
		}
		assert.True(t, hasImpl, "handler %q must have a component_impl edge", n.ID)
	}

	// Ledger: parallel route and optional catch-all must not produce nodes.
	_, noParallel := routesByPath["@modal"]
	assert.False(t, noParallel, "parallel route @modal must not produce a route node")
	_, noOpt := routesByPath["[[...opt]]"]
	assert.False(t, noOpt, "optional catch-all must not produce a route node")

	// Ledger entries must exist for ledgered constructs.
	store, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	defer store.Close()
	unresolved, err := store.ListUnresolvedRefs(context.Background())
	require.NoError(t, err)
	var ledgerNames []string
	for _, u := range unresolved {
		if u.Kind == "route_convention_unresolved" {
			ledgerNames = append(ledgerNames, u.Name)
		}
	}
	hasParallelLedger := containsAnySubstr(ledgerNames, "@modal")
	hasOptLedger := containsAnySubstr(ledgerNames, "[[...opt]]")
	assert.True(t, hasParallelLedger, "parallel route must appear in unresolved ledger: %v", ledgerNames)
	assert.True(t, hasOptLedger, "optional catch-all must appear in unresolved ledger: %v", ledgerNames)
}

// TestSynthesizeFileRoutes_NegativeNoDep ensures a pages/ dir without the
// "next" dep produces zero file_convention nodes.
func TestSynthesizeFileRoutes_NegativeNoDep(t *testing.T) {
	dir := t.TempDir()
	svc := filepath.Join(dir, "web")
	require.NoError(t, os.MkdirAll(svc, 0o755))
	writeFile(t, svc, "package.json", `{"dependencies": {"react": "^18.0.0"}}`)
	mkdirWrite(t, svc, "pages/about.tsx", `export default function About() {}`)

	cfg := &workspace.WorkspaceConfig{
		Name: "nodeptest", Version: "1",
		Services: []workspace.Service{{Name: "web", Path: svc, Language: "javascript"}},
	}
	dbDir := t.TempDir()
	idx := openIndex(t, cfg, dbDir, true)
	for _, n := range idx.Nodes {
		assert.NotEqual(t, "file_convention", n.Meta["defined_by"],
			"no dep → no synthesized route nodes; got %+v", n)
	}
}

// TestSynthesizeFileRoutes_SvelteKit exercises SvelteKit page/handler synthesis.
func TestSynthesizeFileRoutes_SvelteKit(t *testing.T) {
	dir := t.TempDir()
	svc := filepath.Join(dir, "sk")
	require.NoError(t, os.MkdirAll(svc, 0o755))
	writeFile(t, svc, "package.json", `{"dependencies": {"@sveltejs/kit": "^2.0.0"}}`)
	mkdirWrite(t, svc, "src/routes/blog/[slug]/+page.svelte",
		`<script>export let data;</script><main>{data.title}</main>`)
	mkdirWrite(t, svc, "src/routes/api/items/+server.ts",
		`export async function GET({url}) { return new Response("ok") }`)

	cfg := &workspace.WorkspaceConfig{
		Name: "sktest", Version: "1",
		Services: []workspace.Service{{Name: "sk", Path: svc, Language: "javascript"}},
	}
	dbDir := t.TempDir()
	idx := openIndex(t, cfg, dbDir, true)

	var foundPage, foundHandler bool
	for _, n := range idx.Nodes {
		if n.Meta["defined_by"] != "file_convention" {
			continue
		}
		if n.Type == graph.NodeTypeRoute && n.Meta["path"] == "/blog/:slug" {
			foundPage = true
		}
		if n.Type == graph.NodeTypeHTTPHandler && n.Meta["path"] == "/api/items" &&
			n.Meta["method"] == "GET" {
			foundHandler = true
		}
	}
	assert.True(t, foundPage, "sveltekit: page /blog/:slug must be synthesized")
	assert.True(t, foundHandler, "sveltekit: GET /api/items must be synthesized")
}

// TestSynthesizeFileRoutes_Nuxt exercises Nuxt pages and server/api handlers.
func TestSynthesizeFileRoutes_Nuxt(t *testing.T) {
	dir := t.TempDir()
	svc := filepath.Join(dir, "nuxt")
	require.NoError(t, os.MkdirAll(svc, 0o755))
	writeFile(t, svc, "package.json", `{"dependencies": {"nuxt": "^3.0.0"}}`)
	mkdirWrite(t, svc, "pages/users/[id].vue",
		`<template><div>{{ user }}</div></template>`)
	mkdirWrite(t, svc, "server/api/items.get.ts",
		`export default defineEventHandler(() => [])`)
	mkdirWrite(t, svc, "server/api/config.ts",
		`export default defineEventHandler(() => ({}))`)

	cfg := &workspace.WorkspaceConfig{
		Name: "nuxttest", Version: "1",
		Services: []workspace.Service{{Name: "nuxt", Path: svc, Language: "javascript"}},
	}
	dbDir := t.TempDir()
	idx := openIndex(t, cfg, dbDir, true)

	var foundPage, foundGetHandler, foundAllHandler bool
	for _, n := range idx.Nodes {
		if n.Meta["defined_by"] != "file_convention" {
			continue
		}
		if n.Type == graph.NodeTypeRoute && n.Meta["path"] == "/users/:id" {
			foundPage = true
		}
		if n.Type == graph.NodeTypeHTTPHandler && n.Meta["path"] == "/api/items" &&
			n.Meta["method"] == "GET" {
			foundGetHandler = true
		}
		if n.Type == graph.NodeTypeHTTPHandler && n.Meta["path"] == "/api/config" &&
			n.Meta["method"] == "" {
			foundAllHandler = true
		}
	}
	assert.True(t, foundPage, "nuxt: page /users/:id must be synthesized")
	assert.True(t, foundGetHandler, "nuxt-server: GET /api/items must be synthesized")
	assert.True(t, foundAllHandler, "nuxt-server: ALL /api/config (method='') must be synthesized")
}

// TestSynthesizeFileRoutes_Remix exercises Remix dot-path and $param conventions.
func TestSynthesizeFileRoutes_Remix(t *testing.T) {
	dir := t.TempDir()
	svc := filepath.Join(dir, "remix")
	require.NoError(t, os.MkdirAll(svc, 0o755))
	writeFile(t, svc, "package.json",
		`{"dependencies": {"@remix-run/react": "^2.0.0"}}`)
	mkdirWrite(t, svc, "app/routes/_index.tsx", `export default function Index() {}`)
	mkdirWrite(t, svc, "app/routes/posts.$postId.tsx",
		`export default function Post() {}`)

	cfg := &workspace.WorkspaceConfig{
		Name: "remixtest", Version: "1",
		Services: []workspace.Service{{Name: "remix", Path: svc, Language: "javascript"}},
	}
	dbDir := t.TempDir()
	idx := openIndex(t, cfg, dbDir, true)

	var foundIndex, foundDynamic bool
	for _, n := range idx.Nodes {
		if n.Meta["defined_by"] != "file_convention" {
			continue
		}
		if n.Type == graph.NodeTypeRoute && n.Meta["path"] == "/" {
			foundIndex = true
		}
		if n.Type == graph.NodeTypeRoute && n.Meta["path"] == "/posts/:postId" {
			foundDynamic = true
		}
	}
	assert.True(t, foundIndex, "remix: root index / must be synthesized")
	assert.True(t, foundDynamic, "remix: /posts/:postId must be synthesized")
}

// TestSynthesizeFileRoutes_DynamicNormalization verifies [id] → :id output.
func TestSynthesizeFileRoutes_DynamicNormalization(t *testing.T) {
	dir := t.TempDir()
	svc := filepath.Join(dir, "web")
	require.NoError(t, os.MkdirAll(svc, 0o755))
	writeFile(t, svc, "package.json", `{"dependencies": {"next": "^14.0.0"}}`)
	mkdirWrite(t, svc, "pages/posts/[id].tsx", `export default function Post() {}`)

	cfg := &workspace.WorkspaceConfig{
		Name: "dyntest", Version: "1",
		Services: []workspace.Service{{Name: "web", Path: svc, Language: "javascript"}},
	}
	dbDir := t.TempDir()
	idx := openIndex(t, cfg, dbDir, true)

	for _, n := range idx.Nodes {
		if n.Meta["defined_by"] == "file_convention" && n.Type == graph.NodeTypeRoute {
			assert.Equal(t, "/posts/:id", n.Meta["path"],
				"[id] must normalize to :id, not raw [id]")
		}
	}
}

// TestSynthesizeFileRoutes_Determinism runs the full indexer twice on the same
// fixture and requires byte-identical file_convention node+edge output (rule 2).
func TestSynthesizeFileRoutes_Determinism(t *testing.T) {
	cfg := buildNextWorkspace(t)
	dbDir := t.TempDir()

	serialize := func() string {
		idx := openIndex(t, cfg, dbDir, true)
		type nodeKey struct{ ID, TypeStr, Path string }
		var nkeys []nodeKey
		conventionIDs := map[string]bool{}
		for id, n := range idx.Nodes {
			if n.Meta["defined_by"] == "file_convention" {
				conventionIDs[id] = true
				nkeys = append(nkeys, nodeKey{n.ID, string(n.Type), n.Meta["path"]})
			}
		}
		sort.Slice(nkeys, func(i, j int) bool { return nkeys[i].ID < nkeys[j].ID })

		type edgeKey struct{ From, To, TypeStr string }
		var ekeys []edgeKey
		for _, e := range idx.AllEdges() {
			if conventionIDs[e.From] || conventionIDs[e.To] {
				ekeys = append(ekeys, edgeKey{e.From, e.To, string(e.Type)})
			}
		}
		sort.Slice(ekeys, func(i, j int) bool {
			if ekeys[i].From != ekeys[j].From {
				return ekeys[i].From < ekeys[j].From
			}
			return ekeys[i].To < ekeys[j].To
		})
		b, _ := json.Marshal(map[string]interface{}{"nodes": nkeys, "edges": ekeys})
		return string(b)
	}

	first := serialize()
	second := serialize()
	assert.Equal(t, first, second,
		"two index runs must produce byte-identical file-route output (rule 2)")
}

// TestSynthesizeFileRoutes_FanOut verifies both next-pages and next-app activate
// when the "next" dep is present and both root dirs exist (rule 1).
func TestSynthesizeFileRoutes_FanOut(t *testing.T) {
	cfg := buildNextWorkspace(t) // fixture has both pages/ and app/
	dbDir := t.TempDir()
	idx := openIndex(t, cfg, dbDir, true)

	// Both conventions canonicalize to "next" in meta.framework.
	// We verify that route nodes from BOTH pages/ and app/ exist.
	var hasPagesRoute, hasAppRoute bool
	for _, n := range idx.Nodes {
		if n.Meta["defined_by"] != "file_convention" || n.Type != graph.NodeTypeRoute {
			continue
		}
		// pages/ convention routes include /about, /posts/:id, /* (catch-all).
		if n.Meta["path"] == "/about" || n.Meta["path"] == "/*" {
			hasPagesRoute = true
		}
		// app/ convention routes include /dashboard, /pricing.
		if n.Meta["path"] == "/dashboard" || n.Meta["path"] == "/pricing" {
			hasAppRoute = true
		}
	}
	assert.True(t, hasPagesRoute, "fan-out (rule 1): next-pages routes must be synthesized")
	assert.True(t, hasAppRoute, "fan-out (rule 1): next-app routes must be synthesized")
}

// ── helpers ───────────────────────────────────────────────────────────────────

// mkdirWrite creates parent dirs and writes content to the relative file path.
func mkdirWrite(t *testing.T, base, rel, content string) {
	t.Helper()
	abs := filepath.Join(base, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
	require.NoError(t, os.WriteFile(abs, []byte(content), 0o644))
}

// containsAnySubstr reports whether any string in ss contains sub.
func containsAnySubstr(ss []string, sub string) bool {
	for _, s := range ss {
		if len(s) >= len(sub) {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
