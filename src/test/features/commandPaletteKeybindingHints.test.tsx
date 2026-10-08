/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { flush, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  snippetList: vi.fn(),
  probeBatch: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      list: mocks.list,
      snippetList: mocks.snippetList,
      probeBatch: mocks.probeBatch,
    },
    sessionApi: {},
    dbApi: {},
    vaultApi: {},
    terminalApi: {},
  };
});

import { CommandPalette } from "../../app/CommandPalette";
import {
  getKeybinding,
  loadKeybindings,
  matchAppKeybinding,
  resetAllKeybindings,
  setKeybinding,
} from "../../app/keybindings";

const STORAGE_KEY = "nexterm.keybindings.v1";

let mounted: MountedView | undefined;

function mountPalette(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const node: ReactNode = createElement(CommandPalette, {
    onClose: () => {},
    onQuickConnect: () => {},
  });
  const view = mount(createElement(QueryClientProvider, { client }, node));
  return view;
}

function optionHint(label: string): string | null {
  const option = [...(mounted?.container.querySelectorAll<HTMLButtonElement>('[role="option"]') ?? [])].find(
    (o) => o.textContent?.includes(label),
  );
  if (!option) throw new Error(`option not found: ${label}`);
  return option.querySelector(".nx-command-hint")?.textContent ?? null;
}

async function show(): Promise<void> {
  mounted = mountPalette();
  await flush();
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  resetAllKeybindings();
  mocks.list.mockResolvedValue([]);
  mocks.snippetList.mockResolvedValue([]);
  mocks.probeBatch.mockResolvedValue([]);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  resetAllKeybindings();
});

describe("command palette keybinding hints", () => {
  it("shows the default bindings with their descriptions", async () => {
    await show();
    expect(optionHint("快速连接…")).toBe("Ctrl+Shift+K · 最近使用优先");
    expect(optionHint("打开本地终端")).toBe("Ctrl+T · 当前设备");
    expect(optionHint("上下分屏 / 取消分屏")).toBe("Ctrl+\\");
  });

  it("follows rebound bindings and the displayed key matches the dispatched action", async () => {
    act(() => {
      setKeybinding("quickConnect", "Alt+q");
      setKeybinding("newTerminal", "Alt+n");
      setKeybinding("toggleSplit", "Alt+s");
    });
    await show();
    expect(optionHint("快速连接…")).toBe("Alt+Q · 最近使用优先");
    expect(optionHint("打开本地终端")).toBe("Alt+N · 当前设备");
    expect(optionHint("上下分屏 / 取消分屏")).toBe("Alt+S");

    const quick = matchAppKeybinding(
      new KeyboardEvent("keydown", { key: "q", altKey: true }),
    );
    expect(quick).toEqual({ action: "quickConnect", digit: null });
    const terminal = matchAppKeybinding(
      new KeyboardEvent("keydown", { key: "n", altKey: true }),
    );
    expect(terminal).toEqual({ action: "newTerminal", digit: null });
    const split = matchAppKeybinding(
      new KeyboardEvent("keydown", { key: "s", altKey: true }),
    );
    expect(split).toEqual({ action: "toggleSplit", digit: null });
  });

  it("omits the key fragment when a binding is cleared but keeps descriptions", async () => {
    act(() => {
      setKeybinding("quickConnect", null);
      setKeybinding("toggleSplit", null);
    });
    await show();
    expect(optionHint("快速连接…")).toBe("最近使用优先");
    expect(optionHint("上下分屏 / 取消分屏")).toBeNull();
    expect(optionHint("打开本地终端")).toBe("Ctrl+T · 当前设备");
  });

  it("reads persisted overrides in the storage format on load", async () => {
    localStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({ quickConnect: "Alt+q", newTerminal: "Alt+n", toggleSplit: "Alt+s" }),
    );
    act(() => loadKeybindings());
    await show();
    expect(optionHint("快速连接…")).toBe("Alt+Q · 最近使用优先");
    expect(optionHint("打开本地终端")).toBe("Alt+N · 当前设备");
    expect(optionHint("上下分屏 / 取消分屏")).toBe("Alt+S");
    expect(getKeybinding("quickConnect")).toBe("Alt+q");
    expect(getKeybinding("closeTab")).toBe("Mod+w");
  });
});
