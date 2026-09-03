import { createSignal, createEffect, For, Show, onCleanup, onMount } from "solid-js";
import { paletteStore, type RecentItem } from "../../stores/palette";
import { commands as registeredCommands, type Command } from "../../commands/registry";
import { handleIntent } from "../../interaction/gestures";
import { scopeStore } from "../../stores/scope";
import { selectionStore } from "../../stores/selection";
import { treeStore } from "../../stores/tree";
import { parseQuery, parseNodeCard, toggleKindChip, KIND_FILTERS, type ParsedQuery } from "./query";
import { formatLocation, displayLabel } from "../../lib/location";
import { linkExplorerStore } from "../../stores/linkExplorer";

const DEBOUNCE_MS = 150;
const RESULT_LIMIT = 8;

type SymbolEntry = {
  id: string; label: string; type: string; service: string; file: string; line: number;
  endLine?: number;
  // "semantic" = the hit only matched by vector similarity, not a literal
  // token — surfaced as a confidence dot so a fuzzy match never looks as
  // certain as a lexical/exact one.
  retrieval?: string;
};
type FileEntry = { file: string; service: string };
type ServiceEntry = { name: string };
type Entry =
  | { group: "recent"; item: RecentItem }
  | { group: "symbol"; item: SymbolEntry }
  | { group: "file"; item: FileEntry }
  | { group: "service"; item: ServiceEntry }
  | { group: "command"; item: Command };

// note   — the server's vector-arm availability message (semantic).
// advisory — result-quality advisory: the server's "no strong match" note,
//   or a scope error (e.g. an unknown service: chip).
type SymbolResult = { entries: SymbolEntry[]; note: string; advisory: string };

async function fetchSymbols(parsed: ParsedQuery, fleetScope: boolean): Promise<SymbolResult> {
  // A bare "kind:x service:y" query (no free text — e.g. the Stack panel's
  // per-kind bar-chart click) is still a real symbol search; only a totally
  // empty query (nothing typed, no chips) has nothing to search for.
  if (!parsed.text && !parsed.chips.kind) return { entries: [], note: "", advisory: "" };
  const params = new URLSearchParams({ limit: String(RESULT_LIMIT) });
  if (parsed.text) params.set("q", parsed.text);
  if (parsed.chips.kind) params.set("kind", parsed.chips.kind);
  // Scope: an explicit `service:` chip wins; otherwise the Fleet toggle
  // sends "*" (federate the whole fleet). Default (no chip, toggle off) is
  // the current workspace only — the server treats an absent service param
  // as workspace-local (semantic.ScopedSearch).
  if (parsed.chips.service) params.set("service", parsed.chips.service);
  else if (fleetScope) params.set("service", "*");
  try {
    const r = await fetch(`/api/graph/search?${params}`);
    if (!r.ok) {
      // A 400 here is almost always an unknown `service:` chip — the server
      // now rejects it (and lists the valid members) instead of silently
      // returning workspace-local results.
      const err = await r.json().catch(() => null);
      return { entries: [], note: "", advisory: err?.error ?? "" };
    }
    const data = await r.json();
    const note: string = Array.isArray(data) ? "" : (data.semantic ?? "");
    const advisory: string = Array.isArray(data) ? "" : (data.note ?? "");
    let out: SymbolEntry[];
    if (Array.isArray(data)) {
      out = data.map((n: any) => ({
        id: n.id, label: n.label, type: n.type, service: n.service, file: n.file, line: n.line,
        endLine: n.end_line, retrieval: "lexical",
      }));
    } else {
      out = (data.nodes ?? []).map((hit: any) => {
        // The server attaches structured label/node_type/service (looked up
        // from the live index by entity.NodeID) precisely so this never has
        // to fall back to parsing entity.Text: that card is "label type
        // service file", and any node whose *label* itself contains a space
        // (every http_handler, e.g. "GET /api/jobs") shifts every field —
        // service ends up as "http_handler", which then 404s on whatever
        // endpoint gets called next. Only fall back for a stale/older server
        // response that lacks these fields.
        if (hit.label || hit.node_type || hit.service) {
          return {
            id: hit.entity?.ID ?? hit.entity?.NodeID ?? "",
            label: hit.label ?? "",
            type: hit.node_type ?? "",
            service: hit.service ?? "",
            file: hit.entity?.File ?? "",
            line: hit.entity?.Line ?? 0,
            retrieval: hit.retrieval,
          };
        }
        const card = parseNodeCard(hit.entity?.Text ?? "");
        return {
          id: hit.entity?.ID ?? hit.entity?.NodeID ?? "",
          label: card.label,
          type: card.type,
          service: card.service,
          file: hit.entity?.File ?? "",
          line: hit.entity?.Line ?? 0,
          retrieval: hit.retrieval,
        };
      });
    }
    if (parsed.chips.service) out = out.filter(s => s.service === parsed.chips.service);
    return { entries: out.slice(0, RESULT_LIMIT), note, advisory };
  } catch {
    return { entries: [], note: "", advisory: "" };
  }
}

