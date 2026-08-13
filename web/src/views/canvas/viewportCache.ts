// UN.1: drilling into a scope and popping back out restores the viewport
// (pan/zoom) that scope had before — pure so the key/lookup math is
// testable without a Cytoscape instance.
import { Scope } from "../../stores/scope";

export interface Viewport {
  pan: { x: number; y: number };
  zoom: number;
}

const cache = new Map<string, Viewport>();

// The full scope stack (not just the top scope) is the key: "service X" at
// stack depth 2 is a different view than "service X" reached a different
// way, and each keeps its own remembered viewport.
export function stackKey(stack: Scope[]): string {
  return JSON.stringify(stack);
}

export function saveViewport(key: string, vp: Viewport): void {
  cache.set(key, vp);
}

export function getViewport(key: string): Viewport | undefined {
  return cache.get(key);
}

// Test-only: clears module-singleton state between test cases.
export function resetViewportCache(): void {
  cache.clear();
}
