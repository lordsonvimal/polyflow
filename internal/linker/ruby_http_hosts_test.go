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
	t.Parallel()
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
	// PR.3: the endpoint is the *argument* the call site passes, spliced into
	// the host method's parameter hole. `lyra_app` resolves to no path of its own
	// (it is a conditional, not a literal), so it reads as the "*" host segment
	// dynamic_host_strip removes downstream.
	if got := nodes[0].Meta["path"]; got != "*/client_api/v1/agents/register" {
		t.Errorf("path = %q, want */client_api/v1/agents/register", got)
	}
	if nodes[0].Meta["key_dynamic"] != "" {
		t.Errorf("a node that gained a path must stop being key_dynamic, got %q", nodes[0].Meta["key_dynamic"])
	}
}

// TestResolveRubyHTTPHosts_ParamCaller covers shape (3): the http_client posts
// to a *method parameter* whose value flows from a same-file caller passing a
// host method (`def post_request(url); RestClient.post(url) ← post_request(server_api_uri.to_s)`).
func TestResolveRubyHTTPHosts_ParamCaller(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	// PR.3: the path is composed across *two* hops of the same chain the env var
	// travels — @service_base_url contributes "/service_api/v1" and the method
	// body appends "/job_items/update_job_status". Before PR.3 both were
	// traversed and thrown away, which is why orion-lyra resolved 0% of its
	// client URLs despite this pass already reaching the right method.
	if got := nodes[0].Meta["path"]; got != "*/service_api/v1/job_items/update_job_status" {
		t.Errorf("path = %q, want */service_api/v1/job_items/update_job_status", got)
	}
	if got := nodes[0].Meta["path_resolved_via"]; got != "ruby_host_method" {
		t.Errorf("path_resolved_via = %q, want ruby_host_method", got)
	}
}

// TestResolveRubyHTTPHosts_HostOnlyKeepsDynamic pins the boundary of the PR.3
// trade: a host method that appends no route resolves the *host* exactly as
// before and stays key_dynamic, so config_resolve still gets to bind it (or
// ledger a named deploy-secret miss). Only nodes that gain a real path leave
// that path.
func TestResolveRubyHTTPHosts_HostOnlyKeepsDynamic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	conn := writeRuby(t, dir, "conn.rb", `
class Conn
  def lyra_host_url
    ENV.fetch("LYRA_HOST")
  end
end
`)
	caller := writeRuby(t, dir, "caller.rb", `
class Caller
  def go
    RestClient.post(lyra_host_url, {})
  end
end
`)
	nodes := []graph.Node{httpClientNode(caller, 4, "lyra_host_url")}
	changed := ResolveRubyHTTPHosts(nodes, map[string][]string{"svc": {conn, caller}})
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

// TestResolveRubyHTTPHosts_UnfilledHoleIsNotAPath verifies a host method whose
// endpoint parameter the call site does not supply as a literal yields no path
// at all rather than "*/*" — a template of nothing but wildcards would match
// every route in every service.
func TestResolveRubyHTTPHosts_UnfilledHoleIsNotAPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	conn := writeRuby(t, dir, "conn.rb", `
module Conn
  def server_api_url(endpoint)
    "#{ENV.fetch('LYRA_APP')}/#{endpoint}"
  end
end
`)
	caller := writeRuby(t, dir, "caller.rb", `
class Caller
  def go(ep)
    RestClient.post(server_api_url(ep), {})
  end
end
`)
	nodes := []graph.Node{httpClientNode(caller, 4, "server_api_url(ep)")}
	ResolveRubyHTTPHosts(nodes, map[string][]string{"svc": {conn, caller}})
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

// TestResolveRubyHTTPHosts_PathConflictKeepsHost verifies a host-method name
// defined twice with the same env var but *different* routes keeps the
// unambiguous host and drops the ambiguous path, rather than picking one.
func TestResolveRubyHTTPHosts_PathConflictKeepsHost(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := writeRuby(t, dir, "a.rb", `
module A
  def status_url
    "#{ENV.fetch('LYRA_HOST')}/api/v1/alpha"
  end
end
`)
	b := writeRuby(t, dir, "b.rb", `
module B
  def status_url
    "#{ENV.fetch('LYRA_HOST')}/api/v1/beta"
  end
end
`)
	caller := writeRuby(t, dir, "caller.rb", `
class Caller
  def go
    RestClient.post(status_url, {})
  end
end
`)
	nodes := []graph.Node{httpClientNode(caller, 4, "status_url")}
	ResolveRubyHTTPHosts(nodes, map[string][]string{"svc": {a, b, caller}})
	if got := nodes[0].Meta["host_env_var"]; got != "LYRA_HOST" {
		t.Errorf("host_env_var = %q, want LYRA_HOST (env is unambiguous)", got)
	}
	if got := nodes[0].Meta["path"]; got != "" {
		t.Errorf("conflicting routes must yield no path, got %q", got)
	}
}

