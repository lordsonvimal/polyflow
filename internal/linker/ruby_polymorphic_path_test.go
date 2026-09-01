package linker_test

// Tier L.2 — the polymorphic-sink path trace. Mirrors orion-vega-agent's
// NordicImportWatcher: a private Connection#execute reached from three public
// entry methods, each forwarding a different endpoint constant through keyword
// args and an ActiveSupport `delegate` on a separate Uploader class.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
)

const polyConfigRB = `module W
  class Config
    OPTION_VARS = %i[dry_run url verbose].freeze
    attr_accessor(*OPTION_VARS, :errors)
    def initialize(options: {})
      @url = config_val(:url)
    end
    def config_val(option)
      ENV[option.to_s.upcase]
    end
  end
end
`

const polyConnectionRB = `module W
  class Connection
    include RestClient
    VERIFY_ORGANIZATION = "lyra_api/v1/import/verify_credentials"
    attr_accessor :config
    delegate :url, to: :config

    def verify
      get(path: VERIFY_ORGANIZATION)
    end

    def get(path:, payload: {})
      execute(:get, target_url(path), payload)
    end

    def patch(path:, payload: {})
      patch_or_post(method: :patch, path: path, payload: payload)
    end

    def post(path:, payload: {})
      patch_or_post(method: :post, path: path, payload: payload)
    end

    private

    def patch_or_post(method:, path:, payload: {})
      execute(method, target_url(path), payload)
    end

    def execute(method, url, payload)
      Request.execute(method: method, url: url, payload: payload)
    end

    def target_url(path)
      URI.join(url, path).to_s
    end
  end
end
`

const polyUploaderRB = `module W
  class Uploader
    COMPLETE_IMPORT_PATH = "lyra_api/v1/import/complete_import"
    PRE_SIGNED_POST_PATH = "lyra_api/v1/import/presigned_post"

    delegate :get, :post, :patch, to: :connection

    def presigned_url
      post(path: PRE_SIGNED_POST_PATH, payload: {})
    end

    def update_file_information(params)
      patch(path: COMPLETE_IMPORT_PATH, payload: params)
    end

    def upload_file(url)
      post(path: url, payload: {})
    end
  end
end
`

func TestResolveRubyPolymorphicPathSites_VegaImportWatcher(t *testing.T) {
	t.Parallel()
	paths := writeRubyWrapperFixture(t, map[string]string{
		"config.rb":     polyConfigRB,
		"connection.rb": polyConnectionRB,
		"uploader.rb":   polyUploaderRB,
	})
	nodes := parseRubyWrapperFixture(t, "vega", paths)
	serviceFiles := map[string][]string{"vega": paths}

	// Precondition: ruby_http_hosts resolves the host but leaves the sink
	// key_dynamic (no single path).
	linker.ResolveRubyHTTPHosts(nodes, serviceFiles)
	var sink *graph.Node
	for i := range nodes {
		if nodes[i].Meta["pattern"] == "rest_client_request_included" {
			sink = &nodes[i]
		}
	}
	require.NotNil(t, sink, "sink http_client node not minted by the pattern layer")
	require.Equal(t, "URL", sink.Meta["host_env_var"])
	require.Equal(t, "true", sink.Meta["key_dynamic"])

	newNodes, edges := linker.ResolveRubyPolymorphicPathSites(nodes, serviceFiles)

	got := map[string]graph.Node{}
	for _, n := range newNodes {
		got[n.Meta["method"]+" "+n.Meta["path"]] = n
		assert.NotEqual(t, "true", n.Meta["key_dynamic"], "minted node must not stay key_dynamic")
		assert.Equal(t, "URL", n.Meta["host_env_var"])
		assert.Equal(t, `ENV.fetch("URL")`, n.Meta["key_dynamic_raw"])
	}

	for _, want := range []string{
		"GET */lyra_api/v1/import/verify_credentials",
		"POST */lyra_api/v1/import/presigned_post",
		"PATCH */lyra_api/v1/import/complete_import",
	} {
		assert.Contains(t, got, want, "missing minted endpoint")
	}
	// upload_file passes a local param, not a constant — must abstain.
	assert.Len(t, newNodes, 3, "exactly the three constant-backed endpoints, no fabricated node for the local-var call")

	require.NotEmpty(t, edges)
	for _, e := range edges {
		assert.Equal(t, graph.EdgeTypeCalls, e.Type)
	}
	// The sink itself is untouched.
	assert.Equal(t, "true", sink.Meta["key_dynamic"])
}

func TestResolveRubyPolymorphicPathSites_AbstainsWithoutConstant(t *testing.T) {
	t.Parallel()
	conn := `module W
  class Connection
    include RestClient
    attr_accessor :config
    delegate :url, to: :config
    def call(path)
      execute(:get, target_url(path), {})
    end
    def execute(method, url, payload)
      Request.execute(method: method, url: url, payload: payload)
    end
    def target_url(path)
      URI.join(url, path).to_s
    end
  end
end
`
	caller := `module W
  class Job
    delegate :call, to: :connection
    def run(endpoint)
      call(endpoint)
    end
  end
end
`
	paths := writeRubyWrapperFixture(t, map[string]string{
		"config.rb": polyConfigRB, "connection.rb": conn, "job.rb": caller,
	})
	nodes := parseRubyWrapperFixture(t, "vega", paths)
	serviceFiles := map[string][]string{"vega": paths}
	linker.ResolveRubyHTTPHosts(nodes, serviceFiles)

	newNodes, _ := linker.ResolveRubyPolymorphicPathSites(nodes, serviceFiles)
	assert.Empty(t, newNodes, "no literal or constant reaches the sink — must abstain")
}
