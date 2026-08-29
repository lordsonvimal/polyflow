package parser

import (
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
)

const rubyVarFixture = `MAX_RETRIES = 3

class OrdersController
  attr_accessor :cart, :user
  attr_reader :status

  def create
    @order = Order.new
    @@count = 0
  end

  def show
    render json: @order
  end
end
`

func parseRubyVarFixture(t *testing.T) ([]graph.Node, []graph.Edge) {
	t.Helper()
	nodes, edges, _ := extractRubyVariables("app/controllers/orders_controller.rb", "shop", []byte(rubyVarFixture))
	return nodes, edges
}

func TestRubyVariables_Constants(t *testing.T) {
	t.Parallel()
	nodes, _ := parseRubyVarFixture(t)

	c := jsNode(nodes, graph.NodeTypeVariable, "MAX_RETRIES")
	if c == nil {
		t.Fatalf("missing constant node; nodes: %+v", nodes)
	}
	if c.Meta["kind"] != "const" || c.Meta["mutable"] != "false" {
		t.Errorf("constant meta wrong: %+v", c.Meta)
	}
}

func TestRubyVariables_Class(t *testing.T) {
	t.Parallel()
	nodes, _ := parseRubyVarFixture(t)

	cls := jsNode(nodes, graph.NodeTypeClass, "OrdersController")
	if cls == nil {
		t.Fatalf("missing class node; nodes: %+v", nodes)
	}
	if !contains(cls.Meta["methods"], "create") || !contains(cls.Meta["methods"], "show") {
		t.Errorf("class methods wrong: %+v", cls.Meta)
	}
	if !contains(cls.Meta["attrs"], "cart") || !contains(cls.Meta["attrs"], "status") {
		t.Errorf("class attrs wrong: %+v", cls.Meta)
	}
}

func TestRubyVariables_EndLine(t *testing.T) {
	t.Parallel()
	nodes, _ := parseRubyVarFixture(t)

	cls := jsNode(nodes, graph.NodeTypeClass, "OrdersController")
	if cls == nil {
		t.Fatalf("missing class node; nodes: %+v", nodes)
	}
	if cls.Line != 3 || cls.EndLine != 15 {
		t.Errorf("class OrdersController span wrong: got line=%d end_line=%d, want line=3 end_line=15", cls.Line, cls.EndLine)
	}

	fn := jsNode(nodes, graph.NodeTypeFunction, "create")
	if fn == nil {
		t.Fatalf("missing function node; nodes: %+v", nodes)
	}
	if fn.Line != 7 || fn.EndLine != 10 {
		t.Errorf("method create span wrong: got line=%d end_line=%d, want line=7 end_line=10", fn.Line, fn.EndLine)
	}
}

func TestRubyVariables_IvarWritesAndReads(t *testing.T) {
	t.Parallel()
	nodes, edges := parseRubyVarFixture(t)

	order := jsNode(nodes, graph.NodeTypeVariable, "@order")
	if order == nil {
		t.Fatalf("missing @order variable node; nodes: %+v", nodes)
	}
	if order.Meta["scope"] != "instance" || order.Meta["class"] != "OrdersController" {
		t.Errorf("@order meta wrong: %+v", order.Meta)
	}

	if jsEdge(edges, graph.EdgeTypeWrites, "function:create", "variable:@order") == nil {
		t.Errorf("missing writes edge create -> @order; edges: %+v", edges)
	}
	r := jsEdge(edges, graph.EdgeTypeReads, "function:show", "variable:@order")
	if r == nil {
		t.Fatalf("missing reads edge show -> @order")
	}
	if r.Confidence != graph.ConfidenceInferred {
		t.Errorf("ruby edges must be inferred, got %q", r.Confidence)
	}

	count := jsNode(nodes, graph.NodeTypeVariable, "@@count")
	if count == nil || count.Meta["scope"] != "class" {
		t.Errorf("@@count should be class-scoped: %+v", count)
	}
}

