# Phase 6 — HTTP Server + Frontend

**Status**: 🟡 Partial  
**Depends on**: Phases 1–5 complete  
**Design ref**: [API Endpoints](../polyflow-design.md#api-endpoints), [Flow Tracing UX](../polyflow-design.md#flow-tracing-ux-core-feature), [SQLite Schema & Storage Design](../polyflow-design.md#sqlite-schema--storage-design)

---

## Goal

Complete the Go HTTP server and wire the SolidJS frontend so `polyflow serve` opens a working interactive graph in the browser. The skeleton exists (`server.go`, `handlers.go`, `cytoscape.go`, `web/`), but `handleTrace` returns 501 and the frontend has never been run against a real backend.

---

## Current State

**Backend** (`internal/server/`):
- `server.go`: `Server` struct, `New()`, `registerRoutes()`, `Start()` — complete but route paths are wrong (see gap #2 below)
- `handlers.go`: `handleSearch`, `handleNode`, `handleStats`, `handleEvents` — implemented. `handleTrace` → 501. `handleGraph` and `handleNodeSource` are missing entirely.
- `cytoscape.go`: `ToCytoscapeJSON(nodes, edges)` — implemented, untested against frontend

**Frontend** (`web/`):
- All component files exist: `App.tsx`, `Graph.tsx`, `Search.tsx`, `Detail.tsx`, `Filters.tsx`, `LayoutToggle.tsx`, `Notification.tsx`
- All stores exist: `graph.ts`, `search.ts`, `ui.ts`
- `web/package.json` exists but `npm install` has not been run
- Unknown whether stores call the correct API endpoints or handle the actual JSON shapes returned by the backend

---

## What Needs Building

### 1. Fix API Route Paths (`server.go`)

The design doc specifies `/api/graph/` prefixed paths. The current `registerRoutes()` uses `/api/search`, `/api/trace`, etc. — wrong. Update to match the spec:

```go
s.mux.HandleFunc("GET /api/graph",                s.handleGraph)
s.mux.HandleFunc("GET /api/graph/search",         s.handleSearch)
s.mux.HandleFunc("GET /api/graph/trace",          s.handleTrace)
s.mux.HandleFunc("GET /api/node/{id}",            s.handleNode)
s.mux.HandleFunc("GET /api/node/{id}/source",     s.handleNodeSource)
s.mux.HandleFunc("GET /api/stats",                s.handleStats)
s.mux.HandleFunc("GET /api/events",               s.handleEvents)
```

### 2. `GET /api/graph` — Full Graph (Paginated)

New handler `handleGraph` in `handlers.go`:

Query params: `?page=<n>&limit=<n>` (default limit 500, max 2000 — never dump the full graph in one shot).

Steps:
1. Load all nodes from `s.idx.Nodes` (in-memory)
2. Paginate
3. Load all edges where both endpoints are in the page's node set
4. Return `ToCytoscapeJSON(nodes, edges)`

This is what the frontend Graph component calls on initial load to render the overview.

### 3. `handleTrace` — BFS/DFS Subgraph Extraction

`GET /api/graph/trace?root=<id>&direction=<forward|backward|both>&depth=<n>`

Implementation in `handlers.go`:

1. Parse `root`, `direction`, `depth` query params. Default depth: 10. Max depth: 50.
2. Look up root node in `s.idx`. 404 if missing.
3. Call the appropriate `graph` traversal function:
   - `forward` → `graph.Descendants(idx, rootID, depth)`
   - `backward` → `graph.Ancestors(idx, rootID, depth)`
   - `both` → union of both
4. Collect all node IDs from traversal, fetch full `Node` objects from `s.idx.Nodes`
5. Collect all edges where both `From` and `To` are in the result node set
6. Return `ToCytoscapeJSON(nodes, edges)`

No DB hit during trace — everything runs against the in-memory `AdjacencyIndex`.

### 4. `GET /api/node/:id/source` — Full Function Source

New handler `handleNodeSource` in `handlers.go`:

Steps:
1. Look up node by `id` in `s.db` (needs `File` and `Line` from the node)
2. Read the source file from disk
3. Return the full function body as plain text (or JSON `{"source": "..."}`)
4. Respect `snippet_lines` from workspace settings for truncation threshold — but this endpoint always returns the full source (it's the "show more" endpoint)

### 5. Verify `ToCytoscapeJSON` Output Shape

The current `ToCytoscapeJSON` returns:

```json
{
  "nodes": [{"data": {"id": "...", "label": "...", "type": "...", "service": "...", "file": "...", "line": 0}}],
  "edges": [{"data": {"id": "...", "source": "...", "target": "...", "type": "...", "label": "..."}}]
}
```

Verify this matches what `Graph.tsx` feeds to `cytoscape()`. If the frontend expects a flat `elements` array instead of `{nodes, edges}`, adjust `cytoscape.go`. Cytoscape.js accepts both — prefer `{nodes, edges}` for clarity.

### 6. Frontend: Audit and Fix Stores

Run `npm install` in `web/`, then audit each store against the corrected API paths:

**`web/src/stores/search.ts`**:
- Must call `GET /api/graph/search?q=<query>&limit=20`
- Response is `[]*graph.Node` JSON array (not Cytoscape format)
- 200ms debounce on keypress

**`web/src/stores/graph.ts`**:
- Initial load: `GET /api/graph` → renders full overview
- On trace: `GET /api/graph/trace?root=<id>&direction=<forward|backward|both>&depth=<n>`
- Both return `CytoscapeGraph` format (`{nodes, edges}`)
- Wire to `cytoscape` instance in `Graph.tsx`

**`web/src/stores/ui.ts`**:
- Panel state (detail open/close), active layout, selected node ID

**`web/src/components/Detail.tsx`**:
- Calls `GET /api/node/{id}` when a node is selected
- Displays `node`, `edges_from`, `edges_to` from the response
- Shows file path, line number, language, service, type
- "Show more" button calls `GET /api/node/{id}/source` to load the full function body

**`web/src/components/Filters.tsx`**:
- Client-side filtering on current graph data (no new API call)
- Filter by: service name, node type, edge type

**`web/src/components/Notification.tsx`**:
- Connects to `GET /api/events` SSE stream
- Shows "Graph updated" toast when it receives a `graph_updated` event (triggered in Phase 7 by the atomic DB swap)

### 7. Frontend Build Integration

Verify `web/vite.config.ts` has `build.outDir` pointing to `web/dist/`. The Go server already serves `web/dist` via `http.FileServer`.

Add to `Makefile`:
```makefile
web:
	cd web && npm install && npm run build

build: web
	go build -o dist/polyflow ./cmd/polyflow
```

### 8. CORS Headers (Dev Mode Only)

Add to `server.go` for `vite dev` (frontend on port 5173, backend on 9400):
```go
w.Header().Set("Access-Control-Allow-Origin", "http://localhost:5173")
w.Header().Set("Access-Control-Allow-Methods", "GET")
```

Gate this behind a `devMode bool` field on `Server`, set only when `--dev` flag is passed to `polyflow serve`. Not active in production.

### 9. Handler Tests (`handlers_test.go`)

New file `internal/server/handlers_test.go` using `net/http/httptest`:

| Test | What it asserts |
|------|-----------------|
| `TestHandleGraph_OK` | Returns `{nodes, edges}` Cytoscape JSON with non-empty arrays |
| `TestHandleGraph_Pagination` | `?page=2&limit=10` returns second page |
| `TestHandleSearch_OK` | `GET /api/graph/search?q=create` returns 200 with JSON array |
| `TestHandleSearch_MissingQ` | Missing `q` param → 400 |
| `TestHandleNode_OK` | `GET /api/node/{id}` returns node + edges_from + edges_to |
| `TestHandleNode_NotFound` | Unknown ID → 404 |
| `TestHandleNodeSource_OK` | `GET /api/node/{id}/source` returns source text |
| `TestHandleTrace_Forward` | `direction=forward` returns only downstream nodes |
| `TestHandleTrace_Backward` | `direction=backward` returns upstream nodes |
| `TestHandleTrace_Both` | `direction=both` returns union |
| `TestHandleTrace_MissingRoot` | Missing `root` param → 400 |
| `TestHandleTrace_UnknownRoot` | Unknown root ID → 404 |
| `TestHandleStats_OK` | Returns `{"nodes": N, "edges": M}` |

Use a helper `buildTestServer(nodes, edges)` that creates an in-memory `SQLiteStore` + `AdjacencyIndex` and returns a configured `*Server`.

### 10. Cytoscape Tests (`cytoscape_test.go`)

New file `internal/server/cytoscape_test.go`:
- `ToCytoscapeJSON` with empty input returns `{nodes: [], edges: []}`
- Node fields map correctly to Cytoscape data fields
- Edge `From`/`To` map to Cytoscape `source`/`target`

---

## File Changes

| File | Action |
|------|--------|
| `internal/server/server.go` | Fix route paths to `/api/graph/search`, `/api/graph/trace`, add `/api/graph`, `/api/node/{id}/source`; add `devMode` CORS flag |
| `internal/server/handlers.go` | Implement `handleGraph`, `handleTrace`, `handleNodeSource` |
| `internal/server/handlers_test.go` | New — 13 handler tests |
| `internal/server/cytoscape_test.go` | New — 3 cytoscape shape tests |
| `web/src/stores/graph.ts` | Fix API paths, audit response handling |
| `web/src/stores/search.ts` | Fix API path, verify debounce |
| `web/src/stores/ui.ts` | Audit panel state |
| `web/src/components/Detail.tsx` | Fix API path, add "show more" source call |
| `web/src/components/Filters.tsx` | Audit client-side filtering |
| `web/src/components/Notification.tsx` | Verify SSE connection |
| `web/vite.config.ts` | Verify `build.outDir` |
| `Makefile` | Add `web` target, update `build` |

---

## Acceptance Criteria

- [ ] `GET /api/graph` returns paginated Cytoscape JSON
- [ ] `GET /api/graph/search?q=<query>` returns matching nodes
- [ ] `GET /api/graph/trace?root=<id>&direction=forward&depth=5` returns subgraph
- [ ] `GET /api/graph/trace` with missing `root` → 400; unknown root → 404
- [ ] `GET /api/node/{id}` returns node detail with incoming and outgoing edges
- [ ] `GET /api/node/{id}/source` returns full function source text
- [ ] `ToCytoscapeJSON` output shape matches what `cytoscape()` in `Graph.tsx` expects
- [ ] `npm run build` in `web/` produces `web/dist/index.html`
- [ ] `go test ./internal/server/...` passes with ≥ 90% coverage
- [ ] Opening `http://localhost:9400` renders the graph UI with nodes visible
- [ ] Search panel returns results as-you-type
- [ ] Clicking a node opens the detail panel with file/line/service
- [ ] "Show more" in detail panel loads the full function source
- [ ] SSE `/api/events` connection stays open; `Notification` renders without error

---

## What Is Explicitly Out of Scope

- `polyflow serve` CLI wiring (Phase 7)
- Atomic DB swap + SSE `graph_updated` push (Phase 7)
- URL state sync (`?root=...&direction=...` in the browser URL) — v2 polish
- Full layout switching (dagre vs fcose) — `LayoutToggle.tsx` stub is acceptable for v1
