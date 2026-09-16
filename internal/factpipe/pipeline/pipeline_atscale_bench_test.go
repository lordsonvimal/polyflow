package pipeline

// XM.0 (docs/factpipe-cross-framework-matching-plan.md) — a second
// benchmark/test pair, sized to roughly orion's real scale (~2,868 files),
// that reproduces the cost pipeline_bench_test.go's small 4-file fixture
// cannot: the O(frameworks x files) extraction/eval cost that scales with
// corpus size, not the fixed per-Run-call overhead FX.8.PERF's own gate
// already covers. XM.1 and XM.2's win (if any) shows up here, not there.
//
// File content is synthetic-but-structurally-real (real before_action/def/
// class shapes, repeated with varying names) rather than random bytes or
// literal corpus content — tree-sitter has to actually parse and match it,
// and codename policy plus test portability both forbid reading real corpus
// files off disk in a committed test.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// atScaleFileCount mirrors orion's real file count order of magnitude
// (docs/factpipe-cross-framework-matching-plan.md's Context table: orion =
// 2,868 files) closely enough to reproduce its per-file scaling cost without
// this test's own runtime becoming unreasonable under plain `go test ./...`.
const atScaleFileCount = 2800

// atScaleRubyFile is benchRubySrc (pipeline_bench_test.go) with the
// controller/job names templated so every generated file mints distinct
// node IDs — a real corpus never has 2,800 files all declaring the same
// class name, and node IDs derived from file+class would silently collide
// if this fixture did.
func atScaleRubyFile(i int) string {
	return fmt.Sprintf(`class Demo%dController < ApplicationController
  before_action :authenticate
  after_action :audit
  helper_method :current_account

  def index; end
  def show; end

  def authenticate; end
  def audit; end
  def current_account; end
end

class Demo%dJob < ApplicationJob
  def perform; end
end
`, i, i)
}

// atScaleJSFile mirrors benchJSSrc's shapes (a mobx observer HOC + a
// conditional local-URL fetch), templated per file for the same reason as
// atScaleRubyFile.
func atScaleJSFile(i int) string {
	return fmt.Sprintf(`import { observer } from "mobx-react";

const Row%d = observer((props) => {
  return <div>{props.grid.total}</div>;
});
export default Row%d;

export function submitRow%d(ajaxStatus, formData) {
  let url;
  if (formData.id) {
    url = `+"`/api/rows%d/${formData.id}`"+`;
  } else {
    url = "/api/rows%d";
  }
  return ajaxStatus.ajax("Submitting...", { url, method: "POST" });
}
`, i, i, i, i, i)
}

// atScaleGoFile mirrors benchGoSrc's go_http_hosts shape.
func atScaleGoFile(i int) string {
	return fmt.Sprintf(`package main

import (
	"fmt"
	"net/http"
	"strings"
)

type WillowClient%d struct {
	baseURL string
	apiKey  string
}

func NewWillowClient%d(baseURL, apiKey string) *WillowClient%d {
	return &WillowClient%d{baseURL: strings.TrimSuffix(baseURL, "/"), apiKey: apiKey}
}

func (c *WillowClient%d) LookupUserByEmail(email string) error {
	reqURL := fmt.Sprintf("%%s/api/v1/users?email=%%s", c.baseURL, email)
	_, err := http.NewRequest("GET", reqURL, nil)
	return err
}
`, i, i, i, i, i)
}

// atScaleFixtureFiles generates atScaleFileCount files in roughly a real
// service's language mix — mostly Ruby (controllers dominate a Rails repo's
// file count), a meaningful JS minority, a small Go minority — cycling
// through the three generators above.
func atScaleFixtureFiles() []ParsedFile {
	files := make([]ParsedFile, 0, atScaleFileCount)
	for i := 0; i < atScaleFileCount; i++ {
		switch i % 10 {
		case 0, 1, 2, 3, 4, 5: // 60% ruby
			files = append(files, ParsedFile{
				Path: fmt.Sprintf("app/controllers/demo_%d_controller.rb", i), Language: "ruby",
				Src: []byte(atScaleRubyFile(i)),
			})
		case 6, 7, 8: // 30% js
			files = append(files, ParsedFile{
				Path: fmt.Sprintf("components/Row%d.jsx", i), Language: "javascript", Grammar: "jsx",
				Src: []byte(atScaleJSFile(i)),
			})
		default: // 10% go
			files = append(files, ParsedFile{
				Path: fmt.Sprintf("clients/willow_client_%d.go", i), Language: "go",
				Src: []byte(atScaleGoFile(i)),
			})
		}
	}
	return files
}

