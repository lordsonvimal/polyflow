package contract

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// dynamicKindFor maps a contract Kind to the ledger kind string used for
// dynamic (unresolvable) producer keys of that kind.
func dynamicKindFor(k Kind) string {
	switch k {
	case KindHTTP, KindSSE, KindGRPC, KindGraphQL:
		return "dynamic_url"
	case KindKafka, KindNATS, KindRedisPubSub:
		return "dynamic_topic"
	case KindAMQP:
		return "dynamic_url"
	case KindJob, KindSQS:
		return "dynamic_queue"
	case KindPusher, KindHub:
		return "dynamic_channel"
	case KindWebSocket:
		return "dynamic_event"
	default:
		return "dynamic_" + string(k)
	}
}

// Link runs the contract engine against all nodes, applying each rule in order.
// Tier→confidence is fixed: exact→static, normalized/wildcard_anchored→inferred.
// G.6: producers with key_dynamic meta get a dynamic_<kind> ledger entry instead
// of silently dropped (nav-link refinement). Producers with key_candidates fan out
// to N matches; each hit emits an edge with via=branch_enum at inferred confidence.
func (e *Engine) Link(nodes []graph.Node, rules []Rule, links []workspace.Link) Result {
	var result Result
	synthSeen := make(map[string]bool)

	for _, rule := range rules {
		norms := resolveNormalizers(rule.Normalizers)

		producers, consumers := partitionNodes(nodes, rule)
		producers = dedupeProducers(producers, rule.Producer)

		// The candidate set and its indexes are a pure function of
		// (targetSvc, prod.Service) within a rule — spec, norms and the
		// constant parts of env don't vary per producer. Rebuilding them for
		// every producer was ~50% of this pass (buildConsumerIndexes alone was
		// 0.6s on orion); most producers in a rule share a key, so memoize.
		type candSet struct {
			cands []*graph.Node
			idx   consumerIndexes
		}
		candCache := make(map[string]candSet)

		for _, prod := range producers {
			// G.6: dynamic key → surface to ledger, never silently drop
			if prod.Meta["key_dynamic"] == "true" {
				applyDynamicUnmatched(prod, rule, &result)
				continue
			}

			targetSvc := ""
			if rule.Producer.TargetServiceMeta != "" {
				targetSvc = prod.Meta[rule.Producer.TargetServiceMeta]
			}
			// J.2c: an unattributed relative URL cannot leave its own origin.
			if targetSvc == "" && rule.Producer.SameOriginRelative &&
				prod.Service != "" && producerKeyIsRootRelative(prod, rule.Producer) {
				targetSvc = prod.Service
			}
			env := NormalizeEnv{
				FromService: prod.Service,
				ToService:   targetSvc,
				Links:       links,
			}

			ck := targetSvc + "\x00" + prod.Service
			cs, cached := candCache[ck]
			if !cached {
				cs.cands = filterByService(consumers, targetSvc)
				cs.cands = filterBySameServicePolicy(cs.cands, rule.Edge.SameService, prod.Service)
				cs.idx = buildConsumerIndexes(cs.cands, rule.Consumer, norms, env)
				candCache[ck] = cs
			}
			cands, idx := cs.cands, cs.idx

			// A relative URL in browser-executed code resolves against the
			// origin that served the page. Prefer — rather than force — the
			// producer's own service: a JS bundle that is its own service with
			// no routes of its own is proxied to a backend, and pinning it to
			// its origin would erase a real edge. So restrict the candidate set
			// only when the own service actually answers this key, which also
			// pre-empts the tier ordering: an own-service route that would have
			// matched at the normalized tier must not lose to a foreign one
			// that happens to match at the exact tier.
			if rule.Producer.BrowserSameOrigin && targetSvc == "" && prod.Service != "" &&
				isBrowserExecuted(prod) && producerKeyIsRelativeURL(prod, rule.Producer) {
				ownIdx := buildConsumerIndexes(
					filterByService(cands, prod.Service), rule.Consumer, norms, env)
				var probe Result
				if matchProducer(prod, rule, norms, env, ownIdx, &probe) {
					idx = ownIdx
				}
			}

			// G.6: key_candidates fan-out — try each candidate independently
			keyCands := ParseKeyCandidates(prod.Meta["key_candidates"])
			if len(keyCands) > 0 {
				dynField := findDynamicKeyField(prod, rule.Producer)
				anyMatched := false
				for _, cand := range keyCands {
					if matchProducerWithKeyOverride(prod, rule, norms, env, idx, dynField, cand, &result) {
						anyMatched = true
						// continue: all candidates try to match (don't break on first hit)
					}
				}
				if !anyMatched {
					applyUnmatched(prod, rule, targetSvc, &result, synthSeen)
				}
				continue
			}

			matched := matchProducer(prod, rule, norms, env, idx, &result)
			if matched {
				continue
			}

			applyUnmatched(prod, rule, targetSvc, &result, synthSeen)
		}
	}
	return result
}

