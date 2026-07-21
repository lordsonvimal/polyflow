package semantic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// EndpointEmbedder calls an OpenAI-compatible /v1/embeddings HTTP API.
// It covers Ollama (local, unauthenticated) and hosted endpoints such as
// OpenAI or Azure OpenAI (key via an env-var).
type EndpointEmbedder struct {
	url    string // base URL, no trailing slash
	model  string
	apiKey string
	id     string
	client *http.Client
}

// NewEndpointEmbedder creates an EndpointEmbedder.
//   - url: base URL, e.g. "http://localhost:11434" or "https://api.openai.com"
//   - model: model name sent in every request, e.g. "nomic-embed-text"
//   - keyEnv: name of the environment variable that holds the API key; pass ""
//     for unauthenticated endpoints (Ollama, self-hosted).
func NewEndpointEmbedder(url, model, keyEnv string) *EndpointEmbedder {
	if model == "" {
		model = "nomic-embed-text"
	}
	apiKey := ""
	if keyEnv != "" {
		apiKey = os.Getenv(keyEnv)
	}
	return &EndpointEmbedder{
		url:    url,
		model:  model,
		apiKey: apiKey,
		id:     "endpoint:" + model,
		client: &http.Client{},
	}
}

func (e *EndpointEmbedder) ID() string { return e.id }

// Dims returns 0 because the output dimensionality depends on the remote model
// and is not known until the first Embed call.  Stored dims are read back from
// the embeddings table when loading the in-memory matrix for search.
func (e *EndpointEmbedder) Dims() int { return 0 }

// openAIEmbedRequest is the request body for POST /v1/embeddings.
type openAIEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// openAIEmbedData is one item in the response data array.
type openAIEmbedData struct {
	Object    string    `json:"object"`
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

// openAIEmbedResponse is the top-level response body.
type openAIEmbedResponse struct {
	Object string            `json:"object"`
	Data   []openAIEmbedData `json:"data"`
	Error  *openAIEmbedError `json:"error,omitempty"`
}

type openAIEmbedError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// Embed POSTs texts to the configured /v1/embeddings endpoint and returns
// float32 vectors in input order (the API may return them out of order; they
// are re-sorted by the index field in the response).
func (e *EndpointEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(openAIEmbedRequest{Model: e.model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("endpoint embedder: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("endpoint embedder: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	httpResp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("endpoint embedder: http: %w", err)
	}
	defer httpResp.Body.Close()

	respBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("endpoint embedder: read body: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("endpoint embedder: server %d: %s", httpResp.StatusCode, respBytes)
	}

	var apiResp openAIEmbedResponse
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("endpoint embedder: unmarshal: %w", err)
	}
	if apiResp.Error != nil && apiResp.Error.Message != "" {
		return nil, fmt.Errorf("endpoint embedder: API error: %s", apiResp.Error.Message)
	}
	if len(apiResp.Data) != len(texts) {
		return nil, fmt.Errorf("endpoint embedder: got %d vectors for %d inputs", len(apiResp.Data), len(texts))
	}

	// Re-order by index field so callers receive vectors in input order.
	out := make([][]float32, len(texts))
	for _, d := range apiResp.Data {
		if d.Index < 0 || d.Index >= len(out) {
			return nil, fmt.Errorf("endpoint embedder: response index %d out of range [0,%d)", d.Index, len(out))
		}
		out[d.Index] = d.Embedding
	}
	for i, v := range out {
		if v == nil {
			return nil, fmt.Errorf("endpoint embedder: missing vector for index %d", i)
		}
	}
	return out, nil
}
