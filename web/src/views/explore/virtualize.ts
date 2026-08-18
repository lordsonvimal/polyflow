// Simple fixed-row-height windowing — no virtualization dependency, so a
// 2,792-file RailsSvc tree only ever mounts the visible slice + overscan.
export interface Window {
  start: number;
  end: number; // exclusive
  topPad: number;
  bottomPad: number;
}

export function computeWindow(
  scrollTop: number,
  viewportHeight: number,
  rowHeight: number,
  total: number,
  overscan = 8,
): Window {
  if (total === 0 || rowHeight <= 0) return { start: 0, end: 0, topPad: 0, bottomPad: 0 };
  const visibleCount = Math.ceil(viewportHeight / rowHeight);
  // Clamp against `total` — `scrollTop` is a signal that only updates on
  // real scroll events, so it can be stale (pointing past the end) right
  // after the row list shrinks underneath it (collapsing a branch,
  // switching service). An unclamped firstVisible pushes `start` past the
  // real content, producing a bogus topPad that strands the visible rows
  // far down under a blank gap and desyncs the native scrollbar.
  const maxFirstVisible = Math.max(0, total - visibleCount);
  const firstVisible = Math.min(Math.max(0, Math.floor(scrollTop / rowHeight)), maxFirstVisible);
  const start = Math.max(0, firstVisible - overscan);
  const end = Math.min(total, firstVisible + visibleCount + overscan);
  return { start, end, topPad: start * rowHeight, bottomPad: (total - end) * rowHeight };
}
