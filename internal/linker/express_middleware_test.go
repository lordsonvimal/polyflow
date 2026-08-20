package linker_test

// Express middleware chain, through REAL parses (same discipline as
// rails_filters_test.go's bug-class #6 note): `app.use(authMiddleware)`
// becomes a call an agent can walk, from the setup function that registers
// it to every route sharing its receiver variable.

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

func writeExpressFixture(t *testing.T, files map[string]string) []string {
	t.Helper()
	dir := t.TempDir()
	var paths []string
	for name, content := range files {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
		paths = append(paths, p)
	}
	return paths
}

// parseExpressFixture returns both nodes and edges — unlike
// parseJSWrapperFixture (js_wrapper_calls_test.go), LinkExpressMiddleware
// needs the parser's own already-resolved `calls` edges (the same-file
// bare-identifier-passed-as-callback resolution, tagged `via: func_arg`) to
// find a middleware's target, so they can't be discarded here.
func parseExpressFixture(t *testing.T, svc string, paths []string) ([]graph.Node, []graph.Edge) {
	t.Helper()
	reg, err := patterns.DefaultRegistry("../../patterns")
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)
	var nodes []graph.Node
	var edges []graph.Edge
	for _, f := range paths {
		p := parser.ForFile(f)
		require.NotNil(t, p, "no parser for %s", f)
		ns, es, _, err := p.Parse(f, svc, m, nil)
		require.NoError(t, err)
		nodes = append(nodes, ns...)
		edges = append(edges, es...)
	}
	return nodes, edges
}

func handlerIDFor(t *testing.T, nodes []graph.Node, label string) string {
	t.Helper()
	for _, n := range nodes {
		if n.Type == graph.NodeTypeHTTPHandler && n.Label == label {
			return n.ID
		}
	}
	t.Fatalf("no http_handler node labelled %q in %v", label, nodes)
	return ""
}

func funcIDFor(t *testing.T, nodes []graph.Node, label string) string {
	t.Helper()
	for _, n := range nodes {
		if n.Type == graph.NodeTypeFunction && n.Label == label && n.Meta["pattern"] == "function_decl" {
			return n.ID
		}
	}
	t.Fatalf("no function_decl node labelled %q in %v", label, nodes)
	return ""
}

// TestLinkExpressMiddleware_BareReference is Express's most common idiom:
// the middleware is passed by bare reference (`app.use(authMiddleware)`,
// no invocation), not a factory call. The parser's own local call
// resolution already produces a `setup -[calls]-> authMiddleware` edge for
// this (tagged via:func_arg) — LinkExpressMiddleware must reuse it, not
// require an invocation shape.
func TestLinkExpressMiddleware_BareReference(t *testing.T) {
	t.Parallel()
	paths := writeExpressFixture(t, map[string]string{
		"server.js": `const express = require("express");
const app = express();

function authMiddleware(req, res, next) { next(); }
function getUser(req, res) {}

function setup() {
  app.use(authMiddleware);
  app.get("/users/:id", getUser);
}
`,
	})
	nodes, edges := parseExpressFixture(t, "svc", paths)

	newEdges, unresolved := linker.LinkExpressMiddleware(nodes, edges)
	require.Empty(t, unresolved, "authMiddleware should resolve via the parser's own func_arg calls edge")
	require.Len(t, newEdges, 1)

	handlerID := handlerIDFor(t, nodes, "get /users/:id")
	middlewareID := funcIDFor(t, nodes, "authMiddleware")

	e := newEdges[0]
	assert.Equal(t, handlerID, e.From, "handler-calls-guard modeling, not guard-calls-handler")
	assert.Equal(t, middlewareID, e.To)
	assert.Equal(t, graph.EdgeTypeCalls, e.Type)
	assert.Equal(t, "express_middleware_use", e.Meta["via"])
}

// TestLinkExpressMiddleware_ProtectsEveryHandlerOnReceiver: a single
// `.use(mw)` registration guards every handler sharing its receiver
// variable, not just the one right after it (mirrors Gin's scope, which
// is receiver-scoped rather than registration-order-scoped).
func TestLinkExpressMiddleware_ProtectsEveryHandlerOnReceiver(t *testing.T) {
	t.Parallel()
	paths := writeExpressFixture(t, map[string]string{
		"server.js": `const express = require("express");
const app = express();

function authMiddleware(req, res, next) { next(); }
function getUser(req, res) {}
function deleteUser(req, res) {}

function setup() {
  app.use(authMiddleware);
  app.get("/users/:id", getUser);
  app.delete("/users/:id", deleteUser);
}
`,
	})
	nodes, edges := parseExpressFixture(t, "svc", paths)

	newEdges, unresolved := linker.LinkExpressMiddleware(nodes, edges)
	require.Empty(t, unresolved)
	require.Len(t, newEdges, 2)

	froms := map[string]bool{}
	for _, e := range newEdges {
		froms[e.From] = true
	}
	assert.True(t, froms[handlerIDFor(t, nodes, "get /users/:id")])
	assert.True(t, froms[handlerIDFor(t, nodes, "delete /users/:id")])
}

// TestLinkExpressMiddleware_DifferentReceiverNotGuarded: middleware
// registered on `app` must not protect routes registered on an unrelated
// `router` variable in the same file — v1 has no Gin-style derived-receiver
// chain (see LinkExpressMiddleware's doc comment), so scope is strictly
// same-receiver.
func TestLinkExpressMiddleware_DifferentReceiverNotGuarded(t *testing.T) {
	t.Parallel()
	paths := writeExpressFixture(t, map[string]string{
		"server.js": `const express = require("express");
const app = express();
const router = express.Router();

function authMiddleware(req, res, next) { next(); }
function getUser(req, res) {}

function setup() {
  app.use(authMiddleware);
  router.get("/users/:id", getUser);
}
`,
	})
	nodes, edges := parseExpressFixture(t, "svc", paths)

	newEdges, _ := linker.LinkExpressMiddleware(nodes, edges)
	assert.Empty(t, newEdges, "router's handler shares no receiver with app.use(authMiddleware)")
}

// TestLinkExpressMiddleware_UnresolvedExternalMiddleware: a middleware from
// an external/unindexed package (`express.static(...)`, no local
// declaration to find) must fall through to the unresolved ledger, never a
// guessed edge — mirrors LinkGinMiddleware's `cors.New(...)` case. This is
// also the exact shape synergy's tether app uses today
// (apps/tether/server/src/index.ts:19), which has zero app.get/post routes
// in that file — so it additionally exercises the "no handlers in scope"
// no-op path, not just unresolved target.
func TestLinkExpressMiddleware_UnresolvedExternalMiddleware(t *testing.T) {
	t.Parallel()
	paths := writeExpressFixture(t, map[string]string{
		"server.js": `const express = require("express");
const app = express();

function setup() {
  app.use(express.static("/dist"));
}
`,
	})
	nodes, edges := parseExpressFixture(t, "svc", paths)

	newEdges, unresolved := linker.LinkExpressMiddleware(nodes, edges)
	assert.Empty(t, newEdges, "no handlers registered on app in this file — nothing to guard")
	assert.Empty(t, unresolved, "LinkGinMiddleware's own convention: skip silently when there's no handler in scope yet")
}
