import { useEffect } from "react";
import { type QueryClient } from "@tanstack/react-query";
import { useUi, connectAsset } from "../store";
import { layoutBootstrapped, layoutRestoreStatus, startLayoutSync } from "../layout";
import { wireAccountSwitchCleanup } from "../accountSwitch";
import { startUnreadTracking } from "../unread";
import { startAwarenessTracking } from "../sessionAwareness";
import { startInboxNotifications } from "../../features/inbox/inboxNotifications";
import { getUpdateState } from "../useUpdate";
import { promptUpdateIfNeeded } from "../updatePrompt";
import { assetApi } from "../../ipc/commands";
import { listenEvent } from "../../ipc/events";
import { onAssetDataChanged } from "../../ipc/assetEvents";
import { TRANSPORT, WEB } from "../../ipc/env";
import { registerPromptHandler, registerDialogHandlers, registerDialogEpochSource, type ChoiceResult } from "../../ui/dialogs";

export function dialogLevelForKind(kind?: string): "info" | "warning" {
  return kind === "info" ? "info" : "warning";
}

// useWebAuthRefresh refreshes the browser account state once the shell mounts.
export function useWebAuthRefresh(): void {
  useEffect(() => {
    if (WEB) {
      void import("../../features/auth/store").then((m) => m.useAuth.getState().refresh());
    }
  }, []);
}

// useAppDialogHandlers routes the ui/dialogs prompt, ask, confirm, message and choose calls to the in-app dialog host.
export function useAppDialogHandlers(): void {
  useEffect(() => {
    registerPromptHandler((message, value, options) => {
      const { openTextPrompt } = useUi.getState();
      return new Promise<string | null>((resolve) => {
        openTextPrompt({ message, value, ...options, resolve });
      });
    });
    registerDialogEpochSource(() => useUi.getState().accountEpoch);
  }, []);

  useEffect(() => {
    registerDialogHandlers({
      ask: (message, options) =>
        new Promise<boolean>((resolve) => {
          useUi.getState().openAppDialog({
            kind: "ask",
            message,
            title: options?.title,
            level: dialogLevelForKind(options?.kind),
            resolve,
          });
        }),
      confirm: (message) =>
        new Promise<boolean>((resolve) => {
          useUi.getState().openAppDialog({
            kind: "confirm",
            message,
            level: "warning",
            resolve,
          });
        }),
      message: (message) =>
        new Promise<void>((resolve) => {
          useUi
            .getState()
            .openAppDialog({ kind: "message", message, level: "info", resolve: () => resolve() });
        }),
      choose: (message, options) =>
        new Promise<ChoiceResult | null>((resolve) => {
          useUi.getState().openAppChoice({
            title: options?.title ?? "请选择",
            message,
            options: options?.choices ?? [],
            level: options?.level ?? "info",
            rememberLabel: options?.rememberLabel,
            resolve: (key, remember) => resolve(key === null ? null : { key, remember }),
          });
        }),
    });
  }, []);
}

let bootLocalTried = false;

// useAppBootEffects starts the app-lifetime background work: layout sync, unread and attention tracking, update checks,
// session list refreshes, account-switch cleanup and the first-run local terminal.
export function useAppBootEffects(queryClient: QueryClient): void {
  useEffect(() => {
    startLayoutSync();
    startUnreadTracking();
    startAwarenessTracking();
    startInboxNotifications();
  }, []);

  // Check silently at desktop startup and every four hours: no toast; UpdateBanner presents any update.
  // After each check completes, prompt once per version with dedup gating (see updatePrompt).
  useEffect(() => {
    if (TRANSPORT !== "desktop") return;
    const check = () => {
      void getUpdateState()
        .check()
        .then(() => promptUpdateIfNeeded());
    };
    check();
    const interval = window.setInterval(check, 4 * 60 * 60 * 1000);
    return () => window.clearInterval(interval);
  }, []);

  useEffect(() => {
    void useUi.getState().resyncSessions({ silent: true });
  }, []);

  useEffect(() => {
    let disposed = false;
    let inFlight = false;
    let queued = false;
    const refresh = () => {
      if (disposed) return;
      if (inFlight) {
        queued = true;
        return;
      }
      inFlight = true;
      const finish = () => {
        inFlight = false;
        if (!queued || disposed) return;
        queued = false;
        refresh();
      };
      void useUi.getState().resyncSessions({ silent: true }).then(finish, finish);
    };
    const offAssetData = onAssetDataChanged(refresh);
    const subscriptions: (() => void)[] = [];
    // Only a sync that applied remote changes can alter asset-backed sessions; plain status and round events cannot.
    const refreshIfApplied = (payload?: { applied?: boolean }) => {
      if (payload?.applied === true) refresh();
    };
    for (const topic of ["sync://status"]) {
      try {
        void Promise.resolve(listenEvent<{ applied?: boolean } | undefined>(topic, refreshIfApplied)).then((off) => {
          if (typeof off !== "function") return;
          if (disposed) off();
          else subscriptions.push(off);
        }).catch((error: unknown) => {
          console.warn("[NexTerm] Failed to subscribe asset session refresh", error);
        });
      } catch (error) {
        console.warn("[NexTerm] Failed to subscribe asset session refresh", error);
      }
    }
    return () => {
      disposed = true;
      queued = false;
      offAssetData();
      for (const off of subscriptions) off();
    };
  }, []);

  // With auth=on, when login/register/init brings the gate to ready, account-level requests issued while mounted behind the gate
  // all hit 401; the wiring resets those query keys and clears account-scoped caches on switches. Toasts are not all cleared on ready:
  // the 8s/3.5s auto-dismiss already bounds their lifetime, and terminal/file/AI and non-401 errors remain valid feedback after login.
  // Account-level auth-transition cleanup lives in ../accountSwitch so it can be exercised without mounting the shell.
  useEffect(() => wireAccountSwitchCleanup(queryClient), [queryClient]);

  useEffect(() => {
    if (bootLocalTried) return;
    bootLocalTried = true;
    void (async () => {
      try {
        await layoutBootstrapped;
        if (layoutRestoreStatus().pending || layoutRestoreStatus().preview) return;
        const list = await assetApi.list();
        if (list.some((a) => !a.builtin)) return;
        const builtin = list.find((a) => a.builtin && a.kind === "local");
        if (!builtin) return;
        if (useUi.getState().workspaces.length > 0) return;
        await connectAsset(builtin, { preserveUserFocus: true });
      } catch {
      }
    })();
  }, []);
}
