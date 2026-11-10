/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  chooseSelectOption,
  deferred,
  flush,
  mount,
  setInputValue,
  waitForRendered,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  snippetList: vi.fn(),
  probeBatch: vi.fn(),
  sessionList: vi.fn(),
  listLive: vi.fn(),
  listDurable: vi.fn(),
  write: vi.fn(),
  search: vi.fn(),
  copyText: vi.fn(),
  toast: vi.fn(),
  cwds: new Map<string, string>(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: { list: mocks.list, snippetList: mocks.snippetList, probeBatch: mocks.probeBatch },
    sessionApi: { list: mocks.sessionList },
    dbApi: {},
    vaultApi: {},
    terminalApi: { listLive: mocks.listLive, listDurable: mocks.listDurable, write: mocks.write },
  };
});
vi.mock("../../ipc/searchApi", () => ({
  commandSearchApi: { search: mocks.search },
  transcriptHostSearchApi: { search: vi.fn() },
}));
vi.mock("../../ui/clipboard", () => ({ copyText: mocks.copyText }));
vi.mock("../../features/files/cwdFollow", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../features/files/cwdFollow")>();
  return { ...actual, useTerminalCwd: (tabId: string | null | undefined) => (tabId ? (mocks.cwds.get(tabId) ?? null) : null) };
});

import { JumpPalette } from "../../app/JumpPalette";
import { COMMAND_SEARCH_DEBOUNCE_MS } from "../../app/jump/commandHistorySearch";
import { resetTabRecency } from "../../app/jump/tabRecency";
import { resetAwarenessStore } from "../../app/sessionAwarenessStore";
import { useUi, type AppTab, type Workspace } from "../../app/store";
import { useTranscriptFocus } from "../../features/terminal/transcriptFocus";
import type { Asset } from "../../ipc/commands";

const WEB: Asset = {
  id: "a1",
  groupId: null,
  kind: "ssh",
  name: "web-01",
  host: "10.0.0.8",
  port: 22,
  username: "root",
  authKind: "agent",
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

function item(id: number, overrides: Record<string, unknown> = {}) {
  return {
    id,
    sessionId: "s1",
    tabId: "tab",
    assetId: "a1",
    command: `docker ps ${id}`,
    cwd: "",
    source: "shell",
    exitCode: 0,
    startedAt: Date.now() - 60_000,
    finishedAt: Date.now() - 59_000,
    transcriptId: `tr-${id}`,
    ...overrides,
  };
}

function terminal(id: string): AppTab {
  return { id, kind: "terminal", title: id, sessionId: "s1", tabId: `kernel-${id}`, closable: true };
}

function workspace(): Workspace {
  return {
    id: "ws-a",
    kind: "session",
    title: "ws-a",
    sessionId: "s1",
    assetId: "a1",
    assetKind: "ssh",
    closable: true,
    panes: [{ id: "ws-a-pane", tabs: [terminal("t-build")], activeTabId: "t-build" }],
    activePaneId: "ws-a-pane",
  };
}

function combobox(view: MountedView): HTMLInputElement {
  const input = view.container.querySelector<HTMLInputElement>('[role="combobox"]');
  if (!input) throw new Error("combobox not found");
  return input;
}

function keyDown(target: EventTarget, key: string, init: KeyboardEventInit = {}): void {
  act(() => {
    target.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init }));
  });
}

function rowTexts(view: MountedView): string[] {
  return [...view.container.querySelectorAll('[data-testid="command-history-row"]')].map((row) => row.textContent ?? "");
}

function cwdToggle(view: MountedView): HTMLButtonElement {
  const button = [...view.container.querySelectorAll<HTMLButtonElement>("button[aria-pressed]")].find((b) => b.textContent === "仅当前目录");
  if (!button) throw new Error("directory toggle not found");
  return button;
}

let mounted: MountedView | undefined;
const onClose = vi.fn();

async function mountPalette(): Promise<MountedView> {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(createElement(QueryClientProvider, { client }, createElement(JumpPalette, { onClose })));
  await flush();
  await flush();
  return mounted;
}

