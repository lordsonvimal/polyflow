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
// wrapper closure. Tier J.1 adds the consumer wrapper side — a `QueueBind` whose
// exchange is a parameter of a bounded wrapper closure (interface dispatch
// included, when the method name+arity has one unambiguous implementation), with
// struct-field exchanges resolved against the static declaration table they load
// from (see go_struct_tables.go).

// amqpPubWrapper records a function whose publish exchange derives from one of its
// own parameters.
type amqpPubWrapper struct {
	fn                   *ssa.Function
	exchangeParamIndex   int    // receiver-inclusive SSA param index carrying the exchange
	routingKeyParamIndex int    // receiver-inclusive param index carrying the routing key, or -1
	routingKeyConst      string // wrapper-hardcoded routing key literal, when routingKeyParamIndex < 0
}

// amqpBindWrapper records a function whose QueueBind exchange derives from one of
// its own parameters — the consumer-side twin of amqpPubWrapper (J.1). The real
// dsw-manager chain is
// `declareQueues → queueDeclarer.BindQueue (interface) → amqpQueueAdapter.BindQueue
// → (*AMQPClient).BindQueue → channel.QueueBind`, so the binding's exchange is a
// parameter three hops away from the call site that knows its value.
type amqpBindWrapper struct {
	fn                   *ssa.Function
	exchangeParamIndex   int // receiver-inclusive param index carrying the exchange
	routingKeyParamIndex int // receiver-inclusive param index carrying the routing key, or -1
	queueNameParamIndex  int // receiver-inclusive param index carrying the queue name, or -1
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
	bindByFn := findBindParamWrappers(inService, fset)

	var nodes []graph.Node
	var edges []graph.Edge
	seenNodes := map[string]bool{}
	seenEdges := map[string]bool{}
	// queueChannels indexes table-resolved channels by the queue name they bind,
	// so a `Consume(QueueBuildLogs, …)` call site — which names only its queue —
	// can subscribe to the exchange/routing-key channels that queue receives on.
	queueChannels := map[string][]string{}

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

	// emitTableChannel mints the consumer channel for one (row × routing key) of
	// a static binding table. Same ID keying as emit — so a service that both
	// declares the table and binds literally dedupes to one node — but it carries
	// the row's own provenance (file/line of the declaration, queue name) so the
	// topology can be traced back to where it is declared rather than to the
	// generic `declareQueues` loop.
	emitTableChannel := func(callerID, file string, line int, exchange, routingKey, queueName, tableType string) {
		if exchange == "" || exchange == "*" {
			return
		}
		channelKey := exchange + "/" + routingKey
		channelID := fmt.Sprintf("%s:channel:%s", service, channelKey)
		if !seenNodes[channelID] {
			seenNodes[channelID] = true
			meta := map[string]string{
				"exchange":     exchange,
				"routing_key":  routingKey,
				"pattern":      "amqp_queue_bind",
				"resolved_via": "static_table",
				"table_type":   tableType,
				"synthesized":  "amqp_wrapper",
			}
			if queueName != "" {
				meta["queue_name"] = queueName
				queueChannels[queueName] = append(queueChannels[queueName], channelID)
			}
			nodes = append(nodes, graph.Node{
				ID:      channelID,
				Type:    graph.NodeTypeChannel,
				Label:   channelKey,
				Service: service,
				File:    file,
				Line:    line,
				Meta:    meta,
			})
		}
		edgeID := fmt.Sprintf("amqpsub:%s->%s", channelID, callerID)
		if !seenEdges[edgeID] {
			seenEdges[edgeID] = true
			edges = append(edges, graph.Edge{
				ID: edgeID, From: channelID, To: callerID,
				Type: graph.EdgeTypeSubscribes, Confidence: graph.ConfidenceStatic,
				Meta: map[string]string{"via": "amqp_static_table"},
			})
		}
	}

	// emitBind resolves one binding call site — direct or wrapper-mediated — into
	// consumer channel node(s). A const exchange mints one channel; an exchange
	// that is a field of a static declaration table (J.1) mints one channel per
	// (table row × routing key), which is not a guess but the declared topology.
	emitBind := func(callerID, file string, line int, queueArg, keyArg, exArg ssa.Value) {
		if ex, ok := ssaConstString(exArg); ok {
			key, _ := ssaConstString(keyArg) // "" for fanout
			emit(callerID, file, line, ex, key, "consumer")
			return
		}
		exField, tbl, ok := tableFieldOf(exArg, inService)
		if !ok {
			return // honest dynamic: the unresolved bind stays unrepresented
		}
		keyField := tableFieldNamed(keyArg, inService, tbl.TypeName)
		queueField := tableFieldNamed(queueArg, inService, tbl.TypeName)
		constKey, constKeyOK := ssaConstString(keyArg)

		for _, row := range tbl.Rows {
			exchange := row.Fields[exField]
			if exchange == "" {
				continue // this row's exchange did not decode — never fabricate one
			}
			rowFile, rowLine := file, line
			if p := fset.Position(row.Pos); p.IsValid() {
				rowFile, rowLine = relPath(p.Filename), p.Line
			}
			for _, key := range tableRoutingKeys(row, keyField, constKey, constKeyOK) {
				emitTableChannel(callerID, rowFile, rowLine, exchange, key, row.Fields[queueField], tbl.TypeName)
			}
		}
	}

	for _, caller := range sortedServiceFns(inService, fset) {
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
				pos := fset.Position(instr.Pos())
				if !pos.IsValid() {
					pos = fset.Position(caller.Pos())
				}
				file := relPath(pos.Filename)
				if graph.IsTestFilePath(file) {
					continue // test scaffolding, not a real producer (mirrors X.9 guard)
				}
				if common.IsInvoke() {
					// Interface dispatch. The concrete publisher is unknown, but a
					// bind whose method name+arity has exactly one implementation
					// among the in-service bind wrappers is unambiguous — the
					// `declareQueues(d queueDeclarer)` shape J.1 targets.
					if w, ok := bindWrapperForInvoke(common, bindByFn); ok {
						if q, k, ex, ok := bindCallArgs(common.Args, w, -1); ok {
							emitBind(callerID, file, pos.Line, q, k, ex)
						}
					}
					continue
				}
				callee, ok := common.Value.(*ssa.Function)
				if !ok {
					continue
				}

				// (a) direct amqp publish whose exchange is a const/literal.
				if exArg, keyArg, ok := amqpPublishArgs(common); ok {
					if ex, ok := ssaConstString(exArg); ok {
						key, _ := ssaConstString(keyArg) // "" when dynamic — fanout/default
						emit(callerID, file, pos.Line, ex, key, "producer")
					}
					continue
				}
				// (b) direct consumer QueueBind. Read from SSA (not tree-sitter)
				// precisely because amqp091 code puts an inline comment on each
				// QueueBind argument, which shifts positional tree-sitter
				// capture; the typed call has no comments.
				if queueArg, keyArg, exArg, ok := amqpBindQueueArgs(common); ok {
					emitBind(callerID, file, pos.Line, queueArg, keyArg, exArg)
					continue
				}
				// (c) wrapper bind: resolve the exchange arg at this call site.
				if bw, ok := bindByFn[callee]; ok {
					if q, k, ex, ok := bindCallArgs(common.Args, bw, 0); ok {
						emitBind(callerID, file, pos.Line, q, k, ex)
					}
					continue
				}
				// (d) wrapper publish: resolve the exchange arg at this call site.
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

	linkQueueConsumers(inService, fset, resolveFunc, relPath, queueChannels, seenEdges, &edges)

	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	return nodes, edges
}

// linkQueueConsumers joins `Consume(<queue>)` call sites to the channels that
// queue is bound to by a static table. A consumer that names only its queue is
// otherwise stuck as a `dynamic` subscriber with no exchange — the vacuum J.3's
// broker-hint fan-out fills with phantoms. The queue name is a constant here (a
// package-level const, already inlined by SSA), and the queue→exchange mapping is
// declared, so the resulting edges are static, not inferred: a queue bound to N
// routing keys really does deliver all N to its consumer.
//
// Scope: direct amqp091 `Consume` plus a bounded closure of wrappers whose queue
// argument is one of their own parameters, on statically-dispatched calls. An
// interface-dispatched consume is left alone — unlike a bind, it carries no
// second piece of evidence with which to disambiguate implementations.
func linkQueueConsumers(
	inService map[*ssa.Function]bool,
	fset *token.FileSet,
	resolveFunc func(*ssa.Function) (string, bool),
	relPath func(string) string,
	queueChannels map[string][]string,
	seenEdges map[string]bool,
	edges *[]graph.Edge,
) {
	if len(queueChannels) == 0 {
		return // cheap gate: no static table resolved, nothing to join
	}
	fns := sortedServiceFns(inService, fset)
	consumeByFn := findConsumeQueueWrappers(fns)

	for _, caller := range fns {
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
					continue
				}
				callee, ok := common.Value.(*ssa.Function)
				if !ok {
					continue
				}
				pos := fset.Position(instr.Pos())
				if pos.IsValid() && graph.IsTestFilePath(relPath(pos.Filename)) {
					continue // test scaffolding, not a real consumer
				}

				queueArg, ok := amqpConsumeQueueArg(common)
				if !ok {
					idx, known := consumeByFn[callee]
					if !known || idx >= len(common.Args) {
						continue
					}
					queueArg = common.Args[idx]
				}
				queue, ok := ssaConstString(queueArg)
				if !ok || queue == "" {
					continue // dynamic queue name: the honest dynamic subscriber stands
				}
				for _, channelID := range queueChannels[queue] {
					edgeID := fmt.Sprintf("amqpsub:%s->%s", channelID, callerID)
					if seenEdges[edgeID] {
						continue
					}
					seenEdges[edgeID] = true
					*edges = append(*edges, graph.Edge{
						ID: edgeID, From: channelID, To: callerID,
						Type: graph.EdgeTypeSubscribes, Confidence: graph.ConfidenceStatic,
						Meta: map[string]string{"via": "amqp_queue_table", "queue_name": queue},
					})
				}
			}
		}
	}
}