async function fetchFiles(parsed: ParsedQuery): Promise<FileEntry[]> {
  const params = new URLSearchParams({ limit: String(RESULT_LIMIT) });
  if (parsed.text) params.set("q", parsed.text);
  try {
    const r = await fetch(`/api/files?${params}`);
    if (!r.ok) return [];
    const data = await r.json();
    let out: FileEntry[] = (data.files ?? []).map((f: any) => ({ file: f.file, service: f.service }));
    if (parsed.chips.service) out = out.filter(f => f.service === parsed.chips.service);
    return out.slice(0, RESULT_LIMIT);
  } catch {
    return [];
  }
}

// Services aren't a search-indexed entity — /api/stack is the whole list, so
// this filters the already-cached treeStore.services() (loaded once, on
// Palette mount, below) synchronously rather than issuing a network request
// on every keystroke. Empty until that initial load resolves.
function fetchServices(parsed: ParsedQuery): ServiceEntry[] {
  const needle = parsed.text.toLowerCase();
  let out = treeStore.services().map(s => ({ name: s.name }));
  if (needle) out = out.filter(s => s.name.toLowerCase().includes(needle));
  if (parsed.chips.service) out = out.filter(s => s.name === parsed.chips.service);
  return out.slice(0, RESULT_LIMIT);
}

function matchCommands(parsed: ParsedQuery): Command[] {
  const needle = parsed.text.toLowerCase();
  const all = registeredCommands();
  if (!needle) return all.slice(0, RESULT_LIMIT);
  return all.filter(c => c.label.toLowerCase().includes(needle)).slice(0, RESULT_LIMIT);
}

// A symbol result's Enter/pick behavior: land in a neighborhood scope
// centered on the picked node (depth 4), selected and revealed in the file
// tree, in one action. File scope (UN.2's original behavior) dumped every
// symbol declared in the file — including hundreds of unrelated ones for a
// large router/handlers file — and collapsed cross-service edges into a
// single boundary stub, hiding the actual connected node. Neighborhood
// scope shows only what's really connected, cross-service included, since
// /api/graph/trace does a real BFS with no service filtering.
//
// depth 4, not 3: /api/graph/trace has no truncation signal (no boundary
// stub for a node whose own neighbors fall just past the depth cutoff), so
// a root 3 hops from a hub node (e.g. onMouseDown -> addEventListener ->
// onUp) silently drops that hub's own children (onUp's removeEventListener
// calls) with nothing in the UI indicating they exist. One extra hop of
// headroom is a cheap mitigation; it doesn't fix arbitrarily deep chains,
// which need real frontier-stub support in the trace endpoint.
function openSymbol(id: string, file: string) {
  scopeStore.push({ kind: "neighborhood", nodeId: id, depth: 4 });
  selectionStore.setSelection({ kind: "node", id });
  if (file) treeStore.reveal(id);
}

// UF.8: "Explore links" — selects the symbol and leaves a one-shot request
// for DetailHost's LinkExplorer toggle, deliberately WITHOUT pushing a
// scope change (unlike openSymbol/pick), so the peek/browse stays
// non-committal until the user acts inside the panel itself.
function exploreLinks(id: string) {
  selectionStore.setSelection({ kind: "node", id });
  linkExplorerStore.request(id);
}

