# Polyflow — Tool Landscape Analysis

Cross-service static analysis tool comparison: Polyflow vs. all adjacent tools across every relevant category.
Captured 2026-07-27.

---

## TL;DR

**Polyflow occupies an empty cell in the tool landscape.** No other tool combines static analysis with cross-service boundary crossing. Every tool that crosses service boundaries requires live running infrastructure (runtime). Every static analysis tool stops at a single service/repo boundary.

---

## The Tools Reviewed

### Graphify (`github.com/Graphify-Labs/graphify`)

| Attribute | Detail |
|---|---|
| **What it is** | A local heterogeneous knowledge graph builder — code, docs, PDFs, images, audio |
| **Core mechanism** | Tree-sitter AST (pass 1, free) + Claude subagents for docs/images (pass 2, paid) + NetworkX graph |
| **Languages** | ~40 via tree-sitter grammars |
| **Cross-service?** | No. Operates on a local folder. No HTTP linking, no broker linking. |
| **Multi-repo?** | Manual merge only (`graphify global add`) — run per-repo, then merge JSON files |
| **Output** | `graph.json`, interactive `graph.html` (vis.js), Obsidian vault, wiki articles, Cypher/GraphML/SVG |
| **Primary user** | AI coding agents (Claude Code, Cursor, Codex) — reduces token spend on code exploration |
| **What it does well** | 71.5× token reduction on large mixed corpora; handles non-code assets (PDFs, video transcripts) |
| **Key gap vs Polyflow** | No concept of service boundaries, HTTP calls, message brokers, SSE, WebSocket, gRPC. Its "cross-repo" story is additive graph merging, not protocol-level call linking. |

Graphify is an **AI agent memory/compression tool**. Polyflow is a **distributed system dependency mapper**. They target adjacent problems.

---

### Serena (`github.com/oraios/serena`)

| Attribute | Detail |
|---|---|
| **What it is** | MCP server that wraps LSP — gives AI agents IDE-grade navigation (go-to-def, find-refs, rename) |
| **Core mechanism** | Spawns 70+ language servers (pyright, gopls, rust-analyzer, etc.); wraps LSP responses as MCP tools |
| **Languages** | 70+ (anything with an LSP server) |
| **Cross-service?** | No. Anchored to one `project_root`. The only multi-project feature is `QueryProjectTool` — an agent can manually call into a second registered local directory. No automatic cross-service resolution. |
| **Output** | Structured JSON responses to MCP tool calls (symbol lists, reference lists, diagnostics) |
| **Primary user** | AI coding agents doing single-project development work |
| **What it does well** | Best-in-class LSP coverage (v1.6.1, July 2026); JetBrains plugin backend option; persistent memory for agents |
| **Key gap vs Polyflow** | Cannot parse `axios.post('/api/orders')` and link it to the Go handler in another repo. No call graph persisted. No broker/SSE/WebSocket linking. Single-project scope by design. |

Serena is an **AI agent IDE proxy**. Polyflow is a **cross-service call graph**. They are architecturally complementary — Serena could use Polyflow's MCP server for inter-service navigation queries it cannot answer from LSP alone.

---

## Broader Landscape: Every Relevant Tool Category

### Runtime Distributed Tracing (cross-service but NOT static)

These require live running services. They see only observed execution paths — dead code and untested flows are invisible.

| Tool | Mechanism | Cross-service | Key Limitation |
|---|---|---|---|
| **Jaeger / Zipkin** | Span collection | Yes | Requires instrumented running code |
| **OpenTelemetry** | SDK instrumentation | Yes | Framework only; no source analysis |
| **Datadog APM / New Relic / Dynatrace** | Runtime agents | Yes | Commercial; runtime only |
| **Keploy** | eBPF kernel intercept | Yes (HTTP, gRPC, Kafka, RabbitMQ) | Needs running app; no source analysis |
| **AppMap** | Test-run instrumentation | Partial (single-app HTTP/SQL/jobs) | Runtime, single-app scope, Ruby/Java/Python/Node only |
| **Kiali / Istio** | Service mesh proxy | Yes | Requires Kubernetes + Istio sidecar |
| **Cilium Hubble** | eBPF | Yes | Requires Linux eBPF; network-level only |

None of these cross the static/dynamic line — they all require a running system.

---

### Static Analysis — Single-Service (static but NOT cross-service)

