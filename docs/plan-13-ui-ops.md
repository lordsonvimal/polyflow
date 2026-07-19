# Plan 13 — UI Ops & Observability (Tier U-O): jobs, tool-call log, config, health, docs, share

Status legend: `pending` · `in progress` · `done`

> **Prerequisites — execute after `docs/plan-12-ui-flows.md`** and
> plan-9's UB.2 (audit), UB.3 (jobs), UB.4 (config), UB.5 (health).
> The UX specification in plan-10 is binding. Follows `docs/phases.md`
> (rules 1–12, one phase per commit, outcome note in the same commit).
> Read ONLY this file plus `docs/phases.md` (and plan-10's UX spec) to
> implement any phase.

## Context

The final tier makes the UI operationally complete: run and watch the
tool from the browser (problem 4), debug agent tool calls with a real
log (problem 10), view/edit workspace.yaml (problem 11), see the
tool's own health and trust numbers (extra scope), read the docs
(problem 12), and share/save everything (extra scope).

---

### Phase UO.0 — Jobs UI: index from the browser `pending`

**Problem.** Problem 4 — no tool operations from the UI; indexing must
be careful UX (progress, cancel, no surprises).

**Deliverable.**
- Top-bar `Index ▸` button: click → `POST /api/jobs {kind:"index"}`;
  while running it renders a progress ring + `done/total` (SSE
  `job_progress`); hover shows elapsed + current stage; click while
  running opens the Jobs drawer tab (it also auto-opens on job start).
  A dropdown on the button offers "Full re-index" (`args.full: true`).
- Jobs drawer tab (`views/ops/JobsTab.tsx`): running job card
  (progress bar, live `log_tail` autoscrolling with pause, Cancel
  button with confirm) + history list (`GET /api/jobs`, state icon,
  duration, error expander for failed).
- Completion: success toast + non-destructive banner "Graph updated —
  Reload view" (per plan-10 spec; reload re-resolves the current scope
  and restores selection where ids survive, notice where they don't —
  US.1 fallback path). Failure: persistent error toast with verbatim
  error + "open Jobs tab".
- 409 single-flight response → toast "Index already running" + open tab.

**Tests.** Button state machine (idle/running/disabled); SSE progress
wiring updates ring and bar; cancel confirm → DELETE call; completion
banner triggers scope re-resolve (store-level integration); 409
handling; history rendering incl. failed-with-error.

**Acceptance.** Index chessleap end-to-end from the browser: progress
visible, cancel works, second run completes and the canvas reloads
non-destructively.

### Phase UO.1 — Tool-call log UI `pending`

**Problem.** Problem 10 — debugging agent tool calls needs a live,
searchable, clearable log with great UX.

**Deliverable.** `views/ops/ToolCallsTab.tsx` (drawer tab):
- Live tail via SSE `tool_call` events prepended to the list
  (pause/resume button freezes the viewport, buffered events flush on
  resume with a "+N new" pill); backfill/pagination via
  `GET /api/toolcalls`.
- Filter row: source chips (mcp/http), tool dropdown (distinct values
  from loaded rows), status (ok/error), time presets (15m/1h/24h/all),
  and a free-text box (`q`) — matching text is **highlighted** in
  params/error cells (mark tags).
- Rows: time (relative + exact on hover), source badge, tool, duration
  (color-scaled: >1 s amber, >5 s red), result size, status. Row click
  expands full pretty-printed params + error; "jump to node" link when
  a param value matches a known node id (resolves via `/api/node/{id}`,
  builds the file scope — UN.2 behavior).
- **Clear all**: button with confirm dialog stating the row count →
  `DELETE /api/toolcalls` → empty state ("Log cleared · new calls
  appear live").
- Errors in recording (SSE gap) are honest: if the SSE stream drops,
  the US.5 banner covers it; on reconnect the tab refetches page 1 and
  marks a "possible gap" divider line if ids are non-contiguous.

**Tests.** Live prepend + pause buffering + flush pill; filter →
request params exact; highlight marks; duration color thresholds;
clear-all confirm → DELETE + empty state; gap divider on
non-contiguous ids; jump-to-node scope build.

**Acceptance.** With `polyflow mcp` registered in an agent session,
watch calls stream live, filter to `tool=trace`, highlight a node id,
jump to it on canvas, clear the log.

### Phase UO.2 — Config editor `pending`

**Problem.** Problem 11 — view/update workspace.yaml with validation
(frozen: form + raw YAML toggle).

**Deliverable.** Config activity (`views/config/ConfigPanel.tsx`):
- Loads `GET /api/config`; header shows the file path + a "changed on
  disk" watch (refetch on window focus; etag drift → banner with
  "reload" / "keep editing").
