# polyflow quickstart

Get from zero to a working MCP-served impact query in under five minutes.

## Prerequisites

- Go 1.22+ with CGO enabled
- SQLite (bundled via go-sqlite3)

## 1. Install

```sh
git clone https://github.com/lordsonvimal/polyflow
cd polyflow
make build
# Add dist/ to PATH, or run ./dist/polyflow directly.
```

## 2. Initialize your workspace

Run this in your project root (or a directory containing your services):

```sh
polyflow init
```

This creates `workspace.yaml` listing detected services.  Edit it if auto-detection misses a service:

```yaml
services:
  - name: api
    path: ./api
    language: go
  - name: web
    path: ./web
    language: typescript
```

## 3. Index

```sh
polyflow index
```

This walks every service, parses source files, and builds the graph.  Subsequent runs are incremental.

Verify the index is healthy:

```sh
polyflow doctor
```

If `doctor` reports zero-pattern-match services, check `polyflow patterns --list` to confirm your language and framework are supported.

## 4. Register the MCP server

```sh
claude mcp add polyflow -- polyflow mcp
```

Or for other MCP hosts, point them at `polyflow mcp` (stdio transport, no flags needed).

## 5. First impact query

```sh
polyflow impact --target <FunctionName>
```

Replace `<FunctionName>` with any function, class, or route in your codebase.  Example:

```sh
polyflow impact --target handleCheckout
```

The output shows every file that would need attention if `handleCheckout` changed, grouped by service, with a `verification_summary` block showing how many edges are confirmed vs. candidate.

## 6. (Optional) Runtime capture

To verify static edges with real traffic:

```sh
polyflow capture start my-session
# ... exercise your service ...
polyflow capture stop my-session
polyflow index   # re-index to fuse the captured evidence
```

See [docs/instrumentation.md](instrumentation.md) for per-stack setup.

---

**Next steps:**

- `polyflow trace --target <node>` — trace call chains
- `polyflow impact --diff` — blast radius of your current git diff
- `polyflow doctor --propose .polyflow/proposals` — auto-generate contract rules from runtime gaps
- `polyflow eval` — measure recall against ground-truth cases