| Tool | Languages | Cross-service | Key Gap |
|---|---|---|---|
| **CodeQL** | Java, JS/TS, Python, Go, C++, C#, Ruby | No | Separate databases per repo; no HTTP linking |
| **Semgrep** | 30+ | No | Pattern matching within one repo |
| **SonarQube** | 40+ | No | SAST within one project |
| **Sourcegraph SCIP** | 10+ | Cross-repo xref only | No HTTP/broker linking; just symbol navigation |
| **Kythe** | C++, Java, Go | No | Reference index within one codebase |
| **ArchUnit** | Java only | No | Bytecode import rules, intra-app |
| **Soot / WALA** | JVM, Android | No | Call graph within one program |
| **dependency-cruiser** | JS/TS only | No | Module import graph, no HTTP calls |
| **Spring Modulith** | Java only | No | Module boundary verification in a monolith |
| **NDepend** | .NET/C# only | No | Single-project dependency metrics |
| **Microsoft Application Inspector** | C, C++, C#, Java, JS, Go, Ruby, Python | No | Pattern presence detection only; no graph |
| **CodeScene** | Multi-language | No | Git history + code health; no inter-service |

---

### Architecture Documentation (manual declarations, NOT analysis)

| Tool | Mechanism | Auto-discovers HTTP/broker calls? |
|---|---|---|
| **Structurizr** | Manual DSL | No — architect writes `svc_a -> svc_b "Uses"` by hand |
| **Backstage** | Manual `catalog-info.yaml` | No — teams maintain YAML metadata |
| **PlantUML** | Text diagram definitions | No |
| **MicroFreshener** | GUI-based drawing | No |
| **microTOSCA** | TOSCA schema vocabulary | No — modeling framework, not scanner |

---

### API Spec / Contract-Driven Tools (spec-centric, NOT code-scanning)

These consume API specifications (OpenAPI, AsyncAPI, protobuf) rather than scanning source code. Publisher-consumer relationships must be declared in the spec — not inferred from call sites.

| Tool | Mechanism | Limitation |
|---|---|---|
| **EventCatalog** | AsyncAPI/OpenAPI specs + Kafka Schema Registry | Spec-driven; does not scan call sites |
| **Apollo Rover / GraphOS** | GraphQL SDL composition | Schema-level only; not source code |
| **Microcks** | OpenAPI, AsyncAPI, gRPC proto, GraphQL | Mocking + contract testing from specs |
| **Spring Cloud Contract** | Consumer-driven contract testing | Requires knowing relationships upfront |
| **SwaggerHub / Stoplight** | API design management | Catalog of specs; no code scanning |
| **Buf** | Protobuf schema toolchain | Schema compat only; no service call graph |
| **AsyncAPI Tooling** | AsyncAPI spec files | Relationships declared in spec, not inferred |

---

### Infrastructure / Configuration Topology (infra-level, NOT code-level)

| Tool | Mechanism | Limitation |
|---|---|---|
| **docker-compose-viz** | Parses `docker-compose.yml` | Detects `depends_on`/`links` only; no HTTP or broker calls |
| **InfraMap** | Terraform HCL / state files | Resource references in Terraform; no HTTP detection |
| **Terrascan** | IaC security compliance | Policy checks; no dependency graph generation |

---

### Adjacent Single-Repo Code Graph Tools

| Tool | What it does | Cross-service |
|---|---|---|
| **code-review-graph** | Blast-radius within a repo (tree-sitter) | No |
| **repomix** | Bundles repo into one AI-readable file | No |
| **GitNexus** | Knowledge graph, impact analysis, single repo | No |
| **Graphify** | Multi-format knowledge graph, single folder | No |
| **Serena** | LSP-backed MCP tools for AI agents | No |

---

### Data Lineage Tools (data assets, not service code)

| Tool | Mechanism | Scope |
|---|---|---|
| **DataHub** | Metadata ingestion from Snowflake, Airflow, dbt, Kafka | Data asset lineage (table → pipeline → dashboard); not application HTTP dependencies |
| **Spline** | Runtime Spark listener | Column-level lineage within Spark jobs |

---

## The Competitive Matrix

| Capability | Polyflow | Graphify | Serena | CodeQL/Semgrep | Jaeger/OTel | Backstage |
|---|---|---|---|---|---|---|
| Static analysis | Yes | Yes | Yes (via LSP) | Yes | No | No |
| Cross-service | Yes | No | No | No | Yes | Manual |
| HTTP call linking | Yes | No | No | No | Yes (runtime) | Manual |
| Broker linking (Kafka/RabbitMQ/Sidekiq) | Yes | No | No | No | Yes (runtime) | Manual |
| SSE / WebSocket | Yes | No | No | No | Partial | Manual |
| gRPC | Yes | No | No | No | Yes (runtime) | Manual |
| Multi-language | Yes (Go, JS/TS, Ruby, Python, Templ) | Yes (~40) | Yes (70+ LSPs) | Yes (~10) | Language-agnostic | N/A |
| Multi-repo (first-class) | Yes | Manual merge | No | No | Yes | Manual YAML |
| Persisted call graph | Yes (SQLite) | Yes (JSON) | No | Yes (CodeQL DB) | No (ephemeral) | No |
| Evidence fusion (static + runtime + spec) | Yes | No | No | No | Runtime only | No |
| AI agent MCP tools | Yes | Yes | Yes (primary) | No | No | No |
| Zero infrastructure | Yes (single binary) | Yes | Yes | Yes | No (collectors needed) | No |
| DOM mutation tracking | Yes | No | No | No | No | No |
| Variable data flow | Yes | No | No | Partial | No | No |

