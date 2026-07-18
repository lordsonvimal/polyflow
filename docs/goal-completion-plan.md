# Polyflow — Goal-Completion Plan (post-architecture end-to-end)

Status legend: `pending` · `in progress` · `done`

> **Prerequisites.** Tiers A, D, C.2, and P.2 assume the architecture plans
> are implemented: contract-matching (G.0–G.5, incl. the G.5 `doctor` command
> P.2 builds on), evidence-fusion (F.0–F.5), runtime flows (R.0–R.5). **Tier E (evaluation) has no prerequisites and may start
> immediately** — it measures whatever exists. Tier L needs only the current
> pattern/matcher infrastructure. Each tier states its own dependencies; a
> contributor can execute any phase from this document alone plus the pinned
> surfaces in the referenced plans.

## Context

The architecture plans build the machinery: one contract engine (breadth), a
version matrix (fidelity), evidence fusion (correctness), runtime capture
(confirmation). This plan is everything *around* that machinery required to
actually complete the goal — **an AI agent gets a complete, correct,
trustworthy blast radius on any repo** — and to prove it:

- **Tier A** — agents must *see* the new truth (provenance in query outputs).
- **Tier E** — the goal needs a *metric* (ground-truth recall evaluation).
- **Tier L** — "any repo" needs more *languages* (Python first) **and the
  legacy-web idioms** real projects wire flows through (ERB views, `window`
  globals, jQuery — the L.W phases).
- **Tier I** — blast radius needs the *type-relationship links* every
  language has but the graph lacks (inherits/implements/instantiates/
  imports — the intra-language semantics audit, 2026-07-15).
- **Tier D** — the self-improving loop needs an *operator workflow*.
- **Tier C** — the graph must stay *fresh* (CI + staleness).
- **Tier S** — humans and agents need *natural-language retrieval* of nodes
  and flows — expanded into its own plan: `docs/semantic-search-plan.md`
  (S.0–S.4: pure-Go embedded static embedder, hybrid FTS∪vector search
  everywhere, flow chains as retrievable units, sidecar/endpoint upgrade
  ladder, measured accuracy via Tier E).
- **Tier P** — the end-to-end outcome must be *proven and packaged*.

Trust contract (unchanged, binding on every phase): recall over precision; no
silent gaps; confidence/provenance labeled; blind-spot sections survive any
token budget.

Follows the repo per-phase process (`docs/phases.md`).

---

## Tier A — Agent-facing provenance surface

*Depends on F.0 (Edge.Sources/VerificationState/VerifiedGranularity exist) and
F.4 (states are populated). Without this tier the fusion work is invisible to
agents.*

### Phase A.1 — Provenance in query outputs `done`

**Problem.** `context`, `impact`, and `trace` JSON emit per-edge `confidence`
only. `verification_state`, `sources`, and `verified_granularity` never reach
the agent, so a `verified` edge and an unconfirmed `candidate` edge look
identical.

**Deliverable.**
- Per-edge fields in all three commands' JSON: `verification_state`,
  `verified_granularity`, and `sources` as compact strings
  (`"runtime:<session>/<trace_id>"`, `"static:file.go:42"` — the SourceRef
  `Provider + ":" + Ref` form; full structs only under a `--verbose-sources`
  flag to protect token budgets). `sources` is ordered by
  `(provider, ref)` — Sources[] arrives from a merge and must not surface
  in map/merge order (rule 2, `docs/phases.md`); the JSON golden tests
  below only stay stable if this ordering is real.
- A new **always-present** top-level section in each response:

```json
"verification_summary": {
  "verified": 41, "candidate": 12, "observed_only_gap": 2, "conflicting": 0,
  "note": "12 candidate edges are static-only; verify before relying on them.
           2 observed-only gaps indicate flows the static graph missed."
}
```

  Like `unresolved` (Phase 1.1), it is `{}`-never-absent and **survives any
  token budget** (extend the `internal/budget` floor exactly as Phase 1.3 did
  for the unresolved section).
- Text/chain formats append one summary line.

**Files.** `internal/impact/` (assemble paths), `internal/context/`,
`internal/trace/trace.go`, `internal/budget/` (floor), shared summary helper
in `internal/graph` (beside `UnresolvedNote`).

**Tests.** Summary math unit tests; budget-floor test (tiny `--max-tokens`
keeps `verification_summary` whole); JSON golden tests for all three commands;
`{}`-when-clean test (absence ≠ certainty).

**Acceptance.** `polyflow impact --target <x> --format json` on the fusion
fixtures shows per-edge states + the summary; `--max-tokens 200` still carries
both `unresolved` and `verification_summary` intact.

**Outcome (2026-07-18).** Implemented exactly as specified. `internal/graph/verification.go` adds `BuildVerificationSummary`, `VerificationSummaryLine`, `CompactSources`, `SortedSources`. All three query commands (`context`, `impact`, `trace`) now emit `verification_state`, `verified_granularity`, and `sources` (compact by default, full `SourceRef` structs under `--verbose-sources`) per edge/hop/node, and an always-present `verification_summary` top-level block. Budget floor holds naturally: `VerificationSummary` is a value-typed struct field, not part of the trimmed `Files` slice. Sources ordered by `(provider, ref)` for determinism. Text format appends a summary line via `VerificationSummaryLine`. MCP server and eval runner updated. `SchemaVersion` NOT bumped (query output shape change only, not stored graph). New tests: `internal/graph/verification_test.go`, `internal/impact/a1_test.go`, `internal/context/a1_test.go`, `internal/trace/a1_test.go` — all pass. `BenchmarkIndexCold` unaffected (output path only).

### Phase A.2 — MCP filters + semantics teaching `pending`

**Problem.** MCP tools expose none of the new fields, and their descriptions
don't tell agents what the states *mean*, so agents can't act on them.

**Deliverable.**
- MCP inputs on `context`/`impact`/`trace`: `min_verification`
  (`"verified"|"declared"|"observed"|"any"`, default `"any"` — recall over
  precision means filtering is opt-in) and `verbose_sources` (bool).
- Tool descriptions updated with the pinned semantics paragraph:
  *"Edges carry verification_state: `verified` edges are confirmed by runtime
  or declared contracts — do not re-verify. `candidate` edges are static-only —
  one cheap grep confirms them. `observed_only_gap` edges were seen at runtime
  but missed by static analysis — treat as real. The verification_summary and
  unresolved sections are always present; empty means clean, absent means
  error."*

**Files.** `internal/mcpserver/` (handlers + descriptions), depth-convention
docs comment.

**Tests.** In-memory-transport MCP tests: filter honored, default returns
everything, descriptions contain the semantics text (guards accidental
regression).

**Acceptance.** `claude mcp add polyflow -- polyflow mcp`; an impact call with
`min_verification: "verified"` returns only confirmed edges plus the summary
showing what was filtered out (filtered counts stay visible — hiding them
would fake certainty).

### Phase A.3 — Verification-aware ranking `pending`

**Problem.** `RelatedFiles` ranking (direct refs → hop distance → node count)
and impact rollups ignore verification, so a verified dependency and a
speculative one interleave arbitrarily.

**Deliverable.** Add verification as a *tie-breaker only* (never a filter):
within equal rank, `verified` > `declared`/`observed` > `inferred` >
`candidate`. `rollupCallers`/`Summarize` order file groups the same way.
Ranking change is documented in the output (`ranking: "refs,hops,verification"`).

