// Package jobs implements the UB.3 background-job engine: index, eval, and
// reconcile runs triggered from the UI (POST /api/jobs), tracked with
// progress/cancellation, and persisted in ops.db so history survives a
// server restart. It wraps the exact same internals the CLI commands use
// (indexer.Run, eval.Run/RunAll, evidence.BuildReport) — one engine, two
// surfaces — never a bespoke reimplementation.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lordsonvimal/polyflow/internal/eval"
	"github.com/lordsonvimal/polyflow/internal/evidence"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/indexer"
	"github.com/lordsonvimal/polyflow/internal/ops"
	"github.com/lordsonvimal/polyflow/internal/semantic"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// Kind is the set of runnable job kinds.
type Kind string

const (
	KindIndex     Kind = "index"
	KindEval      Kind = "eval"
	KindReconcile Kind = "reconcile"
	KindInit      Kind = "init"
)

// Kinds lists the valid job kinds, in the order the 400 error names them.
var Kinds = []Kind{KindIndex, KindEval, KindReconcile, KindInit}

func validKind(k string) bool {
	for _, v := range Kinds {
		if string(v) == k {
			return true
		}
	}
	return false
}

// logRingCap is the in-memory log_tail ring buffer size; GET responses
// return only the last logTailReturn lines of it (UB.3 item 5).
const (
	logRingCap    = 200
	logTailReturn = 50
)

// progressThrottle is the minimum spacing between persisted/broadcast
// progress updates (UB.3 item 3: "throttled to >=100ms apart").
const progressThrottle = 100 * time.Millisecond

// ErrUnknownKind is returned by Start for a kind not in Kinds.
type ErrUnknownKind struct{ Kind string }

func (e ErrUnknownKind) Error() string {
	names := make([]string, len(Kinds))
	for i, k := range Kinds {
		names[i] = string(k)
	}
	return fmt.Sprintf("unknown job kind %q; supported kinds: %s", e.Kind, strings.Join(names, ", "))
}

// ErrEvalDirMissing is returned by Start for an eval job whose corpus path
// does not exist — honest 422, not a crash.
type ErrEvalDirMissing struct{ Path string }

func (e ErrEvalDirMissing) Error() string {
	return fmt.Sprintf("eval corpus path %q does not exist", e.Path)
}

// ErrConflict is returned by Start when the requested kind cannot run
// alongside an already-running job (single-flight, UB.3 item 1).
type ErrConflict struct{ Running ops.Job }

func (e ErrConflict) Error() string {
	return fmt.Sprintf("job %s (%s) is already running", e.Running.ID, e.Running.Kind)
}

// ResolveEmbedder builds the embedder to use for an index job from the
// workspace config, mirroring the CLI's selectEmbedder/resolveEmbedder.
// Injected so this package never depends on cmd/polyflow.
type ResolveEmbedder func(cfg *workspace.WorkspaceConfig) (emb semantic.Embedder, closeFn func(), err error)

// Options configures a Manager.
type Options struct {
	Ops             *ops.Store
	WorkspacePath   string           // polyflow.yml path (indexer.Options.Config source)
	DBPath          string           // graph.db path (reconcile job's store)
	Broadcast       func(msg string) // pushes a raw SSE JSON string; nil-safe
	ResolveEmbedder ResolveEmbedder  // required for index jobs
}

// Manager runs and tracks background jobs.
type Manager struct {
	opsStore      *ops.Store
	workspacePath string
	dbPath        string
	broadcast     func(string)
	resolveEmb    ResolveEmbedder

	mu      sync.Mutex
	running map[Kind]*runState // at most one entry per kind; index excludes all others
}

// runState is the live, in-memory view of a running job — more current than
// the persisted row between throttled writes, and the only place a job's
// context.CancelFunc lives.
type runState struct {
	job    ops.Job
	cancel context.CancelFunc

	logMu sync.Mutex
	log   []string

	lastPersist time.Time
}

// NewManager constructs a Manager. Ops may be nil (jobs disabled — Start
// returns an error naming that ops.db is unavailable, mirroring UB.2's
// nil-ops handling elsewhere in this server).
func NewManager(o Options) *Manager {
	broadcast := o.Broadcast
	if broadcast == nil {
		broadcast = func(string) {}
	}
	return &Manager{
		opsStore:      o.Ops,
		workspacePath: o.WorkspacePath,
		dbPath:        o.DBPath,
		broadcast:     broadcast,
		resolveEmb:    o.ResolveEmbedder,
		running:       make(map[Kind]*runState),
	}
}

// IndexArgs is the args payload for kind "index".
type IndexArgs struct {
	Full bool `json:"full"`
}

// EvalArgs is the args payload for kind "eval".
type EvalArgs struct {
	Corpus string `json:"corpus"`
	Case   string `json:"case"`
}

