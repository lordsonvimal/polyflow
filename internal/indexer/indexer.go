// Package indexer implements the full polyflow indexing pipeline: scan →
// (incremental) parse → semantic analysis → linking passes → atomic DB swap.
// Extracted from the CLI so the pipeline is testable and benchmarkable.
package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/lordsonvimal/polyflow/internal/deps"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// Options configures an indexing run.
type Options struct {
	Config      *workspace.WorkspaceConfig
	DBDir       string // default: meta.DBDir
	PatternsDir string // default: "patterns/"
	Workers     int    // default: GOMAXPROCS
	Full        bool   // force full re-parse, ignoring the incremental cache
	Log         io.Writer
	Progress    func(done, total int)
}

// Stats reports what an indexing run did.
type Stats struct {
	TotalFiles   int
	ParsedFiles  int // actually parsed (changed or new)
	SkippedFiles int // unchanged, served from the incremental cache
	ErrorFiles   int
	Nodes        int
	Edges        int
	CrossLinks   int
	Elapsed      time.Duration
}

// Run executes the pipeline and atomically swaps the graph DB on success.
func Run(ctx context.Context, opts Options) (*Stats, error) {
	start := time.Now()
	cfg := opts.Config
	if cfg == nil {
		return nil, fmt.Errorf("indexer: nil workspace config")
	}
	if opts.DBDir == "" {
		opts.DBDir = meta.DBDir
	}
	if opts.PatternsDir == "" {
		opts.PatternsDir = "patterns/"
	}
	if opts.Workers <= 0 {
		opts.Workers = runtime.GOMAXPROCS(0)
	}
	logw := opts.Log
	if logw == nil {
		logw = io.Discard
	}
	progress := opts.Progress
	if progress == nil {
		progress = func(int, int) {}
	}

	if err := os.MkdirAll(opts.DBDir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", opts.DBDir, err)
	}

	// Load the incremental cache from the previous graph, if any.
	finalDB := filepath.Join(opts.DBDir, meta.DBFile)
	oldHashes := map[string]*graph.FileHash{}
	oldSemantic := map[string][2]string{} // service → (fingerprint, edgesJSON)
	if !opts.Full {
		if _, err := os.Stat(finalDB); err == nil {
			if oldStore, err := graph.NewSQLiteStore(finalDB); err == nil {
				if hs, err := oldStore.ListFileHashes(ctx); err == nil {
					oldHashes = hs
				}
				for _, svc := range cfg.Services {
					if fp, edges, err := oldStore.GetSemanticCache(ctx, svc.Name); err == nil && fp != "" {
						oldSemantic[svc.Name] = [2]string{fp, edges}
					}
				}
				oldStore.Close()
			}
		}
	}

	tmpDB := filepath.Join(opts.DBDir, "graph.db.tmp")
	_ = os.Remove(tmpDB)
	store, err := graph.NewSQLiteStore(tmpDB)
	if err != nil {
		return nil, fmt.Errorf("open tmp store: %w", err)
	}
	defer store.Close()

	reg, err := patterns.DefaultRegistry(opts.PatternsDir)
	if err != nil {
		return nil, fmt.Errorf("load default patterns: %w", err)
	}
	for _, p := range cfg.Patterns {
		pf, err := patterns.LoadFile(p)
		if err != nil {
			return nil, fmt.Errorf("load custom pattern %s: %w", p, err)
		}
		reg.RegisterFile(pf)
	}

	// ── Scan services ────────────────────────────────────────────────────────
	type serviceFiles struct {
		svc   workspace.Service
		files []string
		deps  []deps.Dependency
	}
	svcPaths := make([]string, len(cfg.Services))
	for i, svc := range cfg.Services {
		abs, err := filepath.Abs(svc.Path)
		if err != nil {
			abs = svc.Path
		}
		svcPaths[i] = abs
	}

	var allSvcFiles []serviceFiles
	for idx, svc := range cfg.Services {
		absSvcPath, _ := filepath.Abs(svc.Path)
		var extraExcludes []string
		for i, other := range svcPaths {
			if i == idx {
				continue
			}
			rel, err := filepath.Rel(absSvcPath, other)
			if err == nil && !strings.HasPrefix(rel, "..") && rel != "." {
				extraExcludes = append(extraExcludes, rel+"/**")
			}
		}
		excludes := append(append([]string{}, cfg.Index.Exclude...), extraExcludes...)
		files, err := walkService(svc.Path, excludes)
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", svc.Name, err)
		}

		svcDeps, err := deps.Resolve(absSvcPath)
		if err != nil {
			fmt.Fprintf(logw, "  Warning: dependency resolution for %s: %v\n", svc.Name, err)
		}
		for i := range svcDeps {
			d := svcDeps[i]
			if err := store.UpsertDependency(ctx, &graph.Dependency{
				Service: svc.Name, Ecosystem: d.Ecosystem, Name: d.Name,
				Version: d.Version, Kind: d.Kind,
			}); err != nil {
				return nil, err
			}
		}
		fmt.Fprintf(logw, "  %s: %d files (%s, %d deps)\n", svc.Name, len(files), svc.Language, len(svcDeps))
		allSvcFiles = append(allSvcFiles, serviceFiles{svc, files, svcDeps})
	}

	stats := &Stats{}
	for _, sf := range allSvcFiles {
		stats.TotalFiles += len(sf.files)
	}

	var allNodes []graph.Node
	var allEdges []graph.Edge
	bw := graph.NewBatchWriter(store)

	// Service-level datastore nodes from resolved driver dependencies.
	for _, sf := range allSvcFiles {
		for _, n := range deps.DatastoreNodes(sf.svc.Name, sf.deps) {
			node := n
			if err := bw.AddNode(ctx, &node); err != nil {
				return nil, err
			}
			allNodes = append(allNodes, node)
		}
	}

	// ── Parse (incremental) ──────────────────────────────────────────────────
	done := 0
	now := time.Now().Unix()
	// serviceFingerprint accumulates per-service "path:hash" lines for the
	// semantic cache key.
	svcHashLines := map[string][]string{}

	for _, sf := range allSvcFiles {
		matcher := patterns.NewTreeSitterMatcherForService(reg, sf.deps)

		var toParse []string
		hashes := map[string]string{}
		for _, file := range sf.files {
			data, err := os.ReadFile(file)
			if err != nil {
				stats.ErrorFiles++
				done++
				progress(done, stats.TotalFiles)
				continue
			}
			sum := sha256.Sum256(data)
			h := hex.EncodeToString(sum[:])
			hashes[file] = h
			svcHashLines[sf.svc.Name] = append(svcHashLines[sf.svc.Name], file+":"+h)

			old := oldHashes[file]
			if old != nil && old.ContentHash == h && old.Service == sf.svc.Name {
				// Unchanged: reuse cached parse results, skip tree-sitter.
				var nodes []graph.Node
				var edges []graph.Edge
				if json.Unmarshal([]byte(old.NodesJSON), &nodes) == nil &&
					json.Unmarshal([]byte(old.EdgesJSON), &edges) == nil {
					for i := range nodes {
						if err := bw.AddNode(ctx, &nodes[i]); err != nil {
							return nil, err
						}
						allNodes = append(allNodes, nodes[i])
					}
					for i := range edges {
						if err := bw.AddEdge(ctx, &edges[i]); err != nil {
							return nil, err
						}
						allEdges = append(allEdges, edges[i])
					}
					if old.Errored {
						stats.ErrorFiles++
						_ = store.UpsertParseError(ctx, &graph.ParseError{
							FilePath: file, Service: sf.svc.Name, ErrorCount: 1, IndexedAt: now,
						})
					}
					old.IndexedAt = now
					if err := store.UpsertFileHash(ctx, old); err != nil {
						return nil, err
					}
					stats.SkippedFiles++
					done++
					progress(done, stats.TotalFiles)
					continue
				}
			}
			toParse = append(toParse, file)
		}

		pool := parser.NewWorkerPool(opts.Workers, matcher, sf.svc.Name)
		for result := range pool.Run(toParse) {
			done++
			stats.ParsedFiles++
			progress(done, stats.TotalFiles)

			fh := &graph.FileHash{
				FilePath: result.File, Service: sf.svc.Name,
				ContentHash: hashes[result.File], IndexedAt: now,
				NodesJSON: "[]", EdgesJSON: "[]",
			}
			if result.Err != nil {
				stats.ErrorFiles++
				fh.Errored = true
				_ = store.UpsertParseError(ctx, &graph.ParseError{
					FilePath: result.File, Service: sf.svc.Name, ErrorCount: 1, IndexedAt: now,
				})
				if err := store.UpsertFileHash(ctx, fh); err != nil {
					return nil, err
				}
				continue
			}
			nodesJSON, _ := json.Marshal(result.Nodes)
			edgesJSON, _ := json.Marshal(result.Edges)
			fh.NodesJSON, fh.EdgesJSON = string(nodesJSON), string(edgesJSON)
			if err := store.UpsertFileHash(ctx, fh); err != nil {
				return nil, err
			}
			for i := range result.Nodes {
				n := result.Nodes[i]
				if err := bw.AddNode(ctx, &n); err != nil {
					return nil, err
				}
				allNodes = append(allNodes, n)
			}
			for i := range result.Edges {
				e := result.Edges[i]
				if err := bw.AddEdge(ctx, &e); err != nil {
					return nil, err
				}
				allEdges = append(allEdges, e)
			}
		}
	}

	// Flush tree-sitter nodes+edges before the semantic pass (FK constraints).
	if err := bw.Flush(ctx); err != nil {
		return nil, err
	}

	knownNodeIDs := make(map[string]bool, len(allNodes))
	for _, n := range allNodes {
		knownNodeIDs[n.ID] = true
	}

	// ── Semantic pass (go/packages), cached per service fingerprint ─────────
	var semanticWarnings []string
	fset := token.NewFileSet()
	for _, sf := range allSvcFiles {
		analyzer := parser.ServiceAnalyzerFor(sf.svc.Language)
		if analyzer == nil {
			continue
		}
		fingerprint := fingerprintLines(svcHashLines[sf.svc.Name])

		var semEdges []graph.Edge
		if cached, ok := oldSemantic[sf.svc.Name]; ok && cached[0] == fingerprint {
			_ = json.Unmarshal([]byte(cached[1]), &semEdges)
			fmt.Fprintf(logw, "  Semantic analysis: %s — cached (%d edges)\n", sf.svc.Name, len(semEdges))
		} else {
			absSvcPath, err := filepath.Abs(sf.svc.Path)
			if err != nil {
				absSvcPath = sf.svc.Path
			}
			fmt.Fprintf(logw, "  Semantic analysis: %s...\n", sf.svc.Name)
			sem := analyzer.AnalyzeService(absSvcPath, sf.svc.Name, fset, knownNodeIDs)
			if sem.Warning != "" {
				fmt.Fprintf(logw, "  Warning: %s\n", sem.Warning)
				semanticWarnings = append(semanticWarnings, sem.Warning)
				continue
			}
			semEdges = sem.Edges
			fmt.Fprintf(logw, "  Semantic analysis: %s — %d call edges added\n", sf.svc.Name, len(semEdges))
		}

		edgesJSON, _ := json.Marshal(semEdges)
		if err := store.UpsertSemanticCache(ctx, sf.svc.Name, fingerprint, string(edgesJSON)); err != nil {
			return nil, err
		}
		bwSem := graph.NewBatchWriter(store)
		for i := range semEdges {
			e := semEdges[i]
			if !knownNodeIDs[e.From] || !knownNodeIDs[e.To] {
				continue
			}
			if err := bwSem.AddEdge(ctx, &e); err != nil {
				return nil, err
			}
			allEdges = append(allEdges, e)
		}
		if err := bwSem.Flush(ctx); err != nil {
			return nil, err
		}
	}

	if len(semanticWarnings) > 0 {
		warningsJSON, _ := json.Marshal(semanticWarnings)
		_ = store.SetMeta(ctx, "semantic_warnings", string(warningsJSON))
	} else {
		_ = store.SetMeta(ctx, "semantic_warnings", "[]")
	}

	// ── Linking passes ───────────────────────────────────────────────────────
	writeEdges := func(edges []graph.Edge) error {
		bwE := graph.NewBatchWriter(store)
		for i := range edges {
			e := edges[i]
			if err := bwE.AddEdge(ctx, &e); err != nil {
				return err
			}
			allEdges = append(allEdges, e)
		}
		return bwE.Flush(ctx)
	}

	// JS/TS component + import-aware linking.
	{
		svcFiles := make(map[string][]string, len(allSvcFiles))
		for _, sf := range allSvcFiles {
			svcFiles[sf.svc.Name] = sf.files
		}
		jsLinker := linker.NewJSLinker()
		jsEdges, removeIDs := jsLinker.LinkJS(allNodes, allEdges, svcFiles)
		if err := writeEdges(jsEdges); err != nil {
			return nil, err
		}
		if len(removeIDs) > 0 {
			if err := store.DeleteNodes(ctx, removeIDs); err != nil {
				return nil, fmt.Errorf("delete proxy nodes: %w", err)
			}
			filtered := allNodes[:0]
			for _, n := range allNodes {
				if !removeIDs[n.ID] {
					filtered = append(filtered, n)
				}
			}
			allNodes = filtered
		}
	}

	if err := writeEdges(linker.LinkRouteHandlers(allNodes)); err != nil {
		return nil, err
	}
	if err := writeEdges(linker.LinkDatastores(allNodes)); err != nil {
		return nil, err
	}
	if err := writeEdges(linker.LinkBrokerChannels(allNodes)); err != nil {
		return nil, err
	}
	if err := writeEdges(linker.LinkWebSocketMessages(allNodes)); err != nil {
		return nil, err
	}
	if err := writeEdges(linker.LinkHubFanout(allNodes)); err != nil {
		return nil, err
	}
	if err := writeEdges(linker.LinkJobQueues(allNodes)); err != nil {
		return nil, err
	}
	if err := writeEdges(linker.LinkPusherChannels(allNodes)); err != nil {
		return nil, err
	}

	// Broker hint linking (via: rabbitmq + exchange).
	{
		hintNodes, hintEdges := linker.LinkBrokerHints(cfg.Links, allNodes)
		bwHint := graph.NewBatchWriter(store)
		for i := range hintNodes {
			n := hintNodes[i]
			if err := bwHint.AddNode(ctx, &n); err != nil {
				return nil, err
			}
			allNodes = append(allNodes, n)
		}
		if err := bwHint.Flush(ctx); err != nil {
			return nil, err
		}
		if err := writeEdges(hintEdges); err != nil {
			return nil, err
		}
	}

	// Cross-service HTTP linking.
	hintedNodes := linker.ApplyHints(cfg.Links, allNodes, allEdges)
	l := linker.New(cfg)
	crossEdges, err := l.Link(hintedNodes, allEdges)
	if err != nil {
		return nil, fmt.Errorf("link: %w", err)
	}
	bw2 := graph.NewBatchWriter(store)
	for i := range crossEdges {
		e := crossEdges[i]
		if _, err := store.GetNode(ctx, e.To); err != nil {
			_ = bw2.AddNode(ctx, &graph.Node{
				ID: e.To, Type: graph.NodeTypeHTTPHandler, Label: e.To, Service: "unresolved",
				File: "unresolved", Language: "unknown",
			})
		}
	}
	if err := bw2.Flush(ctx); err != nil {
		return nil, err
	}
	if err := writeEdges(crossEdges); err != nil {
		return nil, err
	}
	stats.CrossLinks = len(crossEdges)

	if err := store.SetMeta(ctx, "last_indexed", strconv.FormatInt(time.Now().Unix(), 10)); err != nil {
		return nil, err
	}
	store.Close()

	if err := os.Rename(tmpDB, finalDB); err != nil {
		return nil, fmt.Errorf("atomic swap: %w", err)
	}

	if s, err := graph.NewSQLiteStore(finalDB); err == nil {
		stats.Nodes, stats.Edges, _ = s.Stats(ctx)
		s.Close()
	}
	stats.Elapsed = time.Since(start)
	return stats, nil
}

// fingerprintLines hashes the sorted per-file hash lines of a service.
func fingerprintLines(lines []string) string {
	sorted := append([]string(nil), lines...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return hex.EncodeToString(sum[:])
}

// walkService collects parseable files under root, honoring exclude globs.
func walkService(root string, excludes []string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		for _, pattern := range excludes {
			if matched, _ := doublestar.Match(pattern, rel); matched {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if d.IsDir() {
			return nil
		}
		if parser.ForFile(path) == nil {
			return nil
		}
		files = append(files, path)
		return nil
	})
	return files, err
}
