/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, clickButton, deferred, flush, flushUntil, mount, setInputValue, waitForRendered, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  promptText: vi.fn(),
  pickLocalFile: vi.fn(),
  pickSavePath: vi.fn(),
  finishSave: vi.fn(),
  discardStaged: vi.fn(),
  roots: vi.fn(),
  list: vi.fn(),
  write: vi.fn(),
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
      write: mocks.write,
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

import { FileTree } from "../../features/files/FileTree";
import { resetCwdFollowForTest } from "../../features/files/cwdFollow";
import { useUi, type AppTab } from "../../app/store";
import type { FileEntryDto } from "../../ipc/types";

const SID = "s1";
const GONE_HINT = "会话已在服务端删除，请重新连接这台主机";

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
  const title = options.title ?? "web-01";
  const kind = options.assetKind ?? (options.assetId ? "ssh" : "local");
  const tab: AppTab = {
    id: "t1",
    kind: "terminal",
    title,
    sessionId: SID,
    closable: true,
  };
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
    leftOpen: true,
    leftWidth: 260,
    toasts: [],
  });
}

function workspaceSessionId(): string | undefined {
  return useUi.getState().workspaces.find((w) => w.id === "ws1")?.sessionId;
}

function TreeFromStore() {
  const sessionId = useUi((s) => s.workspaces.find((w) => w.id === "ws1")?.sessionId ?? null);
  return sessionId ? createElement(FileTree, { sessionId }) : null;
}

function mountTree(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(TreeFromStore)));
}

function button(label: string): HTMLButtonElement | undefined {
  return [...(mounted?.container.querySelectorAll("button") ?? [])].find(
    (b) => b.textContent?.trim() === label,
  );
}