// ReconcileArgs is the args payload for kind "reconcile".
type ReconcileArgs struct {
	ProposeDir string `json:"propose_dir"`
}

// InitArgs is the args payload for kind "init" — the UO.7 setup-mode
// discovery job. Root defaults to "." (the server's working directory).
type InitArgs struct {
	Root string `json:"root"`
}

// Start validates and launches a job, returning its initial (running) record.
// argsJSON is the raw "args" object from the POST body (nil/empty → zero
// value for the kind's args struct).
func (m *Manager) Start(kind string, argsJSON json.RawMessage) (ops.Job, error) {
	if !validKind(kind) {
		return ops.Job{}, ErrUnknownKind{Kind: kind}
	}
	if m.opsStore == nil {
		return ops.Job{}, fmt.Errorf("jobs are not available (ops.db not open)")
	}
	k := Kind(kind)

	// Kind-specific pre-flight validation, before touching the single-flight slot.
	var evalArgs EvalArgs
	if k == KindEval {
		if len(argsJSON) > 0 {
			if err := json.Unmarshal(argsJSON, &evalArgs); err != nil {
				return ops.Job{}, fmt.Errorf("invalid args: %w", err)
			}
		}
		if evalArgs.Corpus == "" {
			evalArgs.Corpus = "eval/corpus"
		}
		if _, err := os.Stat(evalArgs.Corpus); err != nil {
			return ops.Job{}, ErrEvalDirMissing{Path: evalArgs.Corpus}
		}
	}

	m.mu.Lock()
	if conflict, ok := m.conflictLocked(k); ok {
		m.mu.Unlock()
		return ops.Job{}, ErrConflict{Running: conflict}
	}

	id := newJobID()
	ctx, cancel := context.WithCancel(context.Background())
	rs := &runState{
		job: ops.Job{
			ID:        id,
			Kind:      kind,
			Args:      string(normalizeArgs(argsJSON)),
			State:     "running",
			StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		},
		cancel: cancel,
	}
	m.running[k] = rs
	m.mu.Unlock()

	if err := m.opsStore.UpsertJob(context.Background(), rs.job); err != nil {
		fmt.Fprintf(os.Stderr, "polyflow: job record failed: %v\n", err)
	}

	go m.run(ctx, k, rs, argsJSON, evalArgs)

	return rs.job, nil
}

// conflictLocked reports the running job (if any) that blocks starting kind,
// per the single-flight rule: index excludes every other kind, and a kind
// already running blocks a second instance of itself. Must be called with
// m.mu held.
func (m *Manager) conflictLocked(k Kind) (ops.Job, bool) {
	if rs, ok := m.running[KindIndex]; ok {
		return rs.job, true
	}
	if k == KindIndex && len(m.running) > 0 {
		// Deterministic pick (bug-class rule 2: never map-iteration order) —
		// at most one of eval/reconcile is expected concurrently in practice,
		// but the tie-break is stable regardless.
		var kinds []string
		for rk := range m.running {
			kinds = append(kinds, string(rk))
		}
		sort.Strings(kinds)
		return m.running[Kind(kinds[0])].job, true
	}
	if rs, ok := m.running[k]; ok {
		return rs.job, true
	}
	return ops.Job{}, false
}

func normalizeArgs(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	return raw
}

// Get returns the freshest known state of job id: the live in-memory copy if
// it is still running, otherwise the persisted ops.db row.
func (m *Manager) Get(id string) (ops.Job, error) {
	m.mu.Lock()
	for _, rs := range m.running {
		if rs.job.ID == id {
			job := rs.snapshotLocked()
			m.mu.Unlock()
			return job, nil
		}
	}
	m.mu.Unlock()
	if m.opsStore == nil {
		return ops.Job{}, ops.ErrJobNotFound
	}
	return m.opsStore.GetJob(context.Background(), id)
}

// List returns the newest-first job history from ops.db, with any
// currently-running rows' in-memory (fresher) state substituted in.
func (m *Manager) List(limit int) ([]ops.Job, error) {
	if m.opsStore == nil {
		return nil, fmt.Errorf("jobs are not available (ops.db not open)")
	}
	jobs, err := m.opsStore.ListJobs(context.Background(), limit)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	live := make(map[string]ops.Job, len(m.running))
	for _, rs := range m.running {
		live[rs.job.ID] = rs.snapshotLocked()
	}
	m.mu.Unlock()
	for i, j := range jobs {
		if l, ok := live[j.ID]; ok {
			jobs[i] = l
		}
	}
	return jobs, nil
}

