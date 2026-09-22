package linker_test

// Tests for LinkJSCallTableLookups: a call to a helper whose whole body is
// `return TABLE[param]` resolves for a literal call-site argument, whether
// TABLE lives in the same file, one import hop away, or the helper itself is
// imported into the calling file.

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
)

func TestLinkJSCallTableLookups_ImportedHelperAndTable(t *testing.T) {
	t.Parallel()
	_, paths := writeJSWrapperFixture(t, map[string]string{
		"ClientRoutes.js": `export default {
  audit_log: "/collaborate?tab=audit",
  home: "/",
};
`,
		"routeMap.js": `import ClientRoutes from "./ClientRoutes";

export function generateURL(name) {
  return ClientRoutes[name];
}
`,
		"caller.jsx": `import { generateURL } from "./routeMap";

export function load() {
  return fetch(generateURL("audit_log"));
}
`,
	})
	nodes := parseJSWrapperFixture(t, "svc", paths)
	serviceFiles := map[string][]string{"svc": paths}

	var callerFile string
	for _, p := range paths {
		if filepath.Base(p) == "caller.jsx" {
			callerFile = p
		}
	}
	require.NotEmpty(t, callerFile)

	var before *graph.Node
	for i := range nodes {
		if nodes[i].Meta["key_dynamic"] == "true" {
			before = &nodes[i]
		}
	}
	require.NotNil(t, before, "fixture should produce a key_dynamic http call node before the pass runs")
	require.Equal(t, "", before.Meta["url"])

	patched := linker.LinkJSCallTableLookups(nodes, serviceFiles)
	require.Len(t, patched, 1)
	assert.Equal(t, "/collaborate?tab=audit", patched[0].Meta["url"])
	assert.Equal(t, "", patched[0].Meta["key_dynamic"])
	assert.Equal(t, "", patched[0].Meta["key_dynamic_raw"])
	assert.Equal(t, "js_call_table_lookup", patched[0].Meta["key_via"])
}

func TestLinkJSCallTableLookups_SameFileHelperAndTable(t *testing.T) {
	t.Parallel()
	_, paths := writeJSWrapperFixture(t, map[string]string{
		"local.js": `const ROUTES = {
  audit_log: "/collaborate?tab=audit",
};

function generateURL(name) {
  return ROUTES[name];
}

export function load() {
  return fetch(generateURL("audit_log"));
}
`,
	})
	nodes := parseJSWrapperFixture(t, "svc", paths)
	serviceFiles := map[string][]string{"svc": paths}

	patched := linker.LinkJSCallTableLookups(nodes, serviceFiles)
	require.Len(t, patched, 1)
	assert.Equal(t, "/collaborate?tab=audit", patched[0].Meta["url"])
}

func TestLinkJSCallTableLookups_UnknownKeyStaysDynamic(t *testing.T) {
	t.Parallel()
	_, paths := writeJSWrapperFixture(t, map[string]string{
		"local.js": `const ROUTES = {
  audit_log: "/collaborate?tab=audit",
};

function generateURL(name) {
  return ROUTES[name];
}

export function load() {
  return fetch(generateURL("no_such_route"));
}
`,
	})
	nodes := parseJSWrapperFixture(t, "svc", paths)
	serviceFiles := map[string][]string{"svc": paths}

	patched := linker.LinkJSCallTableLookups(nodes, serviceFiles)
	assert.Empty(t, patched, "a literal argument with no matching table entry must not be patched")
}

func TestLinkJSCallTableLookups_BranchingHelperStaysDynamic(t *testing.T) {
	t.Parallel()
	_, paths := writeJSWrapperFixture(t, map[string]string{
		"local.js": `const ROUTES = {
  audit_log: "/collaborate?tab=audit",
};

function generateURL(name) {
  if (name === "audit_log") {
    return ROUTES[name];
  }
  return "/";
}

export function load() {
  return fetch(generateURL("audit_log"));
}
`,
	})
	nodes := parseJSWrapperFixture(t, "svc", paths)
	serviceFiles := map[string][]string{"svc": paths}

	patched := linker.LinkJSCallTableLookups(nodes, serviceFiles)
	assert.Empty(t, patched, "a multi-statement helper body must not be inlined")
}
