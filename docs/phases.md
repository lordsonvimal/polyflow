# Polyflow — Per-Phase Process

The phase plans (`docs/agent-context-plan.md`, `docs/contract-matching-plan.md`,
`docs/versioning-matrix-plan.md`, `docs/evidence-fusion-plan.md`,
`docs/runtime-flow-plan.md`, `docs/goal-completion-plan.md`,
`docs/semantic-search-plan.md`, and the numbered series
`docs/plan-1-recall-hardfails.md` through `docs/plan-14-agent-trust.md`)
all follow this process. The original gap-closing plan that carried these rules is complete and was
removed; this doc keeps the rules themselves.

Status legend used in every plan: `pending` · `in progress` · `done`.

## Implementation order (cross-plan roadmap)

Every phase doc is self-contained, but phases from different plans depend on
each other. Implement in this order (parallel tracks marked; each plan's own
prerequisite banner is authoritative if it conflicts):

1. **E.1–E.2** (goal-completion, eval harness) — *no prerequisites*. Build
   the corpus + recall scorer + CI ratchet first so every later phase is
   measured and regression-gated from day one.
2. **G.0–G.3** (contract-matching) — the engine, HTTP + messaging ports,
   route-group fix. G.0's pinned surface is what F.0, R.1, and V.1 compose
   against; nothing downstream starts before G.0 lands. (The semver gate
   `patterns.VersionInRange` already exists — V.0/V.1 refinements slot in
   only when a rule actually needs them.)
3. **G.4–G.5 and G.6–G.7** (parallel branches after G.3) — new kinds +
   doctor coverage; dynamic keys + indirection. G.5's doctor is a
   prerequisite for D and P.2.
4. **F.0** (evidence-fusion substrate) → **R.0–R.5** (runtime-flow, slotting
   into F.2's position) → **F.3–F.5** (config resolution, state
   computation, conflict handling).
5. **A.1–A.3** (goal-completion, provenance surface) — needs F.0 + F.4;
   makes fusion visible to agents.
6. **Tier L** (L.P0–L.P4 Python, L.W0–L.W2 legacy web) — needs only current
   pattern/matcher infra plus, for checklist items 4 and 7, the contract
   engine (G.4) and walker/indirection conventions (G.6/G.7). Can start
   any time after step 3; L.W1/L.W2 even earlier.
7. **S.0–S.4** (semantic-search) — independent parallel track; depends only
   on the graph store. Can run alongside steps 2–6; must finish before P.1.
   **Tier I** (I.1–I.3, intra-language semantic links: inherits/implements/
   instantiates/imports) is likewise a no-prerequisite parallel track —
   slot it anywhere after E.1; I.2's cross-file resolution improves once
   L.W1's global tables exist but does not wait for them.
8. **Tier D** (doctor --propose, ledger burn-down) and **Tier C** (CI/PR
   freshness) — need G.5 and F-states respectively.
9. **P.1–P.2** (proof benchmarks) — last; P.1 needs A + E + S, P.2 needs G.5.

**V.2/V.3 sidecars** are divergence-triggered (versioning-matrix plan): do
not build them until a V.4 matrix cell actually diverges.

## Gap-closing series (plans 1–8) — order relative to the roadmap above

The numbered plans close coverage gaps found in the 2026-07-17 sufficiency
review (plans 1–6) and the 2026-07-18 fleet audit (plans 7–8). Execute them
**in filename order**; each doc's own prerequisite banner pins where it
interleaves with the roadmap above:

1. `docs/plan-1-recall-hardfails.md` (Tier B) — no prerequisites; start
   any time. Fixes measured baseline hard-fails; B.0's unparsed-file
   ledger is a substrate for plans 2–6. B.1 is a prerequisite for
   plan 3's L.N1.
2. `docs/plan-2-modern-web.md` (Tier M) — needs only the contract engine
   (done). File-based routing, Vue/Svelte SFCs, tRPC, Angular.
3. `docs/plan-3-language-breadth.md` (Tier L continued) — after L.P
   (Python) closes on its eval number. Java/Spring, C#/ASP.NET,
   PHP/Laravel; one language at a time, each closed by an eval number.
4. `docs/plan-4-deployment-topology.md` (Tier K) — K.0/K.1 any time;
   K.2/K.3 after F.0.
