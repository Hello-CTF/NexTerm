/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  mount,
  setInputValue,
  waitFor,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  fsList: vi.fn(),
  mountList: vi.fn(),
  mountCreate: vi.fn(),
  groupUpdate: vi.fn(),
  assetList: vi.fn(),
  assetSearch: vi.fn(),
  groupList: vi.fn(),
  snippetList: vi.fn(),
  vaultStatus: vi.fn(),
  promptText: vi.fn(),
  listCredentials: vi.fn(),
  toast: vi.fn(),
  listenEvent: vi.fn(),
}));

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return {
    ...actual,
    promptText: mocks.promptText,
    pickLocalFile: vi.fn(),
    pickSavePath: vi.fn(),
    finishSave: vi.fn(),
    discardStaged: vi.fn(),
  };
});
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    fsApi: {
      list: mocks.fsList,
      read: vi.fn(),
      write: vi.fn(),
      rename: vi.fn(),
      chmod: vi.fn(),
      checksum: vi.fn(),
      mkdir: vi.fn(),
      delete: vi.fn(),
      upload: vi.fn(),
      download: vi.fn(),
      packDownload: vi.fn(),
      extract: vi.fn(),
    },
    mountApi: {
      list: mocks.mountList,
      create: mocks.mountCreate,
      remove: vi.fn(),
    },
    terminalApi: { write: vi.fn() },
    sessionApi: { probe: vi.fn(), list: vi.fn().mockResolvedValue([]) },
    assetApi: {
      list: mocks.assetList,
      search: mocks.assetSearch,
      groupList: mocks.groupList,
      groupUpdate: mocks.groupUpdate,
      groupDelete: vi.fn(),
      groupCreate: vi.fn(),
      snippetList: mocks.snippetList,
      readKeyFile: vi.fn(),
    },
    vaultApi: {
      status: mocks.vaultStatus,
      listCredentials: mocks.listCredentials,
      setCredential: vi.fn(),
    },
  };
});
vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return { ...actual, listenEvent: mocks.listenEvent };
});
vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getVirtualItems: () =>
      Array.from({ length: count }, (_, i) => ({ index: i, start: i * 30, size: 30, key: i })),
    getTotalSize: () => count * 30,
  }),
}));

import { FileBrowser } from "../../features/files/FileBrowser";
import { FileTree } from "../../features/files/FileTree";
import { MountPanel } from "../../features/files/MountPanel";
import { AssetTree } from "../../features/explorer/AssetTree";
import { CredentialsSidebar } from "../../features/credentials/CredentialsSidebar";
import { CredentialsPanel } from "../../features/credentials/CredentialsPanel";
import { CredentialsView } from "../../features/credentials/CredentialsView";
import { NewCredentialModal } from "../../features/credentials/NewCredentialModal";
import { useUi } from "../../app/store";
import type { FileEntryDto } from "../../ipc/types";

const HOME_ENTRIES: FileEntryDto[] = [
  {
    name: "logs",
    path: "~/logs",
    kind: "dir",
    size: 0,
    mode: "drwxr-xr-x",
    owner: "root",
    group: "root",
    mtime: 1,
    symlinkTarget: null,
  },
  {
    name: "notes.txt",
    path: "~/notes.txt",
    kind: "file",
    size: 12,
    mode: "-rw-r--r--",
    owner: "root",
    group: "root",
    mtime: 1,
    symlinkTarget: null,
  },
];

const GROUP = {
  id: "g1",
  parentId: null,
  name: "生产",
  sort: 0,
  createdAt: 1,
  updatedAt: 1,
};

const CREDENTIALS = [
  {
    id: "c1",
    name: "db-prod",
    kind: "password",
    createdAt: 1,
    updatedAt: 1,
    usedBy: [],
    source: null,
    refPath: null,
    hasPassphrase: false,
  },
  {
    id: "c2",
    name: "deploy-key",
    kind: "private_key",
    createdAt: 1,
    updatedAt: 1,
    usedBy: [{ id: "a1", name: "web-1", kind: "ssh" }],
    source: "inline",
    refPath: null,
    hasPassphrase: false,
  },
];

