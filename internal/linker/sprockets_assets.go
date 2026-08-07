package linker

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/sprockets"
)

// LinkSprocketsAssets builds the Rails asset-pipeline graph (Tier K.3):
//
//	layout.html.erb --imports--> application.js --imports--> studies.es6
//
// Two mechanisms, one edge type. `//= require` directives in an asset's header
// comment give the manifest→asset half; `javascript_include_tag` /
// `stylesheet_link_tag` in an ERB template give the page→manifest half. Joined,
// they answer "which page loads this file?" as a plain backward walk, with no
// new node type.
//
// Must run after LinkContainment. It mints the file nodes containment did not —
// an asset manifest of nothing but `//= require` lines and an ERB layout both
// declare nothing, and both are endpoints here (see fileNodeIndex).
func LinkSprocketsAssets(nodes []graph.Node, serviceFiles map[string][]string) (newNodes []graph.Node, newEdges []graph.Edge, unresolved []graph.UnresolvedRef) {
	files := newFileNodeIndex(nodes)

	seen := make(map[string]bool)
	addEdge := func(from, to, label, mechanism string, meta map[string]string) {
		id := fmt.Sprintf("imports:%s->%s", from, to)
		if from == to || seen[id] {
			return
		}
		seen[id] = true
		meta["mechanism"] = mechanism
		newEdges = append(newEdges, graph.Edge{
			ID: id, From: from, To: to, Type: graph.EdgeTypeImports,
			Label: label, Confidence: graph.ConfidenceStatic, Meta: meta,
		})
	}

	svcNames := make([]string, 0, len(serviceFiles))
	for svc := range serviceFiles {
		svcNames = append(svcNames, svc)
	}
	sort.Strings(svcNames) // map iteration must never reach output (bug-class #2)

	for _, svc := range svcNames {
		idx := newAssetIndex(serviceFiles[svc])
		if len(idx.roots) == 0 {
			continue // no app/assets tree: not a Sprockets service
		}
		linkAssetDirectives(svc, idx, files, addEdge, &unresolved)
		linkIncludeTags(svc, idx, serviceFiles[svc], files, addEdge, &unresolved)
	}

	return files.minted, append(newEdges, files.mintedEdges...), unresolved
}

// linkAssetDirectives walks every asset file's header comment block.
func linkAssetDirectives(svc string, idx *assetIndex, files *fileNodeIndex,
	addEdge func(from, to, label, mechanism string, meta map[string]string),
	unresolved *[]graph.UnresolvedRef) {

	for _, file := range idx.assets {
		src, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		directives := sprockets.ScanDirectives(src)
		if len(directives) == 0 {
			continue
		}
		fromID := files.ensure(svc, file)
		for _, d := range directives {
			targets := idx.resolveDirective(file, d)
			if len(targets) == 0 {
				*unresolved = append(*unresolved, graph.UnresolvedRef{
					Service: svc, File: file, Line: d.Line,
					Name: d.Path, Kind: "sprockets_require_unresolved",
				})
				continue
			}
			// `require_tree` names every file it expands to. Emitting only the
			// first would be the fan-out bug (phases.md #1); the directive is a
			// statement about all of them.
			for _, t := range targets {
				addEdge(fromID, files.ensure(svc, t),
					"//= "+d.Verb+" "+d.Path, "sprockets",
					map[string]string{"directive": d.Verb, "logical_path": d.Path})
			}
		}
	}
}

// linkIncludeTags wires each ERB template to the manifests it loads.
func linkIncludeTags(svc string, idx *assetIndex, serviceFiles []string, files *fileNodeIndex,
	addEdge func(from, to, label, mechanism string, meta map[string]string),
	unresolved *[]graph.UnresolvedRef) {

	erb := make([]string, 0, 16)
	for _, f := range serviceFiles {
		if strings.HasSuffix(f, ".erb") {
			erb = append(erb, f)
		}
	}
	sort.Strings(erb)

	for _, file := range erb {
		src, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		tags := sprockets.ScanIncludeTags(src)
		if len(tags) == 0 {
			continue
		}
		var fromID string
		for _, tag := range tags {
			kind := jsAsset
			if tag.Helper == "stylesheet_link_tag" {
				kind = cssAsset
			}
			var target string
			if !tag.Dynamic {
				target = idx.resolveLogical(file, tag.Name, kind)
			}
			if target == "" {
				*unresolved = append(*unresolved, graph.UnresolvedRef{
					Service: svc, File: file, Line: tag.Line,
					Name: tag.Name, Kind: "sprockets_include_unresolved",
				})
				continue
			}
			if fromID == "" {
				fromID = files.ensure(svc, file)
			}
			addEdge(fromID, files.ensure(svc, target),
				tag.Helper+" "+tag.Name, "include_tag",
				map[string]string{"helper": tag.Helper, "logical_path": tag.Name})
		}
	}
}

// assetKind selects which extensions a logical path is probed with first. A
// bare "application" names application.js from `javascript_include_tag` and
// application.scss from `stylesheet_link_tag`, and both files exist in the same
// app — the helper is the only thing that disambiguates them.
type assetKind int

const (
	anyAsset assetKind = iota
	jsAsset
	cssAsset
)

var (
	jsAssetExts  = []string{".js", ".es6", ".js.es6", ".js.erb", ".coffee"}
	cssAssetExts = []string{".css", ".scss", ".sass", ".css.erb"}
)

func (k assetKind) extensions() []string {
	switch k {
	case jsAsset:
		return append(append([]string{}, jsAssetExts...), cssAssetExts...)
	case cssAsset:
		return append(append([]string{}, cssAssetExts...), jsAssetExts...)
	}
	return append(append([]string{}, jsAssetExts...), cssAssetExts...)
}

