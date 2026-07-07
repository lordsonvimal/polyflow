# Phase 5 — Cross-Service Linker

**Status**: ⬜ Pending  
**Depends on**: Phases 1–4 complete  
**Design ref**: [Cross-Service Linking](../polyflow-design.md#cross-service-linking), [Constraint-Based Resolver](../polyflow-design.md#cross-service-linking--constraint-based-resolver)

---

## Goal

Make `internal/linker/` production-ready: connect HTTP client call nodes in one service to HTTP handler nodes in another, with confidence scoring. The skeleton (`linker.go`, `hints.go`) exists but only does exact string equality on raw `method + path`. This phase replaces that with a proper constraint-based resolver.

---

## Current State

`linker.go` — `Linker.Link()` does:
- Exact `routeKey(method, path)` string match
- No path normalization
- No confidence scoring

`hints.go` — `ApplyHints()` does:
- Base URL prefix stripping (ENV_VAR=URL format)
- No wildcard normalization after stripping

Both files compile and are wired into the graph model correctly.

---

## What Needs Building

### 1. Path Normalization (`linker.go`)

Replace the raw key comparison with a normalization step before matching:

| Framework pattern | Normalized |
|-------------------|-----------|
| `/users/:id` | `/users/*` |
| `/users/:id/posts/:postId` | `/users/*/posts/*` |
| `/users/[0-9]+` | `/users/*` |
| `/api/v1` + `/users` (Chi group) | `/api/v1/users` |

Rules:
- Replace `:param` segments with `*`
- Replace `{param}` (Go stdlib) segments with `*`
- Replace `[regex]` segments with `*`
- Strip trailing slashes

Add `normalizePath(path string) string` — pure function, no side effects.

### 2. Confidence Scoring

Extend `graph.Edge.Meta` to carry a `"confidence"` key. Values: `"static"`, `"inferred"`, `"partial"`, `"unknown"`.

| Match condition | Confidence |
|----------------|------------|
| Literal string exact match (before normalization) | `"static"` |
| Match only after wildcard normalization | `"inferred"` |
| Base URL resolved from hint + normalized match | `"inferred"` |
| Client call path is dynamic/unresolvable (no handler matched) | `"unknown"` — still emit the edge |

The last case is critical: a client node whose path couldn't be resolved must still emit a cross-service edge with `confidence: unknown`. This preserves the signal that a call exists even when the target can't be determined statically. Use a synthetic target ID of `"unresolved:<service>"` (or the service-level node if one exists) so the edge appears in the graph.

### 3. Base URL Resolution (`hints.go`)

Upgrade `ApplyHints` to resolve base URLs from `workspace.yaml` `links:` entries before the linker runs, not after:

New field in `workspace.Link`: `BaseURL string` (yaml: `base_url`). Update `workspace/config.go` accordingly.

```yaml
links:
  - from: nextgen-frontend
    to: nextgen-backend
    base_url: "/api"
```

`ApplyHints` strips the `base_url` prefix from matching client call paths before they reach `Linker.Link()`, enabling the linker to match `/api/users` (client) → `/users` (handler).

### 4. Linker Integration Test (`linker_test.go`)

New file `internal/linker/linker_test.go` with table-driven cases:

| Case | Input | Expected |
|------|-------|---------|
| Exact match | client `GET /users`, handler `GET /users` | 1 edge, `confidence: static` |
| Param normalization | client `GET /users/123`, handler `GET /users/:id` | 1 edge, `confidence: inferred` |
| Base URL hint | client `GET /api/users` with `base_url: /api`, handler `GET /users` | 1 edge, `confidence: inferred` |
| Method mismatch | client `POST /users`, handler `GET /users` | 0 edges |
| No match — unresolvable | client `GET /dynamic/${id}`, no matching handler | 1 edge, `confidence: unknown` |
| Cross-service only | client and handler in same service | 0 edges (same-service calls already tracked by `calls` edges) |

### 5. Hints Integration Test (`hints_test.go`)

New file `internal/linker/hints_test.go`:
- `ApplyHints` with `base_url` strips prefix correctly
- `ApplyHints` with `hint` (ENV_VAR=URL) annotates `target_service` meta correctly
- Nil/empty links slice is a no-op

---

## File Changes

| File | Action |
|------|--------|
| `internal/linker/linker.go` | Add `normalizePath()`, upgrade `Link()` to normalize before match, emit `confidence: unknown` edges for unresolvable calls |
| `internal/linker/hints.go` | Add `BaseURL` resolution; update `ApplyHints` to pre-process paths |
| `internal/workspace/config.go` | Add `BaseURL string` field to `Link` struct (yaml: `base_url`) |
| `internal/linker/linker_test.go` | New — table-driven tests for all match cases including `unknown` |
| `internal/linker/hints_test.go` | New — tests for hint resolution |
| `internal/linker/testdata/` | New — fixture workspace configs used in tests |

---

## Acceptance Criteria

- [ ] `normalizePath` converts `:param`, `{param}`, `[regex]` segments to `*`
- [ ] `Link()` matches normalized paths and sets `confidence` meta on emitted edges
- [ ] Unresolvable client calls emit an edge with `confidence: unknown` (not silently dropped)
- [ ] Same-service client→handler pairs produce no cross-service edges
- [ ] `ApplyHints` strips `base_url` prefix before match
- [ ] `linker_test.go` passes all 6 table cases
- [ ] `hints_test.go` passes
- [ ] `go test ./internal/linker/...` passes with ≥ 90% coverage
- [ ] No changes to `graph.Store` or `graph.Node/Edge` struct signatures

---

## What Is Explicitly Out of Scope

- OpenAPI/Swagger as authoritative override (v1.5)
- Constant propagation beyond base URL hints (v1.5)
- Message broker linking (Sidekiq, Bunny, Pusher) — those edges already exist via pattern matching; the linker only handles HTTP for v1
