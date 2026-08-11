package graph

import "sort"

// NodeType classifies what kind of code entity a node represents.
type NodeType string

const (
	NodeTypeHTTPHandler NodeType = "http_handler"
	NodeTypeHTTPClient  NodeType = "http_client"
	NodeTypeFunction    NodeType = "function"
	// NodeTypeMethod is a concrete method (struct/class receiver) or, when
	// meta.kind="interface_method" (Tier Y.5b), an abstract interface method —
	// the dispatch target of an SSA invoke call. Interface-method nodes carry
	// meta.interface=<interface node ID> and are minted only when a `calls`
	// edge dispatches through them.
	NodeTypeMethod     NodeType = "method"
	NodeTypeComponent  NodeType = "component"
	NodeTypeRoute      NodeType = "route"
	NodeTypeWorker     NodeType = "worker"
	NodeTypePublisher  NodeType = "publisher"
	NodeTypeSubscriber NodeType = "subscriber"
	// NodeTypeElement is a generic DOM element node (successor to NodeTypeTemplElement).
	// The Language field distinguishes the source: templ, html, jsx, erb.
	NodeTypeElement NodeType = "element"
	// NodeTypeTemplElement is kept as a deprecated alias for stored graphs; new
	// minting uses NodeTypeElement. (job_enqueue/sidekiq_enqueue precedent.)
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
	// NodeTypeTable is a database table referenced by a SQL statement, parsed
	// from a datastore call node's meta.sql (the FROM/INTO/UPDATE target). It
	// turns the opaque store endpoint into a real entity so a query terminates
	// at the table it touches. Meta: name.
	NodeTypeTable NodeType = "table"
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
	// NodeTypeGRPCClient is a gRPC call site (stub.Method or cc.Invoke).
	// Meta: service_method (e.g. "/UserService/GetUser").
	NodeTypeGRPCClient NodeType = "grpc_client"
	// NodeTypeGRPCHandler is a gRPC server handler registration or method impl.
	// Meta: service_method (e.g. "/UserService/GetUser").
	NodeTypeGRPCHandler NodeType = "grpc_handler"
	// NodeTypeGraphQLClient is a GraphQL client operation call site
	// (useQuery / useMutation / useSubscription). Meta: operation (name).
	NodeTypeGraphQLClient NodeType = "graphql_client"
	// NodeTypeGraphQLResolver is a server-side GraphQL resolver registration.
	// Meta: operation (name matching the client's query/mutation field).
	NodeTypeGraphQLResolver NodeType = "graphql_resolver"
	// NodeTypeRouteGroup is route *scaffolding*: a group declaration that
	// contributes a path prefix to the routes nested inside it, but is not
	// itself an endpoint anyone can call — `resources :users do`,
	// `namespace :api do`, `api := r.Group("/v1")`. Kept as a node because
	// path composition reads it (composeRailsRoutePaths, EnrichRouteGroups),
	// but typed apart from http_handler so that type keeps meaning "an
	// endpoint you can call": it is what the contract engine matches clients
	// against, what `entrypoints`/`flows` treat as a flow root, and what the
	// file hierarchy lists as a symbol. A group carries no method and no
	// composed path, so it could only ever have been noise in those three
	// places. Meta: pattern, plus the group's own capture (resource, ns,
	// prefix/var_name/receiver). Tier HH.3.
	NodeTypeRouteGroup NodeType = "route_group"
)

// MetaIsTest marks a node whose call site sits inside a test-DSL harness
// (Jest/Mocha/Playwright, RSpec, Go testing). X.0: comm-classified sites
// (http_client/publisher/subscriber) in test-DSL scope are demoted to a
// plain NodeTypeFunction and stamped with this meta key instead of minting
// a bogus communication node — the call/blast-radius edge is kept, the
// contract engine and coverage denominators are not.
const MetaIsTest = "is_test"

