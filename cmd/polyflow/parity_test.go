package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/meta"
)

// TestParityMatrixCoversAllCommands walks the live cobra command tree (the
// same walk buildCLIDocs uses for GET /api/docs/cli) and asserts every
// command's full path ("polyflow config service add") is named somewhere
// in web/src/docs/parity.md. This is UO.7's Rule-12 test: a CLI command
// added without updating the parity matrix fails CI instead of the two
// surfaces silently drifting apart (docs/plan-13-ui-ops.md UO.7).
func TestParityMatrixCoversAllCommands(t *testing.T) {
	matrixPath := filepath.Join("..", "..", "web", "src", "docs", "parity.md")
	data, err := os.ReadFile(matrixPath)
	if err != nil {
		t.Fatalf("read parity matrix %s: %v", matrixPath, err)
	}
	matrix := string(data)

	var walk func(cmds []meta.CLICommand, prefix string)
	var missing []string
	walk = func(cmds []meta.CLICommand, prefix string) {
		for _, c := range cmds {
			path := strings.TrimSpace(prefix + " " + c.Name)
			full := "polyflow " + path
			if !strings.Contains(matrix, full) {
				missing = append(missing, full)
			}
			walk(c.Subcommands, path)
		}
	}
	walk(buildCLIDocs(rootCmd), "")

	if len(missing) > 0 {
		t.Errorf("parity matrix %s is missing %d command(s):\n  %s", matrixPath, len(missing), strings.Join(missing, "\n  "))
	}
}
