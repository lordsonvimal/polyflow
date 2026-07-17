# Polyflow — Plan 2: Modern Web-Framework Flows (Tier M)

Status legend: `pending` · `in progress` · `done`

> **Prerequisites.** The contract engine (G.0–G.5, `done`) and the JS
> variable/linker machinery (Phase 0.3, `done`). M.3 additionally needs
> nothing beyond G.4's additive-kind property. This is plan **2 of 6** —
> execute after plan 1 (B.0's unparsed ledger is how `.vue`/`.svelte`
> counts become visible before M.1/M.2 land).
>
> Follows `docs/phases.md`: one phase per commit, positive+negative fixtures,
> `BenchmarkIndexCold` held, SchemaVersion bump on stored-shape change, all
> nine bug-class rules binding. Rules 1 (fan-out), 2 (determinism), and
> 6 (capture hygiene + real-parse tests) are called out per phase where the
> prior incidents predict recurrence.

## Context

**Why.** The existing JS/TS coverage assumes *call-site route registration*
(`app.get("/x", h)`) and *ESM imports*. The dominant modern JS stacks broke
both assumptions:

- **File-based routing** (Next.js, SvelteKit, Nuxt, Remix): the route table
  is the *filesystem*. There is no call site for any pattern to match — an
  entire routing layer produces zero nodes today.
- **Vue and Svelte single-file components**: `.vue`/`.svelte` have no
  registered parser — whole frontends are invisible (not even ledger
  entries until B.0).
- **Typed RPC** (tRPC): the cross-boundary key is a *procedure path*, not a
  URL — no contract kind exists for it.
- **Angular**: routes live in decorator metadata and `RouterModule` arrays;
  HTTP calls go through an injected `HttpClient`.

Verified constraint: the pinned `smacker/go-tree-sitter` module ships a
`svelte` grammar but **no vue grammar** (checked 2026-07-17 against the
module inventory at `v0.0.0-20240827094217`). Vue therefore uses a
hand-rolled SFC splitter — the ERB precedent (L.W0).

Trust contract carried through: file-derived routes that can't be fully
reconstructed are ledgered, never guessed; every new producer flows through
the **existing** http/rpc contract rules with zero engine changes.

---

## Core model — route synthesis (M.0's pinned surface)

File-based routes are produced by a **workspace-level synthesis pass**, not a
per-file parser: the route's identity is the *path of the file*, so it needs
directory context no per-file `Parse` call has.

```go
// internal/linker/file_routes.go
// RouteConvention describes one framework's filesystem routing dialect.
// Conventions are data — adding a framework is a new table row + fixtures,
// never new traversal code.
type RouteConvention struct {
    Framework string   // "next-pages" | "next-app" | "sveltekit" | "nuxt" | "remix"
    // DetectDeps: the convention activates iff any of these packages appear
    // in the service's resolved npm deps (deps.Resolve already parses
    // package.json). Empty ⇒ never auto-active.
    DetectDeps []string
    // RootDirs: candidate route roots relative to the service dir, first
    // existing one wins (e.g. next-pages: ["pages", "src/pages"]).
    RootDirs []string
    // PageGlob / HandlerGlob: which files are pages (navigation targets)
    // vs API handlers (http_handler nodes). Go path.Match syntax per segment.
    PageGlob    string
    HandlerGlob string
}

// SynthesizeFileRoutes runs once per service after parsing, before the
// contract engine. It emits:
//   - NodeTypeRoute nodes for pages   (meta: path, defined_by=file_convention)
//   - http_handler nodes for API files (meta: method, path,
//     defined_by=file_convention)
//   - EdgeTypeComponentImpl route -> the file's default-export component /
//     exported handler function node when one exists (resolved via
//     NodesInFile; absent ⇒ edge to the NodeTypeFile node instead — the
//     route must never dangle)
//   - graph.UnresolvedRef{Kind: "route_convention_unresolved"} for any
//     special file the dialect table below doesn't cover.
func SynthesizeFileRoutes(svcDir, service string, files []string,
    deps []deps.Dependency, nodesInFile func(string) []graph.Node) Result
```

