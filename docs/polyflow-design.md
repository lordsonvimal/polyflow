# Polyflow — Cross-Service Code Flow Analyzer

## Overview

Polyflow is a static analysis tool that traces code flow across multiple services and programming languages. It parses source code, detects inter-service communication patterns (HTTP, message brokers, SSE), and renders an interactive graph visualization in the browser.

**Key differentiator**: No existing tool combines static cross-language analysis + inter-service communication detection (HTTP, RabbitMQ, Kafka, SSE) + variable dependency tracking + interactive graphical exploration in one tool.

**Upgraded design**: v1 uses tree-sitter for fast structural extraction. v1.5+ upgrades to compiler/language-server APIs per language for semantic accuracy — closing the gap with tools like Kythe, Sourcegraph SCIP, and CodeQL on the dimensions that matter for cross-service flow tracing.

---

## Core Decisions

| Decision | Choice |
|----------|--------|
| Name | `polyflow` (single constant in `internal/meta/meta.go` for easy rename) |
| Language | Go |
| Analysis approach | Hybrid: static analysis primary, optional runtime enrichment |
| Parser | Tree-sitter (v1 structural) → compiler APIs per language (v1.5 semantic) |
| Languages (v1) | Go, JavaScript/TypeScript, Ruby, Templ (HTML) |
| Pattern registry | Declarative YAML, user-extensible, community-friendly |
| Cross-service linking | Constraint-based URL resolver + OpenAPI as authoritative override + user hints |
| Repository setup | Workspace config file (local paths v1, Git URLs v2) |
| Graph store | SQLite (`modernc.org/sqlite`, pure Go, no cgo) + in-memory adjacency map |
| Performance | Goroutine worker pool, pipeline pattern with channels |
| Visualization | Cytoscape.js served via local HTTP server |
| Core UX | Search -> select root -> choose direction -> isolated subgraph |
| API format | Cytoscape JSON directly from Go backend |
| CLI framework | Cobra |
| Indexing | Explicit only (`polyflow index`), incremental by default (file-hash cache; `--full` to force re-parse) |
| Framework detection | Auto-detected from imports/Gemfile/package.json, with user override |
| Test coverage | 90% minimum enforced in CI |

---

## Architecture

### Go Module Structure

```
polyflow/
├── cmd/polyflow/               # Cobra CLI entrypoint
│   └── main.go
├── internal/
│   ├── meta/                   # Name, version constants (SINGLE rename point)
│   │   └── meta.go
│   ├── parser/                 # Tree-sitter parsing, per-language extractors
│   │   ├── parser.go           # Interface + worker pool orchestrator
│   │   ├── go.go               # Go-specific AST extraction
│   │   ├── javascript.go       # JS/TS-specific AST extraction
│   │   ├── ruby.go             # Ruby-specific AST extraction
│   │   ├── templ.go            # Templ parsing via github.com/a-h/templ/parser/v2
│   │   └── testdata/           # Fixture source files per language
│   ├── patterns/               # YAML pattern loading and matching
│   │   ├── loader.go           # YAML -> struct hydration
│   │   ├── matcher.go          # Tree-sitter query compilation + matching
│   │   ├── registry.go         # Pattern registry (built-in + custom)
│   │   └── testdata/           # Pattern test fixtures
│   ├── graph/                  # Node/edge model, SQLite store
│   │   ├── model.go            # Node/Edge type definitions
│   │   ├── store.go            # SQLite CRUD interface
│   │   ├── writer.go           # Batch pipeline writer
│   │   ├── query.go            # BFS/DFS traversal, search
│   │   └── testdata/
│   ├── linker/                 # Cross-service connection resolution
│   │   ├── linker.go           # Route matching engine
│   │   ├── hints.go            # User hint processing from workspace config
│   │   └── testdata/
│   ├── workspace/              # Workspace config parsing
│   │   ├── config.go           # workspace.yaml schema + parser
│   │   └── detect.go           # Framework auto-detection
│   └── server/                 # HTTP server + API endpoints
│       ├── server.go           # HTTP server setup
│       ├── handlers.go         # API route handlers
│       └── cytoscape.go        # Graph -> Cytoscape JSON transformation
├── web/                        # Frontend (SolidJS + Cytoscape.js)
│   ├── src/
│   │   ├── App.tsx             # Root component, URL state sync
│   │   ├── components/
│   │   │   ├── Graph.tsx       # Cytoscape.js wrapper (imperative via ref)
│   │   │   ├── Search.tsx      # Search panel with as-you-type results
│   │   │   ├── Detail.tsx      # Node/edge detail panel
│   │   │   ├── Filters.tsx     # Service, language, edge type filters
│   │   │   ├── LayoutToggle.tsx # Layout dropdown selector
│   │   │   └── Notification.tsx # "Graph updated" toast
│   │   ├── stores/
│   │   │   ├── graph.ts        # Graph state signals (nodes, edges, active trace)
│   │   │   ├── search.ts       # Search state + debounced API calls
│   │   │   └── ui.ts           # UI state (panels, layout, pins)
│   │   └── index.tsx           # Entry point
│   ├── index.html
│   ├── package.json
│   └── vite.config.ts          # Vite bundler (outputs to dist/ for Go embed)
├── patterns/                   # Built-in YAML pattern definitions
│   ├── go/
│   │   ├── chi_routes.yaml
│   │   ├── net_http_client.yaml
│   │   ├── net_http_handler.yaml
│   │   ├── resty.yaml
│   │   ├── amqp091.yaml
│   │   ├── datastar_sse.yaml
│   │   └── goroutines.yaml
│   ├── javascript/
│   │   ├── axios.yaml
│   │   ├── fetch.yaml
│   │   ├── xmlhttprequest.yaml
│   │   ├── datastar_actions.yaml
│   │   ├── dom_access.yaml
│   │   ├── dom_mutation.yaml
│   │   ├── dom_tree.yaml
│   │   ├── dom_events.yaml
│   │   ├── dom_create.yaml
│   │   └── jquery.yaml
│   ├── typescript/
│   │   ├── interfaces.yaml         # interface/type declarations → data shape extraction
│   │   ├── enums.yaml              # enum definitions
│   │   └── type_annotations.yaml   # function param types → link to interface for data flow
│   ├── ruby/
│   │   ├── rails_routes.yaml
│   │   ├── rails_controllers.yaml
│   │   ├── net_http.yaml
│   │   ├── faraday.yaml
│   │   ├── httparty.yaml
│   │   ├── rest_client.yaml
│   │   ├── bunny.yaml
│   │   ├── sidekiq.yaml
│   │   └── pusher.yaml
│   └── templ/                  # No YAML patterns — templ uses Go-native extraction
│       └── README.md           # Documents that templ parsing uses a-h/templ/parser/v2 Visitor
├── .polyflow/                  # Generated (git-ignored)
│   └── graph.db                # SQLite graph store
├── workspace.yaml              # User-created project config
├── go.mod
├── go.sum
└── Makefile
```

---

## Upgraded Architecture (v1.5+)

### Why Tree-sitter Alone Isn't Enough

Tree-sitter provides **syntax** (AST structure). Reliable analysis of closures, type resolution, scope chains, interface satisfaction, and cross-file data flow requires **semantics** — which only compiler/language-server APIs provide. Every serious competitor (Kythe, Sourcegraph SCIP, CodeQL) solves this using each language's own compiler as the analysis engine.

The upgraded design is a **hybrid approach**:
- **Tree-sitter** — fast structural extraction for patterns, DOM, Datastar, YAML-driven detection (unchanged)
- **Compiler APIs** — semantic accuracy for call graphs, type resolution, closure tracking, data flow

### Per-Language Semantic Upgrades

#### Go — `golang.org/x/tools` (highest priority, biggest win)

Replace tree-sitter Go call graph extraction with Go's own compiler API:

```go
import (
    "golang.org/x/tools/go/packages"
    "golang.org/x/tools/go/callgraph"
    "golang.org/x/tools/go/callgraph/rta"
    "golang.org/x/tools/go/pointer"
)

cfg := &packages.Config{Mode: packages.LoadAllSyntax}
pkgs, _ := packages.Load(cfg, "./...")
// Full type-resolved call graph, pointer analysis, interface satisfaction
```

**What this adds over tree-sitter:**
- Full type resolution — `x.Method()` resolved to exact implementing struct, not just "something with Method"
- Closure capture analysis — the compiler already computed this; we read it
- Interface satisfaction — which structs implement which interfaces across packages
- Pointer analysis via `go/pointer` — resolves dynamic dispatch through interfaces
- Cross-package call edges without manual import tracing

**Accuracy: ~80% (tree-sitter) → ~95% (compiler API)**

This makes Go analysis comparable to or better than Kythe for call graph purposes.

#### TypeScript / JavaScript — TypeScript Compiler API

Replace tree-sitter TS extraction with the TypeScript compiler API for typed codebases:

```typescript
import * as ts from 'typescript';

const program = ts.createProgram(rootFiles, compilerOptions);
const checker = program.getTypeChecker();

// Resolved types, inferred types, cross-file references
// Correct closure variable tracking
// React/SolidJS prop type resolution
```

**What this adds over tree-sitter:**
- Full type resolution across files — inferred types, not just annotated ones
- Resolved function overloads
- Correct scope chain for closure variable detection
- React/SolidJS prop tracing — the TS compiler knows the type of `onClick` and can resolve where a callback flows
- Custom hook detection — `useQuery('/api/users')` resolved to its HTTP call via type signature

**Accuracy: ~70% (tree-sitter) → ~90% (TS compiler API)**

For plain JS files (no TypeScript), tree-sitter remains the fallback.

#### Ruby — RuboCop AST + Sorbet

Ruby has no compiler API equivalent. Layered approach:
- **RuboCop AST** — more accurate than tree-sitter for Ruby idioms, handles edge cases, understands Rails conventions
- **Prism parser** (Ruby core team) — new high-accuracy Ruby parser, better error recovery than tree-sitter
- **Sorbet integration** — if the codebase uses Sorbet type annotations, type-level accuracy comparable to TypeScript strict mode
- **Gemfile.lock parsing** — accurate gem vs. repo classification

Ruby remains the weakest language due to metaprogramming. The honest ceiling is ~75% with all upgrades. Document this limitation clearly.

