package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/lordsonvimal/polyflow/internal/meta"
)

// IgnoreFileName is the optional per-workspace ignore file. Each line is a
// doublestar glob matched against service-relative paths, same as
// index.exclude; blank lines and #-comments are skipped.
const IgnoreFileName = ".polyflowignore"

// Service describes a single service (repo or subdirectory) in the workspace.
type Service struct {
	Name       string   `yaml:"name"`
	Path       string   `yaml:"path"`
	Language   string   `yaml:"language"`
	Frameworks []string `yaml:"frameworks,omitempty"`
	Port       int      `yaml:"port,omitempty"`
}

// Link declares a known dependency between two services (HTTP or broker).
type Link struct {
	From     string `yaml:"from"`
	To       string `yaml:"to"`
	Via      string `yaml:"via,omitempty"`      // e.g. "rabbitmq"
	Hint     string `yaml:"hint,omitempty"`     // e.g. "USER_SERVICE_URL=http://localhost:8081"
	BaseURL  string `yaml:"base_url,omitempty"` // e.g. "/api" — stripped from client paths before matching
	Exchange string `yaml:"exchange,omitempty"` // via: rabbitmq — broker exchange connecting the services
}

// IndexConfig holds settings for the index command.
type IndexConfig struct {
	Exclude []string `yaml:"exclude"`
}

// RuntimeEvidenceConfig holds settings for the OTLP trace evidence provider (R.1+).
type RuntimeEvidenceConfig struct {
	// ServiceNames maps raw OTel resource.service.name values to the polyflow
	// service names declared in the workspace.  Unmapped names that also do not
	// match a workspace service directly are surfaced in the ingest ledger.
	//   chessleap-api: api
	ServiceNames map[string]string `yaml:"service_names,omitempty"`
	// SSERoutes lists route paths (e.g. "/events", "/stream") that the mapper
	// must treat as SSE connections even when the span does not carry the
	// http.response.header.content-type attribute.  Use this when the OTel
	// instrumentation does not capture response headers.
	SSERoutes []string `yaml:"sse_routes,omitempty"`
}

// EvidenceConfig holds settings for the evidence-fusion providers (F.1+).
// Fields added in later phases (trace source, toggles) will be wired then;
// parsing an unrecognised future field is rejected at load by the strict
// struct decoder to prevent silent misconfiguration.
type EvidenceConfig struct {
	// ContractGlobs are doublestar globs (relative to each service path) that
	// locate IDL/spec files.  If empty, the contract provider uses built-in
	// defaults (openapi.yaml, *.proto, *.graphql, asyncapi.yaml, …).
	ContractGlobs []string `yaml:"contract_globs,omitempty"`
	// Runtime holds OTLP trace evidence settings (R.1+).
	Runtime RuntimeEvidenceConfig `yaml:"runtime,omitempty"`
	// StaleAfter is the duration after which a runtime capture is considered
	// stale. A verified edge whose runtime sources are all older than this
	// threshold adds a stale_evidence count to the verification_summary
	// without downgrading the verification_state. Default: 30 days.
	// Accepts Go duration strings: "720h", "168h", "30m".
	StaleAfter string `yaml:"stale_after,omitempty"`
}

// DefaultStaleAfter is the default freshness threshold used when workspace
// config is absent or stale_after is not set.
const DefaultStaleAfter = 30 * 24 * time.Hour

// StaleAfterDuration returns the configured stale_after duration, defaulting
// to DefaultStaleAfter when the field is empty or unparseable.
func (e *EvidenceConfig) StaleAfterDuration() time.Duration {
	if e.StaleAfter == "" {
		return DefaultStaleAfter
	}
	d, err := time.ParseDuration(e.StaleAfter)
	if err != nil {
		return DefaultStaleAfter
	}
	return d
}

// Settings holds workspace-level defaults for the server and display.
type Settings struct {
	SnippetLines  int    `yaml:"snippet_lines"`  // default 30
	DefaultLayout string `yaml:"default_layout"` // default "dagre-lr"
	DefaultDepth  int    `yaml:"default_depth"`  // default 5
	Port          int    `yaml:"port"`           // default 9400
}

// SearchConfig holds workspace-level settings for hybrid retrieval (S.0+).
type SearchConfig struct {
	// Embedder selects the embedding backend: "static" (default, no setup),
	// "sidecar" (llama.cpp nomic-embed-text, S.3), or "endpoint" (OpenAI-
	// compatible API, S.3).  Changed Embedder triggers a full re-embed on
	// the next index run so vector spaces are never mixed.
	Embedder string `yaml:"embedder,omitempty"`
	// Synonyms maps user-visible terms to code vocabulary, expanding both
	// the FTS query and the embedding input text (S.2).
	// Example: checkout: [falcon, purchase]
	Synonyms map[string][]string `yaml:"synonyms,omitempty"`
}

// WorkspaceConfig is the parsed representation of workspace.yaml.
type WorkspaceConfig struct {
	Name     string         `yaml:"name"`
	Version  string         `yaml:"version"`
	Services []Service      `yaml:"services"`
	Links    []Link         `yaml:"links"`
	Patterns []string       `yaml:"patterns,omitempty"`
	Index    IndexConfig    `yaml:"index"`
	Settings Settings       `yaml:"settings"`
	Evidence EvidenceConfig `yaml:"evidence,omitempty"`
	Search   SearchConfig   `yaml:"search,omitempty"`
}

// DefaultExcludes returns the exclude globs written by `polyflow init`.
// They cover dependency dirs, build output, and fixture/data dirs.
//
// Test CODE (Go *_test.go, JS/TS *.test.* / *.spec.*, Ruby spec/) is
// deliberately NOT excluded: tests are real callers, and a blast radius
// that omits "which tests break if I change this" is a silent recall gap
// (this bit the chessleap eval corpus — test-file callers/importers were
// invisible). Fixture DIRS stay excluded: testdata/ and *_test/ hold data
// and intentionally-broken sources, not callers. Workspaces that want the
// old behavior add the globs back in index.exclude.
func DefaultExcludes() []string {
	return []string{
		"**/node_modules/**", "**/vendor/**", "**/dist/**",
		"**/testdata/**", "**/*_test/**", "**/tmp/**",
	}
}

// LoadIgnoreFile reads .polyflowignore from dir. A missing file is not an
// error — it returns nil. Patterns without a glob metacharacter also match
// everything beneath them (gitignore-style directory entries).
func LoadIgnoreFile(dir string) []string {
	data, err := os.ReadFile(filepath.Join(dir, IgnoreFileName))
	if err != nil {
		return nil
	}
	var patterns []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSuffix(line, "/")
		patterns = append(patterns, line)
		if !strings.ContainsAny(line, "*?[{") {
			patterns = append(patterns, line+"/**")
		}
	}
	return patterns
}

// EffectivePort returns the configured port, falling back to the default.
func (cfg *WorkspaceConfig) EffectivePort() int {
	if cfg.Settings.Port > 0 {
		return cfg.Settings.Port
	}
	return meta.DefaultPort
}

// Load reads and parses a workspace.yaml file at path.
func Load(path string) (*WorkspaceConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read workspace config %s: %w", path, err)
	}
	var cfg WorkspaceConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse workspace config: %w", err)
	}
	return &cfg, nil
}

// Save writes the config back to path atomically.
func Save(path string, cfg *WorkspaceConfig) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal workspace config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write workspace config tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename workspace config: %w", err)
	}
	return nil
}
