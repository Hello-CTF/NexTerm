/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, clickButton, deferred, flush, flushUntil, mount, setInputValue, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  promptText: vi.fn(),
  pickLocalFile: vi.fn(),
  pickSavePath: vi.fn(),
  finishSave: vi.fn(),
  discardStaged: vi.fn(),
  roots: vi.fn(),
  list: vi.fn(),
  rename: vi.fn(),
  chmod: vi.fn(),
  checksum: vi.fn(),
  mkdir: vi.fn(),
  remove: vi.fn(),
  upload: vi.fn(),
  download: vi.fn(),
  packDownload: vi.fn(),
  extract: vi.fn(),
  termWrite: vi.fn(),
  copy: vi.fn(),
  lineExec: vi.fn(),
  connect: vi.fn(),
  connectLocal: vi.fn(),
  connectDevice: vi.fn(),
  knownHostAccept: vi.fn(),
  listenEvent: vi.fn(),
}));

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return {
    ...actual,
    ask: mocks.ask,
    promptText: mocks.promptText,
    pickLocalFile: mocks.pickLocalFile,
    pickSavePath: mocks.pickSavePath,
    finishSave: mocks.finishSave,
    discardStaged: mocks.discardStaged,
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    fsApi: {
      roots: mocks.roots,
      list: mocks.list,
      rename: mocks.rename,
      chmod: mocks.chmod,
      checksum: mocks.checksum,
      mkdir: mocks.mkdir,
      delete: mocks.remove,
      upload: mocks.upload,
      download: mocks.download,
      packDownload: mocks.packDownload,
      extract: mocks.extract,
    },
    sessionApi: {
      connect: mocks.connect,
      connectLocal: mocks.connectLocal,
      connectDevice: mocks.connectDevice,
      lineExec: mocks.lineExec,
    },
    terminalApi: { write: mocks.termWrite },
    assetApi: { knownHostAccept: mocks.knownHostAccept },
  });
});

vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    listenEvent: mocks.listenEvent,
    EVENTS: { ...actual.EVENTS, fsProgress: "fs://progress" },
  };
});

vi.mock("../../ui/clipboard", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/clipboard")>();
  return { ...actual, copyText: mocks.copy };
});

vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getVirtualItems: () =>
      Array.from({ length: count }, (_, i) => ({ index: i, start: i * 30, size: 30, key: i })),
    getTotalSize: () => count * 30,
  }),
}));

import { FileBrowser } from "../../features/files/FileBrowser";
import { resetCwdFollowForTest } from "../../features/files/cwdFollow";
import { useUi, type AppTab } from "../../app/store";
import type { FileEntryDto } from "../../ipc/types";

const SID = "s1";
const TAB_ID = "files-1";

const NOT_FOUND = {
  code: "not_found",
  message: "会话不存在或已关闭，请刷新后重试",
  detail: { resource: "session" },
};
const PATH_NOT_FOUND = {
  code: "not_found",
  message: "目录不存在",
  detail: { resource: "path" },
};
const LEGACY_NOT_FOUND = { code: "not_found", message: "旧服务返回的资源不存在" };

const CHANGED_HOST_KEY = {
  code: "host_key_pending",
  message: "SSH host key for 127.0.0.1:22 changed",
  detail: {
    host: "127.0.0.1",
    port: 22,
    keyType: "ssh-ed25519",
    fingerprint: "SHA256:new",
    changed: true,
    known: [{ keyType: "ssh-ed25519", fingerprint: "SHA256:old" }],
  },
};

const NEW_SESSION = {
  id: "s2",
  assetId: "asset-1",
  name: "web-01",
  kind: "ssh",
  status: "connected" as const,
  tabs: [],
  createdAt: 1,
};

const NEW_LOCAL_SESSION = {
  id: "s-local",
  assetId: null,
  name: "Local",
  kind: "local",
  status: "connected" as const,
  tabs: [],
  createdAt: 1,
};

const NEW_DEVICE_SESSION = {
  id: "s-dev",
  assetId: "device/d-1",
  name: "build-box",
  kind: "device",
  status: "connected" as const,
  tabs: [],
  createdAt: 1,
};

function entry(name: string, kind: string): FileEntryDto {
  return {
    name,
    path: `~/${name}`,
    kind,
    size: kind === "dir" ? 4096 : 10,
    mode: kind === "dir" ? "755" : "644",
    owner: "u",
    group: "u",
    mtime: 1,
    symlinkTarget: null,
  };
}

const ENTRIES = [entry("a.txt", "file"), entry("sub", "dir")];

let mounted: MountedView | undefined;

function seedWorkspace(options: { assetId?: string; assetKind?: string; title?: string } = {}): void {
  const tab: AppTab = { id: TAB_ID, kind: "files", title: "Files", sessionId: SID, closable: true };
  const title = options.title ?? "web-01";
  const kind = options.assetKind ?? (options.assetId ? "ssh" : "local");
  useUi.setState({
    workspaces: [
      {
        id: "ws1",
        kind: "session",
        title,
        sessionId: SID,
        assetId: options.assetId,
        assetKind: kind,
        panes: [{ id: "p1", tabs: [tab], activeTabId: tab.id }],
        activePaneId: "p1",
        splitRatio: 0.5,
        closable: true,
      },
    ],
    activeWorkspaceId: "ws1",
    sessions: [
      {
        id: SID,
        assetId: options.assetId ?? null,
        name: title,
        kind,
        status: "connected",
        tabs: [],
        createdAt: 0,
      },
    ],
    toasts: [],
  });
}

