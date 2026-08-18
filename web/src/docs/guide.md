# UI guide

## Gesture grammar

The same gestures mean the same thing everywhere — tree rows, canvas
nodes, edges, and groups all follow this table.

| Gesture | Meaning |
|---|---|
| hover | tooltip (label · kind · `file:line`) + highlight incident edges |
| single-click | select → detail panel |
| double-click | drill/expand (service → internals, folder → files, file → symbols, group → expand) |
| right-click | context menu — isolate flows through here, set as path start/end, impact from here, show source, copy context, expand/collapse, copy path, hide |
| shift-click / marquee-drag | add to multi-selection |
| scroll / drag-canvas / drag-node | zoom / pan / reposition |
| hover on a link-list row | **peek** — ghost-preview on canvas, no state change |

## Peek vs commit

Any list naming graph elements not yet on canvas (link lists, flow
lists, path lists, search results) supports two depths of engagement:

- **Peek** — hovering a row ghost-previews the referenced elements on
  canvas without changing the URL, scope stack, or selection. Free to
  explore; impossible to get lost in.
- **Commit** — clicking (or Enter) makes it real: scope expansion or
  navigation, per the row's action. Every commit is one `Esc` or
  breadcrumb-pop away from undo.

## Keyboard shortcuts

The table below is generated from the app's live shortcut registry —
it always matches what's actually bound.
