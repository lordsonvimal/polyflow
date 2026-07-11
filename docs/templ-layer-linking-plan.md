# Polyflow — Templ/JS Layer Reconnection Plan

Status legend: `pending` · `in progress` · `done (commit <sha>)`

## Goal

Polyflow's Go backend graph is healthy, but the **templ UI layer is a
disconnected island**: nothing bridges the Go ↔ templ ↔ JS boundaries, so the
entire front half of every flow floats free. Measured on chessleap (536 files,
7,915 nodes):

- **347 of 428 templ components (81%) are fully isolated** — no edge in or out.
- `contains` / `imports` / `renders` / `defined_in` / `datastar_action` edges:
  **0 of each**.
- 221 `data-on:click` handlers present, **0 parsed**; 57 `@get`/`@post`
  datastar actions present, **0 linked**.
- **378 of 428 templ components** have an identically-named generated Go
  function (`*_templ.go`) the call graph already reaches — a ready-made bridge
  that is never drawn.
- 2,267 generated `*_templ.go` function nodes double-represent every component
  and inflate the graph.

The through-line: a small number of missing bridges at the Go↔templ↔JS seams
sever `route → handler → component → datastar action → handler`. This plan
reconnects them so a complete flow is traceable and the templ layer stops
reading as dead.

**Trust contract (carried from `docs/agent-context-plan.md`):** recall over
precision for the agent path; no silent gaps (unresolved references surface,
never dropped); confidence-labeled edges. New partial resolutions (concatenated
datastar paths, template-literal selectors) ship labeled, not dropped.

Ground rules (from `docs/phases.md`): every pattern/parser change ships a
positive + negative fixture; one phase per commit; this doc is updated as each
phase completes. Parser/linker changes that alter stored shape must bump
`graph.SchemaVersion` so cached graphs re-index (`internal/indexer/indexer.go`
forces a full re-index on version change).

**Acceptance metric for the tier:** isolated templ components 347 → <50, and at
least one complete `route → handler → component → datastar action → handler`
chain traceable via `polyflow trace`. Each phase adds a chessleap-derived
isolated-node ratchet assertion.

## Per-phase process (do these in order, every phase)

1. **Unit tests** — write/extend Go unit tests + the required positive +
   negative fixtures for the pattern/parser/linker change. `go test ./...`
   green before moving on.
2. **Manual verification in chessleap** — build the binary, full-reindex
   chessleap, and inspect the graph DB to confirm the phase's edges actually
   appear on real code (see "Verifying against chessleap" below). Eyeball a
   concrete example named in the phase (e.g. `PuzzleRows`, a `data-on:click`).
3. **Benchmark** — confirm no regression in index time / graph size:
   - `make bench` runs the suite (`go test ./... -bench=. -benchtime=5s -run=^$`).
     Documented targets to hold: `BenchmarkIndexCold` (`internal/e2e`,
     synthetic 10k files < 30s) and `BenchmarkIndexIncremental100Changed` (< 3s).
   - For a parser/matcher-only change, the fast subset is
     `go test -bench=. -benchtime=5s -run=^$ ./internal/patterns/ ./internal/parser/`.
   - Real-world signal: `time /tmp/polyflow index --full` in chessleap. Record
     before/after node + edge counts (from the isolated-node query above) so the
     `> Outcome:` blockquote can cite the delta.
4. **Commit** — one phase per commit; bump `graph.SchemaVersion` if stored
   shape changed; update this doc's phase status to `done (commit <sha>)` with
   an `> Outcome:` blockquote (measured deltas), matching
   `docs/agent-context-plan.md`.

---

## Working context (read this first each session)

This section is self-contained so a fresh session can pick up any phase after
context is cleared. Each phase below lists **Entry context** (exactly what to
re-read) so you don't need the whole conversation history.

### Build, index, inspect

