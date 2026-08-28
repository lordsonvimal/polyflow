package linker

import (
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// makeSQLTableNode builds a table node the way internal/parser/sql.go's
// SQLParser would mint one for a CREATE TABLE statement.
func makeSQLTableNode(id, service, file, name string) graph.Node {
	return graph.Node{
		ID: id, Type: graph.NodeTypeTable, Label: name,
		Service: service, File: file, Language: "sql",
	}
}

// TestLinkSQLReferences_InlineAndStandalone is SQ1's pinned fixture: an
// inline `col TYPE REFERENCES other(col)` and a standalone `FOREIGN KEY
// (col) REFERENCES other(col)` each resolve to one `references` edge.
func TestLinkSQLReferences_InlineAndStandalone(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	usersFile := filepath.Join(dir, "users.sql")
	mustWrite(t, usersFile, "CREATE TABLE users (id INT PRIMARY KEY);\n")

	ordersFile := filepath.Join(dir, "orders.sql")
	mustWrite(t, ordersFile,
		"CREATE TABLE orders (id INT PRIMARY KEY, user_id INT REFERENCES users(id));\n")

	shipmentsFile := filepath.Join(dir, "shipments.sql")
	mustWrite(t, shipmentsFile,
		"CREATE TABLE shipments (id INT PRIMARY KEY, order_id INT, FOREIGN KEY (order_id) REFERENCES orders(id));\n")

	nodes := []graph.Node{
		makeSQLTableNode("n:users", "svc", usersFile, "users"),
		makeSQLTableNode("n:orders", "svc", ordersFile, "orders"),
		makeSQLTableNode("n:shipments", "svc", shipmentsFile, "shipments"),
	}
	svcFiles := map[string][]string{"svc": {usersFile, ordersFile, shipmentsFile}}

	edges, unresolved := LinkSQLReferences(nodes, svcFiles)
	if len(unresolved) != 0 {
		t.Errorf("expected no unresolved entries, got %+v", unresolved)
	}
	if len(edges) != 2 {
		t.Fatalf("expected 2 references edges, got %d: %+v", len(edges), edges)
	}

	wantPairs := map[[2]string]string{
		{"n:orders", "n:users"}:     "id",
		{"n:shipments", "n:orders"}: "id",
	}
	for _, e := range edges {
		if e.Type != graph.EdgeTypeReferences {
			t.Errorf("edge type = %q, want references", e.Type)
		}
		refCol, ok := wantPairs[[2]string{e.From, e.To}]
		if !ok {
			t.Errorf("unexpected edge %s -> %s", e.From, e.To)
			continue
		}
		if e.Meta["ref_column"] != refCol {
			t.Errorf("edge %s -> %s: ref_column = %q, want %q", e.From, e.To, e.Meta["ref_column"], refCol)
		}
		delete(wantPairs, [2]string{e.From, e.To})
	}
	if len(wantPairs) != 0 {
		t.Errorf("missing expected edges: %+v", wantPairs)
	}
}

// TestLinkSQLReferences_UnresolvedTarget verifies a REFERENCES clause whose
// target table isn't indexed in this workspace ledgers rather than being
// dropped silently or fabricated.
func TestLinkSQLReferences_UnresolvedTarget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	ordersFile := filepath.Join(dir, "orders.sql")
	mustWrite(t, ordersFile,
		"CREATE TABLE orders (id INT PRIMARY KEY, customer_id INT REFERENCES customers(id));\n")

	nodes := []graph.Node{makeSQLTableNode("n:orders", "svc", ordersFile, "orders")}
	svcFiles := map[string][]string{"svc": {ordersFile}}

	edges, unresolved := LinkSQLReferences(nodes, svcFiles)
	if len(edges) != 0 {
		t.Fatalf("expected no fabricated edges, got %+v", edges)
	}
	if len(unresolved) != 1 || unresolved[0].Kind != "sql_reference_unresolved" || unresolved[0].Name != "customers" {
		t.Fatalf("expected one sql_reference_unresolved ledger entry for 'customers', got %+v", unresolved)
	}
}

