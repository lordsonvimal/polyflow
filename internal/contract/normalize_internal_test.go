package contract

import "testing"

// TestPathMatchesPattern_WildcardAnchoring pins the acceptance rule for the
// wildcard_anchored tier. The rejection cases are the measured false-positive
// fan-out from a single maple-manager call site (Tier PR.2):
//
//	const url = `/api/v1/${configType}-configs/${configId}/dependent-apps`;
//
// which normalizes to /api/v1/*/*/dependent-apps and matched eight handler
// routes, of which two were correct.
func TestPathMatchesPattern_WildcardAnchoring(t *testing.T) {
	const depApps = "/api/v1/*/*/dependent-apps"

	cases := []struct {
		name    string
		key     string
		pattern string
		want    bool
	}{
		// The two real routes the call site reaches. Both survive: they agree
		// on the non-boilerplate literal `dependent-apps`.
		{"dependent-apps exec-configs", depApps, "/api/v1/exec-configs/*/dependent-apps", true},
		{"dependent-apps app-configs", depApps, "/api/v1/app-configs/*/dependent-apps", true},

		// The six false positives. Every one of them agreed on `api` and `v1`
		// and nothing else: the segments that would have discriminated
		// (`languages`, `admin`, …) sit against key-side wildcards, and so
		// were never compared at all.
		{"reject languages/ides", depApps, "/api/v1/languages/ides/*", false},
		{"reject user-apps category", depApps, "/api/v1/user-apps/category/*", false},
		{"reject apps category", depApps, "/api/v1/apps/category/*", false},
		{"reject ws build-logs", depApps, "/api/v1/ws/build-logs/*", false},
		{"reject ws workflow-runs", depApps, "/api/v1/ws/workflow-runs/*", false},
		{"reject cross-service admin users", depApps, "/api/v1/admin/users/*", false},

		// Load-bearing true positive from the hand-verified audit: a partial
		// path recovered from an interpolated template reaching a route whose
		// parameter sits mid-path.
		{"base-images sync", "/api/v1/base-images/*/sync", "/api/v1/base-images/*/sync", true},

		// Rule 1 alone: no non-boilerplate literal agreement anywhere.
		{"all-boilerplate anchor", "/api/v1/*/*/*", "/api/v1/admin/users/*", false},
		{"version segment is boilerplate", "/v2/*/*", "/v2/admin/users", false},

		{"key wildcard vs pattern literal tail", "/api/v1/base-images/*", "/api/v1/base-images/sync", true},

		// A concrete key tail landing on a route parameter is a client binding
		// that parameter, not a false match. Both of these are chessleap
		// golden edges that an earlier, stricter draft of this rule dropped.
		{"concrete tail binds route param", "/practice/*/assign-color/black", "/practice/*/assign-color/*", true},
		{"interpolated tail binds route param", "/practice/*/control/*/*engine*human", "/practice/*/control/*/*", true},

		// A key with no wildcards of its own is a concrete URL and binds route
		// params freely, including at the tail. This is the common case and
		// must not be touched by either rule.
		{"concrete url binds trailing param", "/api/v1/users/123", "/api/v1/users/*", true},
		{"concrete url all-boilerplate prefix", "/api/v1/123", "/api/v1/*", true},

		// Partial wildcard: `` `/api/v1/${configType}-configs/${id}/dependent-apps` ``
		// reconstructs to a segment `*-configs`, not a bare `*`. Its fixed
		// `-configs` suffix is a real anchor and must bind the route literals
		// `exec-configs` / `app-configs` — while still rejecting `ws` etc.
		{"partial wildcard binds exec-configs", "/api/v1/*-configs/*/dependent-apps", "/api/v1/exec-configs/*/dependent-apps", true},
		{"partial wildcard binds app-configs", "/api/v1/*-configs/*/dependent-apps", "/api/v1/app-configs/*/dependent-apps", true},
		{"partial wildcard rejects ws build-logs", "/api/v1/*-configs/*/dependent-apps", "/api/v1/ws/build-logs/*", false},
		{"partial wildcard suffix must match", "/maple/*-configs/x", "/maple/roles/x", false},
		{"partial wildcard do-revert", "/maple/*-configs/*/v/*/do-revert", "/maple/app-configs/*/v/*/do-revert", true},

		// Unchanged basics.
		{"length mismatch", "/api/v1/users", "/api/v1/users/*", false},
		{"literal conflict", "/api/v1/users/*", "/api/v1/teams/*", false},
		{"exact literals", "/api/v1/health", "/api/v1/health", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pathMatchesPattern(tc.key, tc.pattern); got != tc.want {
				t.Errorf("pathMatchesPattern(%q, %q) = %v, want %v", tc.key, tc.pattern, got, tc.want)
			}
		})
	}
}

func TestIsBoilerplateSegment(t *testing.T) {
	boilerplate := []string{"api", "API", "apis", "rest", "public", "internal", "v1", "v2", "v10", "V1"}
	for _, seg := range boilerplate {
		if !isBoilerplateSegment(seg) {
			t.Errorf("isBoilerplateSegment(%q) = false, want true", seg)
		}
	}
	// `v` alone is not a version, and a route named after a version-like word
	// is still a real segment.
	discriminating := []string{"v", "vault", "users", "dependent-apps", "admin", "health", "v1beta"}
	for _, seg := range discriminating {
		if isBoilerplateSegment(seg) {
			t.Errorf("isBoilerplateSegment(%q) = true, want false", seg)
		}
	}
}
