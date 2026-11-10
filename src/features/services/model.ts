import type { FinderMatch, ServiceHostResult, ServiceUnit } from "../../ipc/servicesApi";
import { t } from "../../i18n";

export type ServiceStateFilter = "all" | "running" | "failed" | "inactive" | "other";

export const SERVICE_ACTION_LABELS: Record<string, string> = {
  status: t("状态"),
  start: t("启动"),
  stop: t("停止"),
  restart: t("重启"),
  reload: t("重载"),
  enable: t("启用"),
  disable: t("禁用"),
  "set-automatic": t("设为自动启动"),
  "set-manual": t("设为手动启动"),
  "set-disabled": t("禁用启动"),
};

export const SYSTEMD_ROW_ACTIONS = ["status", "start", "stop", "restart", "reload", "enable", "disable"] as const;
export const WINDOWS_ROW_ACTIONS = ["status", "start", "stop", "restart", "set-automatic", "set-manual", "set-disabled"] as const;

const WINDOWS_STATE_LABELS: Record<string, string> = {
  Running: t("运行中"),
  Stopped: t("已停止"),
  Paused: t("已暂停"),
  "Start Pending": t("正在启动"),
  "Stop Pending": t("正在停止"),
  "Continue Pending": t("正在继续"),
  "Pause Pending": t("正在暂停"),
};

const WINDOWS_START_MODE_LABELS: Record<string, string> = {
  Auto: t("自动"),
  Delayed: t("自动（延迟）"),
  Manual: t("手动"),
  Disabled: t("已禁用"),
  Boot: t("引导"),
  System: t("系统"),
};

export function isWindowsHost(host: Pick<ServiceHostResult, "platform">): boolean {
  return host.platform === "windows";
}

export function windowsStateLabel(state: string): string {
  return WINDOWS_STATE_LABELS[state] ?? state;
}

export function windowsStartModeLabel(mode: string | undefined): string {
  return mode ? (WINDOWS_START_MODE_LABELS[mode] ?? mode) : "";
}

export function windowsStateTone(state: string): string {
  if (state === "Running") return "text-green-300";
  if (state === "Stopped") return "text-neutral-400";
  return "text-amber-300";
}

/** Actions for the bulk selector: Windows ones when every selected host was listed as Windows. */
export function bulkActionsFor(
  selectedIds: readonly string[],
  listed: readonly Pick<ServiceHostResult, "platform" | "result">[] | undefined,
): readonly string[] {
  if (!listed || selectedIds.length === 0) return SYSTEMD_ROW_ACTIONS;
  const platforms = new Map(listed.map((host) => [host.result.sessionId, host.platform]));
  return selectedIds.every((id) => platforms.get(id) === "windows") ? WINDOWS_ROW_ACTIONS : SYSTEMD_ROW_ACTIONS;
}

export const EXEC_STATUS_LABELS: Record<string, string> = {
  success: t("成功"),
  exit_error: t("非零退出"),
  transport_error: t("传输错误"),
  timeout: t("超时"),
  canceled: t("已取消"),
};

function windowsStateMatches(unit: ServiceUnit, state: ServiceStateFilter): boolean {
  switch (state) {
    case "running":
      return unit.active === "Running";
    case "failed":
      return false;
    case "inactive":
      return unit.active === "Stopped";
    case "other":
      return unit.active !== "Running" && unit.active !== "Stopped";
    default:
      return true;
  }
}

export function filterServiceUnits(
  units: readonly ServiceUnit[],
  query: string,
  state: ServiceStateFilter,
  windows = false,
): ServiceUnit[] {
  const needle = query.trim().toLowerCase();
  return units.filter((unit) => {
    const haystack = windows
      ? `${unit.name}\n${unit.description}\n${unit.active}\n${windowsStateLabel(unit.active)}\n${unit.startMode ?? ""}`
      : `${unit.name}\n${unit.description}\n${unit.load}\n${unit.active}\n${unit.sub}`;
    if (needle && !haystack.toLowerCase().includes(needle)) {
      return false;
    }
    if (windows) return windowsStateMatches(unit, state);
    switch (state) {
      case "running":
        return unit.active === "active" && unit.sub === "running";
      case "failed":
        return unit.active === "failed" || unit.sub === "failed" || unit.load === "not-found";
      case "inactive":
        return unit.active === "inactive" || unit.sub === "dead";
      case "other":
        return !(
          (unit.active === "active" && unit.sub === "running") ||
          unit.active === "failed" || unit.sub === "failed" || unit.load === "not-found" ||
          unit.active === "inactive" || unit.sub === "dead"
        );
      default:
        return true;
    }
  });
}

const WINDOWS_FAILURE_MESSAGES: Record<string, string> = {
  not_found: t("该主机上没有这个服务"),
  permission: t("权限不足：需要以管理员身份连接才能管理该服务"),
  powershell: t("该主机找不到 PowerShell，无法管理 Windows 服务"),
  output: t("无法识别 Windows 服务列表的输出"),
};

export function serviceResultMessage(host: ServiceHostResult): string {
  if (isWindowsHost(host)) {
    const known = host.failureKind ? WINDOWS_FAILURE_MESSAGES[host.failureKind] : undefined;
    if (known) return known;
    const result = host.result;
    return result.error || result.stderr || result.stdout || EXEC_STATUS_LABELS[result.status] || t("执行失败");
  }
  if (host.systemd === "unsupported") return t("该主机不支持 systemd，或当前无法访问 system bus");
  if (host.failureKind === "sudo") return t("sudo 不可用或需要密码；非交互执行未跳过该主机");
  const result = host.result;
  return result.error || result.stderr || result.stdout || EXEC_STATUS_LABELS[result.status] || t("执行失败");
}

export function matchPID(match: FinderMatch): number {
  return match.process?.pid ?? match.listener?.pid ?? 0;
}

export function matchCommand(match: FinderMatch): string {
  return match.process?.args || match.listener?.program || t("（无进程信息）");
}

export function listenerTargetHost(address: string): string {
  const normalized = address.trim().replace(/^\[|\]$/g, "");
  if (!normalized || normalized === "*" || normalized === "0.0.0.0" || normalized === "::" || normalized === "0:0:0:0:0:0:0:0") {
    return "127.0.0.1";
  }
  return normalized;
}

export function formatListenAddress(address: string, port: number): string {
  return `${address.includes(":") && !address.startsWith("[") ? `[${address}]` : address}:${port}`;
}

export function parseLocalListenPort(value: string): number | null {
  const port = Number(value.trim());
  return Number.isInteger(port) && port >= 1 && port <= 65535 ? port : null;
}
