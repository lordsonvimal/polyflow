# Polyflow — Plan 5: Cloud Messaging & Serverless Wiring (Tier Q)

Status legend: `pending` · `in progress` · `done`

> **Prerequisites.** Q.0 needs only the contract engine (G.0–G.7, `done`).
> **Q.1 needs R.4** (the async span mapper it extends). **Q.2 needs F.0**
> (it emits under the `config` evidence provider — one of the five pinned
> provider names; no sixth name is minted) and shares manifest discovery
> with F.3/K.2. Q.3 needs only Q.0's kinds. Python-side patterns in any
> phase are gated on L.P0 (Python grammar) — ship Go/JS/Ruby now, add the
> Python rows when L.P0 lands (each noted inline). This is plan **5 of 6**.
>
> Follows `docs/phases.md`; the nine bug-class rules are binding. Rule 1
> (fan-out) is the named risk everywhere here: cloud pub/sub is fan-out *by
> design* (one SNS topic → N SQS queues → N consumers).

## Context

**Why.** Service A never calls service B in a serverless-shaped system —
it publishes to SQS/SNS/EventBridge, and *infrastructure configuration*
(an event source mapping, a topic subscription, a bucket notification)
decides who runs. Three separate pieces are missing today, and all three
must exist before such a repo gets a usable blast radius:

1. **Code-side recognition** (Q.0): SDK publish/consume calls have no
   contract kinds — `sqs`, `sns`, `eventbridge`, `gcp_pubsub`,
   `azure_servicebus` are not in the engine's kind vocabulary.
2. **Runtime confirmation** (Q.1): the R.4 mapper only knows
   `amqp|kafka|nats|job` — spans with `messaging.system=aws_sqs` would
   land in the ingest ledger as unmapped.
3. **The infrastructure-declared hop** (Q.2/Q.3): the SNS→SQS
   subscription and the SQS→Lambda trigger exist *only* in IaC. No code
   pattern can ever see them; they are declared evidence (the F.1
   semantics: deterministic, `confidence=declared`).

**Key vocabulary decision (pinned).** Channel keys are **bare resource
names** (`orders-queue`, `order-events`), never URLs or ARNs. Producers and
IaC name the same resource three ways —
`https://sqs.us-east-1.amazonaws.com/123/orders-queue`,
`arn:aws:sqs:us-east-1:123:orders-queue`, `orders-queue` — and the join
fails silently unless all reduce to one form (the F.1 `{x}`-vs-`:x` lesson,
same class). One new registered normalizer handles it:

```go
// internal/contract/normalize.go — registered in init() like the others.
// arn_or_url_tail reduces an AWS/GCP/Azure resource identifier to its bare
// resource name: the last path segment of a URL, the last ":"-segment of
// an ARN, the last "/"-segment of a GCP resource path
// ("projects/p/topics/order-events" → "order-events"). Bare names pass
// through unchanged. Pure key transform (no node context) — a normalizer,
// not an enrichment pass.
RegisterNormalizer("arn_or_url_tail", arnOrURLTail)
```

Trust contract carried through: generated/tokenized resource names are
ledgered, never guessed; every IaC construct parsed but not mapped is
ledgered with a named reason (rule 3 — the F.1 `servers:`/webhooks
precedent).

---

## Phases (one commit each)

### Phase Q.0 — Cloud messaging kinds + SDK patterns `pending`

**Problem.** `sqsClient.SendMessage(...)` produces nothing today (only S3
and Bedrock have AWS patterns).

**Deliverable.**

