# Polyflow — Plan 14: Agent Trust — Measured Correctness ≥ 95% (Tier T)

Status legend: `pending` · `in progress` · `done`

> **Prerequisites: eval harness (E.1–E.3), evidence fusion (verification
> states), runtime capture.** T.0 and T.1 run on current infrastructure any
> time after plan-1 B.3 (qualified eval targets). T.2/T.3 need only the MCP
> server. Fleet-wide numbers additionally need the plan-7 fleet corpora —
> chessleap works immediately. Execute **before plan-9** lands its
> `/api/health` shape if possible: T.0's stamp is an additive field there
> (touchpoint noted in T.0).
>
> Follows the repo per-phase process (`docs/phases.md`) — one phase per
> commit, positive+negative fixtures, `BenchmarkIndexCold` held,
> `graph.SchemaVersion` bump on stored-shape change, and the nine proven
> bug-class rules are binding.

## Context

The product goal ([[polyflow-agent-context-goal]] restated): an agent using
polyflow instead of grep/read exploration must reach **correct answers ≥ 95%
of the time**, cheaply enough that it keeps using the tool. One silent miss
and the agent falls back to grepping *on top of* the query cost — trust is
the whole ballgame.

Most of the trust machinery **already exists — do not re-spec it**:

- **Per-query blind spots**: `impact.Result.AttachUnresolved`
  (`internal/impact/impact.go`) scopes the unresolved ledger to traversed
  files; `context`/`trace` carry the same `unresolved` + `unresolved_note`
  sections. Always present, `[]` when clean.