const rubyCallsFixture = `class UserManager
  def create(params)
    validate(params)
    self.notify(params)
  end

  def validate(params)
    true
  end

  def notify(params)
    later_helper(params)
  end
end

class OrderManager
  def notify(params)
    false
  end
end

def later_helper(params)
  params
end
`

func TestRubyVariables_BareCallResolvesSameClass(t *testing.T) {
	t.Parallel()
	_, edges, _ := extractRubyVariables("app/services/user_manager.rb", "shop", []byte(rubyCallsFixture))

	if jsEdge(edges, graph.EdgeTypeCalls, "function:create", "function:validate") == nil {
		t.Errorf("missing calls edge create -> validate; edges: %+v", edges)
	}
}

func TestRubyVariables_ExplicitSelfCallResolves(t *testing.T) {
	t.Parallel()
	_, edges, _ := extractRubyVariables("app/services/user_manager.rb", "shop", []byte(rubyCallsFixture))

	if jsEdge(edges, graph.EdgeTypeCalls, "function:create", "function:notify") == nil {
		t.Errorf("missing calls edge create -> self.notify; edges: %+v", edges)
	}
}

func TestRubyVariables_SameNameDifferentClassDoesNotCollide(t *testing.T) {
	t.Parallel()
	nodes, edges, _ := extractRubyVariables("app/services/user_manager.rb", "shop", []byte(rubyCallsFixture))

	// UserManager#create calls UserManager#notify — must NOT resolve to
	// OrderManager#notify even though both classes are in the same file and
	// share the method name (rule 9: no attribution by luck).
	var userNotify, orderNotify *graph.Node
	for i := range nodes {
		if nodes[i].Label == "notify" && nodes[i].Meta["class"] == "UserManager" {
			userNotify = &nodes[i]
		}
		if nodes[i].Label == "notify" && nodes[i].Meta["class"] == "OrderManager" {
			orderNotify = &nodes[i]
		}
	}
	if userNotify == nil || orderNotify == nil {
		t.Fatalf("expected both notify methods as nodes; nodes: %+v", nodes)
	}
	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls && e.To == orderNotify.ID {
			t.Errorf("calls edge must not target OrderManager#notify: %+v", e)
		}
	}
}

func TestRubyVariables_BareCallForwardReferenceResolves(t *testing.T) {
	t.Parallel()
	// later_helper is defined below notify() in the file — forward
	// references must still resolve via the pre-collection pass.
	_, edges, _ := extractRubyVariables("app/services/user_manager.rb", "shop", []byte(rubyCallsFixture))

	if jsEdge(edges, graph.EdgeTypeCalls, "function:notify", "function:later_helper") == nil {
		t.Errorf("missing forward-reference calls edge notify -> later_helper; edges: %+v", edges)
	}
}

func TestRubyVariables_UnresolvableBareCallGoesToLedger(t *testing.T) {
	t.Parallel()
	// `audit_trail` is app-defined somewhere the extractor cannot see (a
	// concern, say) — a real blind spot, and the ledger must keep it.
	//
	// `render` sits beside it as the control: it is an ActionController method
	// that no repository defines, so no future pass can ever resolve it.
	// Ledgering it only inflated the "verify N unresolved references manually"
	// footer that agents act on by opening files (see ruby_builtins.go).
	src := `class OrdersController
  def show
    render json: @order
    audit_trail(@order)
  end
end
`
	_, _, unresolved := extractRubyVariables("app/controllers/orders_controller.rb", "shop", []byte(src))

	var gotAudit, gotRender bool
	for _, u := range unresolved {
		if u.Kind != "call_ref" {
			continue
		}
		switch u.Name {
		case "audit_trail":
			gotAudit = true
		case "render":
			gotRender = true
		}
	}
	if !gotAudit {
		t.Errorf("expected unresolved call_ref for app-defined `audit_trail`; unresolved: %+v", unresolved)
	}
	if gotRender {
		t.Errorf("framework builtin `render` must not be ledgered; unresolved: %+v", unresolved)
	}
}

