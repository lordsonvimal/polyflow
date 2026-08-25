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

// stdinCapturingTools opts in commands whose real input arrives over stdin
// as a single JSON payload rather than as flags/args, so opsPersistentPreRun
// knows it's safe to read stdin to completion upfront and record it.
var stdinCapturingTools = map[string]bool{"hook-context-inject": true}

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
	paramsMap := map[string]any{
		"flags": flags,
		"args":  args,
	}
	// stdinCapturingTools' real input arrives as a JSON payload piped over
	// stdin (e.g. hook-context-inject's PostToolUse payload — see
	// hook_context_inject.go), not as flags/args, so "Input" in the tool-call
	// log would otherwise always serialize to {"args":[],"flags":{}} and never
	// show what the caller actually sent. Only opt in commands known to read
	// stdin exactly once and to completion (a single JSON object, then EOF) —
	// unlike `init --interactive`'s line-by-line prompts or `capture`'s
	// pass-through streaming to a child process, reading it fully upfront
	// here is safe for those.
	if stdinCapturingTools[tool] {
		if data, err := io.ReadAll(os.Stdin); err == nil {
			paramsMap["stdin"] = string(data)
			// Replace stdin with a fresh pipe fed from the bytes just
			// consumed, so the command's own Run still sees the same input.
			if pr, pw, perr := os.Pipe(); perr == nil {
				go func() {
					pw.Write(data)
					pw.Close()
				}()
				os.Stdin = pr
			}
		}
	}
	paramsJSON, _ := json.Marshal(paramsMap)

	// longRunningTools never return, so opsFinalize (which restores os.Stdout
	// and drains the tee pipe) never runs for them either — os.Stdout would
	// stay silently swapped to the pipe's write end for the rest of the
	// process's life. That's more than a missed observation: `serve` spawns
	// its own child processes (workspace-switch restart, self-update, the
	// templ sidecar) with cmd.Stdout = os.Stdout, so every one of them would
	// inherit a pipe whose read end dies the moment this process exits or
	// GCs the reader goroutine — the child's first stdout write then gets
	// SIGPIPE and it dies silently before doing anything else. Skip the tee
	// for these the same way CPU profiling already is.
	var r, w *os.File
	var perr error
	if !skipCPUProfile {
		r, w, perr = os.Pipe()
	}
	if skipCPUProfile || perr != nil {
		// Tee unavailable/skipped — still record params/timing/status, just no result text.
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
