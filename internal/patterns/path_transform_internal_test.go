package patterns

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// sveltekitTransform mirrors docs/factpipe-vocabulary.md's worked example.
func sveltekitTransform() *PathTransformSpec {
	return &PathTransformSpec{
		Delimiter: "/",
		DropSet:   []string{"", "index", "+page", "+server"},
		Rules: []PathTransformRule{
			{Match: "[[...*]]", Action: "unresolved"},
			{Match: "@*", Action: "unresolved"},
			{Match: "(*)", Action: "drop"},
			{Match: "[...*]", Replace: "*"},
			{Match: "[*]", Replace: ":$1"},
		},
	}
}

func TestApplyPathTransform(t *testing.T) {
	spec := sveltekitTransform()
	tests := []struct {
		name string
		text string
		want string
	}{
		{"static", "about", "/about"},
		{"index dropped", "index", "/"},
		{"dynamic segment", "blog/[slug]", "/blog/:slug"},
		{"nested dynamic", "posts/[id]/comments/[commentId]", "/posts/:id/comments/:commentId"},
		{"catch-all", "docs/[...slug]", "/docs/*"},
		{"route group dropped", "(marketing)/about", "/about"},
		{"page marker dropped", "blog/[slug]/+page", "/blog/:slug"},
		{"server marker dropped", "items/+server", "/items"},
		{"optional catch-all unresolved", "docs/[[...opt]]", ""},
		{"parallel route unresolved", "dashboard/@modal", ""},
		{"root", "", "/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applyPathTransform(tt.text, spec)[0].Str
			if got != tt.want {
				t.Errorf("applyPathTransform(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

func TestApplyPathTransform_NilSpec(t *testing.T) {
	if got := applyPathTransform("blog/[slug]", nil)[0].Str; got != "" {
		t.Errorf("nil spec: got %q, want \"\"", got)
	}
}

func TestApplyPathTransform_DefaultDelimiter(t *testing.T) {
	spec := &PathTransformSpec{Rules: []PathTransformRule{{Match: "[*]", Replace: ":$1"}}}
	got := applyPathTransform("posts/[id]", spec)[0].Str
	if got != "/posts/:id" {
		t.Errorf("default delimiter: got %q, want /posts/:id", got)
	}
}

func TestMatchGlob(t *testing.T) {
	tests := []struct {
		seg, pattern, wantCap string
		wantOK                bool
	}{
		{"[id]", "[*]", "id", true},
		{"[...slug]", "[...*]", "slug", true},
		{"[...slug]", "[*]", "...slug", true}, // ordering in the rule table, not the matcher, disambiguates
		{"[[...opt]]", "[...*]", "", false},   // prefix "[..." doesn't match "[[.."
		{"about", "[*]", "", false},
		{"index", "index", "", true},
		{"about", "index", "", false},
	}
	for _, tt := range tests {
		cap, ok := matchGlob(tt.seg, tt.pattern)
		if ok != tt.wantOK || (ok && cap != tt.wantCap) {
			t.Errorf("matchGlob(%q, %q) = (%q, %v), want (%q, %v)", tt.seg, tt.pattern, cap, ok, tt.wantCap, tt.wantOK)
		}
	}
}

// The ordered-rule-table contract: matchPathSegment must try [[...*]] before
// [...*] before [*], since a looser pattern would otherwise shadow the more
// specific ones (see TestMatchGlob's [...slug] vs [*] row).
func TestMatchPathSegment_OrderMatters(t *testing.T) {
	rules := sveltekitTransform().Rules
	mapped, action, matched := matchPathSegment("[[...opt]]", rules)
	if !matched || action != "unresolved" {
		t.Errorf("optional catch-all: got (%q, %q, %v), want unresolved", mapped, action, matched)
	}
	mapped, action, matched = matchPathSegment("[...slug]", rules)
	if !matched || action != "" || mapped != "*" {
		t.Errorf("catch-all: got (%q, %q, %v), want (*, \"\", true)", mapped, action, matched)
	}
	mapped, action, matched = matchPathSegment("[id]", rules)
	if !matched || action != "" || mapped != ":id" {
		t.Errorf("dynamic: got (%q, %q, %v), want (:id, \"\", true)", mapped, action, matched)
	}
}

// path_transform's config rides in the YAML as a sub-block on the arg, not a
// string verb spec — this pins the decode shape against the vocabulary doc's
// worked example (docs/factpipe-vocabulary.md §1f).
func TestArgSpec_PathTransformYAML(t *testing.T) {
	const doc = `
capture: file
extract: path_transform
path_transform:
  delimiter: "/"
  drop_set: ["", "index", "+page", "+server"]
  rules:
    - { match: "[[...*]]", action: unresolved }
    - { match: "@*",       action: unresolved }
    - { match: "(*)",      action: drop }
    - { match: "[...*]",   replace: "*" }
    - { match: "[*]",      replace: ":$1" }
`
	var spec ArgSpec
	if err := yaml.Unmarshal([]byte(doc), &spec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if spec.PathTransform == nil {
		t.Fatal("PathTransform not decoded")
	}
	if len(spec.PathTransform.Rules) != 5 {
		t.Fatalf("got %d rules, want 5", len(spec.PathTransform.Rules))
	}
	if got := applyPathTransform("blog/[slug]", spec.PathTransform)[0].Str; got != "/blog/:slug" {
		t.Errorf("decoded spec applyPathTransform = %q, want /blog/:slug", got)
	}
}

func TestFactSpec_ValidatePathTransform(t *testing.T) {
	pt := &PathTransformSpec{Rules: []PathTransformRule{{Match: "[*]", Replace: ":$1"}}}
	cases := []struct {
		name    string
		arg     ArgSpec
		wantErr bool
	}{
		{"extract without block", ArgSpec{Name: "path", Extract: "path_transform"}, true},
		{"block without extract", ArgSpec{Name: "path", Extract: "text", PathTransform: pt}, true},
		{"matched pair", ArgSpec{Name: "path", Extract: "path_transform", PathTransform: pt}, false},
		{"plain verb", ArgSpec{Name: "path", Extract: "text"}, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			f := FactSpec{Pred: "page_route", Args: ArgList{tt.arg}}
			err := f.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