// applyDynamicUnmatched surfaces a dynamic-key producer to the ledger.
// The nav-link "drop" policy is refined: dynamic nav links reach the ledger
// rather than being silently dropped (unmatched literals still drop as before).
func applyDynamicUnmatched(prod *graph.Node, rule Rule, result *Result) {
	dynKind := dynamicKindFor(rule.Kind)
	result.Unresolved = append(result.Unresolved, graph.UnresolvedRef{
		Service: prod.Service,
		File:    prod.File,
		Line:    prod.Line,
		Name:    prod.Meta["key_dynamic_raw"],
		Kind:    dynKind,
	})
}

// findDynamicKeyField returns the first key field in spec.Key that has no
// value in prod.Meta and no valid fallback. This is the field that
// key_candidates values should be substituted for.
func findDynamicKeyField(prod *graph.Node, spec EndpointSpec) string {
	for _, field := range spec.Key {
		if prod.Meta[field] != "" {
			continue
		}
		hasFallback := false
		for _, fb := range spec.KeyFallbacks[field] {
			if prod.Meta[fb] != "" {
				hasFallback = true
				break
			}
		}
		if !hasFallback {
			return field
		}
	}
	return ""
}

// matchProducerWithKeyOverride matches a single key_candidate value by
// injecting it as an override for the dynamic field. Each hit emits an edge
// with via=branch_enum at inferred confidence (regardless of match tier).
// All consumers sharing the matched key receive an edge (recall over
// precision); the method-override loop stops at the first override that
// produced at least one edge.
func matchProducerWithKeyOverride(
	prod *graph.Node,
	rule Rule,
	norms []Normalizer,
	env NormalizeEnv,
	idx consumerIndexes,
	dynField, cand string,
	result *Result,
) bool {
	var baseOverride map[string]string
	if dynField != "" {
		baseOverride = map[string]string{dynField: cand}
	}

	for _, methodOverride := range candidateMethodOverrides(prod, rule.Producer) {
		override := mergeOverrides(baseOverride, methodOverride)
		rawFields := buildRawFields(prod, rule.Producer, override)
		normFields := applyNormsToFields(rawFields, norms, env)
		if keyVoided(rawFields, normFields) {
			continue
		}
		rawKey := strings.Join(rawFields, " ")
		normKey := strings.Join(normFields, " ")

		hits, _, matchMeta := findMatches(rawKey, normKey, rule.Match, idx)
		emitted := false
		for _, hit := range hits {
			if !sameServiceAllowed(rule.Edge.SameService, prod, hit) {
				continue
			}
			edgeMeta := map[string]string{
				"confidence": graph.ConfidenceInferred,
				"via":        "branch_enum",
			}
			for k, v := range matchMeta {
				edgeMeta[k] = v
			}
			result.Edges = append(result.Edges, graph.Edge{
				ID:         fmt.Sprintf("%s:%s->%s", rule.Edge.IDPrefix, prod.ID, hit.ID),
				From:       prod.ID,
				To:         hit.ID,
				Type:       rule.Edge.Type,
				Label:      normKey,
				Confidence: graph.ConfidenceInferred,
				Meta:       edgeMeta,
			})
			emitted = true
		}
		if emitted {
			return true
		}
	}
	return false
}

// mergeOverrides combines two override maps into one. The second (b) wins on
// conflicts. Returns nil if both are nil.
func mergeOverrides(a, b map[string]string) map[string]string {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	merged := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		merged[k] = v
	}
	for k, v := range b {
		merged[k] = v
	}
	return merged
}

