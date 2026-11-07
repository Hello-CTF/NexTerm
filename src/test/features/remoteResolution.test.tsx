/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";

const mocks = vi.hoisted(() => ({
  prepare: vi.fn(),
  releaseSecret: vi.fn(),
  assetGet: vi.fn(),
  createRdpClient: vi.fn(),
  createVncClient: vi.fn(),
  prompt: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    assetApi: { get: mocks.assetGet },
    wakeApi: { wake: vi.fn() },
    remoteDesktopApi: {
      prepare: mocks.prepare,
      releaseSecret: mocks.releaseSecret,
      setPassword: vi.fn(),
      certificate: vi.fn(),
      trustCertificate: vi.fn(),
    },
  });
});
vi.mock("../../features/remote/RdpClient", () => ({ createRdpClient: mocks.createRdpClient }));
vi.mock("../../features/remote/VncClient", () => ({ createVncClient: mocks.createVncClient }));
vi.mock("../../app/store", () => ({ requestCloseTab: vi.fn(), useUi: { getState: () => ({ pushToast: vi.fn() }) } }));
vi.mock("../../app/shell/shellMode", () => ({ isTouchShell: () => false }));
vi.mock("../../ui/dialogs", () => ({ promptText: mocks.prompt, ask: vi.fn() }));

import { RemoteDesktopPane } from "../../features/remote/RemoteDesktopPane";
import { resetKeyboardCaptureForTest } from "../../app/inputCapture";
import { resetRemoteStatusForTest } from "../../features/remote/remoteStatus";
import type { ClientCallbacks, ClientOptions, RemoteClient } from "../../features/remote/remoteTypes";
import { flush, mount, type MountedView } from "./reactTestUtils";

type Outcome = "applied" | "pending" | "unavailable" | "failed";

interface FakeClient extends RemoteClient {
  callbacks: ClientCallbacks;
  options: ClientOptions;
  resize: RemoteClient["resize"] & ReturnType<typeof vi.fn>;
}

let size = { width: 1920, height: 1080 };
let resizeOutcome: Outcome = "applied";
let clients: FakeClient[] = [];
let view: MountedView | undefined;

function fakeClient(protocol: "rdp" | "vnc", options: ClientOptions, callbacks: ClientCallbacks): FakeClient {
  return {
    protocol,
    supportsResize: protocol === "rdp",
    callbacks,
    options,
    connect: vi.fn(async () => callbacks.onConnected()),
    disconnect: vi.fn(),
    setScale: vi.fn(),
    setViewOnly: vi.fn(),
    resize: vi.fn(() => resizeOutcome),
    focus: vi.fn(),
    blur: vi.fn(),
    sendKey: vi.fn(),
    sendCombo: vi.fn(async () => undefined),
    sendText: vi.fn(),
    releaseAllKeys: vi.fn(),
    sendClipboard: vi.fn(async () => true),
    fetchClipboard: vi.fn(async () => true),
    setForeground: vi.fn(),
    inputElement: () => null,
    thumbnail: () => null,
    desktopSize: () => size,
  } as unknown as FakeClient;
}

function arrange(kind: "rdp" | "vnc", options: Record<string, unknown>) {
  mocks.assetGet.mockResolvedValue({ id: "asset-1", kind, name: "box", options });
  mocks.prepare.mockImplementation(async () => ({
    ticket: "t",
    protocol: kind,
    assetId: "asset-1",
    name: "box",
    host: "10.0.0.5",
    port: kind === "rdp" ? 3389 : 5900,
    username: "u",
    domain: "",
    promptPassword: false,
    hasPassword: true,
    width: typeof options.width === "number" ? options.width : undefined,
    height: typeof options.height === "number" ? options.height : undefined,
    expiresAtMs: Date.now() + 60_000,
  }));
  mocks.releaseSecret.mockResolvedValue({ username: "u", domain: "", password: "p" });
  const make = (protocol: "rdp" | "vnc") => async (_stage: HTMLElement, o: ClientOptions, c: ClientCallbacks) => {
    const client = fakeClient(protocol, o, c);
    clients.push(client);
    return client;
  };
  mocks.createRdpClient.mockImplementation(make("rdp"));
  mocks.createVncClient.mockImplementation(make("vnc"));
}

beforeEach(() => {
  size = { width: 1920, height: 1080 };
  resizeOutcome = "applied";
  clients = [];
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
});

afterEach(() => {
  view?.unmount();
  view = undefined;
  resetKeyboardCaptureForTest();
  resetRemoteStatusForTest();
  vi.useRealTimers();
  vi.clearAllMocks();
});

async function mountPane() {
  view = mount(createElement(RemoteDesktopPane, { tabId: "tab-1", assetId: "asset-1", visible: true }));
  await flush();
  await flush();
}

const toolbar = () => document.querySelector<HTMLElement>("[data-testid='remote-toolbar']")!;
const toolbarButton = (name: string) => [...toolbar().querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === name)!;
const menuItem = (label: string) => [...document.querySelectorAll<HTMLElement>("[role='menuitem']")].find((e) => e.textContent?.includes(label))!;
const notice = () => document.querySelector<HTMLElement>("[data-testid='remote-resolution-notice']");

