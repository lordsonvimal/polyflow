package semantic

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/lordsonvimal/polyflow/internal/sidecar"
)

const (
	// SidecarEmbedderID identifies the nomic-embed-text-v1.5 Q8 sidecar in the
	// embeddings table.  A changed ID triggers a full re-embed (never mix spaces).
	SidecarEmbedderID = "sidecar:nomic-embed-text-v1.5-q8"
	// SidecarEmbedderDims is the output dimensionality of nomic-embed-text-v1.5.
	SidecarEmbedderDims = 768
	// SidecarBinaryName is the executable that polyflow spawns for sidecar embedding.
	SidecarBinaryName = "polyflow-embed-sidecar"
)

// sidecarEmbedReq is the JSON request sent to the embedding sidecar.
type sidecarEmbedReq struct {
	Texts []string `json:"texts"`
}

// sidecarEmbedResp is the JSON response from the embedding sidecar.
type sidecarEmbedResp struct {
	Vectors [][]float32 `json:"vectors"`
	Error   string      `json:"error"`
}

// SidecarEmbedder produces embeddings via the polyflow-embed-sidecar subprocess.
// It reuses the V.2 sidecar IPC transport (length-prefixed JSON over stdio);
// the message schema is its own: {"texts":[...]} → {"vectors":[[...]],"error":""}.
//
// On any transport error the process is marked dead and subsequent Embed calls
// return the original error — callers surface it as a search degradation note.
type SidecarEmbedder struct {
	mu      sync.Mutex
	client  *sidecar.Client
	dead    bool
	deadErr error
}

// NewSidecarEmbedder wraps an already-started sidecar.Client.  Use
// sidecar.StartClient(SidecarBinaryName) to obtain the client; the caller is
// responsible for closing it when done (via Close).
func NewSidecarEmbedder(c *sidecar.Client) *SidecarEmbedder {
	return &SidecarEmbedder{client: c}
}

func (e *SidecarEmbedder) ID() string { return SidecarEmbedderID }
func (e *SidecarEmbedder) Dims() int  { return SidecarEmbedderDims }

// Embed sends texts to the sidecar over the length-prefixed JSON IPC channel
// and returns their float32 vectors.  On any transport or protocol error the
// sidecar is marked dead and this method returns an error for all future calls.
func (e *SidecarEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dead {
		return nil, e.deadErr
	}
	payload, err := json.Marshal(sidecarEmbedReq{Texts: texts})
	if err != nil {
		return nil, fmt.Errorf("sidecar embedder: marshal request: %w", err)
	}
	raw, err := e.client.RoundTrip(payload)
	if err != nil {
		e.dead = true
		e.deadErr = fmt.Errorf("sidecar embedder: transport: %w", err)
		return nil, e.deadErr
	}
	var resp sidecarEmbedResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		e.dead = true
		e.deadErr = fmt.Errorf("sidecar embedder: unmarshal response: %w", err)
		return nil, e.deadErr
	}
	if resp.Error != "" {
		// Application-level error from the sidecar (e.g. model not loaded).
		// Not a transport failure — don't mark dead; caller may retry.
		return nil, fmt.Errorf("sidecar embedder: %s", resp.Error)
	}
	if len(resp.Vectors) != len(texts) {
		e.dead = true
		e.deadErr = fmt.Errorf("sidecar embedder: got %d vectors for %d inputs", len(resp.Vectors), len(texts))
		return nil, e.deadErr
	}
	return resp.Vectors, nil
}

// Close shuts down the underlying sidecar process.
func (e *SidecarEmbedder) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client != nil {
		e.client.Close()
	}
}
