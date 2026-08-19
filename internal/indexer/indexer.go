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
	"golang.org/x/sync/errgroup"

	contractdata "github.com/lordsonvimal/polyflow/contracts"
	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/deps"
	"github.com/lordsonvimal/polyflow/internal/evidence"
	"github.com/lordsonvimal/polyflow/internal/evidence/config_resolve"
	"github.com/lordsonvimal/polyflow/internal/evidence/contract_ingest"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/lordsonvimal/polyflow/internal/semantic"
	"github.com/lordsonvimal/polyflow/internal/sidecar"
	"github.com/lordsonvimal/polyflow/internal/toolchain"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// Options configures an indexing run.
type Options struct {
	Config       *workspace.WorkspaceConfig
	DBDir        string // default: meta.DBDir
	PatternsDir  string // default: "" → built-in patterns embedded in the binary; set to load from disk instead
	ContractsDir string // default: "" → no workspace-custom rules; set to the workspace root to load <dir>/contracts/*.yaml
	Workers      int    // default: GOMAXPROCS
	Full         bool   // force full re-parse, ignoring the incremental cache
	// NoEmbed skips the embedding pass entirely.  The next index without
	// NoEmbed will re-embed all entities (no incremental delta).  The
	// degradation reason is stamped in the "embed_status" meta key so
	// search can surface "semantic: unavailable: embeddings skipped".
	NoEmbed bool
	// Embedder overrides the embedding backend for this run.  When nil,
	// runEmbedPass falls back to DefaultStaticEmbedder (the safe default).
	// Set by the CLI from the workspace search.embedder config (S.3).
	Embedder semantic.Embedder
	Log      io.Writer
	Progress func(done, total int)
}

