package pipeline

// BenchmarkJSLocalURL (Tier RC.2 before/after gate,
// docs/js-declarative-composition-cluster-plan.md) — the post-migration
// counterpart of internal/linker's retired BenchmarkResolveJSLocalURLs
// (same 40-file, one-dynamic-site-per-file corpus), run against the
// pipeline.Run("js_local_url") path instead of the direct Go call.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func BenchmarkJSLocalURL(b *testing.B) {
	dir := b.TempDir()
	const numFiles = 40
	nodes := make([]graph.Node, 0, numFiles)
	files := make([]string, 0, numFiles)
	for i := 0; i < numFiles; i++ {
		rel := fmt.Sprintf("modules/f%d.es6", i)
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			b.Fatal(err)
		}
		src := fmt.Sprintf(`function reload%d(id, isNew) {
  let url;
  if (isNew) {
    url = "/app/things%d/new";
  } else {
    url = `+"`/app/things%d/${id}/edit`"+`;
  }
  $.ajax({ url, type: "GET" });
}
`, i, i, i)
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			b.Fatal(err)
		}
		files = append(files, p)
		nodes = append(nodes, graph.Node{
			ID: fmt.Sprintf("svc:f%d.es6:http_client:8", i), Type: graph.NodeTypeHTTPClient,
			Label: "dynamic", Service: "svc", File: p, Line: 8, Language: "javascript",
			Meta: map[string]string{
				"method": "GET", "key_dynamic": "true", "key_dynamic_raw": `{ url, type: "GET" }`,
			},
		})
	}

	reg, err := LoadEmbedded()
	if err != nil {
		b.Fatalf("LoadEmbedded: %v", err)
	}
	fw := reg.ByName("js_local_url")
	if fw == nil {
		b.Fatal("js_local_url framework not embedded")
	}
	active := []*Framework{fw}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		in := make([]graph.Node, len(nodes))
		copy(in, nodes)
		for j := range in {
			m := make(map[string]string, len(in[j].Meta))
			for k, v := range in[j].Meta {
				m[k] = v
			}
			in[j].Meta = m
		}
		if _, err := Run(active, nil, graph.Snapshot{Nodes: in, Files: files}); err != nil {
			b.Fatal(err)
		}
	}
}
