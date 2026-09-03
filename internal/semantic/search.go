package semantic

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/lordsonvimal/polyflow/internal/budget"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Search-response shaping defaults (IA §2/§3): the search surface leads with
// code-bearing nodes rather than a flow/doc flood.
const (
	SearchSnippetLines = 4 // source lines inlined per node hit
	SearchFlowCap      = 5 // flows kept in a search response (full set via flows/trace)
	SearchDocCap       = 3 // doc chunks kept in a search response
)

// exactMatchNodeCap bounds the node section when the query already landed an
// exact-label hit (see Search's hasExactNode). A caller who typed (or, more
// commonly, copied from a prior tool result) the precise symbol name doesn't
// need a 20-deep ranked list to find it — the exact hit(s) plus a couple of
// close neighbors (same name in a sibling service, an overload) cover
// disambiguation without paying for the long tail.
const exactMatchNodeCap = 5

// exactMatchFlowCap and exactMatchDocCap bound (not zero) the flow/doc
// sections on an exact-label hit. A live bench trial (2026-08-12, the
// juniper AMQP heartbeat task) showed why zeroing them outright is
// unsafe: the query "heartbeat" exact-matched an unrelated container-stats
// HTTP callback method also named Heartbeat, which won rank 0 on the
// exact-match floor even though the real target — the runner_heartbeat AMQP
// channel — only qualified as a lexical hit. That silently dropped the
// precomputed agent-to-manager flow chain that would have answered the
// query outright, forcing ~15 extra trace/grep round-trips to reconstruct
// it by hand. A small non-zero cap keeps the common case (the exact match
// really is what the caller wants) nearly as cheap as zero, while leaving a
// flow/doc escape hatch for the case it isn't.
const (
	exactMatchFlowCap = 2
	exactMatchDocCap  = 1
)

// weakNodeCap bounds the node section when no hit had a strong anchor (see
// hasStrongNodeAnchor / Response.Note). A handful of best-effort guesses is
// useful; twenty BM25 near-misses on a single common query stem is the noise
// the caller asked search to cut.
const weakNodeCap = 5

// ShapeSearchResponse applies the shared search-surface ergonomics used by both
// the CLI and the MCP tool (IA §2/§3): cap the flow/doc sections so nodes stay
// visible, and inline a few source lines per node so the first call shows code.
// Snippets are best-effort — budget.Snippet returns "" on any failure. root is
// the workspace root for relative node files ("." when the process cwd is the
// workspace). Node backfill for empty results stays with the caller (it needs
// the graph store).
func ShapeSearchResponse(resp *Response, root string, flowCap, docCap, snippetLines int) {
	if flowCap >= 0 && len(resp.Flows) > flowCap {
		resp.Flows = resp.Flows[:flowCap]
	}
	if docCap >= 0 && len(resp.Docs) > docCap {
		resp.Docs = resp.Docs[:docCap]
	}
	if snippetLines <= 0 {
		return
	}
	for i := range resp.Nodes {
		e := resp.Nodes[i].Entity
		if e.File == "" || e.Line <= 0 {
			continue
		}
		resp.Nodes[i].Snippet = budget.Snippet(root, e.File, e.Line, snippetLines)
	}
}

// Hit is one item in a typed search result section.
type Hit struct {
	Entity    Entity  `json:"entity"`
	Score     float64 `json:"score"`
	Retrieval string  `json:"retrieval"` // "exact" | "lexical" | "semantic" | "fused"
	// Snippet is a few source lines inlined at query time (IA §2) so the first
	// call shows code, not just an id. Empty when unavailable or off.
	Snippet string `json:"snippet,omitempty"`
}

// Response is the structured output of Search — typed sections for nodes,
// flow chains, and doc chunks, plus a degradation note when vector search
// is unavailable.
type Response struct {
	Nodes    []Hit  `json:"nodes"`
	Flows    []Hit  `json:"flows"`
	Docs     []Hit  `json:"docs"`
	Semantic string `json:"semantic"` // "" when active | "unavailable: <reason>"
	// Note is a caller-facing advisory about result quality, distinct from
	// Semantic (which is only about vector-arm availability). It is set when
	// no node hit had a strong anchor — no exact match, no hit corroborated
	// by both retrieval arms, nothing containing the whole query — so the
	// ranked list is a best-effort lexical guess rather than a real answer.
	Note string `json:"note,omitempty"`
}

