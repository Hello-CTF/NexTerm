import { useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";
import { terminalApi, type LiveTabInfo, type SessionInfo } from "../../ipc/commands";
import { useUi, type Workspace } from "../../app/store";

const COUNT_REFRESH_MS = 10_000;

export type BackgroundTab = Omit<LiveTabInfo, "lastOutputMsAgo"> & {
  lastOutputMsAgo?: number;
  remote?: boolean;
};

export interface KnownDurableTab {
  id: string;
  title?: string;
}

type DurableList = Awaited<ReturnType<typeof terminalApi.listDurable>>;
type VerifiedQuickSessions = Record<string, string>;

export function durableHostKey(kind: string, assetId?: string, quickTargetId?: string): string | null {
  if (kind !== "ssh" && kind !== "device") return null;
  const target = assetId || (kind === "ssh" ? quickTargetId : undefined);
  return target ? JSON.stringify([kind, target]) : null;
}

export function workspaceHostKey(workspace: Workspace): string | null {
  return durableHostKey(workspace.assetKind ?? "", workspace.assetId, workspace.quickTarget?.id);
}

export function workspaceSessionMatches(
  workspace: Workspace,
  session: { id: string; assetId: string | null; kind: string },
  verifiedQuickSessions: VerifiedQuickSessions,
): boolean {
  if (session.kind !== workspace.assetKind) return false;
  if (workspace.assetId) return session.assetId === workspace.assetId;
  const key = workspaceHostKey(workspace);
  return Boolean(key && workspace.quickTarget && !session.assetId &&
    (workspace.sessionId === session.id || verifiedQuickSessions[key] === session.id));
}

export function backgroundTabKey(tab: Pick<BackgroundTab, "sessionId" | "tabId">): string {
  return JSON.stringify([tab.sessionId, tab.tabId]);
}

export interface HostGate {
  remoteOwner: boolean;
  identityMismatch: boolean;
  needsReconnect: boolean;
}

// resolveHostGate decides whether the owning workspace can currently be queried for background terminals.
export function resolveHostGate(
  owner: Workspace | undefined,
  hostSession: SessionInfo | undefined,
  verified: VerifiedQuickSessions,
): HostGate {
  const remoteOwner =
    owner?.kind === "session" &&
    (owner.assetKind === "ssh" || owner.assetKind === "device") &&
    Boolean(owner.assetId || owner.quickTarget);
  const identityMismatch = remoteOwner && Boolean(owner && hostSession && !workspaceSessionMatches(owner, hostSession, verified));
  const needsReconnect = remoteOwner && (!hostSession || identityMismatch || hostSession.status !== "connected");
  return { remoteOwner, identityMismatch, needsReconnect };
}

export interface BackgroundScope {
  sessions: SessionInfo[];
  workspaces: Workspace[];
  verified: VerifiedQuickSessions;
}

// backgroundTargets lists the connected SSH/device sessions whose durable terminals belong to the selected host.
export function backgroundTargets(
  scope: BackgroundScope,
  opts: { showAll: boolean; hostSessionId: string | null; owner?: Workspace; remoteOwner: boolean },
): SessionInfo[] {
  return scope.sessions.filter((session) =>
    (opts.showAll || session.id === opts.hostSessionId) &&
    (!opts.remoteOwner || opts.showAll || (opts.owner && workspaceSessionMatches(opts.owner, session, scope.verified))) &&
    session.status === "connected" &&
    (session.kind === "ssh" || session.kind === "device") &&
    (Boolean(session.assetId) || scope.workspaces.some((workspace) =>
      workspace.kind === "session" && workspace.sessionId === session.id &&
      workspaceSessionMatches(workspace, session, scope.verified))),
  );
}

export function hostKeyForSession(scope: BackgroundScope, session: SessionInfo): string | null {
  const matching = scope.workspaces.find((workspace) =>
    workspace.kind === "session" && workspace.sessionId === session.id && workspaceSessionMatches(workspace, session, scope.verified));
  return matching ? workspaceHostKey(matching) : durableHostKey(session.kind, session.assetId ?? undefined);
}

export function openTerminalTabIds(workspaces: Workspace[]): Set<string> {
  const ids = new Set<string>();
  for (const workspace of workspaces) {
    for (const pane of workspace.panes) {
      for (const tab of pane.tabs) {
        if (tab.kind === "terminal" && tab.tabId) ids.add(tab.tabId);
      }
    }
  }
  return ids;
}

export interface CollectedBackgroundTabs {
  list: BackgroundTab[];
  knownTabsByHost: Map<string, Map<string, KnownDurableTab>>;
  incompleteHosts: Set<string>;
  failedRemote: { session: SessionInfo; reason: unknown }[];
}

// collectBackgroundTabs is the single place that decides which terminals are takeable. Both the page and the
// activity-bar badge go through it, so their numbers cannot drift apart.
export function collectBackgroundTabs(
  scope: BackgroundScope,
  input: {
    live: LiveTabInfo[] | null;
    remote: { session: SessionInfo; tabs: DurableList | null; reason?: unknown }[];
    openTabIds: Set<string>;
  },
): CollectedBackgroundTabs {
  const list: BackgroundTab[] = [];
  const known = new Set<string>();
  const knownTabsByHost = new Map<string, Map<string, KnownDurableTab>>();
  const incompleteHosts = new Set<string>();
  const failedRemote: { session: SessionInfo; reason: unknown }[] = [];
  const keyForTab = (sessionId: string, tabId: string) => {
    const session = scope.sessions.find((item) => item.id === sessionId && item.status === "connected");
    const hostKey = session ? hostKeyForSession(scope, session) : null;
    return hostKey ? JSON.stringify([hostKey, tabId]) : backgroundTabKey({ sessionId, tabId });
  };
  for (const tab of input.live ?? []) {
    if (tab.exited || input.openTabIds.has(tab.tabId)) continue;
    const key = keyForTab(tab.sessionId, tab.tabId);
    if (known.has(key)) continue;
    known.add(key);
    if (tab.subscribers === 0) list.push(tab);
  }
  for (const { session, tabs, reason } of input.remote) {
    const hostKey = hostKeyForSession(scope, session);
    if (tabs === null) {
      if (hostKey) incompleteHosts.add(hostKey);
      failedRemote.push({ session, reason });
      continue;
    }
    if (hostKey) {
      const knownTabs = knownTabsByHost.get(hostKey) ?? new Map<string, KnownDurableTab>();
      for (const tab of tabs) {
        if (!tab.dead) knownTabs.set(tab.id, { id: tab.id, title: tab.title });
      }
      knownTabsByHost.set(hostKey, knownTabs);
    }
    for (const tab of tabs) {
      if (tab.dead || input.openTabIds.has(tab.id)) continue;
      const key = hostKey ? JSON.stringify([hostKey, tab.id]) : backgroundTabKey({ sessionId: session.id, tabId: tab.id });
      if (known.has(key)) continue;
      known.add(key);
      list.push({
        tabId: tab.id,
        sessionId: session.id,
        sessionName: session.name,
        sessionKind: session.kind,
        cols: tab.cols,
        rows: tab.rows,
        controller: null,
        subscribers: 0,
        viewers: 0,
        exited: false,
        title: tab.title,
        remote: true,
      });
    }
  }
  return { list, knownTabsByHost, incompleteHosts, failedRemote };
}

// visibleBackgroundTabs narrows collected terminals to the selected host (or, for the all-hosts view, to
// every reachable host). Local terminals only appear when the local workspace itself is selected.
export function visibleBackgroundTabs(
  all: BackgroundTab[],
  scope: BackgroundScope,
  opts: { hostSessionId: string | null; showAll: boolean; blocked: boolean },
): BackgroundTab[] {
  if (opts.hostSessionId && !opts.blocked) return all.filter((t) => t.sessionId === opts.hostSessionId);
  if (!opts.showAll) return [];
  return all.filter((tab) => {
    if (tab.sessionKind !== "ssh" && tab.sessionKind !== "device") return true;
    const session = scope.sessions.find((candidate) => candidate.id === tab.sessionId);
    if (!session || session.status !== "connected") return false;
    return Boolean(session.assetId || scope.workspaces.some((workspace) =>
      workspace.sessionId === session.id && workspaceSessionMatches(workspace, session, scope.verified)));
  });
}


const observedRetainableSessions = new Set<string>();
const listeners = new Set<() => void>();

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}

