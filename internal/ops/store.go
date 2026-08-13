// Package ops persists the tool-call audit log (MCP/CLI/UI) and ops-level
// settings (e.g. log retention) in a store separate from graph.db, so it
// survives the indexer's rebuild-then-atomic-rename of the graph database.
package ops

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SchemaVersion identifies the ops.db shape, recorded in the meta table for
// future migrations. Unlike graph.SchemaVersion this never forces a rebuild
// (there is nothing to rebuild from) — it is informational.
const SchemaVersion = "1"

// Schema is the SQLite DDL for ops.db.
const Schema = `
PRAGMA journal_mode=WAL;

CREATE TABLE IF NOT EXISTS tool_calls (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	ts               TEXT NOT NULL,
	source           TEXT NOT NULL,
	tool             TEXT NOT NULL,
	params           TEXT NOT NULL,
	duration_ms      INTEGER NOT NULL,
	status           TEXT NOT NULL,
	error            TEXT NOT NULL DEFAULT '',
	result           TEXT NOT NULL DEFAULT '',
	result_bytes     INTEGER NOT NULL,
	result_truncated INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_tool_calls_ts     ON tool_calls(ts);
CREATE INDEX IF NOT EXISTS idx_tool_calls_source ON tool_calls(source);
CREATE INDEX IF NOT EXISTS idx_tool_calls_tool   ON tool_calls(tool);
CREATE INDEX IF NOT EXISTS idx_tool_calls_status ON tool_calls(status);

CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS jobs (
	id             TEXT PRIMARY KEY,
	kind           TEXT NOT NULL,
	args           TEXT NOT NULL DEFAULT '{}',
	state          TEXT NOT NULL,
	started_at     TEXT NOT NULL,
	ended_at       TEXT NOT NULL DEFAULT '',
	progress_done  INTEGER NOT NULL DEFAULT 0,
	progress_total INTEGER NOT NULL DEFAULT 0,
	error          TEXT NOT NULL DEFAULT '',
	result         TEXT NOT NULL DEFAULT '',
	log_tail       TEXT NOT NULL DEFAULT '[]'
);

CREATE INDEX IF NOT EXISTS idx_jobs_started_at ON jobs(started_at);
CREATE INDEX IF NOT EXISTS idx_jobs_kind       ON jobs(kind);
`

// MaxResultBytes caps the persisted "result" text. Beyond this the row holds
// only the first MaxResultBytes bytes and result_truncated=1; result_bytes
// always reports the true untruncated size (a fixed constant, not a user
// setting — unlike retention, which controls row count).
const MaxResultBytes = 64 * 1024

// DefaultRetention is the tool_calls row-count cap seeded on first open.
const DefaultRetention = 100

// RetentionKey is the settings table key controlling tool_calls row count.
const RetentionKey = "tool_call_retention"

// MinRetention and MaxRetention bound a valid PUT /api/settings value.
const (
	MinRetention = 1
	MaxRetention = 10000
)

// Call is one recorded tool invocation, as supplied by a recording call
// site (MCP tool wrapper, UI handler middleware, or the CLI hook).
type Call struct {
	Source     string // "mcp" | "cli" | "ui"
	Tool       string
	Params     string // JSON
	DurationMS int64
	Status     string // "ok" | "error"
	Error      string
	Result     string // full output, before any capping
}

// ToolCall is a persisted tool_calls row.
type ToolCall struct {
	ID              int64  `json:"id"`
	TS              string `json:"ts"`
	Source          string `json:"source"`
	Tool            string `json:"tool"`
	Params          string `json:"params"`
	DurationMS      int64  `json:"duration_ms"`
	Status          string `json:"status"`
	Error           string `json:"error"`
	Result          string `json:"result"`
	ResultBytes     int64  `json:"result_bytes"`
	ResultTruncated bool   `json:"result_truncated"`
}

