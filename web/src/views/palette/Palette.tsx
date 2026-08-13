import { createSignal, createEffect, For, Show, onCleanup } from "solid-js";
import { paletteStore, type RecentItem } from "../../stores/palette";
import { commands as registeredCommands, type Command } from "../../commands/registry";
import { handleIntent } from "../../interaction/gestures";
import { scopeStore } from "../../stores/scope";
import { parseQuery, parseNodeCard, type ParsedQuery } from "./query";

const DEBOUNCE_MS = 150;
const RESULT_LIMIT = 8;

type SymbolEntry = { id: string; label: string; type: string; service: string; file: string; line: number };
type FileEntry = { file: string; service: string };
type Entry =
  | { group: "recent"; item: RecentItem }
  | { group: "symbol"; item: SymbolEntry }
  | { group: "file"; item: FileEntry }
  | { group: "command"; item: Command };

async function fetchSymbols(parsed: ParsedQuery): Promise<SymbolEntry[]> {
  if (!parsed.text) return [];
  const params = new URLSearchParams({ q: parsed.text, limit: String(RESULT_LIMIT) });
  if (parsed.chips.kind) params.set("kind", parsed.chips.kind);
  try {
    const r = await fetch(`/api/graph/search?${params}`);
    if (!r.ok) return [];
    const data = await r.json();
    let out: SymbolEntry[];
    if (Array.isArray(data)) {
      out = data.map((n: any) => ({ id: n.id, label: n.label, type: n.type, service: n.service, file: n.file, line: n.line }));
    } else {
      out = (data.nodes ?? []).map((hit: any) => {
        const card = parseNodeCard(hit.entity?.Text ?? "");
        return {
          id: hit.entity?.ID ?? hit.entity?.NodeID ?? "",
          label: card.label,
          type: card.type,
          service: card.service,
          file: hit.entity?.File ?? "",
          line: hit.entity?.Line ?? 0,
        };
      });
    }
    if (parsed.chips.service) out = out.filter(s => s.service === parsed.chips.service);
    return out.slice(0, RESULT_LIMIT);
  } catch {
    return [];
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

function matchCommands(parsed: ParsedQuery): Command[] {
  const needle = parsed.text.toLowerCase();
  const all = registeredCommands();
  if (!needle) return all.slice(0, RESULT_LIMIT);
  return all.filter(c => c.label.toLowerCase().includes(needle)).slice(0, RESULT_LIMIT);
}

export default function Palette() {
  let inputEl: HTMLInputElement | undefined;
  const [query, setQuery] = createSignal("");
  const [symbols, setSymbols] = createSignal<SymbolEntry[]>([]);
  const [files, setFiles] = createSignal<FileEntry[]>([]);
  const [cmds, setCmds] = createSignal<Command[]>([]);
  const [highlight, setHighlight] = createSignal(0);

  let debounceTimer: ReturnType<typeof setTimeout> | undefined;
  let seq = 0;
  onCleanup(() => clearTimeout(debounceTimer));

  function runSearch(raw: string) {
    const parsed = parseQuery(raw);
    setCmds(matchCommands(parsed));
    if (!parsed.text && !parsed.chips.kind && !parsed.chips.service) {
      setSymbols([]);
      setFiles([]);
      return;
    }
    const mySeq = ++seq;
    Promise.all([fetchSymbols(parsed), fetchFiles(parsed)]).then(([s, f]) => {
      if (mySeq !== seq) return; // a newer keystroke already superseded this request
      setSymbols(s);
      setFiles(f);
    });
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
      ...cmds().map(item => ({ group: "command", item }) as Entry),
    ];
  }

  function pick(entry: Entry) {
    switch (entry.group) {
      case "symbol": {
        const s = entry.item;
        handleIntent({ type: "select", target: { kind: "node", id: s.id } });
        handleIntent({ type: "drill", target: { kind: "node", id: s.id } });
        paletteStore.addRecent({ id: s.id, kind: "symbol", label: s.label, sub: `${s.service} · ${s.file}` });
        break;
      }
      case "file": {
        const f = entry.item;
        scopeStore.push({ kind: "file", service: f.service, path: f.file });
        paletteStore.addRecent({ id: `${f.service}:${f.file}`, kind: "file", label: f.file, sub: f.service });
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
          handleIntent({ type: "select", target: { kind: "node", id: r.id } });
          handleIntent({ type: "drill", target: { kind: "node", id: r.id } });
        } else if (r.kind === "file") {
          const [service, ...rest] = r.id.split(":");
          scopeStore.push({ kind: "file", service, path: rest.join(":") });
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
          <div class="max-h-96 overflow-y-auto text-sm">
            <Show when={query().trim() === ""}>
              <Group label="RECENT">
                <For each={paletteStore.recent()}>
                  {(r, i) => (
                    <Row active={highlight() === i()} onClick={() => pick({ group: "recent", item: r })}>
                      <span class="text-neutral-200">{r.label}</span>
                      <Show when={r.sub}><span class="text-neutral-500 ml-2 text-xs">{r.sub}</span></Show>
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
                      <span class="text-neutral-200">{s.label}</span>
                      <span class="text-neutral-500 ml-2 text-xs">{s.type} · {s.service} · {s.file}:{s.line}</span>
                    </Row>
                  )}
                </For>
              </Group>
              <Group label="FILES">
                <For each={files()}>
                  {(f, i) => (
                    <Row active={highlight() === symbols().length + i()} onClick={() => pick({ group: "file", item: f })}>
                      <span class="text-neutral-200">{f.file}</span>
                      <span class="text-neutral-500 ml-2 text-xs">{f.service}</span>
                    </Row>
                  )}
                </For>
              </Group>
              <Group label="COMMANDS">
                <For each={cmds()}>
                  {(c, i) => (
                    <Row active={highlight() === symbols().length + files().length + i()} onClick={() => pick({ group: "command", item: c })}>
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
      <div class="px-3 pt-2 pb-1 text-[10px] font-semibold tracking-wide text-neutral-500">{props.label}</div>
      {props.children}
    </div>
  );
}

function Row(props: { active: boolean; onClick: () => void; children: any }) {
  return (
    <div
      class={`px-3 py-1.5 cursor-pointer flex items-baseline ${props.active ? "bg-neutral-700" : "hover:bg-neutral-800"}`}
      onClick={props.onClick}
    >
      {props.children}
    </div>
  );
}
