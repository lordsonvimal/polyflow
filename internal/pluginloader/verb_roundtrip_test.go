package pluginloader

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/lordsonvimal/polyflow/sdk/linkplugin"
)

// buildVerbPlugin compiles internal/pluginloader/testdata/verbplugin into
// tmpDir and returns the binary path — the FX.9 sibling of
// roundtrip_test.go's buildFakePlugin.
func buildVerbPlugin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "verbplugin")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/verbplugin")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build verbplugin: %v\n%s", err, out)
	}
	return bin
}

func launchVerbPlugin(t *testing.T) *LaunchedPlugin {
	t.Helper()
	bin := buildVerbPlugin(t)
	m := &Manifest{
		Name:            "verbplugin",
		ProtocolVersion: linkplugin.ProtocolVersion,
		Dir:             filepath.Dir(bin),
		Entrypoint:      filepath.Base(bin),
		Components: []Component{
			{ID: "verbs", Package: "verbplugin-marker", Language: "go", ProvidesVerbs: []string{"ancestor_matching"}},
		},
	}
	plugin, err := Launch(m)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(plugin.Close)
	return plugin
}

// TestVerbRoundTrip_HandshakeVerbsExtract proves the wire protocol itself
// (Verbs -> ExtractVerb, over the real subprocess boundary — never an
// in-process stub) end-to-end, independent of internal/patterns' dispatch,
// which internal/patterns' own TestPluginVerb_AncestorMatching_* test covers
// with a same-logic stub (the two together are FX.9's "reference verb plugin
// / produces the same facts as the in-tree verb" acceptance, split across
// the one RPC boundary internal/patterns can't cross in its own tests).
func TestVerbRoundTrip_HandshakeVerbsExtract(t *testing.T) {
	plugin := launchVerbPlugin(t)

	names, err := plugin.Verbs(context.Background())
	if err != nil {
		t.Fatalf("Verbs: %v", err)
	}
	if len(names) != 1 || names[0] != "ancestor_matching" {
		t.Fatalf("Verbs() = %v, want [ancestor_matching]", names)
	}

	node := linkplugin.VerbNode{
		Type: "simple_symbol", Text: ":current_user", StartLine: 2, EndLine: 2,
		Ancestors: []linkplugin.VerbNode{
			{Type: "call", Text: "helper_method :current_user"},
			{Type: "class", Text: "class UsersController < ApplicationController\nend"},
		},
	}

	vals, ok, err := plugin.Client.ExtractVerb(context.Background(), "ancestor_matching", "class,UsersController", node, "users_controller.rb", "ruby")
	if err != nil {
		t.Fatalf("ExtractVerb: %v", err)
	}
	if !ok || len(vals) != 1 || vals[0].Str != "true" {
		t.Fatalf("ExtractVerb(class,UsersController) = %+v, ok=%v, want [true], ok=true", vals, ok)
	}

	miss, ok, err := plugin.Client.ExtractVerb(context.Background(), "ancestor_matching", "class,NoSuchClass", node, "users_controller.rb", "ruby")
	if err != nil {
		t.Fatalf("ExtractVerb (miss): %v", err)
	}
	if !ok || len(miss) != 1 || miss[0].Str != "" {
		t.Fatalf("ExtractVerb(class,NoSuchClass) = %+v, ok=%v, want [\"\"], ok=true", miss, ok)
	}

	// An unadvertised verb name declines rather than erroring.
	_, ok, err = plugin.Client.ExtractVerb(context.Background(), "no_such_verb", "", node, "users_controller.rb", "ruby")
	if err != nil {
		t.Fatalf("ExtractVerb (unknown verb): %v", err)
	}
	if ok {
		t.Errorf("ExtractVerb(no_such_verb) ok = true, want false")
	}
}

// TestRegisterVerbs_WiresIntoPatternsDispatch proves LaunchedPlugin.RegisterVerbs
// (the real load-time wiring internal/indexer/plugin_link.go calls) makes
// the plugin's verb reachable through internal/patterns.RunRegisteredVerb —
// the same lookup runVerb's default case uses.
func TestRegisterVerbs_WiresIntoPatternsDispatch(t *testing.T) {
	patterns.ResetVerbProviders()
	defer patterns.ResetVerbProviders()

	plugin := launchVerbPlugin(t)

	notes, err := plugin.RegisterVerbs(context.Background(), nil)
	if err != nil {
		t.Fatalf("RegisterVerbs: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("RegisterVerbs notes = %+v, want none", notes)
	}

	node := patterns.VerbNode{
		Type: "simple_symbol", Text: ":current_user",
		Ancestors: []patterns.VerbNode{{Type: "class", Text: "class UsersController\nend"}},
	}
	vals, ok := patterns.RunRegisteredVerb("ancestor_matching", node, "class,UsersController", "f.rb", "ruby")
	if !ok || len(vals) != 1 || vals[0].Str != "true" {
		t.Fatalf("RunRegisteredVerb after RegisterVerbs = %+v, ok=%v, want [true], ok=true", vals, ok)
	}
}

// TestRegisterVerbs_GenericOnlyRejection proves FX.9's generic-only rule: a
// verb name that contains a "known framework name" is rejected at
// registration (never wired into internal/patterns), reported as a
// CoverageNote rather than a hard failure.
func TestRegisterVerbs_GenericOnlyRejection(t *testing.T) {
	patterns.ResetVerbProviders()
	defer patterns.ResetVerbProviders()

	plugin := launchVerbPlugin(t)

	notes, err := plugin.RegisterVerbs(context.Background(), []string{"ancestor_matching"})
	if err != nil {
		t.Fatalf("RegisterVerbs: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("RegisterVerbs notes = %+v, want exactly 1 rejection", notes)
	}

	if _, ok := patterns.RunRegisteredVerb("ancestor_matching", patterns.VerbNode{}, "", "", ""); ok {
		t.Errorf("rejected verb was registered anyway")
	}
}