function storeTabSessionId(): string | undefined {
  for (const w of useUi.getState().workspaces) {
    for (const p of w.panes) {
      const t = p.tabs.find((candidate) => candidate.id === TAB_ID);
      if (t) return t.sessionId;
    }
  }
  return undefined;
}

function BrowserFromStore() {
  const sessionId = useUi((s) => {
    for (const w of s.workspaces) {
      for (const p of w.panes) {
        const t = p.tabs.find((candidate) => candidate.id === TAB_ID);
        if (t) return t.sessionId ?? null;
      }
    }
    return null;
  });
  return sessionId ? createElement(FileBrowser, { sessionId }) : null;
}

function mountFromStore(): MountedView {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, retryDelay: 0 } },
  });
  return mount(
    createElement(QueryClientProvider, { client }, createElement(BrowserFromStore)),
  );
}

function mountStandalone(): MountedView {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, retryDelay: 0 } },
  });
  return mount(
    createElement(QueryClientProvider, { client }, createElement(FileBrowser, { sessionId: SID })),
  );
}

function panel(): HTMLElement | null {
  return mounted?.container.querySelector(".nx-alert") ?? null;
}

function buttonTexts(): string[] {
  return [...(mounted?.container.querySelectorAll("button") ?? [])].map(
    (b) => b.textContent?.trim() ?? "",
  );
}

function toolbarButton(label: string): HTMLButtonElement | undefined {
  return [
    ...(mounted?.container.querySelectorAll<HTMLButtonElement>(".nx-toolbar button") ?? []),
  ].find((b) => b.textContent?.trim() === label);
}

