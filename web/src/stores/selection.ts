import { createSignal } from "solid-js";

export type Selection = { kind: "node" | "edge"; id: string } | null;

const [selection, setSelection] = createSignal<Selection>(null);

export const selectionStore = { selection, setSelection };
