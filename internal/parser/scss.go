package parser

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/css"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// StylesheetParser reads `.scss` and `.css` files (Tier K.5).
//
// Scope is deliberately three things — top-level class/id selectors,
// `@font-face` sources, and (via the linker) the `@import` graph. Selectors are
// the join targets K.4 needs to attribute a jQuery `$(".btn")` listener to the
// rule that styles it; everything else a stylesheet contains (variables,
// mixins, functions, declarations, nested descendant rules) carries no flow and
// is not emitted. See internal/css for why the extraction is hand-written.
type StylesheetParser struct{}

func (p *StylesheetParser) Language() string     { return "css" }
func (p *StylesheetParser) Extensions() []string { return []string{".scss", ".css"} }

func (p *StylesheetParser) Parse(file, service string, _ *patterns.TreeSitterMatcher) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, nil, err
	}
	res := css.Scan(src)
	lang := stylesheetLanguage(file)

	var nodes []graph.Node
	var unresolved []graph.UnresolvedRef

	// One node per distinct selector per file: a rule repeated for several
	// states (`.btn`, `.btn:hover`, `.btn.is-active`) is one join target, not
	// three. First occurrence wins the line.
	seen := make(map[string]bool, len(res.Selectors))
	for _, sel := range res.Selectors {
		text := sel.Text()
		if seen[text] {
			continue
		}
		seen[text] = true
		nodes = append(nodes, graph.Node{
			ID:       nodeID(service, file, graph.NodeTypeElement, text, sel.Line),
			Type:     graph.NodeTypeElement,
			Label:    text,
			Service:  service,
			File:     file,
			Line:     sel.Line,
			Language: lang,
			Meta: map[string]string{
				"pattern":       "stylesheet_selector",
				"selector":      text,
				"selector_kind": sel.Kind,
			},
		})
	}

	for _, fs := range res.FontSources {
		// `src: font-url("#{$file-path}.woff")` inside a mixin — the value is
		// only known after the mixin is called. Ledger it, never guess a file
		// (phases.md #12).
		if fs.Dynamic {
			unresolved = append(unresolved, graph.UnresolvedRef{
				Service: service, File: file, Line: fs.Line,
				Name: fs.URL, Kind: "stylesheet_font_dynamic",
			})
			continue
		}
		if n, ok := fontSourceNode(service, file, lang, fs); ok {
			nodes = append(nodes, n)
		}
	}

	return nodes, nil, unresolved, nil
}

// fontSourceNode turns one resolved `@font-face` src into a node: a remote URL
// becomes an external service, a local path becomes the asset's file node
// (sharing LinkContainment's ID scheme so an indexed asset dedups).
func fontSourceNode(service, file, lang string, fs css.FontSource) (graph.Node, bool) {
	meta := map[string]string{"pattern": "font_face_src", "url": fs.URL}
	if fs.Family != "" {
		meta["font_family"] = fs.Family
	}
	label := fs.Family
	if label == "" {
		label = fs.URL
	}

	if isRemoteAssetURL(fs.URL) {
		meta["scope"] = "remote"
		return graph.Node{
			ID:       nodeID(service, file, graph.NodeTypeExternalService, fs.URL, fs.Line),
			Type:     graph.NodeTypeExternalService,
			Label:    label,
			Service:  service,
			File:     file,
			Line:     fs.Line,
			Language: lang,
			Meta:     meta,
		}, true
	}

	target := fs.URL
	if i := strings.IndexAny(target, "?#"); i >= 0 {
		target = target[:i]
	}
	if target == "" {
		return graph.Node{}, false
	}
	abs := filepath.Clean(filepath.Join(filepath.Dir(file), target))
	meta["scope"] = "local"
	meta["path"] = abs
	return graph.Node{
		ID:       service + ":" + abs + ":" + string(graph.NodeTypeFile),
		Type:     graph.NodeTypeFile,
		Label:    abs,
		Service:  service,
		File:     abs,
		Language: lang,
		Meta:     meta,
	}, true
}

func isRemoteAssetURL(u string) bool {
	return strings.HasPrefix(u, "//") || strings.Contains(u, "://") || strings.HasPrefix(u, "data:")
}

// nodeID builds the service:file:type:label:line ID shape used across parsers.
func nodeID(service, file string, t graph.NodeType, label string, line int) string {
	return service + ":" + file + ":" + string(t) + ":" + label + ":" + strconv.Itoa(line)
}

// stylesheetLanguage tags `.scss` distinctly from `.css` so a search can scope
// to one or the other.
func stylesheetLanguage(file string) string {
	if strings.EqualFold(filepath.Ext(file), ".scss") {
		return "scss"
	}
	return "css"
}

func init() {
	Register(&StylesheetParser{})
}