// Searcher performs hybrid FTS+vector retrieval over the entity corpus.
// The in-memory vector matrix is loaded lazily on first search and invalidated
// (via Invalidate) after each polyflow index run.
type Searcher struct {
	Store    *Store
	Embedder Embedder            // nil → FTS-only; Semantic field carries the reason
	Synonyms map[string][]string // workspace search.synonyms

	mu       sync.RWMutex
	matIDs   []string
	matTypes []string
	mat      []float32 // n×dims row-major, loaded lazily
	dims     int
	loaded   bool
}

// NewSearcher constructs a Searcher. embedder may be nil for FTS-only operation.
func NewSearcher(store *Store, embedder Embedder, synonyms map[string][]string) *Searcher {
	return &Searcher{Store: store, Embedder: embedder, Synonyms: synonyms}
}

// Invalidate drops the cached in-memory matrix so the next Search reloads it.
// Call after a successful polyflow index run.
func (sr *Searcher) Invalidate() {
	sr.mu.Lock()
	sr.loaded = false
	sr.matIDs = nil
	sr.matTypes = nil
	sr.mat = nil
	sr.dims = 0
	sr.mu.Unlock()
}

const rrfK = 60

// retrievalQuota is how many candidates each arm pulls per entity type. It sits
// above the section cap so fusion ranks from a wider pool than it emits, while
// still guaranteeing every section can fill from either arm alone.
func retrievalQuota(limit int) int {
	if q := limit * 2; q > 50 {
		return q
	}
	return 50
}

