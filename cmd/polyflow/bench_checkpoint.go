package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/agentbench"
)

// The checkpoint exists because a bench run spends real money one `claude -p`
// call at a time, and until E.2 the results of those calls only reached disk
// after the last one returned. A quota abort saved its partial report, but a
// Ctrl-C, a dropped connection or a crash threw away everything already paid
// for, and a re-run re-bought every task that had already succeeded.
//
// So: append each measured trial to a JSONL file as it completes, and on the
// next run skip the (task, arm, trial) combinations already bought.

// checkpointVersion guards the on-disk format. A record written by an older
// binary is discarded rather than misread.
const checkpointVersion = 1

// checkpointHeader is the first line of the file. Resuming into a run with a
// different model or corpus would mix two measurements into one report, so the
// parameters that define the measurement are recorded and compared.
type checkpointHeader struct {
	Version int      `json:"version"`
	Model   string   `json:"model"`
	Corpus  string   `json:"corpus"`
	Repo    string   `json:"repo"`
	Arms    []string `json:"arms"`
	Trials  int      `json:"trials"`
}

func (h checkpointHeader) equal(o checkpointHeader) bool {
	return h.Version == o.Version && h.Model == o.Model && h.Corpus == o.Corpus &&
		h.Repo == o.Repo && h.Trials == o.Trials &&
		strings.Join(h.Arms, ",") == strings.Join(o.Arms, ",")
}

// describe renders the fields a mismatch is worth reporting.
func (h checkpointHeader) describe() string {
	return fmt.Sprintf("model=%s corpus=%s repo=%q arms=[%s] trials=%d",
		h.Model, h.Corpus, h.Repo, strings.Join(h.Arms, ","), h.Trials)
}

// checkpointPath is keyed on the repo filter, not the date: a run resumed the
// morning after it was interrupted must find yesterday's work.
func checkpointPath(outDir, repo string) string {
	name := "all"
	if repo != "" {
		name = repo
	}
	return filepath.Join(outDir, ".checkpoint-"+name+".jsonl")
}

// trialKey identifies one paid invocation.
func trialKey(taskID, arm string, trial int) string {
	return fmt.Sprintf("%s\x00%s\x00%d", taskID, arm, trial)
}

// checkpoint appends completed trials to disk and answers what has already
// been bought.
type checkpoint struct {
	path string
	head checkpointHeader
	done map[string]agentbench.TaskResult
}

// loadCheckpoint opens (or starts) the checkpoint for this run.
//
// Only *measured* trials are treated as done. A recorded failure is not a
// purchase worth keeping — it is exactly the thing the operator is re-running
// to get past — so it is replayed.
//
// A header mismatch is an error rather than a silent overwrite: the alternative
// is quietly blending a sonnet run into an opus report.
func loadCheckpoint(outDir, repo string, head checkpointHeader, fresh bool) (*checkpoint, error) {
	head.Version = checkpointVersion
	c := &checkpoint{
		path: checkpointPath(outDir, repo),
		head: head,
		done: map[string]agentbench.TaskResult{},
	}
	if fresh {
		if err := os.Remove(c.path); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("discard checkpoint: %w", err)
		}
		return c, nil
	}

	f, err := os.Open(c.path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open checkpoint: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	if !sc.Scan() {
		return c, nil // empty file — nothing was ever bought
	}
	var onDisk checkpointHeader
	if err := json.Unmarshal(sc.Bytes(), &onDisk); err != nil {
		return nil, fmt.Errorf("checkpoint %s has an unreadable header: %w (use --fresh to discard it)", c.path, err)
	}
	if !onDisk.equal(c.head) {
		return nil, fmt.Errorf(
			"checkpoint %s was written for a different run\n  on disk: %s\n  now:     %s\nuse --fresh to discard it, or --output to keep the runs apart",
			c.path, onDisk.describe(), c.head.describe())
	}

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r agentbench.TaskResult
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			// A torn last line is the normal shape of an interrupted run.
			// Everything before it is still good.
			continue
		}
		if !r.Measured() {
			continue
		}
		c.done[trialKey(r.TaskID, r.Arm, r.Trial)] = r
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read checkpoint: %w", err)
	}
	return c, nil
}

// completed returns a previously bought result for this trial, if there is one.
func (c *checkpoint) completed(taskID, arm string, trial int) (agentbench.TaskResult, bool) {
	r, ok := c.done[trialKey(taskID, arm, trial)]
	return r, ok
}

func (c *checkpoint) count() int { return len(c.done) }

// record appends one finished trial, writing the header first if the file is
// new. Failures are recorded too — they are what a reader needs to understand
// why a resumed run has gaps — but they are not replayed as done.
//
// A checkpoint write failure does not abort the run: the call has already been
// paid for and the in-memory report still holds it. It is reported loudly
// because it means the next interruption will cost more than it should.
func (c *checkpoint) record(r agentbench.TaskResult) {
	if err := c.append(r); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not checkpoint %s [%s]: %v\n", r.TaskID, r.Arm, err)
		return
	}
	if r.Measured() {
		c.done[trialKey(r.TaskID, r.Arm, r.Trial)] = r
	}
}

func (c *checkpoint) append(r agentbench.TaskResult) error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0755); err != nil {
		return err
	}
	_, statErr := os.Stat(c.path)
	f, err := os.OpenFile(c.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	if os.IsNotExist(statErr) {
		head, err := json.Marshal(c.head)
		if err != nil {
			return err
		}
		if _, err := w.Write(append(head, '\n')); err != nil {
			return err
		}
	}
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(line, '\n')); err != nil {
		return err
	}
	return w.Flush()
}

// clear removes the checkpoint. Called only when a run completes in full, so
// that the next run of the same repo starts from scratch instead of silently
// replaying a finished measurement.
func (c *checkpoint) clear() {
	if err := os.Remove(c.path); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "warning: could not remove checkpoint %s: %v\n", c.path, err)
	}
}