function toastTexts(): string {
  return useUi
    .getState()
    .toasts.map((t) => t.text)
    .join("\n");
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  window.localStorage.clear();
  resetCwdFollowForTest();
  mocks.roots.mockResolvedValue({
    pathStyle: "unknown",
    roots: [],
    homePath: null,
    supported: false,
  });
  mocks.list.mockRejectedValue(NOT_FOUND);
  mocks.lineExec.mockResolvedValue(undefined);
  mocks.copy.mockResolvedValue(undefined);
  mocks.connect.mockResolvedValue(NEW_SESSION);
  mocks.connectLocal.mockResolvedValue(NEW_LOCAL_SESSION);
  mocks.knownHostAccept.mockResolvedValue(undefined);
  mocks.ask.mockResolvedValue(false);
  mocks.promptText.mockResolvedValue(null);
  mocks.listenEvent.mockReturnValue(Promise.resolve(() => {}));
  useUi.setState({ workspaces: [], activeWorkspaceId: null, sessions: [], toasts: [] });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("FileBrowser deleted sessions", () => {
  it("shows the real reason and a reconnect action after automatic reconnection fails, without an ineffective retry action", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.connect.mockRejectedValue(new Error("dial tcp: connection refused"));
    mounted = mountFromStore();

    await waitFor(() => expect(panel()?.textContent).toContain("会话已在服务端删除"));
    expect(panel()?.textContent).not.toContain("请刷新后重试");
    expect(buttonTexts()).toContain("重新连接这台主机");
    expect(buttonTexts()).not.toContain("重试");
    await waitFor(() => expect(mocks.connect).toHaveBeenCalledTimes(1));
    expect(mocks.connect).toHaveBeenCalledWith("asset-1");
    await flush();
    expect(mocks.connect).toHaveBeenCalledTimes(1);
  });

  it("reconnects once after detecting a dead session: fresh connect reuses the original tab for the new session without duplicating the workspace, session, or tab", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.list.mockImplementation((sid: string) =>
      sid === SID ? Promise.reject(NOT_FOUND) : Promise.resolve(ENTRIES.map((e) => ({ ...e }))),
    );
    mounted = mountFromStore();

    await waitFor(() => expect(mounted!.container.textContent).toContain("a.txt"));
    expect(mocks.connect).toHaveBeenCalledWith("asset-1");
    expect(mocks.connect).toHaveBeenCalledTimes(1);
    expect(mocks.list).toHaveBeenCalledWith("s2", "~");
    const st = useUi.getState();
    expect(st.workspaces).toHaveLength(1);
    expect(st.workspaces[0]?.panes[0]?.tabs).toHaveLength(1);
    expect(storeTabSessionId()).toBe("s2");
    expect(st.workspaces[0]?.sessionId).toBe("s2");
    expect(st.sessions.filter((s) => s.id === "s2")).toHaveLength(1);
    expect(st.sessions.some((s) => s.id === SID)).toBe(false);
    expect(st.sessions).toHaveLength(1);
    expect(toastTexts()).toContain("已重新连接");
    expect(toolbarButton("上传")?.disabled).toBe(false);
    expect(toolbarButton("新建")?.disabled).toBe(false);
  });

  it("shares one reconnect across multiple file tabs on the same dead session and retargets each tab", async () => {
    const makeWorkspace = (id: string, tabId: string) => ({
      id,
      kind: "session" as const,
      title: "web-01",
      sessionId: SID,
      assetId: "asset-1",
      assetKind: "ssh",
      panes: [
        {
          id: `pane-${id}`,
          tabs: [
            {
              id: tabId,
              kind: "files" as const,
              title: "Files",
              sessionId: SID,
              closable: true,
            },
          ],
          activeTabId: tabId,
        },
      ],
      activePaneId: `pane-${id}`,
      splitRatio: 0.5,
      closable: true,
    });
    useUi.setState({
      workspaces: [makeWorkspace("ws1", "files-1"), makeWorkspace("ws2", "files-2")],
      activeWorkspaceId: "ws1",
      sessions: [
        {
          id: SID,
          assetId: "asset-1",
          name: "web-01",
          kind: "ssh",
          status: "connected",
          tabs: [],
          createdAt: 0,
        },
      ],
      toasts: [],
    });
    const pending = deferred<typeof NEW_SESSION>();
    mocks.connect.mockReturnValue(pending.promise);
    mocks.list.mockImplementation((sid: string) =>
      sid === SID ? Promise.reject(NOT_FOUND) : Promise.resolve(ENTRIES.map((e) => ({ ...e }))),
    );
    function BrowserForTab({ tabId }: { tabId: string }) {
      const sessionId = useUi((state) => {
        for (const workspace of state.workspaces) {
          for (const pane of workspace.panes) {
            const tab = pane.tabs.find((candidate) => candidate.id === tabId);
            if (tab) return tab.sessionId ?? null;
          }
        }
        return null;
      });
      return sessionId ? createElement(FileBrowser, { sessionId }) : null;
    }
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mounted = mount(
      createElement(
        QueryClientProvider,
        { client },
        createElement(
          "div",
          null,
          createElement("div", { key: "first" }, createElement(BrowserForTab, { tabId: "files-1" })),
          createElement("div", { key: "second" }, createElement(BrowserForTab, { tabId: "files-2" })),
        ),
      ),
    );

    await waitFor(() => expect(mocks.connect).toHaveBeenCalled());
    await flush();
    pending.resolve(NEW_SESSION);
    await waitFor(() => {
      const tabs = useUi.getState().workspaces.map((workspace) => workspace.panes[0]?.tabs[0]);
      expect(tabs[0]?.sessionId).toBe("s2");
      expect(tabs[1]?.sessionId).toBe("s2");
    });

    expect(mocks.connect).toHaveBeenCalledTimes(1);
    expect(useUi.getState().sessions.map((session) => session.id)).toEqual(["s2"]);
  });

  it("reconnects only once more when the new session is also deleted, then stops without looping", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mounted = mountFromStore();

    await waitFor(() => expect(mocks.connect).toHaveBeenCalledTimes(2));
    await flush();
    expect(mocks.connect).toHaveBeenCalledTimes(2);
    expect(storeTabSessionId()).toBe("s2");
    expect(panel()?.textContent).toContain("会话已在服务端删除");
    expect(buttonTexts()).toContain("重新连接这台主机");
  });

  it("shows a busy reconnecting state and disables the button during automatic reconnection, then restores the manual action after failure", async () => {
    seedWorkspace({ assetId: "asset-1" });
    const pending = deferred<unknown>();
    mocks.connect.mockReturnValue(pending.promise);
    mounted = mountFromStore();

    await flushUntil(() => buttonTexts().includes("正在重新连接…"));
    const busy = [...mounted.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "正在重新连接…",
    );
    expect(busy?.disabled).toBe(true);
    expect(mocks.connect).toHaveBeenCalledTimes(1);

    pending.reject(new Error("dial tcp: connection refused"));
    await flushUntil(() => buttonTexts().includes("重新连接这台主机"));
    expect(toastTexts()).toContain("重新连接失败");
    expect(mocks.connect).toHaveBeenCalledTimes(1);
  });

  it("synchronizes only workspaces tracking the old session when updateTab retargets a session", () => {
    const tab: AppTab = { id: "t1", kind: "files", title: "Files", sessionId: SID, closable: true };
    useUi.setState({
      workspaces: [
        {
          id: "ws1",
          kind: "session",
          title: "web-01",
          sessionId: "other",
          panes: [{ id: "p1", tabs: [tab], activeTabId: tab.id }],
          activePaneId: "p1",
          splitRatio: 0.5,
          closable: true,
        },
      ],
      sessions: [],
      toasts: [],
    });
    useUi.getState().updateTab("t1", { sessionId: "s2" });
    expect(useUi.getState().workspaces[0]?.sessionId).toBe("other");
    expect(useUi.getState().workspaces[0]?.panes[0]?.tabs[0]?.sessionId).toBe("s2");
  });

  it("disables toolbar and menu actions that would reach the backend for a dead session and explains the real reason", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.connect.mockRejectedValue(new Error("dial tcp: connection refused"));
    mounted = mountFromStore();
    await waitFor(() => expect(panel()).not.toBeNull());

    for (const label of ["上传", "文件夹", "新建"] as const) {
      const btn = toolbarButton(label);
      expect(btn?.disabled).toBe(true);
      expect(btn?.title).toBe("会话已在服务端删除，请重新连接这台主机");
    }
    const goneTitled = [
      ...mounted.container.querySelectorAll<HTMLButtonElement>(".nx-toolbar button"),
    ].filter((b) => b.title === "会话已在服务端删除，请重新连接这台主机");
    expect(goneTitled.every((b) => b.disabled)).toBe(true);
    expect(goneTitled.some((b) => b.textContent?.trim() === "")).toBe(true);

    const more = mounted.container.querySelector<HTMLButtonElement>(
      'button[aria-label="更多操作"]',
    );
    expect(more).not.toBeNull();
    act(() => {
      more!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await flush();
    const items = [...mounted.container.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')];
    for (const label of ["打包下载当前目录", "上传到当前目录", "上传文件夹到当前目录", "新建目录", "刷新"]) {
      const item = items.find((b) => b.textContent?.includes(label));
      expect(item?.disabled, label).toBe(true);
      expect(item?.textContent, label).toContain("会话已删除");
    }
    for (const label of ["在当前终端打开", "在新终端打开"]) {
      expect(
        items.find((b) => b.textContent?.includes(label))?.disabled,
        label,
      ).toBe(false);
    }
    expect(mocks.upload).not.toHaveBeenCalled();
    expect(mocks.mkdir).not.toHaveBeenCalled();
  });

  it("cancelling the host-key change confirmation does not trust or retry, keeps the real reason in the panel, and retries only after a manual click", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.connect.mockRejectedValue(CHANGED_HOST_KEY);
    mounted = mountFromStore();

    await waitFor(() => expect(mocks.ask).toHaveBeenCalledTimes(1));
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("主机 127.0.0.1:22 的密钥已变更");
    await waitFor(() => expect(toastTexts()).toContain("已取消重连"));
    expect(mocks.knownHostAccept).not.toHaveBeenCalled();
    expect(mocks.connect).toHaveBeenCalledTimes(1);
    expect(storeTabSessionId()).toBe(SID);
    expect(panel()?.textContent).toContain("会话已在服务端删除");

    clickButton(mounted.container, "重新连接这台主机");

    await waitFor(() => expect(mocks.ask).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(mocks.connect).toHaveBeenCalledTimes(2));
  });

  it("accepting a host-key change records it, reconnects automatically, and reuses the original tab", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.connect.mockRejectedValueOnce(CHANGED_HOST_KEY);
    mocks.ask.mockResolvedValue(true);
    mocks.list.mockImplementation((sid: string) =>
      sid === SID ? Promise.reject(NOT_FOUND) : Promise.resolve(ENTRIES.map((e) => ({ ...e }))),
    );
    mounted = mountFromStore();

    await waitFor(() => expect(mounted!.container.textContent).toContain("a.txt"));
    expect(mocks.knownHostAccept).toHaveBeenCalledWith("127.0.0.1", 22, "ssh-ed25519", "SHA256:new");
    expect(mocks.connect).toHaveBeenCalledTimes(2);
    expect(storeTabSessionId()).toBe("s2");
    const st = useUi.getState();
    expect(st.workspaces).toHaveLength(1);
    expect(st.workspaces[0]?.panes[0]?.tabs).toHaveLength(1);
  });

  it("reports the real error and preserves the panel and session after automatic reconnection fails, then recovers with a manual retry", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.connect.mockRejectedValue(new Error("dial tcp 127.0.0.1:22: connect: connection refused"));
    mounted = mountFromStore();

    await waitFor(() => expect(toastTexts()).toContain("重新连接失败"));
    expect(toastTexts()).toContain("connection refused");
    expect(storeTabSessionId()).toBe(SID);
    expect(useUi.getState().sessions.map((s) => s.id)).toEqual([SID]);
    expect(panel()?.textContent).toContain("会话已在服务端删除");
    expect(mocks.connect).toHaveBeenCalledTimes(1);

    clickButton(mounted.container, "重新连接这台主机");

    await waitFor(() => expect(mocks.connect).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(buttonTexts()).toContain("重新连接这台主机"));

    mocks.connect.mockResolvedValue(NEW_SESSION);
    mocks.list.mockImplementation((sid: string) =>
      sid === SID ? Promise.reject(NOT_FOUND) : Promise.resolve(ENTRIES.map((e) => ({ ...e }))),
    );
    clickButton(mounted.container, "重新连接这台主机");

    await waitFor(() => expect(mounted!.container.textContent).toContain("a.txt"));
    expect(mocks.connect).toHaveBeenCalledTimes(3);
    expect(storeTabSessionId()).toBe("s2");
  });

  it("falls back to connectLocal for a local session without an asset and reuses the original tab", async () => {
    seedWorkspace();
    mocks.list.mockImplementation((sid: string) =>
      sid === SID ? Promise.reject(NOT_FOUND) : Promise.resolve(ENTRIES.map((e) => ({ ...e }))),
    );
    mounted = mountFromStore();

    await waitFor(() => expect(mounted!.container.textContent).toContain("a.txt"));
    expect(mocks.connectLocal).toHaveBeenCalledTimes(1);
    expect(mocks.connect).not.toHaveBeenCalled();
    expect(storeTabSessionId()).toBe("s-local");
  });

  it("reconnects a dead session in a device workspace through the device connection and loads the new session in the original tab", async () => {
    seedWorkspace({ assetId: "device/d-1", assetKind: "device", title: "build-box" });
    mocks.connectDevice.mockResolvedValue(NEW_DEVICE_SESSION);
    mocks.list.mockImplementation((sid: string) =>
      sid === SID ? Promise.reject(NOT_FOUND) : Promise.resolve(ENTRIES.map((e) => ({ ...e }))),
    );
    mounted = mountFromStore();

    await waitFor(() => expect(mounted!.container.textContent).toContain("a.txt"));
    expect(mocks.connectDevice).toHaveBeenCalledWith("d-1", "build-box");
    expect(mocks.connectDevice).toHaveBeenCalledTimes(1);
    expect(mocks.connect).not.toHaveBeenCalled();
    expect(mocks.connectLocal).not.toHaveBeenCalled();
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(storeTabSessionId()).toBe("s-dev");
    expect(useUi.getState().sessions.filter((s) => s.id === "s-dev")).toHaveLength(1);
    expect(toastTexts()).toContain("已重新连接");
  });

  it("reports the real missing-asset reason for an SSH asset that no longer exists without blindly retrying", async () => {
    seedWorkspace({ assetId: "asset-gone" });
    mocks.connect.mockRejectedValue({ code: "not_found", message: "资产不存在" });
    mounted = mountFromStore();

    await waitFor(() => expect(toastTexts()).toContain("重新连接失败"));
    expect(toastTexts()).toContain("资源不存在：资产不存在");
    expect(mocks.connect).toHaveBeenCalledWith("asset-gone");
    await flush();
    expect(mocks.connect).toHaveBeenCalledTimes(1);
    expect(storeTabSessionId()).toBe(SID);
    expect(panel()?.textContent).toContain("会话已在服务端删除");
  });

  it("reports the precise reason when the file tab is missing and does not connect blindly or retry in a loop", async () => {
    mounted = mountStandalone();

    await waitFor(() => expect(toastTexts()).toContain("找不到这个文件标签的主机信息"));
    expect(mocks.connect).not.toHaveBeenCalled();
    expect(mocks.connectLocal).not.toHaveBeenCalled();
    expect(useUi.getState().toasts).toHaveLength(1);
    const firstToastId = useUi.getState().toasts[0]?.id;

    clickButton(mounted.container, "重新连接这台主机");

    await waitFor(() => expect(useUi.getState().toasts[0]?.id).not.toBe(firstToastId));
    expect(useUi.getState().toasts).toHaveLength(1);
    expect(mocks.connect).not.toHaveBeenCalled();
    expect(mocks.connectLocal).not.toHaveBeenCalled();
  });
});

