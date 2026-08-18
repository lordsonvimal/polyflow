# Polyflow vs. GitNexus — 10 Use Cases on `datascience`

Live, executed comparison against [GitNexus](https://github.com/abhigyanpatwari/GitNexus) on the
`datascience` fleet repo (dsw-agent + dsw-manager, 1086 files). Both tools indexed fresh.
Captured 2026-08-19. Supersedes the docs-only GitNexus row in `competitive-comparison.md` with
runtime evidence.

## Indexing

| Metric | polyflow | GitNexus |
|---|---|---|
| Wall time | 97s → **19.7–44s** after the load-mode fix below | 33.7s |
| Nodes | 21,544 | 30,531 |
| Edges | 54,135 | 112,601 |
| Cross-service links | 36 | 0 (no HTTP/route edge type exists) |

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
- polyflow `impact --diff`: **wrong** — reported `"file has no nodes in the graph"` for a file/line
  that unambiguously has nodes. Real bug — see fix below.

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
| 8 | Git-diff impact | **GitNexus** (polyflow has a real hunk-mapping bug) |
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
  `LoadAllSyntax` on error — closes the indexing-speed gap (97s → 19.7–44s) without losing
  recall on repos with `//go:embed`-pattern build errors (confirmed via the gotify eval corpus).
