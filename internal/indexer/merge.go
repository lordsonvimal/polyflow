package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// MergeStats reports what a MergeServiceDBs run did.
type MergeStats struct {
	Services int
	Nodes    int
	Edges    int
}

// mergeTable describes one incremental-cache table copied verbatim per
// service during a merge — full column list, used for both the pre-copy
// sweep and the INSERT ... SELECT. Every one of these tables is scoped
// entirely by a `service` column, so "delete this service's rows, then
// insert its current rows" is a correct full replace, unlike nodes/edges
// (handled separately below) where edges must cascade off node deletion.
var mergeTables = []struct {
	name    string
	columns []string
}{
	{"file_hashes", []string{"file_path", "service", "content_hash", "indexed_at", "nodes_json", "edges_json", "unresolved_json", "errored"}},
	{"dependencies", []string{"service", "ecosystem", "name", "version", "kind"}},
	{"unresolved_refs", []string{"service", "file", "line", "name", "kind"}},
	{"parse_errors", []string{"file_path", "service", "error_count", "first_error_line", "indexed_at"}},
	{"semantic_cache", []string{"service", "fingerprint", "nodes_json", "edges_json", "referenced_json"}},
}

// MergeServiceDBs attaches each service's graph.db read-only and copies nodes, edges, and the
// incremental-cache tables into dst. Runs graph.AssertServiceScopedIDs (FR.0) before any copy —
// a violation aborts the whole merge, nothing partial is written (bug-class #12: fail loud, not
// silently partial).
//
// dst must already have Schema applied (graph.NewSQLiteStore creates it). Existing rows in dst
// for a service NOT present in services are left untouched (lets a caller merge a subset without
// nuking services it didn't touch — used by FR.5's "relink only the changed pair" path).
func MergeServiceDBs(ctx context.Context, dst *graph.SQLiteStore, services map[string]string) (*MergeStats, error) {
	if len(services) == 0 {
		return &MergeStats{}, nil
	}

	nodesByService, err := loadServiceNodes(ctx, services)
	if err != nil {
		return nil, err
	}
	if nodeID, svcA, svcB, ok := graph.AssertServiceScopedIDs(nodesByService); !ok {
		return nil, fmt.Errorf("merge: node ID %q was produced by both service %q and %q — refusing to merge", nodeID, svcA, svcB)
	}

	db := dst.DB()
	for name, path := range services {
		if err := mergeOneService(ctx, db, name, path); err != nil {
			return nil, fmt.Errorf("merge service %q: %w", name, err)
		}
	}

	if err := rebuildNodesFTS(ctx, db); err != nil {
		return nil, fmt.Errorf("merge: rebuild nodes_fts: %w", err)
	}

	nodeCount, edgeCount, err := dst.Stats(ctx)
	if err != nil {
		return nil, fmt.Errorf("merge: stats: %w", err)
	}
	return &MergeStats{Services: len(services), Nodes: nodeCount, Edges: edgeCount}, nil
}

// loadServiceNodes opens each service DB just long enough to read its node
// set, for the FR.0 precondition check. Doing this before touching dst means
// a collision aborts the merge with dst still in its pre-merge state.
func loadServiceNodes(ctx context.Context, services map[string]string) (map[string][]*graph.Node, error) {
	out := make(map[string][]*graph.Node, len(services))
	for name, path := range services {
		st, err := graph.NewSQLiteStore(path)
		if err != nil {
			return nil, fmt.Errorf("merge: open service %q db %s: %w", name, path, err)
		}
		idx, err := st.BuildIndex(ctx)
		if err != nil {
			_ = st.Close()
			return nil, fmt.Errorf("merge: load nodes for service %q: %w", name, err)
		}
		if err := st.Close(); err != nil {
			return nil, fmt.Errorf("merge: close service %q db: %w", name, err)
		}
		nodes := make([]*graph.Node, 0, len(idx.Nodes))
		for _, n := range idx.Nodes {
			nodes = append(nodes, n)
		}
		out[name] = nodes
	}
	return out, nil
}