// Search performs hybrid FTS+vector retrieval and returns typed result sections.
// limit is applied per section (nodes, flows, docs).
func (sr *Searcher) Search(ctx context.Context, q string, limit int) (Response, error) {
	if limit <= 0 {
		limit = 20
	}
	expanded := sr.expandQuery(q)
	// Each arm retrieves a quota per entity type, never a pooled window: the
	// sections are capped independently below, so a pooled window just lets the
	// most numerous type (flows) decide what the others get to show.
	perType := retrievalQuota(limit)

	// ── Lexical arm ──────────────────────────────────────────────────────────
	ftsHits, ftsErr := sr.Store.FTSSearchPerType(ctx, buildFTS5Query(expanded), perType)
	if ftsErr != nil {
		return Response{}, fmt.Errorf("fts search: %w", ftsErr)
	}

	// ── Vector arm ───────────────────────────────────────────────────────────
	semanticNote := ""
	var vHits []rawVecHit

	if sr.Embedder == nil {
		// Read the reason stamped by the indexer into the embed_status meta key.
		status := sr.Store.GetEmbedStatus(ctx)
		if status == "ok" || status == "" {
			semanticNote = "unavailable: embeddings skipped"
		} else {
			semanticNote = status
		}
	} else {
		mat, matErr := sr.ensureMatrix(ctx)
		if matErr != nil {
			semanticNote = "unavailable: " + matErr.Error()
		} else if mat.n == 0 {
			semanticNote = "unavailable: no embeddings indexed"
		} else {
			qVecs, embErr := sr.Embedder.Embed(ctx, []string{expanded})
			if embErr != nil {
				semanticNote = "unavailable: " + embErr.Error()
			} else {
				vHits = cosineTopKPerType(qVecs[0], mat, perType)
			}
		}
	}

	// ── RRF fusion ───────────────────────────────────────────────────────────
	fused := rrfFuse(ftsHits, vHits, q)

	// ── Load entity metadata ─────────────────────────────────────────────────
	allIDs := make([]string, 0, len(fused))
	for _, e := range fused {
		allIDs = append(allIDs, e.entityID)
	}
	entityMap, loadErr := sr.Store.LoadEntitiesByIDs(ctx, allIDs)
	if loadErr != nil {
		return Response{}, fmt.Errorf("load entity meta: %w", loadErr)
	}

	// ── Build typed sections ─────────────────────────────────────────────────
	// rrfFuse already sorts exact-label hits first (bug-class rule 9), so a
	// hit at rank 0 with Retrieval=="exact" means the caller's query already
	// pinpointed a real symbol. In that case the long ranked tail behind it is
	// noise the caller pays tokens for and doesn't need: cap nodes tightly.
	// Flows/docs are trimmed, not zeroed — see exactMatchFlowCap/DocCap: the
	// exact match is sometimes a same-named but unrelated symbol (a coincidence
	// on a common bare word like "heartbeat"), and zeroing flows outright then
	// silently discards the one answer that would have actually helped.
	nodeCap, flowCap, docCap := limit, limit, limit
	note := ""
	switch {
	case len(fused) > 0 && fused[0].entityType == "node" && fused[0].retrieval == "exact":
		nodeCap = min(exactMatchNodeCap, limit)
		flowCap, docCap = exactMatchFlowCap, exactMatchDocCap
	case hasNodeHit(fused) && !hasStrongNodeAnchor(fused):
		// No exact hit, nothing corroborated by both arms, nothing matching the
		// whole query — the ranked tail is a lexical guess, not an answer.
		// Trim it hard and say so rather than paying tokens for red herrings.
		nodeCap = min(weakNodeCap, limit)
		note = "no strong match for \"" + q + "\" — showing closest lexical guesses; " +
			"try a more specific name, or add kind:/service: to narrow"
	}

	resp := Response{Semantic: semanticNote, Note: note}
	nodeLim, flowLim, docLim := 0, 0, 0
	for _, e := range fused {
		if nodeLim >= nodeCap && flowLim >= flowCap && docLim >= docCap {
			break
		}
		ent := entityMap[e.entityID]
		ent.ID = e.entityID
		ent.Type = e.entityType
		hit := Hit{Entity: ent, Score: roundScore(e.score), Retrieval: e.retrieval}
		switch e.entityType {
		case "node":
			if nodeLim < nodeCap {
				resp.Nodes = append(resp.Nodes, hit)
				nodeLim++
			}
		case "flow":
			if flowLim < flowCap {
				resp.Flows = append(resp.Flows, hit)
				flowLim++
			}
		case "doc":
			if docLim < docCap {
				resp.Docs = append(resp.Docs, hit)
				docLim++
			}
		}
	}
	return resp, nil
}

// ── Matrix loading ────────────────────────────────────────────────────────────

type matrixState struct {
	ids   []string
	types []string
	mat   []float32
	dims  int
	n     int
}

func (sr *Searcher) ensureMatrix(ctx context.Context) (matrixState, error) {
	sr.mu.RLock()
	if sr.loaded {
		m := matrixState{ids: sr.matIDs, types: sr.matTypes, mat: sr.mat, dims: sr.dims, n: len(sr.matIDs)}
		sr.mu.RUnlock()
		return m, nil
	}
	sr.mu.RUnlock()

	sr.mu.Lock()
	defer sr.mu.Unlock()
	if sr.loaded { // re-check under write lock
		return matrixState{ids: sr.matIDs, types: sr.matTypes, mat: sr.mat, dims: sr.dims, n: len(sr.matIDs)}, nil
	}
	ids, types, mat, dims, err := sr.Store.LoadVectors(ctx)
	if err != nil {
		return matrixState{}, err
	}
	sr.matIDs = ids
	sr.matTypes = types
	sr.mat = mat
	sr.dims = dims
	sr.loaded = true
	return matrixState{ids: ids, types: types, mat: mat, dims: dims, n: len(ids)}, nil
}

// ── Vector search ─────────────────────────────────────────────────────────────

type rawVecHit struct {
	entityID   string
	entityType string
	rank       int // 1-based
}

