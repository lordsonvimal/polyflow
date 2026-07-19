# Plan 9 — UI Backend Enablers (Tier U-B): data + APIs the new web UI needs

Status legend: `pending` · `in progress` · `done`

> **Prerequisites — execute after `docs/plan-8-multi-repo.md`** (assumes
> plans 1–8 are done; multi-repo workspaces and `~/` path expansion exist)
> **and before plans 10–13** (every UI plan consumes these endpoints).
> Plan-6's N.3 still runs strictly last of everything. Follows
> `docs/phases.md` (rules 1–12 binding, one phase per commit, outcome note
> in the same commit). Read ONLY this file plus `docs/phases.md` to
> implement any phase. **No `web/` changes in this plan.**

## Context

The web UI overhaul (plans 10–13) needs backend capabilities that do not
exist today:

- Nodes carry a single `Line int` — no end line anywhere (model, SQLite
  schema v17, API). Tree-sitter parsers have `EndPoint()` available and
  drop it.
- No hierarchy endpoint (the UI pages raw nodes, max 2000/page).
- MCP tool calls (`internal/mcpserver`, 4 tools) are logged nowhere.
- The server API is read-only: no way to trigger an index, edit
  workspace.yaml, or query flows/health/context.
- `graph.db` is rebuilt as `graph.db.tmp` + atomic rename on index
  (`internal/indexer/indexer.go` ~line 251), so anything that must survive
  a reindex (audit log, job history, saved views) needs a separate store.

Pinned existing interfaces this plan builds on (verify at implementation;
list any drift in the outcome note):

- `internal/server/server.go`: `type Server struct{ … broadcast chan string }`,
  `New(db graph.Store, idx *graph.AdjacencyIndex) *Server`, `Reload(idx)`,
  `registerRoutes()`, SSE fan-out of JSON strings via `broadcast`.
- `internal/indexer/indexer.go`: `Options.Progress func(done, total int)`.
- `internal/context/builder.go`: `Build(idx, targetID, task string, depth int,
  verboseSources bool) *Result`; `Result.InlineSnippets(root, lines)`,
  `Result.ApplyBudget(maxTokens, forceSummary)`.
- `internal/budget/budget.go`: `Snippet(root, file string, start, n int)`,
  `Estimate(v any) int`.
- `internal/trace/trace.go`: `Run(idx, rootID, direction string, depth int,
  verboseSources bool) *Result` (Result has `Nodes []Hop`, `Chains []Chain`).
- `internal/workspace/config.go`: strict `Load` / atomic `Save`.

JSON field names below are the contract and may not change; Go identifiers
may adapt to the codebase.

---

### Phase UB.0 — Line ranges: `Node.EndLine` end to end `pending`

**Problem.** Every node knows only its start line. The UI cannot show
`file:12–48`, cannot bound source display (today `/api/node/{id}/source`
returns the whole file), and context snippets guess with a fixed line
count.

**Deliverable.**
1. `internal/graph/model.go`: add `EndLine int \`json:"end_line,omitempty"\``
   to `Node`, directly after `Line`. Semantics: **last line of the node's
   full extent, 1-based, inclusive; `0` = unknown** (never guessed — trust
   contract). For single-point nodes (an HTTP call site, a variable read)
   `EndLine` is the end row of the capturing AST node, which may equal
   `Line`.
2. Bump `graph.SchemaVersion` `"17"` → `"18"` (forces full reindex,
   discarding stale incremental caches — rule from `docs/phases.md`).
3. `internal/graph/store.go`: `end_line INTEGER NOT NULL DEFAULT 0` column
   on `nodes` (both the CREATE TABLE and the `ALTER TABLE ADD COLUMN`
   migration path used since v15); read/write it in every node
   scan/upsert (grep for the column list; enumerate the touched
   statements in the outcome note).
