package indexer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// wantLinkPassNames is the pipeline's expected shape, in execution order.
// FR.5a's tripwire: a change here must be a deliberate edit to
// buildLinkPasses (reordering, adding, or removing a pass), never an
// accidental drift.
var wantLinkPassNames = []string{
	"js_link",
	"js_globals",
	"js_type_relations",
	"ruby_type_relations",
	"ruby_class_method_calls",
	"ruby_receiver_type_calls",
	"ruby_associations",
	"rails_filters",
	"ruby_mixin_methods",
	"ruby_wrapper_url_call_sites",
	"ruby_http_hosts",
	"go_http_hosts",
	"js_http_hosts",
	"config_baseurl",
	"route_handlers",
	"grpc_handlers",
	"rails_route_actions",
	"route_components",
	"templ_components",
	"templ_scripts",
	"dom_definitions",
	"dom_contracts",
	"containment",
	"ensure_scanned_files",
	"js_import_edges",
	"js_api_wrapper_calls",
	"stylesheet_imports",
	"sprockets_assets",
	"rails_views",
	"ruby_import_edges",
	"datastores",
	"tables",
	"response_shapes",
	"resource_signals",
	"sse_clients",
	"broker_hints",
	"rails_nav_helpers",
	"file_route_synthesis",
	"load_contract_rules",
	"apply_hints_and_enrich",
	"gin_middleware",
	"express_middleware",
	"enrich_aliases",
	"amqp_handshake",
	"amqp_message_type_dispatch",
	"contract_engine",
	"sse_push",
	"contract_coverage",
}

// TestBuildLinkPasses_MatchesPipelineShape proves buildLinkPasses builds the
// exact ordered list Run() expects, without needing a real index run — the
// closures are constructed but never executed here.
func TestBuildLinkPasses_MatchesPipelineShape(t *testing.T) {
	passes := buildLinkPasses(&linkPipelineState{})
	names := make([]string, len(passes))
	for i, p := range passes {
		names[i] = p.name
	}
	assert.Equal(t, wantLinkPassNames, names)
}
