# Plan 12 — UI Flows & Context (Tier U-F): isolation, group view, copy context, impact, coverage

Status legend: `pending` · `in progress` · `done`

> **Prerequisites — execute after `docs/plan-11-ui-navigation.md`** and
> plan-9's UB.5 (flow/seam endpoints) + UB.6 (context bundles). The UX
> specification in plan-10 is binding. Follows `docs/phases.md`
> (rules 1–12, one phase per commit, outcome note in the same commit).
> Read ONLY this file plus `docs/phases.md` (and plan-10's UX spec) to
> implement any phase.

## Context

This tier is the payoff: isolating a cross-service flow in seconds and
copying LLM-ready context from anything. It closes problem 7 (all five
frozen isolation mechanisms), the waypoint flow builder, seam/group
isolation, universal context copy, the impact/diff extra scope, and the
canvas half of problem 5 (coverage honesty: verification-state edge
styling + ⚠ ledger overlay).

Pinned shared type (used by the scope stack, URL codec, and every phase
here — added to `stores/scope.ts` in UF.0):

```ts
type FlowRef =
  | { kind: "through"; nodeId: string; entrypointId: string }
  | { kind: "path"; from: string; to: string; index: number }
  | { kind: "waypoints"; ids: string[]; direction: "forward"|"backward" }
  | { kind: "seam"; edgeId: string }
  | { kind: "varflow"; nodeId: string }
  | { kind: "edgeset"; nodeId: string; edgeTypes: string[] }  // lens-scoped flows from a node
  | { kind: "pins"; ids: string[] };                          // pinboard (UF.7)
```

`edgeset` flows take their `edgeTypes` from the active lens
(plan-11 UN.5) by default — "Isolate <lens name> flow from here" in the
context menu — so every lens doubles as a flow type.

---

### Phase UF.0 — Flow-lane renderer `pending`

**Problem.** An isolated flow needs a purpose-built reading layout, not
a generic graph.

**Deliverable.** `views/flows/FlowLane.tsx` + scope resolver
`scopes/flow.ts`:
- Input: a chain list (UB.5 shape). Layout: **left→right by hop order,
  one horizontal swimlane per service** (service name pinned at the
  lane's left edge), dagre-LR within the constraint (rank = hop index;
  lane = service). Hop edges labeled channel + confidence;
  verification-state styling per plan-10 spec (solid/dashed/dotted/
  double-line).
- Entering a flow: 200 ms fade-out of non-members, then lane layout
  morph (US.3 transition rules apply). Breadcrumb chip
  `Flow: <entrypoint label> → <terminus label> [×]`; `[×]`/`Esc` pops
  back to the prior scope with its cached viewport.
- Cross-service hops render a lane-crossing edge with the channel key
  in a pill; clicking any hop node/edge = normal selection (detail
  panel, gestures unchanged).
- `FlowRef` encodes in the URL (US.1 codec) — flows are shareable and
  saveable.
- Truncated chains (UB.5 `truncated: true`) show an end-cap marker
  "+ more (depth limit)" that re-queries deeper — never silently ends.

**Tests.** Lane assignment (multi-service fixture chain → services get
distinct lanes, hop order preserved); chip pop restores prior scope;
truncation cap renders and re-queries; URL round-trip for each FlowRef
kind; two-run determinism of element order.

**Acceptance.** A hand-verified chessleap route→SSE flow renders as a
readable left-to-right lane.

### Phase UF.1 — Flows-through-here + entrypoint catalog `pending`

**Problem.** The two catalog-style entries into flows (problem 7's
intersection-point idea; the route-table view).

**Deliverable.**
- Context-menu + detail-panel action "Isolate flows through here" →
  `/api/flows/through/{id}` → detail panel lists flows grouped by
  entrypoint (entrypoint label, hop count, services touched,
  min-verification badge); hover pre-highlights the flow's members in
  the current scope (cheap dim, no layout change); click isolates as a
  lane (UF.0, `FlowRef{kind:"through"}`).
- **Flows activity** (`views/flows/Catalog.tsx`): searchable, sortable
  table of `/api/flows/entrypoints` (kind icon, label, channel,
  service, file:range); kind filter chips (route/consumer/worker/
  entrypoint-func); row click → its forward flow as a lane. Honest
  footer from UB.5 `skipped` counts ("312 callbacks / 41 unreachable
  not listed — show anyway" toggle). The catalog registers a "Flows"
  group in the palette (US.4 registry).
- Empty/huge states: >500 entrypoints virtualized; zero flows through a
  node → panel says so with the node's edge counts (not a blank list).

**Tests.** Grouped list rendering from fixture response; hover
pre-highlight adds/removes classes only (no layout calls); catalog
filter/sort determinism; skipped-counts footer math; palette group
registration.

**Acceptance.** chessleap: pick a mid-chain function, isolate one of
its flows in ≤3 clicks; catalog lists the same routes as
`eval/corpus/chessleap/manifest.yaml` spot-checks.

### Phase UF.2 — Path finder + waypoint flow builder `pending`

**Problem.** "How does A reach B?" and iterative refinement to one
unique flow (user-requested: select a node, then repeatedly pick
parent/child flow nodes).

**Deliverable.**
- **Path finder**: context-menu "Set as path start" (pin badge on the
  node, chip in the top bar `A: <label> [×]`) → on a second node "Find
  paths from A" → `/api/flows/paths` → panel lists paths ranked
  (hops · min-confidence), hover previews (dim non-members), click
  isolates (`FlowRef{kind:"path"}`), "Overlay all" renders the union
  with per-path color accents (≤5 colors, then grouped).
  `reachable: false` renders honestly: "No static path A→B" plus both
  nodes' nearest-entrypoint info and a link to check `/api/unresolved`
  for either file (the gap might be a ledgered miss — problem 5 tie-in).
- **Waypoint builder** (`views/flows/WaypointBuilder.tsx`): "Start flow
  here" opens a panel with the seed node as first chip; `/api/flows/
  refine` supplies upstream/downstream candidate lists (label ·
  service · via edge type); clicking a candidate appends a waypoint
  chip and re-queries; chips removable mid-list (re-validates the
  remainder; broken segment → inline error naming the disconnected
  pair, chips kept for editing). The canvas shows the growing lane
  live after each change. Result is `FlowRef{kind:"waypoints"}` —
  shareable, saveable, copyable.

**Tests.** Start/end chip state machine (set, replace, clear); ranked
list determinism; overlay color grouping >5 paths; unreachable state
content; waypoint append/remove/re-validate flows including the
disconnected-pair error; live lane updates per change (store-level).

**Acceptance.** Fleet workspace: build the nextGen→CDR-Agent RabbitMQ
flow by waypoints in ≤5 clicks from the publisher; path-find the same
pair and get the identical chain.

### Phase UF.3 — Seam isolation + service-pair channels `pending`

**Problem.** One gesture on any connector (cross-service edge, channel,
queue, route, DOM event) must isolate the complete group flowing
through it (problem 7 + user's seam requirement).

**Deliverable.**
- Context-menu on ANY edge: "Isolate seam" → `/api/seam/{edge-id}` →
  lane view with producers left, channel pill center, consumers right;
  multiple producers/consumers stack vertically within their lanes
  (rule-1 fan-out made visible). Detail panel shows the seam summary:
  channel key, verification state, evidence sources, producer/consumer
  counts.
- **Service-pair drill-in**: in the overview scope, single-click on an
  aggregated service edge opens the detail panel listing every concrete
  channel crossing that pair (from `/api/services/channels?from=&to=`):
  kind icon, channel key, verification badge, producer/consumer counts;
  click a channel → its seam isolation. This is the primary
  cross-service exploration entry (plan-10 UX spec §flow-isolation 3).
- DOM-event and pub/sub seams need no special casing — they are edges
  with channel meta; verify with fixtures that `datastar_action`,
  `publishes`/`subscribes`, and `dom_listen` seams all resolve (rule-12
  spirit: an edge kind the seam endpoint can't expand renders the edge
  pair alone with an explicit "no channel closure" note, never an
  error).

**Tests.** Seam lane with 2 consumers renders both (rule 1); service-
pair channel list exact from fixture; channel click → seam scope push;
no-channel edge → pair + note; verification badges match fixture
states.

**Acceptance.** Fleet workspace overview: click nextGen↔CDR edge → see
the RabbitMQ channel list → isolate `cdr_requests` → both repos' chains
in one lane view.

### Phase UF.4 — Multi-select group view `pending`

**Problem.** "View a group of nodes and understand its relationships"
(user requirement 4 of the addendum).

**Deliverable.** Marquee-drag + shift-click multi-selection (gesture
layer already reserves them); selection HUD chip "N selected —
View as group"; group scope (`Scope{kind:"group"}`) renders the induced
subgraph: exactly the selected nodes + all edges among them (any
layout; default fcose). "Add all matches" action in the palette/filter
bar adds current-filter matches to the selection (budget-checked).
Detail panel (group selected) shows the relationship summary:
edge-type counts, services touched, shared channels, contained files;
plus per-pair matrix for ≤8 nodes (compact grid, edge-type glyphs).
Group is URL-encodable and copyable (UF.5).

**Tests.** Induced-subgraph math (edges strictly within selection);
marquee → selection ids; matrix ≤8 gate; add-all respects budget
dialog; URL round-trip.

**Acceptance.** Select a chessleap handler + its template + its store
func → group view shows exactly their interconnections.

### Phase UF.5 — Context-copy workbench `pending`

**Problem.** The goal-closing feature: every element and view copyable
as LLM-ready context (universal "Copy context").

**Deliverable.**
- "Copy context" on: node/edge detail panel, flow chip menu, group HUD,
  scope breadcrumb menu, and `⌘⇧C` for the current selection — all
  routes to one module `views/context/copy.ts` that builds the UB.6
  request: current element(s) → `elements`, mode toggle
  **Viewed / Expanded** (expanded shows a depth stepper 1–5), snippets
  toggle, token budget select (2k/8k/32k/custom).
- **Preview drawer** (bottom drawer gains a "Context" tab): rendered
  markdown preview + raw toggle, token estimate, truncation warnings
  from UB.6 (`omitted` list rendered prominently), Copy button
  (clipboard) + "Download .md". Recent bundles (last 10, in-memory)
  listed for re-copy.
- Viewed mode for a scope sends exactly the on-canvas element ids
  (post-filter, post-cluster: clustered containers expand to their
  members' ids — what you *see* is what you copy, and cluster
  expansion is stated in the request preview line "142 nodes (3
  clusters expanded)").
- Errors (unknown id after reindex) surface the UB.6 error verbatim
  with a "refresh view" action.

**Tests.** Request building per source (node/edge/flow/group/scope);
viewed-mode cluster expansion ids; preview renders `omitted`
prominently; clipboard called with exact markdown; recent-bundles ring.

**Acceptance.** Isolate the nextGen→CDR RabbitMQ flow (UF.3) → Copy
context (Expanded, snippets on) → pasted markdown contains both repos'
consumer/producer code ranges and the channel line — hand-verified.

### Phase UF.6 — Impact & diff visualization + coverage overlay `pending`

**Problem.** Extra-scope item (impact/diff on canvas) + the canvas half
of problem 5 (missing edges must be debuggable).

**Deliverable.**
- **Impact scope** (Impact activity + context menu "Impact from here"):
  `Scope{kind:"impact"}` renders the blast radius (existing
  `/api/file/impact` + trace backward for nodes) with depth rings
  (target accented; direct dependents strong; transitive fading);
  direction toggle up/down/both; depth stepper. A "Diff" tab in the
  Impact activity calls a new `GET /api/impact/diff` (thin wrapper over
  the existing `impact --diff` internals incl. plan-8 Z.1 multi-root
  semantics; single permitted server addition in this plan, with
  handler tests + `unmapped_hunks` passthrough) and renders changed
  nodes badged `M` with the union blast radius; `unmapped_hunks`
  (incl. `no_git_repo`) listed in the panel — never dropped.
- **Coverage overlay** (toggle in the filter bar, default on):
  verification-state edge styling everywhere (already specced),
  plus ⚠ badges on nodes whose file has unresolved refs; badge click →
  Unresolved drawer tab pre-filtered to that file with entries linked
  back ("this is why an edge may be missing here"). The Unresolved tab
  itself (from UN.0's badge work) gains kind filters + free-text search
  mirroring `/api/unresolved` params.

**Tests.** Ring depth classes; direction/depth re-query; diff view
badges + unmapped list rendering; overlay toggle adds/removes classes
only; badge → pre-filtered drawer state.

**Acceptance.** Edit one chessleap file, Diff tab shows its blast
radius; a known ledgered gap (from `polyflow status --unresolved`)
is reachable in ≤2 clicks from its ⚠ badge.

### Phase UF.7 — Pinboard: flows through pinned nodes only `pending`

**Problem.** Viewing a specific flow in detail by pinning a few nodes:
only connections flowing through the pinned set should remain visible,
with easy clearing (user addendum).

**Deliverable.**
- Pinning: `p` on hovered/selected node, context-menu "Pin to
  pinboard", or pin icon in the detail panel. Pinned nodes get a 📌
  badge on canvas and a chip in the **pin tray** under the top bar
  (plan-10 layout gallery #6); chips: click = select the node, `×` =
  unpin, `[clear all]` empties the tray. Pins persist in `ViewState`
  (URL-shareable) and survive scope changes.
- **Pinboard mode** engages automatically at ≥2 pins (1 pin only
  badges): the canvas keeps only nodes/edges lying on some flow path
  passing through **all** pins (order-free). Resolution: consecutive
  connectivity via `/api/flows/paths` between pin pairs in both
  directions, union of the k=5 path sets per pair, intersected down to
  paths traversing every pin; computed server-side via
  `/api/flows/refine` where an ordering exists (it validates and
  stitches), client-side set intersection otherwise. Everything else
  fades out (Feel rules), *not* removed from the scope — unpinning
  restores instantly without refetch.
- Honest empty result: "No flow passes through all N pins" with
  per-pair reachability (which pair broke) and one-click "remove
  <node>" suggestions — never a silent blank canvas.
- Pins compose with the active lens (UN.5): pinboard paths are
  computed over the lens's edge set, so "Data-flow through these 3
  pins" works.
- `FlowRef{kind:"pins"}` scope: "View as flow lane" action in the tray
  renders the pinboard result as a UF.0 lane; Copy context (UF.5)
  works on it.

**Tests.** Pin/unpin/clear state machine incl. URL round-trip; 2-pin
and 3-pin path-set intersection math (fixture with a diamond so a
non-through branch must disappear); empty-result names the broken
pair; lens composition (same pins, different lens → different edge
set); fade-not-remove (unpin restores without network calls —
assert no fetch).

**Acceptance.** Fleet workspace: pin nextGen's publisher and
CDR-Agent's handler → only the RabbitMQ flow remains; add an unrelated
third pin → honest empty state naming the disconnected pair; clear all
→ full scope returns instantly.

### Phase UF.8 — Link explorer: peek or commit upstream/downstream `pending`

**Problem.** Viewing a node's detailed links upstream/downstream with
search/filter — from a selected node or from search — with a UX that
either previews (peek) or updates the graph (commit), per plan-10's
binding "Peek vs commit" principle (user addendum).

**Deliverable.** `views/explore/LinkExplorer.tsx` — a detail-panel
section for any selected node (and openable from a palette result via
"Explore links" without first committing a scope change):
- Header: `[upstream N | downstream N]` toggle (counts from the
  adjacency data; both directions loaded lazily). Depth stepper 1–3
  (default 1); depth >1 groups rows by path ("via X → Y").
- Filter row: `kind:` `service:` chips + free text — filtering the
  loaded link list client-side, same syntax as the palette (UN.2).
- Rows (per plan-10 gallery #7): target label, edge-type icon +
  channel, service, `file:12–48`, verification dot. **Hover = peek**
  (ghost nodes + glowing edges per the shell's ghost renderer);
  **`＋` = commit-expand** (adds the target and its connecting edge to
  the current scope — scope gains an `expanded` id list in
  `ViewState`, budget-checked); **`→` = commit-navigate** (pushes the
  target's file scope with it selected, UN.2 behavior).
- Keyboard: `[` / `]` peek-walk the top upstream/downstream row from
  the current selection; Enter commits `→`, `⇧Enter` commits `＋`.
- Rule-1 honesty: the list is the *complete* adjacency (paginated at
  100 per direction with exact totals), including edges whose far node
  is outside the current scope or service — nothing hidden by scope.

**Tests.** Direction toggle + lazy loads; filter chip narrowing;
peek renders ghosts and leaves ViewState untouched (assert store
equality); `＋` adds exactly node+edge and re-budgets; `→` pushes file
scope; depth-2 grouping; pagination totals; keyboard walk.

**Acceptance.** chessleap: from a store function, filter upstream
links to `kind:http_handler`, peek each without the canvas changing
state, commit-expand two of them, then commit-navigate to one — all
without losing the original scope (breadcrumb pops back).

---

## Key files

- New: `web/src/views/flows/{FlowLane,Catalog,WaypointBuilder,Pinboard}.tsx`,
  `web/src/views/explore/LinkExplorer.tsx`,
  `web/src/views/context/copy.ts` + drawer tab,
  `web/src/views/impact/*`, `web/src/views/canvas/scopes/{flow,group,impact}.ts`.
- Modified: `stores/scope.ts` (FlowRef), gesture/context-menu registry,
  palette registry, `internal/server` (UF.6 diff endpoint only).

## Traceability

| Phase | Closes |
|---|---|
| UF.0 | problem 7 — flow rendering foundation |
| UF.1 | problem 7 (flows-through-here, entrypoint catalog) |
| UF.2 | problem 7 (path finder) + waypoint flow builder (addendum 1) |
| UF.3 | problem 7 (service-pair, seam/intersection isolation — addendum 5) |
| UF.4 | group view (addendum 4) |
| UF.5 | universal context copy (addendum 2, 5) — the goal-closing feature |
| UF.6 | impact/diff extra scope; problem 5 (missing edges debuggable) |
| UF.7 | pinboard flow filtering (addendum: pin nodes → only through-flows visible, clearable) |
| UF.8 | link explorer with peek/commit (addendum: detailed up/downstream by search/filter) |

## Developer use-case sweep

"What runs through this function?" → UF.1. "How does A reach B?" →
UF.2. "Who talks over this queue/event/route?" → UF.3. "How do these N
things relate?" → UF.4. "Give me context to paste into an LLM" → UF.5.
"What breaks if I change this / what does my diff touch?" → UF.6. "Why
is the edge I expected missing?" → UF.6 overlay → ledger. "Show only
what flows through these exact nodes" → UF.7. "Who calls this /
what does this reach — preview before I change my view" → UF.8. Declared
non-goals: runtime capture/observed-flow UI (owned by plan-13 UO.6,
not this tier); editing/annotating flows; exporting video/animated
walkthroughs.

## Verification

Vitest per phase (positive + negative + determinism on ordered sets);
Go handler tests for the UF.6 endpoint; manual acceptance per phase on
chessleap and, for UF.2/UF.3/UF.5, the fleet workspace
(nextGen ↔ CDR-Agent RabbitMQ case from plan-8 Z.2) with results
recorded in outcome notes. The UF.5 acceptance artifact (the pasted
bundle for the RabbitMQ flow) goes in the outcome note verbatim — it is
this tier's goal-closing proof, mirroring plan-8 Z.2's trace artifact.
