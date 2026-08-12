#!/usr/bin/env python3
"""PostToolUse hook: auto-augment grep/cat/sed/head/tail/Read with graph context.

Replaces the earlier polyflow-first.py / polyflow-read-gate.py PreToolUse
hooks, which denied a tool call once and printed a suggestion — "deny and
hope the agent pivots to the right polyflow tool with a good query." Tracing
real bench transcripts showed that bet doesn't reliably pay off: the nudge
only fires once, the agent has to guess which polyflow tool + query
reproduces what it wanted, and once one polyflow call has happened the grep
hook gets out of the way entirely, letting dozens more grep calls through
unenriched (2026-08-12 datascience AMQP trials).

This hook never blocks anything. It lets the tool call run exactly as the
agent intended, then appends a compact `[polyflow]` block — callers/callees
for a grepped symbol, or the functions/methods declared in a
cat/sed/head/tail/Read'd file plus their callers — read directly out of the
local .polyflow/graph.db. The payoff lands on the FIRST matching call, not
contingent on the agent choosing to retry differently. Deduplicated per
session per target so re-inspecting the same symbol/file doesn't repeat the
same block and inflate tokens across many calls.

Fails open everywhere: any parse error, missing field, locked/missing db, or
unexpected exception exits 0 with no output — a broken hook must never be
able to break a tool call's real output.
"""
import json
import os
import re
import shlex
import sqlite3
import sys

SEEN_DIR = "/tmp/polyflow-context-injected"

FILE_VIEW_CMDS = {"cat", "sed", "head", "tail"}
GREP_CMDS = {"grep"}
SEGMENT_SPLIT_RE = re.compile(r"\||;|&&|\|\|")
IDENTIFIER_RE = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*$")

MAX_CONTEXT_CHARS = 600
CALLS_EDGE = "calls"


def find_polyflow_db(start_dir: str, max_levels: int = 6):
    d = os.path.abspath(start_dir)
    for _ in range(max_levels):
        cand = os.path.join(d, ".polyflow", "graph.db")
        if os.path.exists(cand):
            return cand
        parent = os.path.dirname(d)
        if parent == d:
            break
        d = parent
    return None


def extract_target(payload):
    """Returns (mode, value) where mode is "symbol" (a grepped identifier) or
    "file" (a cat/sed/head/tail/Read target), or (None, None) if this call
    isn't one we care about."""
    tool_input = payload.get("tool_input", {})

    if "file_path" in tool_input:
        fp = tool_input.get("file_path")
        return ("file", fp) if fp else (None, None)

    command = tool_input.get("command", "")
    if not command:
        return None, None

    for segment in SEGMENT_SPLIT_RE.split(command):
        try:
            tokens = shlex.split(segment)
        except ValueError:
            continue
        if not tokens:
            continue
        cmd_name = os.path.basename(tokens[0])
        rest = [t for t in tokens[1:] if not t.startswith("-")]

        if cmd_name in GREP_CMDS and rest:
            pattern = rest[0]
            if IDENTIFIER_RE.match(pattern):
                return "symbol", pattern
            continue

        if cmd_name in FILE_VIEW_CMDS:
            for tok in rest:
                if "/" in tok or re.search(r"\.\w{1,6}$", tok):
                    return "file", tok
    return None, None


def rel_path(file_path, cwd):
    if cwd and file_path.startswith(cwd):
        return file_path[len(cwd):].lstrip("/")
    return file_path


def related_labels(cur, node_id, direction, limit=4):
    if direction == "in":
        cur.execute(
            'SELECT n.label FROM edges e JOIN nodes n ON e."from"=n.id '
            'WHERE e."to"=? AND e.type=? LIMIT ?',
            (node_id, CALLS_EDGE, limit),
        )
    else:
        cur.execute(
            'SELECT n.label FROM edges e JOIN nodes n ON e."to"=n.id '
            'WHERE e."from"=? AND e.type=? LIMIT ?',
            (node_id, CALLS_EDGE, limit),
        )
    return [r[0] for r in cur.fetchall() if r[0]]


def symbol_context(cur, term):
    cur.execute(
        "SELECT id, type, label, file, line FROM nodes WHERE label = ? "
        "COLLATE NOCASE LIMIT 3",
        (term,),
    )
    rows = cur.fetchall()
    if not rows:
        return None
    parts = []
    for node_id, ntype, label, file, line in rows:
        loc = f"{file}:{line}" if file else ""
        seg = f"{label} ({ntype}) {loc}".strip()
        callers = related_labels(cur, node_id, "in")
        callees = related_labels(cur, node_id, "out")
        if callers:
            seg += f" | callers: {', '.join(callers)}"
        if callees:
            seg += f" | calls: {', '.join(callees)}"
        parts.append(seg)
    return parts


def file_context(cur, file_path, cwd):
    rel = rel_path(file_path, cwd)
    cur.execute(
        "SELECT id, type, label, line FROM nodes WHERE file = ? "
        "AND type IN ('function','method','http_handler','subscriber','worker') "
        "ORDER BY line LIMIT 6",
        (rel,),
    )
    rows = cur.fetchall()
    if not rows:
        return None
    parts = []
    for node_id, ntype, label, line in rows:
        callers = related_labels(cur, node_id, "in", limit=3)
        seg = f"{label}({ntype}):{line}"
        if callers:
            seg += f" <- {', '.join(callers)}"
        parts.append(seg)
    return parts


def already_seen(session_id, key) -> bool:
    if not session_id:
        return False
    path = os.path.join(SEEN_DIR, session_id + ".json")
    seen = set()
    if os.path.exists(path):
        try:
            with open(path) as fh:
                seen = set(json.load(fh))
        except (OSError, ValueError):
            seen = set()
    if key in seen:
        return True
    seen.add(key)
    try:
        os.makedirs(SEEN_DIR, exist_ok=True)
        with open(path, "w") as fh:
            json.dump(sorted(seen), fh)
    except OSError:
        pass
    return False


def main() -> None:
    payload = json.load(sys.stdin)

    mode, value = extract_target(payload)
    if not mode:
        return

    cwd = payload.get("cwd") or os.getcwd()
    db_path = find_polyflow_db(cwd)
    if not db_path:
        return

    if already_seen(payload.get("session_id"), f"{mode}:{value}"):
        return

    try:
        conn = sqlite3.connect(db_path, timeout=2)
        conn.execute("PRAGMA query_only = 1")
        cur = conn.cursor()
        if mode == "symbol":
            parts = symbol_context(cur, value)
            label = "symbol"
        else:
            parts = file_context(cur, value, cwd)
            label = "file"
        conn.close()
    except sqlite3.Error:
        return  # db locked mid-index, corrupt, or schema mismatch — fail open

    if not parts:
        return

    block = f"[polyflow graph — {label} '{value}'] " + "; ".join(parts)
    if len(block) > MAX_CONTEXT_CHARS:
        block = block[:MAX_CONTEXT_CHARS] + "…"

    print(json.dumps({"additionalContext": block}))


if __name__ == "__main__":
    try:
        main()
    except Exception:
        pass  # fail open: a broken hook must never disrupt the real tool output
