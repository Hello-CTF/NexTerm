import type { FinderMatch, ServiceHostResult, ServiceUnit } from "../../ipc/servicesApi";

export type ServiceStateFilter = "all" | "running" | "failed" | "inactive" | "other";

export const SERVICE_ACTION_LABELS: Record<string, string> = {
  status: "状态",
  start: "启动",
  stop: "停止",
  restart: "重启",
  reload: "重载",
  enable: "启用",
  disable: "禁用",
};

export const EXEC_STATUS_LABELS: Record<string, string> = {
  success: "成功",
  exit_error: "非零退出",
  transport_error: "传输错误",
  timeout: "超时",
  canceled: "已取消",
};

export function filterServiceUnits(
  units: readonly ServiceUnit[],
  query: string,
  state: ServiceStateFilter,
): ServiceUnit[] {
  const needle = query.trim().toLowerCase();
  return units.filter((unit) => {
    if (needle && !`${unit.name}\n${unit.description}\n${unit.load}\n${unit.active}\n${unit.sub}`.toLowerCase().includes(needle)) {
      return false;
    }
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

export function serviceResultMessage(host: ServiceHostResult): string {
  if (host.systemd === "unsupported") return "该主机不支持 systemd，或当前无法访问 system bus";
  if (host.failureKind === "sudo") return "sudo 不可用或需要密码；非交互执行未跳过该主机";
  const result = host.result;
  return result.error || result.stderr || result.stdout || EXEC_STATUS_LABELS[result.status] || "执行失败";
}

export function matchPID(match: FinderMatch): number {
  return match.process?.pid ?? match.listener?.pid ?? 0;
}

export function matchCommand(match: FinderMatch): string {
  return match.process?.args || match.listener?.program || "（无进程信息）";
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
