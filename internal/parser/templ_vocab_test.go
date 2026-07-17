package parser

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// matcherWithVariant returns a TreeSitterMatcher with the DatastarVariant set,
// backed by an empty pattern registry (templ parser doesn't use tree-sitter).
func matcherWithVariant(variant string) *patterns.TreeSitterMatcher {
	m := patterns.NewTreeSitterMatcher(patterns.NewRegistry())
	m.DatastarVariant = variant
	return m
}

// countDatastarActions counts http_client nodes with datastar=true.
func countDatastarActions(nodes []graph.Node) int {
	n := 0
	for _, node := range nodes {
		if node.Type == graph.NodeTypeHTTPClient && node.Meta["datastar"] == "true" {
			n++
		}
	}
	return n
}

// TestTemplParser_V0Vocab_Positive: hyphen syntax with v0 matcher → finds actions.
func TestTemplParser_V0Vocab_Positive(t *testing.T) {
	p := &TemplParser{}
	nodes, _, _, err := p.Parse("testdata/datastar_v0.templ", "app", matcherWithVariant("datastar-v0"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := countDatastarActions(nodes)
	// 2 http_client nodes: static @post + partial @get; signal-only handler is not counted.
	if got != 2 {
		t.Errorf("v0 vocab: datastar actions = %d, want 2", got)
	}
}

// TestTemplParser_V0Vocab_WrongVersion (same-shape wrong-version negative):
// hyphen-syntax file processed with v1 matcher → zero datastar actions.
func TestTemplParser_V0Vocab_WrongVersion(t *testing.T) {
	p := &TemplParser{}
	nodes, _, _, err := p.Parse("testdata/datastar_v0.templ", "app", matcherWithVariant("datastar-v1"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := countDatastarActions(nodes)
	if got != 0 {
		t.Errorf("v1 matcher on v0 file: datastar actions = %d, want 0 (colon-only vocab must not match data-on-*)", got)
	}
}

// TestTemplParser_V1Vocab_WrongVersion (same-shape wrong-version negative):
// colon-syntax file processed with v0 matcher → zero datastar actions.
func TestTemplParser_V1Vocab_WrongVersion(t *testing.T) {
	p := &TemplParser{}
	nodes, _, _, err := p.Parse("testdata/datastar.templ", "app", matcherWithVariant("datastar-v0"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := countDatastarActions(nodes)
	if got != 0 {
		t.Errorf("v0 matcher on v1 file: datastar actions = %d, want 0 (hyphen-only vocab must not match data-on:*)", got)
	}
}

// TestTemplParser_FallbackVocabAcceptsBoth: nil matcher → combined vocab →
// both colon and hyphen syntax produce actions.
func TestTemplParser_FallbackVocab_V1File(t *testing.T) {
	p := &TemplParser{}
	nodes, _, _, err := p.Parse("testdata/datastar.templ", "app", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Existing test coverage: 3 actions from colon-syntax file.
	got := countDatastarActions(nodes)
	if got != 3 {
		t.Errorf("nil matcher + v1 file: datastar actions = %d, want 3", got)
	}
}

func TestTemplParser_FallbackVocab_V0File(t *testing.T) {
	p := &TemplParser{}
	nodes, _, _, err := p.Parse("testdata/datastar_v0.templ", "app", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// fallback accepts hyphen too → 2 actions from v0 file.
	got := countDatastarActions(nodes)
	if got != 2 {
		t.Errorf("nil matcher + v0 file: datastar actions = %d, want 2", got)
	}
}

// TestTemplParser_V0Vocab_ReactiveHyphenAttr: hyphen reactive attr recognized in v0.
func TestTemplParser_V0Vocab_ReactiveHyphenAttr(t *testing.T) {
	p := &TemplParser{}
	nodes, _, _, err := p.Parse("testdata/datastar_v0.templ", "app", matcherWithVariant("datastar-v0"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var signals int
	for _, n := range nodes {
		if n.Type == graph.NodeTypeSignal {
			signals++
		}
	}
	// data-attr-class={'active': $boardOpen} → 1 reactive read for boardOpen.
	if signals < 1 {
		t.Errorf("v0 vocab: signal nodes = %d, want ≥1 from data-attr-class", signals)
	}
}

// TestTemplParser_V1Vocab_DataInit: data-init={"@get('…')"} is an action
// source under the v1 vocab (SSE-subscribe-on-mount idiom). Regression for
// the chessleap play-events-sse eval case: the @get inside data-init was
// invisible, so the templ→handler edge (and the page's blast-radius
// membership) silently vanished.
func TestTemplParser_V1Vocab_DataInit(t *testing.T) {
	p := &TemplParser{}
	nodes, _, _, err := p.Parse("testdata/datastar_init.templ", "app", matcherWithVariant("datastar-v1"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := countDatastarActions(nodes)
	// 2 http_client nodes: concatenated @get + static @get; signal-only init excluded.
	if got != 2 {
		t.Errorf("v1 vocab: data-init datastar actions = %d, want 2", got)
	}
}

// TestTemplParser_V0Vocab_DataInitNotMatched (wrong-version negative):
// data-init is a v1 idiom; the v0 hyphen vocab must not match it.
func TestTemplParser_V0Vocab_DataInitNotMatched(t *testing.T) {
	p := &TemplParser{}
	nodes, _, _, err := p.Parse("testdata/datastar_init.templ", "app", matcherWithVariant("datastar-v0"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := countDatastarActions(nodes); got != 0 {
		t.Errorf("v0 matcher on data-init file: datastar actions = %d, want 0", got)
	}
}

// TestTemplParser_CombinedVocab_DataInit: unversioned projects (combined
// fallback vocab) must also see data-init actions.
func TestTemplParser_CombinedVocab_DataInit(t *testing.T) {
	p := &TemplParser{}
	nodes, _, _, err := p.Parse("testdata/datastar_init.templ", "app", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := countDatastarActions(nodes); got != 2 {
		t.Errorf("combined vocab: data-init datastar actions = %d, want 2", got)
	}
}
