package semantic

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Hit is one item in a typed search result section.
type Hit struct {
	Entity    Entity  `json:"entity"`
	Score     float64 `json:"score"`
	Retrieval string  `json:"retrieval"` // "exact" | "lexical" | "semantic" | "fused"
}

// Response is the structured output of Search — typed sections for nodes,
// flow chains, and doc chunks, plus a degradation note when vector search
// is unavailable.
type Response struct {
	Nodes    []Hit  `json:"nodes"`
	Flows    []Hit  `json:"flows"`
	Docs     []Hit  `json:"docs"`
	Semantic string `json:"semantic"` // "" when active | "unavailable: <reason>"
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

// Search performs hybrid FTS+vector retrieval and returns typed result sections.
// limit is applied per section (nodes, flows, docs).
func (sr *Searcher) Search(ctx context.Context, q string, limit int) (Response, error) {
	if limit <= 0 {
		limit = 20
	}
	expanded := sr.expandQuery(q)

	// ── Lexical arm ──────────────────────────────────────────────────────────
	ftsHits, ftsErr := sr.Store.FTSSearch(ctx, buildFTS5Query(expanded), 50)
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
				vHits = cosineTopK(qVecs[0], mat, 50)
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
	resp := Response{Semantic: semanticNote}
	nodeLim, flowLim, docLim := 0, 0, 0
	for _, e := range fused {
		if nodeLim >= limit && flowLim >= limit && docLim >= limit {
			break
		}
		ent := entityMap[e.entityID]
		ent.ID = e.entityID
		ent.Type = e.entityType
		hit := Hit{Entity: ent, Score: roundScore(e.score), Retrieval: e.retrieval}
		switch e.entityType {
		case "node":
			if nodeLim < limit {
				resp.Nodes = append(resp.Nodes, hit)
				nodeLim++
			}
		case "flow":
			if flowLim < limit {
				resp.Flows = append(resp.Flows, hit)
				flowLim++
			}
		case "doc":
			if docLim < limit {
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
}

// rrfFuse merges FTS and vector hit lists using Reciprocal Rank Fusion (k=60).
// Sort order: exact-match first, then score desc, ties broken by entity ID
// (bug-class rules 2 and 9: deterministic, exact-match floor).
func rrfFuse(ftsHits []ftsHit, vecHits []rawVecHit, q string) []fusedEntry {
	type entry struct {
		entityType string
		label      string
		ftsRank    int
		vecRank    int
	}
	combined := make(map[string]*entry)
	for _, h := range ftsHits {
		combined[h.EntityID] = &entry{
			entityType: h.EntityType,
			label:      h.Label,
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
			retrieval:  retrievalLabel(e.ftsRank, e.vecRank, e.label, q),
		})
	}

	// Stable sort: exact first, score desc, then entity ID for deterministic ties.
	sort.Slice(out, func(i, j int) bool {
		ei, ej := out[i], out[j]
		iEx := ei.retrieval == "exact"
		jEx := ej.retrieval == "exact"
		if iEx != jEx {
			return iEx
		}
		if ei.score != ej.score {
			return ei.score > ej.score
		}
		return ei.entityID < ej.entityID
	})
	return out
}

func retrievalLabel(ftsRank, vecRank int, label, q string) string {
	if isExact(label, q) {
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

// isExact reports whether label is a case-insensitive exact match for the
// whole query or for any individual whitespace-separated token of the query.
func isExact(label, q string) bool {
	if label == "" {
		return false
	}
	ll := strings.ToLower(strings.TrimSpace(label))
	lq := strings.ToLower(strings.TrimSpace(q))
	if ll == lq {
		return true
	}
	for _, tok := range strings.Fields(lq) {
		if ll == tok {
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
