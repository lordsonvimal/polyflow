package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"

	"github.com/lordsonvimal/polyflow/internal/graph"
	pfcontext "github.com/lordsonvimal/polyflow/internal/context"
	"github.com/lordsonvimal/polyflow/internal/linker"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/lordsonvimal/polyflow/internal/server"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:     meta.Name,
	Short:   meta.Description,
	Version: meta.Version,
}

func init() {
	rootCmd.AddCommand(
		initCmd,
		indexCmd,
		serveCmd,
		searchCmd,
		statusCmd,
		patternsCmd,
		contextCmd,
		impactCmd,
		configCmd,
	)
	initIndexFlags()
	initServeFlags()
	initSearchFlags()
	initStatusFlags()
	initPatternsSubcmds()
	initContextFlags()
	initImpactFlags()
	initConfigSubcmds()
}

// ─── init ────────────────────────────────────────────────────────────────────

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a polyflow workspace",
	RunE:  runInit,
}

func runInit(cmd *cobra.Command, args []string) error {
	cfgPath := meta.ConfigFile
	if _, err := os.Stat(cfgPath); err == nil {
		fmt.Printf("workspace.yaml already exists. Overwrite? [y/N]: ")
		var ans string
		fmt.Scanln(&ans)
		if strings.ToLower(ans) != "y" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	scanner := bufio.NewScanner(os.Stdin)
	prompt := func(msg string) string {
		fmt.Printf("%s", msg)
		scanner.Scan()
		return strings.TrimSpace(scanner.Text())
	}

	cfg := &workspace.WorkspaceConfig{Version: "1"}
	cfg.Name = prompt("Workspace name: ")

	for {
		fmt.Println("Add a service:")
		svc := workspace.Service{}
		svc.Name = prompt("  Name: ")
		svc.Path = prompt("  Path: ")

		// Auto-detect language and frameworks from the service directory.
		hints, _ := workspace.DetectFrameworks(svc.Path)
		detectedLang := ""
		var detectedFW []string
		for _, h := range hints {
			if detectedLang == "" {
				detectedLang = h.Language
			}
			if h.Name != "go-module" && h.Name != "node" && h.Name != "bundler" && h.Name != "pip" && h.Name != "cargo" {
				detectedFW = append(detectedFW, h.Name)
			}
		}

		langPrompt := "  Language (go/javascript/ruby/typescript): "
		if detectedLang != "" {
			langPrompt = fmt.Sprintf("  Language [detected: %s]: ", detectedLang)
		}
		svc.Language = prompt(langPrompt)
		if svc.Language == "" {
			svc.Language = detectedLang
		}

		fwDefault := ""
		if len(detectedFW) > 0 {
			fwDefault = strings.Join(detectedFW, ", ")
		}
		fwPrompt := "  Frameworks (optional, comma-separated): "
		if fwDefault != "" {
			fwPrompt = fmt.Sprintf("  Frameworks [detected: %s]: ", fwDefault)
		}
		fw := prompt(fwPrompt)
		if fw == "" {
			fw = fwDefault
		}
		if fw != "" {
			for _, f := range strings.Split(fw, ",") {
				svc.Frameworks = append(svc.Frameworks, strings.TrimSpace(f))
			}
		}
		cfg.Services = append(cfg.Services, svc)

		more := prompt("Add another service? [y/N]: ")
		if strings.ToLower(more) != "y" {
			break
		}
	}

	if err := workspace.Save(cfgPath, cfg); err != nil {
		return err
	}
	fmt.Printf("Created %s\n", cfgPath)
	return nil
}

// ─── index ───────────────────────────────────────────────────────────────────

var (
	indexWorkspace string
	indexWorkers   int
	indexFull      bool
)

func initIndexFlags() {
	indexCmd.Flags().StringVar(&indexWorkspace, "workspace", meta.ConfigFile, "path to workspace.yaml")
	indexCmd.Flags().IntVar(&indexWorkers, "workers", runtime.GOMAXPROCS(0), "parser worker pool size")
	indexCmd.Flags().BoolVar(&indexFull, "full", true, "full re-index (v1 always does full)")
}

var indexCmd = &cobra.Command{
	Use:   "index",
	Short: "Parse and index all services in the workspace",
	RunE:  runIndex,
}

func runIndex(cmd *cobra.Command, args []string) error {
	cfg, err := workspace.Load(indexWorkspace)
	if err != nil {
		return err
	}

	dbDir := meta.DBDir
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dbDir, err)
	}

	tmpDB := filepath.Join(dbDir, "graph.db.tmp")
	_ = os.Remove(tmpDB)

	store, err := graph.NewSQLiteStore(tmpDB)
	if err != nil {
		return fmt.Errorf("open tmp store: %w", err)
	}

	reg, err := patterns.DefaultRegistry("patterns/")
	if err != nil {
		return fmt.Errorf("load default patterns: %w", err)
	}
	for _, p := range cfg.Patterns {
		pf, err := patterns.LoadFile(p)
		if err != nil {
			return fmt.Errorf("load custom pattern %s: %w", p, err)
		}
		reg.RegisterFile(pf)
	}
	matcher := patterns.NewTreeSitterMatcher(reg)

	fmt.Println("Scanning services...")
	type serviceFiles struct {
		svc   workspace.Service
		files []string
	}
	// Collect other service paths so each service excludes files owned by another service.
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
		// Build extra excludes: any other service path that is a sub-directory of this one.
		var extraExcludes []string
		for i, other := range svcPaths {
			if i == idx {
				continue
			}
			rel, err := filepath.Rel(absSvcPath, other)
			if err == nil && !strings.HasPrefix(rel, "..") {
				extraExcludes = append(extraExcludes, rel+"/**")
			}
		}
		excludes := append(cfg.Index.Exclude, extraExcludes...)
		files, err := walkService(svc.Path, excludes)
		if err != nil {
			return fmt.Errorf("walk %s: %w", svc.Name, err)
		}
		fmt.Printf("  %s: %d files (%s)\n", svc.Name, len(files), svc.Language)
		allSvcFiles = append(allSvcFiles, serviceFiles{svc, files})
	}

	totalFiles := 0
	for _, sf := range allSvcFiles {
		totalFiles += len(sf.files)
	}

	ctx := context.Background()
	var processed atomic.Int64
	start := time.Now()

	// Progress goroutine — cancelled via progressCtx when indexing completes.
	// progressDone is closed once the goroutine has printed the final line.
	progressCtx, stopProgress := context.WithCancel(ctx)
	defer stopProgress()
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				n := int(processed.Load())
				pct := 0
				if totalFiles > 0 {
					pct = n * 100 / totalFiles
				}
				bar := progressBar(pct)
				fmt.Printf("\rIndexing [%s] %d%% (%d/%d files)  ", bar, pct, n, totalFiles)
			case <-progressCtx.Done():
				// Print final 100% line before exiting.
				bar := progressBar(100)
				fmt.Printf("\rIndexing [%s] 100%% (%d/%d files)  ", bar, totalFiles, totalFiles)
				return
			}
		}
	}()

	var allNodes []graph.Node
	var allEdges []graph.Edge
	var totalErrors int
	var semanticWarnings []string

	bw := graph.NewBatchWriter(store)

	for _, sf := range allSvcFiles {
		pool := parser.NewWorkerPool(indexWorkers, matcher, sf.svc.Name)
		for result := range pool.Run(sf.files) {
			processed.Add(1)
			if result.Err != nil {
				totalErrors++
				_ = store.UpsertParseError(ctx, &graph.ParseError{
					FilePath:   result.File,
					Service:    sf.svc.Name,
					ErrorCount: 1,
					IndexedAt:  time.Now().Unix(),
				})
				continue
			}
			for i := range result.Nodes {
				n := result.Nodes[i]
				if err := bw.AddNode(ctx, &n); err != nil {
					return err
				}
				allNodes = append(allNodes, n)
			}
			for i := range result.Edges {
				e := result.Edges[i]
				if err := bw.AddEdge(ctx, &e); err != nil {
					return err
				}
				allEdges = append(allEdges, e)
			}
		}
	}

	// Stop progress display: cancel the goroutine, wait for it to print 100%, then newline.
	stopProgress()
	<-progressDone
	fmt.Println()

	// Flush all tree-sitter nodes+edges before the semantic pass so FK constraints
	// are satisfied when semantic edges reference those nodes.
	if err := bw.Flush(ctx); err != nil {
		return err
	}

	// Build a set of all node IDs now committed to the DB so we can filter
	// semantic edges — only emit edges where both endpoints already exist.
	knownNodeIDs := make(map[string]bool, len(allNodes))
	for _, n := range allNodes {
		knownNodeIDs[n.ID] = true
	}

	// Semantic pass: run go/packages + SSA for Go services.
	// Adds type-resolved call edges on top of tree-sitter nodes.
	// Falls back gracefully if the service has build errors.
	fset := token.NewFileSet()
	for _, sf := range allSvcFiles {
		analyzer := parser.ServiceAnalyzerFor(sf.svc.Language)
		if analyzer == nil {
			continue
		}
		absSvcPath, err := filepath.Abs(sf.svc.Path)
		if err != nil {
			absSvcPath = sf.svc.Path
		}
		fmt.Printf("  Semantic analysis: %s...\n", sf.svc.Name)
		sem := analyzer.AnalyzeService(absSvcPath, sf.svc.Name, fset, knownNodeIDs)
		if sem.Warning != "" {
			fmt.Fprintf(os.Stderr, "  Warning: %s\n", sem.Warning)
			semanticWarnings = append(semanticWarnings, sem.Warning)
			continue
		}
		bwSem := graph.NewBatchWriter(store)
		written := 0
		for i := range sem.Edges {
			e := sem.Edges[i]
			if err := bwSem.AddEdge(ctx, &e); err != nil {
				return err
			}
			allEdges = append(allEdges, e)
			written++
		}
		if err := bwSem.Flush(ctx); err != nil {
			return err
		}
		fmt.Printf("  Semantic analysis: %s — %d call edges added\n", sf.svc.Name, written)
	}

	// Store semantic warnings in DB meta so the web UI can surface them.
	if len(semanticWarnings) > 0 {
		warningsJSON, _ := json.Marshal(semanticWarnings)
		_ = store.SetMeta(ctx, "semantic_warnings", string(warningsJSON))
	} else {
		_ = store.SetMeta(ctx, "semantic_warnings", "[]")
	}

	// JS/TS component + import-aware linking pass.
	// Redirects renders edges from JSX usage proxy nodes to actual declaration
	// nodes, and resolves cross-file function calls through import statements.
	{
		svcFiles := make(map[string][]string, len(allSvcFiles))
		for _, sf := range allSvcFiles {
			svcFiles[sf.svc.Name] = sf.files
		}
		jsLinker := linker.NewJSLinker()
		jsEdges, removeIDs := jsLinker.LinkJS(allNodes, allEdges, svcFiles)

		// Write new JS edges.
		bwJS := graph.NewBatchWriter(store)
		for i := range jsEdges {
			e := jsEdges[i]
			if err := bwJS.AddEdge(ctx, &e); err != nil {
				return err
			}
			allEdges = append(allEdges, e)
		}
		if err := bwJS.Flush(ctx); err != nil {
			return err
		}

		// Remove proxy component usage nodes and their orphaned edges.
		if len(removeIDs) > 0 {
			if err := store.DeleteNodes(ctx, removeIDs); err != nil {
				return fmt.Errorf("delete proxy nodes: %w", err)
			}
			// Remove from in-memory slice so later passes don't see them.
			filtered := allNodes[:0]
			for _, n := range allNodes {
				if !removeIDs[n.ID] {
					filtered = append(filtered, n)
				}
			}
			allNodes = filtered
		}
	}

	// Route → handler linking: emit calls edges from HTTP route nodes to their
	// handler function nodes (resolved by name across the service).
	{
		routeEdges := linker.LinkRouteHandlers(allNodes)
		bwRoute := graph.NewBatchWriter(store)
		for i := range routeEdges {
			e := routeEdges[i]
			if err := bwRoute.AddEdge(ctx, &e); err != nil {
				return err
			}
			allEdges = append(allEdges, e)
		}
		if err := bwRoute.Flush(ctx); err != nil {
			return err
		}
	}

	// Cross-service linking
	hintedNodes := linker.ApplyHints(cfg.Links, allNodes, allEdges)
	l := linker.New(cfg)
	crossEdges, err := l.Link(hintedNodes, allEdges)
	if err != nil {
		return fmt.Errorf("link: %w", err)
	}

	// Insert synthetic unresolved nodes before writing cross-service edges.
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
		return err
	}

	bw3 := graph.NewBatchWriter(store)
	for i := range crossEdges {
		e := crossEdges[i]
		if err := bw3.AddEdge(ctx, &e); err != nil {
			return err
		}
	}
	if err := bw3.Flush(ctx); err != nil {
		return err
	}

	if err := store.SetMeta(ctx, "last_indexed", strconv.FormatInt(time.Now().Unix(), 10)); err != nil {
		return err
	}

	store.Close()

	finalDB := filepath.Join(dbDir, meta.DBFile)
	if err := os.Rename(tmpDB, finalDB); err != nil {
		return fmt.Errorf("atomic swap: %w", err)
	}

	nodeCount, edgeCount, _ := func() (int, int, error) {
		s, err := graph.NewSQLiteStore(finalDB)
		if err != nil {
			return 0, 0, err
		}
		defer s.Close()
		return s.Stats(ctx)
	}()

	elapsed := time.Since(start).Truncate(time.Millisecond)
	fmt.Printf("\nDone. %d files indexed in %s\n", totalFiles, elapsed)
	fmt.Printf("  Nodes: %d | Edges: %d | Links: %d cross-service\n", nodeCount, edgeCount, len(crossEdges))
	if totalErrors > 0 {
		fmt.Printf("  Errors: %d files (run `polyflow status --errors` for details)\n", totalErrors)
	}
	return nil
}

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