5. `docs/plan-5-cloud-events.md` (Tier Q) — Q.0 any time after G;
   Q.1 after R.4; Q.2 after F.0; Q.3 after Q.0.
6. `docs/plan-6-scale-monorepo.md` (Tier N) — N.0–N.2 after E.3 + B.0;
   **N.3 runs strictly last of everything** (it writes the coverage
   contract for whatever actually shipped — including plans 7–8).
7. `docs/plan-7-fleet-stacks.md` (Tier H) — after N.0–N.2, before N.3.
   Express routes, `ws` server WebSocket, Solid Router, delayed_job
   contract rule, and local path-based corpora for the author's seven
   fleet repos (recall numbers for synergy, go-svcb, go-svcc,
   RailsSvc, RailsConsumer1, RailsConsumer2).
8. `docs/plan-8-multi-repo.md` (Tier Z) — after plan 7, before N.3.
   Multi-repo workspaces: path semantics (`~`, workspace-file-relative),
   per-service git roots for `impact --diff`, and the cross-repo fleet
   eval corpus (RailsSvc ↔ agents over RabbitMQ/REST).

## Precision series

- `docs/cross-service-precision-plan.md` (Tier J) — after Tier W and
  Tier X.7/X.11 (both done); independent of the UI series and of N.3.
  Closes the 2026-08-06 fleet-datascience audit: ~75% of cross-service
  edges were false positives and 4 real AMQP producer/consumer pairs
  were missed. J.1 decodes static `[]QueueDecl` binding tables (the
  follow-up `go_amqp_names.go:43` already names); J.2 makes workspace
  `links:` an actual candidate-service allowlist and blocks empty-path
  producers from matching every service's root handler; J.3 gates
  `LinkBrokerHints` on per-node exchange evidence. J.3 is the only
  phase that deliberately trades recall for precision — check the eval
  corpus for cases passing *because* of the fan-out before starting.
- `docs/js-wrapper-url-backtrack-plan.md` (Tier WB) — no prerequisites;
  independent, additive. Today `producer_alias_obj_call` only recognizes an
  object key literally named `"url"`, and `producer_alias_url_call` always
  assumes the URL is positional argument 0 — both silently wrong or silently
  absent for wrappers that name/position their URL parameter differently
  (`apiFetch({ uri: ... })`). WB.1 detects, from the wrapper function's own
  body, which parameter (by position, destructured key, or member-access key)
  actually reaches `fetch`/`axios`/a known alias; WB.2 widens call-site
  capture to every object key; WB.3 (linker) picks the correct candidate per
  wrapper, falling back to a conventional-name list and a
  `wrapper_key_ambiguous` ledger entry when the wrapper is unresolved —
  recall-preserving per the Trust contract. WB.4 (positional-index
  correction) is corpus-gated, not built speculatively.

## UI series (plans 9–13) — the web-UI overhaul

The UI plans close the 2026-07-19 UI review (12 problems + extras:
scale, hierarchy, search, tool ops, coverage visibility, line ranges,
flow isolation, layout, tech stack, tool-call debugging, config
editing, docs; plus context copy, waypoint flows, group/seam isolation,
impact/diff visualization, export/share, saved views, health
dashboard). Execute **in filename order, after plan-8 and before
plan-6's N.3** (N.3's coverage contract covers whatever shipped,
including these):

9. `docs/plan-9-ui-backend.md` (Tier U-B) — backend enablers: node
   line ranges (schema 17→18), `/api/tree`, ops.db + tool-call audit,
   jobs API (index/eval/reconcile), config API, flow/seam/health/stack
   queries, context bundles, runtime capture/ingest/flows API (shared
   session store with the CLI). No `web/` changes.
10. `docs/plan-10-ui-shell.md` (Tier U-S) — the workbench rebuild:
    shell layout, scope stack + URL state, gesture grammar, element
    budget, palette, notification system. **Carries the binding UX
    specification for plans 11–13.**
11. `docs/plan-11-ui-navigation.md` (Tier U-N) — tree explorer,
    drill-down scopes, search-that-navigates, line-range source panel,
    tech-stack view, flow lenses (calls/http/messaging/data/imports/
    dom edge-class modes).
