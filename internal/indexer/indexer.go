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
	Config *workspace.WorkspaceConfig
	// ServiceFilter restricts indexing to these service names. nil (the
	// default) means every service in Config.Services — today's behavior,
	// unchanged. Used by single-service `polyflow index <service>` (FR.2) to
	// point DBDir at that service's own DB without touching the others.
	ServiceFilter []string
	DBDir         string // default: meta.DBDir
	PatternsDir   string // default: "" → built-in patterns embedded in the binary; set to load from disk instead
	ContractsDir  string // default: "" → no workspace-custom rules; set to the workspace root to load <dir>/contracts/*.yaml
	Workers       int    // default: GOMAXPROCS
	Full          bool   // force full re-parse, ignoring the incremental cache
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

	// services is the subset of cfg.Services this run actually parses/links.
	// nil ServiceFilter (the default) means every service — today's behavior,
	// unchanged. svcPaths (below) stays built from the FULL cfg.Services list
	// regardless of the filter, since a filtered service's directory still
	// needs to exclude any other service nested inside it.
	services := cfg.Services
	if len(opts.ServiceFilter) > 0 {
		want := make(map[string]bool, len(opts.ServiceFilter))
		for _, name := range opts.ServiceFilter {
			want[name] = true
		}
		services = nil
		for _, svc := range cfg.Services {
			if want[svc.Name] {
				services = append(services, svc)
			}
		}
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
		for _, svc := range services {
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
	svcToolchainVersions := make(map[string]map[toolchain.Tool]string, len(services))
	var allToolchainNotes []toolchain.CoverageNote
	// svcToolchainProfiles: service → tool → profile stamp (V.2 labeling —
	// which rule variant / sidecar backend interpreted each tool).
	type profileStamp struct {
		Profile  string `json:"profile"`
		Version  string `json:"version"`
		Inferred bool   `json:"inferred,omitempty"`
	}
	svcToolchainProfiles := make(map[string]map[string]profileStamp, len(services))

	// B.0: unparsed-file-class ledger — counts per (service, extension).
	allUnparsedFiles := map[string]map[string]int{}

	// workspaceRoot bounds resolveNode's upward package.json search to this
	// workspace: a service whose path is a language subdirectory (e.g. a
	// Rails repo's `js` service pointed at ./app/javascript, with
	// package.json only at the repo root) otherwise resolves zero npm deps,
	// silently deactivating every package-version-gated pattern for that
	// service.
	workspaceRoot, err := filepath.Abs(".")
	if err != nil {
		workspaceRoot = ""
	}

	var allSvcFiles []serviceFiles
	for _, svc := range services {
		absSvcPath, _ := filepath.Abs(svc.Path)
		var extraExcludes []string
		for _, other := range svcPaths {
			if other == absSvcPath {
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

		svcDeps, err := deps.Resolve(absSvcPath, workspaceRoot)
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
		// Package/version-gated per this service (see stampReflectDispatched):
		// method declarations land here as NodeTypeMethod nodes straight out
		// of the tree-sitter matcher (functions.yaml's method_decl and
		// friends), not out of the later Go SSA semantic pass, so this must
		// be applied in this loop, against these nodes.
		svcReg := reg.ForService(sf.deps)
		reflectMethods := svcReg.AllReflectDispatchedMethods()
		reflectPathPrefixes := svcReg.AllReflectDispatchedPathPrefixes()

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
					stampReflectDispatched(nodes, reflectMethods, reflectPathPrefixes)
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
			stampReflectDispatched(result.Nodes, reflectMethods, reflectPathPrefixes)
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
	// Extracted (FR.5a) into an ordered, addressable list — see link_passes.go
	// for every pass's body. This loop is the only thing that changed: each
	// pass runs in the exact order it used to execute inline, threading state
	// through linkPipelineState instead of closures over Run()'s locals.
	linkState := &linkPipelineState{
		ctx:           ctx,
		store:         store,
		bw:            bw,
		cfg:           cfg,
		opts:          opts,
		stats:         stats,
		allSvcFiles:   allSvcFiles,
		allNodes:      allNodes,
		allEdges:      allEdges,
		allUnresolved: allUnresolved,
	}
	for _, pass := range buildLinkPasses(linkState) {
		if err := pass.exec(); err != nil {
			return nil, fmt.Errorf("link pass %s: %w", pass.name, err)
		}
	}
	allNodes = linkState.allNodes
	allEdges = linkState.allEdges
	allUnresolved = linkState.allUnresolved
	handshakeResolved := linkState.handshakeResolved

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
			if !classifyRoot(n, incoming, referencedIDs) {
				continue
			}
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

// stampRootKind nil-inits n.Meta if needed and sets root_kind.
func stampRootKind(n *graph.Node, kind string) {
	if n.Meta == nil {
		n.Meta = map[string]string{}
	}
	n.Meta["root_kind"] = kind
}

// classifyRoot stamps n's root_kind meta (entrypoint / callback /
// unreachable) if n is a function/method root worth classifying, and reports
// whether it did. incoming is keyed by node ID, true for any inbound edge
// except EdgeTypeContains (structural, not a reference — see caller).
func classifyRoot(n *graph.Node, incoming map[string]bool, referencedIDs map[string]bool) bool {
	if n.Type != graph.NodeTypeFunction && n.Type != graph.NodeTypeMethod {
		return false
	}
	// main/init/(module) are unconditional runtime entry points, checked
	// before the incoming-edge skip below. A synthetic (module) wrapper picks
	// up ordinary non-Contains edges from unrelated passes (e.g.
	// LinkTemplScripts' `imports` edge from the templ component whose
	// <script src> loads the file) that are not a "something calls this"
	// signal the way EdgeTypeCalls/Spawns is. Gating this case on incoming
	// would silently skip stamping root_kind whenever such an edge exists —
	// and deadcode, which only recognizes Calls/Spawns as a caller, would
	// then flag the module wrapper of every <script>-included JS file as
	// dead code (verified: exactly this shape on the juniper corpus).
	if n.Label == "main" || n.Label == "init" || n.Label == "(module)" {
		stampRootKind(n, "entrypoint")
		return true
	}
	// A Rake task/namespace block, or a Rails callback-registration block
	// (DC.18/DC.28, ruby_variables.go's dslBlockNode) is invoked externally
	// (`rake task_name`) or by the framework (the action/model event firing),
	// never by an in-repo call site — the same unconditional entry-point
	// status as main/init, not one contingent on incoming edges the way
	// (script) below is.
	if n.Meta["kind"] == "rake_block" || n.Meta["kind"] == "callback_block" {
		stampRootKind(n, "entrypoint")
		return true
	}
	// SH0/SH1: a shell script's synthetic (script) scope is entrypoint
	// status CONDITIONAL on nothing else invoking it — the opposite polarity
	// from (module) above. A JS module always executes on load regardless of
	// whether anything imports it (browser/bundler entry), but a shell
	// script that another indexed script sources or execs is not itself a
	// root; only a script with zero inbound `calls` (via=exec, SH1) edges is
	// one, exactly like an un-called Go main.
	if n.Label == "(script)" {
		if incoming[n.ID] {
			return false
		}
		stampRootKind(n, "entrypoint")
		return true
	}
	if incoming[n.ID] {
		return false
	}
	kind := "unreachable"
	switch {
	case referencedIDs[n.ID]:
		kind = "callback"
	// object_method_pair (`{ onProceed: function(){...} }`) is only ever
	// reachable via a property/variable read, never a literal call using the
	// property name as an identifier — the exact shape referencedIDs already
	// captures for Go, but referencedIDs itself only comes from the Go SSA
	// analyzer (parser.ServiceAnalyzerFor returns nil for JS/Ruby/Python), so
	// JS callback-shaped object-literal values had no equivalent signal and
	// fell to the "unreachable" default. Safe to treat unconditionally as
	// callback: the `pair` grammar node this pattern matches only ever occurs
	// inside an object literal, never a class body, so it can't misclassify
	// a genuinely dead class method the way object_method_shorthand's shared
	// method_definition query would.
	case n.Meta["pattern"] == "object_method_pair":
		kind = "callback"
	// A `get`/`set` accessor (`get value() {...}`) is invoked by property
	// read/write syntax (`obj.value`), never a call expression — no pattern
	// in the graph can produce a `calls` edge to one, so without this it's
	// zero-caller by construction regardless of real property access.
	case n.Meta["js_accessor"] == "true":
		kind = "callback"
	// A dispatch-table entry (`{ test: /^dep:/i, parse: parseDepPrefix }`)
	// names an already-declared function BY REFERENCE as a property value —
	// only ever invoked indirectly (`entry.parse(x)`), never a literal call
	// site naming it. Stamped by matcher.go's Pass 3c (object_value_ref).
	case n.Meta[graph.MetaReferencedAsValue] == "true":
		kind = "callback"
	}
	stampRootKind(n, kind)
	return true
}

// stampReflectDispatched sets graph.MetaReflectDispatched on every
// NodeTypeMethod node in nodes whose label is in reflectMethods[n.Language].
// Gated to NodeTypeMethod: a free function sharing one of these names (e.g. a
// package-level "String" helper) implements no interface and must stay a
// real deadcode candidate — only a method (which has a receiver, and so is
// eligible for interface satisfaction) can be reflect-dispatched this way.
//
// reflectMethods and pathPrefixes are keyed by language (Registry.
// AllReflectDispatchedMethods/AllReflectDispatchedPathPrefixes), not one
// language for the whole call: a single *service* is routinely polyglot (a
// Rails app with a React frontend is one service tagged `language: ruby`
// that also indexes `.tsx` files), so keying the lookup off a single
// service-level language string would silently never apply a
// javascript-gated file's exclusions (e.g. patterns/javascript/react.yaml,
// DC.4b) to that service's JS/TS nodes.
//
// pathPrefixes additionally restricts specific method names (e.g.
// ActiveRecord migrations' change/up/down, Tier DC.2) to nodes whose file
// path contains the declared prefix — those names are common English words
// with no gem-specific spelling, so an unscoped name match would sweep in
// unrelated methods anywhere in the service. A method name absent from
// pathPrefixes has no such restriction (the gorm.io/devise hook names are
// distinctive enough on their own).
func stampReflectDispatched(nodes []graph.Node, reflectMethods map[string]map[string]bool, pathPrefixes map[string]map[string]string) {
	if len(reflectMethods) == 0 {
		return
	}
	for i := range nodes {
		n := &nodes[i]
		// Every JS/TS pattern file (react.yaml included) declares one bucket,
		// `language: javascript` — a single tree-sitter grammar family covers
		// both — but a .ts/.tsx node's own Language field is stamped
		// "typescript" (tsLanguage, internal/parser/javascript.go). Without
		// this fallback, every TS/TSX file in a mixed-stack service would
		// silently miss every javascript-gated reflect_dispatched_methods
		// entry, which is most of them.
		lang := n.Language
		methods := reflectMethods[lang]
		if len(methods) == 0 && lang == "typescript" {
			lang = "javascript"
			methods = reflectMethods[lang]
		}
		if len(methods) == 0 || !methods[n.Label] {
			continue
		}
		if prefix, ok := pathPrefixes[lang][n.Label]; ok && !strings.Contains(n.File, prefix) {
			continue
		}
		eligible := n.Type == graph.NodeTypeMethod
		if n.Language == "ruby" {
			// Ruby has no method/function split by receiver the way Go does —
			// every instance method a class defines comes out of
			// extractRubyVariables as graph.NodeTypeFunction (ruby_variables.go),
			// so the Go-only "only a receiver method can satisfy an interface"
			// restriction above would blanket-exclude every Devise override
			// hook (DV.3) despite the package/version gate being satisfied.
			eligible = eligible || n.Type == graph.NodeTypeFunction
		}
		if n.Language == "javascript" || n.Language == "typescript" {
			// Same gap, same fix, different language (DC.4b): extractJSVariables
			// mints every class method as graph.NodeTypeFunction too — there is
			// no NodeTypeMethod in JS/TS at all — so without this, a
			// package:react-gated name like componentDidMount would never be
			// eligible and the YAML gate would silently do nothing.
			eligible = eligible || n.Type == graph.NodeTypeFunction
		}
		if !eligible {
			continue
		}
		if n.Meta == nil {
			n.Meta = map[string]string{}
		}
		n.Meta[graph.MetaReflectDispatched] = "true"
	}
}

// isMinifiedAsset reports whether path is a minified/bundled vendor asset
// (e.g. datastar.min.js) rather than authored source. These ship pre-built,
// often as a single line of mangled identifiers with no stable call edges
// back to application code; parsing them as ordinary source floods the graph
// (and the deadcode scan in particular) with thousands of phantom
// zero-caller nodes for library internals nobody wrote or calls directly.
func isMinifiedAsset(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return strings.HasSuffix(base, ".min.js") || strings.HasSuffix(base, ".min.css")
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
		if isMinifiedAsset(path) {
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
