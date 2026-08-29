package linker

import (
	"fmt"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// TestLinkJSReceiverTypeCalls_ThisCallResolvesSameClass is the foundational
// regression guard: confirmed live on gitnexus, 1671 `this.method()` call
// sites in src/core/ingestion alone were previously invisible to every
// existing pass — no parser or linker code resolved a `this.` receiver at
// all, so every helper method called only this way read as dead.
func TestLinkJSReceiverTypeCalls_ThisCallResolvesSameClass(t *testing.T) {
	t.Parallel()
	_, paths := writeJSFixture(t, map[string]string{
		"widget.ts": "export class Widget {\n" +
			"  run() {\n" +
			"    this.helper();\n" +
			"  }\n" +
			"  helper() {\n" +
			"    return 1;\n" +
			"  }\n" +
			"}\n",
	})
	widget := paths[0]

	nodes := []graph.Node{
		jsClassNode("svc", widget, "Widget", 1, 7),
		jsFuncNode("svc", widget, "run", 2),
		jsFuncNode("svc", widget, "helper", 5),
	}
	edges := resolveJSReceiverTypeCalls(widget, "svc",
		map[string]string{"Widget": nodes[0].ID}, nil,
		map[string]map[string]string{nodes[0].ID: {"run": nodes[1].ID, "helper": nodes[2].ID}},
		func(id string) []string { return []string{id} }, nil, map[string]bool{})

	wantID := fmt.Sprintf("calls:%s->%s", nodes[1].ID, nodes[2].ID)
	var got bool
	for _, e := range edges {
		if e.ID == wantID {
			got = true
		}
	}
	if !got {
		t.Fatalf("expected this.helper() to resolve to Widget#helper, edges: %+v", edges)
	}
}

// TestLinkJSReceiverTypeCalls_ThisCallResolvesInheritedMethod proves a
// `this.` call that isn't declared on the same class body still resolves,
// by walking the ancestor chain — a subclass calling an inherited-but-not-
// overridden method must not read as calling nothing.
func TestLinkJSReceiverTypeCalls_ThisCallResolvesInheritedMethod(t *testing.T) {
	t.Parallel()
	_, paths := writeJSFixture(t, map[string]string{
		"widget.ts": "export class Child extends Base {\n" +
			"  run() {\n" +
			"    this.helper();\n" +
			"  }\n" +
			"}\n",
	})
	widget := paths[0]

	childID := fmt.Sprintf("svc:%s:class:Child:1", widget)
	baseID := "svc:base.ts:class:Base:1"
	runID := fmt.Sprintf("svc:%s:function:run:2", widget)
	helperID := "svc:base.ts:function:helper:2"

	ancestorChain := func(id string) []string {
		if id == childID {
			return []string{childID, baseID}
		}
		return []string{id}
	}
	edges := resolveJSReceiverTypeCalls(widget, "svc",
		map[string]string{"Child": childID}, nil,
		map[string]map[string]string{baseID: {"helper": helperID}},
		ancestorChain, nil, map[string]bool{})

	wantID := fmt.Sprintf("calls:%s->%s", runID, helperID)
	var got bool
	for _, e := range edges {
		if e.ID == wantID {
			got = true
		}
	}
	if !got {
		t.Fatalf("expected this.helper() to resolve through the ancestor chain to Base#helper, edges: %+v", edges)
	}
}

// TestLinkJSReceiverTypeCalls_NewLocalVariableResolves covers `const x = new
// Foo(); x.method()` — a local variable whose type comes from a `new`
// expression, not an annotation.
func TestLinkJSReceiverTypeCalls_NewLocalVariableResolves(t *testing.T) {
	t.Parallel()
	_, paths := writeJSFixture(t, map[string]string{
		"gen.ts": "export function build() {\n" +
			"  const generator = new WikiGenerator();\n" +
			"  generator.run();\n" +
			"}\n",
	})
	gen := paths[0]

	genClassID := fmt.Sprintf("svc:%s:class:WikiGenerator:99", gen)
	runID := fmt.Sprintf("svc:%s:function:run:100", gen)
	buildID := fmt.Sprintf("svc:%s:function:build:1", gen)

	edges := resolveJSReceiverTypeCalls(gen, "svc",
		map[string]string{"WikiGenerator": genClassID}, nil,
		map[string]map[string]string{genClassID: {"run": runID}},
		func(id string) []string { return []string{id} }, nil, map[string]bool{})

	wantID := fmt.Sprintf("calls:%s->%s", buildID, runID)
	var got bool
	for _, e := range edges {
		if e.ID == wantID {
			got = true
		}
	}
	if !got {
		t.Fatalf("expected generator.run() to resolve to WikiGenerator#run, edges: %+v", edges)
	}
}

// TestLinkJSReceiverTypeCalls_TypedParameterResolves covers a plain
// concrete-class-typed parameter — `function f(x: Foo) { x.method() }` —
// the simplest shape of the gap: a locally-inferable concrete class, no
// interface indirection at all.
func TestLinkJSReceiverTypeCalls_TypedParameterResolves(t *testing.T) {
	t.Parallel()
	_, paths := writeJSFixture(t, map[string]string{
		"consumer.ts": "export function process(builder: CfgBuilder) {\n" +
			"  builder.newBlock();\n" +
			"}\n",
	})
	consumer := paths[0]

	builderClassID := "svc:builder.ts:class:CfgBuilder:1"
	newBlockID := "svc:builder.ts:function:newBlock:2"
	processID := fmt.Sprintf("svc:%s:function:process:1", consumer)

	edges := resolveJSReceiverTypeCalls(consumer, "svc",
		map[string]string{"CfgBuilder": builderClassID}, nil,
		map[string]map[string]string{builderClassID: {"newBlock": newBlockID}},
		func(id string) []string { return []string{id} }, nil, map[string]bool{})

	wantID := fmt.Sprintf("calls:%s->%s", processID, newBlockID)
	var got bool
	for _, e := range edges {
		if e.ID == wantID {
			got = true
		}
	}
	if !got {
		t.Fatalf("expected builder.newBlock() to resolve to CfgBuilder#newBlock, edges: %+v", edges)
	}
}

// TestLinkJSReceiverTypeCalls_ConstructorPropertyResolves covers the TS
// parameter-property shape — `constructor(private builder: CfgBuilder)` —
// which types `this.builder` for the rest of the class, not just inside the
// constructor body itself.
func TestLinkJSReceiverTypeCalls_ConstructorPropertyResolves(t *testing.T) {
	t.Parallel()
	_, paths := writeJSFixture(t, map[string]string{
		"extractor.ts": "export class Extractor {\n" +
			"  constructor(private builder: CfgBuilder) {}\n" +
			"  run() {\n" +
			"    this.builder.newBlock();\n" +
			"  }\n" +
			"}\n",
	})
	extractor := paths[0]

	builderClassID := "svc:builder.ts:class:CfgBuilder:1"
	newBlockID := "svc:builder.ts:function:newBlock:2"
	extractorClassID := fmt.Sprintf("svc:%s:class:Extractor:1", extractor)
	runID := fmt.Sprintf("svc:%s:function:run:3", extractor)

	edges := resolveJSReceiverTypeCalls(extractor, "svc",
		map[string]string{"CfgBuilder": builderClassID, "Extractor": extractorClassID}, nil,
		map[string]map[string]string{builderClassID: {"newBlock": newBlockID}},
		func(id string) []string { return []string{id} }, nil, map[string]bool{})

	wantID := fmt.Sprintf("calls:%s->%s", runID, newBlockID)
	var got bool
	for _, e := range edges {
		if e.ID == wantID {
			got = true
		}
	}
	if !got {
		t.Fatalf("expected this.builder.newBlock() to resolve via the constructor parameter property, edges: %+v", edges)
	}
}

// TestLinkJSReceiverTypeCalls_InterfaceFanOut covers an interface-typed
// parameter — `config: FieldExtractionConfig` — resolving through EVERY
// class implementing that interface, since the true runtime type is
// unknowable statically (the JS/TS analogue of Ruby's downward override
// dispatch).
func TestLinkJSReceiverTypeCalls_InterfaceFanOut(t *testing.T) {
	t.Parallel()
	_, paths := writeJSFixture(t, map[string]string{
		"consumer.ts": "export function process(config: FieldExtractionConfig) {\n" +
			"  config.extractVisibility();\n" +
			"}\n",
	})
	consumer := paths[0]

	ifaceID := "svc:cfg.ts:interface:FieldExtractionConfig:1"
	implAID := "svc:impl_a.ts:class:PublicConfig:1"
	implBID := "svc:impl_b.ts:class:PrivateConfig:1"
	methodAID := "svc:impl_a.ts:function:extractVisibility:2"
	methodBID := "svc:impl_b.ts:function:extractVisibility:2"
	processID := fmt.Sprintf("svc:%s:function:process:1", consumer)

	edges := resolveJSReceiverTypeCalls(consumer, "svc",
		nil, map[string]string{"FieldExtractionConfig": ifaceID},
		map[string]map[string]string{
			implAID: {"extractVisibility": methodAID},
			implBID: {"extractVisibility": methodBID},
		},
		func(id string) []string { return []string{id} },
		map[string][]string{ifaceID: {implAID, implBID}},
		map[string]bool{})

	wantA := fmt.Sprintf("calls:%s->%s", processID, methodAID)
	wantB := fmt.Sprintf("calls:%s->%s", processID, methodBID)
	var gotA, gotB bool
	for _, e := range edges {
		if e.ID == wantA {
			gotA = true
		}
		if e.ID == wantB {
			gotB = true
		}
	}
	if !gotA || !gotB {
		t.Fatalf("expected config.extractVisibility() to fan out to both implementers, edges: %+v", edges)
	}
}

// TestLinkJSReceiverTypeCalls_UntypedReceiverNeverMatches is the false-
// positive guard: an untyped plain-JS parameter used as a call receiver must
// never be guessed at — this pass only resolves the syntactically
// recoverable slice (this., new-assigned locals, and explicit type
// annotations), never a bare name with no evidence at all.
func TestLinkJSReceiverTypeCalls_UntypedReceiverNeverMatches(t *testing.T) {
	t.Parallel()
	_, paths := writeJSFixture(t, map[string]string{
		"consumer.js": "export function process(thing) {\n" +
			"  thing.method();\n" +
			"}\n",
	})
	consumer := paths[0]

	edges := resolveJSReceiverTypeCalls(consumer, "svc",
		map[string]string{"CfgBuilder": "svc:builder.ts:class:CfgBuilder:1"}, nil,
		map[string]map[string]string{"svc:builder.ts:class:CfgBuilder:1": {"method": "svc:builder.ts:function:method:2"}},
		func(id string) []string { return []string{id} }, nil, map[string]bool{})

	if len(edges) != 0 {
		t.Fatalf("expected no edges for an untyped receiver, got: %+v", edges)
	}
}
