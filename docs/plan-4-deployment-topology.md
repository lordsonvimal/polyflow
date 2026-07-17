# Polyflow — Plan 4: Deployment & Execution Topology (Tier K)

Status legend: `pending` · `in progress` · `done`

> **Prerequisites.** K.0 and K.1 need only current infrastructure. **K.2 and
> K.3 need F.0** (the evidence substrate: `EvidenceProvider`,
> `Edge.Sources[]`) and complement **F.3** (config resolution) — they emit
> under the `config` provider name, one of the five pinned in F.0 (an
> unknown provider name is a registration error, so no sixth name is
> minted). This is plan **4 of 6** — execute after plans 1–3; interleave
> K.2/K.3 after F.0 lands per the cross-plan roadmap.
>
> Follows `docs/phases.md`; rules 1 (fan-out), 2 (determinism), 3
> (parsed-but-unenforced), and 6 (raw captured text) are the named risks
> throughout — deployment files are *all* strings.

## Context

**Why.** The flow between a repo and its deployment layer is invisible
today: a `deploy.sh` that curls an endpoint, a Dockerfile whose ENTRYPOINT
decides which `main()` runs in production, a compose file whose service
names *are* the cross-service URLs (`http://api:8080`), a k8s Ingress that
routes `/api` to a Deployment, a CronJob that is the only caller of a
binary, a CI job that is the only caller of `deploy.sh`. None of these
produce nodes, edges, or ledger entries (until plan 1's B.0 counts the
files). This plan makes the deployment layer a first-class part of the
graph — because "what is impacted if I change X" frequently ends at "this
CronJob stops working."

**Design rules pinned up front:**

1. **Two intake paths.** Shell scripts are per-service *source code* → a
   registered `Parser` (extension-dispatched, like every language).
   Dockerfiles / compose / k8s / CI files are *workspace-level topology* →
   scanned by a collector pass (they have no extension-stable identity and
   need cross-file joins), emitting through the `config` evidence provider
   (K.2/K.3) or the plain linker path (K.1, pre-F.0-compatible).
2. **One new node type, one new edge type** (SchemaVersion bump in K.1):

   ```go
   // internal/graph/model.go
   // NodeTypeDeployUnit is a thing that runs code: a container image,
   // a compose service, a k8s workload, a CI job. Meta["unit_kind"] =
   // "image" | "compose_service" | "k8s_deployment" | "k8s_cronjob"
   // | "ci_job" | ... — one node type, kinds in meta (the
   // publisher/subscriber precedent from G.4).
   NodeTypeDeployUnit NodeType = "deploy_unit"

   // EdgeTypeRuns: deploy_unit -> the code it executes (a service node,
   // an entrypoint function, a script file node). Direction follows
   // uses_type convention: from the dependent (the deploy unit) to the
   // definition (the code). Impact traversal is bidirectional, so
   // "impact of main()" follows incoming runs edges to every deploy unit.
   EdgeTypeRuns EdgeType = "runs"
   ```

3. **Everything unresolvable is ledgered** with a `deploy_`-prefixed kind
   (`deploy_image_unresolved`, `deploy_entrypoint_unresolved`,
   `helm_template_unresolved`, `ci_script_unresolved`). Never guessed.
4. **YAML parsing uses `gopkg.in/yaml.v3`** (already the repo's YAML dep —
   verified in go.mod; do not add a second YAML library). Terraform HCL in
   plan 5 uses the `hcl` tree-sitter grammar already in the module.

---

## Phases (one commit each)

### Phase K.0 — Shell-script parser `pending`

**Problem.** `.sh` files are complete blind spots. Real repos wire deploys,
migrations, seeding, and smoke tests through them.

**Deliverable.** `internal/parser/bash.go` registering `.sh`/`.bash`
(language `"bash"`, `smacker/go-tree-sitter/bash` — grammar verified in the
pinned module) + `patterns/bash/commands.yaml`:

1. **Scopes:** `foo() { … }` → function nodes; top-level commands attribute
   to the `(module)` scope (Phase 0.1's lesson, applied at birth — the
   fallback must exist for bash from day one).