// Store is the ops.db-backed persistence for tool calls and ops settings.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at dsn and applies the schema.
// Mirrors graph.NewSQLiteStore's connection/migration pattern.
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open ops sqlite: %w", err)
	}
	// Single writer connection; WAL set in schema.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout=5000;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply busy_timeout: %w", err)
	}
	if _, err := db.Exec(Schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	s := &Store{db: db}
	if err := s.seed(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) seed() error {
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO settings (key, value) VALUES (?, ?)`,
		RetentionKey, strconv.Itoa(DefaultRetention)); err != nil {
		return fmt.Errorf("seed settings: %w", err)
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO meta (key, value) VALUES ('schema_version', ?)`,
		SchemaVersion); err != nil {
		return fmt.Errorf("seed meta: %w", err)
	}
	return nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error { return s.db.Close() }

// RecordCall inserts one tool-call row, capping result to MaxResultBytes,
// then applies retention (evicting the oldest rows beyond the current
// tool_call_retention setting) in the same transaction as the insert — the
// only eviction path, so "replaces the oldest log" holds exactly. Returns
// the inserted row and the ids of any rows evicted by retention.
func (s *Store) RecordCall(ctx context.Context, c Call) (ToolCall, []int64, error) {
	resultBytes := int64(len(c.Result))
	result := c.Result
	truncated := false
	if len(result) > MaxResultBytes {
		result = result[:MaxResultBytes]
		truncated = true
	}
	ts := time.Now().UTC().Format(time.RFC3339Nano)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ToolCall{}, nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	truncInt := 0
	if truncated {
		truncInt = 1
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO tool_calls (ts, source, tool, params, duration_ms, status, error, result, result_bytes, result_truncated)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, c.Source, c.Tool, c.Params, c.DurationMS, c.Status, c.Error, result, resultBytes, truncInt)
	if err != nil {
		return ToolCall{}, nil, fmt.Errorf("insert tool_call: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return ToolCall{}, nil, fmt.Errorf("last insert id: %w", err)
	}

	retention, err := retentionTx(ctx, tx)
	if err != nil {
		return ToolCall{}, nil, err
	}
	evicted, err := evictOverflowTx(ctx, tx, retention)
	if err != nil {
		return ToolCall{}, nil, err
	}

	if err := tx.Commit(); err != nil {
		return ToolCall{}, nil, fmt.Errorf("commit: %w", err)
	}

	return ToolCall{
		ID: id, TS: ts, Source: c.Source, Tool: c.Tool, Params: c.Params,
		DurationMS: c.DurationMS, Status: c.Status, Error: c.Error,
		Result: result, ResultBytes: resultBytes, ResultTruncated: truncated,
	}, evicted, nil
}

// ListFilter narrows GET /api/toolcalls.
type ListFilter struct {
	Source string
	Tool   string
	Status string
	Q      string // substring match over params, result, AND error
	Since  string // RFC3339Nano; ts >= Since
	Page   int    // 1-based; <1 -> 1
	Limit  int    // <=0 -> 100; >1000 -> 1000
}

// ListResult is the page returned by ListCalls.
type ListResult struct {
	Calls []ToolCall
	Total int
	Page  int
}

// ListCalls returns a filtered, paginated, newest-first page of tool_calls.
func (s *Store) ListCalls(ctx context.Context, f ListFilter) (ListResult, error) {
	page := f.Page
	if page < 1 {
		page = 1
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	var where []string
	var args []any
	if f.Source != "" {
		where = append(where, "source = ?")
		args = append(args, f.Source)
	}
	if f.Tool != "" {
		where = append(where, "tool = ?")
		args = append(args, f.Tool)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.Since != "" {
		where = append(where, "ts >= ?")
		args = append(args, f.Since)
	}
	if f.Q != "" {
		where = append(where, "(params LIKE ? OR result LIKE ? OR error LIKE ?)")
		like := "%" + f.Q + "%"
		args = append(args, like, like, like)
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM tool_calls "+whereClause, args...).Scan(&total); err != nil {
		return ListResult{}, fmt.Errorf("count tool_calls: %w", err)
	}

	query := "SELECT id, ts, source, tool, params, duration_ms, status, error, result, result_bytes, result_truncated FROM tool_calls " +
		whereClause + " ORDER BY id DESC LIMIT ? OFFSET ?"
	qargs := append(append([]any{}, args...), limit, (page-1)*limit)
	rows, err := s.db.QueryContext(ctx, query, qargs...)
	if err != nil {
		return ListResult{}, fmt.Errorf("list tool_calls: %w", err)
	}
	defer rows.Close()

	calls := make([]ToolCall, 0, limit)
	for rows.Next() {
		var c ToolCall
		var truncInt int
		if err := rows.Scan(&c.ID, &c.TS, &c.Source, &c.Tool, &c.Params, &c.DurationMS, &c.Status, &c.Error, &c.Result, &c.ResultBytes, &truncInt); err != nil {
			return ListResult{}, fmt.Errorf("scan tool_call: %w", err)
		}
		c.ResultTruncated = truncInt != 0
		calls = append(calls, c)
	}
	if err := rows.Err(); err != nil {
		return ListResult{}, err
	}

	return ListResult{Calls: calls, Total: total, Page: page}, nil
}

// DeleteAll clears every tool_calls row and returns the count deleted.
func (s *Store) DeleteAll(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tool_calls`)
	if err != nil {
		return 0, fmt.Errorf("delete tool_calls: %w", err)
	}
	return res.RowsAffected()
}

// GetRetention returns the current tool_call_retention setting.
func (s *Store) GetRetention(ctx context.Context) (int, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, RetentionKey).Scan(&v)
	if err == sql.ErrNoRows {
		return DefaultRetention, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read retention: %w", err)
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return DefaultRetention, nil
	}
	return n, nil
}

// SetRetention writes the new retention value and immediately trims
// tool_calls to it (oldest-first) if the new value is lower than the
// current row count. Raising retention after eviction never resurrects
// evicted rows — this is the only eviction path. Caller validates
// MinRetention <= n <= MaxRetention.
func (s *Store) SetRetention(ctx context.Context, n int) ([]int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, RetentionKey, strconv.Itoa(n)); err != nil {
		return nil, fmt.Errorf("write retention: %w", err)
	}

	evicted, err := evictOverflowTx(ctx, tx, n)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return evicted, nil
}

// retentionTx reads the current tool_call_retention setting within tx.
func retentionTx(ctx context.Context, tx *sql.Tx) (int, error) {
	var v string
	err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, RetentionKey).Scan(&v)
	if err == sql.ErrNoRows {
		return DefaultRetention, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read retention: %w", err)
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return DefaultRetention, nil
	}
	return n, nil
}

// evictOverflowTx deletes the oldest tool_calls rows beyond limit, within
// tx, and returns their ids (oldest-first) — the id order IS the eviction
// order since id is monotonically increasing.
func evictOverflowTx(ctx context.Context, tx *sql.Tx, limit int) ([]int64, error) {
	var total int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tool_calls`).Scan(&total); err != nil {
		return nil, fmt.Errorf("count tool_calls: %w", err)
	}
	if total <= limit {
		return nil, nil
	}
	overflow := total - limit
	rows, err := tx.QueryContext(ctx, `SELECT id FROM tool_calls ORDER BY id ASC LIMIT ?`, overflow)
	if err != nil {
		return nil, fmt.Errorf("select evict candidates: %w", err)
	}
	var evicted []int64
	for rows.Next() {
		var eid int64
		if err := rows.Scan(&eid); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan evict id: %w", err)
		}
		evicted = append(evicted, eid)
	}
	rows.Close()
	if len(evicted) == 0 {
		return nil, nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tool_calls WHERE id IN (`+placeholders(len(evicted))+`)`,
		int64SliceToAny(evicted)...); err != nil {
		return nil, fmt.Errorf("evict tool_calls: %w", err)
	}
	return evicted, nil
}

func placeholders(n int) string {
	ph := make([]string, n)
	for i := range ph {
		ph[i] = "?"
	}
	return strings.Join(ph, ",")
}

func int64SliceToAny(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// ErrJobNotFound is returned by GetJob when no row matches id.
var ErrJobNotFound = fmt.Errorf("job not found")

// JobProgress is the {done,total} pair reported during a running job (UB.3).
type JobProgress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

// Job is a persisted jobs row — the UB.3 background-job record (index/eval/
// reconcile), independent of tool_calls (this is state, not an audit entry).
type Job struct {
	ID        string      `json:"id"`
	Kind      string      `json:"kind"`
	Args      string      `json:"args"`  // JSON
	State     string      `json:"state"` // running|succeeded|failed|canceled
	StartedAt string      `json:"started_at"`
	EndedAt   string      `json:"ended_at,omitempty"`
	Progress  JobProgress `json:"progress"`
	Error     string      `json:"error,omitempty"`
	Result    string      `json:"result,omitempty"` // JSON, non-index kinds
	LogTail   []string    `json:"log_tail"`
}

// UpsertJob inserts or fully replaces the jobs row for j.ID — the single
// write path for both job creation and every progress/terminal-state update.
func (s *Store) UpsertJob(ctx context.Context, j Job) error {
	logTail, err := json.Marshal(j.LogTail)
	if err != nil {
		return fmt.Errorf("marshal log_tail: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO jobs (id, kind, args, state, started_at, ended_at, progress_done, progress_total, error, result, log_tail)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			kind = excluded.kind, args = excluded.args, state = excluded.state,
			started_at = excluded.started_at, ended_at = excluded.ended_at,
			progress_done = excluded.progress_done, progress_total = excluded.progress_total,
			error = excluded.error, result = excluded.result, log_tail = excluded.log_tail`,
		j.ID, j.Kind, j.Args, j.State, j.StartedAt, j.EndedAt,
		j.Progress.Done, j.Progress.Total, j.Error, j.Result, string(logTail))
	if err != nil {
		return fmt.Errorf("upsert job: %w", err)
	}
	return nil
}

// GetJob returns the persisted job row for id, or ErrJobNotFound.
func (s *Store) GetJob(ctx context.Context, id string) (Job, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, kind, args, state, started_at, ended_at, progress_done, progress_total, error, result, log_tail
		FROM jobs WHERE id = ?`, id)
	j, err := scanJob(row)
	if err == sql.ErrNoRows {
		return Job{}, ErrJobNotFound
	}
	if err != nil {
		return Job{}, fmt.Errorf("get job: %w", err)
	}
	return j, nil
}

// ListJobs returns jobs newest-first (by started_at, then id, both
// descending — id is monotonically increasing-ish via its timestamp prefix,
// so this is a stable deterministic order even for same-millisecond starts).
func (s *Store) ListJobs(ctx context.Context, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, kind, args, state, started_at, ended_at, progress_done, progress_total, error, result, log_tail
		FROM jobs ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanJob(row rowScanner) (Job, error) {
	var j Job
	var logTail string
	if err := row.Scan(&j.ID, &j.Kind, &j.Args, &j.State, &j.StartedAt, &j.EndedAt,
		&j.Progress.Done, &j.Progress.Total, &j.Error, &j.Result, &logTail); err != nil {
		return Job{}, err
	}
	if logTail != "" {
		_ = json.Unmarshal([]byte(logTail), &j.LogTail)
	}
	return j, nil
}