**Pinned path-mapping dialect table** (M.0 implements exactly these; anything
else in a route dir → ledger, never guessed):

| Framework | File | Route key emitted |
|---|---|---|
| next-pages | `pages/about.tsx` | page `/about` |
| next-pages | `pages/posts/[id].tsx` | page `/posts/:id` |
| next-pages | `pages/[...slug].tsx` | page `/*` (catch-all → wildcard) |
| next-pages | `pages/api/users/[id].ts` | http_handler `ALL /api/users/:id` |
| next-app | `app/dashboard/page.tsx` | page `/dashboard` |
| next-app | `app/(marketing)/pricing/page.tsx` | page `/pricing` (route groups `(x)` stripped) |
| next-app | `app/api/users/route.ts` | one http_handler **per exported verb** (`export async function GET` → `GET /api/users`) — parse exports via the existing TS patterns' function nodes in that file; no exported verb found → ledger |
| next-app | `app/@modal/...` (parallel), `app/[[...opt]]/...` (optional catch-all) | ledger `route_convention_unresolved` |
| sveltekit | `src/routes/blog/[slug]/+page.svelte` | page `/blog/:slug` |
| sveltekit | `src/routes/api/items/+server.ts` | http_handler per exported verb (same rule as next-app) |
| nuxt | `pages/users/[id].vue` | page `/users/:id` |
| nuxt | `server/api/items.get.ts` | http_handler `GET /api/items` (method from the suffix; no suffix ⇒ `ALL`) |
| remix | `app/routes/posts.$postId.tsx` | page `/posts/:postId` (`.` = `/`, `$x` = `:x`) |
| remix | file exports `loader`/`action` | additionally http_handler `GET <path>` / `POST <path>` |

`index`/`page`/`+page` filenames map to the directory path; `:param` output
uses the same `:x` syntax gin/rails patterns emit so `param_wildcard`
normalizes all of them to `*` identically (the F.1 `{x}`-vs-`:x` lesson —
one syntax on the polyflow side, converted at extraction).