4. Parsers: every site that mints a `graph.Node` sets `EndLine` from the
   defining AST node's end position. Add end-line helpers next to the
   existing start-line helpers (`tsLine`-style: `int(n.EndPoint().Row)+1`
   for tree-sitter). Sweep **all of `internal/parser/`** (Go, JS/TS/JSX,
   Ruby, Python, ERB, templ, …) and any linker that mints nodes
   (`internal/linker/`). Rule-12 accounting: after the sweep,
   `grep -rn "Line:" internal/parser internal/linker | grep -v EndLine`
   style audit — every node-minting site either sets `EndLine` or is
   listed in the outcome note with the reason (e.g. synthetic
   service/file nodes stay `0`).
5. API: `internal/server/cytoscape.go` node data gains `"end_line"`;
   `/api/node/{id}` inherits it from `graph.Node` JSON.
6. `GET /api/node/{id}/source?range=1` — when `range=1` and the node has
   `EndLine > 0`, return only lines `Line..EndLine` plus `context` lines
   (query param, default 5) each side. Response JSON:
   `{"file": "...", "start": 12, "end": 48, "context": 5, "first_line": 7,
   "lines": ["…"]}`. Without `range=1` behavior is unchanged (whole
   file) — existing consumers keep working.

**Worked example.** A Go handler spanning lines 12–48: node JSON gains
`"end_line": 48`; `GET /api/node/<id>/source?range=1&context=3` returns
`first_line: 9` and 43 lines.

**Tests.**
- Per-language parser test: fixture function/class with a known extent →
  `EndLine` asserted exactly (Go, TS, Ruby, Python minimum).
- Store round-trip: upsert node with `EndLine`, read back.
- Handler test: `?range=1` bounds; `?range=1` on an `EndLine==0` node
  falls back to whole file with `"end": 0` (honest unknown, not a guess).
- Two-run determinism on a full fixture index (rule 2).

**Acceptance.** Full reindex of chessleap (`~/projects/chessleap`)
succeeds; spot-check 3 hand-verified functions' ranges; `BenchmarkIndexCold`
holds; eval gate green.

### Phase UB.1 — Hierarchy tree endpoint `pending`

**Problem.** The UI needs the Folder → File → Class → Function/Method
outline without paging thousands of raw nodes. The graph has `contains`
edges (service→file→declaration, struct→method) but **no folder level**.
Decision (pinned): folders are **derived server-side from file paths at
query time**, not stored — they carry no code semantics, and deriving
avoids a schema bump and node inflation.

**Deliverable.** `GET /api/tree?service=<name>` (service required; one
service per call — the UI fetches lazily per service). Response:

```json
{"service": "nextgen",
 "tree": [
   {"kind": "folder", "name": "app", "path": "app", "children": [
     {"kind": "folder", "name": "jobs", "path": "app/jobs", "children": [
       {"kind": "file", "name": "sync.rb", "path": "app/jobs/sync.rb",
        "node_id": "<file-node-id>", "children": [
          {"kind": "class", "name": "SyncJob", "node_id": "<id>",
           "line": 3, "end_line": 40, "children": [
             {"kind": "method", "name": "perform", "node_id": "<id>",
              "line": 5, "end_line": 22, "children": []}]}]}]}]}],
 "counts": {"folders": 2, "files": 1, "symbols": 2}}
```

- `kind` ∈ `folder|file|class|struct|function|method|component|variable`
  (mapped from `graph.NodeType`; the mapping table lives in the handler
  and is exhaustive over declaration types — rule 12: any `contains`-child
  type not mapped is included with its raw type string, never dropped).
- Built from the `AdjacencyIndex` `contains` edges + path splitting;
  sorted: folders first, then files, then symbols by `line` (rule 2
  determinism).
- Symbols nested per existing `contains` topology (struct→method already
  exists); symbols with no `contains` parent file attach under their
  `file` path (surfaced, not dropped).

**Tests.** Handler test on a fixture index with nested dirs, a struct
with methods, and one orphan symbol (no contains parent) → exact JSON;
two-run determinism; unknown service → 404 with error body.

