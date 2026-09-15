package pipeline_test

// Tier FX migration of internal/linker/ruby_http_hosts.go's retired
// ResolveRubyHTTPHosts (Tier L) — replaced by
// patterns/ruby/ruby_http_hosts.yaml + rules/ruby/ruby_http_hosts.dl, driven
// by the "ruby_http_hosts" hub provider
// (internal/factpipe/hub_ruby_http_hosts.go). These port the retired Go test
// suite verbatim through pipeline.Run.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

func rhWriteRuby(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

// rhHTTPClientNode builds a dynamic Ruby http_client node whose URL token
// went unresolved (key_dynamic), the state the pattern layer leaves for
// Tier-L.
func rhHTTPClientNode(file string, line int, raw string) graph.Node {
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

// rhRun runs the "ruby_http_hosts" framework and returns the merged nodes
// (patches applied in place), mirroring the retired
// ResolveRubyHTTPHosts(nodes, serviceFiles) signature closely enough that
// the ported assertions below read the same way.
func rhRun(t *testing.T, nodes []graph.Node, svcFiles map[string][]string) []graph.Node {
	t.Helper()
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	fw := reg.ByName("ruby_http_hosts")
	if fw == nil {
		t.Fatal("ruby_http_hosts framework not embedded")
	}
	byID := make(map[string]int, len(nodes))
	for i := range nodes {
		byID[nodes[i].ID] = i
	}
	var changedIDs []string
	for svc, files := range svcFiles {
		var svcNodes []graph.Node
		for i := range nodes {
			if nodes[i].Service == svc {
				svcNodes = append(svcNodes, nodes[i])
			}
		}
		if len(svcNodes) == 0 {
			continue
		}
		res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, graph.Snapshot{Nodes: svcNodes, Files: files})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		for _, p := range res.Patches {
			idx, ok := byID[p.ID]
			if !ok {
				continue
			}
			n := nodes[idx]
			m := make(map[string]string, len(n.Meta)+len(p.Meta))
			for k, v := range n.Meta {
				m[k] = v
			}
			for k, v := range p.Meta {
				m[k] = v
			}
			for _, k := range p.DeleteMeta {
				delete(m, k)
			}
			n.Meta = m
			nodes[idx] = n
			changedIDs = append(changedIDs, p.ID)
		}
	}
	seen := make(map[string]bool, len(changedIDs))
	var changed []graph.Node
	for _, id := range changedIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		changed = append(changed, nodes[byID[id]])
	}
	return changed
}

func TestRubyHTTPHosts_LocalAssignment(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	conn := rhWriteRuby(t, dir, "connection_info.rb", `
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
	base := rhWriteRuby(t, dir, "base_node.rb", `
class BaseNode
  def register(_task = nil)
    url = server_api_url("client_api/v1/agents/register").to_s
    RestClient.post(url, payload, content_type: "application/json")
  end
end
`)
	nodes := []graph.Node{rhHTTPClientNode(base, 5, "url")}
	svcFiles := map[string][]string{"svc": {base, conn}}

	changed := rhRun(t, nodes, svcFiles)
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
	if got := nodes[0].Meta["path"]; got != "*/client_api/v1/agents/register" {
		t.Errorf("path = %q, want */client_api/v1/agents/register", got)
	}
	if nodes[0].Meta["key_dynamic"] != "" {
		t.Errorf("a node that gained a path must stop being key_dynamic, got %q", nodes[0].Meta["key_dynamic"])
	}
}

func TestRubyHTTPHosts_ParamCaller(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	conn := rhWriteRuby(t, dir, "connection_info.rb", `
module Server
  module ConnectionInfo
    def server_api_uri
      URI.join(ENV.fetch("LYRA_HOST", nil), "client_api/v1/agents/register")
    end
  end
end
`)
	agent := rhWriteRuby(t, dir, "agent_node.rb", `
class AgentNode
  def register
    response = post_request(server_api_uri.to_s, payload)
  end

  def post_request(url, payload)
    RestClient.post(url, payload, content_type: "application/json")
  end
end
`)
	nodes := []graph.Node{rhHTTPClientNode(agent, 7, "url")}
	svcFiles := map[string][]string{"svc": {agent, conn}}

	rhRun(t, nodes, svcFiles)
	if got := nodes[0].Meta["host_env_var"]; got != "LYRA_HOST" {
		t.Errorf("host_env_var = %q, want LYRA_HOST (param→caller trace)", got)
	}
}

func TestRubyHTTPHosts_KeywordAndIvarChain(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	conn := rhWriteRuby(t, dir, "connection.rb", `
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
	downloader := rhWriteRuby(t, dir, "downloader.rb", `
class Downloader
  def fetch
    rest.get(path: Connection.instance.update_job_status_url)
  end
end
`)
	nodes := []graph.Node{rhHTTPClientNode(downloader, 4, "path: Connection.instance.update_job_status_url")}
	svcFiles := map[string][]string{"svc": {downloader, conn}}

	rhRun(t, nodes, svcFiles)
	if got := nodes[0].Meta["host_env_var"]; got != "LYRA_HOST" {
		t.Errorf("host_env_var = %q, want LYRA_HOST (ivar chain via attr)", got)
	}
	if got := nodes[0].Meta["path"]; got != "*/service_api/v1/job_items/update_job_status" {
		t.Errorf("path = %q, want */service_api/v1/job_items/update_job_status", got)
	}
	if got := nodes[0].Meta["path_resolved_via"]; got != "ruby_host_method" {
		t.Errorf("path_resolved_via = %q, want ruby_host_method", got)
	}
}

func TestRubyHTTPHosts_HostOnlyKeepsDynamic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	conn := rhWriteRuby(t, dir, "conn.rb", `
class Conn
  def lyra_host_url
    ENV.fetch("LYRA_HOST")
  end
end
`)
	caller := rhWriteRuby(t, dir, "caller.rb", `
class Caller
  def go
    RestClient.post(lyra_host_url, {})
  end
end
`)
	nodes := []graph.Node{rhHTTPClientNode(caller, 4, "lyra_host_url")}
	changed := rhRun(t, nodes, map[string][]string{"svc": {conn, caller}})
	if len(changed) != 1 {
		t.Fatalf("expected the host to still resolve, got %d", len(changed))
	}
	if got := nodes[0].Meta["host_env_var"]; got != "LYRA_HOST" {
		t.Errorf("host_env_var = %q, want LYRA_HOST", got)
	}
	if got := nodes[0].Meta["path"]; got != "" {
		t.Errorf("a host with no route must set no path, got %q", got)
	}
	if nodes[0].Meta["key_dynamic"] != "true" {
		t.Error("host-only resolution must stay key_dynamic for config_resolve")
	}
}

