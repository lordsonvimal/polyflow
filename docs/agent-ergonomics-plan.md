# Polyflow — Agent Ergonomics Plan (Tier A)

Status legend: `pending` · `in progress` · `done (commit <sha>)`

**Goal.** Three agent-facing affordances that make an AI agent using polyflow *cheaper*
(fewer tokens), *faster* (fewer turns), and *more accurate* (reads the exact code, not the
whole file):

- **A.1 — MCP on/off toggle + health.** A one-command way to disable/enable polyflow's MCP
  tools for the *next* agent session, so the same agent can be A/B-measured (with vs. without
  polyflow) without editing client config. Plus a `status` health surface so an agent can tell
  whether the graph is present and fresh before trusting it.
- **A.2 — Project hierarchy (`hierarchy` tool).** One bounded MCP call that returns the shape of
  the workspace (service → directory → file → top-level symbols, with roll-up counts) so an
  agent orienting in an unfamiliar repo replaces dozens of `ls`/grep turns with a single call.
- **A.3 — Span-exact reads (`read` tool + span-aware snippets).** Return exactly the source
  lines of a symbol (function / method / class / struct / interface) — `Line..end_line` — instead
  of a fixed-size window or the whole file. Kills the "re-open the file to be safe" token sink.

**Non-goals.** No new language coverage, no new edge classes, no runtime evidence. This tier is
pure delivery ergonomics over the graph that already exists.

**Prerequisites banner.** Reuse, do not rebuild:
- `internal/mcpserver/mcpserver.go` — tool registration (`mcp.AddTool`), the `Store` interface
  (line 51), `Server.snapshot()` (line 88) that hands each handler a consistent
  `store+idx+searcher` triple. A.2/A.3 add handlers here following the `entrypoints`
  (`internal/mcpserver/entrypoints.go`) enumeration pattern verbatim.
- `internal/graph/model.go` — `AdjacencyIndex{Nodes, OutEdges, InEdges}` (line 347), `Node`
  (line 209, has `Line`, `Meta`, query-only `Snippet` at line 221), `SchemaVersion` (line 206).
- `internal/budget/budget.go:81` — `Snippet(root, file, start, n)` reads a fixed N-line window;
  A.3 adds a span-aware sibling and points it at `meta["end_line"]`.
- `internal/patterns/matcher.go` — **already** captures declaration end (`MatchResult.EndLine`,
  line 33; from tree-sitter `EndPoint().Row`, line 281) and writes `meta["end_line"]` (line 884).
  A.3 only has to fill the *Go-semantic* gap and consume the key that already exists elsewhere.
- `cmd/polyflow/main.go` — cobra command tree (`rootCmd.AddCommand`, line 61); `statusCmd`
  (line 710, `runStatus`) already prints workspace/services/nodes/edges/last-indexed/parse-errors.
  A.1 extends `runStatus` and adds a `mcp on|off` subcommand under `mcpCmd` (`cmd/polyflow/mcp.go`).
- `internal/meta` — `DBDir=".polyflow"`, `ConfigFile`, `Version`.

Self-contained per project convention: implementable by a contributor who reads only this file
plus the interfaces it pins above.

Bump `graph.SchemaVersion` **once, in A.3** (25→26): A.3 populates `meta["end_line"]` on
Go-semantic nodes, so cached graphs must be re-indexed to gain span-exact reads. A.1 and A.2 add
no stored shape and need no bump.

---

## Design note — why #1 is a *toggle*, not a daemon (and the cross-agent alternatives)

polyflow's MCP server is **stdio-only** (`cmd/polyflow/mcp.go:80`,
`srv.Run(ctx, &mcp.StdioTransport{})`). It is **not a long-lived daemon**: the agent *client*
(Claude Code, Cursor, Windsurf, …) spawns `polyflow mcp` as a child process at session start and
kills it at session end. There is therefore no server process for a `start`/`stop` command to
manage, and lifecycle spends **zero agent tokens** — it is transport plumbing the model never sees.

So "stop/start the MCP" is reframed as **"make the next session run with or without polyflow's
tools, on demand, for A/B token measurement."** Ways to do that:

