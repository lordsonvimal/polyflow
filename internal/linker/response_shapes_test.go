package linker

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// TestLinkResponseShapes verifies the cross-language response_of join: a Go
// response struct is matched to a shape-equivalent TS interface, gated on the
// struct actually being a returns target, with untyped/ungated structs left
// alone (#12) and the Jaccard threshold enforced.
func TestLinkResponseShapes(t *testing.T) {
	nodes := []graph.Node{
		{ID: "svc:cyto.go:struct:CytoscapeGraph:1", Type: graph.NodeTypeStruct, Language: "go",
			Meta: map[string]string{"fields": `[{"name":"Nodes","type":"[]X","tag":"json:\"nodes\""},{"name":"Edges","type":"[]Y","tag":"json:\"edges\""}]`}},
		// A Go struct with the same shape but NOT a returns target — must not link.
		{ID: "svc:other.go:struct:NotAResponse:1", Type: graph.NodeTypeStruct, Language: "go",
			Meta: map[string]string{"fields": `[{"name":"Nodes","type":"[]X","tag":"json:\"nodes\""},{"name":"Edges","type":"[]Y","tag":"json:\"edges\""}]`}},
		// TS interface mirror (member list in meta.methods).
		{ID: "web:g.ts:interface:CytoscapeGraph:1", Type: graph.NodeTypeInterface, Language: "typescript",
			Meta: map[string]string{"methods": "nodes,edges"}},
		// TS interface that overlaps too little (1 shared of 3) — below threshold.
		{ID: "web:g.ts:interface:Unrelated:1", Type: graph.NodeTypeInterface, Language: "typescript",
			Meta: map[string]string{"methods": "nodes,foo,bar"}},
		// A Go interface must never be treated as a client type.
		{ID: "svc:x.go:interface:GoIface:1", Type: graph.NodeTypeInterface, Language: "go",
			Meta: map[string]string{"methods": "nodes,edges"}},
	}
	edges := []graph.Edge{
		{ID: "r1", Type: graph.EdgeTypeReturns, From: "svc:h.go:method:handleGraph:1", To: "svc:cyto.go:struct:CytoscapeGraph:1"},
	}

	out := LinkResponseShapes(nodes, edges)

	if len(out) != 1 {
		t.Fatalf("want 1 response_of edge, got %d: %+v", len(out), out)
	}
	e := out[0]
	if e.Type != graph.EdgeTypeResponseOf {
		t.Errorf("type = %q, want response_of", e.Type)
	}
	if e.From != "svc:cyto.go:struct:CytoscapeGraph:1" || e.To != "web:g.ts:interface:CytoscapeGraph:1" {
		t.Errorf("endpoints = %s -> %s", e.From, e.To)
	}
	if e.Confidence != graph.ConfidenceStatic {
		t.Errorf("confidence = %q, want static", e.Confidence)
	}
	if e.Meta["match"] != "shape" || e.Meta["shared"] != "2" || e.Meta["jaccard"] != "1.00" {
		t.Errorf("meta = %+v", e.Meta)
	}
}

// TestLinkResponseShapes_NoReturnsNoEdges verifies the pass is inert when no
// struct is a declared response — nothing is fabricated from shape alone.
func TestLinkResponseShapes_NoReturnsNoEdges(t *testing.T) {
	nodes := []graph.Node{
		{ID: "svc:s.go:struct:A:1", Type: graph.NodeTypeStruct, Language: "go",
			Meta: map[string]string{"fields": `[{"name":"X","tag":"json:\"x\""},{"name":"Y","tag":"json:\"y\""}]`}},
		{ID: "web:t.ts:interface:A:1", Type: graph.NodeTypeInterface, Language: "typescript",
			Meta: map[string]string{"methods": "x,y"}},
	}
	if got := LinkResponseShapes(nodes, nil); len(got) != 0 {
		t.Fatalf("want 0 edges without a returns target, got %d", len(got))
	}
}

// TestGoStructJSONFields checks tag parsing (wire name, omitempty, json:"-",
// and untagged field fallback).
func TestGoStructJSONFields(t *testing.T) {
	raw := `[{"name":"ID","tag":"json:\"id,omitempty\""},{"name":"Secret","tag":"json:\"-\""},{"name":"Plain","tag":""}]`
	got := goStructJSONFields(raw)
	if !got["id"] {
		t.Error("expected wire name 'id' from json tag")
	}
	if got["-"] || got["Secret"] {
		t.Error("json:\"-\" field must be skipped")
	}
	if !got["Plain"] {
		t.Error("untagged field should fall back to its Go name")
	}
}
