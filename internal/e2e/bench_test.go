package e2e_test

// Phase 12 performance benchmarks against a synthetic workspace shaped like
// the real stress tests (synergy: multi-module go.work + several JS apps;
// nextGen: Rails-monolith-sized Ruby tree).
//
// Size defaults to ~1,200 files so `make bench` stays fast; set
// POLYFLOW_BENCH_FILES=10000 to reproduce the documented-target measurement
// (10k files cold, incremental 100-file change). Results are recorded in
// docs/phases.md.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/indexer"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

const (
	benchGoModules = 4 // synergy-style go.work modules
	benchJSApps    = 3 // Nx-style JS apps
)

func benchTotalFiles() int {
	if v := os.Getenv("POLYFLOW_BENCH_FILES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 1200
}

// generateBenchWorkspace writes the synthetic tree and returns its config.
// Distribution: 60% Go across benchGoModules modules (stdlib-only so the
// go/packages semantic pass runs offline), 25% Ruby in one Rails-monolith
// service, 15% JS across benchJSApps apps.
func generateBenchWorkspace(tb testing.TB, root string, totalFiles int) *workspace.WorkspaceConfig {
	tb.Helper()

	goFiles := totalFiles * 60 / 100
	rubyFiles := totalFiles * 25 / 100
	jsFiles := totalFiles - goFiles - rubyFiles

	cfg := &workspace.WorkspaceConfig{Name: "bench", Version: "1"}

	// Go: go.work spanning benchGoModules modules.
	var workUses []string
	perMod := goFiles / benchGoModules
	for m := 0; m < benchGoModules; m++ {
		modDir := filepath.Join(root, fmt.Sprintf("gomod%d", m))
		pkgDir := filepath.Join(modDir, "internal", "app")
		if err := os.MkdirAll(pkgDir, 0o755); err != nil {
			tb.Fatal(err)
		}
		mod := fmt.Sprintf("module example.com/gomod%d\n\ngo 1.22\n", m)
		if err := os.WriteFile(filepath.Join(modDir, "go.mod"), []byte(mod), 0o644); err != nil {
			tb.Fatal(err)
		}
		workUses = append(workUses, fmt.Sprintf("\t./gomod%d", m))
		for i := 0; i < perMod; i++ {
			src := fmt.Sprintf(`package app

import "net/http"

func Handler%[1]d(w http.ResponseWriter, r *http.Request) {
	process%[1]d()
	w.WriteHeader(http.StatusOK)
}

func process%[1]d() {
	http.Get("http://backend/api/item%[1]d")
}
`, i)
			path := filepath.Join(pkgDir, fmt.Sprintf("handler%d.go", i))
			if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
				tb.Fatal(err)
			}
		}
		cfg.Services = append(cfg.Services, workspace.Service{
			Name: fmt.Sprintf("gomod%d", m), Path: filepath.Join(root, fmt.Sprintf("gomod%d", m)), Language: "go",
		})
	}
	work := "go 1.22\n\nuse (\n"
	for _, u := range workUses {
		work += u + "\n"
	}
	work += ")\n"
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte(work), 0o644); err != nil {
		tb.Fatal(err)
	}

	// Ruby: one Rails-monolith-shaped service.
	railsDir := filepath.Join(root, "monolith", "app", "controllers")
	if err := os.MkdirAll(railsDir, 0o755); err != nil {
		tb.Fatal(err)
	}
	for i := 0; i < rubyFiles; i++ {
		src := fmt.Sprintf(`class Resource%[1]dController < ApplicationController
  def index
    render json: Resource%[1]d.all
  end

  def create
    Resource%[1]d.create!(params.permit(:name))
    render json: { ok: true }
  end
end
`, i)
		path := filepath.Join(railsDir, fmt.Sprintf("resource%d_controller.rb", i))
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			tb.Fatal(err)
		}
	}
	cfg.Services = append(cfg.Services, workspace.Service{
		Name: "monolith", Path: filepath.Join(root, "monolith"), Language: "ruby",
	})

	// JS: several apps.
	perApp := jsFiles / benchJSApps
	for a := 0; a < benchJSApps; a++ {
		appDir := filepath.Join(root, fmt.Sprintf("jsapp%d", a), "src")
		if err := os.MkdirAll(appDir, 0o755); err != nil {
			tb.Fatal(err)
		}
		for i := 0; i < perApp; i++ {
			src := fmt.Sprintf(`export function load%[1]d() {
  return fetch('/api/resource%[1]d').then((r) => r.json());
}

function render%[1]d(data) {
  document.getElementById('slot%[1]d').textContent = data.name;
}
`, i)
			path := filepath.Join(appDir, fmt.Sprintf("module%d.js", i))
			if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
				tb.Fatal(err)
			}
		}
		cfg.Services = append(cfg.Services, workspace.Service{
			Name: fmt.Sprintf("jsapp%d", a), Path: filepath.Join(root, fmt.Sprintf("jsapp%d", a)), Language: "javascript",
		})
	}

	return cfg
}

