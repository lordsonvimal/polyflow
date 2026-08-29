#!/usr/bin/env bash
# Fetches the vendored tree-sitter-r grammar (github.com/r-lib/tree-sitter-r,
# MIT) into this directory. The generated grammar source is ~4MB and does not
# belong in this repo's git history (see docs/plan-15-fs-links-py-frameworks-r.md
# Phase R.0) — this script is the build-time substitute for committing it,
# the same "regenerated, not tracked" shape *_templ.go already uses elsewhere
# in this repo (see .gitignore).
#
# Pinned to an exact commit, not a branch, so the grammar this repo builds
# against never silently changes underneath it; each file's sha256 is
# verified against the value recorded when this script was authored, so a
# corrupted download or a compromised upstream fails loudly instead of
# vendoring silently-wrong C source.
set -euo pipefail

COMMIT="58a22794466c0fc15b0d3b40531db751593721e8"
BASE="https://raw.githubusercontent.com/r-lib/tree-sitter-r/${COMMIT}"
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# fetch upstream_rel dest_rel want_sha — upstream_rel is the path under the
# pinned commit; dest_rel is where it ends up in this package (flattened out
# of upstream's src/ layout to sit beside binding.go, matching smacker's own
# per-language package shape, e.g. ruby/parser.c beside ruby/binding.go).
fetch() {
  local upstream_rel="$1" dest_rel="$2" want_sha="$3"
  local dest="${DIR}/${dest_rel}"
  mkdir -p "$(dirname "$dest")"
  if [[ -f "$dest" ]]; then
    got_sha="$(shasum -a 256 "$dest" | awk '{print $1}')"
    if [[ "$got_sha" == "$want_sha" ]]; then
      echo "ok (cached): $dest_rel"
      return
    fi
  fi
  curl -sL --max-time 60 -o "$dest" "${BASE}/${upstream_rel}"
  got_sha="$(shasum -a 256 "$dest" | awk '{print $1}')"
  if [[ "$got_sha" != "$want_sha" ]]; then
    echo "FETCH INTEGRITY FAILURE: $dest_rel" >&2
    echo "  expected sha256 $want_sha" >&2
    echo "  got      sha256 $got_sha" >&2
    exit 1
  fi
  echo "ok (fetched): $dest_rel"
}

fetch "src/parser.c"             "parser.c"             "43ec2413de8aec823c76e6994991fe07d9877e019ab2e1892534a76ce81a0771"
fetch "src/scanner.c"            "scanner.c"             "e99e003ab8b0463dee975432b6f9f7e39cd75eb0047989f064fdf57927fa8e9d"
fetch "src/tree_sitter/parser.h" "tree_sitter/parser.h"  "a1f6ef161fbaf48a0e10fca90ef5290a062462b307b3898aa562993853b9f80a"
fetch "src/tree_sitter/alloc.h"  "tree_sitter/alloc.h"   "b29c1c9fb7cc82f58c84b376df1297d6e2737a1d655fd356db0859e3c29c2fea"
fetch "src/tree_sitter/array.h"  "tree_sitter/array.h"   "5bdf6ed1a78e3409fd443e085ca967a64c188a5d082aaf7f819bccd53a471c94"
fetch "src/node-types.json"      "node-types.json"       "94d900768d13cfc5daa8eefa829c297c1bc0c09582574f381f9ff8a6c5103062"
fetch "LICENSE"                  "LICENSE"               "de2e49529f03d573bc3fa229dc83acfe22c63a5ad1766b289563edfd45b72dea"

echo "tree-sitter-r grammar ready (commit ${COMMIT})."
