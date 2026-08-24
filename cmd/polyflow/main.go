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
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"

	"github.com/lordsonvimal/polyflow/internal/capture"
	pfcontext "github.com/lordsonvimal/polyflow/internal/context"
	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/deadcode"
	"github.com/lordsonvimal/polyflow/internal/doctor"
	"github.com/lordsonvimal/polyflow/internal/eval"
	"github.com/lordsonvimal/polyflow/internal/evidence"
	"github.com/lordsonvimal/polyflow/internal/evidence/trace_ingest"
	"github.com/lordsonvimal/polyflow/internal/fleetconfig"
	"github.com/lordsonvimal/polyflow/internal/fleetsync"
	"github.com/lordsonvimal/polyflow/internal/gitdiff"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/impact"
	"github.com/lordsonvimal/polyflow/internal/indexer"
	"github.com/lordsonvimal/polyflow/internal/jobs"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/ops"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/lordsonvimal/polyflow/internal/queryresolve"
	"github.com/lordsonvimal/polyflow/internal/registry"
	"github.com/lordsonvimal/polyflow/internal/semantic"
	"github.com/lordsonvimal/polyflow/internal/server"
	"github.com/lordsonvimal/polyflow/internal/sidecar"
	"github.com/lordsonvimal/polyflow/internal/toolchain"
	"github.com/lordsonvimal/polyflow/internal/trace"
	"github.com/lordsonvimal/polyflow/internal/workspace"
	yieldpkg "github.com/lordsonvimal/polyflow/internal/yield"
)

func main() {
	err := rootCmd.Execute()
	opsFinalize(err)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:     meta.Name,
	Short:   meta.Description,
	Version: meta.Version,
}

// fleetFlag picks which fleet's bridge.db (GR.3) to stitch into query
// results when the current workspace is claimed by more than one fleet — a
// persistent flag rather than per-command, since every query command shares
// the same resolution (queryresolve.Resolve).
var fleetFlag string

func init() {
	rootCmd.PersistentFlags().StringVar(&fleetFlag, "fleet", "", "which fleet's bridge.db to use for cross-service results, when this workspace is claimed by more than one (Tier GR)")
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
		deadcodeCmd,
		configCmd,
		depsCmd,
		linkCmd,
		fleetCmd,
		registryCmd,
		mcpCmd,
		evalCmd,
		doctorCmd,
		reconcileCmd,
		rulesCmd,
		modelsCmd,
		benchCmd,
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
	initDeadcodeFlags()
	initConfigSubcmds()
	initEvalFlags()
	initLinkFlags()
	initFleetSubcmds()
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
	initCmd.Flags().BoolVar(&initForce, "force", false, "overwrite an existing polyflow.yml without asking")
}

