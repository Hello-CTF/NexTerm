/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  promptText: vi.fn(),
  pickLocalFile: vi.fn(),
  pickSavePath: vi.fn(),
  finishSave: vi.fn(),
  discardStaged: vi.fn(),
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
  connect: vi.fn(),
  connectLocal: vi.fn(),
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
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    fsApi: {
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
    },
    terminalApi: { write: mocks.termWrite },
    assetApi: { ...(actual.assetApi ?? {}), knownHostAccept: mocks.knownHostAccept },
  };
});

vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    listenEvent: mocks.listenEvent,
    EVENTS: { ...actual.EVENTS, fsProgress: "fs://progress" },
  };
});

import { FileTree } from "../../features/files/FileTree";
import { useUi, type AppTab } from "../../app/store";
import type { FileEntryDto } from "../../ipc/types";

const SID = "s1";
const GONE_HINT = "会话已在服务端删除，请重新连接这台主机";

const NOT_FOUND = { code: "not_found", message: "会话不存在或已关闭，请刷新后重试" };

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
  name: "本机",
  kind: "local",
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

function seedWorkspace(options: { assetId?: string } = {}): void {
  const tab: AppTab = {
    id: "t1",
    kind: "terminal",
    title: "web-01",
    sessionId: SID,
    closable: true,
  };
  useUi.setState({
    workspaces: [
      {
        id: "ws1",
        kind: "session",
        title: "web-01",
        sessionId: SID,
        assetId: options.assetId,
        assetKind: options.assetId ? "ssh" : undefined,
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
        name: "web-01",
        kind: options.assetId ? "ssh" : "local",
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
  mocks.list.mockRejectedValue(NOT_FOUND);
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

describe("FileTree 会话已删除", () => {
  it("显示真实原因与「重新连接这台主机」，不提供无效的「重试」，工具栏给出真实禁用原因", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mounted = mountTree();

    await waitFor(() => expect(mounted!.container.textContent).toContain("会话已在服务端删除"));
    expect(mounted.container.textContent).not.toContain("请刷新后重试");
    expect(button("重新连接这台主机")).toBeDefined();
    expect(button("重试")).toBeUndefined();
    for (const ariaLabel of ["新建文件", "新建文件夹", "刷新文件列表", "上传到当前目录"]) {
      const btn = toolbarButton(ariaLabel);
      expect(btn?.disabled, ariaLabel).toBe(true);
      expect(btn?.title, ariaLabel).toBe(GONE_HINT);
    }
    expect(toolbarButton("折叠全部目录")?.disabled).toBe(false);
    expect(mocks.connect).not.toHaveBeenCalled();
  });

  it("重新连接走 fresh connect，工作区会话同步为新会话，树复用并加载，不重复工作区/会话", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.list.mockImplementation((sid: string) =>
      sid === SID ? Promise.reject(NOT_FOUND) : Promise.resolve(ENTRIES.map((e) => ({ ...e }))),
    );
    mounted = mountTree();
    await waitFor(() => expect(button("重新连接这台主机")).toBeDefined());

    clickButton(mounted.container, "重新连接这台主机");

    await waitFor(() => expect(mounted!.container.textContent).toContain("a.txt"));
    expect(mocks.connect).toHaveBeenCalledWith("asset-1");
    expect(mocks.connect).toHaveBeenCalledTimes(1);
    expect(mocks.list).toHaveBeenCalledWith("s2", "~");
    expect(workspaceSessionId()).toBe("s2");
    const st = useUi.getState();
    expect(st.workspaces).toHaveLength(1);
    expect(st.sessions.filter((s) => s.id === "s2")).toHaveLength(1);
    expect(toastTexts()).toContain("已重新连接");
    expect(toolbarButton("上传到当前目录")?.disabled).toBe(false);
  });

  it("取消主机密钥变更确认：不信任、不重试，工作区会话保持原样", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.connect.mockRejectedValue(CHANGED_HOST_KEY);
    mounted = mountTree();
    await waitFor(() => expect(button("重新连接这台主机")).toBeDefined());

    clickButton(mounted.container, "重新连接这台主机");

    await waitFor(() => expect(mocks.ask).toHaveBeenCalledTimes(1));
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("主机密钥已变更 127.0.0.1:22");
    await waitFor(() => expect(toastTexts()).toContain("已取消重连"));
    expect(mocks.knownHostAccept).not.toHaveBeenCalled();
    expect(mocks.connect).toHaveBeenCalledTimes(1);
    expect(workspaceSessionId()).toBe(SID);
    expect(mounted.container.textContent).toContain("会话已在服务端删除");
  });

  it("fresh connect 失败：报告真实错误，工作区会话保持原样", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.connect.mockRejectedValue(new Error("dial tcp 127.0.0.1:22: connect: connection refused"));
    mounted = mountTree();
    await waitFor(() => expect(button("重新连接这台主机")).toBeDefined());

    clickButton(mounted.container, "重新连接这台主机");

    await waitFor(() => expect(toastTexts()).toContain("重新连接失败"));
    expect(toastTexts()).toContain("connection refused");
    expect(workspaceSessionId()).toBe(SID);
    expect(mounted.container.textContent).toContain("会话已在服务端删除");
  });

  it("本地会话（无资产）回退 connectLocal，同样同步工作区会话", async () => {
    seedWorkspace();
    mocks.list.mockImplementation((sid: string) =>
      sid === SID ? Promise.reject(NOT_FOUND) : Promise.resolve(ENTRIES.map((e) => ({ ...e }))),
    );
    mounted = mountTree();
    await waitFor(() => expect(button("重新连接这台主机")).toBeDefined());

    clickButton(mounted.container, "重新连接这台主机");

    await waitFor(() => expect(mounted!.container.textContent).toContain("a.txt"));
    expect(mocks.connectLocal).toHaveBeenCalledTimes(1);
    expect(mocks.connect).not.toHaveBeenCalled();
    expect(workspaceSessionId()).toBe("s-local");
  });
});

describe("FileTree 瞬时文件错误", () => {
  it("保留真实错误与「重试」，工具栏不禁用，重试成功恢复，不引导重新连接", async () => {
    seedWorkspace({ assetId: "asset-1" });
    mocks.list.mockRejectedValueOnce({ code: "sftp", message: "SFTP：连接被对端重置" });
    mocks.list.mockResolvedValue(ENTRIES.map((e) => ({ ...e })));
    mounted = mountTree();

    await waitFor(() => expect(mounted!.container.textContent).toContain("连接被对端重置"));
    expect(button("重试")).toBeDefined();
    expect(button("重新连接这台主机")).toBeUndefined();
    for (const ariaLabel of ["新建文件", "新建文件夹", "刷新文件列表", "上传到当前目录"]) {
      expect(toolbarButton(ariaLabel)?.disabled, ariaLabel).toBe(false);
    }

    clickButton(mounted.container, "重试");

    await waitFor(() => expect(mounted!.container.textContent).toContain("a.txt"));
    expect(mocks.connect).not.toHaveBeenCalled();
    expect(mocks.connectLocal).not.toHaveBeenCalled();
    await flush();
    expect(button("重试")).toBeUndefined();
  });
});
