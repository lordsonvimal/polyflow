# Polyflow — Per-Phase Process

The phase plans (`docs/agent-context-plan.md`, `docs/contract-matching-plan.md`,
`docs/versioning-matrix-plan.md`, `docs/evidence-fusion-plan.md`,
`docs/runtime-flow-plan.md`, `docs/goal-completion-plan.md`,
`docs/semantic-search-plan.md`) all follow this process. The original gap-closing plan that carried these rules is complete and was
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
8. **Tier D** (doctor --propose, ledger burn-down) and **Tier C** (CI/PR
   freshness) — need G.5 and F-states respectively.
9. **P.1–P.2** (proof benchmarks) — last; P.1 needs A + E + S, P.2 needs G.5.

**V.2/V.3 sidecars** are divergence-triggered (versioning-matrix plan): do
not build them until a V.4 matrix cell actually diverges.

Referencing rule for implementers: every prompt/task should name **this file
(process + order) plus the single owning plan doc for the phase being
implemented**. The plan docs are written so that pair is sufficient — no
other context needed.

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
  `BenchmarkIndexCold` (`make bench`).
- **`graph.SchemaVersion` bump** whenever the stored node/edge shape or semantics
  change, so stale incremental caches are discarded.
- **Trust contract.** Recall over precision; no silent gaps — anything unresolvable
  is surfaced (unresolved ledger or labeled low-confidence edge), never dropped;
  `docs/polyflow-design.md` is updated whenever a phase changes a documented
  decision.
