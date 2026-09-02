// Package fleetsync implements the resolver (docs/global-fleet-registry-plan.md,
// "The resolver"): given one fleet member's service definition, produce a
// ready-to-read local graph.db by checking the local machine registry, then
// an optional build cache, and only falling back to a shallow clone + index
// when both miss. The name is deliberately distinct from internal/registry
// and internal/fleetconfig — those are the nouns (data), this is the verb.
package fleetsync

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/fleetconfig"
	"github.com/lordsonvimal/polyflow/internal/indexer"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/registry"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// ResolveOptions carries the paths ResolveService needs beyond the fleet
// service definition itself.
type ResolveOptions struct {
	// RegistryPath is the local machine registry to consult and update.
	// Empty means registry.DefaultPath().
	RegistryPath string
	// CacheDir is the CI build-cache root, keyed by
	// <CacheDir>/<service>/<sha>/graph.db (GR.4 wires this in CI). Empty
	// means step 3 is always a miss — correct for local dev, which always
	// has step 2's fast path once a repo has been indexed once.
	CacheDir string
	// ScratchDir is the parent directory for shallow clones on a step-4
	// miss. Empty means a fresh os.MkdirTemp directory.
	ScratchDir string
}

// ResolveService implements the algorithm from "The resolver" above. Step 0:
// if svc.Git points at a git working tree on this machine, read that
// checkout in place (working tree and all). Otherwise the four remote steps:
// resolve the ref to a SHA, check the local registry for a clean checkout at
// that SHA, check the build cache, and only then clone+index. refOverride
// empty means "use the fleet definition's default ref."
func ResolveService(ctx context.Context, svc fleetconfig.Service, refOverride string, opts ResolveOptions) (dbPath string, resolvedSHA string, err error) {
	ref := svc.Ref
	if refOverride != "" {
		ref = refOverride
	}

	// Step 0: a local working tree. When svc.Git is a path to a git checkout
	// on this machine (rather than a real remote URL), the operator has opted
	// into local-dev semantics — read that checkout as it is on disk, so an
	// uncommitted polyflow.yml is honoured, with no ls-remote and no scratch
	// clone. The resolved SHA is the checkout's current HEAD.
	if wt, ok := localWorktree(ctx, svc.Git); ok {
		headSHA, headErr := gitHeadSHA(ctx, wt)
		if headErr != nil {
			return "", "", headErr
		}
		db := dbPathFor(wt, svc)
		if _, statErr := os.Stat(db); statErr != nil {
			if idxErr := indexLocalCheckout(ctx, wt, svc); idxErr != nil {
				return "", "", fmt.Errorf("index local worktree for %s at %s: %w", svc.Name, wt, idxErr)
			}
		}
		return db, headSHA, nil
	}

	sha, err := lsRemoteSHA(ctx, svc.Git, ref)
	if err != nil {
		return "", "", fmt.Errorf("resolve ref %s@%s: %w", svc.Git, ref, err)
	}

	regPath := opts.RegistryPath
	if regPath == "" {
		regPath, err = registry.DefaultPath()
		if err != nil {
			return "", "", err
		}
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		return "", "", err
	}

	// Step 2: local registry match. A wrong-SHA checkout is a plain miss —
	// never indexed as if it matched. A dirty (uncommitted changes) checkout
	// at the right SHA still matches; see isCleanCheckoutAt.
	if entry, ok := reg.Lookup(svc.Name); ok {
		if clean, cleanErr := isCleanCheckoutAt(ctx, entry.LocalPath, sha); cleanErr == nil && clean {
			db := dbPathFor(entry.LocalPath, svc)
			if _, statErr := os.Stat(db); statErr == nil {
				return db, sha, nil
			}
			// Registered at the right SHA but the graph.db isn't there. This
			// is the common "checkout exists, was never indexed" case — and,
			// for a Subpath (monorepo) member, the very common case of a
			// whole-workspace `polyflow index` having written only the
			// unified graph.db, never the per-service
			// .polyflow/services/<name>/graph.db shard a fleet sync reads.
			// Index the already-local, already-correct checkout in place
			// rather than falling through to step 4's scratch re-clone.
			if idxErr := indexLocalCheckout(ctx, entry.LocalPath, svc); idxErr != nil {
				return "", "", fmt.Errorf("index local checkout for %s at %s: %w", svc.Name, entry.LocalPath, idxErr)
			}
			return db, sha, nil
		}
	}

	// Step 3: build cache, keyed by (service, resolved SHA).
	if opts.CacheDir != "" {
		cached := filepath.Join(opts.CacheDir, svc.Name, sha, meta.DBFile)
		if _, statErr := os.Stat(cached); statErr == nil {
			return cached, sha, nil
		}
	}

	// Step 4: clone + index.
	dbPath, err = cloneAndIndex(ctx, svc, ref, regPath, opts)
	if err != nil {
		return "", "", err
	}

	if opts.CacheDir != "" {
		if cerr := copyToCache(dbPath, opts.CacheDir, svc.Name, sha); cerr != nil {
			return "", "", cerr
		}
	}

	return dbPath, sha, nil
}

