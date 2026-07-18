# Plan 7 — Fleet Stack Coverage (Tier H): Express, ws server, Solid Router, delayed_job, per-repo corpora

Status legend: `pending` · `in progress` · `done`

> **Prerequisites — execute after `docs/plan-6-scale-monorepo.md` N.0–N.2 and
> before N.3** (N.3 documents whatever shipped and runs strictly last).
> Needs: contract engine (done), L.W phases (done), Tier E harness (done).
> Every phase follows `docs/phases.md` (one phase per commit, outcome note in
> the same commit, bug-class rules 1–12 binding). Read ONLY this file plus
> `docs/phases.md` to implement any phase here.

## Context

The 2026-07-18 fleet audit checked polyflow's pattern inventory against the
author's seven real repos. Five stacks/scenarios are uncovered and in no
existing plan:

1. **Express server routes** (`synergy/apps/tether`, any Node backend) — no
   `express` pattern exists; verb-registered routes are invisible.
2. **`ws` server-side WebSocket** (`tether` is primarily a `WebSocketServer`,
   not a REST app) — `patterns/javascript/websocket.yaml` covers only the
   browser client `new WebSocket(url)`.
3. **Solid Router** (`synergy/apps/calendar-ui`) — `<Route path=... component=...>`
   client routes; real code uses **constant refs** (`path={clientRoutes.home}`),
   not string literals.
4. **delayed_job dispatch** (`nextGen`) — Ruby patterns exist
   (`patterns/ruby/delayed_job.yaml`) but no contract rule in
   `contracts/jobs.yaml` links producer→consumer.
5. **No measured recall number for any fleet repo** except chessleap —
   breadth without an eval number doesn't count (Tier E rule).

Trust contract unchanged: recall over precision; no silent gaps; every
non-literal key → `key_dynamic`/ledger, never guessed.

**Query-text rule for every pattern phase here:** the tree-sitter query text
shown is the intended shape; if the actual grammar node names differ, adapt
the query — but the **capture names, roles, fixture files, and
expected.json contents are the contract and may not change**. A fixture
mismatch is a bug in the query, never a license to edit expected.json.

---

### Phase H.0 — Express server routes `pending`

**Problem.** `app.get("/api/x", handler)` / `router.post(...)` produce no
`http_handler` nodes. Express apps have zero server-side surface.

**Deliverable.** `patterns/javascript/express_routes.yaml` — new file, YAML
only, zero engine/matcher code changes (G.4 additive property). Header:

```yaml
language: javascript
version: "1"
package: express
patterns:
```

Three patterns (names pinned — `classifyPattern` maps `*_route`→HTTPHandler
via the existing "route" heuristic; verify with the fixture, do not edit
`classifyPattern` unless the fixture fails, and record it if you must):

1. `express_verb_route` — receiver-agnostic verb registration:

```yaml
  - name: express_verb_route
    query: |
      (call_expression
        function: (member_expression
          object: (identifier) @recv
          property: (property_identifier) @method
          (#match? @method "^(get|post|put|patch|delete|head|options|all)$"))
        arguments: (arguments
          .
          (string) @path
          (_) @handler))
    captures:
      - name: method
        role: http_method
      - name: path
        role: http_path
      - name: handler
        role: handler
```

   Receiver is deliberately any identifier (`app`, `router`, `api`) — the
   `package: express` gate plus the `(string, callback)` argument shape is
   the disambiguator. Known accepted false-positive class: none in the JS
   corpus today (`$.get(url, cb)` is jQuery — those services do not depend
   on `express`, so the gate suppresses the file's patterns; if a service
   ever has both, recall-over-precision accepts the extra candidate edges).

2. `express_verb_route_dynamic` — same shape but non-string first arg
   (`app.get(ROUTES.user, handler)`): capture the expression as `@path`
   with role `http_path`; the matcher's existing dynamic-key handling
   (G.6) stamps `key_dynamic=true` + `dynamic_url` ledger. Never guessed.

3. `express_mount` — `app.use("/prefix", router)`: capture `@path` (string)
   and `@handler`. **Prefix reconstruction across files is NOT attempted**
   (precedent: `gin_route_chained` — recorded descope). Emit the node so
   the mount is visible; meta `mount=true`.

