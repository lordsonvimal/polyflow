package factpipe

import (
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// hub_rails_helpers.go is the "rails_helper_routes" hub provider (see
// hub.go) — the Tier FX FX.8.7 migration of
// internal/linker/rails_helpers.go's BuildRailsHelperMap.
//
// Unlike the pusher hubs, this needs no file I/O and no re-parsing: the
// (helper name -> method, path) lookup is a plain projection of node_meta
// values the Rails route walker already stamped on every http_handler node
// at parse time (route_helper/method/path). The reason it still needs a hub
// rather than a pure `.dl` rule is the two literal-suffixed variant names
// Rails generates for one route (`_path` and `_url`) — string concatenation
// a `.dl` rule body cannot perform (the same restriction FX.8.24's
// diagnosis documented: rule-level string assembly is deliberately
// unsupported, "would let a rule smuggle in a normalizer").
func init() { RegisterHub("rails_helper_routes", railsHelperRoutesHub) }

// railsHelperRoutePred is the fact predicate this hub asserts:
// (Svc, Helper, Method, Path, Rank) — one row per named route, once for
// `_path` and once for `_url`. Rank is a dense 0-based index over each
// (Svc, Helper) group's routes, lexicographically ordered by (Method, Path)
// — computed here rather than in the `.dl` because the engine's `lt`/`le`
// builtins are integer-only (FX.3, `internal/datalog/builtin.go`) and
// cannot order strings; the `.dl` picks the deterministic "keeps the
// client's own id" candidate with a `min(Rank)` aggregate instead.
const railsHelperRoutePred = "rails_helper_route"

type railsHelperRoute struct{ method, path string }

func railsHelperRoutesHub(nodes []graph.Node, files []string, _ string, _ []graph.LinkHint, _ graph.SchemaConfig, _ []graph.UnresolvedRef) []Fact {
	type key struct{ svc, helper string }
	seen := map[string]bool{}
	grouped := map[key][]railsHelperRoute{}

	for i := range nodes {
		n := &nodes[i]
		if n.Language != "ruby" || n.Type != graph.NodeTypeHTTPHandler {
			continue
		}
		name := n.Meta["route_helper"]
		method := strings.ToUpper(n.Meta["method"])
		path := n.Meta["path"]
		if name == "" || method == "" || path == "" {
			continue
		}
		// `_path` and `_url` name the same route; Rails generates both and
		// views use them interchangeably (a mailer reaches for `_url`).
		for _, suffix := range []string{"_path", "_url"} {
			k := key{n.Service, name + suffix}
			dedupKey := k.svc + "\x00" + k.helper + "\x00" + method + "\x00" + path
			if seen[dedupKey] {
				continue
			}
			seen[dedupKey] = true
			grouped[k] = append(grouped[k], railsHelperRoute{method, path})
		}
	}

	keys := make([]key, 0, len(grouped))
	for k := range grouped {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].svc != keys[j].svc {
			return keys[i].svc < keys[j].svc
		}
		return keys[i].helper < keys[j].helper
	})

	var out []Fact
	for _, k := range keys {
		routes := grouped[k]
		sort.Slice(routes, func(i, j int) bool {
			if routes[i].method != routes[j].method {
				return routes[i].method < routes[j].method
			}
			return routes[i].path < routes[j].path
		})
		for rank, r := range routes {
			out = append(out, Fact{
				Pred: railsHelperRoutePred,
				Args: []Atom{Str(k.svc), Str(k.helper), Str(r.method), Str(r.path), Int(int64(rank))},
				Origin: Origin{
					Kind: OriginPrimitive, Pattern: railsHelperRoutePred,
				},
			})
		}
	}
	return out
}