| Approach | Works in | Cost | Caveat |
|---|---|---|---|
| **A.1 state-file gate** (`polyflow mcp off` / `on`) | **any stdio MCP client** | one command | takes effect on next session/reconnect |
| Client-native disable (Claude Code `.mcp.json` + `disabledMcpjsonServers`, or remove/re-add) | Claude Code only | edit JSON + restart | per-client syntax, easy to forget to restore |
| Cursor/Windsurf settings toggle | that client only | UI click + restart | not scriptable |
| `eval/agent-bench/` harness | CI/bench only | full run | not interactive |

**Chosen: the A.1 state-file gate**, because it is the *only* option that is (a) one command,
(b) identical across every MCP client, and (c) scriptable for repeatable A/B runs. The other rows
are documented in `README.md` as alternatives, not built.

**Why a gate and not "kill the process":** killing mid-session just makes the client re-spawn or
error; it does not give a clean "no polyflow" baseline. A gate that makes `polyflow mcp` advertise
**zero query tools** at startup gives the model a genuinely polyflow-free tool list — the correct
control for a token comparison — while leaving `polyflow index`/`status`/`serve` fully working.

---

## Phase A.1 — MCP on/off toggle + `status` health  `done (commit 39b1fcf)`

**Problem.** (1) No scriptable, client-agnostic way to run an agent session *without* polyflow's
tools for a token A/B. (2) `status` reports counts but not **freshness** — an agent cannot tell if
the graph is stale relative to the source, so it may trust a graph that predates recent edits.

**Design.**

*Gate.* A marker file `\<DBDir>/mcp.disabled` (i.e. `.polyflow/mcp.disabled`). Presence = disabled.
`runMCP` reads it once at startup (before `mcpserver.New`); when present it registers **only** a
single `status`-style probe tool and skips the seven query tools, then serves normally. The model
sees a polyflow server with no code-graph tools → clean control.

*Commands* under the existing `mcpCmd` (`cmd/polyflow/mcp.go`):
- `polyflow mcp off` → create the marker, print the reconnect reminder.
- `polyflow mcp on`  → remove the marker, print the reconnect reminder.
- `polyflow mcp status` → print `enabled` / `disabled (run 'polyflow mcp on' to re-enable)`.

*Registration change* (`internal/mcpserver/mcpserver.go`, `New`): add a parameter
`enabled bool`; when false, register only the probe tool. Keep the signature additive by threading
it from `runMCP`.

**Pinned interface.**

```go
// cmd/polyflow/mcp.go
func mcpMarkerPath() string { return filepath.Join(meta.DBDir, "mcp.disabled") }

func mcpEnabled() bool { _, err := os.Stat(mcpMarkerPath()); return os.IsNotExist(err) }

// new subcommands, registered in mcp.go init():
//   polyflow mcp on | off | status
// runMCP change:
//   enabled := mcpEnabled()
//   srv, handle := mcpserver.New(store, idx, meta.Version, loadStaleAfter(meta.ConfigFile), enabled)
//   if !enabled { fmt.Fprintln(os.Stderr, "polyflow mcp: DISABLED (query tools not registered)") }
```

```go
// internal/mcpserver/mcpserver.go
func New(store Store, idx *graph.AdjacencyIndex, version string,
	staleAfter time.Duration, enabled bool) (*mcp.Server, *Server) {
	s := &Server{store: store, idx: idx, staleAfter: staleAfter}
	srv := mcp.NewServer(&mcp.Implementation{Name: "polyflow", Version: version}, nil)
	if !enabled {
		mcp.AddTool(srv, &mcp.Tool{Name: "status",
			Description: "polyflow is DISABLED for this session (run `polyflow mcp on` then " +
				"reconnect). No code-graph tools are available."}, s.disabledProbe)
		return srv, s
	}
	// ... existing seven AddTool calls unchanged ...
}
```

*`status` freshness* (`cmd/polyflow/main.go` `runStatus`): after the existing counts, compare
`last_indexed` (already read) against the newest source mtime under each service path; print
`Freshness: up to date` or `Freshness: STALE — N file(s) changed since last index (run 'polyflow index')`.
Reuse the file walk `index` already performs (respect `index.exclude` / `.polyflowignore`); cap the
walk and short-circuit on the first newer file so `status` stays instant.

