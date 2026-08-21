import { For } from "solid-js";
import { layoutPrefs } from "../stores/layoutPrefs";

type ActivityId = "explore" | "flows" | "impact" | "deadcode" | "health" | "config" | "docs" | "settings";

const ACTIVITIES: { id: ActivityId; icon: string; label: string }[] = [
  { id: "explore",  icon: "⬡", label: "Explore" },
  { id: "flows",    icon: "⇄", label: "Flows" },
  { id: "impact",   icon: "◎", label: "Impact" },
  { id: "deadcode", icon: "☠", label: "Dead code" },
  { id: "health",   icon: "♥", label: "Health" },
  { id: "config",   icon: "⚙", label: "Config" },
  { id: "docs",     icon: "☰", label: "Docs" },
  { id: "settings", icon: "⊞", label: "Settings" },
];

export default function ActivityBar() {
  return (
    <nav
      data-testid="activity-bar"
      class="flex flex-col items-center w-12 shrink-0 border-r border-neutral-800 dark:border-neutral-700 bg-neutral-950 dark:bg-neutral-950 py-2 gap-1"
    >
      <For each={ACTIVITIES}>
        {(a) => (
          <button
            title={a.label}
            onClick={() => {
              // Switching activity while the panel is collapsed produced no
              // visible change, making the sidebar look unresponsive.
              layoutPrefs.setActivity(a.id);
              if (layoutPrefs.panelCollapsed()) layoutPrefs.setPanelCollapsed(false);
            }}
            class={`w-9 h-9 rounded flex items-center justify-center text-lg transition-colors
              ${layoutPrefs.activity() === a.id
                ? "bg-neutral-700 text-white"
                : "text-neutral-400 hover:text-white hover:bg-neutral-800"}`}
          >
            {a.icon}
          </button>
        )}
      </For>
    </nav>
  );
}
