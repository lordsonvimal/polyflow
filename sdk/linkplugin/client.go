package linkplugin

import (
	"context"
	"errors"
	"fmt"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	lpgraph "github.com/lordsonvimal/polyflow/sdk/linkplugin/graph"
	pb "github.com/lordsonvimal/polyflow/sdk/linkplugin/proto"
)

// HostPlugin is the go-plugin client-side stub internal/pluginloader passes
// as the plugin map entry in hashicorp/go-plugin's ClientConfig — it runs in
// core's own process, never the subprocess, so GRPCServer is never invoked.
type HostPlugin struct {
	goplugin.NetRPCUnsupportedPlugin
}

func (HostPlugin) GRPCServer(*goplugin.GRPCBroker, *grpc.Server) error {
	return errors.New("linkplugin: HostPlugin is core's client-side stub and cannot serve")
}

func (HostPlugin) GRPCClient(_ context.Context, broker *goplugin.GRPCBroker, conn *grpc.ClientConn) (interface{}, error) {
	return &Client{client: pb.NewLinkPluginClient(conn), broker: broker}, nil
}

// Client is core's handle to a running plugin subprocess, returned by
// grpcPlugin.GRPCClient. internal/pluginloader drives it; a plugin author
// never sees this type.
type Client struct {
	client pb.LinkPluginClient
	broker *goplugin.GRPCBroker
}

// Handshake validates protocol_version and returns the plugin's declared
// Name(). Called once per plugin subprocess, before any Link/Reconcile call.
func (c *Client) Handshake(ctx context.Context) (string, error) {
	resp, err := c.client.Handshake(ctx, &pb.HandshakeRequest{ProtocolVersion: ProtocolVersion})
	if err != nil {
		return "", err
	}
	if resp.GetProtocolVersion() != ProtocolVersion {
		return "", fmt.Errorf("linkplugin: protocol_version mismatch: core=%d plugin=%d", ProtocolVersion, resp.GetProtocolVersion())
	}
	return resp.GetName(), nil
}

// Requires returns the Capability list a component declared, for validation
// against that component's manifest.yaml requires: entry (the plan's Risks:
// "Requires() under-declaration" mitigation).
func (c *Client) Requires(ctx context.Context, componentID string) ([]Capability, error) {
	resp, err := c.client.Requires(ctx, &pb.RequiresRequest{ComponentId: componentID})
	if err != nil {
		return nil, err
	}
	caps := make([]Capability, 0, len(resp.GetCapabilities()))
	for _, c := range resp.GetCapabilities() {
		caps = append(caps, Capability(c))
	}
	return caps, nil
}

// LinkCallRequest is one batched Link() invocation: one component, one
// service, one file-batch. CapabilitiesServer is nil unless the calling
// component declared at least one Capability in Requires(); when non-nil,
// the client wires a GRPCBroker dial-back so the plugin subprocess can reach
// core's bulk-query capability implementations (internal/pluginloader).
type LinkCallRequest struct {
	ComponentID        string
	Service            string
	Files              []string
	Nodes              []lpgraph.Node
	Capabilities       []Capability
	CapabilitiesServer pb.CapabilitiesServer
}

// Link performs one Link() RPC round-trip.
func (c *Client) Link(ctx context.Context, r LinkCallRequest) (Result, error) {
	nodes := make([]*pb.Node, 0, len(r.Nodes))
	for _, n := range r.Nodes {
		nodes = append(nodes, nodeToPB(n))
	}

	req := &pb.LinkRequest{
		ComponentId: r.ComponentID,
		Service:     r.Service,
		Files:       r.Files,
		Nodes:       nodes,
	}

	if len(r.Capabilities) > 0 && r.CapabilitiesServer != nil {
		id := c.broker.NextId()
		go c.broker.AcceptAndServe(id, func(opts []grpc.ServerOption) *grpc.Server {
			s := grpc.NewServer(opts...)
			pb.RegisterCapabilitiesServer(s, r.CapabilitiesServer)
			return s
		})
		req.BrokerId = id
		caps := make([]string, 0, len(r.Capabilities))
		for _, cp := range r.Capabilities {
			caps = append(caps, string(cp))
		}
		req.Capabilities = caps
	}

	resp, err := c.client.Link(ctx, req)
	if err != nil {
		return Result{}, err
	}
	return resultFromPB(resp.GetResult()), nil
}

// ReconcileCallRequest is one Reconcile() invocation — runs once per plugin,
// after every plugin's Link results across every service and component have
// been merged.
type ReconcileCallRequest struct {
	ComponentResults map[string]Result
	AllResults       map[string]Result
}

// Reconcile performs one Reconcile() RPC round-trip. A plugin that only
// implements Plugin (not Reconciler) returns an empty Result, nil.
func (c *Client) Reconcile(ctx context.Context, r ReconcileCallRequest) (Result, error) {
	componentResults := make(map[string]*pb.Result, len(r.ComponentResults))
	for k, v := range r.ComponentResults {
		componentResults[k] = resultToPB(v)
	}
	allResults := make(map[string]*pb.Result, len(r.AllResults))
	for k, v := range r.AllResults {
		allResults[k] = resultToPB(v)
	}
	resp, err := c.client.Reconcile(ctx, &pb.ReconcileRequest{ComponentResults: componentResults, AllResults: allResults})
	if err != nil {
		return Result{}, err
	}
	return resultFromPB(resp.GetResult()), nil
}
