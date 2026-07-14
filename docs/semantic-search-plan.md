# Polyflow — Semantic Flow Retrieval Plan

Status legend: `pending` · `in progress` · `done (commit <sha>)`

> **Prerequisites.** S.0–S.2 need only the current graph + FTS search. The
> flow-chain corpus (S.1) reuses the existing `internal/trace` chain assembly.
> S.3's sidecar upgrade reuses the sidecar IPC pinned in
> `docs/versioning-matrix-plan.md` (V.2). S.4 needs the eval harness
> (`docs/goal-completion-plan.md` Tier E).

## Context

**Why.** Retrieval today is keyword FTS: it requires lexical contact between
the query and code vocabulary. Agents route around this by iterating synonym
guesses (2–4 cheap search rounds); humans on the web UI cannot — "checkout"
finds nothing when the code says `PurchaseHandler`, and no surface can return
a *flow* ("show me the checkout flow end to end") as the answer unit. This
plan adds hybrid lexical+semantic retrieval over nodes, **flow chains**, and
doc text so both humans and agents get from natural language to the right
graph entity in one call — with the flow itself always coming from the graph,
never from the retriever.

**The trust rule that shapes everything:** semantic matching is
**candidate-generation only**. An embedding hit is a pointer with a labeled
retrieval confidence; the returned flow/impact is exactly as trustworthy as
its graph edges. Retrieval can rank wrong; it must never *assert* wrong.

**Locked decisions (user session, 2026-07-15):**
1. **Local-only by default; API opt-in.** The default path never sends code
   text off-machine. An OpenAI-compatible endpoint (incl. self-hosted Ollama)
   is workspace-config opt-in.
2. **Pure-Go static embedder as the shipped default, model embedded in the
   binary** (int8-quantized, ~8–12MB via `go:embed`; zero setup, works
   air-gapped). Quality upgrades behind one interface: a llama.cpp embedding
   sidecar (nomic-embed-text GGUF), then the API endpoint.
3. **Corpus = nodes + flow chains + docs/comments.** Doc text is what bridges
   org jargon ("Falcon") to code.
4. **One hybrid search everywhere** — the same FTS∪vector fused search backs
   the CLI, the MCP `search` tool, and the web UI. Typed result sections.
5. **Embeddings computed during `polyflow index`**, content-hash gated
   (incremental runs re-embed only changed entities), `--no-embed` to skip.
6. **No vector DB.** Vectors are BLOBs in the existing SQLite
   (`modernc.org/sqlite` is pure-Go — C extensions like sqlite-vec are not an
   option anyway); query-time search is exact in-memory brute force. At
   polyflow scale (≤100k entities) that is <10ms and *exact* — approximate
   indexes earn their complexity only above ~1M vectors.

Follows the repo per-phase process (`docs/phases.md`). Graph outputs are
unchanged — this plan only changes how entities are *found*.

---

## Core model

### Retrieval pipeline

```
query text ──┬─> FTS5/BM25 (existing) ── top 50 ──┐
             │                                     ├─ RRF fusion (k=60),
             └─> Embedder.Embed(query) ──> cosine  │  dedupe by entity id
                 vs in-memory matrix ──── top 50 ──┘
                                │
             typed sections: nodes[] · flows[] · docs[]
             each hit: {entity, score, retrieval: exact|lexical|semantic|fused}
```

- **Glossary expansion:** workspace `search.synonyms`
  (`checkout: [falcon, purchase]`) expands both the FTS query and the
  embedding input text. The escape hatch for vocabulary that exists only in
  people's heads.
- **Degradation:** embeddings unavailable (skipped, stale embedder, corrupt
  table) → search runs FTS-only and the response carries
  `"semantic": "unavailable: <reason>"` — visible, never silently degraded.
- **Chain hits are entry points:** a `flows[]` hit returns the chain's entry
  node + member ids; the UI/agent then renders/traverses the *live* graph
  from there (the chain document is a retrieval index, not a stored answer).

### Pinned Go surface

