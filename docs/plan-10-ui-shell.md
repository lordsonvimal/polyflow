# Plan 10 — UI Shell & Foundation (Tier U-S): the workbench rebuild

Status legend: `pending` · `in progress` · `done`

> **Prerequisites — execute after `docs/plan-9-ui-backend.md`** (consumes
> UB.0/UB.1 fields; degrade gracefully where noted if a UB endpoint is
> missing). Plans 11–13 build on this shell and on the UX spec below,
> which is **binding on them**. Follows `docs/phases.md` (rules 1–12,
> one phase per commit, outcome note in the same commit). Read ONLY this
> file plus `docs/phases.md` to implement any phase.

## Context

This is a **full UX-flow rewrite** of `web/`. The current single-mode
layout (`App.tsx`: toolbar + search/trace/filter sidebar + canvas +
detail) is replaced by a workbench shell. Surviving code: the stack
(SolidJS 1.8 + Cytoscape 3.30 + Vite 5 + Tailwind v4 + Vitest), the
Cytoscape wiring patterns in `Graph.tsx`, and pure libs
(`lib/boundary.ts`, `lib/aggregate.ts`, `lib/export.ts`,
`lib/confidence.ts` — kept with their tests). All components and stores
are rebuilt around a **scope stack**: the ordered list of narrowing
steps that defines what the canvas shows. Every view is a scope; every
scope is URL-encodable, shareable, and budget-checked.

Frozen decisions (do not relitigate): no WebGL — every view is a scoped
subgraph; hard element budget with honest over-budget handling; keep the
stack; detail-on-demand with one gesture grammar everywhere.

---

## The UX specification (binding on plans 10–13)

### Shell layout (real estate)

```
┌──┬───────────┬──────────────────────────────────────┬─────────┐
│A │ EXPLORER  │ ◆ polyflow  ⌘K   [Index ▸] [Share ▾] │ DETAIL  │
│c ├───────────┼──────────────────────────────────────┤ (opens  │
│t │ ▾ nextgen │ nextgen ▸ app ▸ jobs ▸ sync.rb  [×]  │  only   │
│i │  ▾ app/   │ ┌──────────────────────────────────┐ │  on     │
│v │   ▾ jobs/ │ │                                  │ │ select) │
│i │    sync.rb│ │        CANVAS (scoped            │ │         │
│t │     ƒ run │ │        subgraph, ≤1500 el.)      │ │ name    │
│y │ ▸ cdr     │ │                                  │ │ file:   │
│  │ ▸ sce     │ │                                  │ │ 12–48   │
│b │───────────│ └──────────────────────────────────┘ │ edges   │
│a │ ★ VIEWS   ├──────────────────────────────────────┤ source  │
│r │  checkout │ ▤ Jobs │ ⚡Tool calls │ ⚠ Unresolved │ actions │
└──┴───────────┴──────────────────────────────────────┴─────────┘
```

- **Activity bar** (icon column, keys `1`–`7`): Explore · Flows ·
  Impact · Health · Config · Docs · Settings. Switching activities swaps
  the left panel content; the canvas persists.
- **Explorer** (resizable; collapsible to zero width): hierarchy tree
  (service→folder→file→class→ƒ/var) from `/api/tree`, two-way synced
  with the canvas; **Saved views** section pinned at its bottom.
- **Breadcrumb bar** = the scope stack; each crumb clickable to pop back
  to that scope; `[×]` clears to overview; active flow/impact isolation
  shows as a dismissible chip at the end.
- **Detail panel** (right): closed until a selection exists; resizable;
  pinnable (pin the current detail, select another → two panels for
  comparison).
- **Bottom drawer**: closed by default; opens on demand or automatically
  when a job starts; tabs Jobs · Tool calls · Unresolved.
- **Top bar**: product mark `◆ polyflow` at the far left (present on
  every page — it is also the click target for "back to overview"),
  omni-search trigger, Index button with inline progress, Share/Export
  menu, **pin tray** (chips for pinned nodes when the pinboard is
  active — plan 12 UF.7), stats chip (nodes/edges/coverage), theme
  toggle.

### Gesture grammar (uniform across tree rows, canvas nodes, edges, groups)

