package indexer

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// TestRun_ClientRouteRendersComponent is Tier RT.1 end to end.
//
// The linker tests prove the pass returns re-typed nodes in `tagged` rather than
// `newNodes`. This proves the whole pipeline agrees: the re-typed node reaches
// the store under its original ID, so the route's edge — minted before the
// re-type, against that ID — still lands on it, and the graph gained no second
// node for the declaration. Re-typing is the one thing here a unit test cannot
// check end to end, because the type has to survive the store round trip for the
// query the tier exists to serve ("which component does route `cdm` render") to
// return anything.
//
// Note what this cannot catch. Appending the re-typed node to the pipeline's
// working set instead of replacing it in place leaves the store untouched —
// UpsertNode is keyed by ID — so the duplicate is invisible from here and shows
// up only as double-counting inside later passes. The linker-level
// TestClientRouteTargets_MintsNoSecondNode is what holds that line.
func TestRun_ClientRouteRendersComponent(t *testing.T) {
	dir := t.TempDir()
	svc := filepath.Join(dir, "orion")
	react := filepath.Join(svc, "react", "components")
	require.NoError(t, os.MkdirAll(filepath.Join(react, "common"), 0o755))

	writeFile(t, filepath.Join(react, "common"), "ClientRoutes.jsx", `export default {
  cdm: "/standards/:standardId#cdm",
  codelists: "/standards/:standardId#code_lists",
  contents: "/standards/:standardId#contents",
  settings: "/standards/:standardId#settings",
};
`)
	writeFile(t, filepath.Join(react, "common"), "navigation.jsx", `function renderRoute(routeName, params) {
  var kid;
  switch (routeName) {
    case "cdm":
      kid = <CDMTopLevel {...params} />;
      break;
    case "codelists":
      kid = <CodeListTopLevel {...params} />;
      break;
    case "contents":
      kid = <ContentsTopLevel {...params} />;
      break;
    case "settings":
      kid = <SettingsConfig {...params} />;
      break;
  }
  return kid;
}
`)
	writeFile(t, react, "TopLevels.jsx", `import React from "react";
import { connect } from "react-redux";

const CDMTopLevelInner = (props) => <div>{props.id}</div>;
export const CDMTopLevel = connect(mapStateToProps)(CDMTopLevelInner);

export const CodeListTopLevel = (props) => <div>{props.id}</div>;

export class ContentsTopLevel extends React.Component {
  render() {
    return <div />;
  }
}

// Not a component: a route may name a const that holds configuration, and
// re-typing it would launder an object into the component count.
export const SettingsConfig = { tabs: ["a", "b"] };
`)

	cfg := &workspace.WorkspaceConfig{
		Name: "test", Version: "1",
		Services: []workspace.Service{{Name: "orion", Path: svc}},
	}
	dbDir := filepath.Join(dir, ".polyflow")
	runIndexer(t, cfg, dbDir, false)

	store, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	defer store.Close()
	idx, err := store.BuildIndex(context.Background())
	require.NoError(t, err)

	// Every route's render target, by node type. This is the query the tier
	// exists to make answerable.
	rendered := map[string][]string{}
	byLabel := map[string][]*graph.Node{}
	for _, n := range idx.Nodes {
		byLabel[n.Label] = append(byLabel[n.Label], n)
		if n.Type != graph.NodeTypeClientRoute {
			continue
		}
		for _, e := range idx.OutEdges[n.ID] {
			if e.Type != graph.EdgeTypeRenders {
				continue
			}
			to := idx.Nodes[e.To]
			require.NotNil(t, to, "route %s renders a node not in the graph", n.Label)
			rendered[n.Label] = append(rendered[n.Label], to.Label+":"+string(to.Type))
		}
	}
	assert.Equal(t, []string{"CDMTopLevel:component"}, rendered["cdm"],
		"an HOC-wrapped const is the shape every one of cedar's 12 mistyped route "+
			"targets has, and it must be queryable as a component")
	// The shapes RT.1 deliberately does not touch. An arrow const already
	// arrives as a `function` node and a class as a `class` node — neither is a
	// `variable`, so neither is in scope. Re-typing JSX functions at large is a
	// decision docs/cedar-monolith-gap-audit-plan.md already made against, and
	// no route target on cedar has either shape.
	assert.Equal(t, []string{"CodeListTopLevel:function"}, rendered["codelists"])
	assert.Equal(t, []string{"ContentsTopLevel:class"}, rendered["contents"])
	// An object-valued const is not a render target in the first place: the JS
	// parser only stamps Meta["component"] on bindings its own heuristics
	// believe are components, and the route resolver only considers those. So
	// RT.1's not-a-component branch is a second opinion on that stamp rather
	// than the only thing standing between an object and the component count —
	// it is exercised directly in TestClientRouteTargets_NonComponentStaysVariable,
	// where the node can be built with the stamp the parser withholds here.
	assert.Empty(t, rendered["settings"],
		"an object literal became a render target")

	// The in-place gate. One declaration is one node however its type changed;
	// a duplicate is what mints fan-out on the render matcher.
	var ids []string
	for _, n := range byLabel["CDMTopLevel"] {
		ids = append(ids, n.ID+" ("+string(n.Type)+")")
	}
	sort.Strings(ids)
	assert.Len(t, ids, 1, "CDMTopLevel exists %d times after re-typing: %v", len(ids), ids)

	unresolved, err := store.ListUnresolvedRefs(context.Background())
	require.NoError(t, err)
	var notComponents []string
	for _, u := range unresolved {
		if u.Kind == "client_route_target_not_component" {
			notComponents = append(notComponents, u.Name)
		}
	}
	assert.Empty(t, notComponents,
		"nothing here reached the not-a-component branch: the object const never "+
			"became a target, and the three that did are all components")
}
