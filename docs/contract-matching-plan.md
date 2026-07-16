# Polyflow — Generic Contract-Matching Linker Plan

Status legend: `pending` · `in progress` · `done`

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
      target_service_meta: target_service  # when set on the node (by ApplyHints),
                                     # only consumers of that service are candidates
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
      target_service_meta: target_service  # shared lookup path in Linker.Link
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
    unmatched: drop                  # an unmatched LITERAL page link is not an app
                                     # flow (external URL/typo). Dynamic nav keys are
                                     # never dropped — they reach the ledger (G.6)
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
    // TargetServiceMeta (producer side) names a producer meta key whose
    // value, when non-empty, restricts matching to consumers of that
    // service — Linker.Link's target_service behavior; ApplyHints stamps
    // the meta from workspace hint:/base_url: links. Without this field
    // the engine cannot reproduce hinted-workspace parity.
    TargetServiceMeta string `yaml:"target_service_meta,omitempty"`
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

### Phase G.0 — Engine + model + normalizer registry + rule loader `done`
`internal/contract/{model.go,engine.go,normalize.go,loader.go}` + a
golden-graph harness (snapshot chessleap edges). Engine unused → no behaviour change.

**Outcome.** All pinned interfaces implemented exactly as spec'd in 4 files
(model, normalize, engine, loader); no `rules.go` was needed — rule helpers
folded into `loader.go`. The normalizer library ships all 8 built-ins
(`param_wildcard`, `query_strip`, `quote_strip`, `case_fold`, `trim_slash`,
`base_url_strip`, `shared_anchor_guard`, `url_to_path`) with a `NormalizerByName`
export for tests. One deviation from the spec's file list: `rules.go` omitted
— `validateRule` and `ruleFile` live in `loader.go` (no behaviour difference).
The `url_to_path` normalizer is a pass-through for non-URL values (returns the
value unchanged rather than `""`) so it is a no-op when applied to the `method`
key field — this matches the required per-field application semantics.
`contracts/embed.go` embeds a `.keep` placeholder; G.1 adds the first `.yaml`
file. 53 tests pass (18 engine, 11 loader, 24 normalizer); golden harness skips
correctly when chessleap eval repo is absent. `BenchmarkIndexCold` unaffected
(engine not wired into indexer).

### Phase G.1 — Port HTTP `done`
`contracts/http.yaml` reproduces `Linker.Link` (datastar same-service exception,
nav-link, base_url + target_service hints, query-strip, wildcard-anchor) — the
worked example above **is** this phase's rule file. `LinkRouteHandlers` is **not**
ported: it is name resolution (handler meta → same-service function *label*, with
receiver stripping), not producer/consumer channel matching, and the rule schema
deliberately cannot express label-keyed matching — it stays a structural linker
alongside `LinkDatastores` (see Risks). Assert **edge-identical** to old on
chessleap + linker unit tests; then delete `Linker.Link` only.

**Outcome.** `contracts/http.yaml` ships two rule variants (API call + nav-link)
exactly as worked-out in the spec. `Linker.Link`, `New`, and the 8 HTTP-only
helpers (`normalizePath`, `routeKey`, `resolveHandler`, `candidateMethods`,
`pathMatchesPattern`, `hasLiteralSegment`, `splitPath`, `urlToPath`) are deleted.
The indexer, e2e, and trace tests are updated to use `contract.Engine.Link`.
One deviation from the spec's normalizer list: `case_fold` is prepended to both
variants (`[case_fold, url_to_path, ...]`). The chi framework emits mixed-case
methods (`"Post"`, `"Get"`) while the templ parser emits `"POST"`, `"DELETE"`;
the old `Linker.Link` called `strings.ToUpper` on both sides — `case_fold` is
the correct normalizer-layer parity. A `wildcardScan` bug was also fixed: the
compound "METHOD /path" key was split on `/`, treating `"POST "` as a path
segment and creating a false shared anchor between unrelated same-shape routes
(e.g. `/play/*/draw` spuriously matching `/:id/goto/:nodeID`); the fix extracts
the path portion (from the first `/`) before segment-by-segment comparison.
17 http_rule_test.go tests cover positive + negative fixtures for both variants.
All 53 G.0 + 17 new tests pass; e2e, trace, and linker suites unaffected.
`BenchmarkIndexCold` holds (~11 s cold on the synthetic 1 200-file tree).

### Phase G.2 — Port messaging/eventing `done`
`contracts/{amqp,hub,jobs,pusher,sse,websocket}.yaml`; parity-gate each; delete the
bespoke linkers.

