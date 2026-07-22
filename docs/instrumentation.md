# polyflow instrumentation recipes

Polyflow's evidence fusion uses OpenTelemetry traces to confirm static edges
and discover runtime flows the static graph missed.  Point any OTLP-capable
instrumentation at polyflow's built-in receiver:

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
OTEL_TRACES_EXPORTER=otlp
OTEL_SERVICE_NAME=<name matching your workspace.yaml service>
```

Then start a capture session before exercising your service:

```sh
polyflow capture start my-session
# ... run or exercise the service ...
polyflow capture stop my-session
polyflow index   # re-index to fuse captured evidence
```

## Per-stack recipes

| Stack | Recipe |
|-------|--------|
| Go | OTel Go SDK + `otelhttp` (net/http) / `otelgin` (gin) middleware; exporter reads env vars automatically |
| Node | `@opentelemetry/auto-instrumentations-node` via `--require`; zero code changes |
| Browser JS | `@opentelemetry/sdk-trace-web` + fetch/XHR instrumentations; configure receiver CORS if on a different origin |
| Ruby/Rails | `opentelemetry-sdk` + `opentelemetry-instrumentation-all`; add initializer + set env vars |
| Java | OTel Java agent (`-javaagent:opentelemetry-javaagent.jar`); zero code changes |
| Python | `opentelemetry-instrument <cmd>`; zero code changes |
| Service mesh / eBPF | Envoy/Istio tracing config or Cilium/Hubble → OTLP export to the same endpoint; zero app changes (HTTP/gRPC flows only) |
| Existing OTel collector | Add an `otlp` exporter to the collector pointing at `http://localhost:4318`, or use the collector's file exporter + `polyflow ingest <file>` |

## Service name mapping

If your instrumentation uses service names that differ from the names in
`workspace.yaml`, add a mapping:

```yaml
# workspace.yaml
evidence:
  runtime:
    service_names:
      my-api-service: api   # OTel name → polyflow service name
```

## Verification states after fusion

After `polyflow index` with captured evidence:

- `verified` — edge confirmed by both static analysis and runtime trace
- `observed_only_gap` — runtime saw this flow; static analysis missed it (real gap)
- `candidate` — static analysis only; one grep can confirm

Run `polyflow doctor` to see stale evidence warnings and re-capture suggestions.

---

*Promoted from the runtime-flow-plan appendix (R.5).  See also:
[docs/quickstart.md](quickstart.md)*
