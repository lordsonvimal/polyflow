#!/usr/bin/env bash
# eval/agent-bench/run.sh — thin wrapper around `polyflow bench`.
# Run from the repo root. Passes all arguments through.
#
# Examples:
#   ./eval/agent-bench/run.sh                     # all arms, 1 trial
#   ./eval/agent-bench/run.sh --trials 3          # 3 trials per task/arm
#   ./eval/agent-bench/run.sh --arm with_polyflow_semantic
#   ./eval/agent-bench/run.sh --dry-run
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

# Build from source before measuring. A stale binary on $PATH has previously
# invented a regression that was already fixed in the tree, and each bench run
# costs real tokens — too expensive to spend on the wrong build.
BIN="$(mktemp -d)/polyflow"
go build -o "$BIN" ./cmd/polyflow
trap 'rm -rf "$(dirname "$BIN")"' EXIT

# Not exec'd: the trap has to survive to clean up, and the MCP config the bench
# writes points at $BIN for the duration of the run.
"$BIN" bench \
  --corpus eval/corpus \
  --output eval/agent-bench/results \
  "$@"
