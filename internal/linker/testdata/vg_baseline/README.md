# Tier VG differential baselines (VG.0)

Frozen output of the four JS URL-resolution passes — `js_local_url`,
`js_prop_urls`, `js_prop_transport`, `js_prop_client` — for one corpus. Every
later phase of `docs/js-value-graph-pilot-plan.md` is verified by diffing a
candidate capture against one of these files: the tier ships **no new edges**,
so a `LOST` or `CHANGED` row from `tools/vgdiff` is a regression.

## What a baseline records

For every `http_client` node these passes mint or mutate: node ID, service,
file, line, `Meta[path|method|url]`, and the `http_call` edges out of it. Plus
every `prop_client_dynamic_url` blind-spot ledger row, keyed as the linker's
retraction logic keys it (`linker.PropURLRetractKey`).

`SourceRef.Layer` / `.Rule` are **not** compared — VG.3 stamps them (SA.1), so
diffing them would flag every row on the run the tool exists to check.

## Capturing

The capture is a skipped Go test, not a CLI command. It reads an
already-cold graph DB — it does not index — so determinism is the caller's
responsibility:

```bash
make build
rm -rf "$POLYFLOW_CORPUS/.polyflow" && ./dist/polyflow index   # cold; stop any serve/mcp first

PF_VG_CAPTURE=internal/linker/testdata/vg_baseline/<corpus>.json \
POLYFLOW_CORPUS="$POLYFLOW_CORPUS" \
  go test ./internal/linker/ -run TestCaptureVGBaseline -count=1
```

The `corpus` field is taken from the output filename. Run it twice on the same
cold index; the files must be byte-identical (`Baseline.Marshal` sorts every
slice). A non-deterministic capture invalidates the tier — see
`polyflow-js-duplicate-label-call-nondeterminism`.

## Committed vs local

- **Committed:** public eval corpora only (`gotify.json`, `lobsters.json`).
  `gotify` currently produces zero rows from these passes — a legitimate
  "still zero" tripwire.
- **Local only, never committed:** `cedar.json` (the real Rails+React
  monolith). It embeds the real repository name and file tree; the `.gitignore`
  here blocks it. Regenerate it before each VG measurement run.

## Diffing

```bash
go run ./tools/vgdiff <baseline>.json <candidate>.json   # non-zero exit on any diff
```
