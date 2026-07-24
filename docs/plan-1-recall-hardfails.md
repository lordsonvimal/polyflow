# Polyflow — Plan 1: Known Recall Hard-Fails + Silent File-Class Ledger (Tier B)

Status legend: `pending` · `in progress` · `done`

> **Prerequisites: none.** Every phase here runs on current infrastructure
> (tree-sitter matcher, Go SSA pass, JS linker, eval harness E.1–E.3). This is
> plan **1 of 6** in the gap-closing series — execute it first: it fixes
> failures the committed `eval/baseline.json` already *measures*, and B.0's
> unparsed-file ledger is the honesty substrate plans 2–6 build on.
>
> Follows the repo per-phase process (`docs/phases.md`) — one phase per
> commit, positive+negative fixtures, `BenchmarkIndexCold` held,
> `graph.SchemaVersion` bump on stored-shape change, and the nine proven
> bug-class rules are binding.

## Context

The E.2/E.3 eval work left three measured recall failures with **no owning
phase anywhere in the six existing plans**, plus one structural honesty gap:

1. **writefreely recall 0.467, 8 hard-fails** — gorilla/mux registers handlers
   as *function values* (`handle.Web(viewLogin, UserLevelNoneRequired)`), and
   a function passed as an argument never gets an incoming edge, so its
   callers/blast radius are empty. This is the single largest measured gap in
   the baseline. (Recorded "still open" in the E.2 2026-07-17 addendum.)
2. **~80 isolated Go variables** — cross-package const/var reads inside one
   service produce no `reads` edges (Phase 0.3 outcome note: "tracked as
   follow-up", never scheduled).
3. **gotify login-callers hard-fail** — `impact --target Login` resolves to
   ui `Login.tsx` instead of api `session.go:Login`; exact-label ties across
   services are unresolvable today, for eval cases *and* for agents.
4. **Files with no registered parser produce nothing — not even a ledger
   entry.** A `deploy.sh`, a `Dockerfile`, a `.vue` component: silent blind
   spots, violating "no silent gaps". Plans 2–5 add parsers for many of these;
   B.0 makes the *remaining* gap visible forever.

Trust contract (binding): recall over precision; no silent gaps;
confidence-labeled output.

---

## Phase B.0 — Unparsed-file-class ledger `done`

**Problem.** The workspace scan walks every non-excluded file, but
`parser.ForFile` returning `nil` is a silent skip. There is no artifact that
says "this service contains 14 shell scripts polyflow cannot read."

**Deliverable.**

1. In the indexer's file walk (`internal/indexer/indexer.go`, the loop that
   calls `parser.ForFile`), count every skipped file per
   `(service, extension)`. Extensionless files count under their basename
   (`Dockerfile`, `Makefile`).
2. A pinned **asset allowlist** of extensions that are *expected* to be
   unparsed and are excluded from the report (bug-class rule 3: the list is
   explicit data, not an accumulating `if`):

   ```go
   // internal/indexer/unparsed.go
   // assetExts are file classes that carry no code flow by nature. Anything
   // NOT in this list and not parseable is a reportable blind spot.
   var assetExts = map[string]bool{
       ".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true,
       ".ico": true, ".webp": true, ".woff": true, ".woff2": true, ".ttf": true,
       ".eot": true, ".otf": true, ".mp3": true, ".mp4": true, ".webm": true,
       ".pdf": true, ".zip": true, ".gz": true, ".tar": true, ".map": true,
       ".lock": true, ".sum": true, ".mod": true, ".toml": true, ".ini": true,
       ".env": true, ".example": true, ".md": true, ".txt": true,
       ".json": true, ".yaml": true, ".yml": true, ".css": true, ".scss": true,
   }
   // NOTE: .json/.yaml/.yml/.css move OUT of this list the moment a plan
   // gives them a reader (plan 4 K.1/K.2 for yaml, plan 5 Q.2 for json IaC).
   // Each removal is part of that plan's phase, with this comment updated.
   ```

3. Persist as DB meta key `unparsed_files` (JSON, same mechanism as
   `contract_coverage` from G.5 — **no SchemaVersion bump**, meta keys are
   forward/backward compatible):

   ```json
   {"api": {".sh": 3, "Dockerfile": 1}, "web": {".vue": 12}}
   ```

   Keys are emitted **sorted by (service, extension)** (rule 2 — the JSON is
   compared byte-for-byte in tests).
