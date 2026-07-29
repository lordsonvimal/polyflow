package parser

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go/token"

	"golang.org/x/tools/go/ssa"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Tier W.2 — interprocedural AMQP exchange-name resolution.
//
// The dominant real cross-service AMQP producer shape wraps the broker publish
// behind one or more helpers whose exchange is a *parameter*, literal only at the
// outermost call site (the real svc-c-mgr chain is
// `controller → Manager.Publish(exchange,…) → AMQPClient.Publish(ctx,exchange,…)
// → channel.PublishWithContext(ctx,exchange,…)`, two wrapper hops):
//
//	c.messagingMgr.Publish("shinyproxy_config", "", body, opts)
//
// The tree-sitter matcher only mints a producer channel node when the exchange is
// a literal at the `channel.Publish` call itself, so every wrapper-mediated
// publish is invisible and cannot join a cross-service consumer. This pass closes
// that indirection with the SSA program the semantic analyzer already builds — the
// exact technique Tier X.7 uses for HTTP wrapper URLs (`go_wrapper_urls.go`),
// reusing its `ssaConstString` / `ssaUnwrap` / `paramIndex` helpers. Package-level
// string consts (e.g. `ExchangeFileSyncOps`) are already inlined to `*ssa.Const`
// by SSA construction, so const-named exchanges resolve for free.
//
// It emits, per resolved publish, a NodeTypeChannel node keyed
// `service:channel:<exchange>/<routing_key>` — byte-identical to the matcher's
// Pass-4 channel ID — plus a `publishes` edge from the calling function. The
// contract engine (`contracts/amqp.yaml`) then joins this producer channel to the
// consumer channel that W.1's `QueueBind` capture mints on the other service.
//
// Scope: `amqp091-go` `Publish` / `PublishWithContext` reached across a bounded
// wrapper closure. The consumer wrapper side (registry-struct-field exchanges like
// svc-c-mgr's `BindQueue(q.Name, key, q.Exchange)`) is a follow-up — a struct
// field load is not a `*ssa.Const` and cannot be resolved here.

// amqpPubWrapper records a function whose publish exchange derives from one of its
// own parameters.
type amqpPubWrapper struct {
	fn                   *ssa.Function
	exchangeParamIndex   int // receiver-inclusive SSA param index carrying the exchange
	routingKeyParamIndex int // receiver-inclusive param index carrying the routing key, or -1
	routingKeyConst      string // wrapper-hardcoded routing key literal, when routingKeyParamIndex < 0
}

