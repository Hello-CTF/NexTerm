import { useLayoutEffect, useSyncExternalStore } from "react";
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
  if (observed || retainable || tabs.some((tab) => tab.kind === "background")) return true;
  const terminals = tabs.filter((tab) => tab.kind === "terminal");
  // An unprobed host must retain an explicit path to inspect older durable sessions.
  return (kind === "ssh" || kind === "device") &&
    (terminals.length === 0 || terminals.some((tab) => tab.retainable !== false));
}
