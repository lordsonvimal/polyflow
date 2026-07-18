# Polyflow — Plan 3: Language Breadth — Java/Spring, C#/ASP.NET, PHP/Laravel (Tier L continued)

Status legend: `pending` · `in progress` · `done`

> **Prerequisites.** Current pattern/matcher/contract infrastructure (all
> `done`): the contract engine (G.0–G.5), dynamic-key walkers (G.6),
> alias/instance enrichment (G.7), Tier I edge types, and the eval harness
> (E.1–E.3). Python (L.P0–L.P4, `docs/goal-completion-plan.md`) should land
> **before** this plan starts — the Tier L rule "do not start a second
> language before the Python eval number exists" is binding; this plan then
> instantiates the same pinned 11-item checklist three more times. This is
> plan **3 of 6**.
>
> Follows `docs/phases.md` (process + the nine bug-class rules). The
> **pinned new-language checklist** in `docs/goal-completion-plan.md` Tier L
> is the master template; each language section below states, item by item,
> how it satisfies or descopes every checklist entry — a skipped item
> without a written claim fails review.

## Context

Go/JS/TS/Ruby (+Python after L.P) covers roughly half the repos that matter
worldwide. The three largest missing ecosystems, in order of repo population
and enterprise weight: **Java (Spring)**, **C# (ASP.NET Core)**,
**PHP (Laravel)**. All three grammars ship in the pinned
`smacker/go-tree-sitter` module (`java`, `csharp`, `php` — verified
2026-07-17 against `v0.0.0-20240827094217`); a `kotlin` grammar also ships
but Kotlin is **explicitly deferred** (written claim: one language at a
time, closed by an eval number — Kotlin/Spring shares Spring's patterns and
becomes cheap after L.J).

Per the Tier L rule, **each language closes on a measured eval number, not a
pattern count**: one hand-verified corpus repo with ≥15 cases per language,
baseline ratcheted in the closing phase's commit. **The corpus repo must be a
real public OSS repo meeting E.2's pinned selection criteria** — a
self-authored fixture proves the patterns match code written to match them,
not generality, and does not close a language (the L.P3 pyflask deviation,
recorded 2026-07-18; synthetic fixtures are welcome as fast smoke suites
*in addition to*, never instead of, the real repo). Each new language's
template/view parser (Blade, JSP, Razor) is additionally bound by bug-class
rule 11 (`docs/phases.md`): comment constructs blank entirely, with a
commented-out-instance fixture per recognized pattern.

String-literal hygiene per language (rule 6 — extend the matcher
quote-strip list in each language's first phase and test through real
fixture parses):

| Language | Literal forms to strip |
|---|---|
| Java | `"x"`, text blocks `"""x"""` |
| C# | `"x"`, verbatim `@"x"`, interpolated `$"x"` (interpolation ⇒ G.6 dynamic unless all-literal), raw `"""x"""` |
| PHP | `'x'`, `"x"` (interpolation in double quotes ⇒ dynamic), heredoc `<<<EOT`, nowdoc `<<<'EOT'` |

