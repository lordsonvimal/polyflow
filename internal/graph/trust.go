package graph

import (
	"context"
	"encoding/json"
	"strconv"
	"time"
)

// TrustStampMetaKey is the DB meta key the trust stamp is persisted under
// (same mechanism as contract_coverage / unparsed_files — no SchemaVersion
// bump, meta keys are forward/backward compatible).
const TrustStampMetaKey = "trust_stamp"

// TrustStamp is the persisted record of the workspace's last measured eval
// (plan-14 Tier T.0). Measured=false is the zero state: this index has never
// been evaluated — absence of measurement is reported, never implied away.
// Always present on impact/context/trace results, like VerificationSummary —
// an absent section would look like certainty.
type TrustStamp struct {
	Measured     bool    `json:"measured"`
	Corpus       string  `json:"corpus,omitempty"`
	Cases        int     `json:"cases,omitempty"`
	Recall       float64 `json:"recall,omitempty"`
	HardFails    int     `json:"hard_fails,omitempty"`
	SilentMisses int     `json:"silent_misses,omitempty"`
	MeasuredAt   string  `json:"measured_at,omitempty"` // RFC 3339 UTC
	Stale        bool    `json:"stale,omitempty"`       // computed at read time, never stored
}

// persistedTrustStamp is the on-disk shape written to TrustStampMetaKey.
// Fields are declared in alphabetical key order so json.Marshal output is
// byte-deterministic without a map round-trip. Measured is not stored —
// presence of this record is what "measured" means; LoadTrustStamp sets
// TrustStamp.Measured=true on successful decode.
type persistedTrustStamp struct {
	Cases        int     `json:"cases,omitempty"`
	Corpus       string  `json:"corpus,omitempty"`
	HardFails    int     `json:"hard_fails,omitempty"`
	MeasuredAt   string  `json:"measured_at,omitempty"`
	Recall       float64 `json:"recall,omitempty"`
	SilentMisses int     `json:"silent_misses,omitempty"`
}

// EncodeTrustStamp marshals the measured fields of stamp to the sorted JSON
// persisted under TrustStampMetaKey.
func EncodeTrustStamp(stamp TrustStamp) ([]byte, error) {
	p := persistedTrustStamp{
		Cases:        stamp.Cases,
		Corpus:       stamp.Corpus,
		HardFails:    stamp.HardFails,
		MeasuredAt:   stamp.MeasuredAt,
		Recall:       stamp.Recall,
		SilentMisses: stamp.SilentMisses,
	}
	return json.Marshal(p)
}

// MetaReader is the minimal capability LoadTrustStamp needs. Store satisfies
// it; callers with a narrower store interface (e.g. mcpserver.Store) only
// need to add GetMeta to pass their store through directly.
type MetaReader interface {
	GetMeta(ctx context.Context, key string) (string, error)
}

// LoadTrustStamp reads the workspace's trust stamp via store's meta table.
// A never-stamped workspace (or a stamp that fails to parse) returns
// TrustStamp{Measured: false}, nil — absence of measurement is a valid,
// expected state, not an error.
//
// Stale is set when the "last_indexed" meta key (written by `polyflow
// index`) is newer than the stamp's MeasuredAt — the graph changed since it
// was measured.
func LoadTrustStamp(ctx context.Context, store MetaReader) (TrustStamp, error) {
	raw, err := store.GetMeta(ctx, TrustStampMetaKey)
	if err != nil {
		return TrustStamp{Measured: false}, nil
	}
	var p persistedTrustStamp
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return TrustStamp{Measured: false}, nil
	}
	stamp := TrustStamp{
		Measured:     true,
		Corpus:       p.Corpus,
		Cases:        p.Cases,
		Recall:       p.Recall,
		HardFails:    p.HardFails,
		SilentMisses: p.SilentMisses,
		MeasuredAt:   p.MeasuredAt,
	}

	lastIndexedRaw, err := store.GetMeta(ctx, "last_indexed")
	if err != nil {
		return stamp, nil
	}
	unix, err := strconv.ParseInt(lastIndexedRaw, 10, 64)
	if err != nil {
		return stamp, nil
	}
	measuredAt, err := time.Parse(time.RFC3339, p.MeasuredAt)
	if err != nil {
		return stamp, nil
	}
	stamp.Stale = time.Unix(unix, 0).UTC().After(measuredAt)
	return stamp, nil
}
