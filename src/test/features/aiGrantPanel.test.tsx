/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import type { Asset } from "../../ipc/commands";
import { clickButton, flush, flushUntil, mount, setInputValue, setSelectValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  web: false,
  assetList: vi.fn(),
  fleetDevices: vi.fn(),
  grantList: vi.fn(),
  grantSet: vi.fn(),
  grantRevoke: vi.fn(),
  ruleList: vi.fn(),
  ruleSet: vi.fn(),
  ruleRevoke: vi.fn(),
  ask: vi.fn(),
  toast: vi.fn(),
  onClose: vi.fn(),
}));

vi.mock("../../features/fleet/clients", () => ({
  fleetClient: () => ({ devices: mocks.fleetDevices }),
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
  grantApi: {
    list: mocks.grantList,
    set: mocks.grantSet,
    revoke: mocks.grantRevoke,
    ruleList: mocks.ruleList,
    ruleSet: mocks.ruleSet,
    ruleRevoke: mocks.ruleRevoke,
  },
}));
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask };
});
vi.mock("../../ipc/env", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/env")>();
  return { ...actual, get WEB() { return mocks.web; } };
});

import { GrantPanel } from "../../features/ai/GrantPanel";
import { useUi } from "../../app/store";
import { useAuth } from "../../features/auth/store";
import type { AccountUser } from "../../ipc/authApi";

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

function accountOf(role: AccountUser["role"]): AccountUser {
  return {
    id: "u-1",
    username: "alice",
    display_name: "",
    role,
    state: "active",
    must_change_password: false,
    mfa_enabled: false,
    created_at: 1,
    updated_at: 1,
    last_login_at: 0,
  };
}

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
  await flush();
}

afterEach(async () => {
  await flush();
  view?.unmount();
  view = null;
});