```bash
# Build the binary (from the polyflow repo root)
cd ~/Projects/polyflow && go build -o /tmp/polyflow ./cmd/polyflow

# Full reindex of the test project (writes ~/Projects/chessleap/.polyflow/graph.db)
cd ~/Projects/chessleap && /tmp/polyflow index --full

# Inspect the resulting graph
DB=~/Projects/chessleap/.polyflow/graph.db
sqlite3 $DB "SELECT type,count(*) FROM nodes GROUP BY type ORDER BY 2 DESC;"
sqlite3 $DB "SELECT type,count(*) FROM edges GROUP BY type ORDER BY 2 DESC;"
# Isolated nodes by type (the core metric):
sqlite3 $DB "SELECT n.type, COUNT(*) FROM nodes n
  WHERE n.id NOT IN (SELECT \"from\" FROM edges)
    AND n.id NOT IN (SELECT \"to\" FROM edges)
  GROUP BY n.type ORDER BY 2 DESC;"
```

`chessleap` is a Go + templ + Datastar + gin monolith (single service, path
`.`, ~394 Go / 108 templ / 30 JS files). It is the canonical manual-verification
target for this plan. Its `.polyflow/workspace.yaml` already registers it.

### Baseline metrics (chessleap, before any phase in this plan)

Node types: variable 2806, function 1967, method 1042, struct 531,
dom_target 436, component 428, datastore 344, http_handler 235, http_client 101,
worker 15, publisher 5, subscriber 4, class 1.
Edge types: calls 6751, captures 2452, uses_type 1001, writes 706, reads 457,
queries 208, dom_read 178, dom_listen 146, persists 135, dom_write 82,
http_call 71, navigates_to 47, datastar_bind 33, spawns 31, dom_create 30,
flows_to 24, hub_broadcast 15. **Absent: contains, imports, renders,
defined_in, datastar_action, sse_endpoint.**
Isolated: 1038/7915 total; **component 347, struct 205, variable 363,
function 86, method 36**.

### Pipeline map (where things wire)

- **`internal/indexer/indexer.go` `Run()`** — the whole pipeline: scan → parse
  (incremental, per-file cache) → Go semantic pass (go/packages) → **linking
  passes** (~lines 461–576) → root classification → atomic DB swap. Linkers are
  invoked here; a new linker pass is added with a `writeEdges(linker.LinkXxx(allNodes))`
  call alongside the others (~line 510).
- **`internal/linker/linker.go`** — cross-service/HTTP linking. `LinkRouteHandlers`
  (route→handler by label, works well) and `Linker.Link` (client→handler by
  path). The **same-service skip is at lines 185–187** (T.3). Also
  `LinkDatastores`, `LinkBrokerChannels`, `LinkHubFanout`, etc.
- **`internal/linker/js_linker.go`** — JS/TS import-aware call/read/write
  linking; component-proxy redirection; JSX event-prop resolution.
- **`internal/parser/templ.go`** — templ Visitor. Emits component nodes and
  datastar/DOM edges. **No-op visitors to implement:** `VisitTemplElementExpression`
  / `VisitCallTemplateExpression` (composition), `VisitScriptElement` /
  `VisitScriptTemplate` (script tags). Datastar regex `reDataOnAction` at line
  21; `data-on-` prefix checks at lines 142 & 247.
- **`internal/parser/go_semantic.go`** — go/packages call graph; reaches the
  generated `*_templ.go` functions.
- **`internal/patterns/`** — YAML pattern loader + tree-sitter matcher; Go/JS
  patterns live in `patterns/<lang>/*.yaml`, each with a `_test/` fixture dir
  (`input.*`, `expected.json`, `negative.*`). Matcher passes: Pass 1 decls,
  Pass 2 pattern nodes, Pass 3 call refs.
- **`internal/graph/model.go`** — Node/Edge structs, `NodeType*`/`EdgeType*`
  constants, `SchemaVersion`. New edge/node types are added here.

### Datastar conventions in chessleap (critical for T.2–T.4)

- Event syntax is **colon** — `data-on:click` (221 uses), not `data-on-click`.
  The templ AST surfaces `data-on:click={...}` as an **ExpressionAttribute**.
- Action values are Go expressions, not bare `@post('/path')`:
  - `data-on:click={ templ.JSExpression("@post('/play/" + gameID + "/draw')") }`
  - `data-on:click={ "$sig = 0; @post('/practice/" + id + "/control')" }`
- Reactive attrs present: `data-show` (166), `data-class` (62), `data-text`
  (54), `data-signals` (40), `data-bind` (25), `data-attr:*` (28), `data-when`
  (12).
