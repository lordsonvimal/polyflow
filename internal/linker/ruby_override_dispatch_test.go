package linker

import (
	"fmt"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func edgeTos(edges []graph.Edge) []string {
	out := make([]string, len(edges))
	for i, e := range edges {
		out[i] = e.To
	}
	return out
}

// TestOverrideDispatch_BaseClassCallFansOutToAllSubclassOverrides is shape 1:
// UserProvisioning::Products::Base#perform calls perform_create (bare,
// implicit self) and three subclasses each override it — none showed a
// caller before this pass. Bug-class #1 applies: three edges, not one guess.
func TestOverrideDispatch_BaseClassCallFansOutToAllSubclassOverrides(t *testing.T) {
	t.Parallel()
	svc := "orion"
	baseFile := "/repo/app/services/products/base.rb"
	base := mixinClass(svc, baseFile, "Base", 1, 10)
	perform := mixinMethod(svc, baseFile, "Base", "perform", 3, 6)

	sub1File := "/repo/app/services/products/willow.rb"
	sub1 := mixinClass(svc, sub1File, "Willow", 1, 10)
	sub1M := mixinMethod(svc, sub1File, "Willow", "perform_create", 3, 5)

	sub2File := "/repo/app/services/products/vega_lyra.rb"
	sub2 := mixinClass(svc, sub2File, "VegaLyra", 1, 10)
	sub2M := mixinMethod(svc, sub2File, "VegaLyra", "perform_create", 3, 5)

	sub3File := "/repo/app/services/products/mdr.rb"
	sub3 := mixinClass(svc, sub3File, "Mdr", 1, 10)
	sub3M := mixinMethod(svc, sub3File, "Mdr", "perform_create", 3, 5)

	nodes := []graph.Node{base, perform, sub1, sub1M, sub2, sub2M, sub3, sub3M}
	edges := []graph.Edge{
		inheritsEdge(sub1.ID, base.ID, "superclass"),
		inheritsEdge(sub2.ID, base.ID, "superclass"),
		inheritsEdge(sub3.ID, base.ID, "superclass"),
	}
	refs := []graph.UnresolvedRef{callRef(svc, baseFile, 4, "perform_create")}

	got, resolved, ledger := LinkRubyOverrideDispatch(nodes, edges, refs)
	require.Len(t, got, 3)
	assert.ElementsMatch(t, []string{sub1M.ID, sub2M.ID, sub3M.ID}, edgeTos(got))
	for _, e := range got {
		assert.Equal(t, perform.ID, e.From)
		assert.Equal(t, graph.EdgeTypeCalls, e.Type)
		assert.Equal(t, "override_dispatch", e.Meta["via"])
	}
	assert.Empty(t, ledger)
	assert.True(t, resolved[RubyCallRefKey(baseFile, 4, "perform_create")])
}

// TestOverrideDispatch_BaseDefinesItsOwnStubButSubclassesStillFanOut is
// shape 1's ACTUAL real-world case, confirmed live against orion-atlas:
// Base does not leave perform_create abstract via a call_ref miss, it
// declares its own `raise NotImplementedError` stub — extractRubyVariables
// already resolves that same-file, same-class call to a real edge before
// this pass runs, so unlike the synthetic call_ref fixture above, there is
// no unresolved ledger entry to scan at all. This is the "in addition to A's
// own foo" input source (already-resolved same-file self-call edges), not
// the call_ref source.
func TestOverrideDispatch_BaseDefinesItsOwnStubButSubclassesStillFanOut(t *testing.T) {
	t.Parallel()
	svc := "orion"
	baseFile := "/repo/app/services/products/base.rb"
	base := mixinClass(svc, baseFile, "Base", 1, 20)
	perform := mixinMethod(svc, baseFile, "Base", "perform", 3, 6)
	performCreate := mixinMethod(svc, baseFile, "Base", "perform_create", 8, 10)

	subFile := "/repo/app/services/products/willow.rb"
	sub := mixinClass(svc, subFile, "Willow", 1, 10)
	subM := mixinMethod(svc, subFile, "Willow", "perform_create", 3, 5)

	nodes := []graph.Node{base, perform, performCreate, sub, subM}
	edges := []graph.Edge{
		inheritsEdge(sub.ID, base.ID, "superclass"),
		// The same-file self-call edge extractRubyVariables already emits —
		// nil Meta is resolveBareCall's signature, not an incidental omission.
		{ID: "calls:" + perform.ID + "->" + performCreate.ID, From: perform.ID, To: performCreate.ID, Type: graph.EdgeTypeCalls},
	}

	got, _, ledger := LinkRubyOverrideDispatch(nodes, edges, nil)
	require.Len(t, got, 1)
	assert.Equal(t, perform.ID, got[0].From)
	assert.Equal(t, subM.ID, got[0].To)
	assert.Equal(t, "override_dispatch", got[0].Meta["via"])
	assert.Empty(t, ledger)
}

// TestOverrideDispatch_SameFileLiteralConstantCallNeverFansOut pins the
// boundary the same-file-resolved-edge input source must respect: a same
// file `OtherClass.method` call also resolves through resolveBareCall (nil
// Meta), but the caller's and target's owning classes differ, so this must
// not be mistaken for a self-call and must never fan out.
func TestOverrideDispatch_SameFileLiteralConstantCallNeverFansOut(t *testing.T) {
	t.Parallel()
	svc := "orion"
	file := "/repo/app/services/thing.rb"
	caller := mixinClass(svc, file, "Caller", 1, 6)
	callerM := mixinMethod(svc, file, "Caller", "run", 3, 5)
	other := mixinClass(svc, file, "Other", 8, 14)
	otherM := mixinMethod(svc, file, "Other", "helper", 10, 12)

	subFile := "/repo/app/services/other_sub.rb"
	sub := mixinClass(svc, subFile, "OtherSub", 1, 10)
	subM := mixinMethod(svc, subFile, "OtherSub", "helper", 3, 5)

	nodes := []graph.Node{caller, callerM, other, otherM, sub, subM}
	edges := []graph.Edge{
		inheritsEdge(sub.ID, other.ID, "superclass"),
		{ID: "calls:" + callerM.ID + "->" + otherM.ID, From: callerM.ID, To: otherM.ID, Type: graph.EdgeTypeCalls},
	}

	got, _, _ := LinkRubyOverrideDispatch(nodes, edges, nil)
	assert.Empty(t, got, "an explicit different-class same-file call must not fan out")
}

// TestOverrideDispatch_ConcernSelfCallFansOutToAllIncluderOverrides is shape
// 2: a concern calls self.foo where multiple *includers* (not subclasses)
// override foo — the mixin case must fan out exactly like the inheritance
// case does.
func TestOverrideDispatch_ConcernSelfCallFansOutToAllIncluderOverrides(t *testing.T) {
	t.Parallel()
	svc := "orion"
	concernFile := "/repo/app/models/concerns/audit_commentable.rb"
	concern := mixinClass(svc, concernFile, "AuditCommentable", 1, 10)
	auditComment := mixinMethod(svc, concernFile, "AuditCommentable", "audit_comment", 3, 6)

	orderFile := "/repo/app/models/order.rb"
	order := mixinClass(svc, orderFile, "Order", 1, 10)
	orderM := mixinMethod(svc, orderFile, "Order", "audit_record_identifier", 3, 5)

	invoiceFile := "/repo/app/models/invoice.rb"
	invoice := mixinClass(svc, invoiceFile, "Invoice", 1, 10)
	invoiceM := mixinMethod(svc, invoiceFile, "Invoice", "audit_record_identifier", 3, 5)

	nodes := []graph.Node{concern, auditComment, order, orderM, invoice, invoiceM}
	edges := []graph.Edge{
		inheritsEdge(order.ID, concern.ID, "mixin"),
		inheritsEdge(invoice.ID, concern.ID, "mixin"),
	}
	refs := []graph.UnresolvedRef{callRef(svc, concernFile, 4, "audit_record_identifier")}

	got, resolved, ledger := LinkRubyOverrideDispatch(nodes, edges, refs)
	require.Len(t, got, 2)
	assert.ElementsMatch(t, []string{orderM.ID, invoiceM.ID}, edgeTos(got))
	assert.Empty(t, ledger)
	assert.True(t, resolved[RubyCallRefKey(concernFile, 4, "audit_record_identifier")])
}

// TestOverrideDispatch_TypedLocalVariableReceiverFansOut is shape 3:
// `user.mini_orange_sync_action` where `user` is a plain local variable of
// inferred type User (not self). This shape must be fed through
// ruby_receiver_types.go's existing type inference and emitClassMethodCall's
// fan-out hook, not a second resolution mechanism — so this exercises
// LinkRubyReceiverTypeCalls end to end (real Ruby source), not
// LinkRubyOverrideDispatch directly.
func TestOverrideDispatch_TypedLocalVariableReceiverFansOut(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	syncJobFile := writeRuby(t, dir, "sync_job.rb", `
class SyncJob
  def run
    user = User.new
    user.mini_orange_sync_action
  end
end
`)
	userFile := writeRuby(t, dir, "user.rb", `
class User
  def mini_orange_sync_action
    false
  end
end
`)
	adminFile := writeRuby(t, dir, "admin_user.rb", `
class AdminUser < User
  def mini_orange_sync_action
    true
  end
end
`)

	// writeRuby's fixture bodies open with a leading newline (matching this
	// package's other source-based tests), so line 1 is blank and `class`
	// lands on line 2.
	svc := "svc"
	syncJob := mixinClass(svc, syncJobFile, "SyncJob", 2, 7)
	run := mixinMethod(svc, syncJobFile, "SyncJob", "run", 3, 6)
	user := mixinClass(svc, userFile, "User", 2, 6)
	userM := mixinMethod(svc, userFile, "User", "mini_orange_sync_action", 3, 5)
	admin := mixinClass(svc, adminFile, "AdminUser", 2, 6)
	adminM := mixinMethod(svc, adminFile, "AdminUser", "mini_orange_sync_action", 3, 5)

	nodes := []graph.Node{syncJob, run, user, userM, admin, adminM}
	edges := []graph.Edge{inheritsEdge(admin.ID, user.ID, "superclass")}
	serviceFiles := map[string][]string{svc: {syncJobFile, userFile, adminFile}}

	got, unresolved := LinkRubyReceiverTypeCalls(nodes, edges, serviceFiles)

	require.Contains(t, edgeTos(got), userM.ID, "the ordinary receiver-typed edge must still resolve: got=%v unresolved=%v", got, unresolved)
	require.Contains(t, edgeTos(got), adminM.ID, "AdminUser's override must also be reachable from the same call site")

	var overrideEdge *graph.Edge
	for i := range got {
		if got[i].To == adminM.ID {
			overrideEdge = &got[i]
		}
	}
	require.NotNil(t, overrideEdge)
	assert.Equal(t, run.ID, overrideEdge.From)
	assert.Equal(t, "override_dispatch", overrideEdge.Meta["via"])
}

// TestOverrideDispatch_LiteralClassMethodCallNeverFansOut pins the boundary:
// an explicit `Const.method` call (LinkRubyClassMethodCalls, a literal
// receiver) names an exact class with no runtime-polymorphism ambiguity, so
// it must never gain override edges even when the named class has
// overriding subclasses — only an inferred receiver's dispatch is genuinely
// ambiguous.
func TestOverrideDispatch_LiteralClassMethodCallNeverFansOut(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	callerFile := writeRuby(t, dir, "job.rb", `
class SyncJob
  def run
    User.mini_orange_sync_action
  end
end
`)
	userFile := writeRuby(t, dir, "user.rb", `
class User
  def self.mini_orange_sync_action
    false
  end
end
`)
	adminFile := writeRuby(t, dir, "admin_user.rb", `
class AdminUser < User
  def self.mini_orange_sync_action
    true
  end
end
`)

	svc := "svc"
	syncJob := mixinClass(svc, callerFile, "SyncJob", 2, 6)
	run := mixinMethod(svc, callerFile, "SyncJob", "run", 3, 5)
	user := mixinClass(svc, userFile, "User", 2, 5)
	userM := mixinMethod(svc, userFile, "User", "mini_orange_sync_action", 3, 4)
	admin := mixinClass(svc, adminFile, "AdminUser", 2, 5)
	adminM := mixinMethod(svc, adminFile, "AdminUser", "mini_orange_sync_action", 3, 4)

	nodes := []graph.Node{syncJob, run, user, userM, admin, adminM}
	serviceFiles := map[string][]string{svc: {callerFile, userFile, adminFile}}

	got, _ := LinkRubyClassMethodCalls(nodes, serviceFiles)

	require.Contains(t, edgeTos(got), userM.ID)
	assert.NotContains(t, edgeTos(got), adminM.ID, "a literal Const.method call must not fan out to a subclass override")
}

// TestOverrideDispatch_FanoutCapLedgersExcessInsteadOfSprayingEdges is the
// deliberately-wide-hierarchy case: more overrides than rubyOverrideFanoutCap
// must ledger the whole set rather than silently truncating or crashing —
// mirroring templ_layer.go's maxClassFanout precedent (suppress and ledger,
// don't spray).
func TestOverrideDispatch_FanoutCapLedgersExcessInsteadOfSprayingEdges(t *testing.T) {
	t.Parallel()
	svc := "orion"
	baseFile := "/repo/app/services/base.rb"
	base := mixinClass(svc, baseFile, "Base", 1, 10)
	perform := mixinMethod(svc, baseFile, "Base", "perform", 3, 6)

	nodes := []graph.Node{base, perform}
	var edges []graph.Edge
	for i := 0; i < rubyOverrideFanoutCap+1; i++ {
		file := fmt.Sprintf("/repo/app/services/sub_%d.rb", i)
		label := fmt.Sprintf("Sub%d", i)
		sub := mixinClass(svc, file, label, 1, 10)
		subM := mixinMethod(svc, file, label, "perform_create", 3, 5)
		nodes = append(nodes, sub, subM)
		edges = append(edges, inheritsEdge(sub.ID, base.ID, "superclass"))
	}
	refs := []graph.UnresolvedRef{callRef(svc, baseFile, 4, "perform_create")}

	got, resolved, ledger := LinkRubyOverrideDispatch(nodes, edges, refs)
	assert.Empty(t, got, "the cap must suppress edges, not spray rubyOverrideFanoutCap+1 of them")
	require.Len(t, ledger, 1)
	assert.Equal(t, "ruby_override_fanout_capped", ledger[0].Kind)
	assert.True(t, resolved[RubyCallRefKey(baseFile, 4, "perform_create")],
		"the raw call_ref must still be explained (by the capped ledger entry), not left as a plain unresolved miss")
}

// TestOverrideDispatch_NoDanglingEdges pins the bug-class check
// docs/rails-devise-gem-plan.md's DV.2 fixture work established as the norm
// for synthesized-edge phases: every emitted edge's To must be a real node ID.
func TestOverrideDispatch_NoDanglingEdges(t *testing.T) {
	t.Parallel()
	svc := "orion"
	baseFile := "/repo/app/services/base.rb"
	base := mixinClass(svc, baseFile, "Base", 1, 10)
	perform := mixinMethod(svc, baseFile, "Base", "perform", 3, 6)
	subFile := "/repo/app/services/sub.rb"
	sub := mixinClass(svc, subFile, "Sub", 1, 10)
	subM := mixinMethod(svc, subFile, "Sub", "perform_create", 3, 5)

	nodes := []graph.Node{base, perform, sub, subM}
	edges := []graph.Edge{inheritsEdge(sub.ID, base.ID, "superclass")}
	refs := []graph.UnresolvedRef{callRef(svc, baseFile, 4, "perform_create")}

	got, _, _ := LinkRubyOverrideDispatch(nodes, edges, refs)
	require.NotEmpty(t, got)

	known := map[string]bool{}
	for _, n := range nodes {
		known[n.ID] = true
	}
	for _, e := range got {
		assert.True(t, known[e.From], "dangling From: %s", e.From)
		assert.True(t, known[e.To], "dangling To: %s", e.To)
	}
}
