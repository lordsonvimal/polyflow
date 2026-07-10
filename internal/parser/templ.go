package parser

import (
	"fmt"
	"regexp"
	"strings"

	templparser "github.com/a-h/templ/parser/v2"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// TemplParser parses .templ files using the a-h/templ/parser/v2 typed AST.
type TemplParser struct{}

func (p *TemplParser) Language() string     { return "templ" }
func (p *TemplParser) Extensions() []string { return []string{".templ"} }

// reDataOnAction matches data-on-<event>="@verb('/path')" attribute values.
var reDataOnAction = regexp.MustCompile(`^@(get|post|put|delete|patch)\s*\(\s*['"]([^'"]+)['"]`)

// reSignalRef matches $signalName used in data-text / data-indicator values.
var reSignalRef = regexp.MustCompile(`^\$([A-Za-z_]\w*)`)

// reOnEventAttr matches native DOM event attributes: onclick, oninput, …
// (data-on-* datastar actions are handled separately and never reach this).
var reOnEventAttr = regexp.MustCompile(`^on[a-z]+$`)

func (p *TemplParser) Parse(file, service string, _ *patterns.TreeSitterMatcher) ([]graph.Node, []graph.Edge, error) {
	tf, err := templparser.Parse(file)
	if err != nil {
		return nil, nil, err
	}

	v := &templVisitor{file: file, service: service}
	if err := tf.Visit(v); err != nil {
		return v.nodes, v.edges, err
	}
	return v.nodes, v.edges, nil
}

// templVisitor implements templparser.Visitor and accumulates nodes and edges.
type templVisitor struct {
	file             string
	service          string
	nodes            []graph.Node
	edges            []graph.Edge
	currentComponent string // node ID of the enclosing HTMLTemplate component
}

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
	v.currentComponent = nodeID
	for _, child := range t.Children {
		if err := child.Visit(v); err != nil {
			return err
		}
	}
	v.currentComponent = prev
	return nil
}

func (v *templVisitor) VisitElement(e *templparser.Element) error {
	lineNo := line(e.Range.From)
	for _, attr := range e.Attributes {
		if err := attr.Visit(v); err != nil {
			return err
		}
		_ = lineNo
	}
	for _, child := range e.Children {
		if err := child.Visit(v); err != nil {
			return err
		}
	}
	return nil
}

func (v *templVisitor) VisitConstantAttribute(ca *templparser.ConstantAttribute) error {
	key := ca.Key.String()
	val := ca.Value
	lineNo := line(ca.Range.From)

	switch {
	// data-on-<event>="@verb('/path')"
	case strings.HasPrefix(key, "data-on-"):
		if m := reDataOnAction.FindStringSubmatch(val); m != nil {
			method := strings.ToUpper(m[1])
			path := m[2]
			nodeID := templNodeID(v.service, v.file, lineNo, graph.NodeTypeHTTPClient, method+":"+path)
			v.nodes = append(v.nodes, graph.Node{
				ID: nodeID, Type: graph.NodeTypeHTTPClient,
				Label:    fmt.Sprintf("%s %s", method, path),
				Service:  v.service,
				File:     v.file,
				Line:     lineNo,
				Language: "templ",
				Meta:     map[string]string{"method": method, "path": path, "datastar": "true"},
			})
			v.edges = append(v.edges, componentEdge(v.currentComponent, nodeID, graph.EdgeTypeDatastarAction))
		}

	// data-bind / data-signals / data-model
	case key == "data-bind" || key == "data-signals" || key == "data-model":
		nodeID := templNodeID(v.service, v.file, lineNo, graph.NodeTypeComponent, "bind:"+val)
		v.nodes = append(v.nodes, graph.Node{
			ID: nodeID, Type: graph.NodeTypeComponent,
			Label:    val,
			Service:  v.service,
			File:     v.file,
			Line:     lineNo,
			Language: "templ",
			Meta:     map[string]string{"signal": val},
		})
		v.edges = append(v.edges, componentEdge(v.currentComponent, nodeID, graph.EdgeTypeDatastarBind))

	// data-text / data-indicator referencing $signal
	case key == "data-text" || key == "data-indicator":
		if m := reSignalRef.FindStringSubmatch(val); m != nil {
			nodeID := templNodeID(v.service, v.file, lineNo, graph.NodeTypeComponent, "read:"+val)
			v.nodes = append(v.nodes, graph.Node{
				ID: nodeID, Type: graph.NodeTypeComponent,
				Label:    val,
				Service:  v.service,
				File:     v.file,
				Line:     lineNo,
				Language: "templ",
				Meta:     map[string]string{"signal": m[1]},
			})
			v.edges = append(v.edges, componentEdge(v.currentComponent, nodeID, graph.EdgeTypeDatastarBind))
		}

	// href / action pointing to a server path — nav links, not API calls
	case key == "href" || key == "action":
		if strings.HasPrefix(val, "/") {
			nodeID := templNodeID(v.service, v.file, lineNo, graph.NodeTypeHTTPClient, "href:"+val)
			v.nodes = append(v.nodes, graph.Node{
				ID: nodeID, Type: graph.NodeTypeHTTPClient,
				Label:    val,
				Service:  v.service,
				File:     v.file,
				Line:     lineNo,
				Language: "templ",
				Meta:     map[string]string{"path": val, "method": "GET", "nav_link": "true"},
			})
			v.edges = append(v.edges, componentEdge(v.currentComponent, nodeID, graph.EdgeTypeNavigatesTo))
		}

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

// ExpressionAttribute covers data-on-click={ expr } style attributes.
func (v *templVisitor) VisitExpressionAttribute(ea *templparser.ExpressionAttribute) error {
	key := ea.Key.String()
	val := strings.TrimSpace(ea.Expression.Value)
	// Strip surrounding quotes if any
	if len(val) >= 2 {
		c := val[0]
		if (c == '"' || c == '\'') && val[len(val)-1] == c {
			val = val[1 : len(val)-1]
		}
	}
	lineNo := line(ea.Range.From)

	if strings.HasPrefix(key, "data-on-") {
		if m := reDataOnAction.FindStringSubmatch(val); m != nil {
			method := strings.ToUpper(m[1])
			path := m[2]
			nodeID := templNodeID(v.service, v.file, lineNo, graph.NodeTypeHTTPClient, method+":"+path)
			v.nodes = append(v.nodes, graph.Node{
				ID: nodeID, Type: graph.NodeTypeHTTPClient,
				Label:    fmt.Sprintf("%s %s", method, path),
				Service:  v.service,
				File:     v.file,
				Line:     lineNo,
				Language: "templ",
				Meta:     map[string]string{"method": method, "path": path, "datastar": "true"},
			})
			v.edges = append(v.edges, componentEdge(v.currentComponent, nodeID, graph.EdgeTypeDatastarAction))
		}
		return nil
	}

	// Native DOM event attributes with expression values: onclick={ handler }
	if reOnEventAttr.MatchString(key) {
		v.addEventAttr(key, val, lineNo)
	}
	return nil
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
func (v *templVisitor) VisitScriptElement(se *templparser.ScriptElement) error {
	return nil
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
func (v *templVisitor) VisitTemplElementExpression(*templparser.TemplElementExpression) error {
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
