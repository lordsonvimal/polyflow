package indexer

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/pluginloader"
)

// insertPluginPasses splices one Link pass per (plugin, component) and one
// Reconcile pass per plugin into the otherwise-fixed buildLinkPasses list,
// at the positions docs/linker-plugin-architecture-plan.md's "DB persistence
// & pass timing" section specifies: Link passes are scopeSameServiceOnly,
// same slot as js_link/ruby_associations/etc, so they run right before
// file_route_synthesis (itself documented as running "after per-file
// parsing and all linking passes above"); Reconcile passes are
// scopeCrossService cross-service join barriers, same slot amqp_handshake
// already occupies, so they run immediately after it. A workspace with no
// discovered plugins pays nothing here — both inserts are no-ops.
func insertPluginPasses(st *linkPipelineState, passes []namedPass) []namedPass {
	if len(st.pluginManifests) == 0 {
		return passes
	}
	passes = insertPassesBefore(passes, "file_route_synthesis", pluginLinkPasses(st))
	passes = insertPassesAfter(passes, "amqp_handshake", pluginReconcilePasses(st))
	return passes
}

func insertPassesBefore(passes []namedPass, anchor string, insert []namedPass) []namedPass {
	if len(insert) == 0 {
		return passes
	}
	for i, p := range passes {
		if p.name == anchor {
			out := make([]namedPass, 0, len(passes)+len(insert))
			out = append(out, passes[:i]...)
			out = append(out, insert...)
			return append(out, passes[i:]...)
		}
	}
	return append(passes, insert...)
}

func insertPassesAfter(passes []namedPass, anchor string, insert []namedPass) []namedPass {
	if len(insert) == 0 {
		return passes
	}
	for i, p := range passes {
		if p.name == anchor {
			out := make([]namedPass, 0, len(passes)+len(insert))
			out = append(out, passes[:i+1]...)
			out = append(out, insert...)
			return append(out, passes[i+1:]...)
		}
	}
	return append(passes, insert...)
}

// pluginLinkPasses returns one scopeSameServiceOnly pass per manifest
// component, named "plugin:<manifest.Name>/<component.ID>" so an error
// wrapped by Run()'s "link pass %s: %w" identifies exactly which plugin
// component failed.
func pluginLinkPasses(st *linkPipelineState) []namedPass {
	var out []namedPass
	for _, m := range st.pluginManifests {
		m := m
		for _, c := range m.Components {
			c := c
			out = append(out, namedPass{
				name:  fmt.Sprintf("plugin:%s/%s", m.Name, c.ID),
				scope: scopeSameServiceOnly,
				exec:  func() error { return runPluginComponentLink(st, m, c) },
			})
		}
	}
	return out
}

// pluginReconcilePasses returns one scopeCrossService pass per manifest —
// Reconcile runs once per plugin (not per component), never per service.
func pluginReconcilePasses(st *linkPipelineState) []namedPass {
	var out []namedPass
	for _, m := range st.pluginManifests {
		m := m
		out = append(out, namedPass{
			name:  fmt.Sprintf("plugin:%s/reconcile", m.Name),
			scope: scopeCrossService,
			exec:  func() error { return runPluginReconcile(st, m) },
		})
	}
	return out
}

// launchPlugin lazily spawns m's subprocess the first time any of its
// components actually qualifies for a service, and caches the handle for
// reuse by every later component/service pair and by this plugin's
// Reconcile pass — "once per plugin per index run", never once per call
// (docs/linker-plugin-architecture-plan.md's load sequence step 6).
func (st *linkPipelineState) launchPlugin(m *pluginloader.Manifest) (*pluginloader.LaunchedPlugin, error) {
	if st.pluginClients == nil {
		st.pluginClients = map[string]*pluginloader.LaunchedPlugin{}
	}
	if c, ok := st.pluginClients[m.Name]; ok {
		return c, nil
	}
	c, err := pluginloader.Launch(m)
	if err != nil {
		return nil, fmt.Errorf("launch plugin %s: %w", m.Name, err)
	}
	st.pluginClients[m.Name] = c
	return c, nil
}

// recordPluginResult pools one (component, service) Link call's result into
// this plugin's per-component accumulator, the exact shape Reconcile's
// ComponentResults/AllResults need ("this plugin's own per-component Link
// output pooled across every service").
func (st *linkPipelineState) recordPluginResult(pluginName, componentID string, result pluginloader.LinkResult) {
	if st.pluginComponentResults == nil {
		st.pluginComponentResults = map[string]map[string]pluginloader.LinkResult{}
	}
	byComponent := st.pluginComponentResults[pluginName]
	if byComponent == nil {
		byComponent = map[string]pluginloader.LinkResult{}
		st.pluginComponentResults[pluginName] = byComponent
	}
	acc := byComponent[componentID]
	acc.Edges = append(acc.Edges, result.Edges...)
	acc.Unresolved = append(acc.Unresolved, result.Unresolved...)
	acc.Retract = append(acc.Retract, result.Retract...)
	byComponent[componentID] = acc
}

// unresolvedRefKey is the Result.Retract key format a plugin author uses to
// name one of its own earlier Unresolved entries for later dropping —
// mirrors linker.DropResolvedRefs' role for the in-process amqp_handshake
// precedent, but keyed by (Kind, File, Name) since a plugin's Retract list
// crosses an RPC boundary and can't carry a Go closure or map reference.
func unresolvedRefKey(r graph.UnresolvedRef) string {
	return r.Kind + "\x00" + r.File + "\x00" + r.Name
}

