# Polyflow — Static End-to-End Flow Coverage Plan (Tier Y)

Status legend: `pending` · `in progress` · `done (commit <sha>)`

**Goal.** Push *static* analysis to maximum coverage of the full end-to-end causal
chain — **DOM event → http call → route handler → db query → response type → render** —
without runtime traces, by joining clues the parsers already extract and adding a few
declarative-construct extractors. Companion to `docs/systemic-gaps-plan.md` (Tier X): X
resolves cross-service *edges*; Y assembles those edges into connected *flows* and cleans
the dangling-node noise that makes the graph look sparser than it is. The runtime half
(actual-path selection, response↔render correlation without shared types) is explicitly
**out of scope** — it belongs to Tier X.6 (OTel fusion). See "Honest ceiling" below.

**Prerequisites banner.** Reuse, do not rebuild:
- `docs/systemic-gaps-plan.md` (Tier X). **Y.3 (http→handler URL join) IS Tier X.1** applied to
  this repo's own frontend↔backend pair — the KeyWalker template reconstruction and the
  `param_wildcard`/`url_to_path` normalizers. Do not fork a second URL matcher.
- `docs/phases.md` — the 12 bug-class rules bind on every phase here, especially #1 (fan-out,
  never first-match), #8 (test code is production code — never exclude as a class; but Y measures
  on the *real-source* graph, see Baseline), #12 (exhaustive intake: every element reaches output
  or the ledger — an unresolvable dynamic clue is **ledgered**, never a fabricated edge).
- `internal/parser/go_semantic.go`, `internal/parser/go_variables.go` — the Go SSA + type-checker
  passes. Y.1/Y.2/Y.4/Y.5 extend these.
- `internal/parser/js_variables.go` — the JS/TS tree-sitter pass (signals/setters tracked). Y.6/Y.7
  extend this.
- `internal/contract/keywalk_{go,javascript}.go` + `internal/contract/normalize.go` — the URL/key
  reconstruction engine reused by Y.3.

Self-contained per project convention: implementable by a contributor who reads this file plus the
one owning plan it names (Tier X for Y.3).

Bump `graph.SchemaVersion` in any phase changing stored node/edge shape (Y.1 removes nodes; Y.4/Y.5
add edge classes; Y.6/Y.7 add node/edge classes).

---

## Baseline (measured 2026-07-27, clean reindex)

After excluding swept-in fixtures (`eval/**` added to `polyflow.yml`'s `index.exclude`, stale
`.polyflow/graph.db` removed, `polyflow index --full`):

```
Nodes: 2975 | Edges: 5801 | Cross-service links: 78
```

**Dangling nodes (no edge either direction): 341 / 2975 = 11.5%.** Breakdown:

| type | lang | count | root cause |
|---|---|---|---|
| interface | go | 204 | **203 are external stdlib/3rd-party stubs** (`context.Context`, `encoding/json.Marshaler`) minted eagerly in `extractImplements`; only 1 real project interface (`rowScanner`) dangles → Y.1 |
| variable | go | 116 | **109 are package `const`** (SSA inlines them as literals → no `reads` edge is structurally possible); 7 are genuinely-unused package `var`s (correct to dangle) → Y.2 |
| interface | typescript | 17 | TS types with no `uses_type`/`implements` edge (JS type-relation pass gap) → Y.1 note |
| variable | typescript | 4 | module-scope stores never read across module boundary → Y.2 note |