// Stats reports what an indexing run did.
type Stats struct {
	TotalFiles   int
	ParsedFiles  int // actually parsed (changed or new)
	SkippedFiles int // unchanged, served from the incremental cache
	ErrorFiles   int
	Nodes        int
	Edges        int
	// ContractEdges is every distinct edge the contract engine emitted, most of
	// which are same-service by design (http.yaml is `same_service: keep`, so a
	// page fetching its own API is a real internal edge).
	ContractEdges int
	// CrossLinks counts only the contract edges whose endpoints resolve to two
	// different, known services. It was previously set to len(contractResult.
	// Edges) while being printed as "cross-service", which overstated the real
	// figure 5.5× on the juniper fleet (563 reported, 103 actual).
	CrossLinks int
	Elapsed    time.Duration
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

	// Load the incremental cache from the previous graph, if any. Only the
	// workspace fingerprint (a single meta row) is fetched up front — it's
	// all the no-change fast path below needs. The heavy per-file/per-service
	// tables (file hashes with their cached parse-result blobs, semantic
	// cache, embedding metadata) are loaded lazily, only if the fingerprint
	// check falls through to a real rebuild. On an unchanged workspace this
	// avoids scanning tables that would otherwise never be used.
	finalDB := filepath.Join(opts.DBDir, meta.DBFile)
	oldHashes := map[string]*graph.FileHash{}
	oldSemantic := map[string][4]string{} // service → (fingerprint, nodesJSON, edgesJSON, referencedJSON)
	oldFingerprint := ""
	// oldEmbedMeta: entity_id → "embedder_id\x00content_hash" for hash-gating.
	oldEmbedMeta := map[string]string{}
	var oldStore *graph.SQLiteStore
	oldSchemaOK := false
	if !opts.Full {
		if _, err := os.Stat(finalDB); err == nil {
			if s, err := graph.NewSQLiteStore(finalDB); err == nil {
				oldStore = s
				// Cached results from an older data-model generation are
				// unusable — ignore them all and re-index from scratch.
				ver, _ := oldStore.GetMeta(ctx, "schema_version")
				if ver == graph.SchemaVersion {
					oldSchemaOK = true
					if fp, err := oldStore.GetMeta(ctx, "workspace_fingerprint"); err == nil {
						oldFingerprint = fp
					}
				} else {
					fmt.Fprintf(logw, "  Schema version changed (%q → %q) — full re-index\n", ver, graph.SchemaVersion)
				}
			}
		}
	}
	// loadIncrementalCache pulls the heavy per-file/per-service tables. Called
	// only once the no-change fast path has been ruled out.
	loadIncrementalCache := func() {
		if oldStore == nil || !oldSchemaOK {
			return
		}
		if hs, err := oldStore.ListFileHashes(ctx); err == nil {
			oldHashes = hs
		}
		for _, svc := range cfg.Services {
			if fp, nodes, edges, referenced, err := oldStore.GetSemanticCache(ctx, svc.Name); err == nil && fp != "" {
				oldSemantic[svc.Name] = [4]string{fp, nodes, edges, referenced}
			}
		}
		if metas, err := oldStore.ListEmbeddingMeta(ctx); err == nil {
			for _, m := range metas {
				oldEmbedMeta[m.EntityID] = m.EmbedderID + "\x00" + m.ContentHash
			}
		}
	}

	// Built-in patterns come from the binary's embedded copy by default, so the
	// indexer works from any working directory. An explicit PatternsDir (tests,
	// pattern development) overrides with an on-disk directory.
	var (
		reg *patterns.Registry
		err error
	)
	if opts.PatternsDir == "" {
		reg, err = patterns.EmbeddedRegistry()
	} else {
		reg, err = patterns.DefaultRegistry(opts.PatternsDir)
	}
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

	// .polyflowignore patterns apply on top of index.exclude; the file lives
	// at the workspace root (the directory the indexer runs from).
	ignorePatterns := workspace.LoadIgnoreFile(".")

	tcReg := toolchain.DefaultRegistry()
	// svcToolchainVersions: service name → tool → resolved version string.
	svcToolchainVersions := make(map[string]map[toolchain.Tool]string, len(cfg.Services))
	var allToolchainNotes []toolchain.CoverageNote
	// svcToolchainProfiles: service → tool → profile stamp (V.2 labeling —
	// which rule variant / sidecar backend interpreted each tool).
	type profileStamp struct {
		Profile  string `json:"profile"`
		Version  string `json:"version"`
		Inferred bool   `json:"inferred,omitempty"`
	}
	svcToolchainProfiles := make(map[string]map[string]profileStamp, len(cfg.Services))

	// B.0: unparsed-file-class ledger — counts per (service, extension).
	allUnparsedFiles := map[string]map[string]int{}

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
		excludes := append(append([]string{}, cfg.Index.Exclude...), ignorePatterns...)
		excludes = append(excludes, extraExcludes...)
		files, unparsed, err := walkService(svc.Path, excludes)
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", svc.Name, err)
		}
		if len(unparsed) > 0 {
			allUnparsedFiles[svc.Name] = unparsed
		}

		svcDeps, err := deps.Resolve(absSvcPath)
		if err != nil {
			fmt.Fprintf(logw, "  Warning: dependency resolution for %s: %v\n", svc.Name, err)
		}

		tcVersions := toolchain.ResolveToolchain(absSvcPath, svcDeps)
		svcToolchainVersions[svc.Name] = tcVersions
		selections, notes := toolchain.SelectAll(tcReg, svc.Name, tcVersions)
		allToolchainNotes = append(allToolchainNotes, notes...)
		stamps := make(map[string]profileStamp, len(selections))
		for _, sel := range selections {
			profile := sel.Backend.RuleVariant
			if profile == "" {
				profile = sel.Backend.SidecarBackend
			}
			stamps[string(sel.Tool)] = profileStamp{Profile: profile, Version: sel.Version, Inferred: sel.Inferred}
		}
		svcToolchainProfiles[svc.Name] = stamps

		fmt.Fprintf(logw, "  %s: %d files (%s, %d deps)\n", svc.Name, len(files), svc.Language, len(svcDeps))
		allSvcFiles = append(allSvcFiles, serviceFiles{svc, files, svcDeps})
	}

	stats := &Stats{}
	for _, sf := range allSvcFiles {
		stats.TotalFiles += len(sf.files)
	}

	// ── Hash pre-pass + no-change fast path ──────────────────────────────────
	// Hash every file up front. If the workspace fingerprint (config + file
	// set + content hashes + pattern files) matches the previous run, the
	// graph cannot differ — skip the rebuild entirely.
	//
	// This runs even when nothing changed (it's what proves that), so the
	// read+hash of every file is parallelized across opts.Workers the same
	// way the parse phase below is — otherwise it dominates a no-op re-index.
	now := time.Now().Unix()
	type hashJob struct {
		svc  string
		file string
	}
	var jobs []hashJob
	for _, sf := range allSvcFiles {
		for _, file := range sf.files {
			jobs = append(jobs, hashJob{sf.svc.Name, file})
		}
	}
	jobHashes := make([]string, len(jobs)) // "" = unreadable, recorded as an error during the parse loop
	// jobData retains each file's bytes alongside its hash so the parse phase
	// below can reuse them instead of reading every file from disk a second
	// time. Pruned to each service's changed-file subset as the parse loop
	// consumes it (see fileData below) rather than held for the whole run.
	jobData := make([][]byte, len(jobs))
	{
		g, _ := errgroup.WithContext(ctx)
		g.SetLimit(opts.Workers)
		for i, j := range jobs {
			i, j := i, j
			g.Go(func() error {
				data, err := os.ReadFile(j.file)
				if err != nil {
					return nil
				}
				sum := sha256.Sum256(data)
				jobHashes[i] = hex.EncodeToString(sum[:])
				jobData[i] = data
				return nil
			})
		}
		_ = g.Wait()
	}
	hashes := map[string]string{}         // file → content hash
	fileData := map[string][]byte{}       // file → pre-read bytes, consumed+pruned per service below
	svcHashLines := map[string][]string{} // semantic cache key input
	var fpLines []string
	for i, j := range jobs {
		h := jobHashes[i]
		if h == "" {
			continue
		}
		hashes[j.file] = h
		fileData[j.file] = jobData[i]
		svcHashLines[j.svc] = append(svcHashLines[j.svc], j.file+":"+h)
		fpLines = append(fpLines, j.svc+":"+j.file+":"+h)
	}
	cfgJSON, _ := json.Marshal(cfg)
	fpLines = append(fpLines, "config:"+string(cfgJSON))
	fpLines = append(fpLines, "patterns:"+patternsFingerprint(opts.PatternsDir, cfg.Patterns))
	// Include the embedder ID in the fingerprint so that changing the embedder
	// (S.3) invalidates the incremental cache and triggers a full re-embed.
	if opts.Embedder != nil {
		fpLines = append(fpLines, "embedder:"+opts.Embedder.ID())
	} else {
		fpLines = append(fpLines, "embedder:static-v1-int8") // default when nil
	}
	workspaceFingerprint := fingerprintLines(fpLines)

	if !opts.Full && oldFingerprint != "" && workspaceFingerprint == oldFingerprint {
		finalStore, err := graph.NewSQLiteStore(finalDB)
		if err == nil {
			defer finalStore.Close()
			runAt := time.Now().Unix()
			_ = finalStore.SetMeta(ctx, "last_indexed", strconv.FormatInt(runAt, 10))
			if n, e, err := finalStore.Stats(ctx); err == nil {
				stats.Nodes, stats.Edges = n, e
			}
			if v, err := finalStore.GetMeta(ctx, "cross_links"); err == nil {
				stats.CrossLinks, _ = strconv.Atoi(v)
			}
			if v, err := finalStore.GetMeta(ctx, "contract_edges"); err == nil {
				stats.ContractEdges, _ = strconv.Atoi(v)
			}
			// D.2: record history row using the persisted unresolved refs.
			if refs, hErr := finalStore.ListUnresolvedRefs(ctx); hErr == nil {
				rows := aggregateUnresolvedHistory(refs, runAt)
				if wErr := finalStore.WriteUnresolvedHistory(ctx, rows); wErr == nil {
					_ = finalStore.PruneUnresolvedHistory(ctx, 50)
				}
			}
			stats.SkippedFiles = stats.TotalFiles
			stats.Elapsed = time.Since(start)
			fmt.Fprintf(logw, "  No changes since last index — graph reused.\n")
			if oldStore != nil {
				oldStore.Close()
			}
			return stats, nil
		}
		// Fall through to a full build if the previous DB cannot be opened.
	}
	// No-change fast path didn't apply — this is a real (re)build, so pull in
	// whatever incremental cache exists to skip per-file/per-service work below.
	loadIncrementalCache()
	if oldStore != nil {
		oldStore.Close()
	}

	// V.2: one sidecar process pool for the whole run; per-service routers
	// dispatch sidecar'd engines (templ) through it. A missing/dead sidecar
	// falls back to the in-process parser with a coverage note — never an
	// aborted run, never a dropped file.
	sidecarMgr := sidecar.NewManager("")
	defer sidecarMgr.Shutdown()

	tmpDB := filepath.Join(opts.DBDir, "graph.db.tmp")
	_ = os.Remove(tmpDB)
	store, err := graph.NewBuildStore(tmpDB)
	if err != nil {
		return nil, fmt.Errorf("open tmp store: %w", err)
	}
	defer store.Close()

	for _, sf := range allSvcFiles {
		for i := range sf.deps {
			d := sf.deps[i]
			if err := store.UpsertDependency(ctx, &graph.Dependency{
				Service: sf.svc.Name, Ecosystem: d.Ecosystem, Name: d.Name,
				Version: d.Version, Kind: d.Kind,
			}); err != nil {
				return nil, err
			}
		}
	}

	var allNodes []graph.Node
	var allEdges []graph.Edge
	var allUnresolved []graph.UnresolvedRef // recall gauge: references that resolved to nothing
	bw := graph.NewFreshBatchWriter(store)

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
	// File-hash records are collected and written in one transaction at the
	// end of the parse phase — per-row autocommit costs one fsync per file.
	var fhBatch []*graph.FileHash

	for _, sf := range allSvcFiles {
		matcher := patterns.NewTreeSitterMatcherForService(reg, sf.deps)
		// V.1: wire the resolved datastar vocabulary into the matcher so the
		// templ parser applies the correct attribute-key syntax for this service.
		if dsVersion, ok := svcToolchainVersions[sf.svc.Name][toolchain.ToolDatastar]; ok && dsVersion != "" {
			dsSel := tcReg.Select(toolchain.ToolDatastar, dsVersion)
			matcher.DatastarVariant = dsSel.Backend.RuleVariant
		}

		var toParse []string
		for _, file := range sf.files {
			h, ok := hashes[file]
			if !ok { // unreadable during the hash pre-pass
				stats.ErrorFiles++
				done++
				progress(done, stats.TotalFiles)
				continue
			}

			old := oldHashes[file]
			if old != nil && old.ContentHash == h && old.Service == sf.svc.Name {
				// Unchanged: reuse cached parse results, skip tree-sitter.
				var nodes []graph.Node
				var edges []graph.Edge
				if json.Unmarshal([]byte(old.NodesJSON), &nodes) == nil &&
					json.Unmarshal([]byte(old.EdgesJSON), &edges) == nil {
					var cachedUnresolved []graph.UnresolvedRef
					if json.Unmarshal([]byte(old.UnresolvedJSON), &cachedUnresolved) == nil {
						allUnresolved = append(allUnresolved, cachedUnresolved...)
					}
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
					fhBatch = append(fhBatch, old)
					stats.SkippedFiles++
					done++
					progress(done, stats.TotalFiles)
					continue
				}
			}
			toParse = append(toParse, file)
		}

		// Hand the parse phase the bytes already read for these files during
		// the hash pre-pass instead of making every parser re-read from disk.
		svcSource := make(parser.SourceCache, len(toParse))
		for _, file := range toParse {
			svcSource[file] = fileData[file]
		}

		router := sidecar.NewRouter(sidecarMgr, tcReg, sf.svc.Name, svcToolchainVersions[sf.svc.Name])
		pool := parser.NewWorkerPool(opts.Workers, matcher, sf.svc.Name)
		pool.SetRoute(router.ParserFor)
		pool.SetSourceCache(svcSource)
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
				fhBatch = append(fhBatch, fh)
				continue
			}
			nodesJSON, _ := json.Marshal(result.Nodes)
			edgesJSON, _ := json.Marshal(result.Edges)
			unresolvedJSON, _ := json.Marshal(result.Unresolved)
			fh.NodesJSON, fh.EdgesJSON, fh.UnresolvedJSON = string(nodesJSON), string(edgesJSON), string(unresolvedJSON)
			fhBatch = append(fhBatch, fh)
			allUnresolved = append(allUnresolved, result.Unresolved...)
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
		// This service's bytes (parsed or cache-hit) are no longer needed —
		// drop them now rather than holding the whole workspace's file
		// contents in memory for the entire parse loop.
		for _, file := range sf.files {
			delete(fileData, file)
		}
		// Sidecar routing outcomes (inferred selections, in-process fallbacks).
		allToolchainNotes = append(allToolchainNotes, router.Notes()...)
	}

	// Flush tree-sitter nodes+edges before the semantic pass (FK constraints).
	if err := bw.Flush(ctx); err != nil {
		return nil, err
	}
	if err := store.UpsertFileHashes(ctx, fhBatch); err != nil {
		return nil, err
	}

	knownNodeIDs := make(map[string]bool, len(allNodes))
	for _, n := range allNodes {
		knownNodeIDs[n.ID] = true
	}

	// ── Semantic pass (go/packages), cached per service fingerprint ─────────
	var semanticWarnings []string
	referencedIDs := map[string]bool{} // callback-classification input (root_kind)
	fset := token.NewFileSet()
	for _, sf := range allSvcFiles {
		analyzer := parser.ServiceAnalyzerFor(sf.svc.Language)
		if analyzer == nil {
			continue
		}
		fingerprint := fingerprintLines(svcHashLines[sf.svc.Name])

		var semNodes []graph.Node
		var semEdges []graph.Edge
		var semReferenced []string
		if cached, ok := oldSemantic[sf.svc.Name]; ok && cached[0] == fingerprint {
			_ = json.Unmarshal([]byte(cached[1]), &semNodes)
			_ = json.Unmarshal([]byte(cached[2]), &semEdges)
			_ = json.Unmarshal([]byte(cached[3]), &semReferenced)
			fmt.Fprintf(logw, "  Semantic analysis: %s — cached (%d nodes, %d edges)\n", sf.svc.Name, len(semNodes), len(semEdges))
		} else {
			absSvcPath, err := filepath.Abs(sf.svc.Path)
			if err != nil {
				absSvcPath = sf.svc.Path
			}
			// templ codegen preflight: regenerate missing/stale _templ.go so the
			// go/packages build below succeeds instead of failing on undefined
			// generated symbols and dropping the whole service to tree-sitter.
			if sf.svc.Language == "go" {
				if ts := ensureTemplGenerated(absSvcPath); ts.note != "" {
					fmt.Fprintf(logw, "  templ: %s\n", ts.note)
				}
			}
			fmt.Fprintf(logw, "  Semantic analysis: %s...\n", sf.svc.Name)
			sem := analyzer.AnalyzeService(absSvcPath, sf.svc.Name, fset, knownNodeIDs)
			if sem.Warning != "" {
				fmt.Fprintf(logw, "  Warning: %s\n", sem.Warning)
				semanticWarnings = append(semanticWarnings, sem.Warning)
				continue
			}
			semNodes, semEdges, semReferenced = sem.Nodes, sem.Edges, sem.Referenced
			fmt.Fprintf(logw, "  Semantic analysis: %s — %d nodes, %d edges added\n", sf.svc.Name, len(semNodes), len(semEdges))
		}
		for _, id := range semReferenced {
			referencedIDs[id] = true
		}

		nodesJSON, _ := json.Marshal(semNodes)
		edgesJSON, _ := json.Marshal(semEdges)
		referencedJSON, _ := json.Marshal(semReferenced)
		if err := store.UpsertSemanticCache(ctx, sf.svc.Name, fingerprint, string(nodesJSON), string(edgesJSON), string(referencedJSON)); err != nil {
			return nil, err
		}
		bwSem := graph.NewBatchWriter(store)
		// Semantic nodes (variables, structs) land before edges so FK
		// references and the knownNodeIDs endpoint check both hold.
		for i := range semNodes {
			n := semNodes[i]
			if err := bwSem.AddNode(ctx, &n); err != nil {
				return nil, err
			}
			knownNodeIDs[n.ID] = true
			allNodes = append(allNodes, n)
		}
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
	var jsImportedNames map[string]bool
	{
		svcFiles := make(map[string][]string, len(allSvcFiles))
		for _, sf := range allSvcFiles {
			svcFiles[sf.svc.Name] = sf.files
		}
		jsLinker := linker.NewJSLinker()
		jsEdges, removeIDs, linkerUnresolved, importedNames := jsLinker.LinkJS(allNodes, allEdges, svcFiles)
		jsImportedNames = importedNames
		// Parser-level call_ref candidates that an import statement explains
		// are either resolved by the linker or point at external packages —
		// both are accounted for; the rest are real blind spots.
		filtered := allUnresolved[:0]
		for _, u := range allUnresolved {
			if u.Kind == "call_ref" && importedNames[u.File+"\x00"+u.Name] {
				continue
			}
			filtered = append(filtered, u)
		}
		allUnresolved = append(filtered, linkerUnresolved...)
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
			// DeleteNodes cascades edge deletion in the store; the in-memory
			// edge set must match, or the evidence reconciler re-upserts edges
			// whose endpoints no longer exist (FK failure aborts the index).
			filteredEdges := allEdges[:0]
			for _, e := range allEdges {
				if !removeIDs[e.From] && !removeIDs[e.To] {
					filteredEdges = append(filteredEdges, e)
				}
			}
			allEdges = filteredEdges
		}
	}
	// L.W1: global/window symbol resolution + inline handler linking.
	// Runs after LinkJS so imports-first ordering is enforced via jsImportedNames.
	{
		svcFiles := make(map[string][]string, len(allSvcFiles))
		for _, sf := range allSvcFiles {
			svcFiles[sf.svc.Name] = sf.files
		}
		globalEdges, globallyResolved, globalCollisions := linker.LinkJSGlobals(allNodes, allUnresolved, jsImportedNames, svcFiles)
		// Suppress call_refs that global resolution explained.
		filtered := allUnresolved[:0]
		for _, u := range allUnresolved {
			if u.Kind == "call_ref" && globallyResolved[u.File+"\x00"+u.Name] {
				continue
			}
			filtered = append(filtered, u)
		}
		allUnresolved = append(filtered, globalCollisions...)
		if err := writeEdges(globalEdges); err != nil {
			return nil, err
		}
	}

	// JS/TS cross-file inherits/implements/instantiates edges.
	{
		svcFiles := make(map[string][]string, len(allSvcFiles))
		for _, sf := range allSvcFiles {
			svcFiles[sf.svc.Name] = sf.files
		}
		jsTypeEdges, jsTypeUnresolved := linker.LinkJSTypeRelations(allNodes, svcFiles)
		if err := writeEdges(jsTypeEdges); err != nil {
			return nil, err
		}
		allUnresolved = append(allUnresolved, jsTypeUnresolved...)
	}
	// Ruby cross-file inherits/implements/instantiates edges.
	{
		svcFiles := make(map[string][]string, len(allSvcFiles))
		for _, sf := range allSvcFiles {
			svcFiles[sf.svc.Name] = sf.files
		}
		rubyTypeEdges, rubyTypeUnresolved := linker.LinkRubyTypeRelations(allNodes, svcFiles)
		if err := writeEdges(rubyTypeEdges); err != nil {
			return nil, err
		}
		allUnresolved = append(allUnresolved, rubyTypeUnresolved...)
	}
	// Cross-file `ClassName.method_name` calls (Product.find_by,
	// UserCategoryRuleSet.latest_for, LicenseReportJob.create!) — the
	// same-file case is extractRubyVariables' job; this is the cross-file
	// half, same split as the type-relations pass above.
	{
		svcFiles := make(map[string][]string, len(allSvcFiles))
		for _, sf := range allSvcFiles {
			svcFiles[sf.svc.Name] = sf.files
		}
		classCallEdges, classCallUnresolved := linker.LinkRubyClassMethodCalls(allNodes, svcFiles)
		if err := writeEdges(classCallEdges); err != nil {
			return nil, err
		}
		allUnresolved = append(allUnresolved, classCallUnresolved...)
	}
	// ActiveRecord has_many/belongs_to/has_one associations — a
	// class-granularity `calls` edge to the associated model, the same
	// shape emitClassMethodCall uses for a call that lands on no method
	// node (an ActiveRecord finder, a `scope` macro).
	{
		svcFiles := make(map[string][]string, len(allSvcFiles))
		for _, sf := range allSvcFiles {
			svcFiles[sf.svc.Name] = sf.files
		}
		assocEdges, assocUnresolved := linker.LinkRubyAssociations(allNodes, svcFiles)
		if err := writeEdges(assocEdges); err != nil {
			return nil, err
		}
		allUnresolved = append(allUnresolved, assocUnresolved...)
	}
	// Rails filter chain: before_action/around_action/after_action → the method
	// the callback names, from the declaring class and from each action it
	// guards. Needs the Ruby method nodes' qualified_name, so it runs after the
	// parse phase; independent of the type-relation edges above.
	{
		svcFiles := make(map[string][]string, len(allSvcFiles))
		for _, sf := range allSvcFiles {
			svcFiles[sf.svc.Name] = sf.files
		}
		filterEdges, filterUnresolved := linker.LinkRailsFilters(allNodes, svcFiles)
		if err := writeEdges(filterEdges); err != nil {
			return nil, err
		}
		allUnresolved = append(allUnresolved, filterUnresolved...)
	}
	// C.4: a bare Ruby call the parser could not bind in its own file, resolved
	// against the methods the calling class inherits or mixes in. Must run after
	// LinkRubyTypeRelations, whose `inherits` edges are the ancestor chain this
	// walks — and which is also what keeps it from binding a call to the copy of
	// lib/dx.rb another service vendors.
	{
		mixinEdges, mixinResolved, mixinCollisions := linker.LinkRubyMixinMethods(allNodes, allEdges, allUnresolved)
		filtered := allUnresolved[:0]
		for _, u := range allUnresolved {
			if u.Kind == "call_ref" && mixinResolved[linker.RubyCallRefKey(u.File, u.Line, u.Name)] {
				continue
			}
			filtered = append(filtered, u)
		}
		allUnresolved = append(filtered, mixinCollisions...)
		if err := writeEdges(mixinEdges); err != nil {
			return nil, err
		}
	}

	// Tier-L: rewrite dynamic Ruby http_client URLs (`url`, `path: url`) to the
	// concrete `ENV.fetch("VAR")` their host method resolves to, cross-file, so
	// the downstream config_resolve provider can bind them (or ledger a *named*
	// deploy-secret miss) instead of an unactionable token. Runs before the
	// contract engine + config_resolve so both see the upgraded key_dynamic_raw;
	// re-persist the mutated nodes so the store copy matches the in-memory set
	// the contract provider reads back.
	{
		svcFiles := make(map[string][]string, len(allSvcFiles))
		for _, sf := range allSvcFiles {
			svcFiles[sf.svc.Name] = sf.files
		}
		if hostNodes := linker.ResolveRubyHTTPHosts(allNodes, svcFiles); len(hostNodes) > 0 {
			for i := range hostNodes {
				n := hostNodes[i]
				if err := bw.AddNode(ctx, &n); err != nil {
					return nil, err
				}
			}
			if err := bw.Flush(ctx); err != nil {
				return nil, err
			}
		}

		// J.2b: the Go analogue — stamp Meta["env_var"] on Go http_client nodes
		// whose base URL traces back to an os.Getenv read, so ApplyHints (J.2c)
		// can turn a workspace `hint: SOME_URL` into a target_service allowlist.
		// Must run before ApplyHints, like the Ruby pass.
		if hostNodes := linker.ResolveGoHTTPHosts(allNodes, svcFiles); len(hostNodes) > 0 {
			for i := range hostNodes {
				n := hostNodes[i]
				if err := bw.AddNode(ctx, &n); err != nil {
					return nil, err
				}
			}
			if err := bw.Flush(ctx); err != nil {
				return nil, err
			}
		}

		// Tier JH: the JS/TS analogue of the two passes above. Neither traces a
		// JS/TS client at all, so this is the only source of Meta["env_var"] /
		// Meta["host_default_literal"] for JS/TS nodes — must also run before
		// Tier CB, same as the Go/Ruby passes.
		if hostNodes := linker.ResolveJSHTTPHosts(allNodes, svcFiles); len(hostNodes) > 0 {
			for i := range hostNodes {
				n := hostNodes[i]
				if err := bw.AddNode(ctx, &n); err != nil {
					return nil, err
				}
			}
			if err := bw.Flush(ctx); err != nil {
				return nil, err
			}
		}

		// Tier CB: the two passes above recover *which* env var a client's base
		// URL comes from; this one reads the path component out of that
		// variable's checked-in value and composes it onto the node's own path,
		// so a client deployed behind `API_URL=https://host/api/v2` can join the
		// `/api/v2/...` route it really calls. Runs here so it sees fresh stamps
		// from both, and well before ApplyHints. Both passes mutate allNodes in
		// place, so only re-persisting is needed.
		svcDirs := make(map[string]string, len(allSvcFiles))
		for _, sf := range allSvcFiles {
			svcDirs[sf.svc.Name] = sf.svc.Path
		}
		if prefixNodes := linker.ResolveConfigBaseURLPaths(allNodes, svcDirs); len(prefixNodes) > 0 {
			for i := range prefixNodes {
				n := prefixNodes[i]
				if err := bw.AddNode(ctx, &n); err != nil {
					return nil, err
				}
			}
			if err := bw.Flush(ctx); err != nil {
				return nil, err
			}
		}
	}

	if err := writeEdges(linker.LinkRouteHandlers(allNodes)); err != nil {
		return nil, err
	}
	{
		grpcEdges, grpcUnresolved := linker.LinkGRPCHandlers(allNodes)
		if err := writeEdges(grpcEdges); err != nil {
			return nil, err
		}
		allUnresolved = append(allUnresolved, grpcUnresolved...)
	}
	// Rails routes name their action by convention, not by the Meta["handler"]
	// receiver string LinkRouteHandlers keys on, so they need their own pass.
	{
		railsActionEdges, railsActionUnresolved := linker.LinkRailsRouteActions(allNodes)
		if err := writeEdges(railsActionEdges); err != nil {
			return nil, err
		}
		allUnresolved = append(allUnresolved, railsActionUnresolved...)
	}
	{
		routeCompEdges, routeCompUnresolved := linker.LinkRouteComponents(allNodes)
		if err := writeEdges(routeCompEdges); err != nil {
			return nil, err
		}
		allUnresolved = append(allUnresolved, routeCompUnresolved...)
	}
	if err := writeEdges(linker.LinkTemplComponents(allNodes)); err != nil {
		return nil, err
	}
	// templ <script src> → JS file imports.
	{
		scriptEdges, scriptUnresolved := linker.LinkTemplScripts(allNodes)
		if err := writeEdges(scriptEdges); err != nil {
			return nil, err
		}
		allUnresolved = append(allUnresolved, scriptUnresolved...)
	}
	// JS DOM target → templ element `defined_in` (creates templ_element nodes).
	{
		domNodes, domEdges, domUnresolved := linker.LinkDOMDefinitions(allNodes)
		for i := range domNodes {
			n := domNodes[i]
			if err := bw.AddNode(ctx, &n); err != nil {
				return nil, err
			}
			allNodes = append(allNodes, n)
		}
		if err := bw.Flush(ctx); err != nil {
			return nil, err
		}
		if err := writeEdges(domEdges); err != nil {
			return nil, err
		}
		allUnresolved = append(allUnresolved, domUnresolved...)
	}
	// templ producer (data-testid/id) attribute -> JS attribute-selector
	// consumer `dom_contract` (IA.5): component -> JS site directly, no
	// intermediate node, so investigate/walkFlows reach it in one hop.
	{
		_, contractEdges, contractUnresolved := linker.LinkDOMContracts(allNodes)
		if err := writeEdges(contractEdges); err != nil {
			return nil, err
		}
		allUnresolved = append(allUnresolved, contractUnresolved...)
	}
	// Structural backbone: service→file→declaration + struct→method contains
	// edges (mints synthetic service/file nodes, so persist them before wiring).
	{
		containNodes, containEdges := linker.LinkContainment(allNodes)
		for i := range containNodes {
			n := containNodes[i]
			if err := bw.AddNode(ctx, &n); err != nil {
				return nil, err
			}
			allNodes = append(allNodes, n)
		}
		if err := bw.Flush(ctx); err != nil {
			return nil, err
		}
		if err := writeEdges(containEdges); err != nil {
			return nil, err
		}
	}
	// Backbone completeness: mint a bare file node for every scanned file that
	// LinkContainment skipped (barrel/re-export-only and enum-only files declare
	// nothing containment-shaped). Runs before the JS import-edge pass so those
	// files are already valid, persisted import targets rather than mint-on-miss
	// fallbacks there.
	{
		svcFiles := make(map[string][]string, len(allSvcFiles))
		for _, sf := range allSvcFiles {
			svcFiles[sf.svc.Name] = sf.files
		}
		barrelNodes, barrelEdges := linker.EnsureAllScannedFiles(allNodes, svcFiles)
		for i := range barrelNodes {
			n := barrelNodes[i]
			if err := bw.AddNode(ctx, &n); err != nil {
				return nil, err
			}
			allNodes = append(allNodes, n)
		}
		if err := bw.Flush(ctx); err != nil {
			return nil, err
		}
		if err := writeEdges(barrelEdges); err != nil {
			return nil, err
		}
	}
	// JS/TS + Ruby file-level import edges (file→file between NodeTypeFile nodes).
	// Runs after LinkContainment so the file nodes are present in allNodes.
	{
		svcFiles := make(map[string][]string, len(allSvcFiles))
		for _, sf := range allSvcFiles {
			svcFiles[sf.svc.Name] = sf.files
		}
		jsImportEdges, updatedFileNodes, jsImportUnresolved := linker.LinkJSImportEdges(allNodes, svcFiles)
		for i := range updatedFileNodes {
			n := updatedFileNodes[i]
			if err := bw.AddNode(ctx, &n); err != nil {
				return nil, err
			}
		}
		if err := bw.Flush(ctx); err != nil {
			return nil, err
		}
		if err := writeEdges(jsImportEdges); err != nil {
			return nil, err
		}
		allUnresolved = append(allUnresolved, jsImportUnresolved...)
	}
	// JS/TS wrapped API-client calls (services/ApiServices.js-style shared
	// axios/fetch wrappers): mints an http_client node for a call to a
	// WB.1-detected wrapper even across files and even when the URL argument
	// is a local variable, not a literal — producer_alias_url_call/obj_call
	// require a literal at the call site and never fire otherwise.
	{
		svcFiles := make(map[string][]string, len(allSvcFiles))
		for _, sf := range allSvcFiles {
			svcFiles[sf.svc.Name] = sf.files
		}
		wrapperNodes, wrapperEdges := linker.LinkJSAPIWrapperCalls(allNodes, svcFiles)
		for i := range wrapperNodes {
			n := wrapperNodes[i]
			if err := bw.AddNode(ctx, &n); err != nil {
				return nil, err
			}
			allNodes = append(allNodes, n)
		}
		if err := bw.Flush(ctx); err != nil {
			return nil, err
		}
		if err := writeEdges(wrapperEdges); err != nil {
			return nil, err
		}
	}
	// Tier K.5: stylesheet @import graph + containment for the selector and
	// @font-face nodes the stylesheet parser mints.
	{
		svcFiles := make(map[string][]string, len(allSvcFiles))
		for _, sf := range allSvcFiles {
			svcFiles[sf.svc.Name] = sf.files
		}
		cssNodes, cssEdges, cssUnresolved := linker.LinkStylesheetImports(allNodes, svcFiles)
		for i := range cssNodes {
			n := cssNodes[i]
			if err := bw.AddNode(ctx, &n); err != nil {
				return nil, err
			}
			allNodes = append(allNodes, n)
		}
		if err := bw.Flush(ctx); err != nil {
			return nil, err
		}
		if err := writeEdges(cssEdges); err != nil {
			return nil, err
		}
		allUnresolved = append(allUnresolved, cssUnresolved...)
	}
	// Tier K.3: Rails asset pipeline — `//= require` directives plus the
	// `javascript_include_tag` page bindings that sit on top of them.
	{
		svcFiles := make(map[string][]string, len(allSvcFiles))
		for _, sf := range allSvcFiles {
			svcFiles[sf.svc.Name] = sf.files
		}
		assetNodes, assetEdges, assetUnresolved := linker.LinkSprocketsAssets(allNodes, svcFiles)
		for i := range assetNodes {
			n := assetNodes[i]
			if err := bw.AddNode(ctx, &n); err != nil {
				return nil, err
			}
			allNodes = append(allNodes, n)
		}
		if err := bw.Flush(ctx); err != nil {
			return nil, err
		}
		if err := writeEdges(assetEdges); err != nil {
			return nil, err
		}
		allUnresolved = append(allUnresolved, assetUnresolved...)
	}
	// Tier K.2: Rails view layer — partial nesting, the controller→template
	// convention, and the react_component mount seam.
	{
		svcFiles := make(map[string][]string, len(allSvcFiles))
		for _, sf := range allSvcFiles {
			svcFiles[sf.svc.Name] = sf.files
		}
		viewNodes, viewEdges, viewUnresolved := linker.LinkRailsViews(allNodes, svcFiles)
		for i := range viewNodes {
			n := viewNodes[i]
			if err := bw.AddNode(ctx, &n); err != nil {
				return nil, err
			}
			allNodes = append(allNodes, n)
		}
		if err := bw.Flush(ctx); err != nil {
			return nil, err
		}
		if err := writeEdges(viewEdges); err != nil {
			return nil, err
		}
		allUnresolved = append(allUnresolved, viewUnresolved...)
	}
	{
		svcFiles := make(map[string][]string, len(allSvcFiles))
		for _, sf := range allSvcFiles {
			svcFiles[sf.svc.Name] = sf.files
		}
		rubyImportEdges, rubyImportUnresolved := linker.LinkRubyImportEdges(allNodes, svcFiles)
		if err := writeEdges(rubyImportEdges); err != nil {
			return nil, err
		}
		allUnresolved = append(allUnresolved, rubyImportUnresolved...)
	}

	if err := writeEdges(linker.LinkDatastores(allNodes)); err != nil {
		return nil, err
	}
	// Y.3c: parse table names out of datastore call SQL and terminate each
	// query/persist at a real table entity (mints table nodes).
	{
		tableNodes, tableEdges := linker.LinkTables(allNodes)
		for i := range tableNodes {
			n := tableNodes[i]
			if err := bw.AddNode(ctx, &n); err != nil {
				return nil, err
			}
			allNodes = append(allNodes, n)
		}
		if err := bw.Flush(ctx); err != nil {
			return nil, err
		}
		if err := writeEdges(tableEdges); err != nil {
			return nil, err
		}
	}
	// Y.4: join server response DTOs to the client interfaces that mirror their
	// JSON shape (cross-language response_of). Runs after all returns/consumes
	// edges are collected so it can gate on server-declared response structs.
	if err := writeEdges(linker.LinkResponseShapes(allNodes, allEdges)); err != nil {
		return nil, err
	}
	// Y.6: join a createResource loader's http_client to the reactive signal it
	// feeds (http_client → signal flows_to). Needs the calls edges from Pass 2,
	// so it runs after the bulk of edges are collected.
	if err := writeEdges(linker.LinkResourceSignals(allNodes, allEdges)); err != nil {
		return nil, err
	}
	if err := writeEdges(linker.LinkSSEClients(allNodes)); err != nil {
		return nil, err
	}

	// Broker hint linking (via: rabbitmq + exchange).
	{
		hintNodes, hintEdges, hintUnresolved := linker.LinkBrokerHints(cfg.Links, allNodes)
		allUnresolved = append(allUnresolved, hintUnresolved...)
		for i := range hintNodes {
			n := hintNodes[i]
			if err := bw.AddNode(ctx, &n); err != nil {
				return nil, err
			}
			allNodes = append(allNodes, n)
		}
		if err := bw.Flush(ctx); err != nil {
			return nil, err
		}
		if err := writeEdges(hintEdges); err != nil {
			return nil, err
		}
	}

	// L.W0: resolve Rails route-helper names on nav_link_rails_helper nodes to
	// real method+path so the http contract rule (G.1 nav variant) can match them.
	// Must run before ApplyHints so the resolved path is visible to the engine.
	{
		railsUpdated, railsUnresolved := linker.ResolveRailsNavHelpers(allNodes)
		// Build a quick ID→index map for O(1) in-place updates to allNodes.
		nodeByID := make(map[string]int, len(allNodes))
		for i, n := range allNodes {
			nodeByID[n.ID] = i
		}
		for i := range railsUpdated {
			n := railsUpdated[i]
			if err := bw.AddNode(ctx, &n); err != nil {
				return nil, err
			}
			if idx, ok := nodeByID[n.ID]; ok {
				allNodes[idx] = n
			} else {
				// Fan-out candidate: new node not in allNodes yet.
				allNodes = append(allNodes, n)
			}
		}
		if err := bw.Flush(ctx); err != nil {
			return nil, err
		}
		allUnresolved = append(allUnresolved, railsUnresolved...)
	}

	// M.0: file-based route synthesis (Next.js, SvelteKit, Nuxt, Remix).
	// Runs after per-file parsing and all linking passes, before the contract engine,
	// so synthesized http_handler nodes participate in cross-service linking.
	{
		// Build nodesInFile map from allNodes at this point (post-parse, post-link).
		fileNodeMap := make(map[string][]graph.Node, len(allNodes))
		for _, n := range allNodes {
			if n.File != "" {
				fileNodeMap[n.File] = append(fileNodeMap[n.File], n)
			}
		}
		nodesInFile := func(absFile string) []graph.Node { return fileNodeMap[absFile] }

		for _, sf := range allSvcFiles {
			absSvcPath, _ := filepath.Abs(sf.svc.Path)
			// Route synthesis needs ALL service files (including unparsed like .svelte, .vue),
			// not just the parser-handled subset — file-based routers are identified by their
			// filesystem paths, which exist regardless of whether a parser is registered.
			allSvcFilesList := walkAllFiles(absSvcPath)
			fr := linker.SynthesizeFileRoutes(absSvcPath, sf.svc.Name, allSvcFilesList, sf.deps, nodesInFile)
			for i := range fr.Nodes {
				n := fr.Nodes[i]
				if err := bw.AddNode(ctx, &n); err != nil {
					return nil, err
				}
				allNodes = append(allNodes, n)
			}
			if err := bw.Flush(ctx); err != nil {
				return nil, err
			}
			if err := writeEdges(fr.Edges); err != nil {
				return nil, err
			}
			allUnresolved = append(allUnresolved, fr.Unresolved...)
		}
	}

	// Cross-service contract linking (HTTP, AMQP, Hub, Jobs, Pusher, WebSocket via contracts/*.yaml).
	// opts.ContractsDir may add workspace-custom rules on top of the embedded defaults (G.5).
	contractRules, err := contract.Load(contractdata.FS, opts.ContractsDir)
	if err != nil {
		return nil, fmt.Errorf("contract rules: %w", err)
	}
	hintedNodes := linker.ApplyHints(cfg.Links, allNodes, allEdges)
	// G.3 pre-engine enrichment: reconstruct full route paths for nodes inside
	// router groups (gin r.Group / chi r.Route). This is a contextual node-join
	// that normalizers cannot perform; it mutates only the working copy returned
	// by ApplyHints, not the persisted allNodes.
	enrichedNodes := contract.EnrichRouteGroups(hintedNodes)
	// The composition above is computed for matching, on a working copy. Agents
	// query the *stored* graph, so the composed route has to reach it too —
	// otherwise a gin handler declared inside `v1.Group("/api/v1")` is persisted
	// reading `/users/:id`, which routes nowhere and which no search for the
	// real path can find. Only label + meta["full_path"] are written back; see
	// contract.setPath for why meta["path"] must stay raw.
	if err := persistComposedRoutes(ctx, bw, enrichedNodes, allNodes); err != nil {
		return nil, err
	}
	// Gin middleware chain: handler --calls--> the middleware guarding it
	// (r.Use/group.Use), so `impact`/`context` on a route or a middleware
	// function surfaces the other side without a separate tool.
	{
		mwEdges, mwUnresolved := linker.LinkGinMiddleware(enrichedNodes, allEdges)
		if err := writeEdges(mwEdges); err != nil {
			return nil, err
		}
		allUnresolved = append(allUnresolved, mwUnresolved...)
	}
	// G.7 pre-engine enrichment: resolve alias/instance bindings and one-hop
	// wrapper functions. Alias binding nodes (NodeTypeVariable with alias_name
	// or instance_name meta) are removed from the working copy; their info feeds
	// the alias table used to rewrite call nodes before Engine.Link.
	enrichedNodes, aliasUnresolved := contract.EnrichAliases(enrichedNodes)
	allUnresolved = append(allUnresolved, aliasUnresolved...)
	// K.6 step 3 pre-engine enrichment: carry a runtime-negotiated queue name
	// across the repo boundary on the registration handshake's field symbol, so
	// the existing queue_name contract can join publisher to consumer. Resolves
	// keys only — it emits no edges of its own.
	handshakeUnresolved, handshakeResolved := linker.LinkAMQPHandshake(enrichedNodes)
	allUnresolved = linker.DropResolvedRefs(allUnresolved, handshakeResolved)
	allUnresolved = append(allUnresolved, handshakeUnresolved...)
	// AH follow-up: the message-type dispatch join, distinct from and
	// unblocked by the queue-name handshake above — it answers "what breaks
	// if I change this message's shape" rather than "where does it go".
	// Emits edges directly (not through the contract engine) since the join
	// is on a bare constant name, not a structural role any contracts/*.yaml
	// rule already models.
	if mtEdges := linker.LinkAMQPMessageTypeDispatch(enrichedNodes); len(mtEdges) > 0 {
		if err := writeEdges(mtEdges); err != nil {
			return nil, err
		}
	}
	eng := &contract.Engine{}
	contractResult := eng.Link(enrichedNodes, contractRules, cfg.Links)

	for i := range contractResult.Nodes {
		n := contractResult.Nodes[i]
		_ = bw.AddNode(ctx, &n)
	}
	if err := bw.Flush(ctx); err != nil {
		return nil, err
	}
	if err := writeEdges(contractResult.Edges); err != nil {
		return nil, err
	}
	allUnresolved = append(allUnresolved, contractResult.Unresolved...)
	stats.ContractEdges, stats.CrossLinks = countContractEdges(contractResult.Edges, enrichedNodes)

	// G.5: persist per-kind coverage so `polyflow doctor` can report matched/unresolved.
	coverage := contract.ComputeCoverage(contractRules, contractResult)
	if coverageJSON, marshalErr := json.Marshal(coverage); marshalErr == nil {
		_ = store.SetMeta(ctx, "contract_coverage", string(coverageJSON))
	}

	// ── Root classification ──────────────────────────────────────────────────
	// With the full edge set assembled, function/method nodes with no incoming
	// edges are roots. Distinguish the three very different meanings so agents
	// and the UI don't have to guess: entrypoint (run by the runtime),
	// callback (referenced / satisfies an external interface — invoked by a
	// framework), unreachable (nothing references it: dead-code candidate).
	{
		// Containment is structural, not a reference: a file→function `contains`
		// edge does not make the function reached, so it must not mask a root.
		incoming := make(map[string]bool, len(allEdges))
		for _, e := range allEdges {
			if e.Type == graph.EdgeTypeContains {
				continue
			}
			incoming[e.To] = true
		}
		bwR := graph.NewBatchWriter(store)
		for i := range allNodes {
			n := &allNodes[i]
			if n.Type != graph.NodeTypeFunction && n.Type != graph.NodeTypeMethod {
				continue
			}
			if incoming[n.ID] {
				continue
			}
			kind := "unreachable"
			switch {
			case n.Label == "main" || n.Label == "init" || n.Label == "(module)":
				kind = "entrypoint"
			case referencedIDs[n.ID]:
				kind = "callback"
			}
			if n.Meta == nil {
				n.Meta = map[string]string{}
			}
			n.Meta["root_kind"] = kind
			if err := bwR.AddNode(ctx, n); err != nil {
				return nil, err
			}
		}
		if err := bwR.Flush(ctx); err != nil {
			return nil, err
		}
	}

	// ── Evidence-fusion reconciliation (F.0) ────────────────────────────────
	// Wrap the static pipeline output as the first evidence provider, stamp
	// all edges with provenance, and re-upsert them so the store reflects
	// Sources[]/VerificationState on every edge.
	{
		staticProv := evidence.NewStaticProvider(allNodes, allEdges, allUnresolved)
		contractProv := contract_ingest.NewContractProvider()
		configProv := config_resolve.NewConfigProvider(allNodes, allUnresolved)
		rec, err := evidence.NewReconciler(staticProv, contractProv, configProv)
		if err != nil {
			return nil, fmt.Errorf("evidence reconciler: %w", err)
		}
		result, err := rec.Reconcile(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("evidence reconcile: %w", err)
		}
		// Re-upsert reconciled edges (ON CONFLICT DO UPDATE stamps the new fields)
		// and persist any synthetic nodes the reconciler minted for gap edges —
		// without them the gap edges would dangle.
		bwEv := graph.NewBatchWriter(store)
		staticNodeIDs := make(map[string]bool, len(allNodes))
		for i := range allNodes {
			staticNodeIDs[allNodes[i].ID] = true
		}
		for i := range result.Nodes {
			if staticNodeIDs[result.Nodes[i].ID] {
				continue
			}
			n := result.Nodes[i]
			if err := bwEv.AddNode(ctx, &n); err != nil {
				return nil, fmt.Errorf("persist reconciled node: %w", err)
			}
		}
		for i := range result.Edges {
			e := result.Edges[i]
			if err := bwEv.AddEdge(ctx, &e); err != nil {
				return nil, fmt.Errorf("persist reconciled edge: %w", err)
			}
		}
		if err := bwEv.Flush(ctx); err != nil {
			return nil, fmt.Errorf("flush reconciled edges: %w", err)
		}
		// Use the reconciler's unresolved list (may include gap ledger entries
		// from non-static providers in F.1+; for F.0 it equals allUnresolved).
		allUnresolved = result.Unresolved
		// The config provider re-derives its ledger from the *persisted* nodes,
		// which still read key_dynamic because the handshake resolution lives on
		// the pre-engine working copy. Retract again here, or the three sites
		// K.6 just linked come back reported as unresolvable.
		allUnresolved = linker.DropResolvedRefs(allUnresolved, handshakeResolved)
	}

	// ── Embed pass (S.0) ─────────────────────────────────────────────────────
	// Produce or update vector embeddings for every finalized node.
	// Runs after all linking and evidence reconciliation so the node set is
	// complete.  Skipped if opts.NoEmbed — the degradation reason is stamped
	// so the search layer can surface it in the response.
	if opts.NoEmbed {
		if err := store.SetMeta(ctx, "embed_status", "unavailable: embeddings skipped"); err != nil {
			return nil, err
		}
	} else {
		if embedErr := runEmbedPass(ctx, store, allNodes, allEdges, oldEmbedMeta, opts, logw); embedErr != nil {
			fmt.Fprintf(logw, "  Warning: embed pass: %v\n", embedErr)
			if serr := store.SetMeta(ctx, "embed_status", "unavailable: "+embedErr.Error()); serr != nil {
				return nil, serr
			}
		} else {
			if err := store.SetMeta(ctx, "embed_status", "ok"); err != nil {
				return nil, err
			}
		}
	}

	// ── Recall gauge ─────────────────────────────────────────────────────────
	// Persist the blind-spot ledger so `polyflow status` can report exactly
	// which references the graph is missing instead of failing silently.
	for i := range allUnresolved {
		if allUnresolved[i].Service == "" {
			// Parser-level refs already carry service via MatchToGraph; keep
			// a defensive default for linker records.
			allUnresolved[i].Service = "unknown"
		}
	}
	// Refs are recorded at parse time, when they really are unresolved; the
	// linkers above then resolve many of them. Drop those now, so the
	// "verify these N manually" footer names blind spots that are still blind
	// rather than sending an agent to read files the graph already connected.
	allUnresolved = graph.RetractResolvedRefs(allUnresolved, allNodes, allEdges)
	if err := store.ReplaceUnresolvedRefs(ctx, allUnresolved); err != nil {
		return nil, err
	}
	if err := store.SetMeta(ctx, "unresolved_refs", strconv.Itoa(len(allUnresolved))); err != nil {
		return nil, err
	}

	// B.0: persist unparsed file class ledger (always written, {} when clean).
	_ = store.SetMeta(ctx, "unparsed_files", serializeUnparsed(allUnparsedFiles))

	// Toolchain versions + coverage ledger (V.0 seams) + profile stamps (V.2).
	if tcJSON, err := json.Marshal(svcToolchainVersions); err == nil {
		_ = store.SetMeta(ctx, "toolchain_versions", string(tcJSON))
	}
	if tcProfJSON, err := json.Marshal(svcToolchainProfiles); err == nil {
		_ = store.SetMeta(ctx, "toolchain_profiles", string(tcProfJSON))
	}
	// SelectAll iterates a version map, so note order is stabilized here
	// before it reaches stored output (bug-class rule 2).
	sort.SliceStable(allToolchainNotes, func(i, j int) bool {
		a, b := allToolchainNotes[i], allToolchainNotes[j]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		if a.Tool != b.Tool {
			return a.Tool < b.Tool
		}
		return a.Note < b.Note
	})
	if len(allToolchainNotes) == 0 {
		_ = store.SetMeta(ctx, "toolchain_coverage", "[]")
	} else if tcCovJSON, err := json.Marshal(allToolchainNotes); err == nil {
		_ = store.SetMeta(ctx, "toolchain_coverage", string(tcCovJSON))
	}

	if err := store.SetMeta(ctx, "last_indexed", strconv.FormatInt(time.Now().Unix(), 10)); err != nil {
		return nil, err
	}
	if err := store.SetMeta(ctx, "schema_version", graph.SchemaVersion); err != nil {
		return nil, err
	}
	if err := store.SetMeta(ctx, "workspace_fingerprint", workspaceFingerprint); err != nil {
		return nil, err
	}
	if err := store.SetMeta(ctx, "cross_links", strconv.Itoa(stats.CrossLinks)); err != nil {
		return nil, err
	}
	if err := store.SetMeta(ctx, "contract_edges", strconv.Itoa(stats.ContractEdges)); err != nil {
		return nil, err
	}
	store.Close()

	// Atomic swap. The previous DB's WAL sidecar files must go too: the new
	// file was built with an in-memory journal and has none of its own, and
	// a reader pairing the renamed DB with the old -wal/-shm sees garbage
	// (empty tables, phantom cache misses).
	_ = os.Remove(finalDB + "-wal")
	_ = os.Remove(finalDB + "-shm")
	if err := os.Rename(tmpDB, finalDB); err != nil {
		return nil, fmt.Errorf("atomic swap: %w", err)
	}

	if s, err := graph.NewSQLiteStore(finalDB); err == nil {
		var statsErr error
		stats.Nodes, stats.Edges, statsErr = s.Stats(ctx)
		if statsErr != nil {
			fmt.Fprintf(logw, "  Warning: read final stats: %v\n", statsErr)
		}
		// D.2: append history row and prune to last 50 runs.
		histRows := aggregateUnresolvedHistory(allUnresolved, time.Now().Unix())
		if wErr := s.WriteUnresolvedHistory(ctx, histRows); wErr != nil {
			fmt.Fprintf(logw, "  Warning: write unresolved history: %v\n", wErr)
		} else {
			_ = s.PruneUnresolvedHistory(ctx, 50)
		}
		s.Close()
	} else {
		fmt.Fprintf(logw, "  Warning: open graph for stats: %v\n", err)
	}
	stats.Elapsed = time.Since(start)
	return stats, nil
}