function toolbarButton(ariaLabel: string): HTMLButtonElement | undefined {
  return (
    mounted?.container.querySelector<HTMLButtonElement>(`button[aria-label="${ariaLabel}"]`) ??
    undefined
  );
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
  useUi.setState({
    workspaces: [],
    activeWorkspaceId: null,
    sessions: [],
    leftOpen: true,
    leftWidth: 260,
    toasts: [],
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("FileTree deleted session", () => {
  it("after automatic reconnect fails, shows the real cause and a manual reconnect action, omits the ineffective retry action, and explains disabled toolbar actions", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.connect.mockRejectedValue(new Error("dial tcp: connection refused"));
    mounted = mountTree();

    await waitForRendered(() => expect(mounted!.container.textContent).toContain("会话已在服务端删除"));
    expect(mounted.container.textContent).not.toContain("请刷新后重试");
    expect(button("重新连接这台主机")).toBeDefined();
    expect(button("重试")).toBeUndefined();
    for (const ariaLabel of ["新建文件", "新建目录", "刷新文件列表", "上传到当前目录"]) {
      const btn = toolbarButton(ariaLabel);
      expect(btn?.disabled, ariaLabel).toBe(true);
      expect(btn?.title, ariaLabel).toBe(GONE_HINT);
    }
    expect(toolbarButton("折叠全部目录")?.disabled).toBe(false);
    await waitForRendered(() => expect(mocks.connect).toHaveBeenCalledTimes(1));
    expect(mocks.connect).toHaveBeenCalledWith("asset-1");
    await flush();
    expect(mocks.connect).toHaveBeenCalledTimes(1);
  });

  it("automatically reconnects once after detecting a dead session: fresh connect synchronizes the workspace session, reuses and loads the tree, and avoids duplicate workspaces/sessions", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.list.mockImplementation((sid: string) =>
      sid === SID ? Promise.reject(NOT_FOUND) : Promise.resolve(ENTRIES.map((e) => ({ ...e }))),
    );
    mounted = mountTree();

    await waitForRendered(() => expect(mounted!.container.textContent).toContain("a.txt"));
    expect(mocks.connect).toHaveBeenCalledWith("asset-1");
    expect(mocks.connect).toHaveBeenCalledTimes(1);
    expect(mocks.list).toHaveBeenCalledWith("s2", "~");
    expect(workspaceSessionId()).toBe("s2");
    const st = useUi.getState();
    expect(st.workspaces).toHaveLength(1);
    expect(st.sessions.filter((s) => s.id === "s2")).toHaveLength(1);
    expect(st.sessions.some((s) => s.id === SID)).toBe(false);
    expect(st.sessions).toHaveLength(1);
    expect(toastTexts()).toContain("已重新连接");
    expect(toolbarButton("上传到当前目录")?.disabled).toBe(false);
  });

  it("reconnects only once more when the new session is also deleted, then stops without looping", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mounted = mountTree();

    await waitForRendered(() => expect(mocks.connect).toHaveBeenCalledTimes(2));
    await flush();
    expect(mocks.connect).toHaveBeenCalledTimes(2);
    expect(workspaceSessionId()).toBe("s2");
    expect(mounted!.container.textContent).toContain("会话已在服务端删除");
    expect(button("重新连接这台主机")).toBeDefined();
  });

  it("shows a reconnecting state and disables the button during automatic reconnect, then restores the manual button after failure", async () => {
    seedWorkspace({ assetId: "asset-1" });
    const pending = deferred<unknown>();
    mocks.connect.mockReturnValue(pending.promise);
    mounted = mountTree();

    await flushUntil(() => button("正在重新连接…") !== undefined);
    expect(button("正在重新连接…")?.disabled).toBe(true);
    expect(mocks.connect).toHaveBeenCalledTimes(1);

    pending.reject(new Error("dial tcp: connection refused"));
    await flushUntil(() => button("重新连接这台主机") !== undefined);
    expect(toastTexts()).toContain("重新连接失败");
    expect(mocks.connect).toHaveBeenCalledTimes(1);
  });

  it("canceling host-key change confirmation does not trust or retry, preserves the workspace session, and retries only after a manual click", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.connect.mockRejectedValue(CHANGED_HOST_KEY);
    mounted = mountTree();

    await waitForRendered(() => expect(mocks.ask).toHaveBeenCalledTimes(1));
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("主机 127.0.0.1:22 的密钥已变更");
    await waitForRendered(() => expect(toastTexts()).toContain("已取消重连"));
    expect(mocks.knownHostAccept).not.toHaveBeenCalled();
    expect(mocks.connect).toHaveBeenCalledTimes(1);
    expect(workspaceSessionId()).toBe(SID);
    expect(mounted.container.textContent).toContain("会话已在服务端删除");

    clickButton(mounted.container, "重新连接这台主机");

    await waitForRendered(() => expect(mocks.ask).toHaveBeenCalledTimes(2));
    await waitForRendered(() => expect(mocks.connect).toHaveBeenCalledTimes(2));
  });

  it("automatic reconnect failure: reports the real error, preserves the workspace session, and recovers after a successful manual retry", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.connect.mockRejectedValue(new Error("dial tcp 127.0.0.1:22: connect: connection refused"));
    mounted = mountTree();

    await waitForRendered(() => expect(toastTexts()).toContain("重新连接失败"));
    expect(toastTexts()).toContain("无法连接到目标主机");
    expect(workspaceSessionId()).toBe(SID);
    expect(mounted.container.textContent).toContain("会话已在服务端删除");
    expect(mocks.connect).toHaveBeenCalledTimes(1);

    clickButton(mounted.container, "重新连接这台主机");

    await waitForRendered(() => expect(mocks.connect).toHaveBeenCalledTimes(2));
    await waitForRendered(() => expect(button("重新连接这台主机")).toBeDefined());

    mocks.connect.mockResolvedValue(NEW_SESSION);
    mocks.list.mockImplementation((sid: string) =>
      sid === SID ? Promise.reject(NOT_FOUND) : Promise.resolve(ENTRIES.map((e) => ({ ...e }))),
    );
    clickButton(mounted.container, "重新连接这台主机");

    await waitForRendered(() => expect(mounted!.container.textContent).toContain("a.txt"));
    expect(mocks.connect).toHaveBeenCalledTimes(3);
    expect(workspaceSessionId()).toBe("s2");
  });

  it("local sessions without assets automatically fall back to connectLocal and synchronize the workspace session", async () => {
    seedWorkspace();
    mocks.list.mockImplementation((sid: string) =>
      sid === SID ? Promise.reject(NOT_FOUND) : Promise.resolve(ENTRIES.map((e) => ({ ...e }))),
    );
    mounted = mountTree();

    await waitForRendered(() => expect(mounted!.container.textContent).toContain("a.txt"));
    expect(mocks.connectLocal).toHaveBeenCalledTimes(1);
    expect(mocks.connect).not.toHaveBeenCalled();
    expect(workspaceSessionId()).toBe("s-local");
  });

  it("dead sessions in device workspaces automatically reconnect through the device connection and synchronize the workspace session", async () => {
    seedWorkspace({ assetId: "device/d-1", assetKind: "device", title: "build-box" });
    mocks.connectDevice.mockResolvedValue(NEW_DEVICE_SESSION);
    mocks.list.mockImplementation((sid: string) =>
      sid === SID ? Promise.reject(NOT_FOUND) : Promise.resolve(ENTRIES.map((e) => ({ ...e }))),
    );
    mounted = mountTree();

    await waitForRendered(() => expect(mounted!.container.textContent).toContain("a.txt"));
    expect(mocks.connectDevice).toHaveBeenCalledWith("d-1", "build-box");
    expect(mocks.connectDevice).toHaveBeenCalledTimes(1);
    expect(mocks.connect).not.toHaveBeenCalled();
    expect(mocks.connectLocal).not.toHaveBeenCalled();
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(workspaceSessionId()).toBe("s-dev");
    expect(useUi.getState().sessions.filter((s) => s.id === "s-dev")).toHaveLength(1);
    expect(toastTexts()).toContain("已重新连接");
  });

  it("reports the real missing-asset error for an ssh asset that no longer exists instead of blindly retrying", async () => {
    seedWorkspace({ assetId: "asset-gone" });
    mocks.connect.mockRejectedValue({ code: "not_found", message: "资产不存在" });
    mounted = mountTree();

    await waitForRendered(() => expect(toastTexts()).toContain("重新连接失败"));
    expect(toastTexts()).toContain("重新连接失败：资产不存在");
    expect(mocks.connect).toHaveBeenCalledWith("asset-gone");
    await flush();
    expect(mocks.connect).toHaveBeenCalledTimes(1);
    expect(workspaceSessionId()).toBe(SID);
  });
});

