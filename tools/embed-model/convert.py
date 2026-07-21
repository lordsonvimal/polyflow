#!/usr/bin/env python3
"""
Convert potion-base-8M (or any model2vec model) to PLF1 binary format.

Requires: pip install model2vec numpy

Usage:
    python tools/embed-model/convert.py \
        --model minishlab/potion-base-8M \
        --out internal/semantic/model/model.bin

Model: minishlab/potion-base-8M
  Architecture: model2vec (static, no attention)
  Base teacher: bge-base-en-v1.5
  Vocabulary: bert-base-uncased WordPiece (30,522 tokens)
  Dimensions: 256
  License: MIT — see https://huggingface.co/minishlab/potion-base-8M
  SHA256 (embeddings.npy): pin before shipping; update if model is re-released.

The produced PLF1 binary embeds into the polyflow binary via go:embed.
At query time no network access is needed (air-gap safe).
"""

import argparse
import struct
import numpy as np
from model2vec import StaticModel

MAGIC = b"PLF1"


def quantize_int8(matrix: np.ndarray) -> tuple[np.ndarray, np.ndarray]:
    """Per-row max-abs int8 quantization.  scale_i = max|v_i| / 127."""
    max_abs = np.abs(matrix).max(axis=1, keepdims=True)
    max_abs = np.where(max_abs < 1e-12, 1.0, max_abs)
    scales = (max_abs / 127.0).astype(np.float32).squeeze(1)
    quantized = np.round(matrix / max_abs * 127).clip(-128, 127).astype(np.int8)
    return quantized, scales


def write_plf1(path: str, vocab: list[str], matrix: np.ndarray, scales: np.ndarray) -> None:
    n_tokens, dims = matrix.shape
    with open(path, "wb") as f:
        f.write(MAGIC)
        f.write(struct.pack("<I", dims))
        f.write(struct.pack("<I", n_tokens))
        lengths = [len(t.encode()) for t in vocab]
        for l in lengths:
            f.write(struct.pack("<H", l))
        for tok in vocab:
            f.write(tok.encode())
        f.write(matrix.tobytes())  # int8, row-major
        f.write(scales.astype("<f4").tobytes())  # float32 LE
    size_kb = path.__class__(path).stat().st_size / 1024  # type: ignore[attr-defined]
    import os
    size_kb = os.path.getsize(path) / 1024
    print(f"Wrote {path} ({size_kb:.0f} KB, {n_tokens} tokens × {dims} dims)")


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", default="minishlab/potion-base-8M")
    ap.add_argument("--out", default="internal/semantic/model/model.bin")
    args = ap.parse_args()

    print(f"Loading model {args.model!r} ...")
    model = StaticModel.from_pretrained(args.model)

    # model.embedding is an ndarray of shape (vocab_size, dims) in float32.
    embedding = model.embedding.astype(np.float32)
    # Vocabulary: model.tokenizer.get_vocab() returns {token: id} ordered by id.
    vocab_map = model.tokenizer.get_vocab()
    vocab = [tok for tok, _ in sorted(vocab_map.items(), key=lambda kv: kv[1])]
    assert len(vocab) == embedding.shape[0], "vocab/matrix size mismatch"

    quantized, scales = quantize_int8(embedding)
    write_plf1(args.out, vocab, quantized, scales)


if __name__ == "__main__":
    main()
