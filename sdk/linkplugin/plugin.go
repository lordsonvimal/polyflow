// Package linkplugin is the SDK a linker-plugin author imports
// (`go get github.com/lordsonvimal/polyflow/sdk/linkplugin`). It is the only
// polyflow package a plugin depends on — never internal/*. See
// docs/linker-plugin-architecture-plan.md's "Pinned Go surface" section,
// which this file implements verbatim.
package linkplugin

import (
	"context"
	"fmt"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	lpgraph "github.com/lordsonvimal/polyflow/sdk/linkplugin/graph"
	pb "github.com/lordsonvimal/polyflow/sdk/linkplugin/proto"
)

// ProtocolVersion is frozen at the end of Phase 0 — see
// docs/linker-plugin-architecture-plan.md Phase 0. A bump here breaks every
// plugin built against the old SDK (see the plan's Risks section), so this
// changes only with a deliberate, documented protocol revision.
const ProtocolVersion = 1

// Handshake is the go-plugin magic-cookie handshake, shared by both the
// plugin subprocess (Serve) and core's loader (internal/pluginloader). The
// cookie value is not a secret — it only prevents a plugin binary from
// accidentally being invoked directly as a normal program and
// misinterpreting stdin/stdout.
var Handshake = goplugin.HandshakeConfig{
	ProtocolVersion:  ProtocolVersion,
	MagicCookieKey:   "POLYFLOW_LINKPLUGIN",
	MagicCookieValue: "polyflow-linkplugin-v1",
}

// PluginMapKey is the single entry in the go-plugin plugin map — one plugin
// binary hosts exactly one linkplugin.Plugin (which may itself multiplex
// several manifest.yaml components internally, per LinkContext.ComponentID).
const PluginMapKey = "linkplugin"

// Capability is a core primitive a manifest.yaml component can declare in
// its requires: list. Declaring one wires the matching field on LinkContext;
// omitting it leaves that field nil.
type Capability string

const (
	CapContainment    Capability = "containment"
	CapSymbolResolver Capability = "symbol_resolver"
	CapKeyLedger      Capability = "dynamic_key_ledger"
)

// Scope is one bulk-query capability's answer for one file: a class/module
// containment scope (ContainmentIndex), a resolved symbol's defining scope
// (SymbolResolver), or a dynamic-key ledger entry (DynamicKeyLedger).
type Scope struct {
	Kind  string
	ID    string
	Label string
}

// ContainmentIndex, SymbolResolver, DynamicKeyLedger are bulk-query
// interfaces (BulkResolve(files []string) map[string]Scope), never
// single-node methods — see docs/linker-plugin-architecture-plan.md's
// Performance section: the interface shape is the batching-enforcement
// mechanism, not a lint rule or a review checklist. A chatty per-node query
// pattern cannot be expressed against this SDK at all.
type ContainmentIndex interface {
	BulkResolve(files []string) map[string]Scope
}

type SymbolResolver interface {
	BulkResolve(files []string) map[string]Scope
}

type DynamicKeyLedger interface {
	BulkResolve(files []string) map[string]Scope
}

// LinkContext is the primitive surface. Every field is populated in one
// batched RPC round-trip per call, never queried node-by-node.
type LinkContext struct {
	ComponentID string
	Service     string
	Files       []string
	Nodes       []lpgraph.Node

	// Containment, Symbols, KeyLedger are nil unless this component declared
	// the matching Capability in Requires(componentID).
	Containment ContainmentIndex
	Symbols     SymbolResolver
	KeyLedger   DynamicKeyLedger
}

// Result is a plugin's Link/Reconcile output.
type Result struct {
	Edges      []lpgraph.Edge
	Unresolved []lpgraph.UnresolvedRef
	// Retract lists unresolved-ref keys to drop, mirroring DropResolvedRefs
	// (internal/linker/amqp_handshake.go) for the reconcile pass.
	Retract []string
}

// ReconcileContext is populated once per plugin (not per component), after
// every plugin's Link results across every service and every component have
// been merged.
type ReconcileContext struct {
	// ComponentResults is this plugin's own per-component Link output,
	// pooled across every service, keyed by componentID.
	ComponentResults map[string]Result
	// AllResults is read-only visibility into what core's built-in linkers
	// and other plugins already resolved, keyed by plugin Name().
	AllResults map[string]Result
}

// Plugin is what an author implements. main() is exactly:
//
//	linkplugin.Serve(MyPlugin{})
//
// Serve owns the subprocess lifecycle, the wire protocol, and the version
// handshake — an author never touches serialization or IPC directly.
type Plugin interface {
	Name() string

	// Requires is per-component, matching manifest.yaml's components[].requires
	// — validated against the manifest at handshake time by core's loader
	// (mismatch fails loudly, see the plan's Risks: "Requires()
	// under-declaration").
	Requires(componentID string) []Capability

	// Link runs once per (componentID, service, file-batch).
	Link(ctx *LinkContext) (Result, error)
}

