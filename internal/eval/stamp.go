package eval

import (
	"context"
	"fmt"
	"time"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// SaveTrustStamp scores report and persists a graph.TrustStamp to the
// workspace's meta table (plan-14 Tier T.0). The TrustStamp type itself
// lives in internal/graph (like VerificationSummary) because it must be
// importable from internal/impact, which this package (eval) already
// imports — eval cannot be imported back by impact without a cycle.
func SaveTrustStamp(ctx context.Context, store graph.Store, corpus string, report *Report) error {
	var hardFails, silentMisses int
	for _, r := range report.Results {
		if r.HardFail {
			hardFails++
		}
		silentMisses += r.SilentMisses
	}
	stamp := graph.TrustStamp{
		Measured:     true,
		Corpus:       corpus,
		Cases:        len(report.Results),
		Recall:       report.Recall,
		HardFails:    hardFails,
		SilentMisses: silentMisses,
		MeasuredAt:   time.Now().UTC().Format(time.RFC3339),
	}
	data, err := graph.EncodeTrustStamp(stamp)
	if err != nil {
		return fmt.Errorf("marshal trust stamp: %w", err)
	}
	if err := store.SetMeta(ctx, graph.TrustStampMetaKey, string(data)); err != nil {
		return fmt.Errorf("persist trust stamp: %w", err)
	}
	return nil
}
