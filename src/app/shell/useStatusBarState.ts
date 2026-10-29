import { useCallback, useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useUi } from "../store";
import { syncApi, vaultApi } from "../../ipc/commands";
import { WEB } from "../../ipc/env";
import { describeError } from "../../ui/errorText";
import { formatTime } from "../../ui/format";
import { trackWebSyncRequests, WEB_SYNC_ERROR_EVENT, WEB_SYNC_RESULT_EVENT } from "../syncEvents";

export interface VaultStatusSummary {
  // attention is the only reason the status bar shows the vault: nothing is rendered for a healthy or loading vault.
  attention: "uninitialized" | "locked" | "unknown" | null;
  uninitialized: boolean;
  passwordless: boolean;
  text: string;
}

// useVaultStatus subscribes directly to the shared ["vault-status"] query, same source as the settings page;
// no forked copy is maintained via local state + sequence guards.
export function useVaultStatus() {
  const queryClient = useQueryClient();
  const pushToast = useUi((s) => s.pushToast);
  const vaultQuery = useQuery({ queryKey: ["vault-status"], queryFn: () => vaultApi.status() });
  const vaultStatus: VaultStatusSummary = vaultQuery.data
    ? {
        attention: !vaultQuery.data.initialized
          ? "uninitialized"
          : !vaultQuery.data.passwordless && !vaultQuery.data.unlocked
            ? "locked"
            : null,
        uninitialized: !vaultQuery.data.initialized,
        passwordless: vaultQuery.data.passwordless,
        text: !vaultQuery.data.initialized
          ? "凭据库未初始化"
          : vaultQuery.data.passwordless
            ? "已启用 · 未设密码"
            : vaultQuery.data.unlocked
              ? "凭据库已解锁"
              : "凭据库已锁定",
      }
    : vaultQuery.isError
      ? { text: "凭据库状态未知", attention: "unknown", uninitialized: false, passwordless: false }
      : { text: "…", attention: null, uninitialized: false, passwordless: false };
  const refetchVault = vaultQuery.refetch;
  const retryVault = useCallback(() => void refetchVault(), [refetchVault]);

  const lockVault = useCallback(async () => {
    if (vaultStatus.uninitialized) {
      pushToast("info", "凭据库尚未初始化");
      return;
    }
    if (vaultStatus.passwordless) {
      pushToast("info", "空密码凭据库无需锁定");
      return;
    }
    try {
      await vaultApi.lock();
      void queryClient.invalidateQueries({ queryKey: ["vault-status"] });
      pushToast("info", "凭据库已锁定");
    } catch (e) {
      pushToast("error", describeError(e));
    }
  }, [queryClient, pushToast, vaultStatus.uninitialized, vaultStatus.passwordless]);

  const vaultLocked = !!vaultQuery.data?.initialized && !vaultQuery.data.unlocked;
  return { vaultStatus, vaultLocked, lockVault, retryVault };
}

type SyncStatusWithHealth = import("../../ipc/types").SyncStatus & {
  warnings?: string[];
  paused?: boolean;
  vaultLocked?: boolean;
};

type SyncPauseReason = "locked" | "uninitialized";

function syncPauseReason(error: unknown): SyncPauseReason | null {
  if (typeof error === "object" && error !== null) {
    const code = (error as { code?: unknown }).code;
    if (code === "vault_locked") return "locked";
    if (code === "vault_not_init") return "uninitialized";
  }
  const message = describeError(error);
  if (message.includes("凭据库已锁定")) return "locked";
  if (message.includes("凭据库尚未初始化") || message.includes("凭据库未初始化")) return "uninitialized";
  return null;
}

function syncErrorSignature(error: unknown): string {
  const code = typeof error === "object" && error !== null ? (error as { code?: unknown }).code : undefined;
  return `${typeof code === "string" ? code : ""}:${describeError(error)}`;
}

function syncTime(value: string | number | null | undefined): number | null {
  if (value === null || value === undefined || value === "") return null;
  const parsed = typeof value === "number" ? value : Date.parse(value);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : null;
}

interface WebSyncHealth {
  error: string | null;
  pauseReason: SyncPauseReason | null;
  warnings: string[];
  lastAttemptAt: number | null;
  lastSuccessAt: number | null;
}

export interface SyncStatusSummary {
  text: string;
  title: string | undefined;
  className: string;
  warnings: string[];
  showWarnings: boolean;
  // failed is the only sync state the status bar shows as its own item; every other state is quiet.
  failed: boolean;
}

