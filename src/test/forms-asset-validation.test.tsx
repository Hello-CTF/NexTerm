/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { flush, mount, setInputValue, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  create: vi.fn(),
  update: vi.fn(),
  groupList: vi.fn(),
  listAssets: vi.fn(),
  listCredentials: vi.fn(),
  setCredential: vi.fn(),
  probe: vi.fn(),
  toast: vi.fn(),
}));
vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
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
      saveKeyFile: vi.fn(),
      auditQuery: vi.fn(),
    },
    vaultApi: {
      listCredentials: mocks.listCredentials,
      setCredential: mocks.setCredential,
    },
    sessionApi: {
      probe: mocks.probe,
    },
    terminalApi: {},
  };
});

import { AssetEditor } from "../features/explorer/AssetTree";
import { useUi } from "../app/store";

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  vi.useFakeTimers();
  document.body.replaceChildren();
  mocks.groupList.mockResolvedValue([]);
  mocks.listAssets.mockResolvedValue([]);
  mocks.listCredentials.mockResolvedValue([]);
  mocks.create.mockResolvedValue({ id: "a1" });
  mocks.update.mockResolvedValue({ id: "a1" });
  mocks.setCredential.mockResolvedValue({ id: "cred-1" });
  useUi.setState({ pushToast: mocks.toast, sessions: [] });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.useRealTimers();
});

function mountEditor() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(AssetEditor, { kind: "asset", onClose: vi.fn(), onSaved: vi.fn() }),
    ),
  );
}

async function advance(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

function hostInput(): HTMLInputElement {
  const input = mounted!.container.querySelector<HTMLInputElement>('input[placeholder="1.2.3.4"]');
  if (!input) throw new Error("host input not found");
  return input;
}

function portInput(): HTMLInputElement {
  const input = mounted!.container.querySelector<HTMLInputElement>('input[type="number"]');
  if (!input) throw new Error("port input not found");
  return input;
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

function setViewportWidth(width: number): void {
  Object.defineProperty(window, "innerWidth", { value: width, configurable: true });
}

describe("资产表单主机/端口内联校验", () => {
  it("主机失焦后立即报错，错误与字段程序化关联且相邻", async () => {
    mountEditor();
    await flush();
    const host = hostInput();
    setInputValue(host, "bad host");
    blur(host);
    expect(host.getAttribute("aria-invalid")).toBe("true");
    const error = fieldError(host);
    expect(error?.getAttribute("role")).toBe("alert");
    expect(error?.textContent).toBe("主机不能包含空格");
    expect(host.parentElement?.contains(error)).toBe(true);
  });

  it("主机空闲约 500ms 后自动校验，无需失焦", async () => {
    mountEditor();
    await flush();
    const host = hostInput();
    setInputValue(host, "bad host");
    await advance(499);
    expect(host.getAttribute("aria-invalid")).toBeNull();
    expect(fieldError(host)).toBeNull();
    await advance(1);
    expect(host.getAttribute("aria-invalid")).toBe("true");
    expect(fieldError(host)?.textContent).toBe("主机不能包含空格");
  });

  it("主机留空失焦提示必填", async () => {
    mountEditor();
    await flush();
    const host = hostInput();
    blur(host);
    expect(host.getAttribute("aria-invalid")).toBe("true");
    expect(fieldError(host)?.textContent).toBe("请填写主机");
  });

  it("修正主机后空闲 500ms 自动清除错误", async () => {
    mountEditor();
    await flush();
    const host = hostInput();
    setInputValue(host, "bad host");
    blur(host);
    expect(fieldError(host)).not.toBeNull();
    setInputValue(host, "10.0.0.8");
    await advance(500);
    expect(host.getAttribute("aria-invalid")).toBeNull();
    expect(fieldError(host)).toBeNull();
  });

  it("端口越界失焦报错，修正后失焦立即清除", async () => {
    mountEditor();
    await flush();
    const port = portInput();
    setInputValue(port, "70000");
    blur(port);
    expect(port.getAttribute("aria-invalid")).toBe("true");
    expect(fieldError(port)?.textContent).toBe("端口需在 1-65535 之间");
    setInputValue(port, "22");
    blur(port);
    expect(port.getAttribute("aria-invalid")).toBeNull();
    expect(fieldError(port)).toBeNull();
  });

  it("端口非整数空闲 500ms 后报错", async () => {
    mountEditor();
    await flush();
    const port = portInput();
    setInputValue(port, "22.5");
    await advance(500);
    expect(port.getAttribute("aria-invalid")).toBe("true");
    expect(fieldError(port)?.textContent).toBe("端口需在 1-65535 之间");
  });

  it("合法主机与端口不产生错误", async () => {
    mountEditor();
    await flush();
    const host = hostInput();
    const port = portInput();
    setInputValue(host, "10.0.0.8");
    setInputValue(port, "22");
    blur(host);
    blur(port);
    await advance(500);
    expect(host.getAttribute("aria-invalid")).toBeNull();
    expect(port.getAttribute("aria-invalid")).toBeNull();
    expect(mounted!.container.querySelector('[role="alert"]')).toBeNull();
  });

  it("320/390 窄屏下错误文本折行不溢出", async () => {
    for (const width of [320, 390]) {
      setViewportWidth(width);
      mountEditor();
      await flush();
      const host = hostInput();
      const port = portInput();
      setInputValue(host, "bad host");
      setInputValue(port, "70000");
      blur(host);
      blur(port);
      for (const input of [host, port]) {
        const error = fieldError(input);
        expect(error?.className).toContain("break-words");
        expect(error?.className).not.toContain("whitespace-nowrap");
      }
      expect(host.closest("div")?.className).toContain("min-w-0");
      expect(port.closest("div")?.className).toContain("shrink-0");
      mounted!.unmount();
      mounted = undefined;
    }
    setViewportWidth(1024);
  });
});
