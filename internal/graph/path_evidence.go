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
// Generic REST-namespace segments (`api`, `v1`, `v2`, …) are not counted: every
// service that exposes an HTTP API mounts something under `/api/v1`, so
// `*/api/v1/users` pins a service no better than `*/users` does — measured on the
// juniper fleet, `/api/v1/users` resolves in two services (willow and
// orion-atlas) because both are Rails apps with the same versioned-API
// convention. Stripping the namespace segments makes that a one-literal path the
// contract engine can suppress on fan-out, instead of a three-literal path it
// trusts.
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
		// Generic REST-namespace segments name a convention, not a route.
		if isAPINamespaceSegment(seg) {
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

// isAPINamespaceSegment reports whether seg is a generic REST-namespace token
// (`api`, `rest`, or a version marker like `v1`/`v2`) rather than a segment that
// names a resource. These appear in front of nearly every HTTP API and so carry
// no information about which service a call targets.
func isAPINamespaceSegment(seg string) bool {
	l := strings.ToLower(seg)
	if l == "api" || l == "rest" {
		return true
	}
	if len(l) >= 2 && l[0] == 'v' {
		for _, r := range l[1:] {
			if r < '0' || r > '9' {
				return false
			}
		}
		return true
	}
	return false
}
