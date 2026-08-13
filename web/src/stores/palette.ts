import { createSignal } from "solid-js";

const RECENT_KEY = "pf:paletteRecent";
const RECENT_MAX = 20;

export type RecentItem = {
  id: string;
  kind: "symbol" | "file" | "service" | "command";
  label: string;
  sub?: string;
};

function loadRecent(): RecentItem[] {
  try {
    const raw = localStorage.getItem(RECENT_KEY);
    const parsed = raw ? JSON.parse(raw) : [];
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

const [isOpen, setIsOpen] = createSignal(false);
const [recent, setRecent] = createSignal<RecentItem[]>(loadRecent());
// Set by openWithQuery, consumed once (and cleared) by Palette.tsx's mount
// effect — lets any view (StackPanel's number click-navigate, UN.4) open
// the palette pre-filtered without owning Palette's own query state.
const [pendingQuery, setPendingQuery] = createSignal<string | undefined>(undefined);

function addRecent(item: RecentItem) {
  const next = [item, ...recent().filter(r => !(r.id === item.id && r.kind === item.kind))].slice(0, RECENT_MAX);
  setRecent(next);
  localStorage.setItem(RECENT_KEY, JSON.stringify(next));
}

export const paletteStore = {
  isOpen,
  recent,
  pendingQuery,
  clearPendingQuery: () => setPendingQuery(undefined),
  open: () => setIsOpen(true),
  openWithQuery: (q: string) => {
    setPendingQuery(q);
    setIsOpen(true);
  },
  close: () => setIsOpen(false),
  toggle: () => setIsOpen(v => !v),
  addRecent,
};