// mergeOneService attaches one service's db, sweeps dst's existing rows for
// that service (so a file/node deleted on the source side disappears here
// too — the edges delete rides the node delete's cascade), then bulk-copies
// the service's current rows in.
func mergeOneService(ctx context.Context, db *sql.DB, name, path string) error {
	if _, err := db.ExecContext(ctx, `ATTACH DATABASE ? AS srcdb`, path); err != nil {
		return fmt.Errorf("attach: %w", err)
	}
	defer db.ExecContext(ctx, `DETACH DATABASE srcdb`) //nolint:errcheck

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM edges WHERE "from" IN (SELECT id FROM nodes WHERE service=?) OR "to" IN (SELECT id FROM nodes WHERE service=?)`,
		name, name); err != nil {
		return fmt.Errorf("sweep edges: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM nodes WHERE service=?`, name); err != nil {
		return fmt.Errorf("sweep nodes: %w", err)
	}
	// Scoped to service=name on both copies: srcdb is a per-service DB, but
	// reconciliation (evidence.Reconcile) evaluates contract/config evidence
	// workspace-wide and can mint nodes/edges owned by *other* services while
	// indexing this one (e.g. a spec elsewhere in the workspace produces a
	// gap-endpoint node). Those rows already exist in dst from a prior full
	// index or another service's relink; only sweeping+copying name's own
	// rows here (not srcdb's full contents) avoids re-inserting an ID dst
	// already has and colliding on the nodes primary key.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO nodes (id, type, label, service, file, line, end_line, language, meta)
		SELECT id, type, label, service, file, line, end_line, language, meta FROM srcdb.nodes WHERE service=?`, name); err != nil {
		return fmt.Errorf("copy nodes: %w", err)
	}
	// A handful of sink nodes (e.g. the contract engine's "unresolved" node,
	// internal/contract/engine.go's applyUnmatched) are deliberately
	// service="" — shared workspace-wide by every producer whose target
	// service couldn't be resolved at all, so there is no single owner to
	// scope them to. INSERT OR IGNORE: srcdb regenerates the same
	// deterministic IDs every run, and dst (or an earlier service in this
	// same merge) may already have inserted them.
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO nodes (id, type, label, service, file, line, end_line, language, meta)
		SELECT id, type, label, service, file, line, end_line, language, meta FROM srcdb.nodes WHERE service=''`); err != nil {
		return fmt.Errorf("copy global sink nodes: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO edges (id, "from", "to", type, label, meta, confidence, method, path, sources_json, verification_state, verified_granularity)
		SELECT e.id, e."from", e."to", e.type, e.label, e.meta, e.confidence, e.method, e.path, e.sources_json, e.verification_state, e.verified_granularity
		FROM srcdb.edges e
		WHERE e."from" IN (SELECT id FROM srcdb.nodes WHERE service=?) OR e."to" IN (SELECT id FROM srcdb.nodes WHERE service=?)`, name, name); err != nil {
		return fmt.Errorf("copy edges: %w", err)
	}

	for _, t := range mergeTables {
		cols := strings.Join(t.columns, ", ")
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE service=?`, t.name), name); err != nil {
			return fmt.Errorf("sweep %s: %w", t.name, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (%s) SELECT %s FROM srcdb.%s`, t.name, cols, cols, t.name)); err != nil {
			return fmt.Errorf("copy %s: %w", t.name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// rebuildNodesFTS regenerates nodes_fts from the merged nodes table. It is
// derived state, not copied across the ATTACHed DBs (FTS5 virtual tables
// don't support cross-DB copy), so it is rebuilt in bulk once after every
// service has been merged rather than row-by-row per service. The
// "qualified" column mirrors (*graph.Node).QualifiedLabel(): meta's
// qualified_name if set, else meta's class, else empty.
func rebuildNodesFTS(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM nodes_fts`); err != nil {
		return fmt.Errorf("clear: %w", err)
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO nodes_fts (id, label, file, service, qualified)
		SELECT id, label, file, service,
			COALESCE(NULLIF(json_extract(meta, '$.qualified_name'), ''), json_extract(meta, '$.class'), '')
		FROM nodes`)
	return err
}
