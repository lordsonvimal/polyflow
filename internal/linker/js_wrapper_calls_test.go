package linker_test

// AH-follow-up-adjacent regression: the JS fetchGraph found-not-fixed gap
// (Tier JH's Measured result) traced to LinkJSAPIWrapperCalls only knowing a
// function is a wrapper when its OWN body calls fetch/axios directly — a
// wrapper of a wrapper (`fetchWithTimeout` forwards to another in-service
// helper, which itself forwards to `fetch`) was invisible. These tests pin
// the transitive same-service discovery that closes that class of gap.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

func writeJSWrapperFixture(t *testing.T, files map[string]string) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	var paths []string
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return dir, paths
}

func parseJSWrapperFixture(t *testing.T, svc string, paths []string) []graph.Node {
	t.Helper()
	reg, err := patterns.DefaultRegistry("../../patterns")
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)
	var nodes []graph.Node
	for _, f := range paths {
		p := parser.ForFile(f)
		require.NotNil(t, p, "no parser for %s", f)
		ns, _, _, err := p.Parse(f, svc, m, nil)
		require.NoError(t, err)
		nodes = append(nodes, ns...)
	}
	return nodes
}

// TestLinkJSAPIWrapperCalls_TransitiveWrapper is the fetchGraph shape,
// simplified to a same-service two-hop chain: `outer` forwards its own `url`
// param to `inner`, and only `inner`'s body calls `fetch` directly. Before
// transitive discovery, `outer` was invisible to LinkJSAPIWrapperCalls
// because nothing in `outer`'s own body calls fetch/axios.
func TestLinkJSAPIWrapperCalls_TransitiveWrapper(t *testing.T) {
	t.Parallel()
	dir, paths := writeJSWrapperFixture(t, map[string]string{
		"client.ts": `function inner(url: string, init: RequestInit = {}) {
  return fetch(url, init);
}

function outer(url: string) {
  return inner(url, {});
}

export function fetchGraph(params: string) {
  const url = ` + "`/api/graph${params}`" + `;
  return outer(url);
}
`,
	})
	nodes := parseJSWrapperFixture(t, "svc", paths)
	serviceFiles := map[string][]string{"svc": paths}

	newNodes, _, _, _ := linker.LinkJSAPIWrapperCalls(nodes, serviceFiles)

	var sawOuterCallSite bool
	for _, n := range newNodes {
		if n.Meta["wrapper"] == "outer" {
			sawOuterCallSite = true
			assert.Equal(t, "url", n.Meta["url_expr"],
				"outer's call site should mint an http_client node from its own url argument")
		}
	}
	assert.True(t, sawOuterCallSite,
		"outer was not discovered as a transitive wrapper of inner, which itself wraps fetch")
	_ = dir
}

// TestLinkJSAPIWrapperCalls_DedupesProducerAliasDuplicate is RT.5: a wrapper
// called with a literal URL (`apiGet("/app/things")`) is captured both as a
// producer_alias_url_call (any `ident("literal")`) and, richer, as a wrapper
// call site. The producer_alias_url_call duplicate at the same call site must
// be returned for removal.
func TestLinkJSAPIWrapperCalls_DedupesProducerAliasDuplicate(t *testing.T) {
	t.Parallel()
	_, paths := writeJSWrapperFixture(t, map[string]string{
		"api.ts": `export function apiGet(path: string) {
  return fetch(path);
}

export function load() {
  return apiGet("/app/things");
}
`,
	})
	nodes := parseJSWrapperFixture(t, "svc", paths)
	serviceFiles := map[string][]string{"svc": paths}

	var aliasDupID string
	for _, n := range nodes {
		if n.Type == graph.NodeTypeHTTPClient && n.Meta["pattern"] == "producer_alias_url_call" {
			aliasDupID = n.ID
		}
	}
	require.NotEmpty(t, aliasDupID, "fixture should produce a producer_alias_url_call node for apiGet(\"/app/things\")")

	newNodes, _, _, removeIDs := linker.LinkJSAPIWrapperCalls(nodes, serviceFiles)

	var wrapperAtSameSite bool
	for _, n := range newNodes {
		if n.Meta["wrapper"] == "apiGet" && n.Meta["url"] == "/app/things" {
			wrapperAtSameSite = true
		}
	}
	require.True(t, wrapperAtSameSite, "apiGet call site should mint a wrapper http_client node")
	assert.True(t, removeIDs[aliasDupID], "producer_alias_url_call duplicate should be marked for removal")
}

