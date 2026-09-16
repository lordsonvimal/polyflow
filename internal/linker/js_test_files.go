package linker

import "github.com/lordsonvimal/polyflow/internal/jsast"

// crIsTestFile delegates to internal/jsast.IsTestFile. Moved here from
// internal/linker/js_prop_client.go (Tier FX, schema_url_link +
// js_prop_clients migration) after that file's own pass (LinkJSPropClients)
// migrated to internal/factpipe/hub_schema_url_link.go — crIsTestFile is
// still needed by valuegraph_adapter.go, the same "trimmed remnant" shape as
// ruby_host_registry.go/js_prop_url_helpers.go from earlier migrations.
func crIsTestFile(rel string) bool {
	return jsast.IsTestFile(rel)
}