// useSyncStatusSummary is the status bar's sync label: desktop reads the sync link and status queries,
// the browser follows the web sync request events.
export function useSyncStatusSummary(vaultLocked: boolean): SyncStatusSummary {
  const pushToast = useUi((s) => s.pushToast);
  const syncLink = useQuery({
    queryKey: ["sync-link"],
    queryFn: () => syncApi.linkGet(),
    enabled: !WEB,
  });
  const syncStatus = useQuery({
    queryKey: ["sync-status"],
    queryFn: async () => (await syncApi.status()) as SyncStatusWithHealth,
    enabled: !WEB,
  });
  const [webSyncHealth, setWebSyncHealth] = useState<WebSyncHealth>({
    error: null, pauseReason: null, warnings: [], lastAttemptAt: null, lastSuccessAt: null,
  });
  const webErrorSignatureRef = useRef<Set<string>>(new Set());

  useEffect(() => {
    if (!WEB) return;
    const onError = (event: Event) => {
      const cause = (event as CustomEvent<unknown>).detail;
      const pauseReason = syncPauseReason(cause);
      const message = describeError(cause);
      const signature = syncErrorSignature(cause);
      setWebSyncHealth((previous) => ({
        error: pauseReason ? null : message,
        pauseReason,
        warnings: [],
        lastAttemptAt: Date.now(),
        lastSuccessAt: previous.lastSuccessAt,
      }));
      if (pauseReason) {
        webErrorSignatureRef.current.clear();
      } else if (!webErrorSignatureRef.current.has(signature)) {
        webErrorSignatureRef.current.add(signature);
        pushToast("error", `同步失败：${message}`);
      }
    };
    const onResult = (event: Event) => {
      const result = (event as CustomEvent<import("../../features/settings/SyncCard").WebSyncResult>).detail;
      if (!result || typeof result !== "object") return;
      const completedAt = syncTime(result.lastSuccessAt) ?? Date.now();
      webErrorSignatureRef.current.clear();
      setWebSyncHealth({
        error: null,
        pauseReason: null,
        warnings: Array.isArray(result.warnings) ? result.warnings : [],
        lastAttemptAt: completedAt,
        lastSuccessAt: completedAt,
      });
    };
    window.addEventListener(WEB_SYNC_ERROR_EVENT, onError);
    window.addEventListener(WEB_SYNC_RESULT_EVENT, onResult);
    const stopTracking = trackWebSyncRequests();
    return () => {
      window.removeEventListener(WEB_SYNC_ERROR_EVENT, onError);
      window.removeEventListener(WEB_SYNC_RESULT_EVENT, onResult);
      stopTracking();
    };
  }, [pushToast]);

  const linkConfigured = !!(syncLink.data?.url && syncLink.data.username && syncLink.data.hasPassword);
  const syncConfigured = linkConfigured || !!syncStatus.data?.configured;
  const desktopPauseReason: SyncPauseReason | null = syncStatus.data?.paused || syncStatus.data?.vaultLocked
    ? syncStatus.data.vaultLocked || vaultLocked ? "locked" : "uninitialized"
    : syncConfigured && vaultLocked
      ? "locked"
      : syncPauseReason(syncStatus.error);
  const pauseReason = WEB ? webSyncHealth.pauseReason : desktopPauseReason;
  const lastError = WEB
    ? webSyncHealth.error
    : syncStatus.data?.lastError ?? syncLink.data?.lastError ?? "";
  const lastAttemptAt = WEB ? webSyncHealth.lastAttemptAt : syncTime(syncStatus.data?.lastAttemptAt);
  const lastSuccessAt = WEB ? webSyncHealth.lastSuccessAt : syncTime(syncStatus.data?.lastSuccessAt);
  const syncFailed = !pauseReason && !!lastError &&
    (lastSuccessAt === null || lastAttemptAt === null || lastAttemptAt >= lastSuccessAt);
  const text = WEB
    ? pauseReason
      ? `同步已暂停：${pauseReason === "locked" ? "凭据库已锁定" : "凭据库未初始化"}`
      : syncFailed
        ? "同步失败"
        : "服务端：浏览器模式"
    : pauseReason && syncConfigured
      ? `同步已暂停：${pauseReason === "locked" ? "凭据库已锁定" : "凭据库未初始化"}`
      : syncFailed
        ? "同步失败"
        : syncLink.isError && !syncStatus.data
          ? "同步：不可用"
          : syncLink.data === undefined && !syncStatus.data
            ? "同步：…"
            : syncConfigured
              ? "同步：已配置"
              : "同步：本地模式";
  const title = syncFailed
    ? `${lastError}${lastAttemptAt ? ` · 上次尝试：${formatTime(lastAttemptAt)}` : ""}`
    : [
        lastSuccessAt ? `上次成功：${formatTime(lastSuccessAt)}` : null,
        lastAttemptAt ? `上次尝试：${formatTime(lastAttemptAt)}` : null,
      ].filter(Boolean).join(" · ") || undefined;
  const warnings = WEB ? webSyncHealth.warnings : syncStatus.data?.warnings ?? [];
  const showWarnings = warnings.length > 0 && !pauseReason;
  const className = syncFailed ? "text-red-300" : pauseReason && (WEB || syncConfigured) ? "text-amber-300" : "";
  return { text, title, className, warnings, showWarnings, failed: syncFailed };
}
