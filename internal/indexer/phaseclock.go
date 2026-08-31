package indexer

import (
	"fmt"
	"io"
	"os"
	"time"
)

// phaseClock prints wall-clock durations for each pipeline phase when
// PF_INDEX_TIMING is set to a non-empty value. It is a diagnostic aid for
// "why is a cold index slow" questions — disabled, mark() is a cheap nil/bool
// check and emits nothing.
//
//	PF_INDEX_TIMING=1 polyflow index <svc> --full
type phaseClock struct {
	enabled bool
	w       io.Writer
	start   time.Time // run start, for the closing total
	last    time.Time // previous mark, for per-phase deltas
}

func newPhaseClock(w io.Writer) *phaseClock {
	now := time.Now()
	return &phaseClock{
		enabled: os.Getenv("PF_INDEX_TIMING") != "",
		w:       w,
		start:   now,
		last:    now,
	}
}

// mark records the time since the previous mark under the given phase name.
func (p *phaseClock) mark(name string) {
	if p == nil || !p.enabled {
		return
	}
	now := time.Now()
	fmt.Fprintf(p.w, "  ⏱  %-24s %8.2fs\n", name, now.Sub(p.last).Seconds())
	p.last = now
}

// done prints the cumulative wall time for the whole run.
func (p *phaseClock) done() {
	if p == nil || !p.enabled {
		return
	}
	fmt.Fprintf(p.w, "  ⏱  %-24s %8.2fs\n", "total", time.Since(p.start).Seconds())
}
