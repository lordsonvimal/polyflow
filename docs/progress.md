# Polyflow — Implementation Progress

Reference: [polyflow-design.md](./polyflow-design.md)

---

## Status Summary

| Phase | Area | Status | Coverage |
|-------|------|--------|----------|
| 1 | Foundation (meta, workspace, CLI skeleton) | ✅ Done | — |
| 4 | Graph / SQLite store | ✅ Done | 84.7% |
| 3 | Pattern loading + tree-sitter matcher | ✅ Done | 75.0% |
| 2 | Per-language parsers | ✅ Done | 100% |
| 5 | Cross-service linker | ⬜ Pending | — |
| 6 | HTTP server + frontend | 🟡 Partial | — |
| 7 | End-to-end tests | ⬜ Pending | — |

---

## Completed

### Phase 1 — Foundation
`internal/meta/`, `internal/workspace/`, `cmd/polyflow/`

- `meta.go`: single source of truth for name, version, paths, default port
- `workspace/config.go`: `WorkspaceConfig` struct; `Load()` parses `workspace.yaml`
- `workspace/detect.go`: `DetectFrameworks()` stub (wired, not yet filled)
- `cmd/polyflow/main.go`: Cobra CLI with all subcommands registered (`init`, `index`, `serve`, `search`, `status`, `patterns`, `context`, `impact`, `config`) — all print "not yet implemented"

