package graph

import "strings"

// Path-evidence strengths carried to the contract engine in
// Meta["path_evidence"]. Only "weak" is stamped on a node; "strong" is the
// default and needs no marker.
const (
	PathEvidenceNone   = ""
	PathEvidenceWeak   = "weak"
	PathEvidenceStrong = "strong"
)

// PathEvidence grades how well a composed pattern pins *which* service is being
// called. It is the gate for emitting an http_client producer at all, and the
// input to the contract engine's fan-out suppression rule.
//
// When the host is opaque — a struct field, a parameter, anything rendered `*` —
// the path is the only evidence there is. Two or more literal segments name an
// API surface (`*/client_api/v1/users`, `*/api/v1/pdv/reindex`) and are strong
// evidence. A single literal segment is the interesting case: `*/user-apps`
// names a real endpoint that exists in exactly one service, while `*/health`
// and `*/login` name conventions that every service implements.
//
// This used to be decided by requiring two literal segments, which is a proxy
// for the real question and got `*/user-apps` wrong — a silent false negative
// for every single-segment path behind an opaque host. The count cannot tell
// the two apart because the difference is not in the pattern: it is in how many
// services answer to it. So a single segment is reported as *weak* rather than
// rejected, and the contract engine — which can see the handlers — suppresses
// it exactly when it spans more than one service. Measured on the juniper
// fleet: `/health` resolves in 3 services (7 handlers) and `/login` in 2 (8
// handlers), both suppressed; `/user-apps` resolves in 1 (2 handlers) and links.
//
// A known host is self-discriminating, so one literal segment is strong there.
//
// This lives in graph rather than in the parser that first minted the stamp
// because any pass that *rewrites* Meta["path"] — Tier CB composes a
// config-supplied prefix onto it — must re-grade what it wrote, and the linker
// cannot import the parser.
func PathEvidence(pattern string) string {
	literals := 0
	for _, seg := range strings.Split(pattern, "/") {
		if seg == "" || strings.Contains(seg, "*") {
			continue
		}
		// A bare scheme (`http:`) is not path evidence.
		if strings.HasSuffix(seg, ":") {
			continue
		}
		literals++
	}
	switch {
	case literals == 0:
		return PathEvidenceNone
	case literals >= 2 || !strings.Contains(pattern, "*"):
		return PathEvidenceStrong
	default:
		return PathEvidenceWeak
	}
}
