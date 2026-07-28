package mcpserver

import (
	"context"
	"path"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lordsonvimal/polyflow/internal/budget"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

type hierarchyInput struct {
	Service   string `json:"service,omitempty" jsonschema:"restrict to one service; empty = all services"`
	Path      string `json:"path,omitempty" jsonschema:"file or directory prefix to expand under (e.g. internal/parser)"`
	Depth     int    `json:"depth,omitempty" jsonschema:"traversal depth: 1 services, 2 dirs and files (default), 3 top-level symbols"`
	MaxTokens int    `json:"max_tokens,omitempty" jsonschema:"token budget; over budget the deepest level collapses to counts"`
}

type hierNode struct {
	Name     string      `json:"name"`               // service / dir / file / symbol label
	Kind     string      `json:"kind"`               // "service"|"dir"|"file"|<node type>
	File     string      `json:"file,omitempty"`
	Line     int         `json:"line,omitempty"`
	ID       string      `json:"id,omitempty"`       // node id at symbol level → feed to read/context
	Count    int         `json:"count,omitempty"`    // direct children rolled up (when collapsed)
	Children []*hierNode `json:"children,omitempty"`
}

type hierarchyOutput struct {
	Workspace string      `json:"workspace"`
	Roots     []*hierNode `json:"roots"`
	Truncated bool        `json:"truncated,omitempty"` // true when max_tokens forced roll-up
}

// hierarchySymbolTypes are the top-level declared node types surfaced at depth
// 3. Variables, params, elements and other fine-grained nodes are excluded so
// the tree reads like a symbol outline, not a token dump.
var hierarchySymbolTypes = map[graph.NodeType]bool{
	graph.NodeTypeFunction:        true,
	graph.NodeTypeMethod:          true,
	graph.NodeTypeClass:           true,
	graph.NodeTypeStruct:          true,
	graph.NodeTypeInterface:       true,
	graph.NodeTypeComponent:       true,
	graph.NodeTypeHTTPHandler:     true,
	graph.NodeTypeSubscriber:      true,
	graph.NodeTypeWorker:          true,
	graph.NodeTypeGRPCHandler:     true,
	graph.NodeTypeGraphQLResolver: true,
}

// fileBucket collects the top-level symbols declared in one file, plus the
// file's own path (files enter the tree even when they hold no listed symbol,
// so the layout is complete).
type fileBucket struct {
	file    string
	symbols []*graph.Node
}

// hierarchy returns the structural shape of the workspace as a budgeted tree:
// service → directory → file → top-level symbols, with roll-up counts. It walks
// idx.Nodes once (same enumeration as entrypoints) and is depth- and
// token-capped so a large graph never dumps everything.
func (s *Server) hierarchy(ctx context.Context, req *mcp.CallToolRequest, in hierarchyInput) (*mcp.CallToolResult, any, error) {
	_, idx, _ := s.snapshot()

	prefix := strings.TrimSuffix(in.Path, "/")

	// service → dir → file → bucket
	tree := map[string]map[string]map[string]*fileBucket{}
	workspace := ""
	for _, n := range idx.Nodes {
		if n.File == "" {
			continue
		}
		if in.Service != "" && n.Service != in.Service {
			continue
		}
		if prefix != "" && !underPath(n.File, prefix) {
			continue
		}
		if workspace == "" && in.Service == "" {
			workspace = n.Service
		}
		dir := path.Dir(n.File)
		dirs := tree[n.Service]
		if dirs == nil {
			dirs = map[string]map[string]*fileBucket{}
			tree[n.Service] = dirs
		}
		files := dirs[dir]
		if files == nil {
			files = map[string]*fileBucket{}
			dirs[dir] = files
		}
		b := files[n.File]
		if b == nil {
			b = &fileBucket{file: n.File}
			files[n.File] = b
		}
		if hierarchySymbolTypes[n.Type] {
			b.symbols = append(b.symbols, n)
		}
	}
	if in.Service != "" {
		workspace = in.Service
	}

	// Build at the requested depth, then collapse the deepest level while the
	// output exceeds max_tokens (deterministic, coarse roll-up like impact/flows).
	depth := in.Depth
	switch {
	case depth <= 0:
		depth = 2
	case depth > 3:
		depth = 3
	}

	out := hierarchyOutput{Workspace: workspace}
	for {
		out.Roots = buildHierRoots(tree, depth)
		if in.MaxTokens <= 0 || depth <= 1 || budget.Estimate(out) <= in.MaxTokens {
			break
		}
		depth--
		out.Truncated = true
	}
	if out.Roots == nil {
		out.Roots = []*hierNode{}
	}
	return jsonResult(out)
}

// buildHierRoots renders the collected tree to the given depth (1=services,
// 2=+dirs/files, 3=+symbols). Collapsed levels carry Count = direct-child count.
func buildHierRoots(tree map[string]map[string]map[string]*fileBucket, depth int) []*hierNode {
	roots := make([]*hierNode, 0, len(tree))
	for _, svc := range sortedKeys(tree) {
		dirs := tree[svc]
		svcNode := &hierNode{Name: svc, Kind: "service"}
		if depth < 2 {
			svcNode.Count = len(dirs)
			roots = append(roots, svcNode)
			continue
		}
		for _, dir := range sortedKeys(dirs) {
			files := dirs[dir]
			dirNode := &hierNode{Name: dir, Kind: "dir"}
			for _, file := range sortedKeys(files) {
				b := files[file]
				fileNode := &hierNode{Name: path.Base(file), Kind: "file", File: file}
				if depth < 3 {
					fileNode.Count = len(b.symbols)
				} else {
					syms := append([]*graph.Node(nil), b.symbols...)
					sort.Slice(syms, func(i, j int) bool {
						if syms[i].Line != syms[j].Line {
							return syms[i].Line < syms[j].Line
						}
						return syms[i].ID < syms[j].ID
					})
					for _, sym := range syms {
						fileNode.Children = append(fileNode.Children, &hierNode{
							Name: sym.Label,
							Kind: string(sym.Type),
							File: sym.File,
							Line: sym.Line,
							ID:   sym.ID,
						})
					}
				}
				dirNode.Children = append(dirNode.Children, fileNode)
			}
			svcNode.Children = append(svcNode.Children, dirNode)
		}
		roots = append(roots, svcNode)
	}
	return roots
}

// underPath reports whether file sits at or under the directory/file prefix p.
func underPath(file, p string) bool {
	return file == p || strings.HasPrefix(file, p+"/")
}

// sortedKeys returns the keys of m in ascending order (deterministic output;
// bug-class rule 2 — never iterate a map into output).
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