// TestLinkSQLReferences_CollisionFansOutNotFirstMatch verifies a same-name
// table collision across two files in the same service fans out to every
// candidate plus a ledger entry, never first-match (rule 1).
func TestLinkSQLReferences_CollisionFansOutNotFirstMatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	dupFileA := filepath.Join(dir, "a.sql")
	mustWrite(t, dupFileA, "CREATE TABLE dup (id INT PRIMARY KEY);\n")
	dupFileB := filepath.Join(dir, "b.sql")
	mustWrite(t, dupFileB, "CREATE TABLE dup (id INT PRIMARY KEY);\n")

	refFile := filepath.Join(dir, "ref.sql")
	mustWrite(t, refFile, "CREATE TABLE refs (id INT PRIMARY KEY, dup_id INT REFERENCES dup(id));\n")

	nodes := []graph.Node{
		makeSQLTableNode("n:dupA", "svc", dupFileA, "dup"),
		makeSQLTableNode("n:dupB", "svc", dupFileB, "dup"),
		makeSQLTableNode("n:refs", "svc", refFile, "refs"),
	}
	svcFiles := map[string][]string{"svc": {dupFileA, dupFileB, refFile}}

	edges, unresolved := LinkSQLReferences(nodes, svcFiles)
	if len(edges) != 2 {
		t.Fatalf("expected fan-out to both dup candidates, got %d: %+v", len(edges), edges)
	}
	gotTargets := map[string]bool{}
	for _, e := range edges {
		if e.From != "n:refs" {
			t.Errorf("edge from = %q, want n:refs", e.From)
		}
		gotTargets[e.To] = true
	}
	if !gotTargets["n:dupA"] || !gotTargets["n:dupB"] {
		t.Errorf("expected edges to both n:dupA and n:dupB, got %+v", edges)
	}

	var collision bool
	for _, u := range unresolved {
		if u.Kind == "sql_reference_collision" && u.Name == "dup" {
			collision = true
		}
	}
	if !collision {
		t.Errorf("expected a sql_reference_collision ledger entry, got %+v", unresolved)
	}
}

// TestLinkSQLReferences_TwoRunDeterminism verifies byte-identical output
// across two runs on the same input (bug-class rule 2 — this pass iterates a
// same-service table-name map).
func TestLinkSQLReferences_TwoRunDeterminism(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	usersFile := filepath.Join(dir, "users.sql")
	mustWrite(t, usersFile, "CREATE TABLE users (id INT PRIMARY KEY);\n")
	ordersFile := filepath.Join(dir, "orders.sql")
	mustWrite(t, ordersFile,
		"CREATE TABLE orders (id INT PRIMARY KEY, user_id INT REFERENCES users(id));\n")

	nodes := []graph.Node{
		makeSQLTableNode("n:usersA", "svcA", usersFile, "users"),
		makeSQLTableNode("n:ordersA", "svcA", ordersFile, "orders"),
		makeSQLTableNode("n:usersB", "svcB", usersFile, "users"),
		makeSQLTableNode("n:ordersB", "svcB", ordersFile, "orders"),
	}
	svcFiles := map[string][]string{
		"svcA": {usersFile, ordersFile},
		"svcB": {usersFile, ordersFile},
	}

	edges1, unresolved1 := LinkSQLReferences(nodes, svcFiles)
	edges2, unresolved2 := LinkSQLReferences(nodes, svcFiles)

	if len(edges1) != len(edges2) {
		t.Fatalf("edge count differs across runs: %d vs %d", len(edges1), len(edges2))
	}
	for i := range edges1 {
		if edges1[i].ID != edges2[i].ID {
			t.Errorf("edge order/content differs at index %d: %q vs %q", i, edges1[i].ID, edges2[i].ID)
		}
	}
	if len(unresolved1) != len(unresolved2) {
		t.Fatalf("unresolved count differs across runs: %d vs %d", len(unresolved1), len(unresolved2))
	}
	for i := range unresolved1 {
		if unresolved1[i] != unresolved2[i] {
			t.Errorf("unresolved entry differs at index %d: %+v vs %+v", i, unresolved1[i], unresolved2[i])
		}
	}
}
