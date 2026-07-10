package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"

	pfcontext "github.com/lordsonvimal/polyflow/internal/context"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/impact"
	"github.com/lordsonvimal/polyflow/internal/indexer"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/lordsonvimal/polyflow/internal/server"
	"github.com/lordsonvimal/polyflow/internal/trace"
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
		traceCmd,
		configCmd,
		depsCmd,
		mcpCmd,
	)
	initDepsFlags()
	initIndexFlags()
	initServeFlags()
	initSearchFlags()
	initStatusFlags()
	initPatternsSubcmds()
	initContextFlags()
	initImpactFlags()
	initTraceFlags()
	initConfigSubcmds()
}

// ─── init ────────────────────────────────────────────────────────────────────

var (
	initInteractive bool
	initForce       bool
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a polyflow workspace (auto-discovers services)",
	RunE:  runInit,
}

func init() {
	initCmd.Flags().BoolVar(&initInteractive, "interactive", false, "prompt for each service instead of auto-discovering")
	initCmd.Flags().BoolVar(&initForce, "force", false, "overwrite an existing workspace.yaml without asking")
}

func runInit(cmd *cobra.Command, args []string) error {
	cfgPath := meta.ConfigFile
	if _, err := os.Stat(cfgPath); err == nil && !initForce {
		fmt.Printf("workspace.yaml already exists. Overwrite? [y/N]: ")
		var ans string
		fmt.Scanln(&ans)
		if strings.ToLower(ans) != "y" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	if !initInteractive {
		cfg, err := workspace.Discover(".")
		if err != nil {
			return fmt.Errorf("discover services: %w", err)
		}
		if len(cfg.Services) == 0 {
			return fmt.Errorf("no services found (no go.mod/go.work, package.json, or Gemfile) — use --interactive to add them manually")
		}
		fmt.Println("Discovered services:")
		for _, s := range cfg.Services {
			fw := ""
			if len(s.Frameworks) > 0 {
				fw = " [" + strings.Join(s.Frameworks, ", ") + "]"
			}
			fmt.Printf("  %-24s %-30s %s%s\n", s.Name, s.Path, s.Language, fw)
		}
		for _, l := range cfg.Links {
			fmt.Printf("  link: %s -> %s (via %s)\n", l.From, l.To, l.Via)
		}
		if err := workspace.Save(cfgPath, cfg); err != nil {
			return err
		}
		fmt.Printf("Created %s — edit it or use `polyflow config service` to adjust.\n", cfgPath)
		return nil
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
	indexCmd.Flags().BoolVar(&indexFull, "full", false, "force a full re-parse, ignoring the incremental cache")
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

	fmt.Println("Scanning services...")
	stats, err := indexer.Run(context.Background(), indexer.Options{
		Config:  cfg,
		Workers: indexWorkers,
		Full:    indexFull,
		Log:     os.Stdout,
		Progress: func(done, total int) {
			pct := 0
			if total > 0 {
				pct = done * 100 / total
			}
			fmt.Printf("\rIndexing [%s] %d%% (%d/%d files)  ", progressBar(pct), pct, done, total)
			if done == total {
				fmt.Println()
			}
		},
	})
	if err != nil {
		return err
	}

	fmt.Printf("\nDone. %d files indexed in %s (%d parsed, %d unchanged)\n",
		stats.TotalFiles, stats.Elapsed.Truncate(time.Millisecond), stats.ParsedFiles, stats.SkippedFiles)
	fmt.Printf("  Nodes: %d | Edges: %d | Links: %d cross-service\n", stats.Nodes, stats.Edges, stats.CrossLinks)
	if stats.ErrorFiles > 0 {
		fmt.Printf("  Errors: %d files (run `polyflow status --errors` for details)\n", stats.ErrorFiles)
	}
	return nil
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
	if err := watchDB(dbPath, func() { reloadDB(dbPath, srv) }); err != nil {
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
// calls onChange whenever the graph database is updated (polyflow index
// renames graph.db.tmp → graph.db, so directory-level Create/Write events
// on the db path are the signal).
func watchDB(dbPath string, onChange func()) error {
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
					onChange()
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
	searchKind   string
)

func initSearchFlags() {
	searchCmd.Flags().StringVar(&searchFormat, "format", "table", "output format: table or json")
	searchCmd.Flags().IntVar(&searchLimit, "limit", 20, "max results")
	searchCmd.Flags().StringVar(&searchKind, "kind", "", "restrict results: 'file' or a node type (function, variable, http_handler, …)")
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

	ctx := context.Background()

	// --kind file searches file paths and prints per-file aggregates.
	if searchKind == "file" {
		idx, err := store.BuildIndex(ctx)
		if err != nil {
			return err
		}
		files := graph.ListFiles(idx, args[0], searchLimit)
		if searchFormat == "json" {
			return json.NewEncoder(os.Stdout).Encode(files)
		}
		for _, f := range files {
			total := 0
			for _, c := range f.Counts {
				total += c
			}
			fmt.Printf("  %-60s %3d nodes [%s]\n", f.File, total, f.Service)
		}
		return nil
	}

	// Node-type kinds over-fetch then filter, so a sparse type still fills
	// the requested limit.
	fetchLimit := searchLimit
	if searchKind != "" {
		fetchLimit = searchLimit * 10
	}
	nodes, err := store.SearchNodes(ctx, args[0], fetchLimit)
	if err != nil {
		return err
	}
	if searchKind != "" {
		filtered := nodes[:0]
		for _, n := range nodes {
			if string(n.Type) == searchKind {
				filtered = append(filtered, n)
			}
		}
		nodes = filtered
		if len(nodes) > searchLimit {
			nodes = nodes[:searchLimit]
		}
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
	statusErrors     bool
	statusUnresolved bool
	statusWS         string
)

func initStatusFlags() {
	statusCmd.Flags().BoolVar(&statusErrors, "errors", false, "list files with parse errors")
	statusCmd.Flags().BoolVar(&statusUnresolved, "unresolved", false, "list references the graph could not resolve (blind spots)")
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

	// Recall gauge: the graph's known blind spots. Impact/context answers are
	// only trustworthy when this ledger is reviewed, not when it is empty by
	// omission.
	unresolvedRefs, err := store.ListUnresolvedRefs(ctx)
	if err != nil {
		return err
	}
	if len(unresolvedRefs) > 0 {
		byKind := map[string]int{}
		for _, u := range unresolvedRefs {
			byKind[u.Kind]++
		}
		var kindParts []string
		for _, kind := range []string{"call_ref", "import_ref"} {
			if byKind[kind] > 0 {
				kindParts = append(kindParts, fmt.Sprintf("%d %s", byKind[kind], kind))
			}
		}
		fmt.Printf("  Unresolved refs: %d (%s) — graph blind spots (--unresolved for details)\n",
			len(unresolvedRefs), strings.Join(kindParts, ", "))
	}

	if statusErrors {
		fmt.Println()
		for _, pe := range parseErrors {
			fmt.Printf("  PARTIAL  %s:%d    (%d error)\n", pe.FilePath, pe.FirstErrorLine, pe.ErrorCount)
		}
	}
	if statusUnresolved {
		fmt.Println()
		for _, u := range unresolvedRefs {
			fmt.Printf("  UNRESOLVED  %-10s %s:%d  %s (%s)\n", u.Service, u.File, u.Line, u.Name, u.Kind)
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
	contextTarget       string
	contextTask         string
	contextDepth        int
	contextFormat       string
	contextMaxTokens    int
	contextSummary      bool
	contextSnippetLines int
)

func initContextFlags() {
	contextCmd.Flags().StringVar(&contextTarget, "target", "", "search query to find root node (required)")
	contextCmd.Flags().StringVar(&contextTask, "task", "debug", "task type: impact, generate, debug, refactor")
	contextCmd.Flags().IntVar(&contextDepth, "depth", 5, "max traversal depth (0 = unlimited)")
	contextCmd.Flags().StringVar(&contextFormat, "format", "json", "output format: json or text")
	contextCmd.Flags().IntVar(&contextMaxTokens, "max-tokens", 0, "approximate token budget for output (0 = unlimited); over budget, per-node detail rolls up per file")
	contextCmd.Flags().BoolVar(&contextSummary, "summary", false, "emit the file-grouped rollup instead of per-node detail")
	contextCmd.Flags().IntVar(&contextSnippetLines, "snippet-lines", 0, "inline N source lines per node in detail output (0 = off)")
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

	unresolved, err := store.ListUnresolvedRefs(ctx)
	if err != nil {
		return err
	}
	result.AttachUnresolved(unresolved)
	result.InlineSnippets(".", contextSnippetLines)

	out := result.ApplyBudget(contextMaxTokens, contextSummary)
	if contextFormat == "text" {
		if s, ok := out.(*pfcontext.Summary); ok {
			return printContextSummaryText(s)
		}
		return printContextText(result)
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}

func printContextSummaryText(s *pfcontext.Summary) error {
	if s.Target == nil {
		fmt.Fprintln(os.Stdout, "Target: (not found)")
		return nil
	}
	fmt.Fprintf(os.Stdout, "Target: %s (%s) %s:%d\n\n", s.Target.Label, s.Target.Type, s.Target.File, s.Target.Line)

	if len(s.Files) > 0 {
		fmt.Fprintf(os.Stdout, "Files (%d nodes, %d edges):\n", s.TotalNodes, s.TotalEdges)
		for _, f := range s.Files {
			fmt.Fprintf(os.Stdout, "  %-10s depth %-2d %-60s %2d nodes via %s [%s]\n",
				f.Direction, f.MinDepth, f.File, f.Nodes, strings.Join(f.EdgeTypes, ","), f.Service)
		}
		fmt.Fprintln(os.Stdout)
	}

	if len(s.CrossService) > 0 {
		fmt.Fprintln(os.Stdout, "Cross-service:")
		for _, cs := range s.CrossService {
			fmt.Fprintf(os.Stdout, "  %s → %s → %s\n", cs.FromService, cs.Label, cs.ToService)
		}
		fmt.Fprintln(os.Stdout)
	}

	printUnresolvedText(s.Unresolved)
	if s.Budget != nil && s.Budget.Note != "" {
		fmt.Fprintf(os.Stdout, "(%s)\n", s.Budget.Note)
	}
	return nil
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
		fmt.Fprintln(os.Stdout)
	}

	printUnresolvedText(r.Unresolved)
	return nil
}

// printUnresolvedText renders the traversal-scoped blind spots appended to
// text-format query output.
func printUnresolvedText(refs []graph.UnresolvedRef) {
	if len(refs) == 0 {
		return
	}
	fmt.Fprintf(os.Stdout, "Unresolved references in traversed files (%d — verify manually, edges may be missing):\n", len(refs))
	for _, u := range refs {
		fmt.Fprintf(os.Stdout, "  %s:%d  %s (%s)\n", u.File, u.Line, u.Name, u.Kind)
	}
}

// ─── trace ───────────────────────────────────────────────────────────────────

var (
	traceRoot      string
	traceDirection string
	traceDepth     int
	traceFormat    string
)

func initTraceFlags() {
	traceCmd.Flags().StringVar(&traceRoot, "root", "", "search query to find the root node (required)")
	traceCmd.Flags().StringVar(&traceDirection, "direction", "forward", "trace direction: forward, backward, or both")
	traceCmd.Flags().IntVar(&traceDepth, "depth", 10, "max traversal depth (0 = unlimited)")
	traceCmd.Flags().StringVar(&traceFormat, "format", "text", "output format: json, text, or chain")
	_ = traceCmd.MarkFlagRequired("root")
}

var traceCmd = &cobra.Command{
	Use:   "trace",
	Short: "Trace multi-hop flows from a node (chain format prints linear A → B → C paths)",
	RunE:  runTrace,
}

func runTrace(cmd *cobra.Command, args []string) error {
	if traceDirection != "forward" && traceDirection != "backward" && traceDirection != "both" {
		return fmt.Errorf("unknown direction: %s (use: forward, backward, both)", traceDirection)
	}
	if traceFormat != "json" && traceFormat != "text" && traceFormat != "chain" {
		return fmt.Errorf("unknown format: %s (use: json, text, chain)", traceFormat)
	}

	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	ctx := context.Background()
	matches, err := store.SearchNodes(ctx, traceRoot, 5)
	if err != nil || len(matches) == 0 {
		return fmt.Errorf("node not found for query: %s", traceRoot)
	}
	root := matches[0]

	idx, err := store.BuildIndex(ctx)
	if err != nil {
		return err
	}

	result := trace.Run(idx, root.ID, traceDirection, traceDepth)
	if result == nil {
		return fmt.Errorf("root node %s not in graph", root.ID)
	}

	unresolved, err := store.ListUnresolvedRefs(ctx)
	if err != nil {
		return err
	}
	result.AttachUnresolved(unresolved)

	switch traceFormat {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(result)
	case "chain":
		for _, c := range result.Chains {
			fmt.Fprintln(os.Stdout, c.Text)
		}
		if result.Truncated {
			fmt.Fprintf(os.Stderr, "(truncated at %d chains)\n", trace.MaxChains)
		}
		if result.UnresolvedNote != "" {
			fmt.Fprintf(os.Stderr, "(%s)\n", result.UnresolvedNote)
		}
		return nil
	}
	return printTraceText(result)
}

func printTraceText(r *trace.Result) error {
	t := r.Root
	fmt.Fprintf(os.Stdout, "Trace: %s (%s) %s:%d\n", t.Label, t.Type, t.File, t.Line)
	fmt.Fprintf(os.Stdout, "Direction: %s   Depth: %d   Services: %s\n\n",
		r.Direction, r.Depth, strings.Join(r.Services, ", "))

	for _, h := range r.Nodes {
		indent := strings.Repeat("  ", h.Depth)
		boundary := ""
		if h.CrossService {
			boundary = fmt.Sprintf(" ‖%s‖", h.Service)
		}
		version := ""
		if v, ok := h.NodeMeta["resolved_version"]; ok {
			version = fmt.Sprintf(" (%s@%s)", h.NodeMeta["package"], v)
		}
		fmt.Fprintf(os.Stdout, "%s-[%s]->%s %s%s  %s:%d\n",
			indent, h.EdgeType, boundary, h.Label, version, h.File, h.Line)
	}

	if len(r.Chains) > 0 {
		fmt.Fprintf(os.Stdout, "\nChains (%d):\n", len(r.Chains))
		for _, c := range r.Chains {
			fmt.Fprintf(os.Stdout, "  %s\n", c.Text)
		}
	}
	if r.Truncated {
		fmt.Fprintf(os.Stdout, "(truncated at %d chains)\n", trace.MaxChains)
	}
	if len(r.Unresolved) > 0 {
		fmt.Fprintln(os.Stdout)
		printUnresolvedText(r.Unresolved)
	}
	return nil
}

// ─── impact ──────────────────────────────────────────────────────────────────

var (
	impactTarget       string
	impactDepth        int
	impactService      string
	impactFormat       string
	impactFile         string
	impactDirection    string
	impactMaxTokens    int
	impactSummary      bool
	impactSnippetLines int
)

func initImpactFlags() {
	impactCmd.Flags().StringVar(&impactTarget, "target", "", "search query for the target node")
	impactCmd.Flags().StringVar(&impactFile, "file", "", "file path: report impact at file granularity")
	impactCmd.Flags().StringVar(&impactDirection, "direction", "backward", "with --file: forward, backward or both")
	impactCmd.Flags().IntVar(&impactDepth, "depth", 10, "max traversal depth (0 = unlimited)")
	impactCmd.Flags().StringVar(&impactService, "service", "", "filter results to a specific service")
	impactCmd.Flags().StringVar(&impactFormat, "format", "text", "output format: text or json")
	impactCmd.Flags().IntVar(&impactMaxTokens, "max-tokens", 0, "approximate token budget for output (0 = unlimited); over budget, per-node detail rolls up per file")
	impactCmd.Flags().BoolVar(&impactSummary, "summary", false, "emit the file-grouped rollup instead of per-node detail")
	impactCmd.Flags().IntVar(&impactSnippetLines, "snippet-lines", 0, "inline N source lines per node in detail output (0 = off)")
	impactCmd.MarkFlagsOneRequired("target", "file")
	impactCmd.MarkFlagsMutuallyExclusive("target", "file")
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

	if impactFile != "" {
		idx, err := store.BuildIndex(ctx)
		if err != nil {
			return err
		}
		out, err := impact.BuildFile(idx, impactService, impactFile, impactDirection, impactDepth)
		if err != nil {
			return err
		}
		unresolved, err := store.ListUnresolvedRefs(ctx)
		if err != nil {
			return err
		}
		out.AttachUnresolved(unresolved)
		out.ApplyBudget(impactMaxTokens)
		if impactFormat == "json" {
			return json.NewEncoder(os.Stdout).Encode(out)
		}
		fmt.Fprintf(os.Stdout, "Impact of %s (%s, direction=%s):\n\n", out.File, out.Service, out.Direction)
		for _, e := range out.Impacted {
			fmt.Fprintf(os.Stdout, "  depth %-2d %-60s %2d nodes via %s [%s]\n",
				e.MinDepth, e.File, e.Nodes, strings.Join(e.EdgeTypes, ","), e.Service)
		}
		fmt.Fprintf(os.Stdout, "\nTotal: %d files impacted\n", len(out.Impacted))
		if len(out.Unresolved) > 0 {
			fmt.Fprintln(os.Stdout)
			printUnresolvedText(out.Unresolved)
		}
		if out.Budget != nil && out.Budget.Note != "" {
			fmt.Fprintf(os.Stdout, "(%s)\n", out.Budget.Note)
		}
		return nil
	}

	matches, err := store.SearchNodes(ctx, impactTarget, 5)
	if err != nil || len(matches) == 0 {
		return fmt.Errorf("node not found for query: %s", impactTarget)
	}
	root := matches[0]

	idx, err := store.BuildIndex(ctx)
	if err != nil {
		return err
	}

	out := impact.Build(idx, root, impactDepth, impactService)

	unresolved, err := store.ListUnresolvedRefs(ctx)
	if err != nil {
		return err
	}
	out.AttachUnresolved(unresolved)
	out.InlineSnippets(".", impactSnippetLines)

	budgeted := out.ApplyBudget(impactMaxTokens, impactSummary)
	if impactFormat == "json" {
		return json.NewEncoder(os.Stdout).Encode(budgeted)
	}
	if s, ok := budgeted.(*impact.Summary); ok {
		return printImpactSummaryText(s)
	}
	return printImpactText(out)
}

func printImpactSummaryText(s *impact.Summary) error {
	t := s.Target
	fmt.Fprintf(os.Stdout, "Impact analysis for: %s (%s) %s:%d\n\n", t.Label, t.Type, t.File, t.Line)

	if len(s.Files) > 0 {
		fmt.Fprintln(os.Stdout, "Files in blast radius:")
		for _, f := range s.Files {
			fmt.Fprintf(os.Stdout, "  depth %-2d %-60s %2d nodes via %s [%s]\n",
				f.MinDepth, f.File, f.Nodes, strings.Join(f.EdgeTypes, ","), f.Service)
		}
		fmt.Fprintln(os.Stdout)
	}

	if len(s.EntryPoints) > 0 {
		fmt.Fprintln(os.Stdout, "Entry points (no callers):")
		for _, ep := range s.EntryPoints {
			fmt.Fprintf(os.Stdout, "  %s\n", ep)
		}
		fmt.Fprintln(os.Stdout)
	}

	if len(s.ServicesAffected) > 0 {
		fmt.Fprintf(os.Stdout, "Services affected: %s\n", strings.Join(s.ServicesAffected, ", "))
	}
	for _, xs := range s.CrossServiceTriggers {
		fmt.Fprintf(os.Stdout, "Cross-service triggers: %s (%d http_call edges)\n", xs.FromService, xs.EdgeCount)
	}

	fmt.Fprintf(os.Stdout, "\nTotal: %d nodes in blast radius\n", s.TotalCallers)
	if len(s.Unresolved) > 0 {
		fmt.Fprintln(os.Stdout)
		printUnresolvedText(s.Unresolved)
	}
	if s.Budget != nil && s.Budget.Note != "" {
		fmt.Fprintf(os.Stdout, "(%s)\n", s.Budget.Note)
	}
	return nil
}

func printImpactText(out *impact.Result) error {
	t := out.Target
	fmt.Fprintf(os.Stdout, "Impact analysis for: %s (%s) %s:%d\n\n", t.Label, t.Type, t.File, t.Line)

	// Split callers into direct (depth 1) and indirect (depth > 1).
	var direct, indirect []impact.Caller
	for _, c := range out.Callers {
		if c.Depth == 1 {
			direct = append(direct, c)
		} else {
			indirect = append(indirect, c)
		}
	}

	if len(direct) > 0 {
		fmt.Fprintln(os.Stdout, "Direct callers (depth 1):")
		for _, c := range direct {
			fmt.Fprintf(os.Stdout, "  %-40s %s:%d\n",
				fmt.Sprintf("%s  [%s]", c.Label, c.EdgeType), c.File, c.Line)
		}
		fmt.Fprintln(os.Stdout)
	}

	if len(indirect) > 0 {
		fmt.Fprintf(os.Stdout, "Indirect callers (depth 2-%d):\n", out.Depth)
		for _, c := range indirect {
			fmt.Fprintf(os.Stdout, "  %-40s %s:%d\n",
				fmt.Sprintf("%s  [%s]", c.Label, c.EdgeType), c.File, c.Line)
		}
		fmt.Fprintln(os.Stdout)
	}

	if len(out.EntryPoints) > 0 {
		fmt.Fprintln(os.Stdout, "Entry points (no callers):")
		for _, ep := range out.EntryPoints {
			fmt.Fprintf(os.Stdout, "  %-40s %s:%d\n", ep.Label, ep.File, ep.Line)
		}
		fmt.Fprintln(os.Stdout)
	}

	if len(out.ServicesAffected) > 0 {
		fmt.Fprintf(os.Stdout, "Services affected: %s\n", strings.Join(out.ServicesAffected, ", "))
	}

	for _, xs := range out.CrossServiceTriggers {
		fmt.Fprintf(os.Stdout, "Cross-service triggers: %s (%d http_call edges)\n", xs.FromService, xs.EdgeCount)
	}

	fmt.Fprintf(os.Stdout, "\nTotal: %d nodes in blast radius\n", out.TotalCallers)
	if len(out.Unresolved) > 0 {
		fmt.Fprintln(os.Stdout)
		printUnresolvedText(out.Unresolved)
	}
	return nil
}

// ─── deps ────────────────────────────────────────────────────────────────────

var (
	depsService string
	depsFormat  string
)

func initDepsFlags() {
	depsCmd.Flags().StringVar(&depsService, "service", "", "filter to one service")
	depsCmd.Flags().StringVar(&depsFormat, "format", "table", "output format: table or json")
}

var depsCmd = &cobra.Command{
	Use:   "deps",
	Short: "List resolved dependency versions per service",
	RunE:  runDeps,
}

func runDeps(cmd *cobra.Command, args []string) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	list, err := store.ListDependencies(context.Background(), depsService)
	if err != nil {
		return err
	}

	if depsFormat == "json" {
		return json.NewEncoder(os.Stdout).Encode(list)
	}
	for _, d := range list {
		fmt.Printf("  %-20s %-10s %-45s %-15s %s\n", d.Service, d.Ecosystem, d.Name, d.Version, d.Kind)
	}
	return nil
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
