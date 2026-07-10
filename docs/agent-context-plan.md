# Polyflow — Agent-Context Accuracy & Retrieval Plan

Status legend: `pending` · `in progress` · `done (commit <sha>)`

## Goal

Polyflow's primary use case is **AI-agent context retrieval**: an agent asks
"what is impacted if I change X" (function, variable, file, or uncommitted
diff) and receives the complete, cross-service blast radius in a few KB of
JSON instead of burning tokens on grep/read exploration.

The trust contract that makes this work:

- **Recall over precision for the agent path.** A false-positive edge costs
  the agent one cheap verification; a silently missing edge ships a bug.
  (Measured failure example: `impact --target hiddenTypes` reported 3 nodes
  while missing the real usage in `web/src/stores/derived.ts:33`.)
- **No silent gaps.** Every reference the indexer could not resolve must be
  surfaced as an explicit unresolved item, never dropped.
- **Confidence-labeled output.** Uncertain edges are included and labeled so
  the agent knows exactly what to verify.

Ground rules (carried from `docs/phases.md`): every pattern change ships a
positive + negative fixture; tests pass before each commit; one phase per
commit; this doc is updated as each phase completes.

---

## Tier 0 — Graph recall (prerequisite for everything else)

### Phase 0.1 — Go top-level call refs `done`

**Bug:** call-ref patterns matched outside any function body in Go files are
silently dropped in `MatchToGraph` Pass 3 (`internal/patterns/matcher.go`):
the synthetic `(module)` fallback only exists for JS. Result: every cobra
subcommand (`RunE: runIndex` at package level) has no incoming edge — all
CLI `run*` functions appear as roots.

**Fix:** for Go files with no enclosing function, resolve the edge source by
fallback: same-file `main` → same-file `init`. Applies to Pass 3 call refs.

**Tests:** matcher unit test with cobra-style source (package-level
`&cobra.Command{RunE: runX}` + `main`); fixture check that
`patterns/go/cobra_test` still passes; full reindex of this repo must show
`runIndex`, `runServe`, etc. with incoming edges from `main`.

### Phase 0.2 — Goroutine worker outflow `done`

**Bug:** `worker` nodes (`go func(){…}`) have spawns in-edges but **zero
outgoing edges**: Pass 2/3 only register `function`/`method` nodes as
enclosing scopes, so all calls inside a goroutine body attribute to the outer
named function.

**Fix:**
1. Register `worker` nodes (with `end_line` spans) in the enclosing-scope
   index; attribute pattern nodes/call refs inside the body to the worker.
   Nodes must skip themselves during enclosure lookup.
2. Go semantic pass: SSA anonymous functions (`Run$1`) currently resolve to
   the parent function by name-stripping; map them to the worker node at the
   same file+line when one exists, so semantic call edges flow out of the
   worker.

**Tests:** unit tests for both attribution paths (tree-sitter + SSA); reindex
must show every worker node with ≥1 outgoing edge in this repo.

### Phase 0.3 — Cross-file JS variable linking + reads/writes retyping `done`

> Outcome: web isolated variables 105 → 3 (the 3 are store objects, whose
> member-level edges are the precise representation); reads 121 → 229,
> writes 19 → 59. `impact --target hiddenTypes` now returns the full
> derived.ts chain. Remaining 80 isolated variables are Go-side
> (cross-package const reads via the SSA pass) — tracked as follow-up in
> Phase 0.5's gauge.

**Bug (measured):** 105 of 394 variable nodes are fully isolated.
1. `js_variables.go` tracks reads/writes same-file only; imported constants
   (`DEFAULT_CONFIDENCE`, `CANVAS_BG`, exported stores) get zero cross-file
   edges.
2. Linker-resolved member calls on store objects (`uiStore.hiddenTypes()`,
   `uiStore.setNotification(...)`) emit generic `calls` edges to variable
   nodes — semantically these are reads (accessor) / writes (setter), so the
   read/write flow the UI is supposed to show almost never materializes.
3. Signal reads inside module-level arrow bodies (e.g. `createMemo` in
   `derived.ts`) are missing entirely.

**Fix:**
1. Extract per-file import maps (imported name → source module) in the JS
   variable extractor; emit unresolved read/write references for imported
   identifiers; resolve them in `js_linker` to variable nodes
   (confidence `inferred`), dropping into the unresolved log (Phase 0.5)
   when no target exists.
2. Attribute variable accesses inside module-level arrow/function expressions
   to the declaring variable's node or the `(module)` node instead of
   skipping.
3. Retype linker-resolved calls to signal variables: accessor → `reads`,
   setter (`setX` from the same `createSignal` destructure) → `writes`
   (meta records the paired signal).

**Tests:** extractor unit tests (imported const read, store member read/write,
memo-body read); linker tests; reindex acceptance: isolated-variable count
drops from 105 to near zero (allowing genuinely unused consts), and
`impact --target hiddenTypes` includes `derived.ts`.