**Worked example.**

```
$ polyflow mcp off
polyflow MCP disabled. Reconnect your agent (restart the session) to run WITHOUT polyflow tools.
$ polyflow mcp status
disabled (run 'polyflow mcp on' to re-enable)
$ polyflow status
  Workspace: fleet
  Services: 7 (3 Ruby, 2 Go, 2 TypeScript)
  Nodes: 33789 | Edges: 50826 | Cross-service links: 1240
  Last indexed: 2026-07-27 14:02:11 (18h ago)
  Freshness: STALE — 3 file(s) changed since last index (run 'polyflow index')
```

**A/B recipe (documented in README).**
```
polyflow mcp on  && <reconnect>  # measure "with polyflow": ctx tokens / turns / recall
polyflow mcp off && <reconnect>  # measure "without": same task, tools absent
```

**Tests.**
- `mcpEnabled()` true when no marker, false when present; `off`/`on` create/remove it idempotently.
- `New(..., enabled=false)` registers exactly one tool named `status`; `enabled=true` registers the
  seven query tools (assert names).
- `runStatus` prints `STALE` when a service file mtime > `last_indexed`, `up to date` otherwise
  (table test with a temp workspace + touched file).

**Outcome.** One scriptable, client-agnostic A/B switch (0 agent tokens); agents gain a freshness
signal so they never silently trust a stale graph. **No schema bump.**

---

## Phase A.2 — Project hierarchy tool (`hierarchy`)  `pending`

**Problem.** None of the seven tools give a *structural overview*. An agent orienting in an
unfamiliar repo still runs `ls`/`find`/grep to learn the layout — the exact multi-turn token sink
polyflow exists to remove. The graph already holds the answer: `service` → `file` → symbol nodes,
joined by `contains` edges (`EdgeTypeContains`).

**Design.** A read-only MCP tool `hierarchy` that walks `idx.Nodes` (same enumeration pattern as
`entrypoints`, `internal/mcpserver/entrypoints.go:47`) and builds a **budgeted tree**:
`service → directory → file → top-level symbols`. Depth- and count-capped with roll-ups so a
33k-node graph never dumps everything.

Inputs:
- `service` (optional) — scope to one service; empty = all services (rolled up to service+dir level).
- `path` (optional) — a file or directory prefix to expand under (e.g. `internal/parser`).
- `depth` (optional, default 2) — `1`=services, `2`=+dirs/files, `3`=+top-level symbols.
- `max_tokens` (optional) — over budget, deepest level collapses to counts (like `impact`/`flows`).

Symbol selection at depth 3: only **top-level declared** nodes (functions, methods, classes,
structs, interfaces, http_handlers) — filter by node type, skip variable/param/element noise. Order
deterministically (service, path, line, id) per bug-class rule 2 (never iterate a map into output).

**Pinned interface.**

```go
// internal/mcpserver/hierarchy.go
type hierarchyInput struct {
	Service   string `json:"service,omitempty"`
	Path      string `json:"path,omitempty"`
	Depth     int    `json:"depth,omitempty"`      // 1|2|3, default 2
	MaxTokens int    `json:"max_tokens,omitempty"`
}
type hierNode struct {
	Name     string      `json:"name"`               // service / dir / file / symbol label
	Kind     string      `json:"kind"`               // "service"|"dir"|"file"|<node type>
	File     string      `json:"file,omitempty"`
	Line     int         `json:"line,omitempty"`
	ID       string      `json:"id,omitempty"`       // node id at symbol level → feed to read/context
	Count    int         `json:"count,omitempty"`    // children rolled up (when collapsed)
	Children []*hierNode `json:"children,omitempty"`
}
type hierarchyOutput struct {
	Workspace string      `json:"workspace"`
	Roots     []*hierNode `json:"roots"`
	Truncated bool        `json:"truncated,omitempty"` // true when max_tokens forced roll-up
}

func (s *Server) hierarchy(ctx context.Context, req *mcp.CallToolRequest,
	in hierarchyInput) (*mcp.CallToolResult, any, error)
```