describe("GrantPanel long-term device grants", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.web = false;
    mocks.assetList.mockResolvedValue(ASSETS);
    mocks.fleetDevices.mockResolvedValue({ devices: [] });
    mocks.grantList.mockResolvedValue([]);
    mocks.ruleList.mockResolvedValue([]);
    mocks.grantSet.mockImplementation((deviceId: string, kinds: string[]) =>
      Promise.resolve({ deviceId, kinds, updatedAt: 1 }),
    );
    mocks.ask.mockResolvedValue(true);
    useUi.setState({ pushToast: mocks.toast });
    useAuth.setState({ user: null });
  });

  it("disables everything by default and explains the shared instance-scoped grant semantics", async () => {
    await mountPanel();
    const text = view!.container.textContent ?? "";
    expect(text).toContain("默认关闭");
    expect(text).toContain("桌面和明确开放部署使用共享授权");
    expect(text).toContain("对当前 NexTerm 实例的所有会话生效");
    expect(text).toContain("普通终端写入和命令执行无需逐次确认");
    expect(text).toContain("只读模式下也会放行");
    expect(text).toContain("高危或无法判断的操作仍按当前权限模式处理");
    expect(text).toContain("禁止操作始终拒绝");
    expect(text).toContain("请仅用于可信设备");
    for (const name of ["生产 Web"]) {
      expect(rowFor(name).textContent).toContain("未授权");
    }
    expect(view!.container.querySelectorAll("button").length).toBeGreaterThan(0);
    expect(text).not.toContain("已授权：");
  });

  it("hides grant controls for database assets and excludes them from rule device options", async () => {
    await mountPanel();
    expect(() => rowFor("数据库")).toThrow();
    const text = view!.container.textContent ?? "";
    expect(text).toContain("数据库资产不支持设备授权");
    expect(text).not.toContain("已授权：");
    clickButton(view!.container, "添加规则");
    await flush();
    const options = [...view!.container.querySelector<HTMLSelectElement>("select[aria-label='规则设备']")!.options].map(
      (o) => o.textContent,
    );
    expect(options).toContain("生产 Web");
    expect(options).not.toContain("数据库");
  });

  it("requires explicit confirmation before granting and grants the selected kinds", async () => {
    await mountPanel();
    clickButton(rowFor("生产 Web"), "开启授权");
    await flush();
    expect(mocks.ask).toHaveBeenCalledOnce();
    const message = mocks.ask.mock.calls[0][0] as string;
    expect(message).toContain("生产 Web");
    expect(message).toContain("对当前 NexTerm 实例的所有会话生效");
    expect(message).toContain("只读模式下也会放行");
    expect(message).toContain("无需逐次确认");
    expect(message).toContain("高危或无法判断的操作仍按当前权限模式处理");
    expect(message).toContain("禁止操作始终拒绝");
    expect(mocks.grantSet).toHaveBeenCalledWith("asset-1", ["terminal_write"]);
    await flushUntil(() => rowFor("生产 Web").textContent?.includes("已授权：终端写入") === true);
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("已开启"));
  });

  it("can include command execution in the grant", async () => {
    await mountPanel();
    const row = rowFor("生产 Web");
    clickButton(row, "○ 命令执行");
    await flush();
    clickButton(rowFor("生产 Web"), "开启授权");
    await flush();
    expect(mocks.grantSet).toHaveBeenCalledWith("asset-1", ["terminal_write", "session_exec"]);
  });

  it("does not grant when confirmation is canceled", async () => {
    mocks.ask.mockResolvedValue(false);
    await mountPanel();
    clickButton(rowFor("生产 Web"), "开启授权");
    await flush();
    expect(mocks.grantSet).not.toHaveBeenCalled();
    expect(rowFor("生产 Web").textContent).toContain("未授权");
  });

  it("requires confirmation before revoking and explains the resulting permission-mode behavior", async () => {
    mocks.grantList.mockResolvedValue([{ deviceId: "asset-1", kinds: ["terminal_write"], updatedAt: 1 }]);
    await mountPanel();
    expect(rowFor("生产 Web").textContent).toContain("已授权：终端写入");
    clickButton(rowFor("生产 Web"), "撤销");
    await flushUntil(() => mocks.grantRevoke.mock.calls.length > 0);
    expect(mocks.ask).toHaveBeenCalledOnce();
    const message = mocks.ask.mock.calls[0][0] as string;
    expect(message).toContain("生产 Web");
    expect(message).toContain("将按当前权限模式处理");
    expect(message).toContain("授权记录会从当前 NexTerm 实例删除");
    expect(mocks.grantRevoke).toHaveBeenCalledWith("asset-1");
    await flushUntil(() => rowFor("生产 Web").textContent?.includes("未授权") === true);
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("按当前权限模式处理"));
  });

  it("does not revoke when revocation is canceled", async () => {
    mocks.ask.mockResolvedValue(false);
    mocks.grantList.mockResolvedValue([{ deviceId: "asset-1", kinds: ["terminal_write"], updatedAt: 1 }]);
    await mountPanel();
    clickButton(rowFor("生产 Web"), "撤销");
    await flush();
    expect(mocks.grantRevoke).not.toHaveBeenCalled();
    expect(rowFor("生产 Web").textContent).toContain("已授权：终端写入");
  });

  it("shows list-loading errors and allows retry", async () => {
    mocks.grantList.mockRejectedValueOnce(new Error("设备授权未配置"));
    view = mount(createElement(GrantPanel, { onClose: mocks.onClose }));
    await flushUntil(() => view!.container.textContent?.includes("加载失败") === true);
    expect(view!.container.textContent).toContain("设备授权未配置");
    clickButton(view!.container, "重试");
    await flushUntil(() => view!.container.querySelectorAll("div.rounded-lg").length > 0);
    expect(rowFor("生产 Web").textContent).toContain("未授权");
  });

  it("shows grant failures and leaves the device unauthorized", async () => {
    mocks.grantSet.mockRejectedValue(new Error("设备授权种类仅支持 terminal_write/session_exec"));
    await mountPanel();
    clickButton(rowFor("生产 Web"), "开启授权");
    await flush();
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("开启授权失败"));
    expect(rowFor("生产 Web").textContent).toContain("未授权");
  });
});

