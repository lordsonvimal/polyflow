# Plan 8 — Multi-Repo Workspaces (Tier Z): one graph across separate git repos

Status legend: `pending` · `in progress` · `done`

> **Prerequisites — execute after `docs/plan-7-fleet-stacks.md` (needs its
> corpora for Z.2) and before plan-6's N.3** (N.3 documents whatever shipped
> and runs strictly last of everything). Follows `docs/phases.md` (rules
> 1–12 binding, one phase per commit, outcome note in the same commit).
> Read ONLY this file plus `docs/phases.md` to implement any phase.

## Context

The author's fleet is seven separate git repos that call each other:
nextGen enqueues RabbitMQ work its agents (nextGen-CDR-Agent,
nextGen-SCE-Agent) consume; services call each other over REST. Every
existing plan assumes one workspace = one repo. Today:

- `workspace.Service.Path` goes through `filepath.Abs` **relative to the
  process CWD**, not the workspace file — running `polyflow index
  --workspace /x/workspace.yaml` from another directory silently resolves
  paths wrong (or errors). No `~` expansion exists.
- `impact --diff` shells `git diff` once, assuming the workspace root is
  the single git repo.
- Nothing tests a workspace whose services live in different repos, and no
  eval case covers a cross-repo edge.

The cross-service linking itself needs **zero changes**: the contract
engine joins on channel keys and never looks at git boundaries. This tier
is path semantics, diff mechanics, and proof.

---

### Phase Z.0 — Workspace path semantics for out-of-tree services `pending`

**Problem.** Paths resolve against CWD; `~` is not expanded; a workspace
listing services in other repos only works by accident when run from the
right directory.

**Deliverable.** In `internal/workspace/config.go` `Load()` (single
choke-point — verify every consumer gets paths from Load, grep
`svc.Path` first and list the consumers in the outcome note):

1. **Pinned resolution order** for each `Service.Path`, applied at Load:
   a. expand a leading `~/` to `os.UserHomeDir()` (exactly the prefix
      `~/`, not `~user/` — that form errors with a named message);
   b. if still relative, join to **the directory containing the loaded
      workspace.yaml** (NOT the CWD);
   c. `filepath.Clean`. Store the resolved absolute path back on the
      struct so all downstream code is unchanged.
2. **Validation at Load:** a resolved path that does not exist or is not a
   directory → error naming the service and both the raw and resolved
   path (silently indexing an empty dir fakes completeness). A workspace
   with two services resolving to the same directory → error (duplicate
   service roots corrupt same-service semantics).
3. `polyflow init` output is unchanged (it writes `./`-relative paths for
   the discovered single-repo case; multi-repo workspaces are hand-written
   — document this in the workspace.yaml comment header it generates:
   one added comment line: `# paths may be absolute or ~/-prefixed; relative paths resolve against this file`).

**Worked example** (this exact file is the Z.2 fixture seed):

```yaml
# ~/Projects/fleet-workspace/workspace.yaml
name: fleet
version: "1"
services:
  - name: nextgen
    path: ~/Projects/nextGen
    language: ruby
  - name: cdr-agent
    path: ~/Projects/nextGen-CDR-Agent
    language: ruby
links:
  - {from: nextgen, to: cdr-agent, via: rabbitmq}
```

**Tests** (`internal/workspace/config_test.go` additions, all through real
`Load()` on temp files — rule 6):
- relative path resolves against the workspace file's dir, not CWD
  (change CWD in the test to prove it);
- `~/` expands; `~user/` errors with the named message;
- nonexistent path errors naming service + both paths;
- duplicate resolved roots error;
- absolute path passes through unchanged;
- existing single-repo workspaces load identically (regression: load this
  repo's own `workspace.yaml`, assert resolved paths equal the old
  behavior when CWD == workspace dir).

**Acceptance.** From an unrelated CWD:
`polyflow index --workspace ~/Projects/fleet-workspace/workspace.yaml`
indexes both repos into one graph (node counts per service recorded).
No `graph.SchemaVersion` bump (no stored shape change).

### Phase Z.1 — `impact --diff` across per-service git repos `pending`

**Problem.** `internal/gitdiff` runs one `git diff` at the workspace root.
In a multi-repo workspace, uncommitted changes in service repos are
invisible — worse than an error, it reports "no impact" (a silent gap).

**Deliverable.**
1. Per-service git root discovery: for each service, `git -C <svcPath>
   rev-parse --show-toplevel` (cache per distinct root; services sharing a
   monorepo root share one diff run).