// Cancel requests cancellation of the running job id via its context. The
// job transitions to "canceled" asynchronously once the underlying work
// observes ctx.Err() — see run(). Returns the job's state at the moment of
// the request (still "running").
func (m *Manager) Cancel(id string) (ops.Job, error) {
	m.mu.Lock()
	for _, rs := range m.running {
		if rs.job.ID == id {
			job := rs.snapshotLocked()
			cancel := rs.cancel
			m.mu.Unlock()
			cancel()
			return job, nil
		}
	}
	m.mu.Unlock()
	// Not running: either unknown or already terminal — either way there is
	// nothing to cancel.
	if m.opsStore == nil {
		return ops.Job{}, ops.ErrJobNotFound
	}
	j, err := m.opsStore.GetJob(context.Background(), id)
	if err != nil {
		return ops.Job{}, err
	}
	if j.State == "running" {
		// Persisted as running but not in m.running: a prior server process
		// died mid-job. Nothing left to cancel; the row already misrepresents
		// reality, which GetJob callers should treat as stale.
		return j, fmt.Errorf("job %s has no active run to cancel (state stale from a previous process)", id)
	}
	return j, nil
}

func (rs *runState) snapshotLocked() ops.Job {
	rs.logMu.Lock()
	tail := lastN(rs.log, logTailReturn)
	rs.logMu.Unlock()
	j := rs.job
	j.LogTail = tail
	return j
}

func (rs *runState) appendLog(line string) {
	if line == "" {
		return
	}
	rs.logMu.Lock()
	rs.log = append(rs.log, line)
	if len(rs.log) > logRingCap {
		rs.log = rs.log[len(rs.log)-logRingCap:]
	}
	rs.logMu.Unlock()
}

func lastN(lines []string, n int) []string {
	if len(lines) <= n {
		out := make([]string, len(lines))
		copy(out, lines)
		return out
	}
	out := make([]string, n)
	copy(out, lines[len(lines)-n:])
	return out
}

// run executes the work for kind and finalizes rs.job (state/error/result),
// persists it, broadcasts job_done, and clears the single-flight slot.
func (m *Manager) run(ctx context.Context, k Kind, rs *runState, argsJSON json.RawMessage, evalArgs EvalArgs) {
	var (
		result string
		runErr error
	)
	switch k {
	case KindIndex:
		var args IndexArgs
		if len(argsJSON) > 0 {
			_ = json.Unmarshal(argsJSON, &args)
		}
		runErr = m.runIndex(ctx, rs, args)
	case KindEval:
		result, runErr = m.runEval(ctx, rs, evalArgs)
	case KindReconcile:
		var args ReconcileArgs
		if len(argsJSON) > 0 {
			_ = json.Unmarshal(argsJSON, &args)
		}
		result, runErr = m.runReconcile(ctx, rs, args)
	case KindInit:
		var args InitArgs
		if len(argsJSON) > 0 {
			_ = json.Unmarshal(argsJSON, &args)
		}
		result, runErr = m.runInit(ctx, rs, args)
	}

	state := "succeeded"
	errMsg := ""
	if runErr != nil {
		if ctx.Err() != nil {
			state = "canceled"
			errMsg = "canceled"
		} else {
			state = "failed"
			errMsg = runErr.Error()
		}
	}

	m.mu.Lock()
	rs.job.State = state
	rs.job.Error = errMsg
	rs.job.Result = result
	rs.job.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
	job := rs.snapshotLocked()
	delete(m.running, k)
	m.mu.Unlock()

	if m.opsStore != nil {
		if err := m.opsStore.UpsertJob(context.Background(), job); err != nil {
			fmt.Fprintf(os.Stderr, "polyflow: job record failed: %v\n", err)
		}
	}
	m.broadcastEvent("job_done", job)
}

func (m *Manager) broadcastEvent(typ string, job ops.Job) {
	msg, err := json.Marshal(map[string]any{"type": typ, "job": job})
	if err != nil {
		return
	}
	m.broadcast(string(msg))
}

// reportProgress updates rs's in-memory progress and, throttled to
// >=100ms apart, persists it and broadcasts job_progress (UB.3 item 3).
func (m *Manager) reportProgress(rs *runState, done, total int) {
	m.mu.Lock()
	rs.job.Progress.Done = done
	rs.job.Progress.Total = total
	throttled := time.Since(rs.lastPersist) < progressThrottle
	var job ops.Job
	if !throttled {
		rs.lastPersist = time.Now()
		job = rs.snapshotLocked()
	}
	m.mu.Unlock()
	if throttled {
		return
	}
	if m.opsStore != nil {
		if err := m.opsStore.UpsertJob(context.Background(), job); err != nil {
			fmt.Fprintf(os.Stderr, "polyflow: job record failed: %v\n", err)
		}
	}
	m.broadcastEvent("job_progress", job)
}

// ─── index ───────────────────────────────────────────────────────────────