Registration (`mcpserver.go` `New`, alongside the others):

```go
mcp.AddTool(srv, &mcp.Tool{
	Name: "hierarchy",
	Description: "Return the structural shape of the workspace: service → directory → file → " +
		"top-level symbols, with roll-up counts. Use this FIRST to orient in an unfamiliar repo " +
		"instead of ls/find/grep — one call replaces directory exploration. Scope with service/path; " +
		"raise depth to 3 for symbols. Symbol-level `id` feeds directly into read, context, or impact.",
}, s.hierarchy)
```

**Worked example.** `hierarchy(service="polyflow", path="internal/mcpserver", depth=3)`:

```json
{"workspace":"polyflow","roots":[{"name":"polyflow","kind":"service","children":[
  {"name":"internal/mcpserver","kind":"dir","children":[
    {"name":"mcpserver.go","kind":"file","file":"internal/mcpserver/mcpserver.go","children":[
      {"name":"New","kind":"function","file":"internal/mcpserver/mcpserver.go","line":98,
       "id":"polyflow:internal/mcpserver/mcpserver.go:function:New:98"},
      {"name":"search","kind":"method","line":250,"id":"...:method:search:250"}]},
    {"name":"entrypoints.go","kind":"file","count":6}]}]}]}
```

**Tests.**
- Fixture workspace (2 services, nested dirs) → depth 1 returns only service roots; depth 2 adds
  dir/file; depth 3 adds symbol nodes with usable `id`s.
- `service=` filter excludes other services; `path=` prefix scopes correctly.
- `max_tokens` small → `Truncated=true` and deepest level collapses to `Count` (no symbol dump).
- Deterministic ordering across two runs on the same graph.

**Outcome.** One bounded call replaces the orientation grep/ls loop; symbol `id`s chain straight
into `read`/`context`/`impact`. **No schema bump.**

---

## Phase A.3 — Span-exact reads (`read` tool + span-aware snippets)  `done (commit cf2c762)`

> **Implementation note.** The Go-semantic `end_line` gap was NOT in
> `go_semantic.go` (those sites are SSA position lookups, not node creation).
> `function`/`method` nodes already carried `end_line` (they come from the
> tree-sitter matcher). The real gap was `struct`/`interface` nodes, created in
> `internal/parser/go_variables.go` from the type checker — filled via a one-time
> `*ast.TypeSpec` → closing-brace-line map keyed on the type name's `token.Pos`.
> Verified on the polyflow graph: struct 276/276, function 777/777, local
> interface 9/9 now carry `end_line` (the 11 external/no-file interface nodes
> legitimately have no readable span).

**Problem.** `budget.Snippet` (`internal/budget/budget.go:81`) reads a **fixed N-line window** from
a node's `Line`. A 200-line function is truncated; a 5-line one over-reads — either way the agent
re-opens the whole file "to be safe", spending the tokens polyflow should save. The precise span is
available: pattern/framework nodes **already** carry `meta["end_line"]` (`matcher.go:884`); only
Go-semantic nodes (function/method/struct/interface) lack it, and no tool returns a node's exact
source by id.

**Design — two parts.**

*(a) Fill the Go-semantic `end_line` gap* (`internal/parser/go_semantic.go`). Everywhere a node's
`Line` is set from `fset.Position(x.Pos()).Line` (lines 192, 237, 822 for funcs/methods; the struct
and interface `TypeSpec` sites likewise), also set
`meta["end_line"] = fset.Position(x.End()).Line`. `ast.Node.End()` gives the closing brace/position;
this is exact and free. Do the same for the tree-sitter variable/type passes only if trivial
(`EndPoint().Row+1`); otherwise leave to the window fallback — never fabricate a span.

*(b) Span-aware snippet + `read` tool.* Add a `budget.SnippetSpan` that prefers an explicit end line
and caps the read; make the existing `snippet_lines` inlining in context/impact call it with
`meta["end_line"]` when present (so those tools also get exact spans, window otherwise). Add a
dedicated MCP `read` tool that returns a node's exact source by id.