describe("GrantPanel authorization rules", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.assetList.mockResolvedValue(ASSETS);
    mocks.fleetDevices.mockResolvedValue({ devices: [] });
    mocks.grantList.mockResolvedValue([]);
    mocks.ruleList.mockResolvedValue([]);
    mocks.ask.mockResolvedValue(true);
    useUi.setState({ pushToast: mocks.toast });
  });

  function ruleRow(scope: string): HTMLElement {
    const rows = [...view!.container.querySelectorAll<HTMLElement>("div.rounded-lg")];
    const row = rows.find((r) => r.textContent?.includes(scope));
    if (!row) throw new Error(`rule row not found: ${scope}`);
    return row;
  }

  it("shows rule scopes and expiration and explicitly marks permanent rules", async () => {
    mocks.ruleList.mockResolvedValue([
      { id: "rule-1", deviceId: "asset-1", action: "write_file", path: "/var/log/**", expiresAt: 0, createdAt: 1, updatedAt: 1 },
      { id: "rule-2", deviceId: "asset-2", action: "exec_commands", path: "", expiresAt: 4102444800000, createdAt: 1, updatedAt: 1 },
    ]);
    await mountPanel();
    const permanent = ruleRow("/var/log/**");
    expect(permanent.textContent).toContain("生产 Web");
    expect(permanent.textContent).toContain("写入文件 /var/log/**");
    expect(permanent.textContent).toContain("永久");
    const expiring = ruleRow("执行命令");
    expect(expiring.textContent).toContain("数据库");
    expect(expiring.textContent).not.toContain("全部路径");
    expect(expiring.textContent).toContain("过期");
  });

  it("requires confirmation before revoking a rule and calls revoke after confirmation", async () => {
    mocks.ruleList.mockResolvedValue([
      { id: "rule-1", deviceId: "asset-1", action: "write_file", path: "/var/log/**", expiresAt: 0, createdAt: 1, updatedAt: 1 },
    ]);
    await mountPanel();
    clickButton(ruleRow("/var/log/**"), "撤销");
    await flushUntil(() => mocks.ruleRevoke.mock.calls.length > 0);
    expect(mocks.ask).toHaveBeenCalledOnce();
    const message = mocks.ask.mock.calls[0][0] as string;
    expect(message).toContain("生产 Web");
    expect(message).toContain("/var/log/**");
    expect(mocks.ruleRevoke).toHaveBeenCalledWith("rule-1");
    await flushUntil(() => view!.container.textContent?.includes("写入文件 /var/log/**") === false);
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("已撤销授权规则"));
  });

  it("confirms scope and expiration before calling ruleSet when adding a rule", async () => {
    mocks.ruleSet.mockImplementation((deviceId: string, action: string, path: string, expiresAt: number) =>
      Promise.resolve({ id: "rule-new", deviceId, action, path, expiresAt, createdAt: 1, updatedAt: 1 }),
    );
    await mountPanel();
    clickButton(view!.container, "添加规则");
    await flush();
    setSelectValue(view!.container.querySelector<HTMLSelectElement>("select[aria-label='规则设备']")!, "asset-1");
    setSelectValue(view!.container.querySelector<HTMLSelectElement>("select[aria-label='规则动作']")!, "write_file");
    setInputValue(view!.container.querySelector<HTMLInputElement>("input[aria-label='规则路径范围']")!, "/var/log/**");
    setSelectValue(view!.container.querySelector<HTMLSelectElement>("select[aria-label='规则有效期']")!, "7d");
    clickButton(view!.container, "添加");
    await flushUntil(() => mocks.ruleSet.mock.calls.length > 0);
    expect(mocks.ask).toHaveBeenCalledOnce();
    const message = mocks.ask.mock.calls[0][0] as string;
    expect(message).toContain("生产 Web");
    expect(message).toContain("写入文件 /var/log/**");
    expect(message).toContain("7 天");
    const [deviceId, action, path, expiresAt] = mocks.ruleSet.mock.calls[0];
    expect(deviceId).toBe("asset-1");
    expect(action).toBe("write_file");
    expect(path).toBe("/var/log/**");
    expect(expiresAt).toBeGreaterThan(Date.now());
    await flushUntil(() => view!.container.textContent?.includes("写入文件 /var/log/**") === true);
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("已添加授权规则"));
  });

  it("disables adding a file-write rule without a path and does not require a path for command rules", async () => {
    await mountPanel();
    clickButton(view!.container, "添加规则");
    await flush();
    setSelectValue(view!.container.querySelector<HTMLSelectElement>("select[aria-label='规则设备']")!, "asset-1");
    const addButton = () => [...view!.container.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === "添加");
    expect(addButton()?.disabled).toBe(true);
    setSelectValue(view!.container.querySelector<HTMLSelectElement>("select[aria-label='规则动作']")!, "exec_commands");
    expect(view!.container.querySelector("input[aria-label='规则路径范围']")).toBeNull();
    expect(addButton()?.disabled).toBe(false);
  });

  it("does not call ruleSet when add confirmation is canceled", async () => {
    mocks.ask.mockResolvedValue(false);
    await mountPanel();
    clickButton(view!.container, "添加规则");
    await flush();
    setSelectValue(view!.container.querySelector<HTMLSelectElement>("select[aria-label='规则设备']")!, "asset-1");
    setInputValue(view!.container.querySelector<HTMLInputElement>("input[aria-label='规则路径范围']")!, "/var/log/**");
    clickButton(view!.container, "添加");
    await flush();
    expect(mocks.ruleSet).not.toHaveBeenCalled();
  });
});