// atScaleNodesPerFile/atScaleEdgesPerFile are picked so the synthetic
// graph.Snapshot's total node/edge count lands in the same order of
// magnitude as orion's real graph, scaled from cedar's measured 50,800
// nodes / 132,904 edges over 5,217 files (docs/factpipe-cross-framework-
// matching-plan.md's Context) down to atScaleFileCount files: roughly 10
// nodes and 25 edges per file.
const (
	atScaleNodesPerFile = 10
	atScaleEdgesPerFile = 25
)

// atScaleSnapshot builds a synthetic graph.Snapshot sized per the constants
// above — factpipe.GraphFacts (FX.1's bridge) emits several base facts per
// node and per edge, and that one-time bridging cost is part of what XM.2
// targets, so this fixture needs a graph, not just files.
func atScaleSnapshot(files []ParsedFile) graph.Snapshot {
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}

	nodeCount := len(files) * atScaleNodesPerFile
	nodes := make([]graph.Node, nodeCount)
	for i := range nodes {
		f := files[i%len(files)]
		nodes[i] = graph.Node{
			ID:       fmt.Sprintf("n%d", i),
			Type:     graph.NodeTypeMethod,
			Label:    fmt.Sprintf("method_%d", i),
			Service:  "atscale",
			File:     f.Path,
			Line:     (i % 40) + 1,
			Language: f.Language,
		}
	}

	edgeCount := len(files) * atScaleEdgesPerFile
	edges := make([]graph.Edge, edgeCount)
	for i := range edges {
		from := nodes[i%nodeCount].ID
		to := nodes[(i+1)%nodeCount].ID
		edges[i] = graph.Edge{
			ID:   fmt.Sprintf("e%d", i),
			From: from,
			To:   to,
			Type: graph.EdgeTypeCalls,
		}
	}

	return graph.Snapshot{
		Nodes:       nodes,
		Edges:       edges,
		Files:       paths,
		ServicePath: ".",
	}
}

// BenchmarkFrameworkPipelineAtScale runs every embedded framework's Run
// against the orion-scale fixture. `go test ./internal/factpipe/pipeline/
// -bench BenchmarkFrameworkPipelineAtScale -benchmem -run '^$' -benchtime=3x`
// (explicit -benchtime: the default 1s target would otherwise re-run this
// many times, and each iteration is multiple seconds).
func BenchmarkFrameworkPipelineAtScale(b *testing.B) {
	reg, err := LoadEmbedded()
	if err != nil {
		b.Fatalf("LoadEmbedded: %v", err)
	}
	fws := reg.All()
	files := atScaleFixtureFiles()
	snap := atScaleSnapshot(files)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Run(fws, files, snap); err != nil {
			b.Fatalf("Run: %v", err)
		}
	}
}

// atScalePerfBudget is XM.0's own ceiling for one whole-registry Run over
// the orion-scale fixture. Measured cold (single run, no warmup — that is
// the real production shape: `factpipe_frameworks` runs each service's
// pipeline.Run exactly once, never repeatedly) on the same machine as this
// plan's Context numbers (Apple M4): ~2.9s standalone (three repeated
// -count=1 runs of just this test: 2.895s/2.904s/2.897s — bridge ~50ms,
// extract ~443ms, derive ~1.77s, emit negligible). This fixture's synthetic
// content produces zero final edges/nodes since its graph.Snapshot node IDs
// don't line up with what extraction mints — that's fine, XM.0 measures
// cost, not correctness; see cedar/orion vgdiff for the real correctness
// gate.
//
// Under `go test ./... -count=1` (every package's tests competing for CPU,
// which is this repo's real CI shape, not the scoped single-package run
// above), five separate full-repo runs measured 5.9s, 7.9s, 13.9s, 15.0s,
// and 18.0s — SA.7's "~1.3x measured baseline" convention undershoots
// badly here because the dominant variance source is contention from other
// packages' tests (parser/indexer/matrix run heavy CPU work concurrently),
// not this test's own cost, and that contention itself is highly variable
// run to run. Budget is set from the worst observed contended number with a
// real safety margin, not the clean standalone one (~2.9s) or a naive 1.3x
// of it. XM.1 and XM.2 should each lower this constant (with the new
// measured number, taken the same contended way — full `go test ./...`,
// several runs, not the scoped single-package invocation — in the commit
// message and in docs/factpipe-cross-framework-matching-plan.md's status
// table) once they land, not just pass under the old one.
const atScalePerfBudget = 30 * time.Second