describe("FileBrowser transient file errors", () => {
  it("preserves the real error and retry action after one automatic retry fails, recovers with a manual retry, and does not offer reconnection", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.list.mockRejectedValueOnce({ code: "sftp", message: "SFTP：连接被对端重置" });
    mocks.list.mockRejectedValueOnce({ code: "sftp", message: "SFTP：连接被对端重置" });
    mocks.list.mockResolvedValue(ENTRIES.map((e) => ({ ...e })));
    mounted = mountFromStore();

    await waitFor(() => expect(panel()?.textContent).toContain("连接被对端重置"));
    expect(mocks.list).toHaveBeenCalledTimes(2);
    expect(buttonTexts()).toContain("重试");
    expect(buttonTexts()).not.toContain("重新连接这台主机");
    expect(toolbarButton("上传")?.disabled).toBe(false);
    expect(toolbarButton("新建")?.disabled).toBe(false);

    clickButton(mounted.container, "重试");

    await waitFor(() => expect(mounted!.container.textContent).toContain("a.txt"));
    expect(mocks.list).toHaveBeenCalledTimes(3);
    expect(mocks.connect).not.toHaveBeenCalled();
    expect(mocks.connectLocal).not.toHaveBeenCalled();
    await flush();
    expect(panel()).toBeNull();
  });
});

