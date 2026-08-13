package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/lordsonvimal/polyflow/internal/capture"
)

var (
	ingestSession string
)

var ingestCmd = &cobra.Command{
	Use:   "ingest <file>",
	Short: "Import a pre-captured OTLP trace dump into a polyflow capture session",
	Args:  cobra.ExactArgs(1),
	RunE:  runIngest,
}

func init() {
	ingestCmd.Flags().StringVar(&ingestSession, "session", "", "session name (default: timestamp)")
	rootCmd.AddCommand(ingestCmd)
}

// sessionName returns the effective session name (user-supplied or timestamp).
func sessionName(flag string) string { return capture.DefaultSessionName(flag) }

// capturesBase returns the directory that holds all capture sessions.
func capturesBase() string { return capture.BaseDir() }

func runIngest(cmd *cobra.Command, args []string) error {
	path := args[0]
	name := capture.DefaultSessionName(ingestSession)

	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("ingest: read %s: %w", path, err)
	}

	mgr := capture.NewManager(capture.BaseDir())
	n, err := mgr.Ingest(name, raw)
	if err != nil {
		return fmt.Errorf("ingest: %w", err)
	}

	fmt.Printf("Ingested %d spans into session %q (%s)\n", n, name, capture.BaseDir()+"/"+name)
	return nil
}
