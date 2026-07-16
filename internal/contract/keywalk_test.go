package contract_test

// Tests for G.6 KeyWalker implementations. Each walker is tested via a
// parsed tree-sitter snippet (the node passed to WalkKey is the root of a
// re-parsed expression fragment — consistent with the pipeline where walkers
// process expression captures from tree-sitter pattern matches).

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sitter "github.com/smacker/go-tree-sitter"
	gositter "github.com/smacker/go-tree-sitter/golang"
	jssitter "github.com/smacker/go-tree-sitter/javascript"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/parser"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func parseJS(t *testing.T, src string) (*sitter.Node, []byte) {
	t.Helper()
	b := []byte(src)
	root, err := sitter.ParseCtx(context.Background(), b, jssitter.GetLanguage())
	require.NoError(t, err)
	return root, b
}

func parseGo(t *testing.T, src string) (*sitter.Node, []byte) {
	t.Helper()
	b := []byte(src)
	root, err := sitter.ParseCtx(context.Background(), b, gositter.GetLanguage())
	require.NoError(t, err)
	return root, b
}

func parseRuby(t *testing.T, src string) (*sitter.Node, []byte) {
	t.Helper()
	b := []byte(src)
	root, err := sitter.ParseCtx(context.Background(), b, rubysitter.GetLanguage())
	require.NoError(t, err)
	return root, b
}

// firstExpr descends through wrapper nodes (program, expression_statement) to
// return the innermost first expression node suitable for passing to WalkKey.
func firstExpr(root *sitter.Node) *sitter.Node {
	node := root
	for {
		var child *sitter.Node
		for i := 0; i < int(node.ChildCount()); i++ {
			c := node.Child(i)
			if c != nil && c.Type() != "comment" {
				child = c
				break
			}
		}
		if child == nil {
			return node
		}
		switch child.Type() {
		case "program", "expression_statement":
			node = child
		default:
			return child
		}
	}
}

func noConsts(name string) (string, bool) { return "", false }

// ── JS walker ────────────────────────────────────────────────────────────────

func TestJSWalker_Language(t *testing.T) {
	w := contract.KeyWalkerFor("javascript")
	require.NotNil(t, w)
	assert.Equal(t, "javascript", w.Language())
}

func TestJSWalker_StringLiteral(t *testing.T) {
	root, src := parseJS(t, `"/admin"`)
	w := contract.KeyWalkerFor("javascript")
	vals, dyn := w.WalkKey(firstExpr(root), src, noConsts)
	assert.False(t, dyn)
	assert.Equal(t, []string{"/admin"}, vals)
}

func TestJSWalker_Ternary(t *testing.T) {
	// isAdmin ? "/admin" : "/dashboard" → two candidates
	root, src := parseJS(t, `isAdmin ? "/admin" : "/dashboard"`)
	w := contract.KeyWalkerFor("javascript")
	vals, dyn := w.WalkKey(firstExpr(root), src, noConsts)
	assert.False(t, dyn)
	require.Len(t, vals, 2)
	sort.Strings(vals)
	assert.Equal(t, []string{"/admin", "/dashboard"}, vals)
}

func TestJSWalker_Identifier_Resolved(t *testing.T) {
	// const ORDERS_TOPIC = "orders.created" — resolved via ConstResolver
	root, src := parseJS(t, `ORDERS_TOPIC`)
	w := contract.KeyWalkerFor("javascript")
	resolver := func(name string) (string, bool) {
		if name == "ORDERS_TOPIC" {
			return "orders.created", true
		}
		return "", false
	}
	vals, dyn := w.WalkKey(firstExpr(root), src, resolver)
	assert.False(t, dyn)
	assert.Equal(t, []string{"orders.created"}, vals)
}

func TestJSWalker_Identifier_Dynamic(t *testing.T) {
	// Unknown identifier → dynamic
	root, src := parseJS(t, `someVar`)
	w := contract.KeyWalkerFor("javascript")
	vals, dyn := w.WalkKey(firstExpr(root), src, noConsts)
	assert.True(t, dyn)
	assert.Nil(t, vals)
}

