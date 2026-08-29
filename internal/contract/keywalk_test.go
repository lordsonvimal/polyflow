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
	pythonsitter "github.com/smacker/go-tree-sitter/python"
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

func parsePython(t *testing.T, src string) (*sitter.Node, []byte) {
	t.Helper()
	b := []byte(src)
	root, err := sitter.ParseCtx(context.Background(), b, pythonsitter.GetLanguage())
	require.NoError(t, err)
	return root, b
}

// firstPythonExpr descends module > expression_statement to the bare
// expression node — Python's equivalent of firstExpr for the JS/Ruby
// grammars, which use "program" instead of "module" at the root.
func firstPythonExpr(root *sitter.Node) *sitter.Node {
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
		case "module", "expression_statement":
			node = child
		default:
			return child
		}
	}
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

// lastPythonExpr returns the expression of the last top-level statement in
// root (a module), unwrapping expression_statement — used by the PK.2
// if/elif/else branch tests, where the use site is a bare-identifier read
// following the assigning statements.
func lastPythonExpr(root *sitter.Node) *sitter.Node {
	var last *sitter.Node
	for i := 0; i < int(root.ChildCount()); i++ {
		c := root.Child(i)
		if c != nil && c.Type() != "comment" {
			last = c
		}
	}
	if last != nil && last.Type() == "expression_statement" {
		return last.Child(0)
	}
	return last
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

func TestJSWalker_TemplateLiteral_Reconstructed(t *testing.T) {
	// X.1b: template with interpolation reconstructs to a wildcarded
	// template instead of bailing dynamic — "${id}" becomes a "*" hole.
	root, src := parseJS(t, "`/users/${id}`")
	w := contract.KeyWalkerFor("javascript")
	vals, dyn := w.WalkKey(firstExpr(root), src, noConsts)
	assert.False(t, dyn)
	require.Len(t, vals, 1)
	assert.Equal(t, "/users/*", vals[0])
}

func TestJSWalker_TemplateLiteral_FullyWildcard_StaysDynamic(t *testing.T) {
	// A template with no concrete content at all (e.g. just "${id}") must
	// not reconstruct to a bare "*" — that could spuriously match any other
	// dynamic key at the normalized tier. Stays dynamic-ledgered.
	root, src := parseJS(t, "`${id}`")
	w := contract.KeyWalkerFor("javascript")
	vals, dyn := w.WalkKey(firstExpr(root), src, noConsts)
	assert.True(t, dyn)
	assert.Nil(t, vals)
}

func TestJSWalker_Concat_Reconstructed(t *testing.T) {
	// "room-" + id → "room-*"
	root, src := parseJS(t, `"room-" + id`)
	w := contract.KeyWalkerFor("javascript")
	vals, dyn := w.WalkKey(firstExpr(root), src, noConsts)
	assert.False(t, dyn)
	require.Len(t, vals, 1)
	assert.Equal(t, "room-*", vals[0])
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

func TestGoWalker_Sprintf_Reconstructed(t *testing.T) {
	root, src := parseGo(t, `package p; var _ = fmt.Sprintf("%s/api/v1/builds/%s", cfg.AgentURL, buildID)`)
	var callNode *sitter.Node
	var find func(*sitter.Node)
	find = func(n *sitter.Node) {
		if n.Type() == "call_expression" {
			if fn := n.ChildByFieldName("function"); fn != nil && fn.Type() == "selector_expression" {
				callNode = n
				return
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			find(n.Child(i))
		}
	}
	find(root)
	require.NotNil(t, callNode)

	w := contract.KeyWalkerFor("go")
	vals, dyn := w.WalkKey(callNode, src, noConsts)
	assert.False(t, dyn)
	require.Len(t, vals, 1)
	assert.Equal(t, "*/api/v1/builds/*", vals[0])
}

func TestGoWalker_StringConcat_Reconstructed(t *testing.T) {
	root, src := parseGo(t, `package p; var _ = "/activity/play/" + attemptID + "/move"`)
	// Take the outermost binary_expression only — the outer node wraps a
	// nested left-associative chain, and descending further would find the
	// inner (leftmost) sub-expression instead.
	var binNode *sitter.Node
	var find func(*sitter.Node)
	find = func(n *sitter.Node) {
		if binNode != nil {
			return
		}
		if n.Type() == "binary_expression" {
			binNode = n
			return
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			find(n.Child(i))
		}
	}
	find(root)
	require.NotNil(t, binNode)

	w := contract.KeyWalkerFor("go")
	vals, dyn := w.WalkKey(binNode, src, noConsts)
	assert.False(t, dyn)
	require.Len(t, vals, 1)
	assert.Equal(t, "/activity/play/*/move", vals[0])
}

func TestGoWalker_PathJoin_Reconstructed(t *testing.T) {
	root, src := parseGo(t, `package p; var _ = path.Join(base, "v1", id)`)
	var callNode *sitter.Node
	var find func(*sitter.Node)
	find = func(n *sitter.Node) {
		if n.Type() == "call_expression" {
			callNode = n
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			find(n.Child(i))
		}
	}
	find(root)
	require.NotNil(t, callNode)

	w := contract.KeyWalkerFor("go")
	vals, dyn := w.WalkKey(callNode, src, noConsts)
	assert.False(t, dyn)
	require.Len(t, vals, 1)
	assert.Equal(t, "*/v1/*", vals[0])
}

func TestGoWalker_FullyWildcardConcat_StaysDynamic(t *testing.T) {
	root, src := parseGo(t, `package p; var _ = a + b`)
	var binNode *sitter.Node
	var find func(*sitter.Node)
	find = func(n *sitter.Node) {
		if n.Type() == "binary_expression" {
			binNode = n
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			find(n.Child(i))
		}
	}
	find(root)
	require.NotNil(t, binNode)

	w := contract.KeyWalkerFor("go")
	vals, dyn := w.WalkKey(binNode, src, noConsts)
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

func TestRubyWalker_SimpleSymbol_Resolved(t *testing.T) {
	// PR.1: a symbol is a literal, so a key field holding one (`queue_name:
	// :builds`) must not be ledgered dynamic. Deliberately not upper-cased —
	// the contract engine's case_fold normalizer aligns the two sides.
	root, src := parseRuby(t, `:builds`)
	w := contract.KeyWalkerFor("ruby")
	vals, dyn := w.WalkKey(firstExpr(root), src, noConsts)
	assert.False(t, dyn)
	require.Len(t, vals, 1)
	assert.Equal(t, "builds", vals[0])
}

func TestRubyWalker_SimpleSymbol_ConstResolverStillWins(t *testing.T) {
	// The new symbol case sits on a different node type than the constant
	// case, so constant precedence is untouched. Pinned because a symbol and
	// a constant can name the same queue.
	root, src := parseRuby(t, `QUEUE`)
	w := contract.KeyWalkerFor("ruby")
	vals, dyn := w.WalkKey(firstExpr(root), src, func(name string) (string, bool) {
		if name == "QUEUE" {
			return "builds.high", true
		}
		return "", false
	})
	assert.False(t, dyn)
	assert.Equal(t, []string{"builds.high"}, vals)
}

func TestRubyWalker_Interpolation_Reconstructed(t *testing.T) {
	// X.1b: "room-#{room.id}" reconstructs to "room-*" instead of leaking
	// the #{...} marker into the raw key text (bug-class #6).
	root, src := parseRuby(t, `"room-#{room.id}"`)
	w := contract.KeyWalkerFor("ruby")
	vals, dyn := w.WalkKey(firstExpr(root), src, noConsts)
	assert.False(t, dyn)
	require.Len(t, vals, 1)
	assert.Equal(t, "room-*", vals[0])
}

func TestRubyWalker_Concat_Reconstructed(t *testing.T) {
	root, src := parseRuby(t, `"room-" + id`)
	w := contract.KeyWalkerFor("ruby")
	vals, dyn := w.WalkKey(firstExpr(root), src, noConsts)
	assert.False(t, dyn)
	require.Len(t, vals, 1)
	assert.Equal(t, "room-*", vals[0])
}

func TestRubyWalker_Shovel_Reconstructed(t *testing.T) {
	root, src := parseRuby(t, `"room-" << id`)
	w := contract.KeyWalkerFor("ruby")
	vals, dyn := w.WalkKey(firstExpr(root), src, noConsts)
	assert.False(t, dyn)
	require.Len(t, vals, 1)
	assert.Equal(t, "room-*", vals[0])
}

func TestRubyWalker_FullyInterpolated_StaysDynamic(t *testing.T) {
	root, src := parseRuby(t, `"#{room.id}"`)
	w := contract.KeyWalkerFor("ruby")
	vals, dyn := w.WalkKey(firstExpr(root), src, noConsts)
	assert.True(t, dyn)
	assert.Nil(t, vals)
}

// ── Python walker ────────────────────────────────────────────────────────────

func TestPythonWalker_Language(t *testing.T) {
	w := contract.KeyWalkerFor("python")
	require.NotNil(t, w)
	assert.Equal(t, "python", w.Language())
}

func TestPythonWalker_StringLiteral(t *testing.T) {
	root, src := parsePython(t, `"/admin"`)
	w := contract.KeyWalkerFor("python")
	vals, dyn := w.WalkKey(firstPythonExpr(root), src, noConsts)
	assert.False(t, dyn)
	assert.Equal(t, []string{"/admin"}, vals)
}

func TestPythonWalker_Ternary(t *testing.T) {
	// "/a" if flag else "/b" → two candidates (PK gate 2)
	root, src := parsePython(t, `"/a" if flag else "/b"`)
	w := contract.KeyWalkerFor("python")
	vals, dyn := w.WalkKey(firstPythonExpr(root), src, noConsts)
	assert.False(t, dyn)
	require.Len(t, vals, 2)
	sort.Strings(vals)
	assert.Equal(t, []string{"/a", "/b"}, vals)
}

func TestPythonWalker_Identifier_Resolved(t *testing.T) {
	root, src := parsePython(t, `BASE_URL`)
	w := contract.KeyWalkerFor("python")
	resolver := func(name string) (string, bool) {
		if name == "BASE_URL" {
			return "https://example.com", true
		}
		return "", false
	}
	vals, dyn := w.WalkKey(firstPythonExpr(root), src, resolver)
	assert.False(t, dyn)
	assert.Equal(t, []string{"https://example.com"}, vals)
}

func TestPythonWalker_Identifier_Dynamic(t *testing.T) {
	root, src := parsePython(t, `some_var`)
	w := contract.KeyWalkerFor("python")
	vals, dyn := w.WalkKey(firstPythonExpr(root), src, noConsts)
	assert.True(t, dyn)
	assert.Nil(t, vals)
}

func TestPythonWalker_FString_ResolvedConstant(t *testing.T) {
	// PK gate 3: f"{BASE_URL}/users" where BASE_URL is a module-level literal
	// resolves to a concrete host/path, not a ledger entry.
	root, src := parsePython(t, `f"{BASE_URL}/users"`)
	w := contract.KeyWalkerFor("python")
	resolver := func(name string) (string, bool) {
		if name == "BASE_URL" {
			return "https://example.com", true
		}
		return "", false
	}
	vals, dyn := w.WalkKey(firstPythonExpr(root), src, resolver)
	assert.False(t, dyn)
	require.Len(t, vals, 1)
	assert.Equal(t, "https://example.com/users", vals[0])
}

func TestPythonWalker_FString_UnresolvedStaysDynamic(t *testing.T) {
	// PK gate 4: an unresolvable interpolated identifier still ledgers
	// cleanly — Python does not wildcard-hole an unresolved host the way
	// Ruby/JS do for path segments.
	root, src := parsePython(t, `f"{unresolvable_var}/x"`)
	w := contract.KeyWalkerFor("python")
	vals, dyn := w.WalkKey(firstPythonExpr(root), src, noConsts)
	assert.True(t, dyn)
	assert.Nil(t, vals)
}

func TestPythonWalker_FString_PlainLiteral(t *testing.T) {
	// No interpolation at all — same "string" node type, must still resolve
	// as a plain literal.
	root, src := parsePython(t, `f"/admin"`)
	w := contract.KeyWalkerFor("python")
	vals, dyn := w.WalkKey(firstPythonExpr(root), src, noConsts)
	assert.False(t, dyn)
	assert.Equal(t, []string{"/admin"}, vals)
}

func TestPythonWalker_IfElifElse_Branches(t *testing.T) {
	// PK.2: name assigned once per mutually-exclusive branch of a single
	// if/elif/else, all branches literal, else present → enumerate all three.
	src := "if flag:\n" +
		"    x = \"/a\"\n" +
		"elif flag2:\n" +
		"    x = \"/b\"\n" +
		"else:\n" +
		"    x = \"/c\"\n" +
		"x\n"
	root, srcB := parsePython(t, src)
	w := contract.KeyWalkerFor("python")
	vals, dyn := w.WalkKey(lastPythonExpr(root), srcB, noConsts)
	assert.False(t, dyn)
	require.Len(t, vals, 3)
	sort.Strings(vals)
	assert.Equal(t, []string{"/a", "/b", "/c"}, vals)
}

func TestPythonWalker_IfElse_NoElif_Branches(t *testing.T) {
	// Same shape without an elif — just if/else.
	src := "if flag:\n" +
		"    x = \"/a\"\n" +
		"else:\n" +
		"    x = \"/b\"\n" +
		"x\n"
	root, srcB := parsePython(t, src)
	w := contract.KeyWalkerFor("python")
	vals, dyn := w.WalkKey(lastPythonExpr(root), srcB, noConsts)
	assert.False(t, dyn)
	require.Len(t, vals, 2)
	sort.Strings(vals)
	assert.Equal(t, []string{"/a", "/b"}, vals)
}

func TestPythonWalker_IfElif_NoElse_StaysDynamic(t *testing.T) {
	// No else clause → branches aren't exhaustive (some path leaves x
	// unset), so the binding must stay unknowable rather than guessed.
	src := "if flag:\n" +
		"    x = \"/a\"\n" +
		"elif flag2:\n" +
		"    x = \"/b\"\n" +
		"x\n"
	root, srcB := parsePython(t, src)
	w := contract.KeyWalkerFor("python")
	vals, dyn := w.WalkKey(lastPythonExpr(root), srcB, noConsts)
	assert.True(t, dyn)
	assert.Nil(t, vals)
}

func TestPythonWalker_IfElifElse_UnresolvedBranchStaysDynamic(t *testing.T) {
	// One branch assigns a non-literal (unresolvable) value → the whole
	// group must decline, not silently drop the unresolvable branch.
	src := "if flag:\n" +
		"    x = \"/a\"\n" +
		"else:\n" +
		"    x = some_var\n" +
		"x\n"
	root, srcB := parsePython(t, src)
	w := contract.KeyWalkerFor("python")
	vals, dyn := w.WalkKey(lastPythonExpr(root), srcB, noConsts)
	assert.True(t, dyn)
	assert.Nil(t, vals)
}

func TestPythonWalker_IfElifElse_ExtraReassignmentStaysDynamic(t *testing.T) {
	// A reassignment outside the if/elif/else entirely makes the whole
	// binding ambiguous, same as the pre-existing count>1 bailout.
	src := "x = \"/z\"\n" +
		"if flag:\n" +
		"    x = \"/a\"\n" +
		"else:\n" +
		"    x = \"/b\"\n" +
		"x\n"
	root, srcB := parsePython(t, src)
	w := contract.KeyWalkerFor("python")
	vals, dyn := w.WalkKey(lastPythonExpr(root), srcB, noConsts)
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
