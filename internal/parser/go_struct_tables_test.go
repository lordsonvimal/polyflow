package parser

import (
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// tableModule lays out the exact maple-manager `Queues()` shape J.1 targets, plus
// the three shapes the decoder must refuse: a computed field, a non-literal
// return, and a table read through a range loop.
func tableModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/tabletest\n\ngo 1.25.0\n",
		"tables.go": `package main

const (
	QueueBuildLogs      = "build_logs_queue"
	ExchangeBuildLogs   = "build_logs"
	RoutingKeyHeartbeat = "heartbeat.ping"
)

type QueueDecl struct {
	Name        string
	Durable     bool
	Exchange    string
	RoutingKeys []string
}

func Queues() []QueueDecl {
	return []QueueDecl{
		{
			Name: QueueBuildLogs, Durable: true, Exchange: ExchangeBuildLogs,
			RoutingKeys: []string{"logs.build.*", "logs.workflow.*"},
		},
		{
			Name: "runner_heartbeats_queue", Durable: true, Exchange: "runner_heartbeat",
			RoutingKeys: []string{RoutingKeyHeartbeat},
		},
		{
			Name: "container_events_queue", Durable: true, Exchange: "container_events",
			RoutingKeys: []string{"container.#"},
		},
	}
}

func computedQueues(suffix string) []QueueDecl {
	return []QueueDecl{
		{Name: "n", Exchange: "pre" + suffix, RoutingKeys: []string{"k"}},
	}
}

func indirectQueues() []QueueDecl {
	return Queues()
}

type sink interface{ take(a, b, c string) }

func rangeTable(s sink) {
	for _, q := range Queues() {
		for _, rk := range q.RoutingKeys {
			s.take(q.Name, rk, q.Exchange)
		}
	}
}

func main() {}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// loadTableSSA builds the fixture's SSA program and returns its in-service
// function set keyed by name.
func loadTableSSA(t *testing.T, dir string) (map[string]*ssa.Function, map[*ssa.Function]bool) {
	t.Helper()
	cfg := &packages.Config{Mode: packages.LoadAllSyntax, Dir: dir, Fset: token.NewFileSet()}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if packages.PrintErrors(pkgs) > 0 {
		t.Fatal("fixture has build errors")
	}
	prog, ssaPkgs := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	prog.Build()

	byName := map[string]*ssa.Function{}
	inService := map[*ssa.Function]bool{}
	for _, p := range ssaPkgs {
		if p == nil {
			continue
		}
		for _, m := range p.Members {
			fn, ok := m.(*ssa.Function)
			if !ok {
				continue
			}
			byName[fn.Name()] = fn
			inService[fn] = true
		}
	}
	return byName, inService
}

func TestResolveStructTable_ScalarAndSliceFields(t *testing.T) {
	t.Parallel()
	byName, _ := loadTableSSA(t, tableModule(t))

	tbl, ok := resolveStructTable(byName["Queues"])
	if !ok {
		t.Fatal("Queues() did not decode")
	}
	if tbl.TypeName != "QueueDecl" {
		t.Errorf("TypeName = %q, want QueueDecl", tbl.TypeName)
	}
	if len(tbl.Rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(tbl.Rows))
	}
	// Row 0 exercises const-identifier fields, which SSA has already inlined.
	if got := tbl.Rows[0].Fields["Name"]; got != "build_logs_queue" {
		t.Errorf("row 0 Name = %q, want build_logs_queue", got)
	}
	if got := tbl.Rows[0].Fields["Exchange"]; got != "build_logs" {
		t.Errorf("row 0 Exchange = %q, want build_logs", got)
	}
	want := []string{"logs.build.*", "logs.workflow.*"}
	got := tbl.Rows[0].Slices["RoutingKeys"]
	if len(got) != len(want) {
		t.Fatalf("row 0 RoutingKeys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row 0 RoutingKeys[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if got := tbl.Rows[1].Slices["RoutingKeys"]; len(got) != 1 || got[0] != "heartbeat.ping" {
		t.Errorf("row 1 RoutingKeys = %v, want [heartbeat.ping]", got)
	}
	if got := tbl.Rows[2].Fields["Exchange"]; got != "container_events" {
		t.Errorf("row 2 Exchange = %q, want container_events", got)
	}
	// A non-string field is neither decoded nor guessed.
	if _, present := tbl.Rows[0].Fields["Durable"]; present {
		t.Error("Durable (bool) must not appear in Fields")
	}
	if !tbl.Rows[0].Pos.IsValid() {
		t.Error("row 0 has no position")
	}
}

func TestResolveStructTable_RejectsComputedField(t *testing.T) {
	t.Parallel()
	byName, _ := loadTableSSA(t, tableModule(t))

	tbl, ok := resolveStructTable(byName["computedQueues"])
	if ok {
		t.Fatalf("a parameterised table function must not decode, got %+v", tbl)
	}
}

func TestResolveStructTable_RejectsNonLiteralReturn(t *testing.T) {
	t.Parallel()
	byName, _ := loadTableSSA(t, tableModule(t))

	if tbl, ok := resolveStructTable(byName["indirectQueues"]); ok {
		t.Fatalf("`return Queues()` must not decode, got %+v", tbl)
	}
}

func TestTableFieldOf_RangeOverTableCall(t *testing.T) {
	t.Parallel()
	byName, inService := loadTableSSA(t, tableModule(t))

	fn := byName["rangeTable"]
	if fn == nil {
		t.Fatal("rangeTable not found")
	}
	// The interface call `s.take(q.Name, rk, q.Exchange)` carries all three
	// shapes: two direct row-field loads and one element of a slice field.
	var args []ssa.Value
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			ci, ok := instr.(ssa.CallInstruction)
			if !ok || !ci.Common().IsInvoke() || ci.Common().Method.Name() != "take" {
				continue
			}
			args = ci.Common().Args
		}
	}
	if len(args) != 3 {
		t.Fatalf("take() call not found (args=%d)", len(args))
	}

	cases := []struct {
		arg  int
		want string
	}{
		{0, "Name"},
		{1, "RoutingKeys"},
		{2, "Exchange"},
	}
	for _, tc := range cases {
		field, tbl, ok := tableFieldOf(args[tc.arg], inService)
		if !ok {
			t.Errorf("arg %d: not resolved to a table field", tc.arg)
			continue
		}
		if field != tc.want {
			t.Errorf("arg %d: field = %q, want %q", tc.arg, field, tc.want)
		}
		if tbl.TypeName != "QueueDecl" || len(tbl.Rows) != 3 {
			t.Errorf("arg %d: table = %s/%d rows, want QueueDecl/3", tc.arg, tbl.TypeName, len(tbl.Rows))
		}
	}
}
