import { create } from "zustand";
import type { RemoteStatus } from "./remoteTypes";

// Live state of the remote desktop panes that are mounted, keyed by the UI tab id. A restored tab has no entry until
// the user connects it, which the overview reads as disconnected.
export interface RemoteEntry {
  status: RemoteStatus;
  thumb?: string;
  thumbAt?: number;
}

// One small picture per connected session, and no more than this many at once, so a crowd of sessions cannot grow
// memory without bound. The oldest picture is dropped first.
export const MAX_THUMBNAILS = 24;
export const THUMBNAIL_WIDTH = 240;
export const THUMBNAIL_INTERVAL_MS = 5000;

interface RemoteStatusState {
  entries: Readonly<Record<string, RemoteEntry>>;
}

export const useRemoteStatus = create<RemoteStatusState>(() => ({ entries: {} }));

export function setRemoteStatus(tabId: string, status: RemoteStatus): void {
  useRemoteStatus.setState((state) => {
    const current = state.entries[tabId];
    if (current?.status === status) return state;
    // A dropped session keeps its last picture: it shows what was on screen when the link broke.
    return { entries: { ...state.entries, [tabId]: { ...current, status } } };
  });
}

export function setRemoteThumbnail(tabId: string, thumb: string, now = Date.now()): void {
  useRemoteStatus.setState((state) => {
    const current = state.entries[tabId];
    if (!current) return state;
    const entries: Record<string, RemoteEntry> = { ...state.entries, [tabId]: { ...current, thumb, thumbAt: now } };
    const withThumb = Object.entries(entries).filter(([, entry]) => entry.thumb);
    if (withThumb.length > MAX_THUMBNAILS) {
      withThumb.sort((a, b) => (a[1].thumbAt ?? 0) - (b[1].thumbAt ?? 0));
      for (const [id] of withThumb.slice(0, withThumb.length - MAX_THUMBNAILS)) {
        entries[id] = { ...entries[id], thumb: undefined, thumbAt: undefined };
      }
    }
    return { entries };
  });
}

export function clearRemoteStatus(tabId: string): void {
  useRemoteStatus.setState((state) => {
    if (!(tabId in state.entries)) return state;
    const entries = { ...state.entries };
    delete entries[tabId];
    return { entries };
  });
}

// The overview asks for pictures only while it is on screen; panes capture only while somebody asks.
let thumbnailDemand = 0;

export function acquireThumbnailDemand(): () => void {
  thumbnailDemand += 1;
  let released = false;
  return () => {
    if (released) return;
    released = true;
    thumbnailDemand -= 1;
  };
}

export function thumbnailsWanted(): boolean {
  return thumbnailDemand > 0;
}

export function resetRemoteStatusForTest(): void {
  thumbnailDemand = 0;
  useRemoteStatus.setState({ entries: {} });
}
