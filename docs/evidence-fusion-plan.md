# Polyflow — Evidence-Fusion Architecture Plan

Status legend: `pending` · `in progress` · `done`

> **The capstone plan.** `docs/contract-matching-plan.md` gives *breadth* (link any
> cross-service convention) and `docs/versioning-matrix-plan.md` gives *fidelity*
> (behave correctly per tool version). Both operate on **static source only**, which
> is provably unable to be complete *and* correct. This plan adds the other **sources
> of truth** — declared contracts, runtime traces, config/IaC — and fuses them into
> one provenance-tracked graph. The contract-matching engine is reused as the **join
> layer** (all sources normalize to the same channel key).

## Context

**Why.** A complete-and-correct cross-service flow graph cannot come from static
analysis alone, and this is a hard limit, not an engineering gap:

- **Static-only cannot be *correct*.** Which branch runs, which interface
  implementation dispatches, what a computed URL/topic string resolves to — all
  undecidable in general (Rice's theorem). Cross-service edges are runtime strings, so
  a source-only graph is always partly a guess.
- **Runtime-only cannot be *complete*.** Observing execution only proves paths that
  actually ran; error/feature-flagged/rare/dead-but-reachable paths never appear.

Therefore neither side alone reaches the goal. **Fusing sources** does: static gives
*completeness* (every possible edge), runtime + contracts give *correctness* (which
are real, and resolve the ambiguous strings), reconciled with provenance.

**What "100%" rigorously means here** (a real target, not a slogan):
- **Complete** = the static superset — every declared/possible edge, nothing silently
  dropped.
- **Correct** = runtime- and/or contract-confirmed — which of those edges are real.
- **Reconciled** = every edge carries `sources[]` + `confidence` + `verification_state`,
  and the gap between "possible" and "confirmed" is tracked explicitly, never faked.

This yields provably-complete-relative-to-source and provably-correct-relative-to-
observed, with the delta labeled — which for AI-agent context retrieval is *better*
than a fake-certain graph, because the agent gets both "what can happen" and "what
does happen," labeled.

**Locked framing (carried from prior sessions):** recall over precision; no silent
gaps (unknowns surface); confidence/provenance on every edge; the residual
(genuinely-dynamic, never-observed, never-declared) is surfaced, never fabricated —
there is no setting that makes it knowable, and claiming otherwise would be dishonest.

Follows the repo per-phase process (`docs/phases.md`): one phase per commit,
positive+negative fixtures, benchmark, doc update, `graph.SchemaVersion` bump when
stored shape changes.

---

## Core model — the evidence graph

Every edge becomes multi-sourced:

```
Edge {
  from, to, kind, key            # channel identity (from the contract engine)
  sources: [ {provider, confidence, ref} ]   # static | contract | runtime | config | llm
  verification_state             # verified | candidate | observed_only_gap | conflicting
  confidence                     # derived from sources (see ladder)
}
```

- **verified** — static ∩ (runtime ∨ contract) agree → highest trust.
- **candidate** — static-only (or llm-only): possible, unconfirmed.
- **observed_only_gap** — runtime/contract shows an edge static missed → a *gap signal*
  that auto-proposes a new contract rule (self-improving loop).
- **conflicting** — sources disagree (e.g. runtime shows an edge static proved
  impossible) → surfaced as a reconciliation finding.

**Confidence ladder:** `verified` > `observed` (runtime) ≈ `declared` (contract) >
`inferred` (static normalized) > `candidate` (static-only / llm) > `unknown`.

**Evidence providers** are pluggable behind one interface so adding a source is
additive, never core surgery.

### Pinned Go surface (F.0 implements exactly this)

These signatures are the F.0 contract — every provider phase (F.1–F.3, the
runtime plan's R.1) and the reconciler build against them as written:

```go
// internal/evidence/provider.go
type Provider interface {
    Name() string // "static" | "contract" | "runtime" | "config" | "llm"
    // Collect returns evidence normalized to contract-engine channel keys.
    // A provider with nothing to say returns empty Evidence, not an error
    // (graceful degradation is the contract).
    Collect(ctx context.Context, ws *workspace.WorkspaceConfig) (Evidence, error)
}

type Evidence struct {
    Nodes      []graph.Node          // synthetic service-level endpoints allowed
    Edges      []graph.Edge          // Sources[] populated by the provider
    Unresolved []graph.UnresolvedRef // provider-specific ledger entries
}

// internal/graph/model.go additions (F.0 — SchemaVersion bump)
type SourceRef struct {
    Provider   string `json:"provider"`   // Provider.Name() value
    Confidence string `json:"confidence"` // ladder value (below)
    // Ref is provider-specific provenance:
    //   static:   "file.go:42"
    //   contract: "openapi.yaml#getGame"
    //   runtime:  "<session>/<trace_id>"
    //   config:   "k8s/deploy.yaml#env.API_URL"
    Ref        string `json:"ref,omitempty"`
    ObservedAt int64  `json:"observed_at,omitempty"` // runtime only, unix seconds
}

// graph.Edge gains exactly three fields:
//   Sources             []SourceRef `json:"sources,omitempty"`
//   VerificationState   string      `json:"verification_state,omitempty"`
//   VerifiedGranularity string      `json:"verified_granularity,omitempty"` // "channel" | "site"
// Back-compat: F.0 wraps today's pipeline so every existing edge carries a
// single static SourceRef; an edge with no Sources is a schema error, not a
// default.

// Verification states — pinned strings.
const (
    StateVerified        = "verified"
    StateCandidate       = "candidate"
    StateObservedOnlyGap = "observed_only_gap"
    StateConflicting     = "conflicting"
)

// Confidence ladder additions. The existing edge-confidence constants
// (static | inferred | partial | unknown) are UNCHANGED — note the naming
// collision: Confidence "static" (a literal string match) is unrelated to
// Provider "static"; do not conflate them.
const (
    ConfidenceObserved  = "observed"  // runtime evidence
    ConfidenceDeclared  = "declared"  // contract/IDL evidence
    ConfidenceCandidate = "candidate" // llm or static-only-unconfirmed
)
```

Each provider emits edges already normalized to the **same channel key** the
contract-matching engine produces — so fusion is a key-join, not per-source glue.

### Join granularity & node identity (what `verified` may claim)

Runtime and contract evidence is **channel-granular**, static evidence is
**call-site-granular**, and conflating them is the one way this plan could
manufacture misinformation:

- An OTel span or an OpenAPI operation confirms
  `(kind, key, from_service, to_service)` — it never identifies *which static call
  site* fired (spans rarely carry code-level attribution). Static edges, by
  contrast, name specific producer/consumer nodes.
- Therefore **`verified` is defined at channel granularity**: when evidence
  confirms a channel, every static edge on that channel becomes
  `verification_state=verified` with `verified_granularity=channel` stamped in
  meta. When three call sites in service A hit the same route and one span
  confirms the channel, all three are channel-verified — none may be read as
  "this specific call site ran." `verified_granularity=site` is set **only** when
  the evidence itself carries code-level attribution (e.g. `code.filepath` span
  attributes), never inferred.
- **Node identity for non-static sources:** providers emit service-level endpoint
  identities plus the channel key, not graph node IDs. Reconciliation resolves
  them to existing static nodes via the key-join; when no static node exists
  (`observed_only_gap`), the reconciler **mints synthetic service-level endpoint
  nodes** tagged with their source, so gap edges are traversable without
  fabricating call sites.
- **Staleness:** evidence inputs (trace dumps, specs, config) are hashed and
  invalidate independently of the per-file source cache; the reconcile join
  recomputes `verification_state` over the full edge set on every index run
  (O(edges)), so verification never goes stale against incremental re-indexing.

---

## Evidence sources (ranked by determinism)

1. **Declared contracts** — OpenAPI/Swagger, gRPC/protobuf IDL, GraphQL SDL,
   **AsyncAPI** (queues/events), Avro/JSON Schema. Turns the string coupling into a
   *typed* link: same proto service / same OpenAPI operation on both sides = a
   deterministic edge. One parser **per standard format**, not per framework.
2. **Runtime tracing** — OpenTelemetry (OTLP) spans + W3C `traceparent` propagation
   give actual parent→child causality across HTTP/gRPC/queues/DB, correct by
   construction. Optional **eBPF / service-mesh L7** (Cilium, Istio) for zero-
   instrumentation. Framework-agnostic — a span is a span regardless of gin/Rails/
   Spring, so **zero per-framework rules**. Fed from a **CI e2e run** so it needs no
   prod access.
3. **Config / IaC resolution** — env vars, k8s manifests, Terraform, mesh/gateway
   config hold the *actual* URLs/topics/queue names; resolve the dynamic strings
   instead of guessing.
4. **Static analysis (existing)** — the completeness skeleton + dead-path coverage,
   and the fallback where 1–3 are absent. Everything today becomes `source=static`.

---

## Phases (one commit each)

### Phase F.0 — Evidence-graph substrate + reconciliation join `done`

**Outcome.** Implemented exactly as specified. `graph.Edge` gained `Sources []SourceRef`, `VerificationState string`, `VerifiedGranularity string`; `SchemaVersion` bumped "14" → "15". *Post-commit review fixes:* gap-edge IDs now include the channel key (two declared-but-static-missed operations from one service previously collapsed into one gap edge — a silent gap drop); the anonymous side of a gap edge mints a deterministic `gap-endpoint:<kind>:<key>` node labeled with the channel key instead of an empty-ID node, and the indexer persists minted nodes so gap edges never dangle; verified edges are stamped `verified_granularity=channel` on every recompute (`graph.GranularityChannel`/`GranularitySite` constants added — `site` reserved for F.2 evidence with code-level attribution); the join is service-scoped — a spec declared by service A confirms a static edge only when either endpoint's service matches A (falling back to the unscoped join when service identity is unknown on both sides, recall over precision), and an unmatched declaration surfaces as A's gap rather than verifying an unrelated same-shaped edge. The SQLite schema adds three columns with DEFAULT values and a try-and-ignore `ALTER TABLE` migration for existing DBs (the bundled SQLite in `modernc.org/sqlite v1.37.1` does not support `ADD COLUMN IF NOT EXISTS`, so we trap the "duplicate column name" error instead). `internal/evidence/` contains `provider.go` (Provider interface + ValidateProviderName), `provenance.go` (StaticProvider — total recomputation, replaces Sources on every run), and `reconcile.go` (Reconciler with multi-valued channel-key join, computeState, deterministic sort). The indexer wires the reconciler after all linking passes and re-upserts reconciled edges. Tests cover: fan-out (≥2 edges sharing one channel key all stamped — not first-match), two-run determinism, state transitions (candidate/verified/observed_only_gap), unknown-provider rejection, and the chessleap static-baseline-unchanged guard (all contract golden edges identical, every edge now carries Sources[]). BenchmarkIndexCold: 10.15s for 1200 files (~8.5 ms/file), well within the documented target.
`internal/evidence/{provider.go,provenance.go,reconcile.go}`. Extend `graph.Edge` with
`sources[]` + `verification_state` + `verified_granularity` (channel | site; see the
join-granularity section — back-compat: existing edges → single `static` source). Reconciliation engine merges edges on `(kind, key)` / `(from, to)` reusing the
contract engine's channel-key normalization. Wrap today's static pipeline as the first
`EvidenceProvider` (`source=static`). No new sources yet — graph identical + provenance
stamped. *SchemaVersion bump.*

**Pinned reconciliation semantics (bug-class rules 1/2/5, `docs/phases.md`):**
- **Multi-valued join, both sides.** One channel confirmation stamps a source
  on **every** static edge sharing `(kind, key, from_service, to_service)` —
  never only the first found. N confirmations of one channel append N source
  refs to each such edge, deduped by `(provider, ref)`. Build the join index
  as `map[key][]*Edge`, never `map[key]*Edge` (the exact single-valued-index
  bug the contract engine shipped with).
- **Merge into existing edges, never duplicate.** A non-static source that
  matches an existing edge appends to `Sources[]` and recomputes
  `verification_state`; it must not mint a second edge with the same
  `(From, To, Type)`. Only `observed_only_gap` (no static edge at all) mints
  nodes/edges, and those get deterministic IDs derived from
  `(provider, kind, key, from, to)` — never a counter or map-order-dependent
  value.
- **Deterministic output.** The reconciler iterates edges in stored order;
  minted synthetic nodes/edges and each edge's `Sources[]` are sorted by a
  stable key before persisting. F.0's tests include a **two-run determinism
  test**: reconcile the same inputs twice, require byte-identical JSON export.
- **The static-baseline-unchanged guard is a real committed snapshot in the
  F.0 commit itself** (chessleap edge set before vs. after wrapping, identical
  apart from the added `Sources[]`/state fields) — not a deferred stub; the
  G.0 golden-harness breach is the precedent this forbids.
- **Unknown provider name** (anything outside the five pinned `Name()`
  values) → load/registration error, not silent acceptance.
- **State recomputation is total.** `verification_state` is recomputed over
  the full edge set every run from `Sources[]` alone — never incrementally
  patched — so a removed session/spec cannot leave a stale `verified`.

### Phase F.1 — Contract-ingestion source `done`
*Post-commit review fix:* spec discovery honors the indexer's exclude set
(`index.exclude` + `.polyflowignore`), so vendored third-party specs under
`node_modules`/`vendor` can no longer mint false contract edges or phantom gaps.
`internal/evidence/contract_ingest/{openapi.go,protobuf.go,graphql.go,asyncapi.go}`.
Parse standard IDL/spec files (discovered via workspace globs) into producer/consumer
nodes+edges tagged `source=contract`, normalized to channel keys. Deterministic links;
`verified` when static agrees, `observed_only_gap` when only the contract knows. One
fixture per format (2-service). Highest determinism-per-effort.

**Pinned key-normalization trap (the join fails silently without this):**
spec path-parameter syntax differs from framework syntax — OpenAPI writes
`/games/{gameID}`, gin writes `/games/:gameID`, Rails writes `/games/:id`.
Every spec-derived key must pass through the **same contract-engine
normalizer chain** static keys use (`param_wildcard` must map `{x}` as well
as `:x` to `*`; extend the normalizer if it only handles `:x` today — check,
don't assume). Worked example: OpenAPI operation `GET /games/{gameID}` and
static handler key `GET /games/:gameID` must both normalize to
`GET /games/*` or the `verified` flip never happens and no test screams.
Required test: one fixture where the spec uses `{param}` syntax and the
static edge uses `:param`, asserting the `verified` flip. Also apply
bug-class rule 3 (`docs/phases.md`): spec constructs parsed but not mapped
(e.g. OpenAPI `servers:` variables, callbacks, webhooks) are ledgered with a
named reason, never silently skipped.

### Phase F.2 — Runtime trace-ingestion source + capture sessions `pending`
**Expanded into its own plan: `docs/runtime-flow-plan.md` (phases R.0–R.5).**
Summary: OTLP is the single intake seam (file ingest + an embedded OTLP receiver);
capture is session-based — `polyflow capture start/stop` for **partial** captures
(the user exercises just the flows they care about) and `capture run -- <cmd>` for
**full** e2e captures — with sessions as additive union (a partial capture can never
downgrade an edge). Span→channel-key mapping, SSE connection edges, async span-link
causality, the ingest ledger, and per-stack instrumentation recipes are all specified
there. Fixture: a captured trace JSON → edges, incl. one **observed-only** edge
static missed.

### Phase F.3 — Config/IaC resolution source `pending`
`internal/evidence/config_resolve/{env.go,k8s.go,terraform.go}`. Resolve dynamic
endpoint/topic/queue strings from config → *upgrades* static `candidate`/`unknown`
edges to resolved keys (`source=config`). Turns `@post(url)` /
`publish(cfg.Topic)` from unresolved into linked. The upgrade targets are
exactly the producers contract-matching G.6 stamps `key_dynamic=true` (with
`dynamic_<kind>` ledger entries) — resolve those first; a resolved producer
clears its ledger entry.

**Pinned resolution semantics:** (a) values read from YAML/env/HCL are raw
source text — strip surrounding quotes and whitespace before key
construction (bug-class rule 6; the quoted-prefix incident applies verbatim
to config values); (b) when one variable resolves to **different values per
environment/overlay** (dev vs prod k8s overlays, multiple tfvars), emit a
candidate upgrade for **each** value (fan-out, rule 1) tagged with its
config source ref — never pick one environment silently; (c) an env var
referenced in code but defined nowhere in the scanned config stays in the
`dynamic_<kind>` ledger with reason `config_not_found` — absence of config
is not license to guess.

### Phase F.4 — Fusion, reconciliation report + doctor coverage `pending`
`reconcile.go` finalizes `verification_state` across all providers; conflicts surfaced.
`polyflow doctor` / `reconcile` prints: % verified, per-kind coverage, the
`candidate` (static-only, unconfirmed) list, and the `observed_only_gap` list — which
**auto-generates candidate contract rules** (the self-improving loop). Merges with the
contract-matching G.5 and versioning V.4 coverage tables.

**Determinism (rule 2):** report rows, gap lists, and auto-generated rule
files are sorted by stable keys (kind, key, service) — counts and file
contents must be byte-identical across two runs on the same inputs
(two-run test required). Auto-proposed rule filenames derive from the
cluster key, never from an emission counter.

### Phase F.5 — LLM last-mile proposer (optional, guardrailed) `pending`
`internal/evidence/llm/proposer.go`. For residual `UnresolvedRef`s only, an LLM
proposes candidate edges — always emitted `source=llm, verification_state=candidate`,
never authoritative, always requiring a second source to become `verified`. Off by
default; bounded, auditable.

---

## Key files

- **New:** `internal/evidence/` (provider, provenance, reconcile),
  `internal/evidence/contract_ingest/` (openapi/protobuf/graphql/asyncapi),
  `internal/evidence/trace_ingest/` (otlp/span_map/ebpf),
  `internal/evidence/config_resolve/` (env/k8s/terraform),
  `internal/evidence/llm/` (proposer), `polyflow capture` + `polyflow reconcile`
  command files, `testdata/evidence/<source>/` fixtures.
- **Modify:** `internal/graph/model.go` (`Edge.Sources`, `Edge.VerificationState`,
  extended confidence constants; `SchemaVersion`), `internal/indexer/indexer.go`
  (run providers → reconcile after the static pass), `internal/contract/engine.go`
  (expose channel-key normalization as the shared join key),
  `internal/workspace/` (evidence config: trace source, contract globs, toggles).

## Reuse (don't rebuild)

- **Contract-matching engine** (`internal/contract/`) — its channel-key normalization
  is the join: OTel spans, OpenAPI operations, and static call sites all normalize to
  the *same* key, so all sources reconcile in one matcher. F.0 depends on it.
- `graph.UnresolvedRef` ledger — the resolve-or-surface substrate; providers add to it.
- Confidence constants (`internal/graph/model.go`) — extend the ladder, don't fork it.
- `deps.Resolve` / workspace config — discovery of contract files, trace endpoints.
- `patterns.VersionInRange` — contract/pattern rule gating still applies to the static
  provider (the versioning plan layers on unchanged).

## Verification

- **Static baseline unchanged:** wrapping today's pipeline as `source=static` yields an
  identical graph (regression) + provenance stamped.
- **Contract source:** a 2-service OpenAPI fixture → deterministic `source=contract`
  edges; the matching static edge flips to `verified`; a contract-declared-but-
  static-missed edge appears as `observed_only_gap`.
- **Runtime source:** feed a captured OTel trace JSON fixture → `source=runtime` edges;
  an observed edge static missed surfaces as a gap + an auto-proposed candidate rule.
- **Chessleap reconciliation:** report static-only vs verified counts; the 24 unresolved
  datastar actions flip to `verified` (channel-granular) if a trace/contract confirms
  their channels, or stay `candidate` (surfaced, not dropped). Assert a multi-call-site
  channel confirmed by one span marks all its edges `verified_granularity=channel`,
  never `site`.
- **Degradation:** a repo with no contracts/traces → providers no-op, graph == static
  (no regression, just no upgrade).
- **Benchmark:** contract/trace ingestion is O(spec/span count); reconciliation is a
  key-join O(edges); hold chessleap index time + `BenchmarkIndexCold`.

## Risks / honest boundaries

- **Requires observability or contracts.** Runtime needs a runnable/observable env
  (CI e2e with OTel) or a collector; contract ingestion needs the specs to exist. With
  neither, the graph degrades gracefully to static — no false completeness.
- **Runtime is partial by nature** (sampling, untested paths) — completeness still
  comes from static; runtime only confirms/corrects. Never treat absence of a span as
  absence of an edge.
- **Traces may carry PII/secrets** — ingest keys/topology only, redact payloads;
  document the data boundary.
- **LLM is candidate-only, never authority** — it cannot be a correctness oracle
  (hallucination); guardrailed to residuals, always second-source-verified.
- **Irreducible residual.** Genuinely-dynamic + never-observed + never-declared edges
  cannot be known by any means; they are surfaced as `candidate`/`unresolved`, never
  fabricated. "100%" is always **relative to available evidence** — with full OTel +
  contracts it is effectively complete+correct for exercised/declared flows.

## Relationship to the other plans

- **contract-matching** (`docs/contract-matching-plan.md`) — the **join/normalization
  substrate**; F.0 depends on it. Every provider emits its channel key.
- **versioning-matrix** (`docs/versioning-matrix-plan.md`) — *fidelity* of the **static**
  provider only; orthogonal to fusion, layers on unchanged.
- **evidence-fusion** (this) — adds the non-static sources of truth; the capstone that
  makes the graph complete *and* correct.

## Sequencing

```
contract-matching:  G.0 ─> G.1 ─> … ─> G.5   (join layer must exist first)
                       │
evidence-fusion:       └─> F.0 ─> F.1 (contracts) ─> F.2 (runtime) ─> F.3 (config)
                                                   └────────────────> F.4 (fusion/report) ─> F.5 (llm, optional)
versioning-matrix:  V.0 ─> V.1 …            (independent; fidelity of the static source)
```
- **F.0 depends on the contract engine** (shared channel key).
- **F.1–F.3 are independent sources** — land in any order; each is additive, gated on/off
  per workspace. **F.2 expands to phases R.0–R.5** in `docs/runtime-flow-plan.md`.
- **F.4 needs ≥2 sources** to show verified/gap deltas; **F.5 is optional.**
