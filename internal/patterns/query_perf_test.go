package patterns_test

import (
	"strings"
	"testing"
	"time"

	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// TestQueryNoCatastrophicBacktracking guards against a class of pattern bug:
// a query using an *unanchored* run of `(_)` wildcards inside an argument_list
// (e.g. `(argument_list (_) @a (_) @b (_) @c (_) @d (_) @e)`) makes tree-sitter
// enumerate every C(N,k) assignment of wildcards to arguments. On real Go files
// with many-argument calls (SQL store layers doing `db.Query(ctx, sql, a, b,
// c, …)`) this blows up to seconds-per-file and stalled indexing at 99%. The
// `#eq?` name predicate does not help — it is applied only after structural
// matching. Anchor wildcard runs with `.` so each matches at most once.
//
// This test synthesises that worst-case shape (many calls, each with a long
// argument list) and asserts the full default pattern set matches it quickly.
func TestQueryNoCatastrophicBacktracking(t *testing.T) {
	reg, err := patterns.DefaultRegistry(patternsRoot)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	m := patterns.NewTreeSitterMatcher(reg)

	var b strings.Builder
	b.WriteString("package p\nfunc f(x T) {\n")
	// Calls with long argument lists are what the unanchored wildcard runs
	// explode over; repeat them so any per-call blowup accumulates visibly.
	for i := 0; i < 200; i++ {
		b.WriteString("\tx.Do(a, b, c, d, e, f, g, h, i, j, k, l)\n")
	}
	b.WriteString("}\n")
	src := []byte(b.String())

	start := time.Now()
	done := make(chan struct{})
	go func() { m.Match("go", "stress.go", src); close(done) }()
	select {
	case <-done:
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("go pattern match took %v on a many-argument file; likely an unanchored wildcard run reintroduced", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("go pattern match hung (>10s) on a many-argument file: unanchored wildcard run causing catastrophic backtracking")
	}
}