**`ALL` method semantics:** an http_handler with `method: ALL` matches any
producer method — implement as meta `method: ""` so the engine's existing
`method_fallback` tiering applies unchanged (zero engine changes; verify
with a fixture, don't assume).

---

## Phases (one commit each)

### Phase M.0 — File-based route synthesis (Next/SvelteKit/Nuxt/Remix) `pending`

**Problem.** See table above — no call site exists, so today these repos have
zero route nodes and every `fetch("/api/users")` in them links to
`unresolved:<svc>`.

**Deliverable.** `SynthesizeFileRoutes` + the five convention rows + indexer
wiring (after per-file parsing, before `EnrichRouteGroups`/`Engine.Link`).
Detection is dep-driven (`next`, `@sveltejs/kit`, `nuxt`, `@remix-run/react`
or `@remix-run/node`); two frameworks detected in one service → both run
(fan-out, rule 1), overlapping route keys are legal. Path construction is
pure string work on repo-relative paths — no captures, but rule 6 still
applies to the *exported-verb* lookup (function labels come from the real
TS parse). Emission iterates files **sorted** (rule 2).

**Tests.** One fixture tree per framework under
`testdata/fileroutes/<framework>/` exercising every table row for that
framework, run through the **real indexer path** (rule 6); dynamic-segment
normalization test (`[id]` → `:id` → engine `*`); route-group strip test;
ledger tests (parallel route, optional catch-all, verb-less `route.ts`);
two-run determinism test; negative: a repo with a `pages/` dir but no
`next` dep → zero synthesized routes.

**Acceptance.** On the next-app fixture: `fetch("/api/users/" + id)` in a
component links `http_call` → the synthesized `ALL /api/users/:id` handler
via the existing `contracts/http.yaml` (zero engine/rule changes — assert the
rule file is untouched in the commit). A Tier E corpus repo using Next.js is
added with ≥10 hand-verified cases; recall recorded in `eval/baseline.json`
(the number closes the phase, per the Tier L convention).

### Phase M.1 — Vue SFC parser (splitter + delegation) `pending`

**Problem.** `.vue` files: no parser, no grammar in the pinned module.

**Deliverable.** `internal/parser/vue.go` registering `.vue` — a
**hand-rolled SFC splitter** (the ERB/L.W0 precedent; the top-level
`<template>`, `<script>`, `<script setup>`, `<style>` blocks are trivially
scannable): blank non-target blocks in place preserving byte offsets, then:

1. **Script ranges** → run the existing JS or TS patterns (lang from
   `<script lang="ts">`) with line-number correction. Imports, functions,
   signals (`ref`/`computed` map to the existing signal machinery only if a
   pattern is added — descope note: Vue reactivity retyping is NOT this
   phase; functions/imports/calls are).
2. **Template range** → treat as HTML: run the html patterns for elements
   with `id`/`class` (feeding L.W2's element index when it lands; until
   then the nodes still exist) and extract:
   - `@click="save"` / `v-on:click="save"` / `@submit.prevent="onSubmit"` →
     event edges. Reuse `normalizeEventName` (U.3) — extend its prefix list
     with `@` and `v-on:`; modifiers (`.prevent`, `.stop`) are stripped
     (add to the strip list, test-pinned). Handler expression `save` /
     `save(x)` → resolve the callee to a script-block function node
     (same-file, confidence `static`); member/complex expressions → the
     existing unresolved `call_ref` path.
   - `<router-link to="/games">` and `<NuxtLink to="/x">` → `http_client`
     node with `nav_link: "true"`, `method: GET`, `path` from `to` —
     flowing through the existing nav-link http rule variant. `:to="expr"`
     (bound) → G.6 walker treatment: literal-resolvable → candidates,
     else `dynamic_url` ledger.
3. Capture hygiene (rule 6): attribute values arrive quoted — strip via the
   matcher quote-strip list; test every extraction through a real `.vue`
   fixture file, not hand-built nodes.

**Tests.** SFC fixture (script setup + template events + router-link +
scoped style ignored); TS-lang fixture; line-offset test (an error/node in
the script block reports the original file line); negative: `<template>`
inside a string literal in the script block does not confuse the splitter
(the splitter only recognizes top-level blocks at column 0); `.vue` files
disappear from B.0's `unparsed_files` (assert the count drops).

**Acceptance.** On a 2-file fixture (component + imported store),
`impact --target useGameStore` includes the `.vue` component; the Nuxt
corpus repo from M.0 (if `.vue`-based) re-runs with template-event and
nav-link cases added.

### Phase M.2 — Svelte parser `pending`

**Problem.** `.svelte`: no parser. (A `svelte` tree-sitter grammar exists in
the pinned module, but the SFC-splitter approach is pinned here for
consistency with M.1/ERB — one mechanism, three formats; the grammar remains
the documented fallback if the splitter proves insufficient, recorded as a
deviation if taken.)

**Deliverable.** `internal/parser/svelte.go` registering `.svelte`, same
splitter architecture as M.1: `<script>`/`<script context="module">` ranges →
JS/TS patterns; markup range → html patterns plus:

- `on:click={save}` → event edge (`normalizeEventName` already handles the
  `on:` prefix per U.3 — verify, don't re-implement).
- `<a href="/x">` → nav-link (existing html patterns cover it — assert).
- `{#if}` blocks around attributes: the enclosed producer attributes ride
  the G.6 walker convention — a `href={cond ? "/a" : "/b"}` emits
  `key_candidates` (the JS walker already handles ternaries; wire the markup
  extraction to hand the expression node to `KeyWalkerFor("javascript")`).
- SvelteKit `export let data` / `load` functions come from M.0's route
  synthesis, not this phase (routes are filesystem-derived).

**Tests.** Mirror M.1's suite (fixture, offsets, events, nav, ternary-href
candidates, `unparsed_files` drop). Negative: a `.svelte` file with only
markup (no script) parses via html patterns alone.

**Acceptance.** SvelteKit corpus repo (from M.0) gains component-level
cases: `impact` on a store function includes the `.svelte` components that
call it.

### Phase M.3 — Typed RPC: tRPC kind + server actions `pending`

**Problem.** tRPC couples client and server on a *procedure path*
(`user.getById`), invisible to the http rule; Next server actions couple on
function identity through a compiler transform.

**Deliverable.**

1. **New contract kind `rpc`** (additive per G.4 — zero engine changes):
   `contracts/rpc.yaml` with producer `rpc_client` / consumer `rpc_handler`
   nodes (two new NodeTypes, SchemaVersion bump), key `[procedure]`,
   normalizers `[quote_strip, trim_slash]`, match `[exact]` only (procedure
   paths are exact identifiers — no wildcard tier), unmatched:
   `unknown_edge`.
2. **Server patterns** (`patterns/typescript/trpc.yaml`): procedure
   registration —

   ```ts
   export const userRouter = router({
     getById: publicProcedure.input(z.string()).query(({input}) => …),
     update:  protectedProcedure.mutation(…),
   })
   ```

   Each key of the `router({…})` object literal whose value chains from
   `*Procedure` → `rpc_handler` node, `procedure` meta = the key name.
   **Router nesting** (`router({user: userRouter})`) composes the full path
   via a meta-enrichment pass (the G.3 route-group precedent, same
   file-scope limits: routers composed across files resolve through the
   import map; unresolvable composition → ledger `rpc_router_unresolved`,
   the affected handlers keep their *local* name as key with
   `Meta["key_partial"]="true"` — surfaced, not guessed).
3. **Client patterns:** `trpc.user.getById.useQuery(…)` /
   `client.user.getById.query(…)` → `rpc_client` node, `procedure` =
   the member chain between the root object and the verb
   (`user.getById`). Root-object identification: any identifier whose
   type/import traces to `createTRPCProxyClient`/`createTRPCReact`
   (imports-first; unresolvable roots → ledger `rpc_client_unresolved`,
   never a bare guess).
4. **Next server actions:** functions in files with the `"use server"`
   directive get `Meta["server_action"]="true"`. Their cross-boundary use is
   plain ESM import + call — **already linked** by Phase 0.3/I.3 machinery;
   `action={saveForm}` JSX props are covered by B.1's func-arg edges.
   Deliverable here is only the meta stamp + a fixture proving the chain
   closes (assert, don't assume).

**Tests.** 2-service fixture (standalone server + client) asserting the
cross-service `rpc_call` edge; nested-router composition; cross-file router
ledger negative; wrong-chain negative (`somethingElse.user.getById` with no
tRPC import → no producer node); server-action end-to-end fixture; fixture
per pattern file (the no-fixture-CI-fail rule).

**Acceptance.** `polyflow trace` on the fixture closes
`component → rpc_client user.getById → rpc_handler → db call`; a tRPC-using
OSS repo is added to the Tier E corpus with ≥8 cases.

### Phase M.4 — Angular: routes, HttpClient, template events `pending`

**Problem.** Angular repos index to disconnected TS classes: decorator
routing, DI, and external templates carry all the flow.

**Deliverable.** `patterns/typescript/angular.yaml` + one linker addition:

1. **Routes:** `RouterModule.forRoot([...])` / `forChild` / standalone
   `provideRouter([...])` route arrays: each object literal with `path:` +
   `component:` → `NodeTypeRoute` (path meta, `:param` from `:id` segments
   as-is) + `component_impl` edge to the component class node
   (imports-first resolution; `loadComponent`/`loadChildren` dynamic
   imports: literal `import("./x")` argument → resolve the file's default
   component, else ledger `route_lazy_unresolved`).
2. **HttpClient producers:** `this.http.get<T>("/api/x")` / `post/put/
   delete/patch` where the receiver is a constructor-injected
   `HttpClient`-typed property → `http_client` node (method from the member
   name, url arg through the standard quote-strip + G.6 walker). Receiver
   typing check is best-effort: any member call `*.http.<verb>(<string>)`
   with an `@angular/common/http` import in-file qualifies (`where` gate on
   the import — precision guard against generic `.get()` false positives).
3. **External templates:** `@Component({templateUrl: "./x.html"})` →
   `imports` edge component-file → template-file (file nodes, the I.3
   convention) so template edits appear in component blast radius; the
   `.html` file already parses via html patterns; `(click)="save()"`
   event-binding extraction: extend the html patterns' event-attr capture
   to the `(event)` parenthesized form, normalized by `normalizeEventName`
   (add `(x)` → `x`), handler resolved to the component class's method
   **via the templateUrl link** (linker addition: template-element event →
   method of the component that declared the template; unresolvable →
   existing unresolved path).
4. **DI descope (written claim):** injector-token indirection
   (`inject(TOKEN)`, provider maps) is *not* resolved — constructor
   param-type `uses_type` edges (existing TS machinery) plus I-tier
   `implements` edges already connect interface-typed injection to
   implementations; full DI graph resolution is recorded here as
   considered-and-deferred, not silently absent.

**Tests.** Route-array fixture (incl. lazy + ledger negative); HttpClient
fixture (positive + a non-HttpClient `.get()` negative); templateUrl event
round-trip through real files (rule 6); fixture per pattern.

**Acceptance.** On an Angular fixture app: `impact --target UserService`
includes the components injecting it and the templates wired to their
methods; an Angular OSS repo joins the Tier E corpus (≥10 cases, number
recorded).

---

## Key files

- **New:** `internal/linker/file_routes.go`, `internal/parser/vue.go`,
  `internal/parser/svelte.go`, `contracts/rpc.yaml`,
  `patterns/typescript/{trpc,angular}.yaml`, fixtures under
  `testdata/fileroutes/`, `testdata/sfc/`, `testdata/contracts/rpc/`.
- **Modify:** `internal/indexer/indexer.go` (M.0 wiring),
  `internal/graph/model.go` (`rpc_client`/`rpc_handler` node types,
  `rpc_call` edge type; SchemaVersion bumps), `patterns/html/events.yaml`
  ((event) form), `internal/patterns/matcher.go` event-name prefix lists,
  `internal/indexer/unparsed.go` (`.vue`/`.svelte` leave the unparsed set).

## Risks / honest notes

- **Conventions drift by major version** (Next pages→app router already
  did). The convention table is data; a new dialect is a new row +
  fixtures, and the versioning plan's `package:`/`version_range:` gate
  applies to convention rows exactly as to patterns (wire `DetectDeps`
  through `deps.Resolve` versions when a dialect is version-split).
- **Vue/Svelte compiler magic** (auto-imports in Nuxt, Svelte stores `$x`
  syntax) is not fully modeled; unresolved references stay surfaced via the
  standard ledgers. Descoped explicitly, revisit driven by eval numbers.
- **tRPC root detection is heuristic** across re-exports; the
  `rpc_client_unresolved` ledger is the honesty valve.

## Sequencing

```
M.0 ─> M.1 ─> M.2 ─> M.3 ─> M.4
        (M.1/M.2 independent of each other; M.0 first because Nuxt/SvelteKit
         fixtures need routes to link against; M.3/M.4 independent of M.1/M.2)
```

Each phase that adds a corpus repo ratchets `eval/baseline.json` in the same
commit (Tier E rules).