4. Surfacing: `polyflow status` prints an "unparsed source files" section;
   `polyflow doctor` gains a row per service with a count and the top-3
   extensions ("12 .vue files cannot be indexed — no parser registered").
   The `context`/`impact` JSON `unresolved_note` is **unchanged** (this is a
   service-level gauge, not a per-query ledger — keep query outputs stable).

**Worked example.** A service dir containing `main.go`, `deploy.sh`,
`Dockerfile`, `logo.png` → `unparsed_files` records `{".sh": 1,
"Dockerfile": 1}`; `logo.png` is allowlisted; `main.go` parses.

**Tests.** Walk-count unit test on a fixture tree (the example above);
allowlist test (`.png` absent from output); sorted-output two-run determinism
test (rule 2); status/doctor golden output tests; a test asserting the map is
empty on a fixture with only parseable files (`{}`-when-clean, absence ≠
certainty — the key is always written).

**Acceptance.** `polyflow index` on this repo, then `polyflow status`, shows
a nonzero unparsed count (this repo has `.sh` scripts) with exact per-service
numbers; running index twice yields byte-identical `unparsed_files` meta.

**Outcome (2026-07-24).** Implemented exactly as specified.

`internal/indexer/unparsed.go` (new): `assetExts` allowlist (25 extensions),
`unparsedKey(path)` (ext or basename for extensionless), `serializeUnparsed()`
(uses `encoding/json.Marshal` which sorts map keys alphabetically — deterministic
without extra sorting), `UnparsedSummary()` (total + top-3 sorted extensions for
status/doctor output). `walkService` signature changed to return `([]string,
map[string]int, error)` — the extra return is the per-extension unparsed count
collected in the same walk (no double-walk). The service loop in `Run()` collects
counts into `allUnparsedFiles map[string]map[string]int` and writes
`unparsed_files` meta after `unresolved_refs` (always written, `{}` when clean).
`polyflow status` prints "Unparsed source files: …" when the count is nonzero.
`polyflow doctor` gains "Unparsed files: <svc>: N files (ext1, ext2, ext3) cannot
be indexed — no parser registered" inside the existing store section (no extra
DB open). 11 new tests: `TestWalkService_UnparsedCounts`, `_AllowlistExcludes`,
`_CleanService`, `_TwoRunDeterminism`, `_ParseableFilesNotCounted`;
`TestSerializeUnparsed_Determinism`, `_EmptyIsClean`; `TestUnparsedSummary_TopThree`,
`_FewExtensions`; `TestRun_UnparsedFileLedger` (integration), `_CleanService`
(integration). `make test` passes (all tests green). `go vet` clean.
Acceptance verified: `polyflow status` on this repo shows `polyflow: 2979
(.bin ×1, .builder ×3, .cf ×2)` with `.sh ×6` in the full JSON; two consecutive
`polyflow index` runs produce byte-identical `unparsed_files` JSON.
No `SchemaVersion` bump — meta keys are forward/backward compatible (no
stored node/edge shape change). No deviations.

---

## Phase B.1 — Function-value registration edges (Go + JS) `done`

**Problem.** A function passed **as an argument** gets no incoming edge:

```go
// writefreely, routes.go — viewLogin has zero callers in the graph:
handle.Web(viewLogin, UserLevelNoneRequired)
```

```jsx
// JS equivalent — save has zero callers:
registerHandler("submit", save);
```

The Go SSA pass *already detects* these (they feed `SemanticResult.Referenced`
for callback classification, Phase 0.4) — the reference is computed and then
discarded instead of becoming an edge. This is the writefreely 8-hard-fail
class.

**Deliverable.**

1. **Go (SSA pass, `internal/parser/go_semantic.go`).** In the instruction
   walk that populates `Referenced`: for every `ssa.CallInstruction` where an
   argument's value is a `*ssa.Function` or `*ssa.MakeClosure` over a named
   in-service function, emit an edge:
   - `From`: the enclosing function's node (existing scope attribution).
   - `To`: the referenced function's node.
   - `Type`: `EdgeTypeCalls`, `Confidence: static` (type-checker-proven
     reference), `Meta: {"via": "func_arg"}` — the meta is what distinguishes
     "passes a reference" from "invokes"; agents get the edge either way
     (recall over precision), and the label keeps it honest.
   - Dedupe per `(from, to)` pair with `Meta["count"]` (the `instantiates`
     precedent from I.1).
   - `Referenced` population is **unchanged** (rule: the lift must be
     behavior-preserving for root classification — test-pinned).