describe("FileBrowser production retry policy (retry: 1)", () => {
  function mountProductionRetry(): MountedView {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: 1, retryDelay: 0, refetchOnWindowFocus: false } },
    });
    return mount(
      createElement(QueryClientProvider, { client }, createElement(BrowserFromStore)),
    );
  }

  it("does not automatically retry not_found: calls fsApi.list once and shows the dead-session panel", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.connect.mockRejectedValue(new Error("dial tcp: connection refused"));
    mounted = mountProductionRetry();

    await waitFor(() => expect(panel()?.textContent).toContain("会话已在服务端删除"));
    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(mocks.list).toHaveBeenCalledWith(SID, "~");
    await flush();
    expect(mocks.list).toHaveBeenCalledTimes(1);
  });

  it("keeps one automatic retry for transient errors: calls twice after failure and recovers with a later retry", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.list.mockRejectedValue({ code: "sftp", message: "SFTP：连接被对端重置" });
    mounted = mountProductionRetry();

    await waitFor(() => expect(panel()?.textContent).toContain("连接被对端重置"));
    expect(mocks.list).toHaveBeenCalledTimes(2);

    mocks.list.mockResolvedValue(ENTRIES.map((e) => ({ ...e })));
    clickButton(mounted.container, "重试");

    await waitFor(() => expect(mounted!.container.textContent).toContain("a.txt"));
    expect(mocks.list).toHaveBeenCalledTimes(3);
  });
});

const WINDOWS_ROOTS = {
  pathStyle: "windows" as const,
  roots: [
    { path: "C:/", label: "C:" },
    { path: "D:/", label: "D:" },
  ],
  homePath: "C:/Users/test",
  supported: true,
};

function windowsEntry(path: string, kind = "dir"): FileEntryDto {
  const name = path.split("/").filter(Boolean).pop() ?? path;
  return { ...entry(name, kind), path };
}

function browserItem(path: string): HTMLElement | undefined {
  return [...(mounted?.container.querySelectorAll<HTMLElement>('[role="treeitem"]') ?? [])].find(
    (row) => row.getAttribute("title") === path,
  );
}

