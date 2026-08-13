import { createSignal } from "solid-js";

const RECENT_KEY = "pf:paletteRecent";
const RECENT_MAX = 20;

export type RecentItem = {
  id: string;
  kind: "symbol" | "file" | "command";
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

function addRecent(item: RecentItem) {
  const next = [item, ...recent().filter(r => !(r.id === item.id && r.kind === item.kind))].slice(0, RECENT_MAX);
  setRecent(next);
  localStorage.setItem(RECENT_KEY, JSON.stringify(next));
}

export const paletteStore = {
  isOpen,
  recent,
  open: () => setIsOpen(true),
  close: () => setIsOpen(false),
  toggle: () => setIsOpen(v => !v),
  addRecent,
};
