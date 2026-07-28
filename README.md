# polyflow

**Cross-service code flow analyzer + MCP context server.**

Polyflow statically indexes one or many repositories into a queryable code graph
(functions, routes, brokers, jobs, components, datastores …) and serves that graph
to AI coding agents over the [Model Context Protocol](https://modelcontextprotocol.io).
Instead of an agent grep-crawling a codebase to answer *"who calls this?"*,
*"what breaks if I change this?"*, or *"how does a request flow across services?"*,
it makes one tool call and gets a resolved, cross-service answer.

The design goal is **agent context retrieval** — maximize recall and honestly
ledger anything it cannot resolve, rather than inventing edges.

---

## Table of contents

- [Why](#why)
- [How it works](#how-it-works)
- [Installation](#installation)
- [Quickstart](#quickstart)
- [Configuration (`workspace.yaml`)](#configuration-workspaceyaml)
- [CLI commands](#cli-commands)
- [MCP server & tools](#mcp-server--tools)
- [Supported languages & frameworks](#supported-languages--frameworks)
- [What the graph captures](#what-the-graph-captures)
- [Runtime evidence (optional)](#runtime-evidence-optional)
- [Multi-repo workspaces](#multi-repo-workspaces)
- [Limitations & honest gaps](#limitations--honest-gaps)
- [Development](#development)

---

## Why

An agent answering a structural question ("find every caller of `validateJWT`")
without a code graph typically fans out across many `grep`/`read` round-trips,
burning tokens and turns. On a measured hard case in this fleet, the tool-free
baseline consumed **~360K context tokens across 15 turns** (and still missed
callers), while the same task through polyflow's MCP tools resolved in **~58K
tokens across 3 turns** with full recall. The win is largest on caller,
blast-radius, and cross-service-flow questions; on trivial single-file lookups
it is roughly break-even.

## How it works

1. **Parse** — every service is walked and parsed with tree-sitter grammars per
   language. A pattern engine (YAML rule packs under `patterns/`) recognizes
   framework constructs: routes, HTTP/broker clients, job enqueues, components,
   datastores, etc.
2. **Graph** — nodes and edges are written to a per-workspace SQLite database at
   `.polyflow/graph.db`. Re-indexing is incremental (content-hash gated).
3. **Link** — cross-service edges are inferred from evidence: HTTP client path ↔
   server route, broker publish ↔ subscribe, gRPC/GraphQL call ↔ handler. Every
   edge carries a **confidence** (`static` / `candidate` / `verified`) and
   unresolved clues are ledgered, never fabricated.
4. **Serve** — the graph is exposed to agents via an MCP stdio server and to
   humans via a web UI / CLI.

## Installation

**Prerequisites:** Go 1.22+ with CGO enabled (SQLite is bundled via
`go-sqlite3`).

```sh
git clone https://github.com/lordsonvimal/polyflow
cd polyflow
make build
# Binary at ./dist/polyflow — add dist/ to PATH or run it directly.
```

Verify:

```sh
polyflow --version
polyflow doctor
```

## Quickstart

```sh
# 1. In your project root (or a folder containing your services):
polyflow init          # auto-discovers services → writes workspace.yaml

# 2. Build the graph:
polyflow index         # incremental on subsequent runs

# 3. Sanity-check coverage:
polyflow doctor        # flags zero-pattern-match services

# 4. Register with an agent (Claude Code shown; any MCP host works):
claude mcp add polyflow -- polyflow mcp

# 5. Query:
polyflow impact --target handleCheckout
polyflow trace  --target handleCheckout
polyflow flows  "checkout order across services"
```

## Configuration (`workspace.yaml`)

`polyflow init` writes a `workspace.yaml` in the current directory. It is the
single source of truth for the workspace; paths resolve relative to the file's
directory. All fields below are optional except `services`.

```yaml
version: 1

services:
  - name: api               # unique service id used everywhere
    path: ./api             # repo root or subdirectory
    language: go            # go | ruby | python | javascript | typescript
    frameworks: [chi]       # optional hint; usually auto-detected
    port: 8080              # optional; helps HTTP link inference

  - name: web
    path: ./web
    language: typescript

# Known cross-service dependencies (seed link inference; usually optional).
links:
  - from: web
    to: api
    via: http               # http | rabbitmq | grpc | graphql
    hint: "API_URL=http://localhost:8080"   # env hint for host resolution
    base_url: /api          # stripped from client paths before route matching
  # via: rabbitmq
  # exchange: orders

# Proposals written by `polyflow link --infer` (do not hand-edit).
links_proposed: []

index:
  exclude:                  # doublestar globs, service-relative
    - "**/node_modules/**"
    - "**/vendor/**"
    - "**/*_test.go"

search:
  embedder: static          # static (default, zero-setup) | sidecar | endpoint
  # endpoint_url: http://localhost:11434     # for embedder: endpoint (Ollama etc.)
  # endpoint_model: nomic-embed-text
  # endpoint_key_env: OPENAI_API_KEY
  synonyms:                 # map user vocabulary → code vocabulary
    login: [authenticate, signin]

evidence:
  contract_globs:           # IDL/spec locations (defaults: openapi.yaml, *.proto, *.graphql, asyncapi.yaml)
    - "api/openapi/*.yaml"
  stale_after: 720h         # runtime evidence freshness threshold (default 30 days)
  runtime:
    service_names:          # map OTel resource.service.name → polyflow service
      chessleap-api: api
    sse_routes: [/events, /stream]

settings:
  snippet_lines: 30         # source lines inlined per node
  default_layout: dagre-lr  # web UI graph layout
  default_depth: 5          # default traversal depth
  port: 9400                # web UI / API server port
```

Additional per-workspace file: **`.polyflowignore`** — one doublestar glob per
line (like `.gitignore`), matched against service-relative paths; merged with
`index.exclude`.

Edit config from the CLI without opening the file:

```sh
polyflow config show
polyflow config set settings.default_depth 6
polyflow config service add --name api --path ./api --language go
polyflow config link add --from web --to api --via http
```

## CLI commands

| Command | Purpose |
|---|---|
| `polyflow init` | Auto-discover services and write `workspace.yaml`. |
| `polyflow index` | Parse all services and build/update the graph (incremental). |
| `polyflow doctor` | Health check; flags zero-match services, can `--propose` contract rules. |
| `polyflow status` | Index statistics (node/edge/link counts) and a freshness verdict (STALE when sources changed since the last index). |
| `polyflow search <query>` | Search the index for matching nodes. |
| `polyflow context --target <node>` | Callers, callees, and cross-service edges around a node. |
| `polyflow trace --target <node>` | Multi-hop call chains (`A → B → C`), incl. cross-service hops. |
| `polyflow impact --target <node>` | Blast radius: everything transitively affected. `--diff` scopes to the current git diff. |
| `polyflow flows "<description>"` | Resolve an end-to-end flow across service boundaries. |
| `polyflow link --infer` | Propose cross-service links from indexed evidence. |
| `polyflow deps` | Resolved dependency versions per service. |
| `polyflow patterns list` / `add <file>` | List loaded pattern packs or register a custom one. |
| `polyflow config …` | View/edit `workspace.yaml` (`show`, `set`, `service`, `link`). |
| `polyflow mcp` | Start the MCP stdio server (used by agents). Subcommands: `on` / `off` / `status` toggle the query tools for the next session (A/B token measurement). |
| `polyflow serve` | Start the web UI + HTTP API. |
| `polyflow capture start\|stop\|run\|ingest` | Runtime OTLP trace capture (see below). |
| `polyflow eval` | Measure recall against ground-truth cases. |
| `polyflow bench` | Agent-outcome benchmark (manual; spends real tokens). |

## MCP server & tools

The server is a spec-compliant MCP implementation built on the official
`modelcontextprotocol/go-sdk`, speaking **stdio** transport. Any MCP-capable
client can use it:

```sh
# Claude Code
claude mcp add polyflow -- polyflow mcp

# Any other host (Cursor, Cline, Windsurf, custom agents): point it at the
# command `polyflow mcp` with stdio transport. Example mcpServers entry:
```

```json
{
  "mcpServers": {
    "polyflow": { "command": "polyflow", "args": ["mcp"] }
  }
}
```

The server reads the `.polyflow/graph.db` in its working directory, so launch it
from the workspace root (one instance per workspace).

### Exposed tools

| Tool | What it answers |
|---|---|
| `search` | Find the exact node/flow/doc chunk matching a query (natural language OK). Start here. |
| `resolve` | Resolve a description or partial name to ranked candidate nodes (disambiguation, saves a round-trip). |
| `context` | Callers, callees, and cross-service edges around a node — or the ranked files related to given file(s). |
| `impact` | Blast radius of changing a node or file: transitive dependents, entry points, affected services. |
| `trace` | Multi-hop call chains from a node as linear paths, including cross-service hops. |
| `flows` | Full end-to-end flow from a starting point across services (HTTP, jobs, pub/sub, gRPC, renders). |
| `entrypoints` | Catalog entry nodes (HTTP routes, subscribers, workers, gRPC/GraphQL handlers) by service/keyword. |
| `read` | Return the exact source span of a symbol (function/method/struct/interface) by node id — its true `Line..end_line`, not a fixed window or the whole file. |

Every tool honors a token budget: large results auto-roll-up per file, and the
`coverage.unresolved` section always survives trimming — those endpoints are the
only ones an agent should fall back to grep for. That is the token-saving
contract.

### Toggling polyflow on/off (A/B token measurement)

To measure what polyflow actually saves, run the *same* agent task twice — once
with the tools available, once without — and compare context tokens and turns.
A state-file gate makes this one command, in any MCP client:

```sh
polyflow mcp on   && <reconnect the agent>   # "with polyflow": tools available
polyflow mcp off  && <reconnect the agent>   # "without": query tools absent
polyflow mcp status                          # enabled | disabled
```

`off` writes `.polyflow/mcp.disabled`; while present, `polyflow mcp` starts but
advertises **zero query tools** (only a `status` probe), so the model sees a
genuinely polyflow-free tool list — the clean control arm. `on` removes the
marker. The change takes effect on the **next** session, because an MCP stdio
server is spawned once per session — so reconnect (restart the session) after
toggling. `polyflow index` / `status` / `serve` keep working regardless.

Alternatives, if you prefer a client-native switch:

| Approach | Works in | Trade-off |
|---|---|---|
| `polyflow mcp off` / `on` (above) | any stdio MCP client | one scriptable command; effective on next reconnect |
| Remove/re-add the server (`claude mcp remove polyflow` / `add`), or `disabledMcpjsonServers` in `.mcp.json` | Claude Code only | per-client syntax; easy to forget to restore |
| Settings UI toggle | Cursor / Windsurf | a click, but not scriptable |

## Supported languages & frameworks

Parsing is tree-sitter based; framework recognition is driven by YAML pattern
packs in `patterns/<language>/`. Current coverage:

| Language | Routes / handlers | HTTP clients | Brokers / jobs | Realtime | Other |
|---|---|---|---|---|---|
| **Go** | `net/http`, chi, gin, gRPC | `net/http`, resty | Kafka, NATS, Redis pub/sub, AMQP (091) | gorilla/websocket, SSE hubs, datastar SSE | GORM, `database/sql`, cobra, AWS S3 (v1/v2), Bedrock, goroutines, functions/structs/interfaces |
| **Ruby** | Rails routes & controllers, Rails nav | Faraday (+instance), HTTParty, RestClient, Net::HTTP | Sidekiq, ActiveJob, DelayedJob, SolidQueue, Bunny, Kicks, AMQP registration handshake | Pusher | AWS S3 |
| **Python** | FastAPI, Flask, Django | requests, httpx | Celery, pika (AMQP), aio-pika | — | functions |
| **JavaScript** | Express | fetch, axios (+instance), XMLHttpRequest | Pusher, WebSocket (client + `ws` server), producer aliases | EventSource/SSE | GraphQL, jQuery, DOM access/create/events/mutation/tree, datastar, constants |
| **TypeScript** | (JS packs apply) | (JS packs apply) | (JS packs apply) | (JS packs apply) | interfaces, enums, type annotations |
| **JSX / React** | solid-router, nav links | (JS packs apply) | — | — | components, elements, calls |
| **HTML / ERB** | Rails nav links | — | — | — | elements, events, nav links |
| **templ / Vue / Svelte** | template components & nav | — | — | — | component rendering & element graph |

Custom coverage: write a pattern pack (see `patterns/*/`) and register it with
`polyflow patterns add <file>`.

> **Maturity note:** Go and the Rails/JS HTTP + broker surfaces are the most
> battle-tested. Ruby dynamic dispatch (method calls resolving to concrete call
> edges) and ERB view→controller wiring are partial and improving. See
> [Limitations](#limitations--honest-gaps).

## What the graph captures

**Node types** include: function, method, class, struct, interface, variable,
service, file, HTTP handler, HTTP client, gRPC handler/client, GraphQL
resolver/client, subscriber, worker, datastore, table, external service,
component, templ/DOM element, and signal.

**Edge types** include: calls, contains, declares, inherits, implements,
instantiates, returns, uses_type, navigates_to, component_impl, http_call,
job_enqueue/job_perform, sidekiq_enqueue/perform, kafka_publish, redis_publish,
pusher_subscribe, ws_upgrade/ws_connect, hub_subscribe/hub_broadcast, consumes,
response_of, and more.

Each edge carries a **confidence**:

- `static` — proven from the source AST (same-service calls, embeddings).
- `candidate` — inferred cross-service link (client path matches a route, broker
  symbol overlaps) not yet runtime-confirmed.
- `verified` — corroborated by runtime OTLP evidence.

## Runtime evidence (optional)

Static edges can be confirmed against real traffic by capturing OTLP traces:

```sh
polyflow capture start my-session
# ... exercise your services ...
polyflow capture stop my-session
polyflow index                       # re-index fuses captured evidence
# or import an existing dump:
polyflow capture ingest trace-dump.json
```

Map raw OTel `service.name` values to workspace service ids under
`evidence.runtime.service_names`. Runtime-corroborated edges become `verified`;
stale captures (older than `evidence.stale_after`, default 30 days) are flagged
without downgrading the edge.

## Multi-repo workspaces

A `workspace.yaml` can list services that live in different repositories (by
path). Cross-service links are inferred across all of them, so an agent can
trace a request from a frontend repo through an API into a background worker in a
third repo. Because the MCP/CLI reads the DB from the current directory, run the
server from the directory holding the multi-service `workspace.yaml`.

## Limitations & honest gaps

Stated plainly so you can judge fit:

- **Ruby dynamic dispatch** — method calls do not always resolve to concrete
  call edges; dynamic HTTP hosts and queue keys are resolved via targeted passes
  and otherwise **ledgered as named unresolved clues**, never invented.
- **ERB view → controller** navigation links are partial.
- **Cross-service inference needs a shared token** — a client whose host comes
  from an env var (e.g. `LYRA_HOST`) links only when the workspace/config exposes
  a matching service id or alias; otherwise it is an honest `config_not_found`
  ledger entry rather than a fabricated edge.
- **Single-workspace server** — one `.polyflow/graph.db` per working directory;
  no multi-workspace multiplexing in a single server process.
- **stdio transport only** — no SSE/HTTP MCP transport, so it targets local
  agents, not remote/hosted hosts.
- **Savings are task-dependent** — large wins on caller/blast-radius/
  cross-service questions; near break-even on trivial single-file lookups.

## Development

```sh
make build      # build ./dist/polyflow
make test       # run the suite
go vet ./...
```

- Graph schema version lives in `internal/graph/model.go` (`SchemaVersion`).
  Bumping it invalidates incremental caches on the next index.
- Pattern packs: `patterns/<language>/<name>.yaml` with a sibling
  `<name>_test/` fixture directory.
- Benchmarks and eval corpora live under `eval/`.

---

*Polyflow is a cross-service code flow analyzer focused on giving AI agents
accurate, token-efficient structural context.*
