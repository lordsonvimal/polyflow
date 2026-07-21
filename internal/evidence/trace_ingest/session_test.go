package trace_ingest_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/evidence/trace_ingest"
)

func writeSessionMeta(t *testing.T, dir, name string, meta trace_ingest.SessionMeta) {
	t.Helper()
	sessDir := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(sessDir, 0o755))
	data, err := json.MarshalIndent(meta, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "meta.json"), data, 0o644))
}

func TestListSessionInfos_Basic(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(2000*86400, 0)

	t0 := now.Add(-43 * 24 * time.Hour)
	t1 := now.Add(-2 * 24 * time.Hour)

	writeSessionMeta(t, dir, "old-session", trace_ingest.SessionMeta{
		Name: "old-session", StartedAt: t0, SpanCount: 10,
	})
	writeSessionMeta(t, dir, "new-session", trace_ingest.SessionMeta{
		Name: "new-session", StartedAt: t1, SpanCount: 5,
	})

	infos := trace_ingest.ListSessionInfos(dir, now)
	require.Len(t, infos, 2)

	// Newest first.
	assert.Equal(t, "new-session", infos[0].Name)
	assert.Equal(t, 5, infos[0].SpanCount)
	assert.Equal(t, "2d old", infos[0].Age)

	assert.Equal(t, "old-session", infos[1].Name)
	assert.Equal(t, "43d old", infos[1].Age)
}

func TestListSessionInfos_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	infos := trace_ingest.ListSessionInfos(dir, time.Now())
	assert.Nil(t, infos)
}

func TestListSessionInfos_MissingDir(t *testing.T) {
	infos := trace_ingest.ListSessionInfos("/nonexistent/path/xyz", time.Now())
	assert.Nil(t, infos)
}

func TestListSessionInfos_SkipsNonDirs(t *testing.T) {
	dir := t.TempDir()
	// Write a plain file — should be skipped.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "not-a-dir.txt"), []byte("x"), 0o644))

	writeSessionMeta(t, dir, "real-session", trace_ingest.SessionMeta{
		Name: "real-session", StartedAt: time.Now(), SpanCount: 1,
	})

	infos := trace_ingest.ListSessionInfos(dir, time.Now())
	require.Len(t, infos, 1)
	assert.Equal(t, "real-session", infos[0].Name)
}

func TestListSessionInfos_SkipsBadMeta(t *testing.T) {
	dir := t.TempDir()
	badDir := filepath.Join(dir, "bad-session")
	require.NoError(t, os.MkdirAll(badDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(badDir, "meta.json"), []byte("not-json"), 0o644))

	infos := trace_ingest.ListSessionInfos(dir, time.Now())
	assert.Nil(t, infos)
}

func TestListSessionInfos_HourAge(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(2000*86400, 0)
	t0 := now.Add(-3 * time.Hour)
	writeSessionMeta(t, dir, "sess", trace_ingest.SessionMeta{Name: "sess", StartedAt: t0})
	infos := trace_ingest.ListSessionInfos(dir, now)
	require.Len(t, infos, 1)
	assert.Equal(t, "3h old", infos[0].Age)
}

func TestListSessionInfos_TodayAge(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(2000*86400, 0)
	t0 := now.Add(-30 * time.Minute)
	writeSessionMeta(t, dir, "sess", trace_ingest.SessionMeta{Name: "sess", StartedAt: t0})
	infos := trace_ingest.ListSessionInfos(dir, now)
	require.Len(t, infos, 1)
	assert.Equal(t, "today", infos[0].Age)
}

func TestListSessionInfos_Determinism(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(2000*86400, 0)
	base := now.Add(-5 * 24 * time.Hour)
	for _, name := range []string{"z-sess", "a-sess", "m-sess"} {
		writeSessionMeta(t, dir, name, trace_ingest.SessionMeta{Name: name, StartedAt: base})
	}
	a := trace_ingest.ListSessionInfos(dir, now)
	b := trace_ingest.ListSessionInfos(dir, now)
	require.Equal(t, a, b, "two runs must produce identical output (rule 2)")
}
