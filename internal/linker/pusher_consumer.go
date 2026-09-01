package linker

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/lordsonvimal/polyflow/internal/railsview"
)

// EnrichPusherConsumers is Tier PU.3 — the consumer half of Pusher realtime
// linking.
//
// The pusher-js subscribe call in the browser
// (`PusherConnection.jsx: instance.subscribe(config.channel)`) takes its channel
// and event from the `pusherConfig` prop, which every orion view builds in
// ERB through `PusherConfigHelper#pusher_config(channel:, event:)`:
//
//	<%= react_component("SceBatchItemsContainer", {
//	  pusherConfig: pusher_config(
//	    channel: PusherClient::CHANNELS[:lro_update],
//	    event:   PusherClient::LRO_DETAILS,
//	  ),
//	}) %>
//
// or, for the shared partial path, `render "shared/pusher", pusher_channel:
// "#{Rails.env}.folder-status.#{@study.id}"` (channel only, no event).
//
// The React layer is a pure pass-through — the channel/event strings never
// appear as JS literals — so the keyed consumer node has to be minted here,
// from the ERB call site, resolving `PusherClient::` constants against the same
// wrapper class the producer pass reads. Each minted `subscriber` node is
// `contains`-linked to its ERB file node so it is reachable in the graph even
// before the contract engine joins it to a producer.
//
// The runtime JS `pusher_subscribe_client` node is left untouched (it stays
// `key_dynamic` — an honest witness that the browser subscribes to a
// server-supplied channel).
func EnrichPusherConsumers(nodes []graph.Node, serviceFiles map[string][]string) ([]graph.Node, []graph.Edge) {
	// ── locate the wrapper class(es): a file with a pusher_trigger* node ──
	hubFiles := map[string]bool{}
	for i := range nodes {
		switch nodes[i].Meta["pattern"] {
		case "pusher_trigger", "pusher_trigger_async":
			if nodes[i].File != "" {
				hubFiles[nodes[i].File] = true
			}
		}
	}
	if len(hubFiles) == 0 {
		return nil, nil
	}

	eventByMethod := map[string]map[string]string{}
	constVals := map[string]map[string]string{}
	hashConstVals := map[string]map[string]map[string]string{}
	for file := range hubFiles {
		fa := parseRubyFileAST(file)
		if fa == nil {
			continue
		}
		collectPusherWrapperFacts(fa, eventByMethod, constVals, hashConstVals)
		fa.release()
	}
	if len(constVals) == 0 && len(hashConstVals) == 0 {
		return nil, nil
	}

	// file node id lookup, so each ERB subscriber has a containment anchor.
	fileNodeID := map[string]string{}
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeFile {
			fileNodeID[nodes[i].Service+"\x00"+nodes[i].File] = nodes[i].ID
		}
	}

	svcFilesSorted := make([]string, 0)
	fileSvc := map[string]string{}
	for svc, files := range serviceFiles {
		for _, f := range files {
			if strings.HasSuffix(f, ".erb") {
				svcFilesSorted = append(svcFilesSorted, f)
				fileSvc[f] = svc
			}
		}
	}
	sort.Strings(svcFilesSorted)

	var newNodes []graph.Node
	var newEdges []graph.Edge
	seen := map[string]bool{}

	for _, file := range svcFilesSorted {
		if graph.IsTestFilePath(file) {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		_, ruby := railsview.SplitERB(src)
		root, release := parsePusherRubySource(ruby)
		if root == nil {
			continue
		}
		svc := fileSvc[file]
		relFile := patterns.RelativizeToCwd(file)
		fileID := fileNodeID[svc+"\x00"+relFile]

		mint := func(pattern, chanSeg, event string, line int) {
			id := fmt.Sprintf("%s:%s:subscriber:%s:%d", svc, relFile, pattern, line)
			if seen[id] {
				return
			}
			seen[id] = true
			meta := map[string]string{
				"pattern":      pattern,
				"channel":      chanSeg,
				"package":      "pusher-js",
				"resolved_via": "pusher_consumer_erb",
			}
			label := chanSeg
			if event != "" {
				meta["event"] = event
				label = chanSeg + " " + event
			}
			newNodes = append(newNodes, graph.Node{
				ID:       id,
				Type:     graph.NodeTypeSubscriber,
				Label:    label,
				Service:  svc,
				File:     relFile,
				Line:     line,
				Language: "erb",
				Meta:     meta,
			})
			if fileID != "" {
				newEdges = append(newEdges, containsEdge(fileID, id))
			}
		}

		var walk func(n *sitter.Node)
		walk = func(n *sitter.Node) {
			if n.Type() == "call" {
				if mn := n.ChildByFieldName("method"); mn != nil {
					switch mn.Content(ruby) {
					case "pusher_config":
						chArg := pusherKeywordArg(n, ruby, "channel")
						chSeg := pusherRefSegment(chArg, ruby, constVals, hashConstVals)
						if chSeg != "" {
							ev := pusherRefLiteral(pusherKeywordArg(n, ruby, "event"), ruby, constVals)
							mint("pusher_subscribe_erb", chSeg, ev, int(n.StartPoint().Row)+1)
						}
					case "render":
						if a0 := pusherArgAt(n, 0); a0 != nil && a0.Type() == "string" &&
							pusherStringLiteral(a0, ruby) == "shared/pusher" {
							chArg := pusherKeywordArg(n, ruby, "pusher_channel")
							chSeg := pusherRefSegment(chArg, ruby, constVals, hashConstVals)
							if chSeg != "" {
								mint("pusher_subscribe_erb_channel", chSeg, "", int(n.StartPoint().Row)+1)
							}
						}
					}
				}
			}
			for i := 0; i < int(n.NamedChildCount()); i++ {
				walk(n.NamedChild(i))
			}
		}
		walk(root)
		release()
	}

	sort.Slice(newNodes, func(a, b int) bool { return newNodes[a].ID < newNodes[b].ID })
	sort.Slice(newEdges, func(a, b int) bool {
		if newEdges[a].From != newEdges[b].From {
			return newEdges[a].From < newEdges[b].From
		}
		return newEdges[a].To < newEdges[b].To
	})
	return newNodes, newEdges
}

