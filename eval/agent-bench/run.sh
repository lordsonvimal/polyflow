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

exec polyflow bench \
  --corpus eval/corpus \
  --output eval/agent-bench/results \
  "$@"
