// Package mcpserver exposes polyflow's query layer (search, context, impact,
// trace) as MCP tools over any MCP transport (the CLI serves stdio). It is a
// thin wrapper: each tool returns the same JSON contract as the CLI command
// of the same name, including the unresolved-references recall gauge, so
// agents get identical answers whichever surface they use.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	pfcontext "github.com/lordsonvimal/polyflow/internal/context"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/impact"
	"github.com/lordsonvimal/polyflow/internal/trace"
)

// Store is the subset of graph.SQLiteStore the MCP tools need.
type Store interface {
	SearchNodes(ctx context.Context, query string, limit int) ([]*graph.Node, error)
	ListUnresolvedRefs(ctx context.Context) ([]graph.UnresolvedRef, error)
}

// Server wires the query layer behind MCP tool handlers. The store and index
// are swappable so a long-lived stdio session picks up reindexes.
type Server struct {
	mu    sync.RWMutex
	store Store
	idx   *graph.AdjacencyIndex
}

// Reload swaps in a freshly built store and index (after `polyflow index`
// rewrote the graph database).
func (s *Server) Reload(store Store, idx *graph.AdjacencyIndex) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store = store
	s.idx = idx
}

// snapshot returns a consistent store+index pair for one tool call.
func (s *Server) snapshot() (Store, *graph.AdjacencyIndex) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.store, s.idx
}

// New builds an MCP server exposing the polyflow query tools. The returned
// *Server handle supports Reload; the *mcp.Server is what runs the session.
func New(store Store, idx *graph.AdjacencyIndex, version string) (*mcp.Server, *Server) {
	s := &Server{store: store, idx: idx}

	srv := mcp.NewServer(&mcp.Implementation{Name: "polyflow", Version: version}, nil)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "search",
		Description: "Search the indexed code graph for nodes (functions, methods, variables, " +
			"HTTP handlers, …) or files matching a query. Use this to find the exact node " +
			"before calling context, impact, or trace.",
	}, s.search)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "context",
		Description: "Show the call context around a node: upstream callers, downstream callees, " +
			"and cross-service edges. The unresolved section lists references in the traversed " +
			"files the indexer could not resolve — verify those manually, edges may be missing.",
	}, s.context)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "impact",
		Description: "Show the blast radius of changing a node or a file: everything that " +
			"transitively depends on it, entry points, and affected services. Directly answers " +
			"'what is impacted if I change X'. The unresolved section lists references the " +
			"indexer could not resolve — verify those manually, the blast radius may be " +
			"under-reported where they appear.",
	}, s.impact)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "trace",
		Description: "Trace multi-hop flows from a node as linear chains (A -> B -> C), " +
			"including cross-service hops. The unresolved section lists references the indexer " +
			"could not resolve — verify those manually, chains may be incomplete.",
	}, s.trace)

	return srv, s
}

// jsonResult marshals v into a text content block, the same JSON the CLI
// emits for the equivalent command.
func jsonResult(v any) (*mcp.CallToolResult, any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}, nil, nil
}

// effectiveDepth maps the MCP depth convention onto the internal one. Over
// JSON an omitted depth arrives as 0, so unlike the CLI, 0 cannot mean
// unlimited here: omitted/0 → def, -1 → unlimited (internal 0).
func effectiveDepth(depth, def int) int {
	switch {
	case depth < 0:
		return 0
	case depth == 0:
		return def
	}
	return depth
}

// resolveNode finds the best node match for a search query, mirroring the
// CLI's target resolution.
func resolveNode(ctx context.Context, store Store, query string) (*graph.Node, error) {
	nodes, err := store.SearchNodes(ctx, query, 5)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("node not found for query: %s", query)
	}
	return nodes[0], nil
}

// ─── search ──────────────────────────────────────────────────────────────────

type searchInput struct {
	Query string `json:"query" jsonschema:"search query (matches node labels and file paths)"`
	Limit int    `json:"limit,omitempty" jsonschema:"max results (default 20)"`
	Kind  string `json:"kind,omitempty" jsonschema:"restrict results: 'file' for file search, or a node type (function, method, variable, http_handler, ...)"`
}

type searchOutput struct {
	Nodes []*graph.Node       `json:"nodes,omitempty"`
	Files []graph.FileSummary `json:"files,omitempty"`
}

func (s *Server) search(ctx context.Context, req *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, any, error) {
	store, idx := s.snapshot()
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}

	if in.Kind == "file" {
		return jsonResult(searchOutput{Files: graph.ListFiles(idx, in.Query, limit)})
	}

	// Node-type kinds over-fetch then filter, so a sparse type still fills
	// the requested limit.
	fetchLimit := limit
	if in.Kind != "" {
		fetchLimit = limit * 10
	}
	nodes, err := store.SearchNodes(ctx, in.Query, fetchLimit)
	if err != nil {
		return nil, nil, err
	}
	if in.Kind != "" {
		filtered := nodes[:0]
		for _, n := range nodes {
			if string(n.Type) == in.Kind {
				filtered = append(filtered, n)
			}
		}
		nodes = filtered
		if len(nodes) > limit {
			nodes = nodes[:limit]
		}
	}
	return jsonResult(searchOutput{Nodes: nodes})
}

// ─── context ─────────────────────────────────────────────────────────────────

