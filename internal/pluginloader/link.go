package pluginloader

import (
	"context"

	"github.com/lordsonvimal/polyflow/internal/deps"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/lordsonvimal/polyflow/sdk/linkplugin"
)

// PackageQualifies is step 3 of the plan's load sequence for one
// (component, service) pair: a component whose manifest.yaml package: is not
// among the service's resolved dependencies is never invoked for that
// service, full stop. Presence only — version_range gating against the
// resolved version is a separate step, VersionQualifies (Phase 2). An empty
// pkg (no dependency declared) always qualifies, matching
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

// resolvedVersion returns the version a service resolved for pkg, or "" if
// pkg isn't among svcDeps. Callers only reach here after PackageQualifies
// has already confirmed presence, but VersionQualifies re-derives it rather
// than take it on faith from the caller.
func resolvedVersion(pkg string, svcDeps []deps.Dependency) string {
	for _, d := range svcDeps {
		if d.Name == pkg {
			return d.Version
		}
	}
	return ""
}

// VersionQualifies is step 4 of the plan's load sequence for one
// (component, service) pair that already passed PackageQualifies: the
// resolved package version is checked against the component's
// version_range using the exact same semver gate pattern YAML files use
// (patterns.VersionInRange, docs/versioning-matrix-plan.md's
// internal/patterns/version.go). An empty VersionRange always qualifies —
// a component with no version_range: declared is assumed version-agnostic.
// A resolved version that fails the range, or that can't be resolved at
// all, does not qualify; unlike PackageQualifies this is never silent — the
// caller must record a CoverageNote (out-of-range service, not skipped
// silently, per the plan's fail-safe contract).
func VersionQualifies(c Component, svcDeps []deps.Dependency) (ok bool, version string) {
	version = resolvedVersion(c.Package, svcDeps)
	if c.VersionRange == "" {
		return true, version
	}
	if version == "" {
		return false, version
	}
	return patterns.VersionInRange(version, c.VersionRange), version
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