func cosineTopK(qvec []float32, m matrixState, k int) []rawVecHit {
	if m.n == 0 || m.dims == 0 || len(qvec) != m.dims {
		return nil
	}
	type scored struct {
		id    string
		etype string
		score float64
	}
	scores := make([]scored, m.n)
	for i := range scores {
		vec := m.mat[i*m.dims : (i+1)*m.dims]
		scores[i] = scored{m.ids[i], m.types[i], cosine32(qvec, vec)}
	}
	// Sort: score desc; tie-break by entity ID for determinism (bug-class rule 2).
	sort.Slice(scores, func(a, b int) bool {
		if scores[a].score != scores[b].score {
			return scores[a].score > scores[b].score
		}
		return scores[a].id < scores[b].id
	})
	if k > m.n {
		k = m.n
	}
	out := make([]rawVecHit, k)
	for i := range out {
		out[i] = rawVecHit{scores[i].id, scores[i].etype, i + 1}
	}
	return out
}

// cosineTopKPerType takes the best perType entities of each type rather than
// the best perType overall, so the vector arm cannot starve a section the way a
// pooled top-k does (see Store.FTSSearchPerType). Ranks are 1-based per type,
// matching the lexical arm so RRF weighs the two symmetrically.
func cosineTopKPerType(qvec []float32, m matrixState, perType int) []rawVecHit {
	if perType <= 0 {
		return nil
	}
	all := cosineTopK(qvec, m, m.n)
	counts := make(map[string]int, len(EntityTypes))
	out := make([]rawVecHit, 0, perType*len(EntityTypes))
	for _, h := range all {
		if counts[h.entityType] >= perType {
			continue
		}
		counts[h.entityType]++
		out = append(out, rawVecHit{h.entityID, h.entityType, counts[h.entityType]})
	}
	return out
}