// applyPluginRetract drops every existing unresolved ref whose
// unresolvedRefKey appears in retract — the reconcile-pass mirror of
// linker.DropResolvedRefs for plugin-sourced entries.
func applyPluginRetract(existing []graph.UnresolvedRef, retract []string) []graph.UnresolvedRef {
	if len(retract) == 0 {
		return existing
	}
	drop := make(map[string]bool, len(retract))
	for _, k := range retract {
		drop[k] = true
	}
	kept := existing[:0]
	for _, r := range existing {
		if !drop[unresolvedRefKey(r)] {
			kept = append(kept, r)
		}
	}
	return kept
}

// runPluginComponentLink is one (plugin, component)'s namedPass body: for
// every service whose resolved deps include this component's package: (step
// 3 of the load sequence — internal/pluginloader.PackageQualifies), batch
// every node this component's patterns produced in that service (Language
// match, step 7) into one Link() RPC call.
func runPluginComponentLink(st *linkPipelineState, m *pluginloader.Manifest, c pluginloader.Component) error {
	for _, sf := range st.allSvcFiles {
		if !pluginloader.PackageQualifies(c.Package, sf.deps) {
			continue
		}
		if ok, version := pluginloader.VersionQualifies(c, sf.deps); !ok {
			st.pluginCoverageNotes = append(st.pluginCoverageNotes, pluginloader.CoverageNote{
				Plugin:    m.Name,
				Component: c.ID,
				Reason: fmt.Sprintf("%s: resolved version %q outside version_range %q for service %s",
					c.Package, version, c.VersionRange, sf.svc.Name),
			})
			continue
		}

		// sf.files is absolute; graph.Node.File may be stored either
		// workspace-root-relative or absolute depending on which parser path
		// produced it (see internal/parser/go_semantic.go's canonicalPath
		// convention, applied inconsistently across per-language parse
		// paths). Comparing through a symlink-resolved-absolute intermediate
		// on both sides — rather than relying on either side's raw string
		// form — makes the match correct regardless of which form a given
		// node happened to get, and immune to a macOS /var-vs-/private/var
		// TempDir symlink besides.
		rawCwd, err := os.Getwd()
		if err != nil {
			rawCwd = "."
		}
		svcFileSet := make(map[string]bool, len(sf.files))
		for _, f := range sf.files {
			svcFileSet[canonicalPluginPath(f)] = true
		}

		var files []string
		var nodes []graph.Node
		seenFile := map[string]bool{}
		for _, n := range st.allNodes {
			if n.Language != c.Language {
				continue
			}
			nFile := n.File
			if !filepath.IsAbs(nFile) {
				nFile = filepath.Join(rawCwd, nFile)
			}
			if !svcFileSet[canonicalPluginPath(nFile)] {
				continue
			}
			nodes = append(nodes, n)
			if !seenFile[n.File] {
				seenFile[n.File] = true
				files = append(files, n.File)
			}
		}
		if len(nodes) == 0 {
			// This component's patterns matched nothing in this service — no
			// Link() call worth making (batching discipline: never call for
			// an empty batch).
			continue
		}

		client, err := st.launchPlugin(m)
		if err != nil {
			return err
		}

		var capServer *pluginloader.CapabilitiesServer
		if len(c.Requires) > 0 {
			capServer = pluginloader.NewCapabilitiesServer(st.allNodes, st.allEdges, st.allUnresolved)
		}

		result, err := client.LinkCall(st.ctx, c.ID, sf.svc.Name, files, nodes, c.Requires, capServer)
		if err != nil {
			return fmt.Errorf("plugin %s/%s link (%s): %w", m.Name, c.ID, sf.svc.Name, err)
		}

		st.allUnresolved = applyPluginRetract(st.allUnresolved, result.Retract)
		st.allUnresolved = append(st.allUnresolved, result.Unresolved...)
		if err := st.writeEdges(result.Edges); err != nil {
			return err
		}
		st.recordPluginResult(m.Name, c.ID, result)
	}
	return nil
}

// runPluginReconcile is one plugin's Reconcile namedPass body — a no-op if
// the plugin was never launched (no component ever qualified for any
// service this run).
func runPluginReconcile(st *linkPipelineState, m *pluginloader.Manifest) error {
	client, ok := st.pluginClients[m.Name]
	if !ok {
		return nil
	}

	componentResults := st.pluginComponentResults[m.Name]

	allResults := map[string]pluginloader.LinkResult{}
	for name, byComponent := range st.pluginComponentResults {
		if name == m.Name {
			continue
		}
		var acc pluginloader.LinkResult
		for _, r := range byComponent {
			acc.Edges = append(acc.Edges, r.Edges...)
			acc.Unresolved = append(acc.Unresolved, r.Unresolved...)
		}
		allResults[name] = acc
	}

	result, err := client.ReconcileCall(st.ctx, componentResults, allResults)
	if err != nil {
		return fmt.Errorf("plugin %s reconcile: %w", m.Name, err)
	}

	st.allUnresolved = applyPluginRetract(st.allUnresolved, result.Retract)
	st.allUnresolved = append(st.allUnresolved, result.Unresolved...)
	return st.writeEdges(result.Edges)
}

// canonicalPluginPath mirrors internal/parser/go_semantic.go's canonicalPath:
// absolute + symlink-resolved, so a path built from a service's Config.Path
// compares equal to graph.Node.File's cwd-relative form regardless of a
// symlinked temp/working directory.
func canonicalPluginPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r
	}
	return abs
}
