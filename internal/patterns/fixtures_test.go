package patterns_test

// TestPatternFixtures enforces the design doc requirement:
// every YAML pattern file must have a corresponding <name>_test/ directory
// with an input fixture file. CI fails if fixtures are missing.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const patternsRoot = "../../patterns"

func TestPatternFixtures(t *testing.T) {
	langs, err := os.ReadDir(patternsRoot)
	if err != nil {
		t.Fatalf("cannot read patterns dir: %v", err)
	}

	for _, langEntry := range langs {
		if !langEntry.IsDir() {
			continue
		}
		langDir := filepath.Join(patternsRoot, langEntry.Name())
		entries, err := os.ReadDir(langDir)
		if err != nil {
			t.Errorf("cannot read %s: %v", langDir, err)
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
				continue
			}
			patternName := strings.TrimSuffix(entry.Name(), ".yaml")
			fixtureDir := filepath.Join(langDir, patternName+"_test")

			if _, err := os.Stat(fixtureDir); os.IsNotExist(err) {
				t.Errorf("missing fixture dir for %s/%s: expected %s",
					langEntry.Name(), entry.Name(), fixtureDir)
				continue
			}

			// At least one input.* file must exist inside the fixture dir.
			files, err := filepath.Glob(filepath.Join(fixtureDir, "input.*"))
			if err != nil || len(files) == 0 {
				t.Errorf("fixture dir %s has no input.* file", fixtureDir)
			}
		}
	}
}
