package linkplugin

import (
	"context"

	pb "github.com/lordsonvimal/polyflow/sdk/linkplugin/proto"
)

// capKind selects which Capabilities RPC a bulkResolverClient calls — the
// three bulk-query capabilities share one wire message shape
// (BulkResolveRequest/Response) but are distinct RPCs so core can back each
// with a different in-process index (internal/pluginloader/capabilities.go).
type capKind int

const (
	capKindContainment capKind = iota
	capKindSymbol
	capKindKeyLedger
)

// bulkResolverClient runs inside the plugin subprocess, implementing
// ContainmentIndex/SymbolResolver/DynamicKeyLedger by dialing back to core
// over the GRPCBroker connection the Link RPC's broker_id named. One RPC per
// BulkResolve call, whatever the size of files — the batching contract the
// SDK's interface shape enforces.
type bulkResolverClient struct {
	kind   capKind
	client pb.CapabilitiesClient
}

func (b *bulkResolverClient) BulkResolve(files []string) map[string]Scope {
	req := &pb.BulkResolveRequest{Files: files}
	var resp *pb.BulkResolveResponse
	var err error
	switch b.kind {
	case capKindContainment:
		resp, err = b.client.ContainmentBulkResolve(context.Background(), req)
	case capKindSymbol:
		resp, err = b.client.SymbolBulkResolve(context.Background(), req)
	case capKindKeyLedger:
		resp, err = b.client.KeyLedgerBulkResolve(context.Background(), req)
	}
	if err != nil {
		return nil
	}
	out := make(map[string]Scope, len(resp.GetScopes()))
	for k, v := range resp.GetScopes() {
		out[k] = Scope{Kind: v.GetKind(), ID: v.GetId(), Label: v.GetLabel()}
	}
	return out
}
