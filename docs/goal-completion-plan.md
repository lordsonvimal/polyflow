# Polyflow — Goal-Completion Plan (post-architecture end-to-end)

Status legend: `pending` · `in progress` · `done (commit <sha>)`

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
- **Tier L** — "any repo" needs more *languages* (Python first).
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

### Phase A.1 — Provenance in query outputs `pending`

**Problem.** `context`, `impact`, and `trace` JSON emit per-edge `confidence`
only. `verification_state`, `sources`, and `verified_granularity` never reach
the agent, so a `verified` edge and an unconfirmed `candidate` edge look
identical.

**Deliverable.**
- Per-edge fields in all three commands' JSON: `verification_state`,
  `verified_granularity`, and `sources` as compact strings
  (`"runtime:<session>/<trace_id>"`, `"static:file.go:42"` — the SourceRef
  `Provider + ":" + Ref` form; full structs only under a `--verbose-sources`
  flag to protect token budgets).
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

### Phase E.1 — Corpus format + scorer `pending`

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

### Phase E.2 — Real-repo corpus `pending`

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

### Phase E.3 — CI regression gate `pending`

**Problem.** Without a gate, recall regressions ship.

**Deliverable.** CI job running the corpus; failure conditions: any
`HardFail`, corpus recall drops below `eval/baseline.json`, or silent-miss
count rises. Improvements update the baseline in the same PR (ratcheting).
`polyflow doctor` gains the eval summary row.

**Tests.** Gate-logic unit tests (ratchet up, never down).

**Acceptance.** A deliberately-broken linker in a scratch branch fails CI with
the specific case IDs that regressed.

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
7. Dynamic-key walker (contract-matching G.6): the language's
   branch-enumeration/constant-resolution walker for producer keys
   (ternary/if/switch shapes), emitting the shared `key_candidates`/
   `key_dynamic` meta — without it, computed URLs/topics in the new
   language are silent gaps.

### Phase L.P0 — Python grammar + core patterns `pending`

**Problem.** A Python repo indexes to nothing today.

**Deliverable.** `smacker/go-tree-sitter/python` wired into `parser.ForFile`;
`patterns/python/functions.yaml` (def/async def/class/method, call refs,
imports — mirror the Go/Ruby pattern files' capture roles); enclosing-scope
attribution verified for nested defs and module level (the `(module)` fallback
must work like JS, per Phase 0.1's lesson).

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

*(Java/C#/PHP repeat L.P0–L.P3 as future phases via the same checklist; do
not start a second language before the Python eval number exists.)*

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
  a proposal without a fixture is not emitted.
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
Tier D: D.1 ─> D.2                       Tier L: L.P0 ─> L.P1 ─> L.P2 ─> L.P3
        │                                                             │
Tier C: C.1 (anytime after 2.1) · C.2 (after R.2)                    │
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
