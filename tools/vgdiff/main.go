// Command vgdiff compares two Tier VG differential baselines
// (docs/js-value-graph-pilot-plan.md, VG.0).
//
//	go run ./tools/vgdiff baseline.json candidate.json
//
// It prints GAINED / LOST / CHANGED rows and exits non-zero on any difference.
// SourceRef.Layer and .Rule are ignored — VG.3 stamps them, so comparing them
// would flag every row on the run the tool exists to check.
package main

import (
	"fmt"
	"os"

	"github.com/lordsonvimal/polyflow/internal/vgbaseline"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: vgdiff <baseline.json> <candidate.json>")
		os.Exit(2)
	}
	base, err := vgbaseline.Load(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "vgdiff:", err)
		os.Exit(2)
	}
	cand, err := vgbaseline.Load(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "vgdiff:", err)
		os.Exit(2)
	}

	d := vgbaseline.Compare(base, cand)
	fmt.Print(d.String())
	if d.HasDiff() {
		fmt.Fprintf(os.Stderr, "\n%d row(s) differ (%d regression)\n", len(d.Rows), d.Regressions())
		os.Exit(1)
	}
}
