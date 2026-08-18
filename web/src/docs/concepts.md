# Concepts

## Scopes

Everything you look at in polyflow is a **scope** — a bounded slice of
the graph (a file, a service, a folder, a neighborhood, a flow lane, an
impact blast radius, a search result). Navigating pushes a new scope
onto the **scope stack**, shown as the breadcrumb bar; every crumb is
clickable to pop back. `Esc` pops one level at a time: close detail →
clear selection → pop isolation → pop scope.

## Flows

A **flow** is a chain of edges through the graph — upstream/downstream
from a node, a path between two waypoints, or everything passing
"through" a chosen node. Flows are canvas-free by default: you get a
ranked list first, and opt in to seeing them on canvas.

## Seams

A **seam** is the point where a flow crosses a service boundary — a
publish/subscribe pair over a channel, an HTTP call, an RPC. Isolating
a seam shows just that boundary: the producer, the channel, and every
consumer sharing it (never just the first match — a shared channel can
have many subscribers).

## Verification states

Every edge polyflow emits carries a verification state, so you can
tell a confirmed call from a static guess at a glance:

- **verified** — static analysis and runtime/contract evidence agree.
- **candidate** — static-only; possible but unconfirmed.
- **observed_only_gap** — seen at runtime or in a contract, but the
  static resolver missed it. This is polyflow's own honesty ledger —
  it says "the graph is incomplete here," not "there is no edge."
- **conflicting** — sources disagree; both worth a second look.

## The trust contract

Recall over precision. Anything polyflow cannot resolve is **ledgered**
as an unresolved reference or a low-confidence edge — never silently
dropped. Truncated results say so. A layout that can't render a shape
says so instead of guessing. If you ever see a number that looks too
clean, check the Health page: it surfaces index coverage, unresolved
counts, and eval recall so the tool's own claims stay checkable.