- Go renders templ via `Component(args).Render(ctx, w)` (77 sites, e.g.
  `views.PuzzleRows(vm).Render(...)`); SSE push is `component.Render(ctx, buf)`
  into an SSE writer (`server/sse.go`) — **no** `PatchElementTempl`/
  `MergeFragmentTempl` in this codebase.
- JS is loaded from templ via
  `<script type="module" src={ helpers.Asset("js/liveclass-room.js") }>` (18
  files); the `src` argument needs literal resolution.

### The T.1 key insight (verify with this query)

```bash
# 378 of 428 components share a label with a generated *_templ.go Go function:
sqlite3 $DB "SELECT COUNT(DISTINCT c.label) FROM nodes c JOIN nodes f
  ON c.label=f.label
  WHERE c.type='component' AND c.language='templ'
    AND f.type='function' AND f.language='go';"
# PuzzleRows twin (component in .templ, function in _templ.go):
sqlite3 $DB "SELECT type,language,file,line FROM nodes WHERE label='PuzzleRows';"
```

---

## Tier 0 — Reconnect the Go↔templ↔JS seams (critical path)

### Phase T.1 — Unify templ components with their generated Go twin `done (commit e93fc1a)`

> Outcome: chose **link** over drop. Dropping `*_templ.go` from the parse walk
> would delete the tree-sitter function nodes the semantic call graph resolves
> against, so `go_semantic.go` (endpoint check at lines 178–180) would silently
> drop every `handler → PuzzleRows(go)` call edge — severing the exact
> reachability the phase exists to preserve. `LinkTemplComponents` instead keeps
> both halves and emits a `component_impl` bridge (confidence `static`,
> `via: templ_generated`) from the generated Go function to the templ component,
> keyed on the derived generated-file path + label (not bare label, so
> same-named components across packages don't collide).
>
> Measured on chessleap (full re-index): **395 `component_impl` edges**;
> **isolated templ components 347 → 0** (component drops out of the isolated-type
> list entirely); total isolated nodes **1038 → 691**. Node count unchanged
> (7915; link strategy keeps the generated nodes), edges 12,762. `PuzzleRows`
> verified reachable end-to-end: `handlers/puzzles.go:Rows` (gin handler)
> `-[calls]->` generated `PuzzleRows` (go) `-[component_impl]->` `PuzzleRows`
> (templ), where all its datastar/DOM edges attach. Benchmarks unchanged
> (`BenchmarkIndexCold` ~9s/1200 files, `IncrementalUnchanged` 26ms) — the pass
> is an O(nodes) map join over already-collected nodes. `SchemaVersion` 3 → 4.
>
> Graph inflation from the 2,267 generated `*_templ.go` nodes is left as-is: it
> does not affect the isolated-component metric (only 86 functions are isolated),
> and removing it safely requires redirecting the semantic call edges — deferred
> rather than risk the reachability this phase establishes.

**Entry context (re-read to start fresh):** the "Working context" section above;
`internal/linker/linker.go` (`LinkRouteHandlers` as the template to mirror);
`internal/indexer/indexer.go` lines 461–576 (linker wiring); `internal/graph/model.go`
(node/edge types, `SchemaVersion`); "The T.1 key insight" queries above.

**Problem (measured):** every templ component is represented twice and never
joined. `PuzzleRows` exists as `component/templ` at
`activity/views/puzzles.templ:394` (where all datastar actions, binds, and DOM
targets attach) **and** as `function/go` at `activity/views/puzzles_templ.go:845`
(which the go/packages call graph reaches — 2 incoming calls). 378/428
components have this twin. The Go-reachable half and the datastar-bearing half
describe the same component but live in disjoint subgraphs, so the whole templ
layer is unreachable from any route. Separately, 2,267 generated `*_templ.go`
function nodes inflate the graph and skew the isolated-node metric.

**Fix:**
1. New linker pass `LinkTemplComponents(nodes) []graph.Edge` in
   `internal/linker/` (mirror `LinkRouteHandlers`), wired into `indexer.go`
   after the semantic pass (~line 510). Match `component/templ` ↔ `function/go`
   on a path-derived key (`.templ` → `_templ.go` + label), **not** bare label,
   to avoid cross-package collisions. Emit a bridge edge (new
   `EdgeTypeComponentImpl`, `via: templ_generated`) so traversal crosses the
   seam.
2. Suppress generated glue: exclude `**/*_templ.go` from the parse walk
   (`walkService` / default excludes) so the 2,267 plumbing nodes disappear,
   while preserving the component↔handler reachability through the bridge.
   Spike link-vs-drop on chessleap and pick by isolated-node delta + graph size.

**Verify (per-phase process):**
1. *Unit tests* — linker unit test (twin match by path key, no cross-package
   mismatch); fixture for a `.templ`/`_templ.go` pair.
2. *chessleap* — reindex; isolated components drop sharply; `PuzzleRows` templ
   node gains an incoming edge from its handler's call chain; generated
   `*_templ.go` node count drops to ~0 (if drop strategy chosen).
3. *Benchmark* — full-reindex time not worse; record node/edge totals (expect a
   large node drop from removing `*_templ.go` glue).
4. *Commit* — bump `SchemaVersion`; set status + `> Outcome:`.

### Phase T.2 — Datastar action extraction: colon syntax + expression values `done (commit 13a020d)`

> Outcome: rewrote the datastar path in `internal/parser/templ.go`. `isDataOnKey`
> now recognizes both the v1 colon form (`data-on:click`) and the legacy hyphen
> form; the colon form arrives as an `ExpressionAttribute`, so it is handled
> there against the **raw** Go expression (not a pre-stripped string).
> `extractDatastarAction` reconstructs the runtime JS string from concatenated Go
> string literals (`reconstructGoString`: interpolated gaps → a sentinel byte),
> finds `@(get|post|put|delete|patch)(` anywhere via the unanchored
> `reDatastarVerb`, then `normalizeDatastarPath` walks the first quoted argument
> tracking in-string vs. in-JS-expression state, collapsing every interpolated /
> dynamic segment to a single `*` and labeling the edge+node
> `confidence: partial` (literal-only paths stay `static`). Signal bindings
> (`data-bind`/`data-signals`/`data-model`, `data-text`/`data-indicator $sig`)
> moved off `component` to a new `NodeTypeSignal`, killing the `$idx + 1` junk.
> Also made `VisitTemplElementExpression` descend into `@Layout(...){ … }`
> children so actions nested inside layout wrappers aren't dropped (this recovered
> the one `data-on:load` SSE `@get` in `session_detail.templ`).
>
> Measured on chessleap (full re-index): **datastar action nodes 0 → 27** (26
> `partial`, 1 `static`; matching all 27 `data-on:*`/`data-on-*` `@verb`
> attributes — the earlier "~250" estimate conflated total `@verb` text
> occurrences with actionable handlers), **datastar_action edges 0 → 27**,
> **signal nodes 0 → 54**, and **component nodes 428 → 395** with **0** `$`-junk
> component nodes remaining. `PuzzleRows`' pager `@get('/rows/*')` and
> `gametoolspanel`'s `@post('/play/*/draw')` verified present with `datastar=true`.
> Total isolated nodes unchanged at **691** (every new node is edge-connected);
> nodes 7915 → 8056, edges 12762 → 12959. Full re-index ~11.4s (parser change is
> O(chars) per attribute; no measurable delta). `SchemaVersion` 4 → 5.
>
> Known partials, shipped labeled not dropped: a two-`@post` ternary
> (`practice_game.templ:538`) captures only the first action; `fmt.Sprintf`
> format placeholders surface literally (`POST %s`); a JS ternary tail can leave
> cosmetic residue (`/practice/*/control/*/*engine*human`). All carry
> `confidence: partial` for T.3's wildcard linker to consume.

