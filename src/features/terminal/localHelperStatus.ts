import { useEffect, useSyncExternalStore } from "react";
import { localHelperApi, type LocalHelperStatus } from "../../ipc/commands";
import { EVENTS, listenEvent } from "../../ipc/events";
import { forgetLocalDurableReadiness } from "../../app/store";

// The local helper runs local terminals outside the app. Its status decides whether local terminals
// open durable and whether the background list asks the local helper for terminals.
let current: LocalHelperStatus | null = null;
let started = false;
const listeners = new Set<() => void>();

function publish(next: LocalHelperStatus | null): void {
  // A changed helper state makes cached local readiness answers stale.
  if (next?.state !== current?.state) forgetLocalDurableReadiness();
  current = next;
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function refreshLocalHelperStatus(): Promise<LocalHelperStatus | null> {
  return Promise.resolve()
    .then(() => localHelperApi.status())
    .then((status) => {
      publish(status);
      return status;
    })
    .catch(() => current);
}

export function ensureLocalHelperStatus(): void {
  if (started) return;
  started = true;
  void Promise.resolve()
    .then(() => listenEvent<LocalHelperStatus>(EVENTS.localHelperStatus, (status) => publish(status)))
    .catch(() => undefined);
  void refreshLocalHelperStatus();
}

export function getLocalHelperStatus(): LocalHelperStatus | null {
  return current;
}

// localDurableEnabled is true once the backend reported a local helper, whatever its state: its
// terminals may exist even while it is starting, outdated or failed, and listing them shows why.
export function localDurableEnabled(status: LocalHelperStatus | null = current): boolean {
  return status !== null && status.state !== "disabled";
}

export function useLocalHelperStatus(enabled = true): LocalHelperStatus | null {
  useEffect(() => {
    if (enabled) ensureLocalHelperStatus();
  }, [enabled]);
  return useSyncExternalStore(subscribe, () => current);
}

export function setLocalHelperStatusForTest(status: LocalHelperStatus | null): void {
  publish(status);
}