function mountWithClient(node: ReactNode): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function keyDown(target: EventTarget, key: string, init: KeyboardEventInit = {}): KeyboardEvent {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

function menuLabels(container: ParentNode): string[] {
  return [...container.querySelectorAll('[role="menuitem"]')].map(
    (b) => b.textContent?.trim() ?? "",
  );
}

function setViewportWidth(width: number): void {
  Object.defineProperty(window, "innerWidth", { value: width, configurable: true });
}

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.resetAllMocks();
  document.body.replaceChildren();
  mocks.fsList.mockImplementation((_sessionId: string, dir: string) =>
    Promise.resolve(dir === "~" ? HOME_ENTRIES : []),
  );
  mocks.mountList.mockResolvedValue([
    { id: "m1", localPoint: "Z:", remote: "\\\\nas\\share", sessionId: "s1", createdAt: 1 },
  ]);
  mocks.mountCreate.mockResolvedValue(undefined);
  mocks.groupUpdate.mockResolvedValue(undefined);
  mocks.assetList.mockResolvedValue([]);
  mocks.assetSearch.mockResolvedValue([]);
  mocks.groupList.mockResolvedValue([{ ...GROUP }]);
  mocks.snippetList.mockResolvedValue([]);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, mode: "master", unlocked: true });
  mocks.promptText.mockResolvedValue(null);
  mocks.listCredentials.mockResolvedValue(CREDENTIALS.map((c) => ({ ...c })));
  mocks.listenEvent.mockReturnValue(Promise.resolve(() => undefined));
  useUi.setState({
    leftOpen: true,
    leftMode: "credentials",
    pushToast: mocks.toast,
    sessions: [],
    workspaces: [
      {
        id: "ws1",
        kind: "session",
        title: "ws",
        closable: true,
        sessionId: "s1",
        panes: [{ id: "p1", activeTabId: null, tabs: [] }],
        activePaneId: "p1",
        splitRatio: 0.5,
      },
    ],
    activeWorkspaceId: "ws1",
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  setViewportWidth(1024);
});