**Path syntax note (rule 6 + normalizer parity):** Express params are
`/users/:id` — the same `:param` syntax the `param_wildcard` normalizer
already maps to `*`. No normalizer changes.

**Fixtures.** `patterns/javascript/express_routes_test/`:
- `input.js` (positive) — pinned content:

```js
const express = require("express");
const app = express();
const router = express.Router();
app.get("/api/users/:id", getUser);
router.post("/api/users", (req, res) => { res.send("ok"); });
app.use("/api/v2", router);
app.get(ROUTES.health, healthCheck);
function getUser(req, res) {}
function healthCheck(req, res) {}
```

  `expected.json`: 3 `express_verb_route`-family matches with
  meta.method/path = (`get`,`/api/users/:id`), (`post`,`/api/users`),
  (`get` + dynamic for `ROUTES.health`); 1 `express_mount`.
- `negative.js` — jQuery-style `$.get("/x", cb)` and a `map.get("k")`
  2-arg-but-not-route call... **no**: `$.get("/x", cb)` structurally
  matches on purpose (gate suppresses it — see above). `negative.js` must
  contain only shapes that fail structurally: `map.get("key")` (one arg),
  `app.get("/x")` (no handler arg), `fetch("/x", opts)` (not a member
  call). Zero matches asserted.

**Tests.** The standard `TestPatternFixtures` subtest (automatic once the
fixture dir exists). Plus one e2e test
`internal/e2e/express_fetch_test.go` mirroring
`python_go_test.go` exactly: svc-node (express, `package.json` with
`"express"` dep, the input.js routes) + svc-web (a JS file with
`fetch("http://svc-node/api/users/42")`), assert ≥1 cross-service
`http_call` edge svc-web→svc-node, plus a two-run determinism check
(rule 2).

**Acceptance.** The e2e fixture links with only YAML added. Reindex
`~/Projects/synergy`; tether's service gains `http_handler` nodes (count
recorded in the outcome note).

### Phase H.1 — `ws` server-side WebSocket `pending`

