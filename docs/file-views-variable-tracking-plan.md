# Polyflow: File-Grouped Views, Variable Tracking & Impact Search (Phases 13–17)

## Context

Polyflow's goal is to let AI agents find code and its impacted areas with minimal token spend, and help humans identify impact quickly. Today the graph models functions/methods/routes/etc. but has **no file grouping, no variable/struct/class nodes, and no variable-flow edges**. The UI renders a flat node graph (with boundary collapse for SDK call sites); search covers labels/files via FTS5; upstream/downstream tracing exists only at node granularity.

This work adds:
1. **File-grouped graph view** (service ▸ file ▸ nodes via Cytoscape compound nodes) as the **default** view, toggleable to the flat view.
2. **Variable tracking** — declarations, mutations, data types, closures, by-ref/by-value flow. Go deep via the existing SSA pipeline; JS/TS/Ruby structural via tree-sitter at lower confidence (user-confirmed scope).
3. **Flow-diagram (structure) view** — classes/structs/interfaces with fields, variables, functions.
4. **Search/trace by filename and variable**, upstream/downstream impact from any file or variable, copy-file-path in the UI.
5. **Agent-first API** — query layer + token-frugal JSON (CLI + REST) built before the UI layers on top.

## Core design decisions

- **No persisted `file` nodes.** File grouping is *derived* from the existing `Node.File` field: client-side compound parents in the UI (same pattern as `web/src/lib/boundary.ts`), and server-side aggregation for API/CLI. Justification: keeps traversal semantics clean (no double-counting through container nodes), zero node-ID/linker/incremental-cache disruption, and `nodes_fts` already indexes the `file` column. Only schema change: add `CREATE INDEX idx_nodes_file ON nodes(file)`.
- **Variables become nodes only when they matter for impact**: package/module-level variables & constants, closure-captured variables, struct/class fields that cross function boundaries. Purely-local variables stay out of the graph (recorded as counts in function-node meta) — this is the node-explosion guard.
- **Struct/class fields live in the struct node's meta** (`fields: [{name, type, tag}]`), not as separate nodes. Field-level edges can come later without schema change.
- **`SemanticResult` must carry Nodes.** Today it only has `Edges` (`internal/parser/parser.go:24`); the Go SSA variable pass produces nodes, so extend to `{Nodes, Edges, Warning}` and plumb through `internal/indexer/indexer.go` + `semantic_cache`.
- **Migration = schema-version bump → full reindex.** The DB is a disposable index (`.polyflow/graph.db`, atomic-swap on write); store a `schema_version` key in the existing `meta` table and force `--full` reindex on mismatch. No SQL migration machinery needed.

## Data model additions (`internal/graph/model.go`)

New NodeTypes:
```go
NodeTypeVariable NodeType = "variable" // pkg/module-level var, captured var; meta: data_type, scope, kind(var|const|signal), mutable
NodeTypeStruct   NodeType = "struct"   // Go struct; meta: fields JSON
NodeTypeClass    NodeType = "class"    // TS/JS/Ruby class; meta: fields/props JSON
```
New EdgeTypes:
```go
EdgeTypeDeclares EdgeType = "declares" // function/file scope → variable
EdgeTypeReads    EdgeType = "reads"    // function → variable
EdgeTypeWrites   EdgeType = "writes"   // function → variable (mutation); meta: op(assign|append|delete…)
EdgeTypeCaptures EdgeType = "captures" // closure → outer variable; meta: by(ref|value)
EdgeTypeFlowsTo  EdgeType = "flows_to" // arg→param / var→var; meta: mode(ref|value), data_type
EdgeTypeUsesType EdgeType = "uses_type"// function/variable → struct/class/interface
```
Variable node ID follows the existing hash scheme (`service:file:variable:name:line`) — fits without changes.

---

## Phase 13 — File-grouped view + file-level impact (agent API first)

