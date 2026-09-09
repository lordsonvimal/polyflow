package vgbaseline

import (
	"fmt"
	"sort"
	"strings"
)

// Class is the three-way classification of a differing row.
type Class string

const (
	// Gained: the candidate resolved something the baseline did not.
	Gained Class = "GAINED"
	// Lost: a baseline row the candidate no longer produces — a regression.
	Lost Class = "LOST"
	// Changed: same site, different resolved value.
	Changed Class = "CHANGED"
)

// Row is one line of a diff.
type Row struct {
	Class  Class
	Kind   string // "client" | "ledger"
	Key    string
	Detail string
}

// Diff is the full comparison of two baselines.
type Diff struct {
	Rows []Row
}

// HasDiff reports whether anything differs. vgdiff exits non-zero when true.
func (d *Diff) HasDiff() bool { return len(d.Rows) > 0 }

// Lost reports the number of regression rows (LOST or CHANGED) — VG.3/VG.4
// acceptance is "zero LOST and zero CHANGED".
func (d *Diff) Regressions() int {
	n := 0
	for _, r := range d.Rows {
		if r.Class == Lost || r.Class == Changed {
			n++
		}
	}
	return n
}

func (d *Diff) String() string {
	if len(d.Rows) == 0 {
		return "no differences\n"
	}
	rows := append([]Row(nil), d.Rows...)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Class != rows[j].Class {
			return rows[i].Class < rows[j].Class
		}
		if rows[i].Kind != rows[j].Kind {
			return rows[i].Kind < rows[j].Kind
		}
		return rows[i].Key < rows[j].Key
	})
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "%-7s %-6s %s\n", r.Class, r.Kind, r.Detail)
	}
	return b.String()
}

// clientCmpKey is the comparable projection of a client — node identity plus
// resolved value plus the http_call edge set. SourceRef.Layer/.Rule are
// excluded on purpose (see EdgeRecord).
func clientCmpKey(c ClientRecord) string {
	edges := make([]string, 0, len(c.Edges))
	for _, e := range c.Edges {
		edges = append(edges, e.From+"|"+e.To+"|"+e.Type)
	}
	sort.Strings(edges)
	return strings.Join([]string{
		c.ID, c.Service, c.File, fmt.Sprint(c.Line),
		c.Path, c.Method, c.URL,
		strings.Join(edges, ","),
	}, "\x1f")
}

func siteKey(c ClientRecord) string {
	return c.Service + "\x00" + c.File + "\x00" + fmt.Sprint(c.Line)
}

// Compare diffs baseline (base) against candidate (cand).
func Compare(base, cand *Baseline) *Diff {
	d := &Diff{}

	baseByID := map[string]ClientRecord{}
	for _, c := range base.Clients {
		baseByID[c.ID] = c
	}
	candByID := map[string]ClientRecord{}
	for _, c := range cand.Clients {
		candByID[c.ID] = c
	}

	var lostOnly, gainedOnly []ClientRecord
	for _, c := range base.Clients {
		if o, ok := candByID[c.ID]; ok {
			if clientCmpKey(c) != clientCmpKey(o) {
				d.Rows = append(d.Rows, Row{Changed, "client", c.ID,
					fmt.Sprintf("%s  %q -> %q", c.ID, valueOf(c), valueOf(o))})
			}
			continue
		}
		lostOnly = append(lostOnly, c)
	}
	for _, c := range cand.Clients {
		if _, ok := baseByID[c.ID]; !ok {
			gainedOnly = append(gainedOnly, c)
		}
	}

	// Re-pair a lost row with a gained row at the same site: that is a
	// resolved-value change (different ID because the ID embeds the path),
	// not a genuine loss + gain.
	gainedBySite := map[string][]int{}
	for i, c := range gainedOnly {
		gainedBySite[siteKey(c)] = append(gainedBySite[siteKey(c)], i)
	}
	usedGained := map[int]bool{}
	for _, lc := range lostOnly {
		cand := gainedBySite[siteKey(lc)]
		matched := -1
		for _, gi := range cand {
			if !usedGained[gi] {
				matched = gi
				break
			}
		}
		if matched >= 0 {
			usedGained[matched] = true
			gc := gainedOnly[matched]
			d.Rows = append(d.Rows, Row{Changed, "client", siteKey(lc),
				fmt.Sprintf("%s:%d  %q -> %q", lc.File, lc.Line, valueOf(lc), valueOf(gc))})
			continue
		}
		d.Rows = append(d.Rows, Row{Lost, "client", lc.ID,
			fmt.Sprintf("%s  (%s)", lc.ID, valueOf(lc))})
	}
	for gi, gc := range gainedOnly {
		if usedGained[gi] {
			continue
		}
		d.Rows = append(d.Rows, Row{Gained, "client", gc.ID,
			fmt.Sprintf("%s  (%s)", gc.ID, valueOf(gc))})
	}

	// Ledger: keyed by (file, line). A row present in the candidate but not
	// the baseline is a resolution the engine lost (LOST); the reverse is a
	// GAINED resolution.
	baseLedger := map[string]LedgerRecord{}
	for _, r := range base.Ledger {
		baseLedger[r.Key] = r
	}
	candLedger := map[string]LedgerRecord{}
	for _, r := range cand.Ledger {
		candLedger[r.Key] = r
	}
	for k, r := range candLedger {
		if _, ok := baseLedger[k]; !ok {
			d.Rows = append(d.Rows, Row{Lost, "ledger", k,
				fmt.Sprintf("%s:%d %s %q (now unresolved)", r.File, r.Line, r.Kind, r.Name)})
		}
	}
	for k, r := range baseLedger {
		if _, ok := candLedger[k]; !ok {
			d.Rows = append(d.Rows, Row{Gained, "ledger", k,
				fmt.Sprintf("%s:%d %s %q (now resolved)", r.File, r.Line, r.Kind, r.Name)})
		}
	}

	return d
}

func valueOf(c ClientRecord) string {
	v := c.URL
	if v == "" {
		v = c.Path
	}
	if c.Method != "" {
		return c.Method + " " + v
	}
	return v
}
