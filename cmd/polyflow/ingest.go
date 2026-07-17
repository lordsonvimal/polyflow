package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/lordsonvimal/polyflow/internal/evidence/trace_ingest"
	"github.com/lordsonvimal/polyflow/internal/meta"
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
func sessionName(flag string) string {
	if flag != "" {
		return flag
	}
	return time.Now().UTC().Format("2006-01-02T15-04-05")
}

// capturesBase returns the directory that holds all capture sessions.
func capturesBase() string {
	return filepath.Join(meta.DBDir, "captures")
}

func runIngest(cmd *cobra.Command, args []string) error {
	path := args[0]
	name := sessionName(ingestSession)
	dir := filepath.Join(capturesBase(), name)

	spans, err := trace_ingest.ParseOTLPFile(path)
	if err != nil {
		return fmt.Errorf("ingest: parse %s: %w", path, err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ingest: create session dir: %w", err)
	}

	// Read the raw file and append it as one JSONL line to spans.otlp.json.
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	spansFile := filepath.Join(dir, "spans.otlp.json")
	if err := trace_ingest.WriteSessionSpans(spansFile, raw); err != nil {
		return fmt.Errorf("ingest: write session spans: %w", err)
	}

	// Write meta.json.
	meta := map[string]interface{}{
		"name":       name,
		"started_at": time.Now().UTC().Format(time.RFC3339),
		"span_count": len(spans),
		"mode":       "ingest",
	}
	metaData, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), metaData, 0o644); err != nil {
		return fmt.Errorf("ingest: write meta.json: %w", err)
	}

	fmt.Printf("Ingested %d spans into session %q (%s)\n", len(spans), name, dir)
	return nil
}
