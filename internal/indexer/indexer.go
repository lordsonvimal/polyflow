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
	oldSemantic := map[string][4]string{} // service → (fingerprint, nodesJSON, edgesJSON, referencedJSON)
	oldFingerprint := ""
	// oldEmbedMeta: entity_id → "embedder_id\x00content_hash" for hash-gating.
	oldEmbedMeta := map[string]string{}
	if !opts.Full {
		if _, err := os.Stat(finalDB); err == nil {
			if oldStore, err := graph.NewSQLiteStore(finalDB); err == nil {
				// Cached results from an older data-model generation are
				// unusable — ignore them all and re-index from scratch.
				ver, _ := oldStore.GetMeta(ctx, "schema_version")
				if ver == graph.SchemaVersion {
					if hs, err := oldStore.ListFileHashes(ctx); err == nil {
						oldHashes = hs
					}
					for _, svc := range cfg.Services {
						if fp, nodes, edges, referenced, err := oldStore.GetSemanticCache(ctx, svc.Name); err == nil && fp != "" {
							oldSemantic[svc.Name] = [4]string{fp, nodes, edges, referenced}
						}
					}
					if fp, err := oldStore.GetMeta(ctx, "workspace_fingerprint"); err == nil {
						oldFingerprint = fp
					}
					// Load embedding metadata for incremental re-embed gating.
					if metas, err := oldStore.ListEmbeddingMeta(ctx); err == nil {
						for _, m := range metas {
							oldEmbedMeta[m.EntityID] = m.EmbedderID + "\x00" + m.ContentHash
						}
					}
				} else {
					fmt.Fprintf(logw, "  Schema version changed (%q → %q) — full re-index\n", ver, graph.SchemaVersion)
				}
				oldStore.Close()
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
		files, err := walkService(svc.Path, excludes)
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", svc.Name, err)
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
	now := time.Now().Unix()
	hashes := map[string]string{}     // file → content hash
	svcHashLines := map[string][]string{} // semantic cache key input
	var fpLines []string
	for _, sf := range allSvcFiles {
		for _, file := range sf.files {
			data, err := os.ReadFile(file)
			if err != nil {
				continue // recorded as an error during the parse loop
			}
			sum := sha256.Sum256(data)
			h := hex.EncodeToString(sum[:])
			hashes[file] = h
			svcHashLines[sf.svc.Name] = append(svcHashLines[sf.svc.Name], file+":"+h)
			fpLines = append(fpLines, sf.svc.Name+":"+file+":"+h)
		}
	}
	cfgJSON, _ := json.Marshal(cfg)
	fpLines = append(fpLines, "config:"+string(cfgJSON))
	fpLines = append(fpLines, "patterns:"+patternsFingerprint(opts.PatternsDir, cfg.Patterns))
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
			return stats, nil
		}
		// Fall through to a full build if the previous DB cannot be opened.
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

		router := sidecar.NewRouter(sidecarMgr, tcReg, sf.svc.Name, svcToolchainVersions[sf.svc.Name])
		pool := parser.NewWorkerPool(opts.Workers, matcher, sf.svc.Name)
		pool.SetRoute(router.ParserFor)
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

	if err := writeEdges(linker.LinkRouteHandlers(allNodes)); err != nil {
		return nil, err
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
	if err := writeEdges(linker.LinkSSEClients(allNodes)); err != nil {
		return nil, err
	}

	// Broker hint linking (via: rabbitmq + exchange).
	{
		hintNodes, hintEdges := linker.LinkBrokerHints(cfg.Links, allNodes)
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
	// G.7 pre-engine enrichment: resolve alias/instance bindings and one-hop
	// wrapper functions. Alias binding nodes (NodeTypeVariable with alias_name
	// or instance_name meta) are removed from the working copy; their info feeds
	// the alias table used to rewrite call nodes before Engine.Link.
	enrichedNodes, aliasUnresolved := contract.EnrichAliases(enrichedNodes)
	allUnresolved = append(allUnresolved, aliasUnresolved...)
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
	stats.CrossLinks = len(contractResult.Edges)

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
	if err := store.UpsertUnresolvedRefs(ctx, allUnresolved); err != nil {
		return nil, err
	}
	if err := store.SetMeta(ctx, "unresolved_refs", strconv.Itoa(len(allUnresolved))); err != nil {
		return nil, err
	}

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
	emb, err := semantic.DefaultStaticEmbedder()
	if err != nil {
		return fmt.Errorf("load static embedder: %w", err)
	}
	embedderID := emb.ID()

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

	// Embed in batches of 256 to bound memory.
	const batchSize = 256
	sem := semantic.NewStore(store.DB())
	for start := 0; start < len(toEmbed); start += batchSize {
		end := start + batchSize
		if end > len(toEmbed) {
			end = len(toEmbed)
		}
		batch := toEmbed[start:end]
		texts := make([]string, len(batch))
		for i, e := range batch {
			texts[i] = e.Text
		}
		vecs, embErr := emb.Embed(ctx, texts)
		if embErr != nil {
			return fmt.Errorf("embed batch [%d:%d]: %w", start, end, embErr)
		}
		if uErr := sem.BatchUpsertEmbeddings(ctx, batch, vecs, embedderID); uErr != nil {
			return fmt.Errorf("upsert embeddings [%d:%d]: %w", start, end, uErr)
		}
	}
	return nil
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
