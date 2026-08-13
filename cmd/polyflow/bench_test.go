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
  workspace: polyflow.yml
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

// The protocol has documented a 10-per-repo cap since P.1 but nothing enforced
// it, so the default run was the whole 186-task corpus — 372 paid invocations,
// past any session budget and therefore certain to abort partway through.
func TestCapPerRepo(t *testing.T) {
	tasks := []benchTask{
		{TaskID: "a/1", Repo: "a"}, {TaskID: "a/2", Repo: "a"}, {TaskID: "a/3", Repo: "a"},
		{TaskID: "b/1", Repo: "b"}, {TaskID: "b/2", Repo: "b"},
	}
	got := capPerRepo(tasks, 2)
	want := []string{"a/1", "a/2", "b/1", "b/2"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].TaskID != w {
			t.Errorf("task[%d] = %q, want %q (the cap must preserve corpus order)", i, got[i].TaskID, w)
		}
	}

	if n := len(capPerRepo(tasks, 0)); n != len(tasks) {
		t.Errorf("cap 0 kept %d tasks, want all %d", n, len(tasks))
	}
	if n := len(capPerRepo(tasks, 99)); n != len(tasks) {
		t.Errorf("cap above the count kept %d tasks, want all %d", n, len(tasks))
	}
	if tasks[3].TaskID != "b/1" {
		t.Error("capPerRepo must not scribble on its input slice")
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