func progressBar(pct int) string {
	width := 12
	filled := pct * width / 100
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	return bar
}

// ─── serve ───────────────────────────────────────────────────────────────────

var (
	servePort   int
	serveHost   string
	serveNoOpen bool
	serveWS     string
	serveDev    bool
)

func initServeFlags() {
	serveCmd.Flags().IntVar(&servePort, "port", 0, "override port")
	serveCmd.Flags().StringVar(&serveHost, "host", "127.0.0.1", "host to listen on")
	serveCmd.Flags().BoolVar(&serveNoOpen, "no-open", false, "skip browser launch")
	serveCmd.Flags().StringVar(&serveWS, "workspace", meta.ConfigFile, "path to workspace.yaml")
	serveCmd.Flags().BoolVar(&serveDev, "dev", false, "enable CORS for Vite dev server (port 5173)")
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the polyflow web UI and API server",
	RunE:  runServe,
}

func runServe(cmd *cobra.Command, args []string) error {
	cfg, err := workspace.Load(serveWS)
	if err != nil {
		return err
	}

	port := servePort
	if port == 0 {
		port = cfg.EffectivePort()
	}

	dbPath := filepath.Join(meta.DBDir, meta.DBFile)
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return fmt.Errorf("graph database not found at %s — run `polyflow index` first", dbPath)
	}

	store, err := graph.NewSQLiteStore(dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

	ctx := context.Background()
	idx, err := store.BuildIndex(ctx)
	if err != nil {
		return fmt.Errorf("build index: %w", err)
	}

	var srv *server.Server
	if serveDev {
		srv = server.NewDev(store, idx)
	} else {
		srv = server.New(store, idx)
	}

	// Watch graph.db for atomic swaps (polyflow index renames graph.db.tmp → graph.db).
	// On a Write or Create event, reopen the store, rebuild the index, and push a
	// graph_updated SSE event to all connected browser clients.
	if err := watchDB(dbPath, srv); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not start DB watcher: %v\n", err)
	}

	if serveHost == "0.0.0.0" {
		fmt.Fprintln(os.Stderr, "Warning: server exposed on all interfaces (0.0.0.0)")
	}

	url := fmt.Sprintf("http://%s:%d", serveHost, port)
	if serveHost == "0.0.0.0" || serveHost == "" {
		url = fmt.Sprintf("http://localhost:%d", port)
	}

	if !serveNoOpen {
		go openBrowser(url)
	}

	return srv.StartOn(serveHost, port)
}