// TestLinkJSAPIWrapperCalls_ForwardedParamResolvesToSoleLiteral is JP.2:
// `function loadFolders() { return list("/app/folders"); }` calls a wrapper
// (`list(url) { return apiGet(url); }`) whose own body forwards its `url`
// parameter. The wrapper call site's argument is the parameter, but the
// wrapper is called exactly once with a single literal — so the minted node
// should carry that literal, not fall to dynamic.
func TestLinkJSAPIWrapperCalls_ForwardedParamResolvesToSoleLiteral(t *testing.T) {
	t.Parallel()
	_, paths := writeJSWrapperFixture(t, map[string]string{
		"api.ts": `export function apiGet(path: string) {
  return fetch(path);
}

function list(url: string) {
  return apiGet(url);
}

export function loadFolders() {
  return list("/app/folders");
}
`,
	})
	nodes := parseJSWrapperFixture(t, "svc", paths)
	serviceFiles := map[string][]string{"svc": paths}

	newNodes, _, _, _ := linker.LinkJSAPIWrapperCalls(nodes, serviceFiles)

	var sawListSite bool
	for _, n := range newNodes {
		if n.Meta["wrapper"] == "list" {
			sawListSite = true
			assert.Equal(t, "/app/folders", n.Meta["url"],
				"list(url) call site should adopt loadFolders's sole literal argument")
			assert.Empty(t, n.Meta["key_dynamic"])
		}
	}
	assert.True(t, sawListSite, "list was not discovered as a transitive wrapper of apiGet")
}

// TestLinkJSAPIWrapperCalls_NestedClosureNotAttributed guards the "do not
// descend into a nested closure" rule: a callback defined INSIDE a wrapper
// candidate that itself forwards a parameter must not make the OUTER
// function a wrapper on the strength of an inner closure's own unrelated
// parameter.
func TestLinkJSAPIWrapperCalls_NestedClosureNotAttributed(t *testing.T) {
	t.Parallel()
	_, paths := writeJSWrapperFixture(t, map[string]string{
		"client.ts": `function inner(url: string) {
  return fetch(url);
}

function outer(unrelated: string) {
  const helper = (url: string) => inner(url);
  return helper(unrelated);
}
`,
	})
	nodes := parseJSWrapperFixture(t, "svc", paths)
	serviceFiles := map[string][]string{"svc": paths}

	newNodes, _, _, _ := linker.LinkJSAPIWrapperCalls(nodes, serviceFiles)

	for _, n := range newNodes {
		assert.NotEqual(t, "outer", n.Meta["wrapper"],
			"outer must not be attributed as a wrapper via its nested closure's own forwarding")
	}
}

// TestLinkJSAPIWrapperCalls_MethodFromWrapperName is JG.2: a wrapper whose
// name carries an HTTP verb (`apiPut`) stamps Meta["method"] on the minted
// http_client node, so the contract match keys on [method, path] instead of
// falling through http.yaml's method_fallback (which would let a
// `PUT .../unlock` client match a `GET /users/unlock` route).
func TestLinkJSAPIWrapperCalls_MethodFromWrapperName(t *testing.T) {
	t.Parallel()
	_, paths := writeJSWrapperFixture(t, map[string]string{
		"ApiServices.js": `export function apiPut(url, body) {
  return fetch(url, { method: "PUT", body });
}
export function apiGet(url) {
  return fetch(url);
}
`,
		"Container.jsx": `import { apiPut, apiGet } from "./ApiServices";

function unlock(baseUrl, id) {
  return apiPut(` + "`${baseUrl}/${id}/unlock`" + `);
}
function load(baseUrl) {
  return apiGet(` + "`${baseUrl}/list`" + `);
}
`,
	})
	nodes := parseJSWrapperFixture(t, "svc", paths)
	serviceFiles := map[string][]string{"svc": paths}

	newNodes, _, _, _ := linker.LinkJSAPIWrapperCalls(nodes, serviceFiles)

	var sawPut, sawGet bool
	for _, n := range newNodes {
		switch n.Meta["wrapper"] {
		case "apiPut":
			sawPut = true
			assert.Equal(t, "PUT", n.Meta["method"])
			// The body pins `{ method: "PUT" }` explicitly, so body detection
			// wins over the name-derived verb (both agree here).
			assert.Equal(t, "js_wrapper_body", n.Meta["method_resolved_via"])
		case "apiGet":
			sawGet = true
			assert.Equal(t, "GET", n.Meta["method"])
		}
	}
	assert.True(t, sawPut, "apiPut call site not minted")
	assert.True(t, sawGet, "apiGet call site not minted")
}

