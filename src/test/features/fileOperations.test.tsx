/** @vitest-environment jsdom */
//
// M60：rename / chmod / checksum 三个文件操作的界面测试。
//
// 覆盖边界（对应任务书的每一条）：
//   · 输入预检：跨路径名、空名、非八进制 / 超界 mode、未知算法 —— 一律不发 RPC；
//   · 符号链接 / 覆盖 / 权限收窄 / 算法边界：提醒与确认文案到位，取消即中止；
//   · 后端权威：后端拒绝原样呈现，前端不冒充授权；
//   · 状态纪律：double-submit 被 busy 挡住，卸载后的完成回调不碰任何状态，
//     传输进度条与无关 dirty 编辑器不受操作影响。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  deferred,
  flush,
  mount,
  waitFor,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  askChoice: vi.fn(),
  promptText: vi.fn(),
  pickLocalFile: vi.fn(),
  pickSavePath: vi.fn(),
  finishSave: vi.fn(),
  discardStaged: vi.fn(),
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
  toast: vi.fn(),
  listenEvent: vi.fn(),
}));
vi.mock("../../ui/dialogs", () => ({
  ask: mocks.ask,
  askChoice: mocks.askChoice,
  promptText: mocks.promptText,
  pickLocalFile: mocks.pickLocalFile,
  pickSavePath: mocks.pickSavePath,
  finishSave: mocks.finishSave,
  discardStaged: mocks.discardStaged,
}));
vi.mock("../../ipc/commands", () => ({
  fsApi: {
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
  terminalApi: { write: mocks.termWrite },
  sessionApi: {},
}));
vi.mock("../../ipc/events", () => ({
  listenEvent: mocks.listenEvent,
  EVENTS: { fsProgress: "fs://progress" },
}));
// jsdom 没有 ResizeObserver，虚拟滚动永远量不到尺寸、一行都不渲染。
// 这里把整个列表按行高铺平 —— 测的是操作逻辑，不是虚拟滚动本身。
vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getVirtualItems: () =>
      Array.from({ length: count }, (_, i) => ({ index: i, start: i * 30, size: 30, key: i })),
    getTotalSize: () => count * 30,
  }),
}));

import type { AppTab } from "../../app/store";
import { useUi } from "../../app/store";
import type { FileEntryDto } from "../../ipc/types";
import { isDirtyFileEditor, setFileEditorDirty } from "../../features/files/editorGuards";
import {
  CHECKSUM_ALGOS,
  DEFAULT_CHECKSUM_ALGO,
  losesOwnerAccess,
  parseOctalMode,
  validateEntryName,
} from "../../features/files/fileOps";
import { FileBrowser } from "../../features/files/FileBrowser";
import { FileTree } from "../../features/files/FileTree";

const SID = "s1";

function entry(name: string, kind: string, extra: Partial<FileEntryDto> = {}): FileEntryDto {
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
    ...extra,
  };
}

const entries = [
  entry("a.txt", "file"),
  entry("b.txt", "file"),
  entry("sub", "dir"),
  entry("link", "symlink", { symlinkTarget: "~/a.txt", mode: "" }),
];

function editorTab(path: string): AppTab {
  return { id: `file-${SID}-${path}`, kind: "files", title: "a.txt", sessionId: SID, path, closable: true };
}

function mountBrowser(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(FileBrowser, { sessionId: SID })));
}

function mountTree(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(FileTree, { sessionId: SID })));
}

function rowByPath(container: HTMLElement, path: string): HTMLElement {
  const row = [...container.querySelectorAll<HTMLElement>("div[title]")].find((d) =>
    (d.getAttribute("title") ?? "").startsWith(path),
  );
  if (!row) throw new Error(`Row not found: ${path}`);
  return row;
}

function openRowMenu(container: HTMLElement, path: string): void {
  act(() => {
    rowByPath(container, path).dispatchEvent(
      new MouseEvent("contextmenu", { bubbles: true, cancelable: true }),
    );
  });
}

function menuItem(container: ParentNode, label: string): HTMLButtonElement {
  const button = [...container.querySelectorAll('[role="menuitem"]')].find((b) =>
    b.textContent?.includes(label),
  );
  if (!button) throw new Error(`Menu item not found: ${label}`);
  return button as HTMLButtonElement;
}