| Gesture | Meaning everywhere |
|---|---|
| hover | tooltip (label · kind · `file:12–48`) + highlight incident edges |
| single-click | select → detail panel (node: info/edges/source; edge: channel, provenance, verification_state; group: contents summary) |
| double-click | drill/expand (service→internals, folder→files, file→symbols, collapsed group→expand, boundary→expand) |
| right-click | context menu: Isolate flows through here · Set as path start/end · Impact from here · Show source · Copy context · Expand/Collapse · Copy path · Hide |
| shift-click / marquee-drag | add to multi-selection (group view, plan 12) |
| scroll / drag-canvas / drag-node | zoom / pan / reposition (pins the node for the session) |
| `Esc` | close detail → clear selection → pop isolation → pop scope (in that order, one level per press) |
| `⌘K` / `/` | command palette / omni-search |
| `⌘⇧C` | Copy context for current selection (plan 12) |
| `p` | pin/unpin the hovered or selected node to the pinboard (plan 12 UF.7) |
| `[` / `]` | peek one hop upstream / downstream from the selection (plan 12 UF.8) |
| hover on a link-list row | **peek**: ghost-preview on canvas, no state change (see below) |

### Peek vs commit (binding interaction principle)

Every list that names graph elements not currently on canvas (upstream/
downstream link lists, flow lists, path lists, waypoint candidates,
search results) supports both depths of engagement:

- **Peek** — hover (or focus, keyboard) a row: the referenced elements
  render as **ghosts** on the canvas (reduced opacity, dashed halo,
  positioned by a light incremental layout near their anchor) and the
  connecting edges glow; moving off the row removes the ghosts. Peek
  never changes `ViewState`, the URL, or the scope stack — it is free
  to explore and impossible to get lost in.
- **Commit** — click (or Enter): the peeked elements are added for real
  (scope expansion) or the view navigates (scope push), per the
  action's label. Every commit is one `Esc`/breadcrumb-pop away from
  undo.

Rows show both affordances: the row body peeks, an explicit `＋` (add
to view) or `→` (go there) button commits. Ghost rendering counts
against the element budget with a small reserved headroom (100
elements); a peek that would exceed it shows a count chip instead of
ghosts ("+38 nodes — commit to view").

### View modes & navigation flow

Land on **Service Overview** (services + aggregated cross-service
channel edges — 10–50 elements). Double-click drills: **Service
internals** (folders as collapsed compound groups) → **Folder** (files)
→ **File** (symbols + intra-file edges). At every level the canvas
shows only that scope plus its boundary edges; edges leaving scope
render as **stub connectors** (small arrow chip naming the external
target); clicking a stub offers "expand scope to include target". Other
first-class scopes reachable from any node: **Neighborhood** (related
files, depth-configurable), **Impact** (blast radius, direction toggle),
**Flow lane** (plan 12), **Structure** (classes/variables of the
current scope). Layout is per-scope (dagre LR for flows/lanes, fcose
for container scopes) and user-overridable; when dagre cannot handle
compound parents the layout picker disables it with the reason shown —
**no silent fallback** (trust contract).

### Operations UX (implemented across plans 11–13; contract set here)

- **Index**: top-bar `Index ▸` → POST job → button becomes a progress
  ring with `done/total`; Jobs tab auto-opens with the live log; Cancel
  button; on completion a toast + non-destructive "Graph updated —
  reload view?" (never yanks the canvas mid-thought). Errors land in
  the Jobs tab verbatim.
- **Tool-call log**: Tool calls tab — live SSE tail (pause/resume),
  filter chips (source/tool/status/time), free-text highlight, row
  expansion to full params/result JSON, Clear-all with confirm,
  "jump to node" links when params name a node.
- **Config**: Config activity — form sections (Services, Links,
  Excludes, Settings, Evidence) + `YAML` toggle; strict-loader
  validation inline; etag conflict → "changed on disk" prompt; save
  offers "Re-index now?".
- **Search/filter**: `⌘K` overlay with result groups (symbols · files ·
  flows · commands) and scoping chips (kind/service/confidence); Enter
  focuses the result in its scope (auto-navigating the scope stack).
  Persistent filters (confidence, edge types, services) live in one
  compact filter bar above the canvas showing an active-filter count.
- **Export/Share**: current view → PNG/SVG/JSON/Mermaid; Copy link =
  full view state in the URL. **Saved views** = named URL states
  persisted in ops.db.

### Feel

