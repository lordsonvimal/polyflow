package linker_test

// Ported from the retired internal/linker/react_prop_urls.go (Tier FX
// migration of LinkReactPropURLs to patterns/generic/react_prop_urls.yaml +
// internal/factpipe/hub_react_prop_urls.go): reactPropURLsApply below runs
// the framework and merges its patch: output exactly like
// internal/indexer/link_passes.go's react_prop_urls pass does, in place of
// the retired function's direct node mutation. Returns only the nodes that
// were actually patched, matching the retired function's `changed []graph.Node`
// return contract the assertions below rely on.

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

func reactPropURLsApply(t *testing.T, nodes []graph.Node, serviceFiles map[string][]string) []graph.Node {
	t.Helper()
	reg, err := pipeline.LoadEmbedded()
	require.NoError(t, err)
	fw := reg.ByName("react_prop_urls")
	require.NotNil(t, fw, "react_prop_urls framework not embedded")

	var allFiles []string
	for _, fs := range serviceFiles {
		allFiles = append(allFiles, fs...)
	}

	res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, graph.Snapshot{Nodes: nodes, Files: allFiles})
	require.NoError(t, err)

	byID := make(map[string]int, len(nodes))
	for i := range nodes {
		byID[nodes[i].ID] = i
	}
	var changed []graph.Node
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
		for _, k := range p.DeleteMeta {
			delete(m, k)
		}
		for k, v := range p.Meta {
			m[k] = v
		}
		n.Meta = m
		if p.Label != "" {
			n.Label = p.Label
		}
		changed = append(changed, n)
	}
	return changed
}

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
			ID: "js:UppyUploader.jsx:http_client:x:" + wrapper + ":" + strconv.Itoa(line), Type: graph.NodeTypeHTTPClient,
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
		client(20, "apiPost", `sign_part_url.replace("/0/", `+"`/${x}/`"+`)`),
		client(30, "apiPost", "bogus_url"),
		client(40, "apiPost", "url"), // bare `url` — must abstain (local-var risk)
	}

	changed := reactPropURLsApply(t, nodes, map[string][]string{"orion": {erb}, "js": {"UppyUploader.jsx"}})

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

func TestLinkReactPropURLs_JSXToJSXTemplateLiteralProp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	child := filepath.Join(dir, "JobDetailModal.jsx")
	require.NoError(t, os.WriteFile(child, []byte(`import React from "react";
import { apiGet } from "../services/ApiServices";
export function JobDetailModal({ onClose, url, updateKey }) {
  const load = () => apiGet(url).then((r) => r.data);
  return <div onClick={load}>{onClose}</div>;
}
`), 0o644))
	parent := filepath.Join(dir, "ProgressiveFeedbackCard.jsx")
	require.NoError(t, os.WriteFile(parent, []byte(`import React from "react";
import { JobDetailModal } from "./JobDetailModal";
export function ProgressiveFeedbackCard({ lroInfo }) {
  return (
    <JobDetailModal onClose={() => {}} url={`+"`/app/lro/${lroInfo.lroId}?study_id=${lroInfo.studyId}`"+`} />
  );
}
`), 0o644))

	nodes := []graph.Node{
		{
			ID: "n:routes:http_handler:lro_show", Type: graph.NodeTypeHTTPHandler, Service: "orion", File: "config/routes.rb",
			Meta: map[string]string{"route_helper": "lro", "path": "/app/lro/:id", "method": "GET"},
		},
		{
			ID: "js:" + child + ":function:JobDetailModal:3", Type: graph.NodeTypeFunction, Label: "JobDetailModal",
			Service: "js", File: child, Meta: map[string]string{"name": "JobDetailModal"},
		},
		{
			ID: "js:" + parent + ":function:ProgressiveFeedbackCard:3", Type: graph.NodeTypeFunction, Label: "ProgressiveFeedbackCard",
			Service: "js", File: parent, Meta: map[string]string{"name": "ProgressiveFeedbackCard"},
		},
		{
			ID: "js:" + child + ":http_client:x", Type: graph.NodeTypeHTTPClient, Service: "js", File: child, Line: 4, Language: "javascript",
			Meta: map[string]string{"pattern": "js_api_wrapper_call_site", "wrapper": "apiGet", "url_expr": "url", "key_dynamic": "true", "key_dynamic_raw": "url"},
		},
	}

	changed := reactPropURLsApply(t, nodes, map[string][]string{"js": {child, parent}})
	require.Len(t, changed, 1)
	assert.Equal(t, "/app/lro/*", changed[0].Meta["url"])
	assert.Equal(t, "GET", changed[0].Meta["method"])
	assert.NotEqual(t, "true", changed[0].Meta["key_dynamic"])
	assert.Equal(t, "react_prop_url", changed[0].Meta["path_resolved_via"])
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
    const url = add_lro_details_url.replace("/0/", `+"`/${lroId}/`"+`);
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

	changed := reactPropURLsApply(t, nodes, map[string][]string{"orion": {erb}, "js": {jsx}})
	require.Len(t, changed, 1)
	assert.Equal(t, "/client_api/v1/lros/*/add_details", changed[0].Meta["url"])
	assert.Equal(t, "POST", changed[0].Meta["method"])
	assert.NotEqual(t, "true", changed[0].Meta["key_dynamic"])
}