describe("FileTree transient file errors", () => {
  it("preserves the real error and retry action, leaves the toolbar enabled, recovers after a successful retry, and does not suggest reconnecting", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.list.mockRejectedValueOnce({ code: "sftp", message: "SFTP：连接被对端重置" });
    mocks.list.mockResolvedValue(ENTRIES.map((e) => ({ ...e })));
    mounted = mountTree();

    await waitForRendered(() => expect(mounted!.container.textContent).toContain("连接被对端重置"));
    expect(button("重试")).toBeDefined();
    expect(button("重新连接这台主机")).toBeUndefined();
    for (const ariaLabel of ["新建文件", "新建目录", "刷新文件列表", "上传到当前目录"]) {
      expect(toolbarButton(ariaLabel)?.disabled, ariaLabel).toBe(false);
    }

    clickButton(mounted.container, "重试");

    await waitForRendered(() => expect(mounted!.container.textContent).toContain("a.txt"));
    expect(mocks.connect).not.toHaveBeenCalled();
    expect(mocks.connectLocal).not.toHaveBeenCalled();
    await flush();
    expect(button("重试")).toBeUndefined();
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

function treeItem(path: string): HTMLElement | undefined {
  return [...(mounted?.container.querySelectorAll<HTMLElement>('[role="treeitem"]') ?? [])].find(
    (row) => row.getAttribute("title") === path,
  );
}

function pressEnter(element: HTMLInputElement): void {
  act(() => {
    element.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
  });
}

function openBlankMenu(): void {
  const tree = mounted?.container.querySelector('[role="tree"]');
  if (!tree) throw new Error("tree not found");
  act(() => {
    tree.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
  });
}

function menuLabels(): string[] {
  return [...(mounted?.container.querySelectorAll('[role="menuitem"]') ?? [])].map(
    (item) => item.textContent ?? "",
  );
}

describe("FileTree Windows roots and navigation", () => {
  it("keeps the computer container virtual and excludes file and terminal operations", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.roots.mockResolvedValue(WINDOWS_ROOTS);
    mocks.list.mockResolvedValue([]);
    mounted = mountTree();
    await waitForRendered(() => expect(treeItem("C:/")).toBeDefined());
    expect(treeItem("D:/")).toBeDefined();

    mocks.list.mockClear();
    for (const label of ["新建文件", "新建目录", "上传到当前目录"]) {
      expect(toolbarButton(label)?.disabled, label).toBe(true);
    }
    openBlankMenu();
    const labels = menuLabels();
    expect(labels.some((label) => label.includes("刷新"))).toBe(true);
    for (const forbidden of ["上传", "新建", "删除", "终端打开"]) {
      expect(labels.some((label) => label.includes(forbidden)), forbidden).toBe(false);
    }
    await flush();
    expect(mocks.list).not.toHaveBeenCalled();
  });

  it("expands drives independently and isolates a missing path to its own drive", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.roots.mockResolvedValue(WINDOWS_ROOTS);
    mocks.list.mockImplementation((_sid: string, path: string) => {
      if (path === "C:/") return Promise.resolve([windowsEntry("C:/Windows")]);
      if (path === "D:/") return Promise.reject(PATH_NOT_FOUND);
      return Promise.resolve([]);
    });
    mounted = mountTree();
    await waitForRendered(() => expect(treeItem("D:/")).toBeDefined());

    click(treeItem("C:/")!);
    await waitForRendered(() => expect(treeItem("C:/Windows")).toBeDefined());
    click(treeItem("D:/")!);
    await waitForRendered(() => expect(mounted!.container.textContent).toContain("目录不存在"));

    expect(treeItem("C:/Windows")).toBeDefined();
    expect(treeItem("D:/")).toBeDefined();
    expect(mocks.connect).not.toHaveBeenCalled();
    expect(toolbarButton("刷新文件列表")?.disabled).toBe(false);
    expect(button("重新连接这台主机")).toBeUndefined();
    expect(button("重试")).toBeDefined();

    mocks.list.mockImplementation((_sid: string, path: string) => {
      if (path === "C:/") return Promise.resolve([windowsEntry("C:/Windows")]);
      if (path === "D:/") return Promise.resolve([windowsEntry("D:/Project")]);
      return Promise.resolve([]);
    });
    clickButton(mounted.container, "重试");
    await waitForRendered(() => expect(treeItem("D:/Project")).toBeDefined());
    expect(treeItem("C:/Windows")).toBeDefined();
  });

  it("normalizes a bare drive and rejects drive-relative input with an absolute-path hint", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.roots.mockResolvedValue(WINDOWS_ROOTS);
    mocks.list.mockResolvedValue([]);
    mounted = mountTree();
    await waitForRendered(() => expect(treeItem("D:/")).toBeDefined());

    click(toolbarButton("输入路径跳转")!);
    let input = mounted.container.querySelector<HTMLInputElement>(".nx-pathbar input");
    expect(input).not.toBeNull();
    setInputValue(input!, "D:");
    pressEnter(input!);
    await waitForRendered(() => expect(mocks.list).toHaveBeenCalledWith(SID, "D:/"));

    click(toolbarButton("上级目录 此电脑")!);
    click(toolbarButton("输入路径跳转")!);
    input = mounted.container.querySelector<HTMLInputElement>(".nx-pathbar input");
    setInputValue(input!, "D:relative");
    pressEnter(input!);
    await waitForRendered(() => expect(toastTexts()).toContain("需要绝对路径"));
    expect(mocks.list).not.toHaveBeenCalledWith(SID, "D:relative");
  });

  it("treats an unclassified not_found as an ordinary error without automatic or manual reconnect", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.list.mockRejectedValue(LEGACY_NOT_FOUND);
    mounted = mountTree();

    await waitForRendered(() => expect(mounted!.container.textContent).toContain("旧服务返回的资源不存在"));
    await flush();
    expect(mocks.connect).not.toHaveBeenCalled();
    expect(mounted.container.textContent).not.toContain("会话已在服务端删除");
    expect(button("重试")).toBeDefined();
    expect(button("重新连接这台主机")).toBeUndefined();
    expect(toolbarButton("刷新文件列表")?.disabled).toBe(false);
  });

  it("treats a path not_found as a local error without reconnect or global disabling", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.list.mockRejectedValue(PATH_NOT_FOUND);
    mounted = mountTree();

    await waitForRendered(() => expect(mounted!.container.textContent).toContain("目录不存在"));
    await flush();
    expect(mocks.connect).not.toHaveBeenCalled();
    expect(button("重试")).toBeDefined();
    expect(button("重新连接这台主机")).toBeUndefined();
    expect(toolbarButton("刷新文件列表")?.disabled).toBe(false);
  });

  it("follows a Windows terminal cwd out of the computer container", async () => {
    seedWorkspace({ assetId: "asset-1" });
    useUi.getState().updateTab("t1", { tabId: "k1" });
    mocks.roots.mockResolvedValue(WINDOWS_ROOTS);
    mocks.list.mockResolvedValue([]);
    let control: ((payload: unknown) => void) | undefined;
    mocks.listenEvent.mockImplementation((topic: string, handler: (payload: unknown) => void) => {
      if (topic === "terminal://control") control = handler;
      return Promise.resolve(() => {});
    });
    mounted = mountTree();
    await waitForRendered(() => expect(treeItem("D:/")).toBeDefined());

    act(() => {
      control?.({ tabId: "k1", version: 1, cwd: "D:/Project" });
    });
    await waitForRendered(() => expect(mocks.list).toHaveBeenCalledWith(SID, "D:/Project"));
    await waitForRendered(() =>
      expect(mounted!.container.querySelector('.nx-path-crumb[title="D:/Project"]')).not.toBeNull(),
    );
  });

  it("keeps a UNC share as the parent boundary before returning to the computer", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.roots.mockResolvedValue(WINDOWS_ROOTS);
    mocks.list.mockResolvedValue([]);
    mounted = mountTree();
    await waitForRendered(() => expect(treeItem("D:/")).toBeDefined());

    click(toolbarButton("输入路径跳转")!);
    const input = mounted.container.querySelector<HTMLInputElement>(".nx-pathbar input");
    setInputValue(input!, String.raw`\\server\share\dir`);
    pressEnter(input!);
    await waitForRendered(() => expect(mocks.list).toHaveBeenCalledWith(SID, "//server/share/dir"));

    click(toolbarButton("上级目录 //server/share")!);
    await waitForRendered(() => expect(mocks.list).toHaveBeenCalledWith(SID, "//server/share"));
    click(toolbarButton("上级目录 此电脑")!);
    await waitForRendered(() => expect(treeItem("D:/")).toBeDefined());
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
    useUi.getState().updateTab("t1", { tabId: "k1" });
    mocks.list.mockResolvedValue([windowsEntry(dir)]);
    mounted = mountTree();
    await waitForRendered(() => expect(treeItem(dir)).toBeDefined());

    act(() => {
      treeItem(dir)!.dispatchEvent(
        new MouseEvent("contextmenu", { bubbles: true, cancelable: true }),
      );
    });
    await waitForRendered(() => expect(menuLabels().some((label) => label.includes("在当前终端打开"))).toBe(true));
    const item = [...mounted.container.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find(
      (candidate) => candidate.textContent?.includes("在当前终端打开"),
    );
    click(item!);
    await flush();

    expect(mocks.lineExec).toHaveBeenCalledWith("k1", command);
    expect(mocks.termWrite).not.toHaveBeenCalled();
  });
});

