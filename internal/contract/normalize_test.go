package contract_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

func env0() contract.NormalizeEnv { return contract.NormalizeEnv{} }

func envLinks(from, to, baseURL string) contract.NormalizeEnv {
	return contract.NormalizeEnv{
		FromService: from,
		ToService:   to,
		Links:       []workspace.Link{{From: from, To: to, BaseURL: baseURL}},
	}
}

func callNorm(name, value string, env contract.NormalizeEnv) string {
	norm := contract.NormalizerByName(name)
	return norm(value, env)
}

// --- param_wildcard ---

func TestNormParamWildcard_ColonParam(t *testing.T) {
	assert.Equal(t, "/users/*", callNorm("param_wildcard", "/users/:id", env0()))
}

func TestNormParamWildcard_BraceParam(t *testing.T) {
	assert.Equal(t, "/users/*", callNorm("param_wildcard", "/users/{id}", env0()))
}

func TestNormParamWildcard_RegexParam(t *testing.T) {
	assert.Equal(t, "/users/*", callNorm("param_wildcard", "/users/[0-9]+", env0()))
}

func TestNormParamWildcard_NoParam(t *testing.T) {
	// Negative: no param patterns → unchanged
	assert.Equal(t, "/users/list", callNorm("param_wildcard", "/users/list", env0()))
}

func TestNormParamWildcard_PrintfVerb(t *testing.T) {
	// Go fmt.Sprintf-built client paths reduce to the same shape a :id handler does.
	assert.Equal(t, "/dsw/roles/*/update", callNorm("param_wildcard", "/dsw/roles/%d/update", env0()))
	assert.Equal(t, "/apps/*/config", callNorm("param_wildcard", "/apps/%s/config", env0()))
	assert.Equal(t, "/x/*", callNorm("param_wildcard", "/x/%02d", env0()))
}

func TestNormParamWildcard_PrintfDoesNotEatURLEncoding(t *testing.T) {
	// %XX URL-encoding must survive: the verb set is d/s/v, and %2f/%20 end in
	// a non-{d,s,v} char so they are left intact.
	assert.Equal(t, "/a%2fb", callNorm("param_wildcard", "/a%2fb", env0()))
	assert.Equal(t, "/a%20b", callNorm("param_wildcard", "/a%20b", env0()))
}

func TestNormParamWildcard_JSInterpolation(t *testing.T) {
	// X.10c: a JS template-literal `${…}` collapses its whole segment to * (the
	// literal decoration `-configs` is a runtime concatenation, not matchable),
	// so the client path meets the composed handler on the wildcard tier.
	assert.Equal(t, "/api/v1/*/*/dependent-apps",
		callNorm("param_wildcard", "/api/v1/${configType}-configs/${configId}/dependent-apps", env0()))
	// A bare interpolation segment reduces like any dynamic param.
	assert.Equal(t, "/users/*", callNorm("param_wildcard", "/users/${userId}", env0()))
	// Interpolation mid-segment with a prefix also collapses the whole segment.
	assert.Equal(t, "/files/*", callNorm("param_wildcard", "/files/img-${id}.png", env0()))
	// No interpolation → untouched (the `$` alone is not a trigger).
	assert.Equal(t, "/price/$5", callNorm("param_wildcard", "/price/$5", env0()))
}

// --- query_strip ---

func TestNormQueryStrip_WithQuery(t *testing.T) {
	assert.Equal(t, "/users", callNorm("query_strip", "/users?page=1", env0()))
}

func TestNormQueryStrip_NoQuery(t *testing.T) {
	// Negative: no query → unchanged
	assert.Equal(t, "/users", callNorm("query_strip", "/users", env0()))
}

// --- quote_strip ---

func TestNormQuoteStrip_DoubleQuotes(t *testing.T) {
	assert.Equal(t, "orders.created", callNorm("quote_strip", `"orders.created"`, env0()))
}

func TestNormQuoteStrip_SingleQuotes(t *testing.T) {
	assert.Equal(t, "orders.created", callNorm("quote_strip", `'orders.created'`, env0()))
}

func TestNormQuoteStrip_Backtick(t *testing.T) {
	assert.Equal(t, "orders.created", callNorm("quote_strip", "`orders.created`", env0()))
}

func TestNormQuoteStrip_NoQuotes(t *testing.T) {
	// Negative: no quotes → unchanged
	assert.Equal(t, "orders.created", callNorm("quote_strip", "orders.created", env0()))
}

func TestNormQuoteStrip_MismatchedQuotes(t *testing.T) {
	// Negative: mismatched quotes → unchanged
	assert.Equal(t, `"hello'`, callNorm("quote_strip", `"hello'`, env0()))
}

// --- case_fold ---

func TestNormCaseFold_Upper(t *testing.T) {
	assert.Equal(t, "get", callNorm("case_fold", "GET", env0()))
}

func TestNormCaseFold_Mixed(t *testing.T) {
	assert.Equal(t, "orders.created", callNorm("case_fold", "Orders.Created", env0()))
}

func TestNormCaseFold_AlreadyLower(t *testing.T) {
	// Negative: already lowercase → unchanged
	assert.Equal(t, "get", callNorm("case_fold", "get", env0()))
}

// --- trim_slash ---

func TestNormTrimSlash_TrailingSlash(t *testing.T) {
	assert.Equal(t, "/users", callNorm("trim_slash", "/users/", env0()))
}

