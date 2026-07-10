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
	return extractRubyVariables("app/controllers/orders_controller.rb", "shop", []byte(rubyVarFixture))
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
