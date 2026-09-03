package selfupdate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// git runs a git command in dir, failing the test on error.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func commit(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-m", "c-"+content)
}

// TestCheckOutdated_DivergedBranch is the force-push case: after the remote
// is rewritten under the local checkout's feet, CheckOutdated must report
// the branch as both ahead and behind (Diverged), not just "outdated" —
// that's what lets `polyflow update` refuse a doomed fast-forward instead of
// letting `git pull` abort with "Need to specify how to reconcile divergent
// branches".
func TestCheckOutdated_DivergedBranch(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	git(t, remote, "init", "-q", "-b", "main")
	commit(t, remote, "a.txt", "base")

	local := t.TempDir()
	git(t, t.TempDir(), "clone", "-q", remote, local)
	git(t, local, "config", "user.email", "t@t")
	git(t, local, "config", "user.name", "t")

	// Local adds its own commit.
	commit(t, local, "local.txt", "localwork")

	// Remote history moves on independently (force-push equivalent).
	commit(t, remote, "b.txt", "remotework")

	status, err := CheckOutdated(ctx, local)
	if err != nil {
		t.Fatalf("CheckOutdated: %v", err)
	}
	if !status.Diverged() {
		t.Fatalf("expected Diverged, got ahead=%d behind=%d", status.Ahead, status.Behind)
	}

	local1, err := LocalOnlyCommits(ctx, local)
	if err != nil || local1 == "" {
		t.Fatalf("LocalOnlyCommits = %q, %v; want non-empty", local1, err)
	}

	// A fast-forward must fail; a reset to upstream must recover.
	if err := Pull(ctx, local, nil); err == nil {
		t.Fatal("Pull (ff-only) should fail on a diverged branch")
	}
	if err := ResetToUpstream(ctx, local, nil); err != nil {
		t.Fatalf("ResetToUpstream: %v", err)
	}
	after, err := CheckOutdated(ctx, local)
	if err != nil {
		t.Fatalf("CheckOutdated after reset: %v", err)
	}
	if after.Diverged() || after.Outdated() {
		t.Fatalf("after reset: ahead=%d behind=%d, want clean", after.Ahead, after.Behind)
	}
}
