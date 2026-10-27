import { useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";
import { sessionApi, terminalApi, type LiveTabInfo, type SessionAttentionState, type SessionInfo } from "../../ipc/commands";
import type { AggregateHost } from "../../ipc/generated/types";
import { connectDevice, deviceAssetId, nextTabId, useUi, type Workspace } from "../../app/store";
import type { AggregateRow } from "../../app/sessionAggregate";
import { describeError } from "../../ui/errorText";
import { getLocalHelperStatus, localDurableEnabled, useLocalHelperStatus } from "./localHelperStatus";

const COUNT_REFRESH_MS = 10_000;

export type BackgroundTab = Omit<LiveTabInfo, "lastOutputMsAgo"> & {
  lastOutputMsAgo?: number;
  remote?: boolean;
  // The host's helper predates activity reporting, so output and detach times are unknown.
  activityUnknown?: boolean;
  // A client is attached to the helper session right now, so it has no detach time.
  attachedElsewhere?: boolean;
  detachedMsAgo?: number;
  // When this device received the listing; ages are relative to it.
  fetchedAt?: number;
  // The times come from the host's clock without ages, so they are shown as host time.
  hostClock?: boolean;
  // createdAt is the helper's creation time, on the host's clock.
  createdOnHost?: boolean;
  // The row comes from the cross-host listing: its host has no session here that was listed directly, such as a
  // device reached through the server. Taking it over goes through this host.
  aggregateHost?: AggregateHost;
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
  // localDurable is set when local terminals run in the local helper, so they are listed from it.
  localDurable?: boolean;
}

// backgroundTargets lists the connected SSH/device sessions (and local sessions when a local helper runs)
// whose durable terminals belong to the selected host.
export function backgroundTargets(
  scope: BackgroundScope,
  opts: { showAll: boolean; hostSessionId: string | null; owner?: Workspace; remoteOwner: boolean },
): SessionInfo[] {
  return scope.sessions.filter((session) =>
    (opts.showAll || session.id === opts.hostSessionId) &&
    (!opts.remoteOwner || opts.showAll || (opts.owner && workspaceSessionMatches(opts.owner, session, scope.verified))) &&
    session.status === "connected" &&
    (session.kind === "ssh" || session.kind === "device" || (scope.localDurable === true && session.kind === "local")) &&
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
    // fetchedAt is when that listing arrived on this device's clock; ages in it are relative to it.
    remote: { session: SessionInfo; tabs: DurableList | null; reason?: unknown; fetchedAt?: number }[];
    openTabIds: Set<string>;
    // When the live listing arrived on this device's clock; defaults to now.
    liveFetchedAt?: number;
    // The cross-host listing; it adds the sessions of hosts not listed directly above.
    aggregate?: readonly AggregateRow[];
  },
): CollectedBackgroundTabs {
  const collectedAt = Date.now();
  const liveFetchedAt = input.liveFetchedAt ?? collectedAt;
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
  // A durable tab this app still holds is listed live; the helper's listing adds what only it knows.
  const liveRows = new Map<string, BackgroundTab>();
  for (const tab of input.live ?? []) {
    if (tab.exited || input.openTabIds.has(tab.tabId)) continue;
    const key = keyForTab(tab.sessionId, tab.tabId);
    if (known.has(key)) continue;
    known.add(key);
    if (tab.subscribers === 0) {
      const row: BackgroundTab = { ...tab, fetchedAt: liveFetchedAt };
      // The backend leaves a zero age out; the live listing always sends one with a detach time.
      if (row.detachedAt) row.detachedMsAgo ??= 0;
      liveRows.set(key, row);
      list.push(row);
    }
  }
  for (const { session, tabs, reason, fetchedAt = collectedAt } of input.remote) {
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
      const live = liveRows.get(key);
      if (live) {
        live.state ??= tab.state;
        // The helper created the session; this app may only have reattached to it.
        if (tab.createdAt > 0) {
          live.createdAt = tab.createdAt;
          live.createdOnHost = true;
        }
      }
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
        // A durable descriptor title is the canonical custom title.
        titleCustom: tab.title ? true : undefined,
        remote: true,
        durable: true,
        createdAt: tab.createdAt > 0 ? tab.createdAt : undefined,
        createdOnHost: true,
        state: tab.state,
        fetchedAt,
        ...(tab.activity
          ? {
              // Zero means no output yet; a null detach time means a client is attached.
              lastOutputAt: tab.activity.lastOutputAt ?? 0,
              lastOutputMsAgo: tab.activity.lastOutputMsAgo,
              detachedAt: tab.activity.detachedAt ?? undefined,
              detachedMsAgo: tab.activity.detachedMsAgo,
              hostClock: tab.activity.asOf === undefined || undefined,
              attachedElsewhere: tab.activity.attached || undefined,
            }
          : { activityUnknown: true }),
      });
    }
  }
  // A host listed directly above, or a tab this app holds, is already covered; the cross-host listing adds the rest,
  // such as a device reached only through the server.
  const listedSessions = new Set(input.remote.map(({ session }) => session.id));
  const coveredTabs = new Set([...list.map((tab) => tab.tabId), ...(input.live ?? []).map((tab) => tab.tabId)]);
  for (const { session: item, host } of input.aggregate ?? []) {
    if (item.dead || input.openTabIds.has(item.tabId) || coveredTabs.has(item.tabId)) continue;
    if (host.sessionId && listedSessions.has(host.sessionId)) continue;
    coveredTabs.add(item.tabId);
    list.push({
      tabId: item.tabId,
      sessionId: host.sessionId ?? "",
      sessionName: host.name,
      sessionKind: host.kind,
      cols: item.cols,
      rows: item.rows,
      controller: null,
      subscribers: 0,
      viewers: 0,
      exited: false,
      title: item.title,
      titleCustom: item.title ? true : undefined,
      remote: true,
      durable: true,
      createdAt: item.createdAt || undefined,
      createdOnHost: true,
      state: ATTENTION_STATES.has(item.attention.state) ? (item.attention.state as SessionAttentionState) : undefined,
      fetchedAt: host.fetchedAt || collectedAt,
      lastOutputAt: item.lastOutputAt ?? 0,
      detachedAt: item.detachedAt || undefined,
      hostClock: true,
      attachedElsewhere: item.attached || undefined,
      aggregateHost: host,
    });
  }
  return { list, knownTabsByHost, incompleteHosts, failedRemote };
}

