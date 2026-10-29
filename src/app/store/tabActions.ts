import type { FocusDirection } from "../splitTree";
import type { SessionInfo } from "../../ipc/commands";
import { fileEditorRenamePending } from "../../features/files/editorGuards";
import { baseName } from "../../features/files/pathUtils";
import { focusedActiveWorkspace, focusedWorkspaces } from "../projectFocus";
import { regularTerminalReason } from "./durableReadiness";
import { isWorkspaceToolTab, nextTabId } from "./model";
import type { AppTab, LeftMode, SessionRuntimeInfo, UiState, Workspace } from "./types";
import { useUi } from "./useUi";
import { clipText } from "../../ui/textClip";

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

// Terminal opens per session that are still deciding their mode, so quick repeated opens get distinct titles.
const pendingTerminalOpens = new Map<string, number>();

function sessionTerminalCount(sessionId: string): number {
  const ws = useUi.getState().workspaces.find((w) => w.sessionId === sessionId);
  return ws?.panes.flatMap((p) => p.tabs).filter((t) => t.kind === "terminal").length ?? 0;
}

// openTerminalTab is the explicit "new terminal" action. It opens a durable terminal when the session's durable
// helper is ready; while the helper is still preparing or unavailable it opens a regular terminal without waiting for
// the helper and records the reason on the tab, so the degradation is visible. The mode decision itself is bounded
// (see regularTerminalReason).
export async function openTerminalTab(
  session: SessionInfo,
  title?: string,
  paneId?: string,
  options?: { directory?: string },
) {
  const pending = pendingTerminalOpens.get(session.id) ?? 0;
  const number = sessionTerminalCount(session.id) + pending + 1;
  pendingTerminalOpens.set(session.id, pending + 1);
  let regularReason: string | undefined;
  try {
    regularReason = await regularTerminalReason(session);
  } finally {
    const left = (pendingTerminalOpens.get(session.id) ?? 1) - 1;
    if (left > 0) pendingTerminalOpens.set(session.id, left);
    else pendingTerminalOpens.delete(session.id);
  }
  useUi.getState().ensureWorkspace({
    kind: "session",
    sessionId: session.id,
    assetId: session.assetId ?? undefined,
    title: session.name,
    assetKind: session.kind,
  });
  useUi.getState().addTab(
    {
      id: nextTabId(`term-${session.id}`),
      kind: "terminal",
      title: title ?? `终端 ${number}`,
      sessionId: session.id,
      pendingDirectory: options?.directory,
      ...(regularReason ? { regularReason } : {}),
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

// Tab titles are capped at the backend's rename limit, counted in code points (maxTabTitleRunes).
export const MAX_TAB_TITLE_CODE_POINTS = 80;

// Controls become spaces; zero-width space, bidi embedding/override/isolate controls and the
// deprecated format controls U+206A-206F are dropped so a title cannot hide or reorder tab
// strip text (LRM/RLM/ALM, ZWJ and ZWNJ stay). Emoji-forming characters (ZWJ, variation selectors, skin
// tones, tags, keycaps, regional indicators) are kept, and clipping never splits a cluster.
export function sanitizeRemoteTabTitle(raw: string): string {
  const cleaned = raw
    .replace(/[\u0000-\u001f\u007f-\u009f]/g, " ")
    // Lone surrogates become U+FFFD here, as JSON decoding does on the backend, so the
    // displayed and the stored title agree.
    .replace(/\p{Cs}/gu, "\ufffd")
    .replace(/[\u200b\u202a-\u202e\u2066-\u206f]/g, "")
    .replace(/\s+/g, " ")
    .trim();
  return clipText(cleaned, MAX_TAB_TITLE_CODE_POINTS).trim();
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

// isHostWorkspace reports whether the workspace is a session workspace, whose sidebar defaults to its file tree.
export function isHostWorkspace(ws: Workspace | null | undefined): boolean {
  return ws?.kind === "session" && Boolean(ws.sessionId);
}

// showLeftPanel is the asset/credentials switch of the left sidebar. In a host workspace it is a temporary override,
// and a click on the already active button while the dock is open reverts to the file tree; elsewhere it sets the
// persisted choice. dockOpen is whether the dock is visible now; the return value says whether the caller should
// open the dock.
export function showLeftPanel(mode: LeftMode, dockOpen = true): boolean {
  const st = useUi.getState();
  const ws = st.workspaces.find((w) => w.id === st.activeWorkspaceId);
  if (!isHostWorkspace(ws)) {
    st.setLeftMode(mode);
    st.setLeftOverride(null);
    return true;
  }
  if (st.leftOverride === mode && dockOpen) {
    st.setLeftOverride(null);
    return false;
  }
  st.setLeftOverride(mode);
  return true;
}

export function openCredentialsSidebar() {
  if (showLeftPanel("credentials", useUi.getState().leftOpen)) useUi.getState().setLeftOpen(true);
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