**Acceptance.** `curl /api/tree?service=<chessleap-svc>` returns the full
outline in <100 ms on chessleap.

### Phase UB.2 — Ops store + tool-call audit log `pending`

**Problem.** Agent (MCP) and HTTP API calls are undebuggable — logged
nowhere. The audit must survive reindex, so it cannot live in `graph.db`.

**Deliverable.**
1. `internal/ops/store.go` — `ops.db` SQLite next to `graph.db`
   (same `meta.DBDir`), WAL mode, own schema version in a `meta` table.
   Never touched by the indexer.
2. Table:
   ```sql
   CREATE TABLE tool_calls (
     id INTEGER PRIMARY KEY AUTOINCREMENT,
     ts TEXT NOT NULL,               -- RFC3339Nano UTC
     source TEXT NOT NULL,           -- 'mcp' | 'http'
     tool TEXT NOT NULL,             -- MCP tool name or HTTP route pattern
     params TEXT NOT NULL,           -- JSON
     duration_ms INTEGER NOT NULL,
     status TEXT NOT NULL,           -- 'ok' | 'error'
     error TEXT NOT NULL DEFAULT '',
     result_bytes INTEGER NOT NULL
   );
   CREATE INDEX idx_tool_calls_ts ON tool_calls(ts);
   ```
3. Recording middleware: (a) `internal/mcpserver` wraps all 4 tool
   handlers; (b) `internal/server` wraps `/api/*` handlers (static SPA
   and `/api/events` excluded). Failures to record are logged to stderr
   and never fail the call.
