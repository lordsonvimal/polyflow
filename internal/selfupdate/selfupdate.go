// Package selfupdate implements `polyflow update` / `--check`: pull
// polyflow's own source, rebuild it, and (in the update path) refresh every
// workspace this machine knows about — the registry's indexed repos and the
// fleets built from them — so a code change to polyflow's parsers/linkers
// doesn't sit unused until someone remembers to reindex by hand.
package selfupdate

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/registry"
)

// modulePath identifies polyflow's own go.mod, so FindRepo doesn't mistake
// some unrelated ancestor directory's go.mod for the polyflow checkout.
const modulePath = "module github.com/lordsonvimal/polyflow"

// repoPathEnv lets a non-standard checkout location be pointed at explicitly,
// same reasoning as registry's POLYFLOW_HOME override.
const repoPathEnv = "POLYFLOW_REPO"

// FindRepo resolves the local polyflow source checkout, in order: explicit
// (the --repo-path flag), $POLYFLOW_REPO, the machine registry's own entry
// for polyflow (GR.1 self-registers it the same as any other workspace,
// under polyflow.yml's `name: polyflow`, whenever `polyflow index` runs
// standalone from this repo), and finally walking up from the current
// directory for polyflow's own go.mod. The registry lookup is what lets
// `polyflow update`/`--check` work from any directory once this
// repo has been indexed once — no env var or flag required. It never falls
// back to a guessed path — an update that silently rebuilds the wrong repo
// is worse than one that asks.
func FindRepo(explicit string) (string, error) {
	if explicit != "" {
		if err := verifyModule(explicit); err != nil {
			return "", err
		}
		return filepath.Abs(explicit)
	}
	if env := os.Getenv(repoPathEnv); env != "" {
		if err := verifyModule(env); err != nil {
			return "", err
		}
		return filepath.Abs(env)
	}
	if dir, ok := findFromRegistry(); ok {
		return dir, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	for {
		if verifyModule(dir) == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find polyflow's own checkout — pass --repo-path, set $%s, or run `polyflow index` from it once to register it", repoPathEnv)
		}
		dir = parent
	}
}

// findFromRegistry looks up polyflow's own entry in registry.yml. A stale or
// wrong entry (moved checkout, module mismatch) is treated as absent rather
// than an error — the caller's other discovery methods, or a clear final
// error, are safer than trusting a registry entry that no longer checks out.
func findFromRegistry() (string, bool) {
	regPath, err := registry.DefaultPath()
	if err != nil {
		return "", false
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		return "", false
	}
	e, ok := reg.Lookup(meta.Name)
	if !ok || e.LocalPath == "" {
		return "", false
	}
	if err := verifyModule(e.LocalPath); err != nil {
		return "", false
	}
	return e.LocalPath, true
}

func verifyModule(dir string) error {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return err
	}
	firstLine, _, _ := strings.Cut(string(data), "\n")
	if strings.TrimSpace(firstLine) != modulePath {
		return fmt.Errorf("%s is not the polyflow repo (go.mod module mismatch)", dir)
	}
	return nil
}

// IsDirty reports whether repoDir has uncommitted changes — an update must
// never `git pull` over a user's in-progress work.
func IsDirty(repoDir string) (bool, error) {
	if err := restoreGitkeep(repoDir); err != nil {
		return false, err
	}
	out, err := exec.Command("git", "-C", repoDir, "status", "--porcelain").Output()
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	return len(bytes.TrimSpace(out)) > 0, nil
}

// restoreGitkeep recreates web/dist/.gitkeep when it's missing. It's the
// only tracked path under web/dist/ (everything else is .gitignore'd), kept
// solely so `//go:embed all:dist` (web/embed.go) compiles on a fresh
// checkout — every `make`/`make install` run (including a prior --update's
// own Build, or a manual build a user ran outside this tool) empties
// web/dist via vite and re-touches the placeholder, but that recreation can
// be lost (e.g. a build that failed partway through, or one that ran under
// sudo and left web/dist state a later non-root run can't clean up). Left
// alone, a missing placeholder shows up as a real "uncommitted change" and
// permanently blocks --update until a human runs `git checkout` by hand —
// exactly the manual intervention this command exists to avoid. Recreating
// it is always safe: its entire tracked content is an empty file.
func restoreGitkeep(repoDir string) error {
	gitkeep := filepath.Join(repoDir, "web", "dist", ".gitkeep")
	if _, err := os.Stat(gitkeep); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(gitkeep), 0o755); err != nil {
		return fmt.Errorf("recreate %s: %w", filepath.Dir(gitkeep), err)
	}
	if err := os.WriteFile(gitkeep, nil, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", gitkeep, err)
	}
	return nil
}

// Pull fast-forwards repoDir's checkout to its upstream, streaming output to
// out. It uses `git merge --ff-only` rather than a plain `git pull` so it
// never depends on the user's `pull.rebase` / `pull.ff` config — an unset
// config plus a diverged branch (e.g. after someone force-pushed the remote)
// makes bare `git pull` abort with "Need to specify how to reconcile
// divergent branches", which is exactly the case ResetToUpstream handles.
// The upstream ref is assumed already fetched (CheckOutdated does that).
func Pull(ctx context.Context, repoDir string, out io.Writer) error {
	return runStreamed(ctx, repoDir, out, "git", "-C", repoDir, "merge", "--ff-only", "@{u}")
}

