/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import type { Asset } from "../../ipc/commands";
import { clickButton, flush, flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  demo: false,
  assetList: vi.fn(),
  grantList: vi.fn(),
  grantSet: vi.fn(),
  grantRevoke: vi.fn(),
  ask: vi.fn(),
  toast: vi.fn(),
  onClose: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: { list: mocks.assetList },
    sessionApi: {},
    terminalApi: {},
    dbApi: {},
    vaultApi: {},
  };
});
vi.mock("../../ipc/grantApi", () => ({
  grantApi: { list: mocks.grantList, set: mocks.grantSet, revoke: mocks.grantRevoke },
}));
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask };
});
vi.mock("../../demo", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../demo")>();
  return { ...actual, get DEMO() { return mocks.demo; } };
});

import { GrantPanel } from "../../features/ai/GrantPanel";
import { useUi } from "../../app/store";

function asset(id: string, name: string, kind = "ssh"): Asset {
  return {
    id,
    groupId: null,
    kind: kind as Asset["kind"],
    name,
    host: "10.0.0.1",
    port: 22,
    username: "root",
    authKind: null,
    keyPath: null,
    credId: null,
    options: {},
    tags: "",
    note: "",
    sort: 0,
    createdAt: 1,
    updatedAt: 1,
    deletedAt: null,
    builtin: false,
  };
}

const ASSETS = [asset("asset-1", "生产 Web"), asset("asset-2", "数据库", "mysql")];

let view: MountedView | null = null;

function rowFor(name: string): HTMLElement {
  const rows = [...view!.container.querySelectorAll<HTMLElement>("div.rounded-lg")];
  const row = rows.find((r) => r.textContent?.includes(name));
  if (!row) throw new Error(`row not found: ${name}`);
  return row;
}

async function mountPanel(): Promise<void> {
  view = mount(createElement(GrantPanel, { onClose: mocks.onClose }));
  await flushUntil(() => view!.container.querySelectorAll("div.rounded-lg").length > 0);
}

describe("GrantPanel 设备长期授权管理", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.demo = false;
    mocks.assetList.mockResolvedValue(ASSETS);
    mocks.grantList.mockResolvedValue([]);
    mocks.grantSet.mockImplementation((deviceId: string, kinds: string[]) =>
      Promise.resolve({ deviceId, kinds, updatedAt: 1 }),
    );
    mocks.ask.mockResolvedValue(true);
    useUi.setState({ pushToast: mocks.toast });
  });

  it("默认全部关闭，并说明授权是安装级而非按用户隔离", async () => {
    await mountPanel();
    const text = view!.container.textContent ?? "";
    expect(text).toContain("默认全部关闭");
    expect(text).toContain("不是按用户隔离");
    expect(text).toContain("对该安装的所有用户生效");
    expect(text).toContain("原本要逐次确认");
    expect(text).toContain("只读模式下这两类常规操作也会被放开");
    expect(text).toContain("授权不覆盖拦截规则命中或拿不准的操作");
    for (const name of ["生产 Web", "数据库"]) {
      expect(rowFor(name).textContent).toContain("未授权");
    }
    expect(view!.container.querySelectorAll("button").length).toBeGreaterThan(0);
    expect(text).not.toContain("已授权：");
  });

  it("开启授权前弹出显式确认，确认后按所选种类调用授权", async () => {
    await mountPanel();
    clickButton(rowFor("生产 Web"), "开启授权");
    await flush();
    expect(mocks.ask).toHaveBeenCalledOnce();
    const message = mocks.ask.mock.calls[0][0] as string;
    expect(message).toContain("生产 Web");
    expect(message).toContain("不是按用户隔离");
    expect(message).toContain("只读模式下也会被放开");
    expect(message).toContain("拦截规则命中或拿不准的仍会问你");
    expect(mocks.grantSet).toHaveBeenCalledWith("asset-1", ["terminal_write"]);
    await flushUntil(() => rowFor("生产 Web").textContent?.includes("已授权：终端写入") === true);
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("已开启"));
  });

  it("可勾选命令执行后一并授权", async () => {
    await mountPanel();
    const row = rowFor("生产 Web");
    clickButton(row, "○ 命令执行");
    await flush();
    clickButton(rowFor("生产 Web"), "开启授权");
    await flush();
    expect(mocks.grantSet).toHaveBeenCalledWith("asset-1", ["terminal_write", "session_exec"]);
  });

  it("取消确认则不发起授权", async () => {
    mocks.ask.mockResolvedValue(false);
    await mountPanel();
    clickButton(rowFor("生产 Web"), "开启授权");
    await flush();
    expect(mocks.grantSet).not.toHaveBeenCalled();
    expect(rowFor("生产 Web").textContent).toContain("未授权");
  });

  it("撤销授权前弹出显式确认，确认后恢复未授权并提示恢复逐次确认", async () => {
    mocks.grantList.mockResolvedValue([{ deviceId: "asset-1", kinds: ["terminal_write"], updatedAt: 1 }]);
    await mountPanel();
    expect(rowFor("生产 Web").textContent).toContain("已授权：终端写入");
    clickButton(rowFor("生产 Web"), "撤销");
    await flushUntil(() => mocks.grantRevoke.mock.calls.length > 0);
    expect(mocks.ask).toHaveBeenCalledOnce();
    const message = mocks.ask.mock.calls[0][0] as string;
    expect(message).toContain("生产 Web");
    expect(message).toContain("恢复逐次确认");
    expect(mocks.grantRevoke).toHaveBeenCalledWith("asset-1");
    await flushUntil(() => rowFor("生产 Web").textContent?.includes("未授权") === true);
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("恢复逐次确认"));
  });

  it("取消撤销确认则不发起撤销", async () => {
    mocks.ask.mockResolvedValue(false);
    mocks.grantList.mockResolvedValue([{ deviceId: "asset-1", kinds: ["terminal_write"], updatedAt: 1 }]);
    await mountPanel();
    clickButton(rowFor("生产 Web"), "撤销");
    await flush();
    expect(mocks.grantRevoke).not.toHaveBeenCalled();
    expect(rowFor("生产 Web").textContent).toContain("已授权：终端写入");
  });

  it("列表加载失败展示错误并可重试", async () => {
    mocks.grantList.mockRejectedValueOnce(new Error("设备授权未配置"));
    view = mount(createElement(GrantPanel, { onClose: mocks.onClose }));
    await flushUntil(() => view!.container.textContent?.includes("加载失败") === true);
    expect(view!.container.textContent).toContain("设备授权未配置");
    clickButton(view!.container, "重试");
    await flushUntil(() => view!.container.querySelectorAll("div.rounded-lg").length > 0);
    expect(rowFor("生产 Web").textContent).toContain("未授权");
  });

  it("开启失败提示错误且保持未授权", async () => {
    mocks.grantSet.mockRejectedValue(new Error("设备授权种类仅支持 terminal_write/session_exec"));
    await mountPanel();
    clickButton(rowFor("生产 Web"), "开启授权");
    await flush();
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("开启授权失败"));
    expect(rowFor("生产 Web").textContent).toContain("未授权");
  });

  it("demo 下不渲染也不派发 IPC", async () => {
    mocks.demo = true;
    view = mount(createElement(GrantPanel, { onClose: mocks.onClose }));
    await flush();
    expect(view!.container.innerHTML).toBe("");
    expect(mocks.assetList).not.toHaveBeenCalled();
    expect(mocks.grantList).not.toHaveBeenCalled();
  });
});
