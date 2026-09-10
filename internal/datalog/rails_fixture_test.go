package datalog_test

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/datalog"
	"github.com/lordsonvimal/polyflow/rules"
)

// classKey / methodKey / fileOf build the deterministic node ids the
// rails_filters.dl fixture uses. classKey(0) is ApplicationController.
func classKey(i int) string  { return "cedar:" + fileOf(i) + ":class:C" + strconv.Itoa(i) }
func methodKey(c, m int) string {
	return "cedar:" + fileOf(c) + ":function:" + fmt.Sprintf("C%d#m%d", c, m)
}
func fileOf(i int) string { return "app/controllers/c" + strconv.Itoa(i) + "_controller.rb" }

// buildRailsFixture loads the FX.8 rails_filters.dl over a cedar-shaped fact
// base: C0 (ApplicationController) declares `before_action :auth` and defines
// `auth`; C1..C500 subclass C0 directly; C501 subclasses C1 and skips :auth on
// its `public` action. Each class Ci defines actions m0, m1 (m1 is private).
func buildRailsFixture(tb testing.TB) *datalog.Engine {
	tb.Helper()
	e := datalog.New(datalog.Options{})

	var node, nodeStart, nodeLine, nodeLabel, defines, classSuper, ancDist [][]string
	var filterCall, filterCb, pubMethod [][]string
	var skipCall, skipCb, skipOnly [][]string

	addClass := func(i int) {
		ck := classKey(i)
		f := fileOf(i)
		node = append(node, []string{ck, "class", f, "cedar"})
		nodeStart = append(nodeStart, []string{ck, "1"})
		nodeLine = append(nodeLine, []string{ck, "1", "40"})
		nodeLabel = append(nodeLabel, []string{ck, "C" + strconv.Itoa(i)})
		// public action m0, private helper m1
		for m := 0; m < 2; m++ {
			mk := methodKey(i, m)
			node = append(node, []string{mk, "function", f, "cedar"})
			nodeStart = append(nodeStart, []string{mk, strconv.Itoa(10 + m*5)})
			defines = append(defines, []string{ck, "m" + strconv.Itoa(m), mk})
			priv := "0"
			if m == 1 {
				priv = "1"
			}
			pubMethod = append(pubMethod, []string{f, "m" + strconv.Itoa(m), strconv.Itoa(10 + m*5), priv})
		}
	}

	addClass(0)
	// C0 defines `auth` and registers before_action :auth at line 2.
	node = append(node, []string{methodKey(0, 9), "function", fileOf(0), "cedar"})
	nodeStart = append(nodeStart, []string{methodKey(0, 9), "5"})
	defines = append(defines, []string{classKey(0), "auth", methodKey(0, 9)})
	filterCall = append(filterCall, []string{fileOf(0), "2", "before_action", "before_action", "", "", "", ""})
	filterCb = append(filterCb, []string{fileOf(0), "2", "before_action", "auth", "0"})

	for i := 1; i <= 500; i++ {
		addClass(i)
		classSuper = append(classSuper, []string{classKey(i), classKey(0)})
		ancDist = append(ancDist, []string{classKey(i), classKey(0), "1"})
	}
	// C501 < C1 < C0
	addClass(501)
	classSuper = append(classSuper, []string{classKey(501), classKey(1)})
	ancDist = append(ancDist, []string{classKey(501), classKey(1), "1"})
	ancDist = append(ancDist, []string{classKey(501), classKey(0), "2"})
	// C501 skips :auth on m0 only.
	skipCall = append(skipCall, []string{fileOf(501), "3", "before_action"})
	skipCb = append(skipCb, []string{fileOf(501), "3", "before_action", "auth"})
	skipOnly = append(skipOnly, []string{fileOf(501), "3", "m0"})

	for rel, rows := range map[string][][]string{
		"node": node, "node_start": nodeStart, "defines": defines,
		"class_super": classSuper, "ancestor_dist": ancDist,
		"filter_call": filterCall, "filter_cb": filterCb, "pub_method": pubMethod,
		"skip_call": skipCall, "skip_cb": skipCb, "skip_only": skipOnly,
		"node_line": nodeLine, "node_label": nodeLabel,
		"node_meta": {},
		"includes_module": {}, "filter_only": {}, "filter_except": {},
		"skip_except": {},
	} {
		tups := make([]datalog.Tuple, len(rows))
		for i, r := range rows {
			tups[i] = datalog.Tuple(r)
		}
		require.NoError(tb, e.Assert(rel, tups), rel)
	}
	require.NoError(tb, e.LoadRules(rules.MustLoad("ruby/rails_filters.dl"), "ruby/rails_filters.dl"))
	return e
}