1. **Kinds** (additive constants beside `KindHub`):
   `sqs`, `sns`, `eventbridge`, `gcp_pubsub`, `azure_servicebus`.
   Contract rules `contracts/{sqs,sns,eventbridge,gcp_pubsub,
   azure_servicebus}.yaml` — all five follow one shape (sqs shown; the
   others differ only in kind, meta key, and edge type):

   ```yaml
   # contracts/sqs.yaml
   version: "1"
   contracts:
     - kind: sqs
       producer:
         node: publisher
         where: { system: "sqs" }        # pattern-gate, the kafka/nats precedent
         key: [queue]
       consumer:
         node: subscriber
         where: { system: "sqs" }
         key: [queue]
       normalizers: [quote_strip, arn_or_url_tail, case_fold]
       match: [exact, normalized]         # no wildcard tier — resource names
                                          # are identifiers, not paths
       edge:
         type: sqs_publish               # new EdgeTypes: sqs_publish,
         id_prefix: link                 # sns_publish, eventbridge_publish,
         same_service: keep              # gcp_publish, servicebus_publish
       unmatched: ledger                 # queue with no in-repo consumer is
                                         # normal (Lambda/other repo) — the
                                         # Q.2 declared hop upgrades it;
                                         # unknown_edge would spam synthetics
   ```

   `same_service: keep` — async self-messaging is a real pattern.
   New edge types → `SchemaVersion` bump. Zero engine changes (assert —
   the G.4 additive property).
2. **Patterns** (each with positive + negative fixtures; every key
   argument rides the language's existing G.6 KeyWalker; config-held
   values become `key_dynamic` → the F.3/Q.2 upgrade targets):
   - **Go** (`patterns/go/{sqs,sns,eventbridge}.yaml`), aws-sdk-go-v2:
     `client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String("…")})`
     → publisher (`system=sqs`, `queue` from QueueUrl); `ReceiveMessage`
     loops → subscriber; `sns.PublishInput{TopicArn:}` → publisher
     (`system=sns`, `topic`); `eventbridge.PutEventsInput` entries'
     `EventBusName`/`DetailType` → publisher (`system=eventbridge`,
     `bus` + `detail_type` meta; key is `[bus]` — DetailType matching is
     Q.2's rule-level refinement, recorded, not guessed here).
     v1 SDK variants ship as separate version-gated pattern files
     (`package: github.com/aws/aws-sdk-go`, the aws_s3_v1/v2 precedent).
   - **JS/TS** (`patterns/javascript/aws_messaging.yaml`), SDK v3:
     `new SendMessageCommand({QueueUrl})` / `PublishCommand({TopicArn})` /
     `PutEventsCommand`; `Consumer.create({queueUrl})` (`sqs-consumer`,
     the dominant JS consume idiom) → subscriber.
   - **Ruby** (`patterns/ruby/aws_messaging.yaml`):
     `sqs.send_message(queue_url:…)`, `sns.publish(topic_arn:…)`,
     Shoryuken workers (`shoryuken_options queue:`) → subscriber.
   - **GCP/Azure**: Go + JS publish/subscribe idioms
     (`pubsubClient.Topic("x").Publish`, `subscription.Receive`;
     `ServiceBusSender`/`Processor`). One file per system per language.
   - **Python rows** (boto3 `send_message`, `publish`; gcp
     `publisher.publish`) — **gated on L.P0**; the rule files above
     already match them once the patterns exist (kind-level config is
     language-agnostic by construction).
3. **Consumers that are Lambda handlers** get no subscriber node from
   code (the trigger is in IaC) — that is Q.2's job; this phase's
   unmatched-→-ledger policy keeps the gap visible until then.

**Worked example** (fixture `testdata/contracts/sqs/`, two services):

```go
// svc-orders: producer
out, _ := client.SendMessage(ctx, &sqs.SendMessageInput{
    QueueUrl: aws.String("https://sqs.us-east-1.amazonaws.com/1/orders-queue"),
})
```
```go
// svc-worker: consumer
msgs, _ := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
    QueueUrl: aws.String("arn:aws:sqs:us-east-1:1:orders-queue"),
})
```

URL and ARN both normalize to `orders-queue` → one `sqs_publish` edge,
confidence `inferred` (normalized tier). That cross-representation join is
the phase's required test.

**Tests.** The worked example (URL vs ARN vs bare name — all three pairs);
`arn_or_url_tail` unit table (URL, ARN, GCP path, bare, empty); fan-out:
two consumers on one queue → 2 edges (rule 1); dynamic QueueUrl from a
variable → `dynamic_queue` ledger (G.6 path); per-pattern fixtures;
determinism.

**Acceptance.** 2-service fixtures link for all five kinds with only
YAML + one normalizer added; `polyflow doctor` coverage table shows the
new kinds.

### Phase Q.1 — Runtime mapper rows for cloud messaging `pending`

*Needs R.4 (extends its messaging table).*

**Problem.** OTel spans from cloud SDKs carry
`messaging.system=aws_sqs|aws_sns|gcp_pubsub|servicebus|eventgrid` — all
unmapped by R.4, so runtime evidence for exactly these flows lands in the
ingest ledger.

**Deliverable.** Extend the R.4 `messaging.system` → kind table (additive
rows, the semconv-drift convention):

| `messaging.system` | contract kind |
|---|---|
| `aws_sqs` | `sqs` |
| `aws_sns` | `sns` |
| `gcp_pubsub` | `gcp_pubsub` |
| `servicebus` | `azure_servicebus` |
| `eventgrid` / `eventhubs` | ledger `otlp_unsupported_messaging_system` (no kind yet — surfaced, rule 3) |

Destination key from `messaging.destination.name` through
`arn_or_url_tail` + the Q.0 rule's normalizer chain (rule from R.1: keys
go through the contract normalizer registry only — never hand-trim in the
mapper). Causality per R.4 unchanged: span links first, key-equality
fallback second — **and the fallback matters more here**: SNS→SQS delivery
crosses AWS-internal hops no SDK instruments, so a publish span and a
process span usually connect only by key. The existing
`causality=key_match` labeling covers it honestly.

