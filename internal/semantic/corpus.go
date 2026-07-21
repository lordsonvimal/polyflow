package semantic

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/trace"
)

// chainNodeCap is the maximum number of hops per chain document (S.1 spec).
const chainNodeCap = 12

// docChunkMaxChars is the approximate max bytes per doc chunk (~200 tokens at
// ~4 chars/token). Markdown headers and paragraph breaks also cut chunks early.
const docChunkMaxChars = 800

// ServicePath pairs an on-disk path with its service name for doc chunk building.
type ServicePath struct {
	Path    string
	Service string
}

// fileNodeEntry holds a line→nodeID pair used for nearest-node association
// when anchoring code doc-comment chunks to the node they document.
type fileNodeEntry struct {
	line int
	id   string
}

// ─── node card builder ─────────────────────────────────────────────────────

// BuildNodeCard produces an Entity for a single graph node.
// Card text format: "label type service file [route/signature meta]"
// The richer format makes "POST /users" findable even when the function is
// named "listUsers" — the method+path meta bridges the vocabulary gap.
func BuildNodeCard(n *graph.Node) Entity {
	text := nodeCardText(n)
	h := sha256.Sum256([]byte(text))
	return Entity{
		ID:          n.ID,
		Type:        "node",
		Text:        text,
		ContentHash: hex.EncodeToString(h[:]),
		NodeID:      n.ID,
		File:        n.File,
		Line:        n.Line,
	}
}

// nodeCardText returns the one-line card string for a node.
func nodeCardText(n *graph.Node) string {
	parts := []string{n.Label, string(n.Type), n.Service, n.File}
	if m := n.Meta; m != nil {
		switch n.Type {
		case graph.NodeTypeHTTPHandler, graph.NodeTypeRoute, graph.NodeTypeHTTPClient:
			method, path := m["method"], m["path"]
			switch {
			case method != "" && path != "":
				parts = append(parts, method+" "+path)
			case method != "":
				parts = append(parts, method)
			case path != "":
				parts = append(parts, path)
			}
		case graph.NodeTypeGRPCHandler, graph.NodeTypeGRPCClient:
			if sm := m["service_method"]; sm != "" {
				parts = append(parts, sm)
			}
		case graph.NodeTypeGraphQLResolver, graph.NodeTypeGraphQLClient:
			if op := m["operation"]; op != "" {
				parts = append(parts, op)
			}
		case graph.NodeTypePublisher, graph.NodeTypeSubscriber:
			for _, k := range []string{"exchange", "routing_key", "channel", "topic"} {
				if v := m[k]; v != "" {
					parts = append(parts, v)
				}
			}
		}
	}
	return strings.Join(parts, " ")
}

// ─── flow chain builder ────────────────────────────────────────────────────

// entrypointTypes are the node types that are meaningful flow roots.
// A forward chain from one of these answers "show me the X flow end to end".
var entrypointTypes = map[graph.NodeType]bool{
	graph.NodeTypeRoute:       true,
	graph.NodeTypeHTTPHandler: true,
	graph.NodeTypeSubscriber:  true,
	graph.NodeTypeWorker:      true,
}

