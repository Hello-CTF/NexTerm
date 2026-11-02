/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { chooseSelectOption, click, clickButton, flush, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    create: vi.fn(),
    update: vi.fn(),
    groupList: vi.fn(),
    listAssets: vi.fn(),
    listCredentials: vi.fn(),
    setCredential: vi.fn(),
    probe: vi.fn(),
    connect: vi.fn(),
    toast: vi.fn(),
  };
});
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      create: mocks.create,
      update: mocks.update,
      groupList: mocks.groupList,
      groupCreate: vi.fn(),
      groupUpdate: vi.fn(),
      groupDelete: vi.fn(),
      list: mocks.listAssets,
      readKeyFile: vi.fn(),
      auditQuery: vi.fn(),
    },
    vaultApi: {
      listCredentials: mocks.listCredentials,
      setCredential: mocks.setCredential,
    },
    sessionApi: {
      probe: mocks.probe,
      connect: mocks.connect,
    },
    terminalApi: {},
  };
});

vi.mock("../../features/fleet/clients", () => ({
  fleetClient: () => ({
    devices: async () => ({
      devices: [
        { id: "d1", name: "build-box", revoked_at: 0 },
        { id: "d2", name: "old-box", revoked_at: 5 },
      ],
    }),
  }),
}));

import { AssetEditor } from "../../features/explorer/AssetTree";
import { useUi } from "../../app/store";
import type { Asset } from "../../ipc/commands";

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.groupList.mockResolvedValue([]);
  mocks.listAssets.mockResolvedValue([]);
  mocks.listCredentials.mockResolvedValue([]);
  mocks.create.mockResolvedValue({ id: "saved-asset" });
  mocks.update.mockResolvedValue({ id: "saved-asset" });
  mocks.setCredential.mockResolvedValue({ id: "cred-1" });
  useUi.setState({ pushToast: mocks.toast, sessions: [] });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function makeAsset(overrides: Partial<Asset> = {}): Asset {
  return {
    id: "db-1",
    groupId: null,
    kind: "postgres",
    name: "orders-db",
    host: "db.internal",
    port: 5432,
    username: "postgres",
    authKind: "password",
    keyPath: null,
    credId: null,
    options: {},
    tags: "",
    note: "",
    sort: 0,
    createdAt: 0,
    updatedAt: 0,
    deletedAt: null,
    builtin: false,
    ...overrides,
  };
}

function sshAsset(id: string, name: string): Asset {
  return makeAsset({
    id,
    kind: "ssh",
    name,
    host: `${id}.internal`,
    port: 22,
    username: "root",
  });
}

function mountEditor(initial?: Asset) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(AssetEditor, { kind: "asset", initial, onClose: vi.fn(), onSaved: vi.fn() }),
    ),
  );
}

function chooseKind(label: string): void {
  chooseSelectOption(mounted!.container, "类型", label);
}

function chooseVia(label: string): void {
  chooseSelectOption(mounted!.container, "通过跳板连接", label);
}

function fillRequiredFields(name = "orders-db"): void {
  setInputValue(mounted!.container.querySelector("input.nx-input") as HTMLInputElement, name);
  setInputValue(mounted!.container.querySelector('input[placeholder="1.2.3.4"]') as HTMLInputElement, "db.internal");
}

async function save(): Promise<void> {
  const button = clickButton(mounted!.container, "保存");
  await flushUntil(() => button.textContent?.trim() === "保存" && !button.disabled);
}

function createdPayload(): Record<string, unknown> {
  expect(mocks.create).toHaveBeenCalledOnce();
  return mocks.create.mock.calls[0]![0] as Record<string, unknown>;
}

function updatedPayload(): Record<string, unknown> {
  expect(mocks.update).toHaveBeenCalledOnce();
  return mocks.update.mock.calls[0]![0] as Record<string, unknown>;
}


