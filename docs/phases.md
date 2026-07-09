# Polyflow — Gap-Closing Phase Plan

Status legend: `pending` · `in progress` · `done (commit <sha>)`

This plan closes the verified gaps between the current v1 implementation and the
user's real stack (Gin/templ/Datastar/SSE-hub, Rails+delayed_job+Pusher, RabbitMQ
Go+Ruby, GORM, dual SQLite drivers, PostgreSQL, WebSocket, AWS S3 SDK v1/v2 +
Bedrock, Yarn/Nx monorepos). One phase per commit; tests pass before moving on;
this doc is updated as each phase completes. `docs/polyflow-design.md` is updated
whenever a phase changes or extends a documented decision.

Ground rules carried through every phase:

- Every new pattern YAML ships with a **positive** fixture (`input.*` +
  `expected.json`) and a **negative** fixture (`negative.*` that must produce zero
  matches). Version-gated patterns additionally ship a same-shape-wrong-version
  negative.
- No Sidekiq, ActionCable, or browser Web Worker patterns — no evidence in any
  surveyed repo. (The pre-existing `sidekiq.yaml` is kept but not extended.)
- New stacks are added as YAML + fixtures only; core matcher/graph code changes
  only for genuine registry capabilities (version gating, dependency resolution).
- 90% coverage gate and the "no fixture → CI fails" rule stay intact.
- gorilla/websocket and bunny patterns are written against the canonical public
  API shapes (gorilla chat example read/write pumps, bunny README publish/
  subscribe). If validation against the real `dsw-manager`/`nextGen` call sites
  shows a mismatch, the user supplies a redacted snippet and the fixtures get
  updated — flagged in the pattern file header comments.

---

## Phase 1 — Fixture verification harness + negative fixtures — done

**Problem**: `expected.json` files exist but nothing reads them; the fixture test
only checks that `input.*` exists. "Zero false positives" is not currently proven
by CI.

**Deliverable**
- `internal/patterns/fixtures_test.go` extended: for every pattern YAML, parse
  `<name>_test/input.*` with the pattern file's language, run the matcher, and
  assert the matched pattern names / node types against `expected.json`.
- Negative-fixture support: every `<name>_test/negative.*` file must produce
  **zero** matches for that pattern file. Presence of at least one negative
  fixture becomes mandatory (CI fails without it).
- Backfill negative fixtures for all existing pattern files.

**Proof**: `go test ./internal/patterns/` fails if any pattern matches its
negative fixture or diverges from `expected.json`; passes on the full tree.

## Phase 2 — Dependency resolution + version-aware pattern gating — pending

**Problem**: no per-service resolved dependency versions; patterns cannot be
scoped to package version ranges (AWS SDK v1 vs v2 is the proof case).

**Deliverable**
- New `internal/deps` package: resolve exact installed versions per service from
  `go.mod` (+`go.sum`), `package.json` + lockfile (`package-lock.json`,
  `yarn.lock` — exact resolved version, and `dependencies` vs `devDependencies`
  location recorded as `kind: prod|dev`), and `Gemfile.lock`.
- New `dependencies` table in the SQLite store
  (`service, ecosystem, name, version, kind`), written during `polyflow index`,
  queryable ("what version of aws-sdk-go does dsw-agent use").
- Pattern YAML schema gains optional top-level `package:` and `version_range:`
  fields (ecosystem-native semver semantics via `Masterminds/semver`; Ruby `~>`
  translated to its semver equivalent). Registry activates a pattern file for a
  service only when the service's resolved version of `package` satisfies
  `version_range`; files without these fields keep current behavior.
- Matched-version metadata: nodes produced by a version-gated pattern carry
  `package` + `resolved_version` in `Meta`, so the graph/UI/JSON can show "this
  S3 upload uses SDK v1".
- Generic capability: nothing AWS-specific in loader/registry/matcher.

**Proof**: unit tests for each lockfile parser (real-shaped fixtures); registry
test proving a pattern gated on `>=2.0.0` activates for a service with v2 and
not for v1; dependency table integration test.

