package linker

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// classCallFuncNode builds a method node owned by class `owner`, the shape
// extractRubyVariables emits.
func classCallFuncNode(svc, file, owner, name string, line int) graph.Node {
	return graph.Node{
		ID:       svc + ":" + file + ":function:" + name + ":" + itoa(line),
		Type:     graph.NodeTypeFunction,
		Label:    name,
		Service:  svc,
		File:     file,
		Line:     line,
		Language: "ruby",
		Meta:     map[string]string{"class": owner},
	}
}

// TestLinkRubyClassMethodCalls_ResolvesSelfMethod covers the concrete gap
// this pass exists for: UserCategoryRuleSet.latest_for, called from
// LicenseReportJobsController#create, where UserCategoryRuleSet lives in a
// different file. It declares a `self.` method by that name, so the edge
// lands on the method, not just the class.
func TestLinkRubyClassMethodCalls_ResolvesSelfMethod(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	controller := writeRuby(t, dir, "license_report_jobs_controller.rb", `
class LicenseReportJobsController
  def create
    UserCategoryRuleSet.latest_for(1, 2)
  end
end
`)
	ruleSet := writeRuby(t, dir, "user_category_rule_set.rb", `
class UserCategoryRuleSet
  def self.latest_for(org_id, product_id)
    true
  end
end
`)

	create := classCallFuncNode("svc", controller, "LicenseReportJobsController", "create", 3)
	nodes := []graph.Node{
		rubyClassNode("svc", controller, "LicenseReportJobsController", 2),
		create,
		rubyClassNode("svc", ruleSet, "UserCategoryRuleSet", 2),
		classCallFuncNode("svc", ruleSet, "UserCategoryRuleSet", "latest_for", 3),
	}
	serviceFiles := map[string][]string{"svc": {controller, ruleSet}}

	edges, unresolved := LinkRubyClassMethodCalls(nodes, serviceFiles)

	var got *graph.Edge
	for i := range edges {
		if edges[i].From == create.ID && edges[i].Type == graph.EdgeTypeCalls {
			got = &edges[i]
		}
	}
	if got == nil {
		t.Fatalf("missing calls edge from create; edges: %+v", edges)
	}
	wantTo := "svc:" + ruleSet + ":function:latest_for:3"
	if got.To != wantTo {
		t.Errorf("edge should land on the latest_for method node, got To=%q want %q", got.To, wantTo)
	}
	if got.Meta["granularity"] == "class" {
		t.Errorf("a resolvable method must not fall back to class granularity: %+v", got)
	}
	if len(unresolved) != 0 {
		t.Errorf("an unambiguous resolution must not be ledgered: %+v", unresolved)
	}
}

// TestLinkRubyClassMethodCalls_FrameworkCallFallsBackToClass covers
// Product.find_by: Product is a real, cross-file model class, but no
// repository defines `find_by` — it's an ActiveRecord finder. The edge must
// still land, at class granularity, so blast radius reaches the model.
func TestLinkRubyClassMethodCalls_FrameworkCallFallsBackToClass(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	controller := writeRuby(t, dir, "license_report_jobs_controller.rb", `
class LicenseReportJobsController
  def create
    Product.find_by(slug: "vega-lyra")
  end
end
`)
	product := writeRuby(t, dir, "product.rb", "class Product\nend\n")

	create := classCallFuncNode("svc", controller, "LicenseReportJobsController", "create", 3)
	productClass := rubyClassNode("svc", product, "Product", 1)
	nodes := []graph.Node{
		rubyClassNode("svc", controller, "LicenseReportJobsController", 2),
		create,
		productClass,
	}
	serviceFiles := map[string][]string{"svc": {controller, product}}

	edges, _ := LinkRubyClassMethodCalls(nodes, serviceFiles)

	var got *graph.Edge
	for i := range edges {
		if edges[i].From == create.ID && edges[i].To == productClass.ID {
			got = &edges[i]
		}
	}
	if got == nil {
		t.Fatalf("missing class-granularity calls edge create -> Product; edges: %+v", edges)
	}
	if got.Meta["granularity"] != "class" || got.Meta["method"] != "find_by" {
		t.Errorf("class-granularity edge meta wrong: %+v", got.Meta)
	}
}

