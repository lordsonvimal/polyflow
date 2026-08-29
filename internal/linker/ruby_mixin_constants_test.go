package linker

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mixinConst builds a const variable node owned by class `owner`.
func mixinConst(svc, file, owner, name string, line int) graph.Node {
	return graph.Node{
		ID:       svc + ":" + file + ":variable:" + name + ":" + itoa(line),
		Type:     graph.NodeTypeVariable,
		Label:    name,
		Service:  svc,
		File:     file,
		Line:     line,
		Language: "ruby",
		Meta: map[string]string{
			"kind": "const", "scope": "module", "mutable": "false",
			"class": owner,
		},
	}
}

func mixinConstRef(svc, file string, line int, name string) graph.UnresolvedRef {
	return graph.UnresolvedRef{Service: svc, File: file, Line: line, Name: name, Kind: "const_ref"}
}

// TestMixinConstants_ResolvesThroughInclude is DC.31's confirmed live shape:
// a module defines a SCREAMING_CASE constant, a class mixes it in via
// `include`, and references it unqualified.
func TestMixinConstants_ResolvesThroughInclude(t *testing.T) {
	t.Parallel()
	constsFile := "/repo/lib/messaging/constants.rb"
	svcFile := "/repo/app/services/data_server_communicator_amqp.rb"

	mod := mixinClass("orion", constsFile, "Constants", 3, 60)
	mtDeleteAgr := mixinConst("orion", constsFile, "Constants", "MT_DELETE_AGR", 30)

	svc := mixinClass("orion", svcFile, "DataServerCommunicatorAmqp", 5, 1200)
	enqueue := mixinMethod("orion", svcFile, "DataServerCommunicatorAmqp", "enqueue_delete", 1160, 1165)

	nodes := []graph.Node{mod, mtDeleteAgr, svc, enqueue}
	edges := []graph.Edge{inheritsEdge(svc.ID, mod.ID, "mixin")}
	refs := []graph.UnresolvedRef{
		mixinConstRef("orion", svcFile, 1162, "MT_DELETE_AGR"),
	}

	got, resolved, ledger := LinkRubyMixinConstants(nodes, edges, refs)
	require.Len(t, got, 1)
	assert.Equal(t, graph.EdgeTypeReads, got[0].Type)
	assert.Equal(t, enqueue.ID, got[0].From)
	assert.Equal(t, mtDeleteAgr.ID, got[0].To)
	assert.Equal(t, "mixin_const", got[0].Meta["via"])
	assert.True(t, resolved[RubyCallRefKey(svcFile, 1162, "MT_DELETE_AGR")])
	assert.Empty(t, ledger)
}

// TestMixinConstants_NoMixinLeavesUnresolved: a class with no ancestor that
// defines the name must not resolve — guards against a false positive that
// would misattribute an edge to an unrelated same-named constant elsewhere
// in the service.
func TestMixinConstants_NoMixinLeavesUnresolved(t *testing.T) {
	t.Parallel()
	constsFile := "/repo/lib/messaging/constants.rb"
	svcFile := "/repo/app/services/unrelated_service.rb"

	mod := mixinClass("orion", constsFile, "Constants", 3, 60)
	mtDeleteAgr := mixinConst("orion", constsFile, "Constants", "MT_DELETE_AGR", 30)

	svc := mixinClass("orion", svcFile, "UnrelatedService", 5, 60)
	run := mixinMethod("orion", svcFile, "UnrelatedService", "run", 10, 20)

	nodes := []graph.Node{mod, mtDeleteAgr, svc, run}
	refs := []graph.UnresolvedRef{
		mixinConstRef("orion", svcFile, 12, "MT_DELETE_AGR"),
	}

	got, resolved, _ := LinkRubyMixinConstants(nodes, nil, refs)
	assert.Empty(t, got)
	assert.False(t, resolved[RubyCallRefKey(svcFile, 12, "MT_DELETE_AGR")])
}

// TestMixinConstants_CrossServiceNeverBinds: the vendored-copy trap (see
// LinkRubyMixinMethods' doc comment) applies identically to constants — an
// inherits edge crossing a service boundary must never bind, since no Ruby
// process can cross it. Kept as an assertion in emitConst; constructed here
// to prove the assertion actually holds (a phantom would multiply itself
// across every service that vendors the same constants file).
func TestMixinConstants_CrossServiceNeverBinds(t *testing.T) {
	t.Parallel()
	constsFile := "/repo/lib/messaging/constants.rb"
	svcFile := "/repo/app/services/data_server_communicator_amqp.rb"

	mod := mixinClass("otherSvc", constsFile, "Constants", 3, 60)
	mtDeleteAgr := mixinConst("otherSvc", constsFile, "Constants", "MT_DELETE_AGR", 30)

	svc := mixinClass("orion", svcFile, "DataServerCommunicatorAmqp", 5, 1200)
	enqueue := mixinMethod("orion", svcFile, "DataServerCommunicatorAmqp", "enqueue_delete", 1160, 1165)

	nodes := []graph.Node{mod, mtDeleteAgr, svc, enqueue}
	edges := []graph.Edge{inheritsEdge(svc.ID, mod.ID, "mixin")}
	refs := []graph.UnresolvedRef{
		mixinConstRef("orion", svcFile, 1162, "MT_DELETE_AGR"),
	}

	got, _, _ := LinkRubyMixinConstants(nodes, edges, refs)
	assert.Empty(t, got)
}
