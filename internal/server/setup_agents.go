package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/lordsonvimal/polyflow/internal/setupagents"
)

// setupAgentInfo is the JSON shape for one entry in GET /api/setup/agents —
// the web wizard's counterpart to `polyflow setup`'s interactive agent
// picker (cmd/polyflow/setup.go's promptAgent). mcp_configured/
// hooks_configured are live filesystem checks, not cached state, so the
// picker reflects reality whether the last setup run was `polyflow setup`
// or this same endpoint — both write the same files.
type setupAgentInfo struct {
	Name                string `json:"name"`
	DisplayName         string `json:"display_name"`
	Description         string `json:"description"`
	SupportsHooks       bool   `json:"supports_hooks"`
	SupportsGlobalScope bool   `json:"supports_global_scope"`
	MCPConfigured       bool   `json:"mcp_configured"`
	MCPStatusError      string `json:"mcp_status_error,omitempty"`
	HooksConfigured     bool   `json:"hooks_configured"`
	HooksStatusError    string `json:"hooks_status_error,omitempty"`
	SupportsNudge       bool   `json:"supports_nudge"`
	NudgeConfigured     bool   `json:"nudge_configured"`
	NudgeStatusError    string `json:"nudge_status_error,omitempty"`
}

// handleSetupAgents handles GET /api/setup/agents?scope=repo|user|global —
// lists every registered coding-agent profile plus its current
// registration status for the requested scope.
func (s *Server) handleSetupAgents(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "repo"
	}
	if !isValidSetupScope(scope) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid scope %q: must be repo, user, or global", scope))
		return
	}

	agents := setupagents.All()
	out := make([]setupAgentInfo, 0, len(agents))
	for _, a := range agents {
		effectiveScope := scope
		if scope == "global" && !a.SupportsGlobalScope() {
			effectiveScope = "user"
		}

		info := setupAgentInfo{
			Name:                a.Name(),
			DisplayName:         a.DisplayName(),
			Description:         a.Description(),
			SupportsHooks:       a.SupportsHooks(),
			SupportsGlobalScope: a.SupportsGlobalScope(),
		}

		if configured, err := a.MCPStatus(effectiveScope); err != nil {
			info.MCPStatusError = err.Error()
		} else {
			info.MCPConfigured = configured
		}

		if a.SupportsHooks() {
			if configured, err := a.HooksStatus(effectiveScope); err != nil {
				info.HooksStatusError = err.Error()
			} else {
				info.HooksConfigured = configured
			}
		}

		info.SupportsNudge = a.SupportsNudge()
		if a.SupportsNudge() {
			if configured, err := setupagents.NudgeStatus(a, effectiveScope); err != nil {
				info.NudgeStatusError = err.Error()
			} else {
				info.NudgeConfigured = configured
			}
		}

		out = append(out, info)
	}

	writeJSON(w, http.StatusOK, map[string]any{"scope": scope, "agents": out})
}

// setupAgentApplyRequest is the POST /api/setup/agent body.
type setupAgentApplyRequest struct {
	Agent string `json:"agent"`
	Scope string `json:"scope"`
}

// setupAgentApplyResult mirrors what `polyflow setup`'s CLI wizard prints —
// same two-line MCP/hooks result shape, same "no hook mechanism" skip note.
type setupAgentApplyResult struct {
	MCPResult    string `json:"mcp_result"`
	HooksResult  string `json:"hooks_result,omitempty"`
	HooksSkipped string `json:"hooks_skipped,omitempty"`
	NudgeResult  string `json:"nudge_result,omitempty"`
	NudgeSkipped string `json:"nudge_skipped,omitempty"`
}

// decodeSetupAgentRequest parses and validates the shared {agent, scope}
// body POST /api/setup/agent and DELETE /api/setup/agent both take,
// resolving "global" to "user" for agents without a real global scope the
// same way handleSetupAgents and the CLI's runSetup already do.
func decodeSetupAgentRequest(w http.ResponseWriter, r *http.Request) (setupagents.Agent, string, bool) {
	var req setupAgentApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return nil, "", false
	}
	if !isValidSetupScope(req.Scope) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid scope %q: must be repo, user, or global", req.Scope))
		return nil, "", false
	}
	agent, ok := setupagents.Get(req.Agent)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("unknown agent %q", req.Agent))
		return nil, "", false
	}

	scope := req.Scope
	if scope == "global" && !agent.SupportsGlobalScope() {
		scope = "user"
	}
	return agent, scope, true
}

// handleSetupAgentApply handles POST /api/setup/agent — the web wizard's
// counterpart to running `polyflow setup --agent <name> --scope <scope>`.
// Runs the exact same setupagents.Agent methods the CLI calls
// (cmd/polyflow/setup.go's runSetupAdd), so both surfaces write identical
// config files.
func (s *Server) handleSetupAgentApply(w http.ResponseWriter, r *http.Request) {
	agent, scope, ok := decodeSetupAgentRequest(w, r)
	if !ok {
		return
	}

	polyflowBin, err := os.Executable()
	if err != nil {
		polyflowBin = "polyflow"
	}

	mcpResult, err := agent.SetupMCP(scope, polyflowBin)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "mcp setup: "+err.Error())
		return
	}
	result := setupAgentApplyResult{MCPResult: mcpResult}

	if agent.SupportsHooks() {
		hooksResult, err := agent.SetupHooks(scope, polyflowBin)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "hook setup: "+err.Error())
			return
		}
		result.HooksResult = hooksResult
	} else {
		result.HooksSkipped = agent.DisplayName() + " has no post-tool-use hook mechanism"
	}

	if agent.SupportsNudge() {
		nudgeResult, err := setupagents.SetupNudge(agent, scope)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "nudge setup: "+err.Error())
			return
		}
		result.NudgeResult = nudgeResult
	} else {
		result.NudgeSkipped = agent.DisplayName() + " has no persistent instructions file polyflow knows how to steer yet"
	}

	writeJSON(w, http.StatusOK, result)
}

// setupAgentRemoveResult mirrors `polyflow setup --remove`'s CLI output.
type setupAgentRemoveResult struct {
	MCPResult   string `json:"mcp_result"`
	HooksResult string `json:"hooks_result,omitempty"`
	NudgeResult string `json:"nudge_result,omitempty"`
}

// handleSetupAgentRemove handles DELETE /api/setup/agent — the web wizard's
// counterpart to `polyflow setup --agent <name> --scope <scope> --remove`.
// Runs the exact same setupagents.Agent Remove* methods the CLI calls
// (cmd/polyflow/setup.go's runSetupRemove): unregisters the MCP server,
// unwires just polyflow's hook entries, and removes the nudge block, each
// step a no-op rather than an error when there's nothing to remove.
func (s *Server) handleSetupAgentRemove(w http.ResponseWriter, r *http.Request) {
	agent, scope, ok := decodeSetupAgentRequest(w, r)
	if !ok {
		return
	}

	mcpResult, err := agent.RemoveMCP(scope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "mcp removal: "+err.Error())
		return
	}
	result := setupAgentRemoveResult{MCPResult: mcpResult}

	if agent.SupportsHooks() {
		hooksResult, err := agent.RemoveHooks(scope)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "hook removal: "+err.Error())
			return
		}
		result.HooksResult = hooksResult
	}

	if agent.SupportsNudge() {
		nudgeResult, err := setupagents.RemoveNudge(agent, scope)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "nudge removal: "+err.Error())
			return
		}
		result.NudgeResult = nudgeResult
	}

	writeJSON(w, http.StatusOK, result)
}

func isValidSetupScope(s string) bool {
	return s == "repo" || s == "user" || s == "global"
}