describe("GrantPanel account-scoped grants (web)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.web = true;
    mocks.assetList.mockResolvedValue(ASSETS);
    mocks.fleetDevices.mockResolvedValue({ devices: [] });
    mocks.grantList.mockResolvedValue([{ deviceId: "asset-1", kinds: ["terminal_write"], updatedAt: 1 }]);
    mocks.ruleList.mockResolvedValue([]);
    mocks.grantSet.mockImplementation((deviceId: string, kinds: string[]) =>
      Promise.resolve({ deviceId, kinds, updatedAt: 1 }),
    );
    mocks.ask.mockResolvedValue(true);
    useUi.setState({ pushToast: mocks.toast });
    useAuth.setState({ user: null });
  });

  it("gives a logged-in regular user full control of current-account grants", async () => {
    useAuth.setState({ user: accountOf("user") });
    await mountPanel();

    const text = view!.container.textContent ?? "";
    expect(text).not.toContain("当前账号不是超管");
    expect(text).toContain("授权按账号隔离，仅对当前账号生效");
    expect(rowFor("生产 Web").textContent).toContain("已授权：终端写入");
    const buttons = [...view!.container.querySelectorAll<HTMLButtonElement>("button")].map((b) => b.textContent);
    expect(buttons).toContain("撤销");
    expect(buttons).toContain("添加规则");

    clickButton(rowFor("生产 Web"), "撤销");
    await flushUntil(() => mocks.grantRevoke.mock.calls.length > 0);
    expect(mocks.ask.mock.calls[0][0] as string).toContain("授权记录会从当前账号删除");
    await flushUntil(() => rowFor("生产 Web").textContent?.includes("未授权") === true);

    clickButton(rowFor("生产 Web"), "开启授权");
    await flushUntil(() => mocks.grantSet.mock.calls.length > 0);
    expect(mocks.ask.mock.calls[1][0] as string).toContain("授权按账号隔离，仅对当前账号生效");
    expect(mocks.grantSet).toHaveBeenCalledWith("asset-1", ["terminal_write"]);
    await flushUntil(() => rowFor("生产 Web").textContent?.includes("已授权：终端写入") === true);
  });

  it("saves rules under the current account in account mode", async () => {
    useAuth.setState({ user: accountOf("user") });
    mocks.ruleSet.mockImplementation((deviceId: string, action: string, path: string, expiresAt: number) =>
      Promise.resolve({ id: "rule-new", deviceId, action, path, expiresAt, createdAt: 1, updatedAt: 1 }),
    );
    await mountPanel();
    clickButton(view!.container, "添加规则");
    await flush();
    setSelectValue(view!.container.querySelector<HTMLSelectElement>("select[aria-label='规则设备']")!, "asset-1");
    setSelectValue(view!.container.querySelector<HTMLSelectElement>("select[aria-label='规则动作']")!, "exec_commands");
    clickButton(view!.container, "添加");
    await flushUntil(() => mocks.ruleSet.mock.calls.length > 0);
    const message = mocks.ask.mock.calls[0][0] as string;
    expect(message).toContain("规则保存在当前账号");
    expect(mocks.ruleSet).toHaveBeenCalledWith("asset-1", "exec_commands", "", 0);
  });

  it("states shared instance-wide scope when no account is present", async () => {
    mocks.grantList.mockResolvedValue([]);
    await mountPanel();
    const text = view!.container.textContent ?? "";
    expect(text).toContain("桌面和明确开放部署使用共享授权");
    expect(text).toContain("对当前 NexTerm 实例的所有会话生效");
    expect(text).not.toContain("授权按账号隔离");
    const buttons = [...view!.container.querySelectorAll<HTMLButtonElement>("button")].map((b) => b.textContent);
    expect(buttons).toContain("开启授权");
  });

  it("surfaces the server owner/target rejection for another account's asset and keeps the row unauthorized", async () => {
    useAuth.setState({ user: accountOf("user") });
    mocks.grantList.mockResolvedValue([]);
    mocks.grantSet.mockRejectedValue(new Error("授权资产不存在或不属于当前用户"));
    await mountPanel();
    clickButton(rowFor("生产 Web"), "开启授权");
    await flush();
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("授权资产不存在或不属于当前用户"));
    expect(rowFor("生产 Web").textContent).toContain("未授权");
  });
});


