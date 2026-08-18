package parser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	jssitter "github.com/smacker/go-tree-sitter/javascript"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// SvelteParser parses Svelte single-file components (.svelte).
//
// Strategy (hand-rolled SFC splitter — same mechanism as M.1 Vue and L.W0 ERB;
// the pinned smacker/go-tree-sitter module does ship a svelte grammar but the
// SFC-splitter approach is pinned for consistency — one mechanism, three
// formats):
//
//  1. Locate top-level <script>, <script context="module">, and <style> blocks
//     by scanning for block-open tags at column 0.
//
//  2. virtualScript: copy of source where everything outside <script> blocks is
//     replaced with spaces (newlines kept). JS/TS patterns run on this.
//
//  3. blankedScript: copy of source where <script> and <style> blocks are
//     replaced with spaces (newlines kept). HTML patterns run on this, giving
//     the markup its natural position. <a href="/x"> nav-links are covered by
//     existing HTML nav_link_href patterns.
//
//  4. Svelte-specific markup attributes — on:event={handler} bindings and
//     href={expr} dynamic hrefs — are extracted via targeted regex scan of
//     blankedScript (HTML tree-sitter does not parse Svelte attribute syntax).
//
// Both passes use the original file path so node IDs and line numbers refer to
// the actual .svelte file.
type SvelteParser struct{}

func (p *SvelteParser) Language() string     { return "svelte" }
func (p *SvelteParser) Extensions() []string { return []string{".svelte"} }

func (p *SvelteParser) Parse(file, service string, matcher *patterns.TreeSitterMatcher, cache SourceCache) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef, error) {
	src, err := readSource(file, cache)
	if err != nil {
		return nil, nil, nil, err
	}

	// `file` arrives absolute (needed for readSource/os.ReadFile); every node
	// this parser mints must carry the cwd-relative form instead, matching
	// the Go semantic pass's convention — extractSvelteMarkupAttrs builds
	// nodes directly (bypassing the matcher's own relativization).
	file = patterns.RelativizeToCwd(file)

	blocks := splitSvelteSFC(src)

	virtualScript, scriptLang := buildSvelteVirtualScript(src, blocks)
	blankedScript := buildSvelteBlankedScript(src, blocks)

	// Script pass: JS or TS patterns depending on lang= attribute.
	grammarLang := "javascript"
	patternLangs := []string{"javascript"}
	if scriptLang == "ts" {
		grammarLang = "typescript"
		patternLangs = append(patternLangs, "typescript")
	}

	var allResults []patterns.MatchResult
	for _, pl := range patternLangs {
		results, matchErr := matcher.MatchWithGrammar(pl, grammarLang, file, virtualScript)
		if matchErr != nil && err == nil {
			err = matchErr
		}
		allResults = append(allResults, results...)
	}
	scriptNodes, scriptEdges, scriptUnresolved := patterns.MatchToGraph(service, allResults)
	setLanguage(scriptNodes, "svelte")

	// HTML pass on blanked-script view (markup visible).
	// <a href="/x"> nav-links are picked up here by html nav_link_href patterns.
	htmlResults, htmlErr := matcher.Match("html", file, blankedScript)
	if htmlErr != nil && err == nil {
		err = htmlErr
	}
	htmlNodes, htmlEdges, htmlUnresolved := patterns.MatchToGraph(service, htmlResults)
	setLanguage(htmlNodes, "svelte")

	// Svelte-specific markup extraction: on:event={handler} and href={expr}.
	svelteNodes, svelteEdges, svelteUnresolved := extractSvelteMarkupAttrs(blankedScript, file, service)

	nodes := append(scriptNodes, htmlNodes...)
	nodes = append(nodes, svelteNodes...)
	edges := append(scriptEdges, htmlEdges...)
	edges = append(edges, svelteEdges...)
	unresolved := append(scriptUnresolved, htmlUnresolved...)
	unresolved = append(unresolved, svelteUnresolved...)

	return nodes, edges, unresolved, err
}

// ─── SFC splitter ─────────────────────────────────────────────────────────────

// reSvelteBlockOpen matches a top-level SFC opening tag at the start of a line.
// Group 1: block type keyword ("script"|"style").
// Group 2 (optional): full attrs string inside the tag.
var reSvelteBlockOpen = regexp.MustCompile(`(?m)^<(script|style)(\s[^>]*)?>`)

// reSvelteContextModule detects the context="module" attribute on a script tag.
var reSvelteContextModule = regexp.MustCompile(`\bcontext=["']module["']`)