// TestLinkRubyClassMethodCalls_SameFileNotDuplicated guards against this
// pass re-emitting an edge extractRubyVariables' same-file fix already owns.
func TestLinkRubyClassMethodCalls_SameFileNotDuplicated(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	file := writeRuby(t, dir, "orders_controller.rb", `
class OrdersController
  def create
    OrderPolicy.allowed?(self)
  end
end

class OrderPolicy
  def self.allowed?(ctx)
    true
  end
end
`)

	nodes := []graph.Node{
		rubyClassNode("svc", file, "OrdersController", 2),
		classCallFuncNode("svc", file, "OrdersController", "create", 3),
		rubyClassNode("svc", file, "OrderPolicy", 8),
		classCallFuncNode("svc", file, "OrderPolicy", "allowed?", 9),
	}
	serviceFiles := map[string][]string{"svc": {file}}

	edges, _ := LinkRubyClassMethodCalls(nodes, serviceFiles)

	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls {
			t.Errorf("same-file constant-receiver call must be left to extractRubyVariables: %+v", e)
		}
	}
}

// TestLinkRubyClassMethodCalls_UnknownReceiverNotLedgered covers a receiver
// that never resolves to a class this service declares — Rails/gem code like
// `Rails.logger.info`. No edge, and critically no ledger noise either: this
// pass isn't the owner of misses it can never explain.
func TestLinkRubyClassMethodCalls_UnknownReceiverNotLedgered(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	controller := writeRuby(t, dir, "orders_controller.rb", `
class OrdersController
  def create
    Rails.logger.info("creating")
  end
end
`)

	nodes := []graph.Node{
		rubyClassNode("svc", controller, "OrdersController", 2),
		classCallFuncNode("svc", controller, "OrdersController", "create", 3),
	}
	serviceFiles := map[string][]string{"svc": {controller}}

	edges, unresolved := LinkRubyClassMethodCalls(nodes, serviceFiles)
	if len(edges) != 0 {
		t.Errorf("unresolvable receiver must not emit an edge: %+v", edges)
	}
	if len(unresolved) != 0 {
		t.Errorf("unresolvable receiver must not be ledgered by this pass: %+v", unresolved)
	}
}

// TestLinkRubyClassMethodCalls_NoCrossServiceBinding mirrors the Tier K.7a
// guard on LinkRubyTypeRelations: Ruby constant lookup is process-local, so a
// call in service A must never resolve to a class in service B, even when B
// vendors an identically-named/identically-shaped copy.
func TestLinkRubyClassMethodCalls_NoCrossServiceBinding(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	consumer := writeRuby(t, dir, "jobs_controller.rb", `
class JobsController
  def create
    LicenseReportJob.create!(name: "x")
  end
end
`)
	aJob := writeRuby(t, dir, "a_license_report_job.rb", "class LicenseReportJob\nend\n")
	bJob := writeRuby(t, dir, "b_license_report_job.rb", "class LicenseReportJob\nend\n")

	create := classCallFuncNode("svc-a", consumer, "JobsController", "create", 3)
	aJobClass := rubyClassNode("svc-a", aJob, "LicenseReportJob", 1)
	nodes := []graph.Node{
		rubyClassNode("svc-a", consumer, "JobsController", 2),
		create,
		aJobClass,
		rubyClassNode("svc-b", bJob, "LicenseReportJob", 1),
	}
	serviceFiles := map[string][]string{
		"svc-a": {consumer, aJob},
		"svc-b": {bJob},
	}

	edges, _ := LinkRubyClassMethodCalls(nodes, serviceFiles)

	if len(edges) != 1 {
		t.Fatalf("expected exactly one edge (bound to svc-a's own LicenseReportJob), got %d: %+v", len(edges), edges)
	}
	if edges[0].To != aJobClass.ID {
		t.Errorf("edge must bind to svc-a's LicenseReportJob, got To=%q", edges[0].To)
	}
}
