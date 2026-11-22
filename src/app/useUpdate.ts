import { create } from "zustand";
import { appUpdateApi } from "../ipc/commands";
import { DESKTOP } from "../ipc/env";
import { EVENTS, listenEvent, type UpdateProgressEvent } from "../ipc/events";
import { describeError } from "../ui/errorText";
import type { UpdateInstallStateDto, UpdateReleaseChannel, UpdateStatusDto } from "../ipc/types";
import { t, translateMessage } from "../i18n";

export type UpdateVerdict =
  | "idle"
  | "checking"
  | "uptodate"
  | "available"
  | "unavailable"
  | "external"
  | "check-failed";

export type UpdateInstallPhase = "download" | "verify" | "install";

export interface UpdateInstallState {
  active: boolean;
  phase: UpdateInstallPhase | null;
  transferred: number;
  total: number;
  error: string | null;
  taskId: string | null;
}

export interface UpdateState {
  status: UpdateStatusDto | null;
  checking: boolean;
  checkError: string | null;
  install: UpdateInstallState;
  // The version installed and waiting for a restart; the backend keeps it, so a reloaded interface restores it.
  installedVersion: string | null;
  restarting: boolean;
  restartError: string | null;
  // skippedVersion persists ("跳过此版本"); dismissedVersion only hides the banner until the next launch.
  skippedVersion: string | null;
  dismissedVersion: string | null;
  // restartDeferred hides the restart banner until the next launch after "稍后"; settings still offer the restart.
  restartDeferred: boolean;
  remindedVersion: string | null;
  // The channel chosen on this machine; null until a check or a change reports it (status.releaseChannel wins).
  releaseChannel: UpdateReleaseChannel | null;
  channelSaving: boolean;
  channelError: string | null;
}

interface UpdateActions {
  check: (force?: boolean) => Promise<void>;
  setReleaseChannel: (channel: UpdateReleaseChannel) => Promise<boolean>;
  installUpdate: () => Promise<void>;
  restart: () => Promise<void>;
  skipVersion: (version: string) => void;
  unskipVersion: () => void;
  dismissVersion: (version: string) => void;
  deferRestart: () => void;
  syncInstallState: () => Promise<void>;
  onProgress: (event: UpdateProgressEvent) => void;
}

const idleInstall: UpdateInstallState = {
  active: false,
  phase: null,
  transferred: 0,
  total: 0,
  error: null,
  taskId: null,
};

let checkSequence = 0;
// installCallPending is set while this interface awaits app_update_install; its result is then the only source of
// the outcome. Without it (after a reload) progress completion is resolved through app_update_state.
let installCallPending = false;
let recoveryRelease: (() => void) | null = null;

const SKIPPED_VERSION_KEY = "nexterm.update.v1";

function loadSkippedVersion(): string | null {
  try {
    return localStorage.getItem(SKIPPED_VERSION_KEY) || null;
  } catch {
    return null;
  }
}

function progressPhase(phase: string | undefined): UpdateInstallPhase {
  return phase === "install" || phase === "verify" ? phase : "download";
}

function releaseRecovery(): void {
  recoveryRelease?.();
  recoveryRelease = null;
}