- **Verification protocol**: every edge carries `verification_state`
  (`verified` / `candidate` / `observed_only_gap`), aggregated in the
  always-present `verification_summary`; the MCP `semanticsParagraph`
  (`internal/mcpserver/mcpserver.go:24`) already teaches agents the fallback
  protocol ("candidate → one cheap grep confirms; empty means clean, absent
  means error").
- **Regression gate**: `eval.CheckGate` (`internal/eval/gate.go`) fails on
  new hard-fails, recall drops, silent-miss rises, and missing repos.

Three gaps remain, with **no owning phase in plans 1–13**:

1. **Trust numbers are invisible at query time.** Recall lives in
   `eval/baseline.json`, keyed by corpus repo. The workspace an agent
   actually queries carries no record of how trustworthy its index is. An
   agent should calibrate differently on chessleap (measured 1.000) than on
   a fresh Rails index (lobsters 0.400 is the honest predictor).
2. **Runtime-observed gaps die as edge states.** An `observed_only_gap` edge
   is a recall bug that found *us* — proof static analysis missed something
   real. Today it renders in output and is never converted into a permanent
   regression case.
3. **The target metric is unmeasured.** Graph recall is a proxy. The claim
   "agents are correct ≥ 95% of the time *using this tool*" is currently
   unfalsifiable: there is no harness that asks an agent real questions
   through the MCP surface and scores the answers. 0.85 recall + spoken
   blind spots can genuinely deliver ≥ 95% agent-correctness (the agent
   verifies exactly the flagged areas) — but only a measurement makes that
   a claim instead of a hope.

Trust contract (binding): recall over precision; no silent gaps;
confidence-labeled output. New here: **no unmeasured trust claims** — every
number an agent (or the README) sees traces to a committed baseline.

---

## Phase T.0 — Workspace trust stamp `pending`

**Problem.** `polyflow status` on an indexed workspace reports nodes, edges,
and unresolved counts — but not whether this workspace's answers have ever
been *measured*. Query outputs carry per-query gauges (`unresolved`,
`verification_summary`) yet nothing that says "this index scored 1.000 on
12 hand-verified cases three days ago" or, equally important, "this index
has never been evaluated."

**Deliverable.**

1. New subcommand `polyflow eval stamp --corpus <dir>`: runs the single
   corpus at `<dir>` (its `manifest.yaml` must reference the **current
   workspace** — validated by comparing `repo.workspace` against the loaded
   workspace name; mismatch is an error, not a skip) against the current
   workspace's `.polyflow/graph.db`, scores it with the existing
   `eval.Score`, and persists the result as DB meta key `trust_stamp` via
   `SetMeta` (same mechanism as `contract_coverage` / `unparsed_files` —
   **no SchemaVersion bump**, meta keys are forward/backward compatible):

   ```json
   {"cases": 12, "corpus": "chessleap", "hard_fails": 0,
    "measured_at": "2026-07-19T10:31:00Z", "recall": 1.0,
    "silent_misses": 0}
   ```

   Keys emitted **sorted** (rule 2 — byte-compared in tests). `recall` is
   the corpus macro-average from `eval.Report`; `silent_misses` and
   `hard_fails` are summed over cases.
2. A pinned struct, single source of truth for readers and writers:

   ```go
   // internal/eval/stamp.go
   // TrustStamp is the persisted record of the workspace's last measured
   // eval. Measured=false is the zero state: this index has never been
   // evaluated — absence of measurement is reported, never implied away.
   type TrustStamp struct {
       Measured     bool    `json:"measured"`
       Corpus       string  `json:"corpus,omitempty"`
       Cases        int     `json:"cases,omitempty"`
       Recall       float64 `json:"recall,omitempty"`
       HardFails    int     `json:"hard_fails,omitempty"`
       SilentMisses int     `json:"silent_misses,omitempty"`
       MeasuredAt   string  `json:"measured_at,omitempty"` // RFC 3339 UTC
       Stale        bool    `json:"stale,omitempty"`       // computed at read time, never stored
   }
   ```

   `LoadTrustStamp(ctx, store)` returns `TrustStamp{Measured: false}` when
   the meta key is absent, and sets `Stale: true` when the index's
   last-indexed time (existing status source) is **newer** than
   `MeasuredAt` — the graph changed since it was measured.
3. Surfacing (all always-present, the `verification_summary` convention —
   absence would look like certainty):
   - `impact`/`context`/`trace` JSON results gain `"trust": {...}`. Like
     `verification_summary`, the field **survives any token budget**.
   - `polyflow status` prints a `Trust:` line —
     `Trust: recall 1.000 over 12 cases (chessleap corpus, 2026-07-19)` or
     `Trust: UNMEASURED — run 'polyflow eval stamp' (see docs/plan-14)` or
     `... STALE (index newer than measurement)`.
   - MCP: `semanticsParagraph` gains one sentence: *"The trust section
     reports this workspace's last measured eval recall; measured=false or
     stale=true means answers here are unaudited — weigh the unresolved
     section more heavily."*
   - Plan-9 touchpoint (additive, no plan-9 edit needed now): `/api/health`
     includes the same `trust` object when plan-9 U-B.6 is built.

**Worked example.** On chessleap:
`polyflow eval stamp --corpus eval/corpus/chessleap` → status shows
`Trust: recall 1.000 over N cases`; `impact --target MoveNotationPanel
--format json --max-tokens 500` still contains the full `trust` block.
On a never-stamped workspace, the same query contains
`"trust": {"measured": false}`.

**Tests.** Stamp round-trip (write → `LoadTrustStamp` → equal); sorted-JSON
byte determinism across two runs; workspace-mismatch error; staleness
(stamp, reindex, read → `stale: true`); budget-survival (tiny `--max-tokens`,
`trust` intact — the `VerificationSummary` test as template); unmeasured
zero state in all three query outputs; status golden outputs for all three
states; MCP round-trip carries `trust`.

**Acceptance.** Chessleap stamped and `status` shows its measured recall;
this repo's own workspace (never stamped) shows `UNMEASURED` — both
verified by running the real binary.

---

## Phase T.1 — Promote runtime gaps to eval cases `pending`

**Problem.** Runtime capture marks edges static analysis missed as
`observed_only_gap` (fusion contract: runtime confirms or exposes gaps,
never fabricates). These are ground-truth recall failures harvested from
real execution — and today they evaporate: nothing turns them into
permanent regression cases, so a parser regression could silently
reintroduce them.

**Deliverable.**

1. New subcommand `polyflow eval promote-gaps --corpus <dir>`: scans the
   current workspace graph for edges with
   `VerificationState == graph.StateObservedOnlyGap` and appends one eval
   case per gap to `<dir>/manifest.yaml`:

   ```yaml
   - id: gap-a1b2c3d4            # gap- + first 8 hex of sha256(from_id|to_id)
     kind: node
     target: <To-node label>
     service: <To-node service>   # the plan-1 B.3 qualifier — required here,
     expected_impacted: []        #   gap targets must resolve unambiguously
     must_not_miss:
       - <From-node file path>
   ```

   Direction pinned: runtime observed `From → To` that static missed, so
   `impact --target <To>` must include `From`'s file — that is exactly the
   miss the gap proves.
2. **Idempotent and deterministic**: skip when a case with the same `id`
   exists; new cases appended sorted by `id`; running twice yields a
   byte-identical manifest (rule 2).
3. Dry-run default prints the cases it *would* add; `--write` persists.
   Each written case is a **failing case by construction** (static missed
   it) until the owning parser fix lands — the eval gate's pre-existing-
   hard-fail exemption (`gate.go` condition 1) means promotion never trips
   the gate by itself; it ratchets the *goal line*, not the pass bar.

**Worked example.** A captured session on datascience observes an AMQP
consume edge `handleReport → parseCSV` that static missed. Promotion adds
case `gap-…` with `target: parseCSV`, `must_not_miss: [<handleReport
file>]`. When a later plan phase teaches the parser that pattern, the case
flips to a pass and is ratcheted like any other.

**Tests.** Fixture graph with two gap edges + one verified edge → golden
manifest (only the gaps promoted, sorted); idempotence (second run,
byte-identical); dry-run writes nothing; dedupe against a hand-authored
case with the same generated id; manifest still loads via `LoadManifest`
after promotion (schema round-trip).

**Acceptance.** On a workspace with a fused capture session (datascience,
or the runtime fixture if unavailable), promote-gaps emits ≥ 1 case,
`polyflow eval` runs the corpus without schema errors, and the new case is
recorded as a pre-existing hard-fail (gate stays green).

---

## Phase T.2 — Agent-correctness corpus + runner `pending`

**Problem.** No harness measures the actual product metric: an agent,
restricted to polyflow's MCP tools, answering real questions about a repo —
scored for correctness. Graph recall (Tier E) is a proxy; the 95% goal is
about *answers*.

**Deliverable.**

1. Corpus manifests gain an optional top-level `agent_cases:` list
   (`internal/eval/corpus.go`, additive — repos without it are simply not
   agent-evaluated):

   ```yaml
   agent_cases:
     - id: move-panel-blast
       question: >
         What files are affected if I change the MoveNotationPanel
         component? List every file.
       required_facts:            # ALL must appear in the answer
         - ui/components/movenotationpanel.templ
         - activity/handlers/play.go
       forbidden_facts:           # NONE may appear (wrong conclusions)
         - "no other files"
       max_turns: 8
   ```

2. New subcommand `polyflow eval agent --corpus <dir>`: for each case,
   invoke an agent CLI with **only polyflow MCP tools** and capture its
   final answer. Pinned default command template (overridable via
   `--agent-cmd` / env `POLYFLOW_AGENT_CMD`; `{...}` placeholders are
   substituted, the prompt is passed on stdin):

   ```
   claude -p --mcp-config {mcp_config} --allowedTools "mcp__polyflow__*" \
     --max-turns {max_turns} --output-format json
   ```

   `{mcp_config}` is a temp file the runner writes, pointing at
   `polyflow mcp` for the corpus workspace. Pinned prompt preamble
   (prepended to every question): *"Answer using only the polyflow tools
   provided. Name concrete file paths. Do not guess: if the tools report
   unresolved references or unmeasured trust, verify or say so."*
3. **Scoring is deterministic — no LLM judge in the loop.** An answer is
   `correct` iff every `required_facts` entry appears in it and no
   `forbidden_facts` entry does. Matching pinned: case-insensitive
   substring after normalizing `\` → `/`; a path fact also matches by its
   basename-suffix (answer says `handlers/play.go`, fact is
   `activity/handlers/play.go` → match; the reverse — fact is a suffix of
   an answer path — does not). LLM nondeterminism moves the *numbers*, not
   the *scoring rules*.
4. Per-case record includes cost, from the agent CLI's JSON output
   (`usage` / `total_cost_usd` fields when present, else zeros):

   ```go
   // internal/eval/agent.go
   type AgentCaseResult struct {
       ID           string   `json:"id"`
       Correct      bool     `json:"correct"`
       MissingFacts []string `json:"missing_facts"`   // always present, [] when correct
       ForbiddenHit []string `json:"forbidden_hit"`   // always present
       Turns        int      `json:"turns,omitempty"`
       InputTokens  int      `json:"input_tokens,omitempty"`
       OutputTokens int      `json:"output_tokens,omitempty"`
       Answer       string   `json:"answer"`          // verbatim, for audit
   }
   type AgentReport struct {
       Repo        string            `json:"repo"`
       Correctness float64           `json:"correctness"` // correct / total
       Results     []AgentCaseResult `json:"results"`
   }
   ```

   Token counts are how this plan reports progress on the token-reduction
   goal: the same questions, answered with tool-only context, at measured
   cost.
5. **This phase needs network + a `claude` CLI login. It is a release
   ritual, not CI** — the runner exits with a distinct message (not a
   silent pass, the `SkippedCorpus` precedent) when the agent CLI is
   absent. Everything else in this plan stays CI-clean.

**Worked example.** The chessleap case above: the agent calls
`mcp__polyflow__impact` once, answers with the file list; both required
facts substring-match; correctness for the repo = 1.0; the report records
~2 turns and the token counts.

**Tests** (no live agent — the runner is tested against a **fake agent
binary** fixture, a shell script echoing canned CLI-shaped JSON; rule 6's
spirit: real runner code path, controlled I/O). Scoring table-test: all
facts present → correct; one missing → incorrect with it listed;
forbidden hit → incorrect; basename-suffix path match and its non-matching
reverse; `\`-normalization. Runner: template substitution golden; missing
CLI → distinct skip, exit code pinned; corpus schema round-trip with
`agent_cases`; report JSON determinism given fixed fake answers.

**Acceptance.** `polyflow eval agent --corpus eval/corpus/chessleap` runs
live on the author machine with ≥ 5 hand-authored `agent_cases` covering
impact, context, trace, and one deliberately-blind area (a question whose
correct answer requires citing an unresolved ref), and prints per-case
verdicts + total tokens. The 5 cases are committed in the same commit.

---

## Phase T.3 — Correctness bar, ratchet, and doctor panel `pending`

**Problem.** T.2 produces numbers; nothing holds them. The ≥ 95% goal needs
the same treatment recall got: a committed baseline, a gate, and a single
honest place to read the current state.

**Deliverable.**

1. `polyflow eval agent --gate` compares against a committed
   `eval/agent-baseline.json` (an `AgentMultiReport` mirroring
   `MultiReport`: `generated_at` + per-repo `AgentReport`s + skipped).
   Pinned failure conditions (mirroring `CheckGate`, implemented as
   `CheckAgentGate` beside it — the shapes differ enough that reuse would
   contort `gate.go`):
   1. any case `correct` in baseline and incorrect now;
   2. per-repo `Correctness` drops below baseline;
   3. a baseline repo absent from the run (`LocalOnly` exemption honored).
2. **The bar**: chessleap `Correctness ≥ 0.95` is the acceptance line for
   the product claim. Fleet repos (plan-7 corpora) are *measured and
   recorded* with dates — their bars ratchet from measured reality, not
   aspiration (the lobsters-0.400 lesson: predictors are published, targets
   are earned). Any README/docs statement of the 95% claim must cite
   `eval/agent-baseline.json` — no unmeasured trust claims (Context rule).
3. `polyflow doctor` gains a **Trust panel** (one table, reusing T.0's
   `LoadTrustStamp` and the graph's existing gauges):

   ```
   Trust
     eval recall        1.000 (12 cases, chessleap, 2026-07-19)   [or UNMEASURED / STALE]
     agent correctness  0.95+ (5 cases, 2026-07-19)               [or UNMEASURED]
     edges verified     18% verified · 79% candidate · 3% observed-gap
     unresolved density 894 refs / 12150 edges (7.4%)
   ```

   Every row degrades to an explicit `UNMEASURED`, never blank.

**Tests.** `CheckAgentGate` table-tests for all three conditions +
`LocalOnly` exemption; baseline round-trip determinism; doctor golden
outputs for measured/unmeasured/stale mixes; a gate test where a *new*
case (absent from baseline) fails without tripping condition 1
(pre-existing-failure precedent — new cases enter the baseline failing,
then ratchet).

**Acceptance.** `eval/agent-baseline.json` committed with the live
chessleap run from T.2; `polyflow eval agent --gate` green against it;
`polyflow doctor` on chessleap shows all four Trust rows populated; on
this repo's workspace shows `UNMEASURED` rows.

---

## Key files

- **New:** `internal/eval/stamp.go` (T.0), `internal/eval/agent.go` +
  `internal/eval/agent_gate.go` (T.2/T.3),
  `eval/agent-baseline.json` (T.3).
- **Modify:** `internal/eval/corpus.go` (T.2 `agent_cases`),
  `internal/impact/impact.go` + `internal/context/summary.go` +
  `internal/context/files.go` + `internal/trace/trace.go` (T.0 `trust`
  field), `internal/mcpserver/mcpserver.go` (T.0 sentence),
  `cmd/polyflow/main.go` (eval subcommands, status line, doctor panel).

## Reuse (do not rebuild)

- `eval.Score`, `eval.LoadManifest`, `eval.RunAll` skip/report machinery
  (`internal/eval/`); `eval.CheckGate` as the structural template for
  `CheckAgentGate`.
- `SetMeta`/`GetMeta` (`internal/graph/store.go:628`) for the stamp — the
  `contract_coverage` precedent.
- `VerificationSummary`'s always-present + budget-survival conventions and
  tests as the template for the `trust` field.
- `graph.StateObservedOnlyGap` and the fused-edge query path (T.1 reads,
  never writes, fusion state).
- `SkippedCorpus`/`LocalOnly` semantics for everything that only runs on
  the author machine.

## Sequencing

```
T.0 ──> T.1 ──> T.2 ──> T.3     (linear; T.1 may swap after T.2 if no
                                 captured session is at hand — it has no
                                 code dependency on T.2)
```

T.0 first: the stamp is the cheapest trust win and plan-9's `/api/health`
wants it. T.2 before T.3 by definition. The whole tier slots after plan-1
(B.3 qualifiers, which T.1 case generation requires) and is most valuable
run again after each of plans 2–8 lands — restamping is one command.

## Risks — honest

- **Agent-run nondeterminism (T.2/T.3).** The same case can pass and fail
  across runs near the bar. Mitigations pinned: deterministic scoring
  rules, `max_turns` caps, and the gate compares *committed baselines* (a
  flaky flip shows up as a reviewed baseline diff, not silent drift). If
  flake exceeds tolerance in practice, add `--runs N` majority voting as a
  follow-up — deliberately descoped now.
- **Substring scoring is a floor, not a ceiling.** An answer could mention
  a required path while reasoning incorrectly around it. `forbidden_facts`
  and the verbatim `Answer` field (hand-audited when authoring cases) bound
  this; the metric is honest as "cites the right files," not "flawless
  prose."
- **External CLI dependency (T.2).** `claude -p` flags may drift; the
  command template is data (`--agent-cmd`/env override), not code — drift
  is a config fix, not a phase.
- **Stamp staleness is coarse (T.0).** Any reindex marks the stamp stale
  even for an irrelevant one-line change. Accepted: false-stale is the
  honest direction (recall-over-precision applied to trust itself), and
  restamping is one command.