function doubleClick(element: HTMLElement): void {
  act(() => {
    element.dispatchEvent(new MouseEvent("dblclick", { bubbles: true, cancelable: true }));
  });
}

function pressEnter(element: HTMLInputElement): void {
  act(() => {
    element.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
  });
}

function pathInput(): HTMLInputElement {
  const input = mounted?.container.querySelector<HTMLInputElement>(".nx-toolbar input");
  if (!input) throw new Error("path input not found");
  return input;
}

function upButton(): HTMLButtonElement {
  const button = mounted?.container.querySelector<HTMLButtonElement>(".nx-toolbar > button");
  if (!button) throw new Error("up button not found");
  return button;
}

function openBlankMenu(): void {
  const scroller = mounted?.container.querySelector('[role="tree"]')?.parentElement;
  if (!scroller) throw new Error("file scroller not found");
  act(() => {
    scroller.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
  });
}

function menuLabels(): string[] {
  return [...(mounted?.container.querySelectorAll('[role="menuitem"]') ?? [])].map(
    (item) => item.textContent ?? "",
  );
}

function attachTerminalTab(): void {
  useUi.setState((state) => ({
    workspaces: state.workspaces.map((workspace) => ({
      ...workspace,
      panes: workspace.panes.map((pane) => ({
        ...pane,
        tabs: [
          {
            id: "terminal-1",
            kind: "terminal" as const,
            title: "Terminal",
            sessionId: SID,
            tabId: "k1",
            closable: true,
          },
          ...pane.tabs,
        ],
        activeTabId: "terminal-1",
      })),
    })),
  }));
}

describe("FileBrowser Windows roots and navigation", () => {
  it("keeps the computer container virtual and excludes file and terminal operations", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.roots.mockResolvedValue(WINDOWS_ROOTS);
    mocks.list.mockResolvedValue([]);
    mounted = mountFromStore();
    await waitFor(() => expect(browserItem("C:/")).toBeDefined());
    expect(browserItem("D:/")).toBeDefined();

    mocks.list.mockClear();
    expect(toolbarButton("上传")?.disabled).toBe(true);
    expect(toolbarButton("文件夹")?.disabled).toBe(true);
    expect(toolbarButton("新建")?.disabled).toBe(true);
    openBlankMenu();
    const labels = menuLabels();
    expect(labels.some((label) => label.includes("刷新"))).toBe(true);
    for (const forbidden of ["上传", "新建", "删除", "终端打开"]) {
      expect(labels.some((label) => label.includes(forbidden)), forbidden).toBe(false);
    }
    await flush();
    expect(mocks.list).not.toHaveBeenCalled();
  });

  it("keeps a drive path error local and allows another drive to open", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.roots.mockResolvedValue(WINDOWS_ROOTS);
    mocks.list.mockImplementation((_sid: string, path: string) => {
      if (path === "C:/") return Promise.resolve([windowsEntry("C:/Windows")]);
      if (path === "D:/") return Promise.reject(PATH_NOT_FOUND);
      return Promise.resolve([]);
    });
    mounted = mountFromStore();
    await waitFor(() => expect(browserItem("D:/")).toBeDefined());

    doubleClick(browserItem("D:/")!);
    await waitFor(() => expect(panel()?.textContent).toContain("目录不存在"));
    await flush();
    expect(mocks.connect).not.toHaveBeenCalled();
    expect(buttonTexts()).toContain("重试");
    expect(buttonTexts()).not.toContain("重新连接这台主机");
    expect(toolbarButton("上传")?.disabled).toBe(false);
    expect(toolbarButton("新建")?.disabled).toBe(false);

    click(upButton());
    await waitFor(() => expect(browserItem("C:/")).toBeDefined());
    doubleClick(browserItem("C:/")!);
    await waitFor(() => expect(browserItem("C:/Windows")).toBeDefined());
    expect(mocks.connect).not.toHaveBeenCalled();
  });

  it("normalizes a bare drive and rejects drive-relative input with an absolute-path hint", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.roots.mockResolvedValue(WINDOWS_ROOTS);
    mocks.list.mockResolvedValue([]);
    mounted = mountFromStore();
    await waitFor(() => expect(browserItem("D:/")).toBeDefined());

    setInputValue(pathInput(), "D:");
    pressEnter(pathInput());
    await waitFor(() => expect(mocks.list).toHaveBeenCalledWith(SID, "D:/"));

    click(upButton());
    await waitFor(() => expect(browserItem("D:/")).toBeDefined());
    setInputValue(pathInput(), "D:relative");
    pressEnter(pathInput());
    await waitFor(() => expect(toastTexts()).toContain("需要绝对路径"));
    expect(mocks.list).not.toHaveBeenCalledWith(SID, "D:relative");
  });

  it("does not auto-reconnect a legacy unclassified not_found and keeps retry and manual recovery", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.list.mockRejectedValue(LEGACY_NOT_FOUND);
    mounted = mountFromStore();

    await waitFor(() => expect(panel()?.textContent).toContain("旧服务返回的资源不存在"));
    await flush();
    expect(mocks.connect).not.toHaveBeenCalled();
    expect(panel()?.textContent).not.toContain("会话已在服务端删除");
    expect(buttonTexts()).toContain("重试");
    expect(buttonTexts()).toContain("重新连接这台主机");
    expect(toolbarButton("上传")?.disabled).toBe(false);
  });

  it("treats a path not_found as a local error without reconnect or global disabling", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.list.mockRejectedValue(PATH_NOT_FOUND);
    mounted = mountFromStore();

    await waitFor(() => expect(panel()?.textContent).toContain("目录不存在"));
    await flush();
    expect(mocks.connect).not.toHaveBeenCalled();
    expect(buttonTexts()).toContain("重试");
    expect(buttonTexts()).not.toContain("重新连接这台主机");
    expect(toolbarButton("上传")?.disabled).toBe(false);
    expect(toolbarButton("新建")?.disabled).toBe(false);
  });

  it("follows a Windows terminal cwd out of the computer container", async () => {
    seedWorkspace({ assetId: "asset-1" });
    attachTerminalTab();
    mocks.roots.mockResolvedValue(WINDOWS_ROOTS);
    mocks.list.mockResolvedValue([]);
    let control: ((payload: unknown) => void) | undefined;
    mocks.listenEvent.mockImplementation((topic: string, handler: (payload: unknown) => void) => {
      if (topic === "terminal://control") control = handler;
      return Promise.resolve(() => {});
    });
    mounted = mountFromStore();
    await waitFor(() => expect(browserItem("D:/")).toBeDefined());

    act(() => {
      control?.({ tabId: "k1", version: 1, cwd: "D:/Project" });
    });
    await waitFor(() => expect(mocks.list).toHaveBeenCalledWith(SID, "D:/Project"));
    await waitFor(() => expect(pathInput().value).toBe("D:/Project"));
  });

  it("keeps a UNC share as the parent boundary before returning to the computer", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.roots.mockResolvedValue(WINDOWS_ROOTS);
    mocks.list.mockResolvedValue([]);
    mounted = mountFromStore();
    await waitFor(() => expect(browserItem("D:/")).toBeDefined());

    setInputValue(pathInput(), String.raw`\\server\share\dir`);
    pressEnter(pathInput());
    await waitFor(() => expect(mocks.list).toHaveBeenCalledWith(SID, "//server/share/dir"));
    click(upButton());
    await waitFor(() => expect(mocks.list).toHaveBeenCalledWith(SID, "//server/share"));
    click(upButton());
    await waitFor(() => expect(browserItem("D:/")).toBeDefined());
    expect(mocks.list).not.toHaveBeenCalledWith(SID, "//server");
  });

  it.each([
    {
      dir: "D:/Project [x]/child's dir",
      command: "Set-Location -LiteralPath 'D:/Project [x]/child''s dir'",
    },
    {
      dir: "//server/share/Project [x]/child's dir",
      command: "Set-Location -LiteralPath '//server/share/Project [x]/child''s dir'",
    },
  ])("uses ExecLine with a literal PowerShell path for the current WinRM terminal: $dir", async ({ dir, command }) => {
    seedWorkspace({ assetId: "asset-1", assetKind: "winrm", title: "windows" });
    attachTerminalTab();
    mocks.list.mockResolvedValue([windowsEntry(dir)]);
    mounted = mountFromStore();
    await waitFor(() => expect(browserItem(dir)).toBeDefined());

    act(() => {
      browserItem(dir)!.dispatchEvent(
        new MouseEvent("contextmenu", { bubbles: true, cancelable: true }),
      );
    });
    await waitFor(() => expect(menuLabels().some((label) => label.includes("在当前终端打开"))).toBe(true));
    const item = [...mounted.container.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find(
      (candidate) => candidate.textContent?.includes("在当前终端打开"),
    );
    click(item!);
    await flush();

    expect(mocks.lineExec).toHaveBeenCalledWith("k1", command);
    expect(mocks.termWrite).not.toHaveBeenCalled();
  });
});