export function useBackgroundSessionsAccess(workspace: Workspace | null | undefined): boolean {
  const accountEpoch = useUi((state) => state.accountEpoch);
  const runtimeEpoch = useUi((state) => state.workspaceRuntimeEpoch);
  const kind = useUi((state) => state.sessions.find((session) => session.id === workspace?.sessionId)?.kind) ?? workspace?.assetKind;
  const key = `${accountEpoch}:${runtimeEpoch}:${workspace?.sessionId ?? ""}`;
  const observed = useSyncExternalStore(subscribe, () => observedRetainableSessions.has(key));
  const tabs = workspace?.panes.flatMap((pane) => pane.tabs) ?? [];
  const retainable = tabs.some((tab) => tab.kind === "terminal" && tab.retainable === true);

  useLayoutEffect(() => {
    if (!workspace?.sessionId || !retainable || observedRetainableSessions.has(key)) return;
    observedRetainableSessions.add(key);
    for (const listener of listeners) listener();
  }, [key, retainable, workspace?.sessionId]);

  if (workspace?.kind !== "session" || !workspace.sessionId) return false;
  // Hosts that support durable terminals keep the entry even when every open tab is regular:
  // durable sessions created earlier may still be running there.
  if (kind === "ssh" || kind === "device") return true;
  return observed || retainable || tabs.some((tab) => tab.kind === "background");
}

