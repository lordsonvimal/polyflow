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

The **match engine** (one function) indexes consumers by `(Kind, Key)`, matches each
producer through a tiered strategy (**exact → normalized → wildcard-anchored**), emits
a typed edge with confidence, and records unmatched producers as `UnresolvedRef`s.
Same-service handling, cross-service-only, and hint overrides are rule-declared, not
hardcoded.

## Declarative rule shape (YAML)

Per kind: producer spec (node type + meta gate), consumer spec, key fields, ordered
normalizers, match strategy, edge type, confidence tiers, same-service policy, hint
source. Loaded like tree-sitter patterns (embedded defaults + workspace-custom),
version-gateable via the existing `package` / `version_range`.

```yaml
contract:
  kind: http
  producer: { node: http_client, key: [method, path] }
  consumer: { node: http_handler, key: [method, path] }
  normalizers: [base_url_strip, query_strip, param_wildcard, group_prefix]
  match: [exact, normalized, wildcard_anchored]
  edge: { type: http_call, same_service: skip_unless_datastar }
```

**Normalizer library** (reusable, referenced by name): `param_wildcard`
(`:id`/`{id}`/`[..]`→`*`), `query_strip`, `quote_strip`, `case_fold`, `trim_slash`,
`base_url_strip`, `group_prefix` (reconstruct gin/chi router-group prefixes — closes
the known gap), `shared_anchor_guard` (≥1 concrete segment), `url_to_path`. A genuinely
new transform = one Go func added to the registry, referenced by name in any rule (the
bounded escape hatch).

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

### Phase G.3 — Close the route-group gap as a rule `pending`
Add `group_prefix` normalizer + gin/chi router-group prefix reconstruction. chessleap
datastar real-handler links **3/27 → ~27/27**; no matcher edits (rule/normalizer only).
Proves gap-fixing is now config.

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
- **Benchmark:** engine is O(producers+consumers) per kind (same as today); hold
  chessleap index time + `BenchmarkIndexCold`.

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
