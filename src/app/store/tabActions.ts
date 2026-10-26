import type { FocusDirection } from "../splitTree";
import type { SessionInfo } from "../../ipc/commands";
import { fileEditorRenamePending } from "../../features/files/editorGuards";
import { baseName } from "../../features/files/pathUtils";
import { focusedActiveWorkspace, focusedWorkspaces } from "../projectFocus";
import { isWorkspaceToolTab, nextTabId } from "./model";
import type { AppTab, SessionRuntimeInfo, UiState, Workspace } from "./types";
import { useUi } from "./useUi";

export function rejectPendingFileLocator(tab: AppTab): boolean {
  if (
    (tab.kind !== "files" && tab.kind !== "log") ||
    !tab.sessionId ||
    !tab.path ||
    !fileEditorRenamePending(tab.sessionId, tab.path)
  ) {
    return false;
  }
  useUi.getState().pushToast("info", "重命名进行中，完成后才能打开该文件");
  return true;
}

export function resolveWorkspaceId(
  tab: AppTab,
  st: { workspaces: Workspace[]; sessions: SessionRuntimeInfo[]; activeWorkspaceId: string | null },
  workspaceId?: string,
): string {
  const { ensureWorkspace } = useUi.getState();
  if (isWorkspaceToolTab(tab)) return ensureWorkspace({ kind: "tools", title: "工具" });
  if (tab.sessionId) {
    const hit = st.workspaces.find((w) => w.sessionId === tab.sessionId);
    if (hit) return hit.id;
  }
  if (tab.connId) {
    const hit = st.workspaces.find((w) => w.connId === tab.connId);
    if (hit) return hit.id;
  }
  if (tab.sessionId) {
    const s = st.sessions.find((x) => x.id === tab.sessionId);
    return ensureWorkspace({
      kind: "session",
      sessionId: tab.sessionId,
      title: s?.name ?? "会话",
      assetId: s?.assetId ?? undefined,
      assetKind: s?.kind,
    });
  }
  if (tab.connId) {
    return ensureWorkspace({
      kind: "db",
      connId: tab.connId,
      dbKind: tab.dbKind,
      title: tab.title,
    });
  }
  if (workspaceId) {
    const explicit = st.workspaces.find((w) => w.id === workspaceId);
    if (explicit) return explicit.id;
  }
  if (st.activeWorkspaceId) return st.activeWorkspaceId;
  return ensureWorkspace({ kind: "tools", title: "工具" });
}

export function useActiveWorkspace(): Workspace | null {
  return useUi((state) => focusedActiveWorkspace(state));
}

function workspaceForNavigation(
  state: Pick<UiState, "workspaces" | "activeWorkspaceId" | "activeProject" | "projectInventory">,
): Workspace | null {
  return focusedActiveWorkspace(state);
}

function cycleIndex(current: number, length: number, direction: 1 | -1): number {
  return (current + direction + length) % length;
}

export function cycleActiveWorkspace(direction: 1 | -1): string | null {
  const state = useUi.getState();
  const current = workspaceForNavigation(state);
  if (!current) return null;
  const visible = focusedWorkspaces(state);
  const index = visible.indexOf(current);
  const target = visible[cycleIndex(index, visible.length, direction)];
  if (!target) return null;
  state.setActiveWorkspace(target.id);
  return target.id;
}

export function cycleActiveTab(direction: 1 | -1): string | null {
  const state = useUi.getState();
  const workspace = workspaceForNavigation(state);
  const panes = workspace?.panes ?? [];
  const pane = panes.find((candidate) => candidate.id === workspace?.activePaneId) ?? panes[0];
  if (!workspace || !pane || pane.tabs.length === 0) return null;
  const current = pane.tabs.find((tab) => tab.id === pane.activeTabId) ?? pane.tabs[pane.tabs.length - 1];
  const target = pane.tabs[cycleIndex(pane.tabs.indexOf(current), pane.tabs.length, direction)];
  if (!target) return null;
  state.setActiveTab(target.id);
  return target.id;
}

// focusActivePane moves focus to the pane next to the active one in `direction` within the focused
// workspace, following the on-screen geometry of the split tree.
export function focusActivePane(direction: FocusDirection): string | null {
  const state = useUi.getState();
  const workspace = workspaceForNavigation(state);
  if (!workspace) return null;
  const target = state.focusPane(direction, workspace.id);
  if (target && state.activeWorkspaceId !== workspace.id) state.setActiveWorkspace(workspace.id);
  return target;
}