func TestJSWalker_TemplateLiteral_Static(t *testing.T) {
	// Pure template literal (no interpolations) → single candidate
	root, src := parseJS(t, "`/admin`")
	w := contract.KeyWalkerFor("javascript")
	vals, dyn := w.WalkKey(firstExpr(root), src, noConsts)
	assert.False(t, dyn)
	require.Len(t, vals, 1)
	assert.Equal(t, "/admin", vals[0])
}

func TestJSWalker_TemplateLiteral_Dynamic(t *testing.T) {
	// Template with interpolation → dynamic
	root, src := parseJS(t, "`/users/${id}`")
	w := contract.KeyWalkerFor("javascript")
	vals, dyn := w.WalkKey(firstExpr(root), src, noConsts)
	assert.True(t, dyn)
	assert.Nil(t, vals)
}

func TestJSWalker_CallExpression_Dynamic(t *testing.T) {
	root, src := parseJS(t, `getHref()`)
	w := contract.KeyWalkerFor("javascript")
	vals, dyn := w.WalkKey(firstExpr(root), src, noConsts)
	assert.True(t, dyn)
	assert.Nil(t, vals)
}

// ── Go walker ────────────────────────────────────────────────────────────────

func TestGoWalker_Language(t *testing.T) {
	w := contract.KeyWalkerFor("go")
	require.NotNil(t, w)
	assert.Equal(t, "go", w.Language())
}

func TestGoWalker_StringLiteral(t *testing.T) {
	root, src := parseGo(t, `package p; var _ = "orders.created"`)
	// The string literal is inside a var declaration; walk to the string node
	w := contract.KeyWalkerFor("go")
	// Parse a bare expression — Go grammar requires a package context, so
	// we extract the interpreted_string_literal from the var declaration.
	var strNode *sitter.Node
	var findStr func(*sitter.Node)
	findStr = func(n *sitter.Node) {
		if n.Type() == "interpreted_string_literal" {
			strNode = n
			return
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			findStr(n.Child(i))
		}
	}
	findStr(root)
	require.NotNil(t, strNode)

	vals, dyn := w.WalkKey(strNode, src, noConsts)
	assert.False(t, dyn)
	assert.Equal(t, []string{"orders.created"}, vals)
}

func TestGoWalker_Identifier_Resolved(t *testing.T) {
	root, src := parseGo(t, `package p; var _ = ORDERS_TOPIC`)
	var identNode *sitter.Node
	var findIdent func(*sitter.Node)
	findIdent = func(n *sitter.Node) {
		if n.Type() == "identifier" && string(src[n.StartByte():n.EndByte()]) == "ORDERS_TOPIC" {
			identNode = n
			return
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			findIdent(n.Child(i))
		}
	}
	findIdent(root)
	require.NotNil(t, identNode)

	w := contract.KeyWalkerFor("go")
	resolver := func(name string) (string, bool) {
		if name == "ORDERS_TOPIC" {
			return "orders.created", true
		}
		return "", false
	}
	vals, dyn := w.WalkKey(identNode, src, resolver)
	assert.False(t, dyn)
	assert.Equal(t, []string{"orders.created"}, vals)
}

func TestGoWalker_Identifier_Dynamic(t *testing.T) {
	root, src := parseGo(t, `package p; var _ = computedTopic`)
	var identNode *sitter.Node
	var findIdent func(*sitter.Node)
	findIdent = func(n *sitter.Node) {
		if n.Type() == "identifier" && string(src[n.StartByte():n.EndByte()]) == "computedTopic" {
			identNode = n
			return
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			findIdent(n.Child(i))
		}
	}
	findIdent(root)
	require.NotNil(t, identNode)

	w := contract.KeyWalkerFor("go")
	vals, dyn := w.WalkKey(identNode, src, noConsts)
	assert.True(t, dyn)
	assert.Nil(t, vals)
}

// ── Ruby walker ──────────────────────────────────────────────────────────────

func TestRubyWalker_Language(t *testing.T) {
	w := contract.KeyWalkerFor("ruby")
	require.NotNil(t, w)
	assert.Equal(t, "ruby", w.Language())
}

