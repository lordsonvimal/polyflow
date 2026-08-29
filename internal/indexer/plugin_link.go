package indexer

import (
	"fmt"
	"io"

	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/lordsonvimal/polyflow/internal/pluginloader"
)

// loadLinkPlugins is docs/linker-plugin-architecture-plan.md's load sequence
// steps 1-2-5: discover every manifest.yaml under repoRoot's plugin
// directories ($POLYFLOW_PLUGINS_DIR, <repoRoot>/.polyflow/plugins), skip
// whole manifests whose protocol_version core doesn't speak, and register
// each remaining component's pattern file into reg via the exact same
// RegisterFile path cfg.Patterns' workspace-custom patterns already use a
// few lines above this function's call site — so a plugin's patterns get
// identical per-service package/version gating (patterns.Registry.ForService,
// applied per-service in the scan loop below) with zero plugin-specific
// matcher logic (plan step 5: "no plugin-specific parser logic").
//
// Subprocess spawning (step 6) is deliberately not done here — it happens
// lazily, once per plugin, the first time a link pass actually needs to call
// it (internal/indexer/plugin_passes.go), so a manifest with no qualifying
// (component, service) pair for this run never pays a process-spawn cost.
func loadLinkPlugins(reg *patterns.Registry, repoRoot string, logw io.Writer) []*pluginloader.Manifest {
	manifestPaths, err := pluginloader.Discover(repoRoot)
	if err != nil {
		fmt.Fprintf(logw, "  Warning: plugin discovery: %v\n", err)
		return nil
	}

	var manifests []*pluginloader.Manifest
	for _, p := range manifestPaths {
		m, err := pluginloader.LoadManifest(p)
		if err != nil {
			fmt.Fprintf(logw, "  Warning: plugin manifest %s: %v\n", p, err)
			continue
		}
		if note := pluginloader.CheckProtocolVersion(m); note != nil {
			fmt.Fprintf(logw, "  Warning: plugin %s: %s\n", note.Plugin, note.Reason)
			continue
		}
		for _, c := range m.Components {
			pf, err := patterns.LoadFile(m.PatternsPath(c))
			if err != nil {
				fmt.Fprintf(logw, "  Warning: plugin %s/%s patterns: %v\n", m.Name, c.ID, err)
				continue
			}
			reg.RegisterFile(pf)
		}
		manifests = append(manifests, m)
	}
	return manifests
}