// extractAMQPNames synthesizes resolved AMQP producer channel nodes for direct and
// wrapper-mediated publishes whose exchange resolves to a string constant. It is
// deterministic: wrappers are gathered as a name-sorted transitive closure, and
// output slices are sorted by ID before return (bug-class #2).
func extractAMQPNames(
	service, dir string,
	fset *token.FileSet,
	inService map[*ssa.Function]bool,
	resolveFunc func(*ssa.Function) (string, bool),
) ([]graph.Node, []graph.Edge) {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	relPath := func(abs string) string {
		if rel, err := filepath.Rel(canonicalPath(cwd), canonicalPath(abs)); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
		return abs
	}

	byFn := findExchangeParamWrappers(inService, fset)

	var nodes []graph.Node
	var edges []graph.Edge
	seenNodes := map[string]bool{}
	seenEdges := map[string]bool{}

	// emit mints (once) the channel node keyed byte-identically to the matcher's
	// Pass-4 channel ID, plus the producer/consumer edge. role "producer" draws a
	// publishes edge caller→channel; role "consumer" a subscribes edge
	// channel→caller (mirroring Pass-4's directions). The channel node itself is
	// what the amqp contract joins cross-service.
	emit := func(callerID, file string, line int, exchange, routingKey, role string) {
		// exchange/routingKey already come unquoted from ssaConstString.
		if exchange == "" || exchange == "*" {
			return // empty/dynamic exchange must never seed a channel (would fan out)
		}
		channelKey := exchange + "/" + routingKey
		channelID := fmt.Sprintf("%s:channel:%s", service, channelKey)
		if !seenNodes[channelID] {
			seenNodes[channelID] = true
			nodes = append(nodes, graph.Node{
				ID:      channelID,
				Type:    graph.NodeTypeChannel,
				Label:   channelKey,
				Service: service,
				Meta:    map[string]string{"exchange": exchange, "routing_key": routingKey, "synthesized": "amqp_wrapper"},
			})
		}
		var edgeID string
		var edge graph.Edge
		if role == "consumer" {
			edgeID = fmt.Sprintf("amqpsub:%s->%s", channelID, callerID)
			edge = graph.Edge{ID: edgeID, From: channelID, To: callerID, Type: graph.EdgeTypeSubscribes, Confidence: graph.ConfidenceStatic, Meta: map[string]string{"via": "amqp_wrapper"}}
		} else {
			edgeID = fmt.Sprintf("amqppub:%s->%s", callerID, channelID)
			edge = graph.Edge{ID: edgeID, From: callerID, To: channelID, Type: graph.EdgeTypePublishes, Confidence: graph.ConfidenceStatic, Meta: map[string]string{"via": "amqp_wrapper"}}
		}
		if !seenEdges[edgeID] {
			seenEdges[edgeID] = true
			edges = append(edges, edge)
		}
	}

	for caller := range inService {
		callerID, ok := resolveFunc(caller)
		if !ok {
			continue
		}
		for _, b := range caller.Blocks {
			for _, instr := range b.Instrs {
				ci, ok := instr.(ssa.CallInstruction)
				if !ok {
					continue
				}
				common := ci.Common()
				if common.IsInvoke() {
					continue // interface dispatch: concrete wrapper/exchange unknown
				}
				callee, ok := common.Value.(*ssa.Function)
				if !ok {
					continue
				}
				pos := fset.Position(instr.Pos())
				if !pos.IsValid() {
					pos = fset.Position(caller.Pos())
				}
				file := relPath(pos.Filename)
				if graph.IsTestFilePath(file) {
					continue // test scaffolding, not a real producer (mirrors X.9 guard)
				}

				// (a) direct amqp publish whose exchange is a const/literal.
				if exArg, keyArg, ok := amqpPublishArgs(common); ok {
					if ex, ok := ssaConstString(exArg); ok {
						key, _ := ssaConstString(keyArg) // "" when dynamic — fanout/default
						emit(callerID, file, pos.Line, ex, key, "producer")
					}
					continue
				}
				// (b) direct consumer QueueBind whose exchange is a const/literal.
				// Read from SSA (not tree-sitter) precisely because amqp091 code
				// puts an inline comment on each QueueBind argument, which shifts
				// positional tree-sitter capture; the typed call has no comments.
				if exArg, keyArg, ok := amqpBindArgs(common); ok {
					if ex, ok := ssaConstString(exArg); ok {
						key, _ := ssaConstString(keyArg) // "" for fanout
						emit(callerID, file, pos.Line, ex, key, "consumer")
					}
					continue
				}
				// (c) wrapper publish: resolve the exchange arg at this call site.
				w, ok := byFn[callee]
				if !ok || w.exchangeParamIndex >= len(common.Args) {
					continue
				}
				ex, ok := ssaConstString(common.Args[w.exchangeParamIndex])
				if !ok {
					continue // non-literal exchange — the honest dynamic ledger stands
				}
				key := w.routingKeyConst
				if w.routingKeyParamIndex >= 0 && w.routingKeyParamIndex < len(common.Args) {
					if k, ok := ssaConstString(common.Args[w.routingKeyParamIndex]); ok {
						key = k
					} else {
						key = "" // dynamic routing key
					}
				}
				emit(callerID, file, pos.Line, ex, key, "producer")
			}
		}
	}

	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	return nodes, edges
}

// amqpPublishArgs returns the (exchange, routingKey) argument values of an
// amqp091-go Publish / PublishWithContext call, or ok=false. Args are
// receiver-inclusive (Args[0] is the *Channel receiver for these static method
// calls), so exchange sits one slot later than its logical signature position.
func amqpPublishArgs(common *ssa.CallCommon) (exchangeArg, routingKeyArg ssa.Value, ok bool) {
	fn, ok := common.Value.(*ssa.Function)
	if !ok || fn.Pkg == nil || fn.Pkg.Pkg == nil || !strings.Contains(fn.Pkg.Pkg.Path(), "amqp") {
		return nil, nil, false
	}
	switch fn.Name() {
	case "Publish": // (recv, exchange, key, mandatory, immediate, msg)
		if len(common.Args) >= 3 {
			return common.Args[1], common.Args[2], true
		}
	case "PublishWithContext": // (recv, ctx, exchange, key, mandatory, immediate, msg)
		if len(common.Args) >= 4 {
			return common.Args[2], common.Args[3], true
		}
	}
	return nil, nil, false
}

// amqpBindArgs returns the (exchange, routingKey) argument values of an
// amqp091-go QueueBind call, or ok=false. Signature is
// `QueueBind(name, key, exchange, noWait, args)`; Args are receiver-inclusive
// (Args[0] is the *Channel receiver), so key=Args[2], exchange=Args[3].
func amqpBindArgs(common *ssa.CallCommon) (exchangeArg, routingKeyArg ssa.Value, ok bool) {
	fn, ok := common.Value.(*ssa.Function)
	if !ok || fn.Pkg == nil || fn.Pkg.Pkg == nil || !strings.Contains(fn.Pkg.Pkg.Path(), "amqp") {
		return nil, nil, false
	}
	if fn.Name() == "QueueBind" && len(common.Args) >= 4 {
		return common.Args[3], common.Args[2], true
	}
	return nil, nil, false
}