func TestRubyWalker_StringLiteral(t *testing.T) {
	root, src := parseRuby(t, `"orders.created"`)
	w := contract.KeyWalkerFor("ruby")
	vals, dyn := w.WalkKey(firstExpr(root), src, noConsts)
	assert.False(t, dyn)
	assert.Equal(t, []string{"orders.created"}, vals)
}

func TestRubyWalker_Identifier_Dynamic(t *testing.T) {
	root, src := parseRuby(t, `topic_name`)
	w := contract.KeyWalkerFor("ruby")
	vals, dyn := w.WalkKey(firstExpr(root), src, noConsts)
	assert.True(t, dyn)
	assert.Nil(t, vals)
}

// ── HTML no-op walker ────────────────────────────────────────────────────────

func TestHTMLWalker_NoOp(t *testing.T) {
	w := contract.KeyWalkerFor("html")
	require.NotNil(t, w, "HTML must have a registered walker")
	// WalkKey on nil returns (nil, false) — no-op, no dynamic
	vals, dyn := w.WalkKey(nil, nil, noConsts)
	assert.False(t, dyn, "HTML walker must not flag as dynamic")
	assert.Nil(t, vals, "HTML walker must return no candidates")
	// Verify it is registered as no-op
	assert.Equal(t, "no-op", contract.KeyWalkerStatus("html"))
}

// ── templ walker ─────────────────────────────────────────────────────────────

func TestTemplWalker_Language(t *testing.T) {
	w := contract.KeyWalkerFor("templ")
	require.NotNil(t, w)
	assert.Equal(t, "templ", w.Language())
}

// ── walker-coverage guard ────────────────────────────────────────────────────

// TestWalkerCoverage_AllLanguagesHaveWalker fails if a registered parser
// language is MISSING a KeyWalker. This is the mechanical guard the checklist
// cannot provide: any new language added via parser.Register must also register
// a KeyWalker (even a no-op, for languages with only static attribute patterns).
func TestWalkerCoverage_AllLanguagesHaveWalker(t *testing.T) {
	for _, lang := range parser.RegisteredLanguages() {
		status := contract.KeyWalkerStatus(lang)
		assert.NotEqual(t, "MISSING", status,
			"language %q has no registered KeyWalker (register one or a no-op explicitly)", lang)
	}
}

// TestWalkerCoverage_9BranchIsDynamic verifies that the JS walker enforces
// the 8-branch cap: a ternary whose branches themselves have ternaries deeper
// than depth 2 is treated as dynamic (never partially enumerated).
func TestWalkerCoverage_9BranchIsDynamic(t *testing.T) {
	// Simulate a deeply-nested ternary (depth > 2) — the walker must return dynamic
	// rather than partially enumerate. We achieve depth-3 nesting:
	// a ? (b ? "/x" : "/y") : (c ? "/z" : (d ? "/w" : "/v"))
	src := `a ? (b ? "/x" : "/y") : (c ? "/z" : (d ? "/w" : "/v"))`
	root, bytes := parseJS(t, src)
	w := contract.KeyWalkerFor("javascript")
	_, dyn := w.WalkKey(firstExpr(root), bytes, noConsts)
	assert.True(t, dyn, "depth-exceeding ternary must be treated as dynamic")
}

// TestParseKeyCandidates verifies the JSON parse helper.
func TestParseKeyCandidates(t *testing.T) {
	assert.Nil(t, contract.ParseKeyCandidates(""))
	assert.Nil(t, contract.ParseKeyCandidates("not-json"))
	assert.Nil(t, contract.ParseKeyCandidates("[]"))
	assert.Equal(t, []string{"/a", "/b"}, contract.ParseKeyCandidates(`["/a","/b"]`))
}

// TestMarshalKeyCandidates verifies the JSON serialisation helper.
func TestMarshalKeyCandidates(t *testing.T) {
	assert.Equal(t, "", contract.MarshalKeyCandidates(nil))
	assert.Equal(t, `["/a","/b"]`, contract.MarshalKeyCandidates([]string{"/a", "/b"}))
}
