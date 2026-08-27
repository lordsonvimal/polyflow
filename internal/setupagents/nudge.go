package setupagents

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// nudgeMarkerStart/End delimit polyflow's own block inside a shared,
// user-owned instructions file (CLAUDE.md, AGENTS.md, ...). Every write here
// is scoped to the text between these markers so setup can add, update, or
// remove polyflow's nudge without touching anything else the user has
// written in the file, and re-running setup replaces the block in place
// instead of appending a second copy.
const (
	nudgeMarkerStart = "<!-- polyflow:nudge:start -->"
	nudgeMarkerEnd   = "<!-- polyflow:nudge:end -->"
)

// nudgeBody is the guidance text polyflow installs. Content, not just
// presence, is load-bearing here: an E.2 agent-bench run (eval/agent-bench)
// found that without this explicit steer, Claude Code's default instinct
// for "trace this across files" is to self-delegate to a grep/Explore
// subagent before it even evaluates which MCP tools are registered — the
// MCP tool descriptions alone did not change that behavior in 22 of 23
// measured sessions.
const nudgeBody = `## Tool preferences

- For call-chain, impact, blast-radius, or cross-file/cross-service relationship questions (e.g. "what calls this", "what breaks if I change this", "trace this request across services"), query polyflow first — before grepping or spawning an Explore subagent. Polyflow answers graph-shaped questions that grep can't; grep/Explore remain fine for known-location lookups and simple string searches.
- If the user's question bundles more than one ask (e.g. "trace flow X and tell me the impacts"), split it into one polyflow call per ask instead of pasting the whole sentence into a single query — a compound query dilutes the match. Strip conversational framing too; pass the core entity/feature name, not the full question.
- If a polyflow call returns empty or low-confidence results, don't fall back to grep/filesystem search yet — call resolve with the same term first to see ranked candidates (it often reveals the query just needs a different node type or service scope), then retry with that.`

func nudgeBlock() string {
	return nudgeMarkerStart + "\n" + nudgeBody + "\n" + nudgeMarkerEnd
}

// nudgeAction reports what mergeNudge had to do, so callers can report an
// accurate result line (created/updated/added/unchanged) without re-deriving
// it from before/after content.
type nudgeAction int

const (
	nudgeUnchanged nudgeAction = iota
	nudgeAdded
	nudgeUpdated
)

// mergeNudge inserts or updates polyflow's marked block inside content,
// leaving everything else untouched. If the block is already present with
// identical text, it's left alone (nudgeUnchanged) so repeated setup runs
// never append a second copy or grow the file. If the block is present but
// stale (nudgeBody changed since it was written), it's replaced in place.
// Note this means hand-edits made *inside* the markers are overwritten on
// the next setup run — the block is treated as machine-owned, the same
// convention the JSON hook merges already use for their entries.
func mergeNudge(content string) (string, nudgeAction) {
	block := nudgeBlock()
	start := strings.Index(content, nudgeMarkerStart)
	end := strings.Index(content, nudgeMarkerEnd)
	if start != -1 && end != -1 && end > start {
		existing := content[start : end+len(nudgeMarkerEnd)]
		if existing == block {
			return content, nudgeUnchanged
		}
		return content[:start] + block + content[end+len(nudgeMarkerEnd):], nudgeUpdated
	}

	trimmed := strings.TrimRight(content, "\n")
	if trimmed == "" {
		return block + "\n", nudgeAdded
	}
	return trimmed + "\n\n" + block + "\n", nudgeAdded
}

// stripNudge removes polyflow's marked block from content, along with the
// blank-line separator mergeNudge added around it, leaving everything else
// untouched. Returns the new content and whether the block was present.
func stripNudge(content string) (string, bool) {
	start := strings.Index(content, nudgeMarkerStart)
	end := strings.Index(content, nudgeMarkerEnd)
	if start == -1 || end == -1 || end < start {
		return content, false
	}
	end += len(nudgeMarkerEnd)

	head := strings.TrimRight(content[:start], "\n")
	tail := strings.TrimLeft(content[end:], "\n")

	switch {
	case head == "" && tail == "":
		return "", true
	case head == "":
		return tail, true
	case tail == "":
		return head + "\n", true
	default:
		return head + "\n\n" + tail, true
	}
}

// SetupNudge adds or updates polyflow's block in the agent's instructions
// file for scope, creating the file (and its parent directory) if it
// doesn't exist yet. Only call this when agent.SupportsNudge() is true.
func SetupNudge(agent Agent, scope string) (string, error) {
	path, err := agent.NudgeFile(scope)
	if err != nil {
		return "", err
	}
	data, rerr := os.ReadFile(path)
	existed := rerr == nil
	if rerr != nil && !os.IsNotExist(rerr) {
		return "", fmt.Errorf("read %s: %w", path, rerr)
	}

	newContent, action := mergeNudge(string(data))
	if action == nudgeUnchanged {
		return fmt.Sprintf("Tool-preference nudge already present in %s.", path), nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(newContent), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}

	switch {
	case !existed:
		return fmt.Sprintf("Created %s with polyflow's tool-preference nudge.", path), nil
	case action == nudgeUpdated:
		return fmt.Sprintf("Updated polyflow's tool-preference nudge in %s.", path), nil
	default:
		return fmt.Sprintf("Added polyflow's tool-preference nudge to %s.", path), nil
	}
}

// RemoveNudge strips polyflow's block from the agent's instructions file for
// scope. A missing file, or a file without the block, is a no-op, not an
// error. The file itself is left in place even if the block was its only
// content — removal only ever touches the marked region.
func RemoveNudge(agent Agent, scope string) (string, error) {
	path, err := agent.NudgeFile(scope)
	if err != nil {
		return "", err
	}
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return fmt.Sprintf("No %s file present — nothing to remove.", path), nil
		}
		return "", fmt.Errorf("read %s: %w", path, rerr)
	}

	newContent, removed := stripNudge(string(data))
	if !removed {
		return fmt.Sprintf("No polyflow nudge found in %s.", path), nil
	}
	if err := os.WriteFile(path, []byte(newContent), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return fmt.Sprintf("Removed polyflow's tool-preference nudge from %s.", path), nil
}

// NudgeStatus reports whether polyflow's block is present in the agent's
// instructions file for scope.
func NudgeStatus(agent Agent, scope string) (bool, error) {
	path, err := agent.NudgeFile(scope)
	if err != nil {
		return false, err
	}
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", path, rerr)
	}
	return strings.Contains(string(data), nudgeMarkerStart), nil
}
