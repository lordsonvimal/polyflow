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

// ResolveService implements the four-step algorithm from "The resolver"
// above: resolve the ref to a SHA, check the local registry for a clean
// checkout at that SHA, check the build cache, and only then clone+index.
// refOverride empty means "use the fleet definition's default ref."
func ResolveService(ctx context.Context, svc fleetconfig.Service, refOverride string, opts ResolveOptions) (dbPath string, resolvedSHA string, err error) {
	ref := svc.Ref
	if refOverride != "" {
		ref = refOverride
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

	// Step 2: local registry match. A dirty or wrong-SHA checkout is a
	// plain miss — never indexed as if it matched.
	if entry, ok := reg.Lookup(svc.Name); ok {
		if clean, cleanErr := isCleanCheckoutAt(ctx, entry.LocalPath, sha); cleanErr == nil && clean {
			return dbPathFor(entry.LocalPath, svc), sha, nil
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
func isCleanCheckoutAt(ctx context.Context, dir, sha string) (bool, error) {
	info, statErr := os.Stat(dir)
	if statErr != nil || !info.IsDir() {
		return false, fmt.Errorf("local path %q: not a directory", dir)
	}
	headOut, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return false, fmt.Errorf("git -C %s rev-parse HEAD: %w", dir, err)
	}
	if strings.TrimSpace(string(headOut)) != sha {
		return false, nil
	}
	statusOut, err := exec.CommandContext(ctx, "git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return false, fmt.Errorf("git -C %s status: %w", dir, err)
	}
	return strings.TrimSpace(string(statusOut)) == "", nil
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
// existing FR.2 indexing pipeline against it, syncs the result into the
// local registry (so the next resolution hits step 2), and returns the
// resulting graph.db path.
func cloneAndIndex(ctx context.Context, svc fleetconfig.Service, ref, regPath string, opts ResolveOptions) (string, error) {
	scratchParent := opts.ScratchDir
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

	wsRoot := cloneDir
	wsConfigPath := filepath.Join(wsRoot, meta.ConfigFile)
	cfg, err := workspace.Load(wsConfigPath)
	if err != nil {
		return "", fmt.Errorf("load cloned workspace config %s: %w", wsConfigPath, err)
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
		return "", fmt.Errorf("index %s: %w", svc.Name, err)
	}

	if err := registry.Sync(regPath, svc.Name, wsRoot); err != nil {
		return "", fmt.Errorf("sync registry: %w", err)
	}

	return filepath.Join(dbDir, meta.DBFile), nil
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