// MetaChannelRole records which side(s) of a broker channel a service was
// observed on: ChannelRoleProducer if it publishes to the exchange,
// ChannelRoleConsumer if it only binds a queue to it, or ChannelRoleBoth.
//
// A channel node is otherwise role-blind — `exchange` and `routing_key` read
// identically whether the site was a `Publish` or a `QueueBind` — and the
// cross-service amqp contract joins channel to channel, so without this it
// emits an edge in BOTH directions for every shared exchange. Measured on the
// datascience fleet 2026-08-09: 8 of 30 cross-service `publishes` edges ran
// backwards along the message flow, claiming dsw-manager published the runner
// heartbeats it consumes and dsw-agent published the build jobs it binds.
//
// Absent means unknown, not "producer": only a site we positively identified as
// bind-only is marked ChannelRoleConsumer, so a channel whose role we cannot
// determine keeps its current behaviour rather than losing its edges.
const MetaChannelRole = "channel_role"

// Channel role values for MetaChannelRole. ChannelRoleBoth is a real state, not
// a fallback: a service that publishes to an exchange and also binds a queue to
// it legitimately sits on both sides, and must stay eligible as a producer.
const (
	ChannelRoleProducer = "producer"
	ChannelRoleConsumer = "consumer"
	ChannelRoleBoth     = "producer,consumer"
)

// MergeChannelRole combines an existing MetaChannelRole value with a newly
// observed one. Roles accumulate: once a service is seen publishing to an
// exchange, a later bind site cannot demote it to consumer-only.
func MergeChannelRole(existing, observed string) string {
	// An unobserved role is not evidence of anything. Merging "" in must leave
	// the value alone, or a site we could not classify would silently promote a
	// consumer-only channel to "both" and undo the suppression.
	if observed == "" {
		return existing
	}
	if existing == "" || existing == observed {
		return observed
	}
	if existing == ChannelRoleBoth {
		return existing
	}
	return ChannelRoleBoth
}

// EdgeType classifies the relationship between two nodes.
type EdgeType string

