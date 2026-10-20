/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, flush, mount, setInputValue, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  forwardEnv: vi.fn(),
  forwardList: vi.fn(),
  forwardCreate: vi.fn(),
  forwardCreateSocks: vi.fn(),
  forwardCreateRemote: vi.fn(),
  forwardRemove: vi.fn(),
  ask: vi.fn(),
  toast: vi.fn(),
}));
vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    forwardApi: {
      env: mocks.forwardEnv,
      list: mocks.forwardList,
      create: mocks.forwardCreate,
      createSocks: mocks.forwardCreateSocks,
      createRemote: mocks.forwardCreateRemote,
      remove: mocks.forwardRemove,
    },
  };
});
vi.mock("../ui/dialogs", () => ({ ask: mocks.ask }));

import { ForwardPanel } from "../features/forward/ForwardPanel";
import { useUi } from "../app/store";

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  vi.useFakeTimers();
  document.body.replaceChildren();
  mocks.forwardEnv.mockResolvedValue({ available: true, platform: "other", listenHost: "127.0.0.1" });
  mocks.forwardList.mockResolvedValue([]);
  mocks.forwardCreateRemote.mockResolvedValue({ id: "f1", kind: "remote" });
  mocks.forwardRemove.mockResolvedValue(null);
  mocks.ask.mockResolvedValue(true);
  useUi.setState({ pushToast: mocks.toast, sessions: [] });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.useRealTimers();
});

function mountForward(): void {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(
    createElement(QueryClientProvider, { client }, createElement(ForwardPanel, { sessionId: "s" })),
  );
}

function blur(element: HTMLElement): void {
  act(() => {
    element.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
  });
}

function fieldError(input: HTMLInputElement): HTMLElement | null {
  const id = input.getAttribute("aria-describedby");
  if (!id) return null;
  return document.getElementById(id);
}

function inputBy(selector: string): HTMLInputElement {
  const input = mounted!.container.querySelector<HTMLInputElement>(selector);
  if (!input) throw new Error(`input not found: ${selector}`);
  return input;
}

function switchToRemote(): void {
  clickButton(mounted!.container, "远程转发");
}