func TestRubyHTTPHosts_UnfilledHoleIsNotAPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	conn := rhWriteRuby(t, dir, "conn.rb", `
module Conn
  def server_api_url(endpoint)
    "#{ENV.fetch('LYRA_APP')}/#{endpoint}"
  end
end
`)
	caller := rhWriteRuby(t, dir, "caller.rb", `
class Caller
  def go(ep)
    RestClient.post(server_api_url(ep), {})
  end
end
`)
	nodes := []graph.Node{rhHTTPClientNode(caller, 4, "server_api_url(ep)")}
	rhRun(t, nodes, map[string][]string{"svc": {conn, caller}})
	if got := nodes[0].Meta["host_env_var"]; got != "LYRA_APP" {
		t.Errorf("host_env_var = %q, want LYRA_APP", got)
	}
	if got := nodes[0].Meta["path"]; got != "" {
		t.Errorf("an all-wildcard template must not become a path, got %q", got)
	}
	if nodes[0].Meta["key_dynamic"] != "true" {
		t.Error("no path means the node stays key_dynamic")
	}
}

func TestRubyHTTPHosts_PathConflictKeepsHost(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := rhWriteRuby(t, dir, "a.rb", `
module A
  def status_url
    "#{ENV.fetch('LYRA_HOST')}/api/v1/alpha"
  end
end
`)
	b := rhWriteRuby(t, dir, "b.rb", `
module B
  def status_url
    "#{ENV.fetch('LYRA_HOST')}/api/v1/beta"
  end
end
`)
	caller := rhWriteRuby(t, dir, "caller.rb", `
class Caller
  def go
    RestClient.post(status_url, {})
  end
end
`)
	nodes := []graph.Node{rhHTTPClientNode(caller, 4, "status_url")}
	rhRun(t, nodes, map[string][]string{"svc": {a, b, caller}})
	if got := nodes[0].Meta["host_env_var"]; got != "LYRA_HOST" {
		t.Errorf("host_env_var = %q, want LYRA_HOST (env is unambiguous)", got)
	}
	if got := nodes[0].Meta["path"]; got != "" {
		t.Errorf("conflicting routes must yield no path, got %q", got)
	}
}

