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
  dismissedVersion: string | null;
}

interface UpdateActions {
  check: () => Promise<void>;
  installUpdate: () => Promise<void>;
  restart: () => Promise<void>;
  dismissBanner: () => void;
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

export const useUpdateStore = create<UpdateState & UpdateActions>()((set, get) => ({
  status: null,
  checking: false,
  checkError: null,
  install: { ...idleInstall },
  installedVersion: null,
  restarting: false,
  restartError: null,
  dismissedVersion: null,

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
    // 进度监听只在安装期间持有: WEB 下 subscribeEvent 会拉起 events 长连接,
    // 平时不订阅; 安装结果以 install() 返回值为准, 不依赖事件收尾。
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

  dismissBanner: () => {
    const status = get().status;
    set({ dismissedVersion: status?.available ? status.version : null });
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

// 检查结论三态与「有更新但不可安装」分开: available 只看有没有新版本,
// canInstall 决定能不能在应用内安装; 后端把检查失败降级进 unavailableReason,
// 传输层抛错进 checkError, 两者都呈现为检查失败。
export function updateVerdict(
  s: Pick<UpdateState, "status" | "checking" | "checkError">,
): UpdateVerdict {
  if (s.checkError) return "check-failed";
  const status = s.status;
  if (!status) return s.checking ? "checking" : "idle";
  if (status.available) return status.canInstall ? "available" : "unavailable";
  if (status.unavailableReason) return "check-failed";
  return "uptodate";
}

export function updateInstallAllowed(status: UpdateStatusDto | null): boolean {
  return DESKTOP && status?.available === true && status.canInstall;
}

let progressRefs = 0;
let progressOff: (() => void) | null = null;

function acquireProgressListener(): () => void {
  progressRefs += 1;
  let released = false;
  if (progressRefs === 1) {
    void listenEvent<UpdateProgressEvent>(EVENTS.updateProgress, (event) => {
      useUpdateStore.getState().onProgress(event);
    }).then((off) => {
      if (released) off();
      else progressOff = off;
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
