package pluginloader

import (
	"path/filepath"
	"testing"
)

func TestLoadManifest(t *testing.T) {
	m, err := LoadManifest(filepath.Join("testdata", "fakeplugin", "manifest.yaml"))
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.Name != "fakeplugin" {
		t.Errorf("Name = %q, want fakeplugin", m.Name)
	}
	if m.ProtocolVersion != 1 {
		t.Errorf("ProtocolVersion = %d, want 1", m.ProtocolVersion)
	}
	if len(m.Components) != 1 || m.Components[0].ID != "fake" {
		t.Fatalf("Components = %+v, want one component id=fake", m.Components)
	}
	if got, want := m.EntrypointPath(), filepath.Join("testdata", "fakeplugin", "fakeplugin"); got != want {
		t.Errorf("EntrypointPath() = %q, want %q", got, want)
	}
}

func TestDiscover(t *testing.T) {
	root, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	// Discover's project-local path is <repoRoot>/.polyflow/plugins, not
	// testdata/fakeplugin directly — point repoRoot at a directory whose
	// .polyflow/plugins would contain manifests, and separately confirm the
	// fixture's manifest.yaml itself parses (TestLoadManifest above). Here
	// we just confirm Discover doesn't error when nothing is found, since
	// this repo has no .polyflow/plugins directory of its own.
	manifests, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(manifests) != 0 {
		t.Errorf("Discover(%s) = %v, want none (no .polyflow/plugins under testdata)", root, manifests)
	}
}

func TestCheckProtocolVersion(t *testing.T) {
	ok := &Manifest{Name: "ok", ProtocolVersion: 1}
	if note := CheckProtocolVersion(ok); note != nil {
		t.Errorf("CheckProtocolVersion(compatible) = %+v, want nil", note)
	}

	bad := &Manifest{Name: "bad", ProtocolVersion: 99}
	note := CheckProtocolVersion(bad)
	if note == nil {
		t.Fatal("CheckProtocolVersion(incompatible) = nil, want a CoverageNote")
	}
	if note.Plugin != "bad" {
		t.Errorf("note.Plugin = %q, want bad", note.Plugin)
	}
}
