# Polyflow vs. GitNexus — 10 Use Cases on `datascience`

Live, executed comparison against [GitNexus](https://github.com/abhigyanpatwari/GitNexus) on the
`datascience` fleet repo (dsw-agent + dsw-manager, 1086 files). Both tools indexed fresh.
Captured 2026-08-19. Supersedes the docs-only GitNexus row in `competitive-comparison.md` with
runtime evidence.

## Indexing

Both indexes deleted and rebuilt from scratch immediately before measurement (`polyflow index
--full` after `rm -rf .polyflow`; `gitnexus analyze` after `gitnexus clean --force`), same binary
that ships today's fixes (see bottom of doc).

| Metric | polyflow | GitNexus |
|---|---|---|
| Wall time (fresh) | **19.8s** | 23.9s |
| Nodes | 21,544 | 30,542 |
| Edges | 54,135 | 112,611 |
| Cross-service links | 36 | 0 (no HTTP/route edge type exists) |
| Clusters / flows | n/a | 1,417 clusters, 300 flows |

Before today's load-mode fix, polyflow took 97s on this repo — 2.9x slower than GitNexus's 33.7s.
After the fix, polyflow now indexes the same repo **faster** than GitNexus on a fresh run (19.8s
vs 23.9s), while resolving 36 cross-service links GitNexus's schema has no edge type to hold at
all.

The node/edge gap is modeling philosophy, not missed extraction: GitNexus promotes struct fields
and doc-comment sections to first-class `Property`/`Section` nodes (16,127 of its total).
Polyflow stores the same field data (name, type, tag) as JSON metadata on the parent struct node
instead — retrievable in one lookup, not independently graph-traversable.

## 10 use cases

**1. Blast radius of a widely-used method** (`GetLatestVersion`, 18 real production call sites,
confirmed by grep)

- polyflow: found all 18 real callers correctly, including route handlers reached transitively.
- GitNexus: `impactedCount: 0, risk: LOW`. Confirmed via `context` too — zero incoming edges.
- Root cause: all 18 call sites are `h.execConfigService.GetLatestVersion(...)` — a struct-field
  receiver (the standard Go dependency-injection pattern). Reproduced on three more unrelated
  methods (`GetAllConfigsGrouped`, `UpdateExecConfig`, `GetByServerID` on a different service) —
  all zero. **Systemic gap in GitNexus's Go call-graph resolution for the single most common Go
  handler/service pattern**, not a one-off miss.

**2. Cross-service HTTP trace** (`RegisterAgent` in dsw-agent → `Heartbeat` handler in
dsw-manager)

- polyflow: full 6-hop chain in one call, across the service boundary, down to `notifySubs`.
- GitNexus: `"status": "no_path"`. No HTTP/route edge type exists in its schema at all.

**3. Precision: impact on a method with zero production callers** (`RegisterOrUpdate`, test-only)

- polyflow: returns all 14 graph-reachable nodes, with non-causal ones labeled
  `(structural — via type/containment, not a verified call)` (today's fix, see below).
- GitNexus: `impactedCount: 0`, clean by construction — doesn't attempt the
  containment-based recall mechanism at all.
- Both now give an honest answer; GitNexus's is simpler because it never walks containment edges.

**4. Concept search** (`"docker build status"`)

- polyflow: direct, relevant hits — `docker_build_service.go`, `docker_build_post_build.go`.
- GitNexus: `query` is process/flow-oriented, not symbol-oriented — returned execution-flow
  summaries (`Create → StatusColor`, `Create → StatusIcon`) tangential to the query. Different
  search paradigm, not directly comparable, but less immediately useful here.

**5. Symbol 360° view** (`AgentService` struct)

- polyflow: deep multi-hop view — methods, callers (incl. tests and `main`), and downstream to
  repository calls and `mysql`/`postgres`/`sqlite` query nodes. Found one cosmetic bug: the
  "Cross-service" section prints `→ contains → dsw-manager` 19 times (same-service containment
  mislabeled under a cross-service header) — worth a follow-up fix, not covered by this doc.
- GitNexus: clean single-hop view — fields as typed `Property` nodes, methods listed, but no
  downstream visibility without a separate query per method.

**6. Interface implementation discovery** (`Provider` interface, 2 known implementers:
`AnthropicProvider`, `BedrockProvider`)

- polyflow: found both implementers correctly (`[implements]`), plus every real call site.
- GitNexus: `IMPLEMENTS` query returned empty. Only 3 total `IMPLEMENTS` edges exist across the
  entire 1086-file, ~9000-function repo. Go's structural typing (no `implements` keyword) isn't
  reliably detected by a tree-sitter-only approach — it needs real type-checking, which is what
  polyflow's `go/packages` semantic pass does.

**7. Same-service multi-hop trace** (handler → service → repository → DB: `GetConfig` →
`GetLatestVersion` → `FindLatestVersion` → `mysql`/`postgres`/`sqlite`)

- polyflow: full 4-hop chain resolved correctly.
- GitNexus: `"status": "no_path"` — same root cause as #1, confirmed a third time across a third
  distinct tool (trace, not just impact/context).

**8. Git-diff-based impact** (single-line edit inside `RegisterOrUpdate`'s body, reverted after
test)

- GitNexus `detect-changes`: correctly identified `RegisterOrUpdate` as the changed symbol.
- polyflow `impact --diff`: was **wrong** — reported `"file has no nodes in the graph"` for a
  file/line that unambiguously has nodes. Root cause: `gitdiff.MultiChanges` root-joins diff paths
  into absolute paths, but `node.File` is workspace-relative for in-tree services; `NodesInFile`'s
  suffix match only checked one direction. **Fixed** (`internal/graph/files.go`, symmetric suffix
  check) — reverified after the fix: `impact --diff` now correctly reports `RegisterOrUpdate`,
  matching GitNexus.

**9. Struct field detail retrieval**

- polyflow: field name/type/tag preserved as JSON metadata (`Meta["fields"]`) on the struct node —
  retrievable but not independently graph-traversable.
- GitNexus: each field is a first-class `Property` node — graph-traversable (e.g. "find all
  structs with a field of type X"), at the cost of ~9K extra nodes.

**10. Tool/capability coverage** (CLI surface, not runtime-tested)

- GitNexus-only: `rename` (multi-file), `cypher` (raw graph queries), PDG/taint analysis (`--pdg`,
  TS/JS only), wiki generation, repo groups with a cross-repo Contract Registry.
- polyflow-only: cross-service AMQP/Kafka/SSE detection, variable-level dataflow tracking,
  deployment topology.

## Token reduction

Measured actual output byte size for equivalent queries — `impact` compared under each tool's
real default (polyflow: `impact.DefaultBudget` = 2000 tokens; GitNexus has no budget concept, its
output is whatever the query returns).

| Query | polyflow (budgeted) | GitNexus |
|---|---|---|
| `impact GetLatestVersion` | 8,013 bytes (~2,003 est. tokens, rolled up: 57 files omitted, budget note attached) | 533 bytes |
| `context AgentService` | 96,477 bytes — **budget not honored, see bug below** | 2,562 bytes |

GitNexus's `impact GetLatestVersion` number is not a fair "efficiency win": it's 533 bytes because
the query returned **zero results** (the struct-field-receiver gap from use case #1) — small
output from a wrong answer isn't token efficiency.

`context AgentService` surfaced a real, separate polyflow bug while measuring this: the
`cross_service` field contains **853 duplicate entries**, all `{"from_service": "", "to_service":
"dsw-manager", "edge_type": "contains"}` — same-service `contains` edges with an unset source
service getting misclassified as cross-service, and not deduplicated. This inflates the response
to ~96KB and **is not trimmed by `--max-tokens`** — the budget mechanism doesn't cover this field
at all, so it defeats polyflow's core token-budgeting design for any struct/service with many
same-service containment edges. Not fixed as part of this session; filed here for follow-up.

Net: where polyflow's budgeting actually engages (`impact`), it returns a complete, correctly
labeled answer in ~2K tokens instead of an unbounded blast radius. Where it doesn't engage
(`context`'s `cross_service` field), the tool's own token-reduction claim is currently broken.
GitNexus has no budgeting mechanism to compare against either way — its outputs are small because
its per-query result sets are small (by design, and in one case, by the resolution gap in #1).

## Scorecard

| # | Use case | Winner |
|---|---|---|
| 1 | Blast radius, common DI pattern | **polyflow** (GitNexus misses ~all struct-field-receiver calls) |
| 2 | Cross-service HTTP trace | **polyflow** (GitNexus has no such edge type) |
| 3 | Precision on dead code | Tie (both honest) |
| 4 | Concept search | polyflow (more directly relevant results) |
| 5 | Symbol 360° view | Mixed — polyflow deeper, GitNexus cleaner |
| 6 | Interface implementation | **polyflow** (GitNexus: 3 total edges in the whole repo) |
| 7 | Same-service multi-hop trace | **polyflow** (same struct-field gap as #1) |
| 8 | Git-diff impact | Tie (polyflow's hunk-mapping bug is now fixed, verified matching GitNexus) |
| 9 | Struct field modeling | Design tradeoff, not a winner |
| 10 | Tool breadth | Different strengths, not comparable |

**Headline finding:** GitNexus has a systemic Go call-resolution gap for method calls through
struct-typed fields (`h.service.Method()`) — the single most common dependency-injection pattern
in Go web codebases — confirmed independently across `impact`, `context`, and `trace`, on 4
unrelated symbols. More consequential than the indexing-speed gap it no longer has, since it
silently produces false negatives (zero risk reported) rather than noise.

**Also found:** a genuine bug in polyflow's `impact --diff` hunk-to-node mapping (#8), fixed
separately — see commit history for the fix and root cause.

## Fixes shipped alongside this comparison

- `fix(impact)`: label structural (containment/instantiate) blast-radius hops instead of letting
  them read as verified calls.
- `perf(parser)`: load Go dependencies as types-only (`LoadSyntax` not `LoadAllSyntax`), retry
  `LoadAllSyntax` on error — closes the indexing-speed gap (97s → 19.8s fresh) without losing
  recall on repos with `//go:embed`-pattern build errors (confirmed via the gotify eval corpus).
- `fix(graph)`: `NodesInFile` now matches absolute-vs-workspace-relative paths in both directions,
  fixing `impact --diff`'s false "file has no nodes in the graph" (use case #8).

## Verdict, by category

**Accuracy (does it find the real relationships)** — **polyflow.** Confirmed independently across
3 tools (`impact`, `context`, `trace`) and 4 unrelated symbols: GitNexus systematically fails to
resolve Go method calls through struct-typed fields (`h.service.Method()`), the standard
dependency-injection pattern. It also detects only 3 `IMPLEMENTS` edges in the entire repo (use
case #6) against Go's structural interface satisfaction, which needs real type-checking that
tree-sitter alone can't do. Polyflow's `go/packages`+SSA semantic pass gets both right.

**Reliability (does it degrade safely, does it do what it claims)** — **Mixed, roughly tied.**
Both tools had a real bug surface this session: polyflow's `impact --diff` mis-mapped hunks to
nodes (fixed), and its `context` command's token budget doesn't cover the `cross_service` field,
letting a 853-entry duplicate-edge bug balloon output 40x past budget (not yet fixed). GitNexus's
false negatives from the struct-field gap (#1) are arguably a worse reliability failure than
either, because they report `risk: LOW` with high confidence instead of surfacing an error —
a silent wrong answer, not a visible one.

**Precision (signal vs. noise in a given answer)** — **Close to tied, slight edge to GitNexus
on cleanliness, polyflow on completeness.** GitNexus's answers are less noisy by never walking
containment/instantiate edges; polyflow's now-labeled structural entries (today's fix) close most
of the gap without giving up recall. But precision is moot where GitNexus returns zero results
instead of the real answer (#1, #6, #7) — an empty result isn't precise, it's wrong.

**Token reduction** — **polyflow, where its budgeting actually works.** `impact` returns a
complete, correctly-scoped answer in ~2K tokens via real rollup/trimming; GitNexus has no
budgeting concept at all, so its small outputs are an artifact of small (sometimes wrong) result
sets, not designed efficiency. But this category comes with a real caveat: `context`'s
`cross_service` field bypasses the budget entirely (the 853-duplicate bug above), so the claim
does not currently hold uniformly across all of polyflow's tools — only where the budget path is
actually wired in.

**Performance (indexing speed)** — **polyflow**, as of today's fix. Was polyflow's clearest
weakness (97s vs. 33.7s, 2.9x slower); now faster than GitNexus on a fresh index of the same repo
(19.8s vs. 23.9s).

**Overall for this project's actual use case (multi-service Go fleet analysis):** polyflow is the
stronger tool by a wide margin — GitNexus's struct-field call-resolution gap alone would make it
unreliable for any real Go codebase using dependency injection, which is effectively all of them.
The one place GitNexus categorically wins is tool breadth (rename, PDG/taint, cross-repo Contract
Registry) — none of which offset a call graph that misses most real method calls in this
language.