// patternsFingerprint hashes the contents of every pattern YAML (built-in
// dir + workspace-registered extras) so pattern edits invalidate the
// no-change fast path.
func patternsFingerprint(dir string, extra []string) string {
	files, _ := filepath.Glob(filepath.Join(dir, "*", "*.yaml"))
	files = append(files, extra...)
	sort.Strings(files)
	h := sha256.New()
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		fmt.Fprintf(h, "%s:%x\n", f, sha256.Sum256(data))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// aggregateUnresolvedHistory counts refs by (service, kind) and returns
// history rows with the given run timestamp, sorted for determinism.
func aggregateUnresolvedHistory(refs []graph.UnresolvedRef, runAt int64) []graph.UnresolvedHistoryRow {
	type key struct{ service, kind string }
	counts := map[key]int{}
	for _, r := range refs {
		counts[key{r.Service, r.Kind}]++
	}
	rows := make([]graph.UnresolvedHistoryRow, 0, len(counts))
	for k, c := range counts {
		rows = append(rows, graph.UnresolvedHistoryRow{RunAt: runAt, Service: k.service, Kind: k.kind, Count: c})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Service != rows[j].Service {
			return rows[i].Service < rows[j].Service
		}
		return rows[i].Kind < rows[j].Kind
	})
	return rows
}

