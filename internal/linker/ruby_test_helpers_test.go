package linker

import (
	"os"
	"path/filepath"
	"testing"
)

// writeRuby writes body to dir/name and returns the path — a small shared
// fixture helper several Ruby-parser test files use (formerly defined in
// ruby_http_hosts_test.go, migrated away in the Tier FX ruby_http_hosts
// pass; the helper itself has nothing to do with that migration, so it
// moved here rather than being deleted with it).
func writeRuby(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}
