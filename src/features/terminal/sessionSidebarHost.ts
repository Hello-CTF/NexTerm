import { create } from "zustand";

interface PaneReport {
  tabId: string;
  wide: boolean;
}

interface SessionSidebarHostState {
  // The pane that holds the session sidebar, per workspace. Memory only; the open flag is the stored preference.
  hosts: Record<string, string>;
  // Written by the visible terminal of each pane so a takeover can tell whether the host pane still fits the sidebar.
  reports: Record<string, PaneReport>;
  setHost: (workspaceId: string, paneId: string) => void;
  report: (paneId: string, tabId: string, wide: boolean) => void;
  // Removes the report only when the same tab wrote it, so a hidden sibling tab cannot erase the visible tab's report.
  clearReport: (paneId: string, tabId: string) => void;
}

export const useSessionSidebarHost = create<SessionSidebarHostState>((set) => ({
  hosts: {},
  reports: {},
  setHost: (workspaceId, paneId) =>
    set((s) => (s.hosts[workspaceId] === paneId ? s : { hosts: { ...s.hosts, [workspaceId]: paneId } })),
  report: (paneId, tabId, wide) =>
    set((s) => {
      const cur = s.reports[paneId];
      if (cur && cur.tabId === tabId && cur.wide === wide) return s;
      return { reports: { ...s.reports, [paneId]: { tabId, wide } } };
    }),
  clearReport: (paneId, tabId) =>
    set((s) => {
      if (s.reports[paneId]?.tabId !== tabId) return s;
      const { [paneId]: _removed, ...rest } = s.reports;
      return { reports: rest };
    }),
}));
