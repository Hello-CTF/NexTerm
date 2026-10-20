import { create } from "zustand";

interface UnreadState {
  unreadTabIds: ReadonlySet<string>;
}

interface UnreadChanges {
  mark?: Iterable<string>;
  clear?: Iterable<string>;
  openTabIds?: ReadonlySet<string>;
}

export const useUnreadStore = create<UnreadState>(() => ({
  unreadTabIds: new Set<string>(),
}));

export function useTabUnread(tabId: string): boolean {
  return useUnreadStore((state) => state.unreadTabIds.has(tabId));
}

export function isTabUnread(tabId: string): boolean {
  return useUnreadStore.getState().unreadTabIds.has(tabId);
}

export function updateUnreadTabs({ mark = [], clear = [], openTabIds }: UnreadChanges): void {
  useUnreadStore.setState((state) => {
    let next: Set<string> | undefined;
    const mutable = () => {
      next ??= new Set(state.unreadTabIds);
      return next;
    };
    for (const tabId of clear) {
      if (state.unreadTabIds.has(tabId)) mutable().delete(tabId);
    }
    for (const tabId of mark) {
      if (openTabIds && !openTabIds.has(tabId)) continue;
      if (!state.unreadTabIds.has(tabId)) mutable().add(tabId);
    }
    return next ? { unreadTabIds: next } : state;
  });
}

export function setTabUnread(tabId: string, unread: boolean): void {
  updateUnreadTabs(unread ? { mark: [tabId] } : { clear: [tabId] });
}

export function clearTabUnread(tabId: string): void {
  updateUnreadTabs({ clear: [tabId] });
}

export function retainUnreadTabs(tabIds: ReadonlySet<string>): void {
  useUnreadStore.setState((state) => {
    if ([...state.unreadTabIds].every((tabId) => tabIds.has(tabId))) return state;
    return {
      unreadTabIds: new Set([...state.unreadTabIds].filter((tabId) => tabIds.has(tabId))),
    };
  });
}

export function resetUnreadTabs(): void {
  useUnreadStore.setState((state) =>
    state.unreadTabIds.size === 0 ? state : { unreadTabIds: new Set<string>() },
  );
}
