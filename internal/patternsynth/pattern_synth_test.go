package patternsynth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/patterns"
)

const fixtureCorpus = "testdata/corpus"

func fixtureOpts() Options {
	return Options{Package: "github.com/example/orion/router", Corpus: fixtureCorpus, Language: "go"}
}

// ── Sample ───────────────────────────────────────────────────────────────────

func TestSample_ClustersByCalleeShape(t *testing.T) {
	clusters, files, err := Sample(fixtureOpts())
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("corpus files = %d, want 3", len(files))
	}

	got := map[string]int{}
	methods := map[string][]string{}
	for _, c := range clusters {
		got[c.Key()] = len(c.Sites)
		methods[c.Key()] = c.Methods
	}
	want := map[string]int{
		"binding(interpreted_string_literal,identifier)":   7,
		"binding(interpreted_string_literal,func_literal)": 1,
		"binding(identifier)":                              2, // r.Use(logging) and r.SetLimit(maxInFlight)
		"package()":                                        1,
		// Not call sites: a declaration receiving a package value, and the call
		// that hands one over. Sampling only `receiver.Method(...)` left both
		// invisible to the proposer.
		"decl(function_declaration,router.Router)":  2,
		"decl(function_declaration,router.Handler)": 1,
		"bare()": 1,
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("cluster %s has %d sites, want %d (all: %v)", k, got[k], n, got)
		}
	}
	if len(clusters) != len(want) {
		t.Errorf("cluster count = %d, want %d: %v", len(clusters), len(want), got)
	}

	verbs := strings.Join(methods["binding(interpreted_string_literal,identifier)"], ",")
	if verbs != "Delete,Get,Post,Put" {
		t.Errorf("verb cluster methods = %q, want the four observed verbs sorted", verbs)
	}
}

// A file that never imports the package contributes no sites, however much its
// calls look like the package's. This is the whole of the sampler's precision.
func TestSample_UnimportedFileIsNotAttributed(t *testing.T) {
	clusters, _, err := Sample(fixtureOpts())
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	for _, c := range clusters {
		for _, s := range c.Sites {
			if s.File == "unrelated.go" {
				t.Errorf("sampled %s:%d from a file that does not import the package", s.File, s.Line)
			}
		}
	}
}

// The two shapes the sampler could not see. A declaration is where a function's
// binding to a package value is written down, and a bare call is where that
// value crosses a function boundary; neither is `receiver.Method(...)`, so
// neither produced a cluster, so no proposer could have reached them.
func TestSample_SamplesDeclarationsAndBareCalls(t *testing.T) {
	clusters, _, err := Sample(fixtureOpts())
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	byKind := map[string][]Site{}
	for _, c := range clusters {
		byKind[c.Kind] = append(byKind[c.Kind], c.Sites...)
	}

	decls := map[string]bool{}
	for _, s := range byKind[SiteDecl] {
		decls[s.Name] = true
		if s.TypeName == "" || s.DeclKind != "function_declaration" {
			t.Errorf("decl site %s has type %q kind %q, want a package type on a function declaration",
				s.Name, s.TypeName, s.DeclKind)
		}
	}
	for _, want := range []string{"Mount", "mountWillow", "logging"} {
		if !decls[want] {
			t.Errorf("no declaration site for %s: %v", want, decls)
		}
	}

	if len(byKind[SiteBareCall]) != 1 {
		t.Fatalf("bare-call sites = %d, want 1 (mountWillow(r))", len(byKind[SiteBareCall]))
	}
	bare := byKind[SiteBareCall][0]
	if bare.Method != "mountWillow" || len(bare.BindingArgs) != 1 || bare.BindingArgs[0] != 0 {
		t.Errorf("bare call = %+v, want mountWillow with the package value at argument 0", bare)
	}
}

// Attribution for a bare call is its arguments and nothing else: it names no
// package identifier, so a call that carries no package value is a local call
// the sampler must not claim.
func TestSample_BareCallWithoutAPackageValueIsNotAttributed(t *testing.T) {
	clusters, _, err := Sample(fixtureOpts())
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	for _, c := range clusters {
		for _, s := range c.Sites {
			if s.Kind == SiteBareCall && len(s.BindingArgs) == 0 {
				t.Errorf("attributed bare call %s at %s:%d with no package argument", s.Method, s.File, s.Line)
			}
		}
	}
}

