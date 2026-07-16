# Polyflow — Versioned Toolchain Matrix Plan

Status legend: `pending` · `in progress` · `done`

> **Reconciled with `docs/contract-matching-plan.md`.** These are two axes of the
> same goal: contract-matching gives **breadth** (link *any* cross-service
> convention), versioning gives **fidelity** (behave correctly per tool *version*).
> They share **one** gating mechanism — the existing `package:` / `version_range:`
> semver gate (`internal/patterns/version.go`). This plan therefore does **not**
> introduce a separate `internal/profiles/` package; version-specific *interpretation
> and linking* is expressed as **version-gated contract rules + tree-sitter pattern
> files**. Only *parser-engine* versions (the `a-h/templ` lib, tree-sitter grammars)
> — which hit Go's hard single-import constraint — need the heavy **sidecar**
> isolation.

## Context

**Why now.** A version audit of the T.1–T.6 templ-layer work showed polyflow's
version-robustness is only *partial*:

- **Already versioned:** library/framework semantics (gin, gorm, aws-sdk v1/v2,
  pusher…) are gated in YAML via `package:` + `version_range:` (semver), filtered
  per-service by resolved deps (`internal/patterns/version.go`
  `Registry.ForService`, `deps.Resolve`). Adding a lib version there is already
  additive.
- **Not versioned (the gap):** the hand-written parsers — templ AST + datastar
  vocabulary + HTML attrs in `internal/parser/templ.go`, the SSA pass in
  `go_semantic.go`, and the five tree-sitter grammars (go/js/ts/html/ruby) — are
  single-version and version-blind. datastar colon-vs-hyphen is an `if` branch,
  not a version selection. polyflow bundles exactly **one** `a-h/templ` lib version
  (v0.3.1020 in `go.mod` today).
- **No proof of coverage:** tested against exactly one real stack (templ
  v0.3.960, datastar-go v1.1.0). No matrix asserting per-(tool, version) behaviour.

**Goal.** A maintainable, scalable version matrix across *every* integrated tool
so that supporting version **N** is an additive change (a version-gated rule/pattern
variant, a sidecar build target for parser engines, and a fixture cell), never
surgery on shared code. Behaviour is selected from the target project's **resolved**
tool versions.

**Locked decisions:**
1. **Full multi-version isolation for parser engines** — version-isolated parser
   **sidecars** (one binary per parsing-engine version); a **router** dispatches each
   file to the sidecar matching the resolved version. Applies to `a-h/templ` + the
   tree-sitter grammars (the tools with the Go single-import constraint). *Interpretation/
   linking* is NOT sidecar'd — it is version-gated rules (see the reconciliation note).
2. **Real per-version fixtures + CI gate** — each `(tool, version)` cell has a
   real manifest + expected graph; a matrix runner asserts all cells; CI fails on
   regression or a registered-but-unfixtured version.
3. **Fail-safe fallback** — unknown/out-of-range version → nearest (newest) known
   rule variant / sidecar backend, stamp `resolved_version` + `profile_used` +
   `confidence=inferred`, and record a coverage note (no silent gaps — matches the
   existing trust contract).

Follows the repo's per-phase process (`docs/phases.md`): one phase per commit,
positive+negative fixtures per change, benchmark, doc update, `graph.SchemaVersion`
bump when stored shape changes.

---

## Target architecture

Two kinds of version-sensitivity, handled by two different mechanisms:

**A. Interpretation & linking semantics** (datastar attribute vocabulary / action
verbs / signal syntax, framework conventions, cross-boundary matching) →
**version-gated rules**, reusing the existing `package:` / `version_range:` gate. No
new package: datastar v2 = a version-gated **contract rule** variant
(`docs/contract-matching-plan.md`) + a version-gated **tree-sitter pattern** variant.
The residual hardcoded branches in `internal/parser/templ.go` (`isDataOnKey`,
`isReactiveAttrKey`, verb regex, reactive-attr set) become data selected by the
resolved datastar/templ version via a thin selector, not an `if`.

**B. Parser-engine versions** (`a-h/templ` lib; tree-sitter grammars for
go/js/ts/html/ruby) → **sidecars + router**, because Go can import only one version
of each. Each sidecar is built against ONE engine version, speaks a stable IPC
(length-prefixed JSON returning graph `Node`/`Edge`/`UnresolvedRef`), and the main
process routes files by resolved version with nearest-fallback.

Shared infrastructure across A and B:

