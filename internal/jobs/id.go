package jobs

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// newJobID returns a "j-<millis><6 random hex chars>" id: time-sortable
// (millisecond prefix) with enough randomness to avoid same-millisecond
// collisions under concurrent Start calls of different kinds. Not a literal
// ULID (no dependency pulled in for one field) but shares its sortable-id
// shape.
func newJobID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("j-%d%s", time.Now().UnixMilli(), hex.EncodeToString(b[:]))
}
