import { createSignal } from "solid-js";

type Layout = "dagre" | "fcose" | "circle" | "grid";

const [selectedNodeId, setSelectedNodeId] = createSignal<string | null>(null);
const [layout, setLayout] = createSignal<Layout>("dagre");
const [activeFilters, setActiveFilters] = createSignal<string[]>([]);
const [notification, setNotification] = createSignal<string | null>(null);

function addFilter(type: string) {
  setActiveFilters((prev) => (prev.includes(type) ? prev : [...prev, type]));
}

function removeFilter(type: string) {
  setActiveFilters((prev) => prev.filter((f) => f !== type));
}

function clearNotification() {
  setNotification(null);
}

export const uiStore = {
  selectedNodeId,
  setSelectedNodeId,
  layout,
  setLayout,
  activeFilters,
  addFilter,
  removeFilter,
  notification,
  setNotification,
  clearNotification,
};