func cosine32(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		ai, bi := float64(a[i]), float64(b[i])
		dot += ai * bi
		na += ai * ai
		nb += bi * bi
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// ── RRF fusion ────────────────────────────────────────────────────────────────

type fusedEntry struct {
	entityID   string
	entityType string
	label      string
	score      float64
	retrieval  string
	nodeType   string
	// bothArms is true when this entity was returned by both the lexical and
	// the vector arm — a corroboration signal a single-arm RRF score cannot
	// express (every single-arm rank-1 hit scores the same 1/(k+1)).
	bothArms bool
	// fullCover is true when the entity's label contains every sub-word of a
	// multi-word query. It sorts such hits above ones that only share one
	// common word ("cancel-build" vs "do-build" for the query "do-build").
	fullCover bool
}

// hasNodeHit reports whether any fused entry is a node.
func hasNodeHit(fused []fusedEntry) bool {
	for _, e := range fused {
		if e.entityType == "node" {
			return true
		}
	}
	return false
}

// hasStrongNodeAnchor reports whether any node hit is trustworthy on its own
// terms: an exact/identifier match, a hit both retrieval arms agreed on, or a
// hit whose label contains the whole query. Its absence means the ranked list
// is a lexical guess (see Response.Note / weakNodeCap).
func hasStrongNodeAnchor(fused []fusedEntry) bool {
	for _, e := range fused {
		if e.entityType != "node" {
			continue
		}
		if e.retrieval == "exact" || e.bothArms || e.fullCover {
			return true
		}
	}
	return false
}

// declarationPriority ranks graph.NodeType strings so a real declaration
// (function/method/class/struct) outranks a pattern-derived annotation node
// minted at the same source line — e.g. a Celery `@shared_task` decorator
// mints a subscriber node at the function's own declaration line, sharing
// its exact label. Both are legitimate "exact" hits for the same query
// token, but the annotation node's FTS card is shorter (fewer meta fields),
// which BM25 rewards regardless of relevance: a synthetic node minted AT a
// declaration was outranking the declaration it describes. Declaration
// types get priority 0; everything else (including nodes with no join, like
// flows/docs) gets 1, so this only ever discriminates among competing exact
// node matches and never touches the flow/doc sections.
func declarationPriority(nodeType string) int {
	switch graph.NodeType(nodeType) {
	case graph.NodeTypeFunction, graph.NodeTypeMethod, graph.NodeTypeClass, graph.NodeTypeStruct:
		return 0
	default:
		return 1
	}
}

// exactEligible reports whether a node type may claim the exact-match floor
// at all — not merely lose a tie-break against one, as declarationPriority
// governs. A short, common identifier like "edges" or "password" is reused
// as a local variable name dozens of times across a repo (and once as a
// synthetic aggregate node, e.g. `polyflow:table:edges`); none of those
// individually name the thing a descriptive query is asking about, yet each
// is an exact label match on one query word, so the real declaration
// (filterEdgesByConfidence, CreatePassword) never even gets to compete on
// score — it loses to the exact/non-exact tier split before score is
// consulted at all. Declaration types are specific enough that an exact
// match on one is trustworthy; nodeType == "" (no join — flows/docs, or a
// caller that hasn't wired NodeType) defaults to eligible so this only ever
// narrows the floor for node hits that name a type explicitly.
func exactEligible(nodeType string) bool {
	if nodeType == "" {
		return true
	}
	switch graph.NodeType(nodeType) {
	case graph.NodeTypeFunction, graph.NodeTypeMethod, graph.NodeTypeClass, graph.NodeTypeStruct,
		graph.NodeTypeInterface, graph.NodeTypeComponent,
		// Route/handler labels ("POST /api/x/do_build") and messaging endpoints
		// are as specific as a declaration name — an exact/identifier match on
		// one names exactly one thing, so they belong on the floor too. This is
		// what lets "do-build" pin its endpoint instead of losing the
		// exact/non-exact tier split to a bare "build" token collision.
		graph.NodeTypeHTTPHandler, graph.NodeTypeHTTPClient, graph.NodeTypeRoute,
		graph.NodeTypeWorker, graph.NodeTypePublisher, graph.NodeTypeSubscriber,
		graph.NodeTypeChannel:
		return true
	default:
		return false
	}
}

// rrfFuse merges FTS and vector hit lists using Reciprocal Rank Fusion (k=60).
// Sort order: exact-match first, then declaration-priority, then score desc,
// ties broken by entity ID (bug-class rules 2 and 9: deterministic,
// exact-match floor).
func rrfFuse(ftsHits []ftsHit, vecHits []rawVecHit, q string) []fusedEntry {
	type entry struct {
		entityType string
		label      string
		nodeType   string
		ftsRank    int
		vecRank    int
	}
	combined := make(map[string]*entry)
	for _, h := range ftsHits {
		combined[h.EntityID] = &entry{
			entityType: h.EntityType,
			label:      h.Label,
			nodeType:   h.NodeType,
			ftsRank:    h.Rank,
		}
	}
	for _, h := range vecHits {
		if e, ok := combined[h.entityID]; ok {
			e.vecRank = h.rank
		} else {
			combined[h.entityID] = &entry{entityType: h.entityType, vecRank: h.rank}
		}
	}

	queryWords := identTokens(q)

	out := make([]fusedEntry, 0, len(combined))
	for id, e := range combined {
		score := 0.0
		if e.ftsRank > 0 {
			score += 1.0 / (float64(rrfK) + float64(e.ftsRank))
		}
		if e.vecRank > 0 {
			score += 1.0 / (float64(rrfK) + float64(e.vecRank))
		}
		out = append(out, fusedEntry{
			entityID:   id,
			entityType: e.entityType,
			label:      e.label,
			score:      score,
			retrieval:  retrievalLabel(e.ftsRank, e.vecRank, e.label, e.nodeType, q),
			nodeType:   e.nodeType,
			bothArms:   e.ftsRank > 0 && e.vecRank > 0,
			fullCover:  e.entityType == "node" && coversAllQueryWords(e.label, queryWords),
		})
	}

	// Stable sort: exact first, then declaration priority, score desc, then
	// entity ID for deterministic ties. Declaration priority is not gated to
	// the exact tier: a bare local variable's generic name ("edges",
	// "password") can score competitively on both FTS and vector arms purely
	// because the word is common, without ever being the thing a multi-word
	// query is actually asking about — filterEdgesByConfidence never reaches
	// the exact tier at all (its label isn't a literal query token) and still
	// needs to win on relevance over noise it never gets to out-score
	// directly, since RRF rewards a generic word's broad co-occurrence over a
	// specific compound identifier's narrower, exact-topic match.
	sort.Slice(out, func(i, j int) bool {
		ei, ej := out[i], out[j]
		iEx := ei.retrieval == "exact"
		jEx := ej.retrieval == "exact"
		if iEx != jEx {
			return iEx
		}
		// Whole-query coverage: a hit containing every query word outranks one
		// that only shares a common stem. No-op for single-word queries (both
		// fullCover are false) and when nothing covers the query (all false).
		if ei.fullCover != ej.fullCover {
			return ei.fullCover
		}
		if pi, pj := declarationPriority(ei.nodeType), declarationPriority(ej.nodeType); pi != pj {
			return pi < pj
		}
		if ei.score != ej.score {
			return ei.score > ej.score
		}
		return ei.entityID < ej.entityID
	})
	return out
}

func retrievalLabel(ftsRank, vecRank int, label, nodeType, q string) string {
	if exactEligible(nodeType) && (isExact(label, q) || identExact(label, q)) {
		return "exact"
	}
	switch {
	case ftsRank > 0 && vecRank > 0:
		return "fused"
	case ftsRank > 0:
		return "lexical"
	default:
		return "semantic"
	}
}

// isExact reports whether label is a case-sensitive exact match for the whole
// query or for any individual whitespace-separated token of the query.
//
// Case-sensitive on purpose: a caller who pastes or types a real identifier
// preserves its casing (CreateApplication, generate_invoice), while a plain
// descriptive word in a natural-language query is typically lowercase even
// when it happens to collide with an unrelated capitalized declaration's
// name — "...gin handler" incidentally matched the `Handler` struct
// case-insensitively and outranked the query's actual identifier,
// `CreateApplication`, on the exact-match floor; "user password hashing"
// matched the `User` model over the intended `CreatePassword`. Comparing
// case-sensitively treats the coincidence as what it is (a fused/lexical
// hit, not an exact one) while every existing passing case already quotes
// the identifier's real casing, so it loses nothing.
func isExact(label, q string) bool {
	if label == "" {
		return false
	}
	l := strings.TrimSpace(label)
	trimmedQ := strings.TrimSpace(q)
	if l == trimmedQ {
		return true
	}
	for _, tok := range strings.Fields(trimmedQ) {
		if l == tok {
			return true
		}
	}
	return false
}

// ── FTS5 query building ───────────────────────────────────────────────────────

// buildFTS5Query converts a natural-language query to a safe FTS5 query
// expression. It delegates to graph.FTS5PrefixQuery so both FTS paths (semantic
// entities_fts here, graph nodes_fts in SearchNodes) share one allowlist-based
// sanitiser — a blocklist miss (e.g. "build.submit" → `syntax error near "."`)
// cannot reappear in only one of them.
func buildFTS5Query(q string) string {
	return graph.FTS5PrefixQuery(q)
}

// ── Glossary expansion ────────────────────────────────────────────────────────

// expandQuery appends synonym expansions to q per the workspace search.synonyms
// map. Both the FTS query and the embedding input see the expanded text so that
// jargon terms (e.g. "Falcon") bridge to code vocabulary (e.g. "purchase").
func (sr *Searcher) expandQuery(q string) string {
	if len(sr.Synonyms) == 0 {
		return q
	}
	words := strings.Fields(strings.ToLower(q))
	var extra []string
	for _, w := range words {
		if syns, ok := sr.Synonyms[w]; ok {
			extra = append(extra, syns...)
		}
	}
	if len(extra) == 0 {
		return q
	}
	return q + " " + strings.Join(extra, " ")
}

// ── Score helpers ─────────────────────────────────────────────────────────────

func roundScore(s float64) float64 {
	return math.Round(s*1000) / 1000
}