// countContractEdges counts distinct contract edges, and how many of those
// genuinely cross a service boundary.
//
// Both counts are deduplicated by edge ID, because the ID is the graph's
// primary key: an edge emitted twice (the same producer matched under two
// rules, or via two tiers) is one row after upsert. Counting the raw slice
// instead reports a number no query against the graph can reproduce — on the
// juniper fleet the engine emitted 109 cross-service edges that stored as
// 94. Reporting the pre-upsert figure would be a smaller version of the very
// overstatement this counter exists to fix.
//
// Cross-service requires both endpoints to resolve to a known, non-empty
// service, and the two to differ. The synthetic `unresolved:<svc>` target
// minted by the unknown_edge policy carries no Service, so an unmatched call is
// excluded — it names a service it failed to reach, which is the opposite of a
// link.
func countContractEdges(edges []graph.Edge, nodes []graph.Node) (total, cross int) {
	service := make(map[string]string, len(nodes))
	for i := range nodes {
		service[nodes[i].ID] = nodes[i].Service
	}
	seen := make(map[string]struct{}, len(edges))
	for _, e := range edges {
		if _, dup := seen[e.ID]; dup {
			continue
		}
		seen[e.ID] = struct{}{}
		total++
		from, to := service[e.From], service[e.To]
		if from != "" && to != "" && from != to {
			cross++
		}
	}
	return total, cross
}