4. API:
   - `GET /api/toolcalls?source=&tool=&status=&q=&since=&page=&limit=` —
     newest first; `q` substring-matches params/error; `limit` default
     100, max 1000; response `{"calls": […], "total": N, "page": 1}`.
   - `DELETE /api/toolcalls` — clears all rows, returns
     `{"deleted": N}` (the UI's "clear all logs").
   - SSE event on `broadcast`: `{"type":"tool_call","call":{…}}` per
     recorded call (live tail).
5. `polyflow serve` and `polyflow mcp` both open the ops store
   (create-if-missing); concurrent access is safe (WAL + busy timeout).

**Tests.** Middleware records ok + error calls with real handlers;
filters/pagination exact; DELETE clears and returns count; a call is
still served when ops.db is unwritable (record-failure tolerance);
reindex (delete + recreate graph.db in test) leaves tool_calls intact.

**Acceptance.** Run `polyflow mcp` + one agent `search` call → row
visible via `curl /api/toolcalls`; survives a `polyflow index` run.

### Phase UB.3 — Jobs API: index and friends from the UI `pending`

**Problem.** The UI cannot trigger indexing; long operations need
progress, cancellation, and history — never a blocked request.

**Deliverable.**
1. `POST /api/jobs` body `{"kind": "index", "args": {"full": false}}` —
   kinds: `index` (args: `full`), `eval` (args: `corpus`, `case`;
   requires `eval/` to exist — absence → 422 naming the path, honest,
   not a crash), `reconcile` (args: `propose_dir` optional). Each kind
   wraps the same internals its CLI command uses (`indexer.Run`, the
   eval runner, the reconcile report) — **one engine, two surfaces**;
   an unknown kind → 400 naming the supported enum (rule 3). Response
   `202 {"job": {…}}`. **Single-flight per kind**: a second POST of a
   running kind → `409` with the running job; different kinds may run
   concurrently except `index`, which excludes all others (it swaps
   the store). Non-index jobs put their JSON result in a `result`
   field on the job record.
2. Job record (also a row in ops.db `jobs` table, same field names):
   `{"id": "j-<ulid>", "kind": "index", "state":
   "running|succeeded|failed|canceled", "started_at": "...",
   "ended_at": "...", "progress": {"done": 120, "total": 689},
   "error": "", "log_tail": ["…last 50 lines…"]}`.
3. Progress: the job runner passes `Options.Progress` into
   `indexer.Run`; every update (throttled to ≥100 ms apart) broadcasts
   SSE `{"type":"job_progress","job":{…}}`; terminal states broadcast
   `{"type":"job_done","job":{…}}` and, on success, trigger the existing
   graph reload path (`Server.Reload`) — the fsnotify watcher already
   handles the db swap; verify no double-reload (note the resolution).
4. `GET /api/jobs?limit=` (history, newest first) ·
   `GET /api/jobs/{id}` · `DELETE /api/jobs/{id}` — cancel via the job's
   `context.Context`; canceled indexing must leave the previous
   `graph.db` intact (the tmp-db + atomic-rename design already
   guarantees this — assert it in tests).
5. The indexer runs with `logw` captured into the job's `log_tail`
   ring buffer (last 200 lines kept, `log_tail` returns last 50).

**Tests.** Full job lifecycle against a small fixture workspace (202 →
progress events observed on SSE → done → graph actually reloaded);
409 single-flight; cancel mid-run → state `canceled`, old graph.db
intact and still served; failed index (broken workspace) → state
`failed` with error surfaced verbatim; job rows persisted in ops.db.

**Acceptance.** From `curl`: start an index of chessleap, watch progress
over `/api/events`, cancel one run, complete another.

### Phase UB.4 — Config API: read/write workspace.yaml `pending`

**Problem.** The UI must view and edit workspace.yaml safely
(concurrent edits, validation) — today config is CLI-only.

**Deliverable.**
1. `GET /api/config` → `{"path": "<abs path>", "raw": "<yaml text>",
   "parsed": {…WorkspaceConfig JSON…}, "etag": "<sha256 of raw>"}`.
2. `PUT /api/config` body `{"raw": "<yaml>", "etag": "<from GET>"}`:
   - etag mismatch with current file content → `409`
     `{"error": "config changed on disk", "current_etag": "…"}`;
   - validate by writing to a temp file and running strict
     `workspace.Load` on it (the loader rejects unknown fields; path
     validation from plan-8 Z.0 applies) → `422` with the loader error
     verbatim on failure;
   - on success: atomic write via the existing `Save` path semantics
     (write temp + rename), respond `{"etag": "<new>", "ok": true}`.
3. No partial/form-level endpoints — the UI's form mode edits the parsed
   structure client-side and PUTs full raw YAML (one write path, one
   validator; pinned decision).
4. **Comments survive**: because PUT takes raw YAML, the form mode must
   round-trip unknown-to-the-form content; the server's only job is
   validate-then-write. Document this contract in the handler comment.

**Tests.** GET/PUT round-trip; stale etag → 409; invalid YAML / unknown
field / nonexistent service path → 422 naming the field; concurrent PUT
race (two PUTs, second gets 409); file on disk edited externally between
GET and PUT → 409.

**Acceptance.** Edit chessleap's workspace.yaml via `curl` PUT, then
`polyflow index` uses the change.

### Phase UB.5 — Flow, seam, health, and stack query endpoints `pending`

**Problem.** Flow isolation, the entrypoint catalog, seam isolation,
coverage/health, and the tech-stack view all need server-side queries
over the in-memory `AdjacencyIndex` (the graph never ships wholesale to
the client).

**Deliverable.** Six endpoints (all GET, all deterministic — rule 2:
sort every list by a stable key):

1. `/api/flows/entrypoints?service=&kind=` — every entrypoint node:
   HTTP handlers/routes, subscribers, workers, plus functions with
   `meta.root_kind == "entrypoint"`. Item: `{"node_id", "kind", "label",
   "service", "file", "line", "end_line", "channel": "<method+path or
   queue/topic, when derivable from the node/edge meta>"}`.
   Rule-12 accounting: response includes `"skipped": [{"type": "...",
   "count": N}]` for root-kind values not shown (`callback`,
   `unreachable`) so the denominator is honest.
2. `/api/flows/through/{id}?limit=` — flows passing through a node:
   run `trace.Run(idx, id, "backward", depth)` to find entrypoint roots,
   then for each root the forward chain through `{id}` (reuse
   `trace.Run` chains; do not reimplement traversal). Response groups by
   entrypoint: `{"flows": [{"entrypoint": {…node…}, "chain": [{"node_id",
   "label", "service", "edge_type", "edge_label", "cross_service",
   "confidence", "verification_state"}…]}], "truncated": false}`.
3. `/api/flows/paths?from=&to=&k=&max_depth=` — up to `k` (default 5,
   max 20) shortest paths `from`→`to` over directed flow edges
   (excluding `contains`), BFS with path copy, ranked by length then
   lexical edge-id sequence (determinism). Same chain item shape as (2).
   No path → `{"paths": [], "reachable": false}` (honest, not 404).
4. `/api/flows/refine?waypoints=<id,id,…>&direction=forward` — waypoint
   flow builder: validates consecutive waypoints are connected (via (3)
   with k=1 between each pair), returns the stitched chain plus
   **candidate next waypoints**: `{"chain": […], "candidates":
   {"upstream": [{…node, "via_edge_type"}…], "downstream": […]}}` —
   the immediate flow-edge neighbors of the current endpoints.
5. `/api/seam/{edge-id}` — seam isolation: for the edge's channel
   (`Meta["channel_key"]` when present, else the edge itself), return
   ALL producers and consumers sharing that channel (rule 1: fan-out,
   never first-match) plus each side's chain to its entrypoint/terminus:
   `{"channel": "...", "verification_state": "...", "producers":
   [{"node": {…}, "chain": […]}], "consumers": […]}`.
6. `/api/stack` + `/api/health` + `/api/unresolved`:
   - `/api/stack`: per service `{"name", "language", "frameworks",
     "deps": [{"name", "version", "ecosystem"}…], "node_counts":
     {type: N}, "edge_counts": {type: N}, "files": N}` (from the
     dependencies table + index).
   - `/api/health`: `{"index": {"indexed_at", "schema_version", "nodes",
     "edges", "parse_errors": N}, "coverage": {"verified": N,
     "candidate": N, "observed_only_gap": N, "conflicting": N},
     "unresolved_total": N, "eval": {"present": bool, "repos":
     [{"name", "recall"}]}}` — eval section reads `eval/baseline.json`
     when present, `"present": false` otherwise (absence surfaced, rule 4).
   - `/api/unresolved?service=&kind=&q=&page=&limit=` — the ledger
     (`unresolved_refs` table), same pagination contract as
     `/api/toolcalls`.

**Worked example (seam).** nextGen publishes to RabbitMQ queue
`cdr_requests`; CDR-Agent consumes it. `GET /api/seam/<publish-edge-id>`
returns one producer chain (controller → publisher call site) and one
consumer chain (consumer class → its handler method), channel
`rabbitmq:cdr_requests`. With two consumers on the queue, **both**
appear (rule-1 test case).

**Tests.** Fixture graph with: 2 services, a shared channel with 2
consumers (rule 1), a diamond for k-paths, an unreachable pair
(`reachable: false`), waypoint refine happy + disconnected-waypoint
error; two-run determinism on every endpoint; entrypoint accounting
includes `skipped` counts.

**Acceptance.** On chessleap: entrypoint catalog lists the known routes;
one seam query returns fan-out matching a hand-verified case from
`eval/corpus/chessleap/manifest.yaml`.

### Phase UB.6 — Context bundle endpoint `pending`

**Problem.** The UI's "Copy context" (every node/edge/flow/group/scope)
must produce LLM-ready markdown — the tool's core purpose, human-driven.

**Deliverable.** `POST /api/context/bundle`:

```json
{"elements": [{"kind": "node|edge|flow|group", "ids": ["…"]}],
 "mode": "viewed",            // "viewed" | "expanded"
 "depth": 3,                   // expanded mode only
 "snippets": true,
 "max_tokens": 8000}
```

Response: `{"markdown": "…", "tokens_estimate": 5200, "truncated":
false, "omitted": []}`.

- **Built on the existing engine, not reimplemented**: node context via
  `context.Build` (+ `InlineSnippets` — snippets bounded by UB.0 ranges:
  when `EndLine > 0` snippet the exact extent, else the existing
  fixed-line fallback via `budget.Snippet`); flows via the chain shape;
  edges include channel key, both endpoints with ranges,
  `verification_state`, and evidence `sources`.
- `mode: "viewed"`: exactly the given ids. `mode: "expanded"`: each
  element's closure (node → `context.Build` at `depth`; edge → its
  UB.5 seam; flow → its chains) — the union, deduplicated,
  deterministic order (rule 2).
