package pluginloader

import (
	"context"

	"github.com/lordsonvimal/polyflow/internal/deps"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/sdk/linkplugin"
)

// PackageQualifies is step 3 of the plan's load sequence for one
// (component, service) pair: a component whose manifest.yaml package: is not
// among the service's resolved dependencies is never invoked for that
// service, full stop. Phase 1 checks presence only — version_range gating
// against the resolved version is Phase 2 (docs/linker-plugin-architecture-plan.md).
// An empty pkg (no dependency declared) always qualifies, matching
// internal/patterns.gateSatisfied's identical convention for pattern files.
func PackageQualifies(pkg string, svcDeps []deps.Dependency) bool {
	if pkg == "" {
		return true
	}
	for _, d := range svcDeps {
		if d.Name == pkg {
			return true
		}
	}
	return false
}

// LinkCall performs one batched Link() RPC round-trip: one component, one
// service, one file-batch (LinkContext's batching requirement — see the
// plan's Performance section). capServer is nil unless requires is
// non-empty, in which case it backs the capability dial-back the plugin
// subprocess may call back into via the go-plugin GRPCBroker.
func (l *LaunchedPlugin) LinkCall(ctx context.Context, componentID, service string, files []string, nodes []graph.Node, requires []string, capServer *CapabilitiesServer) (LinkResult, error) {
	caps := make([]linkplugin.Capability, 0, len(requires))
	for _, r := range requires {
		caps = append(caps, linkplugin.Capability(r))
	}
	req := linkplugin.LinkCallRequest{
		ComponentID:  componentID,
		Service:      service,
		Files:        files,
		Nodes:        toSDKNodes(nodes),
		Capabilities: caps,
	}
	if capServer != nil {
		req.CapabilitiesServer = capServer
	}

	res, err := l.Client.Link(ctx, req)
	if err != nil {
		return LinkResult{}, err
	}
	return fromSDKResult(res), nil
}

// ReconcileCall performs one Reconcile() RPC round-trip — once per plugin,
// after every plugin's Link results across every service and component have
// been merged. A plugin that only implements linkplugin.Plugin (not
// linkplugin.Reconciler) returns an empty LinkResult, nil.
func (l *LaunchedPlugin) ReconcileCall(ctx context.Context, componentResults, allResults map[string]LinkResult) (LinkResult, error) {
	res, err := l.Client.Reconcile(ctx, linkplugin.ReconcileCallRequest{
		ComponentResults: toSDKResultMap(componentResults),
		AllResults:       toSDKResultMap(allResults),
	})
	if err != nil {
		return LinkResult{}, err
	}
	return fromSDKResult(res), nil
}