function clickMenuItem(container: ParentNode, label: string): void {
  click(menuItem(container, label));
}

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.ask.mockResolvedValue(true);
  mocks.promptText.mockResolvedValue(null);
  mocks.askChoice.mockResolvedValue(null);
  mocks.list.mockImplementation((_s: string, p: string) =>
    Promise.resolve(p === "~" ? entries.map((e) => ({ ...e })) : []),
  );
  mocks.rename.mockResolvedValue(undefined);
  mocks.chmod.mockResolvedValue(undefined);
  mocks.checksum.mockResolvedValue("deadbeef");
  mocks.listenEvent.mockReturnValue(Promise.resolve(() => {}));
  useUi.setState({
    pushToast: mocks.toast,
    leftOpen: true,
    leftWidth: 260,
    workspaces: [],
    activeWorkspaceId: null,
    sessions: [],
  });
  setFileEditorDirty(SID, "~/a.txt", false);
  setFileEditorDirty(SID, "~/other.txt", false);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("fileOps 预检（纯函数）", () => {
  it("拒绝跨路径名与点目录名", () => {
    expect(validateEntryName("a/b")).toMatch(/路径分隔符/);
    expect(validateEntryName("a\\b")).toMatch(/路径分隔符/);
    expect(validateEntryName("..")).toBeTruthy();
    expect(validateEntryName(".")).toBeTruthy();
    expect(validateEntryName("  ")).toMatch(/不能为空/);
    expect(validateEntryName(" x ")).toMatch(/首尾/);
    expect(validateEntryName("ok.txt")).toBeNull();
  });

  it("mode 只收八进制数字且不超 0o777", () => {
    expect(parseOctalMode("644")).toEqual({ ok: true, mode: 0o644 });
    expect(parseOctalMode("0644")).toEqual({ ok: true, mode: 0o644 });
    expect(parseOctalMode("888").ok).toBe(false);
    expect(parseOctalMode("rwx").ok).toBe(false);
    expect(parseOctalMode("7777").ok).toBe(false);
    expect(parseOctalMode("").ok).toBe(false);
  });

  it("只有移除所有者读/写才算权限收窄", () => {
    expect(losesOwnerAccess(0o644, 0o000)).toBe(true);
    expect(losesOwnerAccess(0o644, 0o444)).toBe(true);
    expect(losesOwnerAccess(0o644, 0o755)).toBe(false);
    expect(losesOwnerAccess(0o000, 0o644)).toBe(false);
  });

  it("校验算法与后端白名单对齐（md5/sha256，缺省 sha256）", () => {
    expect(CHECKSUM_ALGOS.map((a) => a.key)).toEqual(["sha256", "md5"]);
    expect(DEFAULT_CHECKSUM_ALGO).toBe("sha256");
  });
});