---

## The Summary Table: Static + Cross-Service Gap

| Tool | Static | Cross-service | HTTP linking | Broker linking | SSE/WS/gRPC | Multi-language | No infra required |
|---|---|---|---|---|---|---|---|
| **Polyflow** | Yes | Yes | Yes | Yes (RabbitMQ, Kafka, Sidekiq) | Yes | Yes | Yes |
| Jaeger / Zipkin / OTel | No (runtime) | Yes | Yes | Yes | Yes | Yes | No |
| Datadog / Dynatrace / New Relic | No (runtime) | Yes | Yes | Partial | Yes | Yes | No |
| Kiali / Istio / Hubble | No (runtime) | Yes | Yes | No | Yes (L7) | Yes | No |
| Keploy | No (runtime) | Yes | Yes | Yes (Kafka, RabbitMQ) | Yes | Yes | No |
| CodeQL | Yes | No | No | No | No | Yes (~10) | Yes |
| Semgrep | Yes | No | No | No | No | Yes (~30+) | Yes |
| SonarQube | Yes | No | No | No | No | Yes (40+) | Yes |
| Sourcegraph SCIP | Yes | Cross-repo xref only | No | No | No | Yes (~10) | Yes |
| Kythe | Yes | No | No | No | No | C++, Java, Go | Yes |
| ArchUnit | Yes | No | No | No | No | Java only | Yes |
| Soot / WALA | Yes | No | No | No | No | Java/Android | Yes |
| dependency-cruiser | Yes | No | No | No | No | JS/TS only | Yes |
| Structurizr | Manual DSL | Manual only | Manual | Manual | Manual | DSL only | Yes |
| Backstage | Manual YAML | Manual only | Manual | Manual | Manual | N/A | Yes |
| EventCatalog | Spec-driven | Spec-driven | Spec-driven | Spec-driven | N/A | N/A | Yes |
| docker-compose-viz | Yes (YAML) | Infra-only | No | No | No | N/A | Yes |
| InfraMap | Yes (Terraform) | Infra-only | No | No | No | N/A | Yes |
| Graphify | Yes | No | No | No | No | ~40 | Yes |
| code-review-graph | Yes | No | No | No | No | 30+ | Yes |
| Spring Modulith | Yes | No | No | No | No | Java only | Yes |

---

## What Makes Polyflow Unique

Three properties in combination that no other tool has:

**1. Static + Cross-service**
The only tool that crosses service boundaries without requiring a running system. Every other cross-service tool is runtime-based (APM agents, eBPF, service mesh proxies, OpenTelemetry instrumentation).

**2. Protocol-aware linking from source code**
- Matches `axios.post('/api/builds')` to `r.Post("/api/builds", handler)` by URL normalization
- Matches `channel.Publish(exchange, key)` to `channel.Consume(queue)` by exchange/routing key across separately deployed services
- Matches `Pusher.trigger(channel, event)` to `pusher.subscribe(channel)` across Ruby backend and JavaScript frontend
- Matches Sidekiq/ActiveJob `.perform_async` to worker `perform` by class name
- Links Datastar SSE endpoints across Go handlers and Templ HTML attributes
- No other tool does any of these statically

**3. Polyglot in one graph**
Go + JavaScript/TypeScript + Ruby + Python + Templ all in a single traversable graph with cross-language edges. Most deep static analysis tools (CodeQL, Soot, ArchUnit) are single-language or JVM-only.

**Additional differentiators:**
- **Evidence fusion**: Static analysis + OpenTelemetry runtime traces + OpenAPI/AsyncAPI/Protobuf specs + Kubernetes/Terraform config resolution, merged with source provenance per edge. Confidence degrades gracefully (static → inferred → partial → candidate).
- **DOM mutation tracking**: Traces which JS functions touch which HTML elements, including elements defined in `.templ` files in a separate Go service.
- **Variable data flow**: Package-level variables and closure-captured locals as first-class graph nodes with `reads`/`writes` edges.
- **Token-budgeted AI context**: Pre-computed graph answers "what does this function affect across all services?" in ~800 tokens vs. 40k+ tokens of raw file exploration.
- **Single binary, zero infrastructure**: Pure Go + embedded SQLite; no collector, no agent, no Kubernetes required.

**The academic gap**: Research papers on "recovering microservice architecture from source code" exist (primarily Spring Boot annotation scanning, Java-only, from University of Pisa and Lero). None have produced a maintained general-purpose polyglot tool. Polyflow is the first.