```go
// internal/semantic/embedder.go
type Embedder interface {
    // ID uniquely identifies model+version+quantization, e.g.
    // "static-v1-int8" | "sidecar:nomic-embed-text-v1.5-q8" |
    // "endpoint:text-embedding-3-small". Stored with every vector;
    // a changed ID invalidates and re-embeds (never mix spaces).
    ID() string
    Dims() int
    Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// internal/semantic/corpus.go — what gets embedded.
type Entity struct {
    ID          string // node id | "chain:"+entryNodeID+":"+memberHash | "doc:"+file+":"+chunkN
    Type        string // "node" | "flow" | "doc"
    Text        string // the card/document (see corpus builders, S.1)
    ContentHash string // hash of Text — the incremental gate
    // Anchors back into the graph:
    NodeID  string   // node/doc: nearest node or ""; flow: entry node
    Members []string // flow only: ordered member node ids (≤12)
    File    string
    Line    int
}

// internal/semantic/search.go — the one search everyone calls.
type Hit struct {
    Entity    Entity
    Score     float64 // fused RRF score
    Retrieval string  // "exact" (FTS exact token) | "lexical" (BM25) |
                      // "semantic" (vector-only) | "fused" (both)
}
type Response struct {
    Nodes, Flows, Docs []Hit
    Semantic           string // "" when active | "unavailable: <reason>"
}
func Search(ctx context.Context, q string, limit int) (Response, error)
```

Storage (`SchemaVersion` bump):

```sql
CREATE TABLE IF NOT EXISTS embeddings (
  entity_id    TEXT PRIMARY KEY,
  entity_type  TEXT NOT NULL,          -- node | flow | doc
  content_hash TEXT NOT NULL,
  embedder_id  TEXT NOT NULL,
  dims         INTEGER NOT NULL,
  vector       BLOB NOT NULL,          -- little-endian float32[dims]
  meta         TEXT NOT NULL DEFAULT '{}'  -- anchors: node_id, members, file, line
);
```

### Size & accuracy budget (honest numbers)

- Binary: +8–12MB (int8 static model via `go:embed`; fp32 would be ~30MB —
  rejected). Vectors: 256-dim float32 ≈ 1KB/entity → 50k entities ≈ 51MB
  RAM at query time, ~51MB in SQLite (or half that if S.2 lands the optional
  int8 vector storage).
- Accuracy is **measured, not promised** (S.4): paper MTEB numbers do not
  transfer to "short NL query → code entity". Targets on the Tier E NL
  cases: hybrid recall@10 **≥ 0.80** with the static default, **≥ 0.90**
  with the sidecar embedder. If static misses its bar, this doc records it
  and the sidecar becomes the documented recommendation — no silent
  quality claims.

---

## Phases (one commit each)

### Phase S.0 — Embedder interface + static embedder + storage `pending`

**Problem.** Nothing can produce or store a vector.

**Deliverable.**
- `internal/semantic/{embedder.go,static.go,store.go}`: the pinned interface;
  the pure-Go static embedder — WordPiece tokenizer (~100 lines) + embedded
  int8 token-embedding matrix + per-token scales, mean-pooled and
  L2-normalized (model2vec/potion-style distillation of a bge-class teacher,
  MIT-licensed weights, 256-dim; conversion script under `tools/embed-model/`
  documents provenance + license and emits the `go:embed` binary blob).
- `embeddings` table (pinned above) + batch upsert/load; `SchemaVersion` bump.
- Index integration: after linking, embed entities whose `content_hash`
  changed or whose `embedder_id` differs (`--no-embed` skips; skipping stamps
  the degradation reason).

**Tests.** Tokenizer golden tests; embedding determinism (same text → same
vector); quantization sanity (cosine(int8 path, fp32 reference) > 0.99 on a
fixture set); hash-gate test (unchanged text → no re-embed); offline test
(binary embeds with no network).

**Acceptance.** `polyflow index` on this repo embeds all node cards in one
pass; a second run re-embeds zero; `--no-embed` indexes identically fast.

### Phase S.1 — Corpus builders (nodes, chains, docs) `pending`

**Problem.** What text represents an entity decides retrieval quality more
than the model does.

