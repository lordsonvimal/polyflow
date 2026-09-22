package linker_test

// Tests for LinkJSSwitchDispatchCalls: a call to a function/method whose
// whole body is a switch on one of its own parameters resolves for a literal
// call-site argument matching a case label (or default).

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/linker"
)

func TestLinkJSSwitchDispatchCalls_ObjectLiteralMethodCaseMatch(t *testing.T) {
	t.Parallel()
	// The real shape: PullRequestUtils.generateNameURL(row, key), body is
	// exactly one switch on `key`, each case a template literal with
	// wildcarded interpolation.
	_, paths := writeJSWrapperFixture(t, map[string]string{
		"PullRequestUtils.jsx": `const PullRequestUtils = {
  generateNameURL(row, key) {
    switch (key) {
      case "name":
        return ` + "`/standards/${row.candidate_id}#form&pr=${row.id}`" + `;
      case "source":
        return ` + "`/standards/${row.source_id}#form`" + `;
      default:
        return ` + "`/standards/${row.candidate_id}#form`" + `;
    }
  }
};
export default PullRequestUtils;
`,
		"caller.jsx": `import PullRequestUtils from "./PullRequestUtils";

export function load(row) {
  return fetch(PullRequestUtils.generateNameURL(row, "name"));
}
`,
	})
	nodes := parseJSWrapperFixture(t, "svc", paths)
	serviceFiles := map[string][]string{"svc": paths}

	var before *bool
	for i := range nodes {
		if nodes[i].Meta["key_dynamic"] == "true" {
			b := true
			before = &b
		}
	}
	require.NotNil(t, before, "fixture should produce a key_dynamic node before the pass runs")

	patched := linker.LinkJSSwitchDispatchCalls(nodes, serviceFiles)
	require.Len(t, patched, 1)
	assert.Equal(t, "/standards/*#form&pr=*", patched[0].Meta["url"])
	assert.Equal(t, "", patched[0].Meta["key_dynamic"])
	assert.Equal(t, "", patched[0].Meta["key_dynamic_raw"])
	assert.Equal(t, "js_switch_dispatch", patched[0].Meta["key_via"])
}

func TestLinkJSSwitchDispatchCalls_FallsBackToDefault(t *testing.T) {
	t.Parallel()
	_, paths := writeJSWrapperFixture(t, map[string]string{
		"PullRequestUtils.jsx": `const PullRequestUtils = {
  generateNameURL(row, key) {
    switch (key) {
      case "name":
        return ` + "`/standards/${row.candidate_id}#form&pr=${row.id}`" + `;
      default:
        return ` + "`/standards/${row.candidate_id}#form`" + `;
    }
  }
};
export default PullRequestUtils;
`,
		"caller.jsx": `import PullRequestUtils from "./PullRequestUtils";

export function load(row) {
  return fetch(PullRequestUtils.generateNameURL(row, "unmatched_key"));
}
`,
	})
	nodes := parseJSWrapperFixture(t, "svc", paths)
	serviceFiles := map[string][]string{"svc": paths}

	patched := linker.LinkJSSwitchDispatchCalls(nodes, serviceFiles)
	require.Len(t, patched, 1)
	assert.Equal(t, "/standards/*#form", patched[0].Meta["url"])
}

func TestLinkJSSwitchDispatchCalls_ReassignedDiscriminantStaysDynamic(t *testing.T) {
	t.Parallel()
	// The real linkTo.type shape: the discriminant is reassigned (lowercased,
	// substituted through a lookup table) before the switch, so the switch
	// does not discriminate on the raw parameter — must not be inlined.
	_, paths := writeJSWrapperFixture(t, map[string]string{
		"linkTo.jsx": `var linkTo = {
  type(type, id) {
    type = type.toLowerCase();
    switch (type) {
      case "form":
        return ` + "`/forms/${id}`" + `;
      default:
        return "unknown";
    }
  }
};
export default linkTo;
`,
		"caller.jsx": `import linkTo from "./linkTo";

export function load(id) {
  return fetch(linkTo.type("Form", id));
}
`,
	})
	nodes := parseJSWrapperFixture(t, "svc", paths)
	serviceFiles := map[string][]string{"svc": paths}

	patched := linker.LinkJSSwitchDispatchCalls(nodes, serviceFiles)
	assert.Empty(t, patched, "a discriminant reassigned before the switch must not be inlined")
}

func TestLinkJSSwitchDispatchCalls_UnresolvedCandidateBodyStaysDynamic(t *testing.T) {
	t.Parallel()
	// A case whose body calls a further-unresolvable helper (not a plain
	// return of a literal/template) must leave that call site dynamic.
	_, paths := writeJSWrapperFixture(t, map[string]string{
		"PullRequestUtils.jsx": `const PullRequestUtils = {
  generateNameURL(row, key) {
    switch (key) {
      case "name":
        return computeSomethingComplicated(row);
      default:
        return ` + "`/standards/${row.candidate_id}#form`" + `;
    }
  }
};
export default PullRequestUtils;
`,
		"caller.jsx": `import PullRequestUtils from "./PullRequestUtils";

export function load(row) {
  return fetch(PullRequestUtils.generateNameURL(row, "name"));
}
`,
	})
	nodes := parseJSWrapperFixture(t, "svc", paths)
	serviceFiles := map[string][]string{"svc": paths}

	patched := linker.LinkJSSwitchDispatchCalls(nodes, serviceFiles)
	assert.Empty(t, patched, "a case body that isn't a directly-walkable return must stay dynamic")
}
