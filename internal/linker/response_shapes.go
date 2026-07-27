package linker

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// LinkResponseShapes joins server response DTOs to the client interfaces that
// mirror their JSON shape (Y.4 return-half cross-language join). It considers
// only Go structs that a `returns` edge marks as an actual response body, then
// matches each against every non-Go (TS/JS) interface by JSON field-name set,
// emitting `struct → interface` `response_of` when the shapes agree (Jaccard
// ≥ 0.8 and ≥ 2 shared fields). Untyped responses never produce a returns edge,
// so they never reach here — nothing is fabricated (#12). Recall over precision:
// every shape-equivalent client type is linked, each edge carrying its overlap
// so a consumer can rank.
func LinkResponseShapes(nodes []graph.Node, edges []graph.Edge) []graph.Edge {
	// Structs that are the target of a returns edge = server-declared responses.
	responseStructs := map[string]bool{}
	for _, e := range edges {
		if e.Type == graph.EdgeTypeReturns {
			responseStructs[e.To] = true
		}
	}
	if len(responseStructs) == 0 {
		return nil
	}

	type shape struct {
		id     string
		fields map[string]bool
	}
	var goStructs, tsIfaces []shape
	for i := range nodes {
		n := &nodes[i]
		switch n.Type {
		case graph.NodeTypeStruct:
			if n.Language != "go" || !responseStructs[n.ID] {
				continue
			}
			if fs := goStructJSONFields(n.Meta["fields"]); len(fs) >= 2 {
				goStructs = append(goStructs, shape{n.ID, fs})
			}
		case graph.NodeTypeInterface:
			if n.Language == "go" {
				continue
			}
			if fs := csvSet(n.Meta["methods"]); len(fs) >= 2 {
				tsIfaces = append(tsIfaces, shape{n.ID, fs})
			}
		}
	}

	var out []graph.Edge
	seen := map[string]bool{}
	for _, g := range goStructs {
		for _, t := range tsIfaces {
			shared, jac := setSimilarity(g.fields, t.fields)
			if shared < 2 || jac < 0.8 {
				continue
			}
			id := fmt.Sprintf("response_of:%s->%s", g.id, t.id)
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, graph.Edge{
				ID: id, From: g.id, To: t.id, Type: graph.EdgeTypeResponseOf,
				Confidence: graph.ConfidenceStatic,
				Meta: map[string]string{
					"match":   "shape",
					"shared":  fmt.Sprintf("%d", shared),
					"jaccard": fmt.Sprintf("%.2f", jac),
				},
			})
		}
	}
	return out
}

// goStructJSONFields returns the JSON wire-name set from a struct node's
// meta.fields (the [{name,type,tag}] array): the json tag name when present,
// else the Go field name; json:"-" fields are skipped.
func goStructJSONFields(raw string) map[string]bool {
	if raw == "" {
		return nil
	}
	var fields []struct {
		Name string `json:"name"`
		Tag  string `json:"tag"`
	}
	if json.Unmarshal([]byte(raw), &fields) != nil {
		return nil
	}
	set := map[string]bool{}
	for _, f := range fields {
		name := jsonTagName(f.Tag)
		switch name {
		case "-":
			continue
		case "":
			name = f.Name
		}
		if name != "" {
			set[name] = true
		}
	}
	return set
}

// jsonTagName extracts the wire name from a struct tag like
// `json:"id,omitempty"` → "id"; returns "" when there is no json tag.
func jsonTagName(tag string) string {
	i := strings.Index(tag, `json:"`)
	if i < 0 {
		return ""
	}
	rest := tag[i+len(`json:"`):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	val := rest[:end]
	if c := strings.IndexByte(val, ','); c >= 0 {
		val = val[:c]
	}
	return val
}

// csvSet splits a comma-separated member list into a set.
func csvSet(s string) map[string]bool {
	if s == "" {
		return nil
	}
	set := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			set[p] = true
		}
	}
	return set
}

// setSimilarity returns the shared-member count and the Jaccard index
// (|A∩B| / |A∪B|) of two field-name sets.
func setSimilarity(a, b map[string]bool) (int, float64) {
	shared := 0
	for k := range a {
		if b[k] {
			shared++
		}
	}
	union := len(a) + len(b) - shared
	if union == 0 {
		return 0, 0
	}
	return shared, float64(shared) / float64(union)
}