// TestLinkJSAPIWrapperCalls_MethodFromWrapperBody: a wrapper whose NAME carries
// no verb (`postSSEAndReload`, `runAction`) still gets Meta["method"] when its
// own body pins `fetch(url, { method: "POST" })` — and a wrapper-of-a-wrapper
// inherits it.
func TestLinkJSAPIWrapperCalls_MethodFromWrapperBody(t *testing.T) {
	t.Parallel()
	_, paths := writeJSWrapperFixture(t, map[string]string{
		"actions.js": `export function postSSEAndReload(url, body) {
  return fetch(url, { method: "POST", body });
}
export function runAction(url) {
  return postSSEAndReload(url, {});
}
`,
		"page.jsx": `import { postSSEAndReload, runAction } from "./actions";

function markActive(id) {
  return postSSEAndReload(` + "`/maple/app-configs/${id}/do-mark-active`" + `);
}
function restore(id) {
  return runAction(` + "`/maple/app-configs/${id}/do-restore`" + `);
}
`,
	})
	nodes := parseJSWrapperFixture(t, "svc", paths)
	serviceFiles := map[string][]string{"svc": paths}

	newNodes, _, _, _ := linker.LinkJSAPIWrapperCalls(nodes, serviceFiles)

	var sawDirect, sawTransitive bool
	for _, n := range newNodes {
		switch n.Meta["wrapper"] {
		case "postSSEAndReload":
			sawDirect = true
			assert.Equal(t, "POST", n.Meta["method"])
			assert.Equal(t, "js_wrapper_body", n.Meta["method_resolved_via"])
		case "runAction":
			sawTransitive = true
			assert.Equal(t, "POST", n.Meta["method"], "wrapper-of-a-wrapper inherits the inner verb")
		}
	}
	assert.True(t, sawDirect, "postSSEAndReload call site not minted")
	assert.True(t, sawTransitive, "runAction call site not minted")
}

// ── Tier UB.1 — object-literal transports, member-expression call sites ──────

// TestLinkJSAPIWrapperCalls_ObjectLiteralWrapper is UB.1's worked example. The
// transport is a property of a module-scope object literal and is called
// through a member expression, which is the shape Tier WB missed for two
// independent reasons: its wrapper facts only named functions bound to a
// declarator, and its call-site scan only accepted an identifier callee.
func TestLinkJSAPIWrapperCalls_ObjectLiteralWrapper(t *testing.T) {
	t.Parallel()
	_, paths := writeJSWrapperFixture(t, map[string]string{
		"netUtils.js": `const NetUtils = {
  getJson: async url => (await fetch(url, { credentials: "include" })).json(),
  postForm: async (url, body) => (await fetch(url, { method: "POST", body })).json(),
};

export async function closeIssue(formData) {
  return await NetUtils.postForm("/api/widgets/close", formData);
}

export async function loadWidget(url) {
  return await NetUtils.getJson(url);
}
`,
	})
	nodes := parseJSWrapperFixture(t, "orion", paths)
	serviceFiles := map[string][]string{"orion": paths}

	newNodes, _, ledger, _ := linker.LinkJSAPIWrapperCalls(nodes, serviceFiles)

	var closeSite *graph.Node
	for i := range newNodes {
		if newNodes[i].Meta["wrapper"] == "NetUtils.postForm" {
			closeSite = &newNodes[i]
		}
	}
	require.NotNil(t, closeSite, "NetUtils.postForm call site should mint an http_client node")
	assert.Equal(t, "/api/widgets/close", closeSite.Meta["url"])
	// The verb comes from the wrapper's own body (`method: "POST"`), not from
	// its name: "postForm" is not one of the name-derived verbs.
	assert.Equal(t, "POST", closeSite.Meta["method"])
	assert.Equal(t, "js_wrapper_body", closeSite.Meta["method_resolved_via"])

	// loadWidget forwards its own parameter — a second hop UB.1 does not take.
	// It is ledgered rather than silently counted as covered.
	var dynamicRow bool
	for _, r := range ledger {
		if r.Kind == "wrapper_url_dynamic" && r.Name == "NetUtils.getJson" {
			dynamicRow = true
		}
	}
	assert.True(t, dynamicRow, "unreadable wrapper URL should be ledgered as wrapper_url_dynamic")
}