// assetIndex is one service's Sprockets view: the indexed file set, the load
// paths a bare logical path is resolved against, and the asset files whose
// headers carry directives.
type assetIndex struct {
	files  map[string]bool
	all    []string
	roots  []string // load paths, in Sprockets precedence order
	assets []string // files under a load root that can carry a directive header
}

func newAssetIndex(serviceFiles []string) *assetIndex {
	idx := &assetIndex{files: make(map[string]bool, len(serviceFiles))}
	type rootRank struct {
		path string
		rank int
	}
	rootSet := map[string]int{}
	for _, f := range serviceFiles {
		idx.files[f] = true
		idx.all = append(idx.all, f)
		root, rank := assetLoadRoot(f)
		if root == "" {
			continue
		}
		if _, ok := rootSet[root]; !ok {
			rootSet[root] = rank
		}
		if isDirectiveHost(f) {
			idx.assets = append(idx.assets, f)
		}
	}
	sort.Strings(idx.all)
	sort.Strings(idx.assets)

	ranked := make([]rootRank, 0, len(rootSet))
	for p, r := range rootSet {
		ranked = append(ranked, rootRank{p, r})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].rank != ranked[j].rank {
			return ranked[i].rank < ranked[j].rank
		}
		return ranked[i].path < ranked[j].path
	})
	for _, r := range ranked {
		idx.roots = append(idx.roots, r.path)
	}
	return idx
}

// assetLoadRoot returns the Sprockets load path a file sits under — each
// immediate subdirectory of `app/assets`, `vendor/assets` or `lib/assets` is
// its own load path, which is why `//= require utility/common` and
// `//= require settings/colors` both work without naming `javascripts` or
// `stylesheets`. rank orders the three trees the way Rails appends them.
func assetLoadRoot(file string) (string, int) {
	dir := filepath.Dir(file)
	for {
		parent := filepath.Dir(dir)
		if filepath.Base(parent) == "assets" {
			switch filepath.Base(filepath.Dir(parent)) {
			case "app":
				return dir, 0
			case "vendor":
				return dir, 1
			case "lib":
				return dir, 2
			}
		}
		if parent == dir {
			return "", 0
		}
		dir = parent
	}
}

// isDirectiveHost reports whether a file's comment syntax can hold directives.
func isDirectiveHost(file string) bool {
	switch strings.ToLower(filepath.Ext(file)) {
	case ".js", ".es6", ".css", ".scss":
		return true
	}
	return false
}

// resolveDirective returns every indexed file a directive names.
func (idx *assetIndex) resolveDirective(from string, d sprockets.Directive) []string {
	if recursive, isDir := sprockets.FanoutVerbs(d.Verb); isDir {
		return idx.resolveDir(from, d.Path, d.Ext, recursive)
	}
	if t := idx.resolveLogical(from, d.Path, anyAsset); t != "" {
		return []string{t}
	}
	return nil
}

// resolveLogical maps a logical path to one indexed file. A relative path is
// resolved against the requiring file's directory; anything else against the
// load paths in order.
//
// Load-path order is a *precedence* rule, not ambiguity: Sprockets serves the
// first match and the later ones are shadowed, so taking the first is what the
// running app does. Returns "" when nothing matches — the caller ledgers it,
// which is the correct outcome for `//= require jquery/dist/jquery.min` in a
// workspace whose excludes drop node_modules.
func (idx *assetIndex) resolveLogical(from, spec string, kind assetKind) string {
	spec = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(spec), "/"))
	if spec == "" {
		return ""
	}
	for _, base := range idx.basesFor(from, spec) {
		if hit := idx.probe(base, spec, kind); hit != "" {
			return hit
		}
	}
	return ""
}

// basesFor picks the directories a specifier is resolved against.
func (idx *assetIndex) basesFor(from, spec string) []string {
	if strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") {
		return []string{filepath.Dir(from)}
	}
	return idx.roots
}

func (idx *assetIndex) probe(base, spec string, kind assetKind) string {
	joined := filepath.Clean(filepath.Join(base, spec))
	if idx.files[joined] {
		return joined
	}
	for _, e := range kind.extensions() {
		if idx.files[joined+e] {
			return joined + e
		}
	}
	// `//= require application.js` names the *logical* path application.js,
	// which the pipeline may satisfy with application.js.erb or application.es6.
	if ext := filepath.Ext(joined); ext != "" {
		stem := strings.TrimSuffix(joined, ext)
		for _, e := range kind.extensions() {
			if idx.files[stem+e] {
				return stem + e
			}
		}
	}
	return ""
}

// resolveDir expands a directory directive. ext, when set, is the extension
// filter of `//= link_directory ../builds .css`.
func (idx *assetIndex) resolveDir(from, spec, ext string, recursive bool) []string {
	var bases []string
	if strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") || spec == "." {
		bases = []string{filepath.Dir(from)}
	} else {
		bases = idx.roots
	}
	for _, base := range bases {
		dir := filepath.Clean(filepath.Join(base, spec))
		var out []string
		for _, f := range idx.all {
			if f == from || !underDir(f, dir, recursive) {
				continue
			}
			if ext != "" && !strings.EqualFold(filepath.Ext(f), ext) {
				continue
			}
			out = append(out, f)
		}
		if len(out) > 0 {
			return out // the first load path holding the directory wins
		}
	}
	return nil
}

func underDir(file, dir string, recursive bool) bool {
	if recursive {
		return strings.HasPrefix(file, dir+string(filepath.Separator))
	}
	return filepath.Dir(file) == dir
}
