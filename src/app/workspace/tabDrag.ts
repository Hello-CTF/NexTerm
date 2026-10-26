import { useSyncExternalStore } from "react";

// A tab drag carries the tab id under this type. The source is also kept in this module: a drag that
// started in another window has the type but no source here, so it is ignored (same-window drags only).
export const TAB_DRAG_MIME = "application/x-nexterm-tab";

export interface TabDragSource {
  tabId: string;
  paneId: string;
  /** Tabs in the source pane when the drag started; a pane never gives up its only tab to its own edge. */
  paneTabCount: number;
}

let source: TabDragSource | null = null;
const listeners = new Set<() => void>();

function emit(): void {
  for (const listener of listeners) listener();
}

// Escape cancels the drag. Native drags usually swallow keys and end with dragend themselves; this
// covers webviews that deliver the key, so a later drop is ignored.
function onKeyDown(event: KeyboardEvent): void {
  if (event.key === "Escape" && source) endTabDrag();
}

export function beginTabDrag(next: TabDragSource): void {
  source = next;
  if (typeof window !== "undefined") window.addEventListener("keydown", onKeyDown, true);
  emit();
}

export function endTabDrag(): void {
  if (typeof window !== "undefined") window.removeEventListener("keydown", onKeyDown, true);
  if (!source) return;
  source = null;
  emit();
}

export function currentTabDrag(): TabDragSource | null {
  return source;
}

/** The active same-window tab drag carried by `transfer`, if any. */
export function tabDragFrom(transfer: DataTransfer | null): TabDragSource | null {
  if (!source || !transfer) return null;
  return Array.from(transfer.types ?? []).includes(TAB_DRAG_MIME) ? source : null;
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function useTabDrag(): TabDragSource | null {
  return useSyncExternalStore(subscribe, currentTabDrag, () => null);
}
