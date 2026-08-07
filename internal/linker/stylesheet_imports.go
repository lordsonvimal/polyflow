package linker

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/css"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// LinkStylesheetImports emits file→file `imports` edges for every
// `@import`/`@use`/`@forward` that resolves to another indexed stylesheet
// (Tier K.5), following Sass's own lookup order — exact path, `.scss`, `.css`,
// `_partial`, `dir/_index` — plus the `@import "modules/*"` glob form
// sass-rails supports.
//
// Must run after LinkContainment, whose NodeTypeFile nodes it reuses. It mints
// the missing ones itself and returns them for the indexer to persist:
// containment only reaches a file that declares something, and a Sass partial
// of nothing but `$variables` declares nothing — yet those partials are the
// majority of every import graph's *targets*. Skipping them left 7 of 40 edges.
func LinkStylesheetImports(nodes []graph.Node, serviceFiles map[string][]string) (newNodes []graph.Node, newEdges []graph.Edge, unresolved []graph.UnresolvedRef) {
	fileNodeID := make(map[string]string) // "svc\x00file" → file node ID
	haveService := make(map[string]bool)
	for i := range nodes {
		switch nodes[i].Type {
		case graph.NodeTypeFile:
			fileNodeID[nodes[i].Service+"\x00"+nodes[i].File] = nodes[i].ID
		case graph.NodeTypeService:
			haveService[nodes[i].Label] = true
		}
	}

	seen := make(map[string]bool)
	addEdge := func(from, to, label string, conf string, meta map[string]string) {
		id := fmt.Sprintf("imports:%s->%s", from, to)
		if seen[id] {
			return
		}
		seen[id] = true
		newEdges = append(newEdges, graph.Edge{
			ID: id, From: from, To: to, Type: graph.EdgeTypeImports,
			Label: label, Confidence: conf, Meta: meta,
		})
	}

	svcNames := make([]string, 0, len(serviceFiles))
	for svc := range serviceFiles {
		svcNames = append(svcNames, svc)
	}
	sort.Strings(svcNames) // map iteration must never reach output (bug-class #2)

	// ensureFileNode returns the file node ID for a stylesheet, minting the
	// node (and its service→file contains edge) when containment did not.
	ensureFileNode := func(svc, file string) string {
		key := svc + "\x00" + file
		if id, ok := fileNodeID[key]; ok {
			return id
		}
		id := fmt.Sprintf("%s:%s:%s", svc, file, graph.NodeTypeFile)
		fileNodeID[key] = id
		newNodes = append(newNodes, graph.Node{
			ID:       id,
			Type:     graph.NodeTypeFile,
			Label:    file,
			Service:  svc,
			File:     file,
			Language: languageForFile(file),
			Meta:     map[string]string{"basename": path.Base(file)},
		})
		if haveService[svc] {
			newEdges = append(newEdges, containsEdge("service:"+svc, id))
		}
		return id
	}

	for _, svc := range svcNames {
		idx := newStylesheetIndex(serviceFiles[svc])
		if len(idx.files) == 0 {
			continue
		}
		for _, file := range idx.ordered {
			src, err := os.ReadFile(file)
			if err != nil {
				continue
			}
			imports := css.Scan(src).Imports
			if len(imports) == 0 {
				continue
			}
			fromID := ensureFileNode(svc, file)
			for _, imp := range imports {
				if isExternalStylesheetSpec(imp.Spec) {
					continue // CDN / protocol URL: no edge, no ledger (JS precedent)
				}
				targets, ambiguous := idx.resolve(file, imp.Spec)
				if len(targets) == 0 {
					unresolved = append(unresolved, graph.UnresolvedRef{
						Service: svc, File: file, Line: imp.Line,
						Name: imp.Spec, Kind: "stylesheet_import",
					})
					continue
				}
				// A glob names every file it expands to, so each of those edges
				// is static. Several *load roots* hosting the same specifier is
				// genuine ambiguity: emit to all of them at reduced confidence
				// rather than picking one (bug-class #1).
				conf := graph.ConfidenceStatic
				if ambiguous {
					conf = graph.ConfidencePartial
				}
				for _, t := range targets {
					toID := ensureFileNode(svc, t)
					addEdge(fromID, toID, "@"+imp.Rule+" "+imp.Spec, conf,
						map[string]string{"rule": imp.Rule, "specifier": imp.Spec})
				}
			}
		}
	}

	return newNodes, newEdges, unresolved
}