// BuildFlowChains assembles one Entity per distinct forward chain from each
// entrypoint node in idx.  Chains longer than chainNodeCap hops are trimmed;
// single-hop paths (entry node only) are skipped.
// The output is sorted by entity ID for determinism (bug-class rule 2).
func BuildFlowChains(idx *graph.AdjacencyIndex) []Entity {
	// Collect entrypoint IDs in sorted order so the output order is stable
	// regardless of Go map iteration order (bug-class rule 2).
	var entryIDs []string
	for id, n := range idx.Nodes {
		if entrypointTypes[n.Type] {
			entryIDs = append(entryIDs, id)
		}
	}
	sort.Strings(entryIDs)

	seen := make(map[string]bool)
	var out []Entity

	for _, rootID := range entryIDs {
		result := trace.Run(idx, rootID, "forward", 0, false, 0)
		if result == nil {
			continue
		}
		for _, chain := range result.Chains {
			hops := chain.Hops
			if len(hops) < 2 {
				continue // single-hop = just the root; not a useful chain
			}
			if len(hops) > chainNodeCap {
				hops = hops[:chainNodeCap]
			}

			memberIDs := make([]string, len(hops))
			for i, h := range hops {
				memberIDs[i] = h.ID
			}
			// Dedupe: "chain:<rootID>:<12-char hash of member IDs>"
			memberKey := strings.Join(memberIDs, ",")
			mh := sha256.Sum256([]byte(memberKey))
			memberHash := hex.EncodeToString(mh[:])[:12]
			entityID := "chain:" + rootID + ":" + memberHash

			if seen[entityID] {
				continue
			}
			seen[entityID] = true

			text := chainDocText(idx, hops)
			ch := sha256.Sum256([]byte(text))
			rootNode := idx.Nodes[rootID]
			out = append(out, Entity{
				ID:          entityID,
				Type:        "flow",
				Text:        text,
				ContentHash: hex.EncodeToString(ch[:]),
				NodeID:      rootID,
				Members:     memberIDs,
				File:        rootNode.File,
				Line:        rootNode.Line,
			})
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// chainDocText builds the embedding document for one chain: space-separated
// label tokens plus edge-type annotations and route/exchange meta.
// Both the FTS and vector arms rank identical text (S.1 spec requirement).
func chainDocText(idx *graph.AdjacencyIndex, hops []trace.Hop) string {
	var parts []string
	for i, h := range hops {
		parts = append(parts, h.Label)
		// Preserve meaningful edge types; skip generic "calls"/"contains"
		// to keep documents concise.
		if i > 0 && h.EdgeType != "" && h.EdgeType != "calls" && h.EdgeType != "contains" {
			parts = append(parts, h.EdgeType)
		}
		if n, ok := idx.Nodes[h.ID]; ok && n.Meta != nil {
			for _, k := range []string{"method", "path", "exchange", "topic"} {
				if v := n.Meta[k]; v != "" {
					parts = append(parts, v)
				}
			}
		}
	}
	return strings.Join(parts, " ")
}

// ─── doc chunk builder ─────────────────────────────────────────────────────

// BuildDocChunks extracts doc entities from Markdown files and code
// doc-comments found under each service path.
// allNodes is used to associate doc-comment chunks with the nearest node
// in the same file (sets Entity.NodeID), bridging jargon in comments to
// the code entity they describe.
// Returns entities sorted by ID for determinism.
func BuildDocChunks(svcPaths []ServicePath, allNodes []graph.Node) []Entity {
	// Build file→sorted-lineNode map for nearest-node lookup.
	fileNodes := buildFileNodeMap(allNodes)

	var out []Entity
	for _, sp := range svcPaths {
		chunks := walkServiceDocs(sp.Path, sp.Service, fileNodes)
		out = append(out, chunks...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// buildFileNodeMap returns a map from file path to a sorted slice of
// (line, nodeID) pairs, used to find the nearest node for a doc-comment chunk.
func buildFileNodeMap(allNodes []graph.Node) map[string][]fileNodeEntry {
	m := make(map[string][]fileNodeEntry)
	for _, n := range allNodes {
		if n.File != "" && n.Line > 0 {
			m[n.File] = append(m[n.File], fileNodeEntry{n.Line, n.ID})
		}
	}
	for f := range m {
		sl := m[f]
		sort.Slice(sl, func(i, j int) bool { return sl[i].line < sl[j].line })
		m[f] = sl
	}
	return m
}

// nearestNodeAt returns the ID of the first node in the same file whose line
// is ≥ targetLine, i.e. the node immediately below the comment block.
func nearestNodeAt(file string, targetLine int, fileNodes map[string][]fileNodeEntry) string {
	for _, e := range fileNodes[file] {
		if e.line >= targetLine {
			return e.id
		}
	}
	return ""
}

// walkServiceDocs walks one service directory and returns doc entities for all
// Markdown files and code doc-comments found there.
func walkServiceDocs(svcPath, service string, fileNodes map[string][]fileNodeEntry) []Entity {
	var out []Entity
	_ = filepath.WalkDir(svcPath, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".md", ".mdx", ".markdown":
			out = append(out, chunkMarkdownFile(path, service)...)
		case ".go":
			out = append(out, extractGoDocComments(path, service, fileNodes)...)
		case ".ts", ".tsx", ".js", ".jsx":
			out = append(out, extractJSDocComments(path, service, fileNodes)...)
		case ".rb":
			out = append(out, extractRubyDocComments(path, service, fileNodes)...)
		}
		return nil
	})
	return out
}

// ─── markdown chunker ──────────────────────────────────────────────────────

// chunkMarkdownFile splits a Markdown file into ~200-token chunks.
// Splits at ATX headers (lines starting with #) and whenever the accumulated
// buffer exceeds docChunkMaxChars.
func chunkMarkdownFile(path, service string) []Entity {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var chunks []Entity
	var buf strings.Builder
	startLine := 1
	currentLine := 0
	chunkN := 0

	flush := func() {
		text := strings.TrimSpace(buf.String())
		if text == "" {
			return
		}
		id := fmt.Sprintf("doc:%s:%d", path, chunkN)
		h := sha256.Sum256([]byte(text))
		chunks = append(chunks, Entity{
			ID:          id,
			Type:        "doc",
			Text:        text,
			ContentHash: hex.EncodeToString(h[:]),
			File:        path,
			Line:        startLine,
		})
		chunkN++
		buf.Reset()
	}

	scanner := bufio.NewScanner(f)
	// Allow scanning lines up to 1MB (handles very long generated markdown).
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		currentLine++
		line := scanner.Text()

		// Split before ATX headers.
		if strings.HasPrefix(strings.TrimSpace(line), "#") && buf.Len() > 0 {
			flush()
			startLine = currentLine
		}

		// Flush before writing a line that would overflow the chunk, so each
		// chunk stays within the size budget.  Applies to large single lines
		// (e.g. generated markdown with long paragraphs).
		if buf.Len() > 0 && buf.Len()+len(line)+1 >= docChunkMaxChars {
			flush()
			startLine = currentLine
		}

		buf.WriteString(line)
		buf.WriteByte('\n')

		// Also split if a single line alone overflows (the line itself is huge).
		if buf.Len() >= docChunkMaxChars {
			flush()
			startLine = currentLine + 1
		}
	}
	flush()
	return chunks
}

// ─── code doc-comment extractors ──────────────────────────────────────────

// extractGoDocComments scans a Go file for `//` comment blocks immediately
// before func/type/var/const declarations.
func extractGoDocComments(path, service string, fileNodes map[string][]fileNodeEntry) []Entity {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	return extractLineComments(path, lines, "//",
		[]string{"func ", "type ", "var ", "const "},
		fileNodes)
}

// extractRubyDocComments scans a Ruby file for `#` comment blocks before
// def/class/module declarations.
func extractRubyDocComments(path, service string, fileNodes map[string][]fileNodeEntry) []Entity {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	return extractLineComments(path, lines, "#",
		[]string{"def ", "class ", "module "},
		fileNodes)
}

// extractJSDocComments scans a JS/TS file for `/** … */` blocks before
// function/class/const declarations.
func extractJSDocComments(path, service string, fileNodes map[string][]fileNodeEntry) []Entity {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return extractJSDocBlocks(path, string(data), fileNodes)
}

// extractLineComments is the generic scanner for single-line comment styles
// (Go `//`, Ruby `#`).  commentPrefix is the prefix; declKeywords are the
// tokens that make a line a declaration.  Emits one entity per consecutive
// comment block that immediately precedes a declaration.
func extractLineComments(
	path string,
	lines []string,
	commentPrefix string,
	declKeywords []string,
	fileNodes map[string][]fileNodeEntry,
) []Entity {
	var out []Entity
	var commentBuf []string
	commentStart := 0
	chunkN := 0

	flush := func(beforeLine int) {
		if len(commentBuf) == 0 {
			return
		}
		cleaned := stripCommentPrefixes(commentBuf, commentPrefix)
		if cleaned == "" {
			commentBuf = commentBuf[:0]
			return
		}
		id := fmt.Sprintf("doc:%s:%d", path, chunkN)
		h := sha256.Sum256([]byte(cleaned))
		chunkN++
		nodeID := nearestNodeAt(path, beforeLine, fileNodes)
		out = append(out, Entity{
			ID:          id,
			Type:        "doc",
			Text:        cleaned,
			ContentHash: hex.EncodeToString(h[:]),
			NodeID:      nodeID,
			File:        path,
			Line:        commentStart,
		})
		commentBuf = commentBuf[:0]
	}

	for i, raw := range lines {
		lineNo := i + 1
		trimmed := strings.TrimSpace(raw)

		if strings.HasPrefix(trimmed, commentPrefix) {
			if len(commentBuf) == 0 {
				commentStart = lineNo
			}
			commentBuf = append(commentBuf, trimmed)
			continue
		}

		isDecl := false
		for _, kw := range declKeywords {
			if strings.HasPrefix(trimmed, kw) {
				isDecl = true
				break
			}
		}
		if isDecl && len(commentBuf) > 0 {
			flush(lineNo)
		} else {
			// Inline comment not followed by a declaration — not a doc-comment.
			commentBuf = commentBuf[:0]
		}
	}
	return out
}

// stripCommentPrefixes removes the leading comment marker from each line and
// joins non-empty lines with spaces.
func stripCommentPrefixes(lines []string, prefix string) string {
	var parts []string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, prefix) {
			t = strings.TrimSpace(t[len(prefix):])
		}
		if t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}

// extractJSDocBlocks extracts /** ... */ comment blocks that precede
// JS/TS declaration keywords.
func extractJSDocBlocks(path, src string, fileNodes map[string][]fileNodeEntry) []Entity {
	var out []Entity
	chunkN := 0
	i := 0
	rs := []rune(src)
	n := len(rs)

	lineAt := func(pos int) int {
		line := 1
		for j := 0; j < pos && j < n; j++ {
			if rs[j] == '\n' {
				line++
			}
		}
		return line
	}

	for i < n {
		if i+2 < n && rs[i] == '/' && rs[i+1] == '*' && rs[i+2] == '*' {
			startLine := lineAt(i)
			i += 3
			var body []rune
			for i < n {
				if i+1 < n && rs[i] == '*' && rs[i+1] == '/' {
					i += 2
					break
				}
				body = append(body, rs[i])
				i++
			}
			cleaned := cleanJSDocComment(string(body))
			if cleaned == "" {
				continue
			}
			// Skip whitespace to the next token.
			j := i
			for j < n && (rs[j] == ' ' || rs[j] == '\t' || rs[j] == '\r' || rs[j] == '\n') {
				j++
			}
			declLine := lineAt(j)
			if isJSDecl(string(rs[j:])) {
				id := fmt.Sprintf("doc:%s:%d", path, chunkN)
				h := sha256.Sum256([]byte(cleaned))
				chunkN++
				nodeID := nearestNodeAt(path, declLine, fileNodes)
				out = append(out, Entity{
					ID:          id,
					Type:        "doc",
					Text:        cleaned,
					ContentHash: hex.EncodeToString(h[:]),
					NodeID:      nodeID,
					File:        path,
					Line:        startLine,
				})
			}
		} else {
			i++
		}
	}
	return out
}

// cleanJSDocComment strips leading `*` markers and `@param`-style tags,
// returning joined non-empty lines.
func cleanJSDocComment(raw string) string {
	var parts []string
	for _, line := range strings.Split(raw, "\n") {
		t := strings.TrimSpace(line)
		t = strings.TrimPrefix(t, "*")
		t = strings.TrimSpace(t)
		if t == "" || strings.HasPrefix(t, "@") {
			continue
		}
		parts = append(parts, t)
	}
	return strings.Join(parts, " ")
}

// isJSDecl reports whether src (from a non-blank position) starts with a
// JS/TS declaration keyword.
func isJSDecl(src string) bool {
	for _, kw := range []string{
		"function ", "async function ", "export function ", "export async function ",
		"class ", "export class ", "export default class ",
		"const ", "let ", "var ", "export const ", "export let ",
		"export default function",
	} {
		if strings.HasPrefix(src, kw) {
			return true
		}
	}
	return false
}