const (
	EdgeTypeHTTPCall EdgeType = "http_call"
	EdgeTypeCalls    EdgeType = "calls"
	EdgeTypeRenders  EdgeType = "renders"
	// EdgeTypeComponentImpl bridges a templ component to its generated Go twin
	// (`x.templ` component ↔ `x_templ.go` function). The generated function is
	// what the go/packages call graph reaches, so the edge runs from that
	// function to the templ component — carrying route→handler reachability
	// across the Go↔templ seam into the component where datastar/DOM edges hang.
	EdgeTypeComponentImpl EdgeType = "component_impl"
	// Page navigation (href/action attributes) — user-driven, not an API call.
	EdgeTypeNavigatesTo EdgeType = "navigates_to"
	EdgeTypePublishes   EdgeType = "publishes"
	EdgeTypeSubscribes  EdgeType = "subscribes"
	EdgeTypeImports     EdgeType = "imports"
	// EdgeTypeDefinedIn links a JS DOM-target (querySelector/getElementById) to
	// the templ element that declares the matching id=/class= — the JS↔templ DOM
	// seam. Runs from the JS target to the templ_element definition node.
	EdgeTypeDefinedIn      EdgeType = "defined_in"
	EdgeTypeSpawns         EdgeType = "spawns"
	EdgeTypeSSEEndpoint    EdgeType = "sse_endpoint"
	EdgeTypeDatastarAction EdgeType = "datastar_action"
	EdgeTypeDatastarBind   EdgeType = "datastar_bind"
	// Generic background-job edges: delayed_job, solid_queue, ActiveJob,
	// Sidekiq all map onto these; the meta records which queue system.
	EdgeTypeJobEnqueue EdgeType = "job_enqueue"
	EdgeTypeJobPerform EdgeType = "job_perform"
	// Deprecated: kept as aliases for stored graphs; new code emits the
	// generic job_enqueue/job_perform types.
	EdgeTypeSidekiqEnqueue  EdgeType = "sidekiq_enqueue"
	EdgeTypeSidekiqPerform  EdgeType = "sidekiq_perform"
	EdgeTypePusherTrigger   EdgeType = "pusher_trigger"
	EdgeTypePusherSubscribe EdgeType = "pusher_subscribe"
	EdgeTypeDOMRead         EdgeType = "dom_read"
	EdgeTypeDOMWrite        EdgeType = "dom_write"
	EdgeTypeDOMCreate       EdgeType = "dom_create"
	EdgeTypeDOMRemove       EdgeType = "dom_remove"
	EdgeTypeDOMListen       EdgeType = "dom_listen"
	// EdgeTypeDOMContract links a templ component that declares a stable
	// selector attribute (data-testid, id, other data-*) to the JS site that
	// reads it via a matching attribute selector
	// (querySelector('[data-testid="…"]')). Runs component -> consumer
	// (opposite direction from defined_in, and no intermediate element node)
	// so investigate/walkFlows reach the JS clone/read site in one hop out of
	// the rendering component that resolveNode already landed on.
	EdgeTypeDOMContract EdgeType = "dom_contract"
	EdgeTypeQueries     EdgeType = "queries"  // reads from a datastore
	EdgeTypePersists    EdgeType = "persists" // writes to a datastore
	EdgeTypeCloudCall   EdgeType = "cloud_call"
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
	// Protocol-specific publish edges for message-broker kinds added in G.4.
	// Using per-protocol types (rather than the generic publishes) so graph
	// queries can filter by protocol without inspecting edge meta.
	EdgeTypeKafkaPublish EdgeType = "kafka_publish"
	EdgeTypeNATSPublish  EdgeType = "nats_publish"
	EdgeTypeRedisPublish EdgeType = "redis_publish"
	// gRPC and GraphQL call edges.
	EdgeTypeGRPCCall    EdgeType = "grpc_call"
	EdgeTypeGraphQLCall EdgeType = "graphql_call"
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
	// Response-type edges (Tier Y.4 — the return half of a request flow).
	//
	// returns: handler-function → struct it writes as its JSON response body
	// (the static type of the payload passed to json.Marshal/Encode or a
	// local ResponseWriter-first wrapper such as writeJSON). meta:
	// response_type=<qualified type>, container=slice (when the body is []T),
	// via=json_encode. Untyped bodies (map[string]any) emit no edge (#12).
	EdgeTypeReturns EdgeType = "returns"
	// consumes: client-function → interface it decodes a fetch response into
	// (the annotated/asserted type of `await res.json()` in TS). meta:
	// response_type=<name>, container=slice, via=json_decode. Untyped decodes
	// emit no edge (#12).
	EdgeTypeConsumes EdgeType = "consumes"
	// response_of: Go response struct → TS interface that mirrors its JSON
	// shape, joining the producer and consumer type across the language
	// boundary. Direction: server DTO → client DTO. meta: match=shape,
	// shared=<n>, jaccard=<0..1>. Emitted only for server-declared response
	// types (returns targets); untyped payloads are ledgered, never guessed.
	EdgeTypeResponseOf EdgeType = "response_of"
)

// SchemaVersion identifies the graph data-model generation. Bumped when node
// or edge semantics change in a way that invalidates cached parse results;
// the indexer forces a full re-index when the stored version differs.
const SchemaVersion = "31" // IA.5: dom_contract producer->consumer edge (data-testid/id attribute-selector seam)

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

// QualifiedLabel returns the class-scoped form of a method/function's name
// ("LicenseReportJobsController#create") when the parser recorded one, falling
// back to just the containing class/type name, or "" when neither is set.
//
// A bare method label like "create" is indistinguishable from every other
// same-named method in the repo to both FTS indexes (nodes_fts and the
// semantic corpus) — the containing class only otherwise appears in the file
// path, snake_cased, which can't prefix-match a query written in PascalCase
// (a case/punctuation-convention gap FTS5 prefix matching can't bridge).
func (n *Node) QualifiedLabel() string {
	if n.Meta == nil {
		return ""
	}
	if qn := n.Meta["qualified_name"]; qn != "" {
		return qn
	}
	return n.Meta["class"]
}

