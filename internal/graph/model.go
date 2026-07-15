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
	// NodeTypeVariable is a tracked variable: package/module-level vars and
	// consts, closure-captured locals — variables whose mutation has impact
	// beyond one function. Purely-local variables are NOT nodes (they would
	// explode the graph); they surface as counts in function-node meta.
	// Meta: data_type, kind (var|const), scope (package|captured), mutable.
	NodeTypeVariable NodeType = "variable"
	// NodeTypeStruct is a Go struct type; fields live in meta ("fields" JSON:
	// [{name, type, tag}]), not as separate nodes.
	NodeTypeStruct NodeType = "struct"
	// NodeTypeClass is a JS/TS/Ruby class; properties/methods in meta.
	NodeTypeClass NodeType = "class"
	// NodeTypeSignal is a datastar reactive signal binding (data-bind,
	// data-signals, data-model, data-text/$signal). Meta: signal (the bare
	// signal name). Kept distinct from component so signal-expression values
	// like "$idx + 1" don't pollute the component node set.
	NodeTypeSignal NodeType = "signal"
	// NodeTypeService is a synthetic containment root: one per indexed service,
	// the top of the service→file→declaration `contains` backbone. Carries no
	// file/line (it represents the whole service boundary).
	NodeTypeService NodeType = "service"
	// NodeTypeFile is a synthetic per-file containment node: the middle of the
	// service→file→declaration `contains` backbone, so an agent can ask "what's
	// in this file". Synthesized during linking from existing node file metadata.
	NodeTypeFile NodeType = "file"
)

// EdgeType classifies the relationship between two nodes.
type EdgeType string

const (
	EdgeTypeHTTPCall        EdgeType = "http_call"
	EdgeTypeCalls           EdgeType = "calls"
	EdgeTypeRenders         EdgeType = "renders"
	// EdgeTypeComponentImpl bridges a templ component to its generated Go twin
	// (`x.templ` component ↔ `x_templ.go` function). The generated function is
	// what the go/packages call graph reaches, so the edge runs from that
	// function to the templ component — carrying route→handler reachability
	// across the Go↔templ seam into the component where datastar/DOM edges hang.
	EdgeTypeComponentImpl EdgeType = "component_impl"
	// Page navigation (href/action attributes) — user-driven, not an API call.
	EdgeTypeNavigatesTo EdgeType = "navigates_to"
	EdgeTypePublishes       EdgeType = "publishes"
	EdgeTypeSubscribes      EdgeType = "subscribes"
	EdgeTypeImports         EdgeType = "imports"
	// EdgeTypeDefinedIn links a JS DOM-target (querySelector/getElementById) to
	// the templ element that declares the matching id=/class= — the JS↔templ DOM
	// seam. Runs from the JS target to the templ_element definition node.
	EdgeTypeDefinedIn       EdgeType = "defined_in"
	EdgeTypeSpawns          EdgeType = "spawns"
	EdgeTypeSSEEndpoint     EdgeType = "sse_endpoint"
	EdgeTypeDatastarAction  EdgeType = "datastar_action"
	EdgeTypeDatastarBind    EdgeType = "datastar_bind"
	// Generic background-job edges: delayed_job, solid_queue, ActiveJob,
	// Sidekiq all map onto these; the meta records which queue system.
	EdgeTypeJobEnqueue EdgeType = "job_enqueue"
	EdgeTypeJobPerform EdgeType = "job_perform"
	// Deprecated: kept as aliases for stored graphs; new code emits the
	// generic job_enqueue/job_perform types.
	EdgeTypeSidekiqEnqueue EdgeType = "sidekiq_enqueue"
	EdgeTypeSidekiqPerform EdgeType = "sidekiq_perform"
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
	// WebSocket edges
	EdgeTypeWSUpgrade EdgeType = "ws_upgrade" // HTTP handler upgrades to a WebSocket
	EdgeTypeWSConnect EdgeType = "ws_connect" // client opens a WebSocket
	EdgeTypeWSRead    EdgeType = "ws_read"    // reads/dispatches inbound messages
	EdgeTypeWSWrite   EdgeType = "ws_write"   // writes outbound messages
	EdgeTypeWSSend    EdgeType = "ws_send"    // sends a typed message ({type: "…"})
	// SSE broadcast-hub edges (Subscribe/Unsubscribe/Broadcast channel fan-out)
	EdgeTypeHubSubscribe EdgeType = "hub_subscribe"
	EdgeTypeHubBroadcast EdgeType = "hub_broadcast"
	// Variable-tracking edges. declares: enclosing scope → variable;
	// reads/writes: function → variable (writes meta: op); captures:
	// closure → outer variable (meta: by=ref|value); flows_to: variable →
	// parameter/variable at a call site (meta: mode=ref|value, data_type);
	// uses_type: function/variable → struct/class/interface it references.
	EdgeTypeDeclares EdgeType = "declares"
	EdgeTypeReads    EdgeType = "reads"
	EdgeTypeWrites   EdgeType = "writes"
	EdgeTypeCaptures EdgeType = "captures"
	EdgeTypeFlowsTo  EdgeType = "flows_to"
	EdgeTypeUsesType EdgeType = "uses_type"
	// EdgeTypeContains is the structural backbone: service→file→declaration
	// (function/method/struct/component) and struct→method. Synthesized during
	// linking from existing node file/receiver metadata; answers "what's in this
	// file" / "what hangs off this struct" for agent-context recall.
	EdgeTypeContains EdgeType = "contains"
	// Type-relationship edges (Tier I). Direction: dependent → definition.
	// Impact traversal is bidirectional, so "impact of Base" follows incoming
	// inherits edges to every subclass.
	//
	// inherits: subclass→superclass, embedding struct→embedded type.
	// meta: via=extends|superclass|embedding|mixin; mixin=include|extend|prepend.
	EdgeTypeInherits EdgeType = "inherits"
	// implements: struct/class→interface it satisfies.
	// meta: nominal=true for declared `implements` clauses, false for Go structural.
	EdgeTypeImplements EdgeType = "implements"
	// instantiates: function/method→struct/class it constructs.
	// Deduped per (function, type) pair; meta: count=<n>.
	EdgeTypeInstantiates EdgeType = "instantiates"
)

// SchemaVersion identifies the graph data-model generation. Bumped when node
// or edge semantics change in a way that invalidates cached parse results;
// the indexer forces a full re-index when the stored version differs.
const SchemaVersion = "9"

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

	// Snippet is inlined source (query output only, never persisted): set on
	// copies of index nodes when a query asks for snippet inlining.
	Snippet string `json:"snippet,omitempty"`
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

// FileHash records a file's content hash and cached parse results for
// incremental re-indexing: when the hash is unchanged, the cached
// nodes/edges are reused and tree-sitter parsing is skipped.
type FileHash struct {
	FilePath       string
	Service        string
	ContentHash    string
	IndexedAt      int64
	NodesJSON      string
	EdgesJSON      string
	UnresolvedJSON string // cached UnresolvedRefs for the file ('[]' when none)
	Errored        bool
}

// UnresolvedRef records a reference the indexer saw but could not resolve to
// a node — the graph's blind-spot ledger. A silently missing edge is the
// worst failure mode for impact queries, so every drop is kept visible here
// instead. Kinds: "call_ref" (in-file call reference with no target),
// "import_ref" (imported name with no node in the service).
type UnresolvedRef struct {
	Service string `json:"service"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
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
