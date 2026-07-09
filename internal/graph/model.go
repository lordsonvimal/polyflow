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
	// NodeTypeDatastore covers both service-level datastore nodes (derived
	// from resolved dependencies; meta kind=store, engine, driver) and DB
	// call sites (GORM chains, database/sql queries; meta kind=call, op).
	NodeTypeDatastore NodeType = "datastore"
	// NodeTypeExternalService is a third-party service boundary (cloud SDKs:
	// S3, Bedrock, Pusher-as-a-service, …).
	NodeTypeExternalService NodeType = "external_service"
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
	EdgeTypeQueries         EdgeType = "queries"  // reads from a datastore
	EdgeTypePersists        EdgeType = "persists" // writes to a datastore
	EdgeTypeCloudCall       EdgeType = "cloud_call"
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

// Confidence levels for edges — how certain the linker is about a match.
const (
	ConfidenceStatic   = "static"   // literal string match
	ConfidenceInferred = "inferred" // wildcard/normalized match
	ConfidencePartial  = "partial"  // partially resolved
	ConfidenceUnknown  = "unknown"  // dynamic, unresolvable
)

// Edge represents a directed relationship between two nodes.
type Edge struct {
	ID         string            `json:"id"`
	From       string            `json:"from"`
	To         string            `json:"to"`
	Type       EdgeType          `json:"type"`
	Label      string            `json:"label,omitempty"`
	Confidence string            `json:"confidence,omitempty"` // static | inferred | partial | unknown
	Method     string            `json:"method,omitempty"`     // HTTP method (GET, POST, …)
	Path       string            `json:"path,omitempty"`       // HTTP route path
	Meta       map[string]string `json:"meta,omitempty"`
}

// Dependency is one resolved package version for a service, recorded at
// index time so users and agents can query "what version of X does Y use".
type Dependency struct {
	Service   string `json:"service"`
	Ecosystem string `json:"ecosystem"` // go | npm | rubygems
	Name      string `json:"name"`
	Version   string `json:"version"` // exact resolved version
	Kind      string `json:"kind"`    // prod | dev
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