**Files.** `internal/graph/related.go`, `internal/impact/summary.go`
(`rollupCallers` + the `Result.Summarize`/`DiffResult.Summarize` methods —
note: `Summarize` lives in `internal/impact`, NOT `internal/budget`, whose
exports are `Estimate`/`TrimToFit`/`Snippet`/`AppendNote`).

**Tests.** Tie-break unit test (two files, equal refs/hops, different states);
existing ranking tests unchanged (proves tie-breaker-only).

**Acceptance.** On the fusion fixtures, a verified related file outranks an
equal-scored candidate one; total result sets are identical (nothing dropped).

---

## Tier E — Ground-truth evaluation harness

*No prerequisites — start immediately; it measures whatever the graph can do
today and becomes the regression gate for every later tier.*

### Phase E.1 — Corpus format + scorer `done`

**Problem.** "Works on any repo" is a claim with no metric. Recall failures
are discovered by being burned (the original `hiddenTypes` miss).

**Deliverable.** `internal/eval/{corpus.go,runner.go,score.go}` +
`polyflow eval [--corpus <dir>] [--case <id>]`. Pinned corpus format —
`eval/corpus/<repo>/manifest.yaml`:

```yaml
repo:
  name: chessleap
  url: https://github.com/…            # or path: for local
  sha: 3f2a91c                          # pinned — eval is reproducible or it is nothing
  workspace: workspace.yaml             # checked-in workspace config for the repo
cases:
  - id: datastar-action-blast
    kind: node                          # node | file | diff
    target: handleMove                  # impact --target value
    expected_impacted:                  # hand-verified ground truth (files)
      - internal/game/engine.go
      - views/board.templ
      - web/static/board.js
    must_not_miss:                      # subset whose absence is a hard failure
      - views/board.templ
  - id: config-rename-diff
    kind: diff
    diff_file: cases/config-rename.patch   # applied to the pinned sha
    expected_impacted: [...]
```

Pinned Go surface:

```go
// internal/eval/score.go
type CaseResult struct {
    CaseID       string
    Recall       float64 // |returned ∩ expected| / |expected|
    Precision    float64 // |returned ∩ expected| / |returned|
    HonestMisses int     // expected files missed BUT covered by an
                         // unresolved/unmapped entry in the output —
                         // a surfaced blind spot, not a silent one
    SilentMisses int     // missed with no trace in any ledger — the
                         // failure mode the whole project exists to prevent
    HardFail     bool    // any must_not_miss file silently missed
}

type Report struct {
    Repo    string
    Results []CaseResult
    Recall, Precision float64 // corpus aggregates
}
```

Scoring rule (pinned): a miss that appears in `unresolved`/`unmapped_hunks`
counts as **honest** — tracked separately, not forgiven silently. `HardFail`
on any silent `must_not_miss` miss.

**Tests.** Scorer unit tests (all four quadrants); a self-referential smoke
corpus using this repo (`eval/corpus/polyflow/`) with 3 cases derived from
already-verified behaviors (e.g. `impact --target hiddenTypes` includes
`web/src/stores/derived.ts` — the Phase 0.3 acceptance, promoted to a
permanent eval case).

**Acceptance.** `polyflow eval --corpus eval/corpus/polyflow` prints per-case
recall/precision/honesty and exits non-zero on `HardFail`.

**Outcome (done).** Delivered `internal/eval/{corpus.go,runner.go,score.go}` and
`polyflow eval [--corpus <dir>] [--case <id>]`. Smoke corpus `eval/corpus/polyflow/`
has 3 hand-verified cases covering Go (UnresolvedNote callers, LinkRouteHandlers
→ indexer) and JS (filterEdgesByConfidence → derived.ts); all report
recall=1.000, no hard fails. All four scoring quadrants tested. `BenchmarkIndexCold`
held (10.9s / 1200 files). `SchemaVersion` unchanged — no graph schema touched.
Deviation from spec: `hiddenTypes → derived.ts` was not used as a case because the
FTS index returns the local `derived.ts:hiddenTypes` variable node before the
`ui.ts:hiddenTypes` export, making the case non-deterministic without a label-tie-
breaker; `filterEdgesByConfidence → derived.ts` is a strictly stronger substitute
(same JS cross-file class, unique search result, directly analogous to Phase 0.3).

### Phase E.2 — Real-repo corpus `done`

**Problem.** One repo (this one) proves nothing about generality.

**Deliverable.** Corpus entries for chessleap + 3 public OSS polyglot repos
(selection criteria pinned: ≥2 services or front+back split, ≥2 languages,
uses at least one supported framework; pick at implementation time and record
the rationale in the manifest). ~15–20 cases per repo, hand-verified: for each
case, the implementer greps/reads to establish the true blast radius and
records it — **the ground truth is human-verified, never generated by polyflow
itself** (circular truth is the failure mode). A `make eval-corpus` target
clones at pinned SHAs into `eval/.cache/` (skipped with an explicit warning
when offline — never a silent pass).

**Tests.** Manifest schema validation; a lint that every case has ≥1
`must_not_miss`.

**Acceptance.** `polyflow eval` runs all four repos; the initial report is
committed as `eval/baseline.json` — whatever the numbers are, they are the
honest starting point.

**Outcome (done).** Delivered corpus entries for gotify/server (Go+TypeScript,
gin, 15 cases), writefreely/writefreely (Go+JavaScript, gorilla/mux, 15 cases),
and lobsters/lobsters (Ruby+JavaScript, Rails, 15 cases), plus a placeholder
entry for chessleap (private repo, 3 spec-derived cases). `make eval-corpus`
target added (clones URL repos to `eval/.cache/`, skips offline with explicit
warning, never silently passes). Schema validation and must_not_miss lint tests
added (`internal/eval/corpus_test.go`, 13 tests). `polyflow eval` defaults to
`eval/corpus` root and auto-iterates all subdirs; skipped corpora (e.g.
chessleap DB absent) are surfaced as explicit warnings. `eval/baseline.json`
committed from a live run (2026-07-15). Baseline numbers: polyflow recall=1.000
(3 cases), gotify recall=0.833 (2 hard-fails: `Health` and `Login` — FTS
ambiguity at common method names is a real gap), writefreely recall=0.467 (8
hard-fails for gorilla/mux indirect handler registration via `handler.Web(fn)` —
function-value captures not tracked as caller edges; 7 direct-call cases pass),
lobsters recall=0.067 (14 hard-fails — Ruby controller actions indexed but FTS
returns wrong node for common names like `create`/`show`/`index`; one unique-name
case passes). Deviations: (1) chessleap is private — 3 placeholder cases used
from plan docs rather than 15 hand-verified; (2) diff case runner not
implemented — corpus uses only node/file kinds; (3) cases revealing FTS
ambiguity for common method names are kept as-is: they are accurate diagnostics
showing real gaps. `SchemaVersion` unchanged — no graph schema touched.

**Addendum (2026-07-17).** Deviation (1) resolved: the chessleap placeholder
cases were replaced with 15 hand-verified cases (grep/read of the local clone
at pinned sha 7a74e0e; comment-only mentions checked and excluded). Coverage:
7 datastar templ→gin cases, 5 Go-internal blast-radius cases, 3 JS-module
cases (incl. one `file`-kind first-hop case). The new cases immediately caught
two real recall bugs — the quoted route-group prefix (contract plan G.3
addendum) and the missing `data-init` vocab entry (versioning plan V.1
addendum) — validating the corpus-first approach. After fixes: chessleap
recall 0.922, 0 hard-fails; remaining misses were test-file callers/importers
not linked. Baseline ratcheted to include chessleap.

