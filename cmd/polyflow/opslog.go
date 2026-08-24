package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/ops"
)

// opsRecording holds the state one CLI invocation needs to produce a
// tool_calls row (UB.2 item 3c): the CLI is the only source with no
// HTTP/MCP transport to piggyback on, so it opens ops.db itself and tees
// stdout for the duration of the command.
type opsRecording struct {
	store     *ops.Store
	tool      string
	params    string
	start     time.Time
	stdoutOld *os.File
	pipeW     *os.File
	buf       *bytes.Buffer
	copyDone  chan struct{}

	// CPU/memory profiling (UO.8): a CLI process runs exactly one command, so
	// wrapping the whole invocation in pprof.StartCPUProfile is safe — no
	// concurrent second command can ever race it within one process.
	cpuBuf    bytes.Buffer
	profiling bool
	memStart  runtime.MemStats
}

// startProfiling begins CPU profiling and takes the "before" memory
// snapshot. Called for every recording path (piped stdout or the pipe-
// unavailable fallback) so profiling coverage doesn't depend on os.Pipe
// succeeding.
func (rec *opsRecording) startProfiling() {
	runtime.ReadMemStats(&rec.memStart)
	if err := pprof.StartCPUProfile(&rec.cpuBuf); err == nil {
		rec.profiling = true
	}
}

// stopProfiling ends CPU profiling (if started) and returns the captured
// profile bytes plus the memory-stats delta since startProfiling.
func (rec *opsRecording) stopProfiling() ([]byte, ops.ProfileStats) {
	var cpuProfile []byte
	if rec.profiling {
		pprof.StopCPUProfile()
		cpuProfile = rec.cpuBuf.Bytes()
	}
	var memEnd runtime.MemStats
	runtime.ReadMemStats(&memEnd)
	return cpuProfile, ops.ProfileStats{
		AllocBytes:      int64(memEnd.Alloc),
		TotalAllocBytes: int64(memEnd.TotalAlloc - rec.memStart.TotalAlloc),
		HeapObjects:     int64(memEnd.HeapObjects),
		GCCount:         int64(memEnd.NumGC - rec.memStart.NumGC),
	}
}

// current is set by opsPersistentPreRun and consumed by opsFinalize. A CLI
// process runs exactly one command per invocation, so a single package-level
// slot (no concurrency) is sufficient.
var current *opsRecording

// opsPersistentPreRun is rootCmd's PersistentPreRunE (wired in init below).
// It fires once per invocation, for whichever leaf subcommand cobra
// resolved, before that subcommand's own RunE runs. Commands run outside
// any workspace (no polyflow.yml — e.g. before `init`, or `--version`/
// `--help` which never reach here) skip recording: there is nothing to key
// it to. Failure to open ops.db never fails the command.
func opsPersistentPreRun(cmd *cobra.Command, args []string) error {
	if _, err := os.Stat(meta.ConfigFile); err != nil {
		return nil // not in a workspace
	}
	store, err := ops.Open(filepath.Join(meta.DBDir, meta.OpsFile))
	if err != nil {
		fmt.Fprintf(os.Stderr, "polyflow: tool-call audit log disabled: %v\n", err)
		return nil
	}

	tool := strings.TrimPrefix(cmd.CommandPath(), rootCmd.Name()+" ")
	// longRunningTools never return until killed/interrupted (serve runs the
	// web/API server, mcp runs the stdio MCP loop) — profiling "the whole
	// process lifetime" isn't a meaningful per-operation measurement, and
	// worse, holding the one process-wide CPU profile for as long as `serve`
	// runs would starve every job UO.8 profiles inside it (jobs.Manager's
	// own StartCPUProfile can never acquire the slot). Skip CPU profiling
	// for these; duration/mem-stats/audit recording still happens.
	longRunningTools := map[string]bool{"serve": true, "mcp": true}
	skipCPUProfile := longRunningTools[tool]
	flags := map[string]string{}
	cmd.Flags().Visit(func(f *pflag.Flag) {
		flags[f.Name] = f.Value.String()
	})
	paramsJSON, _ := json.Marshal(map[string]any{
		"flags": flags,
		"args":  args,
	})

	r, w, perr := os.Pipe()
	if perr != nil {
		// Tee unavailable — still record params/timing/status, just no result text.
		rec := &opsRecording{store: store, tool: tool, params: string(paramsJSON), start: time.Now()}
		if !skipCPUProfile {
			rec.startProfiling()
		}
		current = rec
		return nil
	}

	rec := &opsRecording{
		store:     store,
		tool:      tool,
		params:    string(paramsJSON),
		start:     time.Now(),
		stdoutOld: os.Stdout,
		pipeW:     w,
		buf:       &bytes.Buffer{},
		copyDone:  make(chan struct{}),
	}
	if !skipCPUProfile {
		rec.startProfiling()
	}
	os.Stdout = w
	go func() {
		// Real stdout still streams live to the terminal — this is
		// observation, not redirection.
		_, _ = io.Copy(io.MultiWriter(rec.stdoutOld, rec.buf), r)
		close(rec.copyDone)
	}()
	current = rec
	return nil
}

// opsFinalize records the completed call and restores stdout. Called from
// main() after rootCmd.Execute() returns, rather than as a
// PersistentPostRunE: cobra's execute() returns immediately on a RunE error
// without invoking PersistentPostRunE, which would silently drop every
// error-status row — the one case the audit log most needs to capture.
// Recording failures never change the command's exit code or stdout.
func opsFinalize(cmdErr error) {
	rec := current
	current = nil
	if rec == nil {
		return
	}
	defer rec.store.Close()

	// Stop profiling before restoring/closing stdout: StopCPUProfile flushes
	// its buffer synchronously and doesn't touch stdout, but capturing the
	// "after" MemStats snapshot as close to command completion as possible
	// (before the pipe-drain/tee teardown below does its own allocation)
	// keeps the numbers honest.
	cpuProfile, profile := rec.stopProfiling()

	if rec.pipeW != nil {
		os.Stdout = rec.stdoutOld
		rec.pipeW.Close()
		<-rec.copyDone
	}

	status, errMsg := "ok", ""
	if cmdErr != nil {
		status, errMsg = "error", cmdErr.Error()
	}
	result := ""
	if rec.buf != nil {
		result = rec.buf.String()
	}

	if _, _, err := rec.store.RecordCall(context.Background(), ops.Call{
		Source:     "cli",
		Tool:       rec.tool,
		Params:     rec.params,
		DurationMS: time.Since(rec.start).Milliseconds(),
		Status:     status,
		Error:      errMsg,
		Result:     result,
		Profile:    profile,
		CPUProfile: cpuProfile,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "polyflow: ops record failed: %v\n", err)
	}
}

func init() {
	rootCmd.PersistentPreRunE = opsPersistentPreRun
}