// ResetToUpstream hard-resets repoDir's checkout to its upstream, discarding
// any local-only commits. It's the recovery path when the branch has
// diverged from the remote (a force-push) and so can't fast-forward.
// `polyflow update` only calls this behind an explicit --force, and only
// after IsDirty has confirmed the working tree is clean, so nothing
// uncommitted is at stake — but committed local-only work would be lost,
// which is why LocalOnlyCommits is shown to the user first.
func ResetToUpstream(ctx context.Context, repoDir string, out io.Writer) error {
	return runStreamed(ctx, repoDir, out, "git", "-C", repoDir, "reset", "--hard", "@{u}")
}

// LocalOnlyCommits returns the one-line log of commits present in repoDir's
// checkout but not on its upstream — i.e. what a ResetToUpstream would throw
// away. Empty string means none.
func LocalOnlyCommits(ctx context.Context, repoDir string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", repoDir, "log", "--oneline", "@{u}..HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git log @{u}..HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Build runs `make install` in repoDir, streaming output to out — the same
// path a human runs by hand (Makefile:26), so it rebuilds polyflow and its
// polyflow-parse-templ sidecar and installs both onto PATH.
func Build(ctx context.Context, repoDir string, out io.Writer) error {
	if err := cleanWebDist(repoDir); err != nil {
		return err
	}
	return runStreamed(ctx, repoDir, out, "make", "-C", repoDir, "install")
}

// cleanWebDist removes web/dist before the build. Vite's own emptyDir step
// (run as part of `make install`'s `web` target) has been observed to throw
// ENOTEMPTY on web/dist/assets when leftovers survive from a prior
// interrupted/aborted build — rmdir and recreate ourselves first so a stale
// dist/ never turns an unattended --update into a failure requiring a manual
// rebuild. Recreated with .gitkeep (Makefile:9-11's placeholder) so
// `//go:embed all:dist` (web/embed.go) still compiles even if the build
// below fails partway through.
func cleanWebDist(repoDir string) error {
	distDir := filepath.Join(repoDir, "web", "dist")
	if err := os.RemoveAll(distDir); err != nil {
		return fmt.Errorf("clean %s: %w", distDir, err)
	}
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		return fmt.Errorf("recreate %s: %w", distDir, err)
	}
	if err := os.WriteFile(filepath.Join(distDir, ".gitkeep"), nil, 0o644); err != nil {
		return fmt.Errorf("write %s/.gitkeep: %w", distDir, err)
	}
	return nil
}

func runStreamed(ctx context.Context, dir string, out io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", strings.Join(append([]string{name}, args...), " "), err)
	}
	return nil
}

// Status is the result of CheckOutdated.
type Status struct {
	LocalSHA  string
	RemoteSHA string
	Behind    int
	Ahead     int
}

// Outdated reports whether the remote has commits the local checkout lacks.
func (s *Status) Outdated() bool { return s.Behind > 0 }

// Diverged reports whether the local checkout and its upstream have each
// moved on independently — the classic post-force-push state, where a
// fast-forward is impossible and only a hard reset (or manual rebase) can
// reconcile them.
func (s *Status) Diverged() bool { return s.Behind > 0 && s.Ahead > 0 }

// CheckOutdated fetches repoDir's upstream and compares HEAD against it,
// without pulling or rebuilding — so `polyflow update --check` is safe to run
// at any time, including with a dirty tree.
func CheckOutdated(ctx context.Context, repoDir string) (*Status, error) {
	if err := runStreamed(ctx, repoDir, io.Discard, "git", "-C", repoDir, "fetch", "--quiet"); err != nil {
		return nil, fmt.Errorf("git fetch: %w", err)
	}
	localSHA, err := revParse(ctx, repoDir, "HEAD")
	if err != nil {
		return nil, err
	}
	remoteSHA, err := revParse(ctx, repoDir, "@{u}")
	if err != nil {
		return nil, fmt.Errorf("resolve upstream (is a remote tracking branch configured?): %w", err)
	}
	// One `rev-list --count --left-right A...B` gives both sides of the
	// divergence at once: "<ahead>\t<behind>".
	countOut, err := exec.CommandContext(ctx, "git", "-C", repoDir, "rev-list", "--count", "--left-right", "HEAD...@{u}").Output()
	if err != nil {
		return nil, fmt.Errorf("git rev-list: %w", err)
	}
	fields := strings.Fields(string(countOut))
	if len(fields) != 2 {
		return nil, fmt.Errorf("parse rev-list count: unexpected output %q", strings.TrimSpace(string(countOut)))
	}
	ahead, err := strconv.Atoi(fields[0])
	if err != nil {
		return nil, fmt.Errorf("parse ahead count: %w", err)
	}
	behind, err := strconv.Atoi(fields[1])
	if err != nil {
		return nil, fmt.Errorf("parse behind count: %w", err)
	}
	return &Status{LocalSHA: shortSHA(localSHA), RemoteSHA: shortSHA(remoteSHA), Behind: behind, Ahead: ahead}, nil
}

func revParse(ctx context.Context, dir, ref string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", ref).Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse %s: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