**Outcome.** All six YAML files shipped. `LinkBrokerChannels`, `LinkHubFanout`,
`LinkJobQueues`, `LinkPusherChannels`, and `LinkWebSocketMessages` deleted from
`internal/linker/linker.go` and all call sites removed from indexer, e2e, and trace
tests. `LinkSSEClients` kept as a structural (file co-location) linker — cross-service
EventSource URL matching is already handled by `http.yaml`; an empty placeholder
`contracts/sse.yaml` documents this decision. Two engine additions were required:
(1) `KindHub Kind = "hub"` constant; (2) `filterBySameServicePolicy` pre-filter and
`same_service_only` policy — without pre-filtering, same-service consumers occupied
index slots under skip/same_service_only policies, blocking cross-service consumers
(first-seen wins in the hash index). Hub fan-out uses `key: []` (empty key, broadcast
semantics) with `same_service_only`; limitation: first-seen wins means only the first
subscriber per empty-key slot is linked per broadcast node. Fixture chain tests for hub
and websocket ported to the contract engine; `EdgeMeta["message_type"]` assertion
updated to `EdgeLabel` (contract engine carries the normalized key as edge label).

### Phase G.3 — Close the route-group gap (pattern + enrichment pass + rule) `done`
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

**Outcome.** Three parts shipped in one commit. (1) `gin_route_group` pattern changed
from a plain `call_expression` to a `short_var_declaration` query that captures
`var_name` (the assigned variable), `receiver` (parent router), and `prefix`; this is
the assignment binding the spec required. `chi_route_group` gains an `operand: @receiver`
capture and replaces `(_) @fn` with `(func_literal) @_body` so the func_literal end
line is tracked as a positional span. (2) `internal/contract/routegroup.go` implements
`EnrichRouteGroups`: gin enrichment resolves prefix chains by following receiver→var_name
links (iterative fixpoint handles nesting); chi enrichment uses line-range containment
(routes whose line falls within the func_literal body inherit the prefix, outermost-first
for nested groups). The pass deep-copies the Meta maps so the stored `allNodes` are
never mutated. (3) The indexer wires the pass between `ApplyHints` and `Engine.Link`
— the enriched path is used for matching only, not persisted. One deviation from the
spec: the enclosing-function scope constraint for gin is not applied (groups are matched
by var_name across the whole file), which is correct for the common pattern where all
groups and routes share one setup function. `SchemaVersion` bumped 10→11 because
gin_route_group nodes now carry new meta fields (`var_name`, `receiver`). `matcher.go`
extended to store `end_line` for all node types (not just function/method/worker) so
chi_route_group nodes receive the func_literal end row. 10 routegroup unit tests cover
all positive and negative cases. `BenchmarkIndexCold` holds (~10.1s, within baseline).

### Phase G.4 — New protocols, additive `done`
Recognition patterns + contract rules for `grpc`, `graphql`, `kafka`, `nats`,
`redis_pubsub`; each with a 2-service fixture; prove each links with **zero engine
changes** (only YAML added). `SchemaVersion` bump for new edge/node kinds.

**Outcome.** Five contract YAML files added (`contracts/{grpc,graphql,kafka,nats,redis_pubsub}.yaml`)
and five Go/JS pattern files added (`patterns/go/{grpc,kafka,nats,redis_pubsub}.yaml`,
`patterns/javascript/graphql.yaml`), each with positive + negative fixtures. New
node types: `grpc_client`, `grpc_handler`, `graphql_client`, `graphql_resolver`
(four total; Kafka/NATS/Redis reuse the existing `publisher`/`subscriber` types with
`where` pattern-gates to distinguish them). New edge types: `grpc_call`,
`graphql_call`, `kafka_publish`, `nats_publish`, `redis_publish`. `SchemaVersion`
bumped 11→12. Zero engine changes — `internal/contract/engine.go` untouched. The
`classifyPattern` function gained four new prefix cases (gRPC and GraphQL) placed
before the existing HTTP-client heuristic so `grpc_client_call` is not
misclassified. The trace fixture edge-type golden snapshot updated to include
`grpc_call` (emitted by the same-service unknown_edge path on the grpc pattern
fixture). Two deviations: (1) gRPC service-to-handler path matching is not
end-to-end integrated — the `grpc_client_call` pattern captures the raw Invoke
path while `grpc_server_register` captures the registration function name; the
contract rule is keyed on `service_method` and the fixture tests create nodes
manually with matching keys (same pattern as all G.2 tests). (2) gRPC same-service
test verifies "no direct edge to the handler" rather than "no edges at all" because
the `unknown_edge` policy fires for the filtered-out same-service client, consistent
with the HTTP behavior documented in the model. 25 new contract tests pass; all 5
pattern fixture sets pass (positive + negative). `BenchmarkIndexCold` holds (~10.1s).