func TestGoImportIdent_StripsMajorVersion(t *testing.T) {
	for path, want := range map[string]string{
		"github.com/go-chi/chi/v5":     "chi",
		"github.com/gin-gonic/gin":     "gin",
		"github.com/example/orion/v12": "orion",
		"router":                       "router",
	} {
		if got := goImportIdent(path); got != want {
			t.Errorf("goImportIdent(%q) = %q, want %q", path, got, want)
		}
	}
}

// ── Gate ─────────────────────────────────────────────────────────────────────

func TestGate_RejectsNonCompilingQuery(t *testing.T) {
	p := Proposal{Pattern: patterns.Pattern{
		Name:  "broken",
		Query: "(call_expression function: (no_such_node) @x",
	}}
	v := mustValidate(t, p, fixtureOpts())
	assertRejected(t, v, RejectCompile)
}

// The first of the two traps the plan requires: a bare `_` inside an
// argument_list binds anonymous tokens, so the query matches the opening
// parenthesis and every argument reads one position to the left.
func TestGate_RejectsUnanchoredWildcardInArgumentList(t *testing.T) {
	p := Proposal{Pattern: patterns.Pattern{
		Name: "shifted",
		Query: `(call_expression
  arguments: (argument_list _ (interpreted_string_literal) @path))`,
	}}
	v := mustValidate(t, p, fixtureOpts())
	assertRejected(t, v, RejectWildcard)
}

