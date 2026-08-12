#!/usr/bin/env python3
"""PreToolUse hook: nudge file Reads toward files polyflow has already named.

Companion to polyflow-first.py, which nudges Bash grep/find toward the
polyflow MCP tools. That alone doesn't stop the drift this hook targets: an
agent that got a correct, complete answer from search/impact/trace and then
opens extra files "just to verify" — files polyflow never mentioned. The
2026-08-12 juniper AMQP bench trial did exactly this: recall was already
1.0 after a handful of polyflow calls, then ~30 more Bash/Read round-trips
followed anyway, hunting for a consumer function that doesn't exist in the
codebase.

This denies the FIRST Read of a file whose path has never appeared anywhere
in this session's transcript (i.e. polyflow — or anything else — never
surfaced it), with a reason pointing back at search/impact/trace. One-time
nudge per session, not a wall: a real answer sometimes genuinely requires
opening a file polyflow didn't name (a config file with no graph node, e.g.),
so every later unlisted-file Read in the same session is let through.

Fails open everywhere: any parse error, missing field, or unexpected
exception exits 0 with no output, which Claude Code treats as "no opinion" —
a broken hook must never be able to block Read outright.
"""
import json
import os
import sys

MARKER_DIR = "/tmp/polyflow-read-gate-nudged"


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


def file_already_surfaced(transcript_path, file_path, cwd) -> bool:
    """Whether file_path (absolute or repo-relative) appears anywhere earlier
    in the transcript — almost always because a polyflow tool result named it
    (search/impact/trace/flows all echo file paths in their output). A
    substring match is deliberately loose: false positives (path mentioned in
    passing) only make the hook let more reads through, never block one."""
    if not transcript_path or not os.path.exists(transcript_path):
        return True  # no transcript to check → fail open, allow the read
    rel = file_path
    if cwd and file_path.startswith(cwd):
        rel = file_path[len(cwd):].lstrip("/")
    try:
        with open(transcript_path, "r", errors="ignore") as fh:
            for line in fh:
                if file_path in line or (rel and rel in line):
                    return True
    except OSError:
        return True  # fail open
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

    file_path = payload.get("tool_input", {}).get("file_path", "")
    if not file_path:
        return

    cwd = payload.get("cwd") or os.getcwd()
    if not find_polyflow_db(cwd):
        return  # polyflow isn't indexed here; nothing to nudge toward

    if file_already_surfaced(payload.get("transcript_path"), file_path, cwd):
        return  # this file was already named by a prior tool result

    if already_nudged(payload.get("session_id")):
        return  # one nudge per session, not a wall

    reason = (
        f"'{file_path}' hasn't been named by any tool result yet this "
        "session — reading it now looks like manual verification rather "
        "than following up on a polyflow answer. If you already have what "
        "you need from mcp__polyflow__search/impact/trace/flows, trust it "
        "instead of re-reading files to double check (their coverage/"
        "unresolved fields tell you what's incomplete, if anything). If you "
        "genuinely need this file, proceed — this is a one-time nudge for "
        "this session; Read will not be intercepted again."
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
        pass  # fail open: a broken hook must never block Read outright