async function typeCommand(view: MountedView, value: string): Promise<void> {
  setInputValue(combobox(view), value);
  await waitForRendered(() => expect(mocks.search).toHaveBeenCalled());
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  resetTabRecency();
  resetAwarenessStore();
  useTranscriptFocus.setState({ request: null });
  mocks.list.mockResolvedValue([WEB]);
  mocks.snippetList.mockResolvedValue([]);
  mocks.probeBatch.mockResolvedValue({ results: [] });
  mocks.sessionList.mockResolvedValue([]);
  mocks.listLive.mockResolvedValue([]);
  mocks.listDurable.mockResolvedValue([]);
  mocks.write.mockResolvedValue(undefined);
  mocks.copyText.mockResolvedValue(undefined);
  mocks.search.mockResolvedValue({ items: [item(1)], hasMore: false });
  mocks.cwds.clear();
  useUi.setState({
    sessions: [],
    workspaces: [workspace()],
    activeWorkspaceId: "ws-a",
    connectingAssetIds: [],
    pushToast: mocks.toast,
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("jump palette command history search", () => {
  it("does not search unless the input starts with >", async () => {
    const view = await mountPalette();
    vi.useFakeTimers();
    try {
      setInputValue(combobox(view), "docker");
      await act(async () => {
        await vi.advanceTimersByTimeAsync(COMMAND_SEARCH_DEBOUNCE_MS + 150);
      });
      expect(mocks.search).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });

  it("searches with the text after > and re-queries when the filters change", async () => {
    const view = await mountPalette();
    await typeCommand(view, ">docker");
    expect(mocks.search).toHaveBeenLastCalledWith(
      expect.objectContaining({ text: "docker", assetId: null, startedFrom: null, startedTo: null, limit: 100, offset: 0 }),
    );

    chooseSelectOption(view.container, "命令历史主机", "web-01");
    await waitForRendered(() =>
      expect(mocks.search).toHaveBeenLastCalledWith(expect.objectContaining({ text: "docker", assetId: "a1" })),
    );

    const before = Date.now();
    chooseSelectOption(view.container, "命令历史时间范围", "24 小时");
    await waitForRendered(() => {
      const last = mocks.search.mock.calls.at(-1)?.[0] as { startedFrom: number | null };
      expect(last.startedFrom).not.toBeNull();
    });
    const last = mocks.search.mock.calls.at(-1)?.[0] as { startedFrom: number };
    expect(Math.abs(last.startedFrom - (before - 24 * 3600 * 1000))).toBeLessThan(10_000);
  });

  it("shows host names and exit codes, including deleted hosts and unknown exit codes", async () => {
    mocks.search.mockResolvedValue({
      items: [
        item(1, { exitCode: 0 }),
        item(2, { assetId: "gone", exitCode: 2, command: "false" }),
        item(3, { assetId: "device/d1", exitCode: null, command: "uptime" }),
      ],
      hasMore: false,
    });
    const view = await mountPalette();
    await typeCommand(view, ">x");
    await waitForRendered(() => expect(rowTexts(view)).toHaveLength(3));
    const rows = rowTexts(view);
    expect(rows[0]).toContain("web-01");
    expect(rows[1]).toContain("已删除的主机");
    expect(rows[1]).toContain("2");
    expect(rows[2]).toContain("设备");
    expect(rows[2]).toContain("—");
  });

  it("opens the replay with Enter by requesting focus and a history tab", async () => {
    const view = await mountPalette();
    await typeCommand(view, ">docker");
    await waitForRendered(() => expect(rowTexts(view)).toHaveLength(1));
    keyDown(combobox(view), "Enter");

    const request = useTranscriptFocus.getState().request;
    expect(request).toMatchObject({ assetId: "a1", transcriptId: "tr-1" });
    const tabs = useUi.getState().workspaces.flatMap((w) => w.panes.flatMap((p) => p.tabs));
    expect(tabs.some((tab) => tab.kind === "hostHistory" || tab.kind === "history")).toBe(true);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("keeps the palette open and says so when the command has no transcript", async () => {
    mocks.search.mockResolvedValue({ items: [item(1, { transcriptId: null })], hasMore: false });
    const view = await mountPalette();
    await typeCommand(view, ">docker");
    await waitForRendered(() => expect(rowTexts(view)).toHaveLength(1));
    keyDown(combobox(view), "Enter");

    expect(mocks.toast).toHaveBeenCalledWith("info", "该命令没有对应的终端记录");
    expect(onClose).not.toHaveBeenCalled();
    expect(useTranscriptFocus.getState().request).toBeNull();
  });

  it("fills the command into the terminal without a newline on Shift+Enter", async () => {
    const view = await mountPalette();
    await typeCommand(view, ">docker");
    await waitForRendered(() => expect(rowTexts(view)).toHaveLength(1));
    keyDown(combobox(view), "Enter", { shiftKey: true });

    await waitForRendered(() => expect(mocks.write).toHaveBeenCalledTimes(1));
    const [tabId, bytes] = mocks.write.mock.calls[0] as [string, Uint8Array];
    expect(tabId).toBe("kernel-t-build");
    const text = new TextDecoder().decode(bytes);
    expect(text).toBe("docker ps 1");
    expect(text).not.toMatch(/[\r\n]/);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("refuses to fill a multi-line command and offers to copy it", async () => {
    mocks.search.mockResolvedValue({ items: [item(1, { command: "echo a\necho b" })], hasMore: false });
    const view = await mountPalette();
    await typeCommand(view, ">echo");
    await waitForRendered(() => expect(rowTexts(view)).toHaveLength(1));
    keyDown(combobox(view), "Enter", { shiftKey: true });

    expect(mocks.write).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    const call = mocks.toast.mock.calls.find((args) => String(args[1]).includes("未填入终端"));
    expect(call).toBeDefined();
    await act(async () => {
      (call![2] as { onSelect: () => void }).onSelect();
    });
    expect(mocks.copyText).toHaveBeenCalledWith("echo a\necho b");
  });

  it.each([
    ["a bracketed-paste terminator", "echo x\x1b[201~rm -rf /"],
    ["a lone carriage return", "echo a\recho b"],
  ])("refuses to fill a command containing %s", async (_label, command) => {
    mocks.search.mockResolvedValue({ items: [item(1, { command })], hasMore: false });
    const view = await mountPalette();
    await typeCommand(view, ">echo");
    await waitForRendered(() => expect(rowTexts(view)).toHaveLength(1));
    keyDown(combobox(view), "Enter", { shiftKey: true });

    expect(mocks.write).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("控制字符"), expect.anything());
  });

  it("explains why nothing was filled when there is no usable terminal", async () => {
    useUi.setState({ workspaces: [], activeWorkspaceId: null });
    const view = await mountPalette();
    await typeCommand(view, ">docker");
    await waitForRendered(() => expect(rowTexts(view)).toHaveLength(1));
    keyDown(combobox(view), "Enter", { shiftKey: true });
    expect(mocks.write).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("终端"));
  });

  it("ignores a stale response that arrives after a newer one", async () => {
    const first = deferred<{ items: ReturnType<typeof item>[]; hasMore: boolean }>();
    const second = deferred<{ items: ReturnType<typeof item>[]; hasMore: boolean }>();
    mocks.search.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    const view = await mountPalette();
    setInputValue(combobox(view), ">a");
    await waitForRendered(() => expect(mocks.search).toHaveBeenCalledTimes(1));
    setInputValue(combobox(view), ">ab");
    await waitForRendered(() => expect(mocks.search).toHaveBeenCalledTimes(2));

    await act(async () => second.resolve({ items: [item(2, { command: "new result" })], hasMore: false }));
    await act(async () => first.resolve({ items: [item(1, { command: "old result" })], hasMore: false }));
    await flush();
    const rows = rowTexts(view);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toContain("new result");
  });

  it("loads the next page when more results exist and shows errors", async () => {
    mocks.search
      .mockResolvedValueOnce({ items: [item(1)], hasMore: true })
      .mockResolvedValueOnce({ items: [item(2, { command: "page two" })], hasMore: false });
    const view = await mountPalette();
    await typeCommand(view, ">d");
    await waitForRendered(() => expect(rowTexts(view)).toHaveLength(1));
    const more = [...view.container.querySelectorAll("button")].find((b) => b.textContent?.includes("加载更多"))!;
    await act(async () => {
      more.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await waitForRendered(() => expect(rowTexts(view)).toHaveLength(2));
    expect(mocks.search).toHaveBeenLastCalledWith(expect.objectContaining({ offset: 1, limit: 100 }));

    mocks.search.mockRejectedValueOnce(new Error("boom"));
    setInputValue(combobox(view), ">e");
    await waitForRendered(() => expect(view.container.textContent).toContain("boom"));
  });

  it("re-enables load more after the query changes while a page is still loading", async () => {
    const pending = deferred<{ items: ReturnType<typeof item>[]; hasMore: boolean }>();
    mocks.search
      .mockResolvedValueOnce({ items: [item(1)], hasMore: true })
      .mockReturnValueOnce(pending.promise)
      .mockResolvedValueOnce({ items: [item(3, { command: "fresh result" })], hasMore: true });
    const view = await mountPalette();
    await typeCommand(view, ">d");
    await waitForRendered(() => expect(rowTexts(view)).toHaveLength(1));
    const moreButton = () => [...view.container.querySelectorAll("button")].find((b) => /加载更多|加载中/.test(b.textContent ?? ""));
    await act(async () => {
      moreButton()!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await waitForRendered(() => expect(mocks.search).toHaveBeenCalledTimes(2));

    setInputValue(combobox(view), ">de");
    await waitForRendered(() => expect(rowTexts(view)[0]).toContain("fresh result"));
    await waitForRendered(() => {
      const button = moreButton();
      expect(button?.textContent).toContain("加载更多");
      expect(button?.hasAttribute("disabled")).toBe(false);
    });

    await act(async () => pending.resolve({ items: [item(9, { command: "stale page" })], hasMore: false }));
    await flush();
    expect(rowTexts(view)).toHaveLength(1);
    expect(rowTexts(view)[0]).toContain("fresh result");
  });

  it("filters by the active terminal directory and host only while the toggle is on", async () => {
    mocks.cwds.set("kernel-t-build", "/srv/app");
    const view = await mountPalette();
    await typeCommand(view, ">make");
    expect(mocks.search).toHaveBeenLastCalledWith(expect.objectContaining({ text: "make", assetId: null, cwd: null }));

    const toggle = cwdToggle(view);
    expect(toggle.textContent).toContain("仅当前目录");
    expect(toggle.getAttribute("aria-pressed")).toBe("false");
    await act(async () => {
      toggle.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await waitForRendered(() =>
      expect(mocks.search).toHaveBeenLastCalledWith(expect.objectContaining({ text: "make", assetId: "a1", cwd: "/srv/app" })),
    );
    expect(toggle.getAttribute("aria-pressed")).toBe("true");
    expect(view.container.querySelector<HTMLButtonElement>('button[aria-label="命令历史主机"]')?.disabled).toBe(true);

    await act(async () => {
      toggle.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await waitForRendered(() =>
      expect(mocks.search).toHaveBeenLastCalledWith(expect.objectContaining({ text: "make", assetId: null, cwd: null })),
    );
  });

  it("disables the directory toggle while the active terminal has not reported a directory", async () => {
    const view = await mountPalette();
    await typeCommand(view, ">make");
    const toggle = cwdToggle(view);
    expect(toggle.disabled).toBe(true);
    expect(mocks.search).toHaveBeenLastCalledWith(expect.objectContaining({ cwd: null }));
  });

  it("shows each command's directory and lifts commands from the current directory slightly", async () => {
    mocks.cwds.set("kernel-t-build", "/srv/app");
    mocks.search.mockResolvedValue({
      items: [
        item(1, { command: "newest", cwd: "/tmp" }),
        item(2, { command: "second" }),
        item(3, { command: "third", cwd: "/srv/app", assetId: "other" }),
        item(4, { command: "fourth" }),
        item(5, { command: "here far", cwd: "/srv/app" }),
        item(6, { command: "here near", cwd: "/srv/app" }),
      ],
      hasMore: false,
    });
    const view = await mountPalette();
    await typeCommand(view, ">x");
    await waitForRendered(() => expect(rowTexts(view)).toHaveLength(6));
    const order = rowTexts(view).map((row) => ["newest", "second", "third", "fourth", "here far", "here near"].find((c) => row.startsWith(c)));
    expect(order).toEqual(["newest", "here far", "second", "here near", "third", "fourth"]);
    const shown = [...view.container.querySelectorAll('[data-testid="command-history-cwd"]')].map((node) => node.textContent);
    expect(shown).toEqual(["/tmp", "/srv/app", "/srv/app", "/srv/app"]);
  });
});