export const useUpdateStore = create<UpdateState & UpdateActions>()((set, get) => ({
  status: null,
  checking: false,
  checkError: null,
  install: { ...idleInstall },
  installedVersion: null,
  restarting: false,
  restartError: null,
  skippedVersion: loadSkippedVersion(),
  dismissedVersion: null,
  restartDeferred: false,
  remindedVersion: null,
  releaseChannel: null,
  channelSaving: false,
  channelError: null,

  // A forced check (after a channel change) supersedes one still in flight: only the newest result is applied,
  // so an answer computed for the old channel can never overwrite the new one.
  check: async (force = false) => {
    if (get().checking && !force) return;
    const sequence = ++checkSequence;
    set({ checking: true, checkError: null });
    try {
      const status = await appUpdateApi.check();
      if (sequence !== checkSequence) return;
      set({ status, checking: false });
    } catch (e) {
      if (sequence !== checkSequence) return;
      set({ checking: false, checkError: describeError(e) });
    }
  },

  setReleaseChannel: async (channel) => {
    if (get().channelSaving) return false;
    set({ channelSaving: true, channelError: null });
    try {
      const saved = await appUpdateApi.setChannel(channel);
      set({
        channelSaving: false,
        releaseChannel: saved.channel === "rc" ? "rc" : "stable",
        status: null,
        remindedVersion: null,
      });
    } catch (e) {
      set({ channelSaving: false, channelError: describeError(e) });
      return false;
    }
    await get().check(true);
    return true;
  },

  installUpdate: async () => {
    if (get().install.active) return;
    const retry = get().install.error !== null;
    // The progress listener is held only during install: on WEB, subscribeEvent spins up the events long connection,
    // so no subscription in normal times; the install result follows the install() return value, not event delivery.
    const releaseProgress = acquireProgressListener();
    installCallPending = true;
    set({ install: { ...idleInstall, active: true }, restartError: null });
    try {
      // A retry first refreshes the status: a newer release may have replaced the one that failed, and the
      // backend refuses to install a version that is no longer the newest.
      if (retry) {
        await get().check(true);
        const status = get().status;
        if (!status?.available || !status.canInstall) {
          set({ install: { ...idleInstall } });
          return;
        }
      }
      const result = await appUpdateApi.install(get().status?.version || undefined);
      if (result.installed) {
        set({
          installedVersion: result.version || get().status?.version || "",
          restartDeferred: false,
          install: { ...idleInstall },
        });
      } else {
        set({ install: { ...idleInstall, error: result.unavailableReason ? translateMessage(result.unavailableReason) : t("安装未完成") } });
      }
    } catch (e) {
      set({ install: { ...idleInstall, error: describeError(e) } });
    } finally {
      installCallPending = false;
      releaseProgress();
    }
  },

  restart: async () => {
    if (get().restarting) return;
    set({ restarting: true, restartError: null });
    try {
      const result = await appUpdateApi.restart();
      if (result.restarted) return;
      set({ restarting: false, restartError: result.unavailableReason ? translateMessage(result.unavailableReason) : t("重启失败") });
    } catch (e) {
      set({ restarting: false, restartError: describeError(e) });
    }
  },

  skipVersion: (version) => {
    set({ skippedVersion: version });
    try {
      localStorage.setItem(SKIPPED_VERSION_KEY, version);
    } catch {
    }
  },

  unskipVersion: () => {
    set({ skippedVersion: null });
    try {
      localStorage.removeItem(SKIPPED_VERSION_KEY);
    } catch {
    }
  },

  dismissVersion: (version) => set({ dismissedVersion: version }),

  deferRestart: () => set({ restartDeferred: true }),

  // syncInstallState adopts the backend's install state: at startup after a webview reload, and when an install
  // this interface did not start (or no longer awaits) completes.
  syncInstallState: async () => {
    if (!DESKTOP || installCallPending) return;
    let state: UpdateInstallStateDto;
    try {
      state = await appUpdateApi.state();
    } catch (e) {
      console.warn("[NexTerm] Failed to read update install state", e);
      return;
    }
    if (installCallPending) return;
    const installedVersion = state.installedVersion || get().installedVersion;
    if (state.phase === "download" || state.phase === "verify" || state.phase === "install") {
      recoveryRelease ??= acquireProgressListener();
      set({
        installedVersion,
        install: {
          active: true,
          phase: progressPhase(state.phase),
          transferred: state.transferred,
          total: state.total,
          error: null,
          taskId: state.taskId || null,
        },
      });
      return;
    }
    releaseRecovery();
    set({
      installedVersion,
      install: { ...idleInstall, error: state.phase === "failed" && state.error ? translateMessage(state.error) : null },
    });
  },

  onProgress: (event) => {
    const install = get().install;
    if (!install.active) return;
    if (install.taskId === null ? !event.taskId : event.taskId !== install.taskId) return;
    // The end of an install, failed or completed, is settled by the install() result while this interface awaits
    // it, and by the backend state otherwise; an error here would only race that result into a second report.
    if (event.error || (event.done && event.phase === "install")) {
      if (!installCallPending) void get().syncInstallState();
      return;
    }
    set({
      install: {
        ...install,
        taskId: event.taskId,
        phase: progressPhase(event.phase),
        transferred: event.transferred,
        total: event.total,
      },
    });
  },
}));

// The three-state check verdict is separate from "update available but not installable": available only reflects whether a new version exists,
// canInstall decides whether in-app install is possible; the backend degrades check failures into unavailableReason,
// transport errors go into checkError, and both surface as check failed.
export function updateVerdict(
  s: Pick<UpdateState, "status" | "checking" | "checkError">,
): UpdateVerdict {
  if (s.checkError) return "check-failed";
  const status = s.status;
  if (!status) return s.checking ? "checking" : "idle";
  // Platforms that update through a store or a manual download have nothing to check; that is not a failure.
  if (status.channel) return "external";
  if (status.available) return status.canInstall ? "available" : "unavailable";
  if (status.unavailableReason) return "check-failed";
  return "uptodate";
}

export function updateInstallAllowed(status: UpdateStatusDto | null): boolean {
  return DESKTOP && status?.available === true && status.canInstall;
}

let progressRefs = 0;
let progressOff: (() => void) | null = null;
// progressGeneration identifies the current subscription, so one that resolves after every holder released it
// (or after a newer one started) is closed instead of kept.
let progressGeneration = 0;

export function acquireProgressListener(): () => void {
  progressRefs += 1;
  let released = false;
  if (progressRefs === 1) {
    const generation = ++progressGeneration;
    void listenEvent<UpdateProgressEvent>(EVENTS.updateProgress, (event) => {
      useUpdateStore.getState().onProgress(event);
    }).then((off) => {
      if (generation !== progressGeneration || progressRefs === 0) off();
      else progressOff = off;
    }).catch((error: unknown) => {
      console.warn("[NexTerm] Failed to subscribe update progress", error);
    });
  }
  return () => {
    if (released) return;
    released = true;
    progressRefs -= 1;
    if (progressRefs === 0) {
      progressGeneration += 1;
      progressOff?.();
      progressOff = null;
    }
  };
}

export function useUpdate(): UpdateState & UpdateActions {
  return useUpdateStore();
}

export function getUpdateState(): UpdateState & UpdateActions {
  return useUpdateStore.getState();
}

// effectiveReleaseChannel prefers what the last check reported, then what was just saved, then stable.
export function effectiveReleaseChannel(
  s: Pick<UpdateState, "status" | "releaseChannel">,
): UpdateReleaseChannel {
  const reported = s.status?.releaseChannel ?? s.releaseChannel;
  return reported === "rc" ? "rc" : "stable";
}

// The releases page is the fallback for every manual download; a check names the exact release page when it can.
export const RELEASES_PAGE_URL = "https://github.com/Hello-CTF/NexTerm/releases";

export function releasePageURL(status: UpdateStatusDto | null): string {
  return status?.releaseUrl || RELEASES_PAGE_URL;
}
