package factpipe

import "testing"

// TestRhClientPath ports the retired TestRubyClientPath
// (internal/linker/ruby_http_hosts_test.go) — the host-placeholder and
// concreteness rules of the final path stamp, tested directly since
// rhClientPath is unexported.
func TestRhClientPath(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"prefixes the host placeholder", "/api/v1/health", "*/api/v1/health"},
		{"keeps an existing placeholder", "*/api/v1/health", "*/api/v1/health"},
		{"adds a leading slash", "api/v1/health", "*/api/v1/health"},
		{"unfilled hole becomes a wildcard", "/api/\x00id\x00/health", "*/api/*/health"},
		{"all-wildcard is not a path", "*/*", ""},
		{"host alone is not a path", "*", ""},
		{"empty stays empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rhClientPath(tc.in); got != tc.want {
				t.Errorf("rhClientPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRhCallArgs ports the retired TestRubyCallArgs — the text-level
// argument splitter, including the nested-call and unbalanced cases where
// it must yield nothing rather than a wrong split.
func TestRhCallArgs(t *testing.T) {
	cases := []struct {
		name, in string
		want     []string
	}{
		{"single string literal", `server_api_url("client_api/v1/x")`, []string{`"client_api/v1/x"`}},
		{"interpolated literal", `server_api_url("client_api/v1/lros/#{id}")`, []string{`"client_api/v1/lros/#{id}"`}},
		{"two args", `f("a", b)`, []string{`"a"`, "b"}},
		{"comma inside nested call", `f(g(a, b), c)`, []string{"g(a, b)", "c"}},
		{"comma inside string", `f("a,b")`, []string{`"a,b"`}},
		{"no arg list", `plain_url`, nil},
		{"unbalanced yields nothing", `f("a"`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rhCallArgs(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("rhCallArgs(%q) = %q, want %q", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("arg %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