const ATTENTION_STATES: ReadonlySet<string> = new Set(["running", "needs_me", "just_finished", "idle", "exited"]);

export interface BackgroundListing {
  collected: CollectedBackgroundTabs;
  // liveFailed is set when this app's own live listing failed; liveError is its reason.
  liveFailed: boolean;
  liveError?: unknown;
}

// listBackgroundTabs fetches the live listing and the durable listing of every target host, then collects the
// takeable terminals. The page, the activity-bar badge and the jump palette all list through it.
export async function listBackgroundTabs(
  scope: BackgroundScope,
  targets: SessionInfo[],
  openTabIds: Set<string>,
  // The cross-host listing, for the all-hosts view: hosts without a target here still show their sessions.
  aggregate?: readonly AggregateRow[],
): Promise<BackgroundListing> {
  // Each listing keeps its own arrival time: its ages count from when it answered, not from the slowest one.
  // Requests start synchronously; one that throws counts as a failed listing.
  const received = <T,>(request: () => Promise<T>) => {
    try {
      return request().then((value) => ({ value, at: Date.now() }));
    } catch (error) {
      return Promise.reject(error);
    }
  };
  const [live, ...remote] = await Promise.allSettled([
    received(() => terminalApi.listLive()),
    ...targets.map((session) => received(() => terminalApi.listDurable(session.id))),
  ]);
  const collected = collectBackgroundTabs(scope, {
    live: live.status === "fulfilled" ? (live.value.value as LiveTabInfo[]) : null,
    liveFetchedAt: live.status === "fulfilled" ? live.value.at : undefined,
    remote: targets.map((session, index) => {
      const result = remote[index];
      return {
        session,
        tabs: result.status === "fulfilled" ? (result.value.value as DurableList) : null,
        reason: result.status === "rejected" ? result.reason : undefined,
        fetchedAt: result.status === "fulfilled" ? result.value.at : undefined,
      };
    }),
    openTabIds,
    aggregate,
  });
  return live.status === "rejected"
    ? { collected, liveFailed: true, liveError: live.reason }
    : { collected, liveFailed: false };
}

export type TakeOverResult =
  | { ok: true; sessionName: string }
  | { ok: false; canceled: true }
  | { ok: false; error: string };

