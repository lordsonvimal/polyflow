package linker

import (
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