describe("触屏 More 按钮复用同一业务菜单", () => {
  it("FileTree 行内 ⋯ 打开与右键相同的菜单", async () => {
    mounted = mountWithClient(createElement(FileTree, { sessionId: "s1" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("notes.txt"));

    const more = mounted!.container.querySelector<HTMLButtonElement>(
      'button[aria-label="更多操作 notes.txt"]',
    );
    if (!more) throw new Error("row ⋯ button not found");
    click(more);

    await waitFor(() =>
      expect(mounted!.container.querySelector('[role="menu"]')).not.toBeNull(),
    );
    const labels = menuLabels(mounted!.container);
    expect(labels.some((l) => l.includes("重命名"))).toBe(true);
    expect(labels.some((l) => l.includes("权限…"))).toBe(true);
    expect(labels.some((l) => l.includes("校验值…"))).toBe(true);
    expect(labels.some((l) => l.includes("删除"))).toBe(true);

    const rename = [...mounted!.container.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find(
      (b) => b.textContent?.trim() === "重命名",
    );
    if (!rename) throw new Error("rename item not found");
    click(rename);
    await waitFor(() => expect(mocks.promptText).toHaveBeenCalled());
  });

  it("FileBrowser 行内 ⋯ 与工具栏 ⋯ 各自打开对应菜单", async () => {
    mounted = mountWithClient(createElement(FileBrowser, { sessionId: "s1" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("notes.txt"));

    const rowMore = mounted!.container.querySelector<HTMLButtonElement>(
      'button[aria-label="更多操作 notes.txt"]',
    );
    if (!rowMore) throw new Error("row ⋯ button not found");
    click(rowMore);
    await waitFor(() =>
      expect(mounted!.container.querySelector('[role="menu"]')).not.toBeNull(),
    );
    const rowLabels = menuLabels(mounted!.container);
    expect(rowLabels.some((l) => l.includes("在下方编辑"))).toBe(true);
    expect(rowLabels.some((l) => l.includes("重命名"))).toBe(true);
    expect(rowLabels.some((l) => l.includes("校验值…"))).toBe(true);

    keyDown(document.body, "Escape");
    await waitFor(() =>
      expect(mounted!.container.querySelector('[role="menu"]')).toBeNull(),
    );

    const toolbarMore = mounted!.container.querySelector<HTMLButtonElement>(
      'button[aria-label="更多操作"]',
    );
    if (!toolbarMore) throw new Error("toolbar ⋯ button not found");
    click(toolbarMore);
    await waitFor(() =>
      expect(mounted!.container.querySelector('[role="menu"]')).not.toBeNull(),
    );
    const blankLabels = menuLabels(mounted!.container);
    expect(blankLabels.some((l) => l.includes("上传到当前目录"))).toBe(true);
    expect(blankLabels.some((l) => l.includes("新建文件夹"))).toBe(true);
    expect(blankLabels.some((l) => l.includes("刷新"))).toBe(true);
  });

  it("FileBrowser 大小/修改时间列与低频工具栏按钮带窄屏隐藏类", async () => {
    mounted = mountWithClient(createElement(FileBrowser, { sessionId: "s1" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("notes.txt"));

    const headerCells = [...mounted!.container.querySelectorAll("span")].filter((s) =>
      ["大小", "修改时间"].includes(s.textContent?.trim() ?? ""),
    );
    expect(headerCells.length).toBe(2);
    for (const cell of headerCells) {
      expect(cell.className).toContain("max-[560px]:hidden");
    }
    for (const label of ["上传", "下载", "新建", "删除"]) {
      const button = [...mounted!.container.querySelectorAll<HTMLButtonElement>(".nx-toolbar button")].find(
        (b) => b.textContent?.trim() === label,
      );
      expect(button?.className ?? "").toContain("max-[560px]:hidden");
    }
  });
});

describe("单行 Enter 提交表单的 IME 守卫", () => {
  it("FileBrowser 路径框：IME 组合中 Enter 不跳转，普通 Enter 跳转", async () => {
    mounted = mountWithClient(createElement(FileBrowser, { sessionId: "s1" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("notes.txt"));
    const input = mounted!.container.querySelector<HTMLInputElement>(".nx-toolbar input");
    if (!input) throw new Error("path input not found");

    setInputValue(input, "/etc");
    const callsBefore = mocks.fsList.mock.calls.length;
    keyDown(input, "Enter", { isComposing: true });
    await new Promise((resolve) => setTimeout(resolve, 30));
    expect(mocks.fsList.mock.calls.length).toBe(callsBefore);

    keyDown(input, "Enter");
    await waitFor(() =>
      expect(mocks.fsList).toHaveBeenCalledWith("s1", "/etc"),
    );
  });

  it("FileTree 路径编辑：IME 组合中 Enter 不提交，普通 Enter 提交", async () => {
    mounted = mountWithClient(createElement(FileTree, { sessionId: "s1" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("notes.txt"));

    const editToggle = mounted!.container.querySelector<HTMLButtonElement>(
      'button[aria-label="输入路径跳转"]',
    );
    if (!editToggle) throw new Error("path edit toggle not found");
    click(editToggle);
    await waitFor(() =>
      expect(mounted!.container.querySelector(".nx-pathbar input")).not.toBeNull(),
    );
    const input = mounted!.container.querySelector<HTMLInputElement>(".nx-pathbar input");
    if (!input) throw new Error("path edit input not found");

    setInputValue(input, "/var");
    const callsBefore = mocks.fsList.mock.calls.length;
    keyDown(input, "Enter", { isComposing: true });
    await new Promise((resolve) => setTimeout(resolve, 30));
    expect(mocks.fsList.mock.calls.length).toBe(callsBefore);

    keyDown(input, "Enter");
    await waitFor(() => expect(mocks.fsList).toHaveBeenCalledWith("s1", "/var"));
  });

  it("MountPanel 远端路径：IME 组合中 Enter 不创建，普通 Enter 创建", async () => {
    mounted = mountWithClient(createElement(MountPanel, { sessionId: "s1" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("断开"));
    const input = mounted!.container.querySelector<HTMLInputElement>(
      'input[aria-label="远端路径"]',
    );
    if (!input) throw new Error("remote path input not found");

    setInputValue(input, "user@host:/data");
    keyDown(input, "Enter", { isComposing: true });
    await new Promise((resolve) => setTimeout(resolve, 30));
    expect(mocks.mountCreate).not.toHaveBeenCalled();

    keyDown(input, "Enter");
    await waitFor(() => expect(mocks.mountCreate).toHaveBeenCalledTimes(1));
  });

  it("分组重命名：IME 组合中 Enter 不保存，普通 Enter 保存", async () => {
    mocks.groupUpdate.mockClear();
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("生产"));

    const rename = [...mounted!.container.querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.getAttribute("aria-label") === "重命名分组「生产」",
    );
    if (!rename) throw new Error("group rename button not found");
    click(rename);
    await waitFor(() =>
      expect(mounted!.container.querySelector(".nx-modal input")).not.toBeNull(),
    );
    const input = mounted!.container.querySelector<HTMLInputElement>(".nx-modal input");
    if (!input) throw new Error("rename input not found");

    setInputValue(input, "生产环境");
    keyDown(input, "Enter", { isComposing: true });
    await new Promise((resolve) => setTimeout(resolve, 30));
    expect(mocks.groupUpdate).not.toHaveBeenCalled();

    keyDown(input, "Enter");
    await waitFor(() => expect(mocks.groupUpdate).toHaveBeenCalledWith("g1", "生产环境"));
  });
});

describe("MountPanel 窄屏结构", () => {
  it("提示行可换行、挂载时间列带窄屏隐藏类", async () => {
    mounted = mountWithClient(createElement(MountPanel, { sessionId: "s1" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("断开"));

    const hintRow = mounted!.container.querySelector(".nx-code")?.closest("div");
    expect(hintRow?.className ?? "").toContain("flex-wrap");

    const timeHeader = [...mounted!.container.querySelectorAll("th")].find(
      (th) => th.textContent?.trim() === "挂载时间",
    );
    expect(timeHeader?.className ?? "").toContain("max-[560px]:hidden");
    const timeCell = [...mounted!.container.querySelectorAll("td")].find((td) =>
      td.textContent?.includes("1970"),
    );
    expect(timeCell?.className ?? "").toContain("max-[560px]:hidden");
  });
});

describe("凭据 overlay dock 选中后收起", () => {
  it("≤820px 视口点凭据行后左 dock 收起并打开详情", async () => {
    setViewportWidth(320);
    mounted = mountWithClient(createElement(CredentialsSidebar));
    await waitFor(() => expect(mounted!.container.textContent).toContain("db-prod"));

    const row = [...mounted!.container.querySelectorAll<HTMLButtonElement>(".nx-row")].find(
      (b) => b.textContent?.includes("db-prod"),
    );
    if (!row) throw new Error("credential row not found");
    click(row);

    await waitFor(() => expect(useUi.getState().leftOpen).toBe(false));
    const tabs = useUi.getState().workspaces[0]?.panes[0]?.tabs ?? [];
    expect(tabs.some((t) => t.kind === "credentials" && t.credId === "c1")).toBe(true);
  });

  it("宽视口点凭据行不收起左 dock", async () => {
    setViewportWidth(1280);
    mounted = mountWithClient(createElement(CredentialsSidebar));
    await waitFor(() => expect(mounted!.container.textContent).toContain("db-prod"));

    const row = [...mounted!.container.querySelectorAll<HTMLButtonElement>(".nx-row")].find(
      (b) => b.textContent?.includes("db-prod"),
    );
    if (!row) throw new Error("credential row not found");
    click(row);

    await waitFor(() => {
      const tabs = useUi.getState().workspaces[0]?.panes[0]?.tabs ?? [];
      expect(tabs.some((t) => t.kind === "credentials")).toBe(true);
    });
    expect(useUi.getState().leftOpen).toBe(true);
  });
});

describe("凭据头部与新建弹窗窄屏结构", () => {
  it("CredentialsPanel 头部徽标窄屏隐藏、主按钮不裁", async () => {
    mounted = mountWithClient(createElement(CredentialsPanel, { credId: "c1" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("已解锁"));

    const badges = [...mounted!.container.querySelectorAll(".nx-badge")].filter((b) =>
      ["已解锁", "已锁定", "未启用密码保护"].includes(b.textContent?.trim() ?? ""),
    );
    expect(badges.length).toBeGreaterThan(0);
    for (const badge of badges) {
      expect(badge.className).toContain("hidden");
      expect(badge.className).toContain("sm:inline-flex");
    }
    const title = [...mounted!.container.querySelectorAll("span")].find(
      (s) => s.textContent?.trim() === "凭据",
    );
    expect(title?.className ?? "").toContain("whitespace-nowrap");
    const create = [...mounted!.container.querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.textContent?.trim() === "新建凭据",
    );
    expect(create?.className ?? "").toContain("shrink-0");
  });

  it("CredentialsView 头部标题不换行、只读徽标窄屏隐藏、容器可换行", async () => {
    mounted = mountWithClient(createElement(CredentialsView, { view: "text", onChange: () => undefined }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("凭据视图"));

    const title = [...mounted!.container.querySelectorAll("span")].find(
      (s) => s.textContent?.trim() === "凭据视图",
    );
    expect(title?.className ?? "").toContain("whitespace-nowrap");
    expect(title?.className ?? "").toContain("shrink-0");
    const badge = [...mounted!.container.querySelectorAll(".nx-badge")].find(
      (b) => b.textContent?.trim() === "只读",
    );
    expect(badge?.className ?? "").toContain("min-[400px]:inline-flex");
    const header = title?.closest("div.flex");
    expect(header?.className ?? "").toContain("flex-wrap");
    expect(header?.className ?? "").toContain("min-h-[38px]");
  });

  it("NewCredentialModal 类型图标窄屏隐藏、footer 常驻", async () => {
    mounted = mount(
      createElement(NewCredentialModal, { onClose: () => undefined, onSaved: () => undefined }),
    );
    await waitFor(() => expect(mounted!.container.textContent).toContain("新建凭据"));

    const modal = mounted!.container.querySelector(".nx-modal");
    expect(modal?.className ?? "").toContain("flex-col");
    const footer = mounted!.container.querySelector(".nx-modal-footer");
    expect(footer?.className ?? "").toContain("shrink-0");
    const kindButtons = [...mounted!.container.querySelectorAll<HTMLButtonElement>("button")].filter(
      (b) => ["密码", "私钥", "API Key"].includes(b.textContent?.trim() ?? ""),
    );
    expect(kindButtons.length).toBe(3);
    for (const button of kindButtons) {
      const icon = button.querySelector("span");
      expect(icon?.className ?? "").toContain("min-[400px]:flex");
    }
  });
});