**Deliverable.** `internal/semantic/corpus.go` builders:
- **Node cards:** `label | type | service | file path | route/signature meta`
  — one line, e.g. `handlePurchase http_handler api internal/api/purchase.go
  POST /orders`.
- **Flow-chain documents:** reuse `internal/trace` chain assembly from each
  entrypoint root (routes, subscribers, jobs); document = ordered member
  labels + route paths + component names + doc-comments of members, capped
  at 12 nodes/chain; `Members` carries the ids. Chains regenerate when any
  member's file hash changed.
- **Doc chunks:** README/markdown files + extracted code doc-comments,
  ~200-token chunks split on headers/paragraphs, anchored `file:line`,
  associated to the containing service (and nearest node for doc-comments).

**Tests.** One golden test per builder on fixtures; chain-cap test; a
jargon fixture (README says "Falcon handles purchases", code says
`PurchaseHandler`) asserting the doc chunk carries both terms.

**Acceptance.** `polyflow index` on chessleap produces all three entity
types with sane counts (spot-checked in the phase note).

### Phase S.2 — Hybrid search everywhere `pending`

**Problem.** Retrieval exists in pieces; nothing fuses or serves it.

**Deliverable.** `internal/semantic/search.go` (pinned `Search`): FTS top-50
∪ cosine top-50, RRF k=60, dedupe, typed sections, `retrieval` labels,
glossary expansion (`search.synonyms` workspace key), degradation note.
Wire it as **the** search: CLI `polyflow search`, MCP `search` tool (same
JSON; tool description gains: *"query may be natural language; results
include flows — a flows hit's entry node is the starting point for trace"*),
and the web UI search endpoint. In-memory matrix loaded lazily on first
search, invalidated on index.

**Worked example** (fixture: chessleap + the S.1 jargon README):

```
polyflow search "checkout flow" --format json
{
  "flows": [{"entity": {"id": "chain:route:POST /orders:8f3a", "type": "flow",
              "node_id": "route:POST /orders",
              "members": ["route:POST /orders", "fn:handlePurchase",
                          "component:OrderSummary", "publisher:orders.created"]},
             "score": 0.031, "retrieval": "semantic"}],
  "nodes": [{"entity": {"id": "fn:handlePurchase", "type": "node",
              "file": "internal/api/purchase.go", "line": 41},
             "score": 0.028, "retrieval": "fused"}],
  "docs":  [{"entity": {"id": "doc:README.md:3", "type": "doc",
              "file": "README.md", "line": 27},
             "score": 0.016, "retrieval": "semantic"}],
  "semantic": ""
}
```

(No lexical overlap between "checkout" and any hit — the semantic path and
the README bridge carry it.)

**Tests.** Fusion unit tests (RRF math, dedupe, label assignment); the
no-lexical-overlap fixture above; degradation test (`--no-embed` index →
FTS results + `"semantic": "unavailable: embeddings skipped"`); glossary
test; MCP round-trip.

**Acceptance.** The worked example passes verbatim as a golden test; UI
search box hits the same endpoint (manual check, screenshot in phase note).

### Phase S.3 — Embedder upgrade ladder (sidecar + endpoint) `pending`

**Problem.** Static embeddings are the floor; quality-sensitive users need
the ladder without polyflow ever defaulting to it.

**Deliverable.**
- `internal/semantic/sidecar.go`: llama.cpp-based embedding sidecar speaking
  the V.2 length-prefixed JSON IPC (`{"texts": […]}` →
  `{"vectors": [[…]], "error": ""}`), model nomic-embed-text-v1.5 GGUF
  (downloaded by `polyflow models pull`, sha256-pinned — the sidecar is the
  one path with a download, and it is explicit).
- `internal/semantic/endpoint.go`: OpenAI-compatible `/v1/embeddings` client
  (covers Ollama + hosted APIs).
- Workspace config: `search.embedder: static | sidecar | endpoint` (+
  endpoint URL/model/key-env). Changed `Embedder.ID()` → full re-embed on
  next index (never mix vector spaces; test-pinned).

**Tests.** IPC round-trip with a stub sidecar; endpoint client against a
mock; space-mixing guard (two IDs in the table → error naming the fix).

**Acceptance.** Switching `static → sidecar` on a fixture re-embeds
everything once and search still passes the S.2 goldens.

### Phase S.4 — Measured accuracy + benchmark arm `pending`

**Problem.** Every quality claim so far is a design estimate.

**Deliverable.**
- **NL cases in the Tier E corpus** (`docs/goal-completion-plan.md`): per
  repo, ~10 cases `{query: "checkout flow", expect_any_of: [entity ids],
  section: flows|nodes}`; scorer reports recall@10 per embedder tier and
  writes it into `eval/baseline.json`.
- **P.1 third arm:** the agent benchmark runs MCP-with-semantic vs
  MCP-FTS-only vs no-MCP, isolating what anchoring is worth in tokens per
  model tier.
- This doc's accuracy section updated with the measured numbers (including
  a miss, if static lands under 0.80 — that outcome changes the default
  recommendation, not the wording of the result).

