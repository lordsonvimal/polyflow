package pipeline_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// migratedFrameworks lists every framework FX.8 has moved from
// internal/linker Go to a patterns/<lang>/*.yaml + rules/<lang>/*.dl pair,
// run through factpipe_frameworks. Each migration's whole point was
// deleting the framework-specific Go — this test is the tripwire against a
// future change accidentally reintroducing a file for one under
// internal/linker (e.g. a partial revert, or a copy-pasted starting point
// for a new pass that never got renamed).
//
// Append to this list as each further FX.8 migration lands (see
// docs/declarative-framework-pipeline-plan.md's FX.8 row for the current
// roster: rails_filters, rails_model_tables, sprockets_assets landed;
// rails_views not started).
var migratedFrameworks = []string{
	"rails_filters",
	"rails_model_tables",
	"sprockets_assets",
	"pusher_producer",
	"rails_helpers",
	"js_hoc",
	"js_mobx",
	"js_client_routes",
	"stylesheet_imports",
	"gorm_tables",
	"config_baseurl",
	"templ_layer",
	"rails_devise",
}

// TestFX8_NoMigratedFrameworkGoRemains greps internal/linker's file names
// (not contents — a doc comment or test fixture may legitimately mention a
// migrated framework's name in passing) for one of migratedFrameworks.
func TestFX8_NoMigratedFrameworkGoRemains(t *testing.T) {
	root := "../../linker"
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		base := strings.TrimSuffix(strings.TrimSuffix(e.Name(), "_test.go"), ".go")
		for _, fw := range migratedFrameworks {
			if base == fw {
				t.Errorf("internal/linker/%s reintroduces migrated framework %q — FX.8's point was deleting this Go, not keeping it alongside the declarative version", e.Name(), fw)
			}
		}
	}
	// internal/sprockets was sprockets_assets's own scanner package, deleted
	// whole (not just the linker pass) — same tripwire, one directory up.
	if _, err := os.Stat(filepath.Join(root, "..", "sprockets")); err == nil {
		t.Error("internal/sprockets reintroduced — sprockets_assets migrated to patterns/javascript/sprockets_directives.yaml + patterns/erb/sprockets_includes.yaml (FX.8 step 4)")
	}
}