// MemberStatus is one fleet member's read-only resolution snapshot (GR.5's
// `polyflow fleet status`): the same ref-resolve and lookup steps
// ResolveService performs before step 4, but stopping there — a status
// command must never clone, since "read-only, no side effects" is the whole
// point of a status view versus a sync.
type MemberStatus struct {
	Service string
	Ref     string
	SHA     string
	// Source is "local" (a clean checkout at SHA is already registered and
	// its graph.db is on disk), "local-unindexed" (a clean checkout at SHA
	// is registered but its graph.db — or, for a Subpath member, its
	// per-service shard — was never built; the next `fleet sync` will index
	// it in place, no clone), "cache" (opts.CacheDir has a copy keyed by
	// this SHA), or "unresolved" (none hit — the next `fleet sync` clones).
	Source string
	// LocalPath is set when Source == "local" or "local-unindexed".
	LocalPath string
}

// ResolveStatus mirrors ResolveService's step 0 (local working tree) and
// steps 1–3 (resolve ref to a SHA, check the local registry, check the build
// cache) but never falls through to step 4's clone — and, unlike
// ResolveService, never indexes in place, since a status view has no side
// effects.
func ResolveStatus(ctx context.Context, svc fleetconfig.Service, refOverride string, opts ResolveOptions) (*MemberStatus, error) {
	ref := svc.Ref
	if refOverride != "" {
		ref = refOverride
	}

	// Step 0: a local working tree — see ResolveService. Report its current
	// HEAD as the SHA and whether its graph.db exists yet.
	if wt, ok := localWorktree(ctx, svc.Git); ok {
		headSHA, headErr := gitHeadSHA(ctx, wt)
		if headErr != nil {
			return nil, headErr
		}
		st := &MemberStatus{Service: svc.Name, Ref: ref, SHA: headSHA, LocalPath: wt, Source: "local"}
		if _, statErr := os.Stat(dbPathFor(wt, svc)); statErr != nil {
			st.Source = "local-unindexed"
		}
		return st, nil
	}

	sha, err := lsRemoteSHA(ctx, svc.Git, ref)
	if err != nil {
		return nil, fmt.Errorf("resolve ref %s@%s: %w", svc.Git, ref, err)
	}
	st := &MemberStatus{Service: svc.Name, Ref: ref, SHA: sha}

	regPath := opts.RegistryPath
	if regPath == "" {
		regPath, err = registry.DefaultPath()
		if err != nil {
			return nil, err
		}
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		return nil, err
	}

	if entry, ok := reg.Lookup(svc.Name); ok {
		if clean, cleanErr := isCleanCheckoutAt(ctx, entry.LocalPath, sha); cleanErr == nil && clean {
			st.Source = "local"
			if _, statErr := os.Stat(dbPathFor(entry.LocalPath, svc)); statErr != nil {
				st.Source = "local-unindexed"
			}
			st.LocalPath = entry.LocalPath
			return st, nil
		}
	}

	if opts.CacheDir != "" {
		cached := filepath.Join(opts.CacheDir, svc.Name, sha, meta.DBFile)
		if _, statErr := os.Stat(cached); statErr == nil {
			st.Source = "cache"
			return st, nil
		}
	}

	st.Source = "unresolved"
	return st, nil
}