func TestUnanchoredWildcard(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  bool
	}{
		{"bare wildcard shifts arguments", `(argument_list _ (interpreted_string_literal) @p)`, true},
		{"parenthesized wildcard is the safe form", `(argument_list (_) @handler)`, false},
		{"anchored wildcard ignores anonymous nodes", `(argument_list . _ (interpreted_string_literal) @p)`, false},
		{"trailing anchor also anchors", `(argument_list (interpreted_string_literal) @p _ .)`, false},
		{"underscore-prefixed capture is not a wildcard", `(argument_list (func_literal) @_body)`, false},
		{"wildcard outside an argument_list is out of scope", `(block _ (call_expression) @c)`, false},
		{"underscore inside a string is not a wildcard", `(argument_list (identifier) @m (#eq? @m "_"))`, false},
		{"nested list's wildcard is not this list's", `(argument_list (argument_list _ (identifier) @i) @inner)`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, got := UnanchoredWildcard(tc.query); got != tc.want {
				t.Errorf("UnanchoredWildcard(%q) = %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}

// The second required trap: a specced pattern that fires on nothing consumes
// review budget and inflates the pattern count.
func TestGate_RejectsZeroHitPattern(t *testing.T) {
	p := Proposal{Pattern: patterns.Pattern{
		Name: "never_fires",
		Query: `(call_expression
  function: (selector_expression
    field: (field_identifier) @method
    (#eq? @method "NoSuchMethodInThisCorpus"))
  arguments: (argument_list (interpreted_string_literal) @path))`,
	}}
	v := mustValidate(t, p, fixtureOpts())
	assertRejected(t, v, RejectNoHits)
}

func TestGate_RejectsFanoutAboveCap(t *testing.T) {
	opts := fixtureOpts()
	opts.FanoutCap = 2
	p := Proposal{Pattern: patterns.Pattern{
		Name: "everything",
		Query: `(call_expression
  arguments: (argument_list (interpreted_string_literal) @path))`,
	}}
	v := mustValidate(t, p, opts)
	assertRejected(t, v, RejectFanout)
	if len(v.Hits) <= opts.FanoutCap {
		t.Errorf("fixture no longer exceeds the cap (%d hits): the test proves nothing", len(v.Hits))
	}
}

// `r.SetLimit(maxInFlight)` captures a value: not a literal, and not a
// reference to anything the corpus declares as callable. It records that a call
// happened and nothing about it.
func TestGate_RejectsPatternWithNoKeyCapture(t *testing.T) {
	res := mustRun(t, fixtureOpts())
	v := findVerdict(t, res, "router_setlimit")
	assertRejected(t, v, RejectNoKey)
	if v.KeyKind != KeyNone {
		t.Errorf("key kind = %q, want %q", v.KeyKind, KeyNone)
	}
	if len(v.Hits) == 0 {
		t.Error("router_setlimit should still be reported with its hits — a rejection nobody can see is not reviewable")
	}
}

// The other side of the same line: `r.Use(logging)` captures an identifier —
// a node type that says nothing on its own — but the value names a callable the
// corpus declares, which is as joinable as an inline handler. Rejecting it was
// the gate reading the query instead of what the query caught, and it cost
// every middleware registration in the corpus.
func TestGate_CallableReferenceIsAKey(t *testing.T) {
	res := mustRun(t, fixtureOpts())
	v := findVerdict(t, res, "router_use")
	if !v.Accepted {
		t.Fatalf("router_use rejected for %v, want accepted on its callable key", v.Rejects)
	}
	if v.KeyKind != KeyCallable {
		t.Errorf("key kind = %q, want %q", v.KeyKind, KeyCallable)
	}
	if v.CallableFrac < callableKeyFloor {
		t.Errorf("callable fraction = %.2f, want >= %.2f", v.CallableFrac, callableKeyFloor)
	}
}

// A cluster drops the method name so verbs merge. `Use` and `SetLimit` share a
// shape and disagree on the key, so the alternation has to split — otherwise
// the one real key is diluted by the value beside it and both are lost.
func TestPropose_SplitsClusterWhenMethodsDisagreeOnKey(t *testing.T) {
	res := mustRun(t, fixtureOpts())
	use := findVerdict(t, res, "router_use")
	limit := findVerdict(t, res, "router_setlimit")

	if len(use.Hits) != 1 || use.Hits[0].Line != 13 {
		t.Errorf("router_use hits = %v, want only the r.Use(logging) site", use.Hits)
	}
	for _, h := range limit.Hits {
		for _, u := range use.Hits {
			if h.Site() == u.Site() {
				t.Errorf("the two split patterns both match %s — the split must partition the sites", h.Site())
			}
		}
	}
}

func TestIsCallableRef(t *testing.T) {
	declared := map[string]bool{"logging": true, "Mount": true}
	cases := map[string]bool{
		"logging":           true,  // declared in the corpus
		"handlers.Mount":    true,  // qualified reference to a declared callable
		"&Mount":            true,  // address-of a declared callable
		"maxInFlight":       false, // a value the corpus does not declare as callable
		"authMiddleware()":  false, // a call passes a *result*; whether it is callable needs types
		"errors.New(\"x\")": false, // ... and this is why: `New` is declared all over a real corpus
		"gin.H{\"e\": 1}":   false, // a composite literal is not a reference
		"200":               false,
		"":                  false,
	}
	for text, want := range cases {
		if got := isCallableRef(text, declared); got != want {
			t.Errorf("isCallableRef(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestGate_MinPrecisionNeedsLabels(t *testing.T) {
	opts := fixtureOpts()
	opts.MinPrecision = 0.9

	res := mustRun(t, opts)
	for _, v := range res.Verdicts {
		if v.Proposal.Pattern.Name == "router_get_route" {
			assertRejected(t, v, RejectUnlabeled)
		}
	}

	// Same run, now with labels covering the sample: one of the six hits is
	// the false positive in unrelated.go, so precision is 5/6 and below the
	// floor.
	labels := map[string]bool{}
	for _, v := range res.Verdicts {
		for _, h := range v.Sample {
			labels[LabelKey(h)] = h.File != "unrelated.go"
		}
	}
	opts.Labels = labels
	res = mustRun(t, opts)
	v := findVerdict(t, res, "router_get_route")
	assertRejected(t, v, RejectPrecision)
	if v.Labeled != len(v.Sample) || v.Precision >= 0.9 {
		t.Errorf("precision = %.2f over %d labelled, want < 0.9 over the whole sample", v.Precision, v.Labeled)
	}
}

// ── Pipeline ─────────────────────────────────────────────────────────────────

func TestRun_AcceptsRouteAndGroup(t *testing.T) {
	res := mustRun(t, fixtureOpts())

	var accepted []string
	for _, v := range res.Accepted() {
		accepted = append(accepted, v.Proposal.Pattern.Name)
	}
	want := []string{
		"router_binding_arg_call", "router_get_route", "router_handler_param_decl",
		"router_route_group", "router_router_param_decl", "router_use",
	}
	if strings.Join(accepted, ",") != strings.Join(want, ",") {
		t.Fatalf("accepted = %v, want %v", accepted, want)
	}

	// The route pattern must fire in both files that register routes — a
	// pattern that only works in the file it was sampled from has generalized
	// nothing.
	v := findVerdict(t, res, "router_get_route")
	if v.Files < 2 {
		t.Errorf("router_get_route fired in %d files, want at least 2", v.Files)
	}
}

func TestRun_IsDeterministic(t *testing.T) {
	a, b := mustRun(t, fixtureOpts()), mustRun(t, fixtureOpts())
	if a.YAML != b.YAML {
		t.Error("two runs over the same corpus produced different files")
	}
	if a.CorpusSHA != b.CorpusSHA {
		t.Errorf("corpus SHA is unstable: %s vs %s", a.CorpusSHA, b.CorpusSHA)
	}
}

func TestRender_MatchesGoldenAndRoundTrips(t *testing.T) {
	res := mustRun(t, fixtureOpts())

	golden := filepath.Join("testdata", "router.golden.yaml")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, []byte(res.YAML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (regenerate with UPDATE_GOLDEN=1): %v", err)
	}
	if res.YAML != string(want) {
		t.Errorf("rendered file differs from %s:\n--- got ---\n%s", golden, res.YAML)
	}

	if !strings.HasPrefix(res.YAML, GeneratedMarker) {
		t.Error("rendered file does not open with the generated marker")
	}
	for _, must := range []string{res.CorpusSHA, res.Options.Package, res.Options.Corpus} {
		if !strings.Contains(res.YAML, must) {
			t.Errorf("header does not record %q — the pattern cannot be traced back to its evidence", must)
		}
	}

	// The output is a pattern file, not a lookalike: it must load, and its
	// patterns must still match what the run measured.
	tmp := filepath.Join(t.TempDir(), "router.yaml")
	if err := os.WriteFile(tmp, []byte(res.YAML), 0o644); err != nil {
		t.Fatal(err)
	}
	pf, err := patterns.LoadFile(tmp)
	if err != nil {
		t.Fatalf("rendered file does not parse: %v", err)
	}
	if len(pf.Patterns) != len(res.Accepted()) {
		t.Fatalf("rendered %d patterns, accepted %d", len(pf.Patterns), len(res.Accepted()))
	}
	hits, err := RunFile(pf, res.Files, res.Options)
	if err != nil {
		t.Fatalf("run rendered file: %v", err)
	}
	if want := totalHits(res); len(hits) != want {
		t.Errorf("rendered file matched %d sites, the run measured %d", len(hits), want)
	}
}

// ── Acceptance (the gate for the whole tier) ─────────────────────────────────

// Regenerating an existing hand-written pattern file from its corpus must
// produce a file whose hit set on that corpus is a superset of the
// hand-written one's, with no more than 10% additional hits. If synthesis
// cannot reproduce a pattern that already works, it will not discover ones
// that do not exist yet.
func TestAcceptance_ReproducesChiRoutes(t *testing.T) {
	const maxExtra = 0.10
	opts := Options{
		Package:  "github.com/go-chi/chi/v5",
		Corpus:   filepath.Join("..", "..", "patterns", "go", "chi_routes_test"),
		Language: "go",
	}
	res := mustRun(t, opts)

	cmp, err := Compare(res, filepath.Join("..", "..", "patterns", "go", "chi_routes.yaml"))
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if len(cmp.BaseSites) == 0 {
		t.Fatal("baseline matched nothing — the comparison would be vacuous")
	}
	if !cmp.Superset() {
		t.Errorf("synthesis missed %d of %d hand-written sites: %v",
			len(cmp.Missing), len(cmp.BaseSites), cmp.Missing)
	}
	if cmp.ExtraFrac > maxExtra {
		t.Errorf("synthesis added %d extra hits (%.1f%%), over the %.0f%% budget: %v",
			len(cmp.Extra), cmp.ExtraFrac*100, maxExtra*100, cmp.Extra)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func mustRun(t *testing.T, opts Options) Result {
	t.Helper()
	res, err := Run(opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res
}

func mustValidate(t *testing.T, p Proposal, opts Options) Verdict {
	t.Helper()
	_, files, err := Sample(opts)
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	v, err := Validate(p, files, opts)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	return v
}

func assertRejected(t *testing.T, v Verdict, reason string) {
	t.Helper()
	if v.Accepted {
		t.Fatalf("%s was accepted, want rejection %q", v.Proposal.Pattern.Name, reason)
	}
	for _, r := range v.Rejects {
		if strings.HasPrefix(r, reason) {
			return
		}
	}
	t.Errorf("%s rejected for %v, want %q", v.Proposal.Pattern.Name, v.Rejects, reason)
}

func findVerdict(t *testing.T, res Result, name string) Verdict {
	t.Helper()
	for _, v := range res.Verdicts {
		if v.Proposal.Pattern.Name == name {
			return v
		}
	}
	var names []string
	for _, v := range res.Verdicts {
		names = append(names, v.Proposal.Pattern.Name)
	}
	t.Fatalf("no verdict named %q in %v", name, names)
	return Verdict{}
}

func totalHits(res Result) int {
	n := 0
	for _, v := range res.Accepted() {
		n += len(v.Hits)
	}
	return n
}
