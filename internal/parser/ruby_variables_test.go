package parser

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
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

func TestRubyVariables_IvarWritesAndReads(t *testing.T) {
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
	_, edges, _ := extractRubyVariables("app/services/user_manager.rb", "shop", []byte(rubyCallsFixture))

	if jsEdge(edges, graph.EdgeTypeCalls, "function:create", "function:validate") == nil {
		t.Errorf("missing calls edge create -> validate; edges: %+v", edges)
	}
}

func TestRubyVariables_ExplicitSelfCallResolves(t *testing.T) {
	_, edges, _ := extractRubyVariables("app/services/user_manager.rb", "shop", []byte(rubyCallsFixture))

	if jsEdge(edges, graph.EdgeTypeCalls, "function:create", "function:notify") == nil {
		t.Errorf("missing calls edge create -> self.notify; edges: %+v", edges)
	}
}

func TestRubyVariables_SameNameDifferentClassDoesNotCollide(t *testing.T) {
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
	// later_helper is defined below notify() in the file — forward
	// references must still resolve via the pre-collection pass.
	_, edges, _ := extractRubyVariables("app/services/user_manager.rb", "shop", []byte(rubyCallsFixture))

	if jsEdge(edges, graph.EdgeTypeCalls, "function:notify", "function:later_helper") == nil {
		t.Errorf("missing forward-reference calls edge notify -> later_helper; edges: %+v", edges)
	}
}

func TestRubyVariables_UnresolvableBareCallGoesToLedger(t *testing.T) {
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

func TestRubyVariables_ReceiverTypedCallNotAttributed(t *testing.T) {
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