**Tests.** Fixture OTLP traces per system (linked pair; key-match-only
pair; eventgrid → ledger negative) fed through the real R.0 parser
(rule 6/R.1 precedent — no hand-built spans); a static Q.0 fixture edge
flipping to `verified` from a matching trace; determinism.

**Acceptance.** The Q.0 sqs fixture + a captured trace fixture →
`sqs_publish` edge `verified` (channel granularity); doctor's session
coverage (R.5) shows the sqs channel observed.

### Phase Q.2 — IaC-declared event wiring (serverless.yml / SAM / CFN / Terraform) `pending`

*Needs F.0 (`config` provider) + Q.0 (kinds/keys). Shares manifest
discovery with F.3/K.2.*

**Problem.** The hops that exist only in infrastructure: topic→queue
subscriptions, queue→Lambda event source mappings, bucket→function
notifications, EventBridge rules, HTTP API routes→Lambda. Without them, a
serverless repo's graph is disconnected islands *by construction*.

**Deliverable.** `internal/evidence/config_resolve/iac_events.go` —
collectors under the `config` provider, one per format, all emitting the
same three record shapes:

- **resource declaration** → synthetic channel-endpoint node
  (queue/topic/bus, keyed by resolved physical name);
- **wiring edge** → `Sources[]={config}`, `confidence=declared`, edge type
  per the target kind (`sqs_publish` for topic→queue delivery,
  `job_perform`-shaped `runs`-adjacent trigger edges for X→function);