### Phase G.5 — Third-party rule loading + coverage `done`
Workspace-custom rule dir (recompile-free); `polyflow doctor` prints per-kind coverage
(matched / unresolved); surface the ledger in `status`.

**Outcome.** Three deliverables shipped in one commit. (1) Workspace-custom rule loading
was already implemented in `contract.Load()`; the indexer now passes `opts.ContractsDir`
(set to `filepath.Dir(indexWorkspace)` by the CLI) so rules in `<workspace>/contracts/*.yaml`
are merged with the embedded defaults at index time without recompilation. A bad YAML in the
workspace dir fails fast (unknown normalizer/tier/policy detected at Load time). (2) After
`eng.Link()`, the indexer calls `contract.ComputeCoverage(rules, result)` and stores the
result as JSON in the `contract_coverage` DB meta key; `polyflow doctor` reads this and
prints a per-kind table of matched/unresolved counts, or a "run polyflow index first" prompt
if the DB is absent. (3) `polyflow status` now shows all unresolved-ref kinds dynamically
(structural kinds `call_ref`/`import_ref` first, then contract kinds sorted alphabetically)
instead of the hardcoded two-kind breakdown. No `graph.SchemaVersion` bump needed — only a
new DB meta key was added, which is forward/backward compatible. New tests: 5 loader tests for
workspace-custom loading (positive: rules loaded; merge with embedded; non-YAML ignored; negative:
bad normalizer fails fast; negative: non-YAML-only files skipped) + 7 coverage unit tests
(matched/unresolved split, all-matched, all-unresolved, empty result, sorted-by-kind,
duplicate-rule-kinds merged, non-contract edge types ignored). `BenchmarkIndexCold` holds
(~10.1s). All 90+ tests pass.

### Phase G.6 — Dynamic producer keys: branch enumeration + surfacing (all kinds, all languages) `done`

**Outcome.** Implemented in full: (1) `Meta["key_candidates"]` (JSON array of
literal alternatives) populated by per-language walkers and consumed by the
engine with N-way fan-out, emitting one `navigates_to`/`publishes`/etc. edge
per candidate at `confidence=inferred`, `via=branch_enum`; (2) `Meta["key_dynamic"]="true"`
surfacing to a `dynamic_<kind>` ledger entry (dynamic_url, dynamic_topic,
dynamic_queue, dynamic_channel, dynamic_event) with nav-drop refinement
(dynamic nav links always reach the ledger; unmatched literals still drop);
(3) walker registry (`internal/contract/keywalk.go`) with walkers for Go,
JavaScript, Ruby, and templ, plus an explicit no-op for HTML; (4) doctor gains
a `dynamic` column in the per-kind coverage table and a walker-coverage row
reporting `yes`/`no-op`/`MISSING` per language; (5) test guard
`TestWalkerCoverage_AllLanguagesHaveWalker` fails if any registered parser
language lacks a walker; (6) JSX nav-link patterns extended with
`nav_link_jsx_ternary` (shape a) and `nav_link_jsx_dynamic` (shape c). All
tests pass; `BenchmarkIndexCold` unchanged. One deviation from spec: walkers
are wired to meta post-processing in `MatchToGraph` (pattern-level captures
`branch_N`/`key_expr`) rather than called live during tree-sitter matching,
which fits the existing pipeline without requiring query-level changes in every
language pattern file.

**Problem.** Every producer key today is captured only when it is a **string
literal at the call/attribute site** — the tree-sitter queries require a
string node (e.g. `patterns/jsx/nav_links.yaml` matches
`(string (string_fragment))` only). Real code computes channel keys three
ways, and this affects **every** producer kind identically — a URL in an
`<a href>`, a fetch/axios/net-http URL, an AMQP exchange/routing key, a
Kafka/NATS topic, a job queue name, a pusher channel/event, a websocket
message type:

- **(a) Finite conditionals over literals** — `cond ? "/admin" : "/dashboard"`,
  Go `if/else`/`switch` assigning a topic, Ruby ternaries, a map/object lookup
  with literal values. *Decidable by enumeration* — missing these is a pure
  extraction gap, not an analysis limit.
