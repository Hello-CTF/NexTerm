import { useEffect, useRef, useState } from "react";
import { fsApi } from "../../ipc/commands";
import { EVENTS, listenEvent, type FsChangedEvent, type FsWatchStatusEvent } from "../../ipc/events";

// The backend drops a watcher whose lease is not renewed within a minute.
const RENEW_MS = 20_000;

type Listener<T> = (event: T) => void;

function sharedListener<T>(topic: string) {
  const listeners = new Set<Listener<T>>();
  let unlisten: Promise<() => void> | null = null;
  return (listener: Listener<T>): (() => void) => {
    listeners.add(listener);
    if (!unlisten) {
      unlisten = Promise.resolve(listenEvent<T>(topic, (event) => listeners.forEach((each) => each(event))));
    }
    return () => {
      listeners.delete(listener);
      if (listeners.size === 0 && unlisten) {
        const pending = unlisten;
        unlisten = null;
        void pending.then((off) => off?.()).catch(() => undefined);
      }
    };
  };
}

export const subscribeFsChanged = sharedListener<FsChangedEvent>(EVENTS.fsChanged);
const subscribeFsWatchStatus = sharedListener<FsWatchStatusEvent>(EVENTS.fsWatch);

export interface FsWatchStatus {
  mode: string;
  channel: string;
  reason?: string;
}

// useFsWatch follows dirs of a session while enabled and reports how the backend follows them: pushed by the host
// or listed on a timer. The subscription renews its lease while mounted and ends on unmount.
export function useFsWatch(sessionId: string | null | undefined, dirs: readonly string[], enabled = true): FsWatchStatus | null {
  const watcherId = useRef<string>("");
  if (!watcherId.current) watcherId.current = `w-${crypto.randomUUID()}`;
  const [status, setStatus] = useState<FsWatchStatus | null>(null);
  const key = [...new Set(dirs)].sort().join("\u0000");

  useEffect(() => {
    if (!sessionId || !enabled) return;
    return subscribeFsWatchStatus((event) => {
      if (event.sessionId === sessionId) setStatus({ mode: event.mode, channel: event.channel, reason: event.reason });
    });
  }, [sessionId, enabled]);

  useEffect(() => {
    if (!sessionId || !enabled || !key || typeof fsApi.watch !== "function") return;
    const id = watcherId.current;
    const watched = key.split("\u0000");
    let stopped = false;
    const renew = () => {
      void fsApi.watch(sessionId, id, watched).then(
        (result) => {
          if (!stopped) setStatus({ mode: result.mode, channel: result.channel, reason: result.reason });
        },
        () => undefined,
      );
    };
    renew();
    const timer = window.setInterval(renew, RENEW_MS);
    return () => {
      stopped = true;
      window.clearInterval(timer);
      void fsApi.unwatch(sessionId, id).catch(() => undefined);
    };
  }, [sessionId, enabled, key]);

  return enabled ? status : null;
}
