package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/lordsonvimal/polyflow/internal/meta"
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
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a polyflow workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("init: not yet implemented")
		return nil
	},
}

var indexCmd = &cobra.Command{
	Use:   "index",
	Short: "Parse and index all services in the workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("index: not yet implemented")
		return nil
	},
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the polyflow web UI and API server",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("serve: not yet implemented")
		return nil
	},
}

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search the index for nodes matching query",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Printf("search %q: not yet implemented\n", args[0])
		return nil
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show index statistics",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("status: not yet implemented")
		return nil
	},
}

var patternsCmd = &cobra.Command{
	Use:   "patterns",
	Short: "List or validate loaded patterns",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("patterns: not yet implemented")
		return nil
	},
}

var contextCmd = &cobra.Command{
	Use:   "context <node-id>",
	Short: "Show the call context around a node",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Printf("context %q: not yet implemented\n", args[0])
		return nil
	},
}

var impactCmd = &cobra.Command{
	Use:   "impact <node-id>",
	Short: "Show what is impacted by changes to a node",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Printf("impact %q: not yet implemented\n", args[0])
		return nil
	},
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "View or edit polyflow configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("config: not yet implemented")
		return nil
	},
}
