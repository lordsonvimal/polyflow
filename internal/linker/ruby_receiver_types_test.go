package linker

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// TestLinkRubyReceiverTypeCalls_LocalVarNew covers the plain `x = Const.new;
// x.foo` shape — the largest chunk of the receiver-typed gap in real repos.
func TestLinkRubyReceiverTypeCalls_LocalVarNew(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	controller := writeRuby(t, dir, "orders_controller.rb", `
class OrdersController
  def create
    order = Order.new
    order.save
  end
end
`)
	model := writeRuby(t, dir, "order.rb", `
class Order
  def save
    true
  end
end
`)

	nodes := []graph.Node{
		rubyClassNode("svc", controller, "OrdersController", 2),
		classCallFuncNode("svc", controller, "OrdersController", "create", 3),
		rubyClassNode("svc", model, "Order", 2),
		classCallFuncNode("svc", model, "Order", "save", 3),
	}
	serviceFiles := map[string][]string{"svc": {controller, model}}

	edges, unresolved := LinkRubyReceiverTypeCalls(nodes, serviceFiles)

	var got *graph.Edge
	for i := range edges {
		if edges[i].To == "svc:"+model+":function:save:3" {
			got = &edges[i]
		}
	}
	if got == nil {
		t.Fatalf("expected an edge into Order#save, got edges=%v unresolved=%v", edges, unresolved)
	}
	if got.From != "svc:"+controller+":function:create:3" {
		t.Errorf("edge From = %q, want OrdersController#create", got.From)
	}
}

// TestLinkRubyReceiverTypeCalls_MemoReaderReturnType covers the exact shape
// this pass was written for: a memo-reader method (`def aws; @aws ||=
// AwsFacade.new_instance; end`) used as a bare-identifier receiver
// (`aws.complete_multipart_upload`), where AwsFacade.new_instance's own body
// is itself just `AwsFacade.new`.
func TestLinkRubyReceiverTypeCalls_MemoReaderReturnType(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	controller := writeRuby(t, dir, "fast_uploads_controller.rb", `
class FastUploadsController
  def complete_multipart
    aws.complete_multipart_upload(key, upload_id: upload_id, parts: parts)
  end

  def aws
    @aws ||= AwsFacade.new_instance
  end
end
`)
	facade := writeRuby(t, dir, "aws_facade.rb", `
class AwsFacade
  class << self
    def new_instance(storage: nil)
      AwsFacade.new(storage: storage)
    end
  end

  def complete_multipart_upload(key, upload_id:, parts:)
    true
  end
end
`)

	nodes := []graph.Node{
		rubyClassNode("svc", controller, "FastUploadsController", 2),
		classCallFuncNode("svc", controller, "FastUploadsController", "complete_multipart", 3),
		classCallFuncNode("svc", controller, "FastUploadsController", "aws", 7),
		rubyClassNode("svc", facade, "AwsFacade", 2),
		classCallFuncNode("svc", facade, "AwsFacade", "new_instance", 4),
		classCallFuncNode("svc", facade, "AwsFacade", "complete_multipart_upload", 9),
	}
	serviceFiles := map[string][]string{"svc": {controller, facade}}

	edges, unresolved := LinkRubyReceiverTypeCalls(nodes, serviceFiles)

	wantTo := "svc:" + facade + ":function:complete_multipart_upload:9"
	var got *graph.Edge
	for i := range edges {
		if edges[i].To == wantTo {
			got = &edges[i]
		}
	}
	if got == nil {
		t.Fatalf("expected an edge into AwsFacade#complete_multipart_upload, got edges=%v unresolved=%v", edges, unresolved)
	}
	wantFrom := "svc:" + controller + ":function:complete_multipart:3"
	if got.From != wantFrom {
		t.Errorf("edge From = %q, want %q", got.From, wantFrom)
	}
}

// TestLinkRubyReceiverTypeCalls_MemoizedIvarDirect covers `@svc ||= Const.new`
// used directly as a receiver (`@svc.foo`), without going through a reader
// method.
func TestLinkRubyReceiverTypeCalls_MemoizedIvarDirect(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	caller := writeRuby(t, dir, "importer.rb", `
class Importer
  def run
    @client ||= ApiClient.new
    @client.fetch
  end
end
`)
	client := writeRuby(t, dir, "api_client.rb", `
class ApiClient
  def fetch
    true
  end
end
`)

	nodes := []graph.Node{
		rubyClassNode("svc", caller, "Importer", 2),
		classCallFuncNode("svc", caller, "Importer", "run", 3),
		rubyClassNode("svc", client, "ApiClient", 2),
		classCallFuncNode("svc", client, "ApiClient", "fetch", 3),
	}
	serviceFiles := map[string][]string{"svc": {caller, client}}

	edges, _ := LinkRubyReceiverTypeCalls(nodes, serviceFiles)

	wantTo := "svc:" + client + ":function:fetch:3"
	found := false
	for _, e := range edges {
		if e.To == wantTo {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an edge into ApiClient#fetch, got edges=%v", edges)
	}
}

// TestLinkRubyReceiverTypeCalls_NoInferenceNoEdge is the negative case: a
// receiver with no recoverable type (e.g. a method parameter, or a call
// result the pass doesn't try to type) must not produce a fabricated edge.
func TestLinkRubyReceiverTypeCalls_NoInferenceNoEdge(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	caller := writeRuby(t, dir, "processor.rb", `
class Processor
  def run(item)
    item.process
  end
end
`)

	nodes := []graph.Node{
		rubyClassNode("svc", caller, "Processor", 2),
		classCallFuncNode("svc", caller, "Processor", "run", 3),
	}
	serviceFiles := map[string][]string{"svc": {caller}}

	edges, _ := LinkRubyReceiverTypeCalls(nodes, serviceFiles)
	if len(edges) != 0 {
		t.Errorf("expected no edges for an untypeable receiver, got %v", edges)
	}
}