150–250 ms eased transitions for all state changes; scope changes
animate fade-dim → layout morph (Cytoscape `animate`), never above ~500
moving elements (fade-swap beyond that); skeleton loaders for panels,
shimmer for canvas fetches; empty states carry the next action ("No
index yet → Run index"); every long operation is cancellable and never
blocks interaction; `prefers-reduced-motion` respected; light + dark
themes.

### Coverage honesty on canvas

Nodes/files with unresolved refs get a ⚠ badge (click → ledger entries);
edge styling encodes verification_state (solid verified / dashed
candidate / dotted conflicting / double-line observed_only_gap).
Scope-too-big: a dialog states the exact count vs budget and offers
narrowing (pick a folder, collapse level, filter kinds) or
auto-cluster — **never silent truncation**.

### Layout gallery — every page and scenario (binding wireframes)

All pages share the shell frame (activity bar · left panel · top bar
with `◆ polyflow` · optional detail panel · optional bottom drawer);
the wireframes below show what varies. Implementers must match regions
and placements; visual styling is free within the Feel rules.

**1 · Landing / Service Overview** (default scope)
```
◆ polyflow  ⌘K        [Index ▸] [Share ▾]  2,010n/2,812e
 Overview [×]                      lens: [All][Calls][HTTP][Msg][Data][Imports]
┌────────────────────────────────────────────────────────┐
│   (nextgen)══http ×12══(cdr-agent)                     │
│      ║rabbitmq ×2          │http ×3                    │
│   (datascience)─────────(sce-agent)                    │
└────────────────────────────────────────────────────────┘
```
Lens control (plan-11 UN.5) sits right of the breadcrumbs on every
canvas page.

**2 · Drill-down scope** (service/folder/file — tree synced)
```
EXPLORER          │ ◆ polyflow ▸ nextgen ▸ app ▸ jobs [×]  lens:[…]
▾ nextgen         │ ┌────────────────────────────────┐ DETAIL
 ▾ app/           │ │ [sync.rb]──calls──[queue.rb]   │ SyncJob
  ▾ jobs/ ⚠2      │ │     │imports        ⚠         │ file:3–40
   sync.rb ◀      │ │ [util.rb]   ⇢ stub: cdr-agent │ edges·src
```

**3 · Flow lane** (isolated flow — plan 12 UF.0)
```
◆ polyflow  Flow: POST /orders → CDR consumer [×]
┌ nextgen ──────────────────────────────────────────────┐
│ [POST /orders]→[OrdersCtrl]→[publish cdr_requests]    │
├ rabbitmq:cdr_requests ─────────────pill───────────────┤
├ cdr-agent ────────────────────────────────────────────┤
│ [Consumer]→[process]→[store]                          │
└───────────────────────────────────────────────────────┘
```

**4 · Entrypoint catalog** (Flows activity)
```
FLOWS             │ ◆ polyflow · Entrypoints (34)   [route][consumer][worker]
search…           │ ▸ GET /play        chessleap  routes.go:41–44
                  │ ▸ queue cdr_reqs   cdr-agent  consumer.rb:8–31
                  │ footer: 312 callbacks / 41 unreachable hidden [show]
```

**5 · Path finder / waypoint builder** (plan 12 UF.2)
```
A: OrdersCtrl [×] → B: CDR process [×]        [Overlay all]
│ ① 4 hops · verified    ── peek on hover ──  [→ isolate] │
│ ② 6 hops · candidate                        [→ isolate] │
waypoints: (OrdersCtrl)─(publish)─(＋ pick next: 3 candidates)
```

**6 · Pinboard active** (plan 12 UF.7) — pin tray under the top bar
```
◆ polyflow   pins: [OrdersCtrl ×][cdr_requests ×][store ×]  [clear all]
canvas = only nodes/edges on paths through ALL pins; rest hidden
empty result → "No flow passes through all 3 pins — remove one?"
```

**7 · Link explorer / peek** (plan 12 UF.8, detail panel section)
```
DETAIL · SyncJob.perform          [upstream 12 | downstream 8]
filter: kind:… service:… q:…
│ ▸ OrdersCtrl.create   http   nextgen   (hover=ghost) [＋][→] │
│ ▸ Scheduler.tick      calls  nextgen               [＋][→] │
```

**8 · Group view** (plan 12 UF.4)
```
◆ polyflow · Group: 3 nodes [×]      DETAIL · relationships
[handler]──renders──[template]       calls:2 renders:1
     └──calls──[store func]          services: 1 · channels: 0
```

**9 · Health dashboard** (Health activity, canvas-free)
```
◆ polyflow · Health
[Index: 689 files · v18 · 2 parse errors]  [Coverage: ██ verified 61%…]
[Unresolved: 41 → open drawer]             [Eval: chessleap 1.000 …]
```

**10 · Config editor** (Config activity, canvas-free)
```
◆ polyflow · workspace.yaml            [Form|YAML]  [Save]
Services  | name: nextgen  path: ~/Projects/nextGen  lang: ruby [−]
Links     | nextgen → cdr-agent via rabbitmq [−]   [＋ add]
⚠ 422: services[1].path does not exist (inline, under the field)
```

**11 · Docs** (Docs activity, canvas-free)
```
◆ polyflow · Docs     [Setup|CLI|UI guide|Concepts]   search…
nav: index · serve · mcp …  │  # polyflow index
                            │  --workers, --full …
```

**12 · Bottom drawer** (Jobs / Tool calls / Unresolved / Context tabs)
```
▤ Jobs │ ⚡Tool calls │ ⚠ Unresolved │ ⧉ Context          [▁ close]
⚡ 12:01:33 mcp trace {"root":"…"} 220ms ok   [expand][→ node]
filters: [mcp][http] tool:▾ status:▾ q:…      [pause][clear all]
```

**13 · Command palette** (overlay, any page)
```
╭─ ⌘K ────────────────────────────────────────╮
│ > sync kind:function service:nextgen        │
│ SYMBOLS  ƒ SyncJob.perform  nextgen · 5–22  │
│ FLOWS    POST /orders → CDR                 │
│ COMMANDS Switch lens: Calls                 │
╰─────────────────────────────────────────────╯
```

**14 · Over-budget dialog** (any scope, US.3)
```
This scope is 4,812 elements (budget 1,500).
[Narrow: api/ 214 · web/ 1,890 · jobs/ 96 …]
[Auto-cluster to folders]  [Adjust filters]  [Cancel]
```

---

### Phase US.0 — Workbench shell + activity bar + panels `pending`

**Problem.** The current fixed layout can't host the workbench UX.

**Deliverable.** New `web/src/` structure (pinned; files may gain
siblings but these names are the contract):

```
src/
  App.tsx                 # shell grid only
  shell/{ActivityBar,TopBar,Breadcrumbs,BottomDrawer,DetailHost,
         PanelHost,Resizer}.tsx
  stores/{scope.ts,selection.ts,layoutPrefs.ts,notifications.ts}
  interaction/gestures.ts # phase US.2
  views/                  # per-activity panels (plans 11–13 fill these)
  lib/                    # surviving pure libs move here unchanged
```

Shell renders: activity bar (7 slots; unimplemented activities render a
"planned in plan-N" placeholder panel — honest, not hidden), resizable
explorer panel (drag handle, collapse to 0, width persisted in
localStorage), canvas host, detail host (closed by default), bottom
drawer (tabs registered by later plans), top bar (stats chip wired to
`/api/stats`; Index/Share buttons render disabled until plans 13/10
US.4 wire them). Light/dark theme via `prefers-color-scheme` +
localStorage override, Tailwind theme tokens defined once.

**Tests (Vitest + jsdom, colocated `.test.tsx` per repo pattern).**
Shell renders all regions; panel collapse/restore persists; activity
switch swaps panel content and keeps canvas mounted (assert the same
DOM node instance); theme toggle flips class and persists.

**Acceptance.** `npm run dev` against a served chessleap index shows the
shell with live stats; `make build` embeds and `polyflow serve` serves it.

### Phase US.1 — Scope-stack store + URL state `pending`

**Problem.** Every view must be a first-class, shareable, restorable
scope; today view state is scattered and only trace params reach the URL.

**Deliverable.** `stores/scope.ts`:

```ts
type Scope =
  | { kind: "overview" }
  | { kind: "service"; service: string }
  | { kind: "folder"; service: string; path: string }
  | { kind: "file"; service: string; path: string }
  | { kind: "neighborhood"; nodeId: string; depth: number }
  | { kind: "impact"; target: string; direction: "up"|"down"|"both"; depth: number }
  | { kind: "flow"; flow: FlowRef }        // plan 12 defines FlowRef
  | { kind: "group"; nodeIds: string[] };  // plan 12
type ViewState = {
  stack: Scope[];                // last = active
  isolation?: FlowRef;           // overlay chip
  filters: { confidence: string[]; edgeTypes: string[]; services: string[] };
  selection?: { kind: "node"|"edge"; id: string };
  layout?: string;               // per-scope override
};
```

- `push(scope)` / `popTo(i)` / `reset()`; the breadcrumb bar renders
  `stack` directly.
- **URL codec (pinned)**: full `ViewState` ⇄ `location.hash` as
  `#v=<base64url(JSON)>`; codec is versioned (`{"v":1,…}`) and rejects
  unknown versions with a visible "link from a newer version" notice
  (rule-3 spirit). Every state change replaces the hash (debounced
  250 ms); load restores state; unknown ids after reindex → notice +
  graceful fallback to overview (never a blank screen).
- `Esc` ordering per the gesture grammar implemented here.

**Tests.** Codec round-trips every scope kind (property-style: encode →
decode → deep-equal); stale node id fallback; breadcrumb pop truncates
stack; Esc ordering state machine (4 sequential presses from
full state → overview).

**Acceptance.** Copy a URL mid-drill-down, open in a new tab → identical
view.

### Phase US.2 — Shared gesture layer + detail-on-demand `pending`

**Problem.** Interactions must be identical across tree rows, canvas
nodes/edges/groups — today each component wires its own handlers.

**Deliverable.** `interaction/gestures.ts`: one module translating DOM +
Cytoscape events into semantic intents (`select`, `drill`, `menu`,
`hoverTarget`, `multiAdd`, `escape`), consumed by tree, canvas, and
future views. Context-menu component (right-click; menu items are
contributed by activities via a registry — plans 11–13 register theirs;
unimplemented items don't render). Detail host: opens on `select`,
closes on `Esc`, pin button splits into compare mode (max 2 pins).
Hover tooltips (label · kind · `file:12–48` using `end_line`, falling
back to `:12` when `end_line` is 0). Keyboard shortcuts registered in
one table (`interaction/keys.ts`) — this table is the single source for
the shortcut sheet (plan 13 docs page).

**Tests.** Same intent fired from a tree row and a canvas node produces
identical selection state; double vs single click disambiguation
(300 ms window, single-click action not lost); context-menu registry
contribution/removal; Esc reaches scope store in pinned-detail state.

**Acceptance.** Click/double-click/right-click behave identically on a
tree row and its canvas node on chessleap.

### Phase US.3 — Canvas host: budget enforcer + honest over-budget UX `pending`

**Problem.** Unbounded scopes render hairballs or freeze the tab;
truncation without saying so violates the trust contract.

**Deliverable.** `views/canvas/CanvasHost.tsx` + `views/canvas/budget.ts`:
- Every scope resolves to an element list **before** touching Cytoscape;
  `budget.ts` counts nodes+edges against `BUDGET = 1500`.
- Over budget → no render; a dialog states: exact counts ("This scope
  is 4,812 elements; the budget is 1,500"), and offers (a) narrow (pick
  a child folder/service from a list with per-child counts), (b)
  auto-cluster (collapse to folder-level compounds until under budget,
  using `lib/filegroup.ts` precedent), (c) raise filters. Choice (b)
  labels clustered nodes with contained counts ("api/ · 214 nodes").
- Layout per scope kind (fcose containers / dagre-LR flows); layout
  picker disables dagre with visible reason when compounds present
  (replaces today's silent fcose substitution — remove that code path).
- Transitions per the Feel spec: fade-dim then `cy.animate` morph
  ≤500 moving elements, fade-swap above; `prefers-reduced-motion`
  disables motion.
- Loading: shimmer overlay while fetching; fetch errors render in-canvas
  with retry (never an empty canvas without a reason).

**Tests.** Budget math (nodes+edges); over-budget produces dialog data
with exact counts and per-child narrowing counts; auto-cluster result
under budget and labels carry counts; dagre disabled-with-reason state
when compounds present; reduced-motion flag suppresses animation config.

**Acceptance.** Point at synergy (`~/projects/synergy` index): overview
renders; drilling into the largest service triggers the over-budget
dialog with real counts; auto-cluster renders under budget without lag.

### Phase US.4 — Command palette + omni-search shell `pending`

**Problem.** No fast keyboard path to anything; search is a sidebar
afterthought.

**Deliverable.** `views/palette/Palette.tsx` (`⌘K` / `/`): input with
result groups **Symbols · Files · Commands** (Flows group added by plan
12's registration); symbols/files hit `/api/graph/search` + `/api/files`
(debounced 150 ms, stale responses discarded by sequence number);
Commands come from a registry (palette actions: switch activity, change
layout, toggle theme, copy link, …; plans 11–13 contribute). Scoping
chips: kind/service parsed from `kind:route service:nextgen` prefix
syntax and clickable chips. Enter on a symbol → plan-11's focus flow
(until then: selects and pushes a neighborhood scope — note this interim
in the phase outcome). Recent items persisted (localStorage, 20 items).

**Tests.** Debounce + stale-response discard (out-of-order resolution);
chip syntax parsing; command registry contributions; keyboard-only
operation (arrow/enter/esc).

**Acceptance.** `⌘K` → type a chessleap handler name → Enter lands on it.

### Phase US.5 — Notifications, empty states, skeleton system `pending`

**Problem.** Requirement 5: long operations handled with care; today
errors and loading are ad hoc.

**Deliverable.** `stores/notifications.ts` + `shell/Toasts.tsx`: toast
queue (info/success/error, error toasts persist until dismissed, carry
a details expander with the verbatim server error). Skeleton components
for panel/list/tree. Empty-state component with action slot; pinned
empty states: no index yet ("Run index" → jobs UI or CLI instructions
until plan 13), empty scope, no search results, SSE disconnected
(banner with auto-retry countdown — reuses the `/api/events` reconnect).
An `apiFetch` wrapper (one place): JSON errors → typed `ApiError`,
notifications on 5xx, `AbortController` wiring so every in-flight fetch
cancels on scope pop (no stale renders).

**Tests.** Error toast persists + shows verbatim body; abort on scope
pop cancels fetch (assert signal); SSE-lost banner appears/clears on
reconnect; empty states render their actions.

**Acceptance.** Kill `polyflow serve` under the open UI → banner;
restart → auto-recovers.

---

## Key files

- Rewritten: `web/src/App.tsx`, all of `web/src/components/` (replaced
  by `shell/` + `views/`), `web/src/stores/`.
- Kept: `web/src/lib/{boundary,aggregate,export,confidence,filegroup}.ts`
  (+ tests), `web/embed.go`, Vite/Tailwind config, `internal/server`
  (read-only consumer of plan-9 endpoints).
- Deleted in US.0 (recorded in outcome note): old `components/`,
  `stores/{ui,graph,search,derived}.ts` — replaced wholesale.

## Traceability

| Phase | Closes |
|---|---|
| US.0 | problem 8 (layout) — shell real estate; groundwork for all |
| US.1 | export/share extra scope (URL state); problem 7 groundwork |
| US.2 | detail-on-demand decision; problems 3, 8 (consistent UX) |
| US.3 | problem 1 (thousands of nodes) — budget + honest over-budget |
| US.4 | problem 3 (search/filter) — shell portion |
| US.5 | requirement 5 (flawless long-op handling) |

## Developer use-case sweep

"I got lost — where am I?" → breadcrumbs/scope stack (US.1). "Share
exactly what I see" → URL codec (US.1). "The graph is too big" → budget
dialog (US.3). "Keyboard-only navigation" → palette + keys (US.4).
"The server died mid-session" → US.5. Declared non-goals: mobile/touch
layouts; collaborative cursors; browser-history-per-gesture (hash
replace, one history entry per scope push only).

## Explicit non-goals

- No visual-regression screenshot infra (manual acceptance per phase).
- No i18n.
- Old-UI feature parity is NOT a gate — parity arrives via plans 11–13;
  each removed capability is listed in US.0's outcome note with the
  phase that restores it (no silent drops).

## Verification

Per-phase: Vitest suites above; `npm run build` + `make build` embed
check; manual acceptance on chessleap (and synergy for US.3) recorded
in the outcome note; two-run determinism where output is produced (URL
codec, budget clustering). `internal/server` untouched except route
consumption — full `go test ./...` stays green.
