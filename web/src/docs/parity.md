# CLI ↔ UI parity matrix

Every `polyflow` command, mapped to its UI equivalent. Rows marked
**exception** are the only ones allowed to have no UI equivalent
(docs/plan-13-ui-ops.md UO.7) — `internal/server` and `cmd/polyflow` both
route through the same internals (`indexer.Run`, `workspace.Save`,
`patterns.LoadFile`, the capture session store, …) regardless of which
surface triggers them, so a state written from one surface is never
invisible to the other.

| CLI command | UI equivalent |
|---|---|
| `polyflow init` | Setup wizard, step 1 (Overview activity, first-run) — `POST /api/jobs {kind:"init"}` discovers services, `POST /api/setup/apply` writes polyflow.yml via `workspace.SaveInit`, same as this command |
| `polyflow setup` | Setup wizard (interactive terminal wizard's UI counterpart) — same three steps, browser-driven |
| `polyflow index` | Top bar "Index" button / Jobs tab — `POST /api/jobs {kind:"index"}` |
| `polyflow serve` | **exception** — bootstrapping; starts the process the UI runs in |
| `polyflow search <query>` | Explore activity search box |
| `polyflow status` | Health activity (index freshness, error files) |
| `polyflow patterns` | Settings → Patterns |
| `polyflow patterns list` | Settings → Patterns list/search/language-filter |
| `polyflow patterns add <file>` | Settings → Patterns → "Add pattern…" — `POST /api/patterns` |
| `polyflow context` | Context copy (right-click node → "Copy context") / `POST /api/context/bundle` |
| `polyflow trace` | Flows activity — waypoint/path tracing between two nodes |
| `polyflow impact` | Impact activity — blast-radius view for a node or file |
| `polyflow deadcode` | Dead code activity — zero-caller function/method scan, filterable by service/file |
| `polyflow deps` | Health activity → dependency list |
| `polyflow link` | Config activity → `links:` section (form or YAML mode) |
| `polyflow fleet` | **exception** — Tier GR operator surface; the UI's fleet-member switcher (Settings → Fleet, GR.6) covers the same ground via `GET /api/fleet/services`/`POST /api/fleet/active`, not a wrapper around this command |
| `polyflow fleet sync` | **exception** — same as `polyflow fleet`; GR.6's switcher triggers an equivalent resolve/clone on member selection, not this command directly |
| `polyflow fleet status` | **exception** — same as `polyflow fleet`; GR.6's Settings → Fleet panel is the UI's read-only view |
| `polyflow registry` | **exception** — Tier GR operator surface; no UI counterpart (a machine-local index dump, not workspace state) |
| `polyflow config` | Config activity |
| `polyflow config show` | Config activity, YAML mode — `GET /api/config` |
| `polyflow config set <key> <value>` | Config activity, form mode field edit — `PUT /api/config` |
| `polyflow config service` | Config activity → Services section |
| `polyflow config service add` | Config activity → Services → "Add service" |
| `polyflow config service remove` | Config activity → Services → row delete |
| `polyflow config service list` | Config activity → Services list |
| `polyflow config link` | Config activity → Links section |
| `polyflow config link add` | Config activity → Links → "Add link" |
| `polyflow config link remove` | Config activity → Links → row delete |
| `polyflow config exclude` | Config activity → Index excludes section |
| `polyflow config exclude add` | Config activity → Index excludes → add pattern |
| `polyflow config exclude remove` | Config activity → Index excludes → row delete |
| `polyflow mcp` | Docs activity → Setup section, "Register with an agent (MCP)" — observability via the Ops/tool-calls log (UO.1) |
| `polyflow mcp on` | **exception** — shell-environment MCP registration; the UI *is* the human surface, MCP is the agent surface |
| `polyflow mcp off` | **exception** — same as `mcp on` |
| `polyflow mcp status` | Ops activity → tool-calls log shows live MCP call activity in place of a status snapshot |
| `polyflow eval` | Health activity → Eval section — `POST /api/jobs {kind:"eval"}` |
| `polyflow eval stamp` | **exception** — corpus-authoring tool for the eval harness, not a workspace operation |
| `polyflow eval promote-gaps` | **exception** — same as `eval stamp` |
| `polyflow eval agent` | **exception** — same as `eval stamp` |
| `polyflow doctor` | Health & trust dashboard |
| `polyflow reconcile` | Health activity → Reconcile section — `GET /api/reconcile/propose` |
| `polyflow rules` | **exception** — evidence-rule authoring for the reconcile harness, not a workspace operation |
| `polyflow rules promote <proposal.yaml>` | **exception** — same as `rules` |
| `polyflow models` | **exception** — local embedder model management (`--format`-shaped shell output; a one-time machine setup step, not workspace state) |
| `polyflow models pull` | **exception** — same as `models` |
| `polyflow bench` | **exception** — internal agent-benchmark harness (`eval/agent-bench`), not a user-facing workspace operation |
| `polyflow capture` | Runtime capture panel (Flows activity → Runtime tab) |
| `polyflow capture start` | Runtime capture panel → "Start recording" — `POST /api/capture/start` |
| `polyflow capture stop` | Runtime capture panel → "Stop recording" — `POST /api/capture/stop` |
| `polyflow capture run` | **exception** — wraps launching an arbitrary subprocess (`-- <command...>`) under capture; the browser cannot spawn local processes. Start/stop of the same capture session are both in the UI, and the session is visible/stoppable from either surface either way |
| `polyflow ingest <file>` | Runtime capture panel → "Ingest file" — `POST /api/capture/ingest` |
| `polyflow flows [<file>]` | Runtime capture panel → Coverage/flows tables — `GET /api/runtime/flows`, `GET /api/runtime/coverage` |
| `polyflow hook-context-inject` | **exception** — Claude Code hook plumbing (stdin/stdout JSON protocol for an external tool), not a workspace operation with UI-relevant state |

## Shell-oriented flags (exception, all commands)

`--format`, `--output`, and other output-shaping flags are excepted
wholesale: the UI *is* the format. Every other flag on every command above
is reachable through the corresponding UI control (documented per-command
in the CLI reference, Docs activity → CLI reference, which is generated
from the live cobra tree so it can never drift from this table's command
list).