func runIndex(tb testing.TB, cfg *workspace.WorkspaceConfig, dbDir string) *indexer.Stats {
	tb.Helper()
	stats, err := indexer.Run(context.Background(), indexer.Options{
		Config:      cfg,
		DBDir:       dbDir,
		PatternsDir: "../../patterns",
	})
	if err != nil {
		tb.Fatal(err)
	}
	return stats
}

// BenchmarkIndexCold measures a from-scratch index of the synthetic tree.
// Documented target: 10k files < 30s.
func BenchmarkIndexCold(b *testing.B) {
	root := b.TempDir()
	cfg := generateBenchWorkspace(b, root, benchTotalFiles())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		dbDir := filepath.Join(root, fmt.Sprintf("db-cold-%d", i))
		b.StartTimer()
		stats := runIndex(b, cfg, dbDir)
		b.ReportMetric(float64(stats.TotalFiles), "files")
	}
}

// BenchmarkIndexIncrementalUnchanged measures a re-index where nothing
// changed: every file served from the hash cache, zero parses.
func BenchmarkIndexIncrementalUnchanged(b *testing.B) {
	root := b.TempDir()
	cfg := generateBenchWorkspace(b, root, benchTotalFiles())
	dbDir := filepath.Join(root, "db")
	runIndex(b, cfg, dbDir) // prime

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stats := runIndex(b, cfg, dbDir)
		if stats.ParsedFiles != 0 {
			b.Fatalf("expected 0 parsed files on unchanged re-index, got %d", stats.ParsedFiles)
		}
	}
}

// BenchmarkIndexIncremental100Changed measures a re-index after touching 100
// files. Documented target: < 3s.
func BenchmarkIndexIncremental100Changed(b *testing.B) {
	root := b.TempDir()
	cfg := generateBenchWorkspace(b, root, benchTotalFiles())
	dbDir := filepath.Join(root, "db")
	runIndex(b, cfg, dbDir) // prime

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		touchN(b, filepath.Join(root, "gomod0", "internal", "app"), 100, i)
		b.StartTimer()
		stats := runIndex(b, cfg, dbDir)
		if stats.ParsedFiles < 100 {
			b.Fatalf("expected >=100 parsed files after touching 100, got %d", stats.ParsedFiles)
		}
	}
}

// touchN appends a distinct comment to the first n .go files in dir so their
// content hashes change each round.
func touchN(tb testing.TB, dir string, n, round int) {
	tb.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		tb.Fatal(err)
	}
	touched := 0
	for _, e := range entries {
		if touched >= n || e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			tb.Fatal(err)
		}
		fmt.Fprintf(f, "\n// bench round %d\n", round)
		f.Close()
		touched++
	}
	if touched < n {
		tb.Fatalf("only %d files available to touch, need %d", touched, n)
	}
}