See: [CLI Commands](./polyflow-design.md#cli-commands), [Workspace Configuration Schema](./polyflow-design.md#workspace-configuration-schema)

---

### Phase 4 — Graph / SQLite Store
`internal/graph/`

- `model.go`: `Node`, `Edge`, `NodeType`, `EdgeType` constants, `AdjacencyIndex` with `AddNode`/`AddEdge`
- `store.go`: Full `SQLiteStore` implementing the `Store` interface — `UpsertNode`, `UpsertEdge`, `GetNode`, `GetEdge`, `SearchNodes` (FTS5), `ListEdgesFrom`, `ListEdgesTo`, `BuildIndex`, `Stats`, `WithTx`, `Close`. WAL mode + foreign keys on.
- `writer.go`: `BatchWriter` with auto-flush at configurable batch boundary; each flush is a single transaction. `NewBatchWriterWithSize` for testing.
- `query.go`: `Traverse`, `Ancestors`, `Descendants` — BFS/DFS over `AdjacencyIndex`, cycle-safe, depth-limited.
- 25 tests passing across store, query, writer, and error paths.

See: [SQLite Schema & Storage Design](./polyflow-design.md#sqlite-schema--storage-design), [Graph Data Model](./polyflow-design.md#graph-data-model)

---

### Phase 3 — Pattern Loading + Tree-sitter Matcher
`internal/patterns/`, `patterns/`

- `loader.go`: `PatternFile`, `Pattern` (with `Match` filters and `ExtractConfig`), `Capture`; `Load(dir)`, `LoadFile(path)`, `DefaultRegistry(dir)`
- `registry.go`: Thread-safe `Registry` with `Register`, `RegisterFile`, `Get`, `List`, `Languages`
- `matcher.go`: `TreeSitterMatcher` — compiles tree-sitter queries per language on first use (cached, mutex-protected); `Match()` runs queries, applies `FilterPredicates` for `#eq?`/`#match?`, applies YAML-level `Match` value filters; `MatchToGraph()` maps pattern names to `NodeType`/`EdgeType` via keyword classification
- Language grammars wired: `go`, `javascript`, `typescript`, `ruby`
- 30 YAML pattern files under `patterns/` (Go, JavaScript, TypeScript, Ruby)
- 10 tests passing; testdata fixtures for chi routes, net/http client, axios, fetch

See: [YAML Pattern Registry Format](./polyflow-design.md#yaml-pattern-registry-format), [Built-in Pattern Coverage (v1)](./polyflow-design.md#built-in-pattern-coverage-v1)

---

## Pending (implementation order)

### Phase 2 — Per-Language Parsers
`internal/parser/`

- `parser.go`: Updated `Parser` interface signature (`Parse(file, service string, matcher *patterns.TreeSitterMatcher)`); `WorkerPool` accepts matcher + service; `setLanguage` helper stamps `Language` on nodes; `ForFile` and `Register` unchanged.
- `go.go`: Reads file, calls `TreeSitterMatcher.Match("go", ...)`, maps via `MatchToGraph`, stamps `Language:"go"`. Partial results on parse error.
- `javascript.go`: Handles `.js`/`.ts`/`.jsx`/`.tsx`/`.mjs`; dispatches to `"javascript"` or `"typescript"` grammar based on extension.
- `ruby.go`: Same pattern for `.rb`/`.rake` files using `"ruby"` grammar.
- `templ.go`: Regex-based scanner for `.templ` files (a-h/templ not yet a dep) — detects `templ`/`func` component declarations, `data-on-*` Datastar actions, `data-bind`/`data-signals`/`data-model`, `data-text`/`data-indicator` signal reads, `href`/`action` links.
- 15 tests passing in `parser_test.go`; testdata fixtures: `routes.go`, `client.js`, `app.rb`, `page.templ`.

See: [Architecture → parser/](./polyflow-design.md#go-module-structure), [Parser Strategy by Trigger](./polyflow-design.md#parser-strategy-by-trigger), [Datastar/SSE Pattern Detection](./polyflow-design.md#datastarsse-pattern-detection)

---

### Phase 5 — Cross-Service Linker
`internal/linker/`

Connect HTTP clients in one service to HTTP handlers in another:
- `linker.go`: `Linker.Link(nodes, edges []graph.Node/Edge)` — normalize URL paths to wildcard form, match `http_request` edges to `http_handler` nodes across services, emit cross-service `http_request` edges with confidence scoring (`static` / `inferred` / `partial` / `unknown`)
- `hints.go`: read `links:` entries from `workspace.yaml`, apply base URL prefixes and explicit service→service mappings
- Constraint-based resolver: path normalization (`/users/:id` → `/users/*`), constant propagation (up to 3 hops), OpenAPI override if `openapi:` set in workspace config

See: [Cross-Service Linking](./polyflow-design.md#cross-service-linking), [Cross-Service Linking — Constraint-Based Resolver](./polyflow-design.md#cross-service-linking--constraint-based-resolver)

---

### Phase 6 — HTTP Server + Frontend (complete)
`internal/server/`, `web/`

Server handlers exist and compile; `handleTrace` returns 501. Still needed:
- `handleTrace`: implement BFS/DFS subgraph extraction using `AdjacencyIndex` (Phases 4+2 now provide this)
- `cytoscape.go`: verify `ToCytoscapeJSON` output matches what the SolidJS frontend expects
- Frontend: run `npm install` in `web/`, build with Vite, verify the SolidJS app loads and the graph/search/detail panels wire to the API
- `polyflow serve` command: open `SQLiteStore`, call `BuildIndex`, start `Server`

See: [API Endpoints](./polyflow-design.md#api-endpoints), [Flow Tracing UX](./polyflow-design.md#flow-tracing-ux-core-feature)

---

### Phase 7 — CLI Commands + End-to-End Tests
`cmd/polyflow/`, `internal/`

Wire all CLI subcommands to real implementations:
- `polyflow init`: interactive prompts → write `workspace.yaml`
- `polyflow index`: `WorkspaceConfig` → `WorkerPool` → `BatchWriter` → `Linker` → `SQLiteStore`; atomic DB swap (`graph.db.tmp` → `graph.db`)
- `polyflow serve`: open store, `BuildIndex`, start server, open browser
- `polyflow search <query>`: open store, call `SearchNodes`, print results
- `polyflow status`: open store, call `Stats`, print summary
- `polyflow patterns list`: call `DefaultRegistry`, list all patterns
- `polyflow context` / `polyflow impact`: JSON output from graph traversal

E2E tests: fixture workspace with sample Go + JS + Ruby services → `polyflow index` → assert node/edge counts, cross-service links, search results.

See: [CLI Commands](./polyflow-design.md#cli-commands), [Testing Strategy](./polyflow-design.md#testing-strategy), [Build Order](./polyflow-design.md#build-order-dependency-driven)

---

## Deferred (v1.5+)

These are explicitly post-v1 in the design and should not block the above phases:

- Go compiler API (`golang.org/x/tools/go/packages`) — replaces tree-sitter for Go semantic accuracy
- TypeScript compiler API — replaces tree-sitter for TS type-resolved analysis
- OpenAPI/Swagger as authoritative cross-service link source
- SCIP output + consumption
- Ruby Prism + Sorbet integration
- Runtime enrichment (opt-in instrumentation)
- MCP server mode (`polyflow mcp`)
- `polyflow suggest` command
- Git URL workspace support, watch mode

See: [Upgraded Architecture (v1.5+)](./polyflow-design.md#upgraded-architecture-v15), [v2+ Roadmap](./polyflow-design.md#v2-roadmap)
