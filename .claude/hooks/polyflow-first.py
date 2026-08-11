#!/usr/bin/env python3
"""PreToolUse hook: nudge Bash source-code search toward polyflow MCP tools.

The bench harness (eval/agent-bench/) showed an agent falls back to Bash
grep/find for call-graph questions unless something structural stops it — a
system-prompt nudge alone is advisory and gets skipped under sampling
variance (see internal/agentbench, cmd/polyflow/bench.go polyflowNudge).

This denies the FIRST recursive source-code search of a session — `grep -r`,
ripgrep, `git grep`, `ag`, or `find -name` across the repo — with a reason
telling the agent to try an mcp__polyflow__* tool instead. It is a one-time
nudge, not a wall: any later match in the same session is let through
unconditionally once the transcript shows a polyflow tool call happened, and
also once this hook itself has already denied once (so a session where
polyflow genuinely doesn't have the answer can still fall back to grep
without being blocked every single time).

Fails open everywhere: any parse error, missing field, or unexpected
exception exits 0 with no output, which Claude Code treats as "no opinion" —
a broken hook must never be able to block Bash outright.
"""
import json
import os
import re
import sys

MARKER_DIR = "/tmp/polyflow-hook-nudged"

SEARCH_TOKENS_RE = re.compile(r"\brg\b|\bag\b|\bgit\s+grep\b")
GREP_RE = re.compile(r"\bgrep\b")
RECURSIVE_FLAG_RE = re.compile(r"(?<!\S)-\w*r\w*\b|--recursive\b")
FIND_RE = re.compile(r"\bfind\b")
FIND_NAME_RE = re.compile(r"-i?name\b")


def looks_like_source_search(command: str) -> bool:
    if SEARCH_TOKENS_RE.search(command):
        return True
    if GREP_RE.search(command) and RECURSIVE_FLAG_RE.search(command):
        return True
    if FIND_RE.search(command) and FIND_NAME_RE.search(command):
        return True
    return False


def find_polyflow_db(start_dir: str, max_levels: int = 6) -> bool:
    d = os.path.abspath(start_dir)
    for _ in range(max_levels):
        if os.path.exists(os.path.join(d, ".polyflow", "graph.db")):
            return True
        parent = os.path.dirname(d)
        if parent == d:
            break
        d = parent
    return False


def already_tried_polyflow(transcript_path) -> bool:
    if not transcript_path or not os.path.exists(transcript_path):
        return False
    try:
        with open(transcript_path, "r", errors="ignore") as fh:
            for line in fh:
                if "mcp__polyflow__" in line:
                    return True
    except OSError:
        return False
    return False


def already_nudged(session_id) -> bool:
    if not session_id:
        return False
    path = os.path.join(MARKER_DIR, session_id)
    if os.path.exists(path):
        return True
    try:
        os.makedirs(MARKER_DIR, exist_ok=True)
        open(path, "w").close()
    except OSError:
        pass
    return False


def main() -> None:
    payload = json.load(sys.stdin)

    command = payload.get("tool_input", {}).get("command", "")
    if not command or not looks_like_source_search(command):
        return

    cwd = payload.get("cwd") or os.getcwd()
    if not find_polyflow_db(cwd):
        return  # polyflow isn't indexed here; nothing to nudge toward

    if already_tried_polyflow(payload.get("transcript_path")):
        return  # already reached for polyflow this session; let grep through

    if already_nudged(payload.get("session_id")):
        return  # one nudge per session, not a wall

    reason = (
        "A polyflow graph is indexed for this repo. Before grepping for a "
        "symbol, call graph, or blast-radius answer, try the polyflow MCP "
        "tools first (mcp__polyflow__search, mcp__polyflow__impact, "
        "mcp__polyflow__flows) — they answer 'what calls this' / 'what would "
        "need to change' directly, in one call, instead of several grep "
        "round-trips. This is a one-time nudge for this session; Bash search "
        "will not be intercepted again."
    )
    print(json.dumps({
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "deny",
            "permissionDecisionReason": reason,
        }
    }))


if __name__ == "__main__":
    try:
        main()
    except Exception:
        pass  # fail open: a broken hook must never block Bash outright
