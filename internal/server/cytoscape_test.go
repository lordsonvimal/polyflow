package server

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func TestToCytoscapeJSON_Empty(t *testing.T) {
	g := ToCytoscapeJSON(nil, nil)
	if g.Nodes == nil || len(g.Nodes) != 0 {
		t.Errorf("expected empty nodes slice, got %v", g.Nodes)
	}
	if g.Edges == nil || len(g.Edges) != 0 {
		t.Errorf("expected empty edges slice, got %v", g.Edges)
	}
}

func TestToCytoscapeJSON_NodeFields(t *testing.T) {
	nodes := []*graph.Node{
		{ID: "n1", Type: graph.NodeTypeFunction, Label: "myFunc", Service: "svc", File: "a/b.go", Line: 42},
	}
	g := ToCytoscapeJSON(nodes, nil)
	if len(g.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(g.Nodes))
	}
	d := g.Nodes[0].Data
	if d.ID != "n1" {
		t.Errorf("ID: want n1, got %s", d.ID)
	}
	if d.Label != "myFunc" {
		t.Errorf("Label: want myFunc, got %s", d.Label)
	}
	if d.Type != "function" {
		t.Errorf("Type: want function, got %s", d.Type)
	}
	if d.Service != "svc" {
		t.Errorf("Service: want svc, got %s", d.Service)
	}
	if d.File != "a/b.go" {
		t.Errorf("File: want a/b.go, got %s", d.File)
	}
	if d.Line != 42 {
		t.Errorf("Line: want 42, got %d", d.Line)
	}
}

func TestToCytoscapeJSON_EdgeSourceTarget(t *testing.T) {
	edges := []*graph.Edge{
		{ID: "e1", From: "n1", To: "n2", Type: graph.EdgeTypeCalls, Label: "invoke"},
	}
	g := ToCytoscapeJSON(nil, edges)
	if len(g.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(g.Edges))
	}
	d := g.Edges[0].Data
	if d.Source != "n1" {
		t.Errorf("Source: want n1, got %s", d.Source)
	}
	if d.Target != "n2" {
		t.Errorf("Target: want n2, got %s", d.Target)
	}
	if d.Type != "calls" {
		t.Errorf("Type: want calls, got %s", d.Type)
	}
	if d.Label != "invoke" {
		t.Errorf("Label: want invoke, got %s", d.Label)
	}
}
