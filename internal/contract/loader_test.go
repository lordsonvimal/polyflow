package contract_test

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

const validRuleYAML = `version: "1"
contracts:
  - kind: http
    producer:
      node: http_client
      where:
        nav_link: ""
      key: [method, path]
      key_fallbacks:
        path: [url]
      method_fallback: [GET, POST, PUT, PATCH, DELETE, ""]
      target_service_meta: target_service
    consumer:
      node: http_handler
      key: [method, path]
    normalizers: [url_to_path, query_strip, param_wildcard, trim_slash]
    match: [exact, normalized, wildcard_anchored]
    edge:
      type: http_call
      id_prefix: link
      same_service: skip_unless_meta:datastar
      via_meta:
        datastar: datastar_action
    unmatched: unknown_edge
`

func TestLoad_ValidRule(t *testing.T) {
	fsys := fstest.MapFS{
		"http.yaml": &fstest.MapFile{Data: []byte(validRuleYAML)},
	}
	rules, err := contract.Load(fsys, "")
	require.NoError(t, err)
	require.Len(t, rules, 1)

	r := rules[0]
	assert.Equal(t, contract.KindHTTP, r.Kind)
	assert.Equal(t, graph.NodeTypeHTTPClient, r.Producer.Node)
	assert.Equal(t, graph.NodeTypeHTTPHandler, r.Consumer.Node)
	assert.Equal(t, []string{"url_to_path", "query_strip", "param_wildcard", "trim_slash"}, r.Normalizers)
	assert.Equal(t, []contract.MatchTier{contract.TierExact, contract.TierNormalized, contract.TierWildcardAnchored}, r.Match)
	assert.Equal(t, contract.UnmatchedUnknownEdge, r.Unmatched)
	assert.Equal(t, "link", r.Edge.IDPrefix)
	assert.Equal(t, graph.EdgeTypeHTTPCall, r.Edge.Type)
	assert.Equal(t, "skip_unless_meta:datastar", r.Edge.SameService)
	assert.Equal(t, map[string]string{"datastar": "datastar_action"}, r.Edge.ViaMeta)
}

func TestLoad_EmptyFS(t *testing.T) {
	// No yaml files → no rules, no error
	fsys := fstest.MapFS{}
	rules, err := contract.Load(fsys, "")
	require.NoError(t, err)
	assert.Empty(t, rules)
}

func TestLoad_NilFS(t *testing.T) {
	rules, err := contract.Load(nil, "")
	require.NoError(t, err)
	assert.Empty(t, rules)
}

func TestLoad_MultipleFiles(t *testing.T) {
	fsys := fstest.MapFS{
		"http.yaml": &fstest.MapFile{Data: []byte(validRuleYAML)},
		"amqp.yaml": &fstest.MapFile{Data: []byte(`version: "1"
contracts:
  - kind: amqp
    producer:
      node: publisher
      key: [exchange, routing_key]
    consumer:
      node: subscriber
      key: [exchange, routing_key]
    normalizers: [quote_strip]
    match: [exact]
    edge:
      type: publishes
      id_prefix: amqp
      same_service: skip
    unmatched: ledger
`)},
	}
	rules, err := contract.Load(fsys, "")
	require.NoError(t, err)
	assert.Len(t, rules, 2)
}

// --- Validation errors ---

