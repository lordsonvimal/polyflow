package pluginloader

import (
	"context"
	"fmt"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/lordsonvimal/polyflow/sdk/linkplugin"
)

// toWireVerbNode converts internal/patterns' plain VerbNode (built inside
// runVerb from a live *sitter.Node) to the SDK's wire-shaped equivalent —
// the one place this package's verb path crosses from "patterns' own data"
// to "the RPC boundary", matching internal/pluginloader/link.go's existing
// toSDKNodes/fromSDKResult convention for LinkContext.
func toWireVerbNode(n patterns.VerbNode) linkplugin.VerbNode {
	ancestors := make([]linkplugin.VerbNode, 0, len(n.Ancestors))
	for _, a := range n.Ancestors {
		ancestors = append(ancestors, linkplugin.VerbNode{
			Type: a.Type, Text: a.Text, StartLine: a.StartLine, EndLine: a.EndLine,
		})
	}
	return linkplugin.VerbNode{
		Type: n.Type, Text: n.Text, StartLine: n.StartLine, EndLine: n.EndLine,
		Ancestors: ancestors,
	}
}

// Verbs queries a launched plugin's advertised extract: verb names.
func (l *LaunchedPlugin) Verbs(ctx context.Context) ([]string, error) {
	return l.Client.Verbs(ctx)
}

// RegisterVerbs is FX.9's load-time wiring
// (docs/declarative-framework-pipeline-plan.md): for every name l's plugin
// advertises via Verbs(), apply the generic-only check against knownNames
// (a framework-name blocklist — see genericOnlyCheck) and, if accepted,
// register a closure with internal/patterns that performs the ExtractVerb
// RPC round-trip on every call. A rejected name is skipped with a
// CoverageNote, never a hard failure — one plugin's naming mistake must not
// take down an index run. This is a best-effort automatic check (a substring
// match against known framework names), not exhaustive review; FX.0's actual
// review discipline for in-tree primitives still applies to any new plugin
// author manually.
func (l *LaunchedPlugin) RegisterVerbs(ctx context.Context, knownNames []string) ([]CoverageNote, error) {
	names, err := l.Verbs(ctx)
	if err != nil {
		return nil, fmt.Errorf("pluginloader: %s Verbs(): %w", l.Name, err)
	}
	var notes []CoverageNote
	for _, name := range names {
		if bad, hit := genericOnlyCheck(name, knownNames); bad {
			notes = append(notes, CoverageNote{
				Plugin: l.Name,
				Reason: fmt.Sprintf("verb %q rejected: name contains framework name %q (FX.9 generic-only rule)", name, hit),
			})
			continue
		}
		l.registerVerb(name)
	}
	return notes, nil
}

// registerVerb wires one accepted verb name into internal/patterns'
// dispatch. The closure runs inside runVerb (extract.go), which already has
// a live *sitter.Node — it hands this package a plain patterns.VerbNode
// (already ancestor-walked, no proto/SDK type involved on that side) and
// this closure is what does the RPC round-trip.
func (l *LaunchedPlugin) registerVerb(name string) {
	patterns.RegisterVerbProvider(name, func(node patterns.VerbNode, arg, file, grammar string) ([]patterns.VerbResult, bool) {
		wireNode := toWireVerbNode(node)
		vals, ok, err := l.Client.ExtractVerb(context.Background(), name, arg, wireNode, file, grammar)
		if err != nil || !ok {
			return nil, false
		}
		out := make([]patterns.VerbResult, 0, len(vals))
		for _, v := range vals {
			out = append(out, patterns.VerbResult{Str: v.Str, Int: v.Int, IsInt: v.IsInt})
		}
		return out, true
	})
}

// genericOnlyCheck reports whether verb (case-insensitive) contains any of
// knownNames as a substring — FX.9's "a verb named or documented after a
// framework is rejected at registration" rule, applied to the name only
// (a plugin's doc comments live in its own source, outside core's reach).
func genericOnlyCheck(verb string, knownNames []string) (bad bool, hit string) {
	v := strings.ToLower(verb)
	for _, n := range knownNames {
		if n == "" {
			continue
		}
		if strings.Contains(v, strings.ToLower(n)) {
			return true, n
		}
	}
	return false, ""
}
