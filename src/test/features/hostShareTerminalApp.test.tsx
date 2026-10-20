/** @vitest-environment jsdom */

import { act, createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flush, mount, type MountedView } from "./reactTestUtils";
import type { HostShareHandlers } from "../../ipc/hostShareApi";

const mocks = vi.hoisted(() => ({
  create: vi.fn(),
  handlers: null as HostShareHandlers | null,
  terminalProps: null as null | { onInput: (data: string) => void; readOnly?: boolean },
}));

vi.mock("../../ipc/hostShareApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/hostShareApi")>();
  return { ...actual, createHostShareConnection: mocks.create };
});

vi.mock("../../features/sharing/PublicShareTerminal", () => ({
  PublicShareTerminal: (props: { onInput: (data: string) => void; readOnly?: boolean }) => {
    mocks.terminalProps = props;
    return null;
  },
}));

import { HostShareTerminalApp } from "../../features/sharing/HostShareTerminalApp";

let mounted: MountedView | undefined;
let connection: { sendInput: ReturnType<typeof vi.fn>; close: ReturnType<typeof vi.fn> };

function mountApp(onClose = vi.fn()) {
  mounted = mount(createElement(HostShareTerminalApp, {
    deviceId: "device-1", deviceName: "shared-host", ownerUsername: "owner", onClose,
  }));
  return onClose;
}

function ready(permission: "read" | "read_write") {
  act(() => mocks.handlers!.onReady({ sessionId: "s-1", permission, expiresAt: Date.now() + 60_000 }));
}

beforeEach(() => {
  connection = { sendInput: vi.fn(), close: vi.fn() };
  mocks.handlers = null;
  mocks.terminalProps = null;
  mocks.create.mockImplementation((_deviceId: string, handlers: HostShareHandlers) => {
    mocks.handlers = handlers;
    return connection;
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.clearAllMocks();
});

describe("HostShareTerminalApp", () => {
  it("disables xterm and input for read-only shares", () => {
    mountApp();
    ready("read");
    expect(mounted!.container.textContent).toContain("只读");
    expect(mocks.terminalProps!.readOnly).toBe(true);
    act(() => mocks.terminalProps!.onInput("ls\r"));
    expect(connection.sendInput).not.toHaveBeenCalled();
  });

  it("enables xterm and sends input only for read_write", () => {
    mountApp();
    ready("read_write");
    expect(mounted!.container.textContent).toContain("可读写");
    expect(mocks.terminalProps!.readOnly).toBe(false);
    act(() => mocks.terminalProps!.onInput("ls\r"));
    expect(connection.sendInput).toHaveBeenCalledWith("ls\r");
  });

  it("switches the badge, xterm and input to read-only on a repeated ready", () => {
    mountApp();
    ready("read_write");
    act(() => mocks.terminalProps!.onInput("before-shrink"));
    ready("read");
    expect(mounted!.container.textContent).toContain("只读");
    expect(mounted!.container.textContent).not.toContain("可读写");
    expect(mocks.terminalProps!.readOnly).toBe(true);
    act(() => mocks.terminalProps!.onInput("after-shrink"));
    expect(connection.sendInput).toHaveBeenCalledTimes(1);
  });

  it("keeps the revocation error visible when the close frame follows", async () => {
    mountApp();
    ready("read_write");
    act(() => {
      mocks.handlers!.onError({ code: "forbidden", message: "主机分享已吊销" });
      mocks.handlers!.onClose({ code: 1008, reason: "share terminated", wasClean: true });
    });
    await flush();
    expect(mounted!.container.textContent).toContain("主机分享已吊销");
  });

  it("offers retry when the device is offline", () => {
    mountApp();
    act(() => mocks.handlers!.onError({ code: "disconnected", message: "device agent offline" }));
    expect(mounted!.container.textContent).toContain("device agent offline");
    expect([...mounted!.container.querySelectorAll("button")].some((button) => button.textContent === "重新连接")).toBe(true);
  });

  it("shows normal termination and closes the connection on unmount", () => {
    mountApp();
    ready("read");
    act(() => mocks.handlers!.onClose({ code: 1000, reason: "share ended", wasClean: true }));
    expect(mounted!.container.textContent).toContain("共享终端已结束");
    mounted!.unmount();
    mounted = undefined;
    expect(connection.close).toHaveBeenCalledTimes(1);
  });

  it("closes the old connection and creates a fresh one on retry", async () => {
    mountApp();
    ready("read");
    act(() => mocks.handlers!.onClose({ code: 1006, reason: "", wasClean: false }));
    const button = [...mounted!.container.querySelectorAll("button")].find((item) => item.textContent === "重新连接")!;
    act(() => button.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    await flush();
    expect(connection.close).toHaveBeenCalledTimes(1);
    expect(mocks.create).toHaveBeenCalledTimes(2);
  });
});
