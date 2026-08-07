package linker

import (
	"fmt"
	"path"

	"github.com/lordsonvimal/polyflow/internal/graph"
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
