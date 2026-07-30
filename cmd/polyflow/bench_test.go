package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCollectBenchTasks_FlowKind is a regression guard for the exact bug this
// phase closes: a manifest with only a kind=flow case must yield 1 task, not
// be silently skipped like an unrecognized kind would have been.
func TestCollectBenchTasks_FlowKind(t *testing.T) {
	dir := t.TempDir()
	manifest := `
repo:
  name: flowrepo
  path: .
  sha: live
  workspace: workspace.yaml
cases:
  - id: order-flow
    kind: flow
    target: "POST /orders"
    expected_impacted:
      - app/controllers/orders_controller.rb
      - app/jobs/order_worker.rb
    must_not_miss:
      - app/jobs/order_worker.rb
`
	if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	tasks, err := collectBenchTasks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("len(tasks) = %d, want 1 (flow case must not be skipped)", len(tasks))
	}
	tk := tasks[0]
	if tk.Kind != "flow" {
		t.Errorf("Kind = %q, want flow", tk.Kind)
	}
	wantPrompt := "Walk me through what happens when POST /orders in the flowrepo codebase — which files are " +
		"involved, and in what order? List each file involved on its own line, in the order they participate."
	if tk.Prompt != wantPrompt {
		t.Errorf("Prompt = %q, want %q", tk.Prompt, wantPrompt)
	}
}

func TestHasFlowTask(t *testing.T) {
	if hasFlowTask([]benchTask{{Kind: "node"}, {Kind: "file"}}) {
		t.Error("expected false when no flow task present")
	}
	if !hasFlowTask([]benchTask{{Kind: "node"}, {Kind: "flow"}}) {
		t.Error("expected true when a flow task is present")
	}
}