describe("重命名（FileBrowser）", () => {
  async function triggerRename(name: string | null): Promise<void> {
    mocks.promptText.mockResolvedValue(name);
    openRowMenu(mounted!.container, "~/a.txt");
    clickMenuItem(mounted!.container, "重命名");
    await flush();
  }

  beforeEach(async () => {
    mounted = mountBrowser();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
  });

  it("拒绝跨路径输入，不发 RPC", async () => {
    await triggerRename("../evil.txt");
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("路径分隔符"));
    expect(mocks.rename).not.toHaveBeenCalled();
    await triggerRename("sub/inner.txt");
    expect(mocks.rename).not.toHaveBeenCalled();
  });

  it("拒绝空名与首尾空白名", async () => {
    await triggerRename("   ");
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("不能为空"));
    await triggerRename(" b.txt ");
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("首尾"));
    expect(mocks.rename).not.toHaveBeenCalled();
  });

  it("同目录改名成功并刷新列表", async () => {
    await triggerRename("c.txt");
    expect(mocks.rename).toHaveBeenCalledWith(SID, "~/a.txt", "~/c.txt");
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("已重命名"));
    // 父目录缓存被失效 → 重新拉一次
    await waitFor(() => expect(mocks.list.mock.calls.length).toBeGreaterThan(1));
  });

  it("覆盖已有名字前必须显式确认，取消即中止", async () => {
    mocks.ask.mockResolvedValueOnce(false);
    await triggerRename("b.txt");
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("已存在"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.rename).not.toHaveBeenCalled();

    mocks.ask.mockResolvedValueOnce(true);
    await triggerRename("b.txt");
    expect(mocks.rename).toHaveBeenCalledWith(SID, "~/a.txt", "~/b.txt");
  });

  it("符号链接：先讲清「改的是链接本身」再改", async () => {
    mocks.promptText.mockResolvedValue("link2");
    openRowMenu(mounted!.container, "~/link");
    clickMenuItem(mounted!.container, "重命名");
    await flush();
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("符号链接"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.rename).toHaveBeenCalledWith(SID, "~/link", "~/link2");
  });

  it("脏编辑器持有该文件时告警，且操作不替用户清 dirty", async () => {
    const tab = editorTab("~/a.txt");
    act(() => {
      useUi.setState({
        workspaces: [
          {
            id: "ws",
            kind: "session",
            title: "w",
            panes: [{ id: "p", tabs: [tab], activeTabId: tab.id }],
            activePaneId: "p",
            splitRatio: 0.5,
            closable: true,
          },
        ],
        activeWorkspaceId: "ws",
      });
    });
    setFileEditorDirty(SID, "~/a.txt", true);
    await triggerRename("c.txt");
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("未保存"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.rename).toHaveBeenCalledWith(SID, "~/a.txt", "~/c.txt");
    expect(isDirtyFileEditor(tab)).toBe(true);
    setFileEditorDirty(SID, "~/a.txt", false);
  });

  it("后端拒绝原样呈现，且可以原样重试", async () => {
    mocks.rename.mockRejectedValueOnce({ code: "internal", message: "SFTP：目标已存在" });
    await triggerRename("c.txt");
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("SFTP：目标已存在"));
    await triggerRename("c.txt");
    expect(mocks.rename).toHaveBeenCalledTimes(2);
  });

  it("进行中的操作挡住第二次触发（double-submit）", async () => {
    const pending = deferred<void>();
    mocks.rename.mockReturnValueOnce(pending.promise);
    await triggerRename("c.txt");
    expect(mocks.rename).toHaveBeenCalledTimes(1);

    openRowMenu(mounted!.container, "~/b.txt");
    const renameItem = menuItem(mounted!.container, "重命名");
    expect(renameItem.disabled).toBe(true);
    click(renameItem);
    clickMenuItem(mounted!.container, "权限…");
    expect(mocks.chmod).not.toHaveBeenCalled();
    expect(mocks.promptText).not.toHaveBeenCalledWith(
      expect.stringContaining("权限"),
      expect.anything(),
    );

    pending.resolve();
    await flush();
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("已重命名"));
  });

  it("卸载后到达的完成回调不碰任何状态（stale completion）", async () => {
    const pending = deferred<void>();
    mocks.rename.mockReturnValueOnce(pending.promise);
    await triggerRename("c.txt");
    mounted!.unmount();
    mounted = undefined;
    pending.resolve();
    await flush();
    expect(mocks.toast).not.toHaveBeenCalledWith("success", expect.stringContaining("已重命名"));
    expect(mocks.toast).not.toHaveBeenCalledWith("error", expect.anything());
  });
});

describe("权限 chmod（FileBrowser）", () => {
  async function triggerChmod(path: string, mode: string | null): Promise<void> {
    mocks.promptText.mockResolvedValue(mode);
    openRowMenu(mounted!.container, path);
    clickMenuItem(mounted!.container, "权限…");
    await flush();
  }

  beforeEach(async () => {
    mounted = mountBrowser();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
  });

  it("非八进制与超界输入在发 RPC 前被挡下", async () => {
    await triggerChmod("~/a.txt", "888");
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("八进制"));
    await triggerChmod("~/a.txt", "7777");
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("范围"));
    await triggerChmod("~/a.txt", "rwx");
    expect(mocks.chmod).not.toHaveBeenCalled();
  });

  it("接受前导零八进制并发送数值 mode", async () => {
    await triggerChmod("~/a.txt", "0755");
    expect(mocks.chmod).toHaveBeenCalledWith(SID, "~/a.txt", 0o755);
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("权限已更新"));
  });

  it("移除所有者读/写需要危险确认，取消即中止", async () => {
    mocks.ask.mockResolvedValueOnce(false);
    await triggerChmod("~/a.txt", "000");
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("所有者"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.chmod).not.toHaveBeenCalled();

    mocks.ask.mockResolvedValueOnce(true);
    await triggerChmod("~/a.txt", "000");
    expect(mocks.chmod).toHaveBeenCalledWith(SID, "~/a.txt", 0);
  });

  it("符号链接：提醒 chmod 改的是目标", async () => {
    await triggerChmod("~/link", "600");
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("符号链接"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.chmod).toHaveBeenCalledWith(SID, "~/link", 0o600);
  });
});

