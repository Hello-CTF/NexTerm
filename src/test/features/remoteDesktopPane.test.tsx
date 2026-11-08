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
  createClient: vi.fn(),
  closeTab: vi.fn(),
  prompt: vi.fn(),
  ask: vi.fn(),
  touch: vi.fn(() => false),
  wake: vi.fn(),
  toast: vi.fn(),
  desktop: false,
  readLocal: vi.fn(),
  dropListeners: [] as Array<(payload: unknown) => void>,
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    assetApi: { get: mocks.assetGet },
    wakeApi: { wake: mocks.wake },
    localFileApi: { read: mocks.readLocal },
    remoteDesktopApi: {
      prepare: mocks.prepare,
      releaseSecret: mocks.releaseSecret,
      setPassword: mocks.setPassword,
      certificate: mocks.certificate,
      trustCertificate: mocks.trustCertificate,
    },
  });
});
vi.mock("../../ipc/env", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/env")>();
  return {
    ...actual,
    get DESKTOP() {
      return mocks.desktop;
    },
  };
});
vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    listenEvent: vi.fn(async (event: string, handler: (payload: unknown) => void) => {
      if (event !== actual.EVENTS.filesDropped) return actual.listenEvent(event, handler);
      mocks.dropListeners.push(handler);
      return () => {
        mocks.dropListeners = mocks.dropListeners.filter((listener) => listener !== handler);
      };
    }),
  };
});
vi.mock("../../features/remote/VncClient", () => ({ createVncClient: mocks.createClient }));
vi.mock("../../features/remote/RdpClient", () => ({ createRdpClient: mocks.createClient }));
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
import { NATIVE_PASTE_WAIT_MS } from "../../features/remote/remoteKeyboard";

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
    sendClipboard: vi.fn(async () => ({ status: "done" })),
    fetchClipboard: vi.fn(async () => ({ status: "done" })),
    setForeground: vi.fn(),
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
  mocks.createClient.mockImplementation(async (_stage: HTMLElement, options: ClientOptions, callbacks: ClientCallbacks) => {
    return fakeClient(options, callbacks);
  });
});