**End-to-end chain status (measured, this repo's own frontend↔backend):**

| Hop | Edge that carries it | Status | Count |
|---|---|---|---|
| 1. DOM event → handler fn | `dom_listen` (fn→dom_target) | near-empty; targets a `dom_target` abstraction, not `element` nodes | 2 |
| 2. handler fn → http call | `calls` (function→http_client) | ✅ works | 41 |
| 3. http_client → route handler | `http_call` (→http_handler) | 72% break (templated/wildcard URLs unresolved) | 11 resolved / 28 → `unresolved` |
| 3b. route → handler fn | (handler meta `handler:"s.handleNode"`) | ❌ string present, never resolved to the fn node | 0 |
| 4. handler → db | should be fn → `queries`/`persists` | ❌ `queries`/`persists` are `datastore→datastore`, detached from caller | 0 from handler |
| 5. db result → response type | (Encode/decode DTO type) | ❌ neither side records the DTO type | 0 |
| 6. render DOM | `renders` (fn→fn) / dom_write | ❌ modeled fn→fn; no resource→signal→element dataflow, no response tie-in | 13 fn→fn |

Only hop 2 is solid today. **The clues for the rest are largely already captured in node `meta`**
(handler `path`+`method`+`handler`, client wildcarded `url`, query-node SQL+`op`) — the gap is
*joining* them, not *extracting* them. That reframing drives the phase order below.

---

## Part A — Dangling-node cleanups (correctness of the sparsity signal)

These do **not** add flow edges; they remove noise that makes coverage look worse than it is and
that pollutes any "% of nodes connected" trust metric.

### Phase Y.1 — Lazy external-interface stub nodes `done`

**Measured outcome (clean reindex after fix):** nodes 2975→2772 (**203 external stub nodes removed**),
edges unchanged 5801 (**no `implements` edge lost**), cross-service links unchanged 78, **0 orphan-endpoint
edges**. Dangling Go interfaces 204→**1** (the real `rowScanner`, to be resolved by Y.5 `uses_type`). The
17 dangling TS interfaces are the separate `js_type_relations.go` gap (TS note below), untouched here.


**Problem.** `extractImplements` (`internal/parser/go_semantic.go:915-1054`) creates a synthetic
`NodeTypeInterface` node (`syntheticIfaceID`, line 959) for **every** exported interface with ≥1
method in **every** imported package, inside the import-collection loop (line 1004) — *before*
checking satisfaction. The `implements` edge is only added (line 1042) when an in-service struct
actually satisfies it. Result: 203 external interface stubs with no edge (214 external minted, 11
edged).

**Fix.** Make node materialization lazy — a stub node exists **iff** an `implements` edge targets it.
Move the `syntheticIfaceID(...)` call out of the collection loop (line 1004) into the satisfaction
branch (line 1042). Collect only the `*types.Interface` candidates in the loop; mint the node on
first successful `types.Implements`:

```go
// collection loop: store iface only, no node yet
seenExtIface[key] = extIfaceEntry{iface: iface, pkgPath: imp.Path(), name: name}
...
// satisfaction branch:
for _, entry := range seenExtIface {
    if types.Implements(T, entry.iface) || types.Implements(ptrT, entry.iface) {
        nodeID := syntheticIfaceID(entry.pkgPath, entry.name) // now lazy — mints on demand
        addEdge(structID, nodeID, map[string]string{"nominal": "false", "external": "true"})
    }
}
```

**Outcome (predicted).** Dangling interfaces 204→≤2 (the 1 real `rowScanner` + any project iface
with no implementor — investigate `rowScanner` separately; it may be an unexported interface used
only as a param type, addressed by Y.5 `uses_type`). No `implements` edge is lost (edges already
gate on `types.Implements`). Zero behavioral change to real edges.

**TS note.** The 17 dangling TS interfaces are a separate, smaller gap in
`internal/linker/js_type_relations.go` (no `uses_type` edge from a function/param to a TS type
alias). Optional follow-on; low value (frontend-only types), do after Part B.

**Test.** Fixture package that imports `io` and `encoding/json` but implements neither → assert
**zero** `interface` nodes minted for `io.Reader`/`json.Marshaler`. Second fixture where a struct
implements `io.Reader` → assert exactly one `io.Reader` node + the `implements` edge.

### Phase Y.2 — Const-read edges via type-checker Uses `done`

**Problem.** Package-level `var`s get `reads`/`writes` edges because they are `*ssa.Global`s the SSA
instruction walk can see (`internal/parser/go_variables.go:308-382`, `rootGlobal` peel). Go `const`s
are compile-time — SSA inlines them as `*ssa.Const` literals at each use, so they never appear as an
`*ssa.Global` and **no `reads` edge is structurally possible via SSA**. 109 const nodes are minted
(go_variables.go:168) and dangle.

**Fix.** Add a syntactic pass (AST + type-checker, *not* SSA) alongside the SSA walk. For each
package, walk `*ast.Ident`s; where `pkg.TypesInfo.Uses[ident]` is a `*types.Const`, resolve the
enclosing function node and emit a `reads` edge fn→const-node. The const node ID is **already**
registered by qualified name `<pkgPath>.<Name>` in `qualifiedNameIDs` (go_variables.go:178) — the
exact hook this pass needs; no new node bookkeeping.

```go
// per file, per ident:
if c, ok := pkg.TypesInfo.Uses[ident].(*types.Const); ok {
    key := c.Pkg().Path() + "." + c.Name()
    if constID, ok := qualifiedNameIDs[key]; ok {
        if fnID, ok := enclosingFuncID(ident.Pos()); ok {
            addEdge(graph.EdgeTypeReads, fnID, constID,
                map[string]string{"kind": "const", "via": "type_uses"})
        }
    }
}
```

**Outcome (predicted).** Dangling Go vars 116→~7 (the genuinely-unused `var`s, correctly left
dangling — do **not** fabricate edges for them; #12). `reads` edge count rises materially.

**Test.** Fixture with `const Foo = 3` used in two functions and unused const `Bar` → assert two
`reads` edges to `Foo`, zero to `Bar`, and `Bar` still dangles (unused is a real state, not a bug).

**Measured outcome (shipped 2026-07-28).** A typed-AST const-read pass *already existed* (the B.2
`cross-package` pass, go_variables.go:481+) but explicitly skipped same-package consts
(`c.Pkg() == p.Types`) on the false assumption "SSA handles it." The fix was a one-line correction:
skip only builtins (`c.Pkg() == nil`, i.e. `true`/`false`/`iota`) and let same-package consts flow
through the same enclosing-function-range attribution. No new node bookkeeping (const nodes are
already keyed in `qualifiedNameIDs`). Reads edges 845→969 (+124); total edges 5801→5925; nodes
unchanged 2772; cross-service links unchanged 78; 0 orphan-endpoint edges (#10). Dangling Go consts
109→49, total dangling 341-lineage now 78.

The residual 49 (vs. the predicted ~7) is *not* under-attribution — it splits into two correct-to-
dangle classes (#12): **(a) 39 are test-fixture consts in `_test.go`** used only inside `TestXxx`
functions, and the indexer mints no function node for test functions (they are not service surface),
so there is no enclosing node to attribute a read to; **(b) 10 non-test** are either genuinely unused
(`RoleProducer`/`RoleConsumer`, `EdgeTypeSidekiqEnqueue`, `NodeTypeTemplElement` — no reference
anywhere) or referenced only inside **package-scope `var … = map{…}` initializers**
(`graph.EdgeTypeDeclares` in mermaid.go/yield.go) where there is no enclosing function. Emitting a
function-read edge for either would fabricate (#12), so both correctly dangle. The 7 dangling `var`s
and 4 TS consts are likewise genuinely-unused. Deviation ledgered: the spec framed this as a new pass;
it was in fact an unblock of an existing one.

---

## Part B — End-to-end flow assembly (maximum static coverage)

Ordered ROI-first: pure joins (existing clues, no new parsing) before new extractors. Every phase
adds real chain edges and is independently useful.

### Phase Y.3 — http_client → route → handler-fn (the request half) `done`

**Measured outcome (shipped 2026-07-28).** On inspecting the live graph, **two of the three
proposed joins were already satisfied** and the third was already connected — only the db-terminus
entity was missing. What actually shipped is the table extraction (Y.3c's second half); the rest is
ledgered here as already-present so the plan reflects reality, not the prediction.

- **Y.3a (route → handler-fn): already done** by `linker.LinkRouteHandlers` (existing pass). 13/15
  `http_handler` nodes resolve to their function via receiver-stripped label lookup and emit `calls`.
  The 2 that don't (`http.NotFoundHandler()`, `http.FileServer(...)`, and the OTel `r.handleHTTP`
  registered with no method) reference stdlib/external constructors with no in-service function node —
  correct to leave unresolved (#12), not a gap.
- **Y.3b (http_client → http_handler): already done** by the contract engine's keywalk. **All 11
  genuine frontend `fetch` sites resolve — including the templated ones** (`/api/node/*` →
  `/api/node/{id}`, `/api/variable/*/flow` → `/api/variable/{id}/flow`, `/api/node/*/source`,
  plus query-string cases). The plan predicted "11 literal resolve, 28 templated unresolved needing
  KeyWalker" — **that premise was wrong for this repo**: the templated URLs already reconstruct, and
  the "28 unresolved" `http_client → service(unresolved)` edges are **misclassified non-HTTP string
  nodes** (localStorage keys `pf:layout`, sort directions `forward`/`backward`, export formats
  `svg`/`png`) minted by the JS parser, *not* templated URLs. That is a JS-parser **precision** issue
  (opposite of a coverage gap) and out of Tier-Y scope; recall-over-precision does not license chasing
  it here. No KeyWalker work was needed.
- **Y.3c (handler-fn → db): re-anchor already connected; table terminus added.** The predicted
  "re-anchor the query edge from the call node to the enclosing function" is **already achieved** by
  the SSA `calls` edge: every one of the 42 datastore call nodes has an incoming `calls` from its
  enclosing function/method, so `handler-fn → calls → repo-fn → calls → callNode → queries → store` is
  already a connected path — and that shape is *richer* than the plan's `function → store` because it
  keeps the SQL on the call node. Emitting a parallel `function → store` edge would duplicate an
  existing path (#12), so it was **not** added. The one genuinely-missing piece — the query
  terminating at a real entity instead of an opaque driver — shipped as new pass
  `linker.LinkTables`: it parses the table from `meta.sql` (first `FROM`/`INTO`/`UPDATE` target, with
  subquery-skip so `FROM ( SELECT … FROM t )` resolves to `t`) and emits `callNode → table`
  (`queries`/`persists`, confidence `static`). The call node is itself `type=datastore`, so this
  edge *is* the plan's `datastore → table`. **Measured:** 12 table nodes minted (`meta`, `nodes`,
  `edges`, `parse_errors`, `file_hashes`, `unresolved_refs`, `semantic_cache`, `unresolved_history`,
  `embeddings`, `nodes_fts`, `dependencies`, `entities_fts`), 38 `datastore → table` edges (21
  queries + 17 persists), 0 orphan-endpoint edges (#10), 0 dangling tables. PRAGMA / multi-statement
  SQL with no owning table minted nothing (#12). Nodes 2772→2788.

**Net:** the forward request half `fetch → http_call → http_handler → calls → handler-fn → … →
callNode → queries → table` is a fully connected static path for every genuine literal-or-templated
fetch in this repo, now terminating at a real table entity. The plan's KeyWalker/re-anchor work was
unnecessary because the machinery already existed; only the table terminus was new.

---

Three joins that connect hops 3, 3b, 2→4 (original spec, retained for reference):

**Y.3a — route → handler-fn (hop 3b, trivial join).** The `http_handler` node carries
`meta.handler` = `"s.handleNode"` (a `recv.Method` string) and `meta.fn` = registration call. Resolve
that string to the handler function node (receiver-qualified name lookup, same resolver the `calls`
pass uses) and emit an edge `http_handler → function` (type `handles`, or reuse `contains`+meta).
Pure resolution of a string already in `meta`.

**Y.3b — http_client → http_handler (hop 3) = Tier X.1 here.** Both endpoints already carry the
clue: client `meta.url` = `/api/node/*` (wildcarded), handler `meta.path` = `/api/node/{id}`. The
literal cases already resolve (11); the wildcard/templated cases (28 → `unresolved`) are exactly the
Tier X.1 KeyWalker template-reconstruction + `param_wildcard`/`url_to_path` normalizer job. **Wire
this repo's Go client `key_dynamic:true` sites** (`meta.key_dynamic_raw:"url"`) and the frontend
`fetch` wildcard URLs through `internal/contract/keywalk_*.go`. Do **not** write a second matcher.

**Y.3c — handler-fn → db (hop 4 attribution).** Today `queries`/`persists` edges are
`datastore→datastore` (the SSA groups `sql.Open`→`QueryContext` internally). The query node
(`meta.sql`, `meta.op:query|persist`) sits at a known file/line *inside a function*. Re-anchor the
edge source to the **enclosing function** node: emit `function → datastore` (`queries`/`persists`),
so `handler-fn → repo-fn → … → datastore` becomes a connected path via the existing `calls` chain.
Also parse the table name out of `meta.sql` (first `FROM`/`INTO`/`UPDATE` token) and emit
`datastore → table` so the db target is a real entity, not an opaque driver node.

**Outcome (predicted).** For literal-and-reconstructable URLs, the forward path
`fetch → http_call → route → handler-fn → calls → repo-fn → queries → table` is fully connected.
Measure: # of `http_client` sites reaching a `datastore` within N `calls` hops (target: literal
cases 100%, templated cases per X.1 yield ≥95% cross).

**Test.** Fixture with a Solid `fetch("/api/node/" + id)` and a Go `GET /api/node/{id}` handler that
runs `SELECT … FROM nodes` → assert one connected path client→handler-fn→`nodes` table.

### Phase Y.4 — Response-type extraction + type-shape join (the return half) `done`

**Measured outcome (shipped 2026-07-28).** All three sub-parts landed as specified; the return
half now connects end-to-end. New edge types `returns`/`consumes`/`response_of` (schema 20→21,
forced full re-index); no new node types (edges-only pass). Verified against the live graph:

- **Server `returns`** (typed-AST pass in `go_variables.go`, after the const-ref loop). Resolves
  the payload type at each JSON sink via `types.Info.TypeOf`. Sinks recognised: `encoding/json`
  `Marshal`/`MarshalIndent`, `(*json.Encoder).Encode`, and — the wrinkle this repo actually uses —
  any **ResponseWriter-first wrapper** (`writeJSON(w, status, v)`), detected *structurally* by its
  signature, not by name. **30 `returns` edges.** The two real HTTP handlers resolve
  (`handleGraph`/`handleTrace → CytoscapeGraph`, `handleSearch → {graph.Node, semantic.Response}`);
  the rest are honest CLI/internal `json.Marshal` sites. Untyped bodies (`map[string]any`, most
  handlers) emit **no** edge (ledgered, #12). Needed a B.2-style qualified-name fallback for
  `structIDs` (`structIDsByQName`): cross-package response structs (`graph.Node`, `semantic.Response`)
  missed on pointer identity because the type-checker built a test variant — same failure mode B.2
  fixed for globals.
- **Client `consumes`** (tree-sitter pass in `js_variables.go`). Fires on typed `await res.json()`
  — annotated (`const d: NodeDetail = …`) or asserted (`… as GraphNode[]`). **3 `consumes` edges**
  (`fetchGraph`/`fetchTrace → CytoscapeGraph`, `Detail → NodeDetail`). Resolves against same-file
  interfaces only; the one cross-file/imported decode target (`GraphNode` in `search.ts`) and every
  untyped `.json()` are ledgered (#12), no fabricated endpoint. Cross-file TS decode resolution is a
  deliberately-deferred residual (would need the import-aware linker path).
- **Join `response_of`** (`linker.LinkResponseShapes`, gated on `returns` targets). Matches each
  server-declared response struct against every non-Go interface by **JSON field-name set** (json
  tag → wire name), Jaccard ≥ 0.8 and ≥ 2 shared fields. **4 `response_of` edges**, all genuine
  cross-language mirrors: `CytoscapeGraph↔CytoscapeGraph` (1.00), `graph.FileSummary↔FileResult`
  (1.00), `graph.Node↔{GraphNode (0.80), CytoscapeNodeData (0.89)}`. Gating on the (small) server
  returns set kept it from exploding while recall stayed high — every shape-equivalent client type
  is linked, each edge carrying `shared`/`jaccard` so a consumer can rank.

**End-to-end (verified):** `http_handler(/api/graph) → handleGraph → returns → CytoscapeGraph →
response_of → TS CytoscapeGraph ← consumes ← fetchGraph/fetchTrace`. 0 orphan-endpoint edges (#10);
the previously-dangling TS DTO interfaces are now connected. Tests: `TestLinkResponseShapes`,
`TestGoStructJSONFields`, `TestGoY4_Returns`, `TestJSY4_Consumes` (+ schema-version bump); 702 pass
across parser/linker/graph/indexer/server/contract, vet clean. The one e2e failure
(`TestChain_RailsBunnyRabbitGoConsumer`) is the pre-existing `testdata` go.sum issue, unrelated.

<details><summary>Original plan (unchanged, for reference)</summary>

**Problem (hop 5).** The response is not runtime-only — its **type is statically declared**. A
handler does `json.NewEncoder(w).Encode(resp)` where `resp : *NodeDetail`; the client does
`const d: NodeDetail = await r.json()` or `var out BuildStatus; json.Unmarshal(body, &out)`. Neither
side records the DTO type today.

**Fix — extract:**
- **Server:** on the SSA/AST for a handler, capture the static type of the argument to the response
  writer (`Encode(x)`, `json.Marshal(x)`, `c.JSON(code, x)`, templ `Render`). Stamp
  `meta.response_type` = the fully-qualified type on the `http_handler` node, and emit
  `http_handler → type-node` (`returns`).
- **Client:** capture the decode-target type (`r.json() as T` in TS, `Unmarshal(&out)` in Go) at the
  `http_client` site; stamp `meta.response_type`, emit `function → type-node` (`consumes`).

**Fix — join:** for same-language monorepo pairs, the type node identity already links producer and
consumer (same `*types.Named`). For **cross-language** (this repo: Go backend ↔ TS frontend), match
by **JSON shape**: field-name set + JSON tags (Go `json:"x"`) vs TS interface members. Emit a
`response_of` edge (confidence `static`, `meta.match:shape`) when field sets match within a
threshold; ledger (#12) when the payload is untyped (`map[string]any` / `any`) — never guess.

**Outcome.** Closes hop 5: "this handler's result is this DTO, consumed by this component." Enables
the render tie-in in Y.6.

**Test.** Go handler encoding `type NodeDetail struct{ ID string \`json:"id"\`; Label string }` and
a TS `interface NodeDetail { id: string; label: string }` decode → assert one `response_of` edge by
shape. A handler returning `map[string]any` → assert a ledger entry, **no** edge.

</details>

### Phase Y.5 — Interface param/return `uses_type` + interface-dispatch calls `done`

**Outcome (measured, clean reindex).** Both parts landed as specced, schema 21→22.
- **Y.5a `uses_type`** — extended the existing struct `uses_type` `checkType` closure (go_variables.go)
  to also resolve interface params/returns via `interfaceIDs` (+ a B.2-style `interfaceIDsByQName`
  qualified-name fallback). **24 `uses_type`→interface edges.** The last real dangling project
  interface `rowScanner` now has **2 incoming** `uses_type` — dangling Go interfaces 1→**0**.
- **Y.5b dispatch `calls`** — on SSA `common.IsInvoke()` (handled *before* the concrete-callee
  `continue`, since invoke's `StaticCallee` is nil), mint a synthetic interface-method node
  (`NodeTypeMethod`, ID `<ifaceID>:m:<Method>`, meta `kind=interface_method`, `interface=<ifaceID>`)
  and emit `caller → interface-method` `calls` (meta `via=invoke`). **13 interface-method nodes,
  21 dispatch `calls`** where previously `calls`-to-interface = 0. Real blast radius surfaced:
  `Embedder.Embed`, `KeyWalker.WalkKey`, `Parser.Parse`, `Provider.Collect`, `NodeSearcher.SearchNodes`.
- Nodes 2788→**2824**, edges →**6114**, **0 orphan-endpoint edges** (#10). Tests
  `TestGoY5_UsesTypeInterface`/`TestGoY5_DispatchCalls` + schema bump; `go vet` clean.

<details><summary>Original spec</summary>

Two additions that make interfaces first-class in the call graph (and resolve the last real dangling
project interface):

**Y.5a — `uses_type` for interface params/returns.** When a function has an interface-typed
parameter or return (incl. the currently-dangling `rowScanner`), emit `function → interface`
(`uses_type`). Mirrors the existing struct `uses_type` path (go_variables.go:353). Answers "who
consumes this interface".

**Y.5b — interface-dispatch `calls` (hop-agnostic, blast-radius).** On SSA `call.IsInvoke()` (a call
*through* an interface value; `matchesInvoke`/`MakeInterface` already detected at
go_semantic.go:226,883), additionally emit `caller → interface-method` `calls` (create the interface
method as a node — the interface node already carries the method list in `meta`). Keeps the concrete
resolution too. Answers "who dispatches through interface I's method M" — today `calls`-to-interface
= 0.

**Outcome.** "Who creates (implements) interface I" = existing `implements`; "who consumes I" = Y.5a
`uses_type`; "who calls I's methods" = Y.5b `calls`. The full interface picture, statically.

**Test.** Fixture with `type Store interface{ Get() }`, `func Use(s Store){ s.Get() }`, `func New()
Store` → assert `Use`→`Store` `uses_type`, `Use`→`Store.Get` `calls` (invoke), and `New`→`Store`
`uses_type` (return).

</details>

### Phase Y.6 — Render dataflow: resource → signal → DOM (frontend tail) `done`

**Outcome (measured, clean reindex).** Both edges landed, schema 22→23.
- **signal → element `dom_write` (extract, js_variables.go).** A JSX interpolation — text `{sig()}` or
  attribute `attr={sig()}` — that reads a Solid reactive accessor now binds that signal to the
  enclosing DOM element. The element node is minted lazily (a bare `<span>` has no id/class so the
  matcher never emitted one) and *only* when a resolvable accessor read is found, so a `dom_write`
  never dangles (#10). Reactive accessors are the first non-setter binding of
  `createSignal`/`createResource`/`createMemo`; function-local accessors are materialised as
  variable nodes (a signal's reach exceeds one function) while ordinary locals stay out of the graph.
  **14 `dom_write` edges, 13 minted element nodes**, 0 dangling element nodes. Genuine bindings:
  `copied → button`, `source → pre`, `loadingImpact → button`, `exportOpen → button` (Detail.tsx,
  Toolbar.tsx).
- **http_client → signal `flows_to` (via:resource) join (linker.LinkResourceSignals).** A
  `createResource(loaderFn)` accessor is stamped `meta.resource_fn`; the linker resolves the loader
  fn node and walks its `fn → http_client` `calls` edges to emit `http_client → signal`
  (`flows_to`, `via:resource`). **0 edges in-repo** — a correct, ledgered result: polyflow's own
  frontend is *manual* `createSignal` + `fetch`, not `createResource`, so hop 6's resource join has
  no in-repo instance. The path is validated by `TestLinkResourceSignals`; no edge is fabricated (#12).
- Nodes 2824→**2863**, edges 6114→**6176**, **0 orphan-endpoint edges** (#10). Tests
  `TestJSY6_DomWrite`/`TestJSY6_ResourceMeta` (parser) + `TestLinkResourceSignals` (linker) + schema
  bump; `go vet` clean.

**Honest note.** The `dom_write` half fires broadly (any signal-driven element, incl. Solid `<Show>`
control-flow whose condition reads a signal — the signal *does* gate that element). The
`createResource → http_client → signal` half is real but only lights up on codebases that use the
`createResource` idiom; the manual `createSignal`+`fetch`+`setX(await res.json())` pattern is a
`writes` edge on the setter (already captured) and would need a setter-payload type join (deferred,
Y.4-adjacent) to reach the same directional `http_client → signal`.

<details><summary>Original spec</summary>

**Problem (hop 6).** `renders` is fn→fn only. The fetch-result→DOM binding is declarative Solid
reactive dataflow: `const [data] = createResource(fetchFn)` binds the fetch fn to a signal, and JSX
`{data().name}` binds the signal to a DOM element. `js_variables.go` already tracks signals/setters
(`isSetter`, line 64) — half the clue.

**Fix.**
- `createResource(fetchFn)` / `createSignal` fed by a fetch: emit `http_client → signal`
  (`flows_to`, `meta.via:resource`).
- JSX text interpolation `{sig()}` / attribute `={sig()}`: emit `signal → element`
  (`dom_write`) on the enclosing element node.
- Chain with Y.4's `response_of`: `handler → response_of → DTO → consumes → fn → resource → signal →
  dom_write → element` = the render half, statically.

**Outcome.** Closes hop 6 for typed reactive frontends. Ledger untyped/dynamic (`innerHTML =
str`) — recall over precision does **not** license edges from unresolvable dynamic writes (#11-adjacent).

**Test.** Solid fixture `const [d] = createResource(loadNode); return <span>{d().label}</span>` →
assert `loadNode(http_client) → signal(d) → element(span)` path.

</details>

### Phase Y.7 — DOM event → handler (frontend head) `done`

**Measured outcome (shipped 2026-07-28, clean reindex).** The event head now emits real
`element → function` `dom_listen` edges and the chain closes; schema 23→24.

- **JSX event attributes (`js_variables.go`, `handleJSXEvent`).** For each `jsx_opening_element` /
  `jsx_self_closing_element`, an `onClick={ref}` (React/Solid camelCase) or `on:click={ref}` (Solid
  namespaced) attribute whose value is a **bare handler identifier** resolves the handler to its
  function node and emits `element → function` (`dom_listen`, `meta.event` = the DOM event,
  `via:jsx`, confidence `static`). The element node is minted lazily on a resolved handler (same ID
  format as Y.6's `dom_write`, so a listen and a write on one element share a node) → **0 dangling
  endpoints (#10)**. Inline arrow / call / member handlers carry no stable function node and are
  **not** matched; cross-file/imported handlers are **ledgered** (`dom_listen_unresolved`) — never
  fabricated (#12).
- **Vanilla `addEventListener` (`handleAddEventListener`).** `target.addEventListener("evt",
  handler)` with a literal event string and a bare handler emits `element → function` `dom_listen`
  (`via:add_event_listener`), the element labelled by the receiver (`document`/`window`/element ref).
  Dynamic event names and inline handlers are skipped/ledgered (#12).
- **Handler resolution reaches component-local consts.** The dominant React/Solid idiom is a
  function-local `const handleX = () => …`, not a module-level function. A new whole-tree pre-pass
  (`collectLocalFns`) registers every self-attributable function (nested `function` decls +
  `const = arrow/function` at any depth) name→decl-line — the same line the walk mints the node at —
  so `resolveHandlerFn` resolves locals, falling back from the module-level `fnDecls`. Without this
  every real handler in this repo (all component-local) would have missed.

**Measured (this repo's own frontend):** `dom_listen` 2→**7** — **4 JSX** (`button→copy`,
`button→loadSource`, `button→exportSVG`, `button→exportPNG`) + **1 addEventListener**
(`document→closeOnOutsideClick`, a component-local arrow) + the 2 legacy templ `dom_target` edges.
**1 `dom_listen_unresolved`** ledgered. **0 orphan-endpoint edges** across the whole graph (#10).
Nodes 2869→**2874**, edges 6199→**6212**. **Honest note:** every JSX handler in polyflow's own
frontend is an *inline arrow* (`onClick={() => store.foo()}`), correctly ledger-skipped (#12); the 4
JSX edges that fire are the bare-ref handlers (`onClick={copy}`). The templ `@onclick` path already
emitted `component → dom_target` `dom_listen` (untouched here) — a separate abstraction from the
JSX/vanilla `element → function` edges.

**Chain closes (verified):** `button(element) → dom_listen → loadSource(fn) → calls →
fetch(http_client)`, and likewise `exportSVG`/`exportPNG`. Combined with Y.3/Y.4/Y.6, the head of the
button→http→handler→db→type→render chain is now a connected static path for bare-ref handlers.

Tests: `TestJSY7_JSXEvent`, `TestJSY7_AddEventListener` (+ schema-version bump). `go vet` clean.

**Y.7 extension (shipped 2026-07-28) — inline-arrow handlers + TS `uses_type` dangling closure.**
A follow-up pass measured that every JSX handler in this repo is an inline arrow (bare-ref was the
minority) and that 12 TS DTO interfaces still dangled. Both are now closed:

- **Inline-arrow handler linking (`inlineHandlerTargets`).** `onClick={() => doThing()}` binds the
  element to *each same-file function the arrow body invokes* (`dom_listen`, `handler:inline`,
  confidence `inferred`) — the possible-flow head (recall over precision). Member/store-method calls
  (`store.foo()`), signal setters, and no-resolvable-call arrows carry no function node and are
  ledgered (#12). The same logic extends the `addEventListener` inline path. **Measured:** JSX
  `dom_listen` 4→**13** (9 inline: `exportMermaid`×3, `loadFileImpact`×2, `selectFile`,
  `selectResult`, `otherEnd`), total `dom_listen` 7→**16**, 0 orphan endpoints (#10).
- **Same-file TS `uses_type` (`extractTypeUses`, `js_variables.go`).** A `type_identifier` in an
  annotation, interface member, class field, or generic argument that resolves to a same-file
  interface/class emits `<enclosing decl> → type` `uses_type` (`via:type_ref`) — the JS analog of
  the Go struct `uses_type` pass. `enclosingDeclID` attributes to the owning interface / class /
  function / variable (incl. destructured signal accessors). **23 same-file edges**; cleared **11 of
  12** dangling TS interfaces.
- **Cross-file TS `uses_type` (`LinkJSTypeRelations`).** The last one (`GraphEdge`, defined in
  `types.ts`, used only as an imported generic arg in `derived.ts`/`graph.ts`) is resolved through
  the file's import bindings, attributed to the nearest preceding declaration in the consumer file
  (`via:type_ref_import`, `inferred`). **14 edges;** `GraphEdge` now connected.

**Net dangling-node result:** isolated nodes 341 (baseline) → 75 (post-Y.6) → **63**; **dangling TS
interfaces 12 → 0**. The remaining 63 are all correct-to-dangle (#12): 59 Go consts/vars
(test-fixture, genuinely-unused, or package-`var`-map-initializer-only) + 4 TS module vars never read
across a boundary. Tests: `TestJSY7_InlineHandler`, `TestJSY7_TypeUses`. No schema change (edges-only).

<details><summary>Original spec</summary>

**Problem (hop 1).** `dom_listen` is near-empty (2) and points at a `dom_target` abstraction. The
real clue is the JSX event attribute `onClick={handler}` / `onInput={…}` (Solid) or templ
`@onclick`, and vanilla `el.addEventListener("click", fn)`.

**Fix.** In the JSX/templ pass, for each element with an `on<Event>={ref}` attribute, resolve `ref`
to its function node and emit `element → function` (`dom_listen`, `meta.event:"click"`). Handle
`addEventListener("evt", fn)` in `js_variables.go` similarly. Chain: `element → dom_listen → handler
fn → calls → http_client` = the head of the flow.

**Outcome.** Closes hop 1. Combined with Y.3/Y.4/Y.6, the **entire** button→http→handler→db→type→
render chain is a connected static path for the typed/literal-or-reconstructable majority.

**Test.** Solid fixture `<button onClick={onRefresh}>` where `onRefresh` calls `fetch(...)` → assert
`button(element) → onRefresh(fn) → http_client` connected.

</details>

---

## Honest ceiling — what static cannot do even at max coverage

Static analysis yields the **possible-flow graph at high recall**, never the **actual-path trace**.
Four irreducible residuals, each ledgered (#12), each the province of Tier X.6 runtime fusion:

1. **Path selection, not path existence.** A value-dependent URL/dispatch (`fetch(base + kind)`,
   Go interface value chosen at runtime, Ruby `send`) statically reaches *all* matching targets;
   static cannot say which one a given click took. **This is acceptable and correct for the project
   goal** — `[[polyflow-agent-context-goal]]` prioritizes *recall over precision* for blast-radius;
   possible-flow is the target, not a compromise.
2. **Untyped payloads.** `map[string]any` / `JSON.parse` with no declared type → Y.4's type-shape
   join has nothing to match. Degrade to field-name heuristics or ledger; never fabricate.
3. **Config/env/reflection dispatch.** URL from env var, handler chosen by reflection, event name
   built by string concat → no literal skeleton → unresolvable; ledger with a reason code.
4. **Cross-async response↔render correlation without a shared type.** Falls back to the structural
   "the component consuming this resource calls this fetch," which is usually sufficient but is a
   *structural* claim, not a proven causal one.

**Target:** ~100% of *what the code declares can happen* (possible-flow), **not** 100% of *actual
runtime traces*. The latter requires Tier X.6 (OTel residue fusion) and is out of scope for Tier Y.

---

## Sequencing

```
Y.1 ─┐ (cleanup: lazy interface stubs)      cheap, independent
Y.2 ─┘ (cleanup: const reads)               cheap, independent
Y.3 ─┐ (joins: route→fn, http→handler [=X.1], handler→db)   forward request half — highest ROI
Y.4 ─┤ (extract+join: response types)        return half; unblocks Y.6 tie-in
Y.5 ─┤ (interface uses_type + dispatch calls) blast-radius completeness; resolves last dangling iface
Y.6 ─┤ (extract: resource→signal→DOM)        render tail; needs Y.4
Y.7 ─┘ (extract: JSX event→handler)          event head; closes the loop
```

Do **Y.1–Y.3 first** (cleanups + pure joins, zero new-parser risk), reindex, and measure how many of
the 6 hops connect before committing to the new extractors (Y.4–Y.7).

## Verification (whole plan)

- Per phase: the fixture assertions above (fan-out per #1; ledger-not-fabricate per #12).
- Whole-`indexer.Run` fixture asserting **no stored edge has a dangling endpoint** (rule #10) after
  Y.1's node removal.
- Reindex polyflow after each phase; track: dangling-node count (Y.1/Y.2 → ~7), # of 6 hops
  connected on a hand-verified path (Y.3–Y.7), and a "flow completeness" metric = fraction of
  `element`-rooted event handlers that reach a `datastore` and a rendered `element` on a connected
  static path.
- No regression on `make test` / `make bench` / chessleap golden.

Registered in `docs/phases.md` (Tier Y).
