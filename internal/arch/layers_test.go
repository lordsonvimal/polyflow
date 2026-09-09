// Package arch enforces the layer contract in docs/architecture.md as a test.
//
// The contract is a dependency direction: a lower layer never imports a higher
// one. That direction was previously held up by convention and review only,
// which erodes one justified exception at a time. This guard runs in the
// default `go test ./...` invocation on purpose — a guard behind a build tag
// is a guard nobody runs.
//
// Adding a rule below is cheap. Removing one requires either a fix or a dated
// exception in the table; an exception with no date is a permanent one.
package arch

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

const modulePath = "github.com/lordsonvimal/polyflow"

// rule is "package from may not import package to", directly or transitively.
// Paths are module-relative (e.g. "internal/linker").
type rule struct{ from, to, why string }

// Pinned rules. Some name packages that do not exist yet (Tier SA lands
// internal/valuegraph, internal/artifact and internal/datalog); those rules sit
// inert until the package appears, and the test logs which ones are inert so a
// typo does not hide forever.
var forbidden = []rule{
	{"internal/valuegraph", "internal/linker", "L2 is a leaf; the linker calls it, never the reverse"},
	{"internal/valuegraph", "internal/parser", "L2 must not depend on L1"},
	{"internal/valuegraph", "internal/patterns", "L2 must not depend on L1"},
	{"internal/linker", "internal/parser", "pre-existing constraint; linker cannot import parser"},
	{"internal/artifact", "internal/linker", "L1 artifacts is a leaf"},
	{"internal/datalog", "internal/graph", "L3 engine is graph-agnostic; the emitter bridges"},
}

// Dated exceptions, none today. An entry must carry the date it was granted and
// the condition that retires it, so review can tell a decision from a leftover.
var exceptions []struct {
	rule
	since string // YYYY-MM-DD
	until string // the condition that retires it
}

func TestLayerContract(t *testing.T) {
	root := repoRoot(t)
	imports := loadImports(t, root, "./...")

	for _, r := range forbidden {
		if _, ok := imports[modulePath+"/"+r.from]; !ok {
			t.Logf("rule inert: %s does not exist yet (%s -> %s)", r.from, r.from, r.to)
			continue
		}
		if _, ok := imports[modulePath+"/"+r.to]; !ok {
			t.Logf("rule inert: %s does not exist yet (%s -> %s)", r.to, r.from, r.to)
			continue
		}
		if chain, bad := reaches(imports, modulePath+"/"+r.from, modulePath+"/"+r.to); bad {
			if excepted(r) {
				t.Logf("excepted: %s -> %s via %s", r.from, r.to, strings.Join(trimModule(chain), " -> "))
				continue
			}
			t.Errorf("layer contract: %s must not import %s\n  why:  %s\n  path: %s",
				r.from, r.to, r.why, strings.Join(trimModule(chain), " -> "))
		}
	}
}

// TestGuardFiresOnViolation proves the guard fails when it should. The fixture
// under testdata/violation/ imports upwards on purpose; without this test a
// broken loader would report a clean tree forever.
func TestGuardFiresOnViolation(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "internal", "arch")
	imports := loadImports(t, dir, "./testdata/violation/...")

	base := modulePath + "/internal/arch/testdata/violation/"
	cases := []struct {
		name      string
		from, to  string
		wantChain []string
	}{
		{"direct", "lower", "mid", []string{base + "lower", base + "mid"}},
		{"transitive", "lower", "upper", []string{base + "lower", base + "mid", base + "upper"}},
		{"reverse direction is clean", "upper", "lower", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chain, bad := reaches(imports, base+tc.from, base+tc.to)
			if bad != (tc.wantChain != nil) {
				t.Fatalf("reaches(%s, %s) = %v, want %v", tc.from, tc.to, bad, tc.wantChain != nil)
			}
			if !bad {
				return
			}
			if strings.Join(chain, " -> ") != strings.Join(tc.wantChain, " -> ") {
				t.Errorf("chain = %v, want %v", chain, tc.wantChain)
			}
		})
	}
}

func excepted(r rule) bool {
	for _, e := range exceptions {
		if e.from == r.from && e.to == r.to {
			return true
		}
	}
	return false
}

// loadImports returns the module-internal import graph reachable from patterns,
// keyed by full package path. Edges outside the module are dropped: an internal
// package can only reach another internal package through internal ones.
func loadImports(t *testing.T, dir string, patterns ...string) map[string][]string {
	t.Helper()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedImports | packages.NeedDeps,
		Dir:  dir,
		// Tests: false — the contract is about shipped code. A test file may
		// legitimately import anything it needs to build a fixture.
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		t.Fatalf("load %v: %v", patterns, err)
	}
	if len(pkgs) == 0 {
		t.Fatalf("load %v: no packages", patterns)
	}

	imports := map[string][]string{}
	var visit func(p *packages.Package)
	visit = func(p *packages.Package) {
		if !strings.HasPrefix(p.PkgPath, modulePath+"/") && p.PkgPath != modulePath {
			return
		}
		if _, seen := imports[p.PkgPath]; seen {
			return
		}
		imports[p.PkgPath] = nil
		var deps []string
		for path := range p.Imports {
			if strings.HasPrefix(path, modulePath+"/") || path == modulePath {
				deps = append(deps, path)
			}
		}
		sort.Strings(deps) // deterministic chains in failure output
		imports[p.PkgPath] = deps
		for _, d := range deps {
			if dep := p.Imports[d]; dep != nil {
				visit(dep)
			}
		}
	}
	for _, p := range pkgs {
		visit(p)
	}
	return imports
}

// reaches finds the shortest import chain from `from` to `to`, if any. Shortest
// keeps the failure message readable: a direct violation reports two hops, not
// whatever long path the walk happened to find first.
func reaches(imports map[string][]string, from, to string) ([]string, bool) {
	prev := map[string]string{from: ""}
	queue := []string{from}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range imports[cur] {
			if _, seen := prev[next]; seen {
				continue
			}
			prev[next] = cur
			if next == to {
				chain := []string{to}
				for at := cur; at != ""; at = prev[at] {
					chain = append([]string{at}, chain...)
				}
				return chain, true
			}
			queue = append(queue, next)
		}
	}
	return nil, false
}

// trimModule shortens a chain to module-relative paths for the failure message.
func trimModule(chain []string) []string {
	out := make([]string, len(chain))
	for i, p := range chain {
		out[i] = strings.TrimPrefix(p, modulePath+"/")
	}
	return out
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		t.Fatalf("repo root %s has no go.mod: %v", dir, err)
	}
	return dir
}