// parsePusherRubySource parses a raw Ruby byte slice (the virtual-Ruby view of
// an ERB template). Unlike rubyParse it takes bytes, not a path, and always
// owns the tree — the caller must call release.
func parsePusherRubySource(src []byte) (root *sitter.Node, release func()) {
	p := sitter.NewParser()
	p.SetLanguage(rubysitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil || tree == nil {
		return nil, func() {}
	}
	return tree.RootNode(), func() { tree.Close() }
}

// pusherKeywordArg returns the value node of a `name:` keyword argument on call.
func pusherKeywordArg(call *sitter.Node, src []byte, name string) *sitter.Node {
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return nil
	}
	for i := 0; i < int(args.NamedChildCount()); i++ {
		c := args.NamedChild(i)
		if c.Type() != "pair" {
			continue
		}
		k := c.ChildByFieldName("key")
		if k == nil {
			continue
		}
		key := strings.TrimSuffix(strings.TrimPrefix(k.Content(src), ":"), ":")
		if key == name {
			return c.ChildByFieldName("value")
		}
	}
	return nil
}

// pusherRefSegment reduces an ERB channel expression to the one stable segment
// both sides agree on: a bare string's literal middle segment, a
// `PusherClient::CONST` value, or a `PusherClient::CHANNELS[:sym]` lookup.
func pusherRefSegment(
	n *sitter.Node, src []byte,
	constVals map[string]map[string]string,
	hashConstVals map[string]map[string]map[string]string,
) string {
	if n == nil {
		return ""
	}
	switch n.Type() {
	case "string":
		return pusherChannelFromString(n, src)
	case "scope_resolution":
		scope := n.ChildByFieldName("scope")
		name := n.ChildByFieldName("name")
		if scope != nil && name != nil {
			return constVals[scope.Content(src)][name.Content(src)]
		}
	case "element_reference":
		obj := n.ChildByFieldName("object")
		if obj == nil || obj.Type() != "scope_resolution" || n.NamedChildCount() < 2 {
			return ""
		}
		s := obj.ChildByFieldName("scope")
		nm := obj.ChildByFieldName("name")
		if s == nil || nm == nil {
			return ""
		}
		key := strings.TrimPrefix(strings.Trim(n.NamedChild(1).Content(src), `"'`), ":")
		if h := hashConstVals[s.Content(src)][nm.Content(src)]; h != nil {
			return h[key]
		}
	}
	return ""
}

// pusherRefLiteral resolves an ERB event expression to its string value: a
// literal, or a `PusherClient::CONST`.
func pusherRefLiteral(n *sitter.Node, src []byte, constVals map[string]map[string]string) string {
	if n == nil {
		return ""
	}
	switch n.Type() {
	case "string":
		return pusherStringLiteral(n, src)
	case "scope_resolution":
		scope := n.ChildByFieldName("scope")
		name := n.ChildByFieldName("name")
		if scope != nil && name != nil {
			return constVals[scope.Content(src)][name.Content(src)]
		}
	}
	return ""
}