**Accuracy: ~60% (tree-sitter) → ~75% (RuboCop AST + Sorbet where available)**

### Parser Strategy by Trigger

```
For each file:
  1. Detect language (extension + content)
  2. If Go + go/packages available:
       → Use compiler API for semantic analysis
       → Use tree-sitter for YAML pattern matching (DOM, framework patterns)
  3. If TypeScript + tsconfig.json present:
       → Use TS compiler API for type-resolved analysis
       → Use tree-sitter for YAML pattern matching
  4. If JavaScript (no tsconfig):
       → Tree-sitter only
  5. If Ruby:
       → Prism/RuboCop AST primary
       → Sorbet type data if .rbi files present
  6. If Templ:
       → a-h/templ/parser/v2 Visitor (unchanged)
```

### Cross-Service Linking — Constraint-Based Resolver

Replace simple string comparison with a multi-stage resolver:

**Stage 1: Path Normalization**

Normalize all URL patterns to a canonical wildcard form before matching:

| Source | Raw pattern | Normalized |
|--------|------------|------------|
| Chi | `/users/:id` | `/users/*` |
| Express | `/users/:id` | `/users/*` |
| Rails | `/users/:id` | `/users/*` |
| Chi group | `/api/v1` + `/users` | `/api/v1/users` |
| Regex route | `/users/[0-9]+` | `/users/*` |

**Stage 2: Constant Propagation for Base URLs**

```javascript
const BASE = '/api/v1';
axios.get(BASE + '/users');        // resolves to /api/v1/users
axios.get(`${BASE}/users/${id}`);  // resolves to /api/v1/users/* (partial)
```

Resolve up to 3-hop constant chains. Mark unresolvable chains as `confidence: partial`.

**Stage 3: OpenAPI / Swagger as Authoritative Override**

If `workspace.yaml` references an OpenAPI spec, use it as ground truth for endpoint shapes:

```yaml
# workspace.yaml
services:
  - name: nextgen-backend
    path: ./nextgen/backend
    language: ruby
    openapi: ./nextgen/backend/openapi.yaml   # ← authoritative endpoint source
```

OpenAPI-matched edges get `confidence: static` regardless of how the code expresses the URL. Static analysis fills gaps where the spec is missing or incomplete.

**Stage 4: Confidence Scoring**

| Match type | Confidence |
|-----------|------------|
| OpenAPI spec match | `static` |
| Literal string exact match | `static` |
| Normalized wildcard match | `inferred` |
| Base URL resolved + normalized | `inferred` |
| Partial constant propagation | `partial` |
| Unresolvable dynamic URL | `unknown` |

**Expected accuracy: ~65% (string match) → ~85% (constraint resolver + OpenAPI)**

### SCIP Interoperability