### Phase 0.4 — Root-node classification `done`

> Outcome on this repo: 32 callback (templ Visit*, function values), 13
> entrypoint (main/init/(module)), 8 unreachable — each manually verified
> as genuine dead code (Store.UpsertNode/UpsertEdge/GetEdge/UpsertFileHash,
> server.Start, isUncertain, expandAllBoundaries, MatchToNodes). Known
> limitation: JS has no referenced-scan yet, so JS functions referenced
> only dynamically may over-report as unreachable.

**Problem:** 60+ function/method roots with three very different meanings the
graph doesn't distinguish: entrypoints (`main`, `init`, handlers), framework
callbacks (templ `Visit*` — called only by out-of-service code), and genuine
dead code (`Store.UpsertNode` — zero references in repo).

**Fix:** classify at index time, store `root_kind` in node meta:
- `entrypoint`: `main`/`init`/tests, or node types http_handler, subscriber,
  worker, route.
- `callback`: no in-service caller but the function is *referenced*
  (address taken / stored / satisfies an interface used externally — SSA
  referrer check, tree-sitter func_ref fallback).
- `unreachable`: no incoming edges and no references anywhere.

**Tests:** unit tests per class; reindex: cobra run* are non-roots (0.1),
templ Visit* marked `callback`, `UpsertNode` marked `unreachable`.

### Phase 0.5 — Recall gauge (unresolved references) `done`

> Outcome: unresolved refs flow from the matcher (call_ref) and JS linker
> (import_ref, relative imports only) into an unresolved_refs table, cached
> per file for incremental runs; `polyflow status` reports the count and
> `--unresolved` lists details. On this repo: 149 call_refs, all verified
> honest (local component signals, function-valued params, prop handlers —
> dynamic dispatch). Builtins/prototype methods/re-exports are excluded.
> SchemaVersion bumped to 3 so pre-series incremental caches are discarded.

**Problem:** dropped references are invisible, so graph blind spots are
discovered by being burned.

**Fix:** collect every unresolved reference (Pass 3 call refs with no
same-file target, linker misses, import links with no target node) into an
`unresolved_refs` table (file, line, name, kind). `polyflow status` reports
counts per service/kind; `polyflow status --unresolved` lists them.

**Tests:** unit tests for collection + store; status output test; reindex
sanity: unresolved count is nonzero and explains remaining known gaps.

---

## Tier 1 — Agent interface

### Phase 1.1 — Uncertainty in the output contract `done`

> Outcome: `context`, `impact` (node + `--file` modes), and `trace` JSON all
> carry an `unresolved` array (always present, `[]` when clean so absence is
> never mistaken for certainty) scoped to the traversed files, plus an
> `unresolved_note` "verify these N unresolved references manually…" when
> nonempty. Text formats append the same list; chain format notes it on
> stderr. Scoping helper `graph.UnresolvedInFiles` + `graph.UnresolvedNote`
> shared across all three commands. Verified on this repo:
> `impact --target hiddenTypes` surfaces 80 refs, all in traversed files;
> Go-side traces report 0 with an explicit empty section.

`context`/`impact`/`trace` JSON gains an `unresolved` section (from 0.5)
scoped to the traversed files, plus per-edge confidence already present.
Agent-facing message: "verify these N references manually."

### Phase 1.2 — MCP server `done`

> Outcome: `polyflow mcp` serves `search`, `context`, `impact` (node +
> file modes), and `trace` over stdio via the official MCP Go SDK
> (`modelcontextprotocol/go-sdk`). Tools return the same JSON as the CLI
> commands, including the Phase 1.1 unresolved section; tool descriptions
> tell agents to verify unresolved refs. Impact query logic moved from
> `cmd/polyflow` into `internal/impact` so CLI and MCP share one
> implementation; `internal/mcpserver` holds the handlers behind a `Store`
> interface (tested over in-memory transports). The server watches graph.db
> and hot-reloads the index when `polyflow index` reruns mid-session.
> Depth convention over JSON: omitted/0 → default, -1 → unlimited.
> Register with `claude mcp add polyflow -- polyflow mcp`.

Thin stdio MCP wrapper exposing `search`, `context`, `impact`, `trace`
(same query layer as REST). Enables automatic tool discovery by Claude Code
and other agents.

### Phase 1.3 — Token budgeting `done`