type contextInput struct {
	Target string `json:"target" jsonschema:"search query for the target node"`
	Task   string `json:"task,omitempty" jsonschema:"task type: impact (callers only), generate (callees only), debug or refactor (both; default debug)"`
	Depth  int    `json:"depth,omitempty" jsonschema:"max traversal depth (default 5, -1 = unlimited)"`
}

func (s *Server) context(ctx context.Context, req *mcp.CallToolRequest, in contextInput) (*mcp.CallToolResult, any, error) {
	task := in.Task
	if task == "" {
		task = "debug"
	}
	if task != "impact" && task != "generate" && task != "debug" && task != "refactor" {
		return nil, nil, fmt.Errorf("unknown task type: %s (use: impact, generate, debug, refactor)", task)
	}
	depth := effectiveDepth(in.Depth, 5)

	store, idx := s.snapshot()
	root, err := resolveNode(ctx, store, in.Target)
	if err != nil {
		return nil, nil, err
	}

	result := pfcontext.Build(idx, root.ID, task, depth)
	unresolved, err := store.ListUnresolvedRefs(ctx)
	if err != nil {
		return nil, nil, err
	}
	result.AttachUnresolved(unresolved)
	return jsonResult(result)
}

// ─── impact ──────────────────────────────────────────────────────────────────

type impactInput struct {
	Target    string `json:"target,omitempty" jsonschema:"search query for the target node (use this or file)"`
	File      string `json:"file,omitempty" jsonschema:"file path: report impact at file granularity instead of node granularity"`
	Direction string `json:"direction,omitempty" jsonschema:"with file: forward, backward, or both (default backward)"`
	Depth     int    `json:"depth,omitempty" jsonschema:"max traversal depth (default 10, -1 = unlimited)"`
	Service   string `json:"service,omitempty" jsonschema:"filter results to a specific service"`
}

// fileImpactOutput is the file-granularity impact shape, matching
// `polyflow impact --file`.
type fileImpactOutput struct {
	File      string                  `json:"file"`
	Service   string                  `json:"service"`
	Direction string                  `json:"direction"`
	Depth     int                     `json:"depth"`
	Impacted  []graph.FileImpactEntry `json:"impacted"`

	Unresolved     []graph.UnresolvedRef `json:"unresolved"`
	UnresolvedNote string                `json:"unresolved_note,omitempty"`
}

func (s *Server) impact(ctx context.Context, req *mcp.CallToolRequest, in impactInput) (*mcp.CallToolResult, any, error) {
	if (in.Target == "") == (in.File == "") {
		return nil, nil, fmt.Errorf("provide exactly one of target or file")
	}
	depth := effectiveDepth(in.Depth, 10)

	store, idx := s.snapshot()
	unresolved, err := store.ListUnresolvedRefs(ctx)
	if err != nil {
		return nil, nil, err
	}

	if in.File != "" {
		direction := in.Direction
		if direction == "" {
			direction = "backward"
		}
		seeds := graph.NodesInFile(idx, in.Service, in.File)
		if len(seeds) == 0 {
			return nil, nil, fmt.Errorf("file not found in index: %s", in.File)
		}
		out := fileImpactOutput{
			File:      seeds[0].File,
			Service:   seeds[0].Service,
			Direction: direction,
			Depth:     depth,
			Impacted:  graph.FileImpact(idx, in.Service, in.File, direction, depth),
		}
		files := map[string]bool{out.File: true}
		for _, e := range out.Impacted {
			files[e.File] = true
		}
		out.Unresolved = graph.UnresolvedInFiles(unresolved, files)
		out.UnresolvedNote = graph.UnresolvedNote(len(out.Unresolved))
		return jsonResult(out)
	}

	root, err := resolveNode(ctx, store, in.Target)
	if err != nil {
		return nil, nil, err
	}
	out := impact.Build(idx, root, depth, in.Service)
	out.AttachUnresolved(unresolved)
	return jsonResult(out)
}

// ─── trace ───────────────────────────────────────────────────────────────────

type traceInput struct {
	Root      string `json:"root" jsonschema:"search query for the root node"`
	Direction string `json:"direction,omitempty" jsonschema:"trace direction: forward, backward, or both (default forward)"`
	Depth     int    `json:"depth,omitempty" jsonschema:"max traversal depth (default 10, -1 = unlimited)"`
}

func (s *Server) trace(ctx context.Context, req *mcp.CallToolRequest, in traceInput) (*mcp.CallToolResult, any, error) {
	direction := in.Direction
	if direction == "" {
		direction = "forward"
	}
	if direction != "forward" && direction != "backward" && direction != "both" {
		return nil, nil, fmt.Errorf("unknown direction: %s (use: forward, backward, both)", direction)
	}
	depth := effectiveDepth(in.Depth, 10)

	store, idx := s.snapshot()
	root, err := resolveNode(ctx, store, in.Root)
	if err != nil {
		return nil, nil, err
	}

	result := trace.Run(idx, root.ID, direction, depth)
	if result == nil {
		return nil, nil, fmt.Errorf("root node %s not in graph", root.ID)
	}
	unresolved, err := store.ListUnresolvedRefs(ctx)
	if err != nil {
		return nil, nil, err
	}
	result.AttachUnresolved(unresolved)
	return jsonResult(result)
}