describe("ForwardPanel remote forwarding (-R)", () => {
  it("renders remote listen address and target fields after switching to remote forwarding, with port 18080 by default", async () => {
    mountForward();
    await flush();
    expect(mounted!.container.querySelector('input[aria-label="远端监听地址"]')).toBeNull();
    switchToRemote();
    const bindHost = inputBy('input[aria-label="远端监听地址"]');
    expect(bindHost.value).toBe("127.0.0.1");
    expect(inputBy('input[placeholder="18080"]')).toBeTruthy();
    expect(inputBy('input[aria-label="目标主机"]')).toBeTruthy();
    expect(inputBy('input[aria-label="目标端口"]')).toBeTruthy();
  });

  it("shows an inline error and focuses the listen address when it is empty on submit, without creating", async () => {
    mountForward();
    await flush();
    switchToRemote();
    const bindHost = inputBy('input[aria-label="远端监听地址"]');
    setInputValue(bindHost, " ");
    clickButton(mounted!.container, "创建");
    expect(bindHost.getAttribute("aria-invalid")).toBe("true");
    const error = fieldError(bindHost);
    expect(error?.getAttribute("role")).toBe("alert");
    expect(error?.textContent).toBe("远端监听地址不能为空");
    expect(document.activeElement).toBe(bindHost);
    expect(mocks.forwardCreateRemote).not.toHaveBeenCalled();
    expect(mocks.toast).not.toHaveBeenCalled();
  });

  it("shows separate inline errors for invalid remote and target ports on submit", async () => {
    mountForward();
    await flush();
    switchToRemote();
    const bindPort = inputBy('input[placeholder="18080"]');
    const targetPort = inputBy('input[aria-label="目标端口"]');
    setInputValue(bindPort, "70000");
    setInputValue(targetPort, "0");
    clickButton(mounted!.container, "创建");
    expect(fieldError(bindPort)?.textContent).toBe("远端端口要填 1–65535 之间的整数");
    expect(fieldError(targetPort)?.textContent).toBe("目标端口要填 1–65535 之间的整数");
    expect(mocks.forwardCreateRemote).not.toHaveBeenCalled();
  });

  it("reports an invalid remote listen address on blur and clears it immediately after correction", async () => {
    mountForward();
    await flush();
    switchToRemote();
    const bindHost = inputBy('input[aria-label="远端监听地址"]');
    setInputValue(bindHost, "");
    blur(bindHost);
    expect(bindHost.getAttribute("aria-invalid")).toBe("true");
    expect(fieldError(bindHost)?.textContent).toBe("远端监听地址不能为空");
    setInputValue(bindHost, "0.0.0.0");
    blur(bindHost);
    expect(bindHost.getAttribute("aria-invalid")).toBeNull();
    expect(fieldError(bindHost)).toBeNull();
  });

  it("calls createRemote with the bind address and server-side target for valid input", async () => {
    mountForward();
    await flush();
    switchToRemote();
    setInputValue(inputBy('input[aria-label="远端监听地址"]'), "0.0.0.0");
    setInputValue(inputBy('input[placeholder="18080"]'), "18080");
    setInputValue(inputBy('input[aria-label="目标主机"]'), "127.0.0.1");
    setInputValue(inputBy('input[aria-label="目标端口"]'), "3306");
    clickButton(mounted!.container, "创建");
    await flush();
    expect(mocks.forwardCreateRemote).toHaveBeenCalledTimes(1);
    expect(mocks.forwardCreateRemote).toHaveBeenCalledWith("s", "0.0.0.0", 18080, "127.0.0.1", 3306);
    expect(mounted!.container.querySelector('[role="alert"]')).toBeNull();
    expect(mocks.toast).toHaveBeenCalledWith(
      "success",
      "远程转发已就绪 → 远端 0.0.0.0:18080 → NexTerm 所在机器 127.0.0.1:3306",
    );
  });

  it("requires needs_confirm for non-loopback binding and retries with acknowledgeRisk after confirmation", async () => {
    mocks.forwardCreateRemote
      .mockRejectedValueOnce(
        Object.assign(new Error("Confirmation required"), { code: "needs_confirm", detail: { listenHost: "0.0.0.0" } }),
      )
      .mockResolvedValueOnce({ id: "f1", kind: "remote" });
    mountForward();
    await flush();
    switchToRemote();
    setInputValue(inputBy('input[aria-label="远端监听地址"]'), "0.0.0.0");
    setInputValue(inputBy('input[placeholder="18080"]'), "18080");
    setInputValue(inputBy('input[aria-label="目标主机"]'), "127.0.0.1");
    setInputValue(inputBy('input[aria-label="目标端口"]'), "3306");
    clickButton(mounted!.container, "创建");
    await flush();
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("远端监听 0.0.0.0:18080"),
      expect.objectContaining({ title: "确认远程转发风险" }),
    );
    expect(mocks.forwardCreateRemote).toHaveBeenCalledTimes(2);
    expect(mocks.forwardCreateRemote).toHaveBeenLastCalledWith("s", "0.0.0.0", 18080, "127.0.0.1", 3306, true);
    expect(mocks.toast).toHaveBeenCalledWith(
      "success",
      "远程转发已就绪 → 远端 0.0.0.0:18080 → NexTerm 所在机器 127.0.0.1:3306",
    );
  });

  it("does not retry or claim success when the remote exposure confirmation is rejected", async () => {
    mocks.forwardCreateRemote.mockRejectedValueOnce(
      Object.assign(new Error("Confirmation required"), { code: "needs_confirm", detail: { listenHost: "0.0.0.0" } }),
    );
    mocks.ask.mockResolvedValue(false);
    mountForward();
    await flush();
    switchToRemote();
    setInputValue(inputBy('input[aria-label="远端监听地址"]'), "0.0.0.0");
    setInputValue(inputBy('input[placeholder="18080"]'), "18080");
    setInputValue(inputBy('input[aria-label="目标主机"]'), "127.0.0.1");
    setInputValue(inputBy('input[aria-label="目标端口"]'), "3306");
    clickButton(mounted!.container, "创建");
    await flush();
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("远端监听 0.0.0.0:18080"),
      expect.objectContaining({ title: "确认远程转发风险" }),
    );
    expect(mocks.forwardCreateRemote).toHaveBeenCalledTimes(1);
    expect(mocks.forwardCreateRemote).toHaveBeenCalledWith("s", "0.0.0.0", 18080, "127.0.0.1", 3306);
    expect(mocks.toast).not.toHaveBeenCalledWith("success", expect.any(String));
    await flush();
    expect(mocks.forwardCreateRemote).toHaveBeenCalledTimes(1);
  });

  it("renders remote forwarding entries with type badges and remote listen addresses", async () => {
    mocks.forwardList.mockResolvedValue([
      {
        id: "f1",
        sessionId: "s",
        listenHost: "127.0.0.1",
        listenPort: 18080,
        targetHost: "127.0.0.1",
        targetPort: 3306,
        kind: "remote",
        createdAt: 1,
      },
    ]);
    mountForward();
    await flush();
    const row = mounted!.container.querySelector("tbody tr");
    expect(row?.textContent).toContain("远程转发");
    expect(row?.textContent).toContain("127.0.0.1:18080");
    expect(row?.textContent).toContain("NexTerm 所在机器");
    expect(mounted!.container.textContent).toContain("0 条静态转发 · 1 条远程转发 · 0 条 SOCKS5 代理");
  });

  it("reports stop failures without claiming success", async () => {
    mocks.forwardList.mockResolvedValue([
      {
        id: "f1",
        sessionId: "s",
        listenHost: "127.0.0.1",
        listenPort: 18080,
        targetHost: "127.0.0.1",
        targetPort: 3306,
        kind: "remote",
        createdAt: 1,
      },
    ]);
    mocks.forwardRemove.mockRejectedValue({
      code: "io",
      message: "Failed to close the forwarding listener (the remote port may still be listening): ssh: cancel-tcpip-forward failed",
    });
    mountForward();
    await flush();
    clickButton(mounted!.container, "停止");
    await flush();
    expect(mocks.ask).toHaveBeenCalled();
    expect(mocks.forwardRemove).toHaveBeenCalledWith("f1");
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("停止失败"));
    expect(mocks.toast).not.toHaveBeenCalledWith("success", expect.anything());
  });
});
