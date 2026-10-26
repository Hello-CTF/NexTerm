import { useEffect, useMemo } from "react";
import { useUi, type Workspace } from "../../app/store";
import { useSessionPanel } from "./sessionPanelStore";

// SessionPanelContext is what the side panel shows: a host session and, when known, the terminal tab whose working
// directory the panel follows.
export interface SessionPanelContext {
  // key identifies the terminal tab (workspace + tab id), or the workspace itself when no terminal is involved.
  key: string;
  workspaceId: string;
  // tabId is the terminal's tab id; null for a host workspace context without a focused terminal.
  tabId: string | null;
  // terminalId is the backend terminal id reporting OSC 7 directories; absent until the terminal attaches.
  terminalId: string | null;
  sessionId: string | null;
  hostLabel: string;
  // containerId marks a terminal inside a container: its directories are not the host's.
  containerId: string | null;
}

// Where the context came from: the focused terminal, the active host workspace (no terminal focused), the last focused
// terminal elsewhere, a lock, or nothing.
export type SessionPanelSource = "focus" | "workspace" | "last" | "locked" | "none";

interface FocusSource {
  workspaces: Workspace[];
  activeWorkspaceId: string | null;
}

// focusedTerminalContext resolves the focused terminal: the active workspace's active pane's active tab, when it is a
// terminal. Any other focused tab (files, logs, settings, a database) yields null.
export function focusedTerminalContext({ workspaces, activeWorkspaceId }: FocusSource): SessionPanelContext | null {
  const ws = workspaces.find((w) => w.id === activeWorkspaceId);
  if (!ws) return null;
  const pane = ws.panes.find((p) => p.id === ws.activePaneId) ?? ws.panes[0];
  const tab = pane?.tabs.find((t) => t.id === pane.activeTabId);
  if (!tab || tab.kind !== "terminal") return null;
  return {
    key: `${ws.id}:${tab.id}`,
    workspaceId: ws.id,
    tabId: tab.id,
    terminalId: tab.tabId ?? null,
    sessionId: tab.sessionId ?? ws.sessionId ?? null,
    hostLabel: ws.title,
    containerId: tab.containerId ?? null,
  };
}

function hostWorkspaceContext(ws: Workspace): SessionPanelContext | null {
  if (ws.kind !== "session" || !ws.sessionId) return null;
  return {
    key: `${ws.id}:`,
    workspaceId: ws.id,
    tabId: null,
    terminalId: null,
    sessionId: ws.sessionId,
    hostLabel: ws.title,
    containerId: null,
  };
}

// activeWorkspaceContext is the active host workspace when no terminal has focus (an editor tab, or a device workspace
// that has no terminal yet): the panel browses that host without following a directory.
export function activeWorkspaceContext({ workspaces, activeWorkspaceId }: FocusSource): SessionPanelContext | null {
  const ws = workspaces.find((w) => w.id === activeWorkspaceId);
  return ws ? hostWorkspaceContext(ws) : null;
}

// refreshContext re-reads a remembered context from the current workspaces: the terminal may have attached, reconnected
// to a new session, or closed (null).
export function refreshContext(context: SessionPanelContext, workspaces: Workspace[]): SessionPanelContext | null {
  const ws = workspaces.find((w) => w.id === context.workspaceId);
  if (!ws) return null;
  if (context.tabId === null) return hostWorkspaceContext(ws);
  for (const pane of ws.panes) {
    const tab = pane.tabs.find((t) => t.id === context.tabId);
    if (!tab || tab.kind !== "terminal") continue;
    return {
      ...context,
      terminalId: tab.tabId ?? null,
      sessionId: tab.sessionId ?? ws.sessionId ?? null,
      hostLabel: ws.title,
      containerId: tab.containerId ?? null,
    };
  }
  return null;
}

function encode(context: SessionPanelContext | null): string {
  return context ? JSON.stringify(context) : "";
}

function decode(raw: string): SessionPanelContext | null {
  return raw ? (JSON.parse(raw) as SessionPanelContext) : null;
}

export interface ResolvedSessionPanelContext {
  context: SessionPanelContext | null;
  source: SessionPanelSource;
  // lockedGone is true when the locked terminal has closed; the panel stays locked until the user unlocks it.
  lockedGone: boolean;
}

// useSessionPanelContext follows the focused terminal. When another tab has focus the panel keeps the last terminal of
// the active workspace (so its directory stays in view), falls back to the active host workspace, and otherwise keeps
// the last terminal focused anywhere, so glancing at settings or a database does not blank it. A lock pins one context
// regardless of focus.
export function useSessionPanelContext(): ResolvedSessionPanelContext {
  const locked = useSessionPanel((s) => s.locked);
  const last = useSessionPanel((s) => s.last);
  const noteFocused = useSessionPanel((s) => s.noteFocused);
  // Selectors return strings so unrelated store updates do not re-render the panel.
  const focusedRaw = useUi((s) => encode(focusedTerminalContext(s)));
  const workspaceRaw = useUi((s) => encode(activeWorkspaceContext(s)));
  const activeWorkspaceId = useUi((s) => s.activeWorkspaceId);
  const lockedRaw = useUi((s) => (locked ? encode(refreshContext(locked, s.workspaces)) : ""));
  const lastRaw = useUi((s) => (last ? encode(refreshContext(last, s.workspaces)) : ""));

  useEffect(() => {
    const focused = decode(focusedRaw);
    if (focused) noteFocused(focused);
  }, [focusedRaw, noteFocused]);

  return useMemo<ResolvedSessionPanelContext>(() => {
    if (locked) {
      const context = decode(lockedRaw);
      return { context, source: "locked", lockedGone: context === null };
    }
    const focused = decode(focusedRaw);
    if (focused) return { context: focused, source: "focus", lockedGone: false };
    const remembered = decode(lastRaw);
    if (remembered && remembered.workspaceId === activeWorkspaceId) {
      return { context: remembered, source: "last", lockedGone: false };
    }
    const workspace = decode(workspaceRaw);
    if (workspace) return { context: workspace, source: "workspace", lockedGone: false };
    if (remembered) return { context: remembered, source: "last", lockedGone: false };
    return { context: null, source: "none", lockedGone: false };
  }, [locked, lockedRaw, focusedRaw, lastRaw, workspaceRaw, activeWorkspaceId]);
}
