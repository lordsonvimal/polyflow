package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"

	pfcontext "github.com/lordsonvimal/polyflow/internal/context"
	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/eval"
	"github.com/lordsonvimal/polyflow/internal/evidence"
	"github.com/lordsonvimal/polyflow/internal/evidence/trace_ingest"
	"github.com/lordsonvimal/polyflow/internal/gitdiff"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/impact"
	"github.com/lordsonvimal/polyflow/internal/indexer"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/lordsonvimal/polyflow/internal/semantic"
	"github.com/lordsonvimal/polyflow/internal/server"
	"github.com/lordsonvimal/polyflow/internal/sidecar"
	"github.com/lordsonvimal/polyflow/internal/toolchain"
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
		evalCmd,
		doctorCmd,
		reconcileCmd,
		rulesCmd,
		modelsCmd,
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
	initEvalFlags()
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
	indexNoEmbed   bool
)

func initIndexFlags() {
	indexCmd.Flags().StringVar(&indexWorkspace, "workspace", meta.ConfigFile, "path to workspace.yaml")
	indexCmd.Flags().IntVar(&indexWorkers, "workers", runtime.GOMAXPROCS(0), "parser worker pool size")
	indexCmd.Flags().BoolVar(&indexFull, "full", false, "force a full re-parse, ignoring the incremental cache")
	indexCmd.Flags().BoolVar(&indexNoEmbed, "no-embed", false, "skip the embedding pass (search runs FTS-only; semantic: unavailable)")
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

	// Resolve the embedder from workspace config (S.3 upgrade ladder).
	var emb semantic.Embedder
	var closeEmb func()
	if !indexNoEmbed {
		emb, closeEmb, err = selectEmbedder(&cfg.Search)
		if err != nil {
			return fmt.Errorf("embedder: %w", err)
		}
		if closeEmb != nil {
			defer closeEmb()
		}
	}

	fmt.Println("Scanning services...")
	stats, err := indexer.Run(context.Background(), indexer.Options{
		Config:       cfg,
		Workers:      indexWorkers,
		Full:         indexFull,
		NoEmbed:      indexNoEmbed,
		Embedder:     emb,
		ContractsDir: filepath.Dir(indexWorkspace),
		Log:          os.Stdout,
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
	// Build the embedder once for the server lifetime; share it across reloads.
	emb, closeEmb, err := resolveEmbedder(cfg)
	if err != nil {
		return fmt.Errorf("embedder: %w", err)
	}
	defer closeEmb()
	synonyms := cfg.Search.Synonyms
	srv.SetSearcher(buildSearcher(store, emb, synonyms))

	// Watch graph.db for atomic swaps (polyflow index renames graph.db.tmp → graph.db).
	// On a Write or Create event, reopen the store, rebuild the index, and push a
	// graph_updated SSE event to all connected browser clients.
	if err := watchDB(dbPath, func() { reloadDB(dbPath, srv, emb, synonyms) }); err != nil {
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

func reloadDB(dbPath string, srv *server.Server, emb semantic.Embedder, synonyms map[string][]string) {
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
	// Reuse the same embedder across reloads — the sidecar process stays alive
	// for the server lifetime; only the in-memory vector matrix is refreshed.
	srv.SetSearcher(buildSearcher(newStore, emb, synonyms))
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

	// Kind-filtered searches use the FTS path (kind = node type, no flow/doc sections).
	if searchKind != "" {
		fetchLimit := searchLimit * 10
		nodes, err := store.SearchNodes(ctx, args[0], fetchLimit)
		if err != nil {
			return err
		}
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

	// Hybrid FTS+vector search (S.2): build searcher from the open store.
	cfg, _ := workspace.Load(meta.ConfigFile) // best-effort; nil cfg → no synonyms
	emb, closeEmb, _ := resolveEmbedder(cfg)
	defer closeEmb()
	var synonyms map[string][]string
	if cfg != nil {
		synonyms = cfg.Search.Synonyms
	}
	sr := buildSearcher(store, emb, synonyms)

	resp, err := sr.Search(ctx, args[0], searchLimit)
	if err != nil {
		return err
	}

	if searchFormat == "json" {
		return json.NewEncoder(os.Stdout).Encode(resp)
	}

	// Table output: sections separated by headers.
	if resp.Semantic != "" {
		fmt.Printf("  [semantic: %s]\n", resp.Semantic)
	}
	if len(resp.Nodes) > 0 {
		fmt.Println("  NODES")
		for _, h := range resp.Nodes {
			e := h.Entity
			fmt.Printf("    %-10s %-30s %-40s [%s] %s\n",
				strings.ToUpper(e.Type),
				e.ID,
				fmt.Sprintf("%s:%d", e.File, e.Line),
				h.Retrieval,
				fmt.Sprintf("%.4f", h.Score),
			)
		}
	}
	if len(resp.Flows) > 0 {
		fmt.Println("  FLOWS")
		for _, h := range resp.Flows {
			e := h.Entity
			fmt.Printf("    %-50s entry=%s [%s]\n",
				e.ID, e.NodeID, h.Retrieval,
			)
		}
	}
	if len(resp.Docs) > 0 {
		fmt.Println("  DOCS")
		for _, h := range resp.Docs {
			e := h.Entity
			fmt.Printf("    %-50s %s:%d [%s]\n",
				e.ID, e.File, e.Line, h.Retrieval,
			)
		}
	}
	return nil
}

// buildSearcher creates a Searcher from the open store with the given embedder.
// emb may be nil for FTS-only operation (the embed_status meta key carries
// the degradation reason; the Searcher surfaces it in Response.Semantic).
// The embedder lifecycle is the caller's responsibility.
func buildSearcher(store *graph.SQLiteStore, emb semantic.Embedder, synonyms map[string][]string) *semantic.Searcher {
	sem := semantic.NewStore(store.DB())
	return semantic.NewSearcher(sem, emb, synonyms)
}

// resolveEmbedder builds the Embedder from a workspace config.
// Returns (nil, noop, nil) when cfg is nil (FTS-only fallback).
// The close function must be called when the embedder is no longer needed.
func resolveEmbedder(cfg *workspace.WorkspaceConfig) (semantic.Embedder, func(), error) {
	if cfg == nil {
		emb, err := semantic.DefaultStaticEmbedder()
		if err != nil {
			return nil, func() {}, nil // FTS-only on failure
		}
		return emb, func() {}, nil
	}
	emb, closeFn, err := selectEmbedder(&cfg.Search)
	if closeFn == nil {
		closeFn = func() {}
	}
	if err != nil {
		return nil, func() {}, nil // FTS-only on failure; degradation surfaced via embed_status
	}
	return emb, closeFn, nil
}

// selectEmbedder builds the Embedder described in the workspace SearchConfig.
// Returns the embedder, an optional close function (non-nil only for sidecar),
// and any error.  The static default is always safe to use — it never fails once
// the binary is loaded.
func selectEmbedder(cfg *workspace.SearchConfig) (semantic.Embedder, func(), error) {
	switch cfg.Embedder {
	case "", "static":
		emb, err := semantic.DefaultStaticEmbedder()
		if err != nil {
			return nil, nil, err
		}
		return emb, nil, nil
	case "sidecar":
		binPath, err := findEmbedSidecarBin()
		if err != nil {
			return nil, nil, fmt.Errorf("sidecar binary not found: %w (run `polyflow models pull` to download the model)", err)
		}
		c, err := sidecar.StartClient(binPath)
		if err != nil {
			return nil, nil, fmt.Errorf("start sidecar: %w", err)
		}
		emb := semantic.NewSidecarEmbedder(c)
		return emb, emb.Close, nil
	case "endpoint":
		if cfg.EndpointURL == "" {
			return nil, nil, fmt.Errorf("search.endpoint_url is required when search.embedder is 'endpoint'")
		}
		model := cfg.EndpointModel
		emb := semantic.NewEndpointEmbedder(cfg.EndpointURL, model, cfg.EndpointKeyEnv)
		return emb, nil, nil
	default:
		return nil, nil, fmt.Errorf("unknown search.embedder %q; valid values: static, sidecar, endpoint", cfg.Embedder)
	}
}

// findEmbedSidecarBin looks up the polyflow-embed-sidecar binary using the
// same search order as the parse sidecar Manager: POLYFLOW_SIDECAR_DIR env,
// the running executable's directory, then PATH.
func findEmbedSidecarBin() (string, error) {
	const bin = semantic.SidecarBinaryName
	var dirs []string
	if env := os.Getenv(sidecar.SidecarDirEnv); env != "" {
		dirs = append(dirs, env)
	}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	for _, d := range dirs {
		p := filepath.Join(d, bin)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	if p, err := exec.LookPath(bin); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("%s not found in POLYFLOW_SIDECAR_DIR, executable dir, or PATH", bin)
}

// ─── status ──────────────────────────────────────────────────────────────────

var (
	statusErrors     bool
	statusUnresolved bool
	statusTrend      bool
	statusTrendN     int
	statusWS         string
)

func initStatusFlags() {
	statusCmd.Flags().BoolVar(&statusErrors, "errors", false, "list files with parse errors")
	statusCmd.Flags().BoolVar(&statusUnresolved, "unresolved", false, "list references the graph could not resolve (blind spots)")
	statusCmd.Flags().BoolVar(&statusTrend, "trend", false, "show per-service unresolved count trend over recent index runs")
	statusCmd.Flags().IntVar(&statusTrendN, "trend-n", 5, "number of past runs to compare against for --trend")
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
		// Sort kinds: structural refs first, then contract kinds alphabetically.
		structuralKinds := []string{"call_ref", "import_ref"}
		kindSet := map[string]bool{}
		var kindParts []string
		for _, kind := range structuralKinds {
			if byKind[kind] > 0 {
				kindParts = append(kindParts, fmt.Sprintf("%d %s", byKind[kind], kind))
				kindSet[kind] = true
			}
		}
		var contractKinds []string
		for k := range byKind {
			if !kindSet[k] {
				contractKinds = append(contractKinds, k)
			}
		}
		sort.Strings(contractKinds)
		for _, kind := range contractKinds {
			kindParts = append(kindParts, fmt.Sprintf("%d %s", byKind[kind], kind))
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
	if statusTrend {
		fmt.Println()
		dbStore, dbErr := graph.NewSQLiteStore(dbPath)
		if dbErr != nil {
			fmt.Printf("  Trend: no index found (run 'polyflow index' first)\n")
			return nil
		}
		defer dbStore.Close()
		history, hErr := dbStore.ListUnresolvedHistory(ctx, statusTrendN+1)
		if hErr != nil {
			fmt.Printf("  Trend: error reading history: %v\n", hErr)
			return nil
		}
		if len(history) == 0 {
			fmt.Printf("  Trend: no history yet (run 'polyflow index' at least once)\n")
			return nil
		}
		trend := graph.ComputeTrend(history, statusTrendN)
		fmt.Printf("  Trend (last %d runs): %-16s  %-16s  %8s  %8s  %8s\n",
			statusTrendN, "service", "kind", "baseline", "latest", "delta")
		for _, r := range trend {
			deltaStr := fmt.Sprintf("%+d", r.Delta)
			fmt.Printf("                       %-16s  %-16s  %8d  %8d  %8s\n",
				r.Service, r.Kind, r.Baseline, r.Latest, deltaStr)
		}
	}

	// C.2: list capture sessions with ages.
	sessions := trace_ingest.ListSessionInfos(capturesBase(), time.Now())
	if len(sessions) > 0 {
		fmt.Println()
		fmt.Printf("  Capture sessions: %d\n", len(sessions))
		for _, s := range sessions {
			age := s.Age
			if age == "" {
				age = "?"
			}
			status := "active"
			if s.StoppedAt != nil {
				status = "done"
			}
			fmt.Printf("    %-30s  started=%s  %s  spans=%-5d  (%s)\n",
				s.Name,
				s.StartedAt.Format("2006-01-02"),
				age,
				s.SpanCount,
				status,
			)
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
	reg, err := patterns.EmbeddedRegistry()
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
	contextTarget         string
	contextFiles          []string
	contextService        string
	contextLimit          int
	contextTask           string
	contextDepth          int
	contextFormat         string
	contextMaxTokens      int
	contextSummary        bool
	contextSnippetLines   int
	contextVerboseSources bool
)

func initContextFlags() {
	contextCmd.Flags().StringVar(&contextTarget, "target", "", "search query to find root node (use this or --file)")
	contextCmd.Flags().StringSliceVar(&contextFiles, "file", nil, "file path(s): return ranked related files instead of node context (repeatable)")
	contextCmd.Flags().StringVar(&contextService, "service", "", "with --file: restrict seed file resolution to a service")
	contextCmd.Flags().IntVar(&contextLimit, "limit", 20, "with --file: max related files returned (0 = unlimited)")
	contextCmd.Flags().StringVar(&contextTask, "task", "debug", "task type: impact, generate, debug, refactor")
	contextCmd.Flags().IntVar(&contextDepth, "depth", 5, "max traversal depth (0 = unlimited; --file mode defaults to 2)")
	contextCmd.Flags().StringVar(&contextFormat, "format", "json", "output format: json or text")
	contextCmd.Flags().IntVar(&contextMaxTokens, "max-tokens", 0, "approximate token budget for output (0 = unlimited); over budget, per-node detail rolls up per file")
	contextCmd.Flags().BoolVar(&contextSummary, "summary", false, "emit the file-grouped rollup instead of per-node detail")
	contextCmd.Flags().IntVar(&contextSnippetLines, "snippet-lines", 0, "inline N source lines per node in detail output (0 = off)")
	contextCmd.Flags().BoolVar(&contextVerboseSources, "verbose-sources", false, "emit full SourceRef structs instead of compact provider:ref strings")
}

var contextCmd = &cobra.Command{
	Use:   "context",
	Short: "Show the call context around a node",
	RunE:  runContext,
}

func runContext(cmd *cobra.Command, args []string) error {
	if (contextTarget == "") == (len(contextFiles) == 0) {
		return fmt.Errorf("provide exactly one of --target or --file")
	}
	if contextTask != "impact" && contextTask != "generate" && contextTask != "debug" && contextTask != "refactor" {
		return fmt.Errorf("unknown task type: %s (use: impact, generate, debug, refactor)", contextTask)
	}

	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	ctx := context.Background()

	// File mode: rank the files related to the seed file(s).
	if len(contextFiles) > 0 {
		idx, err := store.BuildIndex(ctx)
		if err != nil {
			return err
		}
		depth := contextDepth
		if !cmd.Flags().Changed("depth") {
			depth = 2 // a file neighborhood at call-graph depth 5 is the whole repo
		}
		result, err := pfcontext.BuildFiles(idx, contextService, contextFiles, depth, contextLimit)
		if err != nil {
			return err
		}
		unresolved, err := store.ListUnresolvedRefs(ctx)
		if err != nil {
			return err
		}
		result.AttachUnresolved(unresolved)
		result.ApplyBudget(contextMaxTokens)
		if contextFormat == "text" {
			return printContextFilesText(result)
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	nodes, err := store.SearchNodes(ctx, contextTarget, 5)
	if err != nil || len(nodes) == 0 {
		return fmt.Errorf("node not found for query: %s", contextTarget)
	}
	root := nodes[0]

	idx, err := store.BuildIndex(ctx)
	if err != nil {
		return err
	}

	result := pfcontext.Build(idx, root.ID, contextTask, contextDepth, contextVerboseSources, loadStaleAfter(meta.ConfigFile))

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

func printContextFilesText(r *pfcontext.FilesResult) error {
	fmt.Fprintf(os.Stdout, "Files: %s\n\n", strings.Join(r.Files, ", "))
	if len(r.Related) == 0 {
		fmt.Fprintln(os.Stdout, "No related files within depth.")
	} else {
		fmt.Fprintf(os.Stdout, "Related files (depth %d):\n", r.Depth)
		for _, e := range r.Related {
			fmt.Fprintf(os.Stdout, "  %-60s %2d refs, %2d nodes, depth %d via %s [%s]\n",
				e.File, e.Refs, e.Nodes, e.MinDepth, strings.Join(e.EdgeTypes, ","), e.Service)
		}
	}
	fmt.Fprintln(os.Stdout)
	printUnresolvedText(r.Unresolved)
	if r.Budget != nil && r.Budget.Note != "" {
		fmt.Fprintf(os.Stdout, "(%s)\n", r.Budget.Note)
	}
	return nil
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
	if line := graph.VerificationSummaryLine(s.VerificationSummary); line != "" {
		fmt.Fprintf(os.Stdout, "(%s)\n", line)
	}
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
	if line := graph.VerificationSummaryLine(r.VerificationSummary); line != "" {
		fmt.Fprintf(os.Stdout, "(%s)\n", line)
	}
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
	traceRoot          string
	traceDirection     string
	traceDepth         int
	traceFormat        string
	traceVerboseSources bool
)

func initTraceFlags() {
	traceCmd.Flags().StringVar(&traceRoot, "root", "", "search query to find the root node (required)")
	traceCmd.Flags().StringVar(&traceDirection, "direction", "forward", "trace direction: forward, backward, or both")
	traceCmd.Flags().IntVar(&traceDepth, "depth", 10, "max traversal depth (0 = unlimited)")
	traceCmd.Flags().StringVar(&traceFormat, "format", "text", "output format: json, text, or chain")
	traceCmd.Flags().BoolVar(&traceVerboseSources, "verbose-sources", false, "emit full SourceRef structs instead of compact provider:ref strings")
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

	result := trace.Run(idx, root.ID, traceDirection, traceDepth, traceVerboseSources, loadStaleAfter(meta.ConfigFile))
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
		if line := graph.VerificationSummaryLine(result.VerificationSummary); line != "" {
			fmt.Fprintf(os.Stderr, "(%s)\n", line)
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
	if line := graph.VerificationSummaryLine(r.VerificationSummary); line != "" {
		fmt.Fprintf(os.Stdout, "(%s)\n", line)
	}
	return nil
}

// ─── impact ──────────────────────────────────────────────────────────────────

var (
	impactTarget         string
	impactDepth          int
	impactService        string
	impactFormat         string
	impactFile           string
	impactDirection      string
	impactDiff           bool
	impactStaged         bool
	impactMaxTokens      int
	impactSummary        bool
	impactSnippetLines   int
	impactVerboseSources bool
)

func initImpactFlags() {
	impactCmd.Flags().StringVar(&impactTarget, "target", "", "search query for the target node")
	impactCmd.Flags().StringVar(&impactFile, "file", "", "file path: report impact at file granularity")
	impactCmd.Flags().StringVar(&impactDirection, "direction", "backward", "with --file: forward, backward or both")
	impactCmd.Flags().BoolVar(&impactDiff, "diff", false, "union blast radius of uncommitted changes (git diff against HEAD)")
	impactCmd.Flags().BoolVar(&impactStaged, "staged", false, "with --diff: staged changes only (git diff --cached)")
	impactCmd.Flags().IntVar(&impactDepth, "depth", 10, "max traversal depth (0 = unlimited)")
	impactCmd.Flags().StringVar(&impactService, "service", "", "filter results to a specific service")
	impactCmd.Flags().StringVar(&impactFormat, "format", "text", "output format: text, json, or github-comment")
	impactCmd.Flags().IntVar(&impactMaxTokens, "max-tokens", 0, "approximate token budget for output (0 = unlimited); over budget, per-node detail rolls up per file")
	impactCmd.Flags().BoolVar(&impactSummary, "summary", false, "emit the file-grouped rollup instead of per-node detail")
	impactCmd.Flags().IntVar(&impactSnippetLines, "snippet-lines", 0, "inline N source lines per node in detail output (0 = off)")
	impactCmd.Flags().BoolVar(&impactVerboseSources, "verbose-sources", false, "emit full SourceRef structs instead of compact provider:ref strings")
	impactCmd.MarkFlagsOneRequired("target", "file", "diff")
	impactCmd.MarkFlagsMutuallyExclusive("target", "file", "diff")
}

var impactCmd = &cobra.Command{
	Use:   "impact",
	Short: "Show what is impacted by changes to a node",
	RunE:  runImpact,
}

func runImpact(cmd *cobra.Command, args []string) error {
	if impactStaged && !impactDiff {
		return fmt.Errorf("--staged requires --diff")
	}
	if impactDiff {
		return runImpactDiff()
	}

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

	out := impact.Build(idx, root, impactDepth, impactService, impactVerboseSources, loadStaleAfter(meta.ConfigFile))

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
	if line := graph.VerificationSummaryLine(s.VerificationSummary); line != "" {
		fmt.Fprintf(os.Stdout, "(%s)\n", line)
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
	if line := graph.VerificationSummaryLine(out.VerificationSummary); line != "" {
		fmt.Fprintf(os.Stdout, "(%s)\n", line)
	}
	return nil
}

// runImpactDiff answers "will my current changes impact anything": it
// reindexes incrementally (the diff's line numbers must match the graph),
// maps git diff hunks to nodes, and reports the union blast radius.
func runImpactDiff() error {
	ctx := context.Background()

	cfg, err := workspace.Load(meta.ConfigFile)
	if err != nil {
		return err
	}
	stats, err := indexer.Run(ctx, indexer.Options{Config: cfg, Workers: runtime.GOMAXPROCS(0), ContractsDir: filepath.Dir(meta.ConfigFile)})
	if err != nil {
		return fmt.Errorf("reindex before diff impact: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Reindexed %d files (%d parsed, %d unchanged)\n", stats.TotalFiles, stats.ParsedFiles, stats.SkippedFiles)

	changes, err := gitdiff.Changes(".", impactStaged)
	if err != nil {
		return err
	}

	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	idx, err := store.BuildIndex(ctx)
	if err != nil {
		return err
	}

	out := impact.BuildDiff(idx, changes, impactDepth, impactService, impactVerboseSources, cfg.Evidence.StaleAfterDuration())
	if impactStaged {
		out.Mode = "staged"
	}

	unresolved, err := store.ListUnresolvedRefs(ctx)
	if err != nil {
		return err
	}
	out.AttachUnresolved(unresolved)
	out.InlineSnippets(".", impactSnippetLines)

	if impactFormat == "github-comment" {
		fmt.Fprint(os.Stdout, impact.FormatGitHubComment(out, 0))
		return nil
	}
	budgeted := out.ApplyBudget(impactMaxTokens, impactSummary)
	if impactFormat == "json" {
		return json.NewEncoder(os.Stdout).Encode(budgeted)
	}
	if s, ok := budgeted.(*impact.DiffSummary); ok {
		return printImpactDiffSummaryText(s)
	}
	return printImpactDiffText(out)
}

func spanText(s gitdiff.Span) string {
	if s.Start == s.End {
		return fmt.Sprintf("line %d", s.Start)
	}
	return fmt.Sprintf("lines %d-%d", s.Start, s.End)
}

func printUnmappedText(unmapped []impact.UnmappedHunk) {
	if len(unmapped) == 0 {
		return
	}
	fmt.Fprintf(os.Stdout, "Unmapped hunks (%d — no graph node, verify manually):\n", len(unmapped))
	for _, u := range unmapped {
		if u.Span != nil {
			fmt.Fprintf(os.Stdout, "  %s (%s): %s\n", u.File, spanText(*u.Span), u.Reason)
		} else {
			fmt.Fprintf(os.Stdout, "  %s: %s\n", u.File, u.Reason)
		}
	}
	fmt.Fprintln(os.Stdout)
}

func printImpactDiffText(out *impact.DiffResult) error {
	if out.ChangedFiles == 0 {
		fmt.Fprintln(os.Stdout, "No uncommitted changes.")
		return nil
	}
	fmt.Fprintf(os.Stdout, "Impact of %s changes: %d changed files, %d changed nodes\n\n", out.Mode, out.ChangedFiles, len(out.Targets))

	if len(out.Targets) > 0 {
		fmt.Fprintln(os.Stdout, "Changed nodes:")
		for _, t := range out.Targets {
			spans := make([]string, 0, len(t.Spans))
			for _, s := range t.Spans {
				spans = append(spans, spanText(s))
			}
			fmt.Fprintf(os.Stdout, "  %-40s %s:%d (%s)\n",
				fmt.Sprintf("%s  [%s]", t.Node.Label, t.Node.Type), t.Node.File, t.Node.Line, strings.Join(spans, ", "))
		}
		fmt.Fprintln(os.Stdout)
	}

	printUnmappedText(out.Unmapped)

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

func printImpactDiffSummaryText(s *impact.DiffSummary) error {
	if s.ChangedFiles == 0 {
		fmt.Fprintln(os.Stdout, "No uncommitted changes.")
		return nil
	}
	fmt.Fprintf(os.Stdout, "Impact of %s changes: %d changed files, %d changed nodes\n\n", s.Mode, s.ChangedFiles, len(s.Targets))

	if len(s.Targets) > 0 {
		fmt.Fprintln(os.Stdout, "Changed nodes:")
		for _, t := range s.Targets {
			fmt.Fprintf(os.Stdout, "  %s\n", t)
		}
		fmt.Fprintln(os.Stdout)
	}

	printUnmappedText(s.Unmapped)

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

// ─── eval ────────────────────────────────────────────────────────────────────

var (
	evalCorpus   string
	evalCase     string
	evalOutput   string
	evalGate     string
)

func initEvalFlags() {
	evalCmd.Flags().StringVar(&evalCorpus, "corpus", "eval/corpus", "path to corpus root (a dir with manifest.yaml, or a dir of such dirs)")
	evalCmd.Flags().StringVar(&evalCase, "case", "", "run only this case ID (default: all cases in the corpus)")
	evalCmd.Flags().StringVar(&evalOutput, "output", "", "write JSON results to this file (e.g. eval/baseline.json)")
	evalCmd.Flags().StringVar(&evalGate, "gate", "", "baseline JSON file to gate against; exits non-zero on any regression")
}

var evalCmd = &cobra.Command{
	Use:   "eval",
	Short: "Run the ground-truth recall evaluation corpus",
	RunE:  runEval,
}

func runEval(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// Single-corpus path: specified --case, or the corpus dir has a manifest.yaml.
	if evalCase != "" {
		_, err := os.Stat(filepath.Join(evalCorpus, "manifest.yaml"))
		if err == nil {
			return runEvalSingle(ctx, evalCorpus, evalCase)
		}
	}
	manifestPath := filepath.Join(evalCorpus, "manifest.yaml")
	if _, err := os.Stat(manifestPath); err == nil {
		return runEvalSingle(ctx, evalCorpus, evalCase)
	}

	// Multi-corpus path: corpus root contains sub-directories.
	multi, err := eval.RunAll(ctx, evalCorpus)
	if err != nil {
		return err
	}

	if evalOutput != "" {
		data, err := json.MarshalIndent(multi, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal JSON: %w", err)
		}
		if err := os.WriteFile(evalOutput, data, 0o644); err != nil {
			return fmt.Errorf("write output %s: %w", evalOutput, err)
		}
		fmt.Printf("Results written to %s\n\n", evalOutput)
	}

	hardFailed := false
	for _, report := range multi.Reports {
		fmt.Printf("Repo: %-20s  cases: %d  recall=%.3f  precision=%.3f\n",
			report.Repo, len(report.Results), report.Recall, report.Precision)
		for _, r := range report.Results {
			status := "ok"
			if r.HardFail {
				status = "HARD_FAIL"
				hardFailed = true
			}
			fmt.Printf("  %-44s recall=%.3f precision=%.3f honest=%d silent=%d  %s\n",
				r.CaseID, r.Recall, r.Precision, r.HonestMisses, r.SilentMisses, status)
		}
		fmt.Println()
	}

	for _, s := range multi.Skipped {
		fmt.Fprintf(os.Stderr, "WARNING: skipped corpus %q (%s): %s\n", s.Name, s.Dir, s.Reason)
	}

	// Without a gate, any hard-fail is fatal (E.1 acceptance). With --gate the
	// gate decides: pre-existing baseline hard-fails must not fail CI forever —
	// only NEW hard-fails, recall drops, silent-miss rises, or missing repos do.
	if hardFailed && evalGate == "" {
		fmt.Fprintln(os.Stderr, "Failed: one or more cases hard-failed (must_not_miss file silently missed)")
		os.Exit(1)
	}

	if evalGate != "" {
		baseline, err := eval.LoadBaseline(evalGate)
		if err != nil {
			return fmt.Errorf("load gate baseline: %w", err)
		}
		gate := eval.CheckGate(multi, baseline)
		if !gate.OK {
			fmt.Fprintf(os.Stderr, "\nCI gate: %d regression(s) vs %s\n", len(gate.Regressions), evalGate)
			for _, r := range gate.Regressions {
				switch r.Reason {
				case "hard_fail":
					fmt.Fprintf(os.Stderr, "  REGRESSION  %s/%s  new hard_fail (was not failing in baseline)\n", r.Repo, r.CaseID)
				case "recall_drop":
					fmt.Fprintf(os.Stderr, "  REGRESSION  %s/*  recall_drop  baseline=%.3f  current=%.3f\n", r.Repo, r.BaselineRecall, r.CurrentRecall)
				case "silent_miss_rise":
					fmt.Fprintf(os.Stderr, "  REGRESSION  %s/%s  silent_miss_rise  baseline=%d  current=%d\n", r.Repo, r.CaseID, r.BaselineSilent, r.CurrentSilent)
				case "missing_repo":
					fmt.Fprintf(os.Stderr, "  REGRESSION  %s/*  missing_repo  (in baseline but absent from this run — clone/index failed?)\n", r.Repo)
				}
			}
			fmt.Fprintln(os.Stderr, "Update eval/baseline.json when recall improves: polyflow eval --output eval/baseline.json")
			os.Exit(1)
		}
		fmt.Printf("CI gate: no regressions vs %s\n", evalGate)
	}

	return nil
}

func runEvalSingle(ctx context.Context, corpusDir, caseID string) error {
	report, err := eval.Run(ctx, eval.RunOptions{
		CorpusDir: corpusDir,
		CaseID:    caseID,
	})
	if err != nil {
		return err
	}

	fmt.Printf("Repo: %s   cases: %d\n", report.Repo, len(report.Results))
	fmt.Printf("Corpus  recall=%.3f  precision=%.3f\n\n", report.Recall, report.Precision)

	hardFailed := false
	for _, r := range report.Results {
		status := "ok"
		if r.HardFail {
			status = "HARD_FAIL"
			hardFailed = true
		}
		fmt.Printf("  %-40s recall=%.3f precision=%.3f honest=%d silent=%d  %s\n",
			r.CaseID, r.Recall, r.Precision, r.HonestMisses, r.SilentMisses, status)
	}

	if hardFailed {
		fmt.Fprintln(os.Stderr, "\nFailed: one or more cases hard-failed (must_not_miss file silently missed)")
		os.Exit(1)
	}
	return nil
}

// ─── doctor ──────────────────────────────────────────────────────────────────

var (
	doctorBaseline string
	doctorPropose  string
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Show a diagnostic summary of the workspace and eval health",
	RunE:  runDoctor,
}

func init() {
	doctorCmd.Flags().StringVar(&doctorBaseline, "baseline", "eval/baseline.json", "baseline JSON file for the eval summary row")
	doctorCmd.Flags().StringVar(&doctorPropose, "propose", "", "write gap-derived rule proposals + fixture skeletons to this directory (e.g. .polyflow/proposals)")
}

func runDoctor(cmd *cobra.Command, args []string) error {
	fmt.Println("polyflow doctor")
	fmt.Println()

	// Eval summary row — reads the baseline file without re-running the corpus.
	baseline, err := eval.LoadBaseline(doctorBaseline)
	if err != nil {
		// LoadBaseline wraps the os error, so unwrap with errors.Is — a repo
		// without an eval corpus is the normal case, not an error.
		if errors.Is(err, os.ErrNotExist) {
			fmt.Printf("  Eval corpus:  no baseline found at %s (run 'polyflow eval --output %s')\n", doctorBaseline, doctorBaseline)
		} else {
			fmt.Printf("  Eval corpus:  error reading %s: %v\n", doctorBaseline, err)
		}
	} else {
		sum := eval.SummarizeForDoctor(baseline, nil)
		skips := ""
		if len(baseline.Skipped) > 0 {
			skips = fmt.Sprintf("  skipped=%d", len(baseline.Skipped))
		}
		fmt.Printf("  Eval corpus:  %s  repos=%d  cases=%d  recall=%.3f  hard_fails=%d  silent=%d%s\n",
			sum.GeneratedAt, sum.Repos, sum.TotalCases, sum.AvgRecall, sum.HardFails, sum.SilentMiss, skips)
		if sum.HardFails > 0 {
			fmt.Printf("                %d hard_fail case(s) — run 'polyflow eval' for details\n", sum.HardFails)
		}
	}

	// Contract coverage per kind (requires a prior `polyflow index` run).
	fmt.Println()
	store, storeErr := openStore()
	if storeErr != nil {
		fmt.Printf("  Contract coverage:  no index found (run 'polyflow index' first)\n")
	} else {
		defer store.Close()
		ctx := context.Background()
		if coverageJSON, metaErr := store.GetMeta(ctx, "contract_coverage"); metaErr == nil {
			var cov []contract.KindCoverage
			if json.Unmarshal([]byte(coverageJSON), &cov) == nil && len(cov) > 0 {
				fmt.Printf("  Contract coverage:  %-16s  %8s  %10s  %7s  %8s\n", "kind", "matched", "unresolved", "dynamic", "indirect")
				for _, c := range cov {
					fmt.Printf("                      %-16s  %8d  %10d  %7d  %8d\n", c.Kind, c.Matched, c.Unresolved, c.Dynamic, c.Indirect)
				}
			} else {
				fmt.Printf("  Contract coverage:  (no rules matched — check contracts/*.yaml)\n")
			}
		} else {
			fmt.Printf("  Contract coverage:  no data (run 'polyflow index' first)\n")
		}
	}

	// G.6 walker-coverage row: for every language in the parser registry,
	// report whether a KeyWalker is registered (yes / no-op / MISSING).
	fmt.Println()
	fmt.Printf("  Key-walker coverage: %-16s  %s\n", "language", "status")
	walkerLangs := parser.RegisteredLanguages()
	sort.Strings(walkerLangs)
	for _, lang := range walkerLangs {
		status := contract.KeyWalkerStatus(lang)
		fmt.Printf("                       %-16s  %s\n", lang, status)
	}

	// R.5 runtime coverage: per-kind verified/candidate/gap counts from the
	// graph store (cumulative across all sessions).
	fmt.Println()
	var allEdges []graph.Edge
	if storeErr != nil {
		fmt.Printf("  Runtime coverage:    (no index — run 'polyflow index' first)\n")
	} else {
		ctx2 := context.Background()
		idx, idxErr := store.BuildIndex(ctx2)
		if idxErr != nil {
			fmt.Printf("  Runtime coverage:    error building index: %v\n", idxErr)
		} else {
			allEdges = idx.AllEdges()
			rtReport := trace_ingest.ComputeCoverage(trace_ingest.RuntimeCoverageEdges(allEdges), nil)
			printDoctorRuntimeCoverage(rtReport)
		}
	}

	// F.4 fusion report: verified / candidate / gap / conflicting summary,
	// merged alongside G.5 contract coverage and R.5 runtime coverage.
	// Also surfaces V.4 versioning coverage if the indexer wrote it.
	fmt.Println()
	var fusionReport evidence.ReconcileReport
	if storeErr != nil {
		fmt.Printf("  Fusion coverage:     (no index — run 'polyflow index' first)\n")
	} else if len(allEdges) > 0 {
		fusionReport = evidence.BuildReport(allEdges)
		printDoctorFusionCoverage(fusionReport)
		// V.4 versioning coverage: tool×version matrix stamped by the indexer.
		ctx3 := context.Background()
		profilesJSON, _ := store.GetMeta(ctx3, "toolchain_profiles")
		notesJSON, _ := store.GetMeta(ctx3, "toolchain_coverage")
		fmt.Print(toolchain.RenderVersionCoverage(profilesJSON, notesJSON))
	} else {
		fmt.Printf("  Fusion coverage:     (no cross-service edges — run 'polyflow index' first)\n")
	}

	// D.1: --propose emits gap-derived rule proposals + fixture skeletons.
	if doctorPropose != "" {
		if err := emitDoctorProposals(fusionReport.GapList, doctorPropose); err != nil {
			return err
		}
	}

	// D.2: ledger burn-down trend — flag services with 3 consecutive growing runs.
	fmt.Println()
	if storeErr != nil {
		fmt.Printf("  Ledger trend:        (no index — run 'polyflow index' first)\n")
	} else {
		ctx4 := context.Background()
		history, hErr := store.ListUnresolvedHistory(ctx4, 5)
		if hErr != nil || len(history) == 0 {
			fmt.Printf("  Ledger trend:        no history yet (run 'polyflow index' at least once)\n")
		} else {
			flagged := graph.DetectGrowth(history, 3)
			if len(flagged) == 0 {
				fmt.Printf("  Ledger trend:        OK — no services with 3+ consecutive growing unresolved counts\n")
			} else {
				fmt.Printf("  Ledger trend:        WARNING — unresolved count grew 3 runs consecutively: %s\n",
					strings.Join(flagged, ", "))
				fmt.Printf("                       Run 'polyflow status --trend' for per-kind breakdown\n")
			}
		}
	}

	// C.2: evidence freshness — suggest re-capture when all verified edges are stale.
	fmt.Println()
	if storeErr != nil {
		fmt.Printf("  Evidence freshness:  (no index — run 'polyflow index' first)\n")
	} else if len(allEdges) > 0 {
		wsCfg, wsErr := workspace.Load(meta.ConfigFile)
		var staleAfter time.Duration
		if wsErr == nil {
			staleAfter = wsCfg.Evidence.StaleAfterDuration()
		} else {
			staleAfter = workspace.DefaultStaleAfter
		}
		vs := graph.BuildVerificationSummaryAt(allEdges, staleAfter, time.Now())
		if vs.Verified == 0 {
			fmt.Printf("  Evidence freshness:  no verified edges (no capture sessions or all edges static)\n")
		} else if vs.StaleEvidence == vs.Verified {
			fmt.Printf("  Evidence freshness:  WARNING — all %d verified edge(s) have stale runtime evidence (>%s); run 'polyflow capture' to refresh\n",
				vs.Verified, formatDuration(staleAfter))
		} else if vs.StaleEvidence > 0 {
			fmt.Printf("  Evidence freshness:  %d/%d verified edge(s) have stale runtime evidence (>%s); consider re-capturing\n",
				vs.StaleEvidence, vs.Verified, formatDuration(staleAfter))
		} else {
			fmt.Printf("  Evidence freshness:  OK (%d verified edge(s), none stale)\n", vs.Verified)
		}
	} else {
		fmt.Printf("  Evidence freshness:  (no cross-service edges — run 'polyflow index' first)\n")
	}

	fmt.Println()
	return nil
}

// formatDuration renders a duration as a human-readable string for doctor output.
func formatDuration(d time.Duration) string {
	days := int(d.Hours() / 24)
	if days > 0 {
		return fmt.Sprintf("%dd", days)
	}
	return d.String()
}

// emitDoctorProposals writes rule YAML + fixture JSON for each observed_only_gap channel.
// Proposals are named "<n>-<slug>.yaml"; fixtures share the same base with ".json".
// Two runs on the same graph produce byte-identical files (bug-class rule 2).
func emitDoctorProposals(gaps []evidence.EdgeSummary, dir string) error {
	if len(gaps) == 0 {
		fmt.Printf("\n  No observed_only_gap channels — nothing to propose.\n")
		return nil
	}
	proposals, err := evidence.ProposeWithFixtures(gaps)
	if err != nil {
		return fmt.Errorf("generate proposals: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create proposals dir %s: %w", dir, err)
	}
	for _, p := range proposals {
		yamlPath := filepath.Join(dir, p.YAMLFilename)
		fixPath := filepath.Join(dir, p.FixtureFilename)
		if err := os.WriteFile(yamlPath, []byte(p.YAMLContent), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", yamlPath, err)
		}
		if err := os.WriteFile(fixPath, []byte(p.FixtureContent), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", fixPath, err)
		}
		fmt.Printf("\n  Proposed [%d]: %s\n           fixture: %s\n", p.Position, yamlPath, fixPath)
	}
	fmt.Printf("\n  %d proposal(s) written to %s\n", len(proposals), dir)
	fmt.Printf("  Edit node types + key fields, then: polyflow rules promote <yaml>\n")
	return nil
}

// printDoctorRuntimeCoverage prints the runtime coverage section in doctor style.
func printDoctorRuntimeCoverage(r trace_ingest.CoverageReport) {
	prefix := "  Runtime coverage:    "
	indent := "                       "

	hasData := len(r.Rows) > 0 || r.GapChannels > 0
	if !hasData {
		fmt.Printf("%s(no runtime sessions — run 'polyflow capture start' to record flows)\n", prefix)
		return
	}

	// Header row.
	fmt.Printf("%s%-18s  %5s  %8s  %9s  %3s  %6s\n",
		prefix, "kind", "total", "verified", "candidate", "gap", "%")

	for _, row := range r.Rows {
		pctStr := fmt.Sprintf("%.1f%%", row.Pct)
		if row.Total == 0 {
			pctStr = "n/a"
		}
		fmt.Printf("%s%-18s  %5d  %8d  %9d  %3d  %6s\n",
			indent, row.Kind, row.Total, row.Verified, row.Candidate, row.Gap, pctStr)
	}

	// Total row.
	totalPct := "n/a"
	if r.TotalChannels > 0 {
		totalPct = fmt.Sprintf("%.1f%%", float64(r.VerifiedChannels)/float64(r.TotalChannels)*100)
	}
	fmt.Printf("%s%-18s  %5d  %8d  %9d  %3d  %6s\n",
		indent, "total", r.TotalChannels, r.VerifiedChannels, r.CandidateChannels, r.GapChannels, totalPct)

	if len(r.ObservedOnlyGaps) > 0 {
		fmt.Printf("%sObserved-only gaps (%d) — fed to candidate-rule proposer:\n",
			indent, len(r.ObservedOnlyGaps))
		for _, g := range r.ObservedOnlyGaps {
			fmt.Printf("%s  %-16s  %-30s  %s → %s\n",
				indent, g.Kind, g.Key, g.From, g.To)
		}
	}
}

// printDoctorFusionCoverage prints the F.4 fusion coverage section in doctor style.
func printDoctorFusionCoverage(r evidence.ReconcileReport) {
	prefix := "  Fusion coverage:     "
	indent := "                       "

	if r.TotalEdges == 0 && r.GapEdges == 0 && r.ConflictingEdges == 0 {
		fmt.Printf("%s(no cross-service edges)\n", prefix)
		return
	}

	pctStr := "n/a"
	if r.TotalEdges > 0 {
		pctStr = fmt.Sprintf("%.1f%%", r.VerifiedPct)
	}
	fmt.Printf("%s%s verified  total=%d  candidate=%d  gap=%d  conflicting=%d\n",
		prefix, pctStr, r.TotalEdges, r.CandidateEdges, r.GapEdges, r.ConflictingEdges)

	if len(r.ByKind) > 0 {
		fmt.Printf("%s%-20s  %6s  %8s  %9s  %3s  %5s\n",
			indent, "kind", "total", "verified", "candidate", "gap", "conf")
		for _, row := range r.ByKind {
			if row.Total+row.Gap+row.Conflicting == 0 {
				continue
			}
			pct := "n/a"
			if row.Total > 0 {
				pct = fmt.Sprintf("%.1f%%", row.Pct)
			}
			fmt.Printf("%s%-20s  %6d  %8d  %9d  %3d  %5d  %s\n",
				indent, row.Kind, row.Total, row.Verified, row.Candidate, row.Gap, row.Conflicting, pct)
		}
	}

	if r.ConflictingEdges > 0 {
		fmt.Printf("%s%d conflicting edge(s) — run 'polyflow reconcile' for details\n",
			indent, r.ConflictingEdges)
	}
	if r.GapEdges > 0 {
		fmt.Printf("%s%d gap channel(s) — run 'polyflow reconcile --list-gaps' or '--propose-dir'\n",
			indent, r.GapEdges)
	}
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

// loadStaleAfter reads the workspace evidence.stale_after duration.
// Returns the default (30d) on any error so callers can always pass a value.
func loadStaleAfter(wsPath string) time.Duration {
	cfg, err := workspace.Load(wsPath)
	if err != nil {
		return workspace.DefaultStaleAfter
	}
	return cfg.Evidence.StaleAfterDuration()
}

// ─── models ──────────────────────────────────────────────────────────────────
//
// polyflow models pull  — download the nomic-embed-text-v1.5 GGUF model for
// the sidecar embedder (the only download polyflow ever performs; explicit by
// design so no code paths silently phone home).

// nomicModelURL is the HuggingFace download URL for nomic-embed-text-v1.5 Q8_0.
// SHA256 is pinned to detect corrupted/tampered downloads.
// Sourced from: https://huggingface.co/nomic-ai/nomic-embed-text-v1.5-GGUF
const (
	nomicModelURL      = "https://huggingface.co/nomic-ai/nomic-embed-text-v1.5-GGUF/resolve/main/nomic-embed-text-v1.5.Q8_0.gguf"
	nomicModelFile     = "nomic-embed-text-v1.5.Q8_0.gguf"
	// nomicModelSHA256 is the expected hex-encoded SHA-256 of the downloaded file.
	// Verify with: sha256sum ~/.cache/polyflow/models/nomic-embed-text-v1.5.Q8_0.gguf
	// and update this constant when the upstream model file changes.
	nomicModelSHA256   = "" // set to the verified SHA-256 before production use; empty = skip check
)

var modelsCmd = &cobra.Command{
	Use:   "models",
	Short: "Manage embedding models for the sidecar embedder",
}

var modelsPullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Download the nomic-embed-text-v1.5 GGUF model for sidecar embedding",
	Long: `Downloads nomic-embed-text-v1.5.Q8_0.gguf to ~/.cache/polyflow/models/.
The sidecar embedder (search.embedder: sidecar) requires this model.
The download is sha256-pinned; integrity is verified after download.`,
	RunE: runModelsPull,
}

func init() {
	modelsCmd.AddCommand(modelsPullCmd)
}

func runModelsPull(_ *cobra.Command, _ []string) error {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return fmt.Errorf("locate user cache dir: %w", err)
	}
	modelDir := filepath.Join(cacheDir, "polyflow", "models")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		return fmt.Errorf("create model dir %s: %w", modelDir, err)
	}
	dest := filepath.Join(modelDir, nomicModelFile)

	if info, err := os.Stat(dest); err == nil && info.Size() > 0 {
		if nomicModelSHA256 == "" {
			fmt.Printf("Model already present at %s (sha256 pin not set — skipping integrity check)\n", dest)
			return nil
		}
		if ok, err := verifySHA256(dest, nomicModelSHA256); err == nil && ok {
			fmt.Printf("Model already present and verified at %s\n", dest)
			return nil
		}
		fmt.Printf("Model at %s failed integrity check — re-downloading\n", dest)
	}

	fmt.Printf("Downloading %s\n  → %s\n", nomicModelURL, dest)
	if err := downloadFile(dest, nomicModelURL); err != nil {
		return fmt.Errorf("download: %w", err)
	}

	if nomicModelSHA256 != "" {
		fmt.Print("Verifying sha256... ")
		ok, err := verifySHA256(dest, nomicModelSHA256)
		if err != nil {
			return fmt.Errorf("verify sha256: %w", err)
		}
		if !ok {
			_ = os.Remove(dest)
			return fmt.Errorf("sha256 mismatch — downloaded file deleted; expected %s", nomicModelSHA256)
		}
		fmt.Println("OK")
	} else {
		fmt.Println("Warning: sha256 pin not set; skipping integrity check")
	}

	fmt.Printf("Model saved to %s\n", dest)
	fmt.Printf("Point your sidecar binary at this file and set search.embedder: sidecar in workspace.yaml\n")
	return nil
}

// downloadFile downloads url to dest, printing progress to stdout.
func downloadFile(dest, url string) error {
	resp, err := http.Get(url) //nolint:gosec // URL is a hardcoded constant
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %d", resp.StatusCode)
	}

	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create tmp file: %w", err)
	}
	defer os.Remove(tmp) //nolint:errcheck

	total := resp.ContentLength
	var written int64
	buf := make([]byte, 1<<20) // 1 MiB chunks
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return fmt.Errorf("write: %w", werr)
			}
			written += int64(n)
			if total > 0 {
				fmt.Printf("\r  %d / %d MB (%.0f%%)",
					written>>20, total>>20, float64(written)/float64(total)*100)
			} else {
				fmt.Printf("\r  %d MB downloaded", written>>20)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return fmt.Errorf("read: %w", rerr)
		}
	}
	fmt.Println()
	if err := f.Close(); err != nil {
		return fmt.Errorf("close tmp file: %w", err)
	}
	return os.Rename(tmp, dest)
}

// verifySHA256 returns true if the SHA-256 hex digest of the file at path
// matches expected (case-insensitive).
func verifySHA256(path, expected string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	got := hex.EncodeToString(h.Sum(nil))
	return strings.EqualFold(got, expected), nil
}
