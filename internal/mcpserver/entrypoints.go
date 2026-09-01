package mcpserver

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

type entrypointsInput struct {
	Service string `json:"service,omitempty" jsonschema:"restrict to entrypoints in this service"`
	Type    string `json:"type,omitempty" jsonschema:"restrict to one entrypoint type: http_handler, subscriber, worker, grpc_handler, graphql_resolver"`
	Feature string `json:"feature,omitempty" jsonschema:"case-insensitive substring filter against the entrypoint label and file path"`
	Limit   int    `json:"limit,omitempty" jsonschema:"max entries returned (default 200, -1 = unlimited)"`
}

// entrypointEntry is one catalogued entry node. Method/Path are populated for
// http_handler nodes (stamped by the route matchers into node Meta).
type entrypointEntry struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Label   string `json:"label"`
	Service string `json:"service"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Method  string `json:"method,omitempty"`
	Path    string `json:"path,omitempty"`
}

type entrypointsOutput struct {
	Entrypoints     []entrypointEntry `json:"entrypoints"`
	Total           int               `json:"total"`
	Truncated       bool              `json:"truncated,omitempty"`
	SkippedTestFile int               `json:"skipped_test_file,omitempty"`
}

func (s *Server) entrypoints(ctx context.Context, req *mcp.CallToolRequest, in entrypointsInput) (*mcp.CallToolResult, any, error) {
	if in.Type != "" && !entrypointNodeTypes[graph.NodeType(in.Type)] {
		return nil, nil, fmt.Errorf("unknown entrypoint type: %s (use: http_handler, subscriber, worker, grpc_handler, graphql_resolver)", in.Type)
	}

	_, idx, _ := s.snapshot()
	feature := strings.ToLower(in.Feature)
	queueNames := resolveSubscriberQueues(idx)

	var all []entrypointEntry
	var skippedTestFile int
	for _, n := range idx.Nodes {
		if !entrypointNodeTypes[n.Type] {
			continue
		}
		if in.Type != "" && string(n.Type) != in.Type {
			continue
		}
		// Test-defined handlers are not a production entry surface; exclude
		// them but report the count so the total stays honest.
		if graph.IsTestFilePath(n.File) {
			skippedTestFile++
			continue
		}
		if in.Service != "" && n.Service != in.Service {
			continue
		}
		queue := queueNames[n.ID]
		if feature != "" &&
			!strings.Contains(strings.ToLower(n.Label), feature) &&
			!strings.Contains(strings.ToLower(n.File), feature) &&
			!strings.Contains(strings.ToLower(queue), feature) {
			continue
		}
		path := n.Meta["path"]
		if path == "" {
			path = queue
		}
		all = append(all, entrypointEntry{
			ID:      n.ID,
			Type:    string(n.Type),
			Label:   n.Label,
			Service: n.Service,
			File:    n.File,
			Line:    n.Line,
			Method:  n.Meta["method"],
			Path:    path,
		})
	}

	sort.Slice(all, func(i, j int) bool {
		if all[i].Service != all[j].Service {
			return all[i].Service < all[j].Service
		}
		if all[i].Type != all[j].Type {
			return all[i].Type < all[j].Type
		}
		if all[i].File != all[j].File {
			return all[i].File < all[j].File
		}
		if all[i].Line != all[j].Line {
			return all[i].Line < all[j].Line
		}
		return all[i].ID < all[j].ID
	})

	out := entrypointsOutput{Total: len(all), SkippedTestFile: skippedTestFile}
	limit := in.Limit
	switch {
	case limit < 0:
		out.Entrypoints = all
	case limit == 0:
		limit = 200
		fallthrough
	default:
		if len(all) > limit {
			out.Entrypoints = all[:limit]
			out.Truncated = true
		} else {
			out.Entrypoints = all
		}
	}
	if out.Entrypoints == nil {
		out.Entrypoints = []entrypointEntry{}
	}
	return jsonResult(out)
}

// resolveSubscriberQueues maps each AMQP subscriber node to the queue/routing
// name a `subscribes` edge already resolved. parser/go_amqp_names.go's
// linkQueueConsumers only learns a queue's name once an interprocedural
// QueueBind join attaches it to a `subscribes` edge pointing at the
// *function* wrapping the raw broker Consume call — not at the raw call
// site's own pattern-matched subscriber node, which stays labeled "dynamic"
// forever. Without this join, entrypoints's feature filter can never match a
// query like "build" against a node whose only stamped identity is the
// literal string "dynamic": every generic-wrapper AMQP consumer looks
// identical (confirmed live — `entrypoints(feature:"build", type:"subscriber")`
// against the juniper corpus returned zero results despite a resolved
// `maple-agent:channel:build_jobs/build.submit` -[subscribes]-> consumeJobsInternal
// edge existing in the same graph).
//
// The join is by innermost enclosing function: bucket function/method nodes
// by file and find the last one starting at or before the subscriber's line,
// mirroring the enclosingFunc approximation parser/matcher.go already uses
// at index time for this exact class of "call site vs. wrapping declaration"
// gap.
func resolveSubscriberQueues(idx *graph.AdjacencyIndex) map[string]string {
	byFile := make(map[string][]*graph.Node)
	for _, n := range idx.Nodes {
		if n.Type == graph.NodeTypeFunction || n.Type == graph.NodeTypeMethod {
			byFile[n.File] = append(byFile[n.File], n)
		}
	}
	for _, list := range byFile {
		sort.Slice(list, func(i, j int) bool { return list[i].Line < list[j].Line })
	}

	queueByFunc := make(map[string]string)
	for _, edges := range idx.AllEdges() {
		if edges.Type != graph.EdgeTypeSubscribes {
			continue
		}
		q := edges.Meta["queue_name"]
		if q == "" {
			if from := idx.Nodes[edges.From]; from != nil {
				q = from.Label
			}
		}
		if q != "" {
			queueByFunc[edges.To] = q
		}
	}
	if len(queueByFunc) == 0 {
		return nil
	}

	result := make(map[string]string)
	for _, n := range idx.Nodes {
		if n.Type != graph.NodeTypeSubscriber {
			continue
		}
		var enclosing *graph.Node
		for _, f := range byFile[n.File] {
			if f.Line > n.Line {
				break
			}
			enclosing = f
		}
		if enclosing == nil {
			continue
		}
		if q, ok := queueByFunc[enclosing.ID]; ok {
			result[n.ID] = q
		}
	}
	return result
}
