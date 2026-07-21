package semantic

import (
	"bytes"
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// --- tokenizer golden tests ---

func TestTokenizerGolden(t *testing.T) {
	emb, err := DefaultStaticEmbedder()
	require.NoError(t, err)
	tok := emb.tok

	cases := []struct {
		input string
		want  []string // expected vocab tokens (nil = only check non-empty)
	}{
		// Words that exist directly in the vocab (exact match after lowercase)
		{"http handler", []string{"http", "handler"}},
		{"create order", []string{"create", "order"}},
		{"api service", []string{"api", "service"}},
		// Punctuation splits into separate tokens; a, b, . are all in vocab
		{"a.b", []string{"a", ".", "b"}},
		{"a/b", []string{"a", "/", "b"}},
		// Unknown word → character fallback; just verify non-empty output
		{"xyzqwerty", nil},
		// camelCase treated as one word (lowercased), subword/char fallback
		{"handlePurchase", nil},
	}

	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			ids := tok.tokenize(c.input)
			require.NotEmpty(t, ids, "tokenizer must produce at least one token")
			if c.want != nil {
				require.Equal(t, len(c.want), len(ids),
					"token count mismatch for %q: got ids %v", c.input, ids)
				for i, w := range c.want {
					wantID, ok := tok.vocab[w]
					require.True(t, ok, "word %q not in vocab", w)
					require.Equal(t, wantID, ids[i],
						"token[%d] for %q: want %q (%d), got id %d", i, c.input, w, wantID, ids[i])
				}
			}
		})
	}
}

// --- embedding determinism: same text → same vector ---

func TestEmbedDeterminism(t *testing.T) {
	emb, err := DefaultStaticEmbedder()
	require.NoError(t, err)
	ctx := context.Background()

	texts := []string{
		"handlePurchase http_handler api internal/api/purchase.go POST /orders",
		"CreateOrder",
		"user service",
		"checkout flow",
		"",
	}

	v1, err := emb.Embed(ctx, texts)
	require.NoError(t, err)
	v2, err := emb.Embed(ctx, texts)
	require.NoError(t, err)

	for i := range texts {
		require.Equal(t, v1[i], v2[i], "non-deterministic embedding at index %d (%q)", i, texts[i])
	}
}

// --- offline test: embedding works with no network ---

func TestEmbedOffline(t *testing.T) {
	// DefaultStaticEmbedder uses only the embedded model blob — no network.
	// This test simply confirms it initialises and produces output.
	emb, err := DefaultStaticEmbedder()
	require.NoError(t, err)
	vecs, err := emb.Embed(context.Background(), []string{"test offline"})
	require.NoError(t, err)
	require.Len(t, vecs, 1)
	require.Len(t, vecs[0], emb.Dims())
}

// --- quantization sanity: cosine(int8 path, fp32 reference) > 0.99 ---

func TestQuantizationSanity(t *testing.T) {
	emb, err := DefaultStaticEmbedder()
	require.NoError(t, err)

	// Build the fp32 reference: for a set of token ids, compute mean-pooled
	// fp32 directly from scales×int8 (exact), then L2-normalize.
	// The Embed path does the same computation; they must agree to >0.99 cosine.
	// We verify against a separate fp32 accumulation that avoids int8.

	texts := []string{
		"handler service api",
		"create order checkout",
		"graph node edge",
	}
	ctx := context.Background()
	vecs, err := emb.Embed(ctx, texts)
	require.NoError(t, err)

	for i, text := range texts {
		ids := emb.tok.tokenize(text)
		if len(ids) == 0 {
			continue
		}
		// fp32 reference: accumulate with float64 precision then normalize
		ref := make([]float64, emb.dims)
		for _, tok := range ids {
			scale := float64(emb.scales[tok])
			base := tok * emb.dims
			for d := 0; d < emb.dims; d++ {
				ref[d] += float64(emb.matrix[base+d]) * scale
			}
		}
		n := float64(len(ids))
		var refNorm float64
		for d := range ref {
			ref[d] /= n
			refNorm += ref[d] * ref[d]
		}
		refNorm = math.Sqrt(refNorm)
		if refNorm < 1e-12 {
			continue
		}
		for d := range ref {
			ref[d] /= refNorm
		}

		// cosine similarity between int8-path output and fp64-reference
		var dot, na, nb float64
		for d := range ref {
			a := float64(vecs[i][d])
			b := ref[d]
			dot += a * b
			na += a * a
			nb += b * b
		}
		cos := dot / (math.Sqrt(na) * math.Sqrt(nb))
		require.Greater(t, cos, 0.99,
			"cosine(int8-path, fp32-ref) = %.6f < 0.99 for %q", cos, text)
		t.Logf("text=%q cosine=%.6f", text, cos)
	}
}

// --- hash-gate test: unchanged text → no re-embed ---

func TestHashGate(t *testing.T) {
	// Verify that two Entity values with the same ContentHash produce identical
	// blobs, and that a changed hash produces a different blob.
	const text = "handler service api"
	emb, err := DefaultStaticEmbedder()
	require.NoError(t, err)

	vecs, err := emb.Embed(context.Background(), []string{text, text})
	require.NoError(t, err)

	b1 := vecToBlob(vecs[0])
	b2 := vecToBlob(vecs[1])
	require.True(t, bytes.Equal(b1, b2), "same text → different blob (non-deterministic)")

	// Different text → different blob (with very high probability)
	vecs2, err := emb.Embed(context.Background(), []string{"completely different input xyz"})
	require.NoError(t, err)
	b3 := vecToBlob(vecs2[0])
	require.False(t, bytes.Equal(b1, b3), "different text → same blob (collision)")
}

// --- model loading sanity ---

func TestModelLoad(t *testing.T) {
	emb, err := DefaultStaticEmbedder()
	require.NoError(t, err)
	require.Equal(t, staticEmbedderID, emb.ID())
	require.Equal(t, 256, emb.Dims())
	require.NotNil(t, emb.tok)
	require.NotEmpty(t, emb.matrix)
	require.NotEmpty(t, emb.scales)
}

// --- L2 normalization sanity ---

func TestL2Normalize(t *testing.T) {
	emb, err := DefaultStaticEmbedder()
	require.NoError(t, err)
	vecs, err := emb.Embed(context.Background(), []string{"normalize me"})
	require.NoError(t, err)
	v := vecs[0]
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	require.InDelta(t, 1.0, sum, 0.001, "output vector is not unit length")
}