**Entry context (re-read to start fresh):** the "Working context" section above,
especially "Datastar conventions in chessleap"; `internal/parser/templ.go`
(`reDataOnAction` line 21, `VisitConstantAttribute` ~142, `VisitExpressionAttribute`
~247, signal-binding nodes ~161/176); a few chessleap `.templ` files with
`data-on:click` (grep `data-on:click` under `~/Projects/chessleap`).

**Problem (measured):** 221 `data-on:click` + 57 `@verb()` actions yield zero
edges. Two bugs stack in `internal/parser/templ.go`: (1) the parser only matches
the hyphen form via `strings.HasPrefix(key, "data-on-")` (`templ.go:142,247`),
but chessleap uses Datastar v1 colon syntax `data-on:click`; (2) even when
caught, the value is a Go expression —
`data-on:click={ templ.JSExpression("@post('/play/"+gameID+"/draw')") }` — and
`reDataOnAction` (`templ.go:21`) requires the value to *start with*
`@verb('/path')`, so wrapped/concatenated forms never match. Signal bindings are
also mis-typed as `component`, producing junk nodes labeled `$idx + 1`
(`templ.go:161,176`).

**Fix:**
1. Recognize both `data-on:` and `data-on-` prefixes. The templ AST surfaces
   `data-on:click={...}` as an `ExpressionAttribute`, so handle it in
   `VisitExpressionAttribute`.
