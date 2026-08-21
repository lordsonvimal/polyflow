// Package setupagents defines the per-agent config profiles `polyflow setup`
// dispatches to. Each Agent knows how to register polyflow's MCP server and
// (if the agent has the concept) a post-tool-use context hook, for a given
// scope. Adding support for a new coding agent means adding one file that
// implements Agent and registers itself in the package init — the wizard in
// cmd/polyflow/setup.go never changes.
package setupagents

// Agent is a coding agent polyflow can configure itself for.
type Agent interface {
	// Name is the short id used in `polyflow setup --agent <name>`.
	Name() string
	// DisplayName is the human-readable name shown in the picker.
	DisplayName() string
	// Description is a one-line summary of what's supported, shown in the picker.
	Description() string
	// SupportsHooks reports whether this agent has a post-tool-use hook
	// mechanism at all. When false, SetupHooks is never called.
	SupportsHooks() bool
	// SupportsGlobalScope reports whether this agent has a genuine
	// system-wide (every OS user on this machine) config scope. None of the
	// agents implemented so far do, so "global" always falls back to "user".
	SupportsGlobalScope() bool
	// SetupMCP registers the polyflow MCP server for the given scope
	// ("repo" or "user"; "global" is resolved to one of these by the caller
	// before this is invoked) and returns a human-readable result line.
	SetupMCP(scope, polyflowBin string) (string, error)
	// SetupHooks wires the context-injection hook for the given scope.
	// Only called when SupportsHooks() is true.
	SetupHooks(scope, polyflowBin string) (string, error)
	// MCPStatus reports whether polyflow's MCP server already appears to be
	// registered for the given scope, without writing anything. Used by both
	// the CLI picker and the web setup wizard to show current state before
	// the user chooses to (re-)run setup, so neither surface goes stale
	// relative to config changes made by the other.
	MCPStatus(scope string) (bool, error)
	// HooksStatus reports whether the context-injection hook already appears
	// to be wired for the given scope. Only called when SupportsHooks() is
	// true; agents without a hook mechanism return (false, error) matching
	// SetupHooks' own convention.
	HooksStatus(scope string) (bool, error)
}

var registry []Agent

func register(a Agent) { registry = append(registry, a) }

// All returns every registered agent, in registration order (the order
// they're offered in the picker).
func All() []Agent { return registry }

// Names returns every registered agent's short id.
func Names() []string {
	names := make([]string, len(registry))
	for i, a := range registry {
		names[i] = a.Name()
	}
	return names
}

// Get looks up a registered agent by its short id.
func Get(name string) (Agent, bool) {
	for _, a := range registry {
		if a.Name() == name {
			return a, true
		}
	}
	return nil, false
}