// TestRubyVariables_ERBViewBareCallLedgered is DC.12: a `.erb` view runs with
// `self` bound to the view instance mixing in every helper module, but has no
// enclosing `def` for the pre-DC.12 methodID=="" guard to let through. The
// call itself can't resolve same-file (views define no real methods) — only
// the ledger entry is this pass's job; LinkRubyMixinMethods resolves it
// cross-file against the service's helper modules.
func TestRubyVariables_ERBViewBareCallLedgered(t *testing.T) {
	t.Parallel()
	src := `
  unique_names(workflows)
`
	_, _, unresolved := extractRubyVariables("app/views/task_lists/_filters.html.erb", "app", []byte(src))

	var got bool
	for _, u := range unresolved {
		if u.Kind == "call_ref" && u.Name == "unique_names" {
			got = true
		}
	}
	assert.True(t, got, "expected unique_names to be ledgered as call_ref from ERB view top-level scope; unresolved: %+v", unresolved)
}

// TestRubyVariables_ERBViewSelfCallLedgered covers the other DC.12 shape: an
// explicit `self.` receiver call, same as a bare call for lookup purposes.
func TestRubyVariables_ERBViewSelfCallLedgered(t *testing.T) {
	t.Parallel()
	src := `
  self.current_organization
`
	_, _, unresolved := extractRubyVariables("app/views/layouts/_nav.html.erb", "app", []byte(src))

	var got bool
	for _, u := range unresolved {
		if u.Kind == "call_ref" && u.Name == "current_organization" {
			got = true
		}
	}
	assert.True(t, got, "expected self.current_organization to be ledgered from ERB view scope; unresolved: %+v", unresolved)
}

// TestRubyVariables_ERBViewLocalNotMistakenForCall keeps Tier BC's local-vs-call
// discipline: a locally-scriptletted variable used bare must never be
// ledgered as a call, matching the identifier-case rule already applied
// inside method bodies.
func TestRubyVariables_ERBViewLocalNotMistakenForCall(t *testing.T) {
	t.Parallel()
	src := `
  workflows = @study.workflows
  workflows
`
	_, _, unresolved := extractRubyVariables("app/views/studies/_show.html.erb", "app", []byte(src))

	for _, u := range unresolved {
		if u.Name == "workflows" {
			t.Errorf("a locally-assigned view variable must never be ledgered as a call: %+v", u)
		}
	}
}

// TestRubyVariables_TopLevelRubyScriptBareCallNotLedgered pins DC.12's scope:
// only `.erb` view top-level scope is relaxed. A plain `.rb` top-level DSL
// call (rake task, migration, initializer) keeps its pre-DC.12 zero-noise
// behavior — ledgering every such call would flood the unresolved-ref ledger
// with framework/gem DSL methods no pass can ever resolve.
func TestRubyVariables_TopLevelRubyScriptBareCallNotLedgered(t *testing.T) {
	t.Parallel()
	src := `
  add_indexes(:users)
`
	_, _, unresolved := extractRubyVariables("db/add_index_in_database.rb", "app", []byte(src))

	for _, u := range unresolved {
		if u.Name == "add_indexes" {
			t.Errorf("top-level .rb call sites are out of DC.12 scope and must not be ledgered: %+v", u)
		}
	}
}