**Backend**
- `internal/graph/store.go`: add `idx_nodes_file` index; add `NodesByFile(service, path)` and `ListFiles(q string)` (aggregate: path, service, node counts by type).
- `internal/graph/query.go`: add `FileImpact(idx, service, path, direction, depth)` — seeds traversal from all nodes in the file, returns results **grouped by file**: `[{file, service, nodes: n, viaEdges: [...types], depth: min}]`. Reuses `Traverse`.
- `internal/server/handlers.go` (+`server.go` routes), all additive:
  - `GET /api/files?q=<substr>&limit=` → `[{path, service, counts:{function:12,…}}]`
  - `GET /api/file?service=&path=` → file summary (contained nodes: id/type/label/line only — token-frugal)
  - `GET /api/file/impact?service=&path=&direction=up|down|both&depth=` → grouped-by-file impact above
- `internal/server/mermaid.go`: add `level=file` (subgraph per file inside service subgraphs).
- CLI (`cmd/polyflow/main.go`): `polyflow impact --file <path> [--json]`, `polyflow search --kind file`. Extend `impactOutput` additively.

**UI (`web/`)**
- New `web/src/lib/filegroup.ts` mirroring `boundary.ts`: synthesize compound parent nodes per `(service, file)` with `parent` set on children; compose with boundary collapse (boundary groups become children of their file parent when both active). Unit tests like `boundary.test.ts`.
- `stores/ui.ts`: `viewMode: "files" | "flat"` — **default `"files"`**, persisted in localStorage + URL param.
- `Toolbar.tsx`: view-mode toggle. `Graph.tsx`: render compound file nodes (label = basename, tooltip = full path), double-click to collapse/expand a file.
- `Detail.tsx` + node context: **Copy file path** button (`navigator.clipboard`); when a file group is selected show path, node counts, and **Upstream / Downstream** buttons hitting `/api/file/impact` and rendering the result as a trace.
- `Search.tsx`: file results (grouped header) — clicking focuses the file group.

**Verify**: Go unit tests for `FileImpact`/handlers (fixtures in `internal/server/testdata`); vitest for `filegroup.ts`; run `polyflow index && polyflow serve` on this repo's workspace, confirm default grouped view, copy-path, file upstream/downstream; `polyflow impact --file … --json` output inspected for token frugality.

---

## Phase 14 — Variable data model + Go deep extraction (SSA)

- Add NodeTypes/EdgeTypes above; schema-version bump in `meta` table; `internal/indexer/indexer.go` forces full reindex on version mismatch.
- Extend `SemanticResult{Nodes []graph.Node, ...}` and semantic-cache serialization (nodes+edges) in indexer.
- `internal/parser/go_semantic.go` (or new `go_variables.go` in same package, reusing the already-built SSA program — **no second SSA build**):
  - **Package-level vars/consts**: `pkg.Members` → `*ssa.Global`, `*ssa.NamedConst` → `variable` nodes; type from `go/types` (`obj.Type().String()`).
  - **Structs**: `*ssa.Type` members whose underlying is `*types.Struct` → `struct` nodes with fields (name/type/json tag) in meta; `uses_type` edges from functions whose signature mentions the type.
  - **Mutations**: walk instructions — `*ssa.Store` whose address chain resolves to a Global or FreeVar → `writes` edge from containing function; `*ssa.UnOp`(load) → `reads`.
  - **Closures**: `fn.FreeVars` → `captures` edges (Go closures capture by reference → `by:ref`).
  - **By-ref/by-value**: at call sites, arg type `*types.Pointer`/slice/map/chan → `flows_to` meta `mode:ref`, else `mode:value`; only emit `flows_to` when the value is a tracked variable node (explosion guard).
  - Local-only variable counts go into function node meta (`vars_local: n`).
- `internal/graph/store.go`: include variable labels in FTS (already covered — labels indexed).

**Verify**: new fixture service under `internal/parser/testdata` exercising globals, closures, pointer args, struct fields; `go_semantic_test.go` asserts exact node/edge multisets; coverage gate stays ≥90%.

---

## Phase 15 — JS/TS/Ruby structural variable extraction