func TestRubyHTTPHosts_Abstains(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := rhWriteRuby(t, dir, "parser.rb", `
class TraceParser
  def parse(line)
    rest.get(path: path_from_trace(line).to_s)
  end

  def path_from_trace(line)
    line.split(" ").first
  end
end
`)
	nodes := []graph.Node{rhHTTPClientNode(src, 4, "path: path_from_trace(line).to_s")}
	svcFiles := map[string][]string{"svc": {src}}

	changed := rhRun(t, nodes, svcFiles)
	if len(changed) != 0 {
		t.Fatalf("expected no resolution, got %d", len(changed))
	}
	if nodes[0].Meta["key_dynamic_raw"] != "path: path_from_trace(line).to_s" {
		t.Errorf("raw was rewritten: %q", nodes[0].Meta["key_dynamic_raw"])
	}
}

func TestRubyHTTPHosts_Collision(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := rhWriteRuby(t, dir, "a_conn.rb", `
module A
  def api_url
    ENV.fetch("A_HOST")
  end
end
`)
	b := rhWriteRuby(t, dir, "b_conn.rb", `
module B
  def api_url
    ENV.fetch("B_HOST")
  end
end
`)
	caller := rhWriteRuby(t, dir, "caller.rb", `
class Caller
  def go
    RestClient.post(api_url, {})
  end
end
`)
	nodes := []graph.Node{rhHTTPClientNode(caller, 4, "api_url")}
	svcFiles := map[string][]string{"svc": {a, b, caller}}

	changed := rhRun(t, nodes, svcFiles)
	if len(changed) != 0 {
		t.Fatalf("collision should not resolve, got %d", len(changed))
	}
}

func TestRubyHTTPHosts_NonRubyIgnored(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	conn := rhWriteRuby(t, dir, "connection_info.rb", `
def server_api_url(e)
  ENV.fetch("LYRA_APP")
end
`)
	base := rhWriteRuby(t, dir, "base.rb", `
def register
  url = server_api_url("x").to_s
  RestClient.post(url)
end
`)
	jsNode := rhHTTPClientNode(base, 4, "url")
	jsNode.Language = "javascript"
	alreadyDone := rhHTTPClientNode(base, 4, `ENV.fetch("LYRA_APP")`)
	alreadyDone.ID = "http:already:" + base
	nodes := []graph.Node{jsNode, alreadyDone}
	svcFiles := map[string][]string{"svc": {base, conn}}

	changed := rhRun(t, nodes, svcFiles)
	if len(changed) != 0 {
		t.Fatalf("non-ruby / already-resolved nodes must be skipped, got %d", len(changed))
	}
}

func TestRubyHTTPHosts_DelegateAttrEnv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	config := rhWriteRuby(t, dir, "config.rb", `
module W
  class Config
    OPTION_VARS = %i[dry_run url verbose].freeze
    attr_accessor(*OPTION_VARS, :errors)
    def initialize(options: {})
      @url = config_val(:url)
    end
    def config_val(option)
      options[option] || ENV[option.to_s.upcase] || DEFAULT_SETTINGS[option]
    end
  end
end
`)
	conn := rhWriteRuby(t, dir, "connection.rb", `
module W
  class Connection
    delegate :url, to: :config
    def execute(method, url, payload, headers, raw: false)
      response = Request.execute(method: method, url: url, payload: payload)
    end
  end
end
`)
	nodes := []graph.Node{rhHTTPClientNode(conn, 6, "url")}
	svcFiles := map[string][]string{"svc": {config, conn}}

	changed := rhRun(t, nodes, svcFiles)
	if len(changed) != 1 {
		t.Fatalf("expected 1 resolved node, got %d", len(changed))
	}
	if got := nodes[0].Meta["host_env_var"]; got != "URL" {
		t.Errorf("host_env_var = %q, want URL", got)
	}
	if got := nodes[0].Meta["key_dynamic_raw"]; got != `ENV.fetch("URL")` {
		t.Errorf("key_dynamic_raw = %q, want ENV.fetch(\"URL\")", got)
	}
}

func TestRubyHTTPHosts_DelegateNoEnv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	config := rhWriteRuby(t, dir, "config.rb", `
class Config
  attr_accessor :url
  def initialize(row)
    @url = row.fetch(:url) # a DB value, not ENV
  end
end
`)
	conn := rhWriteRuby(t, dir, "connection.rb", `
class Connection
  delegate :url, to: :config
  def execute(method, url)
    Request.execute(method: method, url: url)
  end
end
`)
	nodes := []graph.Node{rhHTTPClientNode(conn, 5, "url")}
	svcFiles := map[string][]string{"svc": {config, conn}}

	if changed := rhRun(t, nodes, svcFiles); len(changed) != 0 {
		t.Fatalf("no env anywhere → must abstain, got %d resolved", len(changed))
	}
	if nodes[0].Meta["host_env_var"] != "" {
		t.Errorf("host_env_var must stay empty, got %q", nodes[0].Meta["host_env_var"])
	}
}
