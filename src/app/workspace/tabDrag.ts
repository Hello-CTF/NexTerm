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
let fallbackTimer: ReturnType<typeof setTimeout> | null = null;
const listeners = new Set<() => void>();

function emit(): void {
  for (const listener of listeners) listener();
}

// Escape cancels the drag. Native drags usually swallow keys and end with dragend themselves; this
// covers webviews that deliver the key, so a later drop is ignored.
function onKeyDown(event: KeyboardEvent): void {
  if (event.key === "Escape") endTabDrag();
}

const end = () => endTabDrag();

// The drag must not outlive its gesture: a stale source keeps the drop targets over every pane (blocking
// terminal mouse input) and would accept another window's drag. Besides the source tab's own dragend,
// the drag ends on any dragend or drop reaching the window (bubble phase, after the app's handlers
// ran), on window blur, and on the first mouse or pointer event: none fire during a native drag, so
// one arriving means the drag is over, for example after its source tab was removed mid-drag.
const WINDOW_ENDS = ["dragend", "drop", "blur"] as const;
const POINTER_ENDS = ["pointerdown", "mousedown", "mousemove"] as const;

function detach(): void {
  if (fallbackTimer !== null) clearTimeout(fallbackTimer);
  fallbackTimer = null;
  if (typeof window === "undefined") return;
  window.removeEventListener("keydown", onKeyDown, true);
  for (const type of WINDOW_ENDS) window.removeEventListener(type, end);
  for (const type of POINTER_ENDS) window.removeEventListener(type, end, true);
}

export function beginTabDrag(next: TabDragSource): void {
  detach();
  source = next;
  if (typeof window !== "undefined") {
    window.addEventListener("keydown", onKeyDown, true);
    for (const type of WINDOW_ENDS) window.addEventListener(type, end);
    // Armed a tick later so pointer events of the gesture that started the drag do not end it.
    fallbackTimer = setTimeout(() => {
      fallbackTimer = null;
      if (!source) return;
      for (const type of POINTER_ENDS) window.addEventListener(type, end, true);
    }, 0);
  }
  emit();
}

export function endTabDrag(): void {
  detach();
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