- New tree-sitter extraction (in `internal/parser/javascript.go` / `ruby.go`, plus YAML where the pattern system fits, e.g. `patterns/typescript/`):
  - Module-scope `const/let/var` (JS/TS), constants + class ivars/attr_accessor (Ruby) → `variable` nodes; TS type annotations → `data_type` meta; `class` nodes with props/methods listed in meta.
  - Assignments/compound-assignments to tracked names → `writes` edges (confidence `inferred`); bare identifier references → `reads` (confidence `inferred`).
  - Closure heuristic: inner function referencing an outer-scope tracked name → `captures` (confidence `partial`; JS captures by reference semantics noted in meta).
  - By-ref/by-value heuristic: objects/arrays passed → `mode:ref`, primitives literal → `mode:value`, otherwise `unknown`.
- All structural variable edges carry confidence ≤ `inferred` so the existing confidence filter naturally separates them from Go's `static` semantic facts.

**Verify**: fixture dirs with `expected.json` per language (positive + negative, per the Phase-1 harness conventions).

---

## Phase 16 — Search & trace by variable (API/CLI/UI)

- `GET /api/graph/search?q=&kind=variable|file|function|struct|class|…` — additive `kind` filter (type filter on FTS results).
- `GET /api/node/{id}` already returns in/out edges — verify variable nodes flow through; add `GET /api/variable/{id}/flow` returning `{declaredIn, type, readers:[…], writers:[…], captures:[…], flows:[…]}` (compact: id/label/file/line per entry).
- CLI: `polyflow search --kind variable`, `polyflow trace --root <var-node-id>` (works via existing traversal once variables are nodes), `polyflow impact` accepts variable nodes; ensure `context`/`impact` JSON includes new types.
- UI: `Search.tsx` kind chips; `Detail.tsx` variable panel (type, mutators, readers, captured-by, with jump-to-node); `Filters.tsx` + `lib/styles.ts` + `Legend.tsx` styles for new node/edge types (variable = small hexagon, writes = red-ish edge, reads = dim, captures = dashed).

**Verify**: handler tests; e2e chain test tracing a Go global var → mutator fn → HTTP handler across the fixture workspace; manual UI pass.

---

## Phase 17 — Flow-diagram (structure) view + export

- `stores/ui.ts`: third view mode `"structure"`.
- New `web/src/lib/structure.ts`: builds a UML-ish projection — per file: struct/class/interface nodes rendered with multi-line labels (name + fields/types), variables, functions; edges limited to `declares/reads/writes/captures/flows_to/uses_type/calls`; layout `dagre-tb`.
- `Graph.tsx`/`Toolbar.tsx`: wire the mode; file grouping stays active as the container level.
- `internal/server/mermaid.go`: `level=structure` emitting a Mermaid `classDiagram` (structs/classes with fields) + flow edges; exposed in export dropdown (`lib/export.ts`).

**Verify**: mermaid_test.go golden files; vitest for `structure.ts`; visual pass on this repo.

---

## Risks & mitigations

| Risk | Mitigation |
|---|---|
| Node explosion from variables | Only globals/captured/struct-scoped vars become nodes; locals are counts in meta; per-service warning if variable nodes > threshold (log in index summary) |
| SSA cost increase | Reuse the single SSA build already done in `go_semantic.go`; results cached in `semantic_cache` keyed by service fingerprint (unchanged files → no re-run) |
| Incremental-cache staleness with new types | Schema-version bump forces one full reindex; per-file cache format unchanged (nodes/edges JSON already generic) |
| Breaking agent JSON consumers | All API/CLI changes additive (new endpoints, new optional `kind` param, new fields) |
| Compound-node + boundary-collapse interaction | `filegroup.ts` composes after boundary collapse (group nodes get file parents); covered by vitest |

## Execution order & verification

Phases land sequentially (13 → 17), each with its tests green and `make` coverage gate ≥90% before the next. End-to-end check after Phase 16: index this repo's own workspace, then confirm an agent flow — `polyflow search --kind variable <name>` → `polyflow impact --file <path> --json` → outputs are compact and correct; and the UI defaults to the file-grouped view with copy-path and upstream/downstream working.