func (m *Manager) runIndex(ctx context.Context, rs *runState, args IndexArgs) error {
	cfg, err := workspace.Load(m.workspacePath)
	if err != nil {
		return fmt.Errorf("load workspace: %w", err)
	}

	var emb semantic.Embedder
	if m.resolveEmb != nil {
		var closeFn func()
		emb, closeFn, err = m.resolveEmb(cfg)
		if err != nil {
			return fmt.Errorf("embedder: %w", err)
		}
		if closeFn != nil {
			defer closeFn()
		}
	}

	_, err = indexer.Run(ctx, indexer.Options{
		Config:       cfg,
		Full:         args.Full,
		Embedder:     emb,
		ContractsDir: filepath.Dir(m.workspacePath),
		Log:          &lineWriter{fn: rs.appendLog},
		Progress: func(done, total int) {
			m.reportProgress(rs, done, total)
		},
	})
	return err
}

// lineWriter splits writes on '\n' and forwards each complete line to fn —
// the bridge between indexer.Options.Log (an io.Writer) and the job's
// log_tail ring buffer.
type lineWriter struct {
	fn  func(string)
	buf strings.Builder
}

func (w *lineWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			w.fn(w.buf.String())
			w.buf.Reset()
			continue
		}
		w.buf.WriteByte(b)
	}
	return len(p), nil
}

// ─── eval ────────────────────────────────────────────────────────────────

func (m *Manager) runEval(ctx context.Context, rs *runState, args EvalArgs) (string, error) {
	if args.Case != "" {
		if _, err := os.Stat(filepath.Join(args.Corpus, "manifest.yaml")); err == nil {
			return runEvalSingle(ctx, args.Corpus, args.Case)
		}
	}
	if _, err := os.Stat(filepath.Join(args.Corpus, "manifest.yaml")); err == nil {
		return runEvalSingle(ctx, args.Corpus, args.Case)
	}
	multi, err := eval.RunAll(ctx, args.Corpus)
	if err != nil {
		return "", err
	}
	rs.appendLog(fmt.Sprintf("eval: %d repo(s) run, %d skipped, %d broken", len(multi.Reports), len(multi.Skipped), len(multi.Broken)))
	data, err := json.Marshal(multi)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func runEvalSingle(ctx context.Context, corpusDir, caseID string) (string, error) {
	report, err := eval.Run(ctx, eval.RunOptions{CorpusDir: corpusDir, CaseID: caseID})
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(report)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ─── reconcile ───────────────────────────────────────────────────────────

func (m *Manager) runReconcile(ctx context.Context, rs *runState, args ReconcileArgs) (string, error) {
	store, err := graph.NewSQLiteStore(m.dbPath)
	if err != nil {
		return "", fmt.Errorf("open store (run `polyflow index` first): %w", err)
	}
	defer store.Close()

	idx, err := store.BuildIndex(ctx)
	if err != nil {
		return "", fmt.Errorf("build index: %w", err)
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}

	report := evidence.BuildReport(idx.AllEdges())

	var written []string
	if args.ProposeDir != "" {
		if err := os.MkdirAll(args.ProposeDir, 0o755); err != nil {
			return "", fmt.Errorf("create propose-dir %s: %w", args.ProposeDir, err)
		}
		proposals := evidence.ProposeRules(report.GapList)
		sort.Slice(proposals, func(i, j int) bool { return proposals[i].Filename < proposals[j].Filename })
		for _, p := range proposals {
			path := filepath.Join(args.ProposeDir, p.Filename)
			if err := os.WriteFile(path, []byte(p.Content), 0o644); err != nil {
				return "", fmt.Errorf("write %s: %w", path, err)
			}
			written = append(written, path)
		}
		rs.appendLog(fmt.Sprintf("reconcile: %d proposal(s) written to %s", len(written), args.ProposeDir))
	}

	data, err := json.Marshal(map[string]any{
		"report":            report,
		"proposals_written": written,
	})
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ─── init (UO.7 setup mode) ────────────────────────────────────────────────

// runInit discovers services under args.Root using the exact same
// workspace.Discover internals `polyflow init` uses non-interactively. It
// does not write polyflow.yml — the setup wizard shows the result for
// confirmation first; POST /api/setup/apply does the write, byte-identical
// to `polyflow init`'s workspace.SaveInit call.
func (m *Manager) runInit(ctx context.Context, rs *runState, args InitArgs) (string, error) {
	root := args.Root
	if root == "" {
		root = "."
	}
	cfg, err := workspace.Discover(root)
	if err != nil {
		return "", fmt.Errorf("discover services: %w", err)
	}
	rs.appendLog(fmt.Sprintf("init: discovered %d service(s) under %s", len(cfg.Services), root))
	data, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
