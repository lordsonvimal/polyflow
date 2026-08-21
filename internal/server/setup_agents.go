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
}

// handleSetupAgentApply handles POST /api/setup/agent — the web wizard's
// counterpart to running `polyflow setup --agent <name> --scope <scope>`.
// Runs the exact same setupagents.Agent methods the CLI calls
// (cmd/polyflow/setup.go's runSetup), so both surfaces write identical
// config files.
func (s *Server) handleSetupAgentApply(w http.ResponseWriter, r *http.Request) {
	var req setupAgentApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if !isValidSetupScope(req.Scope) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid scope %q: must be repo, user, or global", req.Scope))
		return
	}
	agent, ok := setupagents.Get(req.Agent)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("unknown agent %q", req.Agent))
		return
	}

	scope := req.Scope
	if scope == "global" && !agent.SupportsGlobalScope() {
		scope = "user"
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

	writeJSON(w, http.StatusOK, result)
}

func isValidSetupScope(s string) bool {
	return s == "repo" || s == "user" || s == "global"
}
