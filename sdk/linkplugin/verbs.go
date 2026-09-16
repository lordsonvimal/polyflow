package linkplugin

import (
	"context"

	pb "github.com/lordsonvimal/polyflow/sdk/linkplugin/proto"
)

func verbNodeToPB(n VerbNode) *pb.VerbAstNode {
	return &pb.VerbAstNode{
		Type:      n.Type,
		Text:      n.Text,
		StartLine: n.StartLine,
		EndLine:   n.EndLine,
	}
}

func verbNodeFromPB(n *pb.VerbAstNode) VerbNode {
	return VerbNode{
		Type:      n.GetType(),
		Text:      n.GetText(),
		StartLine: n.GetStartLine(),
		EndLine:   n.GetEndLine(),
	}
}

func verbValueToPB(v VerbValue) *pb.VerbValue {
	return &pb.VerbValue{Str: v.Str, IntVal: v.Int, IsInt: v.IsInt}
}

func verbValueFromPB(v *pb.VerbValue) VerbValue {
	return VerbValue{Str: v.GetStr(), Int: v.GetIntVal(), IsInt: v.GetIsInt()}
}

// verbProviderServer adapts a Plugin implementation to pb.VerbProviderServer.
// Registered unconditionally by grpcPlugin.GRPCServer; impl is type-asserted
// against VerbProvider on every call, the same optionality pattern
// linkPluginServer.Reconcile already uses for the Reconciler interface.
type verbProviderServer struct {
	pb.UnimplementedVerbProviderServer
	impl Plugin
}

func (s *verbProviderServer) Verbs(context.Context, *pb.VerbsRequest) (*pb.VerbsResponse, error) {
	vp, ok := s.impl.(VerbProvider)
	if !ok {
		return &pb.VerbsResponse{}, nil
	}
	return &pb.VerbsResponse{Names: vp.Verbs()}, nil
}

func (s *verbProviderServer) ExtractVerb(_ context.Context, req *pb.ExtractVerbRequest) (*pb.ExtractVerbResponse, error) {
	vp, ok := s.impl.(VerbProvider)
	if !ok {
		return &pb.ExtractVerbResponse{Ok: false}, nil
	}
	node := verbNodeFromPB(req.GetNode())
	ancestors := make([]VerbNode, 0, len(req.GetAncestors()))
	for _, a := range req.GetAncestors() {
		ancestors = append(ancestors, verbNodeFromPB(a))
	}
	node.Ancestors = ancestors

	vals, ok := vp.ExtractVerb(req.GetVerb(), req.GetArg(), node, req.GetFile(), req.GetGrammar())
	if !ok {
		return &pb.ExtractVerbResponse{Ok: false}, nil
	}
	pbVals := make([]*pb.VerbValue, 0, len(vals))
	for _, v := range vals {
		pbVals = append(pbVals, verbValueToPB(v))
	}
	return &pb.ExtractVerbResponse{Values: pbVals, Ok: true}, nil
}

// Verbs queries the plugin's advertised extract: verb names — called once at
// load time, before core wires anything into internal/patterns' verb
// dispatch (FX.9's generic-only enforcement runs against this list).
func (c *Client) Verbs(ctx context.Context) ([]string, error) {
	resp, err := c.verbClient.Verbs(ctx, &pb.VerbsRequest{})
	if err != nil {
		return nil, err
	}
	return resp.GetNames(), nil
}

// ExtractVerb runs one named verb against node — called once per capture,
// per verb use, mirroring an in-tree verb function's own call shape
// (internal/patterns/extract.go's runVerb).
func (c *Client) ExtractVerb(ctx context.Context, verb, arg string, node VerbNode, file, grammar string) ([]VerbValue, bool, error) {
	ancestors := make([]*pb.VerbAstNode, 0, len(node.Ancestors))
	for _, a := range node.Ancestors {
		ancestors = append(ancestors, verbNodeToPB(a))
	}
	req := &pb.ExtractVerbRequest{
		Verb:      verb,
		Arg:       arg,
		Node:      verbNodeToPB(node),
		Ancestors: ancestors,
		File:      file,
		Grammar:   grammar,
	}
	resp, err := c.verbClient.ExtractVerb(ctx, req)
	if err != nil {
		return nil, false, err
	}
	if !resp.GetOk() {
		return nil, false, nil
	}
	vals := make([]VerbValue, 0, len(resp.GetValues()))
	for _, v := range resp.GetValues() {
		vals = append(vals, verbValueFromPB(v))
	}
	return vals, true, nil
}
