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
  const firstVisible = Math.floor(scrollTop / rowHeight);
  const visibleCount = Math.ceil(viewportHeight / rowHeight);
  const start = Math.max(0, firstVisible - overscan);
  const end = Math.min(total, firstVisible + visibleCount + overscan);
  return { start, end, topPad: start * rowHeight, bottomPad: (total - end) * rowHeight };
}
