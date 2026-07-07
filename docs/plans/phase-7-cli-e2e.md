# Phase 7 — CLI Commands + End-to-End Tests

**Status**: ⬜ Pending  
**Depends on**: Phases 1–6 complete  
**Design ref**: [CLI Commands](../polyflow-design.md#cli-commands), [Testing Strategy](../polyflow-design.md#testing-strategy), [Build Order](../polyflow-design.md#build-order-dependency-driven)

---

## Goal

Wire all CLI subcommands in `cmd/polyflow/main.go` to their real implementations. Then validate the entire pipeline — `workspace.yaml` → `polyflow index` → `polyflow serve` / `polyflow search` — with end-to-end tests using fixture workspaces.

---

## Current State

All CLI subcommands print `"not yet implemented"`. All the underlying packages (`parser`, `patterns`, `graph`, `linker`, `server`) are built and tested in isolation. This phase is the integration layer.

The `WorkspaceConfig` struct is also incomplete — it lacks `index.exclude` glob patterns and `settings` (port, snippet_lines, default_layout, default_depth), both of which are required by multiple commands.

---

## What Needs Building

### 0. Complete `WorkspaceConfig` (`workspace/config.go`)

The workspace.yaml schema in the design doc has fields not yet in the struct. Add them before implementing any commands that consume them:

```go
type IndexConfig struct {
    Exclude []string `yaml:"exclude"`
}

type Settings struct {
    SnippetLines   int    `yaml:"snippet_lines"`   // default 30
    DefaultLayout  string `yaml:"default_layout"`  // default "dagre-lr"
    DefaultDepth   int    `yaml:"default_depth"`   // default 5
    Port           int    `yaml:"port"`             // default 9400
}

type WorkspaceConfig struct {
    Name     string    `yaml:"name"`
    Version  string    `yaml:"version"`
    Services []Service `yaml:"services"`
    Links    []Link    `yaml:"links"`
    Patterns []string  `yaml:"patterns"`
    Index    IndexConfig `yaml:"index"`
    Settings Settings  `yaml:"settings"`
}
```

Add a `func (cfg *WorkspaceConfig) EffectivePort() int` helper that returns `cfg.Settings.Port` if set, else `meta.DefaultPort`.

### 1. `polyflow index`

**File**: `cmd/polyflow/main.go` — `indexCmd.RunE`

Pipeline:
1. Load `workspace.yaml` from current directory (or `--workspace` flag path)
2. Ensure `.polyflow/` directory exists (`os.MkdirAll`)
3. Open the tmp store at `.polyflow/graph.db.tmp` via `graph.NewSQLiteStore`
4. Load the default pattern registry (`patterns.DefaultRegistry("patterns/")`); also load any custom pattern paths from `cfg.Patterns`
5. Create a `patterns.TreeSitterMatcher`
6. For each service in `WorkspaceConfig.Services`:
   a. Walk `service.Path`, collecting files — skip paths matching any `cfg.Index.Exclude` globs
   b. Create `parser.WorkerPool(workers, matcher, service.Name)`
   c. Create `graph.BatchWriter(store)`
   d. Stream `WorkerPool.Run(files)` → write nodes+edges via `BatchWriter`; call `store.UpsertParseError` on partial errors
   e. Flush the `BatchWriter`
   f. Accumulate all nodes and edges for the linker
7. Run `linker.New(cfg).Link(allNodes, allEdges)` → write cross-service edges to store
8. Run `linker.ApplyHints(cfg.Links, allNodes, allEdges)` → annotate `target_service` on client edges
9. Write `last_indexed` timestamp via `store.SetMeta(ctx, "last_indexed", unixTimestamp)`
10. Atomic swap: `os.Rename(".polyflow/graph.db.tmp", ".polyflow/graph.db")`
11. Print progress summary

Flags:
- `--workspace <path>` — path to `workspace.yaml` (default: `./workspace.yaml`)
- `--workers <n>` — parser worker pool size (default: `runtime.GOMAXPROCS(0)`)
- `--full` — reserved flag for v1.5 incremental indexing; for v1 always does a full re-index

Progress output (match design doc format):
```
Scanning services...
  service-name: N files (Language)

Indexing [████████░░░░] 67% (1,456/2,171 files)
  Parsing: 12 workers active

Done. 2,171 files indexed in 18.4s
  Nodes: 34,521 | Edges: 87,203 | Links: 142 cross-service
  Errors: 3 files (run `polyflow status --errors` for details)
```

Use a goroutine printing progress every 250ms; overwrite the same line using `\r`.

### 2. `polyflow serve`

**File**: `cmd/polyflow/main.go` — `serveCmd.RunE`

Steps:
1. Load `workspace.yaml`; resolve DB path: `.polyflow/graph.db`
2. Open `graph.NewSQLiteStore(dbPath)` — error with actionable message if DB missing ("run `polyflow index` first")
3. Call `store.BuildIndex(ctx)` → `*graph.AdjacencyIndex`
4. Create `server.New(store, idx)`
5. Start `fsnotify` watcher on `.polyflow/graph.db`; on `Write`/`Create` event: reopen store, `BuildIndex`, call `server.Reload(newIdx)`, broadcast `graph_updated` SSE event
6. Call `server.StartOn(host, port)` — host and port from flags, falling back to `cfg.EffectivePort()` and `"127.0.0.1"`
7. Open browser: `exec.Command("open", url).Start()` on macOS/Linux; `exec.Command("cmd", "/c", "start", url)` on Windows

Flags:
- `--port <n>` — override port
- `--host <addr>` — explicit host; if `"0.0.0.0"` print LAN exposure warning
- `--no-open` — skip browser launch

### 3. `polyflow search`

**File**: `cmd/polyflow/main.go` — `searchCmd.RunE`

Steps:
1. Open `graph.NewSQLiteStore(".polyflow/graph.db")`
2. Call `store.SearchNodes(ctx, args[0], limit)`
3. Print in design-doc format:
```
  FUNCTION  CreateUser()    internal/handlers/user.go:42    [service-name]
  CLASS     UserController  app/controllers/user.rb:3       [service-name]
```

Flags:
- `--format json` — output raw JSON array
- `--limit <n>` — max results (default 20)

### 4. `polyflow status`

**File**: `cmd/polyflow/main.go` — `statusCmd.RunE`

Steps:
1. Load `workspace.yaml`, open store
2. `store.Stats(ctx)` → node/edge counts
3. `store.GetMeta(ctx, "last_indexed")` → timestamp; format as "2026-07-06 14:32:01 (5 minutes ago)"
4. `store.ListParseErrors(ctx)` → error count
5. Print:
```
  Workspace: my-platform
  Services: 4 (2 Go, 1 JS, 1 Ruby)
  Last indexed: 2026-07-06 14:32:01 (5 minutes ago)
  Files: 2,171 | Nodes: 34,521 | Edges: 87,203
  Cross-service links: 142
  Parse errors: 3 files (--errors for details)
```

Flag `--errors`: list each parse error from `store.ListParseErrors()`:
```
  PARTIAL  service/internal/handlers/broken.go:17    (1 error)
```

### 5. `polyflow patterns`

Two subcommands under `patternsCmd`:

**`polyflow patterns list`**:
1. Load `patterns.DefaultRegistry("patterns/")`
2. Print each pattern: name, language, category, description
3. Flag `--language <lang>` to filter

**`polyflow patterns add <file>`**:
1. Validate the given YAML file can be loaded via `patterns.LoadFile(path)`
2. Append the path to `cfg.Patterns` in `workspace.yaml` and rewrite the file
3. Print confirmation

### 6. `polyflow config`

Nine subcommands under `configCmd`. All read `workspace.yaml`, mutate the struct, and rewrite the file atomically (write to `.tmp`, rename).

| Subcommand | Flags | Action |
|-----------|-------|--------|
| `config show` | — | Pretty-print current `workspace.yaml` |
| `config set <key> <value>` | — | Set a settings key (e.g. `port 9401`, `snippet_lines 50`) |
| `config service add` | `--name`, `--path`, `--language`, `--frameworks` | Append service to `services:` |
| `config service remove` | `--name` | Remove service by name |
| `config service list` | — | List all services with path and language |
| `config link add` | `--from`, `--to`, `--via`, `--base-url` | Append link entry |
| `config link remove` | `--from`, `--to` | Remove link by from+to pair |
| `config exclude add <pattern>` | — | Append glob to `index.exclude` |
| `config exclude remove <pattern>` | — | Remove glob from `index.exclude` |

For `config set`, supported keys: `port`, `snippet_lines`, `default_layout`, `default_depth`.

### 7. `polyflow context`

**File**: `cmd/polyflow/main.go` — `contextCmd.RunE`

Flags:
- `--target <query>` — search query to find the root node (required)
- `--task <type>` — one of `impact`, `generate`, `debug`, `refactor` (default: `debug`)

Task type determines traversal depth and output fields:

| Task | Upstream depth | Downstream depth | Extra fields |
|------|---------------|-----------------|--------------|
| `debug` | 3 | 5 | Full source snippet, DOM impact |
| `impact` | 0 | unlimited | Callers list, affected services |
| `generate` | 2 | 3 | Related patterns (siblings), data shapes |
| `refactor` | unlimited | 2 | All dependents that must update |

Output: structured JSON matching the AI agent context format in the design doc (target node, data_flow, upstream, downstream, dom_impact, consumers_in_other_services).

### 8. `polyflow impact`

**File**: `cmd/polyflow/main.go` — `impactCmd.RunE`

Flags:
- `--target <query>` — search query (required)

Steps:
1. `SearchNodes` to find the target node
2. `graph.Ancestors(idx, rootID, maxDepth=0)` — full blast radius
3. Group results by service
4. Output JSON list of affected nodes with service, file, line, type

### 9. `polyflow init`

Interactive wizard reading from stdin:

```
Workspace name: _
Add a service:
  Name: _
  Path: _
  Language (go/javascript/ruby/typescript): _
  Frameworks (optional, comma-separated): _
Add another service? [y/N]: _
```

Write `workspace.yaml` in the current directory. If it already exists, prompt: "workspace.yaml already exists. Overwrite? [y/N]".

### 10. `meta` Table in SQLite (`graph/store.go`)

Required by `polyflow status` (last_indexed) and potentially future versioning. Add to the schema:

```sql
CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
```

Add to `Store` interface and `SQLiteStore`:
```go
SetMeta(ctx context.Context, key, value string) error
GetMeta(ctx context.Context, key string) (string, error)
```

### 11. SSE Broadcast in Server (`server/server.go`)

Required by `polyflow serve` to push `graph_updated` after DB swap.

Add to `Server`:
```go
type Server struct {
    db       graph.Store
    idx      *graph.AdjacencyIndex
    idxMu    sync.RWMutex
    mux      *http.ServeMux
    broadcast chan string  // event payloads
    clients   map[chan string]struct{}
    clientsMu sync.Mutex
}
```

- `handleEvents` registers a per-client channel, streams events, deregisters on disconnect
- `Reload(idx *graph.AdjacencyIndex)` swaps the adjacency index under `idxMu` and sends `"graph_updated"` to `broadcast`
- A background goroutine fans `broadcast` out to all `clients`

### 12. Performance Benchmarks

The design doc requires `Benchmark*` functions for performance-critical paths and specifies these targets:

| Metric | Target |
|--------|--------|
| Index time (cold, 10k files) | < 30s |
| Graph query (trace, depth 10) | < 200ms |
| Node search (FTS5) | < 5ms |
| Serve startup | < 1s |

Add benchmark functions to existing test files:

| File | Benchmark |
|------|-----------|
| `internal/graph/query_test.go` | `BenchmarkTraverse_Depth10` — 10k node graph, depth 10 BFS |
| `internal/graph/store_test.go` | `BenchmarkSearchNodes` — FTS5 search on 10k nodes |
| `internal/patterns/matcher_test.go` | `BenchmarkMatch_GoFile` — match a 500-line Go file against all Go patterns |
| `internal/parser/parser_test.go` | `BenchmarkWorkerPool_100Files` — parse 100 fixture files concurrently |

Run with: `go test ./... -bench=. -benchtime=5s`

### 13. End-to-End Tests

**Location**: `internal/e2e/` (new package)

**Fixture workspace**: `internal/e2e/testdata/workspace/`

Structure:
```
testdata/workspace/
├── workspace.yaml
├── svc-go/
│   └── main.go          (Chi routes + net/http client calls + function definitions)
├── svc-js/
│   └── app.js           (axios calls to /api/*)
├── svc-ruby/
│   └── app.rb           (Rails routes + Faraday client)
└── svc-templ/
    └── page.templ        (data-on-* Datastar actions, data-bind, id/class attributes)
```

`workspace.yaml` fixture:
```yaml
name: "e2e-workspace"
version: "1"
services:
  - name: svc-go
    path: ./svc-go
    language: go
    frameworks: [chi]
  - name: svc-js
    path: ./svc-js
    language: javascript
    frameworks: [axios]
  - name: svc-ruby
    path: ./svc-ruby
    language: ruby
    frameworks: [rails, faraday]
  - name: svc-templ
    path: ./svc-templ
    language: go
links:
  - from: svc-js
    to: svc-go
    base_url: "/api"
index:
  exclude:
    - "**/vendor/**"
    - "**/node_modules/**"
settings:
  port: 9400
  snippet_lines: 30
```

`svc-go/main.go` fixture must define at minimum:
- A Chi route `r.Post("/users", CreateUser)`
- A function `CreateUser` that calls another function
- A `net/http` client call to another service

`svc-templ/page.templ` fixture must define at minimum:
- A `templ` component with `data-on-click="@post('/api/users')"` → cross-links to svc-go handler
- An element with a static `id` attribute → creates a DOM target node

**Test cases** (`e2e_test.go`):

| Test | What it asserts |
|------|----------------|
| `TestIndex_NodeCount` | `Stats()` returns node count ≥ expected minimum per service |
| `TestIndex_CrossServiceLinks` | Linker produces ≥ 1 cross-service edge between svc-js and svc-go |
| `TestIndex_TemplDatastar` | svc-templ Datastar `data-on-click` produces a `datastar_action` edge to svc-go handler |
| `TestIndex_ParseErrors` | Files with intentional syntax errors produce `ParseError` records, not panics |
| `TestIndex_ExcludeGlobs` | Files matching `index.exclude` patterns are not indexed |
| `TestSearch_FindsFunction` | `SearchNodes("CreateUser")` returns the function node from svc-go |
| `TestTrace_Forward` | Trace from Chi handler node finds downstream `calls` edges |
| `TestTrace_Backward` | Trace from axios client node finds upstream callers |
| `TestServe_Graph` | Start server, `GET /api/graph` returns Cytoscape JSON with nodes > 0 |
| `TestServe_Search` | `GET /api/graph/search?q=create` returns non-empty array |
| `TestServe_Trace` | `GET /api/graph/trace?root=<id>&direction=forward&depth=5` returns Cytoscape JSON |

Each test:
1. Creates a temp directory
2. Runs the index pipeline inline (calls the same functions as `indexCmd`, no subprocess)
3. Opens the resulting DB
4. Asserts

---

## File Changes

| File | Action |
|------|--------|
| `internal/workspace/config.go` | Add `IndexConfig`, `Settings` structs; add `Patterns []string`, `Index`, `Settings` fields; add `EffectivePort()` |
| `internal/graph/store.go` | Add `meta` table, `SetMeta`/`GetMeta` to `Store` interface and `SQLiteStore` |
| `internal/server/server.go` | Add SSE broadcast channel + `clients` map; add `Reload(idx)` method |
| `internal/server/handlers.go` | Wire SSE broadcast to `handleEvents` |
| `cmd/polyflow/main.go` | Implement all command `RunE` bodies; wire `configCmd` subcommands; wire `patternsCmd` subcommands |
| `internal/graph/query_test.go` | Add `BenchmarkTraverse_Depth10` |
| `internal/graph/store_test.go` | Add `BenchmarkSearchNodes` |
| `internal/patterns/matcher_test.go` | Add `BenchmarkMatch_GoFile` |
| `internal/parser/parser_test.go` | Add `BenchmarkWorkerPool_100Files` |
| `internal/e2e/e2e_test.go` | New — 11 end-to-end test cases |
| `internal/e2e/testdata/workspace/workspace.yaml` | New fixture |
| `internal/e2e/testdata/workspace/svc-go/main.go` | New fixture |
| `internal/e2e/testdata/workspace/svc-js/app.js` | New fixture |
| `internal/e2e/testdata/workspace/svc-ruby/app.rb` | New fixture |
| `internal/e2e/testdata/workspace/svc-templ/page.templ` | New fixture |
| `Makefile` | Add `test-e2e`, `bench` targets |

---

## Acceptance Criteria

- [ ] `polyflow index` walks all service paths respecting `index.exclude`, writes nodes/edges, prints progress
- [ ] `polyflow serve` opens store, builds index, starts on configured port, opens browser
- [ ] `polyflow search <query>` prints matching nodes in table format; `--format json` outputs raw JSON
- [ ] `polyflow status` prints workspace summary with last-indexed time
- [ ] `polyflow status --errors` lists files with parse errors
- [ ] `polyflow patterns list` prints all loaded patterns; `--language` filters by language
- [ ] `polyflow patterns add <file>` registers a custom pattern and persists it to `workspace.yaml`
- [ ] All 9 `polyflow config` subcommands read and write `workspace.yaml` correctly
- [ ] `polyflow context --target <query> --task <type>` outputs structured JSON per task type
- [ ] `polyflow impact --target <query>` outputs blast-radius JSON grouped by service
- [ ] `polyflow init` creates a valid `workspace.yaml`
- [ ] All 11 E2E tests pass
- [ ] All 4 benchmark functions exist and run without error (`go test ./... -bench=.`)
- [ ] `go test ./...` passes with ≥ 90% coverage across all packages
- [ ] `make build` produces a working binary at `dist/polyflow`

---

## What Is Explicitly Out of Scope

- Incremental re-index (file hash comparison) — full re-index only for v1; incremental is v1.5
- Git URL support in workspace.yaml — local paths only for v1
- Watch mode / live re-index on file save — v2
- `polyflow suggest` command — v2
- MCP server mode — v2
