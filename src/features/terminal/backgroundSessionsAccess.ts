import { useEffect, useLayoutEffect, useState, useSyncExternalStore } from "react";
import { terminalApi } from "../../ipc/commands";
import { useUi, type Workspace } from "../../app/store";

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

// useBackgroundSessionCount counts detached terminals for the workspace host; null means unknown.
export function useBackgroundSessionCount(workspace: Workspace | null | undefined, enabled: boolean): number | null {
  const sessionId = workspace?.kind === "session" ? workspace.sessionId : undefined;
  const status = useUi((state) => state.sessions.find((session) => session.id === sessionId)?.status);
  const kind = useUi((state) => state.sessions.find((session) => session.id === sessionId)?.kind);
  const runtimeEpoch = useUi((state) => state.workspaceRuntimeEpoch);
  const openTabIds = workspace?.panes.flatMap((pane) => pane.tabs)
    .filter((tab) => tab.kind === "terminal" && tab.tabId)
    .map((tab) => tab.tabId)
    .sort()
    .join("\n") ?? "";
  const [count, setCount] = useState<number | null>(null);

  useEffect(() => {
    setCount(null);
    if (!enabled || !sessionId || status !== "connected") return;
    let cancelled = false;
    const open = new Set(openTabIds.split("\n").filter(Boolean));
    void Promise.allSettled([
      Promise.resolve().then(() => terminalApi.listLive()),
      Promise.resolve().then(() => kind === "ssh" || kind === "device" ? terminalApi.listDurable(sessionId) : []),
    ]).then(([live, durable]) => {
      if (cancelled || (live.status === "rejected" && durable.status === "rejected")) return;
      const ids = new Set<string>();
      if (live.status === "fulfilled") {
        for (const tab of live.value) {
          if (tab.sessionId === sessionId && !tab.exited && tab.subscribers === 0 && !open.has(tab.tabId)) ids.add(tab.tabId);
        }
      }
      if (durable.status === "fulfilled") {
        for (const tab of durable.value) {
          if (!tab.dead && !open.has(tab.id)) ids.add(tab.id);
        }
      }
      setCount(ids.size);
    });
    return () => { cancelled = true; };
  }, [enabled, sessionId, status, kind, runtimeEpoch, openTabIds]);

  return count;
}
