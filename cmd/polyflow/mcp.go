package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/mcpserver"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/ops"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Serve the query layer (search, context, impact, trace) as MCP tools over stdio",
	Long: `Serve polyflow's query layer as MCP tools over stdio, for use by AI agents.

Register with Claude Code:
  claude mcp add polyflow -- polyflow mcp

The tools return the same JSON as the equivalent CLI commands, including the
unresolved-references section (graph blind spots to verify manually).`,
	RunE: runMCP,
}

// mcpMarkerPath is the state-file gate: its presence disables the query tools
// for the next `polyflow mcp` session. Lives under .polyflow so it is workspace-
// local and gitignored alongside the graph db.
func mcpMarkerPath() string { return filepath.Join(meta.DBDir, "mcp.disabled") }

// mcpEnabled reports whether the query tools should be registered (marker absent).
func mcpEnabled() bool {
	_, err := os.Stat(mcpMarkerPath())
	return os.IsNotExist(err)
}

const mcpReconnectHint = "Reconnect your agent (restart the session) for this to take effect — " +
	"the MCP server is spawned once per session over stdio."

var mcpOnCmd = &cobra.Command{
	Use:   "on",
	Short: "Enable polyflow's MCP query tools for the next agent session",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := os.Remove(mcpMarkerPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Println("polyflow MCP enabled. " + mcpReconnectHint)
		return nil
	},
}

var mcpOffCmd = &cobra.Command{
	Use:   "off",
	Short: "Disable polyflow's MCP query tools for the next session (A/B token baseline)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := os.MkdirAll(meta.DBDir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(mcpMarkerPath(), nil, 0o644); err != nil {
			return err
		}
		fmt.Println("polyflow MCP disabled — the next session runs WITHOUT polyflow tools. " + mcpReconnectHint)
		return nil
	},
}

var mcpStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Report whether the MCP query tools are enabled or disabled",
	RunE: func(cmd *cobra.Command, args []string) error {
		if mcpEnabled() {
			fmt.Println("enabled")
		} else {
			fmt.Println("disabled (run 'polyflow mcp on' to re-enable)")
		}
		return nil
	},
}

func init() {
	mcpCmd.AddCommand(mcpOnCmd, mcpOffCmd, mcpStatusCmd)
}

func runMCP(cmd *cobra.Command, args []string) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	ctx := context.Background()
	idx, err := buildFleetAwareIndex(ctx, store)
	if err != nil {
		return fmt.Errorf("build index: %w", err)
	}

	cfg, _ := workspace.Load(meta.ConfigFile) // best-effort

	// Build the embedder once for the MCP session lifetime; share across reloads.
	emb, closeEmb, _ := resolveEmbedder(cfg)
	defer closeEmb()
	var synonyms map[string][]string
	if cfg != nil {
		synonyms = cfg.Search.Synonyms
	}

	enabled := mcpEnabled()
	if !enabled {
		fmt.Fprintln(os.Stderr, "polyflow mcp: DISABLED — query tools not registered (run `polyflow mcp on` to re-enable)")
	}

	srv, handle := mcpserver.New(store, idx, meta.Version, loadStaleAfter(meta.ConfigFile), enabled)
	handle.SetSearcher(buildSearcher(store, emb, synonyms))

	// GR.3: search federates across every locally-resolved fleet member by
	// default. closeFleetSearchers is deferred to the end of the session
	// (runMCP is long-lived), not closed early like the CLI's one-shot
	// equivalent.
	fleetSearchers, closeFleetSearchers, err := buildFleetSearchers(emb, synonyms)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: fleet search federation disabled: %v\n", err)
	} else {
		defer closeFleetSearchers()
		handle.SetFleetSearchers(fleetSearchers)
	}

	// UB.2: ops.db lives next to graph.db and is never touched by the
	// indexer, so it survives graph.db's rebuild-then-atomic-rename.
	opsPath := filepath.Join(meta.DBDir, meta.OpsFile)
	if opsStore, err := ops.Open(opsPath); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: tool-call audit log disabled: %v\n", err)
	} else {
		defer opsStore.Close()
		handle.SetOps(opsStore)
	}

	// Pick up reindexes during the session: polyflow index atomically swaps
	// graph.db, so watch it and swap in a fresh store + index. Diagnostics go
	// to stderr — stdout belongs to the MCP protocol.
	dbPath := filepath.Join(meta.DBDir, meta.DBFile)
	if err := watchDB(dbPath, func() {
		newStore, err := graph.NewSQLiteStore(dbPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mcp reload: open store: %v\n", err)
			return
		}
		newIdx, err := buildFleetAwareIndex(context.Background(), newStore)
		if err != nil {
			newStore.Close()
			fmt.Fprintf(os.Stderr, "mcp reload: build index: %v\n", err)
			return
		}
		handle.SetSearcher(buildSearcher(newStore, emb, synonyms))
		handle.Reload(newStore, newIdx)
		fmt.Fprintln(os.Stderr, "polyflow mcp: graph reloaded")
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not start DB watcher: %v\n", err)
	}

	return srv.Run(ctx, &mcp.StdioTransport{})
}