func TestRubyVariables_ConstantReceiverCallResolvesSelfMethod(t *testing.T) {
	t.Parallel()
	// UserCategoryRuleSet.latest_for is a `ClassName.method` call whose
	// receiver is a same-file constant — unambiguous the same way `Foo.new`
	// already is, since the class it names declares exactly one `self.`
	// method with that name.
	src := `class LicenseReportJobsController
  def create
    UserCategoryRuleSet.latest_for(1, 2)
  end
end

class UserCategoryRuleSet
  def self.latest_for(org_id, product_id)
    true
  end
end
`
	nodes, edges, unresolved := extractRubyVariables("app/controllers/license_report_jobs_controller.rb", "shop", []byte(src))

	var latestFor *graph.Node
	for i := range nodes {
		if nodes[i].Label == "latest_for" {
			latestFor = &nodes[i]
		}
	}
	if latestFor == nil {
		t.Fatalf("expected UserCategoryRuleSet.latest_for node; nodes: %+v", nodes)
	}
	if jsEdge(edges, graph.EdgeTypeCalls, "function:create", "function:latest_for") == nil {
		t.Errorf("missing calls edge create -> ClassName.latest_for; edges: %+v", edges)
	}
	for _, u := range unresolved {
		if u.Name == "latest_for" {
			t.Errorf("resolved constant-receiver call must not also be ledgered: %+v", u)
		}
	}
}

func TestRubyVariables_ConstantReceiverFrameworkCallNotLedgered(t *testing.T) {
	t.Parallel()
	// Product.find_by is a `ClassName.method` call to an ActiveRecord finder
	// no repository ever defines. It cannot resolve — Product may not even be
	// declared in this file — and ledgering every such miss would just be
	// noise a same-file pass can never clear (a future cross-file linker
	// pass is the right place for this, not this ledger).
	src := `class LicenseReportJobsController
  def create
    Product.find_by(slug: "vega-lyra")
  end
end
`
	_, edges, unresolved := extractRubyVariables("app/controllers/license_report_jobs_controller.rb", "shop", []byte(src))

	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls && e.From == "shop:app/controllers/license_report_jobs_controller.rb:function:create:2" {
			t.Errorf("unresolvable constant-receiver call must not be attributed: %+v", e)
		}
	}
	for _, u := range unresolved {
		if u.Name == "find_by" {
			t.Errorf("constant-receiver miss must not be ledgered by the same-file pass: %+v", u)
		}
	}
}

func TestRubyVariables_ReceiverTypedCallNotAttributed(t *testing.T) {
	t.Parallel()
	// article.save has an explicit non-self receiver — Ruby's dynamism rules
	// out static type inference, so this must not be attributed to any
	// same-named method (no false positive), nor land in the ledger (it's
	// not a bare/implicit-self call at all).
	src := `class ArticlesController
  def create
    article = Article.new
    article.save
  end
end

class Article
  def save
    true
  end
end
`
	nodes, edges, unresolved := extractRubyVariables("app/controllers/articles_controller.rb", "shop", []byte(src))

	var articleSave *graph.Node
	for i := range nodes {
		if nodes[i].Label == "save" {
			articleSave = &nodes[i]
		}
	}
	if articleSave == nil {
		t.Fatalf("expected Article#save node; nodes: %+v", nodes)
	}
	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls && e.To == articleSave.ID {
			t.Errorf("receiver-typed call must not be attributed: %+v", e)
		}
	}
	for _, u := range unresolved {
		if u.Name == "save" {
			t.Errorf("receiver-typed call must not enter the ledger either: %+v", u)
		}
	}
}

// Tier BC: bare, zero-arg, receiver-less, paren-less identifiers are
// syntactically identical to a local-variable read in tree-sitter-ruby.

func TestRubyVariables_BareIdentifierMemoizationCallResolves(t *testing.T) {
	t.Parallel()
	// The exact live shape from the E.2 orion recall miss:
	// `@category = category` — RHS is a bare call to a private helper, not a
	// local-variable read (there is no local named `category` in `destroy`).
	src := `class CategoriesController
  def destroy
    @category = category
    @category.destroy
  end

  private

  def category
    @_category ||= Category.find_by(id: params[:id])
  end
end
`
	_, edges, unresolved := extractRubyVariables("app/controllers/categories_controller.rb", "shop", []byte(src))

	if jsEdge(edges, graph.EdgeTypeCalls, "function:destroy", "function:category") == nil {
		t.Errorf("missing calls edge destroy -> category; edges: %+v", edges)
	}
	for _, u := range unresolved {
		if u.Name == "category" {
			t.Errorf("resolved bare-identifier call must not also be ledgered: %+v", u)
		}
	}
}

