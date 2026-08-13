import { createSignal } from "solid-js";

export type ScopeKind = "search" | "overview" | "service" | "folder" | "file" | "neighborhood" | "impact" | "flow" | "group";
export type Scope = { kind: ScopeKind; [key: string]: unknown };

const [stack, setStack] = createSignal<Scope[]>([{ kind: "search" }]);

export const scopeStore = {
  stack,
  push: (s: Scope) => setStack(p => [...p, s]),
  popTo: (i: number) => setStack(p => p.slice(0, i + 1)),
  reset: () => setStack([{ kind: "search" }]),
};