2. Loosen `reDataOnAction` to find `@(get|post|put|delete|patch)\(\s*['"]([^'"]+)`
   **anywhere** in the value (covers `templ.JSExpression("@post(…)")` and
   `"$sig=0; @post(…)"`).
3. Resolve concatenated paths (`"@post('/play/" + gameID + "/draw')"`): keep the
   literal prefix, replace interpolated segments with `*`, emit
   `confidence: partial`.
4. Give signal bindings their own node type (or a `signal` type) instead of
   `component`, removing the `$idx + 1` pollution.

**Verify (per-phase process):**
1. *Unit tests* — parser fixtures for colon syntax, `JSExpression` wrapper, and
   concatenated path (positive) + a non-datastar `data-on:click` (negative).
2. *chessleap* — reindex; datastar http_client nodes go 0 → ~250; eyeball a
   known `@post('/play/.../draw')` node exists with `datastar=true`; no more
   `$idx + 1` component junk nodes.
3. *Benchmark* — reindex time not worse.
4. *Commit* — bump `SchemaVersion`; set status + `> Outcome:`.

### Phase T.3 — Link datastar actions → handlers, including same-service `done (commit 51d64cc)`

> Outcome: dropped the same-service skip for datastar clients only
> (`internal/linker/linker.go`): a templ action node reaching its own gin
> handler has no bridging `calls` edge, so for a monolith the loop only closes
> if the edge is emitted here. The edge keeps the existing shape — `http_call`
> with `via: datastar_action` — so no stored-shape change and `SchemaVersion`
> stays 5. Plain same-service HTTP still skips (a `calls` edge already covers
> it).
>
> Making T.2's partial paths match required three matcher fixes, all scoped so
> concrete (cross-service) paths are untouched: (1) **symmetric wildcards** —
> `*` on either side matches any segment, since T.2 puts the wildcard on the
> *client* side (`/play/*/draw`) while routes put it on the *handler* side
> (`:gameID`); (2) a **shared-anchor guard** — a wildcard-bearing path must
> share ≥1 concrete segment with the handler, or wildcards alone would align
> unrelated same-shape routes (`/play/*/draw` spuriously matching `/*/goto/*`);
> (3) **query-string stripping** on the client path
> (`…/history/navigate?direction=1`). Fully-wildcarded paths (`@get(url)` → `*`)
> carry no anchor and surface as unresolved rather than blind-matching.
>
> Measured on chessleap (full re-index): of 27 datastar action nodes, **3 link
> to a real handler** (`GET */*/board-at/live` → `GET /play/:gameID/board-at/:halfMoveIdx`,
> and two `POST */*/history/navigate` → the play/solo navigate handlers); the
> other 24 stay **unresolved** (surfaced, not dropped) — most are handlers
> nested under a `r.Group("/play")` whose stored path drops the group prefix
> (`/:gameID/draw`), a separate route-group-prefix gap left for a future phase.
> Without the shared-anchor guard the symmetric wildcards produced **22 garbage
> links** (e.g. `/play/*/draw` → `/*/goto/*`); the guard cuts those to 0.
> Edges 12,959 → 12,962 (+3), nodes unchanged at 8,056, isolated unchanged at
> 691 (isolated components still 0). Full re-index ~11.1s (linker pass is
> O(clients×handlers), no measurable delta).
>
> Headline acceptance met — a complete loop is traceable via `polyflow trace`:
> `ShowPlayGame -[calls]-> PlayGamePage -[calls]-> MoveNotationPanel
> -[component_impl]-> MoveNotationPanel(go) -[datastar_action]-> POST
> */*/history/navigate -[http_call]-> POST /play/:gameID/history/navigate
> -[calls]-> NavigateHistoryHandler` — i.e. route → handler → component →
> datastar action → handler.