func isStylesheetFile(file string) bool {
	switch strings.ToLower(filepath.Ext(file)) {
	case ".scss", ".css":
		return true
	}
	return false
}

// isExternalStylesheetSpec reports whether a specifier names something outside
// the repository (a protocol URL or a CSS `url()` to a CDN).
func isExternalStylesheetSpec(spec string) bool {
	return strings.HasPrefix(spec, "//") || strings.Contains(spec, "://") ||
		strings.HasPrefix(spec, "data:")
}

// stylesheetIndex is one service's stylesheet file set plus its Sass load
// roots — the directories a bare specifier like "settings/colors" is resolved
// against when it does not resolve relative to the importing file.
type stylesheetIndex struct {
	files   map[string]bool
	ordered []string
	roots   []string
}

func newStylesheetIndex(files []string) *stylesheetIndex {
	idx := &stylesheetIndex{files: map[string]bool{}}
	rootSet := map[string]bool{}
	for _, f := range files {
		if !isStylesheetFile(f) {
			continue
		}
		idx.files[f] = true
		idx.ordered = append(idx.ordered, f)
		if r := stylesheetLoadRoot(f); r != "" {
			rootSet[r] = true
		}
	}
	sort.Strings(idx.ordered)
	for r := range rootSet {
		idx.roots = append(idx.roots, r)
	}
	sort.Strings(idx.roots)
	return idx
}

// stylesheetLoadRoot returns the `.../stylesheets` ancestor of a file, which is
// what Rails puts on the Sass load path. Files outside such a directory (a
// vendored `node_modules` stylesheet) contribute no root.
func stylesheetLoadRoot(file string) string {
	dir := filepath.Dir(file)
	for {
		if filepath.Base(dir) == "stylesheets" {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// resolve returns every indexed stylesheet the specifier names, preferring a
// relative hit over the load roots. ambiguous reports that the specifier
// resolved under more than one load root, which no single edge can express.
// Empty targets means unresolvable.
func (idx *stylesheetIndex) resolve(importingFile, spec string) (targets []string, ambiguous bool) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, false
	}
	if strings.HasSuffix(spec, "*") {
		return idx.resolveGlob(importingFile, spec), false
	}
	if hits := idx.probe(filepath.Dir(importingFile), spec); len(hits) > 0 {
		return hits, false
	}
	var out []string
	for _, root := range idx.roots {
		out = append(out, idx.probe(root, spec)...)
	}
	out = dedupeSorted(out, importingFile)
	return out, len(out) > 1
}

// probe tries Sass's candidate filenames for spec under base, in precedence
// order, and returns the first form that exists. The order is a defined
// language rule, not a fan-out choice.
func (idx *stylesheetIndex) probe(base, spec string) []string {
	joined := filepath.Clean(filepath.Join(base, spec))
	dir, name := filepath.Dir(joined), filepath.Base(joined)
	for _, cand := range []string{
		joined,
		joined + ".scss",
		joined + ".css",
		filepath.Join(dir, "_"+name+".scss"),
		filepath.Join(dir, "_"+name+".css"),
		filepath.Join(joined, "_index.scss"),
		filepath.Join(joined, "index.scss"),
	} {
		if idx.files[cand] {
			return []string{cand}
		}
	}
	return nil
}

// resolveGlob expands `@import "modules/*"` to every stylesheet directly inside
// that directory — sass-rails' glob import, used for five of orion's
// top-level imports. Subdirectories are not included, matching `*` semantics.
func (idx *stylesheetIndex) resolveGlob(importingFile, spec string) []string {
	pattern := strings.TrimSuffix(spec, "*")
	bases := []string{filepath.Dir(importingFile)}
	bases = append(bases, idx.roots...)

	var out []string
	for _, base := range bases {
		dir := filepath.Clean(filepath.Join(base, pattern))
		for _, f := range idx.ordered {
			if filepath.Dir(f) == dir {
				out = append(out, f)
			}
		}
		if len(out) > 0 {
			break // a glob resolves against the first base that has the directory
		}
	}
	return dedupeSorted(out, importingFile)
}

// dedupeSorted removes duplicates and the importing file itself (a glob import
// of a directory containing the importer must not self-loop).
func dedupeSorted(in []string, self string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{self: true}
	var out []string
	for _, v := range in {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