**Problem.** `new WebSocketServer({...})` + `wss.on("connection", handler)`
(tether's actual architecture) produce nothing; the `websocket` contract
kind has a client side only.

**Deliverable.** Extend `patterns/javascript/websocket.yaml` (same file —
it owns the kind) with two patterns, `package: ws` gated via a **separate
file** `patterns/javascript/ws_server.yaml` (a file has one `package:`
gate; the browser client patterns must stay ungated):

1. `ws_server_new` — `new WebSocketServer({...})` (also match
   `new WebSocket.Server({...})`, the CJS form) → node type via name
   (`*_server_*` is not in `classifyPattern`; pin role): captures:
   `- name: server / role: subscriber`. Emits a subscriber-side node the
   existing `contracts/websocket.yaml` rule can join against a client
   `new WebSocket("ws://host/path")`. **Key:** the ws server has no path
   in the constructor when attached to an HTTP server (`{server}` /
   `{noServer:true}`) — in that case stamp `key_dynamic=true` with raw
   `"(attached)"`; when `{port: N}` or `{path: "/x"}` object keys are
   literal, capture `@path` if present.
2. `ws_server_connection` — `X.on("connection", handler)` → handler edge
   from the server node to the handler function (captures: `event`
   role `event_type`, `handler` role `handler`).

**Fixtures.** `patterns/javascript/ws_server_test/`: positive `input.js`
with a `{path: "/terminal"}` server + connection handler + one
`{noServer:true}` dynamic case; `negative.js`: `emitter.on("connection",
h)` where no WebSocketServer exists in file — the `.on` pattern must be
anchored to require a same-file `ws_server_new` capture… tree-sitter
cannot express cross-node anchoring; instead pin: `ws_server_connection`
matches any `.on("connection", h)` and is **gated by `package: ws`** —
services without the dep never run it; the negative therefore asserts the
structural negatives only (`.on("message", h)` → no match for this
pattern name; wrong-arity). Record this precision trade in the pattern
file comment.

**Tests.** Fixture subtests; websocket contract-rule join e2e: ws server
`{path:"/terminal"}` in svc-a + browser `new WebSocket("ws://svc-a/terminal")`
in svc-b → cross-service `websocket` kind edge (assert exact edge type
from `contracts/websocket.yaml`). Determinism run.

**Acceptance.** Reindex synergy: tether shows the WebSocketServer node,
`connection` handler edge, and pwa→tether websocket edge or an honest
`dynamic` ledger entry (record which, with counts).

### Phase H.2 — Solid Router client routes + nav `pending`

**Problem.** `<Route path={clientRoutes.home} component={Home} />` produces
nothing. Client-side route topology and nav for Solid apps is invisible.
Plan-2's M.0 covers file-based routing only — component-config routing is
this phase.

**Deliverable.** `patterns/jsx/solid_router.yaml`, `package: "@solidjs/router"`:

1. `solid_route` — JSX element named `Route`: capture `path` attribute value
   (`@path`, role `http_path` — string literal or expression) and
   `component` attribute value (`@component`, role `component_ref`).
   Literal path → route node meta.path; expression path (the real-world
   case: `clientRoutes.home`) → `key_dynamic=true` + `dynamic_url` ledger
   — **and** a follow-the-constant step: the existing
   `patterns/javascript/constants.yaml` + JS linker already index
   `export const clientRoutes = {home: "/", ...}` object members; emit the
   raw member expression (`clientRoutes.home`) in meta `key_ref` so the
   G.6 `KeyWalkerFor("javascript")` can resolve it to `key_candidates` when
   the object literal is same-service. If the walker cannot resolve, the
   ledger entry stands. Never guess.
2. `solid_route_component_edge` — `renders` edge from the route node to the
   component named in `@component` (same-service label lookup, confidence
   `inferred`; miss → unresolved `component_ref`).
3. `solid_nav_link` — `<A href="/x">` → nav `http_client` node (mirror
   `patterns/jsx/nav_links.yaml` conventions; add there if a shared shape
   fits — decide by reading that file first, record the choice).

**Fixtures.** `patterns/jsx/solid_router_test/`: positive `input.tsx` with
one literal `<Route path="/settings" component={Settings} />`, one
constant-ref route (with the `clientRoutes` object in the same file so the
walker can resolve it), one `<A href="/settings">`; `expected.json` pins:
2 route nodes (one with meta.path=/settings, one with
key_candidates containing the resolved constant), renders edges, 1 nav
node. `negative.tsx`: `<Route>` with no path attr, `<A>` with expression
href → dynamic ledger, a non-router `<Route>`-named local component is
indistinguishable — accepted (gate + recall-over-precision, note in file).

**Tests.** Fixture subtests + one matcher-level test running input.tsx
through the real parse (rule 6) asserting the constant-ref route resolves
to `key_candidates=["/"]`-style values, not a guessed literal.

**Acceptance.** Reindex synergy: calendar-ui shows 3+ route nodes matching
`App.tsx`, each either resolved (key_candidates) or ledgered; recorded in
the outcome note with exact counts.

### Phase H.3 — delayed_job contract rule `pending`

**Problem.** `patterns/ruby/delayed_job.yaml` emits producer/consumer nodes
but `contracts/jobs.yaml` has no delayed_job rule — enqueues never link.

**Deliverable.** One rule in `contracts/jobs.yaml`, kind `job`, mirroring
the sidekiq rule's structure exactly (read it first; copy its
producer/consumer/key/normalizer shape). Key = the job class name
(delayed_job's `SomeJob.delay.perform`, `Delayed::Job.enqueue SomeJob.new`,
`handle_asynchronously :method` — cover whichever forms the *pattern file
already captures*; do NOT extend the pattern file in this phase; if a form
isn't captured, it is already in the ledger and stays there).

**Fixtures.** Contract-rule fixture per the repo convention (positive
2-service enqueue→worker pair, negative same-shape-wrong-package). Check
`contracts/` fixture layout by reading how the sidekiq rule is tested and
mirror it.

**Tests.** Rule fixture tests; one case added to the nextGen corpus (H.4)
targets a delayed_job flow.

**Acceptance.** 2-service fixture links enqueue→job class; nextGen (H.4)
reports ≥1 passing delayed_job case.

### Phase H.4 — Fleet corpora, Go repos: synergy + mysycamore + datascience `pending`

**Problem.** No fleet repo except chessleap has a recall number.

**Deliverable.** Three local path-based corpus entries (the chessleap
precedent — `eval/corpus/<name>/manifest.yaml` with `path:`, pinned sha,
committed workspace; `make eval-corpus` skips them; CI gate exempts via
`SkippedCorpus.LocalOnly`):

| corpus | path | min cases | must cover |
|---|---|---|---|
| synergy | `eval/.cache/synergy` → `~/Projects/synergy` | 12 | 3 express/H.0, 2 ws/H.1, 3 solid-router/H.2, 2 Go gin, 2 cross-service |
| mysycamore | `eval/.cache/mysycamore` → `~/Projects/mysycamore` | 12 | gin routes, templ views, SSE, 2 test-file callers (rule 8) |
| datascience | `eval/.cache/datascience` → `~/Projects/datascience` | 15 | gin, gorilla/websocket, amqp091 publish→consume across dsw-manager/dsw-agent, grpc, 2 test-file callers |

Ground truth is **hand-verified by grep/read of the pinned sha — never
generated by polyflow** (E.2 rule; circular truth is the failure mode).
Every case has ≥1 `must_not_miss`. Symlink setup documented in each
manifest header (the chessleap comment format).

**Tests.** Manifest schema + must_not_miss lint (existing tests pick up new
corpus dirs automatically — verify, don't assume).

**Acceptance.** `polyflow eval` reports all three; `eval/baseline.json`
ratcheted in the same commit; hard-fails are FIXED or recorded as named
open gaps in this doc before the phase closes (rule 9: no luck, no
re-baselining around real gaps).

### Phase H.5 — Fleet corpora, Rails repos: nextGen + CDR-Agent + SCE-Agent `pending`

**Deliverable.** Same mechanics as H.4:

| corpus | path | min cases | must cover |
|---|---|---|---|
| nextgen | `eval/.cache/nextgen` → `~/Projects/nextGen` | 15 | 3 ERB nav (L.W0), 2 jQuery ajax (L.W2), 2 delayed_job (H.3), 2 bunny publish, 3 controller blast-radius, 2 react/vite, 1 test-file caller |
| nextgen-cdr | `eval/.cache/nextgen-cdr` → `~/Projects/nextGen-CDR-Agent` | 10 | rails controllers, bunny consume, rest-client calls |
| nextgen-sce | `eval/.cache/nextgen-sce` → `~/Projects/nextGen-SCE-Agent` | 10 | plain-Ruby lib blast radius, rest_client/net_http calls, 1 test-file caller |

**Known risk to confront, not avoid:** lobsters sits at 0.400 from FTS
ambiguity on common Rails action names; nextGen will hit the same class.
Cases must target uniquely-resolvable entities *or* assert the specific
edge path (rule 9) — and where ambiguity still bites, the failing case
stays in the corpus as a named open gap driving plan-1 B-work; it is
never deleted to make the number look better.

**Acceptance.** All three repos report recall in `eval/baseline.json`;
per-repo numbers + open-gap list recorded in the outcome note. **nextGen
recall ≥0.85 with 0 silent misses is the bar for calling the Rails fleet
professionally usable**; below that, the specific failing cases become
the work list (do not close the phase by lowering the bar).

---

## Key files

- New: `patterns/javascript/express_routes.yaml` + fixtures,
  `patterns/javascript/ws_server.yaml` + fixtures,
  `patterns/jsx/solid_router.yaml` + fixtures,
  `internal/e2e/express_fetch_test.go`,
  `eval/corpus/{synergy,mysycamore,datascience,nextgen,nextgen-cdr,nextgen-sce}/`.
- Modified: `contracts/jobs.yaml` (H.3), `eval/baseline.json` (H.4/H.5),
  this doc (status + outcome notes per phase).

## Verification

Standard per-phase: fixtures positive+negative, two-run determinism where a
set is produced, `BenchmarkIndexCold` hold, full suite green, eval gate
clean. Rules 10–12 (`docs/phases.md`) apply: any node-removal interplay
needs the dangling-edge check; coverage claims only over confirmable kinds.
