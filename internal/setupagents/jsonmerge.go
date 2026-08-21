package setupagents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// readJSONDoc reads path as a JSON object, returning an empty object and
// existed=false if the file doesn't exist yet — merges into a fresh config
// exactly like merges into an existing one.
func readJSONDoc(path string) (doc map[string]any, existed bool, err error) {
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return map[string]any{}, false, nil
		}
		return nil, false, fmt.Errorf("read %s: %w", path, rerr)
	}
	doc = map[string]any{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, true, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	return doc, true, nil
}

func writeJSONDoc(path string, doc map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	out = append(out, '\n')
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// mcpServerConfigured reports whether a top-level "mcpServers" entry named
// name already exists in the JSON document at path. A missing file is not
// configured, not an error — matches readJSONDoc's own "no file yet" case.
func mcpServerConfigured(path, name string) (bool, error) {
	doc, existed, err := readJSONDoc(path)
	if err != nil {
		return false, err
	}
	if !existed {
		return false, nil
	}
	servers, _ := doc["mcpServers"].(map[string]any)
	_, ok := servers[name]
	return ok, nil
}

// mergeMCPServers merges a single `name: {command, args}` entry into the doc's
// top-level "mcpServers" object — the shape shared by Claude Code's .mcp.json,
// Cursor's mcp.json, and most other MCP-aware agents. Idempotent: re-running
// with the same command/args overwrites the same entry rather than duplicating it.
func mergeMCPServers(doc map[string]any, name, command string, args []string) {
	servers, _ := doc["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	servers[name] = map[string]any{
		"command": command,
		"args":    args,
	}
	doc["mcpServers"] = servers
}
