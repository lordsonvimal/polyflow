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

func runMCP(cmd *cobra.Command, args []string) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	ctx := context.Background()
	idx, err := store.BuildIndex(ctx)
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

	srv, handle := mcpserver.New(store, idx, meta.Version, loadStaleAfter(meta.ConfigFile))
	handle.SetSearcher(buildSearcher(store, emb, synonyms))

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
		newIdx, err := newStore.BuildIndex(context.Background())
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
