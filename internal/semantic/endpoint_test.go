package semantic_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/semantic"
)

// mockEmbeddingServer starts an httptest server that returns deterministic
// embeddings of the given dims for each input text.
func mockEmbeddingServer(t *testing.T, dims int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		type dataItem struct {
			Object    string    `json:"object"`
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}
		data := make([]dataItem, len(req.Input))
		for i := range req.Input {
			v := make([]float32, dims)
			for d := range v {
				v[d] = float32(i+1) * 0.001
			}
			data[i] = dataItem{Object: "embedding", Embedding: v, Index: i}
		}
		resp := map[string]any{
			"object": "list",
			"data":   data,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
}

func TestEndpointEmbedder_RoundTrip(t *testing.T) {
	srv := mockEmbeddingServer(t, 4)
	defer srv.Close()

	emb := semantic.NewEndpointEmbedder(srv.URL, "test-model", "")
	if emb.ID() != "endpoint:test-model" {
		t.Errorf("ID() = %q, want endpoint:test-model", emb.ID())
	}

	texts := []string{"hello", "world"}
	vecs, err := emb.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("want 2 vectors, got %d", len(vecs))
	}
	if len(vecs[0]) != 4 {
		t.Fatalf("want 4-dim vector, got %d", len(vecs[0]))
	}
	// Index 0 → 0.001, index 1 → 0.002
	if vecs[0][0] != 0.001 {
		t.Errorf("vecs[0][0] = %v, want 0.001", vecs[0][0])
	}
	if vecs[1][0] != 0.002 {
		t.Errorf("vecs[1][0] = %v, want 0.002", vecs[1][0])
	}
}

func TestEndpointEmbedder_DefaultModel(t *testing.T) {
	srv := mockEmbeddingServer(t, 4)
	defer srv.Close()

	emb := semantic.NewEndpointEmbedder(srv.URL, "", "") // empty model → default
	if emb.ID() != "endpoint:nomic-embed-text" {
		t.Errorf("ID() = %q, want endpoint:nomic-embed-text", emb.ID())
	}
}

func TestEndpointEmbedder_DimsZero(t *testing.T) {
	emb := semantic.NewEndpointEmbedder("http://unused", "test", "")
	if emb.Dims() != 0 {
		t.Errorf("Dims() = %d, want 0 (unknown until first call)", emb.Dims())
	}
}

func TestEndpointEmbedder_APIKeyFromEnv(t *testing.T) {
	const envVar = "TEST_POLYFLOW_EMBED_KEY"
	const key = "sk-test-1234"
	t.Setenv(envVar, key)

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var req struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		data := make([]map[string]any, len(req.Input))
		for i := range req.Input {
			data[i] = map[string]any{"object": "embedding", "embedding": []float32{0.1}, "index": i}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data}) //nolint:errcheck
	}))
	defer srv.Close()

	emb := semantic.NewEndpointEmbedder(srv.URL, "test", envVar)
	_, err := emb.Embed(context.Background(), []string{"ping"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if gotAuth != "Bearer "+key {
		t.Errorf("Authorization header = %q, want Bearer %s", gotAuth, key)
	}
}

func TestEndpointEmbedder_NoKeyEnv(t *testing.T) {
	// When keyEnv is empty, no Authorization header is sent.
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var req struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		data := []map[string]any{{"object": "embedding", "embedding": []float32{0.1}, "index": 0}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data}) //nolint:errcheck
	}))
	defer srv.Close()

	emb := semantic.NewEndpointEmbedder(srv.URL, "test", "")
	_, err := emb.Embed(context.Background(), []string{"ping"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("expected no Authorization header, got %q", gotAuth)
	}
}

func TestEndpointEmbedder_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"quota exceeded","type":"rate_limit_error"}}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()

	emb := semantic.NewEndpointEmbedder(srv.URL, "test", "")
	_, err := emb.Embed(context.Background(), []string{"foo"})
	if err == nil {
		t.Fatal("expected error from 429 response, got nil")
	}
}

func TestEndpointEmbedder_EnvKeyEmpty(t *testing.T) {
	// Env var set but value is empty — no Authorization header.
	const envVar = "TEST_POLYFLOW_EMBED_KEY_EMPTY"
	os.Unsetenv(envVar)

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var req struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		data := []map[string]any{{"object": "embedding", "embedding": []float32{0.1}, "index": 0}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data}) //nolint:errcheck
	}))
	defer srv.Close()

	emb := semantic.NewEndpointEmbedder(srv.URL, "test", envVar)
	_, err := emb.Embed(context.Background(), []string{"ping"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("expected no Authorization header for empty env var, got %q", gotAuth)
	}
}

func TestEndpointEmbedder_Determinism(t *testing.T) {
	srv := mockEmbeddingServer(t, 4)
	defer srv.Close()

	emb := semantic.NewEndpointEmbedder(srv.URL, "test", "")
	texts := []string{"alpha", "beta", "gamma"}

	vecs1, err := emb.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	vecs2, err := emb.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}

	j1, _ := json.Marshal(vecs1)
	j2, _ := json.Marshal(vecs2)
	if string(j1) != string(j2) {
		t.Error("two endpoint calls with same input produced different vectors (determinism violation)")
	}
}