**Acceptance.** `polyflow eval` prints semantic recall@10 per tier;
baseline committed.

---

## Key files

- **New:** `internal/semantic/{embedder.go,static.go,corpus.go,search.go,
  store.go,sidecar.go,endpoint.go}`, `tools/embed-model/` (conversion +
  provenance), embedded model blob, `cmd/polyflow` search command update,
  `testdata/semantic/` fixtures.
- **Modify:** `internal/graph/store.go` (embeddings table; `SchemaVersion`),
  `internal/indexer/indexer.go` (embed pass after linking),
  `internal/mcpserver/` (search tool JSON + description),
  `internal/workspace/config.go` (`search.embedder`, `search.synonyms`),
  web UI search endpoint, `docs/goal-completion-plan.md` (Tier E NL cases,
  P.1 arm).

## Reuse (don't rebuild)

- FTS5 search + `internal/trace` chain assembly + `FileHash` content-hash
  gating + `modernc.org/sqlite` store + V.2 sidecar IPC + Tier E scorer.

## Verification

- Per-phase tests above; plus: **index-time budget** — full embed of this
  repo ≤ 10% over current index time, incremental ≤ 2s (`BenchmarkIndexCold`
  held); **RAM budget** — matrix load ≤ 100MB at 100k entities; the S.2
  no-lexical-overlap golden is the permanent regression test for the
  feature's reason to exist.

## Risks / honest boundaries

- **Static-embedding quality is the open question.** The entire design
  hedges it: hybrid fusion keeps BM25's exact-match floor, S.4 measures it,
  and the ladder upgrades it. If static lands under target, the cost is a
  documented "install the sidecar for best results," not a broken feature.
- **Retrieval can rank the wrong entity.** By design it only *suggests*;
  `retrieval` labels + graph-anchored results keep it from asserting.
  Never feed semantic scores into edge confidence.
- **Binary size** +8–12MB for every user including those who never search
  semantically — accepted deliberately (user decision) for zero-setup and
  air-gapped operation.
- **Embedded-model licensing/provenance** must be pinned in
  `tools/embed-model/` (MIT-licensed weights only); the model is an asset
  with a version, not a magic constant.
- **Chains explode on hub topologies** (one hub → hundreds of chains);
  the 12-node cap + per-entrypoint dedupe bound it; S.1 asserts counts on
  chessleap.

## Relationship to the other plans

- **goal-completion** — this plan is its retrieval tier; Tier E hosts the
  accuracy cases; P.1 gains the third arm. Complements Tier A: A makes
  results *trustworthy to read*, this makes them *findable*.
- **versioning-matrix** — S.3 reuses the V.2 sidecar IPC verbatim.
- **evidence-fusion / runtime** — untouched; retrieval never alters edge
  confidence or verification state.

## Sequencing

```
S.0 (embedder+store) ─> S.1 (corpus) ─> S.2 (hybrid search everywhere)
                                             ├─> S.3 (sidecar/endpoint ladder)
                                             └─> S.4 (measured accuracy + P.1 arm, needs Tier E)
```
