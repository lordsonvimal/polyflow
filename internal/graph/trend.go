package graph

import "sort"

// ServiceKindKey is the grouping key for trend rows.
type ServiceKindKey struct {
	Service string
	Kind    string
}

// TrendRow describes the unresolved count delta for one (service, kind) pair.
type TrendRow struct {
	Service  string
	Kind     string
	Latest   int    // count in the most recent run
	Baseline int    // count N runs ago (0 if fewer than N runs exist)
	Delta    int    // Latest - Baseline (positive = more unresolved)
	Runs     int    // total distinct run timestamps available for this pair
}

// ComputeTrend computes per-(service,kind) deltas from history rows.
// nBack is how many runs back the baseline is taken from; if fewer runs
// exist, the oldest available run is used as the baseline.
// Rows must come from ListUnresolvedHistory (newest first, then service/kind).
func ComputeTrend(history []UnresolvedHistoryRow, nBack int) []TrendRow {
	if len(history) == 0 {
		return nil
	}

	// Group by (service, kind), collecting (run_at, count) pairs newest-first.
	type entry struct {
		runAt int64
		count int
	}
	type pairData struct {
		entries []entry // newest first
	}
	pairs := map[ServiceKindKey]*pairData{}
	pairOrder := []ServiceKindKey{} // insertion order (already sorted by service/kind)

	for _, r := range history {
		k := ServiceKindKey{r.Service, r.Kind}
		if _, ok := pairs[k]; !ok {
			pairs[k] = &pairData{}
			pairOrder = append(pairOrder, k)
		}
		pairs[k].entries = append(pairs[k].entries, entry{r.RunAt, r.Count})
	}

	// Deduplicate: if the same run_at appears more than once (shouldn't happen
	// but guard it), keep the first (newest-first ordering).
	for _, k := range pairOrder {
		d := pairs[k]
		seen := map[int64]bool{}
		deduped := d.entries[:0]
		for _, e := range d.entries {
			if !seen[e.runAt] {
				seen[e.runAt] = true
				deduped = append(deduped, e)
			}
		}
		d.entries = deduped
	}

	// Sort pairOrder for stable output (service asc, kind asc).
	sort.Slice(pairOrder, func(i, j int) bool {
		a, b := pairOrder[i], pairOrder[j]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		return a.Kind < b.Kind
	})

	out := make([]TrendRow, 0, len(pairOrder))
	for _, k := range pairOrder {
		d := pairs[k]
		n := len(d.entries)
		latest := d.entries[0].count
		baselineIdx := nBack - 1
		if baselineIdx >= n {
			baselineIdx = n - 1
		}
		baseline := d.entries[baselineIdx].count
		out = append(out, TrendRow{
			Service:  k.Service,
			Kind:     k.Kind,
			Latest:   latest,
			Baseline: baseline,
			Delta:    latest - baseline,
			Runs:     n,
		})
	}
	return out
}

// DetectGrowth returns the services for which any (service, kind) pair shows
// monotonically increasing counts across the last `consec` run timestamps.
// "3 consecutive runs growing" means the last `consec` data points for that
// pair are each strictly larger than the previous (oldest → newest).
// History must be ordered newest first (as returned by ListUnresolvedHistory).
func DetectGrowth(history []UnresolvedHistoryRow, consec int) []string {
	if consec < 2 || len(history) == 0 {
		return nil
	}

	type entry struct {
		runAt int64
		count int
	}
	pairs := map[ServiceKindKey][]entry{}

	for _, r := range history {
		k := ServiceKindKey{r.Service, r.Kind}
		pairs[k] = append(pairs[k], entry{r.RunAt, r.Count})
	}

	flagged := map[string]bool{}
	for k, entries := range pairs {
		if len(entries) < consec {
			continue
		}
		// entries are newest-first; check the last `consec` entries
		// (oldest `consec` of what we have) for monotonic growth.
		window := entries[:consec] // newest consec entries (newest first)
		growing := true
		for i := 0; i < len(window)-1; i++ {
			// window[i] is newer than window[i+1]; growth means newer > older
			if window[i].count <= window[i+1].count {
				growing = false
				break
			}
		}
		if growing {
			flagged[k.Service] = true
		}
	}

	out := make([]string, 0, len(flagged))
	for svc := range flagged {
		out = append(out, svc)
	}
	sort.Strings(out)
	return out
}
