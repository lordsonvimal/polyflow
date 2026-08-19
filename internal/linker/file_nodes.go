package linker

import (
	"fmt"
	"path"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// fileNodeIndex looks up the NodeTypeFile backbone LinkContainment built, and
// mints the entries it is missing.
//
// Containment only reaches a file that *declares* something. A Sass partial of
// nothing but `$variables`, an asset manifest of nothing but `//= require`
// lines, and an ERB layout all declare nothing — yet those are exactly the
// files an import graph points at. Every pass that wires file→file edges has to
// mint its own endpoints or it silently emits a fraction of its edges (Tier K.5
// lost 33 of 40 that way, Tier K.1 the same class before it).
type fileNodeIndex struct {
	id          map[string]string // "svc\x00file" → node ID
	haveService map[string]bool
	minted      []graph.Node
	mintedEdges []graph.Edge
}

func newFileNodeIndex(nodes []graph.Node) *fileNodeIndex {
	x := &fileNodeIndex{id: map[string]string{}, haveService: map[string]bool{}}
	for i := range nodes {
		switch nodes[i].Type {
		case graph.NodeTypeFile:
			x.id[nodes[i].Service+"\x00"+nodes[i].File] = nodes[i].ID
		case graph.NodeTypeService:
			x.haveService[nodes[i].Label] = true
		}
	}
	return x
}

// ensure returns the file node ID for service/file, minting the node and its
// service→file contains edge when containment did not.
func (x *fileNodeIndex) ensure(service, file string) string {
	// Callers here (rails_views.go, sprockets_assets.go, stylesheet_imports.go)
	// source `file` from the indexer's raw absolute file-walk list (needed for
	// their own os.ReadFile calls), unlike the parser-minted nodes already in
	// `nodes` — those carry the cwd-relative convention (see
	// patterns.RelativizeToCwd). Relativize here too so a minted node's ID/File
	// matches the existing convention instead of forking a duplicate absolute
	// entry (and, via /api/tree's naive File-splitting, a phantom root folder).
	file = patterns.RelativizeToCwd(file)
	key := service + "\x00" + file
	if id, ok := x.id[key]; ok {
		return id
	}
	id := fmt.Sprintf("%s:%s:%s", service, file, graph.NodeTypeFile)
	x.id[key] = id
	x.minted = append(x.minted, graph.Node{
		ID:       id,
		Type:     graph.NodeTypeFile,
		Label:    file,
		Service:  service,
		File:     file,
		Language: languageForFile(file),
		Meta:     map[string]string{"basename": path.Base(file)},
	})
	if x.haveService[service] {
		x.mintedEdges = append(x.mintedEdges, containsEdge("service:"+service, id))
	}
	return id
}

// EnsureAllScannedFiles mints a bare NodeTypeFile node (plus its service→file
// contains edge) for every file the workspace walk scanned, not just the ones
// LinkContainment reached by way of a function/method/struct/component
// declaration. A pure re-export barrel (`export * from './x'`) or an
// enum-only file declares nothing containment-shaped, so without this pass it
// produced zero graph output at all — not even a marker that polyflow saw the
// file — while an import elsewhere in the same service could still point at
// it. svcFiles is service name → every file that service's language walk
// recognised (the same list the JS/TS import-edge and hashing passes use), so
// this only covers real source files, not skipped/unparsed extensions.
func EnsureAllScannedFiles(nodes []graph.Node, svcFiles map[string][]string) ([]graph.Node, []graph.Edge) {
	x := newFileNodeIndex(nodes)
	for service, files := range svcFiles {
		for _, f := range files {
			x.ensure(service, f)
		}
	}
	return x.minted, x.mintedEdges
}