// watchDB starts a background goroutine that watches dbPath for changes and
// calls srv.Reload whenever the graph database is updated.
func watchDB(dbPath string, srv *server.Server) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create watcher: %w", err)
	}
	// Watch the directory — fsnotify on macOS/Linux misses rename events on the
	// file itself, but directory-level events fire reliably for atomic renames.
	if err := watcher.Add(filepath.Dir(dbPath)); err != nil {
		watcher.Close()
		return fmt.Errorf("watch dir: %w", err)
	}
	go func() {
		defer watcher.Close()
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if filepath.Clean(event.Name) != filepath.Clean(dbPath) {
					continue
				}
				if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
					reloadDB(dbPath, srv)
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				fmt.Fprintf(os.Stderr, "DB watcher error: %v\n", err)
			}
		}
	}()
	return nil
}

func reloadDB(dbPath string, srv *server.Server) {
	newStore, err := graph.NewSQLiteStore(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reload: open store: %v\n", err)
		return
	}
	newIdx, err := newStore.BuildIndex(context.Background())
	if err != nil {
		newStore.Close()
		fmt.Fprintf(os.Stderr, "reload: build index: %v\n", err)
		return
	}
	srv.Reload(newIdx)
	fmt.Println("Graph reloaded.")
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// ─── search ──────────────────────────────────────────────────────────────────

var (
	searchFormat string
	searchLimit  int
)

func initSearchFlags() {
	searchCmd.Flags().StringVar(&searchFormat, "format", "table", "output format: table or json")
	searchCmd.Flags().IntVar(&searchLimit, "limit", 20, "max results")
}

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search the index for nodes matching query",
	Args:  cobra.ExactArgs(1),
	RunE:  runSearch,
}