// persistComposedRoutes copies the route-group composition from the contract
// engine's working copy back onto the persisted nodes.
//
// contract.EnrichRouteGroups deliberately works on a deep copy, because the
// engine needs composed paths only for key matching. But the stored graph is
// what agents and the UI read, and there a handler declared inside
// `v1.Group("/api/v1")` → `protected.Group("")` → `admin.Group("/admin")` was
// left as `GET /users/:id` with meta["router"]="admin" — a route that does not
// exist, and one that a search for `/api/v1/admin/users/:id` cannot find, since
// nodes_fts indexes label rather than meta.
//
// Only Label and meta["full_path"] are written back. meta["path"] stays raw on
// purpose: it is the input EnrichRouteGroups composes from, so persisting the
// composed form would double-compose on the next incremental re-index, when the
// cached node is fed through the pass again.
func persistComposedRoutes(
	ctx context.Context,
	bw *graph.BatchWriter,
	enriched []graph.Node,
	persisted []graph.Node,
) error {
	type composedRoute struct{ fullPath, label string }
	composed := make(map[string]composedRoute, len(enriched))
	for i := range enriched {
		n := &enriched[i]
		if fp := n.Meta["full_path"]; fp != "" {
			composed[n.ID] = composedRoute{fullPath: fp, label: n.Label}
		}
	}
	if len(composed) == 0 {
		return nil
	}
	for i := range persisted {
		n := &persisted[i]
		c, ok := composed[n.ID]
		if !ok {
			continue
		}
		if n.Meta == nil {
			n.Meta = make(map[string]string)
		}
		n.Meta["full_path"] = c.fullPath
		n.Label = c.label
		if err := bw.AddNode(ctx, n); err != nil {
			return err
		}
	}
	return bw.Flush(ctx)
}

