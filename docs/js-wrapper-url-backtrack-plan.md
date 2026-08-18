# Tier WB — JS wrapper param/key backtracking for producer_alias calls `in progress`

Status legend: `pending` · `in progress` · `done (commit <sha>)`.

**Prerequisites: none.** Purely additive to `patterns/javascript/producer_alias.yaml`
and `internal/contract/alias.go`. No dependency on any other pending tier; can land
independently of the roadmap in `docs/phases.md`.

---

## Why

`patterns/javascript/producer_alias.yaml` detects HTTP calls made through a local
wrapper function — `apiFetch('/foo')`, `apiFetch({ url: '/foo' })` — via two
patterns:

- `producer_alias_url_call` — takes the **first positional argument**,
  unconditionally, as the URL.
- `producer_alias_obj_call` — takes the value of an object-literal key, but only
  if that key is the **literal string `"url"`** (`#eq? @key "url"` at
  `patterns/javascript/producer_alias.yaml:70`).

Both patterns are typed `http_client` unconditionally by
`classifyPattern`'s `producer_alias_` prefix rule
(`internal/patterns/matcher.go:1693-1694`) — **independent of whether the callee
name resolves to a known alias.** `EnrichAliases`
(`internal/contract/alias.go:50`) only rewrites `Meta` for calls it can resolve;
for an unknown callee it strips `via_alias` and passes the node through unchanged
(`alias.go:160-164`). This is deliberate — `docs/phases.md`'s Trust contract says
*recall over precision; no silent gaps* — so any fix here must stay additive and
must not start dropping nodes it can't explain.

The gap: real wrappers name their URL parameter `uri`, `anyurl`, `endpoint`,
`path`, or destructure it under any name at all. `producer_alias_obj_call`
silently produces **zero edge** for `apiFetch({ uri: '/foo' })` — not a ledgered
gap, just nothing. And `producer_alias_url_call`'s index-0 assumption is
silently *wrong*, not absent, for a wrapper whose signature is
`apiFetch(options, uri)` — it would tag `options` as the URL. A wrong stamp is
worse than a missing one under the Trust contract, because nothing flags it for
review.

The fix: instead of guessing a key name, read the wrapper's **own function
body**. If `apiFetch` calls `fetch(uri)` internally, `uri` (or whatever it's
called) is provably the URL parameter — by position, by destructured key, or by
member-access key (`opts.uri`). Every call site then picks the matching
argument, however it's named, instead of hard-coding `"url"`.

## Core model

This mirrors machinery that already exists twice, in two different flavors:

1. **JS same-file alias/wrapper tables** (`internal/contract/alias.go`) — an
   `aliasTable` maps `service+file+varName → producer info`, built from binding
   nodes (`const f = fetch`) and consumed by call-site nodes (`f(...)`). A
   parallel `wrapperTable` (`wrapperEntry`, `alias.go:8`) already exists for
   exactly this "wrapper function" case — **but no JS pattern populates
   `wrapper_for`/`wrapper_name` today** (confirmed: zero matches outside
   `alias.go` and its test). This plan is what finally drives that table, with
   one new field: which parameter carries the URL.
2. **Go's SSA wrapper-URL propagation** (`internal/parser/go_wrapper_urls.go`,
   `paramURL`/`paramIndex`, `findURLParamWrappers`) — the real analog. It works
   because `go/ssa` gives a whole-program parameter↔call-site-argument graph for
   free. **JS has no equivalent** (tree-sitter + a lightweight linker, no SSA),
   so this plan is pattern-based and same-file only, matching the existing
   `indirKey(service, file, name)` scoping `aliasTable`/`wrapperTable` already
   use. Cross-file wrapper resolution is an explicit non-goal (see below).

New per-wrapper facts, captured from the wrapper's own function body:

```
wrapper_url_param_index   // "0", "1", ... — positional case
wrapper_url_param_key     // "uri" — destructured-param or member-access case
```

At most one of these is set per wrapper (a wrapper either forwards a bare
identifier positionally/via destructuring, or accesses a property off an
un-destructured options param — never both in the same call).

## Phase WB.1 — Wrapper-body detection patterns `done`