**Entry context (re-read to start fresh):** "Working context" above;
`internal/linker/linker.go` `Linker.Link` (the **same-service skip at lines
185–187**, `isNavLink` handling, `resolveHandler`); confirm T.2 has landed
(datastar client nodes must exist first).

**Problem:** for a monolith, `Linker.Link` actively drops the edge that
completes the loop. `internal/linker/linker.go:185-187` skips a client→handler
match when `client.Service == handler.Service`, assuming a `calls` edge already
covers it — but a datastar action (templ node) → gin handler (Go node) has no
such `calls` edge, so the link vanishes. Nav-links (`href`) are kept
same-service; datastar actions are not — inconsistent and fatal for a
single-service app. (Depends on T.2 producing the client nodes.)

**Fix:** for datastar clients (`Meta["datastar"]=="true"`), emit an edge even
when `client.Service == handler.Service` (type `datastar_action`, or `http_call`
with `via: datastar_action`). Keep the skip for genuine intra-service HTTP where
a `calls` edge already exists. Ensure normalized path matching handles the `*`
wildcards from T.2's partial paths.

**Verify (per-phase process):**
1. *Unit tests* — linker test: same-service datastar match emits an edge while
   plain same-service HTTP still skips.
2. *chessleap* — reindex; `datastar_action` edges > 0; trace a complete
   `route → handler → component → action → handler` chain via `polyflow trace`
   (this is the tier's headline acceptance check).
3. *Benchmark* — reindex time / edge count sane.
4. *Commit* — set status + `> Outcome:` (bump `SchemaVersion` only if edge shape
   changed).

### Phase T.4 — `renders` pass: `Component(...).Render(...)` + SSE fragment streaming `pending`

**Entry context (re-read to start fresh):** "Working context" above (the "Go
renders templ via `.Render`" note); T.1's component index (reused here);
`internal/parser/go_semantic.go` and `patterns/go/*.yaml` + a `_test/` fixture
dir as the pattern template; `~/Projects/chessleap/server/sse.go` for the SSE
streaming shape.

**Problem (measured):** the reverse direction (Go handler → templ view) is
invisible. 77 `X.Render(ctx, w)` call sites (`views.PuzzleRows(vm).Render(...)`,
`components.RenderChessBoard(...).Render(...)`) have no detecting pattern.
chessleap's dominant server-push pattern is datastar fragment streaming —
`component.Render(ctx, sseWriter)` into an SSE response (`server/sse.go`), with
**0** `PatchElementTempl`/`MergeFragmentTempl` present — so SSE endpoints emit no
`sse_endpoint` edge and no `renders` edge to the component they stream, which is
why the 5 SSE publishers read as isolated.

**Fix:**
1. New Go pattern (`patterns/go/templ_render.yaml`) or semantic-pass rule:
   detect `<expr>.Render(ctx, w)` where `<expr>` resolves to a templ component
   call; emit a `renders` edge from the enclosing func to the component node
   (reuse T.1's component index).
2. Detect SSE streaming handlers (a `.Render(ctx, buf)` where the buffer/writer
   is an SSE response, plus `PatchElementTempl`/`MergeFragmentTempl` where the
   SDK is used): tag `sse_endpoint` and `renders` the streamed component. Key
   off the SSE response, not the datastar SDK name (chessleap streams via raw
   `.Render`).

**Verify (per-phase process):**
1. *Unit tests* — Go pattern fixtures (component `.Render`, SSE-writer `.Render`)
   positive + a non-templ `.Render` negative.
2. *chessleap* — reindex; handlers gain outgoing `renders` edges; the 5 SSE
   publishers stop reading as isolated.
3. *Benchmark* — reindex time sane.
4. *Commit* — bump `SchemaVersion` if a new edge/node type was added; set status
   + `> Outcome:`.

### Phase T.5 — templ→JS `<script>` loading + JS DOM-target → templ `defined_in` `pending`

**Entry context (re-read to start fresh):** "Working context" above (the
`helpers.Asset(...)` script note); `internal/parser/templ.go` no-op visitors
`VisitScriptElement`/`VisitScriptTemplate` (lines 297/353) and the attribute
visitors that already read `id=`/`class=`; the JS DOM patterns under
`patterns/javascript/dom_*.yaml`.

**Problem:** the JS↔templ seam is empty. `VisitScriptElement` /
`VisitScriptTemplate` (`internal/parser/templ.go:297,353`) are no-ops, so the 18
templ files that load JS via
`<script type="module" src={ helpers.Asset("js/liveclass-room.js") }>` link to
nothing; the `src` is a Go expression needing argument resolution. Separately, JS
creates DOM-target nodes (178 `dom_read`, 146 `dom_listen`) but nothing links
them back to the templ `id=`/`class=` that defines them — the design's promised
`defined_in` edge is absent (0 in the graph).

**Fix:**
1. Implement `VisitScriptElement`/`VisitScriptTemplate`: extract `src`, resolve
   the `helpers.Asset("js/x.js")` literal argument to a path, emit an
   `imports`/`loads` edge from the templ file to the JS file node.
2. New `defined_in` pass: match JS DOM-target selectors (`#id`, `.class`) to
   templ elements carrying that `id=`/`class=` (the templ parser already visits
   attributes — emit definition nodes to match against). `confidence: static`
   for literal selectors, `partial` for template literals.

**Verify (per-phase process):**
1. *Unit tests* — templ fixture with `<script src={ helpers.Asset(...) }>` and an
   element `id`; parser/linker tests for selector→element matching.
2. *chessleap* — reindex; script-loading edges for the 18 templ files; DOM
   targets gain `defined_in` edges (0 → many).
3. *Benchmark* — reindex time sane.
4. *Commit* — bump `SchemaVersion` if edge/node shape changed; set status +
   `> Outcome:`.

---

## Tier 1 — Structural backbone & reactive richness (independent of Tier 0)

### Phase T.6 — `contains` backbone + reactive datastar attributes `pending`

**Entry context (re-read to start fresh):** "Working context" above;
`internal/indexer/indexer.go` (add containment during writer/assembly, not
per-parser); `internal/graph/model.go` (`EdgeTypeContains`, node parent/file
metadata already present); `internal/parser/templ.go` for the reactive-attr
extraction.

**Problem (measured):** service→file→function/method/struct containment is
entirely absent (0 `contains` edges), which is why 205 structs and 86 functions
are isolated. For the agent-context recall goal this is the biggest gap: an agent
can't ask "what's in this file" or "what methods hang off this struct" — behavioral
edges alone can't answer structural questions. Separately, reactive datastar
attributes are skipped entirely: `data-show` (166), `data-class` (62),
`data-attr:*` (28), `data-when` (12), `data-computed`, `data-on-signal-patch` —
each is a signal read that enriches recall.

**Fix:**
1. Emit `contains` edges service→file→{function,method,struct,component} and
   struct→method from existing node file/parent metadata (data already present).
   Add during indexer/writer assembly, not per-parser, to keep it uniform.
2. Handle reactive attrs in the templ parser: `data-show`, `data-class`,
   `data-attr:*`, `data-when`, `data-computed`, `data-on-signal-patch` → `reads`
   edges on the referenced signals.

**Verify (per-phase process):**
1. *Unit tests* — containment assembly + reactive-attr extraction (positive + a
   non-signal `data-*` negative).
2. *chessleap* — reindex; isolated structs (205) and orphan funcs drop to
   near-zero; total isolated-node count falls well below the current 13%.
3. *Benchmark* — `contains` adds a lot of edges; confirm index time and DB size
   stay acceptable.
4. *Commit* — bump `SchemaVersion`; set status + `> Outcome:`.

---

## Sequencing / dependencies

```
T.1 ──┬─> T.3 (needs T.2) ─> full-loop trace
T.2 ──┘
T.4 (needs T.1's component index)
T.5 (independent, after T.1)
T.6 (independent)
```

T.1–T.3 are the critical path to "a complete flow exists" — land them as a
vertical slice first, then T.4–T.6 for breadth. T.6 is the largest single win
for agent-context recall but blocks nothing, so it can land last.
