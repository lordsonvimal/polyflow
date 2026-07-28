package patterns

import "testing"

// TestLooksLikeHTTPEndpoint_X10b locks in the X.10b precision guard: a client
// URL is an endpoint only when it is an absolute path, a full URL, or an X.1
// "*/host" template reconstruction. Query-fragment shards ("**&id2=*") and
// scheme-mentioning string literals ("… http:// …") are noise an un-gated
// .Get(...)/.Post(...) captured by accident, and must be rejected so the bogus
// http_client producer is dropped.
func TestLooksLikeHTTPEndpoint_X10b(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		// Real endpoints — accepted.
		{"/api/v1/health", true},
		{"/", true},
		{"https://pypi.org/pypi/x", true},
		{"http://localhost:8080/x", true},
		{"*/api/v1/service/apps/register", true}, // X.1 wildcarded host
		{"*", true},                              // bare fully-dynamic reconstruction (ledgered elsewhere)

		// X.10b query-fragment shards — a "*" not followed by "/" — rejected.
		{"**&id2=*", false},
		{"**&environment_id=*", false},
		{"**&source_type=*", false},
		{"*?src=*", false},
		{"*=x", false},

		// X.10b string-literal noise: mentions a scheme but contains whitespace,
		// so it is prose, not a URL — rejected.
		{"Logo path must be a full URL (http:// or https://) or a path starting with /", false},
		{"see https://example.com", false},

		// Pre-existing bare-identifier / relative rejects still hold.
		{"user_id", false},
		{"static/js/x.js", false},
		{"", false},
	}
	for _, c := range cases {
		if got := looksLikeHTTPEndpoint(c.in); got != c.want {
			t.Errorf("looksLikeHTTPEndpoint(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