2. **Command extraction** — a pinned command vocabulary (rule 7: every
   vocabulary entry gets a real-repo eval case eventually; start with):
   - `curl`/`wget` with a literal URL argument → `http_client` node
     (`method` from `-X`/`--request`, default GET; `url` meta through the
     standard quote-strip; **`$VAR`/`$(cmd)` in the URL ⇒ G.6 dynamic
     treatment**: register the **bash KeyWalker** — `${VAR:-"literal"}`
     default-value form resolves to the literal candidate; plain `$VAR` /
     command substitution ⇒ `key_dynamic` + `dynamic_url` ledger —
     checklist item 7 satisfied, doctor row `bash: yes`).
   - Invocation of another script by path (`./deploy.sh`, `bash x.sh`,
     `sh scripts/y.sh`, `source lib.sh`, `. lib.sh`) → `calls` edge from
     the enclosing scope to the target script's `NodeTypeFile` node
     (path-resolved relative to the script's dir; missing target →
     ledger `import_unresolved`, the I.3 Ruby precedent).
   - Calling a function defined in the same file or a `source`d file →
     Pass-3-style call refs (same-file first; sourced-file resolution is
     one hop, `inferred`; deeper → unresolved ref, honest).
   - Cloud/deploy CLI calls → `external_service` nodes with an operation
     label: `aws <svc> <op>`, `gcloud`, `az`, `kubectl apply|rollout|…`,
     `docker build|push|run`, `helm upgrade|install`, `terraform
     apply|plan`, `psql`/`mysql` → `NodeTypeDatastore`. One node per
     (service, cli, subcommand), edge `calls` from the enclosing scope.
     The vocabulary is YAML data in the pattern file, not Go code
     (additive-by-config).
   - `export FOO=…` / `FOO=… cmd` → variable nodes (feeding F.3's env
     resolution later; meta `env=true`).
3. **Extensionless executables** (`bin/deploy` with a `#!/bin/bash`
   shebang) are **not** parsed in this phase — they stay in B.0's
   unparsed ledger under their basename (written descope: shebang
   sniffing needs a content peek in the walk hot path; revisit if eval
   evidence demands it).

**Worked example** (fixture `testdata/bash/deploy/deploy.sh`):

```bash
#!/usr/bin/env bash
source ./lib.sh                      # → calls edge to lib.sh file node
migrate() { psql "$DATABASE_URL" -f schema.sql; }   # datastore node
migrate                              # → (module) -calls-> migrate
curl -X POST "http://api:8080/internal/reload"      # → http_client POST,
                                     #   path /internal/reload (links via
                                     #   the http rule once K.1's compose
                                     #   hint maps api → service)
aws s3 cp build/ "s3://$BUCKET/" --recursive         # external_service +
                                     #   dynamic key ledgered
```

**Tests.** The fixture above through the real parse path (rule 6); walker
cases (default-value form → candidate; `$VAR` → dynamic ledger); sourced
function resolution; missing-source negative; two-run determinism; `.sh`
counts drop from `unparsed_files` (assert).

**Acceptance.** `impact --file deploy.sh` on the fixture returns lib.sh and
the http route (once linked); `status --unresolved` shows the dynamic-url
entries. A shell-heavy case is added to one existing corpus repo.

### Phase K.1 — Dockerfile + docker-compose topology `pending`

**Problem.** Which code a container actually runs, and the compose network
names that *are* the inter-service URLs, live nowhere in the graph. The
compose half also unlocks recall everywhere else: `http://api:8080/x`
producers currently need hand-written workspace hints to find service
`api`.

**Deliverable.** `internal/deploy/docker.go` — a workspace-scan collector
(basename match: `Dockerfile`, `Dockerfile.*`, `*.dockerfile`,
`compose.y{a}ml`, `docker-compose*.y{a}ml`), wired in the indexer after
parsing, before hints/engine:

