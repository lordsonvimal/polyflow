package parser

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// VueParser parses Vue single-file components (.vue).
//
// Strategy (hand-rolled SFC splitter — no Vue grammar in the pinned
// smacker/go-tree-sitter module; the top-level block tags are trivially
// scannable at column 0):
//
//  1. Locate top-level <template>, <script>, <script setup>, <style> blocks
//     by scanning for block-open tags at column 0. Blocks that begin at a
//     non-zero column are not recognised (spec-pinned negative test).
//
//  2. virtualScript: copy of source where everything outside <script>/<script setup>
//     blocks is replaced with spaces (newlines kept). JS/TS patterns run on this.
//
//  3. blankedScript: copy of source where <script> and <style> blocks are
//     replaced with spaces (newlines kept). HTML patterns run on this, giving
//     the template range its natural position.
//
//  4. Vue-specific template attributes (@click, v-on:, <router-link to>,
//     <NuxtLink to>) are extracted via a targeted regex scan of the template
//     range in blankedScript — tree-sitter HTML does not parse @ / v-on:
//     attribute names, so these patterns are handled outside the normal
//     HTML pattern pipeline.
//
// Both passes use the original file path so node IDs and line numbers refer
// to the actual .vue file.
type VueParser struct{}

func (p *VueParser) Language() string     { return "vue" }
func (p *VueParser) Extensions() []string { return []string{".vue"} }

func (p *VueParser) Parse(file, service string, matcher *patterns.TreeSitterMatcher) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, nil, err
	}

	// `file` arrives absolute (needed for the os.ReadFile above); every node
	// this parser mints must carry the cwd-relative form instead, matching
	// the Go semantic pass's convention — extractVueTemplateAttrs builds
	// nodes directly (bypassing the matcher's own relativization).
	file = patterns.RelativizeToCwd(file)

	blocks := splitSFC(src)

	virtualScript, scriptLang := buildVirtualScript(src, blocks)
	blankedScript := buildBlankedScript(src, blocks)

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
	setLanguage(scriptNodes, "vue")

	// HTML pass on blanked-script view (template visible).
	htmlResults, htmlErr := matcher.Match("html", file, blankedScript)
	if htmlErr != nil && err == nil {
		err = htmlErr
	}
	htmlNodes, htmlEdges, htmlUnresolved := patterns.MatchToGraph(service, htmlResults)
	setLanguage(htmlNodes, "vue")

	// Vue-specific template extraction: @click / v-on: events and router-link / NuxtLink.
	vueNodes, vueEdges, vueUnresolved := extractVueTemplateAttrs(src, blocks, file, service)

	nodes := append(scriptNodes, htmlNodes...)
	nodes = append(nodes, vueNodes...)
	edges := append(scriptEdges, htmlEdges...)
	edges = append(edges, vueEdges...)
	unresolved := append(scriptUnresolved, htmlUnresolved...)
	unresolved = append(unresolved, vueUnresolved...)

	return nodes, edges, unresolved, err
}

// sfcBlock records the byte range of one top-level SFC block.
type sfcBlock struct {
	kind     string // "template" | "script" | "script setup" | "style"
	attrLang string // value of lang= attribute, e.g. "ts"
	start    int    // byte offset of the opening '<' of the tag
	bodyEnd  int    // byte offset just after the last char of the body (before close tag)
	end      int    // byte offset just after '>' of the closing tag
}

// reBlockOpen matches a top-level SFC opening tag at the start of a line.
// Group 1: block type keyword ("template"|"script"|"style").
// Group 2 (optional): full attrs string inside the tag.
var reBlockOpen = regexp.MustCompile(`(?m)^<(template|script|style)(\s[^>]*)?>`)

// reLangAttr extracts lang="ts" or lang='ts' from an attrs string.
var reLangAttr = regexp.MustCompile(`(?i)lang=["']([^"']+)["']`)

// reSetup detects the "setup" keyword in script tag attrs.
var reSetup = regexp.MustCompile(`\bsetup\b`)