describe("FileTree roots errors", () => {
  it("keeps a manual recovery action visible when the roots query loses the session", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.roots.mockRejectedValue(NOT_FOUND);
    mocks.list.mockResolvedValue([]);
    mocks.connect.mockRejectedValue(new Error("dial tcp: connection refused"));
    mounted = mountTree();

    await waitForRendered(() => expect(mocks.connect).toHaveBeenCalledTimes(1));
    await waitForRendered(() => expect(mounted!.container.textContent).toContain("会话已在服务端删除"));
    expect(button("重新连接这台主机")).toBeDefined();
  });

  it("loads Windows roots after the initial roots request fails and manual retry succeeds", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.roots
      .mockRejectedValueOnce({ code: "sftp", message: "根信息暂时不可用" })
      .mockResolvedValue(WINDOWS_ROOTS);
    mocks.list.mockResolvedValue([]);
    mounted = mountTree();

    await waitForRendered(() => expect(mounted!.container.textContent).toContain("根信息暂时不可用"));
    clickButton(mounted.container, "重试");

    await waitForRendered(() => expect(treeItem("C:/")).toBeDefined());
    expect(treeItem("D:/")).toBeDefined();
    expect(mocks.roots).toHaveBeenCalledTimes(2);
    expect(mocks.connect).not.toHaveBeenCalled();
    expect(mounted.container.textContent).not.toContain("根信息暂时不可用");
  });
});