**Deviation from the plan as written:** shipped without the WB.1a
positional-forward shape. For a sole/first parameter, positional forwarding
doesn't change which argument `producer_alias_url_call` already picks (index
0) — there was nothing for a downstream phase to resolve differently until
WB.4's multi-param case, which is itself corpus-gated and may never be built.
Building WB.1a now would have been dead bookkeeping with no consumer. Shipped:
12 patterns in `patterns/javascript/producer_wrapper_body.yaml` covering the
member-access (WB.1b) and destructured/renamed (WB.1c) shapes only, for both
`fetch` and `axios.<verb>` callees, across `function_declaration` and
arrow/`function_expression` forms. `classifyPattern` types `wrapper_url_*` as
inert `NodeTypeVariable` bookkeeping (`internal/patterns/matcher.go`), same
discipline as `*_alias_binding`/`gin_group_registrar` markers — no edges until
WB.3 consumes them. Positive/negative fixtures:
`patterns/javascript/producer_wrapper_body_test/`.


**Problem:** No pattern captures "this function forwards one of its own
parameters into a `fetch`/known-alias/`axios.<method>` call."

**Deliverable:** new `patterns/javascript/producer_wrapper_body.yaml`, three
patterns covering the three shapes a wrapper takes. All three match
`function_declaration`, `(name) => ...` arrow assigned via
`variable_declarator`, and `function_expression` assigned via
`variable_declarator` — i.e. any of the three ways a named wrapper is defined.

**WB.1a — positional forward** (worked example):

```js
function apiFetch(uri) {
  return fetch(uri);
}
```

```
(function_declaration
  name: (identifier) @wrapper_name
  parameters: (formal_parameters
    (identifier) @param_name . )        ; first param, by position
  body: (statement_block
    (return_statement
      (call_expression
        function: (identifier) @callee
        (#match? @callee "^(fetch)$")     ; extended to known in-file aliases at link time
        arguments: (arguments . (identifier) @arg_name (#eq? @arg_name @param_name))))))
```
captures: `wrapper_name` role `wrapper_url_target`, param position derived from
its index among sibling `(identifier)` params in `formal_parameters` (tree-sitter
can't emit an index directly — capture **all** params as `@param_name` in
source order and let the linker compute the index by matching `@arg_name`'s
text against the Nth `@param_name`, same trick `producer_alias_obj_call`
already relies on for ordered sibling capture).

**WB.1b — member-access forward** (worked example):

```js
function apiFetch(opts) {
  return fetch(opts.uri);
}
```
matches `arguments: (arguments . (member_expression object: (identifier) @param_name property: (property_identifier) @url_key))`
where `@param_name` is the (sole) formal parameter. Captures `wrapper_name` +
`url_key` role `wrapper_url_param_key`.

**WB.1c — destructured-param forward** (worked example):

```js
function apiFetch({ uri }) {
  return fetch(uri);
}
// renamed:
function apiFetch({ uri: myUri }) {
  return fetch(myUri);
}
```
matches `parameters: (formal_parameters (object_pattern (shorthand_property_identifier_pattern) @key ))`
for the shorthand case, and
`(object_pattern (pair_pattern key: (property_identifier) @key value: (identifier) @local))`
for the renamed case; body match requires the call's argument identifier to
equal `@local` (shorthand: equal `@key`). Captures `wrapper_name` + `key`
(the **source key**, not the local rename) role `wrapper_url_param_key`.

