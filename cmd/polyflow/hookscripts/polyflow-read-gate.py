#!/usr/bin/env python3
"""PreToolUse hook: nudge file inspection toward files polyflow already named.

Companion to polyflow-first.py, which nudges Bash grep/find toward the
polyflow MCP tools. That alone doesn't stop the drift this hook targets: an
agent that got a correct, complete answer from search/impact/trace and then
opens extra files "just to verify" — files polyflow never mentioned. The
2026-08-12 datascience AMQP bench trial did exactly this: recall was already
1.0 after a handful of polyflow calls, then ~30 more Bash/Read round-trips
followed anyway, hunting for a consumer function that doesn't exist in the
codebase. A first version of this hook only matched the Read tool; the same
trial pattern re-ran and the agent dodged it entirely by using `grep`/`sed -n`
through Bash instead of Read — so this hook is now registered on BOTH the
Read and Bash matchers, and inspects `cat`/`sed`/`head`/`tail`/single-file
`grep` commands the same way it inspects a Read call.

Denies the FIRST inspection of a file whose path has never appeared anywhere
in this session's transcript (i.e. polyflow — or anything else — never
surfaced it), with a reason pointing back at search/impact/trace. One-time
nudge per session, not a wall: a real answer sometimes genuinely requires
opening a file polyflow didn't name (a config file with no graph node, e.g.),
so every later unlisted-file inspection in the same session is let through.
Recursive/multi-file search (`grep -r`, `find -name`, ...) is deliberately
NOT matched here — that's polyflow-first.py's territory, and treating a
search's directory argument as an unsurfaced "file" would wrongly block
exactly the fallback search that hook is designed to allow once polyflow has
already been tried.

Fails open everywhere: any parse error, missing field, or unexpected
exception exits 0 with no output, which Claude Code treats as "no opinion" —
a broken hook must never be able to block a tool call outright.
"""
import json
import os
import re
import shlex
import sys

MARKER_DIR = "/tmp/polyflow-read-gate-nudged"

# Bash commands treated as "inspect a specific file's content" rather than a
# repo-wide search.
FILE_VIEW_CMDS = {"cat", "sed", "head", "tail", "grep"}
RECURSIVE_FLAG_RE = re.compile(r"(?<!\S)-\w*[rR]\w*\b|--recursive\b")
# Splits a command string into separate simple-command segments on shell
# sequencing/piping operators, so a segment is only "the command that runs
# grep" when grep is its own invocation — not a substring living inside a
# quoted argument to something else (e.g. `python3 -c "print('grep')"`,
# which a plain `\bgrep\b` search over the raw string would wrongly match).
SEGMENT_SPLIT_RE = re.compile(r"\||;|&&|\|\|")


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


def extract_bash_view_target(command: str):
    """Returns the first file-path-looking argument of a cat/sed/head/tail/
    grep command, or None if command isn't a single-file view (recursive
    grep, no recognizable command, or no path-shaped argument).

    Only treats cat/sed/head/tail/grep as a match when one of them is the
    actual invoked command of a shell segment (post shlex.split, token 0's
    basename) — not merely a substring anywhere in the raw command text,
    which would also match e.g. the word "grep" inside a quoted argument to
    an unrelated command like `python3 -c "print('grep')"`.
    """
    if not command:
        return None

    for segment in SEGMENT_SPLIT_RE.split(command):
        try:
            tokens = shlex.split(segment)
        except ValueError:
            continue  # unbalanced quotes in this segment — don't guess
        if not tokens:
            continue
        cmd_name = os.path.basename(tokens[0])
        if cmd_name not in FILE_VIEW_CMDS:
            continue
        if cmd_name == "grep" and RECURSIVE_FLAG_RE.search(segment):
            continue  # a recursive grep is a search, not a file view

        for tok in tokens[1:]:
            if tok.startswith("-"):
                continue
            if "/" not in tok:
                continue  # skip sed range specs, bare filenames, regex patterns
            return tok
    return None


def target_from_payload(payload):
    """Returns the file path to gate, or None if this call isn't one we care
    about (neither a Read nor a recognized single-file Bash view)."""
    tool_input = payload.get("tool_input", {})
    if "file_path" in tool_input:
        return tool_input.get("file_path") or None
    if "command" in tool_input:
        return extract_bash_view_target(tool_input.get("command", ""))
    return None


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

    file_path = target_from_payload(payload)
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
        "session — inspecting it now looks like manual verification rather "
        "than following up on a polyflow answer. If you already have what "
        "you need from mcp__polyflow__search/impact/trace/flows, trust it "
        "instead of re-reading files to double check (their coverage/"
        "unresolved fields tell you what's incomplete, if anything). If you "
        "genuinely need this file, proceed — this is a one-time nudge for "
        "this session; file inspection will not be intercepted again."
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
        pass  # fail open: a broken hook must never block a tool call outright
