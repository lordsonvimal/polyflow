package contract_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/contract"
)

// C.1 covers the shape that made 77% of orion's frontend HTTP call sites
// pathless: the URL argument is a bare identifier bound to a static
// concatenation a few lines above, in the same function. These tests parse
// whole files rather than expression fragments, because the whole point is
// that the answer lives outside the captured expression.

// urlArgAt returns the first argument of the nth `$.get(…)`/`$.ajax(…)` call
// in src — the node a pattern's @url capture would bind.
func urlArgAt(t *testing.T, src string, nth int) (*sitter.Node, []byte) {
	t.Helper()
	root, b := parseJS(t, src)

	var found *sitter.Node
	seen := 0
	var visit func(n *sitter.Node)
	visit = func(n *sitter.Node) {
		if n == nil || found != nil {
			return
		}
		if n.Type() == "call_expression" {
			if args := n.ChildByFieldName("arguments"); args != nil && args.NamedChildCount() > 0 {
				fn := n.ChildByFieldName("function")
				if fn != nil && fn.Type() == "member_expression" {
					obj := fn.ChildByFieldName("object")
					if obj != nil && string(b[obj.StartByte():obj.EndByte()]) == "$" {
						if seen == nth {
							found = args.NamedChild(0)
							return
						}
						seen++
					}
				}
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			visit(n.Child(i))
		}
	}
	visit(root)
	require.NotNil(t, found, "no $.x(…) call #%d in source", nth)
	return found, b
}

func walkURL(t *testing.T, src string, nth int) ([]string, bool) {
	t.Helper()
	node, b := urlArgAt(t, src, nth)
	return contract.KeyWalkerFor("javascript").WalkKey(node, b, noConsts)
}

// TestJSLocalBinding_WorkedExample is deliverables.js:748 verbatim — the
// single most common shape in the corpus.
func TestJSLocalBinding_WorkedExample(t *testing.T) {
	src := `
var App = {
  showTaskDialog: function (event) {
    var $this = this,
      studyId = card.data().studyId,
      deliverableId = card.data().deliverableId,
      url = "/app/studies/" + studyId + "/deliverables/" + deliverableId + ".js";
    $.get(url, { _: $.now() });
  }
};
`
	got, dynamic := walkURL(t, src, 0)

	assert.False(t, dynamic)
	assert.Equal(t, []string{"/app/studies/*/deliverables/*.js"}, got)
}

// TestJSLocalBinding_SiblingFunctionDoesNotLeak — deliverables.js binds `url`
// in three sibling handlers to three different paths. A file-scoped constant
// table would have to pick one of them; the lexical walk must pick the one in
// the reader's own scope.
func TestJSLocalBinding_SiblingFunctionDoesNotLeak(t *testing.T) {
	src := `
var App = {
  editTaskDialog: function (ev) {
    var url = "/app/studies/" + studyId + "/edit";
    $.get(url);
  },
  createDeliverable: function (ev) {
    var url = "/app/studies/" + studyId + "/deliverables/new";
    $.get(url);
  }
};
`
	first, _ := walkURL(t, src, 0)
	second, _ := walkURL(t, src, 1)

	assert.Equal(t, []string{"/app/studies/*/edit"}, first)
	assert.Equal(t, []string{"/app/studies/*/deliverables/new"}, second)
}

// TestJSLocalBinding_ReassignedStaysDynamic — a variable written twice has no
// single value. Picking either would produce a confident wrong path, which is
// worse for an agent than an honest gap.
func TestJSLocalBinding_ReassignedStaysDynamic(t *testing.T) {
	src := `
function go(flag) {
  var url = "/app/a";
  if (flag) { url = "/app/b"; }
  $.get(url);
}
`
	_, dynamic := walkURL(t, src, 0)

	assert.True(t, dynamic, "a reassigned url must not resolve to one branch")
}

// TestJSLocalBinding_ParameterStaysDynamic — the URL arriving as a function
// parameter is the cross-method shape that defeated the Ruby walker in PR.3.
// It is out of scope here and must not be guessed.
func TestJSLocalBinding_ParameterStaysDynamic(t *testing.T) {
	src := `
function fetchIt(url) {
  $.get(url);
}
`
	_, dynamic := walkURL(t, src, 0)

	assert.True(t, dynamic)
}

// TestJSLocalBinding_LaterBindingIsNotRead — a binding below the call cannot
// be what the call read.
func TestJSLocalBinding_LaterBindingIsNotRead(t *testing.T) {
	src := `
function go() {
  $.get(url);
  var url = "/app/late";
}
`
	_, dynamic := walkURL(t, src, 0)

	assert.True(t, dynamic)
}

// TestJSLocalBinding_ClosureScopeWidens — the binding sits in the enclosing
// IIFE, one scope out from the callback that reads it.
func TestJSLocalBinding_ClosureScopeWidens(t *testing.T) {
	src := `
(function () {
  var url = "/app/folders/" + folderId + "/job_state";
  whenReady(function () {
    $.get(url);
  });
})();
`
	got, dynamic := walkURL(t, src, 0)

	assert.False(t, dynamic)
	assert.Equal(t, []string{"/app/folders/*/job_state"}, got)
}

// TestJSLocalBinding_NestedFunctionBindingIsInvisible — a binding declared
// inside a nested callback is not in scope at an outer call site, and must
// not be counted (nor make the outer lookup ambiguous).
func TestJSLocalBinding_NestedFunctionBindingIsInvisible(t *testing.T) {
	src := `
function go() {
  var url = "/app/outer/" + id;
  done(function () {
    var url = "/app/inner";
    send(url);
  });
  $.get(url);
}
`
	got, dynamic := walkURL(t, src, 0)

	assert.False(t, dynamic)
	assert.Equal(t, []string{"/app/outer/*"}, got)
}

// TestJSLocalBinding_BranchLocalConst is deliverables.js:1231
// (initializeTaskModal): `const url` declared once per branch of an if/else.
// Both are in the same function, so a function-granular search sees two
// bindings and gives up on a site where the answer is unambiguous — the call
// can only have read the one in its own branch.
func TestJSLocalBinding_BranchLocalConst(t *testing.T) {
	src := `
function initializeTaskModal() {
  if (issueId) {
    const url = "/app/studies/" + studyId + "/deliverables/" + taskId + "/issues/" + issueId + ".js";
    $.get(url, { _: $.now() });
  } else if (taskId) {
    const url = "/app/studies/" + studyId + "/deliverables/" + taskId + ".js";
    $.get(url, { _: $.now() });
  }
}
`
	first, dyn1 := walkURL(t, src, 0)
	second, dyn2 := walkURL(t, src, 1)

	assert.False(t, dyn1)
	assert.False(t, dyn2)
	assert.Equal(t, []string{"/app/studies/*/deliverables/*/issues/*.js"}, first)
	assert.Equal(t, []string{"/app/studies/*/deliverables/*.js"}, second)
}

// TestJSLocalBinding_ReassignedInBlockStillDynamic is the guard that
// block-granular search must not cost. A write inside a nested block is a
// second binding of the same variable and must still defeat resolution — the
// search may *start* in a block but must never refuse to look inside one.
func TestJSLocalBinding_ReassignedInBlockStillDynamic(t *testing.T) {
	src := `
function go(flag) {
  var url = "/app/a";
  if (flag) { url = "/app/b"; }
  for (var i = 0; i < 2; i++) { url = "/app/c"; }
  $.get(url);
}
`
	_, dynamic := walkURL(t, src, 0)

	assert.True(t, dynamic)
}

// TestJSLocalBinding_TemplateLiteralBinding — `${…}` and `" + x + "` must
// normalize to the same wildcard, or the two halves of the corpus match
// different routes.
func TestJSLocalBinding_TemplateLiteralBinding(t *testing.T) {
	src := "function go() {\n  const url = `/app/users/${userId}/sync`;\n  $.get(url);\n}\n"

	got, dynamic := walkURL(t, src, 0)

	assert.False(t, dynamic)
	assert.Equal(t, []string{"/app/users/*/sync"}, got)
}

// TestJSLocalBinding_MemberTargetIsNotABinding — `this.url = x` does not bind
// the identifier `url`.
func TestJSLocalBinding_MemberTargetIsNotABinding(t *testing.T) {
	src := `
function go() {
  this.url = "/app/a";
  $.get(url);
}
`
	_, dynamic := walkURL(t, src, 0)

	assert.True(t, dynamic)
}

// TestJSObjectOptions_URLFieldIsRead — file_viewer.js:1974. The direct-arg
// jQuery pattern wins the match and binds the whole options object, so the
// path was one field down the tree the whole time.
func TestJSObjectOptions_URLFieldIsRead(t *testing.T) {
	src := `
function go() {
  $.ajax({
    url: "/app/" + objectType + "/" + id + "/actions",
    type: "GET",
    dataType: "text"
  });
}
`
	got, dynamic := walkURL(t, src, 0)

	assert.False(t, dynamic)
	assert.Equal(t, []string{"/app/*/*/actions"}, got)
}

// TestJSObjectOptions_IndirectURLField — the options object holds an
// identifier, which then resolves locally. Both C.1 mechanisms in one site.
func TestJSObjectOptions_IndirectURLField(t *testing.T) {
	src := `
function go() {
  var url = "/app/studies/" + studyId + "/deliverables/" + taskId + "/audit_logs";
  $.ajax({ dataType: "script", method: "GET", url: url });
}
`
	got, dynamic := walkURL(t, src, 0)

	assert.False(t, dynamic)
	assert.Equal(t, []string{"/app/studies/*/deliverables/*/audit_logs"}, got)
}

// TestJSObjectOptions_NoURLFieldStaysDynamic — an options object with no url
// key carries no path.
func TestJSObjectOptions_NoURLFieldStaysDynamic(t *testing.T) {
	src := `
function go() {
  $.ajax({ type: "POST", data: payload });
}
`
	_, dynamic := walkURL(t, src, 0)

	assert.True(t, dynamic)
}

// TestJSConcat_LongChainIsNotTruncated is the second C.1 defect: a `+` chain
// nests one level per operand, so charging it against the depth-2 ternary
// ceiling silently wildcarded the tail of exactly the longest and most
// specific URLs. Five operands must survive intact.
func TestJSConcat_LongChainIsNotTruncated(t *testing.T) {
	src := `
function go() {
  $.get("/app/studies/" + a + "/deliverables/" + b + "/audit_logs");
}
`
	got, dynamic := walkURL(t, src, 0)

	assert.False(t, dynamic)
	assert.Equal(t, []string{"/app/studies/*/deliverables/*/audit_logs"}, got)
}

// TestJSConcat_NonPlusOperatorStaysDynamic — collapsing an arbitrary binary
// expression to "*" would invent a path out of arithmetic.
func TestJSConcat_NonPlusOperatorStaysDynamic(t *testing.T) {
	src := `
function go() {
  $.get(base || fallback);
}
`
	_, dynamic := walkURL(t, src, 0)

	assert.True(t, dynamic)
}

// TestJSConcat_AllWildcardStaysDynamic — the X.1b over-match guard still
// applies once a chain resolves: a template with no concrete content could
// match any route by string equality.
func TestJSConcat_AllWildcardStaysDynamic(t *testing.T) {
	src := `
function go() {
  var url = a + "/" + b;
  $.get(url);
}
`
	_, dynamic := walkURL(t, src, 0)

	assert.True(t, dynamic)
}
