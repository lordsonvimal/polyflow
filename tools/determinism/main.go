// Command determinism runs two cold indexes of a corpus and byte-diffs a
// canonical dump of the resulting graph. It is the SA.1 determinism harness:
// a non-deterministic mint path (map iteration order, a Go map picking one of
// several candidates) shows up here as a diff row instead of as a silently
// flipped edge weeks later.
//
// Usage:
//
//	determinism [corpus-dir]        # default: current directory
//
// The corpus dir must be an indexable polyflow workspace (has a
// .polyflow.yaml). `polyflow` must be on PATH. Exit status is 0 when the two
// runs are byte-identical, 1 on any difference, 2 on a harness error.
//
// Do NOT point this at polyflow's own repo — self-index is non-deterministic
// for unrelated reasons (see docs/static-architecture-target-plan.md, SA.1
// acceptance).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func main() {
	dir := "."
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		fail(err)
	}

	dumps := make([]string, 2)
	for run := 0; run < 2; run++ {
		fmt.Fprintf(os.Stderr, "determinism: cold index %d/2 of %s\n", run+1, abs)
		d, err := coldIndex(abs)
		if err != nil {
			fail(fmt.Errorf("run %d: %w", run+1, err))
		}
		dumps[run] = d
	}

	if dumps[0] == dumps[1] {
		fmt.Println("determinism: OK — two cold indexes produced a byte-identical graph")
		return
	}

	a := writeTemp("determinism-run1-", dumps[0])
	b := writeTemp("determinism-run2-", dumps[1])
	fmt.Fprintf(os.Stderr, "determinism: FAIL — the two cold indexes differ\n")
	fmt.Fprintf(os.Stderr, "  run 1 dump: %s\n  run 2 dump: %s\n", a, b)
	fmt.Fprintf(os.Stderr, "  first difference:\n%s\n", firstDiff(dumps[0], dumps[1]))
	os.Exit(1)
}

// coldIndex removes .polyflow, runs `polyflow index` in dir, then returns a
// canonical dump of the resulting graph.
func coldIndex(dir string) (string, error) {
	if err := os.RemoveAll(filepath.Join(dir, ".polyflow")); err != nil {
		return "", fmt.Errorf("rm -rf .polyflow: %w", err)
	}
	cmd := exec.Command("polyflow", "index")
	cmd.Dir = dir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("polyflow index: %w", err)
	}
	return dumpGraph(filepath.Join(dir, ".polyflow", "graph.db"))
}

// dumpGraph opens the graph DB and serializes every node and edge, each as one
// compact JSON line, nodes sorted by ID then edges sorted by ID. The ordering
// is total and content-independent, so any real difference in what was minted
// surfaces as a changed/added/removed line.
func dumpGraph(dbPath string) (string, error) {
	store, err := graph.NewSQLiteStore(dbPath)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", dbPath, err)
	}
	defer store.Close()

	idx, err := store.BuildIndex(context.Background())
	if err != nil {
		return "", fmt.Errorf("build index: %w", err)
	}

	ids := make([]string, 0, len(idx.Nodes))
	for id := range idx.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var buf []byte
	for _, id := range ids {
		line, err := json.Marshal(idx.Nodes[id])
		if err != nil {
			return "", err
		}
		buf = append(buf, "N "...)
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}
	for _, e := range idx.AllEdges() {
		e := e
		line, err := json.Marshal(&e)
		if err != nil {
			return "", err
		}
		buf = append(buf, "E "...)
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}
	return string(buf), nil
}

func firstDiff(a, b string) string {
	al, bl := splitLines(a), splitLines(b)
	for i := 0; i < len(al) || i < len(bl); i++ {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			return fmt.Sprintf("  line %d:\n  - %s\n  + %s", i+1, truncate(x), truncate(y))
		}
	}
	return "  (no line-level difference — trailing bytes differ)"
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func truncate(s string) string {
	if len(s) > 240 {
		return s[:240] + "…"
	}
	return s
}

func writeTemp(prefix, content string) string {
	f, err := os.CreateTemp("", prefix+"*.txt")
	if err != nil {
		fail(err)
	}
	_, _ = f.WriteString(content)
	_ = f.Close()
	return f.Name()
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "determinism: %v\n", err)
	os.Exit(2)
}
