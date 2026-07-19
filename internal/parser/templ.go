package parser

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	templparser "github.com/a-h/templ/parser/v2"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/lordsonvimal/polyflow/internal/toolchain"
)

// TemplParser parses .templ files using the a-h/templ/parser/v2 typed AST.
type TemplParser struct{}

func (p *TemplParser) Language() string     { return "templ" }
func (p *TemplParser) Extensions() []string { return []string{".templ"} }

// reDatastarVerb finds a datastar backend action `@verb(` anywhere in an
// attribute value. Datastar v1 values are Go expressions, so the action may be
// wrapped (`templ.JSExpression("@post(…)")`) or prefixed with signal writes
// (`"$sig = 0; @post(…)"`) — the match is deliberately unanchored.
var reDatastarVerb = regexp.MustCompile(`(?i)@(get|post|put|delete|patch)\s*\(`)

// reSignalRef matches $signalName used in data-text / data-indicator values.
var reSignalRef = regexp.MustCompile(`^\$([A-Za-z_]\w*)`)

// reReactiveSignal finds every $signalName referenced anywhere in a reactive
// datastar attribute value (data-show, data-class, data-attr:*, data-when,
// data-computed). Dynamic segments (`"$" + sig`) yield no bare name and are
// simply skipped — the readable signals still surface.
var reReactiveSignal = regexp.MustCompile(`\$([A-Za-z_]\w*)`)

// reOnEventAttr matches native DOM event attributes: onclick, oninput, …
// (data-on-* datastar actions are handled separately and never reach this).
var reOnEventAttr = regexp.MustCompile(`^on[a-z]+$`)

func (p *TemplParser) Parse(file, service string, matcher *patterns.TreeSitterMatcher) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef, error) {
	content, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, nil, err
	}
	variant := ""
	if matcher != nil {
		variant = matcher.DatastarVariant
	}
	return p.ParseSource(file, service, content, variant)
}

// ParseSource parses templ source that has already been read (or received
// over the sidecar IPC, which ships content rather than a path).
// templparser.Parse(file) is exactly os.ReadFile + ParseString, so this path
// is byte-equivalent to Parse for on-disk files.
func (p *TemplParser) ParseSource(file, service string, content []byte, datastarVariant string) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef, error) {
	tf, err := templparser.ParseString(string(content))
	if err != nil {
		return nil, nil, nil, err
	}

	v := &templVisitor{
		file:                file,
		service:             service,
		currentComponentIdx: -1,
		vocab:               toolchain.DefaultDatastarVocab(datastarVariant),
	}
	if err := tf.Visit(v); err != nil {
		return v.nodes, v.edges, nil, err
	}
	return v.nodes, v.edges, nil, nil
}

// templVisitor implements templparser.Visitor and accumulates nodes and edges.
type templVisitor struct {
	file                string
	service             string
	nodes               []graph.Node
	edges               []graph.Edge
	currentComponent    string // node ID of the enclosing HTMLTemplate component
	currentComponentIdx int    // index of the enclosing component in v.nodes, -1 outside one
	formMethod          string // method attr of the enclosing <form> (upper-case), "" outside forms
	vocab               toolchain.DatastarVocab // version-selected attribute vocabulary
}

// reFirstStringLit matches the first double-quoted string literal in a Go
// expression, used to pull the asset path out of `helpers.Asset("js/x.js")`.
var reFirstStringLit = regexp.MustCompile(`"([^"]+)"`)

// line converts the 0-indexed templ Line to 1-indexed.
func line(p templparser.Position) int {
	return int(p.Line) + 1
}

func (v *templVisitor) VisitTemplateFile(tf *templparser.TemplateFile) error {
	for _, n := range tf.Nodes {
		if err := n.Visit(v); err != nil {
			return err
		}
	}
	return nil
}

