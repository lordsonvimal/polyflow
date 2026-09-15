package factpipe

import (
	"fmt"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// hub.go is the Tier FX cross-framework fact-sharing mechanism (plan
// docs/declarative-framework-pipeline-plan.md). Most FX.8 passes' facts come
// from one file matched under one framework's own pattern language, but a
// pass occasionally needs a value that can only be computed by real
// tree-sitter extraction over a DIFFERENT language's files (or over the
// graph-so-far), joined against a lookup table built once — the "convention +
// join" a `.dl` rule can express stops at the point a construct needs
// re-parsing, not just matching. The two confirmed cases (2026-09-14,
// internal/linker/pusher_consumer.go's retired EnrichPusherConsumers and its
// EnrichPusherConsumersJS sibling) both need the same Ruby wrapper-constant
// table (`notify_* -> event`, `FOO = "bar"`, `CHANNELS[:x]`) — real AST
// extraction over an arbitrary hub class body, not a tree-sitter query.
//
// A HubProvider is a named Go function, registered once at init, that
// computes read-only facts from the graph-so-far and the service's file list
// — the same two inputs GraphFacts and the base link passes already have —
// and returns them for the caller to merge into a framework's fact set
// before its `.dl` program runs. A framework opts in with a `hub:` block in
// its pattern YAML:
//
//	hub:
//	  - name: pusher_wrapper_erb
//
// This is deliberately NOT a generic "any framework dynamically imports any
// other framework's derived relations" system — Load/Run still pair exactly
// one pattern language to one `.dl` per Framework, and a hub provider's
// output is ordinary base facts to whichever framework(s) name it, computed
// fresh per Run call. Scoped this way, `pipeline.Framework`/`Load`/`Run`
// needed no interface change: `Run` gains one more Apply* step, the same
// shape as ApplyResolves/ApplyConfig/ApplyTable.
//
// svcPath (FX.8.30, added for config_baseurl) is graph.Snapshot.ServicePath
// verbatim — the one input a hub cannot derive from any node or file path
// when it needs to walk a service's checked-in config tree itself
// (internal/configsrc.Load's own k8s/terraform subdirectory search, not a
// single file a node already points at). Empty unless the caller sets
// Snapshot.ServicePath; every hub before FX.8.30 ignores it.
type HubProvider func(nodes []graph.Node, files []string, svcPath string) []Fact

var hubRegistry = map[string]HubProvider{}

// RegisterHub adds a named hub provider. Called from each provider's own
// file's init(); a duplicate name is a programming error, not a data error.
func RegisterHub(name string, fn HubProvider) {
	if _, dup := hubRegistry[name]; dup {
		panic("factpipe: duplicate hub provider " + name)
	}
	hubRegistry[name] = fn
}

// HubSpec is one entry of a framework YAML's `hub:` list.
type HubSpec struct {
	Name string `yaml:"name"`
}

// CompiledHub is a validated HubSpec.
type CompiledHub struct{ spec HubSpec }

// Name is the registered provider this hub block invokes.
func (h CompiledHub) Name() string { return h.spec.Name }

// CompileHubSpecs validates already-decoded specs (the pipeline path) against
// the provider registry — an unregistered name is a load-time error, the same
// discipline Load already applies to a `.dl` that does not compile.
func CompileHubSpecs(specs []HubSpec) ([]CompiledHub, error) {
	out := make([]CompiledHub, 0, len(specs))
	for _, s := range specs {
		if s.Name == "" {
			return nil, fmt.Errorf("hub: missing name")
		}
		if _, ok := hubRegistry[s.Name]; !ok {
			return nil, fmt.Errorf("hub %q: no registered provider", s.Name)
		}
		out = append(out, CompiledHub{spec: s})
	}
	return out, nil
}

// ApplyHub runs every `hub:` block against dst. It is a no-op when the
// framework declares no hub block, so every framework that doesn't need this
// mechanism pays nothing for it — the same inert-by-default shape as
// ApplyConfig / ApplyTable.
func ApplyHub(hs []CompiledHub, nodes []graph.Node, files []string, svcPath string, dst FactSet) {
	for _, h := range hs {
		fn := hubRegistry[h.Name()]
		if fn == nil {
			continue
		}
		for _, f := range fn(nodes, files, svcPath) {
			dst.Add(f)
		}
	}
}
