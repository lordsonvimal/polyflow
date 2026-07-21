# embed-model — Static Embedding Model Generator

## Provenance and License

The **production** model target is **potion-base-8M** from
[minishlab/potion-base-8M](https://huggingface.co/minishlab/potion-base-8M),
a model2vec distillation of bge-base-en-v1.5, released under the **MIT License**.

- Architecture: static embedding (no attention); WordPiece vocabulary from
  bert-base-uncased (30,522 tokens); 256-dimensional float32 → int8-quantized.
- License: MIT (model weights + vocabulary). See LICENSES/MIT-potion.txt.
- Accuracy: MTEB average ≈ 50.0; transfer to code-entity retrieval measured
  in Phase S.4 (see docs/semantic-search-plan.md).

## Generating the development/synthetic model (shipped default)

The binary ships `internal/semantic/model/model.bin`, a **synthetic** model
with a 2048-token vocabulary and random-but-deterministic int8 weights (seed 42).
It is functional for all S.0 infrastructure tests; quality is measured in S.4.

```
cd /path/to/polyflow
go run ./tools/embed-model -out internal/semantic/model/model.bin
```

## Generating from real potion-base-8M weights (production)

```
pip install model2vec numpy
python tools/embed-model/convert.py \
  --model minishlab/potion-base-8M \
  --out internal/semantic/model/model.bin
```

`convert.py` downloads the HuggingFace snapshot (requires network access;
no network access is needed at runtime — the model is embedded in the binary),
quantizes float32 → int8 per-token with per-row max-abs scales, and writes
the PLF1 binary format described below.

## Binary format (PLF1)

```
Offset  Size      Content
0       4 bytes   magic "PLF1"
4       4 bytes   dims (uint32 LE)
8       4 bytes   n_tokens (uint32 LE)
12      n×2 bytes vocab lengths (uint16 LE per token)
var     Σlen      vocab bytes concatenated (UTF-8, no terminators)
var     n×dims    int8 matrix, row-major
var     n×4       float32 LE per-token scales (dequant: val = int8 × scale)
```

The loader in `internal/semantic/static.go` reads this format. A changed format
version requires a new magic string ("PLF2", etc.) or a format-version field —
never silently re-interpret old blobs.
