# Polyflow — Generic Contract-Matching Linker Plan

Status legend: `pending` · `in progress` · `done (commit <sha>)`

## Context

**Why.** Cross-service edges are runtime-string couplings (a URL, a topic, a queue,
a gRPC `service/method`) — inherently heuristic, never compiler-resolved. Today
polyflow encodes each convention as a **bespoke Go linker**: `LinkRouteHandlers`,
`Linker.Link` (HTTP), `LinkBrokerChannels`, `LinkHubFanout`, `LinkJobQueues`,
`LinkPusherChannels`, `LinkSSEClients`, `LinkWebSocketMessages`, plus special-cased
path normalization. Every new protocol = new Go + new matching + new normalizer
tweaks. That is the per-scenario treadmill; it's also where the known gap lives
(gin `r.Group("/play")` prefix stripping left **24/27** chessleap datastar actions
unresolved).

Inspecting them proves they are **one shape**: index producers, index consumers,
build a normalized *channel key* from node meta, match, emit a typed edge — varying
only in (node types, key fields, normalizers, confidence, same-service policy).

**Goal.** Collapse all cross-boundary linking into **one engine driven by declarative
YAML "contract" rules**, so a new framework/protocol is a rule file (config), not core
surgery. The base scales by configuration; anything unmatched surfaces in the
unresolved ledger (recall over precision). This does **not** achieve "zero rules
ever" — a brand-new framework still needs one recognition pattern + one rule — but it
converts growth from *core rewrites* to *additive config*, the realistic ceiling for
cross-service static analysis.

**Locked decisions:** (1) replace the bespoke linkers **after golden-parity**, one
kind at a time; (2) rules are **YAML, recompile-free** (embedded + workspace-custom);
(3) scope = port existing + fix route-group gap + add **gRPC, GraphQL, Kafka, NATS,
Redis pub/sub** now.

Follows the repo per-phase process (`docs/phases.md`): one phase per commit,
positive+negative fixtures, benchmark, doc update, `graph.SchemaVersion` bump when
stored shape changes.

---

## Core model

A **contract** = a normalized cross-boundary connection. Every producer/consumer node
projects to `Contract{Kind, Key, Role, Service, NodeID, Confidence}`:

- **Kind**: `http | amqp | kafka | nats | redis_pubsub | sse | websocket | job |
  pusher | grpc | graphql | …`
- **Role**: `producer | consumer`
- **Key**: normalized channel identity from node meta (method+path, exchange/routing_key,
  topic, subject, queue, service/method, operation name).

The **match engine** (one function) indexes consumers per kind, matches each producer
through a tiered strategy (**exact → normalized → wildcard-anchored**), and emits a
typed edge with confidence. Two behaviors the naive `(Kind, Key)` hash-join picture
hides, both rule-declared:

- **Pair-conditioned normalization.** HTTP base_url stripping depends on the
  *(producer service, target service)* pair, so the consumer index is built per
  producer-target view where a rule declares pair-conditioned normalizers — exactly
  what `Linker.Link` does today with its per-client handler maps.
- **Unmatched policy, per producer class.** Today's behavior is *not* "unmatched →
  ledger" uniformly, and parity must preserve it: an unmatched API call emits a
  **visible unknown-confidence edge** to a synthetic `unresolved:<service>` node (so
  the dangling call appears in `impact`/`trace` traversals — better for the agent
  than a ledger row), while an unmatched nav-link is **silently dropped** by design.
  Each rule declares `unmatched: unknown_edge | ledger | drop`; `unknown_edge` is
  the default for call-like producers.

Same-service handling, cross-service-only, method fallback (an empty client method
tries GET/POST/PUT/PATCH/DELETE in priority order, as `candidateMethods` does today),
and hint overrides are likewise rule-declared, not hardcoded.

## Declarative rule shape (YAML)

Per kind: producer spec (node type + meta gate), consumer spec, key fields, ordered
normalizers, match strategy, edge type, confidence tiers, same-service policy, hint
source. Loaded like tree-sitter patterns (embedded defaults + workspace-custom),
version-gateable via the existing `package` / `version_range`.

```yaml
contract:
  kind: http
  producer:
    node: http_client
    key: [method, path]
    method_fallback: [GET, POST, PUT, PATCH, DELETE]   # empty client method
  consumer: { node: http_handler, key: [method, path] }
  normalizers: [base_url_strip, query_strip, param_wildcard]
  match: [exact, normalized, wildcard_anchored]
  edge: { type: http_call, same_service: skip_unless_datastar }
  unmatched: unknown_edge   # unknown_edge | ledger | drop (nav_link variant: drop)
```

**Normalizer library** (reusable, referenced by name): `param_wildcard`
(`:id`/`{id}`/`[..]`→`*`), `query_strip`, `quote_strip`, `case_fold`, `trim_slash`,
`base_url_strip`, `shared_anchor_guard` (≥1 concrete segment), `url_to_path`. A
genuinely new transform = one Go func added to the registry, referenced by name in any
rule (the bounded escape hatch). Normalizers are **pure key transforms over a single
node's meta** — anything that needs context from *other* nodes (e.g. router-group
prefix reconstruction, see G.3) is a meta-**enrichment pass** that runs before the
engine, not a normalizer.

## Reuse (don't rebuild)

- `normalizePath`, `routeKey`, `resolveHandler`, `urlToPath`, `stripMeta`
  (`internal/linker/linker.go`) → seed normalizers + match tiers.
- `ApplyHints`, `LinkBrokerHints`, `workspace.Link` (base_url, target_service, broker
  hints) → folded in as a hint/override rule source.
- `graph.UnresolvedRef` ledger → the resolve-or-surface output.
- `patterns.VersionInRange` + `Registry.ForService` → rule activation gating.
- Existing tree-sitter YAML patterns already emit producer/consumer nodes+meta; the
  engine only *consumes* them. Only the NEW protocols need new recognition patterns.