// TestLinkJSAPIWrapperCalls_ObjectMethodDoesNotCollideWithFreeFunction is why
// the wrapper fact is keyed "Object.property" and not by the bare property
// name. Both wrappers here are called `postForm` and forward their URL at
// different positions; a bare-name key would give one of the two call sites the
// other's parameter index and mint a node claiming a request to a request body.
func TestLinkJSAPIWrapperCalls_ObjectMethodDoesNotCollideWithFreeFunction(t *testing.T) {
	t.Parallel()
	_, paths := writeJSWrapperFixture(t, map[string]string{
		"mixed.js": `function postForm(opts, url) {
  return fetch(url, { method: "POST" });
}

const NetUtils = {
  postForm: (url, body) => fetch(url, { method: "POST", body }),
};

export function a() {
  return postForm({}, "/api/free/fn");
}

export function b() {
  return NetUtils.postForm("/api/object/method", {});
}
`,
	})
	nodes := parseJSWrapperFixture(t, "orion", paths)
	serviceFiles := map[string][]string{"orion": paths}

	newNodes, _, _, _ := linker.LinkJSAPIWrapperCalls(nodes, serviceFiles)

	urls := map[string]string{}
	for _, n := range newNodes {
		urls[n.Meta["wrapper"]] = n.Meta["url"]
	}
	assert.Equal(t, "/api/free/fn", urls["postForm"], "free function forwards at index 1")
	assert.Equal(t, "/api/object/method", urls["NetUtils.postForm"], "object method forwards at index 0")
}

// TestLinkJSAPIWrapperCalls_UnknownObjectMintsNothing is the guard that keeps
// UB.1 from becoming a phantom-client factory. `.update(...)` on an imported
// third-party object looks exactly like a transport call at the syntax level;
// only the absence of a wrapper fact for that qualified name distinguishes it.
func TestLinkJSAPIWrapperCalls_UnknownObjectMintsNothing(t *testing.T) {
	t.Parallel()
	_, paths := writeJSWrapperFixture(t, map[string]string{
		"chart.js": `import Chart from "chart.js";

const NetUtils = {
  getJson: url => fetch(url),
};

export function render() {
  Chart.update("/not/a/request");
  return NetUtils.getJson("/api/real");
}
`,
	})
	nodes := parseJSWrapperFixture(t, "orion", paths)
	serviceFiles := map[string][]string{"orion": paths}

	newNodes, _, _, _ := linker.LinkJSAPIWrapperCalls(nodes, serviceFiles)

	for _, n := range newNodes {
		assert.NotEqual(t, "Chart.update", n.Meta["wrapper"],
			"a member call on an object with no wrapper fact must mint nothing")
	}
	var sawReal bool
	for _, n := range newNodes {
		if n.Meta["wrapper"] == "NetUtils.getJson" && n.Meta["url"] == "/api/real" {
			sawReal = true
		}
	}
	assert.True(t, sawReal, "the known wrapper in the same file should still resolve")
}
