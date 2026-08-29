package linker

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func typedCallRef(svc, file string, line int, name string) graph.UnresolvedRef {
	return graph.UnresolvedRef{Service: svc, File: file, Line: line, Name: name, Kind: "typed_call_ref"}
}

// TestSoleDefinerCalls_ResolvesUniqueName is DC.32's confirmed live shape: a
// typed-receiver call (`file.clear_lock!`) whose method name has exactly one
// definition anywhere in the service binds to it, even though the caller's
// own class has no ancestor path to that definition -- the receiver's real
// class is unknown, but with only one candidate in the whole service there
// is nothing else the call could reach.
func TestSoleDefinerCalls_ResolvesUniqueName(t *testing.T) {
	t.Parallel()
	lockFile := "/repo/app/models/org_file.rb"
	svcFile := "/repo/app/services/org_file_cleaner.rb"

	lockClass := mixinClass("orion", lockFile, "OrgFile", 1, 20)
	clearLock := mixinMethod("orion", lockFile, "OrgFile", "clear_lock!", 10, 12)

	cleanerClass := mixinClass("orion", svcFile, "OrgFileCleaner", 1, 20)
	cleanup := mixinMethod("orion", svcFile, "OrgFileCleaner", "cleanup", 5, 9)

	nodes := []graph.Node{lockClass, clearLock, cleanerClass, cleanup}
	refs := []graph.UnresolvedRef{
		typedCallRef("orion", svcFile, 7, "clear_lock!"),
	}

	got, resolved := LinkRubySoleDefinerCalls(nodes, nil, refs)
	require.Len(t, got, 1)
	assert.Equal(t, graph.EdgeTypeCalls, got[0].Type)
	assert.Equal(t, cleanup.ID, got[0].From)
	assert.Equal(t, clearLock.ID, got[0].To)
	assert.Equal(t, "sole_definer", got[0].Meta["via"])
	assert.True(t, resolved[RubyCallRefKey(svcFile, 7, "clear_lock!")])
}

// TestSoleDefinerCalls_AmbiguousNameLeftUnresolved proves two definitions of
// the same name in one service never bind -- the exact ambiguity static
// type inference would settle and a heuristic must not guess at.
func TestSoleDefinerCalls_AmbiguousNameLeftUnresolved(t *testing.T) {
	t.Parallel()
	fileA := "/repo/app/models/org_file.rb"
	fileB := "/repo/app/models/study_file.rb"
	svcFile := "/repo/app/services/org_file_cleaner.rb"

	classA := mixinClass("orion", fileA, "OrgFile", 1, 20)
	methodA := mixinMethod("orion", fileA, "OrgFile", "clear_lock!", 10, 12)
	classB := mixinClass("orion", fileB, "StudyFile", 1, 20)
	methodB := mixinMethod("orion", fileB, "StudyFile", "clear_lock!", 8, 10)

	cleanerClass := mixinClass("orion", svcFile, "OrgFileCleaner", 1, 20)
	cleanup := mixinMethod("orion", svcFile, "OrgFileCleaner", "cleanup", 5, 9)

	nodes := []graph.Node{classA, methodA, classB, methodB, cleanerClass, cleanup}
	refs := []graph.UnresolvedRef{
		typedCallRef("orion", svcFile, 7, "clear_lock!"),
	}

	got, resolved := LinkRubySoleDefinerCalls(nodes, nil, refs)
	assert.Empty(t, got)
	assert.False(t, resolved[RubyCallRefKey(svcFile, 7, "clear_lock!")])
}

// TestSoleDefinerCalls_CrossServiceNeverBinds: the same vendored-copy trap
// LinkRubyMixinMethods guards against (see its doc comment) applies here --
// byNameService is built per-service, so a name unique within orion but
// also defined in another service in the same workspace index must never
// cross the boundary.
func TestSoleDefinerCalls_CrossServiceNeverBinds(t *testing.T) {
	t.Parallel()
	lockFile := "/repo/app/models/org_file.rb"
	svcFile := "/repo/app/services/org_file_cleaner.rb"

	lockClass := mixinClass("otherSvc", lockFile, "OrgFile", 1, 20)
	clearLock := mixinMethod("otherSvc", lockFile, "OrgFile", "clear_lock!", 10, 12)

	cleanerClass := mixinClass("orion", svcFile, "OrgFileCleaner", 1, 20)
	cleanup := mixinMethod("orion", svcFile, "OrgFileCleaner", "cleanup", 5, 9)

	nodes := []graph.Node{lockClass, clearLock, cleanerClass, cleanup}
	refs := []graph.UnresolvedRef{
		typedCallRef("orion", svcFile, 7, "clear_lock!"),
	}

	got, _ := LinkRubySoleDefinerCalls(nodes, nil, refs)
	assert.Empty(t, got)
}

// TestSoleDefinerCalls_NoDefinitionLeavesUnresolved: a name with zero
// definitions anywhere (the common case -- a gem/framework method) must not
// panic or fabricate a match.
func TestSoleDefinerCalls_NoDefinitionLeavesUnresolved(t *testing.T) {
	t.Parallel()
	svcFile := "/repo/app/services/org_file_cleaner.rb"
	cleanerClass := mixinClass("orion", svcFile, "OrgFileCleaner", 1, 20)
	cleanup := mixinMethod("orion", svcFile, "OrgFileCleaner", "cleanup", 5, 9)

	nodes := []graph.Node{cleanerClass, cleanup}
	refs := []graph.UnresolvedRef{
		typedCallRef("orion", svcFile, 7, "some_gem_method"),
	}

	got, resolved := LinkRubySoleDefinerCalls(nodes, nil, refs)
	assert.Empty(t, got)
	assert.False(t, resolved[RubyCallRefKey(svcFile, 7, "some_gem_method")])
}