// lsRemoteSHA resolves ref on gitURL to a commit SHA without cloning. A ref
// that is already a 40-char hex SHA (a ref override pinning an exact
// commit) is accepted as-is when it doesn't match a branch head.
func lsRemoteSHA(ctx context.Context, gitURL, ref string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "ls-remote", "--heads", gitURL, ref).Output()
	if err != nil {
		return "", fmt.Errorf("git ls-remote %s %s: %w", gitURL, ref, err)
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		if isSHA(ref) {
			return ref, nil
		}
		return "", fmt.Errorf("ref %q not found on %s", ref, gitURL)
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", fmt.Errorf("unexpected git ls-remote output %q", line)
	}
	return fields[0], nil
}

// localWorktree reports whether gitURL identifies a git working tree on this
// machine (rather than a remote to clone). A plain filesystem path or a
// "file://" URL both qualify; anything with another URL scheme
// ("https://…", "ssh://…") or scp-style "host:path" does not, and neither
// does a bare repository (no working tree, so no polyflow.yml to read in
// place). Returns the absolute path to the work-tree root.
func localWorktree(ctx context.Context, gitURL string) (string, bool) {
	path := strings.TrimPrefix(gitURL, "file://")
	if path == gitURL {
		// No file:// prefix: reject any other scheme and scp-style syntax.
		if strings.Contains(path, "://") {
			return "", false
		}
		if i := strings.IndexByte(path, ':'); i >= 0 && !filepath.IsAbs(path) {
			return "", false
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	if info, statErr := os.Stat(abs); statErr != nil || !info.IsDir() {
		return "", false
	}
	out, err := exec.CommandContext(ctx, "git", "-C", abs, "rev-parse", "--is-inside-work-tree").Output()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		return "", false
	}
	return abs, true
}

// gitHeadSHA returns the commit SHA that dir's HEAD points at.
func gitHeadSHA(ctx context.Context, dir string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git -C %s rev-parse HEAD: %w", dir, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func isSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// isCleanCheckoutAt reports whether dir is a git checkout whose HEAD is
// exactly sha with no uncommitted changes. Either condition failing is a
// miss, not a partial match — no attempt to diff or reconcile a dirty tree.
// isCleanCheckoutAt reports whether dir is a checkout of sha — "clean"
// refers only to being on the right commit, not to an empty `git status`.
// Uncommitted local changes are deliberately allowed to count as a match:
// requiring a spotless working tree forced every sync on an in-progress
// repo to reclone+reindex from HEAD into a scratch dir instead of reusing
// the already-current local .polyflow/graph.db, which is normally the more
// useful state to bridge against (HEAD may lag local work-in-progress, and
// most local checkouts are never fully clean during active development).
func isCleanCheckoutAt(ctx context.Context, dir, sha string) (bool, error) {
	info, statErr := os.Stat(dir)
	if statErr != nil || !info.IsDir() {
		return false, fmt.Errorf("local path %q: not a directory", dir)
	}
	headOut, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return false, fmt.Errorf("git -C %s rev-parse HEAD: %w", dir, err)
	}
	return strings.TrimSpace(string(headOut)) == sha, nil
}

// dbPathFor locates a fleet member's own graph.db under its workspace
// checkout at localPath. A Subpath-less service (the repo IS the service,
// e.g. "willow") is indexed standalone into its workspace root's
// .polyflow/graph.db (FR.2's default DBDir). A Subpath service lives inside
// a multi-service monorepo workspace and is indexed per-service into
// .polyflow/services/<name>/graph.db (the same layout `polyflow index
// <service>` already writes today).
func dbPathFor(localPath string, svc fleetconfig.Service) string {
	if svc.Subpath == "" {
		return filepath.Join(localPath, meta.DBDir, meta.DBFile)
	}
	return filepath.Join(localPath, meta.DBDir, "services", svc.Name, meta.DBFile)
}

// cloneAndIndex shallow-clones svc at ref into a scratch directory, runs the
// existing FR.2 indexing pipeline against it, and returns the resulting
// graph.db path. It syncs the clone into the local registry only when
// opts.ScratchDir was explicitly given (a caller-controlled, presumably
// persistent location, e.g. CI's own workspace) — an auto-generated
// os.MkdirTemp scratch dir is ephemeral (may be gone by the next process
// run, or even by the end of this one on some OSes' temp-cleanup policies),
// so registering it would silently clobber a real, durable registry entry
// for this service with a dangling path. A caller that wants the clone
// registered can always pass its own ScratchDir.
func cloneAndIndex(ctx context.Context, svc fleetconfig.Service, ref, regPath string, opts ResolveOptions) (string, error) {
	scratchParent := opts.ScratchDir
	persistentScratch := scratchParent != ""
	if scratchParent == "" {
		var err error
		scratchParent, err = os.MkdirTemp("", "polyflow-fleetsync-*")
		if err != nil {
			return "", fmt.Errorf("create scratch dir: %w", err)
		}
	} else if err := os.MkdirAll(scratchParent, 0o755); err != nil {
		return "", fmt.Errorf("create scratch dir: %w", err)
	}

	cloneDir := filepath.Join(scratchParent, svc.Name)
	if err := os.RemoveAll(cloneDir); err != nil {
		return "", fmt.Errorf("clear scratch clone dir %s: %w", cloneDir, err)
	}
	cloneOut, err := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--branch", ref, svc.Git, cloneDir).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git clone %s@%s: %w: %s", svc.Git, ref, err, strings.TrimSpace(string(cloneOut)))
	}

	if err := indexLocalCheckout(ctx, cloneDir, svc); err != nil {
		return "", err
	}

	if persistentScratch {
		if err := registry.Sync(regPath, svc.Name, cloneDir); err != nil {
			return "", fmt.Errorf("sync registry: %w", err)
		}
	}

	return dbPathFor(cloneDir, svc), nil
}

