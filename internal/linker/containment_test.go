package linker

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// LinkContainment builds the service→file→declaration backbone and struct→method
// edges, resolving a method's receiver to the struct in its own package while
// refusing to cross-link a same-named struct in a different package.
func TestLinkContainment(t *testing.T) {
	nodes := []graph.Node{
		// Package a: struct User + a method on it (different file, same dir).
		{ID: "svc:a/user.go:struct:User:1", Type: graph.NodeTypeStruct, Label: "User", Service: "svc", File: "a/user.go"},
		{ID: "svc:a/methods.go:method:Save:5", Type: graph.NodeTypeMethod, Label: "Save", Service: "svc", File: "a/methods.go", Meta: map[string]string{"receiver": "*User"}},
		{ID: "svc:a/user.go:function:New:9", Type: graph.NodeTypeFunction, Label: "New", Service: "svc", File: "a/user.go"},
		// Package b: a same-named struct User — must NOT capture package a's method.
		{ID: "svc:b/user.go:struct:User:1", Type: graph.NodeTypeStruct, Label: "User", Service: "svc", File: "b/user.go"},
		// A templ component in another service.
		{ID: "web:views/home.templ:component:Home:3", Type: graph.NodeTypeComponent, Label: "Home", Service: "web", File: "views/home.templ", Language: "templ"},
		// A variable — not a contained type, must be ignored.
		{ID: "svc:a/user.go:variable:maxUsers:2", Type: graph.NodeTypeVariable, Label: "maxUsers", Service: "svc", File: "a/user.go"},
	}

	newNodes, edges := LinkContainment(nodes)

	// Synthetic nodes: 2 services + 4 files (a/user.go, a/methods.go, b/user.go, views/home.templ).
	var services, files int
	for _, n := range newNodes {
		switch n.Type {
		case graph.NodeTypeService:
			services++
		case graph.NodeTypeFile:
			files++
		}
	}
	if services != 2 {
		t.Errorf("service nodes = %d, want 2", services)
	}
	if files != 4 {
		t.Errorf("file nodes = %d, want 4", files)
	}

	type pair struct{ from, to string }
	got := map[pair]bool{}
	for _, e := range edges {
		if e.Type != graph.EdgeTypeContains {
			t.Fatalf("unexpected edge type %q", e.Type)
		}
		got[pair{e.From, e.To}] = true
	}

	// struct→method (same package) present.
	if !got[pair{"svc:a/user.go:struct:User:1", "svc:a/methods.go:method:Save:5"}] {
		t.Errorf("missing struct→method contains edge for package a")
	}
	// The package-b User must NOT contain package-a's method.
	if got[pair{"svc:b/user.go:struct:User:1", "svc:a/methods.go:method:Save:5"}] {
		t.Errorf("package-b struct wrongly captured package-a method")
	}
	// file→declaration for the method, function, struct, component.
	fileFor := func(service, file string) string { return service + ":" + file + ":file" }
	for _, want := range []pair{
		{fileFor("svc", "a/methods.go"), "svc:a/methods.go:method:Save:5"},
		{fileFor("svc", "a/user.go"), "svc:a/user.go:function:New:9"},
		{fileFor("svc", "a/user.go"), "svc:a/user.go:struct:User:1"},
		{fileFor("web", "views/home.templ"), "web:views/home.templ:component:Home:3"},
		{"service:svc", fileFor("svc", "a/user.go")},
		{"service:web", fileFor("web", "views/home.templ")},
	} {
		if !got[want] {
			t.Errorf("missing contains edge %s -> %s", want.from, want.to)
		}
	}
	// The variable must not be contained.
	if got[pair{fileFor("svc", "a/user.go"), "svc:a/user.go:variable:maxUsers:2"}] {
		t.Errorf("variable node wrongly contained")
	}
}
