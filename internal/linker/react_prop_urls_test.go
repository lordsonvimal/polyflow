package linker_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
)

func TestLinkReactPropURLs_ResolvesPropFedEndpoint(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	erb := filepath.Join(dir, "ffu.html.erb")
	require.NoError(t, os.WriteFile(erb, []byte(`<div>
<%= react_component("UppyUploader", {
  create_lro_url: "/client_api/v1/lros",
  sign_part_url: sign_part_folder_fast_uploads_url(@folder),
  bogus_url: helper_that_is_not_a_route_url(@x),
  url: presigned_url_folder_fast_uploads_url(@folder),
}) %>
</div>`), 0o644))

	route := func(helper, path, method string) graph.Node {
		return graph.Node{
			ID: "orion:config/routes.rb:http_handler:" + method + " " + path, Type: graph.NodeTypeHTTPHandler,
			Service: "orion", File: "config/routes.rb",
			Meta: map[string]string{"route_helper": helper, "path": path, "method": method},
		}
	}
	client := func(line int, wrapper, urlExpr string) graph.Node {
		return graph.Node{
			ID:      "js:UppyUploader.jsx:http_client:x:" + wrapper, Type: graph.NodeTypeHTTPClient,
			Service: "js", File: "UppyUploader.jsx", Line: line, Language: "javascript",
			Meta: map[string]string{"pattern": "js_api_wrapper_call_site", "wrapper": wrapper, "url_expr": urlExpr, "key_dynamic": "true", "key_dynamic_raw": urlExpr},
		}
	}

	nodes := []graph.Node{
		route("client_api_v1_lros", "/client_api/v1/lros", "GET"),
		route("client_api_v1_lros", "/client_api/v1/lros", "POST"),
		route("sign_part_folder_fast_uploads", "/app/folders/:folder_id/fast_uploads/sign_part", "POST"),
		route("presigned_url_folder_fast_uploads", "/app/folders/:folder_id/fast_uploads/presigned_url", "POST"),
		{
			ID: "js:UppyUploader.jsx:function:UppyUploader:1", Type: graph.NodeTypeFunction,
			Label: "UppyUploader", Service: "js", File: "UppyUploader.jsx",
			Meta: map[string]string{"name": "UppyUploader"},
		},
		{
			ID: "js:UppyUploader.jsx:variable:UppyUploader:99", Type: graph.NodeTypeVariable,
			Label: "UppyUploader", Service: "js", File: "UppyUploader.jsx",
			Meta: map[string]string{"global_symbol": "UppyUploader", "scope": "global"},
		},
		client(10, "apiPost", "create_lro_url"),
		client(20, "apiPost", `sign_part_url.replace("/0/", ` + "`/${x}/`" + `)`),
		client(30, "apiPost", "bogus_url"),
		client(40, "apiPost", "url"), // bare `url` — must abstain (local-var risk)
	}

	changed := linker.LinkReactPropURLs(nodes, map[string][]string{"orion": {erb}, "js": {"UppyUploader.jsx"}})

	byLine := map[int]graph.Node{}
	for _, n := range changed {
		byLine[n.Line] = n
	}
	require.Contains(t, byLine, 10)
	assert.Equal(t, "/client_api/v1/lros", byLine[10].Meta["url"])
	assert.Equal(t, "POST", byLine[10].Meta["method"])
	assert.NotEqual(t, "true", byLine[10].Meta["key_dynamic"])
	assert.Equal(t, "react_prop_url", byLine[10].Meta["path_resolved_via"])

	require.Contains(t, byLine, 20)
	assert.Equal(t, "/app/folders/*/fast_uploads/sign_part", byLine[20].Meta["url"])

	assert.NotContains(t, byLine, 30, "helper that is not a route must abstain")
	assert.NotContains(t, byLine, 40, "bare `url` identifier must abstain")
}