func runSearch(cmd *cobra.Command, args []string) error {
	dbPath := filepath.Join(meta.DBDir, meta.DBFile)
	store, err := graph.NewSQLiteStore(dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	nodes, err := store.SearchNodes(context.Background(), args[0], searchLimit)
	if err != nil {
		return err
	}

	if searchFormat == "json" {
		return json.NewEncoder(os.Stdout).Encode(nodes)
	}

	for _, n := range nodes {
		fmt.Printf("  %-10s %-30s %-40s [%s]\n",
			strings.ToUpper(string(n.Type)),
			n.Label,
			fmt.Sprintf("%s:%d", n.File, n.Line),
			n.Service,
		)
	}
	return nil
}

// ─── status ──────────────────────────────────────────────────────────────────

var (
	statusErrors bool
	statusWS     string
)

func initStatusFlags() {
	statusCmd.Flags().BoolVar(&statusErrors, "errors", false, "list files with parse errors")
	statusCmd.Flags().StringVar(&statusWS, "workspace", meta.ConfigFile, "path to workspace.yaml")
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show index statistics",
	RunE:  runStatus,
}

func runStatus(cmd *cobra.Command, args []string) error {
	cfg, err := workspace.Load(statusWS)
	if err != nil {
		return err
	}

	dbPath := filepath.Join(meta.DBDir, meta.DBFile)
	store, err := graph.NewSQLiteStore(dbPath)
	if err != nil {
		return fmt.Errorf("open store (run `polyflow index` first): %w", err)
	}
	defer store.Close()

	ctx := context.Background()
	nodeCount, edgeCount, err := store.Stats(ctx)
	if err != nil {
		return err
	}

	lastIndexed := "never"
	if ts, err := store.GetMeta(ctx, "last_indexed"); err == nil {
		if unix, err := strconv.ParseInt(ts, 10, 64); err == nil {
			t := time.Unix(unix, 0)
			ago := time.Since(t).Round(time.Second)
			lastIndexed = fmt.Sprintf("%s (%s ago)", t.Format("2006-01-02 15:04:05"), ago)
		}
	}

	parseErrors, err := store.ListParseErrors(ctx)
	if err != nil {
		return err
	}

	// Count languages
	langCount := make(map[string]int)
	for _, svc := range cfg.Services {
		langCount[svc.Language]++
	}
	var langParts []string
	for lang, count := range langCount {
		langParts = append(langParts, fmt.Sprintf("%d %s", count, titleCase(lang)))
	}

	fmt.Printf("  Workspace: %s\n", cfg.Name)
	fmt.Printf("  Services: %d (%s)\n", len(cfg.Services), strings.Join(langParts, ", "))
	fmt.Printf("  Last indexed: %s\n", lastIndexed)
	fmt.Printf("  Files: N/A | Nodes: %d | Edges: %d\n", nodeCount, edgeCount)
	if len(parseErrors) > 0 {
		fmt.Printf("  Parse errors: %d files (--errors for details)\n", len(parseErrors))
	}

	if statusErrors {
		fmt.Println()
		for _, pe := range parseErrors {
			fmt.Printf("  PARTIAL  %s:%d    (%d error)\n", pe.FilePath, pe.FirstErrorLine, pe.ErrorCount)
		}
	}
	return nil
}

// ─── patterns ────────────────────────────────────────────────────────────────

var patternsCmd = &cobra.Command{
	Use:   "patterns",
	Short: "List or manage loaded patterns",
}

var patternsListLanguage string

func initPatternsSubcmds() {
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all loaded patterns",
		RunE:  runPatternsList,
	}
	listCmd.Flags().StringVar(&patternsListLanguage, "language", "", "filter by language")

	addCmd := &cobra.Command{
		Use:   "add <file>",
		Short: "Register a custom pattern file",
		Args:  cobra.ExactArgs(1),
		RunE:  runPatternsAdd,
	}

	patternsCmd.AddCommand(listCmd, addCmd)
}