> Outcome: `--max-tokens`/`--summary`/`--snippet-lines` on context/impact
> (CLI flags + MCP inputs: `max_tokens`/`summary`/`snippet_lines`).
> `internal/budget` estimates tokens (~4 JSON bytes/token) and picks the
> shape: per-node detail when it fits, file-grouped rollup (`Summarize`)
> when over, tail files trimmed when even the rollup is over — but the
> unresolved section is always carried whole (trust contract: blind spots
> are never cut to save tokens; a tiny budget therefore has a floor).
> Emitted output carries a `budget` section (level, estimated_tokens, note,
> omitted_files) so the agent knows what it got. `impact --file` gained the
> same trimming via `impact.FileResult`, which also de-duplicates the
> file-mode output shape that CLI and MCP each had. Snippet inlining copies
> index-aliased nodes before mutation; failures are best-effort empty.
> Measured on this repo: `impact --target hiddenTypes` detail ≈2.7k tokens;
> `--max-tokens 4000` keeps detail, `--max-tokens 500` rolls up to 5 file
> entries and trims to 1.

## Tier 2 — Missing queries

### Phase 2.1 — Diff-aware impact `done`

> Outcome: `polyflow impact --diff [--staged]` reindexes incrementally,
> parses `git diff -U0` (worktree-vs-HEAD by default, `--cached` with
> `--staged`; `internal/gitdiff`), and maps each hunk to nodes by span
> overlap: declaration line for point nodes, `end_line` meta for
> function/method/worker bodies, with a nearest-preceding open-ended-scope
> fallback (matcher parity). Blast radii are unioned at minimum depth via
> `impact.BuildDiff`; changed nodes appear as `targets`, never double as
> callers. Trust contract carried through: hunks with no node land in an
> `unmapped_hunks` section (always present, like `unresolved`) and survive
> any token budget; deleted files are reported there too — their former
> callers surface as unresolved refs after the reindex. Supports the full
> impact flag set (`--depth/--service/--format/--max-tokens/--summary/`
> `--snippet-lines`); shares `assemble`/`rollupCallers` with the node-target
> path. Untracked files are intentionally skipped: committed code cannot
> reference a brand-new file unless the referencing file also changed, which
> the diff already covers. MCP exposure of the diff tool is a follow-up.
> Verified on this repo: the phase's own working tree mapped 3 changed files
> to 13 nodes, flagged the import-hunk as unmapped, and surfaced
> `mcpserver.go` in the blast radius via the shared budgeting code.

`polyflow impact --diff [--staged]`: map git diff hunks → nodes by
file + line-span, incremental reindex first, union blast radius. Directly
answers "will my current changes impact anything".

### Phase 2.2 — File-granularity related-files answer `done`

> Outcome: `context --file <path>` (repeatable; `--service`/`--limit`/`--depth`/
> `--max-tokens`/`--format` supported) and the MCP `context` tool's `files`
> input return the files related to the seed file(s), answering "where is the
> code for X". Ranking (`graph.RelatedFiles`): direct references first — the
> count of distinct edges with one endpoint in a seed file and the other
> outside, in *either* direction, since relatedness is symmetric though edges
> are not — then hop distance, then reached-node count. Neighborhood is an
> undirected multi-source BFS over all seed nodes at once (a file two hops away
> through a shared caller counts, and hop distances are the true minimum);
> import-derived cross-file edges are already materialized as calls/reads/
> writes/uses_type by the linker, so imports need no special case. Seed files
> are excluded from their own result; paths resolve like `NodesInFile` (exact,
> then "/"+path suffix). Trust contract carried through: any seed path with no
> nodes in the index is an error (a silently ignored seed would fake
> completeness), the unresolved section is scoped to seed+related files and
> always present, and it survives any token budget (only the related list is
> trimmed). File mode defaults `--depth` to 2 — a call-graph neighborhood at
> depth 5 is the whole repo. Verified on this repo: `context --file
> internal/graph/related.go` ranks model.go/files.go/context/files.go (its
> direct dependents and dependencies) above depth-2 neighbors; `--max-tokens
> 300` trims to 8 files with an omitted-count note.

`context` file mode: given file(s), return ranked related files
(graph neighborhood + imports), for "find where the code for X is" prompts.

## UI track (after Tier 0 lands; data must be right before presenting it)

### Phase U.1 — Variables hidden by default `pending`
`showVariables` signal (default off, persisted, structure view exempt) +
toolbar toggle; clear selection if the selected node becomes hidden.

### Phase U.2 — Grouping default hardening `pending`
Stop persisting `groupByFile=off` across sessions (URL param still wins);
fresh landings always grouped.

### Phase U.3 — Event-binding visibility `pending`
Tag JSX event-handler edges (`meta.event` = click/submit/…) in the matcher;
render an edge label/style so onClick bindings are distinguishable from
plain calls. Same for HTML/templ event edges.

### Phase U.4 — Root/dead-code badges & variable flow legibility `pending`
Badge `root_kind` (0.4) in the UI (dead-code highlight); surface captures
(partial confidence) via a hint or promote same-file captures to `inferred`
so closure flow is visible by default.

## Tier 3 — Deferred

Semantic/embedding concept search ("root nodes bug" → files). The agent + FTS
combination covers this acceptably for now.