- **Form mode** (default): sections Services (name/path/language/
  frameworks/port rows, add/remove), Links (from/to/via/hint/base_url/
  exchange), Excludes (glob list), Settings, Evidence (contract_globs,
  runtime service_names map, sse_routes). Form edits patch a parsed
  model; **saving always serializes to raw YAML client-side and PUTs
  raw** (single write path per UB.4); YAML comments in untouched
  regions survive by patching the raw text per-section only where the
  form changed (yaml document-level edit; if a section edit cannot
  preserve comments, the save dialog says which comments are lost
  before writing — honest, never silent).
- **YAML mode**: plain textarea editor (monospace, line numbers), same
  PUT path.
- Validation errors (422) render inline mapped to the section/line
  named in the loader error; 409 etag → diff-style "changed on disk"
  prompt (keep mine / take disk / cancel).
- Successful save → toast + "Re-index now?" action button (fires UO.0's
  job).

**Tests.** Form ⇄ YAML round-trip preserves untouched sections
byte-identically (fixture with comments); 422 mapping renders; 409 flow
choices; save→reindex prompt wiring; add/remove service row edits land
in the PUT body.

**Acceptance.** From the browser: add an exclude glob to chessleap's
workspace.yaml, save, re-index via the prompt, confirm the exclusion
in the new stats.

### Phase UO.3 — Health & trust dashboard `pending`

**Problem.** Extra scope: the tool's own trust numbers (doctor, eval,
ledger, coverage) belong in the UI.

**Deliverable.** Health activity (`views/health/HealthPanel.tsx`),
canvas-free page rendering `/api/health`:
- Index card (indexed_at, schema version, nodes/edges, parse errors —
  count links to a parse-error list section);