12. `docs/plan-12-ui-flows.md` (Tier U-F) — flow lanes, flows-through-
    here, entrypoint catalog, path finder, waypoint builder, seam +
    group isolation, pinboard (through-pins-only filtering), link
    explorer (peek/commit up/downstream), context-copy workbench,
    impact/diff + coverage overlay.
13. `docs/plan-13-ui-ops.md` (Tier U-O) — jobs UI, tool-call log,
    config editor, health dashboard, generated CLI docs + README,
    export/share/saved views, runtime capture UI (record/ingest/fuse),
    and the CLI↔UI parity sweep (patterns, setup mode, parity matrix:
    both surfaces drive the same graph). Ends with the UI
    coverage-contract walk.
14. `docs/plan-14-agent-trust.md` (Tier T) — measured agent trust:
    workspace trust stamp (T.0), runtime-gap → eval-case promotion
    (T.1), agent-in-the-loop correctness eval + ≥ 0.95 bar and ratchet
    (T.2–T.3). T.0/T.1 any time after plan-1 B.3; most valuable re-run
    after each of plans 2–8 lands (restamping is one command). T.0's
    stamp is an additive field in plan-9's `/api/health`.

## Systemic cross-service gaps (Tier X) — after plan-14

`docs/systemic-gaps-plan.md` (Tier X) closes the four systemic gaps measured
2026-07-26 on go-svcb/go-svcc/RailsSvc: literal-only key derivation
(templated URLs 96% unresolved; delayed_job/pusher async 2% linked), test-DSL
comm-node false positives (53–70% of `http_client`), single-workspace scope
(cross-repo flows invisible), and the missing yield bar + OTel residue fallback.
**North star: >80% token savings** on coding/debugging/verifying/reviewing
workflows — earned only via correctness (an incomplete answer costs more than
grep) plus a per-answer coverage block that stops defensive re-grepping.
Composes Tiers G/R/F and plan-8; extends the contract KeyWalkers + normalizers
rather than adding engines. **Reordered for fastest time-to-value on the three
repos** (each phase independently improves real answers when it lands):
X.0 test-DSL suppression → X.1 dynamic-template resolution (URLs *and* channels,
one KeyWalker extension) → X.2 async job linking (delayed_job + ActiveJob) →
X.3 yield scorecard + gate → X.4 MCP `flows`/`entrypoints` redesign (the token
payoff; graded by a ≥80%-reduction benchmark on plan-14) → X.4a flow-task
benchmark harness (the eval/agentbench plumbing X.4's own gate needs — X.4
shipped without it measurable, closed separately) → X.5 cross-repo linking →
X.6 OTel residue fusion. Bars: internal yield 100%, cross-static ≥95%,
cross+runtime 100%-or-ledgered. Sequence X.0→X.1→X.2→X.3→X.4→X.4a→X.5→X.6.

Referencing rule for implementers: every prompt/task should name **this file
(process + order) plus the single owning plan doc for the phase being
implemented**. The plan docs are written so that pair is sufficient — no
other context needed.

### Cross-yield resolution (Tier X.7–X.8) — continues Tier X

`docs/crossyield-resolution-plan.md` closes the two *deep* cross-yield causes
found 2026-07-28 (combined go-svcc/go-svcb/dsw index measured
cross_yield_static=0.028): the shallow causes (CY-#1 http_client precision
guard, CY-#4 external-boundary classification, CY-#5 AWS constructor
suppression + variable-input operations) shipped in that commit. **X.7
interprocedural wrapper URL propagation** is now `done` — implemented in the Go
SSA semantic pass (`internal/parser/go_wrapper_urls.go`) as a transitive-closure
propagation of literal paths across the real two-hop typed-client chain
(`RegisterApp → doWithRetry → doRequest → http.NewRequest`); measured
cross_yield_static 0.089 → 0.114, resolving the go-svcb registration
handshake. See the plan header for the deviations from the pinned parser-stamp +
linker design (SSA has the call-site args; the corpus needed multi-hop). **X.8
route-group prefix composition** is now `done` — but with a corrected root cause.
The plan's premise (the `gin_route` handler node lacks a `router` capture) was
stale: the matcher already stores every named capture, `@router` already flows,
and `EnrichRouteGroups` already composed nested prefixes (go-svcb's
`/apps/register` already enriched to `/api/v1/service/apps/register` for the
engine, which is why X.7 resolved it). The genuine gap was **empty-prefix
middleware-only groups** (`protected := v1.Group("")`, `authored := g.Group("")`):
`EnrichRouteGroups` skipped any group with an empty prefix, so its nested routes
lost the parent chain. Fix is a one-line guard change in
`internal/contract/routegroup.go` (gate on `var_name`, keep empty-prefix groups
in the chain); no pattern/engine/schema change. Measured cross_yield_static
0.114 → 0.129; chessleap golden contract edges 305 → 312 (seven pure additions
under an empty-prefix group, zero removals). See the plan header for the full
deviation writeup and the deferred `routegroup_cross_fn` residue (no
cross-function group in the corpus). X.7 fixed the client end; X.8 the handler
end. Gate still FAILs (0.129 < 0.95): the residue is external/dynamic-URL and
fs-mediated flows catalogued in the investigation, not a route-group defect.