export default function Palette() {
  let inputEl: HTMLInputElement | undefined;
  const [query, setQuery] = createSignal("");
  const [symbols, setSymbols] = createSignal<SymbolEntry[]>([]);
  const [files, setFiles] = createSignal<FileEntry[]>([]);
  const [services, setServices] = createSignal<ServiceEntry[]>([]);
  const [cmds, setCmds] = createSignal<Command[]>([]);
  const [highlight, setHighlight] = createSignal(0);
  // Search scope: false = current workspace only (default), true = the whole
  // fleet. Mirrors the `service` param on /api/graph/search.
  const [fleetScope, setFleetScope] = createSignal(false);
  const [semanticNote, setSemanticNote] = createSignal("");
  const [advisory, setAdvisory] = createSignal("");
  const activeKind = () => parseQuery(query()).chips.kind ?? "";

  let debounceTimer: ReturnType<typeof setTimeout> | undefined;
  let seq = 0;
  onCleanup(() => clearTimeout(debounceTimer));

  // Loaded once and cached on the treeStore singleton (shared with the tree
  // explorer / FilterBar) — not re-fetched per keystroke.
  onMount(() => treeStore.loadServices());

  function runSearch(raw: string) {
    const parsed = parseQuery(raw);
    setCmds(matchCommands(parsed));
    setServices(parsed.chips.kind ? [] : fetchServices(parsed)); // a kind: chip means "symbols only"
    if (!parsed.text && !parsed.chips.kind && !parsed.chips.service) {
      setSymbols([]);
      setFiles([]);
      setSemanticNote("");
      setAdvisory("");
      return;
    }
    const mySeq = ++seq;
    Promise.all([fetchSymbols(parsed, fleetScope()), fetchFiles(parsed)]).then(([s, f]) => {
      if (mySeq !== seq) return; // a newer keystroke already superseded this request
      setSymbols(s.entries);
      setSemanticNote(s.note);
      setAdvisory(s.advisory);
      setFiles(f);
    });
  }

  function toggleFleetScope() {
    setFleetScope(v => !v);
    runSearch(query());
  }

  function onInput(v: string) {
    setQuery(v);
    setHighlight(0);
    clearTimeout(debounceTimer);
    debounceTimer = setTimeout(() => runSearch(v), DEBOUNCE_MS);
  }

  function entries(): Entry[] {
    if (query().trim() === "") {
      return paletteStore.recent().map(item => ({ group: "recent", item }) as Entry);
    }
    return [
      ...symbols().map(item => ({ group: "symbol", item }) as Entry),
      ...files().map(item => ({ group: "file", item }) as Entry),
      ...services().map(item => ({ group: "service", item }) as Entry),
      ...cmds().map(item => ({ group: "command", item }) as Entry),
    ];
  }

  function pick(entry: Entry) {
    switch (entry.group) {
      case "symbol": {
        const s = entry.item;
        openSymbol(s.id, s.file);
        paletteStore.addRecent({ id: s.id, kind: "symbol", label: s.label, sub: `${s.service} · ${s.file}` });
        break;
      }
      case "file": {
        const f = entry.item;
        scopeStore.push({ kind: "file", service: f.service, path: f.file });
        paletteStore.addRecent({ id: `${f.service}:${f.file}`, kind: "file", label: f.file, sub: f.service });
        break;
      }
      case "service": {
        const sv = entry.item;
        scopeStore.push({ kind: "service", service: sv.name });
        paletteStore.addRecent({ id: sv.name, kind: "service", label: sv.name });
        break;
      }
      case "command": {
        const c = entry.item;
        c.run();
        paletteStore.addRecent({ id: c.id, kind: "command", label: c.label });
        break;
      }
      case "recent": {
        const r = entry.item;
        if (r.kind === "symbol") {
          const [, file] = (r.sub ?? "").split(" · ");
          openSymbol(r.id, file ?? "");
        } else if (r.kind === "file") {
          const [service, ...rest] = r.id.split(":");
          scopeStore.push({ kind: "file", service, path: rest.join(":") });
        } else if (r.kind === "service") {
          scopeStore.push({ kind: "service", service: r.id });
        } else {
          registeredCommands().find(c => c.id === r.id)?.run();
        }
        paletteStore.addRecent(r);
        break;
      }
    }
    close();
  }

  function close() {
    paletteStore.close();
    setQuery("");
    setSymbols([]);
    setFiles([]);
    setServices([]);
    setSemanticNote("");
    setAdvisory("");
    setHighlight(0);
  }

  function onKeyDown(e: KeyboardEvent) {
    const list = entries();
    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      close();
      return;
    }
    if (e.key === "ArrowDown") {
      e.preventDefault();
      e.stopPropagation();
      setHighlight(h => (list.length === 0 ? 0 : (h + 1) % list.length));
      return;
    }
    if (e.key === "ArrowUp") {
      e.preventDefault();
      e.stopPropagation();
      setHighlight(h => (list.length === 0 ? 0 : (h - 1 + list.length) % list.length));
      return;
    }
    if (e.key === "Enter") {
      e.preventDefault();
      e.stopPropagation();
      const entry = list[highlight()];
      if (entry) pick(entry);
      return;
    }
  }

  createEffect(() => {
    if (paletteStore.isOpen()) {
      setHighlight(0);
      const pending = paletteStore.pendingQuery();
      if (pending !== undefined) {
        onInput(pending);
        paletteStore.clearPendingQuery();
      }
      queueMicrotask(() => inputEl?.focus());
    }
  });

  return (
    <Show when={paletteStore.isOpen()}>
      <div
        data-testid="palette-overlay"
        class="fixed inset-0 z-50 flex items-start justify-center pt-24 bg-black/50"
        onClick={() => close()}
      >
        <div
          class="w-full max-w-lg bg-neutral-900 border border-neutral-700 rounded-lg shadow-xl overflow-hidden"
          onClick={(e) => e.stopPropagation()}
        >
          <input
            ref={inputEl}
            data-testid="palette-input"
            class="w-full px-3 py-2 bg-transparent text-sm text-neutral-100 outline-none border-b border-neutral-800"
            placeholder="Search symbols, files, commands… (kind:route service:name)"
            value={query()}
            onInput={(e) => onInput(e.currentTarget.value)}
            onKeyDown={onKeyDown}
          />
          <div class="flex items-center gap-1 px-3 py-1.5 border-b border-neutral-800 text-xs">
            <span class="text-neutral-500 mr-1">Scope</span>
            <button
              data-testid="palette-scope-workspace"
              class={`px-2 py-0.5 rounded ${!fleetScope() ? "bg-neutral-700 text-neutral-100" : "text-neutral-400 hover:text-neutral-200"}`}
              onClick={() => { if (fleetScope()) toggleFleetScope(); }}
            >
              This workspace
            </button>
            <button
              data-testid="palette-scope-fleet"
              class={`px-2 py-0.5 rounded ${fleetScope() ? "bg-neutral-700 text-neutral-100" : "text-neutral-400 hover:text-neutral-200"}`}
              onClick={() => { if (!fleetScope()) toggleFleetScope(); }}
            >
              Entire fleet
            </button>
          </div>
          <div class="flex items-center gap-1 px-3 py-1.5 border-b border-neutral-800 text-xs flex-wrap">
            <span class="text-neutral-500 mr-1">Only</span>
            <For each={KIND_FILTERS}>
              {(kf) => (
                <button
                  data-testid={`palette-kind-${kf.kind}`}
                  class={`px-2 py-0.5 rounded ${activeKind() === kf.kind ? "bg-neutral-700 text-neutral-100" : "text-neutral-400 hover:text-neutral-200"}`}
                  onClick={() => onInput(toggleKindChip(query(), kf.kind))}
                >
                  {kf.label}
                </button>
              )}
            </For>
          </div>
          <Show when={semanticNote()}>
            <div
              data-testid="palette-semantic-note"
              class="px-3 py-1 border-b border-neutral-800 text-[11px] text-amber-500/90 truncate"
              title={semanticNote()}
            >
              {semanticNote()}
            </div>
          </Show>
          <Show when={advisory()}>
            <div
              data-testid="palette-advisory"
              class="px-3 py-1 border-b border-neutral-800 text-[11px] text-sky-400/90"
              title={advisory()}
            >
              {advisory()}
            </div>
          </Show>
          <div class="max-h-96 overflow-y-auto overflow-x-hidden text-sm">
            <Show when={query().trim() === ""}>
              <Group label="RECENT">
                <For each={paletteStore.recent()}>
                  {(r, i) => (
                    <Row active={highlight() === i()} onClick={() => pick({ group: "recent", item: r })}>
                      <span class="text-neutral-200 shrink-0">{displayLabel(r.label)}</span>
                      <Show when={r.sub}><span class="text-neutral-400 ml-2 text-xs truncate min-w-0">{r.sub}</span></Show>
                    </Row>
                  )}
                </For>
              </Group>
            </Show>
            <Show when={query().trim() !== ""}>
              <Group label="SYMBOLS">
                <For each={symbols()}>
                  {(s, i) => (
                    <Row active={highlight() === i()} onClick={() => pick({ group: "symbol", item: s })}>
                      <Show when={s.retrieval === "semantic"}>
                        <span
                          data-testid="confidence-dot"
                          title="inferred match (semantic similarity, not a literal match)"
                          class="w-1.5 h-1.5 rounded-full bg-amber-500 mr-1.5 shrink-0"
                        />
                      </Show>
                      <span class="text-neutral-200 shrink-0">{s.label}</span>
                      <span class="text-neutral-400 ml-2 text-xs truncate min-w-0">
                        {s.type} · {s.service} · {formatLocation(s.file, s.line, s.endLine)}
                      </span>
                      <button
                        data-testid="palette-explore-links"
                        class="ml-auto text-xs text-indigo-300 hover:text-indigo-200 opacity-0 group-hover:opacity-100 shrink-0 pl-2"
                        title="Explore links"
                        onClick={(e) => {
                          e.stopPropagation();
                          exploreLinks(s.id);
                          close();
                        }}
                      >
                        links
                      </button>
                    </Row>
                  )}
                </For>
              </Group>
              <Group label="FILES">
                <For each={files()}>
                  {(f, i) => (
                    <Row active={highlight() === symbols().length + i()} onClick={() => pick({ group: "file", item: f })}>
                      <span class="text-neutral-200 truncate min-w-0" title={f.file}>{displayLabel(f.file)}</span>
                      <span class="text-neutral-400 ml-2 text-xs shrink-0">{f.service}</span>
                    </Row>
                  )}
                </For>
              </Group>
              <Group label="SERVICES">
                <For each={services()}>
                  {(sv, i) => (
                    <Row
                      active={highlight() === symbols().length + files().length + i()}
                      onClick={() => pick({ group: "service", item: sv })}
                    >
                      <span class="text-neutral-200">{sv.name}</span>
                    </Row>
                  )}
                </For>
              </Group>
              <Group label="COMMANDS">
                <For each={cmds()}>
                  {(c, i) => (
                    <Row
                      active={highlight() === symbols().length + files().length + services().length + i()}
                      onClick={() => pick({ group: "command", item: c })}
                    >
                      <span class="text-neutral-200">{c.label}</span>
                    </Row>
                  )}
                </For>
              </Group>
            </Show>
          </div>
        </div>
      </div>
    </Show>
  );
}

function Group(props: { label: string; children: any }) {
  return (
    <div>
      <div class="px-3 pt-2 pb-1 text-[10px] font-semibold tracking-wide text-neutral-400">{props.label}</div>
      {props.children}
    </div>
  );
}

function Row(props: { active: boolean; onClick: () => void; children: any }) {
  return (
    <div
      class={`group px-3 py-1.5 cursor-pointer flex items-baseline min-w-0 ${props.active ? "bg-neutral-700" : "hover:bg-neutral-800"}`}
      onClick={props.onClick}
    >
      {props.children}
    </div>
  );
}