## Phases (one commit each, parity-gated)

### Phase G.0 — Engine + model + normalizer registry + rule loader `pending`
`internal/contract/{model.go,engine.go,normalize.go,rules.go,loader.go}` + a
golden-graph harness (snapshot chessleap edges). Engine unused → no behaviour change.

### Phase G.1 — Port HTTP `pending`
`contracts/http.yaml` reproduces `Linker.Link` + `LinkRouteHandlers` (datastar
same-service exception, nav-link, base_url, query-strip, wildcard-anchor). Assert
**edge-identical** to old on chessleap + linker unit tests; then delete
`Linker.Link`/`LinkRouteHandlers`.

### Phase G.2 — Port messaging/eventing `pending`
`contracts/{amqp,hub,jobs,pusher,sse,websocket}.yaml`; parity-gate each; delete the
bespoke linkers.

### Phase G.3 — Close the route-group gap (pattern + enrichment pass + rule) `pending`
Router-group prefixes are **not** reachable by a pure normalizer: `gin_route_group`
today captures only the prefix string, not the variable it is assigned to
(`api := r.Group("/api/v1")` — the assignment binding sits outside the query), so
route nodes carry no group context a key transform could use. Three parts:

1. **Recognition-pattern change:** capture the assignment binding of
   `x := r.Group("/prefix")` (gin) and chi's equivalent, so group nodes carry
   variable + prefix + declaring scope.
2. **Meta-enrichment pass (Go, pre-engine):** join route nodes to group nodes by
   (file, enclosing function, receiver variable), handle nested groups, and stamp
   the reconstructed full path into route meta. This is contextual node-joining,
   not a normalizer.
3. **Rule:** the http contract rule keys on the enriched path — no engine changes.

**Scope:** variable-scoped groups within a function/file (incl. nesting). Groups
passed **across functions/files** (`registerRoutes(g *gin.RouterGroup)`) are out of
scope here — variable-name scoping cannot see them; they need the SSA pass (tracked
as a follow-up), and affected routes stay surfaced as unresolved rather than being
silently mis-prefixed. chessleap datastar real-handler links **3/27 → ~27/27**
(its groups are same-file).

### Phase G.4 — New protocols, additive `pending`
Recognition patterns + contract rules for `grpc`, `graphql`, `kafka`, `nats`,
`redis_pubsub`; each with a 2-service fixture; prove each links with **zero engine
changes** (only YAML added). `SchemaVersion` bump for new edge/node kinds.

### Phase G.5 — Third-party rule loading + coverage `pending`
Workspace-custom rule dir (recompile-free); `polyflow doctor` prints per-kind coverage
(matched / unresolved); surface the ledger in `status`.

---

## Key files

- **New:** `internal/contract/` (model, engine, normalize, rules, loader),
  `contracts/*.yaml` (embedded rule dir),
  `patterns/{go,javascript,…}/{grpc,graphql,kafka,nats,redis}.yaml` (new recognition),
  `testdata/contracts/<kind>/` fixtures, doctor command file.
- **Modify:** `internal/indexer/indexer.go` (replace the block of
  `writeEdges(linker.LinkX(allNodes))` calls with one `engine.Link(allNodes, rules,
  hints)` + unresolved surfacing), `internal/linker/*.go` (delete ported linkers),
  `internal/graph/model.go` (new edge/node kinds; `SchemaVersion`).

## Verification

- **Golden parity gate:** snapshot chessleap edges before G.1; after each port assert
  the edge set is identical (a port that can't reproduce old edges blocks deletion — no
  silent regression). Existing linker unit tests pass or convert to rule-fixture tests.
- **Route-group:** chessleap datastar real-handler links 3 → ~27; total unresolved refs
  drop; `polyflow trace` closes more `route→…→action→handler` loops.
- **Additive proof:** minimal 2-service fixtures for gRPC/GraphQL/Kafka/NATS/Redis under
  `testdata/`; index; assert cross-service edges appear with only YAML added.
- **Coverage:** `polyflow doctor` per-kind matched/unresolved; a test fails if a
  registered kind lacks a fixture.
- **Benchmark:** be honest about complexity — today's HTTP linker is
  **O(clients × handlers)**, not O(P+C): it rebuilds the handler maps per client
  (pair-conditioned base_url stripping) and linearly scans handlers in the wildcard
  tier, and the engine must preserve those semantics for parity. Target: exact tier
  as a hash join; normalized/wildcard tiers may scan per producer. The gate is
  empirical, not asymptotic: hold chessleap index time + `BenchmarkIndexCold`.

## Risks / honest notes

- **Not everything is a contract.** `LinkDatastores` (call-site→service store) and the JS
  import linker (within-service) are structural, not producer/consumer matching — keep
  them out of the engine (documented), don't force-fit.
- **YAML expressiveness ceiling.** Non-key-equality matching (e.g. GraphQL schema
  stitching) may need a new match-strategy/normalizer in Go — bounded escape hatch,
  still declared by name in the rule.
- **Realistic ceiling.** A brand-new framework still needs one recognition pattern + one
  rule. The win is "additive by config, unknowns surfaced," not "never touch the repo."

## Relationship to the versioning-matrix plan

`docs/versioning-matrix-plan.md` addresses a **different axis** (version *fidelity* per
framework). This plan addresses **breadth** (linking *any* cross-service convention).
Contract-matching is higher leverage for "works on all repos"; versioning layers on top
of the same engine later (each contract rule becomes version-gateable).

## Sequencing

```
G.0 ─> G.1 ─> G.2 ─> G.3
                 └─> G.4 ─> G.5   (each new kind = rules + fixture, no engine change)
```