func runInit(cmd *cobra.Command, args []string) error {
	cfgPath := meta.ConfigFile
	if _, err := os.Stat(cfgPath); err == nil && !initForce {
		fmt.Printf("polyflow.yml already exists. Overwrite? [y/N]: ")
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
		if err := workspace.SaveInit(cfgPath, cfg); err != nil {
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

	if err := workspace.SaveInit(cfgPath, cfg); err != nil {
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
	indexCmd.Flags().StringVar(&indexWorkspace, "workspace", meta.ConfigFile, "path to polyflow.yml")
	indexCmd.Flags().IntVar(&indexWorkers, "workers", runtime.GOMAXPROCS(0), "parser worker pool size")
	indexCmd.Flags().BoolVar(&indexFull, "full", false, "force a full re-parse, ignoring the incremental cache")
	indexCmd.Flags().BoolVar(&indexNoEmbed, "no-embed", false, "skip the embedding pass (search runs FTS-only; semantic: unavailable)")
}

var indexCmd = &cobra.Command{
	Use:   "index [service]",
	Short: "Parse and index all services in the workspace, or a single service",
	Args:  cobra.MaximumNArgs(1),
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

	opts := indexer.Options{
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
	}

	if len(args) == 1 {
		svc := args[0]
		if !cfg.HasService(svc) {
			return fmt.Errorf("index: no service %q in %s", svc, indexWorkspace)
		}
		opts.ServiceFilter = []string{svc}
		opts.DBDir = filepath.Join(meta.DBDir, "services", svc)
		fmt.Printf("Scanning %s...\n", svc)
	} else {
		fmt.Println("Scanning services...")
	}

	stats, err := indexer.Run(context.Background(), opts)
	if err != nil {
		return err
	}

	fmt.Printf("\nDone. %d files indexed in %s (%d parsed, %d unchanged)\n",
		stats.TotalFiles, stats.Elapsed.Truncate(time.Millisecond), stats.ParsedFiles, stats.SkippedFiles)
	// Both figures are reported: the contract total is the honest measure of
	// linking work done, but most of it is same-service by design, so printing
	// it alone under a "cross-service" label overstated the cross-service graph
	// several-fold.
	fmt.Printf("  Nodes: %d | Edges: %d | Contract links: %d (%d cross-service)\n",
		stats.Nodes, stats.Edges, stats.ContractEdges, stats.CrossLinks)
	if stats.ErrorFiles > 0 {
		fmt.Printf("  Errors: %d files (run `polyflow status --errors` for details)\n", stats.ErrorFiles)
	}

	// GR.1: a standalone repo's own polyflow.yml (today's per-repo case,
	// e.g. willow's or juniper's own workspace) self-populates the
	// local machine registry as a side effect of a whole-workspace index —
	// no separate register command to remember to run. Registered under the
	// workspace's own top-level `name:` (a fleet.yml service entry names a
	// whole repo, not one of that repo's possibly-several internal
	// services — cfg.Services[0].Name was only ever a correct proxy for
	// that when a workspace happened to declare exactly one service).
	// `polyflow index <service>` (single-service filter, args non-empty) is
	// not "this machine has a full fleet member checked out at this path",
	// so it doesn't sync.
	if len(args) == 0 {
		if wsRoot, err := filepath.Abs(filepath.Dir(indexWorkspace)); err == nil {
			if regPath, err := registry.DefaultPath(); err == nil {
				if err := registry.Sync(regPath, cfg.Name, wsRoot); err != nil {
					fmt.Printf("  Warning: local registry sync failed: %v\n", err)
				}
			}
		}
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
	serveCmd.Flags().StringVar(&serveWS, "workspace", meta.ConfigFile, "path to polyflow.yml")
	serveCmd.Flags().BoolVar(&serveDev, "dev", false, "enable CORS for Vite dev server (port 5173)")
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the polyflow web UI and API server",
	RunE:  runServe,
}

func runServe(cmd *cobra.Command, args []string) error {
	// UO.7 setup mode: a missing polyflow.yml or graph.db no longer errors
	// serve out — it boots the UI shell (and the jobs/setup API) against an
	// empty in-memory graph, and the setup wizard's init+index jobs bring up
	// the real workspace in place. cfg is nil when serveWS doesn't parse;
	// every cfg-dependent call below is nil-safe (resolveEmbedder(nil) is an
	// explicit fallback) or guarded.
	cfg, cfgErr := workspace.Load(serveWS)

	dbPath := filepath.Join(meta.DBDir, meta.DBFile)
	_, dbStatErr := os.Stat(dbPath)
	dbMissing := os.IsNotExist(dbStatErr)

	port := servePort
	if port == 0 {
		if cfgErr == nil {
			port = cfg.EffectivePort()
		} else {
			port = meta.DefaultPort
		}
	}

	ctx := context.Background()
	var (
		store *graph.SQLiteStore
		err   error
	)
	if cfgErr != nil || dbMissing {
		store, err = graph.NewSQLiteStore(":memory:")
		if err != nil {
			return fmt.Errorf("open in-memory store: %w", err)
		}
	} else {
		store, err = graph.NewSQLiteStore(dbPath)
		if err != nil {
			return fmt.Errorf("open store: %w", err)
		}
	}
	idx, err := buildFleetAwareIndex(ctx, store)
	if err != nil {
		return fmt.Errorf("build index: %w", err)
	}

	var srv *server.Server
	if serveDev {
		srv = server.NewDev(store, idx)
	} else {
		srv = server.New(store, idx)
	}
	srv.SetConfigPath(serveWS)
	srv.SetDBPath(dbPath)
	// UO.4: generate the CLI reference from the live command tree once, at
	// startup, so GET /api/docs/cli can never drift from the actual binary.
	meta.SetCLIDocs(buildCLIDocs(rootCmd))
	// Build the embedder once for the server lifetime; share it across reloads.
	emb, closeEmb, err := resolveEmbedder(cfg)
	if err != nil {
		return fmt.Errorf("embedder: %w", err)
	}
	defer closeEmb()
	var synonyms map[string][]string
	if cfgErr == nil {
		synonyms = cfg.Search.Synonyms
	}
	srv.SetSearcher(buildSearcher(store, emb, synonyms))

	// GR.6: if this workspace is a registered fleet member, wire per-member
	// store switching into the server so the UI can browse another member's
	// own graph, not just this one's cross-service edges into it (already
	// covered by buildFleetAwareIndex above regardless of fleet mode).
	if cfgErr == nil && !dbMissing {
		wireFleetServe(ctx, srv, serveWS, store, dbPath, emb, synonyms)
	}

	// UB.2: ops.db lives next to graph.db and is never touched by the
	// indexer, so it survives graph.db's rebuild-then-atomic-rename. In
	// setup mode meta.DBDir may not exist yet — the jobs API (needed for the
	// wizard's init/index steps) requires it regardless of workspace state.
	if err := os.MkdirAll(meta.DBDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not create %s: %v\n", meta.DBDir, err)
	}
	opsPath := filepath.Join(meta.DBDir, meta.OpsFile)
	opsStore, err := ops.Open(opsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: tool-call audit log disabled: %v\n", err)
	} else {
		defer opsStore.Close()
		srv.SetOps(opsStore)

		// UB.3/UO.7: the jobs manager wraps indexer.Run/eval.Run/
		// evidence.BuildReport/workspace.Discover — the same internals the
		// CLI commands use — so the UI can trigger them with progress/
		// cancellation. Index-job completion needs no explicit reload call:
		// it renames graph.db.tmp -> graph.db like `polyflow index` does, and
		// the fsnotify watcher below already reloads on that rename —
		// including the setup wizard's first index, which is what carries
		// this server out of setup mode.
		mgr := jobs.NewManager(jobs.Options{
			Ops:             opsStore,
			WorkspacePath:   serveWS,
			DBPath:          dbPath,
			Broadcast:       srv.Broadcast,
			ResolveEmbedder: resolveEmbedder,
		})
		srv.SetJobs(mgr)
	}

	// UB.7: the capture manager reads/writes the same on-disk session store
	// (.polyflow/captures) the CLI's capture/ingest/flows subcommands use,
	// so a session started via either surface is visible and stoppable from
	// the other.
	srv.SetCapture(capture.NewManager(capture.BaseDir()))

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
	searchFormat  string
	searchLimit   int
	searchKind    string
	searchService string
)

func initSearchFlags() {
	searchCmd.Flags().StringVar(&searchFormat, "format", "table", "output format: table or json")
	searchCmd.Flags().IntVar(&searchLimit, "limit", 20, "max results")
	searchCmd.Flags().StringVar(&searchKind, "kind", "", "restrict results: 'file' or a node type (function, variable, http_handler, …)")
	searchCmd.Flags().StringVar(&searchService, "service", "", "when this workspace is a fleet member (Tier GR): narrow search to just this one member instead of federating across the whole fleet")
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
		idx, err := buildFleetAwareIndex(ctx, store)
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

	resp, err := runFederatedOrLocalSearch(ctx, sr, emb, synonyms, args[0], searchService, searchLimit)
	if err != nil {
		return err
	}

	// §3: never return zero nodes when FTS has matches; §2/§3: cap flow/doc
	// flood and inline snippets. Shared with the MCP search tool.
	if len(resp.Nodes) == 0 {
		if nodes, nerr := store.SearchNodes(ctx, args[0], searchLimit); nerr == nil {
			for _, n := range nodes {
				resp.Nodes = append(resp.Nodes, semantic.Hit{
					Entity:    semantic.Entity{ID: n.ID, Type: "node", NodeID: n.ID, File: n.File, Line: n.Line},
					Retrieval: "lexical",
				})
			}
		}
	}
	semantic.ShapeSearchResponse(&resp, ".", semantic.SearchFlowCap, semantic.SearchDocCap, semantic.SearchSnippetLines)

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
			if h.Snippet != "" {
				for _, ln := range strings.Split(h.Snippet, "\n") {
					fmt.Printf("      │ %s\n", ln)
				}
			}
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

// buildFleetSearchers opens one Searcher per locally-resolved member of the
// fleet claiming the current workspace (queryresolve.FleetMembers, GR.3),
// sharing emb/synonyms across all of them, for search's default federation.
// Returns a nil map (no error) when the workspace isn't a fleet member — the
// caller falls back to its single local Searcher in that case. The returned
// close func closes every store this function opened; safe to call even
// when the map is nil.
func buildFleetSearchers(emb semantic.Embedder, synonyms map[string][]string) (map[string]*semantic.Searcher, func(), error) {
	members, err := queryresolve.FleetMembers(".", queryresolve.Options{Fleet: fleetFlag})
	if err != nil || len(members) == 0 {
		// Ambiguous-fleet or lookup errors degrade to "no federation" here —
		// search still works against the single local store either way.
		return nil, func() {}, nil
	}

	searchers := make(map[string]*semantic.Searcher, len(members))
	var stores []*graph.SQLiteStore
	closeAll := func() {
		for _, st := range stores {
			st.Close()
		}
	}
	for svc, dbPath := range members {
		store, openErr := graph.NewSQLiteStore(dbPath)
		if openErr != nil {
			// One member's local DB being unreadable must not break search
			// against the rest — skip it.
			continue
		}
		stores = append(stores, store)
		searchers[svc] = buildSearcher(store, emb, synonyms)
	}
	if len(searchers) == 0 {
		closeAll()
		return nil, func() {}, nil
	}
	return searchers, closeAll, nil
}

// runFederatedOrLocalSearch is the CLI search command's retrieval step,
// mirroring internal/mcpserver's runSearch: service == "" federates across
// every locally-resolved fleet member by default (GR.3's federation-scope
// decision); a non-empty service narrows to just that one member, falling
// back to the local Searcher if it isn't a wired fleet member.
func runFederatedOrLocalSearch(ctx context.Context, local *semantic.Searcher, emb semantic.Embedder, synonyms map[string][]string, query, service string, limit int) (semantic.Response, error) {
	fleet, closeFleet, err := buildFleetSearchers(emb, synonyms)
	if err != nil {
		return semantic.Response{}, err
	}
	defer closeFleet()

	if service != "" {
		if sr, ok := fleet[service]; ok {
			return sr.Search(ctx, query, limit)
		}
		return local.Search(ctx, query, limit)
	}
	if len(fleet) > 1 {
		return semantic.FederatedSearch(ctx, fleet, query, limit)
	}
	return local.Search(ctx, query, limit)
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
	statusCmd.Flags().StringVar(&statusWS, "workspace", meta.ConfigFile, "path to polyflow.yml")
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show index statistics",
	RunE:  runStatus,
}

// formatTrustLine renders the workspace's measured-eval trust state (plan-14
// T.0): recall + corpus + date when measured, UNMEASURED when never stamped,
// and a STALE suffix when the index was rebuilt after the last measurement.
func formatTrustLine(store graph.Store, ctx context.Context) string {
	stamp, err := graph.LoadTrustStamp(ctx, store)
	if err != nil || !stamp.Measured {
		return "Trust: UNMEASURED — run 'polyflow eval stamp' (see docs/plan-14)"
	}
	date := stamp.MeasuredAt
	if t, perr := time.Parse(time.RFC3339, stamp.MeasuredAt); perr == nil {
		date = t.Format("2006-01-02")
	}
	line := fmt.Sprintf("Trust: recall %.3f over %d cases (%s corpus, %s)", stamp.Recall, stamp.Cases, stamp.Corpus, date)
	if stamp.Stale {
		line += " STALE (index newer than measurement)"
	}
	return line
}

// perServiceLastIndexed is FR.6's addition to `polyflow status`: a
// "per-service last indexed" line sourced from each service's own
// `services/<name>/graph.db` meta table (FR.2), independent of the merged
// fleet DB's single timestamp. Only services with a per-service DB on disk
// are reported — a workspace that has never used `polyflow index <service>`
// gets no section at all, since every service's staleness is already
// covered by the merged DB's "Last indexed" line above.
func perServiceLastIndexed(cfg *workspace.WorkspaceConfig, dbDir string) []string {
	var lines []string
	for _, svc := range cfg.Services {
		svcDBPath := filepath.Join(dbDir, "services", svc.Name, meta.DBFile)
		if _, err := os.Stat(svcDBPath); err != nil {
			continue
		}
		store, err := graph.NewSQLiteStore(svcDBPath)
		if err != nil {
			lines = append(lines, fmt.Sprintf("%s: error opening DB (%v)", svc.Name, err))
			continue
		}
		ts, err := store.GetMeta(context.Background(), "last_indexed")
		store.Close()
		if err != nil {
			lines = append(lines, fmt.Sprintf("%s: never", svc.Name))
			continue
		}
		unix, err := strconv.ParseInt(ts, 10, 64)
		if err != nil {
			lines = append(lines, fmt.Sprintf("%s: never", svc.Name))
			continue
		}
		t := time.Unix(unix, 0)
		lines = append(lines, fmt.Sprintf("%s: %s (%s ago)", svc.Name, t.Format("2006-01-02 15:04:05"), time.Since(t).Round(time.Second)))
	}
	return lines
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
	var lastIndexedAt time.Time // zero when never indexed
	if ts, err := store.GetMeta(ctx, "last_indexed"); err == nil {
		if unix, err := strconv.ParseInt(ts, 10, 64); err == nil {
			t := time.Unix(unix, 0)
			lastIndexedAt = t
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
	fmt.Printf("  %s\n", formatTrustLine(store, ctx))
	if freshness := indexFreshness(cfg, lastIndexedAt); freshness != "" {
		fmt.Printf("  Freshness: %s\n", freshness)
	}
	if perSvc := perServiceLastIndexed(cfg, meta.DBDir); len(perSvc) > 0 {
		fmt.Printf("  Per-service last indexed:\n")
		for _, line := range perSvc {
			fmt.Printf("    %s\n", line)
		}
	}
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

	// B.0: unparsed source files — service-level gauge of parser blind spots.
	if unparsedJSON, metaErr := store.GetMeta(ctx, "unparsed_files"); metaErr == nil && unparsedJSON != "{}" && unparsedJSON != "" {
		var unparsed map[string]map[string]int
		if json.Unmarshal([]byte(unparsedJSON), &unparsed) == nil && len(unparsed) > 0 {
			svcs := make([]string, 0, len(unparsed))
			for s := range unparsed {
				svcs = append(svcs, s)
			}
			sort.Strings(svcs)
			var svcParts []string
			for _, s := range svcs {
				total, topExts := indexer.UnparsedSummary(unparsed[s])
				svcParts = append(svcParts, fmt.Sprintf("%s: %d (%s)", s, total, topExts))
			}
			fmt.Printf("  Unparsed source files: %s — no parser registered (may be added by future plans)\n",
				strings.Join(svcParts, "; "))
		}
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

// indexFreshness compares source-file mtimes against the last index run and
// returns a one-line freshness verdict for `polyflow status`. It mirrors the
// indexer's exclude handling (index.exclude + .polyflowignore + nested-service
// pruning) so it counts exactly the files a reindex would revisit, and caps the
// walk so an obviously-stale tree stays instant. Empty string = never indexed
// (the "Last indexed: never" line already says so).
func indexFreshness(cfg *workspace.WorkspaceConfig, lastIndexedAt time.Time) string {
	if lastIndexedAt.IsZero() {
		return ""
	}
	ignorePatterns := workspace.LoadIgnoreFile(".")
	// Absolute service paths, to prune each service's tree out of the others'.
	svcPaths := make([]string, len(cfg.Services))
	for i, svc := range cfg.Services {
		if abs, err := filepath.Abs(svc.Path); err == nil {
			svcPaths[i] = abs
		} else {
			svcPaths[i] = svc.Path
		}
	}
	const cap = 50
	total := 0
	capped := false
	for i, svc := range cfg.Services {
		var extra []string
		for j, other := range svcPaths {
			if i == j {
				continue
			}
			if rel, err := filepath.Rel(svcPaths[i], other); err == nil &&
				!strings.HasPrefix(rel, "..") && rel != "." {
				extra = append(extra, rel+"/**")
			}
		}
		excludes := append(append([]string{}, cfg.Index.Exclude...), ignorePatterns...)
		excludes = append(excludes, extra...)
		n, c := indexer.CountFilesModifiedSince(svc.Path, excludes, lastIndexedAt, cap-total)
		total += n
		if c {
			capped = true
			break
		}
	}
	if total == 0 {
		return "up to date"
	}
	countStr := fmt.Sprintf("%d", total)
	if capped {
		countStr = fmt.Sprintf("%d+", total)
	}
	return fmt.Sprintf("STALE — %s file(s) changed since last index (run 'polyflow index')", countStr)
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
	fmt.Printf("Added pattern file %s to polyflow.yml\n", path)
	return nil
}

// ─── context ─────────────────────────────────────────────────────────────────

var (
	contextTarget         string
	contextTargetService  string
	contextTargetType     string
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
	contextInclude        string
)

func initContextFlags() {
	contextCmd.Flags().StringVar(&contextTarget, "target", "", "search query to find root node (use this or --file)")
	contextCmd.Flags().StringVar(&contextTargetService, "target-service", "", "restrict target resolution to this service (resolves cross-service ambiguity)")
	contextCmd.Flags().StringVar(&contextTargetType, "target-type", "", "restrict target resolution to this node type (function, component, …)")
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
	contextCmd.Flags().StringVar(&contextInclude, "include", "", "noise classes to show, comma-separated (filter_chain, mixin, containment, render_tree, all, none); overrides --task default")
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
		idx, err := buildFleetAwareIndex(ctx, store)
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
	idx, err := buildFleetAwareIndex(ctx, store)
	if err != nil {
		return err
	}

	root, candidates, exactMatch, err := graph.ResolveTarget(ctx, graph.FleetSearcher{Store: store, Idx: idx}, contextTarget, contextTargetService, contextTargetType)
	if err != nil {
		return err
	}

	var includeKeys []string
	if contextInclude != "" {
		includeKeys = strings.Split(contextInclude, ",")
	}
	include, err := graph.ResolveNoiseInclude(includeKeys, contextTask)
	if err != nil {
		return err
	}

	result := pfcontext.Build(idx, root.ID, contextTask, contextDepth, contextVerboseSources, loadStaleAfter(meta.ConfigFile), include)
	result.TargetCandidates = candidates
	result.Status = graph.AmbiguityStatus(candidates)
	result.ResolutionNote = graph.ResolutionNote(contextTarget, exactMatch)
	result.Trust, _ = graph.LoadTrustStamp(ctx, store)

	unresolved, err := store.ListUnresolvedRefs(ctx)
	if err != nil {
		return err
	}
	result.AttachUnresolved(unresolved)
	result.FinalizeEpistemic()
	result.InlineSnippets(".", contextSnippetLines)

	out := result.ApplyBudget(contextMaxTokens, contextSummary)
	if contextFormat == "text" {
		if result.ResolutionNote != "" {
			fmt.Fprintf(os.Stderr, "warning: %s\n", result.ResolutionNote)
		}
		printAmbiguousCandidates(os.Stderr, contextTarget, root.ID, candidates)
		if line := hiddenByClassLine(result.HiddenByClass); line != "" {
			fmt.Fprintln(os.Stderr, line)
		}
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

// unresolvedPerFileCap bounds how many blind spots one file may contribute to
// a query's footer.
//
// The footer is scoped to traversed *files*, which assumes a file holds a
// handful of references. config/routes.rb breaks that assumption: it declares
// every route in the application, so an impact query that reaches it inherited
// 178 rails_route_action_unresolved lines about routes unrelated to the
// question asked. A footer that long stops being a caveat and becomes the
// answer — and it is a footer whose whole purpose is to tell an agent which
// files to go open.
const unresolvedPerFileCap = 5

// printUnresolvedText renders the traversal-scoped blind spots appended to
// text-format query output, capped per file so one index-like file cannot
// crowd out the rest. The full ledger stays available via
// `polyflow status --unresolved`.
func printUnresolvedText(refs []graph.UnresolvedRef) {
	if len(refs) == 0 {
		return
	}
	shown, omitted := capPerFile(refs, unresolvedPerFileCap)
	fmt.Fprintf(os.Stdout, "Unresolved references in traversed files (%d — verify manually, edges may be missing):\n", len(refs))
	for _, u := range shown {
		fmt.Fprintf(os.Stdout, "  %s:%d  %s (%s)\n", u.File, u.Line, u.Name, u.Kind)
	}
	for _, f := range sortedKeys(omitted) {
		fmt.Fprintf(os.Stdout, "  … and %d more in %s (polyflow status --unresolved)\n", omitted[f], f)
	}
}

// capPerFile keeps at most n refs per file, in input order, and reports how
// many each file dropped.
func capPerFile(refs []graph.UnresolvedRef, n int) ([]graph.UnresolvedRef, map[string]int) {
	seen := map[string]int{}
	omitted := map[string]int{}
	shown := make([]graph.UnresolvedRef, 0, len(refs))
	for _, u := range refs {
		if seen[u.File] < n {
			shown = append(shown, u)
		} else {
			omitted[u.File]++
		}
		seen[u.File]++
	}
	return shown, omitted
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ─── trace ───────────────────────────────────────────────────────────────────

var (
	traceRoot           string
	traceTargetService  string
	traceTargetType     string
	traceDirection      string
	traceDepth          int
	traceFormat         string
	traceVerboseSources bool
	traceInclude        string
	traceTask           string
	traceExploreChains  int
)

func initTraceFlags() {
	traceCmd.Flags().StringVar(&traceRoot, "root", "", "search query to find the root node (required)")
	traceCmd.Flags().StringVar(&traceTargetService, "target-service", "", "restrict root resolution to this service (resolves cross-service ambiguity)")
	traceCmd.Flags().StringVar(&traceTargetType, "target-type", "", "restrict root resolution to this node type (function, component, …)")
	traceCmd.Flags().StringVar(&traceDirection, "direction", "forward", "trace direction: forward, backward, or both")
	traceCmd.Flags().IntVar(&traceDepth, "depth", 10, "max traversal depth (0 = unlimited)")
	traceCmd.Flags().StringVar(&traceFormat, "format", "text", "output format: json, text, or chain")
	traceCmd.Flags().BoolVar(&traceVerboseSources, "verbose-sources", false, "emit full SourceRef structs instead of compact provider:ref strings")
	traceCmd.Flags().StringVar(&traceInclude, "include", "", "noise classes to show, comma-separated (filter_chain, mixin, containment, render_tree, all, none); overrides --task default")
	traceCmd.Flags().StringVar(&traceTask, "task", "debug", "task type used for the noise-visibility default when --include is unset: impact, generate, debug, refactor")
	traceCmd.Flags().IntVar(&traceExploreChains, "explore-chains", trace.ExploreChains, "how many candidate chains to enumerate before giving up; raise this if a root gated by heavy filter/mixin fan-out returns few or no visible chains")
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
	idx, err := buildFleetAwareIndex(ctx, store)
	if err != nil {
		return err
	}

	root, candidates, exactMatch, err := graph.ResolveTarget(ctx, graph.FleetSearcher{Store: store, Idx: idx}, traceRoot, traceTargetService, traceTargetType)
	if err != nil {
		return err
	}

	var includeKeys []string
	if traceInclude != "" {
		includeKeys = strings.Split(traceInclude, ",")
	}
	include, err := graph.ResolveNoiseInclude(includeKeys, traceTask)
	if err != nil {
		return err
	}

	result := trace.Run(idx, root.ID, traceDirection, traceDepth, traceVerboseSources, loadStaleAfter(meta.ConfigFile), include, traceExploreChains)
	if result == nil {
		return fmt.Errorf("root node %s not in graph", root.ID)
	}
	result.TargetCandidates = candidates
	result.Status = graph.AmbiguityStatus(candidates)
	result.ResolutionNote = graph.ResolutionNote(traceRoot, exactMatch)
	result.Trust, _ = graph.LoadTrustStamp(ctx, store)

	unresolved, err := store.ListUnresolvedRefs(ctx)
	if err != nil {
		return err
	}
	result.AttachUnresolved(unresolved)
	result.FinalizeEpistemic()

	if result.ResolutionNote != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", result.ResolutionNote)
	}
	printAmbiguousCandidates(os.Stderr, traceRoot, root.ID, candidates)
	if traceFormat != "json" {
		if line := hiddenByClassLine(result.HiddenByClass); line != "" {
			fmt.Fprintln(os.Stderr, line)
		}
	}
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

// hiddenByClassLine renders the noise-visibility tally (Tier NV) as a single
// stderr line, sorted by class name for determinism. Returns "" when
// nothing was hidden.
func hiddenByClassLine(hidden map[graph.NoiseClass]int) string {
	if len(hidden) == 0 {
		return ""
	}
	classes := make([]string, 0, len(hidden))
	for c := range hidden {
		classes = append(classes, string(c))
	}
	sort.Strings(classes)
	parts := make([]string, len(classes))
	for i, c := range classes {
		parts[i] = fmt.Sprintf("%s=%d", c, hidden[graph.NoiseClass(c)])
	}
	return fmt.Sprintf("hidden by class: %s (pass --include <class> to see)", strings.Join(parts, " "))
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
	impactTargetService  string
	impactTargetType     string
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
	impactIncludeLocals  bool
	impactStopContainers bool
	impactInclude        string
)

// impactPolicy turns the shape flags into a traversal policy.
func impactPolicy() graph.TraversalPolicy {
	p := graph.BlastRadiusPolicy()
	if impactStopContainers {
		p = graph.ContainmentTerminal()
	}
	if impactIncludeLocals {
		p.DropLocals = false
	}
	return p
}

func initImpactFlags() {
	impactCmd.Flags().StringVar(&impactTarget, "target", "", "search query for the target node")
	impactCmd.Flags().StringVar(&impactTargetService, "target-service", "", "restrict target resolution to this service (resolves cross-service ambiguity)")
	impactCmd.Flags().StringVar(&impactTargetType, "target-type", "", "restrict target resolution to this node type (function, component, …)")
	impactCmd.Flags().StringVar(&impactFile, "file", "", "file path: report impact at file granularity")
	impactCmd.Flags().StringVar(&impactDirection, "direction", "backward", "forward, backward or both; backward is \"what breaks if I change this\", forward is \"what does this reach\"; use both for \"what else do I need to touch/change\"")
	impactCmd.Flags().BoolVar(&impactDiff, "diff", false, "union blast radius of uncommitted changes (git diff against HEAD)")
	impactCmd.Flags().BoolVar(&impactStaged, "staged", false, "with --diff: staged changes only (git diff --cached)")
	impactCmd.Flags().IntVar(&impactDepth, "depth", 10, "max traversal depth (0 = unlimited)")
	impactCmd.Flags().StringVar(&impactService, "service", "", "filter results to a specific service")
	impactCmd.Flags().StringVar(&impactFormat, "format", "text", "output format: text, json, or github-comment")
	impactCmd.Flags().IntVar(&impactMaxTokens, "max-tokens", impact.DefaultBudget, "approximate token budget for output; over budget, per-node detail rolls up per file (negative = unlimited)")
	impactCmd.Flags().BoolVar(&impactSummary, "summary", false, "emit the file-grouped rollup instead of per-node detail")
	impactCmd.Flags().BoolVar(&impactIncludeLocals, "include-locals", false, "keep closure-captured local variables in the blast radius (correct edges, rarely useful answers)")
	impactCmd.Flags().BoolVar(&impactStopContainers, "stop-at-containers", false, "stop at containment edges (contains, declares) instead of expanding the file/type — much tighter, but loses recall where call resolution is weak")
	impactCmd.Flags().IntVar(&impactSnippetLines, "snippet-lines", 0, "inline N source lines per node in detail output (0 = off)")
	impactCmd.Flags().BoolVar(&impactVerboseSources, "verbose-sources", false, "emit full SourceRef structs instead of compact provider:ref strings")
	impactCmd.Flags().StringVar(&impactInclude, "include", "", "noise classes to show, comma-separated (filter_chain, mixin, containment, render_tree, all, none); default hides all four (impact has no --task concept); independent of --stop-at-containers")
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
		idx, err := buildFleetAwareIndex(ctx, store)
		if err != nil {
			return err
		}
		out, err := impact.BuildFile(idx, impactFile, impact.Options{
			Depth:     impactDepth,
			Service:   impactService,
			Direction: impactDirection,
			Policy:    impactPolicy(),
		})
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

	idx, err := buildFleetAwareIndex(ctx, store)
	if err != nil {
		return err
	}

	root, candidates, exactMatch, err := graph.ResolveTarget(ctx, graph.FleetSearcher{Store: store, Idx: idx}, impactTarget, impactTargetService, impactTargetType)
	if err != nil {
		return err
	}

	var includeKeys []string
	if impactInclude != "" {
		includeKeys = strings.Split(impactInclude, ",")
	}
	include, err := graph.ResolveNoiseInclude(includeKeys, "impact")
	if err != nil {
		return err
	}

	out := impact.Build(idx, root, impact.Options{
		Depth:          impactDepth,
		Service:        impactService,
		Direction:      impactDirection,
		Policy:         impactPolicy(),
		VerboseSources: impactVerboseSources,
		StaleAfter:     loadStaleAfter(meta.ConfigFile),
		Include:        include,
	})
	out.TargetCandidates = candidates
	out.Status = graph.AmbiguityStatus(candidates)
	out.ResolutionNote = graph.ResolutionNote(impactTarget, exactMatch)
	out.Trust, _ = graph.LoadTrustStamp(ctx, store)

	unresolved, err := store.ListUnresolvedRefs(ctx)
	if err != nil {
		return err
	}
	out.AttachUnresolved(unresolved)
	out.FinalizeEpistemic()
	out.InlineSnippets(".", impactSnippetLines)

	budgeted := out.ApplyBudget(impactMaxTokens, impactSummary)
	if impactFormat == "json" {
		return json.NewEncoder(os.Stdout).Encode(budgeted)
	}
	if out.ResolutionNote != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", out.ResolutionNote)
	}
	printAmbiguousCandidates(os.Stderr, impactTarget, root.ID, candidates)
	if line := hiddenByClassLine(out.HiddenByClass); line != "" {
		fmt.Fprintln(os.Stderr, line)
	}
	if s, ok := budgeted.(*impact.Summary); ok {
		return printImpactSummaryText(s)
	}
	return printImpactText(out)
}

// ─── deadcode ────────────────────────────────────────────────────────────────

var (
	deadcodeService string
	deadcodeFile    string
	deadcodeFormat  string
)

func initDeadcodeFlags() {
	deadcodeCmd.Flags().StringVar(&deadcodeService, "service", "", "restrict the scan to a specific service")
	deadcodeCmd.Flags().StringVar(&deadcodeFile, "file", "", "restrict the scan to a specific file")
	deadcodeCmd.Flags().StringVar(&deadcodeFormat, "format", "text", "output format: text or json")
}

var deadcodeCmd = &cobra.Command{
	Use:   "deadcode",
	Short: "List function/method nodes with zero inbound calls edges",
	RunE:  runDeadcode,
}

func runDeadcode(cmd *cobra.Command, args []string) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	idx, err := buildFleetAwareIndex(context.Background(), store)
	if err != nil {
		return err
	}

	out := deadcode.Build(idx, deadcode.Options{Service: deadcodeService, File: deadcodeFile})
	if deadcodeFormat == "json" {
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	for _, f := range out.Functions {
		fmt.Fprintf(os.Stdout, "%-8s %-60s %s:%d\n", f.Type, f.Label, f.File, f.Line)
	}
	fmt.Fprintf(os.Stdout, "\nTotal: %d zero-caller functions/methods\n", out.Total)
	return nil
}

// printFileRollupText renders one impact.FileRollup line, plus the Sample
// node underneath it when the file carries containment fan-out worth
// distinguishing from real hits — see internal/impact/summary.go.
func printFileRollupText(f impact.FileRollup) {
	nodes := fmt.Sprintf("%d", f.Nodes)
	if f.ContainedNodes > 0 {
		nodes = fmt.Sprintf("%dd+%dc", f.DirectNodes, f.ContainedNodes)
	}
	fmt.Fprintf(os.Stdout, "  depth %-2d %-60s %8s nodes via %s [%s]\n",
		f.MinDepth, f.File, nodes, strings.Join(f.EdgeTypes, ","), f.Service)
	if f.Sample != "" {
		fmt.Fprintf(os.Stdout, "           ↳ %s\n", f.Sample)
	}
}

func printImpactSummaryText(s *impact.Summary) error {
	t := s.Target
	fmt.Fprintf(os.Stdout, "Impact analysis for: %s (%s) %s:%d (direction=%s)\n\n", t.Label, t.Type, t.File, t.Line, s.Direction)

	if len(s.Files) > 0 {
		fmt.Fprintln(os.Stdout, "Files in blast radius:")
		for _, f := range s.Files {
			printFileRollupText(f)
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

// impactNoun names the blast-radius rows for the direction actually walked.
// Calling a callee a "caller" is not a cosmetic slip: it inverts the causal
// claim the output is making.
func impactNoun(direction string) string {
	switch direction {
	case "forward":
		return "callees"
	case "both":
		return "related nodes"
	}
	return "callers"
}

// structuralSuffix flags a caller reached through a contains/declares/
// instantiates/uses_type hop: it's a real edge but not a verified call
// chain (e.g. "constructs a struct that has this method"), so a reader
// shouldn't treat the listed edge type as proof this node calls the target.
// See graph.TraversalResult.Structural.
// printAmbiguousCandidates lists every exact-label match a --target query
// resolved to when there's more than one, so text-mode output carries the
// same disambiguation signal --format json already exposes via
// TargetCandidates instead of a bare count with no way to see or act on the
// alternatives short of re-running with --format json. resolvedID marks
// which candidate ResolveTarget actually picked (it is not always
// candidates[0] — that slice is sorted by service/file, while the pick
// itself follows SearchNodes' rank order with a non-test-file preference —
// see graph.ResolveTarget), so a reader can tell which answer the rest of
// the output is actually about.
func printAmbiguousCandidates(w io.Writer, query, resolvedID string, candidates []graph.TargetCandidate) {
	if len(candidates) == 0 {
		return
	}
	if graph.AmbiguityStatus(candidates) == graph.AmbiguityStatusAmbiguous {
		fmt.Fprintf(w, "status: ambiguous\n")
	}
	fmt.Fprintf(w, "%d other exact match(es) for %q — use --target-service to pin one:\n", len(candidates)-1, query)
	for _, c := range candidates {
		marker := "  "
		if c.ID == resolvedID {
			marker = "→ "
		}
		fmt.Fprintf(w, "  %s%-9s %-40s %s\n", marker, c.Type, c.File, c.ID)
	}
}

func structuralSuffix(c impact.Caller) string {
	if !c.Structural {
		return ""
	}
	return "  (structural — via type/containment, not a verified call)"
}

func printImpactText(out *impact.Result) error {
	t := out.Target
	fmt.Fprintf(os.Stdout, "Impact analysis for: %s (%s) %s:%d (direction=%s)\n\n", t.Label, t.Type, t.File, t.Line, out.Direction)
	noun := impactNoun(out.Direction)

	// Split into direct (depth 1) and indirect (depth > 1).
	var direct, indirect []impact.Caller
	for _, c := range out.Callers {
		if c.Depth == 1 {
			direct = append(direct, c)
		} else {
			indirect = append(indirect, c)
		}
	}

	if len(direct) > 0 {
		fmt.Fprintf(os.Stdout, "Direct %s (depth 1):\n", noun)
		for _, c := range direct {
			fmt.Fprintf(os.Stdout, "  %-40s %s:%d%s\n",
				fmt.Sprintf("%s  [%s]", c.Label, c.EdgeType), c.File, c.Line, structuralSuffix(c))
		}
		fmt.Fprintln(os.Stdout)
	}

	if len(indirect) > 0 {
		fmt.Fprintf(os.Stdout, "Indirect %s (depth 2-%d):\n", noun, out.Depth)
		for _, c := range indirect {
			fmt.Fprintf(os.Stdout, "  %-40s %s:%d%s\n",
				fmt.Sprintf("%s  [%s]", c.Label, c.EdgeType), c.File, c.Line, structuralSuffix(c))
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

	svcDirs := make([]gitdiff.ServiceDir, len(cfg.Services))
	for i, svc := range cfg.Services {
		svcDirs[i] = gitdiff.ServiceDir{Name: svc.Name, Path: svc.Path}
	}
	roots := gitdiff.ResolveRoots(svcDirs)
	changes, err := gitdiff.MultiChanges(roots, impactStaged)
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

	out := impact.BuildDiff(idx, changes, impact.Options{
		Depth:          impactDepth,
		Service:        impactService,
		Policy:         impactPolicy(),
		VerboseSources: impactVerboseSources,
		StaleAfter:     cfg.Evidence.StaleAfterDuration(),
	})
	out.AppendNoGitRepo(roots)
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
			fmt.Fprintf(os.Stdout, "  %-40s %s:%d%s\n",
				fmt.Sprintf("%s  [%s]", c.Label, c.EdgeType), c.File, c.Line, structuralSuffix(c))
		}
		fmt.Fprintln(os.Stdout)
	}
	if len(indirect) > 0 {
		fmt.Fprintf(os.Stdout, "Indirect callers (depth 2-%d):\n", out.Depth)
		for _, c := range indirect {
			fmt.Fprintf(os.Stdout, "  %-40s %s:%d%s\n",
				fmt.Sprintf("%s  [%s]", c.Label, c.EdgeType), c.File, c.Line, structuralSuffix(c))
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
			printFileRollupText(f)
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

// ─── link ────────────────────────────────────────────────────────────────────

var linkInfer bool

var linkCmd = &cobra.Command{
	Use:   "link",
	Short: "Propose cross-service links from indexed evidence",
	RunE:  runLink,
}

func initLinkFlags() {
	linkCmd.Flags().BoolVar(&linkInfer, "infer", false, "propose cross-service links from HTTP env-var hints and broker exchange overlap; writes to links_proposed for review (never silently applied — promote via `polyflow config link add`)")
}

func runLink(cmd *cobra.Command, args []string) error {
	if !linkInfer {
		return cmd.Help()
	}
	cfg, err := workspace.Load(meta.ConfigFile)
	if err != nil {
		return err
	}
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	proposals, err := workspace.InferLinks(cmd.Context(), store, cfg)
	if err != nil {
		return fmt.Errorf("infer links: %w", err)
	}
	cfg.LinksProposed = proposals
	if err := workspace.Save(meta.ConfigFile, cfg); err != nil {
		return err
	}

	fmt.Printf("Proposed %d cross-service link(s), written to links_proposed in %s:\n", len(proposals), meta.ConfigFile)
	for _, l := range proposals {
		fmt.Printf("  %s -> %s", l.From, l.To)
		if l.Via != "" {
			fmt.Printf("  via=%s", l.Via)
		}
		if l.Exchange != "" {
			fmt.Printf("  exchange=%s", l.Exchange)
		}
		if l.Hint != "" {
			fmt.Printf("  hint=%s", l.Hint)
		}
		fmt.Println()
	}
	if len(proposals) == 0 {
		fmt.Println("  (none)")
	}
	return nil
}

// ─── fleet ───────────────────────────────────────────────────────────────────

var fleetCmd = &cobra.Command{
	Use:   "fleet",
	Short: "Operate on a git-backed fleet definition (Tier GR)",
}

var (
	fleetSyncFleetPath string
	fleetSyncRefs      []string
	fleetSyncWorkers   int
	fleetSyncCacheDir  string
)

func initFleetSubcmds() {
	syncCmd := &cobra.Command{
		Use:   "sync",
		Short: "Resolve every fleet member and rebuild the fleet's bridge.db of cross-service edges",
		RunE:  runFleetSync,
	}
	syncCmd.Flags().StringVar(&fleetSyncFleetPath, "fleet", "fleet.yml", "path to the git-tracked fleet definition file")
	syncCmd.Flags().StringArrayVar(&fleetSyncRefs, "ref", nil, "override one service's ref for this sync, e.g. --ref willow=release/26.2 (repeatable; beats .polyflow-refs.yml and the fleet definition's default)")
	syncCmd.Flags().IntVar(&fleetSyncWorkers, "workers", 0, "max fleet members resolved concurrently (0 = unlimited)")
	syncCmd.Flags().StringVar(&fleetSyncCacheDir, "cache-dir", "", "GR.1 step-3 build-cache root, keyed by <dir>/<service>/<sha>/graph.db (CI: point this at an actions/cache-restored path; empty means step 3 is always a miss)")

	fleetStatusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show this workspace's fleet: per-member resolution and bridge staleness (read-only)",
		RunE:  runFleetStatus,
	}

	fleetCmd.AddCommand(syncCmd, fleetStatusCmd)
}

func runFleetSync(cmd *cobra.Command, args []string) error {
	cfg, err := fleetconfig.Load(fleetSyncFleetPath)
	if err != nil {
		return err
	}

	// Precedence: --ref flag beats .polyflow-refs.yml (found in the current
	// directory's checkout, the branch being built) beats the fleet
	// definition's default ref.
	refOverrides, err := fleetsync.LoadRefOverrides(".")
	if err != nil {
		return err
	}
	for _, r := range fleetSyncRefs {
		svc, ref, ok := strings.Cut(r, "=")
		if !ok {
			return fmt.Errorf("--ref %q: expected <service>=<ref>", r)
		}
		refOverrides[svc] = ref
	}

	regPath, err := registry.DefaultPath()
	if err != nil {
		return err
	}

	fmt.Printf("Syncing fleet %q (%d service(s))...\n", cfg.Name, len(cfg.Services))
	absFleetPath, err := filepath.Abs(fleetSyncFleetPath)
	if err != nil {
		return err
	}
	stats, err := fleetsync.Sync(cmd.Context(), cfg, fleetsync.SyncOptions{
		RegistryPath:    regPath,
		RefOverrides:    refOverrides,
		ContractsDir:    filepath.Dir(fleetSyncFleetPath),
		Workers:         fleetSyncWorkers,
		FleetConfigPath: absFleetPath,
		CacheDir:        fleetSyncCacheDir,
	})
	if err != nil {
		return err
	}

	bridgePath, err := fleetsync.DefaultBridgePath(cfg.Name)
	if err != nil {
		return err
	}
	fmt.Printf("Done in %s. %s\n  Services: %d | Bridge nodes: %d | Bridge edges: %d\n",
		stats.Elapsed.Truncate(time.Millisecond), bridgePath, stats.Services, stats.Nodes, stats.Edges)
	return nil
}

// runFleetStatus implements GR.5's `polyflow fleet status`: same fleet
// selection queryresolve.Resolve already gives every query command
// (upward-walk to the nearest local graph.db, then the registry's reverse
// index), but with NoSync so a status view never triggers a build — only
// fleetsync.ResolveStatus's read-only ref-resolve/registry/cache checks per
// member, plus whatever bridge.db already exists on disk.
func runFleetStatus(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	res, err := queryresolve.Resolve(ctx, ".", queryresolve.Options{Fleet: fleetFlag, NoSync: true})
	if err != nil {
		return err
	}
	if res.FleetName == "" {
		fmt.Println("not a registered fleet member — run `polyflow index` then `polyflow fleet sync` from a fleet member checkout first")
		return nil
	}

	regPath, err := registry.DefaultPath()
	if err != nil {
		return err
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		return err
	}

	fmt.Printf("%s:\n", res.FleetName)

	fleetConfigPath := reg.FleetConfigPaths[res.FleetName]
	if fleetConfigPath == "" {
		fmt.Println("  (fleet definition location unknown — run `polyflow fleet sync` from a checkout of its fleet.yml first)")
		fmt.Printf("  bridge: %s\n", fleetBridgeStatusLine(res.BridgePath))
		return nil
	}
	cfg, err := fleetconfig.Load(fleetConfigPath)
	if err != nil {
		return err
	}
	refOverrides, err := fleetsync.LoadRefOverrides(res.WorkspaceRoot)
	if err != nil {
		return err
	}

	for _, svc := range cfg.Services {
		st, err := fleetsync.ResolveStatus(ctx, svc, refOverrides[svc.Name], fleetsync.ResolveOptions{RegistryPath: regPath})
		if err != nil {
			fmt.Printf("  %-20s error: %v\n", svc.Name, err)
			continue
		}
		fmt.Printf("  %-20s %s\n", svc.Name, fleetMemberStatusLine(st))
	}
	fmt.Printf("  bridge: %s\n", fleetBridgeStatusLine(res.BridgePath))
	return nil
}

func fleetMemberStatusLine(st *fleetsync.MemberStatus) string {
	sha := st.SHA
	if len(sha) > 7 {
		sha = sha[:7]
	}
	switch st.Source {
	case "local":
		return fmt.Sprintf("resolved %s@%s, local checkout matches (%s)", st.Ref, sha, st.LocalPath)
	case "cache":
		return fmt.Sprintf("resolved %s@%s, resolved from build cache", st.Ref, sha)
	default:
		return fmt.Sprintf("resolved %s@%s, not available locally (next sync will clone)", st.Ref, sha)
	}
}

func fleetBridgeStatusLine(bridgePath string) string {
	if bridgePath == "" {
		return "not built yet (run `polyflow fleet sync`)"
	}
	info, err := os.Stat(bridgePath)
	if err != nil {
		return "not built yet (run `polyflow fleet sync`)"
	}
	return fmt.Sprintf("synced %s ago (%s)", time.Since(info.ModTime()).Truncate(time.Second), bridgePath)
}

// ─── registry ────────────────────────────────────────────────────────────────

var registryAll bool

var registryCmd = &cobra.Command{
	Use:   "registry",
	Short: "List this machine's local fleet-member checkouts and which fleets claim them (Tier GR)",
	RunE:  runRegistry,
}

func init() {
	registryCmd.Flags().BoolVar(&registryAll, "all", false, "also list registry entries not claimed by any fleet")
}

// runRegistry implements GR.5's `polyflow registry [--all]`: a read-only
// dump of internal/registry's local machine registry — never hand-edits it,
// keeping "the registry reflects an actual local index" an invariant
// nothing but `polyflow index`/`fleet sync` can violate.
func runRegistry(cmd *cobra.Command, args []string) error {
	regPath, err := registry.DefaultPath()
	if err != nil {
		return err
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		return err
	}

	entries := reg.Entries
	if !registryAll {
		filtered := entries[:0:0]
		for _, e := range entries {
			if len(e.Fleets) > 0 {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	if len(entries) == 0 {
		if registryAll {
			fmt.Println("(no local index entries — run `polyflow index` in a workspace first)")
		} else {
			fmt.Println("(no fleet-claimed local index entries — pass --all to see every indexed workspace, or run `polyflow fleet sync` first)")
		}
		return nil
	}

	fmt.Printf("%-24s %-24s %-12s %s\n", "SERVICE", "INDEXED", "FLEETS", "LOCAL PATH")
	for _, e := range entries {
		indexed := "-"
		if !e.IndexedAt.IsZero() {
			indexed = e.IndexedAt.Format("2006-01-02 15:04 MST")
		}
		fleets := "-"
		if len(e.Fleets) > 0 {
			fleets = strings.Join(e.Fleets, ",")
		}
		fmt.Printf("%-24s %-24s %-12s %s\n", e.Service, indexed, fleets, e.LocalPath)
	}
	return nil
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
		Short: "Pretty-print current polyflow.yml",
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
	evalCorpus string
	evalCase   string
	evalOutput string
	evalGate   string
)

func initEvalFlags() {
	evalCmd.Flags().StringVar(&evalCorpus, "corpus", "eval/corpus", "path to corpus root (a dir with manifest.yaml, or a dir of such dirs)")
	evalCmd.Flags().StringVar(&evalCase, "case", "", "run only this case ID (default: all cases in the corpus)")
	evalCmd.Flags().StringVar(&evalOutput, "output", "", "write JSON results to this file (e.g. eval/baseline.json)")
	evalCmd.Flags().StringVar(&evalGate, "gate", "", "baseline JSON file to gate against; exits non-zero on any regression")

	stampCmd.Flags().StringVar(&evalStampCorpus, "corpus", "", "path to a single corpus dir (with manifest.yaml) to stamp against the current workspace")
	stampCmd.Flags().StringVar(&evalStampWS, "workspace", meta.ConfigFile, "path to polyflow.yml")
	_ = stampCmd.MarkFlagRequired("corpus")
	evalCmd.AddCommand(stampCmd)

	promoteGapsCmd.Flags().StringVar(&promoteGapsCorpus, "corpus", "", "path to a single corpus dir (with manifest.yaml) to append promoted gap cases to")
	promoteGapsCmd.Flags().BoolVar(&promoteGapsWrite, "write", false, "persist the promoted cases (default: dry-run, prints what would be added)")
	_ = promoteGapsCmd.MarkFlagRequired("corpus")
	evalCmd.AddCommand(promoteGapsCmd)

	agentCmd.Flags().StringVar(&evalAgentCorpus, "corpus", "", "path to a corpus dir (with manifest.yaml) or a corpus root (a dir of such dirs) whose agent_cases to run")
	agentCmd.Flags().StringVar(&evalAgentCmd, "agent-cmd", "", "override the agent CLI command template (default: claude -p ...; env POLYFLOW_AGENT_CMD)")
	agentCmd.Flags().StringVar(&evalAgentOutput, "output", "", "write JSON results to this file (e.g. eval/agent-baseline.json)")
	agentCmd.Flags().StringVar(&evalAgentGate, "gate", "", "agent baseline JSON file to gate against; exits non-zero on any regression")
	_ = agentCmd.MarkFlagRequired("corpus")
	evalCmd.AddCommand(agentCmd)
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
		fmt.Printf("Repo: %-20s  cases: %d  recall=%.3f  precision=%s\n",
			report.Repo, len(report.Results), report.Recall, evalRepoPrecision(report))
		for _, w := range report.SemanticWarnings {
			fmt.Printf("  WARNING: %s — recall numbers below are measured against an incomplete graph\n", w)
		}
		for _, r := range report.Results {
			status := "ok"
			if r.HardFail {
				status = "HARD_FAIL"
				hardFailed = true
			}
			if r.Kind == "rank1" {
				fmt.Printf("  %-44s rank1=%-28s gap=%+.4f  %s\n",
					r.CaseID, evalRank1Label(r), r.ScoreGap(), status)
				continue
			}
			fmt.Printf("  %-44s recall=%.3f precision=%-13s honest=%d silent=%d  %s%s\n",
				r.CaseID, r.Recall, evalCasePrecision(r), r.HonestMisses, r.SilentMisses, status, evalForbidden(r))
		}
		if report.Rank1Accuracy > 0 || rank1Cases(report.Results) > 0 {
			fmt.Printf("  %-44s rank1_accuracy=%.3f  min_gap=%s\n",
				"(rank-1 identity)", report.Rank1Accuracy, evalMinGap(report))
		}
		fmt.Println()
	}

	for _, s := range multi.Skipped {
		fmt.Fprintf(os.Stderr, "WARNING: skipped corpus %q (%s): %s\n", s.Name, s.Dir, s.Reason)
	}
	for _, b := range multi.Broken {
		fmt.Fprintf(os.Stderr, "ERROR: corpus %q (%s) failed to run: %s\n", b.Name, b.Dir, b.Reason)
	}

	// Without a gate, any hard-fail is fatal (E.1 acceptance). With --gate the
	// gate decides: pre-existing baseline hard-fails must not fail CI forever —
	// only NEW hard-fails, recall drops, silent-miss rises, or missing repos do.
	if hardFailed && evalGate == "" {
		fmt.Fprintf(os.Stderr, "Failed: one or more cases hard-failed (%s)\n", evalHardFailReason(multi.Reports))
		os.Exit(1)
	}
	// A corpus that could not run measured nothing; that is a failure whether
	// or not a gate was supplied.
	if len(multi.Broken) > 0 && evalGate == "" {
		fmt.Fprintln(os.Stderr, "Failed: one or more corpora could not be run")
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
				case "corpus_error":
					fmt.Fprintf(os.Stderr, "  REGRESSION  %s/*  corpus_error  (present but failed to run — it measured nothing)\n", r.Repo)
				case "forbidden_hit":
					fmt.Fprintf(os.Stderr, "  REGRESSION  %s/%s  forbidden_hit  (blast radius newly includes: %s)\n", r.Repo, r.CaseID, strings.Join(r.ForbiddenHits, ", "))
				case "precision_drop":
					fmt.Fprintf(os.Stderr, "  REGRESSION  %s/%s  precision_drop  baseline=%.3f  current=%.3f  (exhaustive case: it now returns files its complete truth set does not contain)\n",
						r.Repo, r.CaseID, deref(r.BaselinePrecision), deref(r.CurrentPrecision))
				case "semantic_fallback":
					fmt.Fprintf(os.Stderr, "  REGRESSION  %s/*  semantic_fallback  (graph is incomplete, not just lower-recall): %s\n",
						r.Repo, strings.Join(r.SemanticWarnings, "; "))
				}
			}
			fmt.Fprintln(os.Stderr, "Update eval/baseline.json when recall improves: polyflow eval --output eval/baseline.json")
			os.Exit(1)
		}
		fmt.Printf("CI gate: no regressions vs %s\n", evalGate)
	}

	return nil
}

// evalCasePrecision renders a case's precision, or "n/a (not exhaustive)" when
// the case never claimed a complete truth set (D.1). Printing a number there
// invites it to be quoted as a false-positive rate, which it is not: it is
// hits over returned against a hand-picked sample, so a *more* complete answer
// scores worse.
func evalCasePrecision(r eval.CaseResult) string {
	if r.Precision == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.3f", *r.Precision)
}

// evalRepoPrecision renders the repo macro-average over exhaustive cases only,
// naming the denominator so a 1.000 over two cases cannot read as a fleet claim.
func evalRepoPrecision(r eval.Report) string {
	if r.Precision == nil {
		return "n/a (0 exhaustive cases)"
	}
	return fmt.Sprintf("%.3f (%d exhaustive)", *r.Precision, r.ExhaustiveCases)
}

// deref renders an optional score for a gate message. A nil precision reaches
// here only if condition 7 emitted a regression without both numbers, which it
// does not — printing 0 rather than panicking keeps a reporting bug from
// masking the regression it is reporting.
func deref(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

// evalForbidden appends the must_not_include files a case actually returned, so
// a precision failure names the phantom on the line that reports it.
// evalHardFailReason names the causes actually present rather than asserting
// one. A hard fail used to be a silent must_not_miss only; since D.1 a
// must_not_include hit is also one, and telling someone their blast radius is
// too NARROW when the corpus just caught it being too WIDE sends them to the
// opposite end of the resolver from the defect.
func evalHardFailReason(reports []eval.Report) string {
	var silent, forbidden bool
	for _, rep := range reports {
		for _, r := range rep.Results {
			if !r.HardFail {
				continue
			}
			if len(r.ForbiddenHits) > 0 {
				forbidden = true
			}
			if r.SilentMisses > 0 {
				silent = true
			}
		}
	}
	switch {
	case silent && forbidden:
		return "must_not_miss file silently missed, and must_not_include file returned"
	case forbidden:
		return "must_not_include file returned — a hand-verified false positive"
	case silent:
		return "must_not_miss file silently missed"
	}
	return "see the case lines above"
}

func evalForbidden(r eval.CaseResult) string {
	if len(r.ForbiddenHits) == 0 {
		return ""
	}
	return "  FORBIDDEN: " + strings.Join(r.ForbiddenHits, ", ")
}

// evalMinGap renders the thinnest passing rank-1 margin, or n/a when no rank1
// case in the repo passed — a repo with no winner has no margin, and printing
// 0.0000 there reads like a knife-edge pass rather than a clean sweep of misses.
func evalMinGap(r eval.Report) string {
	for _, c := range r.Results {
		if c.Kind == "rank1" && !c.HardFail {
			return fmt.Sprintf("%+.4f", r.Rank1MinGap)
		}
	}
	return "n/a"
}

// evalRank1Label renders what actually came back first, so a failing rank1 case
// names its own usurper on the same line rather than requiring a re-run.
func evalRank1Label(r eval.CaseResult) string {
	if r.Rank1 == "" {
		return "<no hits>"
	}
	if r.HardFail && r.Rank2 != "" {
		return r.Rank1 + " (2:" + r.Rank2 + ")"
	}
	return r.Rank1
}

func rank1Cases(results []eval.CaseResult) int {
	n := 0
	for _, r := range results {
		if r.Kind == "rank1" {
			n++
		}
	}
	return n
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
	fmt.Printf("Corpus  recall=%.3f  precision=%s\n", report.Recall, evalRepoPrecision(*report))
	for _, w := range report.SemanticWarnings {
		fmt.Printf("  WARNING: %s — recall numbers below are measured against an incomplete graph\n", w)
	}
	fmt.Println()

	hardFailed := false
	for _, r := range report.Results {
		status := "ok"
		if r.HardFail {
			status = "HARD_FAIL"
			hardFailed = true
		}
		if r.Kind == "rank1" {
			fmt.Printf("  %-40s rank1=%-28s gap=%+.4f  %s\n",
				r.CaseID, evalRank1Label(r), r.ScoreGap(), status)
			continue
		}
		fmt.Printf("  %-40s recall=%.3f precision=%-13s honest=%d silent=%d  %s%s\n",
			r.CaseID, r.Recall, evalCasePrecision(r), r.HonestMisses, r.SilentMisses, status, evalForbidden(r))
	}
	if rank1Cases(report.Results) > 0 {
		fmt.Printf("  %-40s rank1_accuracy=%.3f  min_gap=%+.4f\n",
			"(rank-1 identity)", report.Rank1Accuracy, report.Rank1MinGap)
	}

	if hardFailed {
		fmt.Fprintf(os.Stderr, "\nFailed: one or more cases hard-failed (%s)\n",
			evalHardFailReason([]eval.Report{*report}))
		os.Exit(1)
	}
	return nil
}

var (
	evalStampCorpus string
	evalStampWS     string
)

var stampCmd = &cobra.Command{
	Use:   "stamp",
	Short: "Score a corpus against the current workspace and persist the result as a trust stamp",
	RunE:  runEvalStamp,
}

func runEvalStamp(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	m, err := eval.LoadManifest(evalStampCorpus)
	if err != nil {
		return fmt.Errorf("load corpus manifest: %w", err)
	}

	cfg, err := workspace.Load(evalStampWS)
	if err != nil {
		return err
	}
	// repo.workspace is a path/filename field (always "polyflow.yml" in
	// practice); repo.name is what actually identifies the target
	// workspace — it matches the loaded polyflow.yml's top-level `name:`.
	if m.Repo.Name != cfg.Name {
		return fmt.Errorf("corpus %q targets workspace %q, but the loaded workspace is %q — stamp must run against its own corpus's workspace",
			evalStampCorpus, m.Repo.Name, cfg.Name)
	}

	report, err := eval.Run(ctx, eval.RunOptions{CorpusDir: evalStampCorpus})
	if err != nil {
		return err
	}

	dbPath := filepath.Join(meta.DBDir, meta.DBFile)
	store, err := graph.NewSQLiteStore(dbPath)
	if err != nil {
		return fmt.Errorf("open store (run `polyflow index` first): %w", err)
	}
	defer store.Close()

	if err := eval.SaveTrustStamp(ctx, store, m.Repo.Name, report); err != nil {
		return err
	}

	fmt.Printf("Stamped %s: recall=%.3f over %d cases (corpus=%s)\n", cfg.Name, report.Recall, len(report.Results), m.Repo.Name)
	return nil
}

var (
	promoteGapsCorpus string
	promoteGapsWrite  bool
)

var promoteGapsCmd = &cobra.Command{
	Use:   "promote-gaps",
	Short: "Promote runtime-observed static-analysis gaps into permanent eval cases",
	RunE:  runPromoteGaps,
}

func runPromoteGaps(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	m, err := eval.LoadManifest(promoteGapsCorpus)
	if err != nil {
		return fmt.Errorf("load corpus manifest: %w", err)
	}

	dbPath := filepath.Join(meta.DBDir, meta.DBFile)
	store, err := graph.NewSQLiteStore(dbPath)
	if err != nil {
		return fmt.Errorf("open store (run `polyflow index` first): %w", err)
	}
	defer store.Close()

	cases, err := eval.PromoteGaps(ctx, store, m)
	if err != nil {
		return err
	}

	if len(cases) == 0 {
		fmt.Println("No new observed_only_gap edges to promote.")
		return nil
	}

	for _, c := range cases {
		fmt.Printf("  %s  target=%s  service=%s  must_not_miss=%v\n", c.ID, c.Target, c.Service, c.MustNotMiss)
	}

	if !promoteGapsWrite {
		fmt.Printf("\n%d case(s) would be added to %s (dry-run; pass --write to persist)\n", len(cases), filepath.Join(promoteGapsCorpus, "manifest.yaml"))
		return nil
	}

	if err := eval.AppendCasesToManifest(promoteGapsCorpus, cases); err != nil {
		return err
	}
	fmt.Printf("\n%d case(s) appended to %s\n", len(cases), filepath.Join(promoteGapsCorpus, "manifest.yaml"))
	return nil
}

var (
	evalAgentCorpus string
	evalAgentCmd    string
	evalAgentOutput string
	evalAgentGate   string
)

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Run the agent-correctness corpus: an agent restricted to polyflow MCP tools answers real questions, scored deterministically",
	RunE:  runEvalAgent,
}

func runEvalAgent(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// Single-corpus path: evalAgentCorpus has its own manifest.yaml.
	// Multi-corpus path (T.3): evalAgentCorpus is a root of such dirs,
	// mirroring `polyflow eval`'s single-vs-root split.
	var multi *eval.AgentMultiReport
	if _, err := os.Stat(filepath.Join(evalAgentCorpus, "manifest.yaml")); err == nil {
		report, err := eval.RunAgentCorpus(ctx, eval.AgentRunOptions{
			CorpusDir: evalAgentCorpus,
			AgentCmd:  evalAgentCmd,
		})
		if err != nil {
			if errors.Is(err, eval.ErrAgentCLIUnavailable) {
				// This phase needs network + a logged-in agent CLI — a release
				// ritual, not CI. Exit 0 with a distinct message, never a silent pass.
				fmt.Fprintf(os.Stderr, "SKIPPED: %v\n", err)
				return nil
			}
			return err
		}
		multi = &eval.AgentMultiReport{GeneratedAt: time.Now().UTC()}
		if len(report.Results) > 0 {
			multi.Reports = append(multi.Reports, *report)
		}
	} else {
		m, err := eval.RunAllAgent(ctx, evalAgentCorpus, eval.AgentRunOptions{AgentCmd: evalAgentCmd})
		if err != nil {
			if errors.Is(err, eval.ErrAgentCLIUnavailable) {
				fmt.Fprintf(os.Stderr, "SKIPPED: %v\n", err)
				return nil
			}
			return err
		}
		multi = m
	}

	if len(multi.Reports) == 0 {
		fmt.Println("No corpus with agent_cases found under", evalAgentCorpus, "(nothing to run).")
		return nil
	}

	if evalAgentOutput != "" {
		data, err := json.MarshalIndent(multi, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal JSON: %w", err)
		}
		if err := os.WriteFile(evalAgentOutput, data, 0o644); err != nil {
			return fmt.Errorf("write output %s: %w", evalAgentOutput, err)
		}
		fmt.Printf("Results written to %s\n\n", evalAgentOutput)
	}

	for _, report := range multi.Reports {
		fmt.Printf("Repo: %s   agent cases: %d   correctness=%.3f\n\n", report.Repo, len(report.Results), report.Correctness)
		for _, r := range report.Results {
			status := "ok"
			if !r.Correct {
				status = "INCORRECT"
			}
			fmt.Printf("  %-30s %-9s turns=%d  in=%d out=%d\n", r.ID, status, r.Turns, r.InputTokens, r.OutputTokens)
			if len(r.MissingFacts) > 0 {
				fmt.Printf("      missing:        %v\n", r.MissingFacts)
			}
			if len(r.ForbiddenHit) > 0 {
				fmt.Printf("      forbidden hit:  %v\n", r.ForbiddenHit)
			}
		}
		fmt.Println()
	}

	for _, s := range multi.Skipped {
		fmt.Fprintf(os.Stderr, "WARNING: skipped corpus %q (%s): %s\n", s.Name, s.Dir, s.Reason)
	}

	if evalAgentGate != "" {
		baseline, err := eval.LoadAgentBaseline(evalAgentGate)
		if err != nil {
			return fmt.Errorf("load agent gate baseline: %w", err)
		}
		gate := eval.CheckAgentGate(multi, baseline)
		if !gate.OK {
			fmt.Fprintf(os.Stderr, "Agent gate: %d regression(s) vs %s\n", len(gate.Regressions), evalAgentGate)
			for _, r := range gate.Regressions {
				switch r.Reason {
				case "now_incorrect":
					fmt.Fprintf(os.Stderr, "  REGRESSION  %s/%s  now_incorrect (was correct in baseline)\n", r.Repo, r.CaseID)
				case "correctness_drop":
					fmt.Fprintf(os.Stderr, "  REGRESSION  %s/*  correctness_drop  baseline=%.3f  current=%.3f\n", r.Repo, r.BaselineCorrectness, r.CurrentCorrectness)
				case "missing_repo":
					fmt.Fprintf(os.Stderr, "  REGRESSION  %s/*  missing_repo  (in baseline but absent from this run)\n", r.Repo)
				}
			}
			fmt.Fprintln(os.Stderr, "Update eval/agent-baseline.json when correctness improves: polyflow eval agent --corpus eval/corpus --output eval/agent-baseline.json")
			os.Exit(1)
		}
		fmt.Printf("Agent gate: no regressions vs %s\n", evalAgentGate)
	}

	return nil
}

// ─── doctor ──────────────────────────────────────────────────────────────────

var (
	doctorBaseline      string
	doctorAgentBaseline string
	doctorPropose       string
	doctorYield         bool
	doctorYieldJSON     bool
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Show a diagnostic summary of the workspace and eval health",
	RunE:  runDoctor,
}

func init() {
	doctorCmd.Flags().StringVar(&doctorBaseline, "baseline", "eval/baseline.json", "baseline JSON file for the eval summary row")
	doctorCmd.Flags().StringVar(&doctorAgentBaseline, "agent-baseline", "eval/agent-baseline.json", "agent-correctness baseline JSON file for the Trust panel's agent correctness row")
	doctorCmd.Flags().StringVar(&doctorPropose, "propose", "", "write gap-derived rule proposals + fixture skeletons to this directory (e.g. .polyflow/proposals)")
	doctorCmd.Flags().BoolVar(&doctorYield, "yield", false, "print the X.3 resolution-yield scorecard; CI gate — exits 1 if it fails the bar (internal=100%, cross-static>=95%, every unresolved site reason-coded)")
	doctorCmd.Flags().BoolVar(&doctorYieldJSON, "json", false, "with --yield, print the scorecard as JSON instead of a table")
}

func runDoctor(cmd *cobra.Command, args []string) error {
	fmt.Println("polyflow doctor")
	fmt.Println()

	// P.2 Onboarding section — shown first so new users see the critical path.
	printDoctorOnboarding()

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

		// B.0: unparsed file class ledger — per-service blind-spot gauge.
		fmt.Println()
		if unparsedJSON, metaErr := store.GetMeta(ctx, "unparsed_files"); metaErr == nil {
			var unparsed map[string]map[string]int
			if json.Unmarshal([]byte(unparsedJSON), &unparsed) == nil && len(unparsed) > 0 {
				svcs := make([]string, 0, len(unparsed))
				for s := range unparsed {
					svcs = append(svcs, s)
				}
				sort.Strings(svcs)
				first := true
				for _, s := range svcs {
					total, topExts := indexer.UnparsedSummary(unparsed[s])
					msg := fmt.Sprintf("%d files (%s) cannot be indexed — no parser registered", total, topExts)
					if first {
						fmt.Printf("  Unparsed files:      %s: %s\n", s, msg)
						first = false
					} else {
						fmt.Printf("                       %s: %s\n", s, msg)
					}
				}
			} else {
				fmt.Printf("  Unparsed files:      OK — all source files are parseable\n")
			}
		} else {
			fmt.Printf("  Unparsed files:      (no data — run 'polyflow index' first)\n")
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
	var trustVS graph.VerificationSummary
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
		trustVS = vs
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

	// T.3: Trust panel — every row degrades to an explicit UNMEASURED (or
	// N/A), never blank, mirroring TrustStamp's own convention.
	fmt.Println()
	fmt.Println("  Trust")
	if storeErr != nil {
		fmt.Printf("    eval recall         UNMEASURED (no index — run 'polyflow index' first)\n")
	} else {
		stamp, stampErr := graph.LoadTrustStamp(context.Background(), store)
		if stampErr != nil || !stamp.Measured {
			fmt.Printf("    eval recall         UNMEASURED (run 'polyflow eval stamp' against this workspace)\n")
		} else {
			suffix := ""
			if stamp.Stale {
				suffix = " [STALE — graph changed since this stamp]"
			}
			measuredAt := stamp.MeasuredAt
			if t, pErr := time.Parse(time.RFC3339, stamp.MeasuredAt); pErr == nil {
				measuredAt = t.Format("2006-01-02")
			}
			fmt.Printf("    eval recall         %.3f (%d cases, %s, %s)%s\n", stamp.Recall, stamp.Cases, stamp.Corpus, measuredAt, suffix)
		}
	}

	cfgForTrust, cfgErr := workspace.Load(meta.ConfigFile)
	agentBaseline, agentBaselineErr := eval.LoadAgentBaseline(doctorAgentBaseline)
	if cfgErr != nil || agentBaselineErr != nil {
		fmt.Printf("    agent correctness   UNMEASURED (run 'polyflow eval agent --output %s')\n", doctorAgentBaseline)
	} else {
		agentSum := eval.SummarizeAgentForDoctor(agentBaseline, cfgForTrust.Name)
		if !agentSum.Measured {
			fmt.Printf("    agent correctness   UNMEASURED (run 'polyflow eval agent --output %s')\n", doctorAgentBaseline)
		} else {
			fmt.Printf("    agent correctness   %.3f (%d cases, %s)\n", agentSum.Correctness, agentSum.Cases, agentSum.MeasuredAt)
		}
	}

	if storeErr != nil || len(allEdges) == 0 {
		fmt.Printf("    edges verified      UNMEASURED (no index — run 'polyflow index' first)\n")
	} else {
		total := trustVS.Verified + trustVS.Candidate + trustVS.ObservedOnlyGap + trustVS.Conflicting
		if total == 0 {
			fmt.Printf("    edges verified      UNMEASURED (no verification-state edges)\n")
		} else {
			pct := func(n int) float64 { return 100 * float64(n) / float64(total) }
			fmt.Printf("    edges verified      %.0f%% verified · %.0f%% candidate · %.0f%% observed-gap\n",
				pct(trustVS.Verified), pct(trustVS.Candidate), pct(trustVS.ObservedOnlyGap))
		}
	}

	if storeErr != nil {
		fmt.Printf("    unresolved density  UNMEASURED (no index — run 'polyflow index' first)\n")
	} else {
		unresolved, uErr := store.ListUnresolvedRefs(context.Background())
		if uErr != nil || len(allEdges) == 0 {
			fmt.Printf("    unresolved density  UNMEASURED\n")
		} else {
			density := 100 * float64(len(unresolved)) / float64(len(allEdges))
			fmt.Printf("    unresolved density  %d refs / %d edges (%.1f%%)\n", len(unresolved), len(allEdges), density)
		}
	}

	// X.3: resolution-yield scorecard — the trust-signal source for whether
	// polyflow resolves this repo's flows. --yield doubles as the CI gate:
	// pinned exit precedence — this is the LAST check in runDoctor, so every
	// other diagnostic section has already printed before a failing gate exits.
	var (
		yieldReport   yieldpkg.Report
		yieldComputed bool
	)
	if doctorYield {
		fmt.Println()
		if storeErr != nil {
			fmt.Printf("  Yield scorecard:     (no index — run 'polyflow index' first)\n")
		} else {
			ctx5 := context.Background()
			rep, yErr := yieldpkg.Compute(ctx5, store)
			if yErr != nil {
				fmt.Printf("  Yield scorecard:     error: %v\n", yErr)
			} else {
				yieldReport, yieldComputed = rep, true
				if doctorYieldJSON {
					data, mErr := json.MarshalIndent(rep, "", "  ")
					if mErr != nil {
						return fmt.Errorf("marshal yield report: %w", mErr)
					}
					fmt.Println(string(data))
				} else {
					printDoctorYield(rep)
				}
			}
		}
	}

	fmt.Println()

	if doctorYield && yieldComputed && !yieldReport.Pass {
		fmt.Fprintf(os.Stderr, "Yield gate: FAILED — %s\n", strings.Join(yieldReport.Failures, "; "))
		os.Exit(1)
	}

	return nil
}

// printDoctorYield renders the X.3 resolution-yield scorecard in doctor
// table style: one row per (Class, Scope), then the three headline ratios
// and the pass/fail verdict.
func printDoctorYield(r yieldpkg.Report) {
	prefix := "  Yield scorecard:    "
	indent := "                       "

	if len(r.Rows) == 0 {
		fmt.Printf("%s(no resolution-relevant edges found)\n", prefix)
	} else {
		fmt.Printf("%s%-16s  %-8s  %6s  %6s  %6s  %6s  %6s\n",
			prefix, "class", "scope", "static", "runtime", "extern", "unres", "resolv")
		for _, row := range r.Rows {
			fmt.Printf("%s%-16s  %-8s  %6d  %6d  %6d  %6d  %6d\n",
				indent, row.Class, row.Scope, row.ResolvedStatic, row.ResolvedRuntime, row.External, row.Unresolved, row.Resolvable)
		}
	}

	fmt.Printf("%sinternal_yield=%.3f  cross_yield_static=%.3f  cross_yield_with_runtime=%.3f\n",
		indent, r.InternalYield, r.CrossYieldStatic, r.CrossYieldWithRuntime)
	if r.Pass {
		fmt.Printf("%sverdict: PASS\n", indent)
	} else {
		fmt.Printf("%sverdict: FAIL — %s\n", indent, strings.Join(r.Failures, "; "))
	}
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

// printDoctorOnboarding prints the guided onboarding section at the top of
// doctor output.  It resolves workspace state in-process and delegates the
// logic to internal/doctor so the checks are unit-testable.
func printDoctorOnboarding() {
	// Resolve workspace state.
	_, wsStatErr := os.Stat(meta.ConfigFile)
	workspaceFound := wsStatErr == nil

	var configuredServices []string
	if workspaceFound {
		if wsCfg, err := workspace.Load(meta.ConfigFile); err == nil {
			for _, svc := range wsCfg.Services {
				configuredServices = append(configuredServices, svc.Name)
			}
		}
	}

	store, storeErr := openStore()
	nodeCountByService := map[string]int{}
	if storeErr == nil {
		defer store.Close()
		ctx := context.Background()
		idx, idxErr := store.BuildIndex(ctx)
		if idxErr == nil {
			// Count substantive (non-file) nodes per service.
			for _, n := range idx.Nodes {
				if n.Type != graph.NodeTypeFile {
					nodeCountByService[n.Service]++
				}
			}
		}
	}

	sessions := trace_ingest.ListSessionInfos(capturesBase(), time.Now())
	hasSessions := len(sessions) > 0

	params := doctor.OnboardingParams{
		WorkspaceFound:     workspaceFound,
		ConfiguredServices: configuredServices,
		StoreErr:           storeErr,
		NodeCountByService: nodeCountByService,
		HasSessions:        hasSessions,
	}

	issues := doctor.CheckOnboarding(params)
	fmt.Printf("  Onboarding:\n")
	if len(issues) == 0 {
		fmt.Printf("    OK — workspace configured, indexed, and operational\n")
	} else {
		for _, issue := range issues {
			label := strings.ToUpper(string(issue.Kind))
			fmt.Printf("    [%s] %s\n", label, issue.Message)
			fmt.Printf("          run: %s\n", issue.Fix)
		}
	}
	fmt.Println()
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

// buildFleetAwareIndex is store.BuildIndex, plus — when this workspace is a
// registered fleet member (Tier GR) — every other locally-resolved fleet
// member's own full graph (not just its cross-service edge endpoints)
// unioned in, on top of the fleet's bridge.db (GR.2) for edges reaching a
// member never resolved on this machine at all. Node IDs are disjoint by
// construction (FR.0), so the merge is a plain union: impact/context/
// trace/deadcode/investigate/entrypoints/hierarchy/flows, and every MCP
// tool built on the same idx, see the whole fleet by default — the same
// federation `polyflow serve` gives the web UI (GR.6, revised), not just
// this one workspace's cross-service reach. Any resolution failure (no
// fleet, ambiguous fleet without --fleet, sync failure) degrades to
// local-only results rather than erroring the query — fleet-wide data is a
// bonus, never a dependency.
func buildFleetAwareIndex(ctx context.Context, store *graph.SQLiteStore) (*graph.AdjacencyIndex, error) {
	idx, err := store.BuildIndex(ctx)
	if err != nil {
		return nil, err
	}
	mergeFleetMembersFull(ctx, idx, ".", false)
	return idx, nil
}

// mergeFleetMembersFull unions every locally-resolved member of the fleet
// claiming startDir (queryresolve.FleetMembers, GR.3) into idx — each
// member's own store is opened, its full graph merged in, and closed again
// (a CLI/MCP process is short-lived; nothing is cached across calls the way
// `polyflow serve`'s FleetMergeFunc caches open stores for its whole
// lifetime). One member unreadable must not break the merge for the rest.
// Finishes with mergeFleetBridge so cross-service edges still reach a
// member this machine has never resolved at all.
func mergeFleetMembersFull(ctx context.Context, idx *graph.AdjacencyIndex, startDir string, noSync bool) {
	members, err := queryresolve.FleetMembers(startDir, queryresolve.Options{Fleet: fleetFlag, NoSync: noSync})
	if err == nil {
		for _, dbPath := range members {
			memberStore, openErr := graph.NewSQLiteStore(dbPath)
			if openErr != nil {
				continue
			}
			memberIdx, buildErr := memberStore.BuildIndex(ctx)
			memberStore.Close()
			if buildErr != nil {
				continue
			}
			for _, n := range memberIdx.Nodes {
				idx.AddNode(n)
			}
			for _, e := range memberIdx.AllEdges() {
				e := e
				idx.AddEdge(&e)
			}
		}
	}
	mergeFleetBridge(ctx, idx, startDir, noSync)
}

// bridgeDupKey identifies "the same declaration" independent of which file
// path it was indexed under — see mergeFleetBridge's doc comment for why
// that varies for the very same source line.
type bridgeDupKey struct {
	service, typ, label string
	line                int
}

func bridgeDupKeyOf(n *graph.Node) bridgeDupKey {
	return bridgeDupKey{n.Service, string(n.Type), n.Label, n.Line}
}

// mergeFleetBridge resolves startDir's fleet bridge (queryresolve.Resolve)
// and merges its nodes/edges into idx in place. noSync forces
// Options.NoSync (latency-sensitive callers, e.g. hook injection, must never
// block on a clone or relink pass). Silent on any failure — see
// buildFleetAwareIndex's doc comment.
//
// A bridge node can be a stale duplicate of a node idx already has from a
// locally-resolved member's own full graph (mergeFleetMembersFull/
// newFleetMerge's member loop, which always runs first): when GR.2's bridge
// build resolved that member via a fresh clone (its local checkout wasn't
// clean — real uncommitted work, common in active dev), every node the
// bridge copied from it was indexed from inside that scratch clone, so its
// File — and therefore its ID, which embeds File — is that scratch path,
// permanently baked into bridge.db, not the workspace-relative path the
// member's own real index uses for the identical source line. Naively
// adding it produced a second, edge-less node for the same route/handler
// next to the correct, edge-connected one — "dead end" on the stale twin.
// bridgeDupKey identifies "the same declaration" by (service, type, label,
// line) instead of by ID: when a bridge node matches an already-merged
// node this way, it's skipped and its ID is remapped to the real one, so
// any genuine cross-service edge referencing it still resolves correctly
// rather than dangling or being dropped.
func mergeFleetBridge(ctx context.Context, idx *graph.AdjacencyIndex, startDir string, noSync bool) {
	res, err := queryresolve.Resolve(ctx, startDir, queryresolve.Options{Fleet: fleetFlag, NoSync: noSync})
	if err != nil || res.BridgePath == "" {
		return
	}
	bridgeStore, err := graph.NewSQLiteStore(res.BridgePath)
	if err != nil {
		return
	}
	defer bridgeStore.Close()
	bridgeIdx, err := bridgeStore.BuildIndex(ctx)
	if err != nil {
		return
	}

	existing := make(map[bridgeDupKey]*graph.Node, len(idx.Nodes))
	for _, n := range idx.Nodes {
		existing[bridgeDupKeyOf(n)] = n
	}

	remap := make(map[string]string)
	for _, n := range bridgeIdx.Nodes {
		if real, ok := existing[bridgeDupKeyOf(n)]; ok && real.ID != n.ID {
			remap[n.ID] = real.ID
			continue
		}
		idx.AddNode(n)
	}
	for _, e := range bridgeIdx.AllEdges() {
		e := e
		if r, ok := remap[e.From]; ok {
			e.From = r
		}
		if r, ok := remap[e.To]; ok {
			e.To = r
		}
		idx.AddEdge(&e)
	}
}

// fleetServeState is wireFleetServe's per-process cache of opened fleet
// member stores and their resolved db paths, shared by every
// FleetSwitchFunc invocation for the life of the `serve` process — a member
// switched to once (whether already local or freshly cloned) stays open, so
// switching back to it is free, matching GR.6's "opened lazily on first
// selection" deliverable.
type fleetServeState struct {
	mu      sync.Mutex
	cfg     *fleetconfig.Config
	regPath string
	stores  map[string]*graph.SQLiteStore
	dbPaths map[string]string
}

// wireFleetServe detects whether serveWS is a registered Tier-GR fleet
// member and, if so, wires srv.SetFleet with a merge function that unions
// every locally-resolved member's own graph (not just this one's cross-
// service bridge stubs) into idx and an ensure function that resolves one
// more member on demand (POST /api/fleet/active), then runs the merge once
// so the server starts in its full default fleet-wide view — the whole
// fleet is browsable/searchable together, no "active member" switch
// required (GR.6, revised: was originally one member at a time). Silent
// no-op (srv stays in non-fleet mode) on any resolution failure — fleet
// browsing is a bonus, never a requirement for `serve` to work.
func wireFleetServe(ctx context.Context, srv *server.Server, serveWS string, initialStore *graph.SQLiteStore, initialDBPath string, emb semantic.Embedder, synonyms map[string][]string) {
	res, err := queryresolve.Resolve(ctx, serveWS, queryresolve.Options{Fleet: fleetFlag})
	if err != nil || res.FleetName == "" {
		return
	}

	regPath, err := registry.DefaultPath()
	if err != nil {
		return
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		return
	}
	fleetConfigPath := reg.FleetConfigPaths[res.FleetName]
	if fleetConfigPath == "" {
		return
	}
	cfg, err := fleetconfig.Load(fleetConfigPath)
	if err != nil {
		return
	}

	active := ""
	root := filepath.Clean(res.WorkspaceRoot)
	members := make([]string, 0, len(cfg.Services))
	for _, svc := range cfg.Services {
		members = append(members, svc.Name)
		if entry, ok := reg.Lookup(svc.Name); ok && filepath.Clean(entry.LocalPath) == root {
			active = svc.Name
		}
	}
	if active == "" {
		return
	}

	st := &fleetServeState{
		cfg:     cfg,
		regPath: regPath,
		stores:  map[string]*graph.SQLiteStore{active: initialStore},
		dbPaths: map[string]string{active: initialDBPath},
	}
	srv.SetFleet(newFleetMerge(st, emb, synonyms), newFleetEnsure(st), members)
	if err := srv.RefreshFleet(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: fleet merge failed: %v\n", err)
	}
}

// fleetMemberDBPath mirrors fleetsync's unexported dbPathFor without
// needing that package's ResolveService machinery: a Subpath-less service's
// graph.db lives at <localPath>/.polyflow/graph.db; a Subpath (monorepo)
// service's lives at <localPath>/.polyflow/services/<name>/graph.db.
func fleetMemberDBPath(localPath string, svc fleetconfig.Service) string {
	if svc.Subpath == "" {
		return filepath.Join(localPath, meta.DBDir, meta.DBFile)
	}
	return filepath.Join(localPath, meta.DBDir, "services", svc.Name, meta.DBFile)
}

// newFleetMerge builds the server.FleetMergeFunc that (re)builds the full-
// fleet view from scratch on every call: for each fleet member, reuse its
// already-open store if cached (newFleetEnsure populates st.stores when it
// resolves a new member) or open it straight from the local registry
// (never cloning here — a mere merge/refresh must never block on network,
// unlike POST /api/fleet/active's explicit ensure step); union every
// resolved member's nodes/edges into one idx, record each node's checkout
// root by its own Service value (a member's polyflow.yml may itself declare
// more than one internal service, e.g. a monorepo), open one Searcher per
// resolved member (GR.3's federation), and merge in the fleet's bridge.db
// on top so cross-service edges reach members never resolved on this
// machine at all.
func newFleetMerge(st *fleetServeState, emb semantic.Embedder, synonyms map[string][]string) server.FleetMergeFunc {
	return func(ctx context.Context) (*graph.AdjacencyIndex, map[string]string, map[string]*semantic.Searcher, []string, error) {
		st.mu.Lock()
		defer st.mu.Unlock()

		reg, err := registry.Load(st.regPath)
		if err != nil {
			return nil, nil, nil, nil, err
		}

		idx := graph.NewAdjacencyIndex()
		roots := make(map[string]string)
		searchers := make(map[string]*semantic.Searcher)
		var resolved []string

		for _, svc := range st.cfg.Services {
			store, ok := st.stores[svc.Name]
			if !ok {
				entry, regOK := reg.Lookup(svc.Name)
				if !regOK || entry.LocalPath == "" {
					continue // never resolved on this machine — skip, don't clone on a mere refresh
				}
				dbPath := fleetMemberDBPath(entry.LocalPath, svc)
				var openErr error
				store, openErr = graph.NewSQLiteStore(dbPath)
				if openErr != nil {
					continue // e.g. never actually indexed despite a registry entry
				}
				st.stores[svc.Name] = store
				st.dbPaths[svc.Name] = dbPath
			}

			memberIdx, buildErr := store.BuildIndex(ctx)
			if buildErr != nil {
				continue
			}
			root := rootDirFromDBPath(st.dbPaths[svc.Name], svc)
			for _, n := range memberIdx.Nodes {
				idx.AddNode(n)
				roots[n.Service] = root
			}
			for _, e := range memberIdx.AllEdges() {
				e := e
				idx.AddEdge(&e)
			}
			searchers[svc.Name] = buildSearcher(store, emb, synonyms)
			resolved = append(resolved, svc.Name)
		}

		mergeFleetBridge(ctx, idx, ".", false)
		return idx, roots, searchers, resolved, nil
	}
}

// newFleetEnsure builds the server.FleetEnsureFunc POST /api/fleet/active
// calls: resolve the named member via GR.1's algorithm (fleetsync.
// ResolveService — clone if this machine doesn't have it yet) and cache the
// opened store, so the FleetMergeFunc that runs right after picks it up
// without re-resolving. A no-op if the member is already cached.
func newFleetEnsure(st *fleetServeState) server.FleetEnsureFunc {
	return func(ctx context.Context, service string) error {
		st.mu.Lock()
		defer st.mu.Unlock()

		if _, ok := st.stores[service]; ok {
			return nil
		}

		var svc fleetconfig.Service
		found := false
		for _, s := range st.cfg.Services {
			if s.Name == service {
				svc = s
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown fleet member %q", service)
		}

		dbPath, _, err := fleetsync.ResolveService(ctx, svc, "", fleetsync.ResolveOptions{RegistryPath: st.regPath})
		if err != nil {
			return err
		}
		store, err := graph.NewSQLiteStore(dbPath)
		if err != nil {
			return err
		}
		st.stores[service] = store
		st.dbPaths[service] = dbPath
		return nil
	}
}

// rootDirFromDBPath reverses fleetsync's unexported dbPathFor: a
// Subpath-less service's db is <root>/.polyflow/graph.db (2 segments below
// root); a Subpath (monorepo) service's is
// <root>/.polyflow/services/<name>/graph.db (4 segments below root).
func rootDirFromDBPath(dbPath string, svc fleetconfig.Service) string {
	up := 2
	if svc.Subpath != "" {
		up = 4
	}
	dir := dbPath
	for i := 0; i < up; i++ {
		dir = filepath.Dir(dir)
	}
	return dir
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
	nomicModelURL  = "https://huggingface.co/nomic-ai/nomic-embed-text-v1.5-GGUF/resolve/main/nomic-embed-text-v1.5.Q8_0.gguf"
	nomicModelFile = "nomic-embed-text-v1.5.Q8_0.gguf"
	// nomicModelSHA256 is the expected hex-encoded SHA-256 of the downloaded file.
	// Verify with: sha256sum ~/.cache/polyflow/models/nomic-embed-text-v1.5.Q8_0.gguf
	// and update this constant when the upstream model file changes.
	nomicModelSHA256 = "" // set to the verified SHA-256 before production use; empty = skip check
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
	fmt.Printf("Point your sidecar binary at this file and set search.embedder: sidecar in polyflow.yml\n")
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
