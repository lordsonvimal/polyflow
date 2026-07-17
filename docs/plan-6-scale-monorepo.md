# Polyflow — Plan 6: Scale, Monorepos & the Coverage Contract (Tier N)

Status legend: `pending` · `in progress` · `done`

> **Prerequisites.** The eval harness (E.1–E.3, `done`) and plan 1's B.0
> (the unparsed ledger feeds N.3's tier report). N.0–N.2 are otherwise
> independent of plans 2–5; **N.3 runs last of everything** — it writes the
> honest coverage contract for whatever actually shipped. This is plan
> **6 of 6**.
>
> Follows `docs/phases.md`; rule 4 (gate logic: absence is failure, exit
> order pinned, simulate CI before landing) is the named risk for N.0's
> gate — it is the exact rule class the E.3 gate shipped broken twice.

## Context

**Why.** "Works on any repo" has two axes the other plans don't measure:
**size** (a 30k-file monorepo must index in tolerable time and memory —
today's perf gate is `BenchmarkIndexCold` on a 1,200-file synthetic tree
and chessleap at ~690 files) and **shape** (workspace/service discovery is
hand-written `workspace.yaml`; monorepo tooling already knows the answer).
Two further consistency gaps ride along: **generated code** (protobuf/
openapi output distorts both recall math and search ranking if handled
naively) and the absence of a written **coverage contract** — the goal
docs say "any repo" while the honest guarantee is a matrix of
(languages present × evidence sources available). N.3 writes that matrix
down and makes doctor report where a given repo sits in it.

Trust contract addition pinned for this plan: **degradation is always
labeled**. Any scale-driven fallback (SSA timeout, embedding skip) must
leave the same kind of visible trace an unresolved ref does — a repo must
never look fully indexed when a pass was skipped for size.

---

## Phases (one commit each)

### Phase N.0 — Large-repo perf corpus + resource gate `pending`

**Problem.** No number exists for polyflow on a big repo; a 10× indexing
slowdown would ship unnoticed.

**Deliverable.**

1. **One large corpus entry** in `eval/corpus/` — selection criteria
   pinned (record the choice + rationale in the manifest, the E.2
   convention): ≥10,000 source files after excludes, ≥2 supported
   languages, active OSS. Candidates to evaluate at implementation time:
   `gitlab-org/gitlab` (Ruby+JS), `grafana/grafana` (Go+TS),
   `discourse/discourse` (Ruby+JS). Pinned SHA; ≥10 hand-verified recall
   cases (a big repo is also a *recall* corpus — deep-directory and
   generated-code-adjacent targets specifically).
2. **Perf measurement harness:** `polyflow eval --perf` (extends
   `internal/eval/runner.go`) records per repo:
   `{full_index_seconds, peak_rss_mb, incremental_touch_seconds,
   node_count, edge_count, file_count}`. Peak RSS via
   `runtime.MemStats.Sys` sampled by a ticker during the run (in-process
   ceiling — pinned as the metric; OS RSS differs, the metric name says
   what it is). Incremental = touch one leaf `.go` file, re-run
   `polyflow index`, measure wall time.
3. **Budget gate:** `eval/perf-baseline.json`, committed from the first
   real run. Gate conditions (extend `internal/eval/gate.go` — do NOT
   write a parallel gate, the S.4 rule): fail when
   `full_index_seconds > 1.25 × baseline`, `peak_rss_mb > 1.25 ×
   baseline`, or `incremental_touch_seconds > 2 × baseline` (incremental
   is noisier — wider band, pinned). Improvements ratchet the baseline
   down in the same PR (never up without a recorded justification in the
   phase note). Rule 4 applies in full: a perf repo present in the
   baseline but absent from the run fails `missing_repo`
   (`SkippedCorpus.LocalOnly` exemption honored); with `--gate` the gate
   alone decides the exit code; before landing, simulate CI (cold cache,
   committed baseline) and record the result.
4. **SSA degrade-don't-die:** the Go semantic pass gets a per-service
   wall-clock budget (workspace key `analysis.ssa_timeout`, default
   `120s`): on expiry, the service falls back to tree-sitter-only
   accuracy using the **existing** `SemanticResult.Warning` field
   (mechanism already exists — this phase wires a timeout to it), and
   the warning is persisted + surfaced in `status`/doctor
   (degradation-is-labeled rule). Timeout `0` disables. Same for
   embeddings when Tier S lands (`--no-embed` semantics already label —
   assert, don't rebuild).

**Tests.** Gate-condition unit tests (all three budgets, ratchet-down,
missing_repo, exclusion precedence — mirror the E.3 test list); RSS
sampler sanity; SSA-timeout test (tiny budget on a fixture → Warning set,
nodes still present from tree-sitter, nothing dropped silently);
two-run determinism of the perf JSON's non-timing fields.

**Acceptance.** `eval/perf-baseline.json` committed from a real run on
the chosen large repo; CI runs the perf gate (may be a scheduled/manual
job if runtime is prohibitive — decided and recorded at implementation,
never silently skipped: a skip prints the explicit warning, the E.2
offline convention).

### Phase N.1 — Generated-code policy `pending`

**Problem.** Generated files (protobuf, openapi-gen, GraphQL codegen) are
real code — their symbols are called by hand-written code, so *excluding
them silently breaks recall* (the same class as the test-code incident,
rule 8). But they distort ranking (thousands of near-identical labels)
and pad blast radii with files nobody edits by hand.

**Deliverable.** Index them, label them, deprioritize them — never drop
them (pinned policy):

1. **Detection** — pinned, data-driven (a table in
   `internal/indexer/generated.go`, additive like the asset allowlist):
   - Content markers, checked in the first 5 lines:
     Go `^// Code generated .* DO NOT EDIT\.$`, the cross-language
     `@generated` marker, `# Generated by`, `/* eslint-disable */`-plus-
     codegen-banner combinations listed explicitly.
   - Path/glob markers: `*.pb.go`, `*_pb2.py`, `*.pb.ts`,
     `*.generated.{go,ts,cs}`, `**/__generated__/**`, `zz_generated*.go`,
     `*.g.cs`.
2. **Labeling:** matching files' `NodeTypeFile` node and every node in
   them get `Meta["generated"]="true"` (stamped in the matcher's node
   finalization — one place, all parsers inherit). `SchemaVersion` bump
   (stored meta semantics addition).
3. **Consumption (each a one-line rule, test-pinned):**
   - `SearchNodes`/hybrid search: generated hits rank **below**
     non-generated at equal score (tie-break only — never filtered;
     rule 9's exact-label-first still wins overall: an exact match that
     is generated still beats a prefix match that isn't).
   - `impact`/`context` output: per-node `"generated": true` field +
     the file-group rollup (`Summarize`) annotates generated groups;
     budget trimming prefers dropping generated *detail* first (the
     rollup line survives — blind spots never cut, but boilerplate
     detail goes first).
   - `RelatedFiles`: generated files rank after non-generated at equal
     (refs, hops) — tie-break only, the A.3 shape.
   - Doctor: per-service generated-file count row.
4. **Eval interaction:** corpus cases may set `allow_generated: false`
   to assert a target's blast radius reaches the *hand-written* callers
   through generated intermediaries (a proto message's real impact is
   the services using the stubs — one such case ships with N.0's large
   repo).

**Worked example.** `api.pb.go` (marker match) defines `GetGameRequest`;
`server.go` uses it. `impact --target GetGameRequest` returns both files;
`api.pb.go`'s nodes carry `generated: true`; search for `GetGame` ranks
the handler in `server.go` above the 14 generated stub symbols at equal
bm25 tier.

**Tests.** Detection table tests (every marker, first-5-lines boundary,
a `DO NOT EDIT` string on line 40 → NOT generated); stamp inheritance
across parsers (Go + TS fixture); each consumption rule (search
tie-break incl. the exact-label-still-wins guard; budget
generated-detail-first; related-files tie-break); determinism.

**Acceptance.** On the N.0 large repo: generated count reported by
doctor; the proto-chain eval case passes; search-ranking spot-check
recorded in the phase note.

### Phase N.2 — Monorepo service auto-discovery `pending`

**Problem.** `polyflow init` requires hand-writing `workspace.yaml`; on a
30-package monorepo that is the adoption cliff, and wrong service
boundaries silently corrupt same-service/cross-service semantics.

**Deliverable.** A detection ladder in `polyflow init` (each detector
emits candidate services; results are **merged, deduped by directory,
and printed for confirmation** — init writes the file, the user edits;
auto-discovery never runs implicitly on `index`):

1. `go.work` → one service per `use` directive module.
2. `pnpm-workspace.yaml` `packages:` globs / root `package.json`
   `workspaces` → one service per matched package dir (packages with no
   `src`/no source files after excludes are dropped with a printed
   note — pure-config packages are not services).
3. `nx.json`/`project.json` files, `turbo.json` → project roots as
   services (Nx `project.json` locations are authoritative; turbo
   defers to package.json workspaces — detector order pinned:
   go.work, nx, pnpm/yarn workspaces, turbo, single-service fallback;
   first detector that yields ≥1 service wins per language ecosystem,
   and Go + JS detectors compose in one workspace).
4. Compose files (plan 4 K.1's scan, reused when present): compose
   service `build.context` dirs corroborate/name services (a detector
   row, not a new mechanism).
5. **Bazel is descoped with a written claim**: BUILD-file evaluation is
   its own product; a repo with `WORKSPACE`/`MODULE.bazel` gets a
   printed warning naming the descope + a pointer to manual
   `workspace.yaml`, and a `bazel_workspace_detected` note in doctor —
   considered, visible, not guessed.
6. Every generated service entry carries provenance
   (`# discovered: go.work`) as a YAML comment; `DefaultExcludes()`
   applies per the post-E.2 rules (fixture/data/build dirs only — test
   code stays in, rule 8).

**Tests.** One fixture tree per detector (incl. a go.work + pnpm
polyglot combining both); empty-package drop; detector-order test
(nx beats turbo when both exist); bazel warning fixture; golden
`workspace.yaml` outputs (determinism: globs expanded in sorted order,
rule 2).

**Acceptance.** `polyflow init` on the N.0 large repo (or a pinned
monorepo fixture if that repo is single-module) produces a
`workspace.yaml` needing zero manual edits to index; recorded in the
phase note with the diff against a hand-written one.

### Phase N.3 — The coverage contract (tiering doc + doctor repo report) `pending`

*Runs after everything else in plans 1–6 that will ship has shipped —
it documents reality, so it goes last.*

**Problem.** The goal docs claim "any repo"; the honest deliverable is a
matrix. Undocumented, every gap reads as a bug and every strength is
invisible; documented, an agent/user knows *exactly* what the graph can
promise on their repo before trusting it.

**Deliverable.**

1. **`docs/coverage-contract.md`** — the pinned matrix, two axes:
   - **Rows — repo composition:** each supported language/framework
     tier (full semantic: Go; pattern+linker: JS/TS/Ruby/Python/…;
     template layers; deployment layer; per plan-2/3/4/5 shipped
     phases) and, explicitly, **unsupported** (→ B.0 ledger only).
   - **Columns — evidence available:** static-only / +declared contracts
     (F.1, IaC Q.2) / +runtime capture (R.*). Each cell states the
     guarantee in trust-contract vocabulary: what is `verified`, what is
     `candidate`, what is surfaced-only. One paragraph per cell, no
     hedging adjectives — the F-plan's "what 100% rigorously means"
     style.
   - The goal sections of `docs/agent-context-plan.md` and the
     quickstart (P.2) link to it; "any repo" phrasing in both is
     replaced by a link to the matrix (one-line edits, listed
     explicitly in the phase commit).
2. **`polyflow doctor` repo-tier report** — computes where *this* repo
   sits: per service, the detected languages × (parser present? semantic
   pass? walker? — from the existing registries), unparsed-file counts
   (B.0), evidence sources active (contract globs matched, sessions
   present, IaC found), eval-corpus membership. Output ends with the
   one-line tier verdict per service, e.g.
   `api: static+runtime, full semantic (Go) — verified-capable` /
   `legacy-web: static only, 214 unparsed .php files — candidate-only,
   coverage gaps listed above`. Rows sorted (rule 2); the verdict
   strings are a pinned enum, not free text (they get golden-tested and
   agents may branch on them).
3. **MCP surface:** a `coverage` tool (or a `coverage` field on the
   existing status surface — decide by MCP SDK ergonomics at
   implementation, record the choice) returning the same JSON, so an
   agent can calibrate trust *before* its first impact query. Tool
   description: *"call this once per repo; verified-capable services'
   edges can be trusted at their stated verification_state;
   static-only services' candidate edges each cost one verification."*

**Tests.** Doctor report golden on a fixture workspace hitting three
different tiers; verdict-enum stability test; MCP round-trip;
link-check that the goal docs actually reference the contract file
(a grep test — cheap, keeps the docs honest).

**Acceptance.** Doctor on this repo, chessleap, and one static-only
corpus repo produces three visibly different, accurate tier reports
(pasted into the phase note); `docs/coverage-contract.md` committed with
every cell filled for what actually shipped — including the rows that
say "not supported."

---

## Key files

- **New:** `eval/perf-baseline.json`, `internal/indexer/generated.go`,
  `internal/workspace/discover.go` (N.2 detectors),
  `docs/coverage-contract.md`.
- **Modify:** `internal/eval/{runner.go,gate.go}` (N.0),
  `internal/parser/go_semantic.go` (SSA budget),
  `internal/patterns/matcher.go` (generated stamp),
  `internal/graph/store.go` + search paths (N.1 tie-breaks),
  `internal/impact/summary.go` + `internal/budget/` (generated-first
  trimming), `cmd/polyflow` init/doctor/status, `internal/mcpserver/`
  (N.3), `docs/agent-context-plan.md` goal section (N.3 link edit).

## Risks / honest notes

- **Perf work is discovered, not planned:** N.0 deliberately ships the
  *gate* and the degrade valve, not speculative optimizations — the
  first real large-repo run decides what (if anything) needs a
  follow-up optimization phase, and that phase gets written then, with
  the measured profile in hand.
- **Generated-code detection is heuristic**; the table will grow. The
  policy (index + label) makes a detection miss cost ranking noise, not
  recall — the failure mode is chosen deliberately.
- **Auto-discovery writes config, never truth:** service boundaries are
  a human decision the detectors *propose*; init's confirm-and-edit flow
  is the guard against silently-wrong same-service semantics.

## Sequencing

```
N.0 ─> N.1 ─> N.2 ─> N.3   (N.3 strictly last across ALL plans 1–6)
```