- Coverage card: verification-state distribution as labeled bars
  (verified/candidate/observed_only_gap/conflicting) with an
  explanation line each (plain language, e.g. "candidate: static edge
  not yet confirmed by runtime/contract evidence");
- Unresolved card: total + by-kind counts, click-through to the
  Unresolved drawer tab (UN.0/UF.6's tab);
- Eval card: per-repo recall table with baseline values; `present:
  false` renders the honest empty state ("no eval baseline found — run
  `polyflow eval`") — absence surfaced, not blank (rule-4 spirit).
- Auto-refresh on `graph_updated`/`job_done` SSE.

**Tests.** Renders fixture `/api/health` exactly incl. `present: false`
branch; click-throughs build the right drawer/filter state;
SSE-triggered refetch.

**Acceptance.** After a chessleap index + eval run, the dashboard
matches `polyflow doctor` output side-by-side.

### Phase UO.4 — In-UI docs: CLI reference + guides `pending`

**Problem.** Problem 12 — no usage docs exist anywhere (no README);
the UI should carry complete, always-current docs.

**Deliverable.**
- **Generated CLI reference**: `GET /api/docs/cli` — the server walks
  the cobra command tree (`rootCmd` exposed via a small
  `cmd/polyflow` registry refactor or `internal/meta` hook — pin the
  mechanism in the outcome note) and emits
  `{"commands": [{"name", "short", "long", "usage", "flags":
  [{"name", "shorthand", "default", "usage"}], "subcommands": […]}]}`.
  Because it is generated from the live binary, it can never go stale
  (single source of truth; handler test asserts every registered
  command appears — rule-12 accounting).
- Docs activity (`views/docs/DocsPanel.tsx`): left nav — **Setup**
  (init → index → serve walkthrough, MCP registration snippet
  `claude mcp add polyflow -- polyflow mcp`, workspace.yaml annotated
  example), **CLI reference** (rendered from the endpoint, searchable,
  anchor links per command), **UI guide** (gesture grammar table +
  shortcut sheet rendered from `interaction/keys.ts` — the live
  registry, so it can't drift), **Concepts** (short: scopes, flows,
  seams, verification states, the trust contract).
- Guide/concept prose lives as markdown files in `web/src/docs/*.md`
  imported at build time (versioned with the code).
- Also ship a repo-root `README.md` (short: what polyflow is, quick
  start, pointer to `polyflow serve` docs page) — closing the no-README
  gap in the same phase.

**Tests.** Go: `/api/docs/cli` includes every registered command +
flags (walk assertion); Vitest: search/anchors; shortcut sheet renders
every entry in `keys.ts`.

**Acceptance.** A new user reaches "indexed + UI open + MCP registered"
using only the Setup page.

### Phase UO.5 — Export, share links, saved views `pending`

**Problem.** Extra scope: export current view (PNG/SVG/JSON/Mermaid),
copy links, and persist named views.

**Deliverable.**
- Share menu (top bar): **Copy link** (URL hash already carries full
  ViewState — US.1); **PNG / SVG** of the current canvas (Cytoscape
  export via existing `cytoscape-svg` + `lib/export.ts`); **JSON**
  (the current scope's element list, Cytoscape shape); **Mermaid**
  (`/api/export/mermaid` passthrough with level matched to scope kind).
- **Saved views**: star button in the top bar → name dialog → persists
  `{name, view_state_json, created_at}` via new
  `GET/POST/DELETE /api/views` rows in ops.db (schema:
  `views(id INTEGER PK, name TEXT UNIQUE, state TEXT, created_at
  TEXT)` — the single permitted server addition in this plan, with
  handler tests). Explorer's Saved Views section lists them (click →
  decode + apply, with US.1's stale-id fallback); right-click →
  rename/delete.
- Export failures (canvas too large for PNG rasterize) fall back to
  SVG with a notice.

**Tests.** Go: views CRUD + unique-name conflict → 409; Vitest: save →
list → apply round-trip (mock API), stale-state apply falls back with
notice; export menu calls the right lib per format.

**Acceptance.** Save the fleet RabbitMQ seam view, reload the browser,
reopen it from Saved Views; share the link into another browser
profile and land on the same view.

### Phase UO.6 — Runtime capture UI: record, ingest, fuse `pending`

**Problem.** Starting/stopping runtime capture and getting the
captured evidence into the graph must work from the browser (user
addendum), with the CLI and UI fully interchangeable.

**Deliverable.**
- **`◉ Record` control** in the top bar (left of the Index button):
  click → session dialog (name prefilled `ui-<date>`, ports shown) →
  `POST /api/capture/start`; while capturing, the control pulses red
  with the live span counter (SSE `capture_progress`) and OTLP
  endpoint hint on hover ("point OTEL_EXPORTER_OTLP_ENDPOINT at
  :4318"); click again → stop confirm.
- **On stop** (or ingest completion): summary toast + a prompt
  wired to UB.7's `fusion_hint`: "Captured N spans (M services).
  **Fuse into graph now?**" → runs the index job (UO.0); after it
  completes, edges verified by this session visibly flip styling via
  the normal `graph_updated` reload — the user *sees* dashed
  candidates turn solid.
- **Runtime panel** (Flows activity gains a "Runtime" tab): session
  list (active + on-disk, incl. CLI-started ones — same store);
  per-session observed-flow table (kind, channel key, from→to,
  causality) with its ingest ledger inline (unmapped spans + reasons,
  never hidden); coverage view from `/api/runtime/coverage`: verified
  / observed-only / static-only channel lists — observed-only rows
  carry "propose contract rule" (shows the reconcile proposal YAML,
  copyable) and link into the Health dashboard's coverage card.
  "Import OTLP dump…" button → file upload → ingest → same fuse
  prompt.
- Capture started in the CLI shows live in the UI (status polling +
  SSE) and can be stopped from either surface; the phase's tests
  assert this cross-surface visibility.

**Tests.** Record control state machine (idle/starting/active/
stopping) with SSE counter; stop → fuse prompt → index job wiring;
runtime table + inline ledger rendering from fixture; observed-only
proposal display; CLI-started session appears and is stoppable
(mocked API contract); dump upload flow.

**Acceptance.** With `~/Projects/datascience` running against a
UI-started capture: watch the span counter climb, stop, fuse, and
show one edge whose detail panel now lists a runtime source — then
repeat with `polyflow capture start` from the CLI and confirm the UI
mirrors it.

### Phase UO.7 — CLI parity sweep: patterns, setup mode, the parity matrix `pending`

**Problem.** The user requirement is **every CLI capability available
in the UI**, with both surfaces updating the same graph. After UO.0–
UO.6, the gaps are `patterns`, `init` (serve currently refuses to boot
without an indexed workspace), and proof that nothing else is missing.

**Deliverable.**
- **Patterns viewer** (Settings activity → "Patterns"): list from a
  new `GET /api/patterns` (name, language, version gate, package gate,
  source file, per-pattern kinds/roles — wraps the `patterns list`
  internals); search + language filter; a pattern row expands to its
  YAML (read-only). "Add pattern…" uploads a YAML → `POST
  /api/patterns` → validated exactly like `patterns add` (errors
  verbatim) → saved to the workspace patterns dir → prompts re-index.
- **Setup mode**: `polyflow serve` boots with no workspace.yaml or no
  graph.db instead of erroring: it serves the shell in a guided setup
  page — step 1 pick/confirm workspace root + `init` (runs discovery
  via a new `init` job kind, shows the proposed services for
  confirmation, writes workspace.yaml through the UB.4 path), step 2
  first index (UO.0), step 3 land on overview. The CLI `init`/`index`
  path produces byte-identical results (same internals).
- **The parity matrix** (closes this plan): a pinned table in this
  doc's outcome note AND rendered on the Docs page — every CLI
  command/flag → its UI equivalent or a **declared exception**.
  Pinned exceptions (the only allowed ones): `mcp` (the UI *is* the
  human surface; MCP is the agent surface — observability via UO.1),
  `serve` flags (bootstrapping), shell-oriented output flags
  (`--format`, `--output` — the UI is the format). Rule-12 test: a Go
  test walks the cobra tree and asserts every command name appears in
  the matrix file (`web/src/docs/parity.md`), so a future CLI command
  without a UI story fails CI instead of drifting.
- **Both-ways freshness pinned**: every UI mutation goes through the
  same internals as its CLI twin (jobs → `indexer.Run` etc., config →
  `workspace.Save`, patterns → the `patterns add` writer, capture →
  the shared session store), and every CLI mutation reaches open UIs
  via the existing watchers (`graph_updated` fsnotify on graph.db;
  config watch-on-focus from UO.2; capture status from UB.7). State
  this as the phase's design invariant: **no state is writable from
  one surface and invisible to the other.** The test list includes one
  cross-surface case per store (graph.db, workspace.yaml, ops.db,
  capture sessions).

**Tests.** Go: `/api/patterns` list/add + validation errors verbatim;
init job discovery output; parity-matrix walk test. Vitest: patterns
panel, setup-mode step flow, matrix rendering. Cross-surface: CLI
`polyflow index` under an open (test) UI session → `graph_updated`
observed; UI pattern add visible to CLI `patterns list`.

**Acceptance.** On a fresh clone with no workspace.yaml:
`polyflow serve` → complete setup → indexed overview, browser-only.
The rendered parity matrix accounts for every command in
`polyflow --help`.

---

## Key files

- New: `web/src/views/{ops,config,health,docs,runtime,patterns,setup}/*`,
  `web/src/docs/*.md` (incl. `parity.md`), `README.md` (repo root).
- Modified: top bar (Index/Record/Share/star buttons live), drawer tab
  registry, `internal/server` (docs/cli + views + patterns endpoints,
  with tests), `cmd/polyflow` (cobra tree exposure for UO.4; serve
  setup-mode boot for UO.7).

## Traceability

| Phase | Closes |
|---|---|
| UO.0 | problem 4 (tool operations from UI) |
| UO.1 | problem 10 (tool-call debugging: log, search/filter/highlight, clear-all — addendum 3) |
| UO.2 | problem 11 (view/update workspace.yaml) |
| UO.3 | health/eval dashboard extra scope; problem 5's trust-numbers face |
| UO.4 | problem 12 (docs of all CLI options + setup) |
| UO.5 | export & share + saved views extra scopes |
| UO.6 | runtime capture start/stop/ingest from UI + evidence fusion made visible (addendum) |
| UO.7 | full CLI↔UI parity (patterns, init/setup mode, parity matrix; both surfaces update one graph) |

## Developer use-case sweep

"Re-index without a terminal" → UO.0. "What did my agent just ask, and
why was it slow?" → UO.1. "Add a service/exclude and re-index" → UO.2.
"Can I trust this graph right now?" → UO.3. "How do I even use this
tool?" → UO.4. "Send this exact view to a teammate / keep it for
tomorrow" → UO.5. "Record real traffic and watch the graph get
verified" → UO.6. "Do anything the CLI can do without a terminal — and
never wonder if the other surface is stale" → UO.7. Declared
non-goals: multi-user/auth on saved views (local tool); scheduled/
recurring jobs; log shipping/export of the tool-call audit (JSON
download of the filtered list is in scope for UO.1 via a "Download"
button — included in its deliverable); a span *timeline* visualization
(UO.6 ships tables/coverage; a Gantt-style timeline is a future tier).

## Verification

Vitest per phase; Go handler tests for the three server additions
(docs/cli, views CRUD, plus UB wiring); manual acceptance per phase on
chessleap and the fleet workspace, recorded in outcome notes. UO.6's acceptance
uses `~/Projects/datascience` (the OTel-instrumented fleet repo). Final
tier check (goes in UO.7's outcome note, alongside the parity matrix):
walk all 12 original problems + 4 extras + all addendum features
(context copy, waypoint flows, group/seam isolation, lenses, pinboard,
link explorer, capture, CLI parity) against the shipped UI and record
where each is closed — this is the UI series' coverage contract,
mirroring what plan-6 N.3 does for the backend series.