func runPatternsList(cmd *cobra.Command, args []string) error {
	reg, err := patterns.DefaultRegistry("patterns/")
	if err != nil {
		return err
	}

	langs := reg.Languages()
	for _, lang := range langs {
		if patternsListLanguage != "" && lang != patternsListLanguage {
			continue
		}
		for _, p := range reg.List(lang) {
			fmt.Printf("  %-20s %-12s %s\n", p.Name, lang, p.Extract.NodeType)
		}
	}
	return nil
}

func runPatternsAdd(cmd *cobra.Command, args []string) error {
	path := args[0]
	if _, err := patterns.LoadFile(path); err != nil {
		return fmt.Errorf("invalid pattern file: %w", err)
	}

	cfg, err := workspace.Load(meta.ConfigFile)
	if err != nil {
		return err
	}
	cfg.Patterns = append(cfg.Patterns, path)
	if err := workspace.Save(meta.ConfigFile, cfg); err != nil {
		return err
	}
	fmt.Printf("Added pattern file %s to workspace.yaml\n", path)
	return nil
}

// ─── context ─────────────────────────────────────────────────────────────────

var (
	contextTarget string
	contextTask   string
	contextDepth  int
	contextFormat string
)

func initContextFlags() {
	contextCmd.Flags().StringVar(&contextTarget, "target", "", "search query to find root node (required)")
	contextCmd.Flags().StringVar(&contextTask, "task", "debug", "task type: impact, generate, debug, refactor")
	contextCmd.Flags().IntVar(&contextDepth, "depth", 5, "max traversal depth (0 = unlimited)")
	contextCmd.Flags().StringVar(&contextFormat, "format", "json", "output format: json or text")
	_ = contextCmd.MarkFlagRequired("target")
}

