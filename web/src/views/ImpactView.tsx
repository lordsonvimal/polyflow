// UF.6: Impact activity — the panel half of the impact/diff extra scope.
// The "rings" tab drives the canvas impact scope pushed by CanvasHost's
// "Impact from here" context-menu item (direction/depth controls only;
// the rings themselves render via scopes/impact.ts + CanvasHost's
// stylesheet). The "Diff" tab is self-contained: it calls the new
// GET /api/impact/diff and needs no active scope.
import { createSignal, createMemo, createResource, For, Show } from "solid-js";
import { scopeStore, Scope } from "../stores/scope";
import { apiFetch } from "../lib/apiFetch";
import { displayLabel } from "../lib/location";

type Direction = "up" | "down" | "both";
const DIRECTIONS: { id: Direction; label: string }[] = [
  { id: "up", label: "↑ Up (callers)" },
  { id: "down", label: "↓ Down (reaches)" },
  { id: "both", label: "↕ Both" },
];

interface DiffNodeRef {
  id: string;
  label: string;
  file: string;
  line: number;
}

interface DiffTarget {
  node: DiffNodeRef;
  changed_spans: { start: number; end: number }[];
}

interface UnmappedHunk {
  file: string;
  span?: { start: number; end: number };
  reason: string;
}

interface DiffCaller extends DiffNodeRef {
  depth: number;
  edge_type: string;
}

interface DiffResult {
  mode: string;
  depth: number;
  changed_files: number;
  targets: DiffTarget[];
  unmapped_hunks: UnmappedHunk[];
  callers: DiffCaller[];
  services_affected: string[];
  total_callers: number;
}

function ImpactRingsTab() {
  const impactScope = createMemo(() => {
    const top = scopeStore.stack().at(-1);
    return top?.kind === "impact" ? (top as Extract<Scope, { kind: "impact" }>) : null;
  });

  function update(patch: Partial<Extract<Scope, { kind: "impact" }>>) {
    const s = impactScope();
    if (!s) return;
    scopeStore.replaceTop({ ...s, ...patch });
  }

  return (
    <div class="p-3 text-xs text-neutral-300 space-y-3">
      <Show
        when={impactScope()}
        fallback={
          <div class="text-neutral-500">
            Right-click a node on canvas → "Impact from here" to see its blast radius here.
          </div>
        }
      >
        {(s) => (
          <>
            <div class="text-neutral-400 break-all" title={s().target}>
              Target: <span class="text-neutral-200">{s().target}</span>
            </div>
            <div>
              <div class="text-neutral-500 mb-1">Direction</div>
              <div class="flex gap-1">
                <For each={DIRECTIONS}>
                  {(d) => (
                    <button
                      data-testid={`impact-direction-${d.id}`}
                      class={`px-2 py-0.5 rounded ${s().direction === d.id ? "bg-indigo-600 text-white" : "bg-neutral-800 text-neutral-400 hover:text-white"}`}
                      onClick={() => update({ direction: d.id })}
                    >
                      {d.label}
                    </button>
                  )}
                </For>
              </div>
            </div>
            <div>
              <div class="text-neutral-500 mb-1">Depth: {s().depth}</div>
              <input
                data-testid="impact-depth"
                type="range"
                min="1"
                max="10"
                value={s().depth}
                onInput={(e) => update({ depth: Number(e.currentTarget.value) })}
                class="w-full"
              />
            </div>
          </>
        )}
      </Show>
    </div>
  );
}

