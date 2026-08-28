package indexer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildFixtureTree creates the worked-example fixture:
//   main.go      → parseable
//   deploy.sql    → unparsed blind spot (.sql)
//   Dockerfile   → unparsed blind spot (extensionless)
//   logo.png     → allowlisted asset (absent from output)
func buildFixtureTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "deploy.sql"), []byte("#!/bin/sh"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "logo.png"), []byte("\x89PNG"), 0o644))
	return dir
}

// TestWalkService_UnparsedCounts verifies that walkService counts blind-spot
// files correctly: .sql and Dockerfile appear; logo.png is allowlisted.
func TestWalkService_UnparsedCounts(t *testing.T) {
	t.Parallel()
	dir := buildFixtureTree(t)
	_, unparsed, err := walkService(dir, nil)
	require.NoError(t, err)

	assert.Equal(t, 1, unparsed[".sql"], ".sql count")
	assert.Equal(t, 1, unparsed["Dockerfile"], "Dockerfile count (extensionless uses basename)")
	assert.Equal(t, 2, len(unparsed), "only two non-asset blind spots")
}

// TestWalkService_AllowlistExcludes verifies that allowlisted asset extensions
// do not appear in the unparsed output even when not parseable.
func TestWalkService_AllowlistExcludes(t *testing.T) {
	t.Parallel()
	dir := buildFixtureTree(t)
	_, unparsed, err := walkService(dir, nil)
	require.NoError(t, err)

	_, hasPNG := unparsed[".png"]
	assert.False(t, hasPNG, ".png is an allowlisted asset — must not appear in blind-spot counts")
}

// TestWalkService_CleanService verifies that a service with only parseable
// files returns an empty unparsed map ({}-when-clean).
func TestWalkService_CleanService(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app.js"), []byte("const x = 1"), 0o644))

	_, unparsed, err := walkService(dir, nil)
	require.NoError(t, err)

	assert.Empty(t, unparsed, "no unparsed blind spots on an all-parseable service")
}

// TestSerializeUnparsed_Determinism verifies that serializeUnparsed produces
// byte-identical output on two calls with the same input (bug-class rule 2).
func TestSerializeUnparsed_Determinism(t *testing.T) {
	t.Parallel()
	counts := map[string]map[string]int{
		"api": {".sql": 3, "Dockerfile": 1},
		"web": {".vue": 12, ".svelte": 4},
	}
	first := serializeUnparsed(counts)
	second := serializeUnparsed(counts)
	assert.Equal(t, first, second, "serializeUnparsed must be byte-identical across calls")

	// Verify the JSON is sorted by service name then extension.
	// encoding/json marshals map keys in alphabetical order.
	var parsed map[string]map[string]int
	require.NoError(t, json.Unmarshal([]byte(first), &parsed))
	assert.Equal(t, 3, parsed["api"][".sql"])
	assert.Equal(t, 1, parsed["api"]["Dockerfile"])
	assert.Equal(t, 12, parsed["web"][".vue"])
}

// TestSerializeUnparsed_EmptyIsClean verifies that an empty counts map
// serializes to "{}" (the key is always written, absence ≠ certainty).
func TestSerializeUnparsed_EmptyIsClean(t *testing.T) {
	t.Parallel()
	got := serializeUnparsed(map[string]map[string]int{})
	assert.Equal(t, "{}", got, "empty unparsed map must serialize to {}")
}

// TestUnparsedSummary_TopThree verifies that UnparsedSummary returns total
// count and the top-3 extensions in alphabetical order (only first 3 of 4).
func TestUnparsedSummary_TopThree(t *testing.T) {
	t.Parallel()
	exts := map[string]int{
		".vue":       12,
		".sql":        3,
		"Dockerfile": 1,
		".svelte":    7,
	}
	total, parts := UnparsedSummary(exts)
	assert.Equal(t, 23, total, "total count")
	// sort.Strings: '.' (ASCII 46) < 'D' (ASCII 68), so dot-extensions sort first.
	// Sorted: .sql, .svelte, .vue, Dockerfile → top-3: .sql, .svelte, .vue
	assert.Contains(t, parts, ".sql", "top-3 should include .sql")
	assert.Contains(t, parts, ".svelte", "top-3 should include .svelte")
	assert.Contains(t, parts, ".vue", "top-3 should include .vue")
	assert.NotContains(t, parts, "Dockerfile", "Dockerfile is 4th alphabetically, not in top-3")
}

// TestUnparsedSummary_FewExtensions verifies that fewer than 3 extensions
// works correctly.
func TestUnparsedSummary_FewExtensions(t *testing.T) {
	t.Parallel()
	exts := map[string]int{".sql": 2}
	total, parts := UnparsedSummary(exts)
	assert.Equal(t, 2, total)
	assert.Equal(t, ".sql ×2", parts)
}

// TestWalkService_TwoRunDeterminism verifies that two consecutive walkService
// calls on the same tree produce byte-identical unparsed JSON (rule 2).
func TestWalkService_TwoRunDeterminism(t *testing.T) {
	t.Parallel()
	dir := buildFixtureTree(t)
	// Add more files to exercise ordering.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "deploy2.sql"), []byte("#!/bin/sh"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Makefile"), []byte("all:"), 0o644))

	_, unparsed1, err1 := walkService(dir, nil)
	require.NoError(t, err1)
	_, unparsed2, err2 := walkService(dir, nil)
	require.NoError(t, err2)

	json1 := serializeUnparsed(map[string]map[string]int{"svc": unparsed1})
	json2 := serializeUnparsed(map[string]map[string]int{"svc": unparsed2})
	assert.Equal(t, json1, json2, "two runs on the same tree must produce byte-identical JSON")
}

// TestWalkService_ParseableFilesNotCounted verifies that parseable files
// (.go, .js, .rb, etc.) are not counted in the unparsed map.
func TestWalkService_ParseableFilesNotCounted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app.js"), []byte("const x = 1"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app.rb"), []byte("puts 'hi'"), 0o644))
	// One non-parseable non-asset
	require.NoError(t, os.WriteFile(filepath.Join(dir, "deploy.sql"), []byte("#!/bin/sh"), 0o644))

	_, unparsed, err := walkService(dir, nil)
	require.NoError(t, err)

	assert.Equal(t, map[string]int{".sql": 1}, unparsed, "only .sql is a blind spot")
}