describe("FileBrowser roots errors", () => {
  it("keeps a manual recovery action visible when the roots query loses the session", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.roots.mockRejectedValue(NOT_FOUND);
    mocks.list.mockResolvedValue([]);
    mocks.connect.mockRejectedValue(new Error("dial tcp: connection refused"));
    mounted = mountFromStore();

    await waitFor(() => expect(mocks.connect).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(panel()?.textContent).toContain("会话已在服务端删除"));
    expect(buttonTexts()).toContain("重新连接这台主机");
  });

  it("loads Windows roots after the initial roots request fails and manual retry succeeds", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.roots
      .mockRejectedValueOnce({ code: "sftp", message: "根信息暂时不可用" })
      .mockResolvedValue(WINDOWS_ROOTS);
    mocks.list.mockResolvedValue([]);
    mounted = mountFromStore();

    await waitFor(() => expect(panel()?.textContent).toContain("根信息暂时不可用"));
    clickButton(mounted.container, "重试");

    await waitFor(() => expect(browserItem("C:/")).toBeDefined());
    expect(browserItem("D:/")).toBeDefined();
    expect(mocks.roots).toHaveBeenCalledTimes(2);
    expect(mocks.connect).not.toHaveBeenCalled();
    expect(panel()).toBeNull();
  });
});

