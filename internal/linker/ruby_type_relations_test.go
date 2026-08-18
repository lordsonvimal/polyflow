package linker

import (
	"fmt"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// rubyClassNode builds a class node as the parser layer would emit it.
func rubyClassNode(service, file, label string, line int) graph.Node {
	return graph.Node{
		ID:       service + ":" + file + ":class:" + label,
		Type:     graph.NodeTypeClass,
		Label:    label,
		Service:  service,
		File:     file,
		Line:     line,
		Language: "ruby",
	}
}

// TestLinkRubyTypeRelations_NoCrossServiceBinding is the Tier K.7a regression
// guard.
//
// Ruby constant lookup is process-local: a class in service A can never inherit
// from, mix in, or instantiate a class defined in separately deployed service B.
// The resolver used to key its class table by bare constant name across the whole
// workspace, so when several Ruby repos each vendored their own copy of a shared
// lib (nextGen, nextGen-CDR-Agent and nextGen-SCE-Agent all ship a lib/dx.rb), a
// single `include Dx` bound to every copy — 221 phantom edges from one statement,
// 744 across the 8-service datascience fleet.
//
// Here svc-a and svc-b both define Dx and ApiBaseController. svc-a's consumer must
// bind only to svc-a's copies.
func TestLinkRubyTypeRelations_NoCrossServiceBinding(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	consumer := writeRuby(t, dir, "agent_messages_controller.rb", `
class AgentMessagesController < ApiBaseController
  include Dx

  def create
    Publisher.new
  end
end
`)
	// svc-a's own definitions live in separate files so they resolve cross-file,
	// not via the same-file shortcut.
	aDx := writeRuby(t, dir, "a_dx.rb", "module Dx\nend\n")
	aBase := writeRuby(t, dir, "a_api_base_controller.rb", "class ApiBaseController\nend\n")
	aPub := writeRuby(t, dir, "a_publisher.rb", "class Publisher\nend\n")

	// svc-b vendors its own identically-named copies.
	bDx := writeRuby(t, dir, "b_dx.rb", "module Dx\nend\n")
	bBase := writeRuby(t, dir, "b_api_base_controller.rb", "class ApiBaseController\nend\n")
	bPub := writeRuby(t, dir, "b_publisher.rb", "class Publisher\nend\n")

	nodes := []graph.Node{
		rubyClassNode("svc-a", aDx, "Dx", 1),
		rubyClassNode("svc-a", aBase, "ApiBaseController", 1),
		rubyClassNode("svc-a", aPub, "Publisher", 1),
		rubyClassNode("svc-b", bDx, "Dx", 1),
		rubyClassNode("svc-b", bBase, "ApiBaseController", 1),
		rubyClassNode("svc-b", bPub, "Publisher", 1),
	}
	serviceFiles := map[string][]string{
		"svc-a": {consumer, aDx, aBase, aPub},
		"svc-b": {bDx, bBase, bPub},
	}

	edges, _ := LinkRubyTypeRelations(nodes, serviceFiles)

	nodeService := make(map[string]string, len(nodes))
	for _, n := range nodes {
		nodeService[n.ID] = n.Service
	}

	if len(edges) == 0 {
		t.Fatal("no edges emitted: the resolver should still bind within svc-a")
	}

	boundLabels := map[string]bool{}
	for _, e := range edges {
		toSvc, ok := nodeService[e.To]
		if !ok {
			continue // target is a synthetic/other node
		}
		if toSvc != "svc-a" {
			t.Errorf("cross-service binding: %s edge %s -> %s (service %s); Ruby "+
				"constant lookup is process-local and must never cross a service",
				e.Type, e.From, e.To, toSvc)
		}
		boundLabels[e.To] = true
	}

	// Recall guard: the three same-service bindings must all still be produced,
	// so the fix is scoping and not blanket suppression.
	for _, want := range []string{
		"svc-a:" + aDx + ":class:Dx",
		"svc-a:" + aBase + ":class:ApiBaseController",
		"svc-a:" + aPub + ":class:Publisher",
	} {
		if !boundLabels[want] {
			t.Errorf("missing same-service binding to %s", want)
		}
	}
}

// TestLinkRubyTypeRelations_UnresolvedIsLedgered pins the other half of the K.7a
// contract: when a constant has no definition in the referencing service, the
// reference is ledgered rather than bound to a same-named class elsewhere. An
// `inherits_unresolved` entry is the correct outcome — a cross-service edge is not.
func TestLinkRubyTypeRelations_UnresolvedIsLedgered(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	consumer := writeRuby(t, dir, "only_consumer.rb", "class Widget < RemoteOnlyBase\nend\n")
	bBase := writeRuby(t, dir, "b_remote_only_base.rb", "class RemoteOnlyBase\nend\n")

	nodes := []graph.Node{
		rubyClassNode("svc-b", bBase, "RemoteOnlyBase", 1),
	}
	serviceFiles := map[string][]string{
		"svc-a": {consumer},
		"svc-b": {bBase},
	}

	edges, unresolved := LinkRubyTypeRelations(nodes, serviceFiles)

	for _, e := range edges {
		if e.To == "svc-b:"+bBase+":class:RemoteOnlyBase" {
			t.Fatalf("bound across services to %s instead of ledgering", e.To)
		}
	}

	found := false
	for _, u := range unresolved {
		if u.Name == "RemoteOnlyBase" && u.Kind == "inherits_unresolved" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an inherits_unresolved ledger entry for RemoteOnlyBase, got %+v", unresolved)
	}
}

// rubyClassNodeAt builds a class node with the ID the parser really mints,
// which carries the declaration line. The line is what joins a node to the
// namespace the scan recovered for it, so a test that elides it is not
// exercising the lookup.
func rubyClassNodeAt(service, file, label string, line int) graph.Node {
	n := rubyClassNode(service, file, label, line)
	n.ID = fmt.Sprintf("%s:%s:class:%s:%d", service, file, label, line)
	return n
}

// targetsOf returns the labels an edge from fromID points at.
func targetsOf(t *testing.T, edges []graph.Edge, nodes []graph.Node, fromID string) []string {
	t.Helper()
	label := map[string]string{}
	for _, n := range nodes {
		label[n.ID] = n.Label
	}
	var out []string
	for _, e := range edges {
		if e.From == fromID {
			out = append(out, label[e.To])
		}
	}
	return out
}

// TestLinkRubyTypeRelations_SuperclassResolvesLexically is the regression guard
// for the simple-name trap.
//
// Scoping the class table to a service (K.7a) stopped constants binding across
// repos, but within one service the table was still keyed by bare name and the
// superclass reference was reduced to its *last component* before lookup. So the
// two RepositoryControllers nextGen ships — one top level, one under
// ClientApi::V1 — were indistinguishable, and every subclass of either bound to
// both. What reaches the graph is then a class that inherits from two unrelated
// hierarchies at once, and downstream passes that walk ancestors (the Rails
// filter chain, which is where this was found) follow whichever comes first.
//
// Ruby resolves innermost enclosing namespace outward, so each subclass here has
// exactly one correct answer and they are different answers.
func TestLinkRubyTypeRelations_SuperclassResolvesLexically(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	topBase := writeRuby(t, dir, "repository_controller.rb", `
class RepositoryController
end
`)
	apiBase := writeRuby(t, dir, "v1_repository_controller.rb", `
module ClientApi
  module V1
    class RepositoryController
    end
  end
end
`)
	topSub := writeRuby(t, dir, "documents_controller.rb", `
class DocumentsController < RepositoryController
end
`)
	apiSub := writeRuby(t, dir, "v1_files_controller.rb", `
module ClientApi
  module V1
    class FilesController < RepositoryController
    end
  end
end
`)

	nodes := []graph.Node{
		rubyClassNodeAt("svc", topBase, "RepositoryController", 2),
		rubyClassNodeAt("svc", apiBase, "RepositoryController", 4),
		rubyClassNodeAt("svc", topSub, "DocumentsController", 2),
		rubyClassNodeAt("svc", apiSub, "FilesController", 4),
	}
	edges, _ := LinkRubyTypeRelations(nodes, map[string][]string{
		"svc": {topBase, apiBase, topSub, apiSub},
	})

	for _, tc := range []struct {
		from, want string
	}{
		{fmt.Sprintf("svc:%s:class:DocumentsController:2", topSub), topBase},
		{fmt.Sprintf("svc:%s:class:FilesController:4", apiSub), apiBase},
	} {
		var got []string
		for _, e := range edges {
			if e.From == tc.from && e.Type == graph.EdgeTypeInherits {
				got = append(got, e.To)
			}
		}
		wantID := ""
		for _, n := range nodes {
			if n.File == tc.want {
				wantID = n.ID
			}
		}
		if len(got) != 1 || got[0] != wantID {
			t.Errorf("%s inherits %v; want exactly [%s] — the other "+
				"RepositoryController is in a namespace this class is not in",
				tc.from, got, wantID)
		}
	}
}

// TestLinkRubyTypeRelations_QualifiedReferenceKeepsEveryComponent covers the
// half of the trap that was pure information loss: a `scope_resolution`
// superclass was read for its last component only, so
// `ClientApi::V1::ApiBaseController` was indistinguishable from a top-level
// `ApiBaseController` even though the source spells out which one it means.
func TestLinkRubyTypeRelations_QualifiedReferenceKeepsEveryComponent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	topBase := writeRuby(t, dir, "api_base_controller.rb", "class ApiBaseController\nend\n")
	nsBase := writeRuby(t, dir, "v1_api_base_controller.rb", `
module ClientApi
  module V1
    class ApiBaseController
    end
  end
end
`)
	sub := writeRuby(t, dir, "agents_controller.rb", `
class AgentsController < ClientApi::V1::ApiBaseController
end
`)

	nodes := []graph.Node{
		rubyClassNodeAt("svc", topBase, "ApiBaseController", 1),
		rubyClassNodeAt("svc", nsBase, "ApiBaseController", 4),
		rubyClassNodeAt("svc", sub, "AgentsController", 2),
	}
	edges, _ := LinkRubyTypeRelations(nodes, map[string][]string{
		"svc": {topBase, nsBase, sub},
	})

	from := fmt.Sprintf("svc:%s:class:AgentsController:2", sub)
	var got []string
	for _, e := range edges {
		if e.From == from {
			got = append(got, e.To)
		}
	}
	want := fmt.Sprintf("svc:%s:class:ApiBaseController:4", nsBase)
	if len(got) != 1 || got[0] != want {
		t.Errorf("AgentsController inherits %v; want exactly [%s] — the source "+
			"names the namespaced base outright", got, want)
	}
}

