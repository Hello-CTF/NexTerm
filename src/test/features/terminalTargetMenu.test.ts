import { describe, expect, it, vi } from "vitest";
import { buildTargetMenu, isIpAddress, type TargetMenuDeps } from "../../features/terminal/terminalTargetMenu";
import type { DetectedTarget } from "../../features/terminal/terminalLinkProvider";
import type { MenuItem } from "../../ui/ContextMenu";
import type { Asset } from "../../ipc/commands";

const segment = { row: 0, start: 0, end: 0 };
const ip = (host: string): DetectedTarget => ({ kind: "ip", text: host, host, segment });
const hostport = (host: string, port: number): DetectedTarget => ({ kind: "hostport", text: `${host}:${port}`, host, port, segment });

function asset(id: string, host: string | null, name = id): Asset {
  return { id, host, name, kind: "ssh" } as Asset;
}

function makeDeps(overrides: Partial<TargetMenuDeps> = {}): TargetMenuDeps {
  return {
    sessionId: "s1",
    listAssets: vi.fn().mockResolvedValue([]),
    openHostDetail: vi.fn(),
    openAssetDraft: vi.fn(),
    openForwardTab: vi.fn(),
    copy: vi.fn().mockResolvedValue(undefined),
    toast: vi.fn(),
    describeError: (e) => String(e),
    ...overrides,
  };
}

function labels(items: MenuItem[]): string[] {
  return items.map((item) => (item.kind === "separator" ? "-" : item.kind === "group" ? `# ${item.label}` : item.label));
}

function select(items: MenuItem[], label: string): void {
  const item = items.find((candidate) => candidate.kind === "item" && candidate.label === label);
  if (!item || item.kind !== "item") throw new Error(`no item ${label}`);
  item.onSelect?.();
}

const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