// findExchangeParamWrappers computes the transitive set of functions whose publish
// exchange derives from one of their own parameters. Seed: functions with a direct
// amqp publish whose exchange is a bare parameter. Closure: pass-through wrappers
// that forward their own parameter into a known wrapper's exchange slot. Iteration
// is over a name-sorted function slice to a fixpoint (order-independent); a round
// cap guards mutual recursion. Mirrors go_wrapper_urls.go's findURLParamWrappers.
func findExchangeParamWrappers(inService map[*ssa.Function]bool, fset *token.FileSet) map[*ssa.Function]amqpPubWrapper {
	fns := make([]*ssa.Function, 0, len(inService))
	for fn := range inService {
		fns = append(fns, fn)
	}
	sort.Slice(fns, func(i, j int) bool {
		pi, pj := fset.Position(fns[i].Pos()), fset.Position(fns[j].Pos())
		if pi.Filename != pj.Filename {
			return pi.Filename < pj.Filename
		}
		if pi.Line != pj.Line {
			return pi.Line < pj.Line
		}
		return fns[i].Name() < fns[j].Name()
	})

	byFn := make(map[*ssa.Function]amqpPubWrapper)
	for _, fn := range fns {
		if info, ok := directExchangeParamWrapper(fn); ok {
			byFn[fn] = info
		}
	}
	const maxRounds = 8
	for round := 0; round < maxRounds; round++ {
		changed := false
		for _, fn := range fns {
			if _, done := byFn[fn]; done {
				continue
			}
			if info, ok := forwardingExchangeWrapper(fn, byFn); ok {
				byFn[fn] = info
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return byFn
}

// directExchangeParamWrapper inspects fn's body for an amqp publish whose exchange
// argument is one of fn's own parameters. Records the exchange (and routing-key)
// parameter indices.
func directExchangeParamWrapper(fn *ssa.Function) (amqpPubWrapper, bool) {
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			ci, ok := instr.(ssa.CallInstruction)
			if !ok {
				continue
			}
			common := ci.Common()
			if common.IsInvoke() {
				continue
			}
			exArg, keyArg, ok := amqpPublishArgs(common)
			if !ok {
				continue
			}
			exIdx, ok := paramIndex(exArg, fn)
			if !ok {
				continue // exchange is a literal/const here, not a param — no wrapper
			}
			info := amqpPubWrapper{fn: fn, exchangeParamIndex: exIdx, routingKeyParamIndex: -1}
			if ki, ok := paramIndex(keyArg, fn); ok {
				info.routingKeyParamIndex = ki
			} else if kc, ok := ssaConstString(keyArg); ok {
				info.routingKeyConst = kc
			}
			return info, true
		}
	}
	return amqpPubWrapper{}, false
}

// forwardingExchangeWrapper reports whether fn forwards one of its own parameters
// into a known wrapper's exchange slot, making fn itself an exchange-param wrapper.
func forwardingExchangeWrapper(fn *ssa.Function, byFn map[*ssa.Function]amqpPubWrapper) (amqpPubWrapper, bool) {
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			ci, ok := instr.(ssa.CallInstruction)
			if !ok {
				continue
			}
			common := ci.Common()
			if common.IsInvoke() {
				continue
			}
			callee, ok := common.Value.(*ssa.Function)
			if !ok {
				continue
			}
			g, ok := byFn[callee]
			if !ok || g.exchangeParamIndex >= len(common.Args) {
				continue
			}
			exIdx, ok := paramIndex(common.Args[g.exchangeParamIndex], fn)
			if !ok {
				continue // forwards a literal/const, not fn's own param — not a wrapper
			}
			info := amqpPubWrapper{fn: fn, exchangeParamIndex: exIdx, routingKeyParamIndex: -1, routingKeyConst: g.routingKeyConst}
			if g.routingKeyParamIndex >= 0 && g.routingKeyParamIndex < len(common.Args) {
				if ki, ok := paramIndex(common.Args[g.routingKeyParamIndex], fn); ok {
					info.routingKeyParamIndex = ki
				} else if kc, ok := ssaConstString(common.Args[g.routingKeyParamIndex]); ok {
					info.routingKeyConst = kc
				}
			}
			return info, true
		}
	}
	return amqpPubWrapper{}, false
}