// TestLinkRubyTypeRelations_MixinResolvesInsideTheBody pins the namespace a
// mixin is looked up in: `include Dx` sits in the class *body*, so it resolves
// with the class itself on the nesting, not outside it.
func TestLinkRubyTypeRelations_MixinResolvesInsideTheBody(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	topDx := writeRuby(t, dir, "dx.rb", "module Dx\nend\n")
	nsDx := writeRuby(t, dir, "v1_dx.rb", `
module ClientApi
  module V1
    module Dx
    end
  end
end
`)
	consumer := writeRuby(t, dir, "v1_agents_controller.rb", `
module ClientApi
  module V1
    class AgentsController
      include Dx
    end
  end
end
`)

	nodes := []graph.Node{
		rubyClassNodeAt("svc", topDx, "Dx", 1),
		rubyClassNodeAt("svc", nsDx, "Dx", 4),
		rubyClassNodeAt("svc", consumer, "AgentsController", 4),
	}
	edges, _ := LinkRubyTypeRelations(nodes, map[string][]string{
		"svc": {topDx, nsDx, consumer},
	})

	got := targetsOf(t, edges, nodes, fmt.Sprintf("svc:%s:class:AgentsController:4", consumer))
	if len(got) != 1 {
		t.Fatalf("include Dx bound to %d definitions %v; ClientApi::V1::Dx is "+
			"the only one on the nesting", len(got), got)
	}
	for _, e := range edges {
		if e.To == fmt.Sprintf("svc:%s:class:Dx:1", topDx) {
			t.Errorf("bound to the top-level Dx, which the nesting shadows")
		}
	}
}