## Phase 3 — Gin routes pattern — pending

**Problem**: Gin is the dominant Go web framework across every surveyed Go repo;
no pattern exists.

**Deliverable**: `patterns/go/gin_routes.yaml` — `r.GET/POST/PUT/PATCH/DELETE/
HEAD/OPTIONS/Any` on `gin.Engine`/`gin.RouterGroup`, `Group(...)` route groups,
`Use(...)` middleware, `c.JSON(code, x)` response shapes, `c.Bind/ShouldBindJSON
(&x)` request shapes. Route→handler linking works through the existing
`linker.LinkRouteHandlers` (route nodes resolve handler function names).

**Proof**: positive fixture built from real route shapes (chessleap/mysycamore
style: engine routes, grouped routes, method handlers), negative fixture (chi
and net/http shapes must NOT match), `expected.json` verified by the Phase 1
harness. Zero core code changes.

## Phase 4 — Datastore patterns: GORM, dual SQLite drivers, PostgreSQL — pending

**Deliverable**
- `patterns/go/gorm.yaml`: method-chained query API (`db.Where(...).First(&x)`,
  `db.Create/Save/Delete/Find`), `gorm.Open(...)` dialector detection
  (postgres/sqlite) → datastore node with `orm: gorm` + `driver` metadata.
- Driver-level detection via `internal/deps` (not YAML): `modernc.org/sqlite`
  and `mattn/go-sqlite3` map to one logical `datastore` node type
  (`engine: sqlite`) with `driver` metadata distinguishing pure-Go vs cgo;
  `lib/pq` and `gorm.io/driver/postgres` map to `engine: postgres`.
  New `NodeTypeDatastore` in the graph model + `queries`/`persists` edge types.
- `patterns/go/database_sql.yaml`: raw `sql.Open(driver, dsn)` /
  `db.Query/QueryRow/Exec` call sites.

**Proof**: positive + negative fixtures for both YAML files (negative: GORM
chains must not match raw `database/sql`, and vice versa); unit test proving
both SQLite drivers produce the same datastore node type with different
`driver` meta; design doc Node/Edge tables updated.

## Phase 5 — AWS SDKs: S3 v1 + v2, Bedrock, Ruby aws-sdk-s3 — pending

The version-aware proof case.

**Deliverable**
- `patterns/go/aws_s3_v1.yaml` (`package: github.com/aws/aws-sdk-go`,
  `version_range: ">=1.0.0 <2.0.0"`): `s3.New(sess)`,
  `svc.PutObject(&s3.PutObjectInput{...})` — no context arg.
- `patterns/go/aws_s3_v2.yaml` (`package: github.com/aws/aws-sdk-go-v2/service/s3`):
  `s3.NewFromConfig(cfg)`, `client.PutObject(ctx, &s3.PutObjectInput{...})` —
  context-first.
- `patterns/go/aws_bedrock.yaml`: `bedrockruntime` `InvokeModel`/
  `InvokeModelWithResponseStream` → distinct `external_service` node with
  `service: bedrock` (LLM/AI call, not generic S3).
- `patterns/ruby/aws_s3.yaml`: `Aws::S3::Client#put_object/get_object`,
  `Aws::S3::Resource` bucket/object upload shapes.
- New `NodeTypeExternalService` + `cloud_call` edge type; version metadata on
  nodes per Phase 2.

**Proof**: cross-version negative fixtures — the v1 input must produce zero
matches under the v2 pattern file and vice versa, both at the shape level
(negative fixture) and the gating level (registry test with a go.mod pinning
the other major). Bedrock fixture must not match the S3 patterns.

## Phase 6 — Jobs & brokers: delayed_job, solid_queue, RabbitMQ + Pusher validation — pending

**Deliverable**
- `patterns/ruby/delayed_job.yaml`: `.delay.method(...)`,
  `handle_asynchronously`, `Delayed::Job.enqueue(CustomJob.new(...))`, and
  ActiveJob-style `SomeJob.perform_later` with `queue_adapter :delayed_job`
  shapes → `job_enqueue`/`job_perform` edges (generic edge types, not
  Sidekiq-specific).
