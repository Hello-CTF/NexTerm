import { create } from "zustand";
import { appUpdateApi } from "../ipc/commands";
import { DESKTOP } from "../ipc/env";
import { EVENTS, listenEvent, type UpdateProgressEvent } from "../ipc/events";
import { describeError } from "../ui/errorText";
import type { UpdateStatusDto } from "../ipc/types";

export type UpdateVerdict =
  | "idle"
  | "checking"
  | "uptodate"
  | "available"
  | "unavailable"
  | "external"
  | "check-failed";

export interface UpdateInstallState {
  active: boolean;
  phase: "download" | "install" | null;
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
  installedVersion: string | null;
  restarting: boolean;
  restartError: string | null;
  skippedVersion: string | null;
  remindedVersion: string | null;
}

interface UpdateActions {
  check: () => Promise<void>;
  installUpdate: () => Promise<void>;
  restart: () => Promise<void>;
  skipVersion: (version: string) => void;
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

const SKIPPED_VERSION_KEY = "nexterm.update.v1";

function loadSkippedVersion(): string | null {
  try {
    return localStorage.getItem(SKIPPED_VERSION_KEY) || null;
  } catch {
    return null;
  }
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
  remindedVersion: null,

  check: async () => {
    if (get().checking) return;
    set({ checking: true, checkError: null });
    try {
      const status = await appUpdateApi.check();
      set({ status, checking: false });
    } catch (e) {
      set({ checking: false, checkError: describeError(e) });
    }
  },

  installUpdate: async () => {
    if (get().install.active) return;
    // The progress listener is held only during install: on WEB, subscribeEvent spins up the events long connection,
    // so no subscription in normal times; the install result follows the install() return value, not event delivery.
    const releaseProgress = acquireProgressListener();
    set({ install: { ...idleInstall, active: true }, restartError: null });
    try {
      const result = await appUpdateApi.install(get().status?.version || undefined);
      if (result.installed) {
        set({
          installedVersion: result.version || get().status?.version || "",
          install: { ...idleInstall },
        });
      } else {
        set({ install: { ...idleInstall, error: result.unavailableReason || "安装未完成" } });
      }
    } catch (e) {
      set({ install: { ...idleInstall, error: describeError(e) } });
    } finally {
      releaseProgress();
    }
  },

  restart: async () => {
    if (get().restarting) return;
    set({ restarting: true, restartError: null });
    try {
      const result = await appUpdateApi.restart();
      if (result.restarted) return;
      set({ restarting: false, restartError: result.unavailableReason || "重启失败" });
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

  onProgress: (event) => {
    const install = get().install;
    if (!install.active) return;
    if (install.taskId === null ? !event.taskId : event.taskId !== install.taskId) return;
    if (event.error) {
      set({ install: { ...idleInstall, error: event.error } });
      return;
    }
    set({
      install: {
        ...install,
        taskId: event.taskId,
        phase: event.phase === "install" ? "install" : "download",
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

export function acquireProgressListener(): () => void {
  progressRefs += 1;
  let released = false;
  if (progressRefs === 1) {
    void listenEvent<UpdateProgressEvent>(EVENTS.updateProgress, (event) => {
      useUpdateStore.getState().onProgress(event);
    }).then((off) => {
      if (released) off();
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