// matchProducer tries all candidate (method-override, tier) combinations for
// one producer. Every consumer sharing the matched key receives an edge —
// recall over precision: two services exposing the same route, or N hub
// subscribers on one broadcast channel, all get linked instead of first-seen
// winning silently. Returns true if at least one edge was emitted; the
// method-override loop stops at the first override that produced edges.
func matchProducer(
	prod *graph.Node,
	rule Rule,
	norms []Normalizer,
	env NormalizeEnv,
	idx consumerIndexes,
	result *Result,
) bool {
	for _, override := range candidateMethodOverrides(prod, rule.Producer) {
		rawFields := buildRawFields(prod, rule.Producer, override)
		if keyIsEmpty(rawFields) {
			continue
		}
		normFields := applyNormsToFields(rawFields, norms, env)
		if keyVoided(rawFields, normFields) {
			continue
		}
		rawKey := strings.Join(rawFields, " ")
		normKey := strings.Join(normFields, " ")

		hits, confidence, matchMeta := findMatches(rawKey, normKey, rule.Match, idx)

		// Collect the hits that pass the same-service policy first, so fan-out
		// ambiguity can be judged before edges are emitted (recall is preserved:
		// every eligible hit still gets an edge — bug-class #1).
		eligible := make([]*graph.Node, 0, len(hits))
		for _, hit := range hits {
			if sameServiceAllowed(rule.Edge.SameService, prod, hit) {
				eligible = append(eligible, hit)
			}
		}
		if len(eligible) == 0 {
			continue
		}

		// Weak path evidence: the producer's host was opaque and its path pinned
		// exactly one literal segment (`c.baseURL+"/health"`), so the path is the
		// only thing naming the callee. That is real evidence when one service
		// answers to it and none at all when several do — `/health` resolves in
		// three fleet services and `/login` in two, because they name conventions
		// every service implements rather than routes. The confidence downgrade
		// below cannot express this: it still emits an edge per hit, and ten
		// `partial` edges from one call site are ten wrong answers, not a hedge.
		// So suppress here and let the call fall through to the ledger, where its
		// tree-sitter node already records the call honestly.
		//
		// This is deliberately narrower than it looks: only the Go SSA wrapper
		// pass stamps `path_evidence`, and only for the single-segment case it
		// used to drop outright. Everything it used to accept is unaffected.
		if prod.Meta["path_evidence"] == "weak" && distinctTargetServices(eligible) > 1 {
			continue
		}

		// Fan-out phantom guard: one producer call site resolves to exactly one
		// real target. If the key matched consumers across >1 distinct service,
		// at most one edge is real — downgrade confidence to `partial` (the
		// LinkDatastores multi-target idiom) so evidence fusion never promotes a
		// spec-only confirmation of such an edge to `verified`. Runtime/config,
		// which pin the concrete target, still verify it (see
		// internal/evidence/reconcile.go computeState). Same-service multi-handler
		// fan-out is left at its match confidence: the service is unambiguous.
		edgeConfidence := confidence
		if distinctTargetServices(eligible) > 1 {
			edgeConfidence = graph.ConfidencePartial
		}
		// A producer key that was itself resolved by inference cannot yield a
		// stronger edge than the inference that produced it. The AMQP
		// registration handshake (K.6) is the first such key: the field symbol
		// proves the two services agreed on a queue name, not that this call
		// site is reachable from that consumer in production, so the match tier
		// would otherwise report `exact` for a fact no source line states.
		edgeConfidence = capConfidence(edgeConfidence, prod.Meta["confidence_ceiling"])

		for _, hit := range eligible {
			edgeMeta := map[string]string{"confidence": edgeConfidence}
			for k, v := range matchMeta {
				edgeMeta[k] = v
			}
			for metaKey, viaValue := range rule.Edge.ViaMeta {
				if prod.Meta[metaKey] != "" {
					edgeMeta["via"] = viaValue
				}
			}
			// G.7: propagate alias/wrapper indirection from producer node to edge.
			if via := prod.Meta["via"]; via != "" && edgeMeta["via"] == "" {
				edgeMeta["via"] = via
			}
			// When the producer's key was not written at the call site but
			// derived (K.6: carried across repos on a handshake field symbol),
			// name the mechanism on the edge. Otherwise the node reads
			// `dynamic`, the edge reads `partial`, and nothing anywhere says
			// which field the two services agreed on — leaving an edge a reader
			// has to take on faith.
			if rv := prod.Meta["key_resolved_via"]; rv != "" {
				edgeMeta["resolved_via"] = rv
				if f := prod.Meta["broker_field"]; f != "" {
					edgeMeta["handshake_field"] = f
				}
			}

			result.Edges = append(result.Edges, graph.Edge{
				ID:         fmt.Sprintf("%s:%s->%s", rule.Edge.IDPrefix, prod.ID, hit.ID),
				From:       prod.ID,
				To:         hit.ID,
				Type:       rule.Edge.Type,
				Label:      normKey,
				Confidence: edgeConfidence,
				Meta:       edgeMeta,
			})
		}
		return true
	}
	return false
}

// ConfidenceRank orders the confidence tiers so a ceiling can be applied
// without a table of pairwise comparisons. Unknown strings rank highest so an
// unrecognised ceiling never silently weakens an edge. Exported so callers
// outside this package (e.g. `polyflow status --unknown-edges`'s
// --min-confidence flag) use the same tier order as the engine, rather than
// hand-maintaining a second one that could drift.
func ConfidenceRank(c string) int {
	switch c {
	case graph.ConfidenceUnknown:
		return 0
	case graph.ConfidencePartial:
		return 1
	case graph.ConfidenceInferred:
		return 2
	case graph.ConfidenceStatic:
		return 3
	default:
		return 4
	}
}

// capConfidence lowers conf to ceiling when ceiling is the weaker of the two.
// An empty ceiling means the producer sets no cap.
func capConfidence(conf, ceiling string) string {
	if ceiling == "" {
		return conf
	}
	if ConfidenceRank(ceiling) < ConfidenceRank(conf) {
		return ceiling
	}
	return conf
}