// amqpConsumeQueueArg returns the queue argument of an amqp091-go Consume call.
// Signature is `Consume(queue, consumer, autoAck, exclusive, noLocal, noWait,
// args)`; Args are receiver-inclusive, so the queue is Args[1].
func amqpConsumeQueueArg(common *ssa.CallCommon) (ssa.Value, bool) {
	fn, ok := common.Value.(*ssa.Function)
	if !ok || fn.Pkg == nil || fn.Pkg.Pkg == nil || !strings.Contains(fn.Pkg.Pkg.Path(), "amqp") {
		return nil, false
	}
	if fn.Name() == "Consume" && len(common.Args) >= 2 {
		return common.Args[1], true
	}
	return nil, false
}

// findConsumeQueueWrappers maps each function whose amqp Consume queue derives
// from one of its own parameters to that parameter's receiver-inclusive index,
// closing transitively over forwarding wrappers. fns must already be sorted so
// the fixpoint is order-independent.
func findConsumeQueueWrappers(fns []*ssa.Function) map[*ssa.Function]int {
	byFn := make(map[*ssa.Function]int)
	const maxRounds = 8
	for round := 0; round < maxRounds; round++ {
		changed := false
		for _, fn := range fns {
			if _, done := byFn[fn]; done {
				continue
			}
			idx, ok := consumeQueueParamIndex(fn, byFn)
			if !ok {
				continue
			}
			byFn[fn] = idx
			changed = true
		}
		if !changed {
			break
		}
	}
	return byFn
}