func TestLoad_UnknownNormalizer(t *testing.T) {
	fsys := fstest.MapFS{
		"bad.yaml": &fstest.MapFile{Data: []byte(`version: "1"
contracts:
  - kind: http
    producer: {node: http_client, key: [path]}
    consumer: {node: http_handler, key: [path]}
    normalizers: [not_a_real_normalizer]
    match: [exact]
    edge: {type: http_call, id_prefix: link, same_service: skip}
    unmatched: drop
`)},
	}
	_, err := contract.Load(fsys, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown normalizer")
	assert.Contains(t, err.Error(), "not_a_real_normalizer")
}

func TestLoad_UnknownMatchTier(t *testing.T) {
	fsys := fstest.MapFS{
		"bad.yaml": &fstest.MapFile{Data: []byte(`version: "1"
contracts:
  - kind: http
    producer: {node: http_client, key: [path]}
    consumer: {node: http_handler, key: [path]}
    normalizers: [trim_slash]
    match: [fuzzy_match]
    edge: {type: http_call, id_prefix: link, same_service: skip}
    unmatched: drop
`)},
	}
	_, err := contract.Load(fsys, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown match tier")
}

func TestLoad_UnknownUnmatchedPolicy(t *testing.T) {
	fsys := fstest.MapFS{
		"bad.yaml": &fstest.MapFile{Data: []byte(`version: "1"
contracts:
  - kind: http
    producer: {node: http_client, key: [path]}
    consumer: {node: http_handler, key: [path]}
    normalizers: [trim_slash]
    match: [exact]
    edge: {type: http_call, id_prefix: link, same_service: skip}
    unmatched: ignore_completely
`)},
	}
	_, err := contract.Load(fsys, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown unmatched policy")
}

func TestLoad_MissingUnmatchedPolicy(t *testing.T) {
	fsys := fstest.MapFS{
		"bad.yaml": &fstest.MapFile{Data: []byte(`version: "1"
contracts:
  - kind: http
    producer: {node: http_client, key: [path]}
    consumer: {node: http_handler, key: [path]}
    normalizers: []
    match: [exact]
    edge: {type: http_call, id_prefix: link, same_service: skip}
`)},
	}
	_, err := contract.Load(fsys, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmatched policy is required")
}

func TestLoad_InvalidYAML(t *testing.T) {
	fsys := fstest.MapFS{
		"bad.yaml": &fstest.MapFile{Data: []byte(": invalid: yaml: [")},
	}
	_, err := contract.Load(fsys, "")
	require.Error(t, err)
}

func TestLoad_NonYAMLFilesIgnored(t *testing.T) {
	fsys := fstest.MapFS{
		"http.yaml":   &fstest.MapFile{Data: []byte(validRuleYAML)},
		"README.md":   &fstest.MapFile{Data: []byte("# contracts")},
		".keep":       &fstest.MapFile{Data: []byte("")},
		"embed.go":    &fstest.MapFile{Data: []byte("package contractdata")},
	}
	rules, err := contract.Load(fsys, "")
	require.NoError(t, err)
	assert.Len(t, rules, 1, "only http.yaml should be loaded")
}

func TestLoad_WorkspaceDir_NoContractsSubdir(t *testing.T) {
	// workspaceDir without a contracts/ subdir → no error, no extra rules
	rules, err := contract.Load(nil, t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, rules)
}

func TestLoad_WorkspaceDir_LoadsCustomRules(t *testing.T) {
	// Positive: a contracts/ subdir in the workspace dir is loaded.
	dir := t.TempDir()
	contractsDir := filepath.Join(dir, "contracts")
	require.NoError(t, os.MkdirAll(contractsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(contractsDir, "custom.yaml"), []byte(validRuleYAML), 0o644))

	rules, err := contract.Load(nil, dir)
	require.NoError(t, err)
	require.Len(t, rules, 1, "custom rule from workspace dir must be loaded")
	assert.Equal(t, contract.KindHTTP, rules[0].Kind)
}

func TestLoad_WorkspaceDir_MergesWithEmbedded(t *testing.T) {
	// Workspace-custom rules are appended after embedded rules.
	dir := t.TempDir()
	contractsDir := filepath.Join(dir, "contracts")
	require.NoError(t, os.MkdirAll(contractsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(contractsDir, "custom.yaml"), []byte(validRuleYAML), 0o644))

	embedded := fstest.MapFS{
		"amqp.yaml": &fstest.MapFile{Data: []byte(`version: "1"
contracts:
  - kind: amqp
    producer:
      node: publisher
      key: [exchange, routing_key]
    consumer:
      node: subscriber
      key: [exchange, routing_key]
    normalizers: [quote_strip]
    match: [exact]
    edge:
      type: publishes
      id_prefix: amqp
      same_service: skip
    unmatched: ledger
`)},
	}
	rules, err := contract.Load(embedded, dir)
	require.NoError(t, err)
	assert.Len(t, rules, 2, "embedded + workspace custom rules must both be present")
}

func TestLoad_WorkspaceDir_FailsOnBadCustomRule(t *testing.T) {
	// Negative: a bad YAML in workspace contracts/ causes Load to fail fast.
	dir := t.TempDir()
	contractsDir := filepath.Join(dir, "contracts")
	require.NoError(t, os.MkdirAll(contractsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(contractsDir, "bad.yaml"), []byte(`version: "1"
contracts:
  - kind: http
    producer: {node: http_client, key: [path]}
    consumer: {node: http_handler, key: [path]}
    normalizers: [not_a_real_normalizer]
    match: [exact]
    edge: {type: http_call, id_prefix: link, same_service: skip}
    unmatched: drop
`), 0o644))

	_, err := contract.Load(nil, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown normalizer")
}

func TestLoad_WorkspaceDir_NonYAMLFilesIgnored(t *testing.T) {
	// Non-YAML files in the workspace contracts/ dir are skipped.
	dir := t.TempDir()
	contractsDir := filepath.Join(dir, "contracts")
	require.NoError(t, os.MkdirAll(contractsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(contractsDir, "README.md"), []byte("# custom rules"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(contractsDir, "custom.yaml"), []byte(validRuleYAML), 0o644))

	rules, err := contract.Load(nil, dir)
	require.NoError(t, err)
	assert.Len(t, rules, 1, "only YAML files are loaded; README.md must be ignored")
}

// Rules that set package/version_range must be rejected at load time: the
// engine does not enforce version gating yet, and loading such a rule
// unconditionally would silently apply it to every version.
func TestLoad_VersionGateFields_RejectedUntilEnforced(t *testing.T) {
	dir := t.TempDir()
	contractsDir := filepath.Join(dir, "contracts")
	require.NoError(t, os.MkdirAll(contractsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(contractsDir, "gated.yaml"), []byte(`version: "1"
contracts:
  - kind: http
    package: github.com/gin-gonic/gin
    version_range: ">=1.9.0"
    producer: {node: http_client, key: [path]}
    consumer: {node: http_handler, key: [path]}
    normalizers: [trim_slash]
    match: [exact]
    edge: {type: http_call, id_prefix: link, same_service: skip}
    unmatched: drop
`), 0o644))

	_, err := contract.Load(nil, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version_range")
}
