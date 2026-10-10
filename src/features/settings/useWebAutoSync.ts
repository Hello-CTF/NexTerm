import { useEffect } from "react";
import { syncApi } from "../../ipc/commands";
import { listenEvent } from "../../ipc/events";
import { WEB } from "../../ipc/env";
import { useAuth } from "../auth/store";
import { useUi } from "../../app/store";
import { createHttpPreferenceStore, registerAccountPreferenceStore } from "../../app/preferences";
import { runWebSync, WEB_SYNC_ERROR_EVENT, WEB_SYNC_REQUEST_EVENT, WEB_SYNC_RESULT_EVENT } from "./SyncCard";

const SYNC_PERIOD_MS = 60_000;

export function useWebAutoSync() {
  const userId = useAuth((s) => s.user?.id);
  const dek = useAuth((s) => s.dek);

  useEffect(() => {
    if (!WEB || !userId || !dek) return;
    let cancelled = false;
    let running = false;
    let queued = false;
    let unlisten: (() => void) | undefined;

    const run = async () => {
      if (running) {
        queued = true;
        return;
      }
      running = true;
      try {
        const optIn = await syncApi.kindOptInGet();
        if (cancelled) return;
        const result = await runWebSync(dek, optIn, () => !cancelled);
        if (result && !cancelled) {
          if (result.applied + result.pushed > 0) {
            useUi.getState().bumpModelProfilesRevision();
            registerAccountPreferenceStore(createHttpPreferenceStore());
          }
          window.dispatchEvent(new CustomEvent(WEB_SYNC_RESULT_EVENT, { detail: result }));
        }
      } catch (error) {
        queued = false;
        if (!cancelled) {
          window.dispatchEvent(new CustomEvent(WEB_SYNC_ERROR_EVENT, { detail: error }));
        }
      } finally {
        running = false;
        if (queued && !cancelled) {
          queued = false;
          queueMicrotask(run);
        }
      }
    };

    const request = () => void run();
    const timer = window.setInterval(request, SYNC_PERIOD_MS);
    window.addEventListener(WEB_SYNC_REQUEST_EVENT, request);
    void listenEvent("sync://status", request).then((off) => {
      if (cancelled) off();
      else unlisten = off;
    });
    request();
    return () => {
      cancelled = true;
      window.clearInterval(timer);
      window.removeEventListener(WEB_SYNC_REQUEST_EVENT, request);
      unlisten?.();
    };
  }, [dek, userId]);
}

export function useDesktopAutoSync() {
  useEffect(() => {
    if (WEB) return;
    let disposed = false;
    let unlisten: (() => void) | undefined;
    const wake = () => {
      if (disposed) return;
      useUi.getState().bumpModelProfilesRevision();
      registerAccountPreferenceStore(createHttpPreferenceStore());
      void syncApi.wake().catch(() => undefined);
    };
    void listenEvent("sync://status", wake).then((off) => {
      if (disposed) off();
      else unlisten = off;
    });
    return () => {
      disposed = true;
      unlisten?.();
    };
  }, []);
}
