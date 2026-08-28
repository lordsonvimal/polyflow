package trace_ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRuntimeProviderClearsUnresolvedByLocation verifies that a span
// carrying both code.filepath and code.lineno produces a location-only
// ledger clear keyed on the caller (FromService) — the dynamic-key ledger
// entry a call site like this would have produced is producer-side, so the
// clear must name who executed the call site, not who served it.
func TestRuntimeProviderClearsUnresolvedByLocation(t *testing.T) {
	capturesDir := t.TempDir()
	sessionDir := filepath.Join(capturesDir, "sess1")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	data, err := os.ReadFile(filepath.Join(testFixturesDir, "http_code_attr.otlp.json"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "spans.otlp.json"), data, 0o644))

	p := NewRuntimeProvider(capturesDir, nil)
	ev, err := p.Collect(context.Background(), twoSvcWS())
	require.NoError(t, err)

	require.Len(t, ev.ClearsUnresolvedByLocation, 1)
	loc := ev.ClearsUnresolvedByLocation[0]
	assert.Equal(t, "web", loc.Service, "clear must be keyed on the caller (FromService), not the server")
	assert.Equal(t, "internal/handler/games.go", loc.File)
	assert.Equal(t, 42, loc.Line)
}

// TestRuntimeProviderNoLocationClearWithoutCodeLine verifies that a span with
// code.filepath but no code.lineno (channel granularity only) produces no
// location clear — only a true site-pinned observation may clear a ledger
// entry.
func TestRuntimeProviderNoLocationClearWithoutCodeLine(t *testing.T) {
	capturesDir := t.TempDir()
	sessionDir := filepath.Join(capturesDir, "sess1")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	data, err := os.ReadFile(filepath.Join(testFixturesDir, "http_2svc.otlp.json"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "spans.otlp.json"), data, 0o644))

	p := NewRuntimeProvider(capturesDir, nil)
	ev, err := p.Collect(context.Background(), twoSvcWS())
	require.NoError(t, err)

	assert.Empty(t, ev.ClearsUnresolvedByLocation, "channel-only observation must not clear any ledger location")
}

// TestRuntimeProviderClearsUnresolvedByLocation_Messaging verifies that a
// linked AMQP producer/consumer pair also produces a location clear, keyed
// on the PRODUCER's service and call site — messaging code attribution
// previously didn't exist at all (span_map.go never set CodeFile/CodeFunc on
// messaging refs), so dynamic_topic/dynamic_queue ledger entries could never
// be cleared even when runtime confirmed the exact publish call site.
func TestRuntimeProviderClearsUnresolvedByLocation_Messaging(t *testing.T) {
	capturesDir := t.TempDir()
	sessionDir := filepath.Join(capturesDir, "sess1")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	data, err := os.ReadFile(filepath.Join(testFixturesDir, "msg_amqp_linked.otlp.json"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "spans.otlp.json"), data, 0o644))

	ws := msgWS("publisher", "consumer")
	p := NewRuntimeProvider(capturesDir, nil)
	ev, err := p.Collect(context.Background(), ws)
	require.NoError(t, err)

	require.Len(t, ev.ClearsUnresolvedByLocation, 1)
	loc := ev.ClearsUnresolvedByLocation[0]
	assert.Equal(t, "publisher", loc.Service, "clear must be keyed on the producer, not the consumer")
	assert.Equal(t, "internal/publisher/events.go", loc.File)
	assert.Equal(t, 17, loc.Line)
}

// TestRuntimeProviderClearsUnresolvedByLocation_Deterministic runs Collect
// twice and requires identical output ordering (bug-class rule 2).
func TestRuntimeProviderClearsUnresolvedByLocation_Deterministic(t *testing.T) {
	capturesDir := t.TempDir()
	sessionDir := filepath.Join(capturesDir, "sess1")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	data, err := os.ReadFile(filepath.Join(testFixturesDir, "http_code_attr.otlp.json"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "spans.otlp.json"), data, 0o644))

	p := NewRuntimeProvider(capturesDir, nil)
	ev1, err := p.Collect(context.Background(), twoSvcWS())
	require.NoError(t, err)
	ev2, err := p.Collect(context.Background(), twoSvcWS())
	require.NoError(t, err)
	assert.Equal(t, ev1.ClearsUnresolvedByLocation, ev2.ClearsUnresolvedByLocation)
}
