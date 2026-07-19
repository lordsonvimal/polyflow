# Plan 11 — UI Navigation & Views (Tier U-N): hierarchy, search, source, stack

Status legend: `pending` · `in progress` · `done`

> **Prerequisites — execute after `docs/plan-10-ui-shell.md`** (uses the
> scope stack, gesture layer, canvas host, palette) and plan-9's UB.0
> (line ranges) + UB.1 (`/api/tree`) + UB.5 (`/api/stack`,
> `/api/unresolved`). The UX specification embedded in plan-10 is
> binding here. Follows `docs/phases.md` (rules 1–12, one phase per
> commit, outcome note in the same commit). Read ONLY this file plus
> `docs/phases.md` (and plan-10's UX spec section) to implement any
> phase.

## Context

With the shell in place, this tier delivers how developers *find and
understand* code: the hierarchy tree synced with the canvas, the
drill-down scopes, unified search, line-range display with bounded
source, and the tech-stack view. Problems closed: 2 (hierarchy), 3
(search/filter), 6 (line ranges), 8 (layout intuitiveness), 9 (tech
stack), plus the ⚠ coverage badges' navigation half of problem 5.

---

### Phase UN.0 — Tree explorer, two-way synced `pending`

**Problem.** No hierarchy navigation exists; the graph's `contains`
backbone is invisible.

**Deliverable.** `views/explore/Tree.tsx` + `stores/tree.ts`:
- Renders `/api/tree` per service; services listed from `/api/stack`;
  children fetched lazily per service on first expand (cache until
  `graph_updated` SSE).
- Rows: kind icon (folder/file/class/struct/ƒ/method/component/var),
  name, and for symbols `12–48` range chip (from `line`/`end_line`;
  omit when `end_line` is 0). Virtualized list (simple windowing —
  no dependency) so a 2,792-file nextGen tree scrolls without lag.
- Gestures via `interaction/gestures.ts`: single-click selects (detail
  panel), double-click pushes the matching scope (folder → folder
  scope, file → file scope, symbol → neighborhood scope), right-click
  context menu.
- **Two-way sync**: canvas selection reveals + highlights the tree row
  (auto-expanding ancestors); tree selection highlights the canvas
  element when present in the active scope, else offers "open scope"
  (never silently no-ops).
- ⚠ badge on files/folders with unresolved refs (counts from
  `/api/unresolved?service=`, aggregated up the path); click badge →
  Unresolved drawer tab pre-filtered to that path.

**Tests.** Lazy load per service; virtualization renders only window;
sync both directions (canvas→tree reveal, tree→canvas highlight);
badge aggregation math; orphan symbols (UB.1 contract) render under
their file.

**Acceptance.** nextGen-sized tree (2,792 files) scrolls at 60 fps;
chessleap tree matches `ls -R` spot-checks.

### Phase UN.1 — Drill-down scopes: overview → service → folder → file `pending`

**Problem.** The landing view and the drill hierarchy (plan-10 UX spec
"View modes") don't exist yet; today the UI renders a 2,000-node page.

**Deliverable.** Scope resolvers in `views/canvas/scopes/` (one module
per scope kind, pinned names: `overview.ts`, `service.ts`, `folder.ts`,
`file.ts`, `neighborhood.ts`):
- **overview**: services (`/api/stack` counts on the node) + aggregated
  cross-service edges (reuse `lib/aggregate.ts`), edge labels = channel
  kinds + counts ("http ×12 · rabbitmq ×2").
- **service**: top-level folders as collapsed compounds (counts from
  `/api/tree`), cross-folder edges aggregated; boundary edges to other
  services as stub connectors (plan-10 spec); stub click → "expand
  scope" push.
- **folder**: files + intra-folder edges; **file**: symbols +
  intra-file edges (fetched via `/api/graph/trace`-shaped subgraph
  queries scoped by file — if a dedicated endpoint is needed, add
  `GET /api/scope?kind=file&service=&path=` to `internal/server`
  mirroring the tree handler's index walk; pin its shape in the phase
  outcome and add handler tests; this is the single permitted server
  change in this plan).
- **neighborhood**: `context.Build`-backed via existing
  `/api/file/impact` + `/api/graph/trace`, depth from a detail-panel
  stepper (1–5, default 2).
- Double-click drill per gesture grammar; every scope respects the
  US.3 budget pipeline; deterministic element order into Cytoscape
  (rule 2 — sort by id before add).

**Tests.** Each resolver: fixture index → exact element set (positive)
+ boundary-stub presence (negative: external edge NOT expanded); drill
push/pop restores previous scope's viewport (position cache);
two-run determinism per resolver.

**Acceptance.** chessleap: land on overview, reach a known handler in
≤4 double-clicks; synergy: overview shows the Nx services with real
cross-service edge counts.

### Phase UN.2 — Search that lands you somewhere `pending`

**Problem.** Search results must navigate (auto-building the right
scope), and filters must be one comprehensible surface (problem 3).

**Deliverable.**
- Palette result Enter behavior (replaces US.4's interim): symbol →
  push its **file scope** + select it + reveal in tree; file → file
  scope; service → service scope. Result rows show kind icon, label,
  `service · file:12–48`, and a confidence dot for inferred-only nodes.
- **Filter bar** (`views/canvas/FilterBar.tsx`, one surface above the
  canvas): chips for confidence (static/inferred on by default;
  partial/unknown opt-in, dashed rendering), edge-type groups (calls ·
  http · messaging · data-flow · dom · structure), services. Active
  count badge; one-click "reset". Filters live in `ViewState.filters`
  (US.1) so they encode into shared URLs; changing filters re-runs the
  budget pipeline.
- Filter changes animate (fade out removed elements, 150 ms) and never
  re-layout unless element set changed (no gratuitous motion).

**Tests.** Enter-on-symbol builds file scope + selection + tree reveal
(integration test across stores); filter chips → element-set diffs
exact; URL round-trip with filters; dashed styling applied to opt-in
confidence tiers.

**Acceptance.** Search a chessleap route name → Enter → its file scope
with the node selected and visible in tree, in one action.

### Phase UN.3 — Line ranges + bounded source panel `pending`

**Problem.** Problem 6: nodes show one line; source view loads whole
files with no highlight.

**Deliverable.**
- Everywhere a location renders (detail panel, tooltips, tree chips,
  search rows, flow hops): `file:12–48` format; `:12` when `end_line`
  is 0 (honest unknown — never fabricate an end).
- Detail panel source section: fetch `?range=1&context=5`, render with
  line numbers starting at `first_line`, the node's own extent
  highlighted (background tint), context lines dimmed; "expand context"
  (+10 lines each press, refetch) and "whole file" toggle. Language
  syntax highlight via a small tokenizer only if already available —
  otherwise plain monospace (no new heavy dependency; note the choice).
- "Copy path" copies `file:start–end`.

**Tests.** Range rendering math (first_line offset, highlight rows);
end_line=0 fallback; expand-context refetch params; copy-path format.

**Acceptance.** Select 3 hand-verified chessleap functions → highlighted
extents match the source exactly.

### Phase UN.4 — Tech-stack view `pending`

**Problem.** Problem 9: the stack (languages, frameworks, deps,
services) is invisible in the UI.

**Deliverable.** `views/explore/StackPanel.tsx` — a canvas-free page
(activity: Explore → "Stack" tab, and linked from each service's detail
panel): per service cards with language, frameworks, top dependencies
with versions (`/api/stack`), node/edge-type distribution (compact bar
lists, not charts-library — plain divs), file count; workspace header
row with totals and cross-service channel-kind summary. Every number
click-navigates (e.g. `http_handler 34` → palette pre-filtered
`kind:http_handler service:x`).

**Tests.** Renders fixture `/api/stack` exactly; number click builds
the right palette/filter state; empty-deps service renders honestly
("no dependency manifest found") not blank.

**Acceptance.** The stack page answers "what is this fleet built with?"
for the 7-repo fleet workspace at a glance.

### Phase UN.5 — Flow lenses: one-click edge-class modes `pending`

**Problem.** Isolating a *type* of connection (function calls, service
HTTP calls, messaging, variable/data flow, imports, DOM) requires
assembling edge-type filter chips by hand; developers need named,
one-click modes.

**Deliverable.** A **lens control** (segmented buttons right of the
breadcrumbs, on every canvas page — see plan-10 layout gallery #1)
backed by `views/canvas/lenses.ts` with this pinned table (the lens →
edge-type mapping is the contract; every `graph.EdgeType` must appear
in ≥1 lens or in the pinned "structure-only" remainder list — rule-12
accounting, asserted by a test that walks the edge-type enum):

| Lens | Edge types shown |
|---|---|
| All | everything except `contains` (default) |
| Calls | `calls`, `spawns`, `instantiates` |
| HTTP | `http_call`, `sse_endpoint`, `grpc_call`, `graphql_call`, `navigates_to`, `ws_*` |
| Messaging | `publishes`, `subscribes`, `kafka_publish`, `nats_publish`, `redis_publish`, `job_enqueue`, `job_perform`, `pusher_*`, `hub_*` |
| Data flow | `declares`, `reads`, `writes`, `captures`, `flows_to`, `queries`, `persists` |
| Imports | `imports`, `uses_type`, `inherits`, `implements`, `component_impl` — plus a **module rollup** toggle: aggregate to file→file / folder→folder import edges with counts |
| DOM | `dom_*`, `datastar_*`, `renders`, `defined_in` |

- A lens is a named preset over `ViewState.filters.edgeTypes` (US.1) —
  it composes with scope, encodes in shared URLs, and re-runs the
  budget pipeline; custom chip edits switch the control to "Custom".
- Nodes with zero visible edges under a lens dim to 30% (kept for
  orientation, one click to restore via the lens's "hide unlinked"
  toggle) — never silently removed.
- The Imports lens's module rollup reuses the aggregation approach of
  `lib/aggregate.ts` (client-side, counts on edges, click an
  aggregated edge → the concrete import list in the detail panel).
- Palette commands "Switch lens: <name>" registered (US.4).

**Tests.** Enum-coverage walk (every edge type mapped or listed);
lens → filter state exact; URL round-trip with lens; imports rollup
math (file→file counts) + drill-to-list; dim-vs-hide toggle; custom
edit → "Custom" state.

**Acceptance.** chessleap: Calls lens shows a clean call graph of a
file scope; Imports lens at service scope shows the folder-level
import structure with counts; Messaging lens on the fleet workspace
overview shows only the RabbitMQ topology.

---

## Key files

- New: `web/src/views/explore/{Tree,StackPanel}.tsx`,
  `web/src/views/canvas/{FilterBar}.tsx`, `web/src/views/canvas/lenses.ts`,
  `web/src/views/canvas/scopes/*.ts`, `web/src/stores/tree.ts`.
- Modified: palette Enter behavior (US.4 file), detail panel source
  section (US.2's DetailHost content), possibly
  `internal/server/handlers.go` (+`scope` endpoint, UN.1 only, with
  tests).

## Traceability

| Phase | Closes |
|---|---|
| UN.0 | problem 2 (hierarchy nav); problem 5's find-the-gap half (⚠ badges) |
| UN.1 | problems 1, 8 (intuitive scoped layouts, landing) |
| UN.2 | problem 3 (search/filter) |
| UN.3 | problem 6 (line ranges) |
| UN.4 | problem 9 (tech stack) |
| UN.5 | problem 7 (flow-type isolation: calls/http/messaging/data/imports/dom lenses) |

## Developer use-case sweep

"Where is this function in the project?" → UN.0 sync. "What's inside
this service/folder?" → UN.1. "Take me to X" → UN.2. "Exactly which
lines is this?" → UN.3. "What's this repo built with?" → UN.4. "Show
me only the call graph / only imports / only messaging" → UN.5. Declared
non-goals: editing source from the UI; git blame/history views; symbol
rename/refactor operations.

## Verification

Vitest per component/resolver (positive + negative + two-run
determinism where sets are produced); manual acceptance on chessleap +
one large repo (nextGen tree, synergy overview) recorded per phase;
`make build` embed check; Go suite green (UN.1's optional endpoint has
handler tests).