// indexLocalCheckout runs the FR.2 indexing pipeline against an on-disk
// workspace checkout at wsRoot, writing the graph.db a fleet sync will read
// for svc: the unified .polyflow/graph.db for a Subpath-less member, or the
// per-service .polyflow/services/<name>/graph.db shard for a Subpath member
// (the same layout `polyflow index <service>` writes). Shared by
// cloneAndIndex (fresh clone) and ResolveService's step 2 (an
// already-local, already-correct checkout that was never indexed, or whose
// per-service shard a whole-workspace index never produced).
func indexLocalCheckout(ctx context.Context, wsRoot string, svc fleetconfig.Service) error {
	wsConfigPath := filepath.Join(wsRoot, meta.ConfigFile)
	cfg, err := workspace.Load(wsConfigPath)
	if err != nil {
		return fmt.Errorf("load workspace config %s: %w", wsConfigPath, err)
	}

	dbDir := filepath.Join(wsRoot, meta.DBDir)
	var serviceFilter []string
	if svc.Subpath != "" {
		serviceFilter = []string{svc.Name}
		dbDir = filepath.Join(wsRoot, meta.DBDir, "services", svc.Name)
	}

	if _, err := indexer.Run(ctx, indexer.Options{
		Config:        cfg,
		ServiceFilter: serviceFilter,
		DBDir:         dbDir,
		NoEmbed:       true,
		ContractsDir:  wsRoot,
	}); err != nil {
		return fmt.Errorf("index %s: %w", svc.Name, err)
	}
	return nil
}

// copyToCache populates the build cache with a freshly built graph.db so a
// later resolution (e.g. a sibling CI job) hits step 3 instead of cloning.
func copyToCache(dbPath, cacheDir, service, sha string) error {
	dest := filepath.Join(cacheDir, service, sha, meta.DBFile)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	data, err := os.ReadFile(dbPath)
	if err != nil {
		return fmt.Errorf("read db for cache: %w", err)
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return fmt.Errorf("write cache db: %w", err)
	}
	return nil
}