func (v *templVisitor) VisitHTMLTemplate(t *templparser.HTMLTemplate) error {
	// Extract component name: Expression.Value is e.g. "UserPage(users []User)"
	name := componentName(t.Expression.Value)
	if name == "" {
		name = t.Expression.Value
	}
	lineNo := line(t.Range.From)
	nodeID := templNodeID(v.service, v.file, lineNo, graph.NodeTypeComponent, name)
	v.nodes = append(v.nodes, graph.Node{
		ID:       nodeID,
		Type:     graph.NodeTypeComponent,
		Label:    name,
		Service:  v.service,
		File:     v.file,
		Line:     lineNo,
		Language: "templ",
		Meta:     map[string]string{"name": name},
	})

	prev := v.currentComponent
	prevIdx := v.currentComponentIdx
	v.currentComponent = nodeID
	v.currentComponentIdx = len(v.nodes) - 1
	for _, child := range t.Children {
		if err := child.Visit(v); err != nil {
			return err
		}
	}
	v.currentComponent = prev
	v.currentComponentIdx = prevIdx
	return nil
}

// addComponentMeta appends a value to a newline-separated list stored on the
// enclosing component node's meta. Used to record script `src` assets and DOM
// `id=` definitions the component carries, which cross-file linker passes
// (LinkTemplScripts, LinkDOMDefinitions) resolve after all nodes are collected.
func (v *templVisitor) addComponentMeta(key, val string) {
	if v.currentComponentIdx < 0 || val == "" {
		return
	}
	m := v.nodes[v.currentComponentIdx].Meta
	if m == nil {
		m = map[string]string{}
		v.nodes[v.currentComponentIdx].Meta = m
	}
	if existing := m[key]; existing != "" {
		m[key] = existing + "\n" + val
	} else {
		m[key] = val
	}
}

func (v *templVisitor) VisitElement(e *templparser.Element) error {
	// Forms carry their submit verb on a sibling attribute; anchors may spoof
	// one via data-method (Rails UJS style). Record it before visiting the
	// attributes so the action/href handler can stamp the right method.
	prevMethod := v.formMethod
	v.formMethod = elementMethod(e)
	for _, attr := range e.Attributes {
		if err := attr.Visit(v); err != nil {
			return err
		}
	}
	// Reset before descending: the verb belongs to this element's own
	// action/href, not to links nested inside the form.
	v.formMethod = prevMethod
	for _, child := range e.Children {
		if err := child.Visit(v); err != nil {
			return err
		}
	}
	return nil
}

// elementMethod extracts the HTTP verb from a <form method="…"> or an
// <a data-method="…"> attribute, upper-cased; "" when absent.
func elementMethod(e *templparser.Element) string {
	for _, attr := range e.Attributes {
		ca, ok := attr.(*templparser.ConstantAttribute)
		if !ok {
			continue
		}
		key := strings.ToLower(ca.Key.String())
		if key == "method" || key == "data-method" {
			return strings.ToUpper(strings.TrimSpace(ca.Value))
		}
	}
	return ""
}

func (v *templVisitor) VisitConstantAttribute(ca *templparser.ConstantAttribute) error {
	key := ca.Key.String()
	val := ca.Value
	lineNo := line(ca.Range.From)

	switch {
	// data-on:<event>={ @verb('/path') } / data-on-<event>="@verb('/path')"
	case v.isDataOnKey(key):
		v.addDatastarAction(val, lineNo)

	// data-bind / data-signals / data-model
	case key == "data-bind" || key == "data-signals" || key == "data-model":
		v.addSignalBind("bind:"+val, val, signalName(val), lineNo)

	// data-text / data-indicator referencing $signal
	case key == "data-text" || key == "data-indicator":
		if m := reSignalRef.FindStringSubmatch(val); m != nil {
			v.addSignalBind("read:"+m[1], m[1], m[1], lineNo)
		}

	// Reactive attrs (data-show, data-class, data-attr:*, data-when,
	// data-computed) read one or more signals to drive rendering.
	case v.isReactiveAttrKey(key):
		v.addSignalReads(val, lineNo)

	// href / action pointing to a server path — nav links, not API calls
	case key == "href" || key == "action":
		if strings.HasPrefix(val, "/") {
			method := "GET"
			label := val
			if v.formMethod != "" {
				method = v.formMethod
				label = method + " " + val
			}
			nodeID := templNodeID(v.service, v.file, lineNo, graph.NodeTypeHTTPClient, "href:"+val)
			v.nodes = append(v.nodes, graph.Node{
				ID: nodeID, Type: graph.NodeTypeHTTPClient,
				Label:    label,
				Service:  v.service,
				File:     v.file,
				Line:     lineNo,
				Language: "templ",
				Meta:     map[string]string{"path": val, "method": method, "nav_link": "true"},
			})
			v.edges = append(v.edges, componentEdge(v.currentComponent, nodeID, graph.EdgeTypeNavigatesTo))
		}

	// id="foo" — a DOM definition a JS querySelector/getElementById may target.
	// Recorded on the enclosing component so LinkDOMDefinitions can resolve the
	// JS→templ `defined_in` seam. Stored as "id@line" so the linker can point the
	// edge at the exact element.
	case strings.ToLower(key) == "id" && val != "":
		v.addComponentMeta("dom_ids", fmt.Sprintf("%s@%d", val, lineNo))

	// Native DOM event attributes: onclick="save()" etc.
	case reOnEventAttr.MatchString(key):
		v.addEventAttr(key, val, lineNo)
	}
	return nil
}