function DiffTab() {
  const [staged, setStaged] = createSignal(false);
  const [depth, setDepth] = createSignal(0);

  const [result, { refetch }] = createResource(
    () => ({ staged: staged(), depth: depth() }),
    async ({ staged: st, depth: d }) => {
      const p = new URLSearchParams();
      if (st) p.set("staged", "true");
      if (d > 0) p.set("depth", String(d));
      const r = await apiFetch(`/api/impact/diff?${p}`, { silent: true });
      return (await r.json()) as DiffResult;
    },
  );

  return (
    <div class="p-3 text-xs text-neutral-300 space-y-3">
      <div class="flex items-center gap-3">
        <label class="flex items-center gap-1.5 cursor-pointer">
          <input
            data-testid="diff-staged-toggle"
            type="checkbox"
            checked={staged()}
            onChange={(e) => setStaged(e.currentTarget.checked)}
          />
          <span>Staged only</span>
        </label>
        <label class="flex items-center gap-1.5">
          <span class="text-neutral-500">Depth</span>
          <input
            data-testid="diff-depth"
            type="number"
            min="0"
            class="w-14 bg-neutral-800 rounded px-1"
            value={depth()}
            onChange={(e) => setDepth(Number(e.currentTarget.value) || 0)}
          />
        </label>
        <button
          data-testid="diff-refresh"
          class="ml-auto px-2 py-0.5 rounded bg-neutral-700 hover:bg-neutral-600 text-neutral-200"
          onClick={refetch}
        >
          Refresh
        </button>
      </div>

      <Show when={result.loading}>
        <div class="text-neutral-500">Computing diff impact…</div>
      </Show>

      <Show when={result.error}>
        <div class="text-red-400">
          Failed to load diff impact: {String((result.error as Error)?.message ?? result.error)}
        </div>
      </Show>

      <Show when={!result.loading && !result.error && result()}>
        {(r) => (
          <div class="space-y-3">
            <div class="text-neutral-500">
              {r().mode} · {r().changed_files} changed file{r().changed_files === 1 ? "" : "s"} ·{" "}
              {r().total_callers} in blast radius
              <Show when={r().services_affected.length > 0}>
                {" "}· services: {r().services_affected.join(", ")}
              </Show>
            </div>

            <div>
              <div class="text-neutral-500 mb-1">Changed nodes</div>
              <Show when={r().targets.length > 0} fallback={<div class="text-neutral-600">none mapped</div>}>
                <ul data-testid="diff-targets" class="space-y-1">
                  <For each={r().targets}>
                    {(t) => (
                      <li class="flex items-center gap-1.5">
                        <span
                          data-testid="diff-target-badge"
                          class="px-1 rounded bg-amber-700 text-amber-100 text-[10px] font-semibold leading-none"
                        >
                          M
                        </span>
                        <span class="text-neutral-200 truncate">{displayLabel(t.node.label)}</span>
                        <span class="text-neutral-600 truncate">
                          {t.node.file}:{t.node.line}
                        </span>
                      </li>
                    )}
                  </For>
                </ul>
              </Show>
            </div>

            <div>
              <div class="text-neutral-500 mb-1">Union blast radius</div>
              <Show when={r().callers.length > 0} fallback={<div class="text-neutral-600">no callers</div>}>
                <ul data-testid="diff-callers" class="space-y-1 max-h-40 overflow-y-auto">
                  <For each={r().callers}>
                    {(c) => (
                      <li class="flex items-center gap-1.5">
                        <span class="text-neutral-600 shrink-0">d{c.depth}</span>
                        <span class="text-neutral-200 truncate">{displayLabel(c.label)}</span>
                        <span class="text-neutral-600 truncate">
                          {c.file}:{c.line}
                        </span>
                      </li>
                    )}
                  </For>
                </ul>
              </Show>
            </div>

            {/* Never dropped — rule 12 (docs/phases.md): a changed span the
                graph has no node for (including no_git_repo services) is
                always listed, whether or not it's empty this run. */}
            <div>
              <div class="text-neutral-500 mb-1">Unmapped hunks</div>
              <Show when={r().unmapped_hunks.length > 0} fallback={<div class="text-neutral-600">none</div>}>
                <ul data-testid="diff-unmapped" class="space-y-1">
                  <For each={r().unmapped_hunks}>
                    {(u) => (
                      <li class="text-amber-400">
                        {u.file}
                        <Show when={u.span}>{(sp) => ` :${sp().start}-${sp().end}`}</Show> — {u.reason}
                      </li>
                    )}
                  </For>
                </ul>
              </Show>
            </div>
          </div>
        )}
      </Show>
    </div>
  );
}

export default function ImpactView() {
  const [tab, setTab] = createSignal<"rings" | "diff">("rings");

  return (
    <div data-testid="impact-view">
      <div class="flex items-center gap-2 px-3 pt-2 text-xs">
        <button
          data-testid="impact-tab-rings"
          class={tab() === "rings" ? "text-white" : "text-neutral-500 hover:text-white"}
          onClick={() => setTab("rings")}
        >
          Impact
        </button>
        <button
          data-testid="impact-tab-diff"
          class={tab() === "diff" ? "text-white" : "text-neutral-500 hover:text-white"}
          onClick={() => setTab("diff")}
        >
          Diff
        </button>
      </div>
      <Show when={tab() === "rings"}>
        <ImpactRingsTab />
      </Show>
      <Show when={tab() === "diff"}>
        <DiffTab />
      </Show>
    </div>
  );
}
