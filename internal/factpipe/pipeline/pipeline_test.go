package pipeline_test

import (
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/lordsonvimal/polyflow/internal/deps"
	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

const alphaYAML = `language: ruby
patterns:
  - name: alpha_guard
    query: |
      (call
        method: (identifier) @m (#eq? @m "before_action")
        arguments: (argument_list (simple_symbol) @cb)) @call
    facts:
      - pred: alpha_reg
        args:
          klass: { extract: "enclosing_name(class)" }
          cb:    { capture: cb, extract: string_value }
emit:
  - relation: alpha_edge
    columns: [Klass, Cb]
    edge: { from: { arg: Klass }, to: { arg: Cb }, type: calls, label: guard }
    meta: { via: alpha }
`

const alphaDL = "alpha_edge(K, C) :- alpha_reg(K, C).\n"

const betaYAML = `language: ruby
package: gem-beta
patterns:
  - name: beta_guard
    query: |
      (call
        method: (identifier) @m (#eq? @m "after_action")
        arguments: (argument_list (simple_symbol) @cb)) @call
    facts:
      - pred: beta_reg
        args:
          klass: { extract: "enclosing_name(class)" }
          cb:    { capture: cb, extract: string_value }
emit:
  - relation: beta_edge
    columns: [Klass, Cb]
    edge: { from: { arg: Klass }, to: { arg: Cb }, type: calls, label: guard }
    meta: { via: beta }
`

const betaDL = "beta_edge(K, C) :- beta_reg(K, C).\n"

const rubySrc = `class DemoController < ApplicationController
  before_action :authenticate
  after_action :audit
  def authenticate; end
  def audit; end
end
`

// gammaYAML and deltaYAML (XM.1) deliberately reuse the exact same local
// pattern name ("guard") and an identical query shape across two
// same-language frameworks — the collision buildSharedMatchers' owner
// namespacing must survive, since both patterns now live in one shared
// Registry for "ruby". Each still asserts its own distinct predicate so a
// misrouted match (gamma's match handed to delta's FactSpecs, or vice
// versa) would show up as a missing or wrong edge rather than silently
// matching.
const gammaYAML = `language: ruby
patterns:
  - name: guard
    query: |
      (call
        method: (identifier) @m (#eq? @m "before_action")
        arguments: (argument_list (simple_symbol) @cb)) @call
    facts:
      - pred: gamma_reg
        args:
          klass: { extract: "enclosing_name(class)" }
          cb:    { capture: cb, extract: string_value }
emit:
  - relation: gamma_edge
    columns: [Klass, Cb]
    edge: { from: { arg: Klass }, to: { arg: Cb }, type: calls, label: guard }
    meta: { via: gamma }
`

const gammaDL = "gamma_edge(K, C) :- gamma_reg(K, C).\n"

const deltaYAML = `language: ruby
patterns:
  - name: guard
    query: |
      (call
        method: (identifier) @m (#eq? @m "before_action")
        arguments: (argument_list (simple_symbol) @cb)) @call
    facts:
      - pred: delta_reg
        args:
          klass: { extract: "enclosing_name(class)" }
          cb:    { capture: cb, extract: string_value }
emit:
  - relation: delta_edge
    columns: [Klass, Cb]
    edge: { from: { arg: Klass }, to: { arg: Cb }, type: calls, label: guard }
    meta: { via: delta }
`

const deltaDL = "delta_edge(K, C) :- delta_reg(K, C).\n"

func fixtures() (ruleFS, patternFS fstest.MapFS) {
	ruleFS = fstest.MapFS{
		"ruby/fw_alpha.dl": {Data: []byte(alphaDL)},
		"ruby/fw_beta.dl":  {Data: []byte(betaDL)},
	}
	patternFS = fstest.MapFS{
		"ruby/fw_alpha.yaml": {Data: []byte(alphaYAML)},
		"ruby/fw_beta.yaml":  {Data: []byte(betaYAML)},
	}
	return
}

func demoRun(t *testing.T, fws []*pipeline.Framework) pipeline.Result {
	t.Helper()
	files := []pipeline.ParsedFile{{
		Path:     "app/controllers/demo_controller.rb",
		Language: "ruby",
		Src:      []byte(rubySrc),
	}}
	snap := graph.Snapshot{Nodes: []graph.Node{{ID: "n1", Service: "demo", Type: graph.NodeTypeClass}}}
	res, err := pipeline.Run(fws, files, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestLoadAndActiveGating(t *testing.T) {
	ruleFS, patternFS := fixtures()
	reg, err := pipeline.Load(ruleFS, patternFS)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(reg.All()); got != 2 {
		t.Fatalf("registry has %d frameworks, want 2", got)
	}

	// No gem-beta dependency ⇒ only the ungated framework is active.
	active := reg.Active(nil)
	if len(active) != 1 || active[0].Name != "fw_alpha" {
		t.Fatalf("Active(nil) = %v, want [fw_alpha]", names(active))
	}

	// With gem-beta present, both activate.
	withBeta := reg.Active([]deps.Dependency{{Name: "gem-beta", Version: "1.0.0"}})
	if len(withBeta) != 2 {
		t.Fatalf("Active(gem-beta) = %v, want 2", names(withBeta))
	}
}

func TestRunGatedOutFrameworkProducesNoEdges(t *testing.T) {
	ruleFS, patternFS := fixtures()
	reg, err := pipeline.Load(ruleFS, patternFS)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	res := demoRun(t, reg.Active(nil))
	if len(res.Edges) != 1 {
		t.Fatalf("got %d edges, want 1: %+v", len(res.Edges), res.Edges)
	}
	e := res.Edges[0]
	if e.From != "DemoController" || e.To != "authenticate" || e.Label != "guard" || e.Meta["via"] != "alpha" {
		t.Fatalf("unexpected edge %+v", e)
	}
	if len(e.Sources) != 1 || e.Sources[0].Layer != "L3" || e.Sources[0].Rule != "fw_alpha/alpha_edge" {
		t.Fatalf("edge missing L3 provenance: %+v", e.Sources)
	}

	// With beta active too, the after_action edge appears.
	res2 := demoRun(t, reg.Active([]deps.Dependency{{Name: "gem-beta", Version: "1.0.0"}}))
	var vias []string
	for _, e := range res2.Edges {
		vias = append(vias, e.Meta["via"])
	}
	// Sorted by edge ID: "...->audit" (beta) precedes "...->authenticate" (alpha).
	if !reflect.DeepEqual(vias, []string{"beta", "alpha"}) {
		t.Fatalf("edges via = %v, want [beta alpha]", vias)
	}
}

func TestRunIsDeterministic(t *testing.T) {
	ruleFS, patternFS := fixtures()
	reg, err := pipeline.Load(ruleFS, patternFS)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	a := demoRun(t, reg.All())
	b := demoRun(t, reg.All())
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("Run not deterministic:\n%+v\n%+v", a, b)
	}
}

// TestLoadEmbeddedFrameworks pins the frameworks migrated to the declarative
// pipeline: gin_middleware + express_middleware (FX.7) and rails_filters
// (FX.8), each compiling and carrying an emit spec.
func TestLoadEmbeddedFrameworks(t *testing.T) {
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	got := map[string]string{}
	for _, fw := range reg.All() {
		if fw.Rules == nil || len(fw.Emits) == 0 {
			t.Errorf("framework %s: rules=%v emits=%d", fw.Name, fw.Rules != nil, len(fw.Emits))
		}
		got[fw.Name] = fw.Language
	}
	for name, lang := range map[string]string{
		"gin_middleware": "go", "express_middleware": "javascript", "rails_filters": "ruby",
	} {
		if got[name] != lang {
			t.Errorf("framework %q: language %q, want %q (registry: %v)", name, got[name], lang, got)
		}
	}
}

// TestRunSharedMatcherNoCrossContamination is XM.1's own test
// (docs/factpipe-cross-framework-matching-plan.md): gamma and delta share
// one language, one local pattern name ("guard"), and one query shape, so
// buildSharedMatchers packs both into the same "ruby" Registry. Each must
// still emit only its own edge — a routing bug (owner-namespace stripped
// wrong, or a match fanned out to every framework in the shared registry
// instead of just its owner) would show up here as a missing edge, a
// duplicate, or gamma_reg/delta_reg facts crossing into the wrong
// framework's FactSet.
func TestRunSharedMatcherNoCrossContamination(t *testing.T) {
	ruleFS := fstest.MapFS{
		"ruby/fw_gamma.dl": {Data: []byte(gammaDL)},
		"ruby/fw_delta.dl": {Data: []byte(deltaDL)},
	}
	patternFS := fstest.MapFS{
		"ruby/fw_gamma.yaml": {Data: []byte(gammaYAML)},
		"ruby/fw_delta.yaml": {Data: []byte(deltaYAML)},
	}
	reg, err := pipeline.Load(ruleFS, patternFS)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	res := demoRun(t, reg.All())
	if len(res.Edges) != 2 {
		t.Fatalf("got %d edges, want 2 (one per framework): %+v", len(res.Edges), res.Edges)
	}
	vias := map[string]bool{}
	for _, e := range res.Edges {
		if e.From != "DemoController" || e.To != "authenticate" {
			t.Fatalf("unexpected edge %+v", e)
		}
		vias[e.Meta["via"]] = true
	}
	if !vias["gamma"] || !vias["delta"] {
		t.Fatalf("edges vias = %v, want both gamma and delta present", vias)
	}
}

func names(fws []*pipeline.Framework) []string {
	var out []string
	for _, f := range fws {
		out = append(out, f.Name)
	}
	return out
}