// consumeQueueParamIndex reports the parameter of fn that reaches an amqp Consume
// queue slot, either directly or through an already-known wrapper.
func consumeQueueParamIndex(fn *ssa.Function, byFn map[*ssa.Function]int) (int, bool) {
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
			queueArg, ok := amqpConsumeQueueArg(common)
			if !ok {
				callee, isFn := common.Value.(*ssa.Function)
				if !isFn {
					continue
				}
				idx, known := byFn[callee]
				if !known || idx >= len(common.Args) {
					continue
				}
				queueArg = common.Args[idx]
			}
			if i, ok := paramIndex(queueArg, fn); ok {
				return i, true
			}
		}
	}
	return 0, false
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

// amqpBindQueueArgs returns the (queueName, routingKey, exchange) argument values
// of an amqp091-go QueueBind call, or ok=false. Signature is
// `QueueBind(name, key, exchange, noWait, args)`; Args are receiver-inclusive
// (Args[0] is the *Channel receiver), so name=Args[1], key=Args[2],
// exchange=Args[3].
func amqpBindQueueArgs(common *ssa.CallCommon) (queueArg, routingKeyArg, exchangeArg ssa.Value, ok bool) {
	fn, ok := common.Value.(*ssa.Function)
	if !ok || fn.Pkg == nil || fn.Pkg.Pkg == nil || !strings.Contains(fn.Pkg.Pkg.Path(), "amqp") {
		return nil, nil, nil, false
	}
	if fn.Name() == "QueueBind" && len(common.Args) >= 4 {
		return common.Args[1], common.Args[2], common.Args[3], true
	}
	return nil, nil, nil, false
}