describe("GrantPanel fleet device grant targets", () => {
  const GATEWAY = {
    id: "d-1",
    name: "边缘网关",
    kind: "agent",
    created_at: 1,
    last_seen_at: 1,
    revoked_at: 0,
  };

  beforeEach(() => {
    vi.clearAllMocks();
    mocks.assetList.mockResolvedValue(ASSETS);
    mocks.fleetDevices.mockResolvedValue({ devices: [GATEWAY] });
    mocks.grantList.mockResolvedValue([]);
    mocks.ruleList.mockResolvedValue([]);
    mocks.grantSet.mockImplementation((deviceId: string, kinds: string[]) =>
      Promise.resolve({ deviceId, kinds, updatedAt: 1 }),
    );
    mocks.ask.mockResolvedValue(true);
    useUi.setState({ pushToast: mocks.toast });
    useAuth.setState({ user: null });
  });

  it("uses device/<id> for fleet grant targets, shows a device badge, and omits revoked devices", async () => {
    mocks.fleetDevices.mockResolvedValue({
      devices: [GATEWAY, { ...GATEWAY, id: "d-2", name: "旧网关", revoked_at: 1720000000000 }],
    });
    await mountPanel();
    const row = rowFor("边缘网关");
    expect(row.textContent).toContain("未授权");
    expect(row.textContent).toContain("设备");
    expect(() => rowFor("旧网关")).toThrow();

    clickButton(row, "开启授权");
    await flush();
    expect(mocks.ask).toHaveBeenCalledOnce();
    expect(mocks.ask.mock.calls[0][0] as string).toContain("边缘网关");
    expect(mocks.grantSet).toHaveBeenCalledWith("device/d-1", ["terminal_write"]);
    await flushUntil(() => rowFor("边缘网关").textContent?.includes("已授权：终端写入") === true);
  });

  it("includes fleet devices in rule device options and saves rules with device/<id>", async () => {
    mocks.ruleSet.mockImplementation((deviceId: string, action: string, path: string, expiresAt: number) =>
      Promise.resolve({ id: "rule-new", deviceId, action, path, expiresAt, createdAt: 1, updatedAt: 1 }),
    );
    await mountPanel();
    clickButton(view!.container, "添加规则");
    await flush();
    const select = view!.container.querySelector<HTMLSelectElement>("select[aria-label='规则设备']")!;
    const options = [...select.options].map((o) => o.textContent);
    expect(options).toContain("边缘网关");
    setSelectValue(select, "device/d-1");
    setSelectValue(view!.container.querySelector<HTMLSelectElement>("select[aria-label='规则动作']")!, "exec_commands");
    clickButton(view!.container, "添加");
    await flushUntil(() => mocks.ruleSet.mock.calls.length > 0);
    expect(mocks.ask.mock.calls[0][0] as string).toContain("边缘网关");
    const [deviceId, action, path, expiresAt] = mocks.ruleSet.mock.calls[0];
    expect(deviceId).toBe("device/d-1");
    expect(action).toBe("exec_commands");
    expect(path).toBe("");
    expect(expiresAt).toBe(0);
  });

  it("falls back to asset-only targets without showing a load error when fleet device loading fails", async () => {
    mocks.fleetDevices.mockRejectedValue(new Error("登录服务端账号后可用"));
    await mountPanel();
    expect(rowFor("生产 Web").textContent).toContain("未授权");
    expect(view!.container.textContent ?? "").not.toContain("加载失败");
  });

  it("renders the original id when a rule references an unknown device id", async () => {
    mocks.ruleList.mockResolvedValue([
      { id: "rule-x", deviceId: "device/gone", action: "exec_commands", path: "", expiresAt: 0, createdAt: 1, updatedAt: 1 },
    ]);
    await mountPanel();
    expect(view!.container.textContent ?? "").toContain("device/gone");
  });
});
