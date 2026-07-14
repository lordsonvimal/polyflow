# Polyflow — Per-Phase Process

The phase plans (`docs/agent-context-plan.md`, `docs/contract-matching-plan.md`,
`docs/versioning-matrix-plan.md`, `docs/evidence-fusion-plan.md`,
`docs/runtime-flow-plan.md`) all follow this process. The original gap-closing plan that carried these rules is complete and was
removed; this doc keeps the rules themselves.

Status legend used in every plan: `pending` · `in progress` · `done (commit <sha>)`.

## Ground rules — every phase

- **One phase per commit.** Tests pass before each commit; the owning plan doc is
  updated (status → `done (commit <sha>)`, plus an outcome note) in the same commit.
- **Positive + negative fixtures.** Every new/changed pattern YAML ships a positive
  fixture (`input.*` + `expected.json`) and a negative fixture (`negative.*`, zero
  matches). Version-gated patterns additionally ship a same-shape-wrong-version
  negative. The "no fixture → CI fails" rule stays intact.
- **Additive by config.** New stacks/protocols are added as YAML + fixtures only;
  core matcher/graph/engine code changes only for genuine new capabilities.
- **Benchmark hold.** Changes on the indexing path hold chessleap index time and
  `BenchmarkIndexCold` (`make bench`).
- **`graph.SchemaVersion` bump** whenever the stored node/edge shape or semantics
  change, so stale incremental caches are discarded.
- **Trust contract.** Recall over precision; no silent gaps — anything unresolvable
  is surfaced (unresolved ledger or labeled low-confidence edge), never dropped;
  `docs/polyflow-design.md` is updated whenever a phase changes a documented
  decision.