describe("校验值 checksum（FileBrowser）", () => {
  beforeEach(async () => {
    mounted = mountBrowser();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
  });

  it("算法多选只有 md5/sha256，结果以供复制的输入框呈现", async () => {
    mocks.askChoice.mockResolvedValue("md5");
    mocks.checksum.mockResolvedValue("md5-hash-value");
    openRowMenu(mounted!.container, "~/a.txt");
    clickMenuItem(mounted!.container, "校验值…");
    await flush();

    const choices = mocks.askChoice.mock.calls[0]?.[1]?.choices ?? [];
    expect(choices.map((c: { key: string }) => c.key)).toEqual(["sha256", "md5"]);
    expect(choices.find((c: { key: string }) => c.key === "sha256")?.primary).toBe(true);
    expect(mocks.checksum).toHaveBeenCalledWith(SID, "~/a.txt", "md5");
    expect(mocks.promptText).toHaveBeenCalledWith(
      expect.stringContaining("MD5"),
      "md5-hash-value",
      expect.objectContaining({ multiLine: false }),
    );
  });

  it("算法选择取消则完全不碰后端", async () => {
    mocks.askChoice.mockResolvedValue(null);
    openRowMenu(mounted!.container, "~/a.txt");
    clickMenuItem(mounted!.container, "校验值…");
    await flush();
    expect(mocks.checksum).not.toHaveBeenCalled();
    expect(mocks.promptText).not.toHaveBeenCalled();
  });

  it("目录不提供校验值入口", async () => {
    openRowMenu(mounted!.container, "~/sub");
    expect(() => menuItem(mounted!.container, "校验值…")).toThrow();
  });
});

describe("FileTree 入口与状态搬迁", () => {
  beforeEach(async () => {
    mounted = mountTree();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());
  });

  it("目录行有重命名/权限，没有校验值", () => {
    openRowMenu(mounted!.container, "~/sub");
    expect(menuItem(mounted!.container, "重命名")).toBeTruthy();
    expect(menuItem(mounted!.container, "权限…")).toBeTruthy();
    expect(() => menuItem(mounted!.container, "校验值…")).toThrow();
  });

  it("重命名目录后，展开与选中状态搬到新路径", async () => {
    // 展开 sub（单击目录行），再重命名它
    click(rowByPath(mounted!.container, "~/sub"));
    await waitFor(() =>
      expect(mocks.list).toHaveBeenCalledWith(SID, "~/sub"),
    );
    mocks.promptText.mockResolvedValue("sub2");
    openRowMenu(mounted!.container, "~/sub");
    clickMenuItem(mounted!.container, "重命名");
    await flush();
    expect(mocks.rename).toHaveBeenCalledWith(SID, "~/sub", "~/sub2");
    // 展开态已 remap → 对新路径发列表请求
    await waitFor(() => expect(mocks.list).toHaveBeenCalledWith(SID, "~/sub2"));
  });
});

describe("操作不破坏既有状态", () => {
  it("传输进度条与无关 dirty 编辑器在操作后原样保留", async () => {
    let progressHandler: ((e: unknown) => void) | undefined;
    mocks.listenEvent.mockImplementation((_t: string, cb: (e: unknown) => void) => {
      progressHandler = cb;
      return Promise.resolve(() => {});
    });
    mounted = mountBrowser();
    await waitFor(() => expect(rowByPath(mounted!.container, "~/a.txt")).toBeTruthy());

    act(() => {
      progressHandler?.({ taskId: "t1", transferred: 5, total: 100, done: false });
    });
    expect(mounted.container.querySelector('[data-task-id="t1"]')).not.toBeNull();

    setFileEditorDirty(SID, "~/other.txt", true);
    mocks.promptText.mockResolvedValue("600");
    openRowMenu(mounted!.container, "~/a.txt");
    clickMenuItem(mounted!.container, "权限…");
    await waitFor(() =>
      expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("权限已更新")),
    );

    expect(mounted.container.querySelector('[data-task-id="t1"]')).not.toBeNull();
    expect(
      isDirtyFileEditor({
        id: "x",
        kind: "files",
        title: "other.txt",
        sessionId: SID,
        path: "~/other.txt",
        closable: true,
      }),
    ).toBe(true);
    setFileEditorDirty(SID, "~/other.txt", false);
  });
});