1. **Toolchain registry** — declarative single source of truth.
   `tool → [ {versionRange, ruleVariant | sidecarBackend} ]` for
   `go, javascript, typescript, templ, datastar, html, ruby`. Feeds *both* the
   rule/pattern gate (A) and the sidecar router (B). Adding a version = one row.
2. **Version resolver** — extend `internal/deps` to also resolve *runtime/language*
   versions (Go `go 1.xx` directive via `modfile.Go.Version`; TypeScript from the
   `typescript` dep / `tsconfig`; ECMAScript target; templ from `a-h/templ`;
   datastar from `starfederation/datastar-go` or the JS datastar dep; HTML =
   stable). Output: per-service `tool → version`, joined against the registry with
   `patterns.VersionInRange` (reused) + nearest-fallback.
3. **Coverage ledger + fail-safe** — every rule selection / sidecar dispatch stamps
   `profile_used`/`backend_version`; when inferred, appends a coverage note surfaced
   by `polyflow doctor` (shared with the contract-kind coverage from
   contract-matching phase G.5) and in `status`.

### Pinned Go surface (V.0/V.2 implement exactly this)

```go
// internal/toolchain/registry.go (V.0)
type Tool string // "go" | "javascript" | "typescript" | "templ" | "datastar"
                 // | "html" | "ruby"

// Backend is one registry row: a version range mapped to EITHER a rule/pattern
// variant (mechanism A) OR a sidecar build (mechanism B) — never both.
type Backend struct {
    VersionRange   string // semver expr, evaluated via patterns.VersionInRange
    RuleVariant    string // pattern/contract variant id; "" when sidecar'd
    SidecarBackend string // sidecar build id;            "" when rule-gated
}

// Registry: ordered rows per tool; first satisfied range wins. No row
// satisfied → nearest-NEWEST backend + confidence=inferred + coverage note
// (the fail-safe — never an error, never silent).
type Registry map[Tool][]Backend

// Selection outcome, stamped into graph meta (profile_used/backend_version).
type Selection struct {
    Tool     Tool
    Version  string // resolved from the target project
    Backend  Backend
    Inferred bool   // true when nearest-fallback was used
}

// internal/toolchain/resolve.go (V.0) — extends deps.Resolve for
// runtime/language versions (go directive, typescript dep, a-h/templ,
// datastar-go, …). HTML is the constant "living".
func ResolveToolchain(svcDir string, svcDeps []deps.Dependency) map[Tool]string
```

**Sidecar IPC (V.2) — length-prefixed JSON over stdio**, one long-lived pooled
process per backend, requests serialized per process. **Implement the frame
layer payload-generic** (`protocol.go`: uint32 length + opaque JSON in both
directions; pooling and error-fallback live at this layer) with the message
schema defined per sidecar *type* — the parse schema below is one instance;
the semantic-search plan's embedding sidecar (`{"texts"}→{"vectors"}`) is
another and must reuse `protocol.go` unchanged:

```
request frame:   uint32 (little-endian byte length) + JSON:
  {"file": "views/board.templ", "content_b64": "<base64 source>",
   "tool": "templ", "version": "0.3.1020"}

response frame:  uint32 (little-endian byte length) + JSON:
  {"nodes": [graph.Node…], "edges": [graph.Edge…],
   "unresolved": [graph.UnresolvedRef…], "error": ""}
```

A non-empty `error` (or a dead/missing sidecar) triggers the graceful
in-process fallback + a coverage note — a sidecar failure must never abort an
index run or silently drop a file.

---

## Phases (one commit each)

### Phase V.0 — Registry + resolver + coverage scaffolding `done`
New `internal/toolchain/{registry.go,resolve.go}`; extend `internal/deps/deps.go`
for runtime versions; reuse `patterns.VersionInRange`. Registry seeded with today's
single versions (behaviour unchanged). Adds `tool → version` to graph meta + a
coverage ledger. No parser change yet — establishes the seams. *SchemaVersion bump.*

**Outcome (V.0).** Implemented `internal/toolchain/registry.go` (Tool constants,
Backend, Registry, Selection, Registry.Select, DefaultRegistry) and
`internal/toolchain/resolve.go` (ResolveToolchain, CoverageNote, SelectAll,
readRubyVersion). Added `deps.GoDirective` to extract the `go` directive version
from go.mod. DefaultRegistry seeds all 7 tools: HTML/JavaScript use a catch-all
VersionRange ("") so "living" never triggers Inferred; versioned tools (go, typescript,
templ, datastar, ruby) use semver ranges with nearest-newest fallback on miss.
Indexer now calls ResolveToolchain+SelectAll per service and writes
`toolchain_versions` + `toolchain_coverage` into graph meta. SchemaVersion bumped
12→13. 21 new toolchain tests (all green); full suite green; benchmark holds
(no indexing-path hot-path change — toolchain resolution is a cheap file-read
on the scan pass only). No deviations from pinned interface.