function field(label: string): HTMLInputElement {
  const input = mounted!.container.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`);
  if (!input) throw new Error(`field ${label} not found`);
  return input;
}

describe("AssetEditor remote desktop kinds", () => {
  it("fills in the RDP port and shows the RDP fields without the SSH-only ones", async () => {
    mountEditor();
    chooseKind("远程桌面（RDP）");
    await flush();
    expect(mounted!.container.querySelector<HTMLInputElement>('input[placeholder="1.2.3.4"]')).not.toBeNull();
    expect((mounted!.container.querySelector('input[type="number"]') as HTMLInputElement).value).toBe("3389");
    expect(field("域")).toBeTruthy();
    expect(field("每次连接时输入密码")).toBeTruthy();
    expect(field("启用剪贴板").checked).toBe(true);
    expect(mounted!.container.querySelector('button[aria-label="认证方式"]')).toBeNull();
    expect(mounted!.container.textContent).not.toContain("高级选项");
    expect(mounted!.container.querySelector('button[aria-label="分辨率"]')).not.toBeNull();
  });

  it("uses 5900 for VNC and offers no user name or domain", async () => {
    mountEditor();
    chooseKind("远程桌面（VNC）");
    await flush();
    expect((mounted!.container.querySelector('input[type="number"]') as HTMLInputElement).value).toBe("5900");
    expect(mounted!.container.querySelector('input[aria-label="域"]')).toBeNull();
    expect(mounted!.container.textContent).not.toContain("用户名");
    expect(field("默认只读").checked).toBe(false);
  });

  it("saves an RDP asset with its domain, fixed resolution and options", async () => {
    mountEditor();
    chooseKind("远程桌面（RDP）");
    await flush();
    fillRequiredFields("win-01");
    setInputValue(field("域"), "CORP");
    chooseSelectOption(mounted!.container, "分辨率", "固定分辨率");
    await flush();
    setInputValue(field("宽度"), "1600");
    setInputValue(field("高度"), "900");
    click(field("每次连接时输入密码"));
    await save();
    expect(createdPayload()).toMatchObject({
      kind: "rdp",
      port: 3389,
      options: { domain: "CORP", width: 1600, height: 900, promptPassword: true },
    });
  });

  it("refuses a fixed resolution that is too small", async () => {
    mountEditor();
    chooseKind("远程桌面（RDP）");
    await flush();
    fillRequiredFields("win-01");
    chooseSelectOption(mounted!.container, "分辨率", "固定分辨率");
    await flush();
    setInputValue(field("宽度"), "100");
    clickButton(mounted!.container, "保存");
    await flush();
    expect(mocks.create).not.toHaveBeenCalled();
    expect(mounted!.container.textContent).toContain("分辨率不能小于");
  });

  it("offers SSH assets and devices as the jump and saves the choice", async () => {
    // The asset list holds no devices: they come from the fleet, and revoked ones are not offered.
    mocks.listAssets.mockResolvedValue([sshAsset("ssh-a", "gateway-a")]);
    mountEditor();
    chooseKind("远程桌面（VNC）");
    await flushUntil(() => mounted!.container.querySelector('button[aria-label="通过跳板连接"]') !== null);
    const trigger = mounted!.container.querySelector<HTMLButtonElement>('button[aria-label="通过跳板连接"]')!;
    click(trigger);
    await flushUntil(() => mounted!.container.querySelectorAll('[role="listbox"] [role="option"]').length === 3);
    const labels = [...mounted!.container.querySelectorAll('[role="listbox"] [role="option"]')].map((o) => o.textContent ?? "");
    click(trigger);
    expect(labels.some((label) => label.includes("gateway-a"))).toBe(true);
    expect(labels.some((label) => label.includes("build-box"))).toBe(true);
    expect(labels.some((label) => label.includes("old-box"))).toBe(false);
    chooseVia("build-box");
    fillRequiredFields("box");
    await save();
    expect(createdPayload()).toMatchObject({ kind: "vnc", options: { viaAssetId: "device/d1" } });
  });

  it("keeps unknown options and the jump when an existing RDP asset is edited", async () => {
    mocks.listAssets.mockResolvedValue([sshAsset("ssh-a", "gateway-a")]);
    mountEditor(makeAsset({ kind: "rdp", port: 3389, username: "admin", options: { viaAssetId: "ssh-a", domain: "OLD", futureOption: "keep", icon: "server" } }));
    await flushUntil(() => mounted!.container.querySelector('button[aria-label="通过跳板连接"]')?.textContent?.includes("gateway-a") === true);
    expect(field("域").value).toBe("OLD");
    setInputValue(field("域"), "NEW");
    await save();
    expect(updatedPayload()).toMatchObject({ kind: "rdp", options: { viaAssetId: "ssh-a", domain: "NEW", futureOption: "keep", icon: "server" } });
  });

  it("stores no password when the password is asked at connect time", async () => {
    mountEditor();
    chooseKind("远程桌面（RDP）");
    await flush();
    fillRequiredFields("win-01");
    click(field("每次连接时输入密码"));
    await flush();
    expect(mounted!.container.querySelector('input[aria-label="凭据密码"]')).toBeNull();
    await save();
    expect(mocks.setCredential).not.toHaveBeenCalled();
    expect(createdPayload().credId).toBeNull();
  });
});

describe("AssetEditor wake-on-LAN options", () => {
  it("saves the MAC, broadcast and port on an RDP asset", async () => {
    mountEditor();
    chooseKind("远程桌面（RDP）");
    await flush();
    fillRequiredFields("win-01");
    setInputValue(field("唤醒 MAC 地址"), "AA-BB-CC-DD-EE-01");
    await flush();
    setInputValue(field("唤醒广播地址"), "192.168.1.255");
    setInputValue(field("唤醒端口"), "7");
    await save();
    expect(createdPayload()).toMatchObject({
      kind: "rdp",
      options: { wakeMac: "AA-BB-CC-DD-EE-01", wakeBroadcast: "192.168.1.255", wakePort: 7 },
    });
  });

  it("refuses to save a malformed MAC or broadcast address", async () => {
    mountEditor();
    chooseKind("远程桌面（VNC）");
    await flush();
    fillRequiredFields("box");
    setInputValue(field("唤醒 MAC 地址"), "AABBCCDDEE01");
    clickButton(mounted!.container, "保存");
    await flush();
    expect(mocks.create).not.toHaveBeenCalled();
    expect(mounted!.container.textContent).toContain("MAC 地址格式无效");

    setInputValue(field("唤醒 MAC 地址"), "AA:BB:CC:DD:EE:01");
    await flush();
    setInputValue(field("唤醒广播地址"), "localhost");
    clickButton(mounted!.container, "保存");
    await flush();
    expect(mocks.create).not.toHaveBeenCalled();
    expect(mounted!.container.textContent).toContain("广播地址必须是 IPv4 地址");
  });

  it("removes the wake options when the MAC is cleared and keeps other options", async () => {
    mountEditor(makeAsset({ kind: "rdp", port: 3389, username: "admin", options: { wakeMac: "AA:BB:CC:DD:EE:01", wakePort: 7, futureOption: "keep" } }));
    await flush();
    expect(field("唤醒 MAC 地址").value).toBe("AA:BB:CC:DD:EE:01");
    setInputValue(field("唤醒 MAC 地址"), "");
    await save();
    const options = updatedPayload().options as Record<string, unknown>;
    expect(options.futureOption).toBe("keep");
    expect(options).not.toHaveProperty("wakeMac");
    expect(options).not.toHaveProperty("wakePort");
  });

  it("is not offered for database kinds", async () => {
    mountEditor(makeAsset());
    await flush();
    expect(mounted!.container.querySelector('input[aria-label="唤醒 MAC 地址"]')).toBeNull();
  });
});