// useBackgroundSessionCount counts takeable background terminals of the workspace host with the same rules the
// page uses; null means unknown.
export function useBackgroundSessionCount(workspace: Workspace | null | undefined, enabled: boolean): number | null {
  const sessionId = workspace?.kind === "session" ? workspace.sessionId : undefined;
  const status = useUi((state) => state.sessions.find((session) => session.id === sessionId)?.status);
  const kind = useUi((state) => state.sessions.find((session) => session.id === sessionId)?.kind);
  const assetId = useUi((state) => state.sessions.find((session) => session.id === sessionId)?.assetId);
  const runtimeEpoch = useUi((state) => state.workspaceRuntimeEpoch);
  const openTabIds = workspace?.panes.flatMap((pane) => pane.tabs)
    .filter((tab) => tab.kind === "terminal" && tab.tabId)
    .map((tab) => tab.tabId)
    .sort()
    .join("\n") ?? "";
  const workspaceRef = useRef(workspace);
  workspaceRef.current = workspace;
  const [count, setCount] = useState<number | null>(null);

  useEffect(() => {
    setCount(null);
    if (!enabled || !sessionId || status !== "connected") return;
    let cancelled = false;
    let inFlight = false;
    const refresh = () => {
      if (inFlight) return;
      inFlight = true;
      const st = useUi.getState();
      const owner = workspaceRef.current ?? undefined;
      const hostSession = st.sessions.find((session) => session.id === sessionId);
      const scope: BackgroundScope = { sessions: st.sessions, workspaces: st.workspaces, verified: {} };
      const gate = resolveHostGate(owner, hostSession, scope.verified);
      if (gate.needsReconnect) {
        inFlight = false;
        return;
      }
      const targets = backgroundTargets(scope, { showAll: false, hostSessionId: sessionId, owner, remoteOwner: gate.remoteOwner });
      void Promise.allSettled([
        Promise.resolve().then(() => terminalApi.listLive()),
        ...targets.map((session) => Promise.resolve().then(() => terminalApi.listDurable(session.id))),
      ]).then(([live, ...remote]) => {
        inFlight = false;
        if (cancelled || (live.status === "rejected" && remote.every((r) => r.status === "rejected") && remote.length > 0)) return;
        if (live.status === "rejected" && remote.length === 0) return;
        const collected = collectBackgroundTabs(scope, {
          live: live.status === "fulfilled" ? live.value : null,
          remote: targets.map((session, index) => {
            const result = remote[index];
            return { session, tabs: result.status === "fulfilled" ? result.value : null, reason: result.status === "rejected" ? result.reason : undefined };
          }),
          openTabIds: new Set(openTabIds.split("\n").filter(Boolean)),
        });
        setCount(visibleBackgroundTabs(collected.list, scope, { hostSessionId: sessionId, showAll: false, blocked: false }).length);
      });
    };
    refresh();
    const timer = window.setInterval(refresh, COUNT_REFRESH_MS);
    return () => { cancelled = true; window.clearInterval(timer); };
  }, [enabled, sessionId, status, kind, assetId, runtimeEpoch, openTabIds]);

  return count;
}