afterEach(() => {
  view?.unmount();
  view = undefined;
  resetKeyboardCaptureForTest();
  resetRemoteStatusForTest();
  vi.useRealTimers();
  vi.unstubAllGlobals();
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

  it("reports an asset's fixed resolution that the server never confirms", async () => {
    mocks.assetGet.mockResolvedValue({ id: "asset-1", kind: "rdp", name: "box", options: { width: 1280, height: 720 } });
    const prepare = mocks.prepare.getMockImplementation()!;
    mocks.prepare.mockImplementation(async () => ({ ...(await prepare()), protocol: "rdp" }));
    const resize = vi.fn(() => "pending");
    mocks.releaseSecret.mockResolvedValue({ username: "alice", domain: "", password: "test-password" });
    mocks.createClient.mockImplementation(async (_stage, options, callbacks) => ({
      ...fakeClient(options, callbacks),
      protocol: "rdp",
      supportsResize: true,
      resize,
      desktopSize: () => ({ width: 1440, height: 900 }),
    }));
    vi.stubGlobal("ResizeObserver", class {
      observe() {}
      disconnect() {}
    });
    vi.useFakeTimers();
    await mountPane();
    expect(pane().dataset.status).toBe("connected");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(6500);
    });
    expect(resize).toHaveBeenCalledWith(1280, 720);
    expect(document.querySelector("[data-testid='remote-resolution-notice']")?.textContent).toContain("尚未生效");
    expect(document.querySelector("[data-testid='remote-resolution-notice']")?.textContent).toContain("重新连接");
  });

  it("offers a reconnect when the server does not confirm a requested resolution", async () => {
    mocks.assetGet.mockResolvedValue({ id: "asset-1", kind: "rdp", name: "box", options: { width: 1280, height: 720 } });
    const prepare = mocks.prepare.getMockImplementation()!;
    mocks.prepare.mockImplementation(async () => ({ ...(await prepare()), protocol: "rdp" }));
    const resize = vi.fn(() => "pending");
    mocks.releaseSecret.mockResolvedValue({ username: "alice", domain: "", password: "test-password" });
    mocks.createClient.mockImplementation(async (_stage, options, callbacks) => ({
      ...fakeClient(options, callbacks),
      protocol: "rdp",
      supportsResize: true,
      resize,
      desktopSize: () => ({ width: 1280, height: 720 }),
    }));
    vi.stubGlobal("ResizeObserver", class {
      observe() {}
      disconnect() {}
    });
    vi.useFakeTimers();
    await mountPane();
    expect(pane().dataset.status).toBe("connected");
    await act(async () => {
      [...pane().querySelectorAll("button")].find((button) => button.textContent === "分辨率")!.click();
    });
    await act(async () => {
      [...pane().querySelectorAll<HTMLButtonElement>("button[role=menuitem]")].find((button) => button.textContent === "1366 x 768")!.click();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(6500);
    });
    expect(resize).toHaveBeenCalledWith(1366, 768);
    expect(document.querySelector("[data-testid='remote-resolution-notice']")?.textContent).toContain("尚未确认");
    expect(document.querySelector("[data-testid='remote-resolution-notice']")?.textContent).toContain("重新连接");
  });

  describe("RDP resolution requests", () => {
    type Outcome = "applied" | "pending" | "awaiting";
    let resizeCallbacks: Array<() => void>;
    let reportSize: (width: number, height: number) => void;

    async function mountRdp(options: Record<string, unknown>, resize: () => Outcome, size: () => { width: number; height: number }) {
      mocks.assetGet.mockResolvedValue({ id: "asset-1", kind: "rdp", name: "box", options });
      const prepare = mocks.prepare.getMockImplementation()!;
      mocks.prepare.mockImplementation(async () => ({ ...(await prepare()), protocol: "rdp" }));
      mocks.releaseSecret.mockResolvedValue({ username: "alice", domain: "", password: "test-password" });
      const resizeMock = vi.fn(resize);
      mocks.createClient.mockImplementation(async (_stage, clientOptions, callbacks) => {
        reportSize = (width, height) => callbacks.onDesktopSize?.(width, height);
        return { ...fakeClient(clientOptions, callbacks), protocol: "rdp", supportsResize: true, resize: resizeMock, desktopSize: size };
      });
      resizeCallbacks = [];
      vi.stubGlobal(
        "ResizeObserver",
        class {
          constructor(callback: () => void) {
            resizeCallbacks.push(callback);
          }
          observe() {}
          disconnect() {}
        },
      );
      vi.useFakeTimers();
      await mountPane();
      return resizeMock;
    }

    const notice = () => document.querySelector("[data-testid='remote-resolution-notice']");
    const advance = (ms: number) =>
      act(async () => {
        await vi.advanceTimersByTimeAsync(ms);
      });

    it("clears the notice when the desktop reaches the fixed size late", async () => {
      let current = { width: 1440, height: 900 };
      await mountRdp({ width: 1280, height: 720 }, () => "awaiting", () => current);
      await advance(6500);
      expect(notice()?.textContent).toContain("尚未确认");
      await advance(2000);
      expect(notice()).not.toBeNull();
      current = { width: 1280, height: 720 };
      await act(async () => reportSize(1280, 720));
      expect(notice()).toBeNull();
    });

    it("keeps the notice when the server resizes to some other size", async () => {
      let current = { width: 1440, height: 900 };
      await mountRdp({ width: 1280, height: 720 }, () => "awaiting", () => current);
      await advance(6500);
      expect(notice()).not.toBeNull();
      current = { width: 1600, height: 900 };
      await act(async () => reportSize(1600, 900));
      expect(notice()).not.toBeNull();
    });

    it("counts a rounded size from the server as the answer and shows no notice", async () => {
      let answered = false;
      let current = { width: 1440, height: 900 };
      const resize = await mountRdp({ width: 1280, height: 720 }, () => (answered ? "applied" : "awaiting"), () => current);
      await advance(1500);
      answered = true;
      current = { width: 1278, height: 720 };
      await act(async () => reportSize(1278, 720));
      await advance(10000);
      expect(resize).toHaveBeenCalled();
      expect(notice()).toBeNull();
    });

    it("shows no notice while following the pane, however long the server is silent", async () => {
      vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(1000);
      vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(600);
      const resize = await mountRdp({}, () => "awaiting", () => ({ width: 800, height: 600 }));
      await advance(10000);
      expect(resize).toHaveBeenCalledWith(1000, 600);
      expect(notice()).toBeNull();
    });

    it("gives every new resize its own retry budget", async () => {
      vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(1000);
      vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(600);
      const resize = await mountRdp({}, () => "pending", () => ({ width: 800, height: 600 }));
      await advance(8000);
      const firstChain = resize.mock.calls.length;
      expect(firstChain).toBeGreaterThan(1);
      for (let chain = 0; chain < 3; chain += 1) {
        resize.mockClear();
        await act(async () => resizeCallbacks.forEach((callback) => callback()));
        await advance(8000);
        expect(resize).toHaveBeenCalledTimes(firstChain);
      }
    });
  });

  it("forgets its status when it unmounts", async () => {
    const mounted = await mountPane();
    mounted.unmount();
    view = undefined;
    expect(useRemoteStatus.getState().entries["tab-1"]).toBeUndefined();
    expect(isKeyboardCaptured()).toBe(false);
  });

  it("captures the keyboard on focus inside the screen and shows the release hint", async () => {
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

  async function focusedScreen(): Promise<{ client: FakeClient; canvas: HTMLCanvasElement }> {
    await mountPane();
    const client = (await mocks.createClient.mock.results[0].value) as FakeClient;
    const canvas = document.createElement("canvas");
    canvas.tabIndex = 0;
    pane().querySelector("[data-captured]")!.querySelector("div > div")!.append(canvas);
    act(() => canvas.focus());
    return { client, canvas };
  }

  it("sends the local clipboard before pasting, without handing the paste chord to the remote", async () => {
    const { client, canvas } = await focusedScreen();
    const reachedRemote = vi.fn();
    canvas.addEventListener("keydown", reachedRemote);
    act(() => {
      canvas.dispatchEvent(new KeyboardEvent("keydown", { code: "KeyV", key: "v", ctrlKey: true, bubbles: true, cancelable: true }));
    });
    // No native paste event follows a synthetic keydown, so the pane reads the clipboard itself after the wait, then
    // gives the remote side a moment to take the clipboard in before the paste keys.
    await act(() => new Promise((resolve) => setTimeout(resolve, NATIVE_PASTE_WAIT_MS + 200)));
    await flush();
    expect(reachedRemote).not.toHaveBeenCalled();
    expect(client.sendClipboard).toHaveBeenCalledTimes(1);
    expect(vi.mocked(client.sendKey).mock.calls.map((call) => `${call[0]}:${call[1]}`)).toEqual([
      "ControlLeft:true",
      "KeyV:true",
      "KeyV:false",
      "ControlLeft:false",
    ]);
  });

  it("delivers a native paste ClipboardEvent payload directly to sendClipboard without triggering a fallback read", async () => {
    const { client } = await focusedScreen();
    const target = pane().querySelector<HTMLTextAreaElement>("[data-testid='remote-paste-target']")!;
    const clipboardData = { getData: vi.fn(() => "hello"), types: ["text/plain"] } as unknown as DataTransfer;
    act(() => {
      const pasteEvent = new Event("paste", { bubbles: true, cancelable: true });
      Object.defineProperty(pasteEvent, "clipboardData", { value: clipboardData });
      target.dispatchEvent(pasteEvent);
    });
    await flush();
    expect(client.sendClipboard).toHaveBeenCalledTimes(1);
    expect(client.sendClipboard).toHaveBeenCalledWith(clipboardData);
    expect(vi.mocked(client.focus)).toHaveBeenCalled();
  });

  it("shows why a paste sent nothing and presses no paste keys", async () => {
    const { client } = await focusedScreen();
    vi.mocked(client.sendClipboard).mockResolvedValue({ status: "unsupported", note: "VNC 只能同步文本，图片和文件未发送" });
    const target = pane().querySelector<HTMLTextAreaElement>("[data-testid='remote-paste-target']")!;
    act(() => {
      const pasteEvent = new Event("paste", { bubbles: true, cancelable: true });
      Object.defineProperty(pasteEvent, "clipboardData", { value: { getData: () => "", types: ["Files"], files: [] } });
      target.dispatchEvent(pasteEvent);
    });
    await flush();
    expect(vi.mocked(client.sendKey)).not.toHaveBeenCalled();
    expect(pane().querySelector("[data-testid='remote-clipboard-notice']")?.textContent).toBe("VNC 只能同步文本，图片和文件未发送");
  });

  it("sends files dropped through the desktop shell, and names the folders it left out", async () => {
    mocks.desktop = true;
    mocks.assetGet.mockResolvedValue({ id: "asset-1", kind: "rdp", name: "box", options: {} });
    const prepare = mocks.prepare.getMockImplementation()!;
    mocks.prepare.mockImplementation(async () => ({ ...(await prepare()), protocol: "rdp" }));
    vi.stubGlobal("ResizeObserver", class {
      observe() {}
      disconnect() {}
    });
    try {
      const { client, canvas } = await focusedScreen();
      vi.mocked(client.sendClipboard).mockImplementation(async (data?: DataTransfer | null) => ({ status: data?.files.length ? "done" : "empty" }));
      mocks.readLocal.mockResolvedValue({ path: "/Users/me/report.pdf", size: 3, contentBase64: btoa("pdf") });
      const elementFromPoint = vi.fn(() => canvas);
      Object.defineProperty(document, "elementFromPoint", { configurable: true, value: elementFromPoint });
      await act(async () => {
        for (const listener of mocks.dropListeners) {
          listener({ x: 10, y: 20, paths: [{ path: "/Users/me/report.pdf", dir: false }, { path: "/Users/me/photos", dir: true }] });
        }
      });
      await act(() => new Promise((resolve) => setTimeout(resolve, 300)));
      await flush();
      expect(elementFromPoint).toHaveBeenCalledWith(10, 20);
      expect(mocks.readLocal).toHaveBeenCalledTimes(1);
      const sent = vi.mocked(client.sendClipboard).mock.calls[0]?.[0];
      expect(sent?.files.length).toBe(1);
      expect(await sent!.files[0]!.text()).toBe("pdf");
      expect(sent!.files[0]!.name).toBe("report.pdf");
      expect(vi.mocked(client.sendKey).mock.calls.some(([code]) => code === "KeyV")).toBe(true);
      expect(pane().querySelector("[data-testid='remote-clipboard-notice']")?.textContent).toContain("photos");
    } finally {
      mocks.desktop = false;
      Reflect.deleteProperty(document, "elementFromPoint");
    }
  });

  it("does not read files dropped through the desktop shell into a VNC session", async () => {
    mocks.desktop = true;
    try {
      const { client, canvas } = await focusedScreen();
      Object.defineProperty(document, "elementFromPoint", { configurable: true, value: () => canvas });
      await act(async () => {
        for (const listener of mocks.dropListeners) listener({ x: 1, y: 1, paths: [{ path: "/tmp/a.txt", dir: false }] });
      });
      await flush();
      expect(mocks.readLocal).not.toHaveBeenCalled();
      expect(client.sendClipboard).not.toHaveBeenCalled();
      expect(pane().querySelector("[data-testid='remote-clipboard-notice']")?.textContent).toBe("VNC 只能同步文本，图片和文件未发送");
    } finally {
      mocks.desktop = false;
      Reflect.deleteProperty(document, "elementFromPoint");
    }
  });

  it("ignores a desktop-shell drop that landed outside the remote screen", async () => {
    mocks.desktop = true;
    try {
      const { client } = await focusedScreen();
      Object.defineProperty(document, "elementFromPoint", { configurable: true, value: () => document.body });
      await act(async () => {
        for (const listener of mocks.dropListeners) listener({ x: 1, y: 1, paths: [{ path: "/tmp/a.txt", dir: false }] });
      });
      await flush();
      expect(mocks.readLocal).not.toHaveBeenCalled();
      expect(client.sendClipboard).not.toHaveBeenCalled();
    } finally {
      mocks.desktop = false;
      Reflect.deleteProperty(document, "elementFromPoint");
    }
  });

  it("hands the paste chord to the remote untouched when automatic clipboard sync is off", async () => {
    mocks.assetGet.mockResolvedValue({ id: "asset-1", kind: "vnc", name: "box", options: { clipboardAutoSync: false } });
    const { client, canvas } = await focusedScreen();
    const reachedRemote = vi.fn();
    canvas.addEventListener("keydown", reachedRemote);
    act(() => {
      canvas.dispatchEvent(new KeyboardEvent("keydown", { code: "KeyV", key: "v", ctrlKey: true, bubbles: true, cancelable: true }));
    });
    await flush();
    expect(reachedRemote).toHaveBeenCalledTimes(1);
    expect(client.sendClipboard).not.toHaveBeenCalled();
  });

  it("tells the client whether its tab is in front", async () => {
    await mountPane();
    const client = (await mocks.createClient.mock.results[0].value) as FakeClient;
    expect(client.setForeground).toHaveBeenLastCalledWith(true);
    act(() => view!.root.render(createElement(RemoteDesktopPane, { tabId: "tab-1", assetId: "asset-1", visible: false })));
    await flush();
    expect(client.setForeground).toHaveBeenLastCalledWith(false);
  });

  it("counts down and reconnects with a new ticket after a network drop", async () => {
    vi.useFakeTimers();
    await mountPane();
    const client = mocks.createClient.mock.results[0].value as Promise<FakeClient>;
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
    expect(mocks.createClient.mock.calls.map((call) => (call[1] as ClientOptions).prepared.ticket)).toEqual(["t-1", "t-2"]);
  });

  it("asks before a fourth reconnect when the desktop keeps being taken over within five minutes", async () => {
    vi.useFakeTimers();
    await mountPane();
    const drop: EngineFailure = { protocol: "vnc", stage: "closed", clean: false, wasConnected: true, message: "与远程桌面的连接已中断" };
    // Every reconnect succeeds and is dropped again, as when another device takes the same session back.
    for (let round = 0; round < 3; round++) {
      const live = (await mocks.createClient.mock.results.at(-1)!.value) as FakeClient;
      await act(async () => live.callbacks.onDisconnected(drop));
      expect(document.querySelector("[data-testid='remote-reconnect-paused']")).toBeNull();
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10_000);
      });
      expect(pane().dataset.status).toBe("connected");
    }
    expect(mocks.prepare).toHaveBeenCalledTimes(4);
    const live = (await mocks.createClient.mock.results.at(-1)!.value) as FakeClient;
    await act(async () => live.callbacks.onDisconnected(drop));
    expect(document.querySelector("[data-testid='remote-retry-countdown']")).toBeNull();
    expect(document.querySelector("[data-testid='remote-reconnect-paused']")?.textContent).toContain("另一台设备");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10 * 60_000);
    });
    expect(mocks.prepare).toHaveBeenCalledTimes(4);

    const reconnect = [...document.querySelectorAll<HTMLButtonElement>("[data-testid='remote-disconnect-overlay'] button")].find((b) => b.textContent === "重新连接")!;
    await act(async () => reconnect.click());
    await flush();
    expect(mocks.prepare).toHaveBeenCalledTimes(5);
    expect(pane().dataset.status).toBe("connected");
    // A confirmed reconnect starts a fresh budget.
    const next = (await mocks.createClient.mock.results.at(-1)!.value) as FakeClient;
    await act(async () => next.callbacks.onDisconnected(drop));
    expect(document.querySelector("[data-testid='remote-retry-countdown']")?.textContent).toContain("2 秒");
  });

  it("reconnects automatically again once earlier reconnects leave the five-minute window", async () => {
    vi.useFakeTimers();
    await mountPane();
    const drop: EngineFailure = { protocol: "vnc", stage: "closed", clean: false, wasConnected: true, message: "x" };
    for (let round = 0; round < 3; round++) {
      const live = (await mocks.createClient.mock.results.at(-1)!.value) as FakeClient;
      await act(async () => live.callbacks.onDisconnected(drop));
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10_000);
      });
      // Stable for a while between drops.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(2 * 60_000);
      });
    }
    const live = (await mocks.createClient.mock.results.at(-1)!.value) as FakeClient;
    await act(async () => live.callbacks.onDisconnected(drop));
    expect(document.querySelector("[data-testid='remote-reconnect-paused']")).toBeNull();
    expect(document.querySelector("[data-testid='remote-retry-countdown']")).not.toBeNull();
  });

  it("does not auto-reconnect after an auth failure and offers the two buttons", async () => {
    vi.useFakeTimers();
    mocks.createClient.mockImplementationOnce(async () => {
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
    mocks.createClient.mockImplementationOnce(async () => {
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
    mocks.createClient.mockImplementation(async () => {
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
    mocks.createClient.mockImplementation(async () => {
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
    mocks.createClient.mockImplementation(async () => {
      throw new RemoteEngineError({ protocol: "vnc", stage: "security", message: "VNC 认证失败" });
    });
    mocks.certificate.mockResolvedValue({ status: "pinned", fingerprint: "x" });
    await mountPane();
    expect([...document.querySelectorAll("[data-testid='remote-disconnect-overlay'] button")].some((b) => b.textContent === "唤醒")).toBe(false);
  });

  it("closes the tab from the overlay", async () => {
    mocks.createClient.mockImplementationOnce(async () => {
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
    const first = (await mocks.createClient.mock.results[0].value) as FakeClient;
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
    const first = (await mocks.createClient.mock.results[0].value) as FakeClient;
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
    mocks.createClient.mockImplementationOnce(async (_s: HTMLElement, o: ClientOptions, cb: ClientCallbacks) => {
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
    const connected = (await mocks.createClient.mock.results[1].value) as FakeClient;
    await act(async () => {
      connected.callbacks.onDisconnected({ protocol: "vnc", stage: "closed", clean: false, wasConnected: true, message: "x" });
    });
    expect(document.querySelector("[data-testid='remote-retry-countdown']")).toBeNull();
  });

  it("closing the tab during connect cancels it: the client is closed and nothing more is requested", async () => {
    let finish!: () => void;
    mocks.createClient.mockImplementationOnce(async (_s: HTMLElement, o: ClientOptions, cb: ClientCallbacks) => {
      const client = fakeClient(o, cb);
      client.connect = vi.fn(() => new Promise<void>((resolve) => { finish = resolve; }));
      return client;
    });
    const mounted = await mountPane();
    const client = (await mocks.createClient.mock.results[0].value) as FakeClient;
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
    expect(mocks.createClient).not.toHaveBeenCalled();
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
      const client = (await mocks.createClient.mock.results[0].value) as FakeClient;
      const input = document.querySelector<HTMLInputElement>("input[aria-label='远程键盘输入']")!;
      const enter = keyDown(input, "Enter");
      keyDown(input, "Backspace");
      expect(enter.defaultPrevented).toBe(true);
      const sent = vi.mocked(client.sendKey).mock.calls.map((call) => `${call[0]}:${call[1]}`);
      expect(sent).toEqual(["Enter:true", "Enter:false", "Backspace:true", "Backspace:false"]);
    });

    it("leaves keys that an IME reports as Unidentified to the input event", async () => {
      await mountPane();
      const client = (await mocks.createClient.mock.results[0].value) as FakeClient;
      keyDown(document.querySelector<HTMLInputElement>("input[aria-label='远程键盘输入']")!, "Unidentified");
      expect(client.sendKey).not.toHaveBeenCalled();
    });

    it("turns a deleteContentBackward input event into Backspace", async () => {
      await mountPane();
      const client = (await mocks.createClient.mock.results[0].value) as FakeClient;
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