// TestRubyClientPath covers the host-placeholder and concreteness rules of the
// final path stamp directly.
func TestRubyClientPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, in, want string
	}{
		{"prefixes the host placeholder", "/api/v1/health", "*/api/v1/health"},
		{"keeps an existing placeholder", "*/api/v1/health", "*/api/v1/health"},
		{"adds a leading slash", "api/v1/health", "*/api/v1/health"},
		{"unfilled hole becomes a wildcard", "/api/\x00id\x00/health", "*/api/*/health"},
		{"all-wildcard is not a path", "*/*", ""},
		{"host alone is not a path", "*", ""},
		{"empty stays empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rubyClientPath(tc.in); got != tc.want {
				t.Errorf("rubyClientPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRubyCallArgs covers the text-level argument splitter, including the
// nested-call and unbalanced cases where it must yield nothing rather than a
// wrong split.
func TestRubyCallArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, in string
		want     []string
	}{
		{"single string literal", `server_api_url("client_api/v1/x")`, []string{`"client_api/v1/x"`}},
		{"interpolated literal", `server_api_url("client_api/v1/lros/#{id}")`, []string{`"client_api/v1/lros/#{id}"`}},
		{"two args", `f("a", b)`, []string{`"a"`, "b"}},
		{"comma inside nested call", `f(g(a, b), c)`, []string{"g(a, b)", "c"}},
		{"comma inside string", `f("a,b")`, []string{`"a,b"`}},
		{"no arg list", `plain_url`, nil},
		{"unbalanced yields nothing", `f("a"`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rubyCallArgs(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("rubyCallArgs(%q) = %q, want %q", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("arg %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestResolveRubyHTTPHosts_Abstains verifies the pass leaves a node untouched
// (no fabrication, #12) when the URL does not trace to an env-derived host
// method: a non-host local (`path_from_trace(...)`) and a token method.
func TestResolveRubyHTTPHosts_Abstains(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// TestResolveRubyHTTPHosts_DelegateAttrEnv covers L.1: the host is a bare `url`
// that is `delegate :url, to: :config`, and `Config#url` is an attr_accessor
// (via a `*CONST` splat) backed by `@url = config_val(:url)` whose helper reads
// `ENV[option.to_s.upcase]`. The whole chain lives across three files.
func TestResolveRubyHTTPHosts_DelegateAttrEnv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	config := writeRuby(t, dir, "config.rb", `
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
	conn := writeRuby(t, dir, "connection.rb", `
module W
  class Connection
    delegate :url, to: :config
    def execute(method, url, payload, headers, raw: false)
      response = Request.execute(method: method, url: url, payload: payload)
    end
  end
end
`)
	nodes := []graph.Node{httpClientNode(conn, 6, "url")}
	svcFiles := map[string][]string{"svc": {config, conn}}

	changed := ResolveRubyHTTPHosts(nodes, svcFiles)
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

// TestResolveRubyHTTPHosts_DelegateNoEnv: a delegated host-ish name with no
// env-derived definition anywhere must not resolve (no fabricated host).
func TestResolveRubyHTTPHosts_DelegateNoEnv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	config := writeRuby(t, dir, "config.rb", `
class Config
  attr_accessor :url
  def initialize(row)
    @url = row.fetch(:url) # a DB value, not ENV
  end
end
`)
	conn := writeRuby(t, dir, "connection.rb", `
class Connection
  delegate :url, to: :config
  def execute(method, url)
    Request.execute(method: method, url: url)
  end
end
`)
	nodes := []graph.Node{httpClientNode(conn, 5, "url")}
	svcFiles := map[string][]string{"svc": {config, conn}}

	if changed := ResolveRubyHTTPHosts(nodes, svcFiles); len(changed) != 0 {
		t.Fatalf("no env anywhere → must abstain, got %d resolved", len(changed))
	}
	if nodes[0].Meta["host_env_var"] != "" {
		t.Errorf("host_env_var must stay empty, got %q", nodes[0].Meta["host_env_var"])
	}
}