// TestLinkRubyTypeRelations_AmbiguousNameStaysAmbiguous keeps the fix from
// turning into a first-match guess. When lexical resolution finds nothing — the
// constant is declared in neither the enclosing namespaces nor at top level —
// the simple-name fallback is all that is left, and two same-named definitions
// must produce two candidate edges and a ledger entry rather than one confident
// wrong edge (bug-class #1: fan out, never first-match).
func TestLinkRubyTypeRelations_AmbiguousNameStaysAmbiguous(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	a := writeRuby(t, dir, "a_helper.rb", `
module Alpha
  class Helper
  end
end
`)
	b := writeRuby(t, dir, "b_helper.rb", `
module Beta
  class Helper
  end
end
`)
	consumer := writeRuby(t, dir, "widget.rb", `
module Gamma
  class Widget < Helper
  end
end
`)

	nodes := []graph.Node{
		rubyClassNodeAt("svc", a, "Helper", 3),
		rubyClassNodeAt("svc", b, "Helper", 3),
		rubyClassNodeAt("svc", consumer, "Widget", 3),
	}
	edges, unresolved := LinkRubyTypeRelations(nodes, map[string][]string{
		"svc": {a, b, consumer},
	})

	got := targetsOf(t, edges, nodes, fmt.Sprintf("svc:%s:class:Widget:3", consumer))
	if len(got) != 2 {
		t.Errorf("got %d candidate edges %v; both Helpers are equally plausible "+
			"once the nesting rules neither in", len(got), got)
	}
	for _, e := range edges {
		if e.Confidence != graph.ConfidencePartial {
			t.Errorf("candidate edge %s has confidence %q; want partial", e.ID, e.Confidence)
		}
	}
	found := false
	for _, u := range unresolved {
		if u.Name == "Helper" {
			found = true
		}
	}
	if !found {
		t.Errorf("ambiguous Helper was not ledgered, got %+v", unresolved)
	}
}
