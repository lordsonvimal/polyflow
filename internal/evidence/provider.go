// Package evidence implements the F.0 evidence-fusion substrate: the Provider
// interface, the StaticProvider wrapper, and the Reconciler that merges
// multiple evidence sources into a single provenance-tracked graph.
//
// Five provider names are pinned; any other name is a registration error
// (bug-class rule 3: a parsed-but-unenforced field must fail loudly).
package evidence

import (
	"context"
	"fmt"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// validProviderNames is the exhaustive set of accepted Provider.Name() values.
// A provider returning any other name fails at registration time, not silently.
var validProviderNames = map[string]bool{
	"static":   true,
	"contract": true,
	"runtime":  true,
	"config":   true,
	"llm":      true,
}

// ValidateProviderName returns an error when name is not in the pinned set.
func ValidateProviderName(name string) error {
	if !validProviderNames[name] {
		return fmt.Errorf("evidence: unknown provider name %q; must be one of: static, contract, runtime, config, llm", name)
	}
	return nil
}

// Provider is the evidence-source interface.  Each implementation collects
// edges and nodes normalized to contract-engine channel keys so fusion is a
// key-join, not per-source glue.
//
// A provider with nothing to say returns empty Evidence, not an error
// (graceful degradation is the contract — a repo without contracts or traces
// should degrade to static-only, not fail).
type Provider interface {
	Name() string // one of the five pinned values
	Collect(ctx context.Context, ws *workspace.WorkspaceConfig) (Evidence, error)
}

// Evidence is the output of one Provider.Collect call.
type Evidence struct {
	// Nodes may include synthetic service-level endpoint nodes for non-static
	// providers (observed_only_gap minting).
	Nodes []graph.Node
	// Edges must have Sources[] populated by the provider; each SourceRef
	// carries Provider = p.Name().
	Edges []graph.Edge
	// Unresolved is the provider-specific ledger of references it could not
	// resolve.
	Unresolved []graph.UnresolvedRef
}
