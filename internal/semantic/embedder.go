// Package semantic provides hybrid lexical+semantic retrieval over polyflow
// graph entities (nodes, flow chains, doc chunks).
//
// S.0: Embedder interface + static embedder + DB storage.
// S.1: Corpus builders (node cards, flow chains, doc chunks).
// S.2: Hybrid fused search (FTS ∪ vector, RRF).
package semantic

import "context"

// Embedder produces dense float32 vectors for text inputs.
//
// ID uniquely identifies model+version+quantization (e.g.
// "static-v1-int8", "sidecar:nomic-embed-text-v1.5-q8",
// "endpoint:text-embedding-3-small"). The ID is stored with every vector;
// a changed ID triggers a full re-embed so vector spaces are never mixed.
type Embedder interface {
	ID() string
	Dims() int
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Entity is the unit of retrieval: one entry in the embedding corpus.
// S.0 only embeds nodes; S.1 adds flow chains and doc chunks.
type Entity struct {
	ID          string // node id | "chain:"+entryNodeID+":"+memberHash | "doc:"+file+":"+chunkN
	Type        string // "node" | "flow" | "doc"
	Text        string // the card/document text (corpus builder fills this)
	ContentHash string // sha256 hex of Text — the incremental gate
	// Anchors back into the graph:
	NodeID  string   // node/doc: nearest node or ""; flow: entry node
	Members []string // flow only: ordered member node ids (≤ 12)
	File    string
	Line    int
}

// EmbeddingMeta holds the stored metadata for one entity, used to decide
// whether re-embedding is necessary (without loading the full vector).
type EmbeddingMeta struct {
	EntityID    string
	EmbedderID  string
	ContentHash string
}