### Phase V.1 — Version-gate interpretation via rules (no new package) `done`
Extend the existing `package:` / `version_range:` gate to cover **datastar contract
rules** (contract-matching G.1/G.3) and **templ recognition patterns**. Move the
colon/hyphen + reactive-attr vocabulary out of the `internal/parser/templ.go`
branches into version-gated rule/pattern data, selected by the resolved
datastar/templ version through a thin `internal/toolchain` selector (reusing
`gateSatisfied`). Ship datastar **v0-hyphen** and **v1-colon** as gated variants +
first matrix cells. Pure-Go, no infra; largest maintainability win.
**Depends on the contract engine (contract-matching G.0–G.3).** Fixtures: v0 + v1
datastar fixture projects.

**Outcome (V.1).** Implemented `internal/toolchain/vocab.go` (`DatastarVocab` struct
with `IsDataOnKey`/`IsReactiveAttrKey` methods; three vocabulary constants:
`datastarV0Vocab` hyphen-only, `datastarV1Vocab` colon-only, `datastarCombinedVocab`
backward-compat fallback; `DefaultDatastarVocab(variant)` selector). Added v0 row to
`DefaultRegistry()` for ToolDatastar — v0.x is now a real supported variant, not a
nearest-newest fallback. Added `DatastarVariant string` field to
`patterns.TreeSitterMatcher` (zero import-cycle risk: just a string). Indexer sets
`matcher.DatastarVariant` from the per-service toolchain selection before spawning the
worker pool. `TemplParser.Parse` derives the vocabulary from `matcher.DatastarVariant`
and stores it on `templVisitor`; the hardcoded `isDataOnKey`/`isReactiveAttrKey`
standalone functions are replaced by `templVisitor` methods that delegate to
`DatastarVocab`. Shipped `testdata/datastar_v0.templ` (hyphen fixture) + 7 new
parser tests (positive, wrong-version negatives for both v0 and v1, fallback
coverage) + 11 toolchain vocab tests. All 21 prior toolchain tests green; full suite
green (`go test ./...`). `BenchmarkIndexCold` holds (~9.9 s, 1200 files — vocab
lookup is one map read per service on the scan pass). No SchemaVersion bump — stored
node/edge fields are unchanged. No deviations from pinned interface.

### Phase V.2 — Sidecar protocol + router + templ sidecar (parser-engine fidelity) `pending`
New `internal/sidecar/{protocol.go,router.go,manager.go}` and
`cmd/polyflow-parse-templ/` (built against `a-h/templ`). Router wraps
`parser.ForFile` dispatch in `indexer.go`; nearest-version fallback + labeling.
Migrate the templ parser behind the router (proves isolation on the real-constraint
tool). Scope: **parser-engine version only** — interpretation/linking already
version-gated in V.1. Graph output byte-identical on chessleap (regression guard).

### Phase V.3 — Grammar sidecars (divergence-triggered, not unconditional) `pending`
**Gated on proof, executed after V.4.** Grammars are version-tolerant; standing up
sidecar infra for all five languages with one pinned version each would be pure cost
(per-file IPC on every parse, N build targets, distribution) with zero fidelity gain.
This phase executes **per language, only when a V.4 matrix cell proves a real
behavioural divergence** between two grammar versions of that language. Until then:
one in-process grammar per language, with the matrix documenting the tolerated range.
When triggered: a `cmd/polyflow-parse-grammar/` build target for the diverging
version + a registry row; the router dispatches; zero shared-code edits (mechanism
already proven by the templ sidecar in V.2).

### Phase V.4 — Matrix harness + CI gate + doctor `pending`
`internal/matrix/matrix_test.go` iterates `testdata/matrix/<tool>@<ver>/` (real
manifest + `expected.json`), spins the right rule variant / sidecar backend, asserts
every cell; a coverage test fails when a registered version lacks a fixture.
`polyflow doctor` prints the tool×version coverage table (merged with the per-kind
contract coverage from contract-matching G.5). Wire into CI.

**Value ordering:** V.0–V.1 deliver the declarative registry + version-gated rules
(the maintainability/scalability core, reusing one gate) before any sidecar infra;
V.2 proves the sidecar mechanism on the one tool with a hard constraint (templ);
V.4's matrix then supplies the evidence that decides whether any V.3 grammar sidecar
is ever built.