// splitSvelteSFC identifies top-level <script>, <script context="module">, and
// <style> blocks. Only blocks whose opening tag starts at column 0 (the start of
// a line) are recognised — prevents strings inside scripts from confusing the
// splitter. The Svelte template (markup) is implicit: everything else.
func splitSvelteSFC(src []byte) []sfcBlock {
	matches := reSvelteBlockOpen.FindAllSubmatchIndex(src, -1)
	var blocks []sfcBlock
	for _, m := range matches {
		tagStart := m[0]
		tagEnd := m[1]
		kind := string(src[m[2]:m[3]])

		attrLang := ""
		isModule := false
		if m[4] >= 0 {
			attrs := string(src[m[4]:m[5]])
			if lm := reLangAttr.FindStringSubmatch(attrs); lm != nil {
				attrLang = strings.ToLower(lm[1])
			}
			if kind == "script" && reSvelteContextModule.MatchString(attrs) {
				isModule = true
			}
		}

		blockKind := kind
		if isModule {
			blockKind = "script module"
		}

		closeTag := []byte("</" + kind + ">")
		closeIdx := bytes.Index(src[tagEnd:], closeTag)
		var bodyEnd, blockEnd int
		if closeIdx < 0 {
			bodyEnd = len(src)
			blockEnd = len(src)
		} else {
			bodyEnd = tagEnd + closeIdx
			blockEnd = bodyEnd + len(closeTag)
		}

		blocks = append(blocks, sfcBlock{
			kind:     blockKind,
			attrLang: attrLang,
			start:    tagStart,
			bodyEnd:  bodyEnd,
			end:      blockEnd,
		})
	}
	return blocks
}

// buildSvelteVirtualScript returns a byte slice where everything outside
// <script>/<script context="module"> blocks is replaced with spaces (newlines
// kept). Also returns the lang ("ts" if any script block has lang="ts", else "js").
func buildSvelteVirtualScript(src []byte, blocks []sfcBlock) ([]byte, string) {
	out := bytes.Clone(src)
	for i := range out {
		if out[i] != '\n' {
			out[i] = ' '
		}
	}
	lang := "js"
	for _, b := range blocks {
		if b.kind != "script" && b.kind != "script module" {
			continue
		}
		if b.attrLang == "ts" || b.attrLang == "tsx" {
			lang = "ts"
		}
		openTagClose := findTagBodyStart(src, b.start)
		for i := openTagClose; i < b.bodyEnd; i++ {
			out[i] = src[i]
		}
	}
	return out, lang
}

// buildSvelteBlankedScript returns a byte slice where <script> and <style>
// blocks (including their opening/closing tags) are replaced with spaces
// (newlines kept), leaving the Svelte markup at its original byte offsets.
func buildSvelteBlankedScript(src []byte, blocks []sfcBlock) []byte {
	out := bytes.Clone(src)
	for _, b := range blocks {
		for i := b.start; i < b.end; i++ {
			if out[i] != '\n' {
				out[i] = ' '
			}
		}
	}
	return out
}

// ─── Svelte-specific markup attribute extraction ───────────────────────────────

// reSvelteEvent matches on:eventname|modifier={handler} in Svelte markup.
// Group 1: event name with optional pipe-separated modifiers (e.g. "click|preventDefault").
// Group 2: handler expression inside braces (e.g. "save", "handleClick").
var reSvelteEvent = regexp.MustCompile(`\bon:([\w|]+)=\{([^}]*)\}`)

// reSvelteDynamicHref matches href={expr} in Svelte markup.
// Group 1: expression inside braces (e.g. `"/path"`, `cond ? "/a" : "/b"`).
// Static href="/path" (quoted_attribute_value) is covered by HTML nav_link_href patterns.
var reSvelteDynamicHref = regexp.MustCompile(`\bhref=\{([^}]+)\}`)