// Reconciler is an optional extension to Plugin: a plugin with no
// cross-service/cross-component concern implements Link only and does not
// need to implement this interface. Kept separate from Plugin (rather than
// a required method every author must stub) so "optional" is enforced by
// the type system, not by convention.
type Reconciler interface {
	// Reconcile runs once per plugin, after every plugin's Link results
	// across every service and every component are merged — for
	// handshake-style resolution (e.g. an AMQP publish captured in service A
	// and a consume captured in service B) and for cross-component
	// resolution within one framework.
	Reconcile(ctx *ReconcileContext) (Result, error)
}

// Serve starts the plugin subprocess and blocks until core disconnects. It
// is the entire body of a plugin's main().
func Serve(p Plugin) {
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: Handshake,
		Plugins: map[string]goplugin.Plugin{
			PluginMapKey: &grpcPlugin{impl: p},
		},
		GRPCServer: goplugin.DefaultGRPCServer,
	})
}

// grpcPlugin implements hashicorp/go-plugin's plugin.GRPCPlugin: GRPCServer
// runs inside the plugin subprocess (registers impl against the LinkPlugin
// service); GRPCClient runs inside core's process (wraps the connection into
// a Go-callable Client).
type grpcPlugin struct {
	goplugin.NetRPCUnsupportedPlugin
	impl Plugin
}

func (g *grpcPlugin) GRPCServer(broker *goplugin.GRPCBroker, s *grpc.Server) error {
	pb.RegisterLinkPluginServer(s, &linkPluginServer{impl: g.impl, broker: broker})
	return nil
}

func (g *grpcPlugin) GRPCClient(_ context.Context, broker *goplugin.GRPCBroker, conn *grpc.ClientConn) (interface{}, error) {
	return &Client{client: pb.NewLinkPluginClient(conn), broker: broker}, nil
}

// linkPluginServer adapts a Plugin implementation to the generated
// pb.LinkPluginServer interface, translating wire messages to/from the SDK's
// Go types and wiring capability dial-backs (see capabilities.go) for
// components that declared them.
type linkPluginServer struct {
	pb.UnimplementedLinkPluginServer
	impl   Plugin
	broker *goplugin.GRPCBroker
}

func (s *linkPluginServer) Handshake(_ context.Context, req *pb.HandshakeRequest) (*pb.HandshakeResponse, error) {
	if req.GetProtocolVersion() != ProtocolVersion {
		return nil, fmt.Errorf("linkplugin: protocol_version mismatch: core=%d plugin=%d", req.GetProtocolVersion(), ProtocolVersion)
	}
	return &pb.HandshakeResponse{ProtocolVersion: ProtocolVersion, Name: s.impl.Name()}, nil
}

func (s *linkPluginServer) Requires(_ context.Context, req *pb.RequiresRequest) (*pb.RequiresResponse, error) {
	caps := s.impl.Requires(req.GetComponentId())
	out := make([]string, 0, len(caps))
	for _, c := range caps {
		out = append(out, string(c))
	}
	return &pb.RequiresResponse{Capabilities: out}, nil
}

func (s *linkPluginServer) Link(ctx context.Context, req *pb.LinkRequest) (*pb.LinkResponse, error) {
	nodes := make([]lpgraph.Node, 0, len(req.GetNodes()))
	for _, n := range req.GetNodes() {
		nodes = append(nodes, nodeFromPB(n))
	}

	lc := &LinkContext{
		ComponentID: req.GetComponentId(),
		Service:     req.GetService(),
		Files:       req.GetFiles(),
		Nodes:       nodes,
	}

	var conn *grpc.ClientConn
	if req.GetBrokerId() != 0 {
		var err error
		conn, err = s.broker.Dial(req.GetBrokerId())
		if err != nil {
			return nil, fmt.Errorf("linkplugin: dial capabilities broker: %w", err)
		}
		defer conn.Close()
		capsClient := pb.NewCapabilitiesClient(conn)
		for _, c := range req.GetCapabilities() {
			switch Capability(c) {
			case CapContainment:
				lc.Containment = &bulkResolverClient{kind: capKindContainment, client: capsClient}
			case CapSymbolResolver:
				lc.Symbols = &bulkResolverClient{kind: capKindSymbol, client: capsClient}
			case CapKeyLedger:
				lc.KeyLedger = &bulkResolverClient{kind: capKindKeyLedger, client: capsClient}
			}
		}
	}

	result, err := s.impl.Link(lc)
	if err != nil {
		return nil, err
	}
	return &pb.LinkResponse{Result: resultToPB(result)}, nil
}

func (s *linkPluginServer) Reconcile(_ context.Context, req *pb.ReconcileRequest) (*pb.ReconcileResponse, error) {
	r, ok := s.impl.(Reconciler)
	if !ok {
		return &pb.ReconcileResponse{Result: &pb.Result{}}, nil
	}
	componentResults := make(map[string]Result, len(req.GetComponentResults()))
	for k, v := range req.GetComponentResults() {
		componentResults[k] = resultFromPB(v)
	}
	allResults := make(map[string]Result, len(req.GetAllResults()))
	for k, v := range req.GetAllResults() {
		allResults[k] = resultFromPB(v)
	}
	result, err := r.Reconcile(&ReconcileContext{ComponentResults: componentResults, AllResults: allResults})
	if err != nil {
		return nil, err
	}
	return &pb.ReconcileResponse{Result: resultToPB(result)}, nil
}
