package factpipe

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

// hub_stylesheet_imports.go is the "stylesheet_imports_sites" hub provider
// (see hub.go) — the Tier FX migration of
// internal/linker/stylesheet_imports.go's retired LinkStylesheetImports
// (Tier K.5).
//
// No `facts:` block — Sass's `@import`/`@use`/`@forward` scanning
// (`internal/css.Scan`) is a plain byte scanner, not a tree-sitter grammar,
// and the load-root/probe resolution (following Sass's own file-lookup
// precedence order across a whole service's stylesheet set) is a
// service-wide index a `.dl` join can't build. Stays hand-written Go;
// rules/css/stylesheet_imports.dl is pure pass-through.
//
// `mint:` for the missing NodeTypeFile endpoints (containment only reaches
// a file that declares something; a Sass partial of nothing but
// `$variables` declares nothing, yet partials are the majority of every
// import graph's targets) — reuses `frozenNodeTypes["file"]`, no new
// vocabulary needed. A second relation mints the file's `contains` edge
// from its service node, when one exists — the same two-step
// `internal/linker/file_nodes.go`'s `fileNodeIndex.ensure` always did.
//
// The caller (internal/indexer/link_passes.go) runs this hub once per
// service, like FX.8.8/8.10's per-service fix — `svc` here is simply
// `nodes[0].Service` (the pusher_producer idiom), not a per-file lookup,
// since the retired Go's own service boundary came from its caller's
// `map[service][]files` structure in the first place.
func init() { RegisterHub("stylesheet_imports_sites", stylesheetImportsSitesHub) }

const (
	stylesheetMintPred       = "stylesheet_mint"       // (ID, Label, Svc, File, Language, Basename)
	stylesheetContainsPred   = "stylesheet_contains"   // (From, To)
	stylesheetImportPred     = "stylesheet_import"     // (From, To, Label, Conf, Rule, Spec)
	stylesheetUnresolvedPred = "stylesheet_unresolved" // (Svc, File, Line, Spec)
)

func stylesheetImportsSitesHub(nodes []graph.Node, files []string) []Fact {
	fileNodeID := make(map[string]string) // svc\x00file -> id
	haveService := make(map[string]bool)
	svc := ""
	for i := range nodes {
		n := &nodes[i]
		switch n.Type {
		case graph.NodeTypeFile:
			fileNodeID[n.Service+"\x00"+n.File] = n.ID
		case graph.NodeTypeService:
			haveService[n.Label] = true
		}
		if svc == "" && n.Service != "" {
			svc = n.Service
		}
	}

	idx := csiNewStylesheetIndex(files)
	if len(idx.files) == 0 {
		return nil
	}

	var out []Fact
	minted := map[string]bool{}
	var mintedOrder []string
	mintedFile := map[string]string{} // id -> file
	ensure := func(file string) string {
		key := svc + "\x00" + file
		if id, ok := fileNodeID[key]; ok {
			return id
		}
		id := fmt.Sprintf("%s:%s:%s", svc, file, graph.NodeTypeFile)
		fileNodeID[key] = id
		if !minted[id] {
			minted[id] = true
			mintedOrder = append(mintedOrder, id)
			mintedFile[id] = file
		}
		return id
	}

	seenEdge := map[string]bool{}
	addImportEdge := func(from, to, label, conf, rule, spec string) {
		id := "imports:" + from + "->" + to
		if seenEdge[id] {
			return
		}
		seenEdge[id] = true
		out = append(out, Fact{
			Pred:   stylesheetImportPred,
			Args:   []Atom{Str(from), Str(to), Str(label), Str(conf), Str(rule), Str(spec)},
			Origin: Origin{Kind: OriginPrimitive, Pattern: stylesheetImportPred},
		})
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
		fromID := ensure(file)
		for _, imp := range imports {
			if csiIsExternalSpec(imp.Spec) {
				continue
			}
			targets, ambiguous := idx.resolve(file, imp.Spec)
			if len(targets) == 0 {
				out = append(out, Fact{
					Pred:   stylesheetUnresolvedPred,
					Args:   []Atom{Str(svc), Str(file), Int(int64(imp.Line)), Str(imp.Spec)},
					Origin: Origin{Kind: OriginPrimitive, File: file, Line: imp.Line, Pattern: stylesheetUnresolvedPred},
				})
				continue
			}
			conf := graph.ConfidenceStatic
			if ambiguous {
				conf = graph.ConfidencePartial
			}
			for _, t := range targets {
				toID := ensure(t)
				addImportEdge(fromID, toID, "@"+imp.Rule+" "+imp.Spec, conf, imp.Rule, imp.Spec)
			}
		}
	}

	for _, id := range mintedOrder {
		file := mintedFile[id]
		out = append(out, Fact{
			Pred: stylesheetMintPred,
			Args: []Atom{
				Str(id), Str(file), Str(svc), Str(file), Str(csiLanguageForFile(file)), Str(path.Base(file)),
			},
			Origin: Origin{Kind: OriginPrimitive, File: file, Pattern: stylesheetMintPred},
		})
		if haveService[svc] {
			out = append(out, Fact{
				Pred:   stylesheetContainsPred,
				Args:   []Atom{Str("service:" + svc), Str(id)},
				Origin: Origin{Kind: OriginPrimitive, Pattern: stylesheetContainsPred},
			})
		}
	}
	return out
}

// --- ported structural helpers (csi-prefixed, from stylesheet_imports.go) ---

func csiIsExternalSpec(spec string) bool {
	return strings.HasPrefix(spec, "//") || strings.Contains(spec, "://") ||
		strings.HasPrefix(spec, "data:")
}

func csiLanguageForFile(file string) string {
	switch {
	case strings.HasSuffix(file, ".scss"):
		return "scss"
	case strings.HasSuffix(file, ".css"):
		return "css"
	}
	return ""
}

type csiStylesheetIndex struct {
	files   map[string]bool
	ordered []string
	roots   []string
}

func csiNewStylesheetIndex(files []string) *csiStylesheetIndex {
	idx := &csiStylesheetIndex{files: map[string]bool{}}
	rootSet := map[string]bool{}
	for _, f := range files {
		switch strings.ToLower(filepath.Ext(f)) {
		case ".scss", ".css":
		default:
			continue
		}
		idx.files[f] = true
		idx.ordered = append(idx.ordered, f)
		if r := csiLoadRoot(f); r != "" {
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

func csiLoadRoot(file string) string {
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

func (idx *csiStylesheetIndex) resolve(importingFile, spec string) (targets []string, ambiguous bool) {
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
	out = csiDedupeSorted(out, importingFile)
	return out, len(out) > 1
}

func (idx *csiStylesheetIndex) probe(base, spec string) []string {
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

func (idx *csiStylesheetIndex) resolveGlob(importingFile, spec string) []string {
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
			break
		}
	}
	return csiDedupeSorted(out, importingFile)
}

func csiDedupeSorted(in []string, self string) []string {
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