- `patterns/ruby/solid_queue.yaml`: ActiveJob `perform_later` + recurring task
  declarations under solid_queue.
- Extend `patterns/go/amqp091.yaml`: `PublishWithContext` (current API),
  `ExchangeDeclare`, `ConsumeWithContext`; verify channel-node synthesis still
  links publisher↔subscriber cross-service (dsw-manager↔dsw-agent shape).
- Extend/validate `patterns/ruby/bunny.yaml` (exchange.publish with routing_key
  option, queue.subscribe block) and `patterns/ruby/pusher.yaml`
  (`Pusher.trigger(channel, event, payload)` + `pusher-js` client `subscribe`/
  `bind` in `patterns/javascript/pusher.yaml`, new).
- Generic `job_enqueue`/`job_perform` edge types in the model; sidekiq mapping
  migrates onto them (old constants kept as aliases).

**Proof**: positive + negative fixtures each; cross-language RabbitMQ link test
(Go publisher + Ruby consumer on the same exchange → one channel edge chain) in
the linker tests.

## Phase 7 — Realtime: WebSocket patterns + SSE broadcast-hub — pending

**Deliverable**
- `patterns/go/gorilla_websocket.yaml`: `upgrader.Upgrade(w, r, nil)`,
  read/write pump shapes (`conn.ReadMessage/ReadJSON` loop,
  `conn.WriteMessage/WriteJSON`) → `ws_upgrade`, `ws_read`, `ws_write` edges.
- `patterns/javascript/websocket.yaml`: `new WebSocket(url)`,
  `ws.on('message')` / `ws.onmessage` / `addEventListener('message')`, typed-
  JSON dispatch (`JSON.parse(...).type` switch/map → handler) per
  synergy/tether's shape, `ws.send(JSON.stringify({type: ...}))`.
- `patterns/go/sse_hub.yaml`: broadcast-hub fan-out — `Subscribe()/
  Unsubscribe()/Broadcast()` methods on a hub struct with channel fields, and
  the per-connection writer loop feeding SSE — distinct from `datastar_sse.yaml`
  direct call sites. Edges: `hub_subscribe`, `hub_broadcast` chaining into the
  existing `sse_endpoint`.
- New edge types registered in model + classifyPattern mapping.

**Proof**: fixtures per file; typed-dispatch fixture asserts the message-type
string is captured as edge metadata (so `{type: "battery"}` links client↔server
by type); negative fixtures (plain `for` loop over a channel is not a hub;
`EventSource` is not a WebSocket).

## Phase 8 — Auto-discovery `polyflow init` — pending

**Problem**: prompt-by-prompt init with hand-typed absolute paths (this repo's
own workspace.yaml has stale paths from another username).

**Deliverable**
- `internal/workspace/discover.go`: walk the tree for `go.work` (each module a
  service), `go.mod`, `package.json` (npm/yarn workspaces expanded; Nx
  `project.json` treated as service roots), `Gemfile`. Detect language +
  frameworks via existing `DetectFrameworks`; record Yarn `portal:`/`link:`
  dependencies as auto-generated link hints; store paths relative to the
  workspace root.
- `polyflow init` becomes non-interactive by default: discover → print table →
  write workspace.yaml. `--interactive` keeps the old flow; discovered entries
  are editable via existing `config service` commands (manual override).
- Fix this repo's own workspace.yaml via the new discovery (relative paths).

**Proof**: unit tests against synthetic trees (go.work multi-module, yarn
workspaces + Nx, Rails app, portal: cross-link); integration test:
`init && index` on a fixture monorepo produces a working graph with zero manual
entry.

## Phase 9 — Incremental indexing — pending

**Problem**: design doc specifies `file_hashes` + incremental re-index; the
table and logic don't exist — every index is a full rebuild.

**Deliverable**
- `file_hashes` table (path, service, content_hash, indexed_at) per design doc.
- `polyflow index` default becomes incremental: unchanged files (SHA-256 match)
  skip parsing; their nodes/edges are carried over; changed/new/deleted files
  re-parse with node/edge replacement scoped to file; linking passes re-run on
  the merged set. `--full` forces a rebuild (flag exists, currently a no-op).

