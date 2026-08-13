import { createSignal } from "solid-js";
import { layoutPrefs, type Activity } from "../stores/layoutPrefs";
import { scopeStore, encodeViewState } from "../stores/scope";
import { notificationsStore } from "../stores/notifications";

// Single source of truth for palette commands. Plans 11–13 add entries via
// registerCommand — this file only seeds the shell-level actions US.4 owns.
export type Command = {
  id: string;
  label: string;
  run: () => void;
};

const [registry, setRegistry] = createSignal<Command[]>([]);

export function registerCommand(cmd: Command): void {
  setRegistry(prev => [...prev.filter(c => c.id !== cmd.id), cmd]);
}

export function commands(): Command[] {
  return registry();
}

const ACTIVITIES: { id: Activity; label: string }[] = [
  { id: "explore", label: "Explore" },
  { id: "flows", label: "Flows" },
  { id: "impact", label: "Impact" },
  { id: "health", label: "Health" },
  { id: "config", label: "Config" },
  { id: "docs", label: "Docs" },
  { id: "settings", label: "Settings" },
];

for (const a of ACTIVITIES) {
  registerCommand({
    id: `activity:${a.id}`,
    label: `Switch activity: ${a.label}`,
    run: () => layoutPrefs.setActivity(a.id),
  });
}

registerCommand({
  id: "theme:toggle",
  label: "Toggle theme",
  run: () => layoutPrefs.setTheme(layoutPrefs.theme() === "dark" ? "light" : "dark"),
});

registerCommand({
  id: "share:copy-link",
  label: "Copy link to current view",
  run: () => {
    const url = `${location.origin}${location.pathname}#v=${encodeViewState(scopeStore.viewState())}`;
    const id = `copy-link-${Date.now()}`;
    navigator.clipboard?.writeText(url).then(
      () => notificationsStore.add({ id, kind: "success", message: "Link copied to clipboard" }),
      () => notificationsStore.add({ id, kind: "error", message: "Could not copy link" }),
    );
  },
});