// takeOverBackgroundTab opens a background terminal as a tab of its host workspace. guard may throw to refuse a
// host that no longer belongs to the caller; isCurrent lets the caller drop a result it no longer wants. A
// workspace runtime reset during the session refresh always cancels.
export async function takeOverBackgroundTab(
  info: BackgroundTab,
  opts: { guard?: (session: SessionInfo) => void; isCurrent?: () => boolean } = {},
): Promise<TakeOverResult> {
  const epoch = useUi.getState().workspaceRuntimeEpoch;
  const current = () => useUi.getState().workspaceRuntimeEpoch === epoch && (opts.isCurrent?.() ?? true);
  try {
    const list = await sessionApi.list();
    if (!current()) return { ok: false, canceled: true };
    const session = list.find((item) => item.id === info.sessionId);
    if (!session || session.status !== "connected") {
      throw new Error("主机未连接，请先重新连接再接管");
    }
    opts.guard?.(session);
    const st = useUi.getState();
    st.setSessions(list);
    st.addTab({
      id: nextTabId(`term-${info.sessionId}`),
      kind: "terminal",
      title: info.title || session.name || info.sessionName,
      // Only the backend's own decision time may lock the title; a listing without one
      // (a durable descriptor) leaves it unlocked until the attach reports the time.
      ...(info.title && info.titleCustom && info.titleChangedAt
        ? { renamedByUser: true, customTitleAt: info.titleChangedAt }
        : {}),
      sessionId: info.sessionId,
      tabId: info.tabId,
      closable: true,
    });
    return { ok: true, sessionName: session.name };
  } catch (e) {
    if (!current()) return { ok: false, canceled: true };
    return { ok: false, error: describeError(e) };
  }
}

// takeOverAggregateSession opens a session known from the cross-host listing as a tab: through its host's connected
// session, or, for a device no workspace here holds, after opening the device's workspace.
export async function takeOverAggregateSession(host: AggregateHost, tabId: string, title?: string): Promise<TakeOverResult> {
  const connected = () => {
    const sessions = useUi.getState().sessions;
    const byId = host.sessionId ? sessions.find((item) => item.id === host.sessionId) : undefined;
    if (byId?.status === "connected") return byId;
    const assetId = host.deviceId ? deviceAssetId(host.deviceId) : undefined;
    return assetId ? sessions.find((item) => item.assetId === assetId && item.status === "connected") : undefined;
  };
  let session = connected();
  if (!session && host.deviceId) {
    const outcome = await connectDevice({ id: host.deviceId, name: host.name });
    if (!outcome.ok) return outcome.canceled ? { ok: false, canceled: true } : { ok: false, error: outcome.error ?? "无法连接设备" };
    session = connected();
  }
  if (!session) return { ok: false, error: "主机未连接，请先从资产树打开这台主机" };
  return takeOverBackgroundTab({
    tabId, sessionId: session.id, sessionName: session.name, sessionKind: session.kind,
    cols: 0, rows: 0, controller: null, subscribers: 0, viewers: 0, exited: false, title,
  });
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
    // The cross-host listing only names hosts of this account.
    if (tab.aggregateHost) return true;
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
  const localHelper = useLocalHelperStatus(kind === "local");
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
  // Local terminals of an earlier app run may still be running in the local helper.
  if (kind === "local" && localDurableEnabled(localHelper)) return true;
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
      const scope: BackgroundScope = {
        sessions: st.sessions, workspaces: st.workspaces, verified: {}, localDurable: localDurableEnabled(getLocalHelperStatus()),
      };
      const gate = resolveHostGate(owner, hostSession, scope.verified);
      if (gate.needsReconnect) {
        inFlight = false;
        return;
      }
      const targets = backgroundTargets(scope, { showAll: false, hostSessionId: sessionId, owner, remoteOwner: gate.remoteOwner });
      void listBackgroundTabs(scope, targets, new Set(openTabIds.split("\n").filter(Boolean))).then(({ collected, liveFailed }) => {
        inFlight = false;
        // Nothing answered, so the count is unknown rather than zero.
        if (cancelled || (liveFailed && collected.failedRemote.length === targets.length)) return;
        setCount(visibleBackgroundTabs(collected.list, scope, { hostSessionId: sessionId, showAll: false, blocked: false }).length);
      });
    };
    refresh();
    const timer = window.setInterval(refresh, COUNT_REFRESH_MS);
    return () => { cancelled = true; window.clearInterval(timer); };
  }, [enabled, sessionId, status, kind, assetId, runtimeEpoch, openTabIds]);

  return count;
}
