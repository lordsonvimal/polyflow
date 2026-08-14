import { createSignal, createEffect } from "solid-js";

export type Activity = "explore" | "flows" | "impact" | "health" | "config" | "docs" | "settings";
type Theme = "light" | "dark";

function initTheme(): Theme {
  const stored = localStorage.getItem("pf:theme");
  if (stored === "light" || stored === "dark") return stored;
  return typeof window.matchMedia === "function" && window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function initPanelWidth(): number {
  const v = parseInt(localStorage.getItem("pf:panelWidth") ?? "280", 10);
  return isNaN(v) ? 280 : Math.max(120, v);
}

function initDetailWidth(): number {
  const v = parseInt(localStorage.getItem("pf:detailWidth") ?? "320", 10);
  return isNaN(v) ? 320 : Math.max(240, v);
}

const [activity, setActivity] = createSignal<Activity>("explore");
const [panelWidth, _setPanelWidth] = createSignal<number>(initPanelWidth());
const [panelCollapsed, _setPanelCollapsed] = createSignal<boolean>(
  localStorage.getItem("pf:panelCollapsed") === "true"
);
const [detailWidth, _setDetailWidth] = createSignal<number>(initDetailWidth());
const [theme, _setTheme] = createSignal<Theme>(initTheme());

function setPanelWidth(w: number) {
  const clamped = Math.max(120, w);
  _setPanelWidth(clamped);
  localStorage.setItem("pf:panelWidth", String(clamped));
}

function setDetailWidth(w: number) {
  const clamped = Math.max(240, Math.min(w, window.innerWidth - 200));
  _setDetailWidth(clamped);
  localStorage.setItem("pf:detailWidth", String(clamped));
}

function setPanelCollapsed(v: boolean) {
  _setPanelCollapsed(v);
  localStorage.setItem("pf:panelCollapsed", String(v));
}

function setTheme(t: Theme) {
  _setTheme(t);
  localStorage.setItem("pf:theme", t);
}

export const layoutPrefs = {
  activity, setActivity,
  panelWidth, setPanelWidth,
  panelCollapsed, setPanelCollapsed,
  detailWidth, setDetailWidth,
  theme, setTheme,
};