**Pinned interface.**

```go
// internal/budget/budget.go — new sibling; existing Snippet() unchanged.
// end<=0 → fall back to a maxLines window from start (current behaviour).
// end>=start → return start..end, but never more than maxLines (cap runaway).
func SnippetSpan(root, file string, start, end, maxLines int) (src string, truncated bool)
```

```go
// internal/mcpserver/read.go
type readInput struct {
	Target    string `json:"target"`               // node id (from search/hierarchy/context) OR "file:line"
	MaxLines  int    `json:"max_lines,omitempty"`  // safety cap, default 200
}
type readOutput struct {
	ID        string `json:"id,omitempty"`
	File      string `json:"file"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`             // resolved span end (start+len-1 when unknown)
	Source    string `json:"source"`
	Truncated bool   `json:"truncated,omitempty"`  // span exceeded max_lines
	SpanKnown bool   `json:"span_known"`           // true = exact symbol span; false = window fallback
}

func (s *Server) read(ctx context.Context, req *mcp.CallToolRequest,
	in readInput) (*mcp.CallToolResult, any, error)
```

Handler: resolve `Target` to a node via `idx.Nodes[id]` (or the same
resolve path context/impact use for a partial name); read `n.Line` and
`atoi(n.Meta["end_line"])`; return `SnippetSpan(root, n.File, start, end, maxLines)`. Root is the
service path from config (same lookup the snippet callers already use).

Registration:

```go
mcp.AddTool(srv, &mcp.Tool{
	Name: "read",
	Description: "Return the EXACT source lines of a symbol (function, method, class, struct, " +
		"interface) by node id — its true span, not the whole file. Use after search/hierarchy/context " +
		"give you an id, instead of opening the file. span_known=false means the exact end was unknown " +
		"and a bounded window was returned; max_lines caps runaway spans.",
}, s.read)
```

**Worked example.** `read(target="polyflow:internal/mcpserver/mcpserver.go:function:New:98")`:

```json
{"id":"polyflow:internal/mcpserver/mcpserver.go:function:New:98",
 "file":"internal/mcpserver/mcpserver.go","start_line":98,"end_line":183,
 "span_known":true,"source":"func New(store Store, idx *graph.AdjacencyIndex, ...\n\t...\n}"}
```

(85 lines returned — the whole function, nothing else — vs. the 400-line file.)

**Tests.**
- Go fixture (the `writeIfaceModule` shape in `internal/parser/go_i1_test.go`): assert
  `NewMemStore`, `Get`, `memStore` nodes carry `meta["end_line"]` equal to their closing-brace line.
- `SnippetSpan`: end>=start returns the exact slice; end<=0 falls back to a maxLines window; span
  longer than maxLines → `truncated=true`, exactly maxLines returned.
- `read` tool: known-span node → `span_known=true` and `source` == file lines `start..end`;
  a node lacking `end_line` → `span_known=false`, bounded window; unknown id → error.
- Negative: empty/oversized end never reads past EOF (reuse the `budget_test.go` bounds cases).

**Outcome.** Agents read the exact symbol (typically 5–80 lines) instead of a whole file
(hundreds), and stop guessing window sizes → direct token *and* accuracy win, the canonical
polyflow value. Existing `snippet_lines` in context/impact upgrades from window to exact span for
free. **Schema bump 25→26** (Go-semantic nodes gain `meta["end_line"]`; forces one re-index).

---

## Sequencing & measurement

Order: **A.1 → A.3 → A.2.** A.1 unlocks the A/B harness that *proves* A.3 and A.2 pay off; A.3
before A.2 so `hierarchy`'s symbol `id`s can be demonstrated chaining into `read` end-to-end.

Measure each phase on the fleet graph (33789 nodes / 50826 edges / 1240 links, per
`polyflow status`) with the `eval/agent-bench/` harness, reporting Context Tokens / turns / recall
**with vs. without** (via A.1's toggle) for: an orientation task (A.2), a "show me this function"
task (A.3), and a caller/blast-radius task (regression guard). Record medians, not single trials.
