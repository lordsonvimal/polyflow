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

	edges, unresolved := LinkRubyReceiverTypeCalls(nodes, nil, serviceFiles)

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

	edges, unresolved := LinkRubyReceiverTypeCalls(nodes, nil, serviceFiles)

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

	edges, _ := LinkRubyReceiverTypeCalls(nodes, nil, serviceFiles)

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

// TestLinkRubyReceiverTypeCalls_ImplicitSelfNewInlineChain covers DC.5's
// zero-hop shape: `new(user_license).call` inside `class << self` — no
// intermediate variable, and `new`'s receiver is implicit self (the
// enclosing class), not a literal constant name.
func TestLinkRubyReceiverTypeCalls_ImplicitSelfNewInlineChain(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	file := writeRuby(t, dir, "license_service.rb", `
class LicenseService
  class << self
    def call(user_license)
      new(user_license).call
    end
  end

  def call
    true
  end
end
`)

	nodes := []graph.Node{
		rubyClassNode("svc", file, "LicenseService", 2),
		classCallFuncNode("svc", file, "LicenseService", "call", 4),
		classCallFuncNode("svc", file, "LicenseService", "call", 9),
	}
	serviceFiles := map[string][]string{"svc": {file}}

	edges, _ := LinkRubyReceiverTypeCalls(nodes, nil, serviceFiles)

	wantTo := "svc:" + file + ":function:call:9"
	found := false
	for _, e := range edges {
		if e.To == wantTo {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an edge into the instance #call method, got edges=%v", edges)
	}
}

// TestLinkRubyReceiverTypeCalls_InlineChainNoVariable covers the other
// inline-chain surface: a literal `Const.new(...).method` chain with no
// intermediate variable at all (not the implicit-self case above).
func TestLinkRubyReceiverTypeCalls_InlineChainNoVariable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	caller := writeRuby(t, dir, "importer.rb", `
class Importer
  def run
    ApiClient.new.fetch
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

	edges, _ := LinkRubyReceiverTypeCalls(nodes, nil, serviceFiles)

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

// TestLinkRubyReceiverTypeCalls_SelfCallServiceObjectConvention covers DC.5's
// narrowly-scoped "result-object one-more-hop" shape: `result =
// SomeService.call(x)` followed by `result.success?`, where `SomeService`'s
// `self.call` classmethod delegates via the inline chain `new(x).call`, and
// the instance `#call` method returns `self` (the common convention this
// phase deliberately approximates rather than chasing the instance method's
// own return value in general).
func TestLinkRubyReceiverTypeCalls_SelfCallServiceObjectConvention(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	caller := writeRuby(t, dir, "orders_controller.rb", `
class OrdersController
  def create
    result = SomeService.call(params)
    result.success?
  end
end
`)
	service := writeRuby(t, dir, "some_service.rb", `
class SomeService
  class << self
    def call(params)
      new(params).call
    end
  end

  def call
    self
  end

  def success?
    true
  end
end
`)

	nodes := []graph.Node{
		rubyClassNode("svc", caller, "OrdersController", 2),
		classCallFuncNode("svc", caller, "OrdersController", "create", 3),
		rubyClassNode("svc", service, "SomeService", 2),
		classCallFuncNode("svc", service, "SomeService", "call", 4),
		classCallFuncNode("svc", service, "SomeService", "call", 9),
		classCallFuncNode("svc", service, "SomeService", "success?", 13),
	}
	serviceFiles := map[string][]string{"svc": {caller, service}}

	edges, unresolved := LinkRubyReceiverTypeCalls(nodes, nil, serviceFiles)

	wantTo := "svc:" + service + ":function:success?:13"
	found := false
	for _, e := range edges {
		if e.To == wantTo {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an edge into SomeService#success?, got edges=%v unresolved=%v", edges, unresolved)
	}
}

// TestLinkRubyReceiverTypeCalls_SelfCallDifferentBodyNotInferred is the
// negative case DC.5's acceptance criteria requires: a classmethod named
// `call` whose body is NOT the narrow inline-chain shape (here, a
// conditional returning one of two different classes) must fall through to
// unresolved rather than a fabricated single-type guess.
func TestLinkRubyReceiverTypeCalls_SelfCallDifferentBodyNotInferred(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	caller := writeRuby(t, dir, "orders_controller.rb", `
class OrdersController
  def create
    result = PickerService.call(params)
    result.success?
  end
end
`)
	service := writeRuby(t, dir, "picker_service.rb", `
class PickerService
  class << self
    def call(params)
      if params[:fast]
        FastPicker.new(params)
      else
        SlowPicker.new(params)
      end
    end
  end
end
`)
	fast := writeRuby(t, dir, "fast_picker.rb", `
class FastPicker
  def success?
    true
  end
end
`)
	slow := writeRuby(t, dir, "slow_picker.rb", `
class SlowPicker
  def success?
    true
  end
end
`)

	nodes := []graph.Node{
		rubyClassNode("svc", caller, "OrdersController", 2),
		classCallFuncNode("svc", caller, "OrdersController", "create", 3),
		rubyClassNode("svc", service, "PickerService", 2),
		classCallFuncNode("svc", service, "PickerService", "call", 4),
		rubyClassNode("svc", fast, "FastPicker", 2),
		classCallFuncNode("svc", fast, "FastPicker", "success?", 3),
		rubyClassNode("svc", slow, "SlowPicker", 2),
		classCallFuncNode("svc", slow, "SlowPicker", "success?", 3),
	}
	serviceFiles := map[string][]string{"svc": {caller, service, fast, slow}}

	edges, _ := LinkRubyReceiverTypeCalls(nodes, nil, serviceFiles)

	for _, e := range edges {
		if e.To == "svc:"+fast+":function:success?:3" || e.To == "svc:"+slow+":function:success?:3" {
			t.Errorf("expected no fabricated edge for an untypeable classmethod body, got %v", e)
		}
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

	edges, _ := LinkRubyReceiverTypeCalls(nodes, nil, serviceFiles)
	if len(edges) != 0 {
		t.Errorf("expected no edges for an untypeable receiver, got %v", edges)
	}
}
