/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";

const mocks = vi.hoisted(() => ({
  prepare: vi.fn(),
  releaseSecret: vi.fn(),
  setPassword: vi.fn(),
  certificate: vi.fn(),
  trustCertificate: vi.fn(),
  assetGet: vi.fn(),
  createVncClient: vi.fn(),
  closeTab: vi.fn(),
  prompt: vi.fn(),
  ask: vi.fn(),
  touch: vi.fn(() => false),
  wake: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    assetApi: { get: mocks.assetGet },
    wakeApi: { wake: mocks.wake },
    remoteDesktopApi: {
      prepare: mocks.prepare,
      releaseSecret: mocks.releaseSecret,
      setPassword: mocks.setPassword,
      certificate: mocks.certificate,
      trustCertificate: mocks.trustCertificate,
    },
  });
});
vi.mock("../../features/remote/VncClient", () => ({ createVncClient: mocks.createVncClient }));
vi.mock("../../app/store", () => ({
  requestCloseTab: mocks.closeTab,
  useUi: { getState: () => ({ pushToast: mocks.toast }) },
}));
vi.mock("../../app/shell/shellMode", () => ({ isTouchShell: () => mocks.touch() }));
vi.mock("../../ui/dialogs", () => ({ promptText: mocks.prompt, ask: mocks.ask }));

import { RemoteDesktopPane } from "../../features/remote/RemoteDesktopPane";
import { isKeyboardCaptured, resetKeyboardCaptureForTest } from "../../app/inputCapture";
import { resetRemoteStatusForTest, useRemoteStatus } from "../../features/remote/remoteStatus";
import type { ClientCallbacks, ClientOptions, EngineFailure, RemoteClient } from "../../features/remote/remoteTypes";
import { RemoteEngineError } from "../../features/remote/remoteTypes";
import { flush, mount, type MountedView } from "./reactTestUtils";

interface FakeClient extends RemoteClient {
  callbacks: ClientCallbacks;
  options: ClientOptions;
}

function fakeClient(options: ClientOptions, callbacks: ClientCallbacks): FakeClient {
  return {
    protocol: "vnc",
    supportsResize: false,
    callbacks,
    options,
    connect: vi.fn(async () => callbacks.onConnected()),
    disconnect: vi.fn(),
    setScale: vi.fn(),
    setViewOnly: vi.fn(),
    resize: vi.fn(),
    focus: vi.fn(),
    blur: vi.fn(),
    sendKey: vi.fn(),
    sendCombo: vi.fn(async () => undefined),
    sendText: vi.fn(),
    releaseAllKeys: vi.fn(),
    sendClipboard: vi.fn(async () => true),
    fetchClipboard: vi.fn(async () => true),
    inputElement: () => null,
    thumbnail: () => null,
    desktopSize: () => ({ width: 800, height: 600 }),
  } as unknown as FakeClient;
}

let ticketCounter = 0;
let view: MountedView | undefined;

beforeEach(() => {
  ticketCounter = 0;
  mocks.assetGet.mockResolvedValue({ id: "asset-1", kind: "vnc", name: "box", options: {} });
  mocks.prepare.mockImplementation(async () => ({
    ticket: `t-${++ticketCounter}`,
    protocol: "vnc",
    assetId: "asset-1",
    name: "box",
    host: "10.0.0.5",
    port: 5900,
    username: "",
    domain: "",
    promptPassword: false,
    hasPassword: true,
    expiresAtMs: Date.now() + 60_000,
  }));
  mocks.createVncClient.mockImplementation(async (_stage: HTMLElement, options: ClientOptions, callbacks: ClientCallbacks) => {
    return fakeClient(options, callbacks);
  });
});

afterEach(() => {
  view?.unmount();
  view = undefined;
  resetKeyboardCaptureForTest();
  resetRemoteStatusForTest();
  vi.useRealTimers();
  vi.clearAllMocks();
});

async function mountPane(): Promise<MountedView> {
  view = mount(createElement(RemoteDesktopPane, { tabId: "tab-1", assetId: "asset-1", visible: true }));
  await flush();
  await flush();
  return view;
}

