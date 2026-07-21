package semantic

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"sync"
	"unicode"
	_ "embed"
)

//go:embed model/model.bin
var modelData []byte

const staticEmbedderID = "static-v1-int8"

var (
	defaultOnce      sync.Once
	defaultEmbedder  *StaticEmbedder
	defaultEmbedderr error
)

// DefaultStaticEmbedder returns the singleton StaticEmbedder loaded from
// the embedded model blob. Initialization is lazy and thread-safe.
func DefaultStaticEmbedder() (*StaticEmbedder, error) {
	defaultOnce.Do(func() {
		defaultEmbedder, defaultEmbedderr = loadStaticEmbedder(modelData)
	})
	return defaultEmbedder, defaultEmbedderr
}

// StaticEmbedder is the pure-Go static embedding backend.
// It uses a WordPiece tokenizer over an int8-quantized token matrix;
// embeddings are produced without any network call (air-gap safe).
type StaticEmbedder struct {
	tok    *wordPieceTokenizer
	matrix []int8   // n_tokens × dims, row-major
	scales []float32 // n_tokens
	dims   int
}

func (e *StaticEmbedder) ID() string  { return staticEmbedderID }
func (e *StaticEmbedder) Dims() int   { return e.dims }

// Embed produces L2-normalized float32 vectors for each text in the batch.
// It is safe for concurrent use.
func (e *StaticEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		tokens := e.tok.tokenize(text)
		vec := make([]float32, e.dims)
		if len(tokens) > 0 {
			for _, tok := range tokens {
				scale := e.scales[tok]
				base := tok * e.dims
				for d := 0; d < e.dims; d++ {
					vec[d] += float32(e.matrix[base+d]) * scale
				}
			}
			n := float32(len(tokens))
			for d := range vec {
				vec[d] /= n
			}
			l2normalize(vec)
		}
		out[i] = vec
	}
	return out, nil
}

func l2normalize(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum < 1e-12 {
		return
	}
	inv := float32(1.0 / math.Sqrt(sum))
	for i := range v {
		v[i] *= inv
	}
}

// --- PLF1 model loader ---

func loadStaticEmbedder(data []byte) (*StaticEmbedder, error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("static embedder: model blob too short")
	}
	if string(data[:4]) != "PLF1" {
		return nil, fmt.Errorf("static embedder: bad magic %q", data[:4])
	}
	dims := int(binary.LittleEndian.Uint32(data[4:8]))
	nTokens := int(binary.LittleEndian.Uint32(data[8:12]))
	if dims <= 0 || nTokens <= 0 {
		return nil, fmt.Errorf("static embedder: invalid dims=%d n_tokens=%d", dims, nTokens)
	}

	off := 12
	// Vocab lengths
	if len(data) < off+nTokens*2 {
		return nil, fmt.Errorf("static embedder: truncated vocab lengths")
	}
	lengths := make([]int, nTokens)
	for i := 0; i < nTokens; i++ {
		lengths[i] = int(binary.LittleEndian.Uint16(data[off : off+2]))
		off += 2
	}
	// Vocab bytes
	vocabMap := make(map[string]int, nTokens)
	for i, l := range lengths {
		if len(data) < off+l {
			return nil, fmt.Errorf("static embedder: truncated vocab at token %d", i)
		}
		tok := string(data[off : off+l])
		off += l
		vocabMap[tok] = i
	}
	// Matrix
	matSize := nTokens * dims
	if len(data) < off+matSize {
		return nil, fmt.Errorf("static embedder: truncated matrix")
	}
	matrix := make([]int8, matSize)
	for i := 0; i < matSize; i++ {
		matrix[i] = int8(data[off+i])
	}
	off += matSize
	// Scales
	if len(data) < off+nTokens*4 {
		return nil, fmt.Errorf("static embedder: truncated scales")
	}
	scales := make([]float32, nTokens)
	for i := 0; i < nTokens; i++ {
		scales[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[off : off+4]))
		off += 4
	}

	tok := newWordPieceTokenizer(vocabMap)
	return &StaticEmbedder{tok: tok, matrix: matrix, scales: scales, dims: dims}, nil
}

// --- WordPiece tokenizer ---

// wordPieceTokenizer implements the BERT uncased WordPiece algorithm:
//  1. Lowercase + basic tokenize (split on whitespace and punctuation).
//  2. For each basic token, greedily find the longest vocab match starting
//     from the left (continuation tokens carry the "##" prefix).
//  3. If no match is found at any position, the whole word becomes [UNK].
type wordPieceTokenizer struct {
	vocab      map[string]int
	unkID      int
	maxWordLen int // characters; words longer than this map to [UNK]
}

func newWordPieceTokenizer(vocab map[string]int) *wordPieceTokenizer {
	unkID := 1 // default [UNK] position
	if id, ok := vocab["[UNK]"]; ok {
		unkID = id
	}
	return &wordPieceTokenizer{vocab: vocab, unkID: unkID, maxWordLen: 128}
}

func (t *wordPieceTokenizer) tokenize(text string) []int {
	words := basicTokenize(strings.ToLower(text))
	out := make([]int, 0, len(words)*2)
	for _, w := range words {
		out = append(out, t.wordPiece(w)...)
	}
	return out
}

// basicTokenize splits text on whitespace and punctuation (BERT-style).
// Punctuation characters are emitted as separate single-character tokens.
func basicTokenize(text string) []string {
	var words []string
	var cur strings.Builder
	for _, r := range text {
		if unicode.IsSpace(r) {
			if cur.Len() > 0 {
				words = append(words, cur.String())
				cur.Reset()
			}
		} else if isBasicPunct(r) {
			if cur.Len() > 0 {
				words = append(words, cur.String())
				cur.Reset()
			}
			words = append(words, string(r))
		} else {
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		words = append(words, cur.String())
	}
	return words
}

func isBasicPunct(r rune) bool {
	// ASCII punctuation ranges (matches BERT BasicTokenizer).
	if r <= 0x7F {
		return (r >= '!' && r <= '/') ||
			(r >= ':' && r <= '@') ||
			(r >= '[' && r <= '`') ||
			(r >= '{' && r <= '~')
	}
	return unicode.IsPunct(r) || unicode.IsSymbol(r)
}

// wordPiece applies greedy longest-match WordPiece to a single basic token.
func (t *wordPieceTokenizer) wordPiece(word string) []int {
	runes := []rune(word)
	if len(runes) > t.maxWordLen {
		return []int{t.unkID}
	}
	var tokens []int
	start := 0
	for start < len(runes) {
		end := len(runes)
		found := -1
		for end > start {
			var substr string
			if start == 0 {
				substr = string(runes[start:end])
			} else {
				substr = "##" + string(runes[start:end])
			}
			if id, ok := t.vocab[substr]; ok {
				found = id
				break
			}
			end--
		}
		if found == -1 {
			// No match anywhere from this position → whole word is [UNK].
			return []int{t.unkID}
		}
		tokens = append(tokens, found)
		start = end
	}
	return tokens
}