// fingerprintLines hashes the sorted per-file hash lines of a service.
func fingerprintLines(lines []string) string {
	sorted := append([]string(nil), lines...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return hex.EncodeToString(sum[:])
}

// runEmbedPass builds the full embedding corpus (S.1: node cards + flow chains
// + doc chunks) and upserts vectors + FTS entries for entities whose content
// hash or embedder ID changed.
func runEmbedPass(
	ctx context.Context,
	store *graph.SQLiteStore,
	allNodes []graph.Node,
	allEdges []graph.Edge,
	oldEmbedMeta map[string]string,
	opts Options,
	logw io.Writer,
) error {
	// Resolve the embedder: use the caller-supplied one (S.3 upgrade ladder) or
	// fall back to the static default.
	var emb semantic.Embedder
	if opts.Embedder != nil {
		emb = opts.Embedder
	} else {
		var err error
		emb, err = semantic.DefaultStaticEmbedder()
		if err != nil {
			return fmt.Errorf("load static embedder: %w", err)
		}
	}
	embedderID := emb.ID()

	// Space-mixing guard: reject any table state where two different embedder IDs
	// coexist (interrupted prior run, manual DB edit).  The delta pass below will
	// re-embed everything that doesn't match the current embedder ID, so a clean
	// run always ends in a consistent state — this guard fires only on corruption.
	sem := semantic.NewStore(store.DB())
	if storedID, err := sem.CheckEmbedderConsistency(ctx); err != nil {
		return fmt.Errorf("embed pass aborted: %w; delete the DB and run `polyflow index --full` to recover", err)
	} else if storedID != "" && storedID != embedderID {
		fmt.Fprintf(logw, "  Embedder changed (%s → %s): re-embedding all entities\n", storedID, embedderID)
	}

	// ── Build corpus entities (S.1) ─────────────────────────────────────────
	// 1. Node cards — richer one-line card: label type service file [meta].
	nodeEntities := make([]semantic.Entity, len(allNodes))
	for i := range allNodes {
		nodeEntities[i] = semantic.BuildNodeCard(&allNodes[i])
	}

	// 2. Flow-chain documents — one per distinct chain from each entrypoint.
	idx := graph.NewAdjacencyIndex()
	for i := range allNodes {
		idx.AddNode(&allNodes[i])
	}
	for i := range allEdges {
		idx.AddEdge(&allEdges[i])
	}
	chainEntities := semantic.BuildFlowChains(idx)

	// 3. Doc chunks — markdown files + code doc-comments from service dirs.
	var svcPaths []semantic.ServicePath
	if opts.Config != nil {
		for _, svc := range opts.Config.Services {
			absPath, err := filepath.Abs(svc.Path)
			if err != nil {
				absPath = svc.Path
			}
			svcPaths = append(svcPaths, semantic.ServicePath{Path: absPath, Service: svc.Name})
		}
	}
	docEntities := semantic.BuildDocChunks(svcPaths, allNodes)

	// Combine all entities; dedupe by ID (node cards win over chain/doc on
	// collision — in practice IDs are namespaced and never collide).
	combined := make([]semantic.Entity, 0, len(nodeEntities)+len(chainEntities)+len(docEntities))
	combined = append(combined, nodeEntities...)
	combined = append(combined, chainEntities...)
	combined = append(combined, docEntities...)

	// ── Delta: entities whose content hash or embedder changed ───────────────
	var toEmbed []semantic.Entity
	for _, ent := range combined {
		key := embedderID + "\x00" + ent.ContentHash
		if oldEmbedMeta[ent.ID] == key {
			continue
		}
		toEmbed = append(toEmbed, ent)
	}

	fmt.Fprintf(logw, "  Embedding %d/%d entities (nodes=%d flows=%d docs=%d embedder=%s)\n",
		len(toEmbed), len(combined),
		len(nodeEntities), len(chainEntities), len(docEntities),
		embedderID)

	if len(toEmbed) == 0 {
		return nil
	}

	// Embed in batches of 256 to bound memory. The embed step is pure CPU and
	// the default StaticEmbedder is safe for concurrent use, so batches are
	// computed in parallel across the worker pool (the dominant cost of a cold
	// index — ~60% on a 3-repo fleet). Vectors are collected per batch and the
	// DB upsert is serialized afterward because a single *sql.DB write path is
	// not gained by concurrency and keeps the write deterministic.
	const batchSize = 256
	type batchResult struct {
		batch []semantic.Entity
		vecs  [][]float32
	}
	nBatches := (len(toEmbed) + batchSize - 1) / batchSize
	results := make([]batchResult, nBatches)

	workers := opts.Workers
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)
	for b := 0; b < nBatches; b++ {
		b := b
		start := b * batchSize
		end := start + batchSize
		if end > len(toEmbed) {
			end = len(toEmbed)
		}
		batch := toEmbed[start:end]
		g.Go(func() error {
			texts := make([]string, len(batch))
			for i, e := range batch {
				texts[i] = e.Text
			}
			vecs, embErr := emb.Embed(gctx, texts)
			if embErr != nil {
				return fmt.Errorf("embed batch [%d:%d]: %w", start, end, embErr)
			}
			results[b] = batchResult{batch: batch, vecs: vecs}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}

	// Flatten in batch order and upsert everything in a SINGLE transaction.
	// The previous code committed once per 256-entity batch (~150 fsync'd
	// commits on a large fleet); the embed-side win was being eaten by that
	// serial write amplification. One transaction = one commit = one fsync.
	allBatch := make([]semantic.Entity, 0, len(toEmbed))
	allVecs := make([][]float32, 0, len(toEmbed))
	for _, r := range results {
		allBatch = append(allBatch, r.batch...)
		allVecs = append(allVecs, r.vecs...)
	}
	if uErr := sem.BatchUpsertEmbeddings(ctx, allBatch, allVecs, embedderID); uErr != nil {
		return fmt.Errorf("upsert embeddings: %w", uErr)
	}
	return nil
}

// walkService collects parseable files under root, honoring exclude globs.
// It also counts skipped non-asset files by extension (or basename for
// extensionless files) for the B.0 unparsed-file-class ledger.
// CountFilesModifiedSince counts parser-recognized source files under root
// (respecting the same excludes as walkService) whose mtime is newer than
// since. It short-circuits once capN newer files are found, returning
// capped=true — so `polyflow status` can report staleness without a full stat
// of an already-obviously-stale tree. capN<=0 disables the cap.
func CountFilesModifiedSince(root string, excludes []string, since time.Time, capN int) (count int, capped bool) {
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
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
		if d.IsDir() || parser.ForFile(path) == nil {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		if info.ModTime().After(since) {
			count++
			if capN > 0 && count >= capN {
				capped = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	return count, capped
}

func walkService(root string, excludes []string) ([]string, map[string]int, error) {
	var files []string
	unparsed := map[string]int{}
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
		if parser.ForFile(path) != nil {
			files = append(files, path)
			return nil
		}
		key := unparsedKey(path)
		if !assetExts[key] {
			unparsed[key]++
		}
		return nil
	})
	return files, unparsed, err
}

// walkAllFiles returns every regular file under root (no exclude filtering,
// no asset skipping). Used by M.0 route synthesis which needs to see all files
// in the service directory — including unparsed ones like .svelte and .vue —
// because file-based routing is identified by filesystem path, not parse output.
func walkAllFiles(root string) []string {
	var files []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		// Skip node_modules and other common build artifacts to avoid noise.
		rel, relErr := filepath.Rel(root, p)
		if relErr == nil {
			seg := strings.SplitN(rel, string(filepath.Separator), 2)[0]
			switch seg {
			case "node_modules", ".git", "dist", ".next", ".nuxt", ".svelte-kit", "build", ".output":
				return filepath.SkipDir
			}
		}
		files = append(files, p)
		return nil
	})
	return files
}