- Markdown layout (pinned, top to bottom): `# Context: <summary line>` ·
  per service `## <service>` · per file `### <path> (<lang>)` · per
  element label + `file:START–END` + role line + snippet fence ·
  `## Flow` hop list for flows (`A —http_call→ B` lines with channel +
  verification) · `## Unresolved` for any ledgered gaps touching the
  elements (never dropped) · footer `_polyflow context bundle,
  <node-count> nodes, ~<tokens> tokens_`.
- Over budget: trim snippets first (`budget.TrimToFit` precedent), then
  whole elements smallest-value-last; everything omitted is named in
  `omitted` and in a visible markdown footer line `> Truncated at N
  tokens: omitted X, Y` (honest truncation, rule 12 corollary).

**Tests.** Node bundle exact-markdown golden test (fixture repo); flow
bundle includes hops + both services; over-budget bundle lists omissions
in both `omitted` and the markdown; `snippets: false` omits fences;
unknown id → error naming it (never silently skipped); two-run
determinism (byte-identical markdown).

**Acceptance.** Bundle a chessleap route node with snippets: paste-ready
markdown matches the file content ranges by hand-check.

### Phase UB.7 — Runtime capture & flows API `pending`

**Problem.** Runtime capture (`polyflow capture start|stop|run`,
`ingest`, `flows`) is CLI-only; the UI must start/stop capture, import
dumps, inspect observed flows, and fuse the evidence into the graph —
with **CLI and UI as two surfaces over the same session store**, so a
capture started in either is visible and stoppable from the other.