describe("buildTargetMenu", () => {
  it("offers asset actions and copy for a bare IP, without forwarding", () => {
    expect(labels(buildTargetMenu(ip("10.0.0.5"), makeDeps()))).toEqual(["# 地址 10.0.0.5", "打开已有资产", "新建 SSH 资产", "复制地址"]);
  });

  it("offers forwarding for IP:port and only forwarding and copy for a hostname:port", () => {
    expect(labels(buildTargetMenu(hostport("10.0.0.5", 8080), makeDeps()))).toEqual([
      "# 地址 10.0.0.5:8080",
      "打开已有资产",
      "新建 SSH 资产",
      "建立本地转发 → 10.0.0.5:8080",
      "复制地址",
    ]);
    expect(labels(buildTargetMenu(hostport("db.internal", 5432), makeDeps()))).toEqual([
      "# 地址 db.internal:5432",
      "建立本地转发 → db.internal:5432",
      "复制地址",
    ]);
  });

  it("offers no forwarding on a session that cannot forward (Telnet)", () => {
    expect(labels(buildTargetMenu(hostport("10.0.0.5", 8080), { ...makeDeps(), forwardSupported: false }))).toEqual([
      "# 地址 10.0.0.5:8080",
      "打开已有资产",
      "新建 SSH 资产",
      "复制地址",
    ]);
  });

  it("recognises IPv4 and IPv6 only", () => {
    expect(isIpAddress("192.168.1.1")).toBe(true);
    expect(isIpAddress("fe80::1")).toBe(true);
    expect(isIpAddress("[::1]")).toBe(true);
    expect(isIpAddress("999.1.1.1")).toBe(false);
    expect(isIpAddress("example.com")).toBe(false);
  });

  it("reports when no asset has the host", async () => {
    const deps = makeDeps({ listAssets: vi.fn().mockResolvedValue([asset("a", "10.0.0.9")]) });
    select(buildTargetMenu(ip("10.0.0.5"), deps), "打开已有资产");
    await flush();
    expect(deps.openHostDetail).not.toHaveBeenCalled();
    expect(deps.toast).toHaveBeenCalledWith("info", "没有主机为 10.0.0.5 的资产");
  });

  it("opens the single matching asset, comparing hosts case-insensitively", async () => {
    const deps = makeDeps({ listAssets: vi.fn().mockResolvedValue([asset("a", "FE80::1"), asset("b", null)]) });
    select(buildTargetMenu(ip("fe80::1"), deps), "打开已有资产");
    await flush();
    expect(deps.openHostDetail).toHaveBeenCalledWith("a");
    expect(deps.toast).not.toHaveBeenCalled();
  });

  it("opens the first of several matches and says how many there are", async () => {
    const deps = makeDeps({ listAssets: vi.fn().mockResolvedValue([asset("a", "10.0.0.5"), asset("b", "10.0.0.5")]) });
    select(buildTargetMenu(ip("10.0.0.5"), deps), "打开已有资产");
    await flush();
    expect(deps.openHostDetail).toHaveBeenCalledWith("a");
    expect(deps.toast).toHaveBeenCalledWith("info", expect.stringContaining("2"));
  });

  it("shows an error when the asset list cannot be read", async () => {
    const deps = makeDeps({ listAssets: vi.fn().mockRejectedValue(new Error("boom")) });
    select(buildTargetMenu(ip("10.0.0.5"), deps), "打开已有资产");
    await flush();
    expect(deps.toast).toHaveBeenCalledWith("error", expect.stringContaining("boom"));
  });

  it("prefills a new SSH asset, always defaulting to the SSH port", () => {
    const deps = makeDeps();
    select(buildTargetMenu(ip("10.0.0.5"), deps), "新建 SSH 资产");
    expect(deps.openAssetDraft).toHaveBeenCalledWith({ kind: "ssh", name: "10.0.0.5", host: "10.0.0.5", port: 22 });
    select(buildTargetMenu(hostport("10.0.0.5", 2222), deps), "新建 SSH 资产");
    expect(deps.openAssetDraft).toHaveBeenLastCalledWith({ kind: "ssh", name: "10.0.0.5", host: "10.0.0.5", port: 22 });
  });

  it("uses the registered local-forward integration", async () => {
    const createLocalForward = vi.fn().mockResolvedValue(undefined);
    const deps = makeDeps({ createLocalForward });
    select(buildTargetMenu(hostport("db.internal", 5432), deps), "建立本地转发 → db.internal:5432");
    await flush();
    expect(createLocalForward).toHaveBeenCalledWith({ sessionId: "s1", host: "db.internal", port: 5432 });
    expect(deps.openForwardTab).not.toHaveBeenCalled();
  });

  it("falls back to the port forwarding tab and tells the user the target", () => {
    const deps = makeDeps();
    select(buildTargetMenu(hostport("db.internal", 5432), deps), "建立本地转发 → db.internal:5432");
    expect(deps.openForwardTab).toHaveBeenCalledWith("s1");
    expect(deps.toast).toHaveBeenCalledWith("info", "请在端口转发中填写目标 db.internal:5432");
  });

  it("copies the address and reports success or failure", async () => {
    const deps = makeDeps();
    select(buildTargetMenu(hostport("db.internal", 5432), deps), "复制地址");
    await flush();
    expect(deps.copy).toHaveBeenCalledWith("db.internal:5432");
    expect(deps.toast).toHaveBeenCalledWith("success", expect.any(String));
    const failing = makeDeps({ copy: vi.fn().mockRejectedValue(new Error("denied")) });
    select(buildTargetMenu(ip("10.0.0.5"), failing), "复制地址");
    await flush();
    expect(failing.toast).toHaveBeenCalledWith("error", expect.stringContaining("denied"));
  });
});

describe("terminalIntegrations", () => {
  it("registers, merges and removes only what a call added", async () => {
    const { registerTerminalIntegrations, terminalIntegrations } = await import("../../features/terminal/terminalIntegrations");
    const openInApp = vi.fn();
    const createLocalForward = vi.fn();
    const undoA = registerTerminalIntegrations({ openInApp });
    const undoB = registerTerminalIntegrations({ createLocalForward });
    expect(terminalIntegrations()).toEqual({ openInApp, createLocalForward });
    undoA();
    expect(terminalIntegrations()).toEqual({ createLocalForward });
    undoB();
    expect(terminalIntegrations()).toEqual({});
  });
});