- **function binding** → `runs` edge (plan 4's edge type) from a
  `deploy_unit` (`unit_kind=lambda`) to the in-repo handler function.

Formats and their pinned mappings:

1. **serverless.yml**: `functions.<f>.handler` → handler resolution by
   runtime (pinned formats: Node `src/orders.process` →
   file `src/orders.{js,ts,mjs}` export `process`; Python
   `handlers.orders.process` → gated on L.P0; Go `bootstrap`/binary name →
   the service's `main`; unresolvable → ledger
   `iac_handler_unresolved`). `events:` entries: `sqs: arn:…` →
   queue→function trigger; `sns:` → topic→function; `s3:` →
   bucket-notification edge from an `external_service` S3 node (joining
   the existing aws_s3 pattern nodes by bucket name); `schedule:` →
   `Meta["schedule"]` on the runs edge (the K.2 CronJob convention);
   `httpApi/http:` → a declared `http_handler` node (`method`, `path`) —
   flowing through the existing http rule so in-repo `fetch("/orders")`
   calls link to the Lambda handler (zero engine changes).
2. **SAM / CloudFormation** (yaml or json — both; `.json` leaves B.0's
   asset allowlist for files with a `Resources:`/`"Resources"` top key
   only): `AWS::Serverless::Function` (`Handler`, `Events`),
   `AWS::Lambda::EventSourceMapping` (`EventSourceArn` → queue/stream,
   `FunctionName`), `AWS::SNS::Subscription`
   (`TopicArn`+`Endpoint`+`Protocol: sqs|lambda`),
   `AWS::S3::Bucket.NotificationConfiguration`,
   `AWS::Events::Rule` + `Targets`. **`Ref`/`GetAtt`/`Sub` resolution is
   one hop within the same template** (a `Ref` to a resource with an
   explicit physical name resolves; a `Ref` to a parameter or a
   generated name → ledger `iac_ref_unresolved` — never guessed;
   rule 6: `Sub` strings with non-literal variables ⇒ dynamic).
3. **Terraform** (`.tf`, parsed with the `hcl` tree-sitter grammar
   already in the pinned module — no new Go HCL dependency):
   `aws_lambda_event_source_mapping`, `aws_sns_topic_subscription`,
   `aws_s3_bucket_notification`, `aws_cloudwatch_event_rule`/`_target`,
   `aws_sqs_queue.name`, `aws_sns_topic.name`. Reference resolution:
   `aws_sqs_queue.orders.arn` → the queue resource's literal `name` in
   the same module — one hop, same rule as CFN; `var.`/`local.` with a
   literal default resolves to the default **as a candidate** (fan-out
   per tfvars variant is F.3's rule, reused); no default → ledger.
   Modules are not traversed (written descope: cross-module resolution
   is its own project; ledger `tf_module_unresolved` per call).
4. **The join**: declared queue/topic names go through the same
   `arn_or_url_tail` + Q.0 normalizer chain, so a code-side
   `key_dynamic` producer whose F.3-resolved value or Q.0 literal
   matches a declared resource links across the code↔infra boundary —
   and every static edge on that channel gains the `config` source
   (multi-valued join, rule 1; the F.0 reconciliation machinery does
   this — Q.2 only emits, never joins by hand).

**Worked example** (fixture `testdata/evidence/iac/serverless/`):

```yaml
# serverless.yml (svc-worker)
functions:
  processOrder:
    handler: src/orders.process
    events: [{sqs: arn:aws:sqs:us-east-1:1:orders-queue}]
```

plus svc-orders' `SendMessage` from Q.0's fixture. Expected: deploy_unit
`processOrder` -runs→ `process` in `src/orders.ts`; declared trigger edge
queue `orders-queue` → `process`; the Q.0 `sqs_publish` edge gains a
`config` source; `trace` closes
`svc-orders publisher → orders-queue → process` end to end.

**Tests.** The worked example; per-format fixtures (SAM, CFN json, tf)
asserting the same chain; Ref-to-parameter ledger negative; tf
var-default candidate + no-default ledger; unmapped-construct ledger
(rule 3: e.g. `AWS::Events::ApiDestination` → named reason);
determinism (rule 2 — synthetic node IDs derive from
`(provider, kind, key)`, the F.0 rule); provider no-op degradation test.

**Acceptance.** On the fixture workspace: `impact --target process`
includes svc-orders' publisher (cross-repo-boundary blast radius through
infrastructure); doctor's coverage shows the declared-edge counts per
kind.

### Phase Q.3 — CDK / Pulumi resource extraction `pending`

*Needs Q.0 (kinds). CDK/Pulumi are code (TS/Python/Go), so this is
patterns + one enrichment pass — not an IaC collector.*

**Problem.** `new sqs.Queue(this, "Orders", {queueName: "orders-queue"})`
*defines* the queue other services publish to, and
`queue.grantSendMessages(fn)` / `new SqsEventSource(queue)` wire it — all
invisible; worse, the constructor calls currently index as noise-level
`calls` edges with no cloud semantics.

**Deliverable.** `patterns/typescript/cdk.yaml` (+ Pulumi variants;
Python gated on L.P0):

1. **Resource constructs** with a **literal physical name**
   (`queueName`/`topicName`/`bucketName` props): emit the same
   channel-endpoint node shape Q.2 emits (declared resource,
   `source=config` — the enrichment pass hands them to the config
   provider's output so downstream treatment is identical). Constructs
   **without** a physical name (CDK generates one at deploy time) →
   ledger `cdk_generated_name` naming the construct id — never guessed;
   the construct node still exists (label = construct id,
   `Meta["name_generated"]="true"`) so intra-CDK wiring below still
   renders, but it joins to nothing outside the CDK app.
2. **Wiring calls** (an enrichment pass joining by the in-file
   variable bindings, the G.7 alias-table shape):
   `topic.addSubscription(new subs.SqsSubscription(queue))` →
   declared topic→queue edge; `fn.addEventSource(new SqsEventSource(q))`
   / `lambda.Function` `events` props → trigger edge; `new
   lambda.Function(..., {handler: "orders.process", code:
   Code.fromAsset("src")})` → `runs` edge to the in-repo handler
   (resolution rules shared with Q.2's serverless.yml table — one
   implementation, imported).
3. **Pulumi** (`new aws.sqs.Queue("orders", {name: "orders-queue"})`,
   `queue.onEvent`, `aws.lambda.EventSourceMapping`) — same shapes, one
   pattern file.

**Tests.** Named-queue → cross-boundary join fixture (CDK app + consumer
service, the Q.0/Q.2 chain closed from CDK-declared side);
generated-name ledger negative (+ intra-app wiring still present);
subscription/event-source fixtures; Pulumi fixture; determinism.

**Acceptance.** A repo whose infra is a CDK app and whose services
publish/consume by the declared names gets the same closed `trace` chain
as Q.2's serverless.yml fixture; `status --unresolved` lists every
generated-name construct.

---

## Key files

- **New:** `contracts/{sqs,sns,eventbridge,gcp_pubsub,azure_servicebus}.yaml`,
  `patterns/go/{sqs,sns,eventbridge}.yaml`,
  `patterns/javascript/{aws_messaging,cdk}.yaml`,
  `patterns/ruby/aws_messaging.yaml`,
  `internal/evidence/config_resolve/iac_events.go`,
  `testdata/contracts/<kind>/` + `testdata/evidence/iac/` fixtures.
- **Modify:** `internal/contract/normalize.go` (`arn_or_url_tail`),
  `internal/graph/model.go` (new edge types; SchemaVersion),
  `internal/evidence/trace_ingest/span_map.go` (Q.1 rows),
  `internal/indexer/unparsed.go` (json-with-Resources leaves the
  allowlist), doctor coverage.

## Risks / honest notes

- **Physical names are the join key, and teams increasingly don't set
  them** (CDK-generated names are a best practice). The
  `cdk_generated_name`/`iac_ref_unresolved` ledgers keep that limit
  visible; runtime evidence (Q.1) is the recovery path — spans carry the
  *actual* generated names, and an observed-only gap on a generated-name
  channel is exactly the F.4 self-improving-loop input.
- **Multi-repo reality:** the queue's consumer often lives in another
  repository. Unmatched-→-ledger (not unknown_edge) is chosen for these
  kinds precisely because a missing counterparty is the *normal* case;
  the workspace-level answer (indexing sibling repos into one graph) is
  out of scope and recorded here as considered.
- **EventBridge content filtering** (event patterns on detail-type/source)
  matches more finely than bus-level keys; bus-level linking
  over-approximates (recall over precision — correct direction), with
  `detail_type` meta preserved for a future rule refinement.

## Sequencing

```
Q.0 ─> Q.1 (after R.4)
   └─> Q.2 (after F.0) ─> Q.3
```
