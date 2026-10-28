/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import type { Asset } from "../../ipc/commands";
import { click, clickButton, flush, flushUntil, mount, setInputValue, setSelectValue, type MountedView } from "./reactTestUtils";

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

const ASSETS_WITH_MANY = [
  ...ASSETS,
  asset("asset-3", "dev-k1"),
  asset("asset-4", "dev-m4"),
  asset("asset-5", "oirush-be"),
];

function text(): string {
  return view!.container.textContent ?? "";
}

function checkbox(label: string): HTMLInputElement {
  const input = view!.container.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`);
  if (!input) throw new Error(`checkbox not found: ${label}`);
  return input;
}

async function toggle(label: string): Promise<void> {
  click(checkbox(label));
  await flush();
  await flush();
}

async function mountPanel(currentAssetId?: string): Promise<void> {
  view = mount(createElement(GrantPanel, { currentAssetId }));
  await flushUntil(() => !text().includes("读取授权…"));
  await flush();
}

afterEach(async () => {
  await flush();
  view?.unmount();
  view = null;
});

beforeEach(() => {
  vi.clearAllMocks();
  mocks.web = false;
  mocks.assetList.mockResolvedValue(ASSETS_WITH_MANY);
  mocks.fleetDevices.mockResolvedValue({ devices: [] });
  mocks.grantList.mockResolvedValue([]);
  mocks.ruleList.mockResolvedValue([]);
  mocks.grantSet.mockImplementation((deviceId: string, kinds: string[]) => Promise.resolve({ deviceId, kinds, updatedAt: 1 }));
  mocks.grantRevoke.mockResolvedValue(undefined);
  mocks.ruleRevoke.mockResolvedValue(undefined);
  mocks.ask.mockResolvedValue(true);
  useUi.setState({ pushToast: mocks.toast });
  useAuth.setState({ user: null });
});

describe("GrantPanel lists only what matters", () => {
  it("renders when the backend returns null lists", async () => {
    mocks.fleetDevices.mockResolvedValue({ devices: null });
    mocks.grantList.mockResolvedValue(null);
    mocks.ruleList.mockResolvedValue(null);
    await mountPanel("asset-1");
    expect(text()).toContain("这台主机");
    expect(text()).not.toContain("读取授权失败");
  });

  it("shows the current host and authorized hosts, never every asset", async () => {
    mocks.grantList.mockResolvedValue([{ deviceId: "asset-4", kinds: ["session_exec"], updatedAt: 1 }]);
    await mountPanel("asset-1");
    expect(text()).toContain("这台主机");
    expect(text()).toContain("生产 Web");
    expect(text()).toContain("dev-m4");
    expect(text()).toContain("执行命令");
    expect(text()).not.toContain("dev-k1");
    expect(text()).not.toContain("oirush-be");
  });

  it("finds another host through search and grants it", async () => {
    await mountPanel();
    expect(text()).toContain("没有其他主机开启免确认授权");
    const search = view!.container.querySelector<HTMLInputElement>('input[aria-label="搜索要授权的主机"]')!;
    setInputValue(search, "oirush");
    await flush();
    expect(text()).not.toContain("dev-k1");
    expect(view!.container.querySelector('[role="option"], [role="listbox"]')).toBeNull();
    clickButton(view!.container, "oirush-beSSH");
    await flush();
    const cancel = view!.container.querySelector<HTMLButtonElement>('button[aria-label="取消选择 oirush-be"]');
    expect(cancel).not.toBeNull();
    await toggle("oirush-be：执行命令免确认");
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.grantSet).toHaveBeenCalledWith("asset-5", ["session_exec"]);
    expect(text()).toContain("已授权主机1");
  });

  it("does not offer revoked fleet devices and keys devices by device/<id>", async () => {
    mocks.fleetDevices.mockResolvedValue({
      devices: [
        { id: "d1", name: "edge-live", revoked_at: 0 },
        { id: "d2", name: "edge-gone", revoked_at: 5 },
      ],
    });
    await mountPanel("device/d1");
    expect(text()).toContain("edge-live");
    await toggle("edge-live：执行命令免确认");
    expect(mocks.grantSet).toHaveBeenCalledWith("device/d1", ["session_exec"]);
    const search = view!.container.querySelector<HTMLInputElement>('input[aria-label="搜索要授权的主机"]')!;
    setInputValue(search, "edge");
    await flush();
    expect(text()).not.toContain("edge-gone");
  });
});

describe("GrantPanel switches", () => {
  it("asks before enabling and does nothing when canceled", async () => {
    mocks.ask.mockResolvedValueOnce(false);
    await mountPanel("asset-1");
    await toggle("生产 Web：执行命令免确认");
    expect(mocks.ask.mock.calls[0][0]).toContain("即使在只读模式下也会放行");
    expect(mocks.grantSet).not.toHaveBeenCalled();
  });

  it("adds kinds one at a time and keeps the others when one is turned off", async () => {
    await mountPanel("asset-1");
    await toggle("生产 Web：执行命令免确认");
    await toggle("生产 Web：终端输入免确认");
    expect(mocks.grantSet).toHaveBeenLastCalledWith("asset-1", ["session_exec", "terminal_write"]);
    mocks.ask.mockClear();
    await toggle("生产 Web：执行命令免确认");
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.grantSet).toHaveBeenLastCalledWith("asset-1", ["terminal_write"]);
  });

  it("revokes the grant when the last kind is turned off, without asking", async () => {
    mocks.grantList.mockResolvedValue([{ deviceId: "asset-1", kinds: ["session_exec"], updatedAt: 1 }]);
    await mountPanel("asset-1");
    expect(checkbox("生产 Web：执行命令免确认").checked).toBe(true);
    await toggle("生产 Web：执行命令免确认");
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.grantRevoke).toHaveBeenCalledWith("asset-1");
    expect(checkbox("生产 Web：执行命令免确认").checked).toBe(false);
  });

  it("offers only database queries for database assets and no path rules", async () => {
    await mountPanel("asset-2");
    expect(view!.container.querySelector('input[aria-label="数据库：数据库查询免确认"]')).not.toBeNull();
    expect(view!.container.querySelector('input[aria-label="数据库：执行命令免确认"]')).toBeNull();
    expect(text()).not.toContain("添加路径规则");
    await toggle("数据库：数据库查询免确认");
    expect(mocks.grantSet).toHaveBeenCalledWith("asset-2", ["db_query"]);
  });

  it("surfaces a failed grant and leaves the switch off", async () => {
    mocks.grantSet.mockRejectedValueOnce(new Error("资产不属于当前账号"));
    await mountPanel("asset-1");
    await toggle("生产 Web：执行命令免确认");
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("资产不属于当前账号"));
    expect(checkbox("生产 Web：执行命令免确认").checked).toBe(false);
  });
});

describe("GrantPanel rules", () => {
  it("lists rules with their expiry and revokes one without asking", async () => {
    const now = Date.now();
    mocks.ruleList.mockResolvedValue([
      { id: "r1", deviceId: "asset-1", action: "edit_file", path: "/etc/nginx/**", expiresAt: 0, createdAt: 1 },
      { id: "r2", deviceId: "asset-1", action: "exec_commands", path: "", expiresAt: now - 1000, createdAt: 1 },
    ]);
    await mountPanel("asset-1");
    expect(text()).toContain("修改文件 /etc/nginx/**");
    expect(text()).toContain("永久");
    expect(text()).toContain("已过期");
    click(view!.container.querySelector<HTMLButtonElement>('button[aria-label="撤销规则：修改文件 /etc/nginx/**"]')!);
    await flush();
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.ruleRevoke).toHaveBeenCalledWith("r1");
  });

  it("confirms scope and a one-day default expiry before adding a rule", async () => {
    mocks.ruleSet.mockImplementation((deviceId: string, action: string, path: string, expiresAt: number) =>
      Promise.resolve({ id: "r9", deviceId, action, path, expiresAt, createdAt: 1 }),
    );
    await mountPanel("asset-1");
    clickButton(view!.container, "添加路径规则");
    await flush();
    const add = [...view!.container.querySelectorAll("button")].find((b) => b.textContent === "添加")!;
    expect(add.disabled).toBe(true);
    setInputValue(view!.container.querySelector<HTMLInputElement>('input[aria-label="规则路径范围"]')!, "/var/www/**");
    await flush();
    click(add);
    await flush();
    await flush();
    expect(mocks.ask.mock.calls[0][0]).toContain("范围：修改文件 /var/www/**");
    expect(mocks.ask.mock.calls[0][0]).toContain("有效期：1 天");
    const [, action, path, expiresAt] = mocks.ruleSet.mock.calls[0];
    expect([action, path]).toEqual(["edit_file", "/var/www/**"]);
    expect(expiresAt).toBeGreaterThan(Date.now());
    expect(text()).toContain("修改文件 /var/www/**");
  });

  it("does not need a path for command rules", async () => {
    mocks.ruleSet.mockImplementation((deviceId: string, action: string, path: string, expiresAt: number) =>
      Promise.resolve({ id: "r8", deviceId, action, path, expiresAt, createdAt: 1 }),
    );
    await mountPanel("asset-1");
    clickButton(view!.container, "添加路径规则");
    await flush();
    setSelectValue(view!.container.querySelector<HTMLSelectElement>('select[aria-label="规则动作"]')!, "exec_commands");
    await flush();
    expect(view!.container.querySelector('input[aria-label="规则路径范围"]')).toBeNull();
    click([...view!.container.querySelectorAll("button")].find((b) => b.textContent === "添加")!);
    await flush();
    await flush();
    expect(mocks.ruleSet.mock.calls[0].slice(0, 3)).toEqual(["asset-1", "exec_commands", ""]);
  });

  it("revokes a host's grant and every rule together", async () => {
    mocks.grantList.mockResolvedValue([{ deviceId: "asset-4", kinds: ["session_exec", "terminal_write"], updatedAt: 1 }]);
    mocks.ruleList.mockResolvedValue([{ id: "r1", deviceId: "asset-4", action: "edit_file", path: "/etc/**", expiresAt: 0, createdAt: 1 }]);
    await mountPanel();
    expect(text()).toContain("执行命令、终端输入、1 条规则");
    clickButton(view!.container, "撤销");
    await flush();
    await flush();
    expect(mocks.grantRevoke).toHaveBeenCalledWith("asset-4");
    expect(mocks.ruleRevoke).toHaveBeenCalledWith("r1");
    expect(text()).not.toContain("dev-m4");
  });

  it("renders the original id when a rule references an unknown host", async () => {
    mocks.ruleList.mockResolvedValue([{ id: "r1", deviceId: "device/gone", action: "exec_commands", path: "", expiresAt: 0, createdAt: 1 }]);
    await mountPanel();
    expect(text()).toContain("device/gone");
  });
});

describe("GrantPanel scope and loading", () => {
  it("states the instance-wide scope without an account and the account scope with one", async () => {
    await mountPanel();
    expect(text()).toContain("对这个 NexTerm 实例的所有会话生效");
    view!.unmount();
    useAuth.setState({ user: accountOf("user") });
    await mountPanel();
    expect(text()).toContain("只对当前账号生效");
  });

  it("shows load errors and retries", async () => {
    mocks.grantList.mockRejectedValueOnce(new Error("network down"));
    await mountPanel();
    expect(text()).toContain("无法读取授权：network down");
    clickButton(view!.container, "重试");
    await flushUntil(() => text().includes("已授权主机"));
  });

  it("keeps asset targets when fleet devices fail to load", async () => {
    mocks.fleetDevices.mockRejectedValue(new Error("fleet offline"));
    await mountPanel("asset-1");
    expect(text()).toContain("生产 Web");
    expect(text()).not.toContain("授权加载失败");
  });
});

describe("GrantPanel records", () => {
  it("lists only current grants and offers no legacy section or clear action", async () => {
    mocks.grantList.mockResolvedValue([{ deviceId: "asset-1", kinds: ["session_exec"], permanent: true, updatedAt: 1 }]);
    await mountPanel("asset-1");
    expect(checkbox("生产 Web：执行命令免确认").checked).toBe(true);
    expect(text()).not.toContain("旧格式");
    expect(text()).not.toContain("1.3.0");
    expect(view!.container.querySelector('[aria-label="旧格式授权"]')).toBeNull();
    expect(Array.from(view!.container.querySelectorAll("button")).some((b) => b.textContent === "清除")).toBe(false);
    await toggle("生产 Web：终端输入免确认");
    expect(mocks.ask.mock.calls[0][0]).not.toContain("替换");
  });
});