Polyflow outputs and optionally consumes **SCIP** (Semantic Code Intelligence Protocol — Sourcegraph's open standard for code graph interchange):

**Consuming SCIP:**

If a SCIP index already exists for a service (generated by `scip-typescript`, `scip-go`, etc.), Polyflow reads it instead of re-parsing. This avoids reinventing reference/definition tracking and gives immediate cross-language accuracy for definition/reference edges.

```yaml
# workspace.yaml
services:
  - name: nextgen-frontend
    path: ./nextgen/frontend
    language: typescript
    scip_index: ./.scip/frontend.scip   # ← consume existing index
```

**Producing SCIP:**

`polyflow index --output-scip` emits a SCIP index that can be consumed by Sourcegraph, IDEs, or any SCIP-compatible tool. Makes Polyflow composable with the broader ecosystem.

**What Polyflow adds on top of SCIP:**
- Cross-service HTTP/message broker edge linking (SCIP has no concept of service boundaries)
- DOM mutation tracking
- Datastar/SSE reactive flow
- AI agent context format
- Interactive graph visualization

SCIP handles the reference graph. Polyflow handles the **inter-service communication layer** on top.

### Optional Runtime Enrichment

An opt-in instrumentation mode that supplements static analysis with confirmed runtime call paths:

```bash
# Instrument and record during test execution
polyflow record --services "dsw-manager,dsw-agent" -- go test ./...
polyflow record --services "nextgen-backend" -- bundle exec rspec

# Merges runtime observations into the static graph
polyflow index --merge-runtime
```

**How it works:**
- Injects lightweight tracing (function entry/exit, HTTP calls, message publish/subscribe)
- Records actual call paths to `.polyflow/runtime.db`
- Merges with static graph: static edges marked `source: static`, runtime-confirmed edges marked `source: runtime` or `source: both`

**What runtime enrichment adds:**
- Confirms which static edges are actually exercised
- Catches dynamic dispatch that static analysis misses (Ruby metaprogramming, JS runtime polymorphism)
- Identifies dead code (static edges never seen at runtime)
- Resolves `confidence: unknown` edges where static analysis couldn't determine the target

**What static analysis still provides that runtime can't:**
- Dead code and untested paths (visible even if never executed)
- Full call graph without running every branch
- Zero setup — no instrumentation, no test suite required
- Cross-service tracing without instrumenting every service

Runtime enrichment is a supplement, not a replacement. Static analysis is always the primary.

### Origin Classification (Framework vs. Repo)

Every function call is classified by import origin:

| Origin | Detection | Example |
|--------|-----------|---------|
| `repo` | Import path matches workspace service, or relative import | `../handlers/user.go` |
| `stdlib` | Known stdlib package list per language | `net/http`, `fs`, `json` |
| `framework` | Import from `node_modules`, Go external module, Gemfile gem | `axios`, `chi`, `faraday` |
| `unknown_global` | No import (script-tag globals) — matched against known-globals registry | `$` (jQuery), `htmx`, `_` (lodash) |

Known-globals registry is a built-in YAML list, user-extensible:

```yaml
# built-in known globals
known_globals:
  - { name: "$",      package: jquery,  origin: framework }
  - { name: "jQuery", package: jquery,  origin: framework }
  - { name: "_",      package: lodash,  origin: framework }
  - { name: "htmx",   package: htmx,    origin: framework }
  - { name: "React",  package: react,   origin: framework }
  - { name: "Datastar", package: datastar, origin: framework }
```

Framework calls become **boundary nodes** in the graph — the edge exits your code but does not descend into the framework's internals. HTTP/message patterns then take over at the boundary.

### Event Listener Classification

Distinguish between fundamentally different listener types to avoid conflating them:

| Edge type | Meaning | Example |
|-----------|---------|---------|
| `dom_listen` | Confirmed DOM event listener on a known element | `el.addEventListener('click', fn)` with resolved `el` |
| `handles_event` | Framework-level binding where DOM attachment is abstracted | React `onClick={fn}`, SolidJS `on:click={fn}` |
| `app_event_subscribe` | Application-level event bus subscription | `emitter.on('user:created', fn)` |
| `app_event_publish` | Application-level event bus publish | `emitter.emit('user:created', data)` |

`dom_listen` edges require a resolvable element target. `handles_event` edges record the component + handler without claiming a DOM element. This prevents false-precision claims about React's synthetic event system.

### Revised Accuracy Estimates (Upgraded Design)

| Language | v1 (tree-sitter) | v1.5+ (compiler APIs) | Ceiling |
|----------|-----------------|----------------------|---------|
| Go | ~80% | ~95% | Competitive with CodeQL |
| TypeScript (strict) | ~70% | ~90% | Competitive with Sourcegraph |
| JavaScript | ~65% | ~70% | Tree-sitter only, marginal gain |
| Ruby | ~60% | ~75% | Prism + Sorbet where available |
| Cross-service HTTP | ~65% | ~85% | Better than any static tool |
| Message brokers | ~85% | ~90% | String literals → already high |
| DOM tracking | ~80% | ~85% | Marginal gain |
| Event listeners (scoped) | ~60% | ~80% | Compiler scope resolution helps |

### Competitive Position (Upgraded)

| Capability | Upgraded Polyflow | Closest competitor |
|------------|------------------|--------------------|
| Go call graph accuracy | ~95% | Kythe (~95%), CodeQL (~97%) |
| TS/JS call graph accuracy | ~90% | Sourcegraph SCIP (~92%) |
| Cross-service HTTP linking | ~85% | AppMap (runtime, ~95%) |
| Message broker linking | ~90% | Nobody (static) |
| DOM mutation tracking | ~85% | Nobody |
| Full E2E flow (click → DB → message → DOM) | Yes | Nobody |
| Single binary, zero infrastructure | Yes | Nobody (all SaaS or heavy setup) |
| AI agent pre-computed context | Yes | Nobody |

### Build Priority for Upgraded Design

| Phase | Deliverable | Impact |
|-------|-------------|--------|
| v1 | Tree-sitter parsing, YAML patterns, SQLite graph, basic HTTP linking | Working MVP |
| v1.5 | Go compiler API (`golang.org/x/tools`) | Largest accuracy jump, Go is primary language |
| v1.5 | OpenAPI integration for cross-service linking | Fixes linking for teams with specs |
| v1.6 | TypeScript compiler API | Covers React/SolidJS/Node properly |
| v1.7 | SCIP output + consumption | Ecosystem composability |
| v2 | Ruby Prism + Sorbet integration | Improves weakest language |
| v2 | Runtime enrichment (opt-in) | Closes dynamic dispatch gap |

---

## CLI Commands

| Command | Purpose |
|---------|---------|
| `polyflow init` | Auto-discover services (go.work/go.mod, package.json incl. npm/yarn workspaces + Nx project.json, Gemfile; yarn portal:/link: deps become link hints) and write `workspace.yaml` with relative paths. `--interactive` for the manual prompt flow, `--force` to overwrite |
| `polyflow index` | Parse all services, build/update graph in `.polyflow/graph.db` |
| `polyflow serve` | Start local HTTP server, open browser for graph visualization |
| `polyflow search <query>` | Terminal search — print matching nodes (file, function, line) |
| `polyflow status` | Workspace info: services, last indexed, file/node/edge counts |
| `polyflow patterns list` | List all registered detection patterns |
| `polyflow patterns add <file>` | Register a custom YAML pattern file |
| `polyflow context --target <query> --task <type>` | Generate AI agent-optimized context (JSON) |
| `polyflow impact --target <query>` | Show blast radius of a change |
| `polyflow deps [--service <n>]` | List resolved dependency versions per service |
| `polyflow config service add --name <n> --path <p> --language <l>` | Add a service to workspace |
| `polyflow config service remove --name <n>` | Remove a service |
| `polyflow config service list` | List configured services |
| `polyflow config link add --from <s1> --to <s2> --via <type>` | Add cross-service link hint |
| `polyflow config link remove --from <s1> --to <s2>` | Remove a link hint |
| `polyflow config exclude add <pattern>` | Add an exclude glob pattern |
| `polyflow config exclude remove <pattern>` | Remove an exclude pattern |
| `polyflow config show` | Display current workspace configuration |
| `polyflow config set <key> <value>` | Set a config option (e.g., `snippet_lines 50`) |

**Typical workflow:**
```bash
polyflow init              # one-time setup
polyflow index             # build the graph
polyflow serve             # explore visually at http://localhost:9400
```

**CLI Output Formats:**

```
$ polyflow index
Scanning services...
  dsw-manager: 1,247 files (Go, Templ)
  dsw-agent: 312 files (Go)
  nextgen-frontend: 89 files (JS/TS)
  nextgen-backend: 523 files (Ruby)

Indexing [████████████░░░░░░░░] 67% (1,456/2,171 files)
  Parsing: 12 workers active
  Errors: 3 files (partial data extracted)

Done. 2,171 files indexed in 18.4s
  Nodes: 34,521 | Edges: 87,203 | Links: 142 cross-service
  Errors: 3 files (run `polyflow status --errors` for details)
```

```
$ polyflow search "CreateUser"
  FUNCTION  CreateUser()          internal/handlers/user.go:42    [dsw-manager]
  FUNCTION  CreateUserRequest     internal/models/user.go:15      [dsw-manager]
  CLASS     CreateUserWorker      app/workers/user_worker.rb:3    [nextgen-backend]
```

```
$ polyflow status
  Workspace: sycamore-dsw
  Services: 4 (2 Go, 1 JS, 1 Ruby)
  Last indexed: 2026-07-06 14:32:01 (5 minutes ago)
  Files: 2,171 | Nodes: 34,521 | Edges: 87,203
  Cross-service links: 142
  Parse errors: 3 files (--errors for details)
```

```
$ polyflow status --errors
  PARTIAL  dsw-manager/internal/handlers/broken.go:17    (1 error)
  PARTIAL  nextgen-backend/app/models/legacy.rb:45       (2 errors)
  PARTIAL  nextgen-frontend/src/utils/old.js:8           (1 error)
```

---

## Graph Data Model

### Node Types

| Node Type | Example | Metadata |
|-----------|---------|----------|
| Service | `user-service` | Language, repo path |
| File | `src/handlers/user.go` | Path, language |
| Function/Method | `CreateUser()` | Signature, line number, file, params, return type |
| Class/Struct | `UserController` | Fields, methods list, inheritance |
| Module/Mixin | `Authenticatable` | Included by |
| Interface | `Handler` | Methods required |
| Variable | `dbTimeout` | Type, scope, value if constant |
| External Endpoint | `POST /api/users` | HTTP method, path |
| Message Channel | `user.created` (RabbitMQ topic) | Broker type, queue/topic name |
| Datastore | `postgres`, `sqlite` | kind (store/call), engine, driver(s), orm. Dual drivers for one engine (modernc + mattn SQLite) merge into ONE store node with driver metadata |
| External Service | AWS S3, Bedrock | Provider, service, SDK package + resolved version |
| DOM Target | `#user-list`, `.modal` | Selector string, element type, file where defined |

### Edge Types

| Edge Type | Meaning |
|-----------|---------|
| `calls` | Function A invokes Function B |
| `imports` | File A imports File B |
| `http_request` | Function sends HTTP request to endpoint |
| `http_handler` | Function handles an HTTP route |
| `publishes` | Function publishes to message channel |
| `subscribes` | Function subscribes to message channel |
| `reads` | Function reads a variable |
| `writes` | Function mutates a variable |
| `contains` | Service contains File, File contains Function, Class contains Method |
| `extends` | Class inherits from parent class |
| `includes` | Class/module includes a mixin |
| `implements` | Struct/class implements an interface |
| `has` | Struct/class has a field of another type (composition) |
| `sse_endpoint` | Go handler serves SSE stream |
| `datastar_action` | Templ element triggers backend call via `data-on-*="@get('/path')"` |
| `datastar_bind` | Templ element binds to a signal/fragment |
| `job_enqueue` | Code enqueues a background job (delayed_job `.delay`/`handle_asynchronously`/`Delayed::Job.enqueue`, ActiveJob `perform_later`, Sidekiq `perform_async`); the linker connects the enqueue call site to the job class's `perform` method by class name |
| `job_perform` | Job class processes the job (`def perform` in ApplicationJob subclass, Sidekiq worker) |
| `pusher_trigger` | Code triggers a Pusher event; the linker connects server-side `Pusher.trigger` to pusher-js `subscribe` sites on the same literal channel name, across services |
| `pusher_subscribe` | Client subscribes to Pusher channel |
| `queries` | Code reads from a datastore (GORM chains, database/sql Query*) |
| `persists` | Code writes to a datastore (Create/Save/Delete, Exec*) |
| `cloud_call` | Code calls an external cloud service via an SDK (S3, Bedrock) — carries SDK package + resolved version |
| `ws_upgrade` / `ws_connect` | Server upgrades HTTP to WebSocket / client opens one |
| `ws_read` / `ws_write` / `ws_send` | WebSocket pumps and typed sends; `ws_send` carries the message type and links to matching dispatch cases across services |
| `hub_subscribe` / `hub_broadcast` | SSE broadcast-hub channel fan-out (Subscribe/Unsubscribe/Broadcast) feeding per-connection SSE writers; the linker connects each `Broadcast()` call site to the `Subscribe()` call sites in the same service (partial confidence when a service has multiple hub types) |

Edge types are **extensible** — new YAML patterns can define new edge types without modifying core code.

### DOM-Related Edge Types

| Edge Type | Meaning |
|-----------|---------|
| `dom_read` | Function reads from a DOM element |
| `dom_write` | Function mutates a DOM element (innerHTML, textContent, style, classList, etc.) |
| `dom_create` | Function creates new DOM nodes |
| `dom_remove` | Function removes DOM nodes |
| `dom_listen` | Function attaches event listener to element |

### DOM Target Node Type

| Node Type | Example | Metadata |
|-----------|---------|----------|
| DOM Target | `#user-list`, `.modal`, `form[data-id]` | Selector string, element type, file where defined |

---

## Data Flow Tracking (Params Passed vs. Expected)

Every edge in the graph carries metadata about **what data flows through it** — what the caller passes and what the receiver expects.

### Edge Data Flow Attributes

| Edge Attribute | Description |
|----------------|-------------|
| `params_passed` | List of arguments/payload keys at the call site |
| `params_expected` | List of parameters/fields the receiver declares |
| `type_info` | Type annotations where available (Go struct, TS interface, Ruby strong params) |
| `confidence` | Detection confidence level (see below) |
| `mismatch` | Flag when passed doesn't match expected (v2 feature) |

### Data Flow by Context

| Context | "What's passed" (caller) | "What's expected" (receiver) |
|---------|--------------------------|------------------------------|
| Function call | Arguments at call site | Parameters in signature |
| HTTP request | Request body/query params | Handler's parsed params or struct tags |
| RabbitMQ publish | Message payload | Consumer's unmarshal target struct |
| Sidekiq | Job args: `perform_async(user_id, "welcome")` | Worker's `perform(user_id, template)` signature |
| Datastar SSE | Signal data: `MergeSignals({username: "john"})` | Templ binding: `data-bind="username"` |
| Pusher | Event payload | Client subscription handler params |

### Detection Approach by Language

| Language | How to extract "passed" | How to extract "expected" |
|----------|------------------------|--------------------------|
| Go | Call site argument expressions, struct literal fields | Function signature params, struct field tags (`json:"name"`) |
| JS/TS | Object literals in request body, function call args | Function params, destructuring, TypeScript interfaces |
| Ruby | Hash arguments, strong params in caller | `params.require().permit()`, method signature, keyword args |
| Templ/Datastar | Signal values in `data-signals`, attributes | Go handler reading from signals/request |

### Edge Detail Panel (On Click)

Clicking an edge shows the data flow:

```
Edge: dsw-manager#CreateHandler → POST /api/users → nextgen-backend#UsersController#create

Passed (caller):
  {
    name: string,
    email: string,
    role_id: int
  }

Expected (receiver):
  params.require(:user).permit(:name, :email, :role_id, :department)

Mismatch: receiver expects :department but caller doesn't send it
```

### Confidence Levels

Static analysis cannot resolve all dynamic data flow. Each edge's data carries a confidence level:

| Level | Meaning | Example |
|-------|---------|---------|
| `static` | Extracted from literal/typed source (high trust) | `{name: "john"}`, Go struct literal |
| `inferred` | Resolved via constant propagation or type inference | `KEY = "name"; obj[KEY] = v` |
| `partial` | Some keys detected, others dynamic | Spread operator, loop-built payload |
| `unknown` | Fully dynamic, cannot determine statically | `obj[variable]`, metaprogramming |

**UI display with confidence:**
```
Passed (caller) [confidence: partial]:
  ✓ name: string        ← literal key
  ✓ email: string       ← literal key
  ⚠ ...dynamic keys     ← computed/spread detected but unresolvable
```

### Static Analysis Coverage by Language

| Language | v1 tree-sitter | v1.5+ compiler API | Why |
|----------|---------------|-------------------|-----|
| Go | ~80% | ~95% | Compiler API resolves interface dispatch, closures, cross-package calls |
| TypeScript (strict) | ~70% | ~90% | TS compiler resolves inferred types, prop flows, overloads |
| JavaScript | ~65% | ~70% | No type info; tree-sitter remains primary; marginal gain from scope analysis |
| Ruby | ~60% | ~75% | Prism + Sorbet where available; metaprogramming is a hard ceiling |

### What Static Analysis CAN Detect (Reliable)

| Pattern | Example |
|---------|---------|
| Literal keys | `{name: "john", email: x}` |
| Strong params (Rails) | `params.require(:user).permit(:name, :email)` |
| Struct literals (Go) | `User{Name: "x", Age: 5}` |
| TypeScript interfaces | `interface CreateUserReq { name: string }` |
| Function signatures | `def create(name:, email:)` |
| Destructuring | `const { name, email } = req.body` |
| JSON struct tags (Go) | `json:"user_id"` |

### What Static Analysis CANNOT Detect (Limits)

| Pattern | Example | Why |
|---------|---------|-----|
| Computed keys | `obj[variableName] = value` | Runtime-determined |
| Spread/merge | `{...baseParams, ...extraParams}` | Need to trace origin |
| Metaprogramming (Ruby) | `send(:"#{action}_user", params)` | Runtime string |
| Dynamic hash building | `hash = {}; fields.each { |f| hash[f] = val }` | Loop-built |
| `method_missing` | Ruby magic methods | No static signature |
| Reflection (Go) | `reflect.ValueOf(x).Field(i)` | Runtime resolved |
| Middleware transforms | Express middleware modifying `req.body` | Multi-hop flow |
| Serializer gems | `ActiveModelSerializer`, `jbuilder` | Indirection layer |

### Annotation Escape Hatch

For cases static analysis can't resolve, users can annotate with `# polyflow:params`:

```ruby
# polyflow:params {user_id: integer, action: string, metadata: hash}
channel.publish(exchange, routing_key, build_message(user))
```

```javascript
// polyflow:params {userId: number, filters: object}
axios.post('/api/search', buildSearchPayload(userId, filters));
```

```go
// polyflow:params {user_id: int, name: string, roles: []string}
ch.Publish(exchange, key, buildMsg(user))
```

### Schema File Detection

If the workspace contains API contract files, polyflow uses them as authoritative data shape sources:

| File Type | Usage |
|-----------|-------|
| OpenAPI/Swagger specs | Extract request/response schemas per endpoint |
| JSON Schema files | Map to struct/interface shapes |
| Protobuf `.proto` files | Extract message field definitions (v2) |

### v1 Data Flow Scope

1. Literal/typed extraction (covers Go well, TS well, Ruby/JS partially)
2. Constant propagation (resolves simple variable → value chains)
3. Confidence labeling on every edge
4. `# polyflow:params` annotation escape hatch
5. Schema file detection (OpenAPI/JSON Schema if present)

### v2 Data Flow Additions

- Intra-function data flow analysis (trace variable assignments)
- Serializer parsing (jbuilder, ActiveModelSerializer)
- Spread/merge resolution for JS/TS
- Mismatch detection (caller vs receiver disagreement)

---

## DOM Manipulation Detection

Any code that reads or mutates the DOM is tracked in the graph. This catches unexpected/"random" DOM updates and lets users trace which functions touch which elements.

### Detected DOM Patterns

| Category | Patterns | Example |
|----------|----------|---------|
| Direct element access | `document.getElementById`, `querySelector`, `querySelectorAll` | `document.getElementById('user-list')` |
| Property mutation | `.innerHTML`, `.textContent`, `.value`, `.style.*`, `.className`, `.classList.*` | `el.classList.add('active')` |
| Attribute mutation | `.setAttribute`, `.removeAttribute`, `.dataset.*` | `el.setAttribute('disabled', true)` |
| DOM tree mutation | `.appendChild`, `.removeChild`, `.replaceChild`, `.insertBefore`, `.remove()`, `.append`, `.prepend` | `container.appendChild(newNode)` |
| Element creation | `document.createElement`, `.cloneNode`, `.insertAdjacentHTML` | `document.createElement('div')` |
| Event listener binding | `.addEventListener`, `.removeEventListener`, `.onclick =` | `btn.addEventListener('click', handler)` |
| jQuery (legacy) | `$('#id').html()`, `$('.class').show()`, `$.append()` | `$('#modal').hide()` |
| Datastar signal-driven | `data-text`, `data-show`, `data-class`, `data-attr` | Implicit DOM mutation via reactive binding |

### Linking DOM Targets to Source HTML

The tool connects JS DOM access to the actual HTML element definition:

1. **Extract DOM selectors from JS** — Parse `getElementById('x')`, `querySelector('.y')` → extract selector strings
2. **Extract IDs/classes from HTML/Templ** — Parse all `id=`, `class=`, `data-*` attributes in `.templ` files
3. **Match selectors to elements** — Link JS DOM access to the HTML element where it's defined

**Graph representation:**
```
[JS Function: updateUserList()]
    │
    ├── dom_write → [DOM Target: #user-list]
    │                     │
    │                     └── defined_in → [Templ: users.templ:42]
    │
    └── dom_read → [DOM Target: #status-badge]
                         │
                         └── defined_in → [Templ: layout.templ:15]
```

### DOM Node Detail Panel

Searching for a DOM element (e.g., `#user-list`) shows:

```
[DOM Target: #user-list]
  Defined in: users.templ:42

  Written by:
    ├── updateUserList() at app.js:15        (innerHTML = ...)
    ├── clearUsers() at app.js:30            (innerHTML = "")
    └── Datastar: data-on-click="@get('/users')" → patches via SSE

  Read by:
    └── getUserCount() at utils.js:8         (children.length)

  Event listeners:
    └── click → handleUserClick() at app.js:45
```

### Dynamic DOM Selector Confidence

| Pattern | Example | Confidence |
|---------|---------|------------|
| Static selector string | `getElementById('user-list')` | `static` — literal match |
| Template literal | `` querySelector(`#user-${id}`) `` | `partial` — pattern `#user-*` detected |
| Variable selector | `querySelector(selector)` | `inferred` if constant propagation resolves, else `unknown` |
| Programmatic ID | `el.id = 'item-' + index` | `partial` — pattern detection |
| Datastar reactive | `data-text="$username"` | `static` — declarative, fully parseable |
| innerHTML with HTML | `el.innerHTML = '<div class="x">...'` | `partial` — parse string as HTML |

### DOM Annotation Escape Hatch

For dynamic selectors that can't be resolved:

```javascript
// polyflow:dom #user-${id}
element.textContent = newValue;

// polyflow:dom .dynamic-card
container.querySelector(selectorVar).remove();
```

### Built-in DOM Patterns (v1)

Added to JavaScript/TypeScript pattern files:

| Pattern File | Detects |
|--------------|---------|
| `js/dom_access.yaml` | getElementById, querySelector, querySelectorAll |
| `js/dom_mutation.yaml` | innerHTML, textContent, classList, style, setAttribute |
| `js/dom_tree.yaml` | appendChild, removeChild, insertBefore, append, prepend, remove |
| `js/dom_events.yaml` | addEventListener, removeEventListener, on* properties |
| `js/dom_create.yaml` | createElement, cloneNode, insertAdjacentHTML |
| `js/jquery.yaml` | jQuery selectors and DOM manipulation (legacy support) |
| `internal/parser/templ.go` | Extract all id, class, data-* from templ elements (Go-native, no YAML) |

---

## Function Detail Panel (On Click)

When a user clicks a function/method node:

| Info | Source |
|------|--------|
| File path + line number | AST extraction |
| Function signature | Params, return type |
| Variables used (external) | Module-level vars, constants, config, injected deps |
| Variables modified | Anything written/mutated inside function |
| Calls made (outgoing) | Other functions, HTTP, messages |
| Called by (incoming) | Callers, HTTP handlers, message subscribers |
| Source snippet | Adaptive: full source if ≤ `snippet_lines` (default 30, configurable), truncated with "show more" beyond. AI agent output always includes full source. |

**Variable classification:**

| Type | Example | Detection |
|------|---------|-----------|
| Local | `x := 5` | Declared inside function scope |
| Parameter | `func(ctx, userID)` | Function signature |
| Closure/outer scope | `dbConn` | Referenced but not declared in function |
| Package/module level | `var timeout = 30` | Declared at file/package scope |
| Imported | `http.StatusOK` | From import statements |

---

## OOP Class Handling

Classes/structs render as **compound/group nodes** in Cytoscape.js, visually containing their methods.

**Clicking a class node shows:**
- All methods (public/private/protected)
- Instance variables / fields
- Class-level variables
- Inheritance chain (parent classes, included modules)
- Interfaces satisfied (Go)

**Example rendering:**
```
[UserController] (compound node)
  ├── #create    → calls Sidekiq worker, publishes to Pusher
  ├── #update    → calls Faraday to external service
  └── #destroy   → publishes to RabbitMQ

  Extends: ApplicationController
  Includes: Authentication, Authorization
  Instance vars: @user, @params
```

---

## Cross-Service Linking

**Strategy**: Auto-detect + user hints (hybrid approach)

### Auto-Detection
- Extract endpoint strings from HTTP clients (`axios.post('/api/users')`)
- Extract route definitions from HTTP servers (`router.HandleFunc("/api/users", handler)`)
- Match by URL path string comparison
- Covers ~70-80% of cases (static string routes)

### User Hints (for ambiguous connections)
Defined in `workspace.yaml` under `links:`:
```yaml
links:
  - from: dsw-manager
    to: dsw-agent
    via: rabbitmq
    exchange: "dsw.builds"   # connects publishers/subscribers whose exchange
                             # is not statically resolvable, via a shared
                             # channel node (confidence: static)

  - from: nextgen-frontend
    to: nextgen-backend
    base_url: "/api"
```

---

## Flow Tracing UX (Core Feature)

1. **Search** — User types query (function name, file, route, class). FTS5 prefix matching + Go re-ranking (exact > case-insensitive > prefix length > node type priority). Results appear as-you-type with 200ms debounce.
2. **Root selection** — User picks a node from search results.
3. **Flow direction** — Choose:
   - **Forward** (downstream): "What does this trigger?"
   - **Backward** (upstream): "What triggers this?"
   - **Both**: Full bidirectional flow
4. **Depth control** — Max depth (default unlimited, collapsible levels).
5. **Isolated subgraph** — Only connected nodes shown, everything else hidden.

**Additional interactions:**
- Click node → detail panel (source snippet, metadata, variables)
- Click edge → pattern info ("axios.post detected at auth.js:42")
- Filter by service, language, or edge type
- Pin multiple flows and overlay/compare

**Layout strategy** (context-dependent, user can toggle via dropdown):

| View | Layout | Why |
|------|--------|-----|
| Flow trace (forward/backward) | Dagre (left-to-right) | Directional, shows cause → effect |
| Full service overview | fcose (force-directed) | Clusters related nodes, handles compound nodes |
| Class internals (expanded) | Dagre (top-down) | Methods flow downward |
| DOM impact view | Breadthfirst | Clear depth levels from trigger to DOM target |

Search and filter operate uniformly across all layouts — filtering hides non-matching nodes and re-runs the active layout on the remaining subgraph. Searching highlights matching nodes in-place without changing layout.

**State management:**
- **URL params** (shareable, bookmarkable): `?root=node_123&direction=forward&depth=5&layout=dagre-lr`, `?search=CreateUser`, `?filter=service:dsw-manager,type:function`
- **localStorage** (per-user preferences): pinned flows, default layout, detail panel state, recently searched terms
- Sharing a URL gives the recipient the same trace view. UI preferences persist across sessions.

---

## Workspace Configuration Schema

```yaml
# workspace.yaml
name: "my-platform"
version: 1

services:
  - name: dsw-manager
    path: ./dsw-manager
    language: go
    frameworks: [chi, datastar, templ, amqp091]  # optional, auto-detected if omitted

  - name: dsw-agent
    path: ./dsw-agent
    language: go
    frameworks: [amqp091]

  - name: nextgen-frontend
    path: ./nextgen/frontend
    language: javascript
    frameworks: [axios]

  - name: nextgen-backend
    path: ./nextgen/backend
    language: ruby
    frameworks: [rails, sidekiq, bunny, pusher]

# User hints for cross-service linking
links:
  - from: dsw-manager
    to: dsw-agent
    via: rabbitmq
    exchange: "dsw.builds"

  - from: nextgen-frontend
    to: nextgen-backend
    base_url: "/api"

# Custom pattern files (beyond built-ins)
patterns:
  - ./custom-patterns/pusher.yaml

# Indexing settings
index:
  exclude:
    - "**/vendor/**"
    - "**/node_modules/**"
    - "**/*_test.go"
    - "**/spec/**"

# UI/display settings
settings:
  snippet_lines: 30          # max lines shown before truncation (configurable)
  default_layout: dagre-lr   # dagre-lr, dagre-tb, fcose, breadthfirst
  default_depth: 5           # default trace depth
  port: 9400                 # serve port
```

---

## YAML Pattern Registry Format

Patterns are declarative, user-extensible, and each MUST have test fixtures.

**Example pattern:**
```yaml
# patterns/go/chi_routes.yaml
name: chi_routes
language: go
category: http_handler
description: "Detect Chi router route registrations"
priority: 10  # 1-5: generic built-in, 6-10: framework-specific, 11+: user-defined

patterns:
  - name: chi_method_route
    # Tree-sitter query (S-expression)
    query: |
      (call_expression
        function: (selector_expression
          operand: (identifier) @router
          field: (field_identifier) @method)
        arguments: (argument_list
          (interpreted_string_literal) @path
          (identifier) @handler))
    match:
      method: [Get, Post, Put, Patch, Delete, Head, Options]
    extract:
      node_type: http_handler
      edge_type: http_handler
      attributes:
        http_method: "@method"
        path: "@path"
        handler: "@handler"

  - name: chi_route_group
    query: |
      (call_expression
        function: (selector_expression
          operand: (identifier) @router
          field: (field_identifier) @method)
        arguments: (argument_list
          (interpreted_string_literal) @path
          (func_literal) @group_fn))
    match:
      method: [Route, Group]
    extract:
      node_type: route_group
      attributes:
        path: "@path"
```

**Example test fixture structure:**
```
patterns/go/
├── chi_routes.yaml
└── chi_routes_test/
    ├── input.go              # Sample Chi route code
    └── expected.json         # Expected nodes and edges
```

Every YAML pattern file is **required** to have a corresponding test fixture directory. CI fails if fixtures are missing.

---

## Version-Aware Pattern Matching

Different versions of the same package can have materially different call-site
shapes (proof case: AWS SDK for Go v1 vs v2 — session-based `s3.New(sess)` +
context-less calls vs config-based `s3.NewFromConfig(cfg)` + context-first
calls). A pattern written for one silently misses or misfires on the other.

**Dependency resolution (`internal/deps`)**: at index time, every service's
exact installed versions are resolved from `go.mod`, `package.json` + lockfile
(`package-lock.json` v1–v3 or `yarn.lock` classic/berry — the resolved version,
not the semver range; `dependencies` vs `devDependencies` recorded as
`kind: prod|dev`), and `Gemfile.lock`. Stored in a `dependencies` table
(`service, ecosystem, name, version, kind`) and queryable via `polyflow deps
[--service X]`.

**Pattern gating**: pattern YAML files may declare a top-level gate:

```yaml
# patterns/go/aws_s3_v1.yaml
language: go
package: github.com/aws/aws-sdk-go
version_range: ">=1.0.0 <2.0.0"   # Masterminds semver syntax
```

The registry activates the file's patterns for a service only when that
service depends on `package` and its resolved version satisfies
`version_range` (`package` alone = presence check). Where call shapes diverge
across a major version, ship separate pattern files per range (aws_s3_v1 /
aws_s3_v2) rather than one pattern matching both. Unparseable versions fail
closed. This is a registry capability — nothing is hardcoded per package.

**Metadata**: nodes produced by gated patterns carry `package` and
`resolved_version` in metadata, surfaced in the UI and agent JSON so "this S3
upload uses SDK v1" is answerable without reading code.

**Fixtures**: version-gated patterns must ship a same-shape-wrong-version
negative fixture — a v1-shaped call must produce zero matches under the v2
pattern file and vice versa.

---

## Datastar/SSE Pattern Detection

### Client-side (Templ HTML attributes)

| Attribute | Meaning | Edge Created |
|-----------|---------|--------------|
| `data-on-click="@get('/users')"` | Click triggers GET to /users | `datastar_action` → Go handler at /users |
| `data-on-submit="@post('/users')"` | Submit triggers POST | `datastar_action` → Go handler |
| `data-bind="username"` | Two-way bind to signal | `datastar_bind` |
| `data-signals="{...}"` | Initialize signals | `datastar_bind` |
| `data-text="$username"` | Reactive text | `reads` signal variable |
| `data-indicator="$loading"` | Loading state | `reads` signal variable |

### Server-side (Go Datastar SDK)

| Pattern | Meaning | Edge Created |
|---------|---------|--------------|
| `datastar.MergeFragments(...)` | Send HTML fragment via SSE | `sse_endpoint` |
| `datastar.MergeSignals(...)` | Send signal update via SSE | `sse_endpoint` |
| `datastar.PatchElements(...)` | Patch DOM elements | `sse_endpoint` |
| `sse.Handler(...)` | Register SSE handler | `sse_endpoint` |

---

## Built-in Pattern Coverage (v1)

### Go

| Category | Patterns |
|----------|----------|
| HTTP routes | Chi (`r.Get`, `r.Post`, `r.Route`, `r.Group`), net/http (`HandleFunc`, `Handle`) |
| HTTP clients | `net/http` (http.Get, http.Post, client.Do), Resty |
| RabbitMQ | `amqp091-go` (channel.Publish, channel.Consume, channel.QueueDeclare) |
| Datastar SSE | `datastar.MergeFragments`, `datastar.PatchElements`, `datastar.MergeSignals` |
| Goroutines | `go func()`, `go methodCall()` |

### JavaScript/TypeScript

| Category | Patterns |
|----------|----------|
| HTTP clients | `axios.get/post/put/patch/delete`, `fetch()`, `XMLHttpRequest` |
| Datastar actions | `@get()`, `@post()`, `@put()`, `@patch()`, `@delete()` in `data-on-*` |
| Datastar bindings | `data-bind`, `data-signals`, `data-text`, `data-computed` |
| DOM access | `getElementById`, `querySelector`, `querySelectorAll` |
| DOM mutation | `innerHTML`, `textContent`, `classList.*`, `style.*`, `setAttribute` |
| DOM tree | `appendChild`, `removeChild`, `insertBefore`, `append`, `prepend`, `remove` |
| DOM events | `addEventListener`, `removeEventListener`, `on*` property assignment |
| DOM creation | `createElement`, `cloneNode`, `insertAdjacentHTML` |
| jQuery (legacy) | `$()` selectors, `.html()`, `.show()`, `.hide()`, `.append()` |

### Templ (Go-native extraction, no YAML patterns)

Templ files are parsed using `github.com/a-h/templ/parser/v2` Visitor interface — not tree-sitter. Extraction logic lives in `internal/parser/templ.go`.

| Category | What's Extracted |
|----------|-----------------|
| Datastar attributes | All `data-on-*` action triggers with `@verb('/path')` |
| HTML links | `href`, `action` attributes pointing to routes |
| DOM elements | All `id=`, `class=`, `data-*` attributes (for DOM target nodes) |
| Component signatures | `templ ComponentName(params)` → function nodes |

### Ruby

| Category | Patterns |
|----------|----------|
| HTTP routes | Rails (`get`, `post`, `resources`, `namespace`, controller actions) |
| HTTP clients | `Net::HTTP`, `Faraday`, `HTTParty`, `RestClient` |
| RabbitMQ | Bunny (`queue.publish`, `queue.subscribe`, `exchange.publish` with routing_key) — dependency-gated on the bunny gem |
| Jobs | delayed_job (`.delay`, `handle_asynchronously`, `Delayed::Job.enqueue`), ActiveJob (`perform_later`, `def perform`), solid_queue adapter config; Sidekiq kept only for legacy evidence |
| Pusher | `Pusher.trigger`/`trigger_async` (server), `pusher-js` `subscribe`/`bind` (client, patterns/javascript/pusher.yaml) |
| AWS S3 | `Aws::S3::Client` operations, `upload_file`/`download_file` |

---

## Performance Design

| Concern | Strategy |
|---------|----------|
| Parsing speed | Bounded worker pool (`GOMAXPROCS` goroutines), one file per goroutine |
| Memory | Stream-process: parse file → extract → write to DB → release AST |
| SQLite writes | Batch inserts in transactions (1000 rows/batch), WAL mode |
| Pattern matching | Pre-compile Tree-sitter queries at startup, reuse per file |
| Graph queries | Indexed SQLite columns (node ID, type, service). BFS/DFS with depth limit |
| Large codebases | Incremental indexing: file-scoped re-index + targeted re-linking of changed endpoints only. Full re-link via `--full` flag |
| Frontend | Lazy-load subgraphs, never send full graph to browser |

### Performance Targets (v1)

| Metric | Target |
|--------|--------|
| Files indexed | 100k+ without OOM |
| Index time (cold, 10k files) | < 30 seconds |
| Incremental re-index (100 changed files) | < 3 seconds |
| Graph query (trace, depth 10) | < 200ms |
| Serve startup | < 1 second |

**Measured (Phase 12, M-series laptop, synthetic synergy/nextGen-shaped
workspace — 4 go.work modules + 3 JS apps + Rails-sized Ruby tree; see
`internal/e2e/bench_test.go`, sizes via `POLYFLOW_BENCH_FILES`):**

| Metric | 1,200 files | 10,000 files | Target |
|--------|------------|--------------|--------|
| Cold index | 4.2s | 19.3s | ✓ (< 30s at 10k) |
| Re-index, nothing changed | 31ms | 213ms | ✓ (no-change fast path: workspace fingerprint match skips the rebuild) |
| Re-index, 100 files changed | 2.1s | 15.9s | ✓ at 1.2k; ✗ at 10k |

The 100-changed miss at 10k scale is architectural: incremental indexing
skips *parsing* but still rebuilds the whole graph DB into a tmp file for the
atomic swap (correctness identical to a full rebuild by construction), so the
floor is O(graph), not O(change). Hitting < 3s at 10k requires in-place
incremental DB updates (delete/reinsert per changed file + derived-edge
refresh) — deliberate follow-up, not attempted in v1. Phase 12 also removed
two superlinear costs that dominated before measurement: `DELETE FROM
nodes_fts WHERE id=?` full-FTS-table scans (O(n²) across a build — now
skipped on fresh builds via an in-memory seen-set) and per-row SQL
re-preparation in the batch writer.

### Pipeline Architecture

```
FileReader → Parser → Matcher → GraphWriter
    (channel)   (channel)  (channel)
```

Each stage communicates via Go channels, providing natural backpressure and bounded memory usage.

---

## SQLite Schema & Storage Design

### Why `modernc.org/sqlite` (Pure Go)

- **Single binary distribution** — no cgo, no C compiler, no shared libraries for users
- **Cross-compile any OS/arch** — `GOOS=linux GOARCH=arm64 go build` just works
- **Zero runtime dependencies** — users download one file and run it
- **FTS5 available** — full-text search built-in
- **Performance acceptable** — ~2x write overhead vs cgo (irrelevant: writes only during `polyflow index`, reads are sub-ms)

### Table Schema

```sql
-- Core tables
CREATE TABLE nodes (
    id          TEXT PRIMARY KEY,        -- deterministic hash: service:file:type:name:line
    type        TEXT NOT NULL,           -- function, class, struct, file, service, variable, endpoint, channel, dom_target, module, interface
    name        TEXT NOT NULL,           -- human-readable (e.g., "CreateUser")
    service     TEXT NOT NULL,           -- which service this belongs to
    file_path   TEXT,                    -- relative path within service
    line_start  INTEGER,                 -- start line
    line_end    INTEGER,                 -- end line
    language    TEXT,                    -- go, javascript, typescript, ruby, templ
    visibility  TEXT,                    -- public, private, protected
    signature   TEXT,                    -- function/method signature (searchable)
    selector    TEXT,                    -- DOM target selector (for dom_target nodes)
    metadata    TEXT                     -- JSON blob for type-specific overflow data
);

CREATE TABLE edges (
    id          TEXT PRIMARY KEY,        -- hash of source:target:type
    source_id   TEXT NOT NULL REFERENCES nodes(id),
    target_id   TEXT NOT NULL REFERENCES nodes(id),
    type        TEXT NOT NULL,           -- calls, http_request, publishes, dom_write, etc.
    confidence  TEXT,                    -- static, inferred, partial, unknown
    http_method TEXT,                    -- GET, POST, etc. (for http_* edges)
    path        TEXT,                    -- route path (for http_* and datastar_action edges)
    metadata    TEXT,                    -- JSON: params_passed, params_expected, etc.
    UNIQUE(source_id, target_id, type)
);

-- Incremental indexing
CREATE TABLE file_hashes (
    file_path    TEXT PRIMARY KEY,       -- relative to workspace root
    service      TEXT NOT NULL,
    content_hash TEXT NOT NULL,          -- SHA-256 of file content
    indexed_at   INTEGER NOT NULL        -- unix timestamp
);

-- Parse error tracking (partial data still extracted)
CREATE TABLE parse_errors (
    file_path        TEXT PRIMARY KEY,
    service          TEXT NOT NULL,
    error_count      INTEGER,
    first_error_line INTEGER,
    indexed_at       INTEGER NOT NULL
);

-- Schema versioning (auto re-index on breaking changes)
CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT
);
-- Stores: schema_version, polyflow_version, last_indexed

-- Full-text search
CREATE VIRTUAL TABLE nodes_fts USING fts5(
    name,
    file_path,
    signature,
    selector,
    content=nodes,
    content_rowid=rowid
);

-- Indexes for fast traversal and filtering
CREATE INDEX idx_edges_source ON edges(source_id);
CREATE INDEX idx_edges_target ON edges(target_id);
CREATE INDEX idx_edges_type ON edges(type);
CREATE INDEX idx_edges_confidence ON edges(confidence);
CREATE INDEX idx_edges_path ON edges(path);
CREATE INDEX idx_nodes_type ON nodes(type);
CREATE INDEX idx_nodes_service ON nodes(service);
CREATE INDEX idx_nodes_file ON nodes(file_path);
CREATE INDEX idx_nodes_name ON nodes(name);
CREATE INDEX idx_nodes_selector ON nodes(selector);
```

### Design Principles

- **Hybrid schema** — Frequently-queried fields as indexed columns, type-specific data in JSON `metadata` blob
- **Deterministic node IDs** — Content-derived (`service:file:type:name:line`), so re-indexing produces stable references
- **FTS5 for search** — Sub-millisecond fuzzy search across all node names, paths, signatures
- **JSON metadata parsed on-demand** — Only when displaying detail panel, never during traversal
- **Atomic swap for concurrent access** — `polyflow index` writes to `graph.db.tmp`, atomic renames to `graph.db` on success. `polyflow serve` detects the swap (fsnotify), reloads adjacency map, and notifies browser ("Graph updated"). Zero inconsistent reads, crash-safe (old DB untouched if index fails mid-way).
- **Version stamp** — `meta` table stores `schema_version`. On breaking changes, DB is dropped and user re-indexes. Non-breaking additions (new column, new index) applied via silent `ALTER TABLE`.
- **Partial parse on error** — Files with syntax errors are partially indexed (valid regions extracted, ERROR subtrees skipped). Tracked in `parse_errors` table. `polyflow status` reports count; `polyflow status --errors` lists specifics.

### In-Memory Adjacency Map (Serve Time)

At `polyflow serve` startup, all edges are loaded into an in-memory adjacency structure:

```go
type Edge struct {
    ID         string
    SourceID   string
    TargetID   string
    Type       string
    Confidence string
    Path       string
}

type AdjacencyIndex struct {
    Forward  map[string][]Edge  // source_id → outgoing edges
    Backward map[string][]Edge  // target_id → incoming edges
}
```

- **Traversal (BFS/DFS)** runs entirely in memory — microseconds, not milliseconds
- **500k edges ≈ 50-80MB RAM** — trivial for a local tool
- SQLite only hit for: node detail (metadata fetch), search (FTS5), writes (during index)

### Operation Performance

| Operation | Method | Expected Speed |
|-----------|--------|----------------|
| Graph traversal (depth 10) | In-memory adjacency map | < 1ms |
| Node search | SQLite FTS5 | < 5ms |
| Filter edges by type/confidence/path | Indexed SQL columns | < 10ms |
| Node detail (metadata) | PK lookup + JSON parse | < 1ms |
| Data flow search ("edges passing X") | JSON extract on filtered set | < 50ms |
| Full re-index write (10k files) | Batched transactions | < 30s |

---

## Testing Strategy

### Test Coverage: 90% minimum (CI enforced)

| Layer | Test Type | Approach |
|-------|-----------|----------|
| Pattern YAML loading | Unit | Load YAML, assert correct struct hydration |
| Pattern matching | Unit | Feed AST nodes to matchers, assert detected edges/nodes |
| Tree-sitter parsing | Unit | Parse known source snippets, assert AST extraction |
| Graph store (SQLite) | Integration | In-memory SQLite (`":memory:"`), assert CRUD |
| Linker (cross-service) | Unit | Given endpoints from two services, assert link edges |
| API endpoints | Unit | `httptest` handlers, assert JSON responses |
| CLI commands | Integration | Execute against temp workspace, assert output |
| End-to-end | E2E | Full workspace with fixture repos → index → query → assert |

### Testability Principles

1. **Interfaces everywhere** — Every dependency (file reader, DB, parser) behind an interface for test injection
2. **No global state** — All state passed explicitly, no package-level mutable vars
3. **Fixture-driven pattern tests** — Each YAML pattern ships with `testdata/` containing source + expected output
4. **Table-driven tests** — Go idiomatic, one test function covers all cases per matcher
5. **Mandatory fixtures** — CI fails if a pattern YAML has no corresponding test fixture directory
6. **Benchmarks** — Performance-critical paths (parser, matcher, graph queries) have `Benchmark*` functions

---

## API Endpoints

```
GET  /api/graph                              → full graph (paginated)
GET  /api/graph/search?q=<query>             → search nodes (fuzzy match)
GET  /api/graph/trace?root=<ID>&direction=forward&depth=5  → isolated subgraph
GET  /api/node/:id                           → node detail (source snippet, variables, metadata)
GET  /api/node/:id/source                    → full source of function (for "show more")
GET  /api/stats                              → workspace stats (services, files, nodes, edges, last indexed)
GET  /api/events                             → SSE stream (pushes "graph_updated" when DB swaps after re-index)
```

All responses in Cytoscape JSON format (for graph endpoints) — zero transformation needed on frontend.

**Security:**
- Binds to `127.0.0.1` by default (unreachable from network)
- `--host 0.0.0.0` flag for explicit LAN exposure (shows warning)
- CORS: `Access-Control-Allow-Origin: http://localhost:9400` (blocks cross-origin JS attacks)

**Scope (v1):** Web UI is visualization-only (graph, search, trace, detail panel, filters). All operational commands (index, config, status, patterns) remain CLI-only. Web UI for CLI features is a v2+ consideration.

**UI behaviors (Phase 11, pulled forward from the v2 roadmap where noted):**

- **Version surfacing** — the detail panel shows a `package@resolved_version` chip for framework-boundary and cloud-SDK nodes (e.g. `github.com/aws/aws-sdk-go@1.55.8`), sourced from node meta stamped by version-gated patterns.
- **Boundary collapse (default)** — framework/SDK call sites (anything with `package` in meta, or `external_service` nodes) fold into one group node per (service, package), labeled `pkg@version (n)`, with edges re-routed and deduplicated. Double-click a group (or use the detail panel toggle) to expand its call sites. Application code never collapses.
- **Confidence default** — only `static` + `inferred` edges render by default; `partial`/`unknown` are opt-in via the filter panel and draw dashed/dimmed. Edges without an explicit confidence are structural AST matches and count as static.
- **Two altitudes** — in-depth (per-function) vs high-level (one node per service, cross-service edges aggregated per type with counts). The high-level view matches the service-level Mermaid export exactly.
- **Diagram export (pulled forward from v2)** — `GET /api/export/mermaid?level=service|function[&root=&direction=&depth=]` renders golden-tested Mermaid (per-service subgraphs, `package@version` in labels, dashed uncertain edges); SVG/PNG export client-side from the live Cytoscape canvas (cytoscape-svg), reflecting current filters/collapse state.

---

## Build Order (Dependency-Driven)

| Phase | Deliverable | Outcome |
|-------|-------------|---------|
| 1 | `internal/meta` + `internal/workspace` + CLI skeleton (Cobra) | Foundation |
| 2 | `internal/parser` — Tree-sitter integration, per-language extractors | Core engine |
| 3 | `internal/patterns` — YAML loader, query compiler, matcher engine | Detection layer |
| 4 | `internal/graph` — Node/edge model, SQLite store, pipeline writer | Storage |
| 5 | `internal/linker` — Cross-service route matching, user hints | Service connections |
| 6 | `internal/server` + `web/` — API endpoints, Cytoscape.js frontend | Visualization |
| 7 | E2E tests with fixture workspaces | Full validation |

Each phase ships with: interfaces first, unit tests alongside implementation, fixture data for every pattern, benchmarks for hot paths.

---

## AI Agent Integration

### The Problem: AI Agents Waste Tokens on Discovery

Today's AI coding agents (Claude Code, Cursor, Copilot Workspace, etc.) spend the majority of their token budget on **discovery** — figuring out what code is connected to what. They grep, read file after file, follow imports manually, and often miss connections across service boundaries.

| Task | What agents do today | Token cost |
|------|---------------------|------------|
| Find where to make a change | Grep, read files, follow imports manually | 10k-50k tokens exploring |
| Understand impact of a change | Read all callers, all consumers, guess at deps | 20k-100k tokens |
| Generate a new feature | Read related code for patterns, discover endpoints | 30k-80k tokens |
| Debug a cross-service issue | Read both services, figure out connections | 50k-150k tokens |

Polyflow eliminates this discovery cost entirely — the graph already contains pre-computed relationships, data shapes, and cross-service connections.

### Why This Matters: Real-World Reasoning

**The fundamental insight**: The information an AI agent needs to make a correct change is the same information polyflow computes during indexing — what calls what, what data flows where, what the impact radius is. Computing this once (at index time) and serving it on demand is orders of magnitude cheaper than having every agent session re-derive it from raw source code.

**Cost math**: If a team runs 50 AI agent sessions/day, each spending ~40k tokens on discovery, that's 2M tokens/day wasted on rediscovering what's already knowable from static analysis. At $3/MTok (Claude Sonnet), that's $6/day → $2,190/year on pure waste. For larger teams or more complex tasks, multiply by 10-50x.

**Accuracy improvement**: Agents that explore code by reading files often miss cross-service connections (they don't know to look in another repo). Polyflow guarantees complete coverage — if a connection exists in the indexed workspace, the agent will know about it. This eliminates a class of bugs where AI-generated code breaks downstream consumers it didn't know existed.

### How Polyflow Reduces Token Usage

#### 1. Precise Context Injection (Instead of Broad Search)

**Without polyflow:**
```
Agent prompt: "Add email validation to the user creation flow"
Agent actions:
  1. grep for "user" → 200 results, reads 15 files
  2. Finds handler, reads it
  3. Follows import to repo layer, reads it
  4. Wonders about frontend, greps for "/api/users", reads 3 more files
  5. Misses the RabbitMQ consumer in another service entirely
→ 40k tokens spent, incomplete understanding
```

**With polyflow:**
```
Agent query: polyflow trace --root "POST /api/users" --direction both --depth 5 --format json
→ Returns in ~500 tokens:
  - Exact handler function, file, line
  - All downstream calls (DB, RabbitMQ publish, SSE response)
  - All upstream triggers (frontend form, templ file, route registration)
  - Data shapes at every boundary
  - DOM elements affected
Agent: reads only the 3-4 relevant function bodies
→ 5k tokens total, COMPLETE understanding including cross-service
```

**Token reduction: ~89%** for discovery. **Accuracy improvement: catches connections agents miss.**

#### 2. Impact Analysis Before Code Generation

The most expensive agent mistake is generating code that breaks something it didn't know about. Polyflow eliminates this:

| Question | Without Polyflow | With Polyflow |
|----------|-----------------|---------------|
| "What breaks if I change this function signature?" | Read all callers across all services (miss some) | Backward trace → complete caller list with exact params passed |
| "What services consume this event?" | Grep across repos, hope you find them | Forward trace from publish → all subscribers with expected payload |
| "What DOM elements update when this API responds?" | Read frontend code, trace manually | Graph shows SSE → Datastar → DOM target chain |
| "Is this field used anywhere else?" | Grep (misses dynamic access) | Variable node → all `reads`/`writes` edges |

#### 3. Structured Context for Code Generation

Instead of feeding an AI agent raw source code and hoping it extracts the right information, polyflow provides pre-structured context:

```json
{
  "target": {
    "id": "node_123",
    "type": "function",
    "name": "CreateUser",
    "file": "internal/handlers/user.go",
    "line": 42,
    "signature": "func CreateUser(ctx context.Context, req CreateUserRequest) (*User, error)",
    "source_snippet": "..."
  },
  "data_flow": {
    "params_received": [
      {"name": "name", "type": "string", "confidence": "static"},
      {"name": "email", "type": "string", "confidence": "static"},
      {"name": "role_id", "type": "int", "confidence": "static"}
    ],
    "returns": [
      {"type": "*User", "confidence": "static"},
      {"type": "error", "confidence": "static"}
    ]
  },
  "upstream": [
    {"type": "http_handler", "route": "POST /api/users", "file": "routes.go:15"},
    {"type": "datastar_action", "source": "data-on-submit=\"@post('/api/users')\"", "file": "users.templ:28"}
  ],
  "downstream": [
    {"type": "calls", "target": "UserRepo.Insert", "file": "repo/user.go:22", "params_passed": ["User struct"]},
    {"type": "publishes", "target": "user.created", "broker": "rabbitmq", "params_passed": ["UserCreatedEvent"]},
    {"type": "sse_endpoint", "target": "datastar.MergeFragments", "file": "handlers/user.go:67"}
  ],
  "dom_impact": [
    {"selector": "#user-list", "mutation": "innerHTML", "via": "SSE datastar-patch-elements", "defined_in": "users.templ:42"},
    {"selector": "#user-count", "mutation": "textContent", "via": "data-text=\"$userCount\"", "defined_in": "layout.templ:15"}
  ],
  "related_patterns": [
    {"name": "CreateProject", "file": "internal/handlers/project.go:38", "similarity": "same CRUD pattern with RabbitMQ publish"}
  ],
  "consumers_in_other_services": [
    {"service": "notification-service", "function": "HandleUserCreated", "file": "consumers/user.go:12", "expects": {"user_id": "int", "email": "string"}}
  ]
}
```

This is ~800 tokens of **complete, structured context** that an agent can act on immediately — vs. 40k+ tokens of raw file exploration that still might miss the notification service consumer.

### Real-World Example: Solving a Bug

**Bug**: "Users aren't seeing the updated username in the header after editing their profile"

**Agent without polyflow** (~80k tokens, 3-5 minutes):
1. Grep for "username" → 150 results
2. Read profile update handler → finds DB update
3. Read frontend profile form → finds `axios.put('/api/profile')`
4. Grep for header component → reads layout template
5. Can't figure out how the header updates → reads Datastar docs
6. Eventually finds SSE connection but misses that the signal name is `$currentUser.name` not `$username`
7. Proposes fix that doesn't work
8. Reads more code, finds the actual signal
9. Finally proposes correct fix

**Agent with polyflow** (~8k tokens, 30 seconds):
```bash
polyflow trace --root "PUT /api/profile" --direction forward --depth 5 --format json
```
Response immediately shows:
- Handler updates DB ✓
- Handler calls `datastar.MergeSignals({currentUser: {name: newName}})` 
- This connects to `data-text="$currentUser.name"` in `layout.templ:22` (the header)
- DOM target: `#header-username`

Agent sees the complete chain. If the bug is that MergeSignals isn't being called after the DB update, it knows exactly where to add it and what signal name to use. **One correct fix, first try.**

### Token Cost Comparison

| Task | Without Polyflow | With Polyflow | Savings |
|------|-----------------|---------------|---------|
| Add a field to user creation (full stack) | ~45k tokens | ~5k tokens | **89%** |
| Rename a function (impact check + update) | ~30k tokens | ~2k tokens | **93%** |
| Implement delete similar to create | ~60k tokens | ~8k tokens | **87%** |
| Debug cross-service data not arriving | ~80k tokens | ~6k tokens | **92%** |
| Add new event consumer in another service | ~50k tokens | ~7k tokens | **86%** |
| Understand what a button click triggers E2E | ~35k tokens | ~3k tokens | **91%** |

**Average saving: ~90% token reduction** with higher accuracy (no missed connections).

### Integration Approaches

#### Option A: CLI Pipe (Simplest, v1)

Agent calls polyflow as a subprocess:
```bash
polyflow trace --root "CreateUser" --direction both --depth 5 --format json
polyflow context --target "POST /api/users" --task impact
polyflow search "username" --format json
```

Output piped directly into agent context. Works with any AI tool that can execute shell commands (Claude Code, Aider, custom agents).

#### Option B: MCP Server (Model Context Protocol, v1.1)

Polyflow exposes itself as an MCP tool:

```json
{
  "tool": "polyflow_trace",
  "params": {
    "query": "POST /api/users",
    "direction": "both",
    "depth": 5,
    "include_source": true,
    "include_data_flow": true
  }
}
```

Integrates natively with Claude Code, Cursor, and any MCP-compatible agent. The agent can call polyflow mid-reasoning without leaving its context.

#### Option C: Context File Generation (Batch/Offline)

```bash
polyflow context --target "CreateUser" --task generate > .polyflow/context.json
polyflow context --target "UserController" --task impact > .polyflow/impact.json
```

Pre-generate context files that agents read at session start. Zero latency during agent interaction. Useful for CI/CD pipelines where agents run automated tasks.

### Agent-Optimized Commands (v1)

| Command | Purpose | Output |
|---------|---------|--------|
| `polyflow trace --root <query> --direction forward\|backward\|both --depth N --format json\|text\|chain` | Multi-hop flow trace | JSON: flat hop list + enumerated chains, every hop carrying node meta (incl. `package`/`resolved_version`), edge type/confidence/meta, and cross-service marks. `chain`: one linear path per line, e.g. `(nextgen) publish -[publishes]-> dsw.builds -[subscribes]-> ‖dsw-agent‖ consume` (‖service‖ marks a boundary crossing; `-[type?]->` marks partial/unknown confidence). Chain enumeration is capped at 100 paths (`truncated` flag set). |
| `polyflow context --target <query> --task <type>` | Generate agent-ready context | Structured JSON (see above) |
| `polyflow impact --target <query>` | Show blast radius of a change | List of affected functions, services, DOM elements |
| `polyflow suggest --task "add validation to user creation"` | Suggest which files/functions to modify | Ordered list of change targets with reasoning |

Task types for `polyflow context`:
- `impact` — What would break if this changes?
- `generate` — What patterns/shapes should new code follow?
- `debug` — What's the full flow from trigger to outcome?
- `refactor` — What depends on this that needs updating?

### Why This Is a v1 Feature (Not v2)

The AI agent integration is **zero additional computation** — it's a different output format for data the tool already computes. The graph, edges, data flow, and DOM connections exist in SQLite after indexing. The agent-facing commands are just query + JSON formatter. Implementation cost is minimal but value is transformative.

This also creates a network effect: teams that use polyflow for human visualization will naturally want to feed the same data to their AI tools, and vice versa.

---

## v2+ Roadmap

### Accuracy / Analysis Upgrades
- Go compiler API (`golang.org/x/tools/go/packages` + pointer analysis) — v1.5 priority
- TypeScript compiler API (type-resolved call graph, prop flow tracing) — v1.6 priority
- OpenAPI/Swagger as authoritative cross-service link source — v1.5 priority
- SCIP output + consumption (Sourcegraph ecosystem interop) — v1.7
- Ruby Prism parser + Sorbet integration — v2
- Runtime enrichment mode (opt-in instrumentation during test runs) — v2
- Constraint-based URL resolver (path normalization + constant propagation) — v1.5

### New Language Support
- Python language support
- Java language support
- Vue SFC template parsing (event listeners, HTTP calls)
- Svelte template parsing
- Angular template parsing

### New Detection Patterns
- Kafka detection patterns
- gRPC stub detection
- Database call detection (SQL queries, ORM — ActiveRecord, GORM, Ecto)
- Intra-function data flow analysis (variable assignment tracing)
- Serializer parsing (jbuilder, ActiveModelSerializer)
- Spread/merge resolution for JS/TS
- Data flow mismatch detection (caller vs receiver disagreement)
- Variable reads/writes deep tracking

### CLI / Workflow
- MCP server mode (`polyflow mcp`) — native integration with Claude Code, Cursor, etc.
- `polyflow suggest` command — recommend which files to modify for a given task description
- Git URL support in workspace config (remote cloning)
- Watch mode / live re-index

### UI / Visualization
- Flow comparison (overlay two flows side-by-side)
- Export (SVG, JSON, Mermaid)
- Git history time-travel (trace how a flow changed over commits)
- Shadow DOM / Web Components support
- iframe cross-origin tracking
- Web service deployment (multi-user, hosted)

---

## Key Dependencies

### Go Backend

**v1 (tree-sitter, structural):**

| Package | Purpose |
|---------|---------|
| `github.com/smacker/go-tree-sitter` | Tree-sitter Go bindings |
| `github.com/smacker/go-tree-sitter/javascript` | JS grammar |
| `github.com/smacker/go-tree-sitter/typescript/typescript` | TS grammar |
| `github.com/smacker/go-tree-sitter/ruby` | Ruby grammar |
| `github.com/smacker/go-tree-sitter/golang` | Go grammar |
| `github.com/a-h/templ/parser/v2` | Templ parser (pure Go, typed AST, Visitor interface) |
| `github.com/spf13/cobra` | CLI framework |
| `modernc.org/sqlite` | Pure Go SQLite (no cgo, single binary, cross-compile) |
| `gopkg.in/yaml.v3` | YAML parsing |
| `github.com/stretchr/testify` | Test assertions |
| `github.com/fsnotify/fsnotify` | File watching (serve reload on DB swap) |
| Go 1.22+ stdlib `net/http` | HTTP server (native path params, no framework) |

**v1.5+ (compiler APIs, semantic):**

| Package | Purpose |
|---------|---------|
| `golang.org/x/tools/go/packages` | Go compiler API — type-resolved AST, cross-package loading |
| `golang.org/x/tools/go/callgraph` | Go call graph construction |
| `golang.org/x/tools/go/callgraph/rta` | Rapid Type Analysis — resolves interface dispatch |
| `golang.org/x/tools/go/pointer` | Pointer analysis — resolves dynamic calls through interfaces |
| `golang.org/x/tools/go/ssa` | Static Single Assignment form — enables data flow analysis |
| TypeScript compiler API (via subprocess) | Type-resolved TS/JS analysis — invoked as `tsc --noEmit` worker |
| `github.com/nicolo-ribaudo/tree-sitter-flow` | Flow type annotation support (optional, loaded on `// @flow` pragma) |

### Frontend (SolidJS)

| Package | Purpose |
|---------|---------|
| `solid-js` (~7 KB gzipped) | UI framework — fine-grained reactivity, JSX components |
| `cytoscape` | Graph rendering and interaction |
| `cytoscape-dagre` | Directed graph layout (flow traces) |
| `cytoscape-fcose` | Force-directed layout (overview) |
| `vite` | Bundler — outputs to `web/dist/` for Go `embed.FS` |
| `tailwindcss` | Utility-first CSS |

### Build & Distribution

- Vite bundles SolidJS app → `web/dist/` (static assets)
- Go embeds `web/dist/` via `//go:embed` directive
- Single binary serves frontend + API — no separate frontend deploy
- `make build` runs: `cd web && npm run build && cd .. && go build`

### Cross-Compilation (cgo Required for Tree-sitter)

Tree-sitter (`go-tree-sitter`) requires cgo — it wraps the C tree-sitter runtime. This is the only cgo dependency (`modernc.org/sqlite` remains pure Go). Cross-compilation uses `zig cc` as the C cross-compiler:

```makefile
# Makefile targets for all platforms
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

build-all:
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		echo "Building $$os/$$arch..."; \
		CGO_ENABLED=1 \
		CC="zig cc -target $$(zig_target $$os $$arch)" \
		GOOS=$$os GOARCH=$$arch \
		go build -o dist/polyflow-$$os-$$arch ./cmd/polyflow; \
	done

# Individual platform targets
build-linux-amd64:
	CGO_ENABLED=1 CC="zig cc -target x86_64-linux-gnu" \
	GOOS=linux GOARCH=amd64 go build -o dist/polyflow-linux-amd64 ./cmd/polyflow

build-linux-arm64:
	CGO_ENABLED=1 CC="zig cc -target aarch64-linux-gnu" \
	GOOS=linux GOARCH=arm64 go build -o dist/polyflow-linux-arm64 ./cmd/polyflow

build-darwin-amd64:
	CGO_ENABLED=1 CC="zig cc -target x86_64-macos" \
	GOOS=darwin GOARCH=amd64 go build -o dist/polyflow-darwin-amd64 ./cmd/polyflow

build-darwin-arm64:
	CGO_ENABLED=1 CC="zig cc -target aarch64-macos" \
	GOOS=darwin GOARCH=arm64 go build -o dist/polyflow-darwin-arm64 ./cmd/polyflow

build-windows-amd64:
	CGO_ENABLED=1 CC="zig cc -target x86_64-windows-gnu" \
	GOOS=windows GOARCH=amd64 go build -o dist/polyflow-windows-amd64.exe ./cmd/polyflow
```

**Key points:**
- `zig cc` is a drop-in C cross-compiler (single binary install, handles all targets)
- Output is still a **static, self-contained binary** — zero runtime deps for users
- CI builds all platforms in one pipeline (GitHub Actions + zig)
- Users download one file, run it. No zig/gcc/toolchain needed by end users.
- cgo complexity is **build-time only** — invisible to users

**CI setup (GitHub Actions):**
```yaml
- uses: goto-bus-stop/setup-zig@v2
- run: make build-all
- uses: softprops/action-gh-release@v1
  with:
    files: dist/*
```

---

## Naming Convention (Single Rename Point)

```go
// internal/meta/meta.go
package meta

const (
    Name        = "polyflow"
    Version     = "0.1.0"
    Description = "Cross-service code flow analyzer"
    DBDir       = "." + Name    // .polyflow/
    DBFile      = "graph.db"
    ConfigFile  = "workspace.yaml"
    DefaultPort = 9400
)
```

All CLI output, config directories, help text, and web UI title reference `meta.Name`. Renaming the tool is a one-line change.