var contextCmd = &cobra.Command{
	Use:   "context",
	Short: "Show the call context around a node",
	RunE:  runContext,
}

func runContext(cmd *cobra.Command, args []string) error {
	if contextTask != "impact" && contextTask != "generate" && contextTask != "debug" && contextTask != "refactor" {
		return fmt.Errorf("unknown task type: %s (use: impact, generate, debug, refactor)", contextTask)
	}

	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	ctx := context.Background()
	nodes, err := store.SearchNodes(ctx, contextTarget, 5)
	if err != nil || len(nodes) == 0 {
		return fmt.Errorf("node not found for query: %s", contextTarget)
	}
	root := nodes[0]

	idx, err := store.BuildIndex(ctx)
	if err != nil {
		return err
	}

	result := pfcontext.Build(idx, root.ID, contextTask, contextDepth)

	if contextFormat == "text" {
		return printContextText(result)
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func printContextText(r *pfcontext.Result) error {
	if r.Target == nil {
		fmt.Fprintln(os.Stdout, "Target: (not found)")
		return nil
	}
	fmt.Fprintf(os.Stdout, "Target: %s (%s) %s:%d\n\n", r.Target.Label, r.Target.Type, r.Target.File, r.Target.Line)

	if len(r.Upstream) > 0 {
		fmt.Fprintln(os.Stdout, "Upstream (callers):")
		for _, n := range r.Upstream {
			fmt.Fprintf(os.Stdout, "  %-40s %s:%d\n",
				fmt.Sprintf("%s [%s]", n.Label, n.EdgeType), n.File, n.Line)
		}
		fmt.Fprintln(os.Stdout)
	}

	if len(r.Downstream) > 0 {
		fmt.Fprintf(os.Stdout, "Downstream (callees, depth %d):\n", r.Depth)
		for _, n := range r.Downstream {
			indent := strings.Repeat("  ", n.Depth)
			fmt.Fprintf(os.Stdout, "%s%-40s %s:%d\n",
				indent, fmt.Sprintf("%s [%s]", n.Label, n.EdgeType), n.File, n.Line)
		}
		fmt.Fprintln(os.Stdout)
	}

	if len(r.CrossService) > 0 {
		fmt.Fprintln(os.Stdout, "Cross-service:")
		for _, cs := range r.CrossService {
			fmt.Fprintf(os.Stdout, "  %s → %s → %s\n", cs.FromService, cs.Label, cs.ToService)
		}
	}

	return nil
}

// ─── impact ──────────────────────────────────────────────────────────────────

var impactTarget string

func initImpactFlags() {
	impactCmd.Flags().StringVar(&impactTarget, "target", "", "search query (required)")
	_ = impactCmd.MarkFlagRequired("target")
}

var impactCmd = &cobra.Command{
	Use:   "impact",
	Short: "Show what is impacted by changes to a node",
	RunE:  runImpact,
}

func runImpact(cmd *cobra.Command, args []string) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	ctx := context.Background()
	nodes, err := store.SearchNodes(ctx, impactTarget, 1)
	if err != nil || len(nodes) == 0 {
		return fmt.Errorf("node not found for query: %s", impactTarget)
	}
	root := nodes[0]

	idx, err := store.BuildIndex(ctx)
	if err != nil {
		return err
	}

	results := graph.Ancestors(idx, root.ID, 0)

	type affected struct {
		ID      string `json:"id"`
		Service string `json:"service"`
		File    string `json:"file"`
		Line    int    `json:"line"`
		Type    string `json:"type"`
	}
	byService := make(map[string][]affected)
	for _, r := range results {
		a := affected{
			ID:      r.Node.ID,
			Service: r.Node.Service,
			File:    r.Node.File,
			Line:    r.Node.Line,
			Type:    string(r.Node.Type),
		}
		byService[r.Node.Service] = append(byService[r.Node.Service], a)
	}

	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"root":       root,
		"by_service": byService,
	})
}

