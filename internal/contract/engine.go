package contract

import (
	"fmt"
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
	case KindJob:
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
			env := NormalizeEnv{
				FromService: prod.Service,
				ToService:   targetSvc,
				Links:       links,
			}

			cands := filterByService(consumers, targetSvc)
			cands = filterBySameServicePolicy(cands, rule.Edge.SameService, prod.Service)
			exactIdx, normIdx := buildConsumerIndexes(cands, rule.Consumer, norms, env)

			// G.6: key_candidates fan-out — try each candidate independently
			keyCands := ParseKeyCandidates(prod.Meta["key_candidates"])
			if len(keyCands) > 0 {
				dynField := findDynamicKeyField(prod, rule.Producer)
				anyMatched := false
				for _, cand := range keyCands {
					if matchProducerWithKeyOverride(prod, rule, norms, env, exactIdx, normIdx, dynField, cand, &result) {
						anyMatched = true
						// continue: all candidates try to match (don't break on first hit)
					}
				}
				if !anyMatched {
					applyUnmatched(prod, rule, targetSvc, &result, synthSeen)
				}
				continue
			}

			matched := matchProducer(prod, rule, norms, env, exactIdx, normIdx, &result)
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
func matchProducerWithKeyOverride(
	prod *graph.Node,
	rule Rule,
	norms []Normalizer,
	env NormalizeEnv,
	exactIdx, normIdx map[string]*graph.Node,
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
		rawKey := strings.Join(rawFields, " ")

		normFields := applyNormsToFields(rawFields, norms, env)
		normKey := strings.Join(normFields, " ")

		hit, _ := findMatch(rawKey, normKey, rule.Match, exactIdx, normIdx)
		if hit == nil {
			continue
		}
		if !sameServiceAllowed(rule.Edge.SameService, prod, hit) {
			continue
		}

		edgeMeta := map[string]string{
			"confidence": graph.ConfidenceInferred,
			"via":        "branch_enum",
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
		return true
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
// one producer. Returns true if an edge was emitted.
func matchProducer(
	prod *graph.Node,
	rule Rule,
	norms []Normalizer,
	env NormalizeEnv,
	exactIdx, normIdx map[string]*graph.Node,
	result *Result,
) bool {
	for _, override := range candidateMethodOverrides(prod, rule.Producer) {
		rawFields := buildRawFields(prod, rule.Producer, override)
		rawKey := strings.Join(rawFields, " ")

		normFields := applyNormsToFields(rawFields, norms, env)
		normKey := strings.Join(normFields, " ")

		hit, confidence := findMatch(rawKey, normKey, rule.Match, exactIdx, normIdx)
		if hit == nil {
			continue
		}
		if !sameServiceAllowed(rule.Edge.SameService, prod, hit) {
			continue
		}

		edgeMeta := map[string]string{"confidence": confidence}
		for metaKey, viaValue := range rule.Edge.ViaMeta {
			if prod.Meta[metaKey] != "" {
				edgeMeta["via"] = viaValue
			}
		}
		// G.7: propagate alias/wrapper indirection from producer node to edge.
		if via := prod.Meta["via"]; via != "" && edgeMeta["via"] == "" {
			edgeMeta["via"] = via
		}

		result.Edges = append(result.Edges, graph.Edge{
			ID:         fmt.Sprintf("%s:%s->%s", rule.Edge.IDPrefix, prod.ID, hit.ID),
			From:       prod.ID,
			To:         hit.ID,
			Type:       rule.Edge.Type,
			Label:      normKey,
			Confidence: confidence,
			Meta:       edgeMeta,
		})
		return true
	}
	return false
}

// findMatch tries each tier in order against the consumer indexes and returns
// the first hit plus the corresponding confidence string.
func findMatch(rawKey, normKey string, tiers []MatchTier, exactIdx, normIdx map[string]*graph.Node) (*graph.Node, string) {
	for _, tier := range tiers {
		switch tier {
		case TierExact:
			if h, ok := exactIdx[rawKey]; ok {
				return h, graph.ConfidenceStatic
			}
		case TierNormalized:
			if h, ok := normIdx[normKey]; ok {
				return h, graph.ConfidenceInferred
			}
		case TierWildcardAnchored:
			if h := wildcardScan(normKey, normIdx); h != nil {
				return h, graph.ConfidenceInferred
			}
		}
	}
	return nil, ""
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
func partitionNodes(nodes []graph.Node, rule Rule) (producers, consumers []*graph.Node) {
	for i := range nodes {
		n := &nodes[i]
		if n.Type == rule.Producer.Node && matchesWhere(n, rule.Producer.Where) {
			producers = append(producers, n)
		}
		if n.Type == rule.Consumer.Node && matchesWhere(n, rule.Consumer.Where) {
			consumers = append(consumers, n)
		}
	}
	return
}

// matchesWhere checks a node's meta against a where gate.
// A gate value of "" means the meta key must be absent or empty.
func matchesWhere(n *graph.Node, where map[string]string) bool {
	for key, expected := range where {
		actual := n.Meta[key]
		if expected == "" {
			if actual != "" {
				return false
			}
		} else {
			if actual != expected {
				return false
			}
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

// buildConsumerIndexes builds exact (raw key) and normalized (post-normalizer)
// indexes for the given consumer nodes and the producer's NormalizeEnv.
// First-seen wins when multiple consumers share a key.
func buildConsumerIndexes(
	consumers []*graph.Node,
	spec EndpointSpec,
	norms []Normalizer,
	env NormalizeEnv,
) (exactIdx, normIdx map[string]*graph.Node) {
	exactIdx = make(map[string]*graph.Node, len(consumers))
	normIdx = make(map[string]*graph.Node, len(consumers))
	for _, c := range consumers {
		rawFields := buildRawFields(c, spec, nil)
		rawKey := strings.Join(rawFields, " ")
		if _, exists := exactIdx[rawKey]; !exists {
			exactIdx[rawKey] = c
		}
		normFields := applyNormsToFields(rawFields, norms, env)
		normKey := strings.Join(normFields, " ")
		if _, exists := normIdx[normKey]; !exists {
			normIdx[normKey] = c
		}
	}
	return
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

// wildcardScan tries wildcard_anchored matching of key against all entries in
// normIdx, requiring at least one shared concrete segment. First hit wins.
//
// Compound keys join multiple fields with " " (e.g. "POST /play/*/draw").
// Wildcard segment matching must operate only on the '/'-prefixed path portion
// so that non-path fields (e.g. the HTTP method) do not create false shared
// anchors between semantically different routes.
func wildcardScan(key string, normIdx map[string]*graph.Node) *graph.Node {
	keyPath, keyPrefix := splitAtFirstSlash(key)
	if !hasLiteralSegment(keyPath) {
		return nil
	}
	for consKey, h := range normIdx {
		consPath, consPrefix := splitAtFirstSlash(consKey)
		if keyPrefix != consPrefix {
			continue // method (or other prefix field) mismatch
		}
		if pathMatchesPattern(keyPath, consPath) {
			return h
		}
	}
	return nil
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