func TestRubyVariables_BareIdentifierStatementCallResolves(t *testing.T) {
	t.Parallel()
	// A bare statement call (no assignment at all) — as idiomatic as the
	// memoization shape, e.g. a guard/callback: `authorize!`, `validate`.
	src := `class UserManager
  def create
    validate
  end

  def validate
    true
  end
end
`
	_, edges, _ := extractRubyVariables("app/services/user_manager.rb", "shop", []byte(src))

	if jsEdge(edges, graph.EdgeTypeCalls, "function:create", "function:validate") == nil {
		t.Errorf("missing calls edge create -> bare statement call validate; edges: %+v", edges)
	}
}

func TestRubyVariables_LocalVariableReadNotAttributedAsCall(t *testing.T) {
	t.Parallel()
	// `x` is assigned locally, then read bare later in the same method. Even
	// though a same-named method `x` exists elsewhere in the file, the local
	// read must never be misattributed as a call to it (gate #3).
	src := `class Calculator
  def compute
    x = 1
    foo(x)
    x
  end

  def x
    99
  end
end
`
	nodes, edges, unresolved := extractRubyVariables("app/services/calculator.rb", "shop", []byte(src))

	var xMethod *graph.Node
	for i := range nodes {
		if nodes[i].Label == "x" && nodes[i].Type == graph.NodeTypeFunction {
			xMethod = &nodes[i]
		}
	}
	if xMethod == nil {
		t.Fatalf("expected Calculator#x method node; nodes: %+v", nodes)
	}
	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls && e.To == xMethod.ID {
			t.Errorf("local-variable read must never be attributed as a call: %+v", e)
		}
	}
	for _, u := range unresolved {
		if u.Name == "x" {
			t.Errorf("a local-variable read must not be ledgered either: %+v", u)
		}
	}
}

func TestRubyVariables_LocalAssignedAfterUseStillNotACall(t *testing.T) {
	t.Parallel()
	// Conservative-in-the-false-negative-direction: `y` is assigned AFTER its
	// bare read in source order. Real Ruby would treat the early read as
	// calling method `y` (lexical-position semantics), but this pass
	// deliberately treats a name as local for the WHOLE method if assigned
	// anywhere in it — so this must NOT produce a calls edge (a safe miss,
	// not a false positive).
	src := `class Widget
  def run
    foo(y)
    y = 2
  end

  def y
    99
  end
end
`
	nodes, edges, _ := extractRubyVariables("app/services/widget.rb", "shop", []byte(src))

	var yMethod *graph.Node
	for i := range nodes {
		if nodes[i].Label == "y" && nodes[i].Type == graph.NodeTypeFunction {
			yMethod = &nodes[i]
		}
	}
	if yMethod == nil {
		t.Fatalf("expected Widget#y method node; nodes: %+v", nodes)
	}
	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls && e.To == yMethod.ID {
			t.Errorf("name assigned later in the method must still be treated as local: %+v", e)
		}
	}
}