describe("FileBrowser new WinRM terminal navigation", () => {
  it("uses ExecLine after the new line tab attaches instead of pending terminal input", async () => {
    seedWorkspace({ assetId: "asset-1", assetKind: "winrm", title: "windows" });
    attachTerminalTab();
    mocks.list.mockResolvedValue([windowsEntry("D:/Project")]);
    mounted = mountFromStore();
    await waitFor(() => expect(browserItem("D:/Project")).toBeDefined());

    act(() => {
      browserItem("D:/Project")!.dispatchEvent(
        new MouseEvent("contextmenu", { bubbles: true, cancelable: true }),
      );
    });
    await waitFor(() => expect(menuLabels().some((label) => label.includes("在新终端打开"))).toBe(true));
    const item = [...mounted.container.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find(
      (candidate) => candidate.textContent?.includes("在新终端打开"),
    );
    click(item!);

    await waitFor(() =>
      expect(
        useUi.getState().workspaces[0]?.panes[0]?.tabs.filter((tab) => tab.kind === "terminal"),
      ).toHaveLength(2),
    );
    const newTab = useUi
      .getState()
      .workspaces[0]?.panes[0]?.tabs.find(
        (tab) => tab.kind === "terminal" && tab.id !== "terminal-1",
      );
    expect(newTab?.pendingDirectory).toBeUndefined();
    act(() => {
      useUi.getState().updateTab(newTab!.id, { tabId: "k2" });
    });

    await waitFor(() =>
      expect(mocks.lineExec).toHaveBeenCalledWith("k2", "Set-Location -LiteralPath 'D:/Project'"),
    );
    expect(mocks.termWrite).not.toHaveBeenCalled();
  });
});

describe("FileBrowser shell-aware terminal navigation", () => {
  beforeEach(() => mocks.termWrite.mockResolvedValue(undefined));

  async function openCurrentAt(path: string): Promise<void> {
    act(() => {
      browserItem(path)!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
    });
    await waitFor(() => expect(menuLabels().some((label) => label.includes("在当前终端打开"))).toBe(true));
    const item = [...mounted!.container.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find(
      (candidate) => candidate.textContent?.includes("在当前终端打开"),
    );
    click(item!);
    await flush();
  }

  it.each([
    { shell: "powershell" as const, dir: "D:/Project [x]/child's dir", command: "Set-Location -LiteralPath 'D:/Project [x]/child''s dir'\n" },
    { shell: "posix" as const, dir: "/tmp/child's [x]", command: String.raw`cd '/tmp/child'\''s [x]'` + "\n" },
  ])("uses the existing terminal's $shell command", async ({ shell, dir, command }) => {
    seedWorkspace({ assetId: "asset-1" });
    attachTerminalTab();
    useUi.getState().updateTab("terminal-1", { shell });
    mocks.list.mockResolvedValue([windowsEntry(dir)]);
    mounted = mountFromStore();
    await waitFor(() => expect(browserItem(dir)).toBeDefined());
    await openCurrentAt(dir);
    expect(mocks.termWrite).toHaveBeenCalledTimes(1);
    expect(new TextDecoder().decode(mocks.termWrite.mock.calls[0][1])).toBe(command);
    expect(mocks.lineExec).not.toHaveBeenCalled();
  });

  it.each(["cmd", "unknown"] as const)("does not inject into a %s terminal", async (shell) => {
    const dir = shell === "cmd" ? "//server/share/Project [x]" : "D:/Project [x]";
    seedWorkspace({ assetId: "asset-1" });
    attachTerminalTab();
    useUi.getState().updateTab("terminal-1", { shell });
    mocks.list.mockResolvedValue([windowsEntry(dir)]);
    mounted = mountFromStore();
    await waitFor(() => expect(browserItem(dir)).toBeDefined());
    await openCurrentAt(dir);
    expect(mocks.termWrite).not.toHaveBeenCalled();
    expect(mocks.copy).toHaveBeenCalledWith(dir);
    await waitFor(() => expect(toastTexts()).toContain("路径已复制"));
  });

  it("surfaces a copy failure without injecting into the terminal", async () => {
    const dir = "D:/Project [x]";
    seedWorkspace({ assetId: "asset-1" });
    attachTerminalTab();
    useUi.getState().updateTab("terminal-1", { shell: "cmd" });
    mocks.list.mockResolvedValue([windowsEntry(dir)]);
    mocks.copy.mockRejectedValueOnce(new Error("clipboard denied"));
    mounted = mountFromStore();
    await waitFor(() => expect(browserItem(dir)).toBeDefined());
    await openCurrentAt(dir);

    expect(mocks.termWrite).not.toHaveBeenCalled();
    expect(mocks.copy).toHaveBeenCalledWith(dir);
    await waitFor(() => {
      expect(toastTexts()).toContain("复制路径失败");
      expect(toastTexts()).toContain("clipboard denied");
    });
  });

  it("stores a new terminal's directory as runtime pending state instead of a precomputed command", async () => {
    const dir = "D:/Project [x]/child's dir";
    seedWorkspace({ assetId: "asset-1" });
    mocks.list.mockResolvedValue([windowsEntry(dir)]);
    mounted = mountFromStore();
    await waitFor(() => expect(browserItem(dir)).toBeDefined());
    act(() => {
      browserItem(dir)!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
    });
    const item = [...mounted.container.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find(
      (candidate) => candidate.textContent?.includes("在新终端打开"),
    );
    click(item!);
    await waitFor(() =>
      expect(useUi.getState().workspaces[0]?.panes[0]?.tabs.filter((tab) => tab.kind === "terminal")).toHaveLength(1),
    );
    const newTab = useUi.getState().workspaces[0]?.panes[0]?.tabs.find((tab) => tab.kind === "terminal" && tab.id !== "files-1");
    expect(newTab?.pendingDirectory).toBe(dir);
    expect(mocks.termWrite).not.toHaveBeenCalled();
  });
});
