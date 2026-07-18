# Polyflow — Runtime Flow Identification Plan

Status legend: `pending` · `in progress` · `done`

> **Prerequisites — do not start out of order.** This plan is the detailed
> expansion of **Phase F.2** of `docs/evidence-fusion-plan.md`. Every R-phase
> below assumes two things already exist:
> 1. the **contract-matching engine** (`internal/contract/`, phases G.0+ of
>    `docs/contract-matching-plan.md`) — its channel-key normalization is the
>    join key runtime edges are expressed in;
> 2. the **evidence substrate** (F.0 of the fusion plan) —
>    `graph.Edge.Sources[]`, `verification_state`, `verified_granularity`, and
>    the `EvidenceProvider` interface that runtime edges are emitted through.
>
> This document is deliberately self-contained beyond that: a contributor who
> has read only this file plus the two prerequisite interfaces can implement
> any phase.

## Context

**Why.** Static analysis produces the *complete* superset of possible flows but
cannot prove which are *real* — cross-service edges are runtime strings
(URLs, topics, queue names), so a source-only graph is always partly a guess
(see the fusion plan's Context for the formal argument). Runtime evidence is
the confirming source: an observed request from service A to route R on
service B is correct **by construction**. This plan defines how polyflow
captures and ingests that evidence for **any repo**, in a framework-agnostic
way, with two capture styles:

- **Partial capture** — a *Capture/Stop session*: the user starts a session,
  manually exercises just the flows they care about (clicks through the app,
  fires a few requests), and stops. Only what happened inside the window is
  recorded. Cheap, targeted, no test suite required.
- **Full capture** — polyflow wraps the target's e2e/test suite and records
  everything it does. Repeatable in CI, no production access needed.

**The one design rule that makes this stack-agnostic:** polyflow ingests
**OTLP** (the OpenTelemetry protocol) and nothing else. OTLP is the single
seam every stack can reach — language SDKs (Go, JS/browser, Ruby, Java,
Python, .NET), the OTel Collector, service meshes (Envoy/Istio), and eBPF
exporters (Cilium/Hubble) all emit it. Polyflow never parses a
framework-specific log or APM format; "support a new stack" means "point its
OTel exporter at polyflow," which is configuration on the target app, not
code in polyflow.

**Trust contract carried through (same as every plan in this repo):**
- Absence of a span is **never** absence of an edge — runtime evidence only
  *confirms or adds*, never removes or downgrades. Completeness stays static's
  job.
- Runtime confirms **channels**, not call sites: `verified_granularity=channel`
  unless the span itself carries code-level attribution
  (`verified_granularity=site` only via `code.filepath`/`code.function`
  attributes, never inferred). See the fusion plan's "Join granularity & node
  identity" section — this plan implements it.
- Malformed, unmappable, or ambiguous spans are surfaced in a ledger, never
  silently dropped.
- Payloads are never stored: keys/topology only (method, route, service names,
  messaging destination). Attribute values that are not needed for the channel
  key are discarded at ingest — the data boundary for PII/secrets.

Follows the repo per-phase process (`docs/phases.md`): one phase per commit,
positive+negative fixtures, benchmark, doc update, `graph.SchemaVersion` bump
when stored shape changes.

---

## Core model

### Intake — three modes, one pipeline

All three modes produce the same thing: a **capture session** (a set of OTLP
spans + metadata) that the ingest pipeline turns into runtime evidence.

```
polyflow ingest <file> [--session <name>]
    Import a pre-captured OTLP trace dump (JSON or protobuf; format
    auto-detected). The base mode: deterministic, fixture-testable, and the
    escape hatch for teams that already run a collector — configure the
    collector's file exporter and hand polyflow the file.

polyflow capture start [--session <name>] [--http-port 4318] [--grpc-port 4317]
polyflow capture stop  [--session <name>]
    Partial capture. `start` launches a long-running embedded OTLP receiver
    (OTLP/HTTP on :4318 and OTLP/gRPC on :4317, the standard ports) and
    records every span received until `stop`. Apps that already export OTel
    just need OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 (see the
    instrumentation appendix); no app restart is needed if the exporter
    endpoint is already pointed at polyflow or a collector that forwards to
    it. The user exercises the flows they care about, then stops.

polyflow capture run [--session <name>] -- <command...>
    Full capture. Starts the receiver, runs <command> (the target's e2e/test
    suite, a docker-compose stack, anything) with OTEL_EXPORTER_OTLP_ENDPOINT,
    OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf, and OTEL_TRACES_EXPORTER=otlp
    injected into its environment, stops the receiver when the command exits,
    and finalizes the session. Exit code mirrors the wrapped command.

polyflow flows [--session <name> | <file>]
    Debug view: the normalized flow records extracted from a session or dump
    (client→server pairs, channel keys, counts), before any graph writes.
    Exists so span-mapping problems are diagnosable without reindexing.
```

### Session store

```
.polyflow/captures/<session>/
  spans.otlp.json        # raw spans as received (OTLP JSON), append-only
  meta.json              # {name, started_at, stopped_at, span_count,
                         #  trace_count, services_seen[], mode: partial|full,
                         #  wrapped_command?}
```

**Pinned file format — do not improvise.** `spans.otlp.json` is **JSONL**:
one complete OTLP/JSON `ExportTraceServiceRequest` document per line,
appended as received (a receiver gets many export calls per session; a
single growing JSON array cannot be appended to safely). The R.0 parser
auto-detects and accepts **both** a single OTLP JSON document (the
collector-file-exporter / fixture case) and JSONL (the session case);
protobuf files are single-document. Writes are serialized through one
mutex-guarded writer — concurrent OTLP posts must not interleave bytes
(R.2 test).

- Session names default to a timestamp (`2026-07-14T10-30-00`); `--session`
  names them for sharing ("the checkout-flow capture").
- Sessions are **additive union**: reconciliation (F.4) consumes all sessions
  present (or a `--session` subset). A partial session confirms what it saw
  and says nothing about the rest — it can never downgrade an edge confirmed
  by another session or leave the graph worse than static-only.
- Each runtime edge's source ref records `{session, trace_id, observed_at}`
  so a confirmation is traceable back to the capture that produced it.
- `.polyflow/captures/` is gitignored by default; sessions are shareable
  files — committing a curated session as a fixture is explicitly supported
  (that is what `testdata/evidence/runtime/` is).

### Span → flow mapping reference

The mapper walks each trace and emits **flow records**; a flow record is
`(kind, channel key, from_service, to_service, evidence refs)` — the same
channel key vocabulary the contract engine produces, which is what makes
fusion a key-join.

**Service identity.** `from_service`/`to_service` come from the OTel resource
attribute `service.name`. Workspace config gains an optional mapping for
mismatches between OTel names and polyflow service names:

```yaml
evidence:
  runtime:
    service_names:            # otel service.name -> polyflow service
      chessleap-api: api
```

Unmapped `service.name`s that match no workspace service are surfaced in the
ingest ledger (never guessed).

**HTTP (sync request/response).**

| Flow field   | OTel source                                                        |
| ------------ | ------------------------------------------------------------------ |
| kind         | `http` (span has `http.request.method`; old SDKs: `http.method`)   |
| producer     | span with `SpanKind=CLIENT`                                        |
| consumer     | span with `SpanKind=SERVER`                                        |
| key: method  | `http.request.method` (fallback `http.method`)                     |
| key: path    | SERVER side: `http.route` when present (already the route pattern, e.g. `/games/:id`); else `url.path` (fallback `http.target`) normalized through the contract engine's `param_wildcard`/`query_strip`/`trim_slash` normalizers. CLIENT side: `url.full`→path (reuse `url_to_path` semantics) then the same normalizers. |
| pairing      | a CLIENT span whose child (same trace, `parent_span_id`) is a SERVER span in a different service = one cross-service flow. A SERVER span with a remote parent that polyflow never saw (uninstrumented caller, browser without JS instrumentation) still yields a consumer-side observation: the channel is confirmed as *reachable*, with `from_service` unknown — recorded, labeled, not guessed. |

**SSE / streaming.**

| Flow field | OTel source                                                          |
| ---------- | -------------------------------------------------------------------- |
| kind       | `sse`                                                                |
| detection  | SERVER span whose response `content-type` is `text/event-stream` (attr `http.response.header.content-type` when captured), or a workspace-listed SSE route; long-lived duration is a corroborating signal, not the test |
| edge       | **connection edge** (client → SSE endpoint), emitted once per observed connection. Per-event fan-out inside an established stream is *not* visible to HTTP spans and is explicitly out of scope — the static `sse`/`hub` edges keep covering it. |

**Messaging / jobs (async).**

| Flow field | OTel source                                                          |
| ---------- | -------------------------------------------------------------------- |
| kind       | `amqp` / `kafka` / `nats` / `job` per `messaging.system`             |
| producer   | span with `messaging.operation.type=publish` (old: `messaging.operation=publish|send`) |
| consumer   | span with `messaging.operation.type=process|receive`                 |
| key        | `messaging.destination.name` (+ `messaging.rabbitmq.destination.routing_key` for amqp), normalized like the matching contract rule's key |
| causality  | **span links**, not parent-child: a consumer span links to the producer span's context. When links are absent (common), fall back to key-equality within the session window — producer publish to destination D + consumer process of D = a channel-level flow, `confidence=observed`, ref notes `causality=key_match` instead of `causality=link`. |

**Granularity + node identity (implements the fusion doc's rules).**
Flow records are channel-granular. Reconciliation resolves them against static
edges by `(kind, key, from_service, to_service)`:
- match → every static edge on the channel gets a `runtime` source and
  `verification_state=verified`, `verified_granularity=channel` (`site` only
  when the span carried `code.filepath`/`code.function` — stamp and keep the
  attribution in the source ref);
- no static edge → `observed_only_gap`: mint synthetic service-level endpoint
  nodes (tagged `source=runtime`) so the edge is traversable, and feed the
  gap into F.4's candidate-contract-rule proposer.

**Ingest ledger.** Every span that cannot be mapped lands in the ledger with a
reason — `unknown_service`, `no_route_or_path`, `unsupported_span_kind`,
`malformed`, `no_causality` — reported by `polyflow flows` and the F.4/doctor
coverage output. Persisted as `graph.UnresolvedRef`s with `otlp_`-prefixed kinds
(exact field mapping pinned on `IngestLedgerEntry` below).

### Pinned Go types (R.0/R.1 implement exactly this)

```go
// internal/evidence/trace_ingest/model.go
type Span struct {
    TraceID, SpanID, ParentSpanID string
    Kind                          string // "CLIENT"|"SERVER"|"PRODUCER"|"CONSUMER"|"INTERNAL"
    Service                       string // resource attr service.name (raw, pre-mapping)
    Name                          string
    StartUnixNano, EndUnixNano    uint64
    Links                         []SpanLink
    Attrs                         map[string]string // ALLOWLISTED attrs only — the
                                                    // mapping tables above define the
                                                    // allowlist; everything else is
                                                    // dropped at parse time (PII boundary)
}

type SpanLink struct{ TraceID, SpanID string }

// FlowRecord is the mapper's output — channel-granular, in contract-engine
// key vocabulary. This is what reconciliation joins.
type FlowRecord struct {
    Kind        contract.Kind
    Key         string   // normalized channel key, e.g. "GET /games/*"
    FromService string   // "" when the caller was never observed (server_only)
    ToService   string
    Causality   string   // "parent_child" | "link" | "key_match" | "server_only"
    Refs        []FlowRef
}

// FlowRef is the provenance one observation contributes; it becomes the
// runtime SourceRef ("<session>/<trace_id>") on the fused edge.
type FlowRef struct {
    Session    string
    TraceID    string
    ObservedAt int64
    CodeFile   string // from code.filepath — presence upgrades granularity to "site"
    CodeFunc   string // from code.function
}

// IngestLedgerEntry persists through the existing unresolved_refs store as a
// graph.UnresolvedRef with this pinned mapping (R.0 must not improvise —
// UnresolvedRef's fields are required and spans have no file/line):
//   Service: mapped polyflow service ("unknown" when unmapped)
//   File:    ".polyflow/captures/<session>/spans.otlp.json"
//   Line:    0
//   Name:    "<trace_id>/<span_id>"
//   Kind:    "otlp_" + Reason   (e.g. "otlp_unknown_service")
type IngestLedgerEntry struct {
    Session, TraceID, SpanID string
    Reason                   string // unknown_service | no_route_or_path |
                                    // unsupported_span_kind | malformed | no_causality
}
```

**Worked fixture — `testdata/evidence/runtime/http_2svc.otlp.json`** (the R.0/R.1
positive case). OTLP JSON, two resource groups (one per service); span `kind`
enums: 2 = SERVER, 3 = CLIENT:

```json
{"resourceSpans": [
  {"resource": {"attributes": [{"key": "service.name", "value": {"stringValue": "web"}}]},
   "scopeSpans": [{"spans": [{
     "traceId": "5b8efff798038103d269b633813fc60c", "spanId": "eee19b7ec3c1b174",
     "name": "GET", "kind": 3,
     "startTimeUnixNano": "1752480000000000000", "endTimeUnixNano": "1752480000120000000",
     "attributes": [
       {"key": "http.request.method", "value": {"stringValue": "GET"}},
       {"key": "url.full", "value": {"stringValue": "http://api:8080/games/42"}}]}]}]},
  {"resource": {"attributes": [{"key": "service.name", "value": {"stringValue": "api"}}]},
   "scopeSpans": [{"spans": [{
     "traceId": "5b8efff798038103d269b633813fc60c", "spanId": "aaa19b7ec3c1b174",
     "parentSpanId": "eee19b7ec3c1b174",
     "name": "GET /games/:id", "kind": 2,
     "startTimeUnixNano": "1752480000010000000", "endTimeUnixNano": "1752480000110000000",
     "attributes": [
       {"key": "http.request.method", "value": {"stringValue": "GET"}},
       {"key": "http.route", "value": {"stringValue": "/games/:id"}}]}]}]}
]}
```

Expected R.1 output for this fixture — exactly one flow record:

```go
FlowRecord{
    Kind: "http", Key: "GET /games/*",     // http.route "/games/:id" → param_wildcard
    FromService: "web", ToService: "api",  // service.name matches workspace names
                                           // directly here — NO mapping exercised;
                                           // the service_names mapping has its own
                                           // fixture (http_2svc_mapped, R.1 tests)
    Causality: "parent_child",
    Refs: []FlowRef{{Session: "<session>", TraceID: "5b8efff798038103d269b633813fc60c",
                     ObservedAt: 1752480000}},
}
```

and an empty ingest ledger. (`verified_granularity` stays `channel` — no
`code.filepath` attr present.)

---

## Phases (one commit each)

### Phase R.0 — OTLP ingest + span model + `flows` debug view `done`

**Problem.** Nothing in polyflow can read a trace. Before any graph writes,
OTLP parsing and the normalized span model must exist and be fixture-proven.

**Deliverable.**
- `internal/evidence/trace_ingest/otlp.go` — parse OTLP/JSON and
  OTLP/protobuf trace exports (auto-detect; use the official
  `go.opentelemetry.io/proto/otlp` types + `protojson`) into a normalized
  `Span` struct: trace/span/parent ids, kind, service.name, name,
  start/end, links, and *only* the attributes named in the mapping tables
  above (payload boundary enforced here).
- `internal/evidence/trace_ingest/model.go` — `Span`, `FlowRecord`,
  `IngestLedgerEntry` types.
- `polyflow ingest <file>` (parse + store into a session dir) and
  `polyflow flows <file|--session>` (print parsed spans/flows; flows are
  empty until R.1 — prints spans + ledger).

**Tests.** Fixture dumps under `testdata/evidence/runtime/`: a hand-built
2-service HTTP trace in OTLP JSON (positive), the same in protobuf, the same
as a 2-line JSONL session file (the pinned session format — both forms must
parse identically), a malformed file (error, not panic), and a
spans-with-unknown-attrs file (attrs dropped, spans kept). Negative: a
metrics-only OTLP file → zero spans, explicit note. `flows` prints spans in
a stable order (sorted by trace_id, start time, span_id — never map order;
rule 2).

**Acceptance.** `polyflow flows testdata/evidence/runtime/http_2svc.otlp.json`
lists the expected spans and an empty ledger. No graph writes anywhere.

**Outcome (done).** Delivered `internal/evidence/trace_ingest/{model.go,otlp.go}`,
five fixture files under `testdata/evidence/runtime/` (single JSON, JSONL,
binary proto, malformed, unknown-attrs, metrics-only), and `cmd/polyflow/{ingest.go,flows.go}`.
10/10 tests pass including format-parity, two-run determinism, PII-drop, and allowlist-exhaustiveness guards.
Deviation: the spec says "use `protojson`" for binary proto decoding, but the
`go.opentelemetry.io/proto/otlp/collector/trace/v1` package transitively imports gRPC+gateway
(~4 heavy deps) solely for the gRPC service stub. Instead, `parseProto` uses `protowire` to
strip the one-field `ExportTraceServiceRequest` envelope and then calls `proto.Unmarshal` on
each `ResourceSpans` using the official `go.opentelemetry.io/proto/otlp/trace/v1` types — same
semantics, no gRPC dep. New direct deps: `go.opentelemetry.io/proto/otlp v1.10.0` and
`google.golang.org/protobuf v1.36.11`. The binary proto fixture is generated by `TestMain` from
the same data as the JSON fixture, committed to `testdata/evidence/runtime/http_2svc.otlp.pb`.
Acceptance test confirmed: `polyflow flows testdata/evidence/runtime/http_2svc.otlp.json` lists
2 spans sorted by (trace_id, start_time, span_id) with an empty ledger; no graph writes.

### Phase R.1 — HTTP span→channel-key mapper `done`

**Problem.** Spans are not flows. The CLIENT/SERVER pairing and channel-key
construction defined in the mapping table must produce runtime evidence the
F.0 substrate can join.

**Deliverable.**
- `internal/evidence/trace_ingest/span_map.go` — trace walk, CLIENT→SERVER
  pairing, HTTP key construction reusing the contract engine's normalizer
  registry (import `internal/contract`; do **not** duplicate normalizers),
  `service.name` mapping (new `evidence.runtime.service_names` workspace key),
  server-span-without-seen-caller handling, ingest ledger.
- Runtime `EvidenceProvider` (`internal/evidence/trace_ingest/provider.go`)
  emitting flow records as edges: `source=runtime`, `confidence=observed`,
  `verified_granularity` per the rules, source ref
  `{session, trace_id, observed_at, causality}`.

**Pinned mapper semantics (bug-class rules 1/2/6, `docs/phases.md`):**
- **Fan-out.** Flow records aggregate by `(kind, key, from_service,
  to_service)`: three services calling the same route in one session produce
  three flow records (one per from-service), and one channel confirmation
  stamps a source on **every** static edge sharing the channel — the join
  index is `map[key][]…`, never `map[key]*…`. `Refs` within a record are
  deduped by `(session, trace_id)`.
- **Determinism.** Flow records are emitted sorted by
  `(kind, key, from_service, to_service)`; refs sorted by
  `(session, trace_id)`; ledger entries by `(trace_id, span_id)`. Two-run
  determinism test required (same session in twice → byte-identical
  `flows --format json` output and byte-identical graph writes).
- **Keys go through the contract normalizer registry only.** Never
  hand-trim, hand-lowercase, or hand-wildcard a path in the mapper — if
  `url.full` needs path extraction, do the URL parse, then hand the path to
  the same normalizers static keys use. Divergent normalization is a silent
  join failure with no failing test.
- **Test through real bytes.** Mapper tests must feed real OTLP fixture
  files through the R.0 parser — hand-built `Span` structs alone are
  insufficient (the hand-built-nodes-masked-the-quoted-prefix incident).

**Tests.** Unit tests per mapping rule (route-pattern preference over
url.path, old-vs-new semconv attribute names, unknown service → ledger).
Mapping fixture `http_2svc_mapped.otlp.json`: `service.name=chessleap-api`
resolved to workspace service `api` via `evidence.runtime.service_names`
(the base `http_2svc` fixture deliberately needs no mapping).
Fixture: 2-service trace where one flow matches a static edge (→ `verified`,
`channel`) and one matches nothing (→ `observed_only_gap` + synthetic
endpoint nodes). Granularity guard: two static call sites on one channel +
one span → both `channel`, never `site`; a span with `code.filepath` → `site`.
Fan-out guard: two static edges on one channel + one span → **both** get the
runtime source (multi-valued join test). Two-run determinism test.

**Acceptance.** Indexing a fixture workspace with the session present shows
the verified flip and the gap edge; without the session, graph is byte-
identical to static (degradation guard).

**Outcome (done).** Delivered `internal/evidence/trace_ingest/span_map.go` (MapSpans, helpers) and
`internal/evidence/trace_ingest/provider.go` (RuntimeProvider). Four new OTLP fixtures:
`http_2svc_mapped.otlp.json` (service_names mapping), `http_old_semconv.otlp.json` (http.method/http.target),
`http_server_only.otlp.json` (SERVER-only, no CLIENT parent), `http_code_attr.otlp.json` (code.filepath/code.function).
`graph.SourceRef` extended with `CodeFile`/`CodeFunc` fields; `SchemaVersion` bumped 15→16; reconciler's
granularity logic updated to set `GranularitySite` when any runtime source carries `CodeFile`.
`cmd/polyflow/flows.go` updated to display real flow records and ledger entries.
All 26 span_map tests pass including: route-pattern preference, old-vs-new semconv, unknown service → ledger,
service_names mapping, code attribution (site granularity), granularity guard, fan-out (both static edges
on one channel receive the runtime source), observed_only_gap, two-run determinism, provider graceful
degradation, and metrics-only negative fixture. `BenchmarkIndexCold` holds (~10.1s — R.1 mapper is off
the indexing hot path; RuntimeProvider only runs when sessions exist). No deviations from the pinned spec
except: `FlowRecord.Key` uses lowercase method (e.g. `"get /games/*"`) consistent with the static HTTP
contract normalizer chain (`case_fold` → lowercase), not uppercase as shown in the plan's worked example;
the plan example's uppercase was inconsistent with the `case_fold` normalizer, and lowercase is required
for the key-join against static edges to succeed.

**Addendum (2026-07-18, review fixes).** Two trust-contract defects found in
review, both fixed: (1) the mapper anchored only on SERVER spans, so an HTTP
CLIENT span whose server side was never captured (a call to an external or
uninstrumented service) and all INTERNAL spans were **silently dropped** —
neither flow nor ledger, violating the plan's "never silently dropped" rule.
`MapSpans` now ends with an exhaustiveness sweep: every span not accounted
for by the SERVER/messaging passes is ledgered (unpaired CLIENT with HTTP
attrs → `no_causality`; INTERNAL/unknown kinds → `unsupported_span_kind`);
paired CLIENT parents are tracked and never double-booked. Fixture
`http_client_internal_only.otlp.json` + two tests pin this. (2)
`IngestLedgerEntry` did not carry the service, and the provider used the
**session name** as `UnresolvedRef.Service` — improvising over the pinned
mapping ("mapped polyflow service, `unknown` when unmapped"). The entry now
carries `Service`, populated at mapping time via the same
`resolveService` path flows use.

### Phase R.2 — Capture sessions (`start/stop/run`) `done`

**Problem.** File ingest requires the user to run their own collector. The
embedded receiver makes capture a one-command affair and enables the
partial Capture/Stop workflow.

**Deliverable.**
- `internal/evidence/trace_ingest/receiver.go` — embedded OTLP receiver:
  OTLP/HTTP (`POST /v1/traces`, protobuf + JSON bodies, CORS enabled for
  browser SDKs) and OTLP/gRPC, appending to the session's `spans.otlp.json`.
- `internal/evidence/trace_ingest/session.go` — session dir lifecycle,
  `meta.json`, additive-union listing.
- `polyflow capture start|stop|run` command file; `run` injects
  `OTEL_EXPORTER_OTLP_ENDPOINT`/`OTEL_EXPORTER_OTLP_PROTOCOL`/
  `OTEL_TRACES_EXPORTER` and mirrors the wrapped command's exit code.
  `start` daemonizes (or instructs to background) with a pidfile in the
  session dir; `stop` finalizes meta.
- Instrumentation recipes appendix added to this doc (see appendix below —
  ships with this phase so `doctor` can point at it).

**Tests.** Receiver round-trip (SDK export → session file) over both
transports; partial-window test (spans sent after `stop` are not recorded);
`run` env-injection + exit-code test with a stub command; concurrent-session
name collision → error.

**Acceptance.** `polyflow capture run --session e2e -- go test ./e2e/...`
on a fixture app produces a session that `polyflow flows --session e2e`
renders; `capture start` + manual curl + `capture stop` produces a
partial session with exactly the curled flows.

**Outcome (done).** Delivered `internal/evidence/trace_ingest/session.go`
(`Session`, `SessionMeta`, `NewSession`, `Append`, `Finalize`, pidfile helpers)
and `internal/evidence/trace_ingest/receiver.go` (`Receiver`, HTTP+gRPC
servers, `spansToOTLPJSONLine` normaliser, gRPC `grpcTraceHandler`), plus
`cmd/polyflow/capture.go` (`capture start/stop/run` commands). New direct deps:
`google.golang.org/grpc v1.82.1` and `go.opentelemetry.io/proto/otlp/collector/trace/v1`
(the collector package that R.0 intentionally avoided — now needed to implement
the server-side gRPC TraceService). 17 new tests in
`internal/evidence/trace_ingest/receiver_test.go` plus 2 in
`cmd/polyflow/capture_test.go`; all 19 test packages pass. `BenchmarkIndexCold`
holds (receiver is off the indexing hot path). Deviation: `Session.Append`
normalises all input (multi-line JSON or binary protobuf) to compact single-line
JSON before writing, ensuring JSONL integrity regardless of input format; the
spec said "raw spans as received" which would allow multi-line JSON to break
the JSONL format — compact normalisation is strictly better and required for the
concurrent-writes test to pass. `capture start` instructs the user to background
the process rather than using a hard daemon fork (the "or instructs to background"
clause in the spec), writing a pidfile so `capture stop` can signal it. Acceptance
confirmed manually: `capture start` binds both ports and writes the pidfile;
`capture stop` sends SIGTERM and the receiver finalises cleanly.

### Phase R.3 — SSE/streaming connection flows `done`

**Problem.** Chessleap's real gap is datastar actions + SSE streams; SSE
never terminates like a request, so it needs the connection-edge treatment.

**Deliverable.** SSE detection per the mapping table in `span_map.go`;
`sse` connection flow records; reconciliation against static `sse`
producer/consumer edges.

**Tests.** Fixture trace with an event-stream SERVER span → one connection
edge (not N event edges); a long-lived non-SSE span (websocket upgrade,
slow request) → *not* SSE (negative).

**Acceptance (the chessleap walkthrough — also the e2e proof for the whole
plan).** Instrument chessleap: `otelgin` middleware + Go SDK OTLP exporter
(server side), browser fetch/datastar instrumentation optional. Run
`polyflow capture start`, click through the datastar actions in the UI,
`capture stop`, reindex. Assert: the previously unresolved datastar-action
channels flip to `verified` (channel-granular), SSE connections appear, and
unclicked flows stay `candidate` — surfaced, not dropped.

**Outcome (done).** Delivered SSE detection in `internal/evidence/trace_ingest/span_map.go`
(`isSSESpan`, `runtimeSSERoutes`, `sseNormChain`) and added `SSERoutes []string` to
`workspace.RuntimeEvidenceConfig`. Three new fixtures: `sse_connection.otlp.json` (SERVER
span with `http.response.header.content-type: text/event-stream` → kind=`sse`, path-only
key `/events`, causality=parent_child), `sse_ws_listed_route.otlp.json` (workspace sse_routes
detection without content-type), and `sse_not_sse.otlp.json` (long-lived slow-export span →
kind=`http`, never SSE). Three new tests pass: `TestMapSpansSSEConnection`,
`TestMapSpansSSEWorkspaceListedRoute`, `TestMapSpansSSENotSSE`. All 19 test packages pass.
`BenchmarkIndexCold` holds (~10.2s — SSE detection is O(attrs + ws_routes) and off the index
hot path). No `graph.SchemaVersion` bump required — no new node/edge shape changes.
Deviation: SSE connection keys use path-only format (e.g. `/events`) rather than
`"get /events"`, because (a) SSE is always GET so the method adds no join information,
and (b) the static `sse_endpoint` edges from `go_semantic.go` carry no labels, so
runtime SSE flows typically surface as `observed_only_gap` edges — correctly confirming
the connection exists and feeding the candidate-rule proposer for R.5/F.4.

### Phase R.4 — Async causality (queues/jobs) `done`

**Problem.** Queue and background-job causality crosses trace boundaries via
span links, and many instrumentations omit links entirely.

**Deliverable.** `messaging.*` mapping per the table: link-based causality
first, key-equality fallback (`causality=key_match`) second; kinds
`amqp|kafka|nats|job` joined to the matching contract rules' keys.

**Tests.** Fixture traces: linked publish→process pair (→ flow,
`causality=link`); unlinked publish + process on the same destination
(→ flow, `causality=key_match`); publish with no consumer in-window (→
producer-side observation only, no fabricated consumer). RabbitMQ 2-service
fixture mirroring the existing bunny→amqp091 static fixture chain.

**Acceptance.** The RabbitMQ fixture's static publish→consume edge flips to
`verified` from the linked trace; the key-match-only trace yields `verified`
with the weaker causality recorded in the source ref.

**Outcome (done).** Delivered messaging handling as a second pass in
`internal/evidence/trace_ingest/span_map.go` (four sub-passes: collect,
index, consumer-match, unmatched-producer). Three new OTLP fixtures:
`msg_amqp_linked.otlp.json` (RabbitMQ linked pair, causality=link),
`msg_kafka_unlinked.otlp.json` (Kafka key-match, no span links),
`msg_producer_only.otlp.json` (NATS producer-only, no fabricated consumer).
Extended `kindToEdgeType` in `provider.go` to map AMQP→`publishes`,
Kafka→`kafka_publish`, NATS→`nats_publish`, Job→`job_enqueue` so the
reconciler's (edgeType, label) join key resolves against static contract
edges. 9 new tests pass: AMQPLinked, KafkaKeyMatch, ProducerOnly,
ConsumerNoCausality, OldSemconv, AMQPAcceptance, KafkaAcceptance,
Determinism, FanOut. All 19 test packages pass. `BenchmarkIndexCold` holds
(~10.1s — messaging pass is O(spans), off the indexing hot path). No
`graph.SchemaVersion` bump required (no new node/edge shape changes). No
deviations from the pinned spec: consumer-only spans with no matching
producer in the window are ledgered with `no_causality` (consistent with the
plan's ledger reason vocabulary); the old `messaging.operation` semconv
fallback (`publish|send` for producers, `receive|process` for consumers) is
handled in `isPublishOp`/`isConsumeOp`.

### Phase R.5 — Session coverage report + doctor merge `done`

**Problem.** Without a report, nobody knows what a capture actually proved.

**Deliverable.** Per-session and cumulative coverage: % of static channels
observed, per-kind verified/candidate/gap counts, ingest-ledger summary,
and the `observed_only_gap` list feeding F.4's candidate-rule proposer.
Surfaced via `polyflow doctor` (merged into the shared G.5/V.4/F.4 coverage
tables) and `polyflow flows --session <name> --coverage`.

**Tests.** Coverage math unit tests; doctor output test; a session covering
0 channels reports 0% without downgrading anything; report rows sorted by
(kind, key) — two-run determinism test on the same sessions (rule 2,
`docs/phases.md`).

**Acceptance.** After the R.3 chessleap walkthrough, doctor prints the
verified/candidate split and the datastar channels appear in the verified
column.

**Outcome (done).** Delivered `internal/evidence/trace_ingest/coverage.go`
(`CoverageRow`, `ObservedOnlyGap`, `CoverageReport`, `ComputeCoverage`,
`ComputeSessionCoverage`) and `graph.AdjacencyIndex.AllEdges()` (deterministic
edge enumeration, sorted by ID). `polyflow flows --coverage` computes
per-session coverage by joining flow records against the graph store's indexed
edges via `ComputeSessionCoverage`; when the store is absent it prints
session-only flow-record counts with a note to index first.
`polyflow doctor` gains a "Runtime coverage" section reading cumulative
verification state from all indexed edges via `ComputeCoverage`. 10 new unit
tests pass: `TestComputeCoverage_Basic`, `TestComputeSessionCoverage_Basic`,
`TestComputeSessionCoverage_ZeroChannels`, `TestComputeSessionCoverage_EmptyFlowsZeroPct`,
`TestComputeCoverage_DeterminismTwoRun`, `TestComputeCoverage_RowsSortedByKind`,
`TestComputeCoverage_LedgerSummary`, `TestComputeCoverage_GapsSortedByKindKeyFromTo`,
`TestComputeSessionCoverage_FanOut`, `TestComputeSessionCoverage_Determinism`.
All 19 test packages pass. `BenchmarkIndexCold` holds at ~10.2s (coverage
computation runs only at report time, never on the indexing hot path). No
`graph.SchemaVersion` bump required — no new node/edge shape changes.
Deviation: `graph/model.go` gains `import "sort"` for `AllEdges()` (minor);
no other deviations from the pinned spec.

**Addendum (2026-07-18, review fixes).** (1) The doctor/`flows --coverage`
denominator included every stored edge kind — `calls`, `contains`,
`captures`, and other intra-language edges no span can ever confirm — so
"verified %" on a real repo read ~0% forever and misreported what a capture
proved. Coverage inputs now pass through `RuntimeCoverageEdges`, restricting
the denominator to the kinds `kindToEdgeType` emits (`http_call`,
`sse_endpoint`, `publishes`, `kafka_publish`, `nats_publish`,
`job_enqueue`). The plan text "% of static channels observed" always meant
channels; the implementation now matches it. (2) `polyflow doctor`'s eval
row printed a raw error on any repo without `eval/baseline.json` (the
normal case outside this repo) because `os.IsNotExist` cannot see through
the wrapped error; it now uses `errors.Is(err, os.ErrNotExist)` and prints
the graceful no-baseline hint.

---

## Key files

- **New:** `internal/evidence/trace_ingest/{model.go,otlp.go,span_map.go,provider.go,receiver.go,session.go}`,
  `cmd/polyflow` command files for `ingest`, `capture`, `flows`,
  `testdata/evidence/runtime/` fixtures, this doc.
- **Modify:** `internal/workspace/config.go` (`evidence.runtime.service_names`
  + capture toggles), `internal/graph/model.go` (only if new ledger kinds /
  source-ref fields require it; `SchemaVersion` bump if so),
  `internal/indexer/indexer.go` (runtime provider runs in the F.0 provider
  loop — no bespoke wiring), doctor command file (R.5).

## Reuse (don't rebuild)

- `internal/contract` normalizer registry + channel keys (G.0) — the join
  vocabulary; import, never duplicate.
- F.0 `EvidenceProvider`, `Edge.Sources[]`, `verification_state`,
  `verified_granularity` — runtime is just another provider.
- `graph.UnresolvedRef` ledger — extended with ingest-ledger kinds.
- `go.opentelemetry.io/proto/otlp` + `protojson` — never hand-roll OTLP
  parsing.
- Workspace config loading (`internal/workspace`) — service-name mapping.
- `docs/phases.md` process; `BenchmarkIndexCold` as the perf gate.

## Verification

- Every phase ships positive + negative OTLP fixtures (above).
- **Degradation:** no sessions → graph byte-identical to static.
- **Granularity:** the channel-vs-site guard test (R.1) is a permanent
  regression test — it encodes the plan's core honesty rule.
- **End-to-end:** the R.3 chessleap partial-capture walkthrough is the
  acceptance script for the whole plan.
- **Benchmark:** ingest + mapping is O(spans); reconciliation stays the F.0
  key-join O(edges); hold chessleap index time + `BenchmarkIndexCold`.
  The receiver is out of the indexing hot path entirely (capture time ≠
  index time).

## Risks / honest boundaries

- **Requires instrumentation.** OTLP ingest needs the target to emit OTel —
  polyflow lowers the bar (embedded receiver, env-var recipes) but does not
  remove it. Uninstrumented apps degrade gracefully to static-only. A
  no-instrumentation reverse-proxy tap was considered and deferred: HTTP-only,
  needs header injection for causality, and duplicates what mesh/eBPF OTLP
  exporters already provide through the same seam.
- **Partial capture is partial by definition** — it confirms what ran, says
  nothing else. The session-union + never-downgrade rules make this safe;
  the coverage report (R.5) makes it visible.
- **Browser-side spans** need the receiver's CORS enabled and a
  content-security-policy that allows the exporter endpoint; the client-side
  half of a flow is optional — server spans alone still confirm channels
  (with `from_service` unknown, labeled).
- **Semconv drift.** OTel attribute names changed across SDK generations
  (`http.method`→`http.request.method`, `messaging.operation`→
  `messaging.operation.type`); the mapper accepts both, tests pin both, and
  new drift is an additive mapping-table row.
- **Async links are frequently missing** — the key-equality fallback confirms
  the channel with weaker causality, recorded as such, never upgraded to
  link-grade evidence.
- **Clock skew is irrelevant** — causality comes from trace/span ids and
  links, never timestamps; timestamps are metadata only.
- **PII/secrets:** attribute allowlist at parse time (R.0) is the boundary;
  raw session files may still contain sensitive URLs — hence gitignored by
  default, with curated fixtures the deliberate exception.

## Appendix — instrumentation recipes (per stack)

Point any of these at a polyflow capture session with:
`OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318`
`OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf` `OTEL_TRACES_EXPORTER=otlp`
`OTEL_SERVICE_NAME=<name polyflow maps via evidence.runtime.service_names>`

| Stack        | Recipe                                                                 |
| ------------ | ---------------------------------------------------------------------- |
| Go           | OTel Go SDK + `otelhttp` (net/http) / `otelgin` (gin) middleware; exporter from env |
| Node         | `@opentelemetry/auto-instrumentations-node` via `--require`; zero code changes |
| Browser JS   | `@opentelemetry/sdk-trace-web` + fetch/XHR instrumentations; needs receiver CORS |
| Ruby/Rails   | `opentelemetry-sdk` + `opentelemetry-instrumentation-all`; initializer + env |
| Java         | OTel Java agent (`-javaagent:`); zero code changes                      |
| Python       | `opentelemetry-instrument <cmd>`; zero code changes                     |
| Mesh / eBPF  | Envoy/Istio tracing config or Cilium/Hubble → OTLP export to the same endpoint; zero app changes, HTTP/gRPC only |
| Have a collector already | Add an `otlp` exporter to the collector pointing at polyflow, or use the collector's file exporter + `polyflow ingest` |

## Relationship to the other plans

- **evidence-fusion** (`docs/evidence-fusion-plan.md`) — this plan **is**
  F.2, expanded. F.0 provides the substrate; F.4 consumes the flow records
  for reconciliation/reporting; R.5 merges into F.4's doctor output.
- **contract-matching** (`docs/contract-matching-plan.md`) — supplies the
  channel-key vocabulary and normalizers (G.0); `observed_only_gap` flows
  feed its rule coverage (G.5) via auto-proposed candidate rules.
- **versioning-matrix** (`docs/versioning-matrix-plan.md`) — orthogonal:
  spans are version-agnostic (a span is a span regardless of gin version),
  which is exactly why runtime evidence needs zero per-framework rules.

## Sequencing

```
contract-matching:  G.0 ─> …                    (channel keys exist)
evidence-fusion:      └─> F.0 ─> F.1 ──────────────> F.4 ─> F.5
                            └─> R.0 ─> R.1 ─> R.2 ─> R.3 ─> R.4 ─> R.5
                                (this plan = F.2, expanded)      └─> merges into F.4
```

- **R.0–R.1 depend on G.0 + F.0** (keys + substrate).
- **R.2 (capture)** is independent of R.3–R.4 once R.1 lands.
- **R.3–R.4** extend the mapper; **R.5** closes the loop into F.4.
