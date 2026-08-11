package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/agentbench"
)

func testHeader() checkpointHeader {
	return checkpointHeader{
		Model:  "claude-sonnet-4-6",
		Corpus: "eval/agent-bench/live-e1",
		Repo:   "nextgen",
		Arms:   []string{agentbench.ArmWithSemantics, agentbench.ArmNoPolyflow},
		Trials: 1,
	}
}

func measuredResult(taskID, arm string) agentbench.TaskResult {
	return agentbench.TaskResult{
		TaskID: taskID, Repo: "nextgen", CaseID: "c", Arm: arm, Trial: 1,
		ContextTokens: 1234, Recall: 1.0,
	}
}

// TestCheckpoint_ResumeSkipsBoughtTrials is the whole point of the checkpoint:
// a trial already paid for must not be paid for twice.
func TestCheckpoint_ResumeSkipsBoughtTrials(t *testing.T) {
	dir := t.TempDir()
	c, err := loadCheckpoint(dir, "nextgen", testHeader(), false)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.count() != 0 {
		t.Fatalf("fresh checkpoint has %d records, want 0", c.count())
	}
	c.record(measuredResult("nextgen/a", agentbench.ArmWithSemantics))
	c.record(measuredResult("nextgen/a", agentbench.ArmNoPolyflow))

	c2, err := loadCheckpoint(dir, "nextgen", testHeader(), false)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if c2.count() != 2 {
		t.Fatalf("resumed checkpoint has %d records, want 2", c2.count())
	}
	got, ok := c2.completed("nextgen/a", agentbench.ArmWithSemantics, 1)
	if !ok {
		t.Fatal("bought trial not reported as completed after resume")
	}
	if got.ContextTokens != 1234 || got.Recall != 1.0 {
		t.Errorf("resumed result lost its metrics: %+v", got)
	}
	if _, ok := c2.completed("nextgen/b", agentbench.ArmWithSemantics, 1); ok {
		t.Error("an unbought trial was reported as completed")
	}
}

// TestCheckpoint_FailedTrialsAreReplayed guards the inverse mistake. A recorded
// failure is not a purchase to keep — it is what the operator is re-running to
// get past — so resuming must retry it rather than bake the failure in.
func TestCheckpoint_FailedTrialsAreReplayed(t *testing.T) {
	dir := t.TempDir()
	c, err := loadCheckpoint(dir, "nextgen", testHeader(), false)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	c.record(agentbench.TaskResult{
		TaskID: "nextgen/a", Arm: agentbench.ArmWithSemantics, Trial: 1,
		Error: "session limit reached", ErrorClass: agentbench.FailureQuota,
	})
	if c.count() != 0 {
		t.Errorf("failed trial counted as bought (%d records)", c.count())
	}

	c2, err := loadCheckpoint(dir, "nextgen", testHeader(), false)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := c2.completed("nextgen/a", agentbench.ArmWithSemantics, 1); ok {
		t.Error("resume treated a quota failure as a completed trial")
	}
}

// TestCheckpoint_RefusesMismatchedRun stops the quiet corruption: resuming a
// sonnet run into an opus report would produce a single number describing two
// different measurements.
func TestCheckpoint_RefusesMismatchedRun(t *testing.T) {
	dir := t.TempDir()
	c, err := loadCheckpoint(dir, "nextgen", testHeader(), false)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	c.record(measuredResult("nextgen/a", agentbench.ArmWithSemantics))

	other := testHeader()
	other.Model = "claude-opus-5"
	if _, err := loadCheckpoint(dir, "nextgen", other, false); err == nil {
		t.Fatal("resumed a checkpoint written for a different model")
	} else if !strings.Contains(err.Error(), "--fresh") {
		t.Errorf("error does not say how to recover: %v", err)
	}

	// --fresh is the documented way out, and must actually clear the file.
	c3, err := loadCheckpoint(dir, "nextgen", other, true)
	if err != nil {
		t.Fatalf("--fresh load: %v", err)
	}
	if c3.count() != 0 {
		t.Errorf("--fresh kept %d records", c3.count())
	}
}

// TestCheckpoint_SurvivesATornLastLine models the normal shape of a kill mid
// write. Everything bought before the tear must still be recoverable.
func TestCheckpoint_SurvivesATornLastLine(t *testing.T) {
	dir := t.TempDir()
	c, err := loadCheckpoint(dir, "nextgen", testHeader(), false)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	c.record(measuredResult("nextgen/a", agentbench.ArmWithSemantics))

	f, err := os.OpenFile(c.path, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString(`{"task_id":"nextgen/b","ar`); err != nil {
		t.Fatalf("write torn line: %v", err)
	}
	f.Close()

	c2, err := loadCheckpoint(dir, "nextgen", testHeader(), false)
	if err != nil {
		t.Fatalf("reload after tear: %v", err)
	}
	if c2.count() != 1 {
		t.Errorf("a torn last line cost %d intact records", 1-c2.count())
	}
}

// TestCheckpoint_PerRepoIsolation matters because E.2 runs one repo at a time
// from inside that repo, all writing to the same --output directory.
func TestCheckpoint_PerRepoIsolation(t *testing.T) {
	dir := t.TempDir()
	if a, b := checkpointPath(dir, "nextgen"), checkpointPath(dir, "chessleap"); a == b {
		t.Fatalf("two repos share one checkpoint: %s", a)
	}
	if p := checkpointPath(dir, ""); p != filepath.Join(dir, ".checkpoint-all.jsonl") {
		t.Errorf("unfiltered checkpoint path = %s", p)
	}
}

// TestCheckpoint_ClearedOnCompletion keeps a finished run from replaying itself
// as cached forever.
func TestCheckpoint_ClearedOnCompletion(t *testing.T) {
	dir := t.TempDir()
	c, err := loadCheckpoint(dir, "nextgen", testHeader(), false)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	c.record(measuredResult("nextgen/a", agentbench.ArmWithSemantics))
	c.clear()
	if _, err := os.Stat(c.path); !os.IsNotExist(err) {
		t.Errorf("checkpoint survived clear(): %v", err)
	}
	c.clear() // idempotent — a run that never wrote one must not error
}
