package graph

import (
	"fmt"
	"sort"
	"strings"
)

// TreeNode is one entry in the Folder -> File -> Class/Struct -> Function/
// Method outline. Folders carry no NodeID (they are derived from file paths
// at query time, never stored). Children is never nil so it serializes as
// `[]`, not `null`, for leaf symbols.
type TreeNode struct {
	Kind     string      `json:"kind"`
	Name     string      `json:"name"`
	Path     string      `json:"path,omitempty"`
	NodeID   string      `json:"node_id,omitempty"`
	Line     int         `json:"line,omitempty"`
	EndLine  int         `json:"end_line,omitempty"`
	Children []*TreeNode `json:"children"`
}

// TreeCounts totals the entries in a Tree response.
type TreeCounts struct {
	Folders int `json:"folders"`
	Files   int `json:"files"`
	Symbols int `json:"symbols"`
}

// TreeResult is the response body for GET /api/tree.
type TreeResult struct {
	Service string      `json:"service"`
	Tree    []*TreeNode `json:"tree"`
	Counts  TreeCounts  `json:"counts"`
}

// treeKindByType maps declaration node types onto the tree's kind vocabulary.
// Every other NodeType reached as a `contains` child (e.g. stylesheet
// selectors, ERB markup elements) surfaces with its raw NodeType string
// instead of being dropped (bug-class rule 12: intake is exhaustively
// accounted).
var treeKindByType = map[NodeType]string{
	NodeTypeClass:     "class",
	NodeTypeStruct:    "struct",
	NodeTypeFunction:  "function",
	NodeTypeMethod:    "method",
	NodeTypeComponent: "component",
	NodeTypeVariable:  "variable",
}

func treeKind(t NodeType) string {
	if k, ok := treeKindByType[t]; ok {
		return k
	}
	return string(t)
}

// BuildTree derives the Folder -> File -> Class/Struct -> Function/Method
// outline for one service. Folders are computed at query time by splitting
// file paths — they carry no code semantics, so storing them would only
// bump the schema and inflate the node count for no benefit.
//
// Symbols reach the tree two ways: (1) the existing `contains` backbone
// (service->file->declaration, struct->method), walked from each file node;
// (2) a file-path fallback for declaration types containment never wires
// (class, variable — see internal/linker/containment.go's containedTypes),
// so those are surfaced under their file rather than silently dropped.
func BuildTree(idx *AdjacencyIndex, service string) (*TreeResult, error) {
	known := false
	for _, n := range idx.Nodes {
		if n.Service == service {
			known = true
			break
		}
	}
	if !known {
		return nil, fmt.Errorf("service not found: %s", service)
	}

	// File nodes already wired into the service's `contains` backbone.
	fileNodeIDs := make(map[string]string) // file path -> file node ID
	var fileNodes []*Node
	for _, n := range idx.Nodes {
		if n.Type == NodeTypeFile && n.Service == service {
			fileNodes = append(fileNodes, n)
			fileNodeIDs[n.File] = n.ID
		}
	}
	sort.Slice(fileNodes, func(i, j int) bool { return fileNodes[i].File < fileNodes[j].File })

	// A node is a `contains` root candidate only if nothing at all points to
	// it — checked against every contains edge up front, not just the ones
	// reached from a file node. A Ruby class has no incoming file->class
	// edge (containment.go never wires one) but does have outgoing
	// class->method edges (internal/parser/ruby.go's linkRubyClassMembers),
	// so its methods must not also be treated as root orphans.
	hasIncomingContains := make(map[string]bool)
	for _, edges := range idx.OutEdges {
		for _, e := range edges {
			if e.Type == EdgeTypeContains {
				hasIncomingContains[e.To] = true
			}
		}
	}

	var walk func(parentID string) []*TreeNode
	walk = func(parentID string) []*TreeNode {
		out := make([]*TreeNode, 0)
		for _, e := range idx.OutEdges[parentID] {
			if e.Type != EdgeTypeContains {
				continue
			}
			n, ok := idx.Nodes[e.To]
			if !ok {
				continue
			}
			out = append(out, &TreeNode{
				Kind:     treeKind(n.Type),
				Name:     n.Label,
				NodeID:   n.ID,
				Line:     n.Line,
				EndLine:  n.EndLine,
				Children: walk(n.ID),
			})
		}
		sortSymbols(out)
		return out
	}

	fileChildren := make(map[string][]*TreeNode) // file path -> children
	for _, fn := range fileNodes {
		fileChildren[fn.File] = walk(fn.ID)
	}

	// Fallback: declaration types containment never wires (class, variable)
	// attach directly under their file path, surfaced rather than dropped.
	var orphanNodes []*Node
	for _, n := range idx.Nodes {
		if n.Service != service || n.File == "" || hasIncomingContains[n.ID] {
			continue
		}
		if _, ok := treeKindByType[n.Type]; !ok {
			continue
		}
		orphanNodes = append(orphanNodes, n)
	}
	sort.Slice(orphanNodes, func(i, j int) bool { return orphanNodes[i].ID < orphanNodes[j].ID })

	filePaths := make(map[string]bool, len(fileChildren)+len(orphanNodes))
	for p := range fileChildren {
		filePaths[p] = true
	}
	for _, n := range orphanNodes {
		filePaths[n.File] = true
		// An orphan (no `contains` parent) may still have its own `contains`
		// children — a Ruby class has no incoming file->class edge but does
		// have outgoing class->method edges (internal/parser/ruby.go's
		// linkRubyClassMembers) — so walk it the same as any primary node.
		fileChildren[n.File] = append(fileChildren[n.File], &TreeNode{
			Kind:     treeKind(n.Type),
			Name:     n.Label,
			NodeID:   n.ID,
			Line:     n.Line,
			EndLine:  n.EndLine,
			Children: walk(n.ID),
		})
	}
	for p, kids := range fileChildren {
		sortSymbols(kids)
		fileChildren[p] = kids
	}

	tree, folderCount, fileCount := assembleTree(filePaths, fileNodeIDs, fileChildren)

	return &TreeResult{
		Service: service,
		Tree:    tree,
		Counts: TreeCounts{
			Folders: folderCount,
			Files:   fileCount,
			Symbols: countSymbols(tree),
		},
	}, nil
}