func TestRubyVariables_ParameterNameNotAttributedAsCall(t *testing.T) {
	t.Parallel()
	// Every parameter shape (positional, optional, splat, keyword,
	// hash-splat, block) must be excluded from bare-call resolution, but a
	// default-value expression on an optional/keyword parameter is itself a
	// real call site and must still resolve.
	src := `class Widget
  def run(a, b = fallback, *c, d:, e: fallback, **f, &g)
    a
  end

  def fallback
    1
  end
end
`
	nodes, edges, unresolved := extractRubyVariables("app/services/widget.rb", "shop", []byte(src))

	var fallback *graph.Node
	for i := range nodes {
		if nodes[i].Label == "fallback" && nodes[i].Type == graph.NodeTypeFunction {
			fallback = &nodes[i]
		}
	}
	if fallback == nil {
		t.Fatalf("expected Widget#fallback method node; nodes: %+v", nodes)
	}
	if jsEdge(edges, graph.EdgeTypeCalls, "function:run", "function:fallback") == nil {
		t.Errorf("missing calls edge run -> fallback (default-value expression); edges: %+v", edges)
	}
	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls && e.From == "shop:app/services/widget.rb:function:run:2" && e.To != fallback.ID {
			t.Errorf("a parameter name must never be attributed as a call: %+v", e)
		}
	}
	for _, u := range unresolved {
		switch u.Name {
		case "a", "b", "c", "d", "e", "f", "g":
			t.Errorf("a parameter name must not be ledgered as an unresolved call either: %+v", u)
		}
	}
}

func TestRubyVariables_ForLoopVariableNotAttributedAsCall(t *testing.T) {
	t.Parallel()
	src := `class Report
  def run
    for row in rows
      foo(row)
    end
  end

  def row
    99
  end
end
`
	nodes, edges, _ := extractRubyVariables("app/services/report.rb", "shop", []byte(src))

	var rowMethod *graph.Node
	for i := range nodes {
		if nodes[i].Label == "row" && nodes[i].Type == graph.NodeTypeFunction {
			rowMethod = &nodes[i]
		}
	}
	if rowMethod == nil {
		t.Fatalf("expected Report#row method node; nodes: %+v", nodes)
	}
	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls && e.To == rowMethod.ID {
			t.Errorf("a for-loop variable must never be attributed as a call: %+v", e)
		}
	}
}

func TestRubyVariables_PatternMatchBindingNotAttributedAsCall(t *testing.T) {
	t.Parallel()
	src := `class Report
  def run
    case pair
    in [a, b]
      foo(a, b)
    end
  end

  def a
    99
  end
end
`
	nodes, edges, _ := extractRubyVariables("app/services/report.rb", "shop", []byte(src))

	var aMethod *graph.Node
	for i := range nodes {
		if nodes[i].Label == "a" && nodes[i].Type == graph.NodeTypeFunction {
			aMethod = &nodes[i]
		}
	}
	if aMethod == nil {
		t.Fatalf("expected Report#a method node; nodes: %+v", nodes)
	}
	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls && e.To == aMethod.ID {
			t.Errorf("a case/in pattern binding must never be attributed as a call: %+v", e)
		}
	}
}

func TestRubyVariables_RescueVariableNotAttributedAsCall(t *testing.T) {
	t.Parallel()
	src := `class Report
  def run
    begin
      risky
    rescue StandardError => e
      foo(e)
    end
  end

  def e
    99
  end
end
`
	nodes, edges, _ := extractRubyVariables("app/services/report.rb", "shop", []byte(src))

	var eMethod *graph.Node
	for i := range nodes {
		if nodes[i].Label == "e" && nodes[i].Type == graph.NodeTypeFunction {
			eMethod = &nodes[i]
		}
	}
	if eMethod == nil {
		t.Fatalf("expected Report#e method node; nodes: %+v", nodes)
	}
	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls && e.To == eMethod.ID {
			t.Errorf("a rescue exception variable must never be attributed as a call: %+v", e)
		}
	}
}