// distinctTargetServices counts the distinct non-empty Service values among
// hits. Nodes with an unknown service are ignored — when target identity is
// unknown the unscoped join applies (recall over precision), so it does not
// count toward fan-out ambiguity.
func distinctTargetServices(hits []*graph.Node) int {
	seen := make(map[string]struct{}, len(hits))
	for _, h := range hits {
		if h.Service != "" {
			seen[h.Service] = struct{}{}
		}
	}
	return len(seen)
}

// findMatches tries each tier in order against the consumer indexes and
// returns every consumer on the first tier that hits, plus the corresponding
// confidence string and any tier-specific match metadata. Consumers are
// returned in node-input order (stable).
func findMatches(rawKey, normKey string, tiers []MatchTier, idx consumerIndexes) ([]*graph.Node, string, map[string]string) {
	for _, tier := range tiers {
		switch tier {
		case TierExact:
			if hs := idx.exact[rawKey]; len(hs) > 0 {
				return hs, graph.ConfidenceStatic, nil
			}
		case TierNormalized:
			if hs := idx.norm[normKey]; len(hs) > 0 {
				return hs, graph.ConfidenceInferred, nil
			}
		case TierWildcardAnchored:
			if hs := wildcardScan(normKey, idx); len(hs) > 0 {
				return hs, graph.ConfidenceInferred, wildcardMatchMeta(normKey)
			}
		case TierExchangeOnly:
			if hs := exchangeOnlyScan(normKey, idx); len(hs) > 0 {
				return hs, graph.ConfidencePartial, nil
			}
		}
	}
	return nil, "", nil
}

// wildcardMatchMeta records the anchor strength of a TierWildcardAnchored
// match: the fraction of the matched key's path segments that are concrete
// (not "*"). Phase 2's measurement (docs/cross-service-edge-confidence-plan.md)
// found this population uniformly >=50% concrete-anchored across two fleets —
// well above the flat `inferred` confidence's single bucket — but also found
// no threshold split that would change *which* edges qualify, so this is
// surfaced as metadata for a caller to use rather than folded into a new
// confidence tier (the plan's open design question resolves to the
// lower-risk choice: confidence itself is unchanged by this).
func wildcardMatchMeta(key string) map[string]string {
	keyPath, _ := splitAtFirstSlash(key)
	segs := splitPath(keyPath)
	if len(segs) == 0 {
		return nil
	}
	concrete := 0
	for _, s := range segs {
		if s != "*" {
			concrete++
		}
	}
	ratio := float64(concrete) / float64(len(segs))
	return map[string]string{"anchor_ratio": strconv.FormatFloat(ratio, 'f', 2, 64)}
}

func applyUnmatched(prod *graph.Node, rule Rule, targetSvc string, result *Result, synthSeen map[string]bool) {
	switch rule.Unmatched {
	case UnmatchedUnknownEdge:
		synthID := "unresolved"
		if targetSvc != "" {
			synthID = "unresolved:" + targetSvc
		}
		if !synthSeen[synthID] {
			synthSeen[synthID] = true
			result.Nodes = append(result.Nodes, graph.Node{
				ID:    synthID,
				Type:  graph.NodeTypeService,
				Label: synthID,
				// targetSvc is the resolved consumer-side service the producer
				// tried to reach (host/link resolution), so it's a real owning
				// service when known — left "" only for the genuinely
				// unqualified "unresolved" sink, a single node deliberately
				// shared workspace-wide by every producer whose target service
				// couldn't be resolved at all (see MergeServiceDBs, which copies
				// service="" rows via INSERT OR IGNORE rather than scoping them
				// to one service).
				Service: targetSvc,
			})
		}
		rawKey := strings.Join(buildRawFields(prod, rule.Producer, nil), " ")
		result.Edges = append(result.Edges, graph.Edge{
			ID:         fmt.Sprintf("%s:%s->%s", rule.Edge.IDPrefix, prod.ID, synthID),
			From:       prod.ID,
			To:         synthID,
			Type:       rule.Edge.Type,
			Label:      rawKey,
			Confidence: graph.ConfidenceUnknown,
			Meta:       map[string]string{"confidence": graph.ConfidenceUnknown},
		})
	case UnmatchedLedger:
		rawKey := strings.Join(buildRawFields(prod, rule.Producer, nil), " ")
		result.Unresolved = append(result.Unresolved, graph.UnresolvedRef{
			Service: prod.Service,
			File:    prod.File,
			Line:    prod.Line,
			Name:    rawKey,
			Kind:    string(rule.Kind),
		})
	case UnmatchedDrop:
		// intentionally silent
	}
}