// Confidence levels for edges — how certain the linker is about a match.
const (
	ConfidenceStatic   = "static"   // literal string match
	ConfidenceInferred = "inferred" // wildcard/normalized match
	ConfidencePartial  = "partial"  // partially resolved
	ConfidenceUnknown  = "unknown"  // dynamic, unresolvable
	// F.0: evidence-fusion confidence ladder additions (used in SourceRef.Confidence).
	// These are distinct from the match-quality constants above; ConfidenceStatic
	// (a literal string match quality) is unrelated to Provider name "static".
	ConfidenceObserved  = "observed"  // runtime evidence (OTel spans)
	ConfidenceDeclared  = "declared"  // contract/IDL evidence (OpenAPI, proto, …)
	ConfidenceCandidate = "candidate" // llm or static-only unconfirmed
)

// Verification states for fused evidence edges (F.0).
const (
	StateVerified        = "verified"          // static ∩ (runtime ∨ contract) agree
	StateCandidate       = "candidate"         // static-only (possible, unconfirmed)
	StateObservedOnlyGap = "observed_only_gap" // runtime/contract shows edge static missed
	StateConflicting     = "conflicting"       // sources disagree
)

// Verified-granularity values (F.0). Channel-granular evidence (a span, an
// OpenAPI operation) proves the channel is real — never that a specific call
// site ran. "site" is set only when the evidence itself carries code-level
// attribution (e.g. code.filepath span attributes), never inferred.
const (
	GranularityChannel = "channel"
	GranularitySite    = "site"
)

// SourceRef records one evidence contribution to an edge (F.0).
// Provider is one of the five pinned names: static | contract | runtime | config | llm.
// Confidence uses the evidence-fusion ladder (observed > declared > inferred > candidate > unknown).
// Ref is provider-specific provenance (file:line for static, "<session>/<trace_id>" for runtime, …).
// CodeFile / CodeFunc are set when the span carried code.filepath / code.function attributes;
// their presence upgrades VerifiedGranularity from "channel" to "site" (R.1, runtime only).
type SourceRef struct {
	Provider   string `json:"provider"`
	Confidence string `json:"confidence"`
	Ref        string `json:"ref,omitempty"`
	ObservedAt int64  `json:"observed_at,omitempty"` // runtime only, unix seconds
	CodeFile   string `json:"code_file,omitempty"`   // runtime only, from code.filepath
	CodeFunc   string `json:"code_func,omitempty"`   // runtime only, from code.function
}

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
	// F.0: evidence provenance. Sources must be non-empty after reconciliation
	// (an edge with no Sources[] is a schema error). VerificationState is
	// recomputed from Sources[] on every index run — never incrementally patched.
	Sources             []SourceRef `json:"sources,omitempty"`
	VerificationState   string      `json:"verification_state,omitempty"`
	VerifiedGranularity string      `json:"verified_granularity,omitempty"` // "channel" | "site"
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

// UnresolvedHistoryRow is one row of the per-index-run unresolved count log.
// Each index run writes one row per (service, kind) pair; the history table
// keeps the last 50 distinct run timestamps for trend computation.
type UnresolvedHistoryRow struct {
	RunAt   int64  `json:"run_at"` // unix timestamp of the index run
	Service string `json:"service"`
	Kind    string `json:"kind"`
	Count   int    `json:"count"`
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

// AllEdges returns all edges in the index sorted by ID (bug-class rule 2:
// never iterate a map directly). Each edge appears exactly once.
func (idx *AdjacencyIndex) AllEdges() []Edge {
	seen := make(map[string]bool)
	var edges []Edge
	for _, list := range idx.OutEdges {
		for _, e := range list {
			if !seen[e.ID] {
				edges = append(edges, *e)
				seen[e.ID] = true
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	return edges
}