// splitSFC identifies top-level <template>, <script>, <script setup>, and
// <style> blocks in a Vue SFC source. Only blocks whose opening tag starts at
// column 0 (the start of a line) are recognised — spec-pinned constraint that
// prevents a <template> inside a string literal in the script block from
// confusing the splitter.
func splitSFC(src []byte) []sfcBlock {
	matches := reBlockOpen.FindAllSubmatchIndex(src, -1)
	var blocks []sfcBlock
	for _, m := range matches {
		// m[0]:m[1] = full match; m[2]:m[3] = group 1 (type); m[4]:m[5] = group 2 (attrs)
		tagStart := m[0]
		tagEnd := m[1]
		kind := string(src[m[2]:m[3]])

		attrLang := ""
		isSetup := false
		if m[4] >= 0 {
			attrs := string(src[m[4]:m[5]])
			if lm := reLangAttr.FindStringSubmatch(attrs); lm != nil {
				attrLang = strings.ToLower(lm[1])
			}
			if kind == "script" && reSetup.MatchString(attrs) {
				isSetup = true
			}
		}

		blockKind := kind
		if isSetup {
			blockKind = "script setup"
		}

		// Find the matching close tag: </template>, </script>, or </style>.
		closeTag := []byte("</" + kind + ">")
		closeIdx := bytes.Index(src[tagEnd:], closeTag)
		var bodyEnd, blockEnd int
		if closeIdx < 0 {
			// Unclosed block: consume to end.
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

// buildVirtualScript returns a byte slice where everything outside
// <script>/<script setup> blocks is replaced with spaces (newlines kept).
// Also returns the lang ("ts" if any script block has lang="ts", else "js").
func buildVirtualScript(src []byte, blocks []sfcBlock) ([]byte, string) {
	out := bytes.Clone(src)
	// Blank everything first.
	for i := range out {
		if out[i] != '\n' {
			out[i] = ' '
		}
	}
	lang := "js"
	for _, b := range blocks {
		if b.kind != "script" && b.kind != "script setup" {
			continue
		}
		if b.attrLang == "ts" || b.attrLang == "tsx" {
			lang = "ts"
		}
		// Restore the script body (between open tag close and close tag open).
		// We find the end of the opening tag's '>' and restore from there.
		// The opening tag itself is kept as spaces; only the body is restored.
		openTagClose := findTagBodyStart(src, b.start)
		for i := openTagClose; i < b.bodyEnd; i++ {
			out[i] = src[i]
		}
	}
	return out, lang
}

// buildBlankedScript returns a byte slice where <script> and <style> blocks
// (including their opening/closing tags) are replaced with spaces (newlines
// kept), leaving the <template> range at its original byte offsets.
func buildBlankedScript(src []byte, blocks []sfcBlock) []byte {
	out := bytes.Clone(src)
	for _, b := range blocks {
		if b.kind == "template" {
			continue
		}
		// Blank the entire block (open tag + body + close tag).
		for i := b.start; i < b.end; i++ {
			if out[i] != '\n' {
				out[i] = ' '
			}
		}
	}
	return out
}

// findTagBodyStart returns the byte offset just after the first '>' in src
// starting at tagStart, i.e. where the tag body begins. Falls back to
// tagStart if no '>' is found.
func findTagBodyStart(src []byte, tagStart int) int {
	for i := tagStart; i < len(src); i++ {
		if src[i] == '>' {
			return i + 1
		}
	}
	return tagStart
}

// ─── Vue-specific template attribute extraction ────────────────────────────

// reVueEvent matches @eventname.modifier="handler" and v-on:eventname.modifier="handler"
// in template text. Group 1: raw event+modifier string. Group 2: handler expression.
var reVueEvent = regexp.MustCompile(`(?:@|v-on:)([\w.]+)=["']([^"']*)["']`)

// reRouterLink matches the static `to` attribute of <router-link> and <NuxtLink>.
// Group 1: path value. Bound (:to="expr") is captured by reRouterLinkBound.
var reRouterLink = regexp.MustCompile(`<(?:router-link|NuxtLink)\b[^>]*\bto=["'](/[^"']*)["']`)

// reRouterLinkBound matches :to="expr" (bound, dynamic) on router-link / NuxtLink.
var reRouterLinkBound = regexp.MustCompile(`<(?:router-link|NuxtLink)\b[^>]*:to=["']([^"']*)["']`)

// extractVueTemplateAttrs scans the template range for Vue-specific event
// bindings and router-link / NuxtLink navigation nodes. It operates on the
// original src bytes, restricted to the template block body.
func extractVueTemplateAttrs(src []byte, blocks []sfcBlock, file, service string) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef) {
	// Find the template block body range.
	var tmplStart, tmplEnd int
	found := false
	for _, b := range blocks {
		if b.kind == "template" {
			tmplStart = findTagBodyStart(src, b.start)
			tmplEnd = b.bodyEnd
			found = true
			break
		}
	}
	if !found {
		return nil, nil, nil
	}

	tmpl := src[tmplStart:tmplEnd]

	var nodes []graph.Node
	var edges []graph.Edge
	var unresolved []graph.UnresolvedRef

	// Count newlines before tmplStart to correctly compute line numbers.
	baseLines := bytes.Count(src[:tmplStart], []byte{'\n'})

	// Event bindings: @click="save" / v-on:click="save" / @submit.prevent="onSubmit"
	for _, m := range reVueEvent.FindAllSubmatchIndex(tmpl, -1) {
		rawEvent := string(tmpl[m[2]:m[3]]) // e.g. "click", "submit.prevent"
		handler := string(tmpl[m[4]:m[5]])  // e.g. "save", "onSubmit"
		matchStart := m[0]

		eventName := normalizeVueEvent(rawEvent) // "click", "submit"
		handler = stripCallArgs(handler)         // "save(x)" → "save"

		lineNo := baseLines + bytes.Count(tmpl[:matchStart], []byte{'\n'}) + 1
		nodeID := fmt.Sprintf("%s:%s:vue_event:%d:%s", service, file, lineNo, eventName)
		nodes = append(nodes, graph.Node{
			ID:       nodeID,
			Type:     graph.NodeTypeDOMTarget,
			Label:    eventName + " handler",
			Service:  service,
			File:     file,
			Line:     lineNo,
			EndLine:  lineNo,
			Language: "vue",
			Meta: map[string]string{
				"event":   eventName,
				"handler": handler,
				"pattern": "vue_event_attr",
			},
		})

		if handler != "" {
			// Record as unresolved call_ref — the JS linker resolves script-block
			// functions; same-file resolution is handled by the existing call_ref path.
			unresolved = append(unresolved, graph.UnresolvedRef{
				Service: service,
				File:    file,
				Line:    lineNo,
				Name:    handler,
				Kind:    "call_ref",
			})
		}
	}

	// Static router-link / NuxtLink: to="/path"
	for _, m := range reRouterLink.FindAllSubmatchIndex(tmpl, -1) {
		path := string(tmpl[m[2]:m[3]])
		matchStart := m[0]
		lineNo := baseLines + bytes.Count(tmpl[:matchStart], []byte{'\n'}) + 1
		nodeID := fmt.Sprintf("%s:%s:nav_link_vue:%d:%s", service, file, lineNo, path)
		nodes = append(nodes, graph.Node{
			ID:       nodeID,
			Type:     graph.NodeTypeHTTPClient,
			Label:    "GET " + path,
			Service:  service,
			File:     file,
			Line:     lineNo,
			EndLine:  lineNo,
			Language: "vue",
			Meta: map[string]string{
				"path":     path,
				"method":   "GET",
				"nav_link": "true",
				"pattern":  "nav_link_vue",
			},
		})
	}

	// Bound :to="expr" — ledger as dynamic_url; these can't be resolved statically.
	for _, m := range reRouterLinkBound.FindAllSubmatchIndex(tmpl, -1) {
		expr := string(tmpl[m[2]:m[3]])
		matchStart := m[0]
		lineNo := baseLines + bytes.Count(tmpl[:matchStart], []byte{'\n'}) + 1
		unresolved = append(unresolved, graph.UnresolvedRef{
			Service: service,
			File:    file,
			Line:    lineNo,
			Name:    expr,
			Kind:    "dynamic_url",
		})
	}

	return nodes, edges, unresolved
}

// normalizeVueEvent strips Vue event prefixes (@, v-on:) and modifiers (.prevent, .stop, etc.)
// returning the bare lowercase event name.
func normalizeVueEvent(raw string) string {
	// raw is everything AFTER @ or v-on: was stripped by the regex group.
	// Strip modifiers: "submit.prevent" → "submit".
	if idx := strings.IndexByte(raw, '.'); idx >= 0 {
		raw = raw[:idx]
	}
	return strings.ToLower(raw)
}

// stripCallArgs strips argument lists from handler expressions.
// "save(x)" → "save", "save" → "save".
func stripCallArgs(expr string) string {
	if idx := strings.IndexByte(expr, '('); idx >= 0 {
		return strings.TrimSpace(expr[:idx])
	}
	return strings.TrimSpace(expr)
}

func init() {
	Register(&VueParser{})
}