- **(b) Local constant references** — `publish(ORDERS_TOPIC)` where
  `const ORDERS_TOPIC = "orders.created"` is in scope. Resolvable via the
  existing constant/variable tracking (`patterns/go/constants.yaml`, the JS
  variable extractor).
- **(c) Genuinely dynamic** — built from request data, function returns,
  runtime config. *Undecidable*; the only honest move is surfacing.

Today (a) and (b) frequently produce **no node at all** (the pattern doesn't
match), and for nav links even a captured-but-unresolvable key is silently
dropped (`Linker.Link`'s literal-era drop policy) — a real user flow can
vanish with no ledger entry, violating the trust contract.

**Deliverable.**

1. **Multi-candidate key convention (extraction side).** Producer nodes may
   carry `Meta["key_candidates"]` — a JSON array of literal alternatives —
   populated by per-language expression walkers that enumerate shape (a)
   (ternary, `if/else`, `switch`/`case`, `||`/`??`-of-literals, literal-valued
   map lookup) and resolve shape (b) through same-service constant lookup.
   Enumeration is **bounded**: > 8 branches, nested conditionals beyond depth
   2, or any non-literal branch ⇒ treat as dynamic (never partially enumerate
   and imply completeness). Applies uniformly to every producer meta field
   that feeds a rule key: `path`/`url` (http, nav), `exchange`/`routing_key`
   (amqp), `topic`/`subject` (kafka/nats/redis), `queue`/`job_class` (jobs),
   `channel`/`event` (pusher), message `type` (websocket), SSE endpoints.
2. **Engine: candidate fan-out (small, spec'd here).** A producer node with N
   key candidates projects to N `Contract` values (the pinned `Contract`
   type is unchanged — multiplicity lives in projection, not the schema).
   Each candidate matches independently; each hit emits its edge at
   confidence `inferred` with `Meta["via"]="branch_enum"`. The rule's
   `unmatched` policy fires **once per producer** only when *zero* candidates
   match — N-1 misses alongside a hit are expected, not noise.
3. **Dynamic surfacing (shape c) + nav-drop refinement.** A producer whose
   key field is non-literal and non-enumerable gets
   `Meta["key_dynamic"]="true"` and a ledger entry
   (`UnresolvedRef.Kind = "dynamic_<kind>"`: `dynamic_url`, `dynamic_topic`,
   `dynamic_queue`, `dynamic_channel`, `dynamic_event`). The nav-link `drop`
   policy is refined to its original rationale: **unmatched literal** nav
   links still drop (external links/typos are not app flows);
   **dynamic** nav links always reach the ledger. `key_dynamic` producers
   are the explicit upgrade targets for config resolution (F.3) and runtime
   evidence (R.1) — this meta is the join point those plans consume.
4. **Per-language walkers, shared shape — pinned interface.** One walker per
   pattern language — Go (if/else, switch, package consts), JS/TS/JSX
   (ternary, `||`/`??`, object lookup, template literals whose interpolations
   are all literal-resolvable; otherwise dynamic), Ruby (ternary, if/else,
   case, constants), templ (if/else around attributes; datastar action args)
   — emitting the same `key_candidates`/`key_dynamic` meta so the engine and
   every rule stay language-agnostic. Walkers register through a first-class
   registry mirroring `parser.Register`, so a language *without* a walker is
   a visible, reportable fact rather than a silent degradation:

   ```go
   // internal/contract/keywalk.go
   // KeyWalker enumerates literal alternatives for one producer key
   // expression in one language. Implementations honor the shared bounds
   // (≤8 branches, depth ≤2, all-literal) and never partially enumerate.
   type KeyWalker interface {
       Language() string // matches parser.Parser.Language()
       // WalkKey inspects the tree-sitter node holding a key-field value.
       // Returns (candidates, dynamic): len(candidates) ≥ 2 ⇒ emit
       // key_candidates meta; dynamic=true ⇒ emit key_dynamic meta +
       // ledger entry; (1 literal, false) ⇒ plain static key, no meta.
       WalkKey(node *sitter.Node, src []byte, consts ConstResolver) ([]string, bool)
   }

   // ConstResolver resolves same-service constant references (shape b).
   // Returns ("", false) for anything reassigned or non-literal.
   type ConstResolver func(name string) (string, bool)

   // RegisterKeyWalker wires a walker (from init()), keyed by Language().
   func RegisterKeyWalker(w KeyWalker)

   // KeyWalkerFor returns the walker for a language, or nil. Callers treat
   // nil as "literal-only recognition" — and doctor reports it (below).
   func KeyWalkerFor(lang string) KeyWalker
   ```

   HTML registers a no-op walker explicitly (attributes are static by
   nature) — explicit registration distinguishes "considered, not needed"
   from "forgotten". New languages (Tier L checklist item 7) register a
   walker as a hard checklist requirement; the doctor walker row (below)
   is the enforcement backstop when review misses it.

**Tests.** A language × kind fixture matrix — each supported language gets at
least: one ternary/branch nav-or-client case asserting one edge per branch
(`via=branch_enum`), one constant-resolved publish/enqueue case, one
genuinely-dynamic case asserting the `dynamic_<kind>` ledger entry (and for
nav: asserting it is *not* silently dropped). Negative fixtures: 9-branch
switch ⇒ dynamic (cap honored); reassigned constant ⇒ dynamic (no guessing);
literal-unmatched nav link ⇒ still dropped (policy preserved).

**Acceptance.** `<a href={isAdmin ? "/admin" : "/dashboard"}>` yields two
`navigates_to` edges to two route handlers; a Go `switch` selecting between
three topics yields three `publishes` edges; a computed fetch URL yields a
`dynamic_url` ledger entry visible in `status --unresolved`; per-kind doctor
coverage (G.5) gains a `dynamic` column so the surfaced-but-unlinked count is
tracked per kind. Doctor also gains a **walker-coverage row**: for every
language registered in the parser registry, report whether a `KeyWalker` is
registered (`yes` / `no-op` / **`MISSING`**) — a `MISSING` cell for a language
with producer patterns is a defect, and a test iterates both registries to
fail on it (the mechanical guard the checklist's process rule can't provide).

### Phase G.7 — Producer aliasing, instances & wrappers (all kinds, all languages) `done`

**Outcome (commit implementing this phase).** `EnrichAliases` runs between
`EnrichRouteGroups` and `Engine.Link`. It builds a per-service/per-file alias table
from `alias_name` and `instance_name` binding nodes produced by new YAML patterns
(`axios_create_with_baseurl`, `fetch_alias_binding`, `jquery_alias_binding`,
`axios_destructure`, `axios_method_binding`, `faraday_instance_binding`,
`resty_new_instance`). Alias/instance call nodes with `via_alias` meta are resolved
inline: base URL is prepended and `via=alias` is stamped. One-hop wrapper functions
(detected via `wrapper_for` meta on function nodes) are resolved by composing the
base prefix with the call-site argument; `via=wrapper` is stamped. All four ledger
kinds (`alias_reassigned`, `wrapper_depth`, `factory_dynamic`, `instance_unresolved`)
are emitted to `graph.UnresolvedRef`. The `via` meta propagates from resolved
producer nodes to emitted edges (`engine.matchProducer` now copies `prod.Meta["via"]`
to `edgeMeta`). A new `Indirect` column on `KindCoverage` counts edges resolved via
alias or wrapper; `polyflow doctor` surfaces it. `faraday_http_verb` now requires a
receiver capture (`via_alias`), tightening the previously over-broad Ruby match. The
dead `axios_create_instance` pattern (no-op capture) was removed and replaced by
`axios_create_with_baseurl`. No `graph.SchemaVersion` bump: alias binding nodes are
`NodeTypeVariable` (excluded from containment and calls edges in MatchToGraph) and
removed from the engine working copy by `EnrichAliases`; the new edge meta keys
(`via`, `instance_base_url`, etc.) are backward-compatible additions.



**Problem.** Every producer pattern matches the API's **canonical call shape
by name** — `fetch(...)`, `axios.get(...)`, `$.ajax(...)` — so any
indirection makes the producer invisible, usually with **no ledger entry**
(the worst failure mode). Verified loopholes:

- **Direct alias:** `var a = $.ajax; a({url: "/x"})` — matches nothing; at
  best a `call_ref` to the variable, never an `http_client`.
- **Destructured method:** `const { post } = axios; post("/x")` — nothing.
- **Instance creation:** `const api = axios.create({baseURL: "/api/v1"});
  api.get("/users")` — `axios_create_instance` *captures* the instance
  (`patterns/javascript/axios.yaml`) but the `axios_instance` role is
  **consumed nowhere** (dead capture, verified), and `axios_request`
  requires the literal identifier `axios` — instance calls are invisible.
  Same class: Ruby `Faraday.new(url:)`, Go `resty.New().SetBaseURL(...)`.
- **Wrapper functions:** `function api(path) { return fetch(BASE + path) }`
  — the inner fetch surfaces as `dynamic_url` (G.6) but the literal at the
  call site `api("/orders")` is never propagated: *linkable but unlinked*.
- **Returned callers / factories:** `function client() { return $.ajax }`,
  `makeClient(base)` returning a closure — invisible.

The same indirections apply to every producer kind: publishers
(`const send = producer.send`, a `publish_event(topic)` wrapper), job
enqueuers, pusher triggers — this phase is kind-agnostic by construction.

**Deliverable.**

1. **Alias table (per service, per language).** Reusing the
   variable/constant tracking machinery: single-assignment bindings whose
   value is (i) a producer function reference (`$.ajax`, `fetch`,
   `axios.post`, destructured), or (ii) a **producer instance** created by a
   registered instance idiom (`axios.create`, `Faraday.new`,
   `resty.New()` — each an `instance` pattern role with an optional
   `base_url`/config literal), map alias/instance name → (producer kind,
   base key prefix). Calls through a mapped name are re-matched against the
   original producer pattern shape, emitting standard producer nodes — the
   contract engine and rules are untouched; existing dead captures
   (`axios_instance`) become consumed. **Reassigned names → dynamic**:
   a name bound more than once is never resolved
   (`alias_reassigned` ledger), no guessing.
2. **One-hop wrapper resolution.** A function whose body contains exactly
   one producer call whose key derives from its parameters (identity or
   literal-concat, e.g. `BASE + path`) is marked a **producer wrapper**
   (meta on the function node). Each call site with literal (or
   G.6-enumerable) arguments projects to a producer node with the composed
   key (`via=wrapper`, confidence `inferred`); non-literal call-site args →
   `key_dynamic` on the call site. Depth capped at **one hop** —
   wrappers-of-wrappers and factory-returned closures → ledger
   (`wrapper_depth` / `factory_dynamic`), surfaced never guessed. Wrapper
   detection reuses the existing per-file scope attribution; cross-file
   wrapper calls resolve through imports (existing) and globals (L.W1).
3. **Ledger kinds** (extend `UnresolvedRef.Kind`): `alias_reassigned`,
   `wrapper_depth`, `factory_dynamic`, plus per-kind `instance_unresolved`
   (instance created with non-literal config). Doctor's G.5/G.6 coverage
   table gains an `indirect` column (resolved-via-alias/wrapper counts) so
   the win is measurable.
4. **Per-language instance/alias idiom patterns** (additive YAML, one file
   per idiom like today): JS/TS (`axios.create`, bare re-exports), Ruby
   (`Faraday.new`, `Net::HTTP.new`), Go (`resty.New`, `http.Client{}`
   with helper methods — Go's SSA pass already resolves most method values;
   reuse it, don't duplicate). New languages inherit the requirement via
   the Tier L checklist.

**Tests.** Per language: alias fixture (`var a = $.ajax` → cross-service
`http_call` asserted), destructuring fixture, instance fixture
(`axios.create({baseURL})` + `api.get` → linked with base prefix applied),
wrapper fixture (`api("/orders")` → composed-key edge `via=wrapper`),
publisher-alias fixture (kind-agnosticism proven). Negatives: reassigned
alias → ledger not link; two producer calls in one wrapper → not a wrapper
(ledger); factory closure → `factory_dynamic`.

**Acceptance.** The verified loopholes above all produce either a linked
cross-service edge or a named ledger entry — zero silent cases; the doctor
`indirect` column reports how many producers each repo reaches only through
aliases/wrappers (on chessleap: expected ~0; on the legacy-web eval repo:
expected substantial).

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

- **Not everything is a contract.** `LinkRouteHandlers` (route → handler-function
  name resolution by label), `LinkDatastores` (call-site→service store) and the JS
  import linker (within-service) are structural, not producer/consumer matching —
  keep them out of the engine (documented), don't force-fit.
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
                 ├─> G.4 ─> G.5   (each new kind = rules + fixture, no engine change)
                 └─> G.6 ─> G.7   (dynamic keys, then alias/instance/wrapper
                                   indirection; both widen automatically as G.4
                                   adds kinds — they key on meta fields/roles,
                                   not on rules. G.7's cross-file wrapper calls
                                   also benefit from L.W1 globals)
```
