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

describe("asset form host/port inline validation", () => {
  it("shows an error immediately after host blur, programmatically associated with and adjacent to the field", async () => {
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

  it("validates the host after about 500ms idle without requiring blur", async () => {
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

  it("reports a required host when it is empty on blur", async () => {
    mountEditor();
    await flush();
    const host = hostInput();
    blur(host);
    expect(host.getAttribute("aria-invalid")).toBe("true");
    expect(fieldError(host)?.textContent).toBe("请填写主机");
  });

  it("clears the host error automatically after 500ms idle once corrected", async () => {
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

  it("reports an out-of-range port on blur and clears the error immediately after correction", async () => {
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

  it("reports a non-integer port after 500ms idle", async () => {
    mountEditor();
    await flush();
    const port = portInput();
    setInputValue(port, "22.5");
    await advance(500);
    expect(port.getAttribute("aria-invalid")).toBe("true");
    expect(fieldError(port)?.textContent).toBe("端口需在 1-65535 之间");
  });

  it("produces no errors for a valid host and port", async () => {
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

  it("wraps error text without overflowing at 320/390 widths", async () => {
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

describe("asset form saving is blocked by host/port validation", () => {
  function saveButton(): HTMLButtonElement {
    const button = [...mounted!.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "保存",
    ) as HTMLButtonElement;
    if (!button) throw new Error("save button not found");
    return button;
  }

  function fillName(name: string): void {
    const input = mounted!.container.querySelector<HTMLInputElement>("input.nx-input");
    if (!input) throw new Error("name input not found");
    setInputValue(input, name);
  }

  it("disables save for an invalid host and re-enables it after correction", async () => {
    mountEditor();
    await flush();
    fillName("web-01");
    const host = hostInput();
    setInputValue(host, "bad host");
    blur(host);
    expect(saveButton().disabled).toBe(true);
    expect(mocks.create).not.toHaveBeenCalled();

    setInputValue(host, "10.0.0.8");
    blur(host);
    expect(saveButton().disabled).toBe(false);
  });

  it("disables save and prevents submission for an out-of-range port", async () => {
    mountEditor();
    await flush();
    fillName("web-01");
    const port = portInput();
    setInputValue(port, "70000");
    blur(port);
    expect(saveButton().disabled).toBe(true);
    expect(mocks.create).not.toHaveBeenCalled();

    setInputValue(port, "22");
    blur(port);
    expect(saveButton().disabled).toBe(false);
  });

  it("disables save when the host is empty", async () => {
    mountEditor();
    await flush();
    fillName("web-01");
    blur(hostInput());
    expect(saveButton().disabled).toBe(true);
    expect(mocks.create).not.toHaveBeenCalled();
  });
});
