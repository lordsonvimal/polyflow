package indexer

import (
	"context"
	"fmt"
	"io"

	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
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
// Subprocess spawning (step 6) is deliberately not done here for a Link
// component — it happens lazily, once per plugin, the first time a link pass
// actually needs to call it (internal/indexer/plugin_passes.go), so a
// manifest with no qualifying (component, service) pair for this run never
// pays a process-spawn cost. A component that declares provides_verbs (FX.9,
// docs/declarative-framework-pipeline-plan.md) is the one exception: an
// extract: verb must be registered into internal/patterns' dispatch before
// NewTreeSitterMatcherForService ever runs a query that could name it, which
// is strictly earlier than any link pass — so that one plugin is launched
// eagerly, right here, and the resulting client is returned for the caller
// to reuse (never launched twice) and to close alongside every lazily
// launched one.
func loadLinkPlugins(reg *patterns.Registry, repoRoot string, logw io.Writer) ([]*pluginloader.Manifest, map[string]*pluginloader.LaunchedPlugin) {
	manifestPaths, err := pluginloader.Discover(repoRoot)
	if err != nil {
		fmt.Fprintf(logw, "  Warning: plugin discovery: %v\n", err)
		return nil, nil
	}

	var frameworkNames []string
	if fxReg, err := pipeline.LoadEmbedded(); err == nil {
		for _, fw := range fxReg.All() {
			frameworkNames = append(frameworkNames, fw.Name)
		}
	} else {
		fmt.Fprintf(logw, "  Warning: FX.9 generic-only check: could not load framework names: %v\n", err)
	}

	var manifests []*pluginloader.Manifest
	clients := map[string]*pluginloader.LaunchedPlugin{}
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
		var needsVerbs bool
		for _, c := range m.Components {
			pf, err := patterns.LoadFile(m.PatternsPath(c))
			if err != nil {
				fmt.Fprintf(logw, "  Warning: plugin %s/%s patterns: %v\n", m.Name, c.ID, err)
				continue
			}
			reg.RegisterFile(pf)
			if len(c.ProvidesVerbs) > 0 {
				needsVerbs = true
			}
		}
		if needsVerbs {
			client, err := pluginloader.Launch(m)
			if err != nil {
				fmt.Fprintf(logw, "  Warning: plugin %s: launch for verb registration: %v\n", m.Name, err)
			} else {
				notes, err := client.RegisterVerbs(context.Background(), frameworkNames)
				if err != nil {
					fmt.Fprintf(logw, "  Warning: plugin %s: RegisterVerbs: %v\n", m.Name, err)
				}
				for _, n := range notes {
					fmt.Fprintf(logw, "  Warning: plugin %s: %s\n", n.Plugin, n.Reason)
				}
				clients[m.Name] = client
			}
		}
		manifests = append(manifests, m)
	}
	return manifests, clients
}