// sortSymbols orders symbol children by line, then node ID as a stable
// tie-break (bug-class rule 2: deterministic output, always).
func sortSymbols(nodes []*TreeNode) {
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Line != nodes[j].Line {
			return nodes[i].Line < nodes[j].Line
		}
		return nodes[i].NodeID < nodes[j].NodeID
	})
}

// assembleTree turns a flat set of file paths into a nested folder/file
// tree, attaching each file's already-sorted symbol children. Returns the
// root-level nodes plus folder/file counts.
func assembleTree(filePaths map[string]bool, fileNodeIDs map[string]string, fileChildren map[string][]*TreeNode) ([]*TreeNode, int, int) {
	parentOf := func(p string) string {
		i := strings.LastIndex(p, "/")
		if i < 0 {
			return ""
		}
		return p[:i]
	}
	baseName := func(p string) string {
		i := strings.LastIndex(p, "/")
		if i < 0 {
			return p
		}
		return p[i+1:]
	}

	folders := make(map[string]*TreeNode)
	childSet := make(map[string]map[string]bool) // parent path -> set of immediate child paths

	addChild := func(parent, child string) {
		if childSet[parent] == nil {
			childSet[parent] = make(map[string]bool)
		}
		childSet[parent][child] = true
	}

	var ensureFolderChain func(dir string)
	ensureFolderChain = func(dir string) {
		if dir == "" || folders[dir] != nil {
			return
		}
		parent := parentOf(dir)
		ensureFolderChain(parent)
		folders[dir] = &TreeNode{Kind: "folder", Name: baseName(dir), Path: dir, Children: []*TreeNode{}}
		addChild(parent, dir)
	}

	files := make(map[string]*TreeNode)
	var paths []string
	for p := range filePaths {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, p := range paths {
		dir := parentOf(p)
		ensureFolderChain(dir)
		addChild(dir, p)
		kids := fileChildren[p]
		if kids == nil {
			kids = []*TreeNode{}
		}
		files[p] = &TreeNode{Kind: "file", Name: baseName(p), Path: p, NodeID: fileNodeIDs[p], Children: kids}
	}

	var assemble func(path string) []*TreeNode
	assemble = func(path string) []*TreeNode {
		names := make([]string, 0, len(childSet[path]))
		for c := range childSet[path] {
			names = append(names, c)
		}
		sort.Strings(names)
		var folderList, fileList []*TreeNode
		for _, c := range names {
			if fn, ok := folders[c]; ok {
				fn.Children = assemble(c)
				folderList = append(folderList, fn)
			} else if fn, ok := files[c]; ok {
				fileList = append(fileList, fn)
			}
		}
		return append(folderList, fileList...)
	}

	root := assemble("")
	if root == nil {
		root = []*TreeNode{}
	}
	return root, len(folders), len(files)
}

// countSymbols recursively counts every non-folder, non-file node in the tree.
func countSymbols(nodes []*TreeNode) int {
	n := 0
	for _, t := range nodes {
		if t.Kind != "folder" && t.Kind != "file" {
			n++
		}
		n += countSymbols(t.Children)
	}
	return n
}
