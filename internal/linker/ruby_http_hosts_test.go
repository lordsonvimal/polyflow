package linker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// httpClientNode builds a dynamic Ruby http_client node whose URL token went
// unresolved (key_dynamic), the state the pattern layer leaves for Tier-L.
func httpClientNode(file string, line int, raw string) graph.Node {
	return graph.Node{
		ID:       "http:" + file,
		Type:     graph.NodeTypeHTTPClient,
		Service:  "svc",
		File:     file,
		Line:     line,
		Language: "ruby",
		Meta: map[string]string{
			"key_dynamic":     "true",
			"key_dynamic_raw": raw,
		},
	}
}

func writeRuby(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

// TestResolveRubyHTTPHosts_LocalAssignment covers shape (2): a local variable
// assigned from a cross-file host method (`url = server_api_url(...).to_s`).
func TestResolveRubyHTTPHosts_LocalAssignment(t *testing.T) {
	dir := t.TempDir()
	conn := writeRuby(t, dir, "connection_info.rb", `
module Server
  module ConnectionInfo
    def server_api_url(endpoint)
      lyra_app = if Rails.env.test?
                  "http://localhost:8080"
                else
                  ENV.fetch("LYRA_APP", nil)
                end
      URI("#{lyra_app}/#{endpoint}")
    end

    def token
      ENV.fetch("AGENT_TOKEN", nil)
    end
  end
end
`)
	base := writeRuby(t, dir, "base_node.rb", `
class BaseNode
  def register(_task = nil)
    url = server_api_url("client_api/v1/agents/register").to_s
    RestClient.post(url, payload, content_type: "application/json")
  end
end
`)
	nodes := []graph.Node{httpClientNode(base, 5, "url")}
	svcFiles := map[string][]string{"svc": {base, conn}}

	changed := ResolveRubyHTTPHosts(nodes, svcFiles)
	if len(changed) != 1 {
		t.Fatalf("expected 1 resolved node, got %d", len(changed))
	}
	if got := nodes[0].Meta["key_dynamic_raw"]; got != `ENV.fetch("LYRA_APP")` {
		t.Errorf("key_dynamic_raw = %q, want ENV.fetch(\"LYRA_APP\")", got)
	}
	if got := nodes[0].Meta["host_env_var"]; got != "LYRA_APP" {
		t.Errorf("host_env_var = %q, want LYRA_APP", got)
	}
	if got := nodes[0].Meta["host_resolved_via"]; got != "ruby_env_method" {
		t.Errorf("host_resolved_via = %q, want ruby_env_method", got)
	}
}

// TestResolveRubyHTTPHosts_ParamCaller covers shape (3): the http_client posts
// to a *method parameter* whose value flows from a same-file caller passing a
// host method (`def post_request(url); RestClient.post(url) ← post_request(server_api_uri.to_s)`).
func TestResolveRubyHTTPHosts_ParamCaller(t *testing.T) {
	dir := t.TempDir()
	conn := writeRuby(t, dir, "connection_info.rb", `
module Server
  module ConnectionInfo
    def server_api_uri
      URI.join(ENV.fetch("LYRA_HOST", nil), "client_api/v1/agents/register")
    end
  end
end
`)
	agent := writeRuby(t, dir, "agent_node.rb", `
class AgentNode
  def register
    response = post_request(server_api_uri.to_s, payload)
  end

  def post_request(url, payload)
    RestClient.post(url, payload, content_type: "application/json")
  end
end
`)
	// RestClient.post is on line 7 (1-based, counting the leading newline).
	nodes := []graph.Node{httpClientNode(agent, 7, "url")}
	svcFiles := map[string][]string{"svc": {agent, conn}}

	ResolveRubyHTTPHosts(nodes, svcFiles)
	if got := nodes[0].Meta["host_env_var"]; got != "LYRA_HOST" {
		t.Errorf("host_env_var = %q, want LYRA_HOST (param→caller trace)", got)
	}
}

// TestResolveRubyHTTPHosts_KeywordAndIvarChain covers the `path: url` keyword
// label plus an intra-class ivar chain (method → attr → @ivar → ENV): the
// Connection singleton pattern from the real Lyra agent.
func TestResolveRubyHTTPHosts_KeywordAndIvarChain(t *testing.T) {
	dir := t.TempDir()
	conn := writeRuby(t, dir, "connection.rb", `
class Connection
  def initialize
    @lyra_host = ENV.fetch("LYRA_HOST")
    @service_base_url = "#{lyra_host}/service_api/v1"
  end
  attr_accessor :service_base_url, :lyra_host

  def update_job_status_url
    "#{service_base_url}/job_items/update_job_status"
  end
end
`)
	// Direct call form: rest.get(path: Connection.instance.update_job_status_url)
	downloader := writeRuby(t, dir, "downloader.rb", `
class Downloader
  def fetch
    rest.get(path: Connection.instance.update_job_status_url)
  end
end
`)
	nodes := []graph.Node{httpClientNode(downloader, 4, "path: Connection.instance.update_job_status_url")}
	svcFiles := map[string][]string{"svc": {downloader, conn}}

	ResolveRubyHTTPHosts(nodes, svcFiles)
	if got := nodes[0].Meta["host_env_var"]; got != "LYRA_HOST" {
		t.Errorf("host_env_var = %q, want LYRA_HOST (ivar chain via attr)", got)
	}
}

// TestResolveRubyHTTPHosts_Abstains verifies the pass leaves a node untouched
// (no fabrication, #12) when the URL does not trace to an env-derived host
// method: a non-host local (`path_from_trace(...)`) and a token method.
func TestResolveRubyHTTPHosts_Abstains(t *testing.T) {
	dir := t.TempDir()
	src := writeRuby(t, dir, "parser.rb", `
class TraceParser
  def parse(line)
    rest.get(path: path_from_trace(line).to_s)
  end

  def path_from_trace(line)
    line.split(" ").first
  end
end
`)
	nodes := []graph.Node{httpClientNode(src, 4, "path: path_from_trace(line).to_s")}
	svcFiles := map[string][]string{"svc": {src}}

	changed := ResolveRubyHTTPHosts(nodes, svcFiles)
	if len(changed) != 0 {
		t.Fatalf("expected no resolution, got %d", len(changed))
	}
	if nodes[0].Meta["key_dynamic_raw"] != "path: path_from_trace(line).to_s" {
		t.Errorf("raw was rewritten: %q", nodes[0].Meta["key_dynamic_raw"])
	}
}

// TestResolveRubyHTTPHosts_Collision verifies a host-method name defined in two
// files with *different* env vars is an ambiguous collision — dropped from the
// registry, so neither resolves (honest miss, not a coin-flip guess).
func TestResolveRubyHTTPHosts_Collision(t *testing.T) {
	dir := t.TempDir()
	a := writeRuby(t, dir, "a_conn.rb", `
module A
  def api_url
    ENV.fetch("A_HOST")
  end
end
`)
	b := writeRuby(t, dir, "b_conn.rb", `
module B
  def api_url
    ENV.fetch("B_HOST")
  end
end
`)
	caller := writeRuby(t, dir, "caller.rb", `
class Caller
  def go
    RestClient.post(api_url, {})
  end
end
`)
	nodes := []graph.Node{httpClientNode(caller, 4, "api_url")}
	svcFiles := map[string][]string{"svc": {a, b, caller}}

	changed := ResolveRubyHTTPHosts(nodes, svcFiles)
	if len(changed) != 0 {
		t.Fatalf("collision should not resolve, got %d", len(changed))
	}
}

// TestResolveRubyHTTPHosts_NonRubyIgnored verifies non-Ruby http_client nodes
// and already-resolved (ENV.-carrying) nodes are skipped.
func TestResolveRubyHTTPHosts_NonRubyIgnored(t *testing.T) {
	dir := t.TempDir()
	conn := writeRuby(t, dir, "connection_info.rb", `
def server_api_url(e)
  ENV.fetch("LYRA_APP")
end
`)
	base := writeRuby(t, dir, "base.rb", `
def register
  url = server_api_url("x").to_s
  RestClient.post(url)
end
`)
	jsNode := httpClientNode(base, 4, "url")
	jsNode.Language = "javascript"
	alreadyDone := httpClientNode(base, 4, `ENV.fetch("LYRA_APP")`)
	nodes := []graph.Node{jsNode, alreadyDone}
	svcFiles := map[string][]string{"svc": {base, conn}}

	changed := ResolveRubyHTTPHosts(nodes, svcFiles)
	if len(changed) != 0 {
		t.Fatalf("non-ruby / already-resolved nodes must be skipped, got %d", len(changed))
	}
}
