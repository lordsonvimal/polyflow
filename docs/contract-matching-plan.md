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

**Worked example — the complete `contracts/http.yaml` that G.1 must ship.** Every
field is populated and commented; this is the schema-by-example. Note how the
nav-link behavior is a second *variant* of the same kind (different `where` gate,
edge type, and unmatched policy) rather than an engine special case, and how the
datastar exception generalizes to `skip_unless_meta:<key>` instead of a hardcoded
enum:

```yaml
# contracts/http.yaml — ports Linker.Link (API calls + nav-links).
version: "1"
contracts:
  # Variant 1: API calls (fetch/axios/datastar actions → handlers).
  - kind: http
    producer:
      node: http_client
      where: { nav_link: "" }        # meta gate: absent-or-empty ⇒ not a nav link
      key: [method, path]            # Meta fields, joined "METHOD path"
      key_fallbacks:
        path: [url]                  # Meta["path"] empty → derive from Meta["url"]
                                     # (url_to_path normalizer does the reduction)
      method_fallback: [GET, POST, PUT, PATCH, DELETE, ""]  # empty client method:
                                     # try verbs in this order (candidateMethods parity)
    consumer:
      node: http_handler
      key: [method, path]
    normalizers:                     # applied in order to each key field
      [url_to_path, base_url_strip, query_strip, param_wildcard, trim_slash]
    match: [exact, normalized, wildcard_anchored]  # tiers in order; first hit wins
    edge:
      type: http_call
      id_prefix: link                # edge IDs stay "link:<from>-><to>" (parity!)
      same_service: skip_unless_meta:datastar   # skip | keep | skip_unless_meta:<key>
      via_meta: { datastar: datastar_action }   # producer meta key → edge Meta["via"]
    unmatched: unknown_edge          # visible edge to synthetic unresolved:<svc> node

  # Variant 2: navigation links (href/action) — same channel, different semantics.
  - kind: http
    producer:
      node: http_client
      where: { nav_link: "true" }
      key: [method, path]
      key_fallbacks: { path: [url] }
    consumer:
      node: http_handler
      key: [method, path]
    normalizers: [url_to_path, query_strip, param_wildcard, trim_slash]
    match: [exact, normalized, wildcard_anchored]
    edge:
      type: navigates_to
      id_prefix: nav                 # "nav:<from>-><to>"
      same_service: keep             # a page linking its own routes is the common case
      via_meta: { nav_link: nav_link }
    unmatched: drop                  # an unmatched page link is not an API dependency
```

### Pinned Go surface (G.0 implements exactly this)

These signatures are the G.0 contract — implement them as written so every later
phase (and every plan that references them: F.0's join, V.1's gating) composes
without renegotiation. Tier→confidence mapping is fixed: `exact` → `static`,
`normalized`/`wildcard_anchored` → `inferred`.

```go
// internal/contract/model.go
type Kind string // "http" | "amqp" | "kafka" | "nats" | "redis_pubsub" | "sse"
                 // | "websocket" | "job" | "pusher" | "grpc" | "graphql"
type Role string // "producer" | "consumer"

// Contract is one node projected onto a channel.
type Contract struct {
    Kind    Kind
    Role    Role
    Key     string // normalized channel key, e.g. "GET /games/*"
    RawKey  string // pre-normalization key (exact tier + diagnostics)
    Service string
    NodeID  string
}

// Normalizer transforms one key-field value. Pure function of (value, env):
// it must NOT read other nodes — contextual enrichment happens before the
// engine (G.3's meta-enrichment pass).
type Normalizer func(value string, env NormalizeEnv) string

// NormalizeEnv is the only context a normalizer may condition on. This is
// how pair-conditioned transforms (base_url_strip) work without breaking
// purity: the engine evaluates consumer keys per (FromService, ToService).
type NormalizeEnv struct {
    FromService string           // producer's service
    ToService   string           // consumer's service
    Links       []workspace.Link // hints: base_url, target_service, via/exchange
}

// RegisterNormalizer wires a named transform (from init()). Load fails fast
// on an unknown name — a YAML typo must never silently no-op.
func RegisterNormalizer(name string, fn Normalizer)

type MatchTier string
const (
    TierExact            MatchTier = "exact"             // hash join on RawKey
    TierNormalized       MatchTier = "normalized"        // hash join on Key
    TierWildcardAnchored MatchTier = "wildcard_anchored" // segment match; ≥1 shared
                                                         // concrete segment required
)

type UnmatchedPolicy string
const (
    UnmatchedUnknownEdge UnmatchedPolicy = "unknown_edge" // edge → unresolved:<svc>
    UnmatchedLedger      UnmatchedPolicy = "ledger"       // graph.UnresolvedRef only
    UnmatchedDrop        UnmatchedPolicy = "drop"         // discard (nav-links)
)

// Rule is the YAML-mapped shape (see the worked example above).
type Rule struct {
    Kind         Kind            `yaml:"kind"`
    Package      string          `yaml:"package,omitempty"`       // semver gate —
    VersionRange string          `yaml:"version_range,omitempty"` // patterns.VersionInRange
    Producer     EndpointSpec    `yaml:"producer"`
    Consumer     EndpointSpec    `yaml:"consumer"`
    Normalizers  []string        `yaml:"normalizers"`
    Match        []MatchTier     `yaml:"match"`
    Edge         EdgeSpec        `yaml:"edge"`
    Unmatched    UnmatchedPolicy `yaml:"unmatched"`
}

type EndpointSpec struct {
    Node           graph.NodeType      `yaml:"node"`
    Where          map[string]string   `yaml:"where,omitempty"`         // meta equality; "" ⇒ absent/empty
    Key            []string            `yaml:"key"`                     // meta fields, joined with " "
    KeyFallbacks   map[string][]string `yaml:"key_fallbacks,omitempty"` // per-field meta fallbacks
    MethodFallback []string            `yaml:"method_fallback,omitempty"`
}

type EdgeSpec struct {
    Type        graph.EdgeType    `yaml:"type"`
    IDPrefix    string            `yaml:"id_prefix"`    // edge ID "<prefix>:<from>-><to>" — part of parity
    SameService string            `yaml:"same_service"` // "skip" | "keep" | "skip_unless_meta:<key>"
    ViaMeta     map[string]string `yaml:"via_meta,omitempty"` // producer meta key → Meta["via"] value
}

// internal/contract/loader.go + engine.go
// Load merges embedded defaults + workspace-custom dir; fails on unknown
// normalizer names, tiers, or policies.
func Load(embedded fs.FS, workspaceDir string) ([]Rule, error)

func (e *Engine) Link(nodes []graph.Node, rules []Rule, links []workspace.Link) Result

type Result struct {
    Edges      []graph.Edge          // confidence per the fixed tier mapping
    Nodes      []graph.Node          // synthetic targets (unresolved:<svc>)
    Unresolved []graph.UnresolvedRef // one per UnmatchedLedger miss
}
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
