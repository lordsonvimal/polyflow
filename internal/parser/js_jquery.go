package parser

import (
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Tier K.4 — jQuery listener attribution.
//
// The pattern matcher already mints a dom_target node for every jQuery event
// registration and LinkDOMDefinitions already resolves its selector to the ERB
// elements that declare the matching id/class. What was missing is the other
// half of the edge: the *handler*. 100 of nextGen's 107 jQuery listeners pass an
// anonymous function, which carries no node, so everything inside the handler —
// including the `$.ajax` call the click actually fires — attributed to the
// synthetic per-file `(module)` node. Every listener in a file shared one
// attribution hub, which is what made listener attribution useless:
//
//	before:  (module) --dom_listen--> dom_target(on)      [file-wide hub]
//	         (module) --calls-------> http_client(DELETE /…)
//
//	after:   element(i.delete-agent-message) --dom_listen--> click@i.delete-agent-message
//	                                                             └--calls--> http_client(DELETE /…)
//
// This file reads the call shape and mints the handler node; the selector→element
// join stays in LinkDOMDefinitions, which already owns the element index.

// jqShorthandEvents are the jQuery shorthand event methods — `$(sel).click(fn)`
// is `$(sel).on("click", fn)` with the event name in the method position.
// Kept in sync with patterns/javascript/jquery.yaml's dom_event_jquery_shorthand.
var jqShorthandEvents = map[string]bool{
	"click": true, "submit": true, "change": true, "keyup": true,
	"keydown": true, "focus": true, "blur": true, "input": true,
}

// jqDOMRoots are the receivers that are a real DOM scope rather than a selector
// nobody wrote down. `$(document).on(…)` without a delegated selector genuinely
// listens on the document; there is no element to look for, so it is skipped in
// silence instead of ledgered — the K.2 rule that a ledger entry means "tried and
// failed to resolve", not "nothing to resolve".
var jqDOMRoots = map[string]bool{"document": true, "window": true, "this": true, "body": true}

// jqListener is one resolved jQuery event registration, ready to be stamped onto
// the matcher's dom_target node for the same call.
type jqListener struct {
	line      int    // line of the `.on` / `.click` call — the matcher's node line
	event     string // "click", "ajax:success", …
	selector  string // the element selector to bind to, "" when there is none
	handlerID string // graph node ID of the handler function
	delegated bool
	root      string // receiver expression; the delegate root when delegated
	// Line span of an inline handler body, used to re-attribute the comm nodes
	// the pattern matcher parked on the file's (module) node.
	bodyStart, bodyEnd int
}

// handleJQueryListener reads a jQuery event registration and mints the node its
// handler needs.
//
// Three shapes, and the difference between the first two is the whole point of
// this phase: in the delegated form the element that is actually listened to is
// the *second* argument, not the receiver.
//
//	$(root).on("click", ".target", handler)   → binds ".target"
//	$(".target").on("click", handler)         → binds ".target"
//	$(".target").click(handler)               → binds ".target"
//
// An unreadable handler or a selector held in a variable is ledgered (#12).
func (ex *jsExtractor) handleJQueryListener(call *sitter.Node) {
	fn := call.ChildByFieldName("function")
	if fn == nil || fn.Type() != "member_expression" {
		return
	}
	prop := fn.ChildByFieldName("property")
	if prop == nil {
		return
	}
	method := prop.Content(ex.src)
	if method != "on" && !jqShorthandEvents[method] {
		return
	}
	recv, ok := jqReceiverArg(fn, ex.src)
	if !ok {
		return // not a `$(…)` / `jQuery(…)` receiver
	}
	args := call.ChildByFieldName("arguments")
	if args == nil || args.NamedChildCount() == 0 {
		return
	}

	line := tsLine(call)
	l := jqListener{line: line}
	var handler *sitter.Node

	if method == "on" {
		evt := args.NamedChild(0)
		if evt.Type() != "string" {
			return // dynamic event name — the call shape is not readable
		}
		l.event = stripJSQuote(evt.Content(ex.src))
		switch {
		case args.NamedChildCount() >= 3 && args.NamedChild(1).Type() == "string":
			// Delegated: the second argument is the real target.
			l.delegated = true
			l.selector = stripJSQuote(args.NamedChild(1).Content(ex.src))
			l.root = strings.TrimSpace(recv.Content(ex.src))
			handler = args.NamedChild(2)
		case args.NamedChildCount() >= 2:
			l.selector = ex.jqReceiverSelector(recv, line)
			handler = args.NamedChild(1)
		default:
			return
		}
	} else {
		l.event = method
		l.selector = ex.jqReceiverSelector(recv, line)
		handler = args.NamedChild(0)
	}
	if handler == nil {
		return
	}
	if l.root == "" {
		// Only used for labelling a handler whose receiver named no element
		// (`$(document)`, `$(rowSelector)`) — a bare `click@` would be unreadable.
		l.root = strings.TrimSpace(recv.Content(ex.src))
	}

	// The handler node. A bare identifier resolves to the function it names; an
	// inline function gets a synthetic node labelled after what it listens to,
	// so `search "click@.js-approve-ai-gen"` finds it and its body stops
	// attributing to (module). Anything else — `this.method`, `fn.bind(…)` — has
	// no same-file node to point at and is ledgered.
	switch {
	case handler.Type() == "identifier":
		ref := handler.Content(ex.src)
		fnLine, found := ex.resolveHandlerFn(ref)
		if !found {
			ex.ledgerDOMListen(line, ref)
			return
		}
		l.handlerID = ex.fnNodeID(ref, fnLine)
	case isFunctionNode(handler.Type()):
		name := jqHandlerLabel(l.event, l.selector, l.root)
		hLine := tsLine(handler)
		l.handlerID = ex.fnNodeID(name, hLine)
		l.bodyStart, l.bodyEnd = hLine, int(handler.EndPoint().Row)+1
		ex.addNode(graph.Node{
			ID: l.handlerID, Type: graph.NodeTypeFunction, Label: name,
			Service: ex.service, File: ex.file, Line: hLine, Language: ex.langTag,
			Meta: map[string]string{
				"scope": "handler", "handler": "jquery", "event": l.event,
			},
		})
		// Claim the handler body so walk attributes its calls here instead of
		// inheriting (module). walk visits this call_expression before it
		// descends into the handler, so the claim is always in place first.
		if ex.jqHandlers == nil {
			ex.jqHandlers = map[uint32]jsScope{}
		}
		ex.jqHandlers[handler.StartByte()] = jsScope{fnName: name, fnLine: hLine}
	default:
		ex.ledgerDOMListen(line, strings.TrimSpace(handler.Content(ex.src)))
		return
	}

	ex.jqListeners = append(ex.jqListeners, l)
}

// jqReceiverArg returns the single argument of a `$(…)` / `jQuery(…)` receiver.
func jqReceiverArg(member *sitter.Node, src []byte) (*sitter.Node, bool) {
	obj := member.ChildByFieldName("object")
	if obj == nil || obj.Type() != "call_expression" {
		return nil, false
	}
	callee := obj.ChildByFieldName("function")
	if callee == nil || callee.Type() != "identifier" {
		return nil, false
	}
	if name := callee.Content(src); name != "$" && name != "jQuery" {
		return nil, false
	}
	args := obj.ChildByFieldName("arguments")
	if args == nil || args.NamedChildCount() != 1 {
		return nil, false
	}
	return args.NamedChild(0), true
}

// jqReceiverSelector reads the selector out of a non-delegated `$(x)` receiver.
// A string literal is the selector. A DOM root (document/window) names no element
// and is skipped in silence. Anything else is a selector the source is holding in
// a variable — a clue this pass tried and failed to resolve, so it is ledgered.
func (ex *jsExtractor) jqReceiverSelector(recv *sitter.Node, line int) string {
	if recv.Type() == "string" {
		sel := stripJSQuote(recv.Content(ex.src))
		// A bare tag selector — `$("body")`, `$("table")` — names an element
		// *type*. The element index holds ids and classes only, so there is
		// nothing to look for; ledgering it would claim a failed resolution
		// where none was attempted (the K.2 `render json:` rule).
		if !strings.ContainsAny(sel, ".#[:") {
			return ""
		}
		return sel
	}
	expr := strings.TrimSpace(recv.Content(ex.src))
	if jqDOMRoots[expr] || isFunctionNode(recv.Type()) {
		return ""
	}
	ex.unresolved = append(ex.unresolved, graph.UnresolvedRef{
		Service: ex.service, File: ex.file, Line: line,
		Name: expr, Kind: "selector_dynamic",
	})
	return ""
}

// jqHandlerLabel names an inline handler after what it listens to. The selector
// is the useful half — `click@.js-approve-ai-gen` reads as the thing it is — and
// falls back to the delegate root when the receiver named no element.
func jqHandlerLabel(event, selector, root string) string {
	target := selector
	if target == "" {
		target = root
	}
	if target == "" {
		return event + "@"
	}
	return event + "@" + target
}

// stripJSQuote removes one matching pair of surrounding quotes from a literal.
func stripJSQuote(s string) string {
	if len(s) >= 2 {
		c := s[0]
		if (c == '"' || c == '\'' || c == '`') && s[len(s)-1] == c {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// stampJQueryHandlers writes each listener's handler node and target selector
// onto the pattern matcher's dom_target node for the same call, so
// LinkDOMDefinitions can emit `element --dom_listen--> handler` once it has
// resolved the selector against the element index.
//
// Matching is by (file, line): both passes read the same call expression, and the
// matcher's node ID pins the line. The matcher only captures a selector for the
// delegated form — its query cannot make the receiver's argument optional without
// dropping `$(document).on(evt, fn)` — so the receiver selector is filled in here.
// Deterministic: nodes are walked in slice order and the index is keyed by string.
func stampJQueryHandlers(nodes []graph.Node, listeners []jqListener) {
	if len(listeners) == 0 {
		return
	}
	byLine := make(map[string]*jqListener, len(listeners))
	for i := range listeners {
		// Two listeners on one line (`$(a).on(…).on(…)`) would collide; the
		// first wins rather than fanning a wrong handler onto both.
		key := fmt.Sprintf("%d\x00%s", listeners[i].line, listeners[i].event)
		if _, dup := byLine[key]; !dup {
			byLine[key] = &listeners[i]
		}
	}
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeDOMTarget || !strings.HasPrefix(n.Meta["pattern"], "dom_event_jquery") {
			continue
		}
		// The shorthand pattern carries the event in the method-name capture
		// (`fn`), not in `event` — `$(x).change(fn)` has no event argument to
		// capture. Reading only `event` silently dropped every shorthand
		// listener.
		event := n.Meta["event"]
		if event == "" {
			event = n.Meta["fn"]
		}
		l, ok := byLine[fmt.Sprintf("%d\x00%s", n.Line, event)]
		if !ok || l.handlerID == "" {
			continue
		}
		if n.Meta == nil {
			n.Meta = map[string]string{}
		}
		n.Meta["handler_node"] = l.handlerID
		if l.selector != "" && n.Meta["selector"] == "" {
			n.Meta["selector"] = l.selector
		}
		if l.delegated {
			n.Meta["delegated"] = "true"
			n.Meta["delegate_root"] = l.root
		}
	}
}

// reattributeJQueryHandlers moves the matcher's caller edges off the file's
// synthetic (module) node and onto the inline handler whose body actually
// contains the call.
//
// The matcher attributes a comm node to its enclosing *function node*, and an
// inline jQuery handler had none until this phase minted one — after
// MatchToGraph had already run. So every `$.ajax` inside every listener in a
// file hung off one (module) hub, which is the fan-out that made listener
// attribution useless: the element resolved, the endpoint resolved, and the path
// between them ran through a node shared with every other listener in the file.
// The Ruby pass hit the same seam and fixed it the same way
// (linkRubyEnclosingCalls).
//
// Only (module)-sourced edges are rewritten. A call inside a *named* function
// nested in a handler was already attributed correctly, and stealing it would
// lose a real frame. Deterministic: edges are walked in slice order and the
// innermost containing handler wins.
func reattributeJQueryHandlers(nodes []graph.Node, edges []graph.Edge, listeners []jqListener) []graph.Edge {
	if len(listeners) == 0 {
		return edges
	}
	regLine := map[int]bool{}
	for i := range listeners {
		regLine[listeners[i].line] = true
	}
	lineOf := make(map[string]int, len(nodes))
	ownHandler := map[string]string{}
	isRegistration := map[string]bool{}
	for i := range nodes {
		n := &nodes[i]
		lineOf[n.ID] = n.Line
		if h := n.Meta["handler_node"]; h != "" {
			ownHandler[n.ID] = h
		}
		// The receiver of a registration — the `$(".btn")` in
		// `$(".btn").on("click", fn)` — sits on the handler's first line but
		// evaluates outside it. Without this it would attribute to the handler
		// it is registering, which reads as the handler querying itself.
		if n.Type == graph.NodeTypeDOMTarget && n.Meta["pattern"] == "jquery_selector" && regLine[n.Line] {
			isRegistration[n.ID] = true
		}
	}
	owner := func(line int) string {
		best := -1
		for i := range listeners {
			l := &listeners[i]
			if l.bodyStart == 0 || line < l.bodyStart || line > l.bodyEnd {
				continue
			}
			if best == -1 || l.bodyStart > listeners[best].bodyStart {
				best = i
			}
		}
		if best == -1 {
			return ""
		}
		return listeners[best].handlerID
	}

	for i := range edges {
		e := &edges[i]
		if !strings.HasSuffix(e.From, ":function:(module):0") {
			continue
		}
		line, ok := lineOf[e.To]
		if !ok {
			continue
		}
		handler := owner(line)
		// A registration site sits on the first line of its own handler body, so
		// the naive span test would make each handler listen to itself. Skip
		// exactly that pairing — a *nested* registration inside another handler
		// still re-attributes, which is the answer we want.
		if handler == "" || handler == e.To || ownHandler[e.To] == handler || isRegistration[e.To] {
			continue
		}
		e.From = handler
		e.ID = fmt.Sprintf("%s:%s->%s", string(e.Type), e.From, e.To)
	}
	return edges
}