// extractSvelteMarkupAttrs scans the blankedScript for Svelte-specific event
// bindings and dynamic href expressions. blanked has script/style blocks replaced
// with spaces so events in script blocks are never mistakenly matched.
func extractSvelteMarkupAttrs(blanked []byte, file, service string) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef) {
	var nodes []graph.Node
	var edges []graph.Edge
	var unresolved []graph.UnresolvedRef

	// on:event={handler} bindings.
	for _, m := range reSvelteEvent.FindAllSubmatchIndex(blanked, -1) {
		rawEvent := string(blanked[m[2]:m[3]]) // e.g. "click", "click|preventDefault"
		handler := string(blanked[m[4]:m[5]])  // e.g. "save", "handleClick"
		matchStart := m[0]

		eventName := normalizeSvelteEvent(rawEvent) // "click"
		handler = stripCallArgs(handler)            // "save(x)" → "save"

		lineNo := bytes.Count(blanked[:matchStart], []byte{'\n'}) + 1
		nodeID := fmt.Sprintf("%s:%s:svelte_event:%d:%s", service, file, lineNo, eventName)
		nodes = append(nodes, graph.Node{
			ID:       nodeID,
			Type:     graph.NodeTypeDOMTarget,
			Label:    eventName + " handler",
			Service:  service,
			File:     file,
			Line:     lineNo,
			EndLine:  lineNo,
			Language: "svelte",
			Meta: map[string]string{
				"event":   eventName,
				"handler": handler,
				"pattern": "svelte_event_attr",
			},
		})

		if handler != "" {
			unresolved = append(unresolved, graph.UnresolvedRef{
				Service: service,
				File:    file,
				Line:    lineNo,
				Name:    handler,
				Kind:    "call_ref",
			})
		}
	}

	// href={expr} dynamic hrefs — pass expression to JS walker (G.6 convention).
	w := contract.KeyWalkerFor("javascript")
	for _, m := range reSvelteDynamicHref.FindAllSubmatchIndex(blanked, -1) {
		expr := strings.TrimSpace(string(blanked[m[2]:m[3]]))
		matchStart := m[0]
		lineNo := bytes.Count(blanked[:matchStart], []byte{'\n'}) + 1

		candidates, dynamic := walkSvelteHrefExpr(expr, w)
		switch {
		case dynamic:
			unresolved = append(unresolved, graph.UnresolvedRef{
				Service: service,
				File:    file,
				Line:    lineNo,
				Name:    expr,
				Kind:    "dynamic_url",
			})
		case len(candidates) == 1:
			path := candidates[0]
			nodeID := fmt.Sprintf("%s:%s:nav_link_svelte:%d:%s", service, file, lineNo, path)
			nodes = append(nodes, graph.Node{
				ID:       nodeID,
				Type:     graph.NodeTypeHTTPClient,
				Label:    "GET " + path,
				Service:  service,
				File:     file,
				Line:     lineNo,
				Language: "svelte",
				Meta: map[string]string{
					"path":     path,
					"method":   "GET",
					"nav_link": "true",
					"pattern":  "nav_link_svelte",
				},
			})
		case len(candidates) > 1:
			// Multiple literal branches (e.g. ternary): emit key_candidates meta.
			candidatesJSON, _ := json.Marshal(candidates)
			nodeID := fmt.Sprintf("%s:%s:nav_link_svelte_candidates:%d", service, file, lineNo)
			nodes = append(nodes, graph.Node{
				ID:       nodeID,
				Type:     graph.NodeTypeHTTPClient,
				Label:    "GET " + candidates[0],
				Service:  service,
				File:     file,
				Line:     lineNo,
				Language: "svelte",
				Meta: map[string]string{
					"method":         "GET",
					"nav_link":       "true",
					"key_candidates": string(candidatesJSON),
					"pattern":        "nav_link_svelte",
				},
			})
		}
	}

	return nodes, edges, unresolved
}

// walkSvelteHrefExpr parses the given JS expression string and passes it to the
// JS KeyWalker to enumerate literal URL candidates. Returns (nil, true) for
// dynamic/unresolvable expressions. Returns ([]string, false) for static or
// enumerable expressions.
func walkSvelteHrefExpr(expr string, w contract.KeyWalker) ([]string, bool) {
	if w == nil {
		return nil, true
	}
	b := []byte(expr)
	root, err := sitter.ParseCtx(context.Background(), b, jssitter.GetLanguage())
	if err != nil || root == nil {
		return nil, true
	}
	exprNode := svelteDescendToExpr(root)
	return w.WalkKey(exprNode, b, func(string) (string, bool) { return "", false })
}

// svelteDescendToExpr descends through program and expression_statement wrapper
// nodes to reach the first actual expression node, suitable for passing to WalkKey.
func svelteDescendToExpr(root *sitter.Node) *sitter.Node {
	node := root
	for {
		var child *sitter.Node
		for i := 0; i < int(node.ChildCount()); i++ {
			c := node.Child(i)
			if c != nil && c.Type() != "comment" {
				child = c
				break
			}
		}
		if child == nil {
			return node
		}
		switch child.Type() {
		case "program", "expression_statement":
			node = child
		default:
			return child
		}
	}
}

// normalizeSvelteEvent strips Svelte event modifiers (|preventDefault,
// |stopPropagation, etc.) and returns the bare lowercase event name.
func normalizeSvelteEvent(raw string) string {
	if idx := strings.IndexByte(raw, '|'); idx >= 0 {
		raw = raw[:idx]
	}
	return strings.ToLower(raw)
}

func init() {
	Register(&SvelteParser{})
}
