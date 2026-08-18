package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// customPatternsDir is where POST /api/patterns writes uploaded pattern
// files, relative to the workspace config's own directory (mirroring how
// workspace.Load resolves service paths).
const customPatternsDir = ".polyflow/patterns"

// patternInfo is one row of GET /api/patterns — the UI-facing shape of a
// registered *patterns.Pattern, with its file-level version/package gate
// and source path attached (patterns.Registry drops the source path once
// loaded, so this is assembled straight from patterns.PatternFileInfo).
type patternInfo struct {
	Name         string   `json:"name"`
	Language     string   `json:"language"`
	NodeType     string   `json:"node_type,omitempty"`
	EdgeType     string   `json:"edge_type,omitempty"`
	Roles        []string `json:"roles,omitempty"`
	Package      string   `json:"package,omitempty"`
	VersionRange string   `json:"version_range,omitempty"`
	Source       string   `json:"source"`
	Custom       bool     `json:"custom"`
	Grammars     []string `json:"grammars,omitempty"`
}

// rolesOf collects the distinct capture roles a pattern references — from
// its extract.attributes captures and its legacy top-level captures list.
func rolesOf(p patterns.Pattern) []string {
	seen := map[string]bool{}
	var roles []string
	add := func(r string) {
		if r == "" || seen[r] {
			return
		}
		seen[r] = true
		roles = append(roles, r)
	}
	for _, c := range p.Captures {
		add(c.Role)
	}
	for k := range p.Match {
		add(strings.TrimPrefix(k, "@"))
	}
	return roles
}

func patternInfosFromFile(fi patterns.PatternFileInfo, custom bool) []patternInfo {
	out := make([]patternInfo, 0, len(fi.File.Patterns))
	for _, p := range fi.File.Patterns {
		out = append(out, patternInfo{
			Name:         p.Name,
			Language:     fi.File.Language,
			NodeType:     p.Extract.NodeType,
			EdgeType:     p.Extract.EdgeType,
			Roles:        rolesOf(p),
			Package:      fi.File.Package,
			VersionRange: fi.File.VersionRange,
			Source:       fi.Path,
			Custom:       custom,
			Grammars:     fi.File.Grammars,
		})
	}
	return out
}

// handleListPatterns handles GET /api/patterns?language=&q= — the UO.7
// patterns viewer, wrapping the same `patterns list` internals (embedded
// registry) plus every custom pattern file registered in polyflow.yml's
// `patterns:` list.
func (s *Server) handleListPatterns(w http.ResponseWriter, r *http.Request) {
	embedded, err := patterns.EmbeddedFilesWithPaths()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load embedded patterns: "+err.Error())
		return
	}

	var all []patternInfo
	for _, fi := range embedded {
		all = append(all, patternInfosFromFile(fi, false)...)
	}

	if cfg, err := workspace.Load(s.configPathOrDefault()); err == nil {
		for _, p := range cfg.Patterns {
			fi, err := patterns.LoadFile(p)
			if err != nil {
				continue // a broken custom pattern file shouldn't break the whole list
			}
			all = append(all, patternInfosFromFile(patterns.PatternFileInfo{Path: p, File: fi}, true)...)
		}
	}

	lang := r.URL.Query().Get("language")
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	filtered := make([]patternInfo, 0, len(all))
	for _, p := range all {
		if lang != "" && p.Language != lang {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(p.Name), q) && !strings.Contains(strings.ToLower(p.Language), q) {
			continue
		}
		filtered = append(filtered, p)
	}

	writeJSON(w, http.StatusOK, map[string]any{"patterns": filtered})
}

type addPatternBody struct {
	Name    string `json:"name"`    // file name, e.g. "my_pattern.yaml"
	Content string `json:"content"` // raw YAML text
}

// handleAddPattern handles POST /api/patterns body {"name": "...", "content": "<yaml>"}.
// It validates exactly like the CLI's `patterns add` (patterns.LoadFile,
// errors returned verbatim), then writes the file under the workspace's
// custom patterns dir and registers its path in polyflow.yml via the same
// workspace.Load/Save internals runPatternsAdd uses.
func (s *Server) handleAddPattern(w http.ResponseWriter, r *http.Request) {
	var body addPatternBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "name is required", "field": "name"})
		return
	}
	if filepath.Base(name) != name {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "name must be a bare filename", "field": "name"})
		return
	}
	if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
		name += ".yaml"
	}

	configPath := s.configPathOrDefault()
	dir := filepath.Join(filepath.Dir(configPath), customPatternsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, "create patterns dir: "+err.Error())
		return
	}
	destPath, err := filepath.Abs(filepath.Join(dir, name))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "resolve pattern path: "+err.Error())
		return
	}

	// Validate against a temp file first so an invalid upload never touches
	// the workspace patterns dir or polyflow.yml (same shape as the config
	// PUT handler's temp-file validation trick).
	tmp, err := os.CreateTemp(dir, ".polyflow-pattern-validate-*.yaml")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create validation temp file: "+err.Error())
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(body.Content); err != nil {
		tmp.Close()
		writeError(w, http.StatusInternalServerError, "write validation temp file: "+err.Error())
		return
	}
	if err := tmp.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "close validation temp file: "+err.Error())
		return
	}
	pf, err := patterns.LoadFile(tmpPath)
	if err != nil {
		// Mirrors runPatternsAdd's wrapped error text exactly.
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid pattern file: " + err.Error()})
		return
	}

	if err := os.WriteFile(destPath, []byte(body.Content), 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, "write pattern file: "+err.Error())
		return
	}

	cfg, err := workspace.Load(configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load workspace config: "+err.Error())
		return
	}
	// Stored as an absolute path — cfg.Patterns entries are resolved
	// relative to the process's working directory everywhere they're read
	// (indexer.go, runPatternsList), same as a CLI `patterns add <file>`
	// argument; an absolute path sidesteps that entirely and keeps working
	// regardless of which directory a future `polyflow index`/server
	// process runs from.
	cfg.Patterns = append(cfg.Patterns, destPath)
	if err := workspace.Save(configPath, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, "save workspace config: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"path": destPath,
		"pattern_file": map[string]any{
			"language": pf.Language,
			"patterns": len(pf.Patterns),
		},
	})
}