func TestNormTrimSlash_MultipleSlashes(t *testing.T) {
	assert.Equal(t, "/users", callNorm("trim_slash", "/users///", env0()))
}

func TestNormTrimSlash_RootSlash(t *testing.T) {
	// Root path "/" must be preserved
	assert.Equal(t, "/", callNorm("trim_slash", "/", env0()))
}

func TestNormTrimSlash_NoTrailingSlash(t *testing.T) {
	// Negative: no trailing slash → unchanged
	assert.Equal(t, "/users", callNorm("trim_slash", "/users", env0()))
}

// --- base_url_strip ---

func TestNormBaseURLStrip_MatchingPair(t *testing.T) {
	env := envLinks("api", "web", "/api")
	assert.Equal(t, "/users", callNorm("base_url_strip", "/api/users", env))
}

func TestNormBaseURLStrip_RootAfterStrip(t *testing.T) {
	env := envLinks("api", "web", "/api")
	assert.Equal(t, "/", callNorm("base_url_strip", "/api", env))
}

func TestNormBaseURLStrip_NonMatchingService(t *testing.T) {
	// Negative: env (api→web) does not match the link (other→web) → unchanged
	env := contract.NormalizeEnv{
		FromService: "api",
		ToService:   "web",
		Links:       []workspace.Link{{From: "other", To: "web", BaseURL: "/api"}},
	}
	assert.Equal(t, "/api/users", callNorm("base_url_strip", "/api/users", env))
}

func TestNormBaseURLStrip_NoLinks(t *testing.T) {
	// Negative: no links → unchanged
	assert.Equal(t, "/api/users", callNorm("base_url_strip", "/api/users", env0()))
}

func TestNormBaseURLStrip_PathDoesNotStartWithPrefix(t *testing.T) {
	// Negative: path doesn't have the base_url prefix → unchanged
	env := envLinks("api", "web", "/api")
	assert.Equal(t, "/v2/users", callNorm("base_url_strip", "/v2/users", env))
}

// --- shared_anchor_guard ---

func TestNormSharedAnchorGuard_AllWildcards(t *testing.T) {
	// All wildcards → returns ""
	assert.Equal(t, "", callNorm("shared_anchor_guard", "*/*/", env0()))
}

func TestNormSharedAnchorGuard_SingleWildcard(t *testing.T) {
	assert.Equal(t, "", callNorm("shared_anchor_guard", "*", env0()))
}

func TestNormSharedAnchorGuard_HasLiteral(t *testing.T) {
	// Negative: has a literal segment → unchanged
	assert.Equal(t, "/users/*", callNorm("shared_anchor_guard", "/users/*", env0()))
}

func TestNormSharedAnchorGuard_Empty(t *testing.T) {
	// Empty input → returns ""
	assert.Equal(t, "", callNorm("shared_anchor_guard", "", env0()))
}

// --- url_to_path ---

func TestNormURLToPath_AbsoluteURL(t *testing.T) {
	assert.Equal(t, "/users", callNorm("url_to_path", "https://example.com/users", env0()))
}

func TestNormURLToPath_URLWithQuery(t *testing.T) {
	// url_to_path extracts the path; query_strip strips the query separately
	assert.Equal(t, "/users?page=1", callNorm("url_to_path", "https://example.com/users?page=1", env0()))
}

func TestNormURLToPath_AlreadyPath(t *testing.T) {
	// Already a path → unchanged (pass-through)
	assert.Equal(t, "/users", callNorm("url_to_path", "/users", env0()))
}

func TestNormURLToPath_HTTPMethod(t *testing.T) {
	// HTTP method "GET" is not a URL → returned unchanged (normalizer is a no-op)
	assert.Equal(t, "GET", callNorm("url_to_path", "GET", env0()))
}

func TestNormURLToPath_URLNoPath(t *testing.T) {
	// URL with no path → "/"
	assert.Equal(t, "/", callNorm("url_to_path", "https://example.com", env0()))
}

// --- RegisterNormalizer panics on duplicate ---

func TestRegisterNormalizer_DuplicatePanics(t *testing.T) {
	assert.Panics(t, func() {
		contract.RegisterNormalizer("trim_slash", func(v string, _ contract.NormalizeEnv) string { return v })
	})
}

// TestAMQPTopicWildcard_Normalizer locks J.1's topic collapse: wildcard segments
// (both AMQP forms) become "*", literal segments are untouched, and a value with
// no wildcard — every exchange name — is returned unchanged.
func TestAMQPTopicWildcard_Normalizer(t *testing.T) {
	fn := contract.NormalizerByName("amqp_topic_wildcard")
	if fn == nil {
		t.Fatal("amqp_topic_wildcard is not registered")
	}
	cases := []struct{ in, want string }{
		{"container.#", "container.*"},
		{"container.*", "container.*"},
		{"logs.build.*", "logs.build.*"},
		{"build.submit", "build.submit"},
		{"#", "*"},
		{"*", "*"},
		{"", ""},
		{"build_logs", "build_logs"},
		{"pdv.#", "pdv.*"},
		{"a.#.b", "a.*.b"},
	}
	for _, tc := range cases {
		if got := fn(tc.in, contract.NormalizeEnv{}); got != tc.want {
			t.Errorf("amqp_topic_wildcard(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