function pick(label: string) {
  act(() => toolbarButton("分辨率").click());
  act(() => menuItem(label).click());
}

describe("RDP resolution in the pane", () => {
  it("starts at the asset's fixed size and does not ask the server again for the same size", async () => {
    arrange("rdp", { width: 1920, height: 1080 });
    await mountPane();
    expect(clients[0].options.initialSize).toEqual({ width: 1920, height: 1080 });
    expect(clients[0].resize).not.toHaveBeenCalled();
  });

  it("applies a size picked on the toolbar live", async () => {
    arrange("rdp", { width: 1920, height: 1080 });
    await mountPane();
    pick("1280 x 720");
    await flush();
    expect(clients[0].resize).toHaveBeenCalledWith(1280, 720);
    expect(notice()).toBeNull();
  });

  it("tells the user when the server cannot resize live, and reconnects at the picked size", async () => {
    arrange("rdp", { width: 1920, height: 1080 });
    await mountPane();
    resizeOutcome = "unavailable";
    pick("1366 x 768");
    await flush();
    expect(notice()?.textContent).toContain("不支持在线调整分辨率");
    // The reconnected session comes up at the requested size.
    size = { width: 1366, height: 768 };
    act(() => [...notice()!.querySelectorAll("button")].find((b) => b.textContent === "重新连接")!.click());
    await flush();
    await flush();
    expect(clients).toHaveLength(2);
    expect(clients[1].options.initialSize).toEqual({ width: 1366, height: 768 });
    expect(notice()).toBeNull();
  });

  it("retries while the Display Control channel is still opening", async () => {
    vi.useFakeTimers();
    arrange("rdp", { width: 1920, height: 1080 });
    await mountPane();
    resizeOutcome = "pending";
    pick("1600 x 900");
    expect(clients[0].resize).toHaveBeenCalledTimes(1);
    resizeOutcome = "applied";
    await act(async () => {
      await vi.advanceTimersByTimeAsync(700);
    });
    expect(clients[0].resize).toHaveBeenCalledTimes(2);
    expect(notice()).toBeNull();
  });

  it("never applies a stale retry after the target changed or the pane unmounted", async () => {
    vi.useFakeTimers();
    arrange("rdp", { width: 1920, height: 1080 });
    await mountPane();
    resizeOutcome = "pending";
    pick("1600 x 900");
    pick("1280 x 720");
    expect(clients[0].resize.mock.calls).toEqual([[1600, 900], [1280, 720]]);
    view!.unmount();
    view = undefined;
    resizeOutcome = "applied";
    await vi.advanceTimersByTimeAsync(5000);
    expect(clients[0].resize).toHaveBeenCalledTimes(2);
  });

  it("takes a custom size from the prompt and re-asks on bad input", async () => {
    arrange("rdp", {});
    await mountPane();
    mocks.prompt.mockResolvedValueOnce("50x50").mockResolvedValueOnce("1500 x 800");
    act(() => toolbarButton("分辨率").click());
    act(() => menuItem("自定义").click());
    await flush();
    await flush();
    expect(mocks.prompt).toHaveBeenCalledTimes(2);
    expect(String(mocks.prompt.mock.calls[1][0])).toContain("分辨率不能小于");
    expect(clients[0].resize).toHaveBeenCalledWith(1500, 800);
  });

  it("starts a window-following session at an even size", async () => {
    arrange("rdp", {});
    await mountPane();
    const { width, height } = clients[0].options.initialSize!;
    expect(width % 2).toBe(0);
    expect(height % 2).toBe(0);
  });
});

describe("failure overlay", () => {
  it("shows a readable title for a CredSSP rejection and folds the raw text under details", async () => {
    arrange("rdp", {});
    await mountPane();
    const raw = "[CredSSP @ /home/runner/work/IronRDP/IronRDP/crates/ironrdp-async/src/connector.rs:107] CredSSP: InvalidToken: status is NSTATUS code 0xc00700ea";
    act(() => {
      clients[0].callbacks.onDisconnected({ protocol: "rdp", stage: "closed", wasConnected: true, message: "CredSSP: InvalidToken", detail: raw, rdpKind: 0 });
    });
    const overlay = document.querySelector<HTMLElement>("[data-testid='remote-disconnect-overlay']")!;
    expect(overlay.textContent).toContain("登录被拒绝");
    expect(overlay.textContent).toContain("这类失败不会自动重试");
    const detail = overlay.querySelector("details")!;
    expect(detail.querySelector("summary")?.textContent).toBe("详情");
    expect(detail.querySelector("pre")?.textContent).toContain("0xc00700ea");
    const headline = overlay.querySelector("#nx-remote-overlay-title")!.textContent;
    expect(headline).not.toContain("runner");
  });
});

describe("VNC resolution", () => {
  it("says the server decides and offers nothing to pick", async () => {
    arrange("vnc", {});
    await mountPane();
    act(() => toolbarButton("分辨率").click());
    expect(menuItem("VNC 的分辨率由服务器决定").textContent).toContain("1920 x 1080");
    expect(document.querySelectorAll("[role='menuitem']")).toHaveLength(1);
  });
});
