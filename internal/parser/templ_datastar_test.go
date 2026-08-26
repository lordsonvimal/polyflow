package parser

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// End-to-end through Parse: colon-syntax data-on:click actions produce datastar
// http_client nodes; signal bindings become signal nodes (not component junk);
// signal-only handlers produce nothing.
func TestTemplParser_DatastarActions(t *testing.T) {
	t.Parallel()
	p := &TemplParser{}
	nodes, edges, _, err := p.Parse("testdata/datastar.templ", "app", nil, nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	actions := map[string]graph.Node{} // path -> node
	var signalNodes, componentNodes int
	for _, n := range nodes {
		switch n.Type {
		case graph.NodeTypeHTTPClient:
			if n.Meta["datastar"] == "true" {
				actions[n.Meta["path"]] = n
			}
		case graph.NodeTypeSignal:
			signalNodes++
		case graph.NodeTypeComponent:
			componentNodes++
		}
	}

	// Three @verb actions, one static + two partial.
	if len(actions) != 3 {
		t.Fatalf("datastar actions = %d, want 3: %v", len(actions), keys(actions))
	}
	if n, ok := actions["/play/*/draw"]; !ok {
		t.Errorf("missing /play/*/draw action; got %v", keys(actions))
	} else if n.Meta["method"] != "POST" || n.Meta["confidence"] != graph.ConfidencePartial {
		t.Errorf("/play/*/draw: method=%q confidence=%q", n.Meta["method"], n.Meta["confidence"])
	}
	if n, ok := actions["/play/static/resign"]; !ok {
		t.Errorf("missing static action")
	} else if n.Meta["confidence"] != graph.ConfidenceStatic {
		t.Errorf("static action confidence = %q, want static", n.Meta["confidence"])
	}
	if _, ok := actions["/rows/*"]; !ok {
		t.Errorf("missing /rows/* action; got %v", keys(actions))
	}

	// data-text/data-bind become signal nodes, not component junk.
	if signalNodes != 2 {
		t.Errorf("signal nodes = %d, want 2", signalNodes)
	}
	if componentNodes != 1 { // only the ActionPanel component itself
		t.Errorf("component nodes = %d, want 1 (no $idx+1 junk)", componentNodes)
	}

	// Every datastar action has a datastar_action edge with matching confidence.
	var actionEdges int
	for _, e := range edges {
		if e.Type == graph.EdgeTypeDatastarAction {
			actionEdges++
			if e.Confidence == "" {
				t.Errorf("datastar_action edge %s has no confidence", e.ID)
			}
		}
	}
	if actionEdges != 3 {
		t.Errorf("datastar_action edges = %d, want 3", actionEdges)
	}
}

// TestTemplParser_DatastarClientHandler proves data-on:<event> attributes
// whose value is not a backend @verb(...) action fall back to the same
// dom_target/handler shape a native onclick= attribute gets (so
// LinkJSGlobals resolves window.maple.X the same way for both), while a pure
// signal mutation still produces nothing — the pre-existing behavior for
// that case must not regress into a phantom unresolved dom_target.
func TestTemplParser_DatastarClientHandler(t *testing.T) {
	t.Parallel()
	p := &TemplParser{}
	nodes, edges, _, err := p.Parse("testdata/datastar.templ", "app", nil, nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	var targets []graph.Node
	for _, n := range nodes {
		if n.Type == graph.NodeTypeDOMTarget {
			targets = append(targets, n)
		}
	}
	if len(targets) != 1 {
		t.Fatalf("dom_target nodes = %d, want 1 (signal-only handler must produce none): %+v", len(targets), targets)
	}
	got := targets[0]
	if got.Meta["handler"] != "window.maple.syncAllBaseImages()" {
		t.Errorf("handler = %q, want the plain JS call", got.Meta["handler"])
	}
	if got.Meta["event_type"] != "click" {
		t.Errorf("event_type = %q, want %q", got.Meta["event_type"], "click")
	}
	if got.Meta["pattern"] != "dom_event_attr" {
		t.Errorf("pattern = %q, want dom_event_attr (must match LinkJSGlobals' scoping check)", got.Meta["pattern"])
	}

	var listenEdges int
	for _, e := range edges {
		if e.Type == graph.EdgeTypeDOMListen && e.To == got.ID {
			listenEdges++
		}
	}
	if listenEdges != 1 {
		t.Errorf("dom_listen edges to %s = %d, want 1", got.ID, listenEdges)
	}
}

func TestDatastarEventType(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"data-on:click":  "click",
		"data-on-click":  "click",
		"data-on:change": "change",
		"data-init":      "data-init",
	}
	for key, want := range cases {
		if got := datastarEventType(key); got != want {
			t.Errorf("datastarEventType(%q) = %q, want %q", key, got, want)
		}
	}
}

func keys(m map[string]graph.Node) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// extractDatastarAction handles colon syntax, JSExpression wrappers, signal
// prefixes, and concatenated (interpolated) paths — the Datastar v1 shapes
// found in chessleap.
func TestExtractDatastarAction(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		val        string
		wantMethod string
		wantPath   string
		wantPart   bool
		wantOK     bool
	}{
		{
			name:       "jsexpression wrapper with interpolated segment",
			val:        `templ.JSExpression("@post('/play/" + gameID + "/draw')")`,
			wantMethod: "POST", wantPath: "/play/*/draw", wantPart: true, wantOK: true,
		},
		{
			name:       "signal write prefix then get",
			val:        `"$page = " + strconv.Itoa(page) + "; @get('/rows/" + gameID + "')"`,
			wantMethod: "GET", wantPath: "/rows/*", wantPart: true, wantOK: true,
		},
		{
			name:       "fully static path is confidence static",
			val:        `"@post('/play/static/resign')"`,
			wantMethod: "POST", wantPath: "/play/static/resign", wantPart: false, wantOK: true,
		},
		{
			name:       "multiple interpolations collapse",
			val:        `"@post('/practice/" + sessionID + "/node/' + $nodeID + '/glyph/" + code + "')"`,
			wantMethod: "POST", wantPath: "/practice/*/node/*/glyph/*", wantPart: true, wantOK: true,
		},
		{
			name:       "query string preserved",
			val:        `templ.JSExpression("@post('" + prefix + "/history/navigate?direction=-1')")`,
			wantMethod: "POST", wantPath: "*/history/navigate?direction=-1", wantPart: true, wantOK: true,
		},
		{
			// The options object is the second argument and must not bleed into
			// the path (it produced `/app-configs/save*form*`).
			name:       "options object second arg does not bleed into path",
			val:        `"@post('/app-configs/save', {contentType: 'form'})"`,
			wantMethod: "POST", wantPath: "/app-configs/save", wantPart: false, wantOK: true,
		},
		{
			name:       "interpolated path with options second arg",
			val:        `"@post('/apps/" + appID + "/save', {contentType: 'form'})"`,
			wantMethod: "POST", wantPath: "/apps/*/save", wantPart: true, wantOK: true,
		},
		{
			name:   "signal-only handler has no action",
			val:    `"$flipped = !$flipped"`,
			wantOK: false,
		},
		{
			name:   "plain dom handler has no action",
			val:    `saveForm()`,
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			method, path, partial, ok := extractDatastarAction(tc.val)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (method=%q path=%q)", ok, tc.wantOK, method, path)
			}
			if !ok {
				return
			}
			if method != tc.wantMethod {
				t.Errorf("method = %q, want %q", method, tc.wantMethod)
			}
			if path != tc.wantPath {
				t.Errorf("path = %q, want %q", path, tc.wantPath)
			}
			if partial != tc.wantPart {
				t.Errorf("partial = %v, want %v", partial, tc.wantPart)
			}
		})
	}
}