// bindCallArgs projects a call site's arguments onto a bind wrapper's parameter
// slots. offset is 0 for a static call (args are receiver-inclusive, matching the
// wrapper's own param indices) and -1 for an interface invoke (the receiver is
// common.Value, so every arg sits one slot earlier). A missing routing-key or
// queue-name slot yields a nil value, which callers treat as "dynamic".
func bindCallArgs(args []ssa.Value, w amqpBindWrapper, offset int) (queueArg, keyArg, exchangeArg ssa.Value, ok bool) {
	at := func(paramIdx int) ssa.Value {
		i := paramIdx + offset
		if paramIdx < 0 || i < 0 || i >= len(args) {
			return nil
		}
		return args[i]
	}
	exchangeArg = at(w.exchangeParamIndex)
	if exchangeArg == nil {
		return nil, nil, nil, false
	}
	return at(w.queueNameParamIndex), at(w.routingKeyParamIndex), exchangeArg, true
}

// tableFieldNamed resolves an argument to the name of the table field it loads,
// but only when it belongs to the same table type as the exchange did. A field of
// a *different* table is not evidence about this binding, so it is discarded
// rather than mixed in. Returns "" when the argument is not a table field.
func tableFieldNamed(arg ssa.Value, inService map[*ssa.Function]bool, tableType string) string {
	if arg == nil {
		return ""
	}
	field, tbl, ok := tableFieldOf(arg, inService)
	if !ok || tbl.TypeName != tableType {
		return ""
	}
	return field
}

// tableRoutingKeys returns the routing keys one table row contributes: the row's
// own []string field when the bind ranges over it, its scalar field when the bind
// reads one, an outer literal when the key is constant, and otherwise a single
// empty key — an exchange-only binding, which the amqp contract's `exchange_only`
// tier matches at partial confidence rather than pretending to know the key.
func tableRoutingKeys(row structTableRow, keyField, constKey string, constKeyOK bool) []string {
	if keyField != "" {
		if keys, ok := row.Slices[keyField]; ok {
			return keys
		}
		if k, ok := row.Fields[keyField]; ok {
			return []string{k}
		}
		return nil // the row declares this field but it did not decode: emit nothing
	}
	if constKeyOK {
		return []string{constKey}
	}
	return []string{""}
}

