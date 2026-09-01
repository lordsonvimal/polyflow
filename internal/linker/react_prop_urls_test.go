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

func TestLinkReactPropURLs_OneHopLocalAssignment(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	erb := filepath.Join(dir, "ffu.html.erb")
	require.NoError(t, os.WriteFile(erb, []byte(
		`<%= react_component("Up", { add_lro_details_url: add_details_client_api_v1_lro_url(0) }) %>`), 0o644))
	jsx := filepath.Join(dir, "Up.jsx")
	require.NoError(t, os.WriteFile(jsx, []byte(`export const Up = (props) => {
  const { add_lro_details_url } = props;
  const send = async (lroId) => {
    const url = add_lro_details_url.replace("/0/", ` + "`/${lroId}/`" + `);
    const r = await apiPost(url, { x: 1 });
    return r;
  };
};
window.Up = Up;
`), 0o644))

	nodes := []graph.Node{
		{
			ID: "n:routes:http_handler:add", Type: graph.NodeTypeHTTPHandler, Service: "orion", File: "config/routes.rb",
			Meta: map[string]string{"route_helper": "add_details_client_api_v1_lro", "path": "/client_api/v1/lros/:id/add_details", "method": "POST"},
		},
		{
			ID: "js:" + jsx + ":function:Up:1", Type: graph.NodeTypeFunction, Label: "Up", Service: "js", File: jsx,
			Meta: map[string]string{"name": "Up"},
		},
		{
			ID: "js:" + jsx + ":variable:Up:9", Type: graph.NodeTypeVariable, Label: "Up", Service: "js", File: jsx,
			Meta: map[string]string{"global_symbol": "Up", "scope": "global"},
		},
		{
			ID: "js:" + jsx + ":http_client:x", Type: graph.NodeTypeHTTPClient, Service: "js", File: jsx, Line: 5, Language: "javascript",
			Meta: map[string]string{"pattern": "js_api_wrapper_call_site", "wrapper": "apiPost", "url_expr": "url", "key_dynamic": "true", "key_dynamic_raw": "url"},
		},
	}

	changed := linker.LinkReactPropURLs(nodes, map[string][]string{"orion": {erb}, "js": {jsx}})
	require.Len(t, changed, 1)
	assert.Equal(t, "/client_api/v1/lros/*/add_details", changed[0].Meta["url"])
	assert.Equal(t, "POST", changed[0].Meta["method"])
	assert.NotEqual(t, "true", changed[0].Meta["key_dynamic"])
}