Test-code indexing from day one (rule 8): `src/test/**` (Java),
`*Tests.cs`/`*.Tests` projects (C#), `tests/`/`*Test.php` (PHP) are indexed
like any caller; excludes cover only fixture/data dirs and build output
(`target/`, `bin/`, `obj/`, `vendor/`).

---

## Tier L.J — Java + Spring

### Phase L.J0 — Java grammar + core patterns `pending`

*(Checklist items 1, 2, 10, 11.)*

**Deliverable.** `smacker/go-tree-sitter/java` wired into `parser.ForFile`
(`.java`); `patterns/java/functions.yaml`: class/interface/enum/record
declarations, method declarations, constructors, method-call refs
(`identifier` + `method_invocation` receiver chains — capture the bare
method name for Pass 3 exactly as the Go/Ruby files do), `import`
statements, field declarations. Enclosing-scope attribution: methods nest in
classes; static initializers and field initializers attribute to a synthetic
`(static-init)` scope per class (the `(module)` fallback lesson, Phase 0.1 —
package-level code does not exist in Java, but initializer blocks do).
Anonymous classes and lambdas register as enclosing scopes with `end_line`
spans (the worker-node lesson, Phase 0.2). Text blocks + quotes enter the
strip list; one fixture runs the full parser→matcher path (rule 6).

**Tests.** Pattern fixtures (positive + negative); attribution tests
(lambda body call, static initializer call, nested class); a test-file
fixture (`src/test/java/...`) proving test callers link (rule 8).

**Acceptance.** A small Java service indexes to class/method nodes with
call edges and an honest unresolved count.

### Phase L.J1 — Java dependency resolution `pending`

*(Checklist item 3.)*

**Deliverable.** `internal/deps`: Maven `pom.xml` (direct
`<dependencies>`; `${property}` version indirection resolved against
`<properties>` in the same pom — parent-pom chains are **descoped with a
ledger note** `maven_parent_unresolved`, never guessed) and Gradle **lock
files only** (`gradle.lockfile`, `*.versions.toml` version catalogs);
`build.gradle`/`.kts` script evaluation is descoped (written claim: it is a
Turing-complete build language; the lockfile is the truthful source when
present, and absence ⇒ versions unresolved ⇒ version-gated patterns stay
inactive — fail-safe per the V-plan's nearest-fallback is NOT applied to
ungated recognition patterns, which activate regardless).
`Dependency.Ecosystem: "maven"`.

**Tests.** One fixture per manifest form; property-indirection fixture;
parent-pom ledger negative; a version-gated dummy pattern
activates/deactivates on the lockfile version.

### Phase L.J2 — Spring HTTP + clients → contract engine `pending`

*(Checklist items 4, 7 — zero engine changes; the existing
`contracts/http.yaml` links everything.)*

**Deliverable.**

1. **Handlers** (`patterns/java/spring_web.yaml`):
   `@GetMapping("/x")` / `@PostMapping` / `@PutMapping` / `@DeleteMapping` /
   `@PatchMapping` / `@RequestMapping(value="/x", method=…)` on methods →
   `http_handler` nodes (method from the annotation name; `@RequestMapping`
   without a method ⇒ `method: ""` = ALL, engine method-fallback applies).
   **Class-level `@RequestMapping("/api")` prefixes are a meta-enrichment
   pass** — the G.3 route-group precedent verbatim: capture the class-level
   annotation as a binding node, join by (file, enclosing class), stamp the
   composed path pre-engine. Path variables `{id}` are converted to `:id`
   at extraction (one wildcard syntax repo-wide — the F.1 lesson).
2. **Clients:** `RestTemplate` (`getForObject/postForEntity/exchange` —
   method from the call or the `HttpMethod.X` arg), `WebClient`
   (`.get().uri("/x")` chains — the chained-receiver shape from
   `gin_route_chained` is the precedent), `@FeignClient(url/name)` interface
   methods with `@GetMapping` etc. → `http_client` nodes (Feign `name` →
   `target_service_meta` hint). All URL args ride the **Java KeyWalker**
   (new, checklist 7): ternaries, `String.format`/concat with all-literal
   parts → candidates; anything else → `dynamic_url`. Register via
   `RegisterKeyWalker`; the G.6 doctor walker row must show `java: yes`.
3. **Instance/alias idioms** (checklist 7, G.7): a
   `new RestTemplateBuilder().rootUri("/api")`-built field → instance
   binding with base prefix (reuse `EnrichAliases`' table; new YAML
   patterns only).

**Tests.** Fixture per pattern file; class-prefix enrichment through a real
`.java` fixture (rule 6 — the quoted-prefix incident is the named risk
here: annotation values arrive quoted); a Java-Spring + Go-gin 2-service
`testdata/` fixture asserting cross-service `http_call` edges; walker
matrix cases (ternary URL → 2 edges; computed → `dynamic_url`);
9-branch cap negative.

**Acceptance.** The 2-service fixture links with **only YAML + walker
added** — `internal/contract/engine.go` untouched (assert in the commit).

### Phase L.J3 — Spring DI, messaging, scheduling `pending`

*(Checklist items 4 (messaging), 9; plus the DI class this plan owns.)*

**Deliverable.**

1. **Tier I edges for Java** (checklist 9): `class A extends B` →
   `inherits` (`via=extends`), `implements I` → `implements`
   (`nominal=true`), `new Foo()` → `instantiates` — all in
   `patterns/java/functions.yaml` capture extensions + a
   `LinkJavaTypeRelations` linker pass (the I.2 `LinkJSTypeRelations`
   shape: per-service class-name table, collisions → candidate edges to
   each + ledger, fan-out rule 1).
2. **DI wiring (the recall-critical part):** constructor/field injection of
   an **interface** type → the `uses_type` edge to the interface exists via
   (1); the linker additionally emits `calls`-adjacent resolution:
   `impact` on an implementation must include the classes injecting its
   interface. Mechanism: no new edge type — the I-tier `implements` edge +
   bidirectional impact traversal already closes
   `injector → interface → implementations`; the deliverable is a fixture
   **proving** that chain and a written claim that bean-name/qualifier
   indirection (`@Qualifier`, XML config) is ledgered
   (`di_unresolved`) when a string, never guessed.
3. **Messaging:** `@KafkaListener(topics="x")` → subscriber,
   `kafkaTemplate.send("x", …)` → publisher (existing `kafka` kind);
   `@RabbitListener(queues=)` / `rabbitTemplate.convertAndSend(exchange,
   routingKey, …)` → existing `amqp` kind; `@JmsListener` → `job` kind
   (pragmatic mapping, noted in the rule file). Zero engine changes.
4. **Scheduling/entrypoints:** `@Scheduled` methods and
   `CommandLineRunner.run` → `root_kind=entrypoint` (Phase 0.4's
   classification input list extended — data, not code).

**Tests.** DI chain fixture (interface + 2 impls + injector →
`impact --target impl` includes injector); qualifier ledger negative;
kafka/amqp 2-service fixtures; scheduled-entrypoint classification test.

### Phase L.J4 — Java eval repo + templates claim `pending`

*(Checklist items 5, 6, 8.)*

**Deliverable.** One Spring OSS corpus repo (selection criteria: Spring
Boot, ≥2 services or front+back, ≥1 messaging kind; record rationale in the
manifest), ≥15 hand-verified cases incl. one test-file caller (rule 8) and
one DI-chain case. Checklist item 8 (server-side templates): if the chosen
repo uses Thymeleaf/JSP, a follow-up phase L.J5 is opened for the L.W
scenario classes (nav links `th:href="@{/x}"`, inline handlers, element
ids); if not, the written not-applicable claim goes here.

**Acceptance.** Java recall number committed to `eval/baseline.json` —
the number closes the tier.

---

## Tier L.N — C# / ASP.NET Core

### Phase L.N0 — C# grammar + core patterns + deps `pending`

*(Checklist items 1, 2, 3, 10, 11.)*

**Deliverable.** `smacker/go-tree-sitter/csharp` wired (`.cs`);
`patterns/csharp/functions.yaml`: class/interface/record/struct, methods,
constructors, invocation refs, `using` directives, properties. Top-level
statements (Program.cs without a Main) attribute to a synthetic `(module)`
scope (Phase 0.1 lesson — C# *does* have file-level code since C# 9).
Lambdas/local functions as enclosing scopes with spans (Phase 0.2).
Deps: `.csproj` `<PackageReference Include= Version=>` +
`packages.lock.json` when present; `Directory.Packages.props`
central-version resolution (one hop; deeper import chains → ledger
`msbuild_unresolved`). `Ecosystem: "nuget"`. Verbatim/interpolated/raw
strings into the strip list; interpolated with non-literal holes ⇒ G.6
dynamic. Test projects (`*.Tests`) indexed (rule 8).

**Tests.** Pattern fixtures; top-level-statement attribution; lockfile +
csproj fixtures; interpolated-string dynamic negative.

### Phase L.N1 — ASP.NET routes + HttpClient → contract engine `pending`

*(Checklist items 4, 7.)*

**Deliverable.**

1. **Attribute routing:** `[HttpGet("x")]` / `[HttpPost]` / … on controller
   methods + class-level `[Route("api/[controller]")]` prefix — the same
   G.3-style enrichment pass as L.J2, with the `[controller]` token
   substituted from the class name minus the `Controller` suffix
   (pinned rule; `[action]` likewise; any other bracket token → ledger
   `aspnet_token_unresolved`).
2. **Minimal APIs:** `app.MapGet("/x", handler)` / `MapPost/MapPut/
   MapDelete/MapPatch/Map` → `http_handler` + handler edge (the handler
   arg is a lambda or method group — method groups get B.1-style
   func-arg resolution; note the dependency: **plan 1 B.1 must be done**).
   `MapGroup("/api")` prefixes → the route-group enrichment (G.3 shape,
   again).
3. **Clients:** `httpClient.GetAsync("/x")` / `PostAsync` / …,
   `IHttpClientFactory` named clients with `BaseAddress` configured in
   `AddHttpClient("name", c => c.BaseAddress = new Uri("…"))` → instance
   idiom (G.7 `EnrichAliases` table, YAML only). C# KeyWalker (checklist
   7): ternary + all-literal interpolation/concat → candidates, else
   dynamic; registered, doctor row `csharp: yes`.
4. **Tier I for C#** (checklist 9): `class A : B, I` — base list entries
   resolve to `inherits` (first class) / `implements` (interfaces) via the
   per-service table (collision fan-out, rule 1); `new Foo()` →
   `instantiates`.

**Tests.** Enrichment through real `.cs` fixtures (rule 6 — attribute args
are quoted); `[controller]` substitution test; C#+Go 2-service fixture
cross-linking with YAML only; walker matrix cases; method-group handler
fixture.

**Acceptance.** 2-service fixture links; engine untouched.

### Phase L.N2 — .NET eval repo + messaging + templates claim `pending`

*(Checklist items 4 (messaging), 5, 6, 8.)*

**Deliverable.** MassTransit/`Azure.Messaging.ServiceBus` publish/consume
patterns → existing `amqp`/`job` kinds (plan 5 Q.0 later adds the
cloud-native kinds; these patterns emit kind-compatible meta so Q.0 picks
them up without rework — key fields named `queue`/`topic` exactly as the
existing kinds expect). One ASP.NET OSS corpus repo, ≥15 hand-verified
cases; Razor/Blazor templates: same conditional rule as L.J4 — if the repo
uses them, phase L.N3 opens for the L.W classes (`asp-action` tag helpers
→ route-helper map, the Rails-helper precedent); else the written claim.

**Acceptance.** C# recall number in `eval/baseline.json`.

---

## Tier L.H — PHP / Laravel

### Phase L.H0 — PHP grammar + core patterns + composer `pending`

*(Checklist items 1, 2, 3, 10, 11.)*

**Deliverable.** `smacker/go-tree-sitter/php` wired (`.php`);
`patterns/php/functions.yaml`: functions, classes/interfaces/traits,
methods, call refs (bare + `$this->x()` + `Foo::x()`), `use` imports,
top-level statements → `(module)` scope (PHP is file-executable — the JS
precedent applies directly). Deps: `composer.json` + `composer.lock` →
exact versions, `Ecosystem: "composer"`. Heredoc/nowdoc/quotes into the
strip list; double-quoted interpolation ⇒ dynamic. `tests/` indexed
(rule 8). Tier I (checklist 9): `extends` → `inherits`, `implements` →
`implements`, `use TraitX;` inside a class body → `inherits`
(`via=mixin, mixin=trait` — the Ruby mixin precedent), `new Foo()` →
`instantiates`; constant/class resolution via a per-service table
(collisions → fan-out + ledger).

**Tests.** Pattern fixtures; trait fixture; interpolation-dynamic negative;
composer fixtures.

### Phase L.H1 — Laravel routes + Blade + Guzzle `pending`

*(Checklist items 4, 7, 8 — PHP's template layer is load-bearing, so it is
in-plan, not conditional.)*

**Deliverable.**

1. **Routes** (`patterns/php/laravel_routes.yaml`):
   `Route::get('/x', [UserController::class, 'show'])` (method from the
   static call name; controller+method resolve via the class table →
   handler edge), closure handlers (edge to the closure scope),
   `Route::resource('photos', PhotoController::class)` → the seven RESTful
   entries (the Rails `resources` precedent — enumerate, don't
   special-case), `Route::prefix('admin')->group(…)` → G.3-style
   enrichment (chained receiver, the `gin_route_chained` shape). Route
   *names* (`->name('reports.index')`) build the **route-helper map** for
   Blade (below) — symbol/quote hygiene per rule 6, tested through a real
   `routes/web.php` fixture (the L.W0 warning applies verbatim).
2. **Blade templates:** `.blade.php` — the ERB splitter precedent
   (`{{ }}`, `{!! !!}`, `@directives` blanked in place; embedded PHP ranges
   through the php patterns; html patterns over the rest). Nav:
   `route('reports.index')` inside `href` → helper-map lookup →
   `http_client` nav_link node (fan-out on name collisions, rule 1);
   `action=` forms likewise; unresolvable helper → ledger
   (`laravel_helper_unresolved`).
3. **Clients:** Guzzle (`$client->get('/x')` with a `GuzzleHttp\Client`
   import gate; `new Client(['base_uri' => …])` → G.7 instance idiom) and
   Laravel `Http::get('/x')` facade. PHP KeyWalker: ternary + `.`-concat
   all-literal → candidates, else dynamic; doctor row `php: yes`.
4. **Jobs/queues:** `dispatch(new SendEmail(...))` / `SendEmail::dispatch()`
   → `job_enqueue` producer keyed on the job class name; `class SendEmail
   implements ShouldQueue` → consumer (existing `job` contract kind — the
   sidekiq precedent; zero engine changes).

**Tests.** Route fixtures (all four registration forms + prefix group);
Blade fixture through the real splitter (nav + inline `onclick=` + embedded
call); helper-collision fan-out test; Guzzle base_uri instance fixture;
job round-trip fixture.

### Phase L.H2 — PHP eval repo `pending`

*(Checklist items 5, 6.)*

**Deliverable.** One Laravel OSS corpus repo (criteria: Blade views + jobs
+ ≥1 JS frontend — the polyglot requirement), ≥15 hand-verified cases incl.
a Blade-nav case and a test-file caller.

**Acceptance.** PHP recall number in `eval/baseline.json`.

---

## Key files

- **New:** `patterns/{java,csharp,php}/*.yaml` (+ fixture dirs),
  `internal/parser/{java,csharp,php}.go` (thin grammar registrations),
  `internal/parser/blade.go` (splitter),
  `internal/linker/{java,csharp,php}_types.go` (Tier I passes),
  `internal/contract/keywalk_{java,csharp,php}.go` (walkers),
  `internal/deps/{maven,nuget,composer}.go`.
- **Modify:** `internal/deps/deps.go` (ecosystems),
  `internal/patterns/matcher.go` (quote-strip forms),
  `docs/goal-completion-plan.md` Tier L (link these phases from the
  checklist section).

## Risks / honest notes

- **Build-system truth:** Gradle scripts and MSBuild imports are programs;
  the lockfile-only stance trades version-gate coverage for honesty.
  Recorded per-phase; doctor's version-coverage row shows the holes.
- **DI/reflection ceilings:** Spring bean names, Laravel container strings,
  and .NET DI keyed services resolve only through their typed/interface
  forms; string-keyed indirection is ledgered. This is the known
  DI-heavy-framework limit — visible, not silent.
- **Order within the plan is strict:** Java first (largest population),
  then C#, then PHP; each closes on its eval number before the next starts
  (Tier L rule).

## Sequencing

```
(L.P Python done, plan 1 B.1 done)
L.J0 ─> L.J1 ─> L.J2 ─> L.J3 ─> L.J4     (Java closes on its number)
                                   └─> L.N0 ─> L.N1 ─> L.N2   (then C#)
                                                        └─> L.H0 ─> L.H1 ─> L.H2  (then PHP)
```