function unmountPane(): void {
  view?.unmount();
  view = undefined;
}

function pane(): HTMLElement {
  return document.querySelector<HTMLElement>("[data-testid='remote-desktop-pane']")!;
}

describe("RemoteDesktopPane", () => {
  it("connects when it mounts and reports the session as connected", async () => {
    await mountPane();
    expect(mocks.prepare).toHaveBeenCalledTimes(1);
    expect(pane().dataset.status).toBe("connected");
    expect(useRemoteStatus.getState().entries["tab-1"]?.status).toBe("connected");
  });

  it("forgets its status when it unmounts", async () => {
    const mounted = await mountPane();
    mounted.unmount();
    view = undefined;
    expect(useRemoteStatus.getState().entries["tab-1"]).toBeUndefined();
    expect(isKeyboardCaptured()).toBe(false);
  });

  it("captures the keyboard on focus inside the screen and shows the accent border and hint", async () => {
    await mountPane();
    const screen = pane().querySelector<HTMLElement>("[data-captured]")!;
    const holder = screen.querySelector<HTMLElement>("div > div")!;
    const canvas = document.createElement("canvas");
    canvas.tabIndex = 0;
    holder.append(canvas);
    expect(isKeyboardCaptured()).toBe(false);
    act(() => canvas.focus());
    expect(isKeyboardCaptured()).toBe(true);
    expect(screen.dataset.captured).toBe("true");
    expect(document.querySelector("[data-testid='remote-capture-hint']")?.textContent).toContain("Ctrl+Alt+Shift+Esc");
  });

  it("does not capture the keyboard for focus on the toolbar", async () => {
    await mountPane();
    const button = pane().querySelector<HTMLButtonElement>("[data-testid='remote-toolbar'] button")!;
    act(() => button.focus());
    expect(isKeyboardCaptured()).toBe(false);
  });

  it("releases on the release chord without sending it to the remote", async () => {
    await mountPane();
    const screen = pane().querySelector<HTMLElement>("[data-captured]")!;
    const canvas = document.createElement("canvas");
    canvas.tabIndex = 0;
    screen.querySelector("div > div")!.append(canvas);
    act(() => canvas.focus());
    expect(isKeyboardCaptured()).toBe(true);
    const reachedRemote = vi.fn();
    canvas.addEventListener("keydown", reachedRemote);
    act(() => {
      canvas.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", ctrlKey: true, altKey: true, shiftKey: true, bubbles: true, cancelable: true }));
    });
    expect(isKeyboardCaptured()).toBe(false);
    expect(reachedRemote).not.toHaveBeenCalled();
  });

  it("counts down and reconnects with a new ticket after a network drop", async () => {
    vi.useFakeTimers();
    await mountPane();
    const client = mocks.createVncClient.mock.results[0].value as Promise<FakeClient>;
    const first = await client;
    const drop: EngineFailure = { protocol: "vnc", stage: "closed", clean: false, wasConnected: true, message: "与远程桌面的连接已中断" };
    await act(async () => {
      first.callbacks.onDisconnected(drop);
    });
    expect(pane().dataset.status).toBe("disconnected");
    expect(document.querySelector("[data-testid='remote-retry-countdown']")?.textContent).toContain("2 秒");
    expect(mocks.prepare).toHaveBeenCalledTimes(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2100);
    });
    expect(mocks.prepare).toHaveBeenCalledTimes(2);
    expect(mocks.createVncClient.mock.calls.map((call) => (call[1] as ClientOptions).prepared.ticket)).toEqual(["t-1", "t-2"]);
  });

  it("does not auto-reconnect after an auth failure and offers the two buttons", async () => {
    vi.useFakeTimers();
    mocks.createVncClient.mockImplementationOnce(async () => {
      throw new RemoteEngineError({ protocol: "vnc", stage: "security", message: "VNC 认证失败: bad password" });
    });
    mocks.assetGet.mockResolvedValue({ id: "asset-1", kind: "vnc", name: "box", options: {} });
    mocks.prepare.mockImplementation(async () => ({
      ticket: `t-${++ticketCounter}`, protocol: "vnc", assetId: "asset-1", name: "box", host: "h", port: 5900,
      username: "", domain: "", promptPassword: false, hasPassword: true, expiresAtMs: 1,
    }));
    mocks.certificate.mockResolvedValue({ status: "pinned", fingerprint: "x" });
    await mountPane();
    await flush();
    const overlay = document.querySelector("[data-testid='remote-disconnect-overlay']")!;
    expect(overlay.textContent).toContain("认证失败");
    expect(overlay.textContent).toContain("重新连接");
    expect(overlay.textContent).toContain("关闭");
    expect(document.querySelector("[data-testid='remote-retry-countdown']")).toBeNull();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(mocks.prepare).toHaveBeenCalledTimes(1);
  });

  it("reconnects by hand with the reconnect button using a fresh ticket", async () => {
    mocks.createVncClient.mockImplementationOnce(async () => {
      throw new RemoteEngineError({ protocol: "vnc", stage: "security", message: "VNC 认证失败" });
    });
    mocks.certificate.mockResolvedValue({ status: "pinned", fingerprint: "x" });
    await mountPane();
    const reconnect = [...document.querySelectorAll<HTMLButtonElement>("[data-testid='remote-disconnect-overlay'] button")].find((b) => b.textContent === "重新连接")!;
    await act(async () => reconnect.click());
    await flush();
    expect(mocks.prepare).toHaveBeenCalledTimes(2);
    expect(pane().dataset.status).toBe("connected");
  });

  it("offers wake on a failed connect only when the asset has a wake MAC, and says a sent packet is not a woken host", async () => {
    mocks.createVncClient.mockImplementation(async () => {
      throw new RemoteEngineError({ protocol: "vnc", stage: "connect", message: "无法连接到主机" });
    });
    mocks.assetGet.mockResolvedValue({ id: "asset-1", kind: "vnc", name: "box", options: { wakeMac: "AA:BB:CC:DD:EE:01" } });
    mocks.wake.mockResolvedValue({ assetId: "asset-1", route: "local", target: "255.255.255.255:9", mac: "AA:BB:CC:DD:EE:01", bytes: 102, sentAtMs: 1 });
    await mountPane();
    const overlay = document.querySelector("[data-testid='remote-disconnect-overlay']")!;
    expect(overlay.textContent).toContain("不代表主机已经唤醒");
    const wake = [...overlay.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === "唤醒")!;
    await act(async () => wake.click());
    await flush();
    expect(mocks.wake).toHaveBeenCalledWith("asset-1");
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("不代表主机已经唤醒"));
  });

  it("shows a failed wake as an error and hides wake without a MAC or after an auth failure", async () => {
    mocks.createVncClient.mockImplementation(async () => {
      throw new RemoteEngineError({ protocol: "vnc", stage: "connect", message: "无法连接到主机" });
    });
    mocks.assetGet.mockResolvedValue({ id: "asset-1", kind: "vnc", name: "box", options: { wakeMac: "AA:BB:CC:DD:EE:01" } });
    mocks.wake.mockRejectedValue({ code: "unsupported", message: "该设备的 Agent 版本过旧" });
    await mountPane();
    const wake = [...document.querySelectorAll<HTMLButtonElement>("[data-testid='remote-disconnect-overlay'] button")].find((b) => b.textContent === "唤醒")!;
    await act(async () => wake.click());
    await flush();
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("Agent 版本过旧"));
    unmountPane();

    mocks.assetGet.mockResolvedValue({ id: "asset-1", kind: "vnc", name: "box", options: {} });
    await mountPane();
    expect([...document.querySelectorAll("[data-testid='remote-disconnect-overlay'] button")].some((b) => b.textContent === "唤醒")).toBe(false);
    unmountPane();

    mocks.assetGet.mockResolvedValue({ id: "asset-1", kind: "vnc", name: "box", options: { wakeMac: "AA:BB:CC:DD:EE:01" } });
    mocks.createVncClient.mockImplementation(async () => {
      throw new RemoteEngineError({ protocol: "vnc", stage: "security", message: "VNC 认证失败" });
    });
    mocks.certificate.mockResolvedValue({ status: "pinned", fingerprint: "x" });
    await mountPane();
    expect([...document.querySelectorAll("[data-testid='remote-disconnect-overlay'] button")].some((b) => b.textContent === "唤醒")).toBe(false);
  });

  it("closes the tab from the overlay", async () => {
    mocks.createVncClient.mockImplementationOnce(async () => {
      throw new RemoteEngineError({ protocol: "vnc", stage: "security", message: "VNC 认证失败" });
    });
    mocks.certificate.mockResolvedValue({ status: "pinned", fingerprint: "x" });
    await mountPane();
    const close = [...document.querySelectorAll<HTMLButtonElement>("[data-testid='remote-disconnect-overlay'] button")].find((b) => b.textContent === "关闭")!;
    act(() => close.click());
    expect(mocks.closeTab).toHaveBeenCalledWith("tab-1");
  });

  it("gives the disconnect button the session-ends tooltip", async () => {
    await mountPane();
    const button = pane().querySelector<HTMLButtonElement>("button[aria-label='断开连接']")!;
    expect(button.title).toBe("断开后会话结束");
  });

  it("releases keyboard capture when the connection drops, so the app shortcuts return", async () => {
    await mountPane();
    const first = (await mocks.createVncClient.mock.results[0].value) as FakeClient;
    const canvas = document.createElement("canvas");
    canvas.tabIndex = 0;
    pane().querySelector("[data-captured] div > div")!.append(canvas);
    act(() => canvas.focus());
    expect(isKeyboardCaptured()).toBe(true);
    await act(async () => {
      first.callbacks.onDisconnected({ protocol: "vnc", stage: "closed", clean: false, wasConnected: true, message: "x" });
    });
    expect(isKeyboardCaptured()).toBe(false);
    expect(pane().querySelector<HTMLElement>("[data-captured]")!.dataset.captured).toBe("false");
  });

  it("does not auto-reconnect a session whose password was typed at connect time", async () => {
    vi.useFakeTimers();
    mocks.prepare.mockImplementation(async () => ({
      ticket: `t-${++ticketCounter}`, protocol: "vnc", assetId: "asset-1", name: "box", host: "h", port: 5900,
      username: "", domain: "", promptPassword: true, hasPassword: false, expiresAtMs: 1,
    }));
    mocks.prompt.mockResolvedValue("typed");
    await mountPane();
    await flush();
    const first = (await mocks.createVncClient.mock.results[0].value) as FakeClient;
    await act(async () => {
      first.callbacks.onDisconnected({ protocol: "vnc", stage: "closed", clean: false, wasConnected: true, message: "x" });
    });
    expect(document.querySelector("[data-testid='remote-retry-countdown']")).toBeNull();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(mocks.prepare).toHaveBeenCalledTimes(1);
  });

  it("does not auto-reconnect an RDP-style asset whose flow asked for a password, even when the asset option is off", async () => {
    vi.useFakeTimers();
    mocks.assetGet.mockResolvedValue({ id: "asset-1", kind: "vnc", name: "box", options: {} });
    mocks.prepare.mockImplementation(async () => ({
      ticket: `t-${++ticketCounter}`, protocol: "vnc", assetId: "asset-1", name: "box", host: "h", port: 5900,
      username: "", domain: "", promptPassword: false, hasPassword: false, expiresAtMs: 1,
    }));
    mocks.createVncClient.mockImplementationOnce(async (_s: HTMLElement, o: ClientOptions, cb: ClientCallbacks) => {
      const client = fakeClient(o, cb);
      client.connect = vi.fn(async () => {
        throw new RemoteEngineError({ protocol: "vnc", stage: "security", message: "VNC 认证失败: password required" });
      });
      return client;
    });
    mocks.certificate.mockResolvedValue({ status: "pinned", fingerprint: "x" });
    mocks.prompt.mockResolvedValue("typed");
    await mountPane();
    await flush();
    const connected = (await mocks.createVncClient.mock.results[1].value) as FakeClient;
    await act(async () => {
      connected.callbacks.onDisconnected({ protocol: "vnc", stage: "closed", clean: false, wasConnected: true, message: "x" });
    });
    expect(document.querySelector("[data-testid='remote-retry-countdown']")).toBeNull();
  });

  it("closing the tab during connect cancels it: the client is closed and nothing more is requested", async () => {
    let finish!: () => void;
    mocks.createVncClient.mockImplementationOnce(async (_s: HTMLElement, o: ClientOptions, cb: ClientCallbacks) => {
      const client = fakeClient(o, cb);
      client.connect = vi.fn(() => new Promise<void>((resolve) => { finish = resolve; }));
      return client;
    });
    const mounted = await mountPane();
    const client = (await mocks.createVncClient.mock.results[0].value) as FakeClient;
    expect(client.connect).toHaveBeenCalled();
    mounted.unmount();
    view = undefined;
    expect(client.disconnect).toHaveBeenCalled();
    finish();
    await flush();
    expect(mocks.prepare).toHaveBeenCalledTimes(1);
    expect(useRemoteStatus.getState().entries["tab-1"]).toBeUndefined();
  });

  it("closing the tab while the password prompt is open never sends the password on", async () => {
    let answer!: (value: string | null) => void;
    mocks.prepare.mockImplementation(async () => ({
      ticket: "t-1", protocol: "vnc", assetId: "asset-1", name: "box", host: "h", port: 5900,
      username: "", domain: "", promptPassword: true, hasPassword: false, expiresAtMs: 1,
    }));
    mocks.prompt.mockImplementation(() => new Promise((resolve) => { answer = resolve; }));
    const mounted = await mountPane();
    mounted.unmount();
    view = undefined;
    answer("typed");
    await flush();
    await flush();
    expect(mocks.setPassword).not.toHaveBeenCalled();
    expect(mocks.createVncClient).not.toHaveBeenCalled();
  });

  describe("soft keyboard on touch screens", () => {
    beforeEach(() => mocks.touch.mockReturnValue(true));

    function keyDown(input: HTMLElement, key: string) {
      const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true });
      act(() => {
        input.dispatchEvent(event);
      });
      return event;
    }

    it("sends Enter and Backspace from the hidden input as keys", async () => {
      await mountPane();
      const client = (await mocks.createVncClient.mock.results[0].value) as FakeClient;
      const input = document.querySelector<HTMLInputElement>("input[aria-label='远程键盘输入']")!;
      const enter = keyDown(input, "Enter");
      keyDown(input, "Backspace");
      expect(enter.defaultPrevented).toBe(true);
      const sent = vi.mocked(client.sendKey).mock.calls.map((call) => `${call[0]}:${call[1]}`);
      expect(sent).toEqual(["Enter:true", "Enter:false", "Backspace:true", "Backspace:false"]);
    });

    it("leaves keys that an IME reports as Unidentified to the input event", async () => {
      await mountPane();
      const client = (await mocks.createVncClient.mock.results[0].value) as FakeClient;
      keyDown(document.querySelector<HTMLInputElement>("input[aria-label='远程键盘输入']")!, "Unidentified");
      expect(client.sendKey).not.toHaveBeenCalled();
    });

    it("turns a deleteContentBackward input event into Backspace", async () => {
      await mountPane();
      const client = (await mocks.createVncClient.mock.results[0].value) as FakeClient;
      const input = document.querySelector<HTMLInputElement>("input[aria-label='远程键盘输入']")!;
      act(() => {
        input.dispatchEvent(new InputEvent("input", { inputType: "deleteContentBackward", bubbles: true }));
      });
      expect(vi.mocked(client.sendKey).mock.calls.map((call) => call[0])).toEqual(["Backspace", "Backspace"]);
    });

    it("offers Enter and Backspace on the key bar", async () => {
      await mountPane();
      const labels = [...document.querySelectorAll("[data-testid='remote-keys-bar'] button")].map((button) => button.textContent);
      expect(labels).toContain("Enter");
      expect(labels).toContain("⌫");
    });
  });
});