// findBindParamWrappers computes the transitive set of functions whose QueueBind
// exchange derives from one of their own parameters — the consumer-side twin of
// findExchangeParamWrappers, sharing its seed/closure/fixpoint structure.
func findBindParamWrappers(inService map[*ssa.Function]bool, fset *token.FileSet) map[*ssa.Function]amqpBindWrapper {
	fns := sortedServiceFns(inService, fset)

	byFn := make(map[*ssa.Function]amqpBindWrapper)
	for _, fn := range fns {
		if info, ok := directBindParamWrapper(fn); ok {
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
			if info, ok := forwardingBindWrapper(fn, byFn); ok {
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

// directBindParamWrapper inspects fn's body for an amqp QueueBind whose exchange
// argument is one of fn's own parameters.
func directBindParamWrapper(fn *ssa.Function) (amqpBindWrapper, bool) {
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
			queueArg, keyArg, exArg, ok := amqpBindQueueArgs(common)
			if !ok {
				continue
			}
			exIdx, ok := paramIndex(exArg, fn)
			if !ok {
				continue // exchange is a literal here, not a param — no wrapper
			}
			info := amqpBindWrapper{fn: fn, exchangeParamIndex: exIdx, routingKeyParamIndex: -1, queueNameParamIndex: -1}
			if ki, ok := paramIndex(keyArg, fn); ok {
				info.routingKeyParamIndex = ki
			}
			if qi, ok := paramIndex(queueArg, fn); ok {
				info.queueNameParamIndex = qi
			}
			return info, true
		}
	}
	return amqpBindWrapper{}, false
}

// forwardingBindWrapper reports whether fn forwards one of its own parameters into
// a known bind wrapper's exchange slot, making fn itself a bind wrapper.
func forwardingBindWrapper(fn *ssa.Function, byFn map[*ssa.Function]amqpBindWrapper) (amqpBindWrapper, bool) {
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
			if !ok {
				continue
			}
			queueArg, keyArg, exArg, ok := bindCallArgs(common.Args, g, 0)
			if !ok {
				continue
			}
			exIdx, ok := paramIndex(exArg, fn)
			if !ok {
				continue // forwards a literal, not fn's own param — not a wrapper
			}
			info := amqpBindWrapper{fn: fn, exchangeParamIndex: exIdx, routingKeyParamIndex: -1, queueNameParamIndex: -1}
			if keyArg != nil {
				if ki, ok := paramIndex(keyArg, fn); ok {
					info.routingKeyParamIndex = ki
				}
			}
			if queueArg != nil {
				if qi, ok := paramIndex(queueArg, fn); ok {
					info.queueNameParamIndex = qi
				}
			}
			return info, true
		}
	}
	return amqpBindWrapper{}, false
}

// bindWrapperForInvoke resolves an interface method call to a bind wrapper when
// exactly one in-service wrapper carries that method name and arity. Ambiguity
// (two implementations disagreeing on parameter roles) resolves to nothing — an
// interface with two different binding implementations gives no basis to pick
// one, and guessing would fabricate topology.
func bindWrapperForInvoke(common *ssa.CallCommon, byFn map[*ssa.Function]amqpBindWrapper) (amqpBindWrapper, bool) {
	if common.Method == nil {
		return amqpBindWrapper{}, false
	}
	name := common.Method.Name()
	wantParams := len(common.Args) + 1 // + receiver

	fns := make([]*ssa.Function, 0, len(byFn))
	for fn := range byFn {
		fns = append(fns, fn)
	}
	sort.Slice(fns, func(i, j int) bool { return fns[i].String() < fns[j].String() })

	var found amqpBindWrapper
	haveOne := false
	for _, fn := range fns {
		if fn.Name() != name || len(fn.Params) != wantParams || fn.Signature.Recv() == nil {
			continue
		}
		w := byFn[fn]
		if haveOne {
			if w.exchangeParamIndex != found.exchangeParamIndex ||
				w.routingKeyParamIndex != found.routingKeyParamIndex ||
				w.queueNameParamIndex != found.queueNameParamIndex {
				return amqpBindWrapper{}, false // implementations disagree
			}
			continue
		}
		found, haveOne = w, true
	}
	return found, haveOne
}

// sortedServiceFns returns the in-service functions in a stable position order so
// every pass over them (wrapper discovery, node emission) is reproducible — Go map
// iteration order must never reach output (bug-class #2).
func sortedServiceFns(inService map[*ssa.Function]bool, fset *token.FileSet) []*ssa.Function {
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
		return fns[i].String() < fns[j].String()
	})
	return fns
}

// findExchangeParamWrappers computes the transitive set of functions whose publish
// exchange derives from one of their own parameters. Seed: functions with a direct
// amqp publish whose exchange is a bare parameter. Closure: pass-through wrappers
// that forward their own parameter into a known wrapper's exchange slot. Iteration
// is over a name-sorted function slice to a fixpoint (order-independent); a round
// cap guards mutual recursion. Mirrors go_wrapper_urls.go's findURLParamWrappers.
func findExchangeParamWrappers(inService map[*ssa.Function]bool, fset *token.FileSet) map[*ssa.Function]amqpPubWrapper {
	fns := sortedServiceFns(inService, fset)

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