// addEventAttr emits a dom_target node for a native on<event> attribute and
// a dom_listen edge from the enclosing component.
func (v *templVisitor) addEventAttr(key, val string, lineNo int) {
	nodeID := templNodeID(v.service, v.file, lineNo, graph.NodeTypeDOMTarget, key+":"+val)
	v.nodes = append(v.nodes, graph.Node{
		ID: nodeID, Type: graph.NodeTypeDOMTarget,
		Label:    key + " handler",
		Service:  v.service,
		File:     v.file,
		Line:     lineNo,
		Language: "templ",
		Meta:     map[string]string{"prop": key, "event_type": key[2:], "handler": val, "pattern": "dom_event_attr"},
	})
	v.edges = append(v.edges, componentEdge(v.currentComponent, nodeID, graph.EdgeTypeDOMListen))
}

// ExpressionAttribute covers data-on:click={ expr } style attributes. The
// datastar colon form (`data-on:click={…}`) surfaces here; the value is a Go
// expression, not a bare string, so we hand the raw expression to
// addDatastarAction rather than pre-stripping quotes.
func (v *templVisitor) VisitExpressionAttribute(ea *templparser.ExpressionAttribute) error {
	key := ea.Key.String()
	raw := strings.TrimSpace(ea.Expression.Value)
	lineNo := line(ea.Range.From)

	if v.isDataOnKey(key) {
		v.addDatastarAction(raw, lineNo)
		return nil
	}

	// Reactive attrs with expression values: data-show={ "$" + sig }, etc.
	if v.isReactiveAttrKey(key) {
		v.addSignalReads(raw, lineNo)
		return nil
	}

	// Native DOM event attributes with expression values: onclick={ handler }
	if reOnEventAttr.MatchString(key) {
		v.addEventAttr(key, stripQuotes(raw), lineNo)
	}
	return nil
}

// isReactiveAttrKey reports whether an attribute is a datastar reactive binding
// that reads signals to drive rendering (as opposed to data-on:* actions or
// data-bind writes). Uses the version-selected vocabulary stored on the visitor.
func (v *templVisitor) isReactiveAttrKey(key string) bool {
	return v.vocab.IsReactiveAttrKey(key)
}

// addSignalReads emits a signal node + `reads` edge from the enclosing component
// for every distinct $signal referenced in a reactive attribute value. Dynamic
// signal names (`"$" + sig`) contribute no bare name and are skipped.
func (v *templVisitor) addSignalReads(val string, lineNo int) {
	seen := map[string]bool{}
	for _, m := range reReactiveSignal.FindAllStringSubmatch(val, -1) {
		sig := m[1]
		if seen[sig] {
			continue
		}
		seen[sig] = true
		nodeID := templNodeID(v.service, v.file, lineNo, graph.NodeTypeSignal, "read:"+sig)
		v.nodes = append(v.nodes, graph.Node{
			ID: nodeID, Type: graph.NodeTypeSignal,
			Label:    sig,
			Service:  v.service,
			File:     v.file,
			Line:     lineNo,
			Language: "templ",
			Meta:     map[string]string{"signal": sig},
		})
		v.edges = append(v.edges, componentEdge(v.currentComponent, nodeID, graph.EdgeTypeReads))
	}
}

// isDataOnKey reports whether an attribute key is a datastar event binding.
// Uses the version-selected vocabulary stored on the visitor (v0: hyphen only,
// v1: colon only, combined fallback: both).
func (v *templVisitor) isDataOnKey(key string) bool {
	return v.vocab.IsDataOnKey(key)
}