export async function openTerminalTab(
  session: SessionInfo,
  title?: string,
  paneId?: string,
  options?: { directory?: string },
) {
  const wsId = useUi.getState().ensureWorkspace({
    kind: "session",
    sessionId: session.id,
    assetId: session.assetId ?? undefined,
    title: session.name,
    assetKind: session.kind,
  });
  const ws = useUi.getState().workspaces.find((w) => w.id === wsId);
  const n = ws?.panes.flatMap((p) => p.tabs).filter((t) => t.kind === "terminal").length ?? 0;
  useUi.getState().addTab(
    {
      id: nextTabId(`term-${session.id}`),
      kind: "terminal",
      title: title ?? `终端 ${n + 1}`,
      sessionId: session.id,
      pendingDirectory: options?.directory,
      closable: true,
    },
    paneId,
  );
}

export function findTab(id: string): { tab: AppTab; ws: Workspace } | null {
  for (const w of useUi.getState().workspaces) {
    for (const p of w.panes) {
      const t = p.tabs.find((x) => x.id === id);
      if (t) return { tab: t, ws: w };
    }
  }
  return null;
}

export function sanitizeRemoteTabTitle(raw: string): string {
  const cleaned = raw
    .replace(/[\x00-\x1f\x7f]/g, " ")
    .replace(/\s+/g, " ")
    .trim()
    .slice(0, 80)
    .trim();
  return cleaned;
}

// A custom title needs both the lock and its decision timestamp (see AppTab.customTitleAt).
export function hasCustomTitle(tab: Pick<AppTab, "renamedByUser" | "customTitleAt">): boolean {
  return tab.renamedByUser === true && typeof tab.customTitleAt === "number";
}

export function applyRemoteTabTitle(id: string, rawTitle: string): boolean {
  const found = findTab(id);
  if (!found) return false;
  if (hasCustomTitle(found.tab)) return false;
  const title = sanitizeRemoteTabTitle(rawTitle);
  if (!title || title === found.tab.title) return false;
  useUi.getState().updateTab(id, { title });
  return true;
}

export function takePendingDirectory(storeTabId: string): string | null {
  const st = useUi.getState();
  for (const w of st.workspaces) {
    for (const p of w.panes) {
      const tab = p.tabs.find((t) => t.id === storeTabId);
      if (tab?.pendingDirectory) {
        const directory = tab.pendingDirectory;
        st.updateTab(storeTabId, { pendingDirectory: undefined });
        return directory;
      }
    }
  }
  return null;
}

export function findWritableTerminal(
  sessionId: string,
): { tabId: string; shell: "powershell" | "cmd" | "posix" | "unknown" } | null {
  const st = useUi.getState();
  for (const w of st.workspaces) {
    if (w.sessionId !== sessionId) continue;
    for (const p of w.panes) {
      const ordered = [
        ...p.tabs.filter((t) => t.id === p.activeTabId),
        ...p.tabs.filter((t) => t.id !== p.activeTabId),
      ];
      for (const t of ordered) {
        if (t.kind === "terminal" && t.tabId && !t.dead && !t.exited) {
          return { tabId: t.tabId, shell: t.shell ?? "unknown" };
        }
      }
    }
  }
  return null;
}

export function openFileTab(sessionId: string, path: string) {
  useUi.getState().addTab(fileTabSpec(sessionId, path, baseName(path)));
}

export function openFileTabInSplit(sessionId: string, path: string) {
  useUi.getState().openInLowerPane(fileTabSpec(sessionId, path, baseName(path)));
}

// openDirectoryTab opens a file browser tab at a directory of the session.
export function openDirectoryTab(sessionId: string, path: string) {
  useUi.getState().addTab({
    id: nextTabId(`files-${sessionId}`),
    kind: "files",
    title: baseName(path) || path,
    sessionId,
    directory: path,
    closable: true,
  });
}

export function openLogTab(sessionId: string, path: string) {
  useUi.getState().addTab({
    id: `log-${sessionId}-${path}`,
    kind: "log",
    title: baseName(path),
    sessionId,
    path,
    closable: true,
  });
}

function fileTabSpec(sessionId: string, path: string, name: string): AppTab {
  return {
    id: nextTabId("file"),
    kind: "files",
    title: name,
    sessionId,
    path,
    closable: true,
  };
}

export function openCredentialsSidebar() {
  const st = useUi.getState();
  st.setLeftMode("credentials");
  st.setLeftOpen(true);
}

export function openCredentialsTab(credId?: string) {
  useUi.getState().addTab({
    id: "tab-credentials",
    kind: "credentials",
    title: "凭据",
    ...(credId !== undefined ? { credId } : {}),
    closable: true,
  });
}

export function useCredentialsTabId(): string | undefined {
  return useUi((s) => {
    for (const w of s.workspaces) {
      for (const p of w.panes) {
        const t = p.tabs.find((x) => x.id === "tab-credentials");
        if (t) return t.credId;
      }
    }
    return undefined;
  });
}