// TestFrameworkPipelineAtScaleBudget is XM.0's CI gate — a single cold Run
// over the orion-scale fixture (not a median-of-N loop like
// TestFrameworkPipelinePerfBudget: at this scale, running it 5 times just to
// take a median would itself make `go test ./...` noticeably slower for
// every contributor, and the real production call site never repeats a
// service's Run either). It also logs the per-phase breakdown (RunStats)
// so a regression's phase is visible in test output without re-profiling.
func TestFrameworkPipelineAtScaleBudget(t *testing.T) {
	reg, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	fws := reg.All()
	files := atScaleFixtureFiles()
	snap := atScaleSnapshot(files)

	var st RunStats
	start := time.Now()
	res, err := Run(fws, files, snap, &st)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	t.Logf("XM.0: AtScale Run() wall time = %v (budget %v) — bridge=%v extract=%v derive=%v emit=%v",
		elapsed, atScalePerfBudget, st.Bridge, st.Extract, st.Derive, st.Emit)
	t.Logf("XM.0: AtScale Result — %d edges, %d nodes, %d unresolved, %d ledger rows",
		len(res.Edges), len(res.Nodes), len(res.Unresolved), len(res.Ledger))

	if elapsed > atScalePerfBudget {
		t.Fatalf("XM.0 at-scale budget miss (%v > %v) — see docs/factpipe-cross-framework-matching-plan.md XM.1/XM.2 before assuming this is a one-line fix", elapsed, atScalePerfBudget)
	}
}

// TestFrameworkPipelineAtScaleDeterministic holds the same determinism
// invariant docs/js-crossing-sources-plan.md and Tier FX's own verification
// discipline already require of the real indexer: two Run calls over
// byte-identical input must produce byte-identical (sorted) IDs, at this
// fixture's larger scale where a map-iteration-order bug is more likely to
// surface than in the small 4-file fixture.
func TestFrameworkPipelineAtScaleDeterministic(t *testing.T) {
	reg, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	fws := reg.All()
	files := atScaleFixtureFiles()
	snap := atScaleSnapshot(files)

	res1, err := Run(fws, files, snap)
	if err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	res2, err := Run(fws, files, snap)
	if err != nil {
		t.Fatalf("Run 2: %v", err)
	}

	ids := func(res Result) []string {
		out := make([]string, 0, len(res.Edges)+len(res.Nodes))
		for _, e := range res.Edges {
			out = append(out, "edge:"+e.ID)
		}
		for _, n := range res.Nodes {
			out = append(out, "node:"+n.ID)
		}
		sort.Strings(out)
		return out
	}
	a, b := ids(res1), ids(res2)
	if len(a) != len(b) {
		t.Fatalf("nondeterministic: run1 has %d ids, run2 has %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("nondeterministic at sorted index %d: %q vs %q", i, a[i], b[i])
		}
	}
}

// BenchmarkRelativizeToCwdAtScale isolates patterns.RelativizeToCwd's cost
// at this fixture's file count (per the Risks section of docs/factpipe-
// cross-framework-matching-plan.md: its ~10-12% profile share was not
// confirmed as real vs. a synthetic-harness artifact). Paths are made
// absolute (filepath.Join against the real cwd) before calling it —
// RelativizeToCwd's slow EvalSymlinks branch only triggers on an absolute
// input, which is what the real indexer's file-walk list actually hands it
// (docs/factpipe-cross-framework-matching-plan.md's Context); atScaleFixtureFiles'
// own relative paths would silently take the fast no-op branch and under-report
// this cost. Comparing this number against BenchmarkFrameworkPipelineAtScale's
// total ns/op is the intended read — if this benchmark's cost, times the
// number of relativize calls one Run makes, is a small fraction of the
// total, that confirms "noise"; if not, XM.1/XM.2's implementers should
// look here.
func BenchmarkRelativizeToCwdAtScale(b *testing.B) {
	cwd, err := os.Getwd()
	if err != nil {
		b.Fatalf("os.Getwd: %v", err)
	}
	files := atScaleFixtureFiles()
	abs := make([]string, len(files))
	for i, f := range files {
		abs[i] = filepath.Join(cwd, f.Path)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, p := range abs {
			_ = patterns.RelativizeToCwd(p)
		}
	}
}