Wrapper-body detection recognizes a `fetch`/`axios.<method>` call directly, or a
call to any identifier already known in the same file as an alias (via
`fetch_alias_binding`/`axios_destructure`/etc.) — reuse the existing
`#match?` verb list from `patterns/javascript/axios.yaml:11`, plus a same-file
alias check done at link time (WB.3), not at pattern time (a tree-sitter query
can't see across pattern files).

**Tests:** `patterns/javascript/producer_wrapper_body_test/` — positive
fixtures for WB.1a/b/c, one `negative.js` covering a function that calls
`fetch` with a **literal** (not a forwarded param — must NOT match, that's
already `fetch_call`'s job) and one where the forwarded identifier is a
**local variable**, not a parameter (must NOT match — no param to backtrack to).

**Acceptance:** all three shapes produce exactly one wrapper-fact node each;
existing `fetch_call`/`axios_request` fixtures unaffected.

## Phase WB.2 — Generalize call-site key capture `done`

**Shipped as written**, with one addition beyond the plan text: `key` is now
also declared as a capture (`role: obj_key`) so the value survives into
`Meta["key"]` for WB.3 to read (the query already captured `@key` for the old
`#eq?` predicate — WB.2 just stopped throwing it away). Verified against the
real chessleap corpus, not just fixtures: the golden diff
(`internal/contract/testdata/golden/chessleap_contract_edges.json`) is exactly
one new edge, additive-only — `room-controller.js`'s
`createSignalingClient({ baseURL: \`/liveclass/sessions/${sessionID}/signal\` })`
was silently invisible before (key named `baseURL`, not `url`) and is now
correctly linked to its `gin_route` handler. No edges lost, no ID collisions
observed (multi-key objects in the fixture didn't appear more than once in the
real corpus at the same call site in a way that collided).

**Problem:** `producer_alias_obj_call` only ever captures the `"url"` key;
`producer_alias_url_call` only ever captures argument index 0. Neither leaves
room for the linker to pick a different one.

**Deliverable:** replace `producer_alias_obj_call`'s hard-coded `#eq? @key
"url"` with an unfiltered capture — precedent already exists for this exact
shape in `patterns/javascript/constants.yaml` (`(pair key: (property_identifier)
@key value: (string) @value)` with no equality predicate):

```
  - name: producer_alias_obj_call
    query: |
      (call_expression
        function: (identifier) @via_alias
        arguments: (arguments
          (object
            (pair
              key: (property_identifier) @key
              value: [(string) (template_string)] @url))))
    captures:
      - name: via_alias
        role: alias_caller
      - name: key
        role: obj_key
      - name: url
        role: http_url
```

One call site with N object keys produces N candidate nodes, sharing the same
`call_expression` span (service+file+**line+column**, not line alone — two
wrapper calls can share a line in minified/generated JS, a Risk called out
below). The linker (WB.3) groups by that span and keeps exactly one.

`producer_alias_url_call` is unchanged in shape (still index-0 only); WB.4
(stretch) revisits it.

**Tests:** update `patterns/javascript/producer_alias_test/input.js` with a
multi-key call (`apiFetch({ uri: '/a', method: 'GET' })`) and assert **both**
candidate nodes are captured pre-link (this test lives at the pattern-matcher
level, before `EnrichAliases` runs — matcher output, not final graph output).

**Acceptance:** existing `producer_alias_obj_call` fixtures for the literal
`"url"` key still pass unchanged (single-key objects still produce exactly one
candidate).

## Phase WB.3 — Linker resolution

**Problem:** `EnrichAliases` has no table for WB.1's per-wrapper facts and no
logic to pick one candidate out of WB.2's group.

**Deliverable**, in `internal/contract/alias.go`:

```go
type wrapperURLTarget struct {
    ParamIndex int    // -1 if unset
    ParamKey   string // "" if unset
}

// keyed by indirKey(service, file, wrapperName) — same scoping as aliasTable/wrapperTable
wrapperURLTable := make(map[string]wrapperURLTarget)
```

Pass 1 (alongside the existing `alias_name`/`instance_name`/`wrapper_for` loop):
populate `wrapperURLTable` from WB.1's `wrapper_url_target` /
`wrapper_url_param_key` meta.

Pass 2, new branch before the existing `via_alias` handling: group
`producer_alias_obj_call` candidates by call-site span
(`service+file+line+col`, a new helper `spanKey`), then per group:

1. If the callee resolves in `wrapperURLTable` with a `ParamKey`: keep the
   candidate whose `obj_key` meta equals it, drop the rest. No ledger entry —
   this is a resolved case.
2. Else (callee unknown, or resolved with only `ParamIndex` — irrelevant to an
   obj-style call): fall back to a fixed priority list `["url", "uri", "path",
   "endpoint"]`, keep the first candidate whose key matches, in that order.
3. If step 2 finds no match either (no candidate key is in the priority list,
   or more than one *equally-ranked* candidate exists, e.g. two unknown keys):
   keep the first by source order **and** append an `UnresolvedRef{Kind:
   "wrapper_key_ambiguous"}` — surfaced per the Trust contract instead of
   silently guessed. Never drop the node — recall is preserved either way.

`applyWrapperRes`-style rewrite (new `applyWrapperURLRes`) strips the losing
candidates from `result` and cleans `obj_key` off the survivor before it's
appended, mirroring how `applyAliasRes` already cleans `via_alias`.

**Pinned interfaces:**

```go
func spanKey(n graph.Node) string                 // service+file+line+col
func resolveObjCallGroup(group []graph.Node, wrapperURLTable map[string]wrapperURLTarget) (graph.Node, *graph.UnresolvedRef)
```

**Tests**, `internal/contract/alias_test.go`: three new cases — (a) wrapper
known with `ParamKey: "uri"`, two candidates `uri`/`method` in the group, `uri`
wins, no ledger entry; (b) wrapper unknown, candidates `path`/`foo`, `path`
wins via priority-list fallback, no ledger entry; (c) wrapper unknown,
candidates `foo`/`bar` (neither in the priority list), first-by-source-order
wins **and** a `wrapper_key_ambiguous` ledger entry is emitted.

**Acceptance:** `patterns/javascript/producer_alias_test/expected.json`
regenerated; chessleap golden diff (`internal/contract/testdata/golden/
chessleap_contract_edges.json`) is additive-only (new correctly-keyed edges,
zero edges lost — the WB.2 diff already merged into this branch's baseline adds
one `producer_alias_obj_call` edge with a template-literal `url` key; WB.3
must not regress it).

## Phase WB.4 (stretch, separate commit, corpus-gated)

**Problem:** `producer_alias_url_call` always takes positional arg 0; a
wrapper like `apiFetch(options, uri)` mis-tags `options`.

**Deliverable:** capture all positional args generically (index via sibling
order, same trick as WB.1a), apply `wrapperURLTable`'s `ParamIndex` the same
way WB.3 applies `ParamKey`, same ambiguous-fallback/ledger behavior
(`wrapper_index_ambiguous`, fallback = index 0).

**Do not build this speculatively.** Positional-arg-0 wrappers are the
overwhelming common case (`fetch(url)`-shaped wrappers rarely take the URL as
anything but the sole or first argument). Only take this phase if a real
fleet/chessleap example is found where index-0 is measurably wrong — check
during WB.3's chessleap verification pass before scheduling this.

## Non-goals

- **Cross-file wrapper resolution.** `wrapperURLTable` is same-file, matching
  `aliasTable`/`wrapperTable`'s existing scoping. A cross-file version needs a
  whole-program JS symbol table this codebase doesn't have (see Go's `go/ssa`
  contrast above) — a real follow-up tier, not this one.
- **Multi-hop wrapper chains** (a wrapper calling another wrapper calling
  `fetch`). Go's Tier CH (`docs/go-http-host-hop-limit-plan.md`) solved the
  analogous problem with a bounded fixed point (`maxHostHops`). If corpus
  evidence justifies it, that's Tier WB.5 — out of scope here.
- **Runtime/dataflow proof that the wrapper is genuinely called correctly.**
  This stays syntactic inference, same as everything else in the JS parser.
  Per the Trust contract, ambiguity is ledgered, never guessed silently — it
  does not need to be *proven* right.
- **Spread props** (`apiFetch({ ...opts, url })`) **and computed keys**
  (`{[k]: url}`). No candidate key is captured for either — falls through to
  today's behavior (no match for spread-only objects; a computed key was never
  matched before this plan either). Not a regression.

## Verification

- `patterns/javascript/producer_alias_test/`,
  `patterns/javascript/producer_wrapper_body_test/`, `internal/contract/
  alias_test.go` — all green.
- Chessleap sidecar (`internal/contract/testdata/golden/
  chessleap_contract_edges.json`) diff reviewed by hand: additive only.
- `make bench` (`BenchmarkIndexCold`) — hold, per Ground rules; this is
  pattern + same-file linker work, no reason to regress index time.
- `graph.SchemaVersion` bump (currently `"32"`, `internal/graph/
  model.go:272`) — new node meta fields (`wrapper_url_param_index`,
  `wrapper_url_param_key`, `obj_key` residue if any survives) and a new ledger
  kind change stored shape/semantics.

## Risks

- **Span-grouping collisions.** Grouping WB.2's candidates by line+column
  assumes each `call_expression` has a distinct column — true for
  human-written and typically-formatted generated JS, but minified single-line
  bundles could collide. Mitigate by grouping on the full node byte-range
  (start/end offset) if column proves insufficient in practice; call out in
  the commit if this substitution is needed.
- **Same-name wrapper collision.** Two same-named wrapper functions in one
  file (e.g. inside different closures/scopes) merge in `wrapperURLTable`,
  identical to the known pre-existing limitation in `aliasTable`/`wrapperTable`
  and analogous to Tier CH's cross-package merge. Treat identically: a
  disagreeing merge should resolve to the ambiguous-fallback path, never a
  silently wrong pick — worth an explicit test case in WB.3.
- **Priority-list drift.** The `["url", "uri", "path", "endpoint"]` fallback
  list is a judgment call, not derived from corpus data. If chessleap/fleet
  verification surfaces a common name this list misses, extend the list
  rather than re-deriving it from scratch.

## Sequencing

```
WB.1 (wrapper-body patterns)
   -> WB.2 (generalized call-site capture)
      -> WB.3 (linker resolution, tests, chessleap verification)
         -> WB.4 (optional, only if chessleap/fleet shows index-0 is wrong)
```

Each phase is one commit; tests green and this doc's status line updated to
`done (commit <sha>)` in the same commit, per `docs/phases.md`'s Ground rules.