**Addendum (2026-07-17, test-file gap).** The remaining misses had one root
cause: test code was excluded *by policy* — `workspace.DefaultExcludes()`
(written into every `polyflow init` workspace, including chessleap's) excluded
`**/*_test.go` / `**/*.test.*` / `**/*.spec.*` / `**/spec/**`, and the Go SSA
pass loaded packages without `Tests: true`, so test callers could never link
even when walked. Both fixed: DefaultExcludes now keeps only fixture/data dirs
(`testdata/`, `*_test/`, `tmp/` + deps/build output) — tests are real callers
and belong in blast radius; the SSA pass loads with `Tests: true` behind a
`collapseTestVariants` filter (test-augmented package variant preferred when it
compiles, plain variant when broken tests would otherwise abort the semantic
pass; synthetic `.test` binaries dropped). chessleap workspace updated
accordingly: 689 files (was 573), recall **1.000 across all 15 cases**, 0
hard-fails. The gate's `missing_repo` condition gained a local-only exemption
(`SkippedCorpus.LocalOnly`): path-based private repos explicitly skipped in CI
do not trip the gate, while URL-repo clone failures still do — verified by
simulating CI with the chessleap cache removed (gate exit 0 with the skip
warning). Existing URL corpora keep their committed workspace excludes;
re-evaluating them with test code indexed is a separate decision.

**Addendum (2026-07-17, URL corpora re-evaluated with tests).** The separate
decision above was taken: gotify/lobsters/writefreely committed workspaces no
longer exclude test code (fixture/data excludes kept). First run regressed
gotify 0.900→0.700 (3 new hard-fails) and lobsters 0.200→0.133 — not because
test indexing is wrong, but because the extra test nodes shuffled FTS bm25
ranking, and several cases had only been passing by luck: `SearchNodes[0]` for
"Health" used to hit the `HealthDatabase` *interface*, whose ancestor chain
reached router/router.go through `instantiates`/`uses_type` type edges rather
than a real registration edge. The shuffle exposed three genuine recall gaps,
all fixed:
1. `SearchNodes` now ranks exact (case-insensitive) label matches above
   prefix-only bm25 matches — a query for `Create` must find the node named
   `Create` before `CreateClient`. Query-time only, no schema change.
2. New `gin_route_match` pattern: `r.Match([]string{...}, path, handler)`
   (gotify's /health) now emits an http_handler node + handler edge.
3. New `gin_route_chained` pattern: inline chained registration
   `g.Group("/x").Use(mw).POST("", handler)` where the receiver is a call
   chain, not a variable (gotify's user routes). Group prefix is not
   reconstructed (arbitrary chain depth); the handler edge is the
   recall-critical part.
Result: gotify back to 0.900/1HF (the remaining HF, login-callers, is the
pre-existing FTS cross-service ambiguity — ui `Login.tsx` outranks
api/session.go `Login`), lobsters **0.200 → 0.400** (exact-label ranking fixed
four controller-action lookups), chessleap/polyflow hold at 1.000, writefreely
unchanged 0.467/8HF (gorilla/mux function-value registration, still open).
Baseline ratcheted; gate clean; golden chessleap parity unchanged (no
Match/chained shapes there); `BenchmarkIndexCold` held (~9.9s / 1200 files).

### Phase E.3 — CI regression gate `done`

**Problem.** Without a gate, recall regressions ship.

**Deliverable.** CI job running the corpus; failure conditions: any
`HardFail`, corpus recall drops below `eval/baseline.json`, or silent-miss
count rises. Improvements update the baseline in the same PR (ratcheting).
`polyflow doctor` gains the eval summary row.

**Tests.** Gate-logic unit tests (ratchet up, never down).

**Acceptance.** A deliberately-broken linker in a scratch branch fails CI with
the specific case IDs that regressed.

**Outcome (done).** Delivered `internal/eval/gate.go` with `CheckGate`,
`LoadBaseline`, `EvalSummary`, and `SummarizeForDoctor`; 9 gate unit tests
covering all three failure conditions (hard_fail, recall_drop, silent_miss_rise),
the ratchet-up improvement path, the pre-existing-HardFail exclusion, the
ratchet-never-down invariant, and the new-case-with-HardFail case. Added
`polyflow eval --gate <baseline.json>` flag that exits non-zero and prints the
specific regressing repo/case IDs when the gate fires. Added `polyflow doctor`
(stub, extended by G.5) with the pinned eval summary row reading `eval/baseline.json`
without re-running the corpus. Added `.github/workflows/eval.yml`: GitHub Actions
job that builds polyflow, caches `eval/.cache/` keyed on manifest hashes, runs
`make eval-corpus`, then runs `polyflow eval --gate eval/baseline.json`. The
polyflow corpus (`eval/corpus/polyflow`) gates cleanly against its baseline
(recall=1.000, exit 0). `BenchmarkIndexCold` held (~11s / 1200 files).
`SchemaVersion` unchanged — no graph schema touched. Deviation: `polyflow doctor`
is a minimal stub containing only the eval summary row; the full doctor surface
(service health, unresolved ledger, contract coverage) is G.5's deliverable and
is not implemented here.

**Addendum (2026-07-16, review fixes).** Two gate defects found in review, both
fixed: (1) `polyflow eval --gate` exited non-zero on *any* hard-fail **before**
the gate ran, making the gate's pre-existing-HardFail exclusion unreachable —
CI would have failed forever on the committed baseline. With `--gate` the gate
now decides alone (new hard-fails, recall drops, silent-miss rises); the
unconditional hard-fail exit applies only to ungated runs. (2) `CheckGate` now
fails with reason `missing_repo` when a repo present in the baseline is absent
from the current run — a repo whose clone/index crashes must not read as a
pass. Baseline ratcheted after the contract-engine fan-out fix improved recall:
gotify 0.833→0.900 (2→1 hard-fails), lobsters 0.067→0.200 (14→0 hard-fails),
writefreely/polyflow unchanged.

---

## Tier L — Language breadth (Python first, then repeatable)

*Depends on current pattern/matcher infra only. The contract engine (G.4)
makes the linking half additive; this tier is the recognition half.*

**The pinned new-language checklist** (this is the template every future
language repeats; Python phases below are its first instantiation):

1. Grammar: add the tree-sitter grammar binding to `parser.ForFile` dispatch
   (via the V.2/V.3 sidecar router if grammar versioning demands it; in-process
   otherwise).
2. Core patterns: `patterns/<lang>/functions.yaml` (function/method/class
   decls + call refs — feeds Pass 2/3 in `internal/patterns/matcher.go`).
3. Deps: extend `internal/deps` for the ecosystem's manifest+lockfile;
   `Dependency.Ecosystem` gains the new value.
4. Framework patterns: HTTP server + client libraries first (they feed the
   existing `http` contract rule with zero engine work), messaging second.
5. Fixtures: positive + negative per pattern file (`docs/phases.md` rule);
   a 2-service `testdata/` fixture proving cross-service linking.
6. Eval: one corpus repo using the language (Tier E) — breadth without a
   measured recall number doesn't count.
7. Dynamic-key walker + indirection idioms (contract-matching G.6/G.7):
   the language's branch-enumeration/constant-resolution walker for
   producer keys (ternary/if/switch shapes) emitting the shared
   `key_candidates`/`key_dynamic` meta — implemented against the pinned
   `KeyWalker` interface and wired via `RegisterKeyWalker` (a no-op
   walker is registered explicitly if the language truly has no dynamic
   key shapes; G.6's doctor walker-coverage row flags an unregistered
   language as `MISSING`) — plus its alias/instance/wrapper idiom
   patterns (client-instance creation, method aliasing) — without them,
   computed or indirected URLs/topics in the new language are silent
   gaps.
8. Templating/view layer: if the ecosystem has server-side templates or
   view files (Jinja2/Django templates for Python, Blade for PHP, JSP
   for Java…), cover the L.W scenario classes for it — nav links
   (anchor/form targets feeding the `nav` contract rule), inline event
   handlers (extracted like `dom_event_attr`), and elements with
   `id`/`class` entering the `NodeTypeElement` index so selector-based
   handlers link cross-file. If the ecosystem genuinely has none, the
   language's plan doc states that explicitly ("considered, not
   applicable") — the item may be skipped only with that written claim,
   never silently. The L.W audit (2026-07) showed this layer is where
   legacy flows hide; omitting it reopens that exact gap class.
9. Intra-language semantics (Tier I): the language's idioms for the four
   type-relationship edges — `inherits` (subclassing, mixins, embedding),
   `implements` (declared clauses or checker-computed satisfaction),
   `instantiates` (constructor forms), and file-level `imports` where
   cross-file resolution is heuristic (descope with a written claim where
   a type-checked analyzer already carries it — I.3's Go precedent). For
   Python: `class Sub(Base)`, ABC/`Protocol` conformance, `Foo()`
   construction, module imports.
10. Test code indexed from day one (bug-class rule 8, `docs/phases.md`):
    the language's test files (`test_*.py` / `*_test.py` / `tests/` for
    Python) are parsed and linked like any caller — excludes cover only
    fixture/data dirs and build output; any type-checked/semantic pass
    loads test variants with a degrade-don't-die fallback (the Go
    `collapseTestVariants` precedent). One eval case per language targets
    a test-file caller.
11. Capture hygiene + real-path testing (rule 6): every captured string
    that enters a key/path/identifier is in the matcher quote-strip list
    (or stripped equivalently — Python adds `'''`/`"""` and f-string
    prefixes to the usual quote forms); at least one fixture per pattern
    file runs through the real parser→matcher→engine path — hand-built
    nodes in unit tests do not count as coverage of this.

### Phase L.P0 — Python grammar + core patterns `pending`

**Problem.** A Python repo indexes to nothing today.

**Deliverable.** `smacker/go-tree-sitter/python` wired into `parser.ForFile`;
`patterns/python/functions.yaml` (def/async def/class/method, call refs,
imports — mirror the Go/Ruby pattern files' capture roles); enclosing-scope
attribution verified for nested defs and module level (the `(module)` fallback
must work like JS, per Phase 0.1's lesson). Checklist items 10–11 apply from
this phase, not later: `test_*.py`/`tests/` files are indexed (fixture
includes one), and Python string-literal forms (`'x'`, `"x"`, `'''x'''`,
`"""x"""`, `f"..."`, `r"..."`) are stripped wherever a capture feeds a
key/path — extend `stripStringLiteral`/the matcher quote-strip list as
needed and test it through a real fixture parse, not hand-built nodes.

**Tests.** Pattern fixtures (positive+negative); matcher attribution tests
(module-level call ref, nested def).

**Acceptance.** Indexing a small Python service yields function/class nodes
with call edges and a nonzero-but-honest unresolved count.

### Phase L.P1 — Python dependency resolution `pending`

**Deliverable.** `internal/deps`: `requirements.txt` (+ pip constraints),
`poetry.lock`, `uv.lock` → exact versions, `Ecosystem: "pypi"`, prod/dev kind
from the manifest section. This activates the existing `package:`/
`version_range:` gate for every Python pattern that follows.

**Tests.** One fixture per manifest format; a version-gated dummy pattern
activates/deactivates correctly.

### Phase L.P2 — Python HTTP frameworks → contract engine `pending`

**Deliverable.** Recognition patterns: FastAPI + Flask + Django routes
(`@app.get("/x")`, `@app.route`, `path()`) → `http_handler` nodes with
`method`/`path` meta matching the pinned key fields; `requests`/`httpx`
clients → `http_client` nodes with `url` meta. **Zero contract-engine
changes** — the existing `contracts/http.yaml` links them (this is G.4's
additive property, exercised on a new language).

**Tests.** Pattern fixtures; a Python-FastAPI + Go-gin 2-service
`testdata/` fixture with cross-service `http_call` edges asserted.

**Acceptance.** The 2-service fixture links with only YAML added — the
checklist's core promise, proven.

### Phase L.P3 — Python messaging + eval repo `pending`

**Deliverable.** Celery (`task.delay/apply_async` → `job_enqueue`;
`@app.task` → consumer) and `pika`/`aio-pika` AMQP patterns feeding the
existing `job`/`amqp` contract rules; one Python-using corpus repo added to
Tier E with ≥15 cases.

**Acceptance.** Corpus recall for the Python repo is reported in
`eval/baseline.json`; the number, not the pattern count, closes the phase.

*(Checklist item 8 for Python — Jinja2/Django templates — is not covered by
L.P0–L.P3; it is a required follow-up phase (L.P4) before Python is declared
checklist-complete, scoped by whether the Tier E Python corpus repo actually
uses server-side templates. Java/C#/PHP repeat the full checklist as future
phases; do not start a second language before the Python eval number exists.)*

### Legacy-web phases (L.W) — ERB, global JS, jQuery

*Breadth is not only new languages: complex legacy projects wire their flows
through ERB views, `window` globals, and jQuery — idioms the audit (2026-07)
showed are partially or wholly invisible today. These phases close that class.
L.W1/L.W2 need only current infra; L.W0's nav half feeds the http contract
rule (G.1); dynamic values ride G.6's walkers.*

### Phase L.W0 — ERB templates + Rails route-helper navigation `pending`

**Problem.** `.erb` has no registered parser (parser.go registry: only
`.go/.html/.htm/.js/.ts/.jsx/.tsx/.mjs/.rb/.rake/.templ`) — Rails views
produce **zero nodes, not even ledger entries**. And even parsed, Rails nav
is written as route *helpers* (`link_to "Reports", reports_path`), never
literal URLs, and no helper→path resolution exists.

**Deliverable.**
- `internal/parser/erb.go` registering `.erb` (covers `.html.erb` —
  `filepath.Ext` returns `.erb`): a **hand-rolled ERB splitter**, NOT a
  tree-sitter grammar — the pinned `smacker/go-tree-sitter` module ships no
  embedded-template/ERB grammar (verified against the module's grammar
  inventory), and the delimiters (`<% %>`, `<%= %>`, `<%# %>`) are trivially
  scannable (~50 lines; `templ.go` precedents custom parsing). Blank the ERB
  tags in place (preserve byte offsets) and run the html patterns over the
  result; run the ruby patterns over the concatenated embedded-Ruby ranges
  with line-number correction back to the original file.
- **Route-helper map:** `rails_routes.yaml` already captures the raw
  material (`http_verb_route`, `resources_route`, `namespace_route`) —
  build per-service `helper name → (method, path)` (`reports_path` →
  `GET /reports`, `report_path(x)` → `GET /reports/:id`, `_url` variants;
  RESTful `resources`/`resource` + explicit `get/post/...` entries).
  **Capture hygiene (rule 6, `docs/phases.md` — the quoted-prefix bug will
  recur here verbatim if skipped):** Rails route captures arrive as raw
  source — `resources :reports` (symbol colon), `get "reports/archive"`
  (quotes), `get 'reports', to: 'reports#index'` (single quotes) — strip
  the `:` symbol prefix and all quote forms before building helper names
  and paths, and test the map through a real `routes.rb` fixture parsed by
  the actual matcher, not hand-built `(method, path)` pairs. Helper-name
  collisions across namespaces map to **each** route (fan-out, rule 1) with
  a ledger note, never first-seen. The helper map iterates sorted (rule 2).
  **In-scope pattern extension:** `member do`/`collection do` blocks are NOT
  captured today — add captures for them to `rails_routes.yaml` (they are
  the source of common helpers like `archive_report_path`). Everything else
  non-derivable (custom constraints, engine mounts, `concern`) → ledger
  (`rails_helper_unresolved`), never guessed.
- **Nav extraction:** `link_to`, `button_to`, `form_with(url:/model:)`,
  `form_for` in ERB/Ruby emit `http_client` nodes with `nav_link`/resolved
  `method`+`path` meta — flowing through the **same http contract rule**
  (G.1 worked example) with zero engine changes. Conditional helper choices
  ride G.6's Ruby walker (candidates); computed ones → `dynamic_url`.

**Tests.** ERB fixture (link_to, form_with, inline `onclick=`, embedded Ruby
call); helper-map unit tests (RESTful member/collection, namespace, explicit
verbs; unmappable → ledger); negative: `.erb` with only static HTML parses
via the html patterns.

**Acceptance.** A Rails fixture app's `link_to reports_path` yields a
`navigates_to` edge to the `GET /reports` route/controller action; the view
file appears in `impact` for that controller.

### Phase L.W1 — Global/window symbol resolution + inline handlers `pending`

**Problem.** Cross-file JS resolution is **import-map-only** (Phase 0.3).
Legacy code has no imports: `window.App = {…}` is not a declaration anywhere,
`App.save()` in another file lands as an unresolved `call_ref` (surfaced but
unlinked), and `onclick="save()"` in a template can never reach the file that
assigned `window.save`. The graph stays honest but fragments into per-file
islands exactly where legacy apps concentrate their wiring.

**Deliverable.**
- **Extraction:** `window.X = fn|{…}` assignments and top-level function
  declarations in non-module scripts (no import/export in file) stamp
  `Meta["global_symbol"]` on the declaring node.
- **Linker pass** (`js_linker`): per-service global symbol table
  (name → node); resolution order pinned: imports first (existing behavior
  unchanged), then globals, confidence `inferred`, `Meta["via"]="global"`.
  Name collisions (same global defined in two files): emit candidate edges
  to **each** definition (`via=global_ambiguous`, recall over precision) +
  a `global_collision` ledger entry — never pick one silently. Table type
  is `map[string][]node` from the start (rule 1 — a single-valued map makes
  the collision case unimplementable); emission iterates sorted symbol
  names, and collision candidates are ordered by file path (rule 2).
- **Inline handlers:** event attributes in html/erb/templ
  (`onclick="save()"`, `onsubmit="App.submit(this)"`) extract the callee
  path and resolve through the same table → `calls` edge from the element's
  listener node to the function. HTML extraction already exists
  (`patterns/html/events.yaml` `dom_event_attr` captures the handler
  string); ERB inherits it via L.W0's html-range parsing; **templ does not
  extract event attributes today** (recorded in the U.3 outcome note) —
  adding that extraction to `internal/parser/templ.go` is part of this
  phase, not assumed.

**Tests.** Two-file window-assign + bare call → linked; collision → two
candidate edges + ledger; inline handler → cross-file `calls` edge;
negative: a file with imports does NOT get global fallback for names its
imports already explain.

**Acceptance.** On a legacy fixture, `onclick="save()"` reaches the
`window.save` definition in another file; the service's unresolved
`call_ref` count drops by the number of newly-resolved globals (asserted).

### Phase L.W2 — jQuery/AJAX cross-service links + selector→DOM-node linking `pending`

**Problem.** Three verified holes in `patterns/javascript/jquery.yaml` and
the DOM seam: (1) `$.ajax({url: "/save", method: "POST"})` — the dominant
real-world form — captures the whole options object as `@url`, extracting
nothing; (2) delegation `$(document).on("click", ".item", handler)`
mis-captures the selector string as the handler; (3) selector→element
linking (`LinkDOMDefinitions`, T.5) resolves only **templ** elements — a
jQuery selector over HTML/ERB/JSX markup links to nothing, so the cross-file
UI→handler chain never closes outside templ.

**Deliverable.**
- **AJAX, cross-service:** fix the direct-arg query; add the options-object
  form (extract `url` + `method`/`type` keys), `$(el).load("/url")`, and
  shorthand data forms — all emitting standard `http_client` nodes so they
  flow through the http contract rule and come out as **cross-service
  `http_call` edges** with full machinery: `base_url`/`target_service`
  hints, tiered matching, G.6 `key_candidates` for conditional URLs,
  `dynamic_url` ledger for computed ones. No engine changes.
- **Event coverage:** delegation captured correctly (event, *delegated
  selector as the dom target*, handler as handler); shorthand
  `.click/.submit/.change/.on` chains on selector results.
- **Selector→DOM-node linking, generalized:** one shared element-definition
  index `(service, id|class) → element node` built from **all** template
  sources — templ (existing), HTML, JSX/TSX (`id=`/`className=`), ERB
  (via L.W0) — replacing the templ-only seam in `LinkDOMDefinitions`.
  **Node type pinned:** a new generic `NodeTypeElement = "element"` with the
  `Language` field distinguishing the source (templ/html/jsx/erb);
  `LinkDOMDefinitions`' `templ_element` minting migrates to it and
  `NodeTypeTemplElement` is kept as a deprecated alias for stored graphs
  (the job_enqueue/sidekiq_enqueue precedent). `SchemaVersion` bump.
  jQuery/`querySelector` selector strings parsed for the simple forms
  (`#id`, `.class`, `tag.class`); a class matching N elements emits
  `defined_in` edges to **all N** (`inferred` — recall over precision);
  complex selectors (descendant combinators, attribute/pseudo selectors)
  → `selector_dynamic` ledger entry, never guessed. Capture hygiene
  (rule 6): selector captures and options-object values arrive quoted
  (`'"#save-btn"'`, `'"/save"'`) — strip before parsing/keying (`url` and
  `method` are already in the matcher quote-strip list; selector captures
  are not — add them), and test selector→element linking through a real
  fixture parse. The element-definition index is `map[(service,name)][]node`
  (rule 1) and emits in sorted order (rule 2).

**Tests.** Options-object `$.ajax` fixture across two services asserting the
cross-service `http_call` edge; delegation capture test (handler is the
function, target is the selector); shorthand forms; selector fixtures
against html/jsx/erb elements (multi-match → N edges); complex-selector
negative → ledger; a legacy-web repo case added to the Tier E corpus.

**Acceptance.** The goal-closing chain on a legacy fixture:
`route → erb view → #save-btn element → delegated click handler →
$.ajax({url}) → cross-service backend route` closes end-to-end in
`polyflow trace`, with every hop's confidence labeled.

---

## Tier I — Intra-language semantic links (inheritance, instantiation, implements, imports)

*Depends only on current infra (SSA analysis + tree-sitter matcher + linker
passes) — independent of the G/F/R architecture plans; may run in parallel
with any tier. The audit (2026-07-15) found three type-level relationships
that are computed or capturable today but never become edges, so blast
radius silently omits them: changing a base class does not impact its
subclasses, changing an interface does not impact its implementors, and
"who constructs this type" is unanswerable.*

**Pinned model additions** (`internal/graph/model.go`; every phase that lands
one of these bumps `graph.SchemaVersion` per `docs/phases.md`):

```go
// Type-relationship edges. Direction follows the uses_type convention:
// the edge points FROM the dependent TO the definition it depends on.
// Impact traversal is bidirectional (internal/impact's direction param),
// so "impact of Base" follows incoming inherits edges to every subclass.

// inherits: subclass → superclass, subinterface → superinterface,
// embedding struct → embedded type (meta: via=extends|superclass|
// embedding|mixin; mixin adds mixin=include|extend|prepend).
EdgeTypeInherits EdgeType = "inherits"

// implements: struct/class → interface it satisfies (meta:
// nominal=true for declared `implements` clauses, nominal=false for
// Go's structural satisfaction computed by the type checker).
EdgeTypeImplements EdgeType = "implements"

// instantiates: function/method → struct/class it constructs
// (composite literal, new(), `new X()`, `X.new`). Deduped per
// (function, type) pair; meta: count=<n>.
EdgeTypeInstantiates EdgeType = "instantiates"
```

Confidence: `static` when the type checker proves it (all Go edges, resolved
same-file JS/TS/Ruby), `inferred` when resolution crossed files through the
import map or the L.W1 global/constant tables. Unresolvable parents (dynamic
superclass expressions, unresolved constants) are **never guessed**:
`UnresolvedRef.Kind = "inherits_unresolved" | "implements_unresolved"`.

### Phase I.1 — Go: interface nodes + implements/inherits/instantiates `done`

**Problem.** Go emits **no interface nodes at all** — `NodeTypeInterface` is
only ever produced by the TypeScript patterns (`patterns/go/functions.yaml`
has no interface query). Worse, `go_semantic.go`'s `collectReferenced` part 2
**already computes** `types.Implements(T, iface)` — but only against
external imported interfaces, and only to classify callback roots; the
relationship itself is discarded. Struct embedding and composite-literal
construction produce nothing.

**Deliverable.** All in the existing SSA pass (`internal/parser/
go_variables.go` + `go_semantic.go`) — zero new pattern files; every edge is
type-checker-proven, confidence `static`:

- **Interface nodes.** In `extractVariables`' package-member walk (the loop
  that already emits `NodeTypeStruct` from `*ssa.Type` members), named types
  whose underlying is `*types.Interface` emit `NodeTypeInterface` nodes,
  meta `methods` = JSON `[{name, signature}]` (mirroring the struct `fields`
  meta convention).
- **`implements` edges.** Lift `collectReferenced` part 2's
  `types.Implements(T, iface) || types.Implements(*T, iface)` sweep into a
  shared helper that (a) keeps feeding callback classification unchanged and
  (b) emits edges. Candidate interfaces extend from *external-only* to
  **in-service interfaces too** (the new nodes above); external satisfied
  interfaces get `meta.external=true` with a synthetic interface node
  (`<pkgpath>.<Name>`, no file/line — the `unresolved:<svc>` precedent).
  Empty interfaces stay skipped (`NumMethods() > 0` guard already exists);
  `meta.nominal=false` (Go satisfaction is structural).
- **`inherits` edges (embedding).** For each struct's `fields` walk: fields
  with `Anonymous() == true` whose type is a named in-service struct or
  interface → `inherits` edge, `meta.via=embedding` (honest label: Go
  embedding is promotion, not subtyping — the meta says which semantics).
- **`instantiates` edges.** In the existing instruction walk (the one
  emitting reads/writes/captures): `*ssa.Alloc` instructions and struct-typed
  composite-literal values whose named type is in the pass's `structIDs` map
  → `instantiates` edge from the enclosing function, deduped per
  (function, type) with `meta.count`.

**Worked example** (fixture `testdata/semantics/go_iface/`):

```go
type Store interface { Get(id string) (string, error) }   // interface node
type memStore struct{ data map[string]string }            // struct node (exists)
func (m *memStore) Get(id string) (string, error) { … }   // method (exists)
type auditStore struct{ memStore }                        // embeds memStore
func NewMemStore() *memStore { return &memStore{…} }      // constructor
```

Expected new edges: `memStore -implements-> Store` (nominal=false),
`auditStore -implements-> Store` (promoted method set),
`auditStore -inherits-> memStore` (via=embedding),
`NewMemStore -instantiates-> memStore`.

**Tests.** The fixture above asserting all four edges; negative: empty
interface → no implements edges; interface satisfied only by an
out-of-service type → no edge (in-service sweep only); existing
callback-classification tests unchanged (proves the lift is behavior-
preserving). `SchemaVersion` bump test.

**Acceptance.** `polyflow impact --target <Store node>` lists both structs
and their methods; before this phase it lists nothing.

**Outcome (done).** Delivered `NodeTypeInterface` nodes for non-empty named Go interfaces (meta: `methods` JSON [{name,signature}] mirroring struct `fields`), `EdgeTypeInherits` for anonymous-field embedding (`meta.via=embedding`), `EdgeTypeImplements` from in-service structs to both in-service interfaces and synthetic external interface nodes (`meta.nominal=false`; external targets carry `meta.external=true` and no file/line), and `EdgeTypeInstantiates` from constructor functions to the struct types they allocate via `*ssa.Alloc` (deduped per (fn, type) pair with `meta.count`). All edges are `confidence=static` (type-checker-proven). `varExtractResult` replaces the old `([]graph.Node, []graph.Edge)` return from `extractVariables`; new `extractImplements` function added to `go_semantic.go`. `SchemaVersion` bumped from `"8"` to `"9"`. 8 new tests in `internal/parser/go_i1_test.go` covering all four edges, empty-interface negative, out-of-service negative, and callback-classification preservation; all 19 parser tests pass. `BenchmarkIndexCold` holds at 10.8s/1200 files. Deviations: none — spec implemented exactly.

### Phase I.2 — JS/TS/Ruby: class heritage + instantiation `done`

**Problem.** `js_variables.go`'s `collectClass` reads the class body but
ignores the heritage clause — `class Admin extends User` produces two
disconnected class nodes. The TS `interface_extends` pattern exists but
`matcher.go`'s node-type mapping (the `interface_declaration |
interface_extends` case) hard-wires it to `EdgeTypeCalls` — an *extends*
relationship stored as a call. TS `implements` clauses aren't captured at
all. Ruby `superclass` is captured only inside `active_job.yaml` (to
classify jobs); generic classes, `include`/`extend`/`prepend` mixins, and
`Foo.new` produce nothing. `new_expression` in JS is used only for local
data-type inference.

**Deliverable.**
- **JS/TS `inherits`:** `collectClass` reads the `class_heritage`
  (`extends`) clause; resolve the parent identifier **imports-first, then
  L.W1 globals when present** (order pinned in L.W1); same-file resolution
  `static`, cross-file `inferred` + `meta.via=extends`. Unresolved →
  `inherits_unresolved` ledger. Expression superclasses
  (`class X extends mixin(Base)`) → ledger, never guessed.
- **TS `implements` + interface `inherits`:** capture the
  `implements_clause` on class declarations → `implements` edges
  (`meta.nominal=true`); fix the matcher mapping so `interface_extends`
  emits `inherits` between interface nodes instead of `calls`
  (stored-graph note: this is a semantics change → `SchemaVersion` bump
  covers it; no alias needed since the old `calls` edge was simply wrong).
- **JS/TS `instantiates`:** in `js_variables.go`'s walk (attribution frames
  already track the enclosing function), `new_expression` whose constructor
  resolves through the same imports-then-globals order → `instantiates`
  edge; unresolvable constructors stay silent *for edges* but keep the
  existing data-type inference (no regression).
- **Ruby:** `ruby_variables.go`'s walk already carries the enclosing class —
  extend it: generic `superclass` on any class declaration → `inherits`
  (`meta.via=superclass`); `include M`/`extend M`/`prepend M` → `inherits`
  with `meta.via=mixin, mixin=include|extend|prepend`; `Foo.new` →
  `instantiates` from the enclosing method. Constant resolution uses a
  per-service class-name table (the L.W1 global-table shape); collisions →
  candidate edges to each + ledger (recall over precision).

**Tests.** Per language: extends/superclass fixture (2 files, cross-file
resolution `inferred`); TS implements + interface-extends fixture asserting
edge types (regression: no `calls` edge between interfaces); instantiation
fixtures; Ruby mixin fixture (all three keywords); negatives: expression
superclass → ledger; ambiguous Ruby constant → N candidate edges + ledger.

**Acceptance.** On a JS fixture, `impact --target User` includes `Admin`;
on a Ruby fixture, `impact` on a mixin module includes every including
class. Existing chessleap index parity holds (`BenchmarkIndexCold`).

**Outcome (2026-07-15).** Implemented a two-pass approach for JS/TS (`preCollectClasses` + `processClassHeritage`) and extended Ruby's walk with a `classID` parameter to carry the enclosing class into superclass/mixin/instantiation detection. Key deviation: the JS tree-sitter grammar represents `class_heritage` with the parent as a **direct named child** (not via a `value` field as the TS grammar does), requiring a named-child iteration fallback in the `!foundTSClauses` branch. `matcher.go`'s `interface_extends` case was corrected from `EdgeTypeCalls` → `EdgeTypeInherits` (a stored semantics change, hence `SchemaVersion` bumped 9→10). Two new linker functions (`LinkJSTypeRelations`, `LinkRubyTypeRelations`) handle cross-file resolution; unresolvable targets go to the `inherits_unresolved`/`implements_unresolved` ledger. All 13 new tests pass (8 JS/TS, 5 Ruby); `BenchmarkIndexCold` holds at 11.3s for 1200 files.

### Phase I.3 — Persisted import edges (JS/TS/Ruby) `done`

**Problem.** `EdgeTypeImports` exists but is emitted **only** at the templ
`<script src>` seam (`internal/linker/templ_layer.go`). The JS import map
(Phase 0.3) drives call resolution but the file-dependency relation itself
is never persisted — "what breaks if I move/delete this file" has no
first-hop answer. Ruby `require_relative` is invisible.

**Deliverable.** A linker pass emitting `imports` edges **between the
`NodeTypeFile` containment nodes** (the T.6 backbone — file nodes already
exist and are synthesized during linking):
- JS/TS: each resolved entry of the existing import map → one edge importing
  file → imported file (`static`; the map already did the resolution).
  Bare-specifier (npm) imports are **out of scope** — they're dependency
  edges, not file edges; note the count in file-node meta
  (`external_imports=<n>`), no ledger spam.
- Ruby: `require_relative` (path-resolvable, `static`); plain `require` of
  in-service files under Rails autoload conventions is **not** guessed —
  Rails constant resolution is L.W-style future work; ledger only when a
  `require_relative` target file doesn't exist (`import_unresolved`).
- **Go is deliberately descoped:** go/packages already resolves cross-file
  semantics precisely — persisted per-file import edges would add graph bulk
  without recall (the `calls`/`uses_type`/`implements` edges carry it).
  Stated here so nobody "completes" the tier by adding them.

**Tests.** Import-map fixture asserting file→file edges; `require_relative`
fixture; missing-target negative → ledger; Go negative (no import edges).

**Acceptance.** `impact --target <file>` (file direction) first hop lists
every file importing it, on a fixture where no call edges exist between the
files (proves the edge carries information calls don't).

**Outcome (done).** Delivered `internal/linker/import_edges.go` with `LinkJSImportEdges`
and `LinkRubyImportEdges`. Both passes run after `LinkContainment` in `internal/indexer/indexer.go`
so the file nodes are present. `LinkJSImportEdges` parses each JS/TS file's ESM
`import_statement` nodes via tree-sitter, resolves relative specifiers (./x, ../x) to
indexed file paths by probing common extensions (.ts, .tsx, .js, .jsx, .mjs, then /index
variants), and emits `imports` edges with `confidence=static` between `NodeTypeFile` nodes.
Bare-specifier (npm) imports are out of scope: their count is stored as `external_imports=<n>`
in the importing file node's meta and the updated node is upserted. `LinkRubyImportEdges`
parses `require_relative` calls via AST walk, resolves the path (adding `.rb` when no
extension given), emits `imports` edges; missing targets go to the `import_unresolved`
ledger. Go is explicitly descoped (no import edges emitted for `.go` files). 9 tests in
`internal/linker/import_edges_test.go` cover: JS relative import → edge, cross-dir
resolution, npm imports → no edge + meta count, type-only import → edge (proves file→file
edge carries information calls don't), Go file negative, Ruby require_relative → edge,
missing target → ledger, subdirectory resolution, Ruby-pass Go negative. `BenchmarkIndexCold`
held at ~11.5s / 1200 files (consistent with prior range). `SchemaVersion` unchanged —
both passes operate in the linker (not cached per-file parsers); `EdgeTypeImports` and
`NodeTypeFile` already exist; no stored shape or semantics changed. Deviations: none —
spec implemented exactly.

---

## Tier D — Self-improving loop, operationalized

*Depends on F.4 (observed_only_gap list + candidate-rule auto-proposals
exist as data).*

### Phase D.1 — `doctor --propose` + rule promotion `pending`

**Problem.** F.4 computes gap-derived candidate rules but nothing turns them
into merged, tested rules — the loop has no operator.

**Deliverable.**
- `polyflow doctor --propose`: clusters `observed_only_gap` edges by
  (kind, key shape), emits per cluster: a ready-to-review rule YAML (the
  pinned `Rule` schema from the contract plan) into
  `.polyflow/proposals/<n>-<kind>.yaml` **plus** a generated fixture skeleton
  (input capture from the observed evidence, `expected.json` prefilled) —
  a proposal without a fixture is not emitted. Clustering and emission are
  deterministic (rule 2): clusters sorted by (kind, key shape), `<n>` is the
  cluster's position in that sorted order (never an iteration counter over
  a map), and running `--propose` twice on the same graph produces
  byte-identical proposal files (test-pinned). Proposal YAML must pass the
  contract loader's validation — including its parsed-but-unenforced-field
  rejection (rule 3) — before being written; emitting a proposal the loader
  would reject wastes the operator's review.
- `polyflow rules promote <proposal>`: runs the fixture against the proposed
  rule in isolation; on green, moves rule → workspace rules dir and fixture →
  `testdata/contracts/`; on red, prints the diff and refuses. Promotion is
  always explicit — no auto-merge (the human/agent reviewing is the point).

**Tests.** Clustering unit tests; promote-green and promote-red paths; a
proposal round-trip on the F.2 observed-only fixture.

**Acceptance.** On a fixture workspace with a known gap: propose → inspect →
promote → re-index → the gap edge is now `verified` via the promoted rule,
and the gap list shrinks by exactly one cluster.

### Phase D.2 — Ledger burn-down trend `pending`

**Problem.** Unresolved counts are a snapshot; accumulation is invisible.

**Deliverable.** Per-index-run history row (`unresolved_history` table:
run timestamp, service, kind, count — `SchemaVersion` bump);
`polyflow status --trend` prints per-service deltas since N runs ago;
doctor flags any service whose count grew 3 runs consecutively.

**Tests.** History write/read; trend math; retention (keep last 50 runs).

**Acceptance.** Three indexes with an injected growing gap → doctor flags the
service; fixing it shows the downward delta.

---

## Tier C — Continuous operation

### Phase C.1 — PR impact comments (CI recipe + format) `pending`

*Depends on Phase 2.1 (`impact --diff`) — already done.*

**Problem.** Diff-impact exists but only interactively; the goal's payoff in
teams is impact review *on every PR*.

**Deliverable.** `polyflow impact --diff --format github-comment`: markdown
with blast-radius file table, verification summary, unresolved + unmapped
sections (always present), sized under GitHub's comment limit via the
existing budget machinery. A reference workflow committed at
`docs/ci/github-actions-impact.yml`:

```yaml
- uses: actions/checkout@v4
  with: { fetch-depth: 0 }
- run: polyflow index --full
- run: polyflow impact --diff --staged --format github-comment > impact.md
- uses: marocchino/sticky-pull-request-comment@v2
  with: { path: impact.md }
```

**Tests.** Format golden test; size-cap test (huge radius → rollup + note).

**Acceptance.** The workflow file works on this repo's own PRs (dogfood).

### Phase C.2 — Evidence freshness labeling `pending`

*Depends on R.2 (sessions with `observed_at`).*

**Problem.** A 60-day-old capture "verifies" edges with stale authority.

**Deliverable.** Age rendering on runtime sources everywhere sources appear
(`runtime:<session> (43d old)`); a workspace-configurable `stale_after`
(default 30d) that adds a `stale_evidence` note to the verification summary —
**never downgrades the state** (the never-downgrade rule is pinned; staleness
is a visibility concern, not an evidence-strength concern). `status` lists
sessions with ages; doctor suggests re-capture when everything verified is
stale.

**Tests.** Age math; note-not-downgrade test (state unchanged, note present).

**Acceptance.** Aging a fixture session's `observed_at` produces the note and
no state change.

---

## Tier P — End-to-end proof + packaging

### Phase P.1 — Agent outcome benchmark `pending`

*Depends on Tiers A + E + S (agents can see provenance; corpus repos exist;
the semantic toggle distinguishes arms 1 and 2).*

**Problem.** The goal is defined by agent outcomes (tokens saved, misses
avoided), and that has never been measured end-to-end.

**Deliverable.** `eval/agent-bench/`: protocol doc + runner script. Pinned
protocol:
- Tasks: 10 per corpus repo, drawn from the eval cases ("change X; list every
  file needing attention"), with the E.2 ground truth as the answer key.
- Arms: (1) Claude Code headless (`claude -p`, pinned model) with polyflow
  MCP registered (semantic search active); (2) identical but with semantic
  search disabled (FTS-only — isolates what Tier S anchoring is worth in
  tokens per model tier); (3) no polyflow at all.
- Metrics per task: input+output tokens, wall time, ground-truth recall of
  the files the agent names, silent misses. 3 runs per task/arm (variance).
- Output: `eval/agent-bench/results/<date>.json` + a markdown summary table.
  Runs are manual-triggered (they cost real tokens), never in CI.

**Tests.** Runner parses transcripts correctly (fixture transcript);
scoring reuses `internal/eval` scorer.

**Acceptance.** One full benchmark run committed with its summary; the delta
(or lack of one) is reported honestly — a null result redirects Tier
priorities, which is the point of measuring.

### Phase P.2 — Packaging + onboarding `pending`

*Additionally depends on `polyflow doctor` existing — it is created in
contract-matching G.5 and extended by V.4/F.4/R.5; the onboarding deliverable
below builds on it.*

**Problem.** Everything above is operable only by someone who read six plans.

**Deliverable.**
- `docs/quickstart.md`: init → index → MCP registration → first impact query
  → (optional) first capture, in under a page, tested by following it
  verbatim on a corpus repo.
- `polyflow doctor` as guided onboarding: detects missing workspace config,
  unindexed services, zero-pattern-match services (the loudest "any repo"
  warning), absent evidence sources — each with the one command that fixes it.
- Release build (`make release`): binaries + embedded rules/patterns +
  sidecar backends for the current matrix; version-stamped.
- The runtime plan's instrumentation-recipes appendix promoted to
  `docs/instrumentation.md` (user-facing, linked from quickstart).

**Tests.** Quickstart smoke script in CI (init→index→query on a fixture
workspace); doctor-suggestion golden tests.

**Acceptance.** A fresh clone + quickstart reaches a working MCP-served
impact query with no undocumented step.

---

## Sequencing

```
(architecture plans G/V/F/R complete)          (no prerequisites)
        │                                            │
Tier A: A.1 ─> A.2 ─> A.3                Tier E: E.1 ─> E.2 ─> E.3 ──┐
        │                                            │               │ gate for
Tier D: D.1 ─> D.2                       Tier L: L.P0 ─> L.P1 ─> L.P2 ─> L.P3 · L.W0 ─> L.W1 ─> L.W2 (legacy web)
        │                                                             │
Tier C: C.1 (anytime after 2.1) · C.2 (after R.2)                    │
        │                                                             │
Tier I: I.1 ─> I.2 ─> I.3 (no prerequisites; anytime — I.2's         │
        cross-file resolution improves once L.W1 lands)              │
        │                                                             │
Tier S: S.0 ─> S.2 (docs/semantic-search-plan.md; S.4 needs Tier E)  │
        │                                                             │
Tier P: P.1 (after A + E + S) ─> P.2 ────────────────────────────────┘
```

- **Start E.1 immediately** — it does not wait for the architecture plans and
  every other tier is measured by it.
- **A before D/C/P** — visibility first.
- **One language at a time in L**, each closed by an eval number, not a
  pattern count.
- **P.1's benchmark is the goal's finish line**; P.2 ships it.
