package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/eval"
)

// e1CorpusDir is the E.1 task set. It lives outside eval/corpus on purpose:
// the eval runner has no case for flow/feature_add/test_impact/regression, so
// these manifests would hard-fail `polyflow eval --gate` if they were there.
const e1CorpusDir = "../../eval/agent-bench/live-e1"

// TestE1TaskSet_MeetsCategoryQuotas keeps E.1's five quotas honest. A README
// table saying "6 Rails cases" is a claim; this is the check. Categories are
// identified by id prefix, which is why the ids are named the way they are.
func TestE1TaskSet_MeetsCategoryQuotas(t *testing.T) {
	tasks, err := collectBenchTasks(e1CorpusDir)
	if err != nil {
		t.Fatalf("collect E.1 tasks: %v", err)
	}
	if len(tasks) < 20 {
		t.Errorf("E.1 requires >= 20 tasks spanning the fleet, got %d", len(tasks))
	}

	// Category → the prefixes that mark membership, and the required count.
	quotas := []struct {
		name     string
		min      int
		prefixes []string
	}{
		{"1 Rails route->controller->model", 6, []string{"rails-", "cam-"}},
		{"2 frontend click -> backend", 4, []string{"flow-"}},
		{"3 cross-service HTTP and AMQP", 4, []string{"xsvc-"}},
		{"5 regression safety", 3, []string{"regress-"}},
	}
	for _, q := range quotas {
		n := 0
		for _, task := range tasks {
			for _, p := range q.prefixes {
				if len(task.CaseID) >= len(p) && task.CaseID[:len(p)] == p {
					n++
					break
				}
			}
		}
		if n < q.min {
			t.Errorf("category %s: %d cases, want >= %d", q.name, n, q.min)
		}
	}

	// Category 4 is defined by the truth set being complete, not by a name.
	exhaustive := 0
	dirs, err := eval.FindCorpusDirs(e1CorpusDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range dirs {
		m, err := eval.LoadManifest(dir)
		if err != nil {
			t.Fatalf("load %s: %v", dir, err)
		}
		for _, c := range m.Cases {
			if c.Exhaustive {
				exhaustive++
			}
		}
	}
	if exhaustive < 3 {
		t.Errorf("category 4 blast-radius: %d exhaustive cases, want >= 3", exhaustive)
	}
}

// TestE1TaskSet_ManifestsAreValid runs the schema validator over every E.1
// manifest. ValidateManifest had no production caller before D.1; a bench
// corpus that only fails at spend time is not worth having.
func TestE1TaskSet_ManifestsAreValid(t *testing.T) {
	dirs, err := eval.FindCorpusDirs(e1CorpusDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Fatal("no E.1 corpus dirs found")
	}
	for _, dir := range dirs {
		m, err := eval.LoadManifest(dir)
		if err != nil {
			t.Fatalf("load %s: %v", dir, err)
		}
		for _, verr := range eval.ValidateManifest(m) {
			t.Errorf("%s: %v", filepath.Base(dir), verr)
		}
	}
}

// e1RepoRoots maps each E.1 manifest to the checkout its paths are relative
// to. Bench tasks run from inside the repo, so the truth sets are
// repo-relative rather than workspace-absolute.
var e1RepoRoots = map[string]string{
	"nextgen":     "/Users/lordson/Projects/nextGen",
	"nextgen-cam": "/Users/lordson/Projects/nextGen-CAM",
	"chessleap":   "/Users/lordson/projects/chessleap",
	"synergy":     "/Users/lordson/Projects/synergy",
	"mysycamore":  "/Users/lordson/Projects/mysycamore",
	"datascience": "/Users/lordson/Projects/datascience",
}

// TestE1TaskSet_TruthSetPathsExist catches the quietest way to waste a paid
// benchmark run: a mistyped path. A must_not_miss entry that matches no file
// can never be found, so the case hard-fails forever; a must_not_include entry
// that matches no file can never be hit, so the precision assertion silently
// does nothing. Skipped per repo when the checkout is not on this machine.
func TestE1TaskSet_TruthSetPathsExist(t *testing.T) {
	dirs, err := eval.FindCorpusDirs(e1CorpusDir)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, dir := range dirs {
		m, err := eval.LoadManifest(dir)
		if err != nil {
			t.Fatalf("load %s: %v", dir, err)
		}
		root, ok := e1RepoRoots[m.Repo.Name]
		if !ok {
			t.Errorf("%s: no repo root recorded in e1RepoRoots", m.Repo.Name)
			continue
		}
		if _, err := os.Stat(root); err != nil {
			t.Logf("skipping %s: %s not present on this machine", m.Repo.Name, root)
			continue
		}
		for _, c := range m.Cases {
			for _, group := range [][]string{c.ExpectedImpacted, c.MustNotMiss, c.MustNotInclude} {
				for _, p := range group {
					checked++
					if _, err := os.Stat(filepath.Join(root, p)); err != nil {
						t.Errorf("%s/%s: %s does not exist under %s", m.Repo.Name, c.ID, p, root)
					}
				}
			}
		}
	}
	t.Logf("%d truth-set paths checked", checked)
}

// TestE1TaskSet_EveryCaseHasADecisiveAssertion guards the failure mode a
// recall corpus cannot see: a case whose must_not_miss is empty can be passed
// by an agent that names nothing in particular, and a regression case with
// neither must_not_miss nor must_not_include asserts nothing at all.
func TestE1TaskSet_EveryCaseHasADecisiveAssertion(t *testing.T) {
	dirs, err := eval.FindCorpusDirs(e1CorpusDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range dirs {
		m, err := eval.LoadManifest(dir)
		if err != nil {
			t.Fatalf("load %s: %v", dir, err)
		}
		for _, c := range m.Cases {
			if len(c.MustNotMiss) == 0 && len(c.MustNotInclude) == 0 {
				t.Errorf("%s/%s: neither must_not_miss nor must_not_include — the case cannot fail",
					m.Repo.Name, c.ID)
			}
			if c.Kind == "regression" && c.RegressionSubject == "" {
				t.Errorf("%s/%s: regression case has no regression_subject", m.Repo.Name, c.ID)
			}
		}
	}
}
