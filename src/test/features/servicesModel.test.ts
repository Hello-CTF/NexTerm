import { describe, expect, it } from "vitest";
import {
  bulkActionsFor,
  filterServiceUnits,
  serviceResultMessage,
  windowsStartModeLabel,
  windowsStateLabel,
  formatListenAddress,
  listenerTargetHost,
  parseLocalListenPort,
} from "../../features/services/model";
import type { ServiceHostResult, ServiceUnit } from "../../ipc/servicesApi";

const units: ServiceUnit[] = [
  { name: "nginx.service", load: "loaded", active: "active", sub: "running", description: "HTTP server" },
  { name: "postgres.service", load: "loaded", active: "failed", sub: "failed", description: "Database" },
  { name: "cron.service", load: "loaded", active: "inactive", sub: "dead", description: "Scheduler" },
  { name: "reload.service", load: "loaded", active: "active", sub: "reloading", description: "Reloading" },
];

describe("service unit filtering", () => {
  it("filters by unit text and maps running, failed, inactive and other states", () => {
    expect(filterServiceUnits(units, "http", "all").map((unit) => unit.name)).toEqual(["nginx.service"]);
    expect(filterServiceUnits(units, "", "running").map((unit) => unit.name)).toEqual(["nginx.service"]);
    expect(filterServiceUnits(units, "", "failed").map((unit) => unit.name)).toEqual(["postgres.service"]);
    expect(filterServiceUnits(units, "", "inactive").map((unit) => unit.name)).toEqual(["cron.service"]);
    expect(filterServiceUnits(units, "", "other").map((unit) => unit.name)).toEqual(["reload.service"]);
  });

  it("treats not-found load as failed and ignores case in descriptions", () => {
    const missing = { ...units[0], name: "missing.service", load: "not-found", active: "inactive", sub: "dead" };
    expect(filterServiceUnits([missing], "", "failed")).toHaveLength(1);
    expect(filterServiceUnits(units, "DATABASE", "all").map((unit) => unit.name)).toEqual(["postgres.service"]);
  });
});

describe("finder forward target mapping", () => {
  it("uses loopback for wildcard listeners and preserves specific or IPv6 addresses", () => {
    expect(listenerTargetHost("0.0.0.0")).toBe("127.0.0.1");
    expect(listenerTargetHost("*")).toBe("127.0.0.1");
    expect(listenerTargetHost("::")).toBe("127.0.0.1");
    expect(listenerTargetHost("10.0.0.8")).toBe("10.0.0.8");
    expect(listenerTargetHost("2001:db8::1")).toBe("2001:db8::1");
    expect(formatListenAddress("::", 443)).toBe("[::]:443");
  });

  it("accepts only integer local ports in range", () => {
    expect(parseLocalListenPort("18080")).toBe(18080);
    for (const value of ["0", "65536", "80;id", "1.5", ""]) {
      expect(parseLocalListenPort(value)).toBeNull();
    }
  });
});

describe("Windows service model", () => {
  const windowsUnits: ServiceUnit[] = [
    { name: "Spooler", load: "", active: "Running", sub: "", description: "Print Spooler", startMode: "Auto", pid: 12 },
    { name: "wuauserv", load: "", active: "Stopped", sub: "", description: "Windows Update", startMode: "Manual" },
    { name: "BITS", load: "", active: "Start Pending", sub: "", description: "Transfer", startMode: "Delayed" },
  ];

  it("maps the Windows states onto the state filter without a failed bucket", () => {
    expect(filterServiceUnits(windowsUnits, "", "running", true).map((unit) => unit.name)).toEqual(["Spooler"]);
    expect(filterServiceUnits(windowsUnits, "", "inactive", true).map((unit) => unit.name)).toEqual(["wuauserv"]);
    expect(filterServiceUnits(windowsUnits, "", "other", true).map((unit) => unit.name)).toEqual(["BITS"]);
    expect(filterServiceUnits(windowsUnits, "", "failed", true)).toEqual([]);
    expect(filterServiceUnits(windowsUnits, "print", "all", true).map((unit) => unit.name)).toEqual(["Spooler"]);
    expect(filterServiceUnits(windowsUnits, "delayed", "all", true).map((unit) => unit.name)).toEqual(["BITS"]);
  });

  it("labels states and startup types and leaves unknown values readable", () => {
    expect(windowsStateLabel("Running")).toBe("运行中");
    expect(windowsStateLabel("Stopped")).toBe("已停止");
    expect(windowsStateLabel("Start Pending")).toBe("正在启动");
    expect(windowsStateLabel("Weird")).toBe("Weird");
    expect(windowsStartModeLabel("Delayed")).toBe("自动（延迟）");
    expect(windowsStartModeLabel("Disabled")).toBe("已禁用");
    expect(windowsStartModeLabel(undefined)).toBe("");
  });

  const host = (sessionId: string, platform: string): Pick<ServiceHostResult, "platform" | "result"> => ({
    platform, result: { sessionId } as ServiceHostResult["result"],
  });

  it("offers Windows actions only when every selected host was listed as Windows", () => {
    const listed = [host("w1", "windows"), host("w2", "windows"), host("l1", "systemd")];
    expect(bulkActionsFor(["w1", "w2"], listed)).toContain("set-automatic");
    expect(bulkActionsFor(["w1", "w2"], listed)).not.toContain("reload");
    expect(bulkActionsFor(["w1", "l1"], listed)).toContain("reload");
    expect(bulkActionsFor(["w1", "l1"], listed)).not.toContain("set-manual");
    expect(bulkActionsFor(["w1"], undefined)).toContain("reload");
  });

  it("explains Windows failures instead of showing systemd advice", () => {
    const failed = (failureKind: string): ServiceHostResult => ({
      platform: "windows", systemd: "unknown", failureKind, units: [],
      result: { sessionId: "w", sessionName: "w", status: "exit_error", stdout: "", stderr: "raw", exitCode: 1, durationMs: 1, truncated: false },
    });
    expect(serviceResultMessage(failed("permission"))).toContain("管理员");
    expect(serviceResultMessage(failed("not_found"))).toContain("没有这个服务");
    expect(serviceResultMessage(failed("exit"))).toBe("raw");
  });
});