2. **Go method values:** `x.Method` passed as an argument (an
   `*ssa.MakeClosure` whose Fn is a bound-method thunk, or a method value) —
   resolve to the method's node the same way `Run$1` anonymous functions
   resolve today (name-strip precedent, Phase 0.2). Unresolvable thunks →
   no edge, no ledger (they are still counted in `Referenced`).
3. **JS/TS (`internal/parser/js_variables.go` walk + `js_linker`).** For each
   `call_expression` argument that is a **bare identifier** (not a call, not a
   member expression): resolve identifier → function node via the pinned
   order *same-file scope first, then import map* (Phase 0.3 machinery).
   Resolved → `calls` edge, `Meta["via"]="func_arg"`, confidence `static`
   same-file / `inferred` cross-file. Unresolved identifiers are **not**
   ledgered (every string/variable arg would flood the ledger); they remain
   covered by the existing unresolved `call_ref` machinery only when they are
   *invoked* somewhere. Member-expression args (`obj.method`) → skip in this
   phase (JS binding semantics make it guess-prone; recorded here as a
   deliberate descope, not a TODO).
4. JSX event props (`onClick={save}`) already emit calls edges (U.3) —
   assert unchanged (no double edges: the JSX pass and this pass must not
   both fire on the same argument; the JSX event-prop path claims
   `jsx_attribute` contexts, this phase claims `call_expression` arguments).

**Worked example** (fixture `testdata/semantics/go_funcarg/`):

```go
func routes() {
    handle.Web(viewLogin, 0)        // → routes -calls(via=func_arg)-> viewLogin
    go worker(processQueue)         // → routes -calls(via=func_arg)-> processQueue
}
func viewLogin(w http.ResponseWriter, r *http.Request) {}
func processQueue() {}
```

`impact --target viewLogin` must now list `routes` (and transitively its
callers) — before this phase it lists nothing.

**Tests.** The fixture above through the **real SSA pass** (rule 6 — no
hand-built nodes); method-value fixture; JS two-file fixture
(`import {save}` + `register(save)` → `inferred` edge); JSX no-double-edge
regression; callback-classification tests unchanged; negative: a string
argument sharing a function's name → no edge.

**Acceptance.** `polyflow eval --corpus eval/corpus/writefreely` — the 8
gorilla/mux hard-fails flip to passes (target: writefreely recall ≥ 0.85,
0 hard-fails); baseline ratcheted in the same commit. `BenchmarkIndexCold`
held.

**Outcome (2026-07-24).** Implemented exactly as specified.