func TestRubyVariables_MultiAssignTargetsNotAttributedAsCalls(t *testing.T) {
	t.Parallel()
	// `y, z = pair` and a splat target `a, *b = arr` — multi-assign targets
	// must never be misread as calls even when same-named methods exist.
	src := `class Report
  def run
    y, z = pair
    a, *b = arr
    foo(y, z, a, b)
  end

  def y
    1
  end

  def b
    2
  end
end
`
	nodes, edges, _ := extractRubyVariables("app/services/report.rb", "shop", []byte(src))

	for _, name := range []string{"y", "b"} {
		var m *graph.Node
		for i := range nodes {
			if nodes[i].Label == name && nodes[i].Type == graph.NodeTypeFunction {
				m = &nodes[i]
			}
		}
		if m == nil {
			t.Fatalf("expected Report#%s method node; nodes: %+v", name, nodes)
		}
		for _, e := range edges {
			if e.Type == graph.EdgeTypeCalls && e.To == m.ID {
				t.Errorf("multi-assign target %q must never be attributed as a call: %+v", name, e)
			}
		}
	}
}

func TestRubyVariables_UnresolvedBareIdentifierNotLedgered(t *testing.T) {
	t.Parallel()
	// Ledger policy: an unresolved bare identifier is NOT reported, unlike an
	// unresolved case "call" (TestRubyVariables_UnresolvableBareCallGoesToLedger)
	// — the parser has no guarantee this was ever a call at all.
	src := `class Report
  def run
    mystery_thing
  end
end
`
	_, edges, unresolved := extractRubyVariables("app/services/report.rb", "shop", []byte(src))

	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls {
			t.Errorf("no method named mystery_thing exists anywhere; must not resolve: %+v", e)
		}
	}
	for _, u := range unresolved {
		if u.Name == "mystery_thing" {
			t.Errorf("unresolved bare identifiers must not be ledgered: %+v", u)
		}
	}
}

// TestRubyVariables_RakeTaskBareCallResolves is DC.18: a `task do...end` block
// has no enclosing `def`, so a bare call made directly inside it (a common
// Rake shape — the task body calls a helper method defined later in the same
// file) never had a methodID to key scope tracking off and was silently
// dropped. A synthetic per-block scope should let the existing same-file
// bare-call resolution machinery resolve it exactly like a real method body.
func TestRubyVariables_RakeTaskBareCallResolves(t *testing.T) {
	t.Parallel()
	src := `task :rename_categories do
  rename_categories
end

def rename_categories
  puts "renamed"
end
`
	_, edges, _ := extractRubyVariables("lib/tasks/categories.rake", "shop", []byte(src))

	if jsEdge(edges, graph.EdgeTypeCalls, "rake_block", "function:rename_categories") == nil {
		t.Errorf("missing calls edge from task block -> rename_categories; edges: %+v", edges)
	}
}

// TestRubyVariables_RakeNamespaceTaskBareCallResolves confirms the same
// synthetic scope applies when the bare call sits inside a `task` nested in a
// `namespace` block.
func TestRubyVariables_RakeNamespaceTaskBareCallResolves(t *testing.T) {
	t.Parallel()
	src := `namespace :categories do
  task :add_categories do
    add_categories
  end
end

def add_categories
  puts "added"
end
`
	_, edges, _ := extractRubyVariables("lib/tasks/categories.rake", "shop", []byte(src))

	if jsEdge(edges, graph.EdgeTypeCalls, "rake_block", "function:add_categories") == nil {
		t.Errorf("missing calls edge from nested task block -> add_categories; edges: %+v", edges)
	}
}

// TestRubyVariables_RakeTaskBlockMintsANode: the synthetic rake_block scope
// ID DC.18 introduced is used as an edge's From — every edge's endpoints
// must be real nodes or the graph DB's foreign key rejects the upsert
// (reproduced live: a full orion reindex aborted on
// lib/tasks/audited.rake's `namespace :nordic do` block, whose calls edge
// pointed at a rake_block ID with no matching node). Confirms both that the
// node exists and that its ID matches the one addEdge used as From.
func TestRubyVariables_RakeTaskBlockMintsANode(t *testing.T) {
	t.Parallel()
	src := `task :rename_categories do
  rename_categories
end

def rename_categories
  puts "renamed"
end
`
	nodes, edges, _ := extractRubyVariables("lib/tasks/categories.rake", "shop", []byte(src))

	byID := map[string]bool{}
	for _, n := range nodes {
		byID[n.ID] = true
	}
	found := false
	for _, e := range edges {
		if e.Type != graph.EdgeTypeCalls || !strings.Contains(e.From, "rake_block") {
			continue
		}
		found = true
		if !byID[e.From] {
			t.Errorf("edge From %q has no matching node — would fail the graph DB's foreign key constraint", e.From)
		}
	}
	if !found {
		t.Fatal("expected a calls edge from a rake_block scope")
	}
}