// stripQuotes removes a single matching pair of surrounding quotes.
func stripQuotes(s string) string {
	if len(s) >= 2 {
		c := s[0]
		if (c == '"' || c == '\'') && s[len(s)-1] == c {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// signalName strips a leading `$` from a signal reference, leaving the bare
// name where possible (falls back to the raw value for compound expressions).
func signalName(val string) string {
	if m := reSignalRef.FindStringSubmatch(val); m != nil {
		return m[1]
	}
	return strings.TrimPrefix(val, "$")
}

// addSignalBind emits a signal node (datastar reactive binding) and a
// datastar_bind edge from the enclosing component.
func (v *templVisitor) addSignalBind(idKey, label, signal string, lineNo int) {
	nodeID := templNodeID(v.service, v.file, lineNo, graph.NodeTypeSignal, idKey)
	v.nodes = append(v.nodes, graph.Node{
		ID: nodeID, Type: graph.NodeTypeSignal,
		Label:    label,
		Service:  v.service,
		File:     v.file,
		Line:     lineNo,
		Language: "templ",
		Meta:     map[string]string{"signal": signal},
	})
	v.edges = append(v.edges, componentEdge(v.currentComponent, nodeID, graph.EdgeTypeDatastarBind))
}

// addDatastarAction parses a datastar backend action (`@verb('/path')`) out of
// an attribute value and, when found, emits an http_client node plus a
// datastar_action edge from the enclosing component. Concatenated paths are
// partially resolved (interpolated segments → `*`, confidence: partial).
func (v *templVisitor) addDatastarAction(val string, lineNo int) bool {
	method, path, partial, ok := extractDatastarAction(val)
	if !ok {
		return false
	}
	conf := graph.ConfidenceStatic
	if partial {
		conf = graph.ConfidencePartial
	}
	nodeID := templNodeID(v.service, v.file, lineNo, graph.NodeTypeHTTPClient, method+":"+path)
	v.nodes = append(v.nodes, graph.Node{
		ID: nodeID, Type: graph.NodeTypeHTTPClient,
		Label:    fmt.Sprintf("%s %s", method, path),
		Service:  v.service,
		File:     v.file,
		Line:     lineNo,
		Language: "templ",
		Meta:     map[string]string{"method": method, "path": path, "datastar": "true", "confidence": conf},
	})
	e := componentEdge(v.currentComponent, nodeID, graph.EdgeTypeDatastarAction)
	e.Confidence = conf
	v.edges = append(v.edges, e)
	return true
}

// extractDatastarAction pulls the HTTP method and (possibly wildcarded) path
// out of a datastar action value. It reconstructs the runtime JS string from
// concatenated Go string literals (interpolated gaps become a sentinel), then
// scans for `@verb('…')` and normalizes the path. `partial` is true when any
// segment was interpolated and could not be resolved to a literal.
func extractDatastarAction(val string) (method, path string, partial, ok bool) {
	js := val
	if strings.Contains(val, `"`) {
		js = reconstructGoString(val)
	}
	m := reDatastarVerb.FindStringSubmatchIndex(js)
	if m == nil {
		return "", "", false, false
	}
	method = strings.ToUpper(js[m[2]:m[3]])
	path, partial = normalizeDatastarPath(js[m[1]:])
	if path == "" {
		return "", "", false, false
	}
	return method, path, partial, true
}

// gapSentinel marks a Go-interpolation boundary in a reconstructed JS string;
// the path normalizer turns it into a `*` wildcard.
const gapSentinel = 0

// reconstructGoString joins the contents of concatenated Go double-quoted
// string literals, inserting a sentinel byte where interpolated expressions sat
// between them. Non-string text (identifiers, `templ.JSExpression(`, `+`) is
// dropped. e.g. `templ.JSExpression("@post('/p/" + id + "/x')")` →
// `@post('/p/<sentinel>/x')`.
func reconstructGoString(val string) string {
	var b strings.Builder
	inStr := false
	prevLiteral := false
	for i := 0; i < len(val); i++ {
		c := val[i]
		if inStr {
			if c == '\\' && i+1 < len(val) {
				b.WriteByte(val[i+1])
				i++
				continue
			}
			if c == '"' {
				inStr = false
				prevLiteral = true
				continue
			}
			b.WriteByte(c)
			continue
		}
		if c == '"' {
			if prevLiteral {
				b.WriteByte(gapSentinel)
			}
			inStr = true
		}
	}
	return b.String()
}

// isPathChar reports whether c can appear literally in a URL path segment.
func isPathChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	switch c {
	case '/', '-', '_', '.', ':', '%', '~', '*', '?', '=', '&':
		return true
	}
	return false
}

// normalizeDatastarPath reads the first quoted argument of a `@verb(` call
// (rest is the text just after the `(`), collapsing interpolated / dynamic
// segments to a single `*`. Returns the path and whether anything was
// wildcarded. A bare (unquoted) argument is treated as fully dynamic (`*`).
func normalizeDatastarPath(rest string) (string, bool) {
	i := 0
	for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t') {
		i++
	}
	var openQuote byte
	if i < len(rest) && (rest[i] == '\'' || rest[i] == '"' || rest[i] == '`') {
		openQuote = rest[i]
		i++
	} else {
		// No string literal: a variable/expression argument — fully dynamic.
		return "*", true
	}

	var b strings.Builder
	partial := false
	lastStar := false
	inStr := true // opening quote already consumed
	wildcard := func() {
		partial = true
		if !lastStar {
			b.WriteByte('*')
			lastStar = true
		}
	}
	for i < len(rest) {
		c := rest[i]
		if c == openQuote {
			// Inside the string, a closing quote followed by `)` (or end)
			// terminates the path; otherwise it is a JS-level concat boundary
			// that flips us out of the string into an interpolated expression.
			if inStr {
				j := i + 1
				for j < len(rest) && (rest[j] == ' ' || rest[j] == '\t') {
					j++
				}
				if j >= len(rest) || rest[j] == ')' {
					break
				}
				inStr = false
				wildcard()
			} else {
				inStr = true // reopening the string literal
			}
			i++
			continue
		}
		if !inStr {
			// Inside an interpolated JS expression — already wildcarded.
			i++
			continue
		}
		if c != gapSentinel && isPathChar(c) {
			b.WriteByte(c)
			lastStar = c == '*'
			i++
			continue
		}
		// Go-interpolation sentinel or a non-path char inside the literal.
		wildcard()
		i++
	}
	return b.String(), partial
}

// componentName extracts the bare identifier from a templ expression like "UserPage(users []User)".
func componentName(expr string) string {
	if i := strings.IndexAny(expr, "( \t"); i > 0 {
		return expr[:i]
	}
	return expr
}

// No-op Visit methods required to satisfy the Visitor interface.

func (v *templVisitor) VisitTemplateFileGoExpression(e *templparser.TemplateFileGoExpression) error {
	return nil
}
func (v *templVisitor) VisitPackage(*templparser.Package) error         { return nil }
func (v *templVisitor) VisitWhitespace(*templparser.Whitespace) error   { return nil }
func (v *templVisitor) VisitCSSTemplate(*templparser.CSSTemplate) error { return nil }
func (v *templVisitor) VisitConstantCSSProperty(*templparser.ConstantCSSProperty) error {
	return nil
}
func (v *templVisitor) VisitExpressionCSSProperty(*templparser.ExpressionCSSProperty) error {
	return nil
}
func (v *templVisitor) VisitDocType(*templparser.DocType) error   { return nil }
func (v *templVisitor) VisitText(*templparser.Text) error         { return nil }
// VisitScriptElement records the JS asset a <script src=…> loads so the
// LinkTemplScripts pass can draw an `imports` edge from the templ component to
// the JS file node. The src is a Go expression (`helpers.Asset("js/x.js")`) or
// a literal; we pull the logical asset path out and stash it on the enclosing
// component. Inline scripts (no src) carry no cross-file link and are skipped.
func (v *templVisitor) VisitScriptElement(se *templparser.ScriptElement) error {
	if src := scriptSrc(se.Attributes); src != "" {
		v.addComponentMeta("script_srcs", src)
	}
	return nil
}

// scriptSrc extracts the logical asset path from a <script>'s src attribute.
// Handles both the constant form (`src="/static/js/x.js"`) and the expression
// form (`src={ helpers.Asset("js/x.js") }`), returning the first string literal
// of the expression. Returns "" when there is no src or no resolvable literal.
func scriptSrc(attrs []templparser.Attribute) string {
	for _, attr := range attrs {
		switch a := attr.(type) {
		case *templparser.ConstantAttribute:
			if strings.ToLower(a.Key.String()) == "src" {
				return strings.TrimSpace(a.Value)
			}
		case *templparser.ExpressionAttribute:
			if strings.ToLower(a.Key.String()) == "src" {
				if m := reFirstStringLit.FindStringSubmatch(a.Expression.Value); m != nil {
					return m[1]
				}
			}
		}
	}
	return ""
}
func (v *templVisitor) VisitRawElement(re *templparser.RawElement) error { return nil }
func (v *templVisitor) VisitBoolConstantAttribute(*templparser.BoolConstantAttribute) error {
	return nil
}
func (v *templVisitor) VisitBoolExpressionAttribute(*templparser.BoolExpressionAttribute) error {
	return nil
}
func (v *templVisitor) VisitSpreadAttributes(*templparser.SpreadAttributes) error { return nil }
func (v *templVisitor) VisitConditionalAttribute(*templparser.ConditionalAttribute) error {
	return nil
}
func (v *templVisitor) VisitGoComment(*templparser.GoComment) error   { return nil }
func (v *templVisitor) VisitHTMLComment(*templparser.HTMLComment) error { return nil }
func (v *templVisitor) VisitCallTemplateExpression(*templparser.CallTemplateExpression) error {
	return nil
}
// VisitTemplElementExpression descends into the children of a component-call
// block (`@Layout(...) { ...children... }`) so datastar actions and DOM targets
// nested inside layout wrappers are not dropped. (Composition/renders edges for
// the call itself are handled in a later phase.)
func (v *templVisitor) VisitTemplElementExpression(te *templparser.TemplElementExpression) error {
	for _, child := range te.Children {
		if err := child.Visit(v); err != nil {
			return err
		}
	}
	return nil
}
func (v *templVisitor) VisitChildrenExpression(*templparser.ChildrenExpression) error { return nil }
func (v *templVisitor) VisitIfExpression(n *templparser.IfExpression) error {
	for _, child := range n.Then {
		if err := child.Visit(v); err != nil {
			return err
		}
	}
	for _, child := range n.Else {
		if err := child.Visit(v); err != nil {
			return err
		}
	}
	return nil
}
func (v *templVisitor) VisitSwitchExpression(se *templparser.SwitchExpression) error {
	for _, c := range se.Cases {
		for _, child := range c.Children {
			if err := child.Visit(v); err != nil {
				return err
			}
		}
	}
	return nil
}
func (v *templVisitor) VisitForExpression(fe *templparser.ForExpression) error {
	for _, child := range fe.Children {
		if err := child.Visit(v); err != nil {
			return err
		}
	}
	return nil
}
func (v *templVisitor) VisitGoCode(*templparser.GoCode) error             { return nil }
func (v *templVisitor) VisitStringExpression(*templparser.StringExpression) error { return nil }
func (v *templVisitor) VisitScriptTemplate(*templparser.ScriptTemplate) error     { return nil }
func (v *templVisitor) VisitFallthrough(*templparser.Fallthrough) error           { return nil }

// templNodeID builds a deterministic node ID aligned with the design doc format:
// service:file:type:name:line
func templNodeID(service, file string, line int, nodeType graph.NodeType, name string) string {
	return fmt.Sprintf("%s:%s:%s:%s:%d", service, file, string(nodeType), name, line)
}

func selfEdge(nodeID string, edgeType graph.EdgeType) graph.Edge {
	return graph.Edge{
		ID:    nodeID + ":edge",
		From:  nodeID,
		To:    nodeID,
		Type:  edgeType,
		Label: string(edgeType),
	}
}

// componentEdge returns an edge from a component to an attribute node.
// Falls back to a self-loop when no enclosing component has been seen yet.
func componentEdge(fromID, toID string, edgeType graph.EdgeType) graph.Edge {
	if fromID == "" {
		return selfEdge(toID, edgeType)
	}
	return graph.Edge{
		ID:    fmt.Sprintf("%s:%s->%s", string(edgeType), fromID, toID),
		From:  fromID,
		To:    toID,
		Type:  edgeType,
		Label: string(edgeType),
	}
}

func init() {
	Register(&TemplParser{})
}