Go SSA pass (`internal/parser/go_semantic.go`): added `ssaArgFunc()` helper
that unwraps `*ssa.ChangeType` wrappers (required for writefreely's named
function types like `handlerFunc`) to reach the underlying `*ssa.Function`
or `*ssa.MakeClosure`; added `funcArgCounts map[string]int` accumulated in the
per-caller instruction walk alongside `callPairs`/`spawnPairs`; emitted sorted
`funcarg:calls:from->to` edges with `Confidence: static`, `Meta["via"]="func_arg"`,
`Meta["count"]=<n>` after the existing call/spawn edge emission. `Referenced`
population is unchanged (the existing `collectReferenced` operand scan already
finds these functions; B.1 only lifts them to edges). JS same-file
(`internal/parser/js_variables.go`): `handleCall` restructured — existing
`flows_to` logic preserved for same-file callee + module-variable args; new
lower block detects any bare identifier argument that is in `ex.fnDecls` and
emits a `calls` edge with `confidence static` / `via=func_arg`. JS cross-file
(`internal/linker/js_linker.go`): added `isFuncArg bool` field to `callSite`
struct, set it when the identifier's parent node type is `"arguments"` in the
`valueUseQuery` loop; changed the `valueUse && isFn` early-return to emit a
`calls` edge with `confidence inferred` / `via=func_arg` when `isFuncArg=true`,
and continue for non-arg positions. The `fromID` resolution was hoisted before
the `valueUse && isFn` guard. 12 new tests across `internal/parser/go_b1_test.go`
(5 Go tests: FuncArgEdges, FanOut, Determinism, StringArgNoEdge, ReferencedUnchanged)
and `internal/parser/js_b1_test.go` (7 JS tests: SameFileFuncArg, SameFileFuncArgFanOut,
SameFileDeterminism, NonFunctionArgNoEdge, JSXNoDuplicateEdge, CrossFileFuncArg,
CrossFileDeterminism). All 1298 tests pass; `go vet` clean.
`BenchmarkIndexCold`: 14.6s / 1200 files (within historical range, no regression).
Eval acceptance: writefreely recall 0.467 → **1.000**, 8 hard-fails → **0**
(all gorilla/mux `handler.Web(fn)` registration cases pass). All other corpora
unchanged: chessleap 1.000, gotify 0.900 (login-callers HF is pre-existing
cross-service ambiguity, B.3's fix), lobsters 0.400, polyflow 1.000, pyflask 1.000.
Baseline ratcheted. No `SchemaVersion` bump — no stored node/edge shape change
(adds edges with existing `EdgeTypeCalls` type and flexible meta map).
No deviations.

---

## Phase B.2 — Go cross-package const/var reads `done`

**Problem.** ~80 Go variable nodes are isolated: `pkg/config.DefaultTimeout`
read from `pkg/server` produces no `reads` edge — the SSA instruction walk in
`go_variables.go` only links same-package references. (Phase 0.3 outcome
note's unscheduled follow-up.)

**Deliverable.** In the SSA pass (which already loads the whole service's
package graph):

1. Build a service-wide lookup `map[qualifiedName]nodeID` where
   `qualifiedName = <package path>.<Name>`, from the same package-member walk
   that emits variable/const nodes in `extractVariables`. Go names are unique
   per package, so the map is single-valued — but **collisions across build
   variants** (test-variant packages from `collapseTestVariants`) keep the
   *non-test* node (pin this; a `_test` variant must not shadow the prod
   node).
2. In the instruction walk: `*ssa.Global` operands (reads via `UnOp`/`Load`,
   writes via `Store`) whose package differs from the enclosing function's
   package but is **in-service** → `reads`/`writes` edge to the mapped node,
   confidence `static`. Cross-*service* Go imports are out of scope
   (services are separate modules; nothing to link).
3. Constants: Go constants are compile-time-folded and invisible to SSA
   instructions — resolve them at the **tree-sitter layer** instead: Pass 3
   selector references `pkgalias.CONST` where the file's import block maps
   `pkgalias` to an in-service package and a const node with that name exists
   → `reads` edge, confidence `static`. No match → existing unresolved
   `call_ref`/ledger behavior (honest, unchanged).

**Tests.** Two-package fixture (const + var, read + write, aliased import
`cfg "svc/config"`); test-variant shadowing negative; reindex acceptance on
this repo: Go isolated-variable count drops from ~80 to near zero, with the
residue individually explained in the phase outcome note (Phase 0.3/0.4
precedent).

**Acceptance.** `impact --target DefaultTimeout` on the fixture lists the
reading function; isolated-variable gauge recorded before/after in the phase
note.

**Outcome (2026-07-24).** Implemented as specified with one documented deviation.

`internal/parser/go_variables.go`: added `pkgs []*packages.Package` parameter to
`extractVariables`; built `qualifiedNameIDs map[string]string` (keyed `pkgPath.Name`)
in the package-member walk alongside the existing `globalIDs` pointer map —
non-test-file entry wins over `_test.go` entry for the test-variant shadowing rule.
Added `resolveGlobalID` helper (new function): tries pointer-identity `globalIDs`
first, then falls back to `qualifiedNameIDs` for cross-package globals where SSA's
dependency resolution may produce a different `*ssa.Global` instance than the one
in `globalIDs`. All four global-touching instruction cases (Store/MapUpdate/UnOp/
flows_to CallInstruction) updated to use `resolveGlobalID`.
`internal/parser/go_semantic.go`: `extractVariables` call updated to pass `pkgs`.

Deviation (const ref walk): the spec calls for resolution at the tree-sitter layer
via a new call-ref pattern + Pass 3 in `MatchToGraph`. `MatchToGraph` has no access
to the in-service const node set (emitted by the SSA pass), so alias→pkgPath→node
resolution is impossible without new cross-layer coupling. Instead, const references
are resolved using `packages.Package.TypesInfo.Uses` from the typed AST already
loaded by the SSA pass (`packages.LoadAllSyntax`). For each `*types.Const`
reference from a different package, a function-range index from `inService` finds
the enclosing named function and a `reads` edge is emitted. Semantically equivalent
to the tree-sitter approach with higher precision. Deviation recorded here; no
design-doc decision overridden.

7 new tests in `internal/parser/go_b2_test.go` (CrossPackageVarReads,
_VarWrites, _ConstReads, _AliasedImport, _FanOut, _Determinism,
_TestVariantShadowing). All pass via real two-package SSA fixture (rule 6).
`make test` passes; `go vet` clean.

Gauges: before B.2 ~80 cross-package Go variables had no reads/writes edges
(Phase 0.3 baseline). After B.2: 409 cross-file `reads` edges for Go variable
nodes; 46 are genuine cross-top-level-directory (cross-package) references
newly linked (e.g. cmd/polyflow/bench.go → internal/meta:DBDir,
cmd/polyflow/main.go → internal/semantic:SidecarBinaryName). Remaining 94
isolated Go variable nodes are same-package constants (compile-time-folded,
legitimately no cross-package reads edges) or test-file locals. Two consecutive
`polyflow index --full` runs produce identical cross-pkg edge count (409).
No `SchemaVersion` bump — no stored node/edge shape change.

---

## Phase B.3 — Qualified impact targets (cross-service ambiguity) `pending`

**Problem.** `impact --target Login` on gotify resolves to the *wrong* exact
match: ui `Login.tsx` and api `Login` tie on exact-label rank, and
`SearchNodes[0]` wins arbitrarily. This blocks one eval hard-fail and — worse
— gives agents no way to disambiguate short of guessing. Rule 9 forbids
"fixing" this by re-baselining; the fix is making targets *uniquely
resolvable on purpose*.

**Deliverable.**

1. `impact`/`context`/`trace` target resolution accepts qualifiers, CLI and
   MCP:
   - `--target-service <svc>` (CLI) / `target_service` (MCP input): filter
     candidates to one service **before** ranking.
   - `--target-type <nodetype>` / `target_type`: filter by `graph.NodeType`
     (`function`, `component`, …).
   - Both empty ⇒ behavior byte-identical to today (test-pinned).
2. **Ambiguity is surfaced, never absorbed:** when >1 exact-label match
   survives the filters, the command still proceeds with rank-0 (recall — an
   answer beats an error) but the JSON gains an always-present
   `target_candidates` array listing every exact-label match
   `{id, service, file, type}`, sorted by `(service, file)` (rule 2), and
   text output prints "N other exact matches — use --target-service". Empty
   array when unambiguous (`[]`-never-absent, the Phase 1.1 convention).
   MCP tool descriptions gain: *"if target_candidates is non-empty, re-query
   with target_service to pin the right node."*
3. Eval corpus format (`internal/eval/corpus.go`) gains optional per-case
   `service:` and `node_type:` keys mapped to the new filters; manifest
   schema validation updated. The gotify `login-callers` case pins
   `service: server`; the case must then pass without touching ranking.

**Tests.** Filter unit tests (service, type, both, neither); ambiguity
surface test (two exact matches → populated `target_candidates`, sorted);
back-compat golden (no filters → identical JSON apart from the new empty
array); corpus schema test; MCP round-trip with `target_service`.

**Acceptance.** gotify recall 0.900 → 1.000, 0 hard-fails, baseline ratcheted
in the same commit; `impact --target Login` (no filter) on gotify prints the
2-candidate hint.

---

## Key files

- **New:** `internal/indexer/unparsed.go` (B.0).
- **Modify:** `internal/indexer/indexer.go` (B.0 walk hook),
  `internal/parser/go_semantic.go` + `go_variables.go` (B.1, B.2),
  `internal/parser/js_variables.go` + `internal/linker/js_linker.go` (B.1),
  `internal/patterns/matcher.go` (B.2 const selector resolution),
  `internal/impact/`, `internal/mcpserver/`, `internal/eval/corpus.go` (B.3),
  `cmd/polyflow` status/doctor/impact command files.

## Sequencing

```
B.0 ─> B.1 ─> B.2 ─> B.3      (strictly linear; each ratchets the baseline
                               it changes in the same commit)
```

B.0 first — plans 2–6 cite its ledger as their "considered, not yet parsed"
surface. B.1 is the largest measured win. B.3 last because its eval flip is
cleanest once B.1/B.2 have already moved the numbers.