describe("FileTree new WinRM terminal navigation", () => {
  it("uses ExecLine after the new line tab attaches instead of pending terminal input", async () => {
    seedWorkspace({ assetId: "asset-1", assetKind: "winrm", title: "windows" });
    useUi.getState().updateTab("t1", { tabId: "k1" });
    mocks.list.mockResolvedValue([windowsEntry("D:/Project")]);
    mounted = mountTree();
    await waitForRendered(() => expect(treeItem("D:/Project")).toBeDefined());

    act(() => {
      treeItem("D:/Project")!.dispatchEvent(
        new MouseEvent("contextmenu", { bubbles: true, cancelable: true }),
      );
    });
    await waitForRendered(() => expect(menuLabels().some((label) => label.includes("在新终端打开"))).toBe(true));
    const item = [...mounted.container.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find(
      (candidate) => candidate.textContent?.includes("在新终端打开"),
    );
    click(item!);

    await waitForRendered(() =>
      expect(
        useUi.getState().workspaces[0]?.panes[0]?.tabs.filter((tab) => tab.kind === "terminal"),
      ).toHaveLength(2),
    );
    const newTab = useUi
      .getState()
      .workspaces[0]?.panes[0]?.tabs.find((tab) => tab.kind === "terminal" && tab.id !== "t1");
    expect(newTab?.pendingDirectory).toBeUndefined();
    act(() => {
      useUi.getState().updateTab(newTab!.id, { tabId: "k2" });
    });

    await waitForRendered(() =>
      expect(mocks.lineExec).toHaveBeenCalledWith("k2", "Set-Location -LiteralPath 'D:/Project'"),
    );
    expect(mocks.termWrite).not.toHaveBeenCalled();
  });
});

describe("FileTree shell-aware terminal navigation", () => {
  beforeEach(() => mocks.termWrite.mockResolvedValue(undefined));

  async function openCurrentAt(path: string): Promise<void> {
    act(() => {
      treeItem(path)!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
    });
    await waitForRendered(() => expect(menuLabels().some((label) => label.includes("在当前终端打开"))).toBe(true));
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
    useUi.getState().updateTab("t1", { tabId: "k1", shell });
    mocks.list.mockResolvedValue([windowsEntry(dir)]);
    mounted = mountTree();
    await waitForRendered(() => expect(treeItem(dir)).toBeDefined());
    await openCurrentAt(dir);
    expect(mocks.termWrite).toHaveBeenCalledTimes(1);
    expect(new TextDecoder().decode(mocks.termWrite.mock.calls[0][1])).toBe(command);
    expect(mocks.lineExec).not.toHaveBeenCalled();
  });

  it.each(["cmd", "unknown"] as const)("does not inject into a %s terminal", async (shell) => {
    const dir = shell === "cmd" ? "//server/share/Project [x]" : "D:/Project [x]";
    seedWorkspace({ assetId: "asset-1" });
    useUi.getState().updateTab("t1", { tabId: "k1", shell });
    mocks.list.mockResolvedValue([windowsEntry(dir)]);
    mounted = mountTree();
    await waitForRendered(() => expect(treeItem(dir)).toBeDefined());
    await openCurrentAt(dir);
    expect(mocks.termWrite).not.toHaveBeenCalled();
    expect(mocks.copy).toHaveBeenCalledWith(dir);
    await waitForRendered(() => expect(toastTexts()).toContain("路径已复制"));
  });

  it("surfaces a copy failure without injecting into the terminal", async () => {
    const dir = "D:/Project [x]";
    seedWorkspace({ assetId: "asset-1" });
    useUi.getState().updateTab("t1", { tabId: "k1", shell: "cmd" });
    mocks.list.mockResolvedValue([windowsEntry(dir)]);
    mocks.copy.mockRejectedValueOnce(new Error("clipboard denied"));
    mounted = mountTree();
    await waitForRendered(() => expect(treeItem(dir)).toBeDefined());
    await openCurrentAt(dir);

    expect(mocks.termWrite).not.toHaveBeenCalled();
    expect(mocks.copy).toHaveBeenCalledWith(dir);
    await waitForRendered(() => {
      expect(toastTexts()).toContain("复制路径失败");
      expect(toastTexts()).toContain("clipboard denied");
    });
  });

  it("stores a new terminal's directory as runtime pending state instead of a precomputed command", async () => {
    const dir = "D:/Project [x]/child's dir";
    seedWorkspace({ assetId: "asset-1" });
    mocks.list.mockResolvedValue([windowsEntry(dir)]);
    mounted = mountTree();
    await waitForRendered(() => expect(treeItem(dir)).toBeDefined());
    act(() => {
      treeItem(dir)!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
    });
    const item = [...mounted.container.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find(
      (candidate) => candidate.textContent?.includes("在新终端打开"),
    );
    click(item!);
    await waitForRendered(() =>
      expect(useUi.getState().workspaces[0]?.panes[0]?.tabs.filter((tab) => tab.kind === "terminal")).toHaveLength(2),
    );
    const newTab = useUi.getState().workspaces[0]?.panes[0]?.tabs.find((tab) => tab.kind === "terminal" && tab.id !== "t1");
    expect(newTab?.pendingDirectory).toBe(dir);
    expect(mocks.termWrite).not.toHaveBeenCalled();
  });
});