## Static end-to-end flow coverage (Tier Y) — after/alongside Tier X

`docs/static-flow-coverage-plan.md` (Tier Y) assembles resolved edges into
connected *flows* — the full **DOM event → http → route handler → db → response
type → render** chain — at maximum *static* coverage, and cleans the dangling-node
noise that makes the graph look sparser than it is. Measured baseline (clean
reindex 2026-07-27, `eval/**` excluded): 2975 nodes / 5801 edges, **341 dangling
(11.5%)** of which 203 are eager external-interface stubs and 109 are inlined
package consts — both cleanup targets, not real gaps. Only hop 2 (fn→http) of the
6-hop chain connects today; the other hops' clues are **already in node `meta`**
(handler `path`/`method`/`handler`, client wildcard `url`, query-node SQL) — the
gap is *joining*, not *extracting*. Order: Y.1 lazy interface stubs → Y.2 const
reads (`TypesInfo.Uses`) → Y.3 request-half joins (route→fn, http→handler [**=Tier
X.1**], handler→db attribution) → Y.4 response-type extract + cross-language
shape-join (return half) → Y.5 interface `uses_type` + dispatch `calls` → Y.6
resource→signal→DOM render dataflow → Y.7 JSX event→handler. **Honest ceiling:
static yields the possible-flow graph at high recall, never the actual-path
trace** — value-dependent dispatch, untyped payloads, and env/reflection URLs are
ledgered (#12), and true response↔render causation across async needs Tier X.6
runtime fusion. Reuses Tier X's KeyWalkers/normalizers for Y.3; adds no new engine.
Do Y.1–Y.3 first (cleanups + zero-parser-risk joins), reindex, then measure before
the Y.4–Y.7 extractors. Sequence Y.1→Y.2→Y.3→Y.4→Y.5→Y.6→Y.7.

## Handler-node hygiene (Tier HH) — Rails route extraction

`docs/handler-node-hygiene-plan.md` (Tier HH) fixes the three defects the
2026-08-08 nine-service fleet audit traced to the `http_handler` population.
**301 of 1,648 handler nodes (18%) are not endpoints** — route-group
scaffolding (`resources_route` 135, `namespace_route` 100, `gin_route_group`
66) — and 85 of the 100 `namespace_route` nodes are Rake task namespaces in
`.rake` files, not routes at all. HH.1 gates the receiverless
`http_verb_route` pattern to real routes files (`patterns/ruby/rails_routes.yaml`
matches *any* implicit-self call named `get`/`post`/…, so
`nextGen/app/services/cam/user_category_rules_client.rb:22`'s outbound call to
CAM is indexed as a *route in nextGen* — one missed cross-service link plus a
phantom endpoint carrying a raw `#{…}` path). HH.2 upcases the verb and
refreshes the stale label after path composition: `Meta["path"]` is already
composed correctly but `Meta["method"]` keeps the source's case, and because
`TierExact` indexes the **raw** key and the first hitting tier wins, a
lowercase-verb route is invisible at the exact tier and gets shadowed by an
uppercase one in another service — the mechanism behind the `POST /login`
cross-service FP. HH.3 retypes scaffolding to a new `route_group` node type
without changing any composed path. Sequence HH.1→HH.2→HH.3 (HH.1 first makes
85 of HH.3's nodes vanish for free). Complements, and is independent of, the
matcher-side producer-dedupe and browser-same-origin fixes.

## Ground rules — every phase

- **One phase per commit.** Tests pass before each commit; the owning plan doc is
  updated (status → `done`, plus an outcome note) in the same commit.
- **Positive + negative fixtures.** Every new/changed pattern YAML ships a positive
  fixture (`input.*` + `expected.json`) and a negative fixture (`negative.*`, zero
  matches). Version-gated patterns additionally ship a same-shape-wrong-version
  negative. The "no fixture → CI fails" rule stays intact.
- **Additive by config.** New stacks/protocols are added as YAML + fixtures only;
  core matcher/graph/engine code changes only for genuine new capabilities.
- **Benchmark hold.** Changes on the indexing path hold chessleap index time and
  `BenchmarkIndexCold` (`make bench`). Chessleap is a private local repo:
  `~/projects/chessleap` on the author machine, symlinked (gitignored) to
  `eval/.cache/chessleap` — setup pinned in `eval/corpus/chessleap/manifest.yaml`.
  A second private local proving ground is `~/projects/synergy` (Nx monorepo,
  not in the eval corpus; used for real-repo dry runs, see plan-6 N.2 notes).
- **`graph.SchemaVersion` bump** whenever the stored node/edge shape or semantics
  change, so stale incremental caches are discarded.
- **Trust contract.** Recall over precision; no silent gaps — anything unresolvable
  is surfaced (unresolved ledger or labeled low-confidence edge), never dropped;
  `docs/polyflow-design.md` is updated whenever a phase changes a documented
  decision.

## Proven bug classes — binding on every remaining phase

Each rule below was extracted from a real defect that shipped and was later
caught in review or by the eval corpus (commits `dd75b67`, `3bb9197`,
`fc46dd7`, `e851bcc`; rules 10–12 from the 2026-07-18 review of the
R/F/A/L.W/L.P series, where the defects surfaced only on a real repo the
test suite and eval corpus never resembled). The owning plan docs apply these rules concretely per
phase; when a phase spec and a rule here seem to conflict, stop and surface
it — do not silently pick one.

1. **Fan-out, never first-match.** *(Incident: the contract engine's consumer
   index was single-valued — hub broadcasts linked only the first subscriber;
   shared routes lost edges silently.)* Any lookup that joins two populations
   (producers↔consumers, evidence↔static edges, spans↔channels,
   selectors↔elements, globals↔definitions, helpers↔routes) must be
   **multi-valued**: every entity sharing the matched key gets an edge/source.
   First-seen-wins in a map insert is a recall bug even when every test passes.
   Required test: a fixture with ≥2 entities sharing one key, asserting N
   edges (not 1).
2. **Deterministic output, always.** *(Incident: the wildcard match tier
   iterated a Go map, so edge sets differed between runs.)* Go `map` iteration
   order must never reach any output — edges, flow records, reports, search
   results, proposals, coverage tables, sidecar frames. Iterate a recorded
   insertion-order slice or sort by a stable key before emitting. Required
   test: every phase that produces a set ships a **two-run determinism test**
   (run the pipeline twice on the same input; require byte-identical output).
3. **Reject parsed-but-unenforced config.** *(Incident: `package:` /
   `version_range:` on contract rules were accepted at load and silently
   applied to all versions.)* A schema field the loader parses but the code
   does not yet enforce must **fail at load** with an error naming the phase
   that will enforce it. Silent acceptance manufactures misinformation.
4. **Gate logic: absence is failure; exit order is part of the spec.**
   *(Incidents: baseline repos missing from the current run read as a pass;
   an unconditional hard-fail exit ran before the gate, making the gate's
   pre-existing-failure exclusion unreachable — CI would have failed forever
   on the committed baseline.)* For any CI gate: (a) enumerate the baseline
   and fail on entries absent from the current run (explicit, documented
   exemptions only — e.g. `SkippedCorpus.LocalOnly`); (b) pin the precedence
   of every exit path in the phase doc and test it; (c) before landing,
   simulate CI conditions (remove caches, run on the committed baseline) and
   record the result in the phase note.
5. **Regression harnesses land with the change they guard.** *(Incident: the
   G.0 golden-parity harness was left a stub while the bespoke linkers it
   guarded were deleted — a locked-decision breach.)* When a phase spec
   includes a parity/golden/regression guard, the guard — with a real
   committed snapshot and a determinism check — lands **in the same commit**
   as the risky change. Deferring it is a recorded deviation in the plan doc,
   never a silent TODO.
6. **Captured source text is raw — strip literals before building keys, and
   test through the real parse path.** *(Incident: route-group `prefix`
   captures kept their quote characters (`"\"/play\""`), enrichment built
   unmatchable keys, and every grouped datastar action linked to unresolved —
   while the unit tests passed, because they hand-built nodes with clean
   values.)* Any capture that is concatenated into a channel key, path, or
   identifier goes through `stripStringLiteral` (matcher quote-strip list) or
   an equivalent, and symbols/heredocs/interpolation markers are handled or
   ledgered. Required test: at least one test per phase runs a real fixture
   file through parser→matcher→(linker/engine) end to end — hand-constructed
   nodes alone are insufficient evidence.
7. **Recognition vocabularies are validated against hand-verified real-repo
   cases.** *(Incident: `data-init` was missing from the datastar v1 vocab —
   every SSE-subscribe edge silently dropped; synthetic fixtures never
   noticed.)* Any attribute/verb/method/helper vocabulary (datastar attrs,
   OTel semconv names, jQuery methods, Rails route helpers, framework
   decorators) gets at least one Tier E corpus case exercising it on a real
   repo. Version-gated vocab additions ship wrong-version negatives in both
   directions.
8. **Test code is production code to the graph.** *(Incident: default
   workspace excludes plus a `Tests: false` package load made test callers
   invisible — blast radius omitted "which tests break".)* New-language and
   new-parser phases index test files from day one; semantic/type-checked
   loaders enable test variants with a degrade-don't-die fallback when test
   code fails to compile (the `collapseTestVariants` precedent). Excludes are
   for fixture/data dirs and build output (`testdata/`, `*_test/`, `tmp/`),
   never `*_test.*` / `*.spec.*` as a class.
9. **Never let a case pass by luck.** *(Incident: eval cases passed via bm25
   ranking accidents and unrelated type-edge chains; indexing test code
   shuffled the ranking and "broke" them.)* Search-dependent behavior pins
   exact-label-match-first ranking (the `SearchNodes` rule: exact
   case-insensitive label match outranks prefix-only bm25). Eval cases target
   uniquely-resolvable entities or assert the specific edge path; a case that
   flips under an unrelated change is a case bug or a ranking gap to fix —
   never noise to re-baseline around.
10. **In-memory state must track store deletions.** *(Incident: `LinkJS`
    deleted proxy component nodes from the store — cascading their edges —
    and filtered `allNodes`, but not `allEdges`; the evidence reconciler
    re-upserted the dangling edges and the entire index aborted with a
    FOREIGN KEY failure on the first real repo whose JSX rendered an
    external-library component. Every test and eval repo passed.)* Any pass
    that deletes, merges, or renames nodes must filter **every** in-memory
    collection that later flows to a writer (edges, unresolved refs, caches),
    in the same block as the deletion. Required test: a full-`indexer.Run`
    fixture that exercises the deletion path and asserts no stored edge has
    a dangling endpoint.
11. **Blanking/splitting parsers blank comment bodies.** *(Incident: the ERB
    splitter blanked only the `#` marker of `<%# … %>`, so comment bodies
    became live Ruby and commented-out helpers minted phantom nav edges —
    recall over precision never licenses edges from dead text.)* Any parser
    that produces a blanked/virtual view (ERB, future Jinja2/Blade/JSP —
    checklist item 8) must blank each comment construct **in its entirety**,
    and ship a fixture containing a commented-out instance of every
    recognized pattern, asserting zero matches from it.
12. **Intake is exhaustively accounted: every element reaches output or the
    ledger.** *(Incident: the span mapper anchored on SERVER spans only;
    unpaired CLIENT spans and all INTERNAL spans vanished — no flow, no
    ledger — violating "never silently dropped" while every fixture passed,
    because no fixture contained an unhandled element.)* For any ingest of a
    population (spans, spec operations, config vars, route entries): after
    the mapping passes, a final sweep ledgers everything not yet accounted
    for, and at least one fixture contains an element no mapping pass
    handles, asserting it lands in the ledger. Corollary for reports:
    **coverage denominators only count what the evidence class can actually
    confirm** — a % over a population the source could never verify is
    misinformation (the runtime-coverage-over-`contains`-edges incident).