// partitionNodes separates nodes into producers and consumers for a rule.
//
// Test and spec files are admitted to neither role. A route registered inside a
// `_test.go` / `_spec.rb` wires that test's own assertions, not the running
// service, so it is not an endpoint another service can call; likewise a
// request built in a test is not a production call site. The nodes stay in the
// graph and stay searchable — only contract *link formation* skips them, the
// same line already drawn by X.9's URL synthesis, the AMQP handshake pass,
// InferLinks and route-group registrar seeding.
//
// Measured on the juniper fleet (2026-08-08): 205 http_handler nodes lived
// in test files and were being matched as real endpoints — `handlers_test.go:27`
// was linked as maple-agent's `/health` endpoint, and a settings controller test
// as maple-manager's `PUT /api/v1/settings`.
func partitionNodes(nodes []graph.Node, rule Rule) (producers, consumers []*graph.Node) {
	for i := range nodes {
		n := &nodes[i]
		if graph.IsTestFilePath(n.File) {
			continue
		}
		if n.Type == rule.Producer.Node && matchesWhere(n, rule.Producer.Where) &&
			matchesNotWhere(n, rule.Producer.NotWhere) {
			producers = append(producers, n)
		}
		if n.Type == rule.Consumer.Node && matchesWhere(n, rule.Consumer.Where) &&
			matchesNotWhere(n, rule.Consumer.NotWhere) {
			consumers = append(consumers, n)
		}
	}
	return
}

// matchesNotWhere is the negation of matchesWhere: the node is admitted only if
// none of the listed meta keys holds the excluded value. It exists because some
// node shapes are unambiguously one-sided — a Sneakers `from_queue` binding
// consumes and can never publish — while the rule that matches them has to stay
// symmetric for the shapes that genuinely are (a bunny `channel.queue(name)`
// declaration is written identically by publishers and consumers). Without it
// the queue contract emits both directions and claims a worker publishes to the
// agent that feeds it.
func matchesNotWhere(n *graph.Node, notWhere map[string]string) bool {
	for key, excluded := range notWhere {
		if n.Meta[key] == excluded {
			return false
		}
	}
	return true
}