// ─── config ──────────────────────────────────────────────────────────────────

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "View or edit polyflow configuration",
}

func initConfigSubcmds() {
	showCmd := &cobra.Command{
		Use:   "show",
		Short: "Pretty-print current workspace.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(meta.ConfigFile)
			if err != nil {
				return err
			}
			fmt.Print(string(data))
			return nil
		},
	}

	setCmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a settings key",
		Args:  cobra.ExactArgs(2),
		RunE:  runConfigSet,
	}

	// service subcommands
	svcCmd := &cobra.Command{Use: "service", Short: "Manage services"}
	var svcAddName, svcAddPath, svcAddLang, svcAddFrameworks string
	svcAddCmd := &cobra.Command{
		Use:   "add",
		Short: "Add a service",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := workspace.Load(meta.ConfigFile)
			if err != nil {
				return err
			}
			svc := workspace.Service{Name: svcAddName, Path: svcAddPath, Language: svcAddLang}
			if svcAddFrameworks != "" {
				for _, f := range strings.Split(svcAddFrameworks, ",") {
					svc.Frameworks = append(svc.Frameworks, strings.TrimSpace(f))
				}
			}
			cfg.Services = append(cfg.Services, svc)
			if err := workspace.Save(meta.ConfigFile, cfg); err != nil {
				return err
			}
			fmt.Printf("Added service %s\n", svcAddName)
			return nil
		},
	}
	svcAddCmd.Flags().StringVar(&svcAddName, "name", "", "service name")
	svcAddCmd.Flags().StringVar(&svcAddPath, "path", "", "service path")
	svcAddCmd.Flags().StringVar(&svcAddLang, "language", "", "service language")
	svcAddCmd.Flags().StringVar(&svcAddFrameworks, "frameworks", "", "comma-separated frameworks")

	var svcRemoveName string
	svcRemoveCmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove a service by name",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := workspace.Load(meta.ConfigFile)
			if err != nil {
				return err
			}
			var svcs []workspace.Service
			for _, s := range cfg.Services {
				if s.Name != svcRemoveName {
					svcs = append(svcs, s)
				}
			}
			cfg.Services = svcs
			if err := workspace.Save(meta.ConfigFile, cfg); err != nil {
				return err
			}
			fmt.Printf("Removed service %s\n", svcRemoveName)
			return nil
		},
	}
	svcRemoveCmd.Flags().StringVar(&svcRemoveName, "name", "", "service name to remove")

	svcListCmd := &cobra.Command{
		Use:   "list",
		Short: "List all services",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := workspace.Load(meta.ConfigFile)
			if err != nil {
				return err
			}
			for _, s := range cfg.Services {
				fmt.Printf("  %-20s %-30s %s\n", s.Name, s.Path, s.Language)
			}
			return nil
		},
	}
	svcCmd.AddCommand(svcAddCmd, svcRemoveCmd, svcListCmd)

	// link subcommands
	linkCmd := &cobra.Command{Use: "link", Short: "Manage links"}
	var linkFrom, linkTo, linkVia, linkBaseURL string
	linkAddCmd := &cobra.Command{
		Use:   "add",
		Short: "Add a link",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := workspace.Load(meta.ConfigFile)
			if err != nil {
				return err
			}
			cfg.Links = append(cfg.Links, workspace.Link{From: linkFrom, To: linkTo, Via: linkVia, BaseURL: linkBaseURL})
			if err := workspace.Save(meta.ConfigFile, cfg); err != nil {
				return err
			}
			fmt.Printf("Added link %s -> %s\n", linkFrom, linkTo)
			return nil
		},
	}
	linkAddCmd.Flags().StringVar(&linkFrom, "from", "", "source service")
	linkAddCmd.Flags().StringVar(&linkTo, "to", "", "target service")
	linkAddCmd.Flags().StringVar(&linkVia, "via", "", "via hint")
	linkAddCmd.Flags().StringVar(&linkBaseURL, "base-url", "", "base URL to strip")

	var linkRemoveFrom, linkRemoveTo string
	linkRemoveCmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove a link by from+to",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := workspace.Load(meta.ConfigFile)
			if err != nil {
				return err
			}
			var links []workspace.Link
			for _, l := range cfg.Links {
				if l.From != linkRemoveFrom || l.To != linkRemoveTo {
					links = append(links, l)
				}
			}
			cfg.Links = links
			if err := workspace.Save(meta.ConfigFile, cfg); err != nil {
				return err
			}
			fmt.Printf("Removed link %s -> %s\n", linkRemoveFrom, linkRemoveTo)
			return nil
		},
	}
	linkRemoveCmd.Flags().StringVar(&linkRemoveFrom, "from", "", "source service")
	linkRemoveCmd.Flags().StringVar(&linkRemoveTo, "to", "", "target service")
	linkCmd.AddCommand(linkAddCmd, linkRemoveCmd)

	// exclude subcommands
	excludeCmd := &cobra.Command{Use: "exclude", Short: "Manage index exclude patterns"}
	excludeAddCmd := &cobra.Command{
		Use:   "add <pattern>",
		Short: "Add a glob to index.exclude",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := workspace.Load(meta.ConfigFile)
			if err != nil {
				return err
			}
			cfg.Index.Exclude = append(cfg.Index.Exclude, args[0])
			if err := workspace.Save(meta.ConfigFile, cfg); err != nil {
				return err
			}
			fmt.Printf("Added exclude pattern: %s\n", args[0])
			return nil
		},
	}
	excludeRemoveCmd := &cobra.Command{
		Use:   "remove <pattern>",
		Short: "Remove a glob from index.exclude",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := workspace.Load(meta.ConfigFile)
			if err != nil {
				return err
			}
			var excludes []string
			for _, e := range cfg.Index.Exclude {
				if e != args[0] {
					excludes = append(excludes, e)
				}
			}
			cfg.Index.Exclude = excludes
			if err := workspace.Save(meta.ConfigFile, cfg); err != nil {
				return err
			}
			fmt.Printf("Removed exclude pattern: %s\n", args[0])
			return nil
		},
	}
	excludeCmd.AddCommand(excludeAddCmd, excludeRemoveCmd)

	configCmd.AddCommand(showCmd, setCmd, svcCmd, linkCmd, excludeCmd)
}

func runConfigSet(cmd *cobra.Command, args []string) error {
	cfg, err := workspace.Load(meta.ConfigFile)
	if err != nil {
		return err
	}
	key, val := args[0], args[1]
	switch key {
	case "port":
		n, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("invalid port: %s", val)
		}
		cfg.Settings.Port = n
	case "snippet_lines":
		n, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("invalid snippet_lines: %s", val)
		}
		cfg.Settings.SnippetLines = n
	case "default_layout":
		cfg.Settings.DefaultLayout = val
	case "default_depth":
		n, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("invalid default_depth: %s", val)
		}
		cfg.Settings.DefaultDepth = n
	default:
		return fmt.Errorf("unknown setting: %s (supported: port, snippet_lines, default_layout, default_depth)", key)
	}
	if err := workspace.Save(meta.ConfigFile, cfg); err != nil {
		return err
	}
	fmt.Printf("Set %s = %s\n", key, val)
	return nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func openStore() (*graph.SQLiteStore, error) {
	dbPath := filepath.Join(meta.DBDir, meta.DBFile)
	store, err := graph.NewSQLiteStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open store (run `polyflow index` first): %w", err)
	}
	return store, nil
}
