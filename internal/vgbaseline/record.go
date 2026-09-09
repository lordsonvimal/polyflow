// Package vgbaseline defines the frozen differential baseline for Tier VG
// (docs/js-value-graph-pilot-plan.md, VG.0).
//
// The baseline records, for one corpus, every http_client node minted or
// mutated by the four JS URL-resolution passes (js_local_url, js_prop_urls,
// js_prop_transport, js_prop_client) plus the prop_client_dynamic_url
// blind-spot ledger. Every later phase of the tier is verified by diffing a
// candidate capture against a committed baseline: the tier ships no new edges,
// so a LOST or CHANGED row is a regression.
package vgbaseline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// Baseline is one corpus's frozen resolver output.
type Baseline struct {
	Corpus  string         `json:"corpus"`
	Clients []ClientRecord `json:"clients"`
	Ledger  []LedgerRecord `json:"ledger"`
}

// ClientRecord is one http_client node and the http_call edges out of it.
type ClientRecord struct {
	ID      string       `json:"id"`
	Service string       `json:"service"`
	File    string       `json:"file"`
	Line    int          `json:"line"`
	Path    string       `json:"path"`
	Method  string       `json:"method"`
	URL     string       `json:"url"`
	Edges   []EdgeRecord `json:"edges"`
}

// EdgeRecord is one http_call edge. Layer and Rule carry SA.1 provenance and
// are deliberately ignored by Diff — VG.3 adds them, so comparing them would
// turn every row into CHANGED on the exact run the tool exists for.
type EdgeRecord struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Type  string `json:"type"`
	Layer string `json:"layer,omitempty"`
	Rule  string `json:"rule,omitempty"`
}

// LedgerRecord is one prop_client_dynamic_url UnresolvedRef, keyed as the
// linker's retraction logic keys it (file + line).
type LedgerRecord struct {
	Key     string `json:"key"`
	Service string `json:"service"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
}

// Sort orders every slice for a byte-stable on-disk form and normalizes nil
// slices to empty so an all-zero capture still round-trips.
func (b *Baseline) Sort() {
	if b.Clients == nil {
		b.Clients = []ClientRecord{}
	}
	if b.Ledger == nil {
		b.Ledger = []LedgerRecord{}
	}
	sort.Slice(b.Clients, func(i, j int) bool { return b.Clients[i].ID < b.Clients[j].ID })
	for ci := range b.Clients {
		if b.Clients[ci].Edges == nil {
			b.Clients[ci].Edges = []EdgeRecord{}
		}
		e := b.Clients[ci].Edges
		sort.Slice(e, func(i, j int) bool {
			if e[i].From != e[j].From {
				return e[i].From < e[j].From
			}
			if e[i].To != e[j].To {
				return e[i].To < e[j].To
			}
			return e[i].Type < e[j].Type
		})
	}
	sort.Slice(b.Ledger, func(i, j int) bool {
		if b.Ledger[i].Key != b.Ledger[j].Key {
			return b.Ledger[i].Key < b.Ledger[j].Key
		}
		return b.Ledger[i].Name < b.Ledger[j].Name
	})
}

// Marshal returns the canonical JSON encoding (sorted, indented, trailing
// newline) — two captures of the same cold index must be byte-identical.
func (b *Baseline) Marshal() ([]byte, error) {
	b.Sort()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(b); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Write marshals b to path.
func (b *Baseline) Write(path string) error {
	data, err := b.Marshal()
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Load reads a baseline file.
func Load(path string) (*Baseline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var b Baseline
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &b, nil
}