**Deliverable.**
1. `internal/capture`'s session lifecycle refactored to a shared
   package API (the CLI subcommands already own this logic — extract,
   don't duplicate; list the moved functions in the outcome note).
   Sessions live where the CLI puts them today (disk session store),
   so CLI-started sessions appear in the API and vice versa.
2. Endpoints:
   - `POST /api/capture/start` `{"session": "s1", "http_port": 4318,
     "grpc_port": 4317}` → 202 with status; port-in-use → 409 naming
     the port. `POST /api/capture/stop` `{"session": "s1"}`.
   - `GET /api/capture/status` → `{"active": [{"session", "since",
     "spans_received", "http_port", "grpc_port"}], "sessions":
     [{"session", "spans", "created_at"}]}` — active receivers +
     on-disk sessions. SSE `{"type":"capture_progress"}` with the span
     counter (throttled ≥1 s).
   - `POST /api/capture/ingest` (multipart OTLP dump upload or
     `{"path": "..."}` for a local file) into a named session —
     wraps `polyflow ingest`.
   - `GET /api/runtime/flows?session=` → the `FlowRecord` list
     (kind/key/from_service/to_service/causality/refs) plus the
     ingest ledger (unmapped spans with reasons — rule 12: the ledger
     ships with the flows, never separately optional).
   - `GET /api/runtime/coverage?session=` → the `flows --coverage`
     comparison against the static baseline (verified channels,
     observed-only channels, static-only channels).
3. **Graph update semantics (pinned, both surfaces):** runtime
   evidence fuses into edges (`Sources[]` runtime entries,
   `verification_state` transitions) **at index time** via the F.0
   reconciler. Therefore stop/ingest responses carry
   `"fusion_hint": "run index to fuse this evidence into the graph"`,
   and the UI (UO.6) offers that as a one-click follow-up job. A
   capture or ingest done via CLI converges identically: the next
   index (CLI or UI job) fuses it, and the existing `graph_updated`
   SSE tells every open UI. Observed flows with no static edge do
   **not** mint edges — they surface as `observed_only_gap` in
   `/api/health` coverage and in `/api/runtime/coverage` (trust
   contract: runtime confirms or exposes gaps, never fabricates
   structure).

**Tests.** Start/stop lifecycle with a fake OTLP POST (span counter
increments, status reflects); port conflict 409; ingest of the
existing `testdata/evidence/runtime/*.otlp.json` fixtures → flows +
ledger exact (incl. the unmapped-span ledger entries); coverage
endpoint against a fixture baseline; CLI-started session visible via
API (start via the extracted package as the CLI does, then GET);
two-run determinism on flows/coverage output.

**Acceptance.** Start capture via `curl`, run an instrumented
datascience service (`~/Projects/datascience` is OTel-instrumented)
against it, stop, ingest→index, and watch a candidate edge turn
verified in `/api/node/{id}`.

---

## Key files

- Modified: `internal/graph/model.go` + `store.go` (UB.0),
  `internal/parser/*` + `internal/linker/*` (UB.0),
  `internal/server/{server.go,handlers.go,cytoscape.go}` (all phases),
  `internal/mcpserver/mcpserver.go` (UB.2), `cmd/polyflow/main.go`
  (serve wiring for ops store + jobs).
- New: `internal/ops/` (store, UB.2/UB.3), `internal/server/tree.go`
  (UB.1), `internal/server/{toolcalls,jobs,config,flowsapi,bundle,capture}.go`
  (one file per phase, mirroring handlers.go style); UB.7 additionally
  extracts the capture session lifecycle from `cmd/polyflow/capture.go`
  into a shared package.

## Traceability

| Phase | Closes |
|---|---|
| UB.0 | problem 6 (line ranges) — data layer |
| UB.1 | problem 2 (hierarchy) — data layer |
| UB.2 | problem 10 (tool-call debugging incl. clear-all) — data layer |
| UB.3 | problem 4 (tool operations from UI) — data layer |
| UB.4 | problem 11 (view/update config) — data layer |
| UB.5 | problems 5, 7, 9 + seam isolation, waypoint flows, health dashboard — data layer |
| UB.6 | universal context copy — data layer |
| UB.7 | runtime capture/ingest/flows from UI; CLI↔UI parity on one session store — data layer |

## Developer use-case sweep (this tier answers via API)

"What are this function's exact lines?" → UB.0. "What's in this repo?" →
UB.1/`/api/stack`. "What did the agent ask the tool, and how long did it
take?" → UB.2. "Reindex without leaving the browser?" → UB.3. "Why is
this edge missing?" → `/api/unresolved` + seam verification states
(UB.5). "What do I paste to an LLM?" → UB.6. "Capture runtime traffic and fuse
it into the graph, from either surface" → UB.7 (+ UB.3 index job).
Declared non-goals: auth/multi-user (local single-user tool), config
form-level server endpoints (single PUT path).

## Explicit non-goals

- No `web/` changes (plans 10–13).
- No remote access hardening: `serve` stays localhost-default.
- No streaming/chunked bundle responses (bundles are budget-bounded).

## Verification

Per-phase: handler tests (httptest against a fixture index),
positive+negative, two-run determinism where a set is produced; full
`go test ./...` + eval gate green before each commit; UB.0 additionally:
full chessleap reindex + `BenchmarkIndexCold` hold recorded in the
outcome note. Rule 10 applies to UB.3 (job writes cross memory/store
boundaries): acceptance runs end-to-end on the real repo, not only
fixtures.