**Proof**: unit test proving unchanged files are skipped (parse-count spy);
integration test: index twice, second run parses zero files and produces an
identical graph; edit one file → only that file re-parses.

## Phase 10 — Chain tracing + agent JSON completeness — pending

**Deliverable**
- `polyflow trace --root <query> --direction --depth --format json|text|chain`:
  `chain` prints linear `A → B → C → D` paths (each hop labeled with edge type
  and service boundary marks).
- `context`/`impact`/`trace` JSON includes all new edge/node types (RabbitMQ,
  GORM/datastore, AWS SDK calls with resolved version, Pusher, WebSocket,
  SSE-hub, job queues) — including per-edge `package`/`resolved_version` where
  present, answering "what breaks if I bump aws-sdk-go to v2".

**Proof**: chain output asserted against the RabbitMQ cross-repo fixture chain
and the SSE-hub and WebSocket fixture chains specifically; JSON snapshot test
listing every edge type present in fixtures.

## Phase 11 — UI: versions, boundary collapse, confidence default, diagram export — pending

**Deliverable**
- Detail panel shows `package@resolved_version` for framework-boundary and
  cloud-SDK nodes/edges.
- Boundary nodes (framework/SDK internals: Gin, AWS SDK, bunny/amqp091)
  collapsed by default with edges still visible; per-node expand toggle.
- Default graph renders only `static`+`inferred` confidence edges;
  `partial`/`unknown` opt-in via Filters and visually distinct (dashed/dimmed).
- Export: service-level aggregated view ("high-level") vs per-function view
  ("in-depth") toggle; export current view as Mermaid (server-side
  transformation) and SVG (Cytoscape client-side).
- Verify/finish Search→root-select→isolated-subgraph wiring in `Search.tsx`.

**Proof**: component tests for collapse toggle, confidence filter default, and
export output shape; Mermaid export golden test on the server side; manual
smoke via `polyflow serve` documented in the phase notes.

## Phase 12 — E2E cross-stack chains + performance — pending

**Deliverable**
- E2E fixture workspaces exercising ≥4 hops across ≥3 languages:
  1. templ `data-on-click` → Datastar action → Gin handler → `hub.Broadcast()`
     → SSE patch → client signal/DOM.
  2. Rails controller → bunny publish → RabbitMQ exchange → Go `amqp091`
     consumer (cross-language, cross-repo).
  3. JS WebSocket typed message → Go gorilla read pump → dispatch-by-type
     handler → response write → client `onmessage` handler.
- Benchmarks against a synthetic large workspace shaped like synergy/nextGen
  (multi-module go.work + several JS apps; Rails-monolith-sized Ruby tree);
  assert documented targets (10k files < 30s cold, incremental 100 files < 3s)
  or document measured reality.

**Proof**: `go test ./internal/e2e/` traces each chain end-to-end via the chain
output; `make bench` includes the new benchmarks; results recorded here.

---

## Completion log

(updated as each phase lands — phase, commit, and any deviations from plan)

- **Phase 1 — done.** Harness verifies expected.json multisets + node types and
  enforces/runs negative fixtures for all 37 pattern files. Found and fixed two
  real bugs: Registry stored patterns in a name-keyed map, silently dropping
  same-named query variants (second goroutine_call overwrote the first); and
  unanchored `(_) @x . _*` argument captures in amqp_consume/fetch/axios
  produced combinatorial duplicate matches (21 matches for one Consume call).
  Classifier gaps fixed: xhr_*/jquery_ajax → http_client, jquery_selector →
  dom_target, controller_action → method, queue_declare → channel,
  datastar_on_signal → function. Note: the design doc's "90% coverage CI gate"
  does not exist in-repo (no CI config; measured total 46.8%, dominated by the
  untested cmd/polyflow CLI). New code in later phases ships with tests; adding
  a real CI gate at the honest baseline is tracked as follow-up.
