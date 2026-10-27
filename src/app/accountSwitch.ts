import type { QueryClient } from "@tanstack/react-query";
import { WEB } from "../ipc/env";
import { useUi } from "./store";
import { advanceLayoutAccount, onRemoteChange } from "./layout";
import { resetTransferQueue } from "../features/files/transferStore";
import { cleanupDeviceSessions, reportDeviceSessionCleanup } from "../features/fleet/deviceSessionCleanup";
import { useAssetReachability } from "../features/explorer/assetReachability";
import { resetUnreadTracking } from "./unread";
import { resetAwarenessTracking } from "./sessionAwareness";

// Account-level query keys, aligned with the queryKeys of AssetTree/CredentialsSidebar/CredentialsPanel
// (assets/groups/credentials/snippets/search/vault status/forwards/inventory watch). Reset together on auth
// transitions: before login these requests all hit 401 (grouped assets hidden), and on account switch the
// previous session's cache must not leak into the next.
const ACCOUNT_QUERY_KEYS = [
  ["assets"],
  ["groups"],
  ["credentials"],
  ["snippets"],
  ["asset-search"],
  ["vault-status"],
  ["forwards"],
  ["forwardEnv"],
  ["exec-batch-history"],
  ["exec-batch-history-detail"],
  ["inventory-ssh-watch"],
] as const;

type AuthStore = typeof import("../features/auth/store").useAuth;

// With auth=on, when login/register/init brings the gate to ready, account-level requests issued while mounted behind the gate
// all hit 401; resetQueries cancels in-flight old fetches and refetches (invalidateQueries joins the old fetch when data is empty,
// and the 401 still lands). The reverse direction (logout/session expiry back to login) resets the same key set and clears the session
// list, SQL runtime, and vault state, so the previous session's data cannot leak into the next account. Toasts are not all cleared on ready:
// the 8s/3.5s auto-dismiss already bounds their lifetime, and terminal/file/AI and non-401 errors remain valid feedback after login.
export function subscribeAccountSwitchCleanup(auth: AuthStore, queryClient: QueryClient): () => void {
  return auth.subscribe((s, prev) => {
    const accountChanged = s.user?.id !== prev.user?.id;
    if (accountChanged) {
      // First-frame cleanup: bump the account epoch so keyed views (asset tree with its template,
      // runbook, inventory, and bulk panels) remount clean, and drop account-scoped caches before
      // the next account's data loads. A's in-flight runs stay canceled by the epoch guards above.
      useUi.getState().advanceAccountEpoch();
      useUi.getState().resetAiAccount();
      advanceLayoutAccount();
      if (prev.user) void cleanupDeviceSessions().then(reportDeviceSessionCleanup);
      useUi.getState().resetAccountWorkspaces();
      useAssetReachability.getState().reset();
      resetUnreadTracking();
      resetAwarenessTracking();
      if (s.gate === "ready") onRemoteChange(null);
      for (const key of ACCOUNT_QUERY_KEYS) void queryClient.resetQueries({ queryKey: key });
    }
    if (accountChanged || (prev.gate === "ready" && s.gate === "login")) resetTransferQueue();
    if (s.gate === prev.gate) return;
    if (s.gate === "ready" && prev.gate !== "loading") {
      onRemoteChange(null);
      if (!accountChanged) for (const key of ACCOUNT_QUERY_KEYS) void queryClient.resetQueries({ queryKey: key });
      void useUi.getState().resyncSessions({ silent: true });
      resetAwarenessTracking();
    } else if (prev.gate === "ready" && s.gate === "login") {
      if (!accountChanged) for (const key of ACCOUNT_QUERY_KEYS) void queryClient.resetQueries({ queryKey: key });
      if (!accountChanged) useUi.getState().resetSessions({ clearWorkspaceRuntime: true });
    }
  });
}

// The desktop shell keeps the auth store out of its module graph, so the wiring lazy-loads it;
// behavioral tests use subscribeAccountSwitchCleanup with an eagerly imported store instead.
export function wireAccountSwitchCleanup(queryClient: QueryClient): () => void {
  if (!WEB) return () => {};
  let cancelled = false;
  let unsubscribe: (() => void) | undefined;
  void import("../features/auth/store").then((m) => {
    if (cancelled) return;
    unsubscribe = subscribeAccountSwitchCleanup(m.useAuth, queryClient);
  });
  return () => {
    cancelled = true;
    unsubscribe?.();
  };
}