2. `impact --diff` unions hunks from every distinct root, mapping each
   repo's paths back to the service's files (paths from git are
   root-relative; join to the root, then match against service files).
3. A service whose path is not inside any git repo → one entry in the
   existing `unmapped_hunks` section, kind `no_git_repo`, naming the
   service (surfaced, never silent; the rest of the diff still runs).
   `--staged` applies per-root.

**Pinned semantics (rules 1/2):** hunk→node mapping, union-at-min-depth,
and the `unmapped_hunks` floor are unchanged (Phase 2.1 behavior);
multi-root only changes *where diffs come from*. Roots are processed in
sorted path order; output must be byte-identical across two runs
(determinism test).

**Tests.** Two temp git repos, one workspace: change a file in each →
both blast radii present in one result; change in repo A only → repo B
contributes nothing and nothing errors; non-git service dir →
`no_git_repo` unmapped entry; two-run determinism.

**Acceptance.** In the fleet workspace, edit one file in nextGen and one
in CDR-Agent; `polyflow impact --diff` reports both targets and any
cross-repo edges between them.

### Phase Z.2 — Fleet workspace + cross-repo eval corpus `pending`

**Problem.** Cross-repo flows have zero measured proof.

**Deliverable.**
1. Commit `eval/corpus/fleet/workspace.yaml` — the Z.0 worked example
   extended to nextGen + CDR-Agent + SCE-Agent, service paths via
   `eval/.cache/<name>` symlinks (chessleap precedent; setup comment in
   the manifest header, `SkippedCorpus.LocalOnly` in CI).
2. `eval/corpus/fleet/manifest.yaml` — ≥6 hand-verified cross-repo cases
   (grep/read at pinned shas of ALL repos; record each sha):
   - ≥2 RabbitMQ: a nextGen `bunny` publish whose queue/exchange a
     CDR-Agent consumer reads — expected_impacted includes files from
     BOTH repos; `must_not_miss` includes the consumer file;
   - ≥2 REST: an agent `rest-client` call → nextGen route (or the
     reverse) — the URL is likely env-var-built: if F.3 config resolution
     resolves it, assert the edge; if not, the case's ground truth is the
     `dynamic_url` ledger entry (an honest-miss case asserting the gap is
     SURFACED — scorer counts it `HonestMiss`, and the case documents the
     config file that would resolve it);
   - ≥1 `impact --diff` cross-repo case (Z.1): a patch file touching the
     nextGen publisher, expected blast radius includes the CDR consumer;
   - ≥1 negative: two same-named queues in unrelated repos NOT in the same
     workspace must not link (index the fleet workspace vs. a
     nextGen-only workspace; assert the cross edge exists only in the
     former — guards against key-collision noise claims).
3. This plan's outcome note records the end-to-end
   `polyflow trace` output (edge list) for one RabbitMQ flow — the
   goal-closing artifact.

**Tests.** Corpus lint (must_not_miss) picks the new manifest up; a gate
run with the fleet corpus present.

**Acceptance.** `polyflow eval` reports the fleet corpus; baseline
ratcheted; the trace artifact is in the outcome note. **The bar: every
cross-repo case either passes or is an honest miss with the exact ledger
entry named — zero silent misses.**

---

## Key files

- Modified: `internal/workspace/config.go` (+ tests), `internal/gitdiff/`
  + `internal/impact/` diff path (+ tests), `cmd/polyflow` only if flag
  help text changes.
- New: `eval/corpus/fleet/{workspace.yaml,manifest.yaml}`, symlinks under
  `eval/.cache/` (gitignored; setup pinned in manifest comments).

## Explicit non-goals (descoped with this written claim)

- Cross-repo **version skew** (repo A pinned to an old repo B): one
  workspace = one snapshot of each repo; skew detection is a future tier.
- Remote repos / cloning: services are local checkouts; `url:`-based fleet
  members are not supported here.
- Cross-repo incremental cache invalidation beyond the existing per-file
  hashing (which is already path-keyed and works unchanged).

## Verification

Per-phase fixtures positive+negative; two-run determinism on diff output
and eval results; `BenchmarkIndexCold` hold (Z.0 adds O(services) work at
load only); full suite + eval gate green before each commit. Rule 10
applies to Z.1 (diff paths cross memory/store boundaries): the acceptance
run must complete `index` + `impact --diff` end-to-end on the real fleet,
not only on fixtures.
