package graph

// NodeType classifies what kind of code entity a node represents.
type NodeType string

const (
	NodeTypeHTTPHandler  NodeType = "http_handler"
	NodeTypeHTTPClient   NodeType = "http_client"
	NodeTypeFunction     NodeType = "function"
	NodeTypeMethod       NodeType = "method"
	NodeTypeComponent    NodeType = "component"
	NodeTypeRoute        NodeType = "route"
	NodeTypeWorker       NodeType = "worker"
	NodeTypePublisher    NodeType = "publisher"
	NodeTypeSubscriber   NodeType = "subscriber"
	NodeTypeTemplElement NodeType = "templ_element"
	NodeTypeInterface    NodeType = "interface"
	NodeTypeTypeAlias    NodeType = "type_alias"
	NodeTypeDOMTarget    NodeType = "dom_target"
	NodeTypeChannel      NodeType = "channel"
)

// EdgeType classifies the relationship between two nodes.
type EdgeType string

const (
	EdgeTypeHTTPCall        EdgeType = "http_call"
	EdgeTypeCalls           EdgeType = "calls"
	EdgeTypeRenders         EdgeType = "renders"
	EdgeTypePublishes       EdgeType = "publishes"
	EdgeTypeSubscribes      EdgeType = "subscribes"
	EdgeTypeImports         EdgeType = "imports"
	EdgeTypeSpawns          EdgeType = "spawns"
	EdgeTypeSSEEndpoint     EdgeType = "sse_endpoint"
	EdgeTypeDatastarAction  EdgeType = "datastar_action"
	EdgeTypeDatastarBind    EdgeType = "datastar_bind"
	EdgeTypeSidekiqEnqueue  EdgeType = "sidekiq_enqueue"
	EdgeTypeSidekiqPerform  EdgeType = "sidekiq_perform"
	EdgeTypePusherTrigger   EdgeType = "pusher_trigger"
	EdgeTypePusherSubscribe EdgeType = "pusher_subscribe"
	EdgeTypeDOMRead         EdgeType = "dom_read"
	EdgeTypeDOMWrite        EdgeType = "dom_write"
	EdgeTypeDOMCreate       EdgeType = "dom_create"
	EdgeTypeDOMRemove       EdgeType = "dom_remove"
	EdgeTypeDOMListen       EdgeType = "dom_listen"
)

// Node represents a code entity in the graph.
type Node struct {
	ID       string            `json:"id"`
	Type     NodeType          `json:"type"`
	Label    string            `json:"label"`
	Service  string            `json:"service"`
	File     string            `json:"file"`
	Line     int               `json:"line"`
	Language string            `json:"language"`
	Meta     map[string]string `json:"meta,omitempty"`
}

// Edge represents a directed relationship between two nodes.
type Edge struct {
	ID    string            `json:"id"`
	From  string            `json:"from"`
	To    string            `json:"to"`
	Type  EdgeType          `json:"type"`
	Label string            `json:"label,omitempty"`
	Meta  map[string]string `json:"meta,omitempty"`
}

// ParseError records a file that produced errors during indexing.
// Partial extraction may still have occurred; consult the associated nodes/edges.
type ParseError struct {
	FilePath       string
	Service        string
	ErrorCount     int
	FirstErrorLine int
	IndexedAt      int64 // unix timestamp
}

// AdjacencyIndex is an in-memory representation of the graph for fast traversal.
type AdjacencyIndex struct {
	Nodes    map[string]*Node
	OutEdges map[string][]*Edge // nodeID -> outgoing edges
	InEdges  map[string][]*Edge // nodeID -> incoming edges
}

// NewAdjacencyIndex creates an empty AdjacencyIndex.
func NewAdjacencyIndex() *AdjacencyIndex {
	return &AdjacencyIndex{
		Nodes:    make(map[string]*Node),
		OutEdges: make(map[string][]*Edge),
		InEdges:  make(map[string][]*Edge),
	}
}

// AddNode inserts or replaces a node in the index.
func (idx *AdjacencyIndex) AddNode(n *Node) {
	idx.Nodes[n.ID] = n
}

// AddEdge inserts an edge into the adjacency lists.
func (idx *AdjacencyIndex) AddEdge(e *Edge) {
	idx.OutEdges[e.From] = append(idx.OutEdges[e.From], e)
	idx.InEdges[e.To] = append(idx.InEdges[e.To], e)
}
