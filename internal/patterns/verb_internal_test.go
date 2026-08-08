package patterns

import "testing"

func TestNormalizeHTTPVerb(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		// Go: net/http constants, the idiomatic spelling.
		{"go const get", "http.MethodGet", "GET", true},
		{"go const post", "http.MethodPost", "POST", true},
		{"go const delete", "http.MethodDelete", "DELETE", true},
		{"go const options", "http.MethodOptions", "OPTIONS", true},

		// Ruby: a symbol is a literal verb.
		{"ruby symbol", ":get", "GET", true},
		{"ruby symbol patch", ":patch", "PATCH", true},

		// Plain literals: case-normalized, not otherwise touched.
		{"bare lowercase", "get", "GET", true},
		{"bare uppercase", "GET", "GET", true},

		// Declined. Each of these must be left verbatim by the caller rather
		// than guessed at — a wrong verb matches a wrong handler, which is
		// worse than an unmatched producer.
		{"empty", "", "", false},
		{"not a verb constant", "http.MethodFoo", "", false},
		{"selector that is not a verb const", "c.method", "", false},
		{"receiver field named method", "req.method", "", false},
		{"arbitrary identifier", "execute", "", false},
		{"verb-shaped but unqualified selector", "client.Get", "", false},
		{"bare colon", ":", "", false},
		{"symbol that is not a verb", ":raw_response", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := normalizeHTTPVerb(tt.raw)
			if ok != tt.ok || got != tt.want {
				t.Errorf("normalizeHTTPVerb(%q) = (%q, %v), want (%q, %v)",
					tt.raw, got, ok, tt.want, tt.ok)
			}
		})
	}
}

// A non-net/http package aliased to `http` is the documented benign failure
// mode: it still has to name its constant after a real verb to resolve, and
// the value produced is that verb either way. Pinned so the behavior is a
// decision on record rather than an accident.
func TestNormalizeHTTPVerb_NameMatchedOperandIsBenign(t *testing.T) {
	got, ok := normalizeHTTPVerb("http.MethodTrace")
	if !ok || got != "TRACE" {
		t.Errorf("normalizeHTTPVerb(http.MethodTrace) = (%q, %v), want (TRACE, true)", got, ok)
	}
}