1. **Dockerfile** (tree-sitter `dockerfile` grammar — in the pinned
   module): emit a `deploy_unit` (`unit_kind=image`, label = the
   Dockerfile's dir or `--tag` when derivable → else the path).
   `ENTRYPOINT`/`CMD` (exec *and* shell form) resolve to code:
   - `["/app/server"]` / `go run ./cmd/x` → the service's matching
     `cmd/<x>` main → `runs` edge to that `main` function node.
   - `["npm", "start"]` / `node dist/main.js` → the service's
     package.json `scripts.start` (parse the script line with K.0's
     command extraction — reuse, don't duplicate) → `runs` edge to the
     resolved entry file node.
   - Shell-form strings are raw text (rule 6) — run them through K.0's
     bash command extraction, not ad-hoc splitting.
   - Unresolvable → `deploy_entrypoint_unresolved` ledger.
   - `COPY`/`WORKDIR` establish which workspace service the image wraps
     (build-context dir containment → `runs` edge image → service node);
     ambiguous/multi-service contexts → edge to **each** candidate
     service (fan-out, rule 1) + ledger note.
2. **Compose:** each service block → `deploy_unit`
   (`unit_kind=compose_service`, label = the compose service name):
   - `build.context` → join to the Dockerfile unit (`runs` edge chain
     compose_service → image → service); `image:` without build →
     external image, `Meta["external"]="true"`, no runs edge.
   - `depends_on` → `runs`-adjacent ordering edges? **No** — pinned:
     `depends_on` emits `calls`-nothing; it becomes `Meta["depends_on"]`
     on the unit (startup order is not a code flow; recording it as an
     edge would fake flow semantics — written decision).
   - **The recall payoff:** compose service names become hint links —
     for each compose service mapped to a workspace service, synthesize
     `workspace.Link{base_url: "http://<name>:<port>", target_service:
     <svc>}` entries feeding the **existing** `ApplyHints` (zero engine
     changes; the hint mechanism is G-plan machinery). Ports from
     `ports:`/`expose:`. Now `curl http://api:8080/x` in K.0's fixture
     and `fetch("http://api:8080/x")` anywhere link without manual
     workspace config.
   - `environment:`/`env_file:` values are recorded for F.3's config
     provider to consume (write them into the collector's output;
     F.3 resolves — no duplication of F.3's logic here).
3. Emission order sorted by file path then block order (rule 2);
   `Dockerfile`/compose basenames leave B.0's report (allowlist comment
   updated per B.0's note).

**Tests.** Dockerfile exec+shell entrypoint fixtures (Go + node);
multi-service context fan-out; compose 2-service fixture asserting the
synthesized hints make a previously-unresolved `http_call` link
(the acceptance chain below); external-image negative (no runs edge);
determinism; SchemaVersion bump test.

**Acceptance.** End-to-end fixture: `web` compose service's
`fetch("http://api:8080/games")` → cross-service `http_call` to `api`'s
handler **with zero workspace hint config**; `impact --target main`
(api's main) includes the image and compose deploy_units via incoming
`runs` edges.

### Phase K.2 — Kubernetes topology (manifests → routes → workloads) `pending`

*Needs F.0 (emits through the `config` evidence provider).*

**Problem.** Ingress routing, Service selectors, and CronJob triggers are
deployment-defined flows with no code call site.

**Deliverable.** `internal/evidence/config_resolve/k8s_topology.go` — a
collector under the **`config` provider** (F.3's provider; this file sits
beside F.3's `k8s.go` env-value resolution and shares its manifest
discovery). Scans workspace YAML docs whose top level has `apiVersion` +
`kind` (multi-doc files split on `---`):

1. **Workloads** (`Deployment`/`StatefulSet`/`DaemonSet`/`Job`/`CronJob`)
   → `deploy_unit` (`unit_kind=k8s_<kind lowercased>`). Container `image:`
   joins to K.1 image units by name-tail match (registry prefix and tag
   stripped — rule 6: strip before keying; ambiguous → fan-out to all
   candidates + ledger); `command:`/`args:` override ENTRYPOINT → re-run
   K.1's entrypoint resolution with them.
2. **Service** → synthetic endpoint node keyed
   `(namespace, service name, port)`; `selector` labels match workload
   template labels → edge Service → **every** matching workload
   (multi-valued, rule 1 — two Deployments sharing labels both link).
3. **Ingress** (+ `HTTPRoute` for Gateway API): each rule/path → an
   `http_client`-shaped **declared producer**: `method: ""` (ALL), `path`
   from the rule, `target_service_meta` resolved through the k8s Service →
   workload → polyflow service chain. These flow through the **existing
   http contract rule** as `source=config`, `confidence=declared` edges to
   the in-repo route handlers — an Ingress path with no matching handler
   becomes `observed_only_gap`-style surfaced evidence (the F.1 semantics,
   reused: declared-but-static-missed is a finding, not noise).
   `pathType: Prefix` maps to a trailing wildcard (`/api` → `/api/*` via
   the existing `param_wildcard`/anchor machinery — verify with a fixture,
   don't hand-wildcard; rule from R.1: keys go through the contract
   normalizer registry only).
4. **CronJob** → `runs` edge to the entrypoint (via image join + command
   resolution) with `Meta["schedule"]` = the cron expression; the
   entrypoint function's `root_kind` stays `entrypoint` and gains
   `Meta["triggered_by"]="k8s_cronjob"`.
5. **Env/ConfigMap/Secret refs** → handed to F.3 (its `config_not_found` /
   fan-out-per-overlay rules apply; kustomize overlays = one value
   candidate per overlay, rule 1). **Helm:** files containing `{{` template
   actions are **not parsed** — each gets one `helm_template_unresolved`
   ledger entry naming the file, and the doc gains a documented escape
   hatch: run `helm template` yourself and point the workspace at the
   rendered output. Never guess through a template.

**Tests.** Fixture manifest set (Deployment+Service+Ingress+CronJob,
multi-doc file) → the full chain asserted; selector multi-match fan-out;
prefix-path wildcard join to a static handler (through the real engine,
rule 6); helm ledger negative; two-run determinism (rule 2); provider
no-op when no manifests exist (graph byte-identical — the F-plan
degradation guard).

**Acceptance.** On the fixture: `impact --target <handler>` includes the
Ingress producer and the Deployment unit; `trace` closes
`ingress /api/* → handler`; the CronJob's target `main` shows the `runs`
in-edge with the schedule.

### Phase K.3 — CI/CD pipeline flows `pending`

*Needs K.0 (step scripts) and F.0 (provider seam).*

**Problem.** CI is often the only caller of deploy scripts and the only
place deploy-time URLs appear; today it is invisible.

**Deliverable.** `internal/deploy/ci.go` collector (config provider):
`.github/workflows/*.y{a}ml` and `.gitlab-ci.yml`:

1. Each workflow job → `deploy_unit` (`unit_kind=ci_job`, label
   `<workflow>/<job>`; service = a pinned pseudo-service `"ci"` — it is
   real code execution but belongs to no app service; workspace service
   resolution must not break on it, test-pinned).
2. `run:` step bodies are bash — feed them **through K.0's extraction**
   (curl → http_client under service `ci`, script invocations → `calls`
   edges to the repo's script file nodes: *this closes "who calls
   deploy.sh"*). Multi-line `|` blocks concatenate with line mapping for
   provenance refs.
3. `uses:` actions → `external_service` node per action (`actions/checkout`
   etc.); version tag kept in meta. A pinned small vocabulary of
   deploy-shaped actions (`*/deploy*`, `docker/build-push-action`,
   `azure/webapps-deploy`, …) additionally gets `Meta["deploys"]="true"`
   so doctor can list deployment touchpoints.
4. GitLab: `script:`/`before_script:` arrays → same K.0 treatment;
   `include:` files followed one hop (deeper / remote includes → ledger
   `ci_include_unresolved`).
5. Matrix/expression interpolation (`${{ matrix.x }}`, `$CI_COMMIT_SHA`)
   in URLs/paths ⇒ dynamic (G.6 ledger kinds) — never expanded.

**Tests.** Workflow fixture (run step curling a repo route + invoking
`./deploy.sh`) → both edges asserted through the real chain; gitlab
fixture; expression-dynamic negative; include-hop test + ledger negative;
determinism.

**Acceptance.** `impact --file scripts/deploy.sh` on the fixture lists the
CI job as a caller; the curl from CI links to the in-repo route handler.

---

## Key files

- **New:** `internal/parser/bash.go`, `patterns/bash/commands.yaml` (+
  fixtures), `internal/deploy/{docker.go,ci.go}`,
  `internal/evidence/config_resolve/k8s_topology.go`,
  `internal/contract/keywalk_bash.go`, `testdata/deploy/` fixtures.
- **Modify:** `internal/graph/model.go` (`deploy_unit`, `runs`;
  SchemaVersion), `internal/indexer/indexer.go` (collector wiring),
  `internal/indexer/unparsed.go` (allowlist updates as classes gain
  readers), `internal/workspace/` (synthesized hint links), doctor
  command file (deploy touchpoints row).

## Risks / honest notes

- **Deployment files lie less than code but drift more.** A compose file
  for local dev may not match production. Everything emitted here is
  `confidence=declared` at best — the fusion ladder, not fake certainty;
  C.2's staleness labeling applies to config sources when it lands.
- **Helm/kustomize expressiveness** is bounded deliberately (ledger +
  escape hatch); Terraform-defined k8s is plan 5's territory (HCL).
- **The `ci` pseudo-service** must never match a `where`/same-service
  contract policy accidentally — same_service policies compare service
  strings, and `ci` ≠ any app service, but a test pins it.

## Sequencing

```
K.0 ─> K.1 ─────────────> K.2 ─> K.3
        (K.1 needs K.0's command extraction; K.2/K.3 wait for F.0;
         K.3 also needs K.0)
```