// matchesWhere checks a node's meta against a where gate.
// A gate value of "" means the meta key must be absent or empty. A gate
// value containing "|" is an OR list of exact alternatives (e.g.
// "ws_upgrade|ws_upgrade_fastapi") — cheaper than a prefix/glob selector
// kind when the full set of alternatives is small and known (PW.1).
func matchesWhere(n *graph.Node, where map[string]string) bool {
	for key, expected := range where {
		actual := n.Meta[key]
		if expected == "" {
			if actual != "" {
				return false
			}
			continue
		}
		if strings.Contains(expected, "|") {
			matched := false
			for _, alt := range strings.Split(expected, "|") {
				if actual == alt {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
			continue
		}
		if actual != expected {
			return false
		}
	}
	return true
}

// filterBySameServicePolicy pre-filters consumers based on the same_service
// policy so that the consumer index is built only from eligible nodes. This
// prevents same-service consumers from occupying index slots that should go
// to cross-service consumers (skip policy) and vice-versa (same_service_only).
// skip_unless_meta and keep are handled by sameServiceAllowed post-lookup.
func filterBySameServicePolicy(consumers []*graph.Node, policy, prodService string) []*graph.Node {
	switch policy {
	case "skip":
		out := consumers[:0:0]
		for _, c := range consumers {
			if c.Service != prodService {
				out = append(out, c)
			}
		}
		return out
	case "same_service_only":
		out := consumers[:0:0]
		for _, c := range consumers {
			if c.Service == prodService {
				out = append(out, c)
			}
		}
		return out
	default:
		return consumers
	}
}

// filterByService returns consumers from targetSvc, or all consumers when
// targetSvc is empty (no restriction).
func filterByService(consumers []*graph.Node, targetSvc string) []*graph.Node {
	if targetSvc == "" {
		return consumers
	}
	out := consumers[:0:0]
	for _, c := range consumers {
		if c.Service == targetSvc {
			out = append(out, c)
		}
	}
	return out
}

// consumerIndexes holds the per-producer consumer lookup structures.
// Both maps are multi-valued: every consumer sharing a key is kept, in
// node-input order, so matching fans out instead of first-seen winning.
// normKeys records normalized keys in first-seen order so the wildcard
// tier scans deterministically (map iteration order is random).
type consumerIndexes struct {
	exact    map[string][]*graph.Node
	norm     map[string][]*graph.Node
	normKeys []string
}

// buildConsumerIndexes builds exact (raw key) and normalized (post-normalizer)
// indexes for the given consumer nodes and the producer's NormalizeEnv.
func buildConsumerIndexes(
	consumers []*graph.Node,
	spec EndpointSpec,
	norms []Normalizer,
	env NormalizeEnv,
) consumerIndexes {
	idx := consumerIndexes{
		exact: make(map[string][]*graph.Node, len(consumers)),
		norm:  make(map[string][]*graph.Node, len(consumers)),
	}
	for _, c := range consumers {
		rawFields := buildRawFields(c, spec, nil)
		if keyIsEmpty(rawFields) {
			continue
		}
		normFields := applyNormsToFields(rawFields, norms, env)
		if keyVoided(rawFields, normFields) {
			continue
		}
		rawKey := strings.Join(rawFields, " ")
		idx.exact[rawKey] = append(idx.exact[rawKey], c)
		normKey := strings.Join(normFields, " ")
		if _, exists := idx.norm[normKey]; !exists {
			idx.normKeys = append(idx.normKeys, normKey)
		}
		idx.norm[normKey] = append(idx.norm[normKey], c)
	}
	return idx
}

// keyIsEmpty reports whether the rule declares at least one key field and
// every one of them is the empty string on this node. Rules that key a
// broad, ungated node pool (e.g. X.2's `node: function, where: {}` for
// delayed_job's qualified-method join, where most functions never set
// qualified_name) would otherwise hash-join every unrelated node sharing an
// absent field into one giant "" bucket — a silent false-positive fan-out
// bug-class #1 exists to prevent. Nodes with an all-empty declared key are
// excluded from both producing and consuming a match; producers fall
// through to the rule's normal unmatched policy (ledger), the honest
// outcome for "the join key could not be determined" rather than "do not
// guess". `key: []` (zero declared fields) is a deliberate broadcast-to-all
// wildcard (see hub.yaml) and is never treated as empty.
func keyIsEmpty(fields []string) bool {
	if len(fields) == 0 {
		return false
	}
	for _, f := range fields {
		if f != "" {
			return false
		}
	}
	return true
}

// producerKeyIsRootRelative reports whether a producer's key is both relative
// (no "scheme://" and no protocol-relative "//host") and rootward — no path
// field carrying a segment to discriminate on. See EndpointSpec.SameOriginRelative.
func producerKeyIsRootRelative(prod *graph.Node, spec EndpointSpec) bool {
	for _, v := range buildRawFields(prod, spec, nil) {
		v = normQuoteStrip(v, NormalizeEnv{})
		if strings.Contains(v, "://") || strings.HasPrefix(v, "//") {
			return false
		}
		if strings.HasPrefix(v, "/") && len(splitPath(v)) > 0 {
			return false
		}
	}
	return true
}

// browserLanguages are the languages whose http_client nodes describe code the
// browser executes, so a relative URL in them resolves same-origin. `templ`,
// `erb` and `html` are absent deliberately: those files are server-rendered and
// mostly contain server-side calls. The browser-executed subset of them is
// reached through the datastar meta gate in isBrowserExecuted instead.
var browserLanguages = map[string]bool{
	"javascript": true,
	"typescript": true,
	"jsx":        true,
	"tsx":        true,
	"vue":        true,
	"svelte":     true,
}

// isBrowserExecuted reports whether a producer node describes a request issued
// by a browser. Three signals: the node's language is a browser bundle
// language; the call is a datastar action attribute — `data-on:input="@get('/x')"`
// in a templ/ERB template is rendered into HTML and fired by datastar in the
// page, however server-side the file around it looks; or the node is a
// navigation link.
//
// A nav link is browser-executed by construction, whatever emitted it. The
// language exclusion exists because a server-side *client's* "/api/v1/x" is a
// fragment waiting to be joined to a configured base URL, so it may well name
// another service. An `href` has no base URL to be joined to: the browser
// resolves it against the origin that served the page. Rails' `link_to
// admin_users_path` in a orion view produced five cross-service edges into
// willow, which serves a route of the same name — every one of them a page
// linking to its own app.
func isBrowserExecuted(prod *graph.Node) bool {
	if browserLanguages[strings.ToLower(prod.Language)] {
		return true
	}
	if prod.Meta["nav_link"] == "true" {
		return true
	}
	return prod.Meta["datastar"] != ""
}

// producerKeyIsRelativeURL reports whether every key field that carries a URL
// is relative — no "scheme://" and no protocol-relative "//host". Unlike
// producerKeyIsRootRelative it does NOT require the path to be segment-less: a
// browser-executed "/api/v1/users" is exactly the case this must catch. A field
// that names no path at all (a bare method) is ignored rather than treated as
// relative, so a producer with no URL evidence never gets pinned to its own
// service on the strength of its verb.
func producerKeyIsRelativeURL(prod *graph.Node, spec EndpointSpec) bool {
	sawPath := false
	for _, v := range buildRawFields(prod, spec, nil) {
		v = normQuoteStrip(v, NormalizeEnv{})
		if v == "" {
			continue
		}
		if strings.Contains(v, "://") || strings.HasPrefix(v, "//") {
			return false
		}
		if strings.HasPrefix(v, "/") {
			sawPath = true
		}
	}
	return sawPath
}

// dedupeProducers collapses producer nodes that describe the *same call site*.
//
// The Go SSA wrapper pass (go_wrapper_urls.go) adds a resolved node without
// superseding the raw tree-sitter node it was derived from, so one call site
// yields two producers at the same file:line with the same key — e.g.
// build_api_client.go:120 emits both `http_new_request:120` (url "*/health",
// ungraded) and `HealthCheck:120` (path "*/health", path_evidence "weak",
// confidence_ceiling "partial").
//
// That is not merely redundant, it is a *bypass*: matchProducer suppresses a
// weak-path producer that fans out across services, but the ungraded twin
// carries no path_evidence and sails straight through the guard. On the
// nine-service fleet three `/health` call sites each produced three
// cross-service edges — one right, two wrong — entirely through the twin.
//
// Keeping the richer node preserves every grading the resolvers computed; the
// dropped twin adds no information by construction, since the pair share a key.
// Only contract link formation is affected — both nodes stay in the graph and
// stay searchable, the same line partitionNodes already draws for test files.
func dedupeProducers(producers []*graph.Node, spec EndpointSpec) []*graph.Node {
	if len(producers) < 2 {
		return producers
	}
	bestAt := make(map[string]int, len(producers))
	keep := make([]bool, len(producers))
	for i, p := range producers {
		k := p.Service + "\x00" + p.File + "\x00" + strconv.Itoa(p.Line) + "\x00" +
			strings.Join(buildRawFields(p, spec, nil), " ")
		prev, seen := bestAt[k]
		if !seen {
			bestAt[k] = i
			keep[i] = true
			continue
		}
		if producerEvidenceScore(p) > producerEvidenceScore(producers[prev]) {
			keep[prev] = false
			keep[i] = true
			bestAt[k] = i
		}
	}
	out := make([]*graph.Node, 0, len(producers))
	for i, p := range producers {
		if keep[i] {
			out = append(out, p)
		}
	}
	return out
}

// producerEvidenceScore ranks two nodes describing one call site by how much
// resolver evidence they carry. Graded path evidence outranks everything: it is
// the signal matchProducer's fan-out suppression reads, and losing it is what
// made the duplicate a bypass rather than a duplicate.
func producerEvidenceScore(n *graph.Node) int {
	score := 0
	if n.Meta["path_evidence"] != "" {
		score += 4
	}
	if n.Meta["confidence_ceiling"] != "" {
		score += 2
	}
	if n.Meta["via_wrapper"] != "" || n.Meta["synthesized"] != "" {
		score++
	}
	return score
}

// keyVoided reports whether a guard normalizer (empty_path_guard,
// shared_anchor_guard) blanked a key field that had a value — the chain's way of
// saying "this field carries no routing information".
//
// It must be judged per field, not on the joined key: a voided path leaves a
// non-empty method behind, so `keyIsEmpty` never fires and the pair
// (producer "get ", root handler "get ") would meet on the very emptiness the
// guard introduced. Voiding is therefore applied symmetrically — the producer
// skips matching and falls to its `unmatched` policy, and the consumer is left
// out of both indexes.
func keyVoided(rawFields, normFields []string) bool {
	for i := range rawFields {
		if rawFields[i] != "" && normFields[i] == "" {
			return true
		}
	}
	return false
}

// buildRawFields extracts the key field values from a node's meta,
// applying key_fallbacks and any per-field overrides (used for method_fallback).
func buildRawFields(n *graph.Node, spec EndpointSpec, overrides map[string]string) []string {
	fields := make([]string, len(spec.Key))
	for i, field := range spec.Key {
		if overrides != nil {
			if v, ok := overrides[field]; ok {
				fields[i] = v
				continue
			}
		}
		val := n.Meta[field]
		if val == "" {
			for _, fb := range spec.KeyFallbacks[field] {
				if v := n.Meta[fb]; v != "" {
					val = v
					break
				}
			}
		}
		fields[i] = val
	}
	return fields
}

// applyNormsToFields applies the normalizer chain to each field independently.
func applyNormsToFields(fields []string, norms []Normalizer, env NormalizeEnv) []string {
	result := make([]string, len(fields))
	copy(result, fields)
	for i, v := range result {
		for _, norm := range norms {
			v = norm(v, env)
		}
		result[i] = v
	}
	return result
}

// candidateMethodOverrides returns the set of field overrides to try for a
// producer. When method_fallback is set and the method meta field is empty,
// each fallback method is tried as a separate override. Otherwise a single
// nil override (use meta as-is) is returned.
func candidateMethodOverrides(n *graph.Node, spec EndpointSpec) []map[string]string {
	if len(spec.MethodFallback) == 0 {
		return []map[string]string{nil}
	}
	hasMethodField := false
	for _, f := range spec.Key {
		if f == "method" {
			hasMethodField = true
			break
		}
	}
	if !hasMethodField {
		return []map[string]string{nil}
	}
	if n.Meta["method"] != "" {
		return []map[string]string{nil}
	}
	overrides := make([]map[string]string, len(spec.MethodFallback))
	for i, m := range spec.MethodFallback {
		overrides[i] = map[string]string{"method": m}
	}
	return overrides
}

// sameServiceAllowed checks whether the same-service policy permits emitting
// an edge between prod and cons.
//
// Policies: "skip" (only cross-service), "keep" (both), "same_service_only"
// (only within-service), "skip_unless_meta:<key>" (skip same-service unless
// producer meta key is set; cross-service always allowed).
func sameServiceAllowed(policy string, prod, cons *graph.Node) bool {
	sameService := prod.Service == cons.Service
	switch {
	case policy == "skip":
		return !sameService
	case policy == "keep":
		return true
	case policy == "same_service_only":
		return sameService
	case strings.HasPrefix(policy, "skip_unless_meta:"):
		key := strings.TrimPrefix(policy, "skip_unless_meta:")
		return !sameService || prod.Meta[key] != ""
	default:
		return true
	}
}

// wildcardScan tries wildcard_anchored matching of key against all indexed
// normalized keys, requiring at least one shared concrete segment. Keys are
// scanned in first-seen (node-input) order — never map order — so results
// are deterministic across runs. Every consumer under every matching key is
// returned (recall over precision).
//
// Compound keys join multiple fields with " " (e.g. "POST /play/*/draw").
// Wildcard segment matching must operate only on the '/'-prefixed path portion
// so that non-path fields (e.g. the HTTP method) do not create false shared
// anchors between semantically different routes.
//
// Most-specific match wins: when a wildcarded key matches several routes, only
// those sharing the *most* non-boilerplate literal segments with the key are
// kept. `/app/*/*/actions` (JS `"/app/"+objectType+"/"+id+"/actions"`) matches
// both `/app/folders/*/actions` (anchors on `app` + `actions`) and
// `/app/impact_analyses/*/*` (anchors on `app` alone, with the key's literal
// `actions` falling opposite a route param) — keeping only the higher-scoring
// set drops the second, which is how Rails routing itself would dispatch.
func wildcardScan(key string, idx consumerIndexes) []*graph.Node {
	keyPath, keyPrefix := splitAtFirstSlash(key)
	if !hasLiteralSegment(keyPath) {
		return nil
	}
	type scoredKey struct {
		consKey string
		score   int
	}
	var matched []scoredKey
	best := 0
	for _, consKey := range idx.normKeys {
		consPath, consPrefix := splitAtFirstSlash(consKey)
		if keyPrefix != consPrefix {
			continue // method (or other prefix field) mismatch
		}
		if pathMatchesPattern(keyPath, consPath) {
			s := wildcardAnchorScore(keyPath, consPath)
			matched = append(matched, scoredKey{consKey, s})
			if s > best {
				best = s
			}
		}
	}
	var hits []*graph.Node
	for _, m := range matched {
		if m.score < best {
			continue
		}
		hits = append(hits, idx.norm[m.consKey]...)
	}
	return hits
}

// exchangeOnlyScan matches on the first key field alone (the exchange) when the
// rest of the key — the routing key — constrains nothing on at least one side.
// It is the last-resort tier for AMQP: a publish whose routing key could not be
// resolved, or a fanout exchange bound with `#`, still names its exchange, and an
// exchange is a real rendezvous. Both sides keeping a concrete, differing routing
// key is *not* a match: that is two distinct topics on one exchange.
//
// Keys are scanned in first-seen (node-input) order — never map order — so
// results are deterministic across runs.
func exchangeOnlyScan(key string, idx consumerIndexes) []*graph.Node {
	prodHead, prodRest, ok := splitFirstField(key)
	if !ok || prodHead == "" {
		return nil
	}
	prodOpen := topicIsOpen(prodRest)
	var hits []*graph.Node
	for _, consKey := range idx.normKeys {
		consHead, consRest, ok := splitFirstField(consKey)
		if !ok || consHead != prodHead {
			continue
		}
		if !prodOpen && !topicIsOpen(consRest) {
			continue
		}
		hits = append(hits, idx.norm[consKey]...)
	}
	return hits
}

// splitFirstField splits a compound key into its first field and the remainder.
// ok=false for a single-field key, where the tier would degenerate into the
// normalized tier and match nothing new.
func splitFirstField(key string) (head, rest string, ok bool) {
	i := strings.Index(key, " ")
	if i < 0 {
		return "", "", false
	}
	return key[:i], key[i+1:], true
}

// splitAtFirstSlash splits a compound key at the first '/' occurrence.
// Returns (whole, "") when there is no '/' or it is the leading character
// (path-only keys like "/users"). Otherwise returns (key[i:], key[:i])
// where i is the position of the first '/'.
func splitAtFirstSlash(key string) (path, prefix string) {
	i := strings.Index(key, "/")
	if i <= 0 {
		return key, ""
	}
	return key[i:], key[:i]
}

// resolveNormalizers converts a list of normalizer names into functions.
// Names are validated at Load time, so panic here would indicate a bug.
func resolveNormalizers(names []string) []Normalizer {
	fns := make([]Normalizer, len(names))
	for i, name := range names {
		fn, ok := normRegistry[name]
		if !ok {
			panic(fmt.Sprintf("contract: normalizer %q not in registry (should have been caught by Load)", name))
		}
		fns[i] = fn
	}
	return fns
}