---

## Key files

- **New:** `internal/toolchain/` (registry, resolve, version selector),
  `internal/sidecar/` (protocol/router/manager), `cmd/polyflow-parse-templ/`,
  `cmd/polyflow-parse-grammar/`, `internal/matrix/matrix_test.go`,
  `testdata/matrix/<tool>@<ver>/`, doctor command file. Version-gated **rule/pattern
  variants** live under `contracts/*.yaml` (contract-matching) and
  `patterns/<lang>/*.yaml` — no `internal/profiles/` package.
- **Modify:** `internal/deps/deps.go` (runtime versions), `internal/parser/templ.go`
  (thin version-selector instead of hardcoded branches), `internal/indexer/indexer.go`
  (router dispatch + coverage stamping), `internal/graph/model.go`
  (`profile_used`/`backend_version` meta; `SchemaVersion` bumps),
  `internal/patterns/version.go` (export/reuse `gateSatisfied`/selector).

## Reuse (don't rebuild)

- `patterns.VersionInRange` / `Registry.ForService` / `gateSatisfied` — the **single**
  semver gate; both the contract/pattern rule selection (A) and the sidecar router (B)
  reuse it. No parallel versioning mechanism.
- `deps.Resolve` / `ResolvedVersions` — already extracts package versions; extend for
  runtime/language versions.
- The **contract engine** (`docs/contract-matching-plan.md`, `internal/contract/`) —
  datastar/cross-boundary linking lives here; V.1 only *gates* those rules by version.
- Structural, version-agnostic detectors already present — `isTemplRenderSig`,
  `isDatastarNewSSE` (`go_semantic.go`) — keep as the version-tolerant baseline.

## Verification

- `go test ./...` green; `internal/matrix` runs every cell + a "no registered
  version without a fixture" guard.
- Build sidecars; index chessleap (templ v0.3.960 / datastar v1.1.0) → graph
  **identical to current** node/edge counts (regression), with
  `profile_used=datastar-v1` / `backend_version` stamped.
- Add a synthetic **datastar v0-hyphen** fixture project + a **second templ minor**
  cell → matrix proves >1 version per tool actually diverges and both pass; the v0/v1
  divergence is driven purely by version-gated rules (no code branch).
- Fail-safe check: a fixture pinning a *future* datastar v2 → nearest (v1) rule
  variant applied, `confidence=inferred`, coverage note present (assert, don't crash).
- Benchmark: router/IPC overhead vs. today's in-process parse (`make bench` +
  `time polyflow index --full` on chessleap); hold `BenchmarkIndexCold`. Long-lived
  pooled sidecars keep startup off the hot path.

## Risks / honest notes

- **Sidecars are only for parser engines.** Interpretation/linking versioning is
  cheap (rule gating); do not sidecar it. This keeps the heavy infra scoped to the
  two tools (`a-h/templ`, grammars) that genuinely need it.
- **Heavy option (parser side).** Sidecars add process management, IPC cost, N build
  targets, and cross-platform distribution concerns. Mitigate: pooled long-lived
  processes; a single multiplexed `polyflow-parse` binary with `--engine/--version`
  rather than dozens of tiny binaries; graceful in-process fallback if a sidecar is
  missing.
- **templ is the only tool with a true hard constraint.** Grammars are
  version-tolerant; V.3 is therefore gated on a matrix cell proving a real
  behavioural divergence — otherwise one in-process backend per language, matrix
  documents the tolerated range.
- **Scale.** Full build is multi-week. Phasing puts the registry + version-gated rules
  (V.0–V.1) first so the "add version N additively" property lands early, independent
  of the sidecar rollout.

## Sequencing / dependencies

```
contract-matching:  G.0 ─> G.1 ─> G.2 ─> G.3 ─> G.4 ─> G.5
                                    │(rules exist to gate)      │(doctor coverage)
versioning:         V.0 ─> V.1 ────┘                           │
                             └─> V.2 ─> V.4 ───────────────────┘
                                          └─(divergence proven)─> V.3 (per language)
```
- **V.1 depends on the contract engine** (G.0–G.3): it version-gates contract/pattern
  rules that must exist first.
- **V.2 (templ sidecar) is independent** of contract-matching — pure parser-engine
  isolation.
- **V.3 is divergence-triggered:** it runs only when a V.4 matrix cell proves a
  grammar-version divergence, per language.
- **V.4 doctor coverage** merges with contract-matching G.5 per-kind coverage.
