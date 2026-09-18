package pipeline

// BenchmarkJSPropCrossings (Tier RC.3 before/after gate,
// docs/js-declarative-composition-cluster-plan.md) — the post-migration
// counterpart of a synthetic corpus exercising both UB.2 and UB.3 crossings,
// run against the pipeline.Run("js_prop_crossings") path instead of direct
// calls to the retired linker.LinkJSPropURLs/LinkJSPropTransport.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func BenchmarkJSPropCrossings(b *testing.B) {
	dir := b.TempDir()
	const numSites = 40
	var nodes []graph.Node
	var files []string
	var ledger []graph.UnresolvedRef

	for i := 0; i < numSites; i++ {
		consumerRel := fmt.Sprintf("modules/Consumer%d.jsx", i)
		producerRel := fmt.Sprintf("modules/Producer%d.jsx", i)
		consumer := filepath.Join(dir, consumerRel)
		producer := filepath.Join(dir, producerRel)
		if err := os.MkdirAll(filepath.Dir(consumer), 0o755); err != nil {
			b.Fatal(err)
		}
		consumerSrc := fmt.Sprintf(`export default class WhereUsed%d extends React.Component {
  load = () => {
    const { ajaxStatus, dataURL } = this.props;
    ajaxStatus.get("Loading...", dataURL);
  };
}
`, i)
		producerSrc := fmt.Sprintf(`export function Producer%d({ ajaxStatus, id }) {
  return <WhereUsed%d ajaxStatus={ajaxStatus} dataURL={`+"`/api/things%d/${id}/usage`"+`} />;
}
`, i, i, i)
		if err := os.WriteFile(consumer, []byte(consumerSrc), 0o644); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(producer, []byte(producerSrc), 0o644); err != nil {
			b.Fatal(err)
		}
		files = append(files, consumer, producer)
		nodes = append(nodes,
			graph.Node{
				ID: fmt.Sprintf("svc:%s:class:WhereUsed%d", consumerRel, i), Type: graph.NodeTypeClass,
				Label: fmt.Sprintf("WhereUsed%d", i), Service: "svc", File: consumer, Line: 1, Language: "javascript",
			},
			graph.Node{
				ID: fmt.Sprintf("svc:%s:method:load%d", consumerRel, i), Type: graph.NodeTypeMethod,
				Label: "load", Service: "svc", File: consumer, Line: 2, Language: "javascript",
			},
		)
		ledger = append(ledger, graph.UnresolvedRef{
			Service: "svc", File: consumer, Line: 4, Kind: "prop_client_dynamic_url", Name: "(dynamic)",
		})
	}

	reg, err := LoadEmbedded()
	if err != nil {
		b.Fatalf("LoadEmbedded: %v", err)
	}
	fw := reg.ByName("js_prop_crossings")
	if fw == nil {
		b.Fatal("js_prop_crossings framework not embedded")
	}
	active := []*Framework{fw}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		in := make([]graph.Node, len(nodes))
		copy(in, nodes)
		led := make([]graph.UnresolvedRef, len(ledger))
		copy(led, ledger)
		if _, err := Run(active, nil, graph.Snapshot{Nodes: in, Files: files, Unresolved: led}); err != nil {
			b.Fatal(err)
		}
	}
}