// DC.24: a bare identifier used only as the *receiver* of another call
// (`memoized_helper.chain_method`) was excluded from bare-call resolution
// entirely — the memoized accessor itself never got a calls/reads edge.
// Mirrors the live orion shape: `study_role_assignee_scope` is a
// memoized (`||=`) accessor called only via
// `study_role_assignee_scope.sorted_study_role_users`.
func TestRubyVariables_BareIdentifierAsReceiverResolves(t *testing.T) {
	t.Parallel()
	src := `class RolesController
  def index
    memoized_scope.sorted_users
  end

  def memoized_scope
    @memoized_scope ||= RoleScope.new
  end
end
`
	_, edges, unresolved := extractRubyVariables("app/controllers/roles_controller.rb", "shop", []byte(src))

	if jsEdge(edges, graph.EdgeTypeCalls, "function:index", "function:memoized_scope") == nil {
		t.Errorf("missing calls edge index -> memoized_scope (receiver-position bare identifier); edges: %+v", edges)
	}
	for _, u := range unresolved {
		if u.Name == "memoized_scope" {
			t.Errorf("resolved receiver-position bare identifier must not also be ledgered: %+v", u)
		}
	}
}

// Negative case: `self.method_name` as a receiver must not be treated as the
// DC.24 bare-identifier-receiver shape — it is already handled by the
// existing implicit-self branch and must not gain a duplicate/spurious edge.
func TestRubyVariables_SelfReceiverNotTreatedAsBareIdentifierReceiver(t *testing.T) {
	t.Parallel()
	src := `class RolesController
  def index
    self.memoized_scope.sorted_users
  end

  def memoized_scope
    @memoized_scope ||= RoleScope.new
  end
end
`
	_, edges, _ := extractRubyVariables("app/controllers/roles_controller.rb", "shop", []byte(src))

	if jsEdge(edges, graph.EdgeTypeCalls, "function:index", "function:memoized_scope") == nil {
		t.Errorf("missing calls edge index -> memoized_scope via explicit self receiver; edges: %+v", edges)
	}
}

// Negative case: an actual local variable filling the receiver slot
// (`x = Foo.new; x.bar`) must not gain a spurious edge to a same-named
// method — the existing locals pre-pass already distinguishes this from the
// standalone-identifier case; DC.24 must reuse it, not re-derive it.
func TestRubyVariables_LocalVariableReceiverNotAttributedAsCall(t *testing.T) {
	t.Parallel()
	src := `class RolesController
  def index
    memoized_scope = RoleScope.new
    memoized_scope.sorted_users
  end
end

class Helper
  def memoized_scope
    true
  end
end
`
	nodes, edges, unresolved := extractRubyVariables("app/controllers/roles_controller.rb", "shop", []byte(src))

	var helperMethod *graph.Node
	for i := range nodes {
		if nodes[i].Label == "memoized_scope" && nodes[i].Type == graph.NodeTypeFunction {
			helperMethod = &nodes[i]
		}
	}
	if helperMethod == nil {
		t.Fatalf("expected Helper#memoized_scope method node; nodes: %+v", nodes)
	}
	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls && e.To == helperMethod.ID {
			t.Errorf("local-variable receiver must never be attributed as a call: %+v", e)
		}
	}
	for _, u := range unresolved {
		if u.Name == "memoized_scope" {
			t.Errorf("a local-variable receiver must not be ledgered either: %+v", u)
		}
	}
}
