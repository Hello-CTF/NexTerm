/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  flush,
  mount,
  setInputValue,
  waitFor,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  fsList: vi.fn(),
  mountList: vi.fn(),
  mountCreate: vi.fn(),
  platform: vi.fn(),
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
    systemApi: { platform: mocks.platform },
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
    { id: "m1", localPoint: "Z:", remote: "\\\\nas\\share", sessionId: "s1", managed: true, createdAt: 1 },
  ]);
  mocks.mountCreate.mockResolvedValue(undefined);
  mocks.platform.mockResolvedValue("windows");
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

describe("touch More buttons reuse the same action menus", () => {
  it("FileTree row ⋯ opens the same menu as right-click", async () => {
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

  it("FileBrowser row and toolbar ⋯ buttons open their respective menus", async () => {
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
    expect(blankLabels.some((l) => l.includes("新建目录"))).toBe(true);
    expect(blankLabels.some((l) => l.includes("刷新"))).toBe(true);
  });

  it("FileBrowser size and modified-time columns plus low-frequency toolbar buttons have narrow-screen hiding classes", async () => {
    mounted = mountWithClient(createElement(FileBrowser, { sessionId: "s1" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("notes.txt"));

    const headerCells = [...mounted!.container.querySelectorAll("span")].filter((s) =>
      ["大小", "修改时间"].includes(s.textContent?.trim() ?? ""),
    );
    expect(headerCells.length).toBe(2);
    for (const cell of headerCells) {
      expect(cell.className).toContain("max-[560px]:hidden");
    }
    for (const label of ["上传", "新建"]) {
      const button = [...mounted!.container.querySelectorAll<HTMLButtonElement>(".nx-toolbar button")].find(
        (b) => b.textContent?.trim() === label,
      );
      expect(button?.className ?? "").toContain("max-[560px]:hidden");
    }
  });
});

describe("IME guards for single-line Enter-to-submit forms", () => {
  it("FileBrowser path input: Enter during IME composition does not navigate; regular Enter navigates", async () => {
    mounted = mountWithClient(createElement(FileBrowser, { sessionId: "s1" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("notes.txt"));
    const input = mounted!.container.querySelector<HTMLInputElement>(".nx-toolbar input");
    if (!input) throw new Error("path input not found");

    setInputValue(input, "/etc");
    const callsBefore = mocks.fsList.mock.calls.length;
    keyDown(input, "Enter", { isComposing: true });
    await flush();
    expect(mocks.fsList.mock.calls.length).toBe(callsBefore);

    keyDown(input, "Enter");
    await waitFor(() =>
      expect(mocks.fsList).toHaveBeenCalledWith("s1", "/etc"),
    );
  });

  it("FileTree path editing: Enter during IME composition does not submit; regular Enter submits", async () => {
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
    await flush();
    expect(mocks.fsList.mock.calls.length).toBe(callsBefore);

    keyDown(input, "Enter");
    await waitFor(() => expect(mocks.fsList).toHaveBeenCalledWith("s1", "/var"));
  });

  it("MountPanel remote path: Enter during IME composition does not create; regular Enter creates", async () => {
    mounted = mountWithClient(createElement(MountPanel, { sessionId: "s1" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("断开"));
    const input = mounted!.container.querySelector<HTMLInputElement>(
      'input[aria-label="远端路径"]',
    );
    if (!input) throw new Error("remote path input not found");

    setInputValue(input, "user@host:/data");
    keyDown(input, "Enter", { isComposing: true });
    await flush();
    expect(mocks.mountCreate).not.toHaveBeenCalled();

    keyDown(input, "Enter");
    await waitFor(() => expect(mocks.mountCreate).toHaveBeenCalledTimes(1));
  });

  it("group rename: Enter during IME composition does not save; regular Enter saves", async () => {
    mocks.groupUpdate.mockClear();
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("生产"));

    const opener = mounted!.container.querySelector<HTMLButtonElement>(
      '[role="treeitem"][aria-expanded] button[aria-label^="更多操作 "]',
    );
    if (!opener) throw new Error("group overflow button not found");
    click(opener);
    const rename = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find((b) =>
      b.textContent?.includes("重命名分组"),
    );
    if (!rename) throw new Error("group rename menu item not found");
    click(rename);
    await waitFor(() =>
      expect(mounted!.container.querySelector(".nx-modal input")).not.toBeNull(),
    );
    const input = mounted!.container.querySelector<HTMLInputElement>(".nx-modal input");
    if (!input) throw new Error("rename input not found");

    setInputValue(input, "生产环境");
    keyDown(input, "Enter", { isComposing: true });
    await flush();
    expect(mocks.groupUpdate).not.toHaveBeenCalled();

    keyDown(input, "Enter");
    await waitFor(() => expect(mocks.groupUpdate).toHaveBeenCalledWith("g1", "生产环境"));
  });
});

describe("MountPanel narrow-screen layout", () => {
  it("hint rows wrap, the column is labeled local mount point, and mount time is not shown", async () => {
    mounted = mountWithClient(createElement(MountPanel, { sessionId: "s1" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("断开"));

    const hintRow = mounted!.container.querySelector(".nx-code")?.closest("div");
    expect(hintRow?.className ?? "").toContain("flex-wrap");

    const headers = [...mounted!.container.querySelectorAll("th")].map((th) => th.textContent?.trim());
    expect(headers).toContain("本地挂载点");
    expect(headers).not.toContain("挂载时间");
    expect(mounted!.container.querySelector("tbody tr")?.querySelectorAll("td")).toHaveLength(3);
  });
});

describe("MountPanel optional credentials collapse", () => {
  it("username and password are collapsed by default and expand after clicking Use other credentials (optional)", async () => {
    mounted = mountWithClient(createElement(MountPanel, { sessionId: "s1" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("断开"));

    expect(mounted!.container.querySelector('input[aria-label="挂载用户名（可选）"]')).toBeNull();
    expect(mounted!.container.querySelector('input[aria-label="挂载密码（可选）"]')).toBeNull();

    const toggle = [...mounted!.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("使用其他凭据"),
    );
    if (!toggle) throw new Error("credential collapse toggle not found");
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    click(toggle);

    await waitFor(() =>
      expect(
        mounted!.container.querySelector('input[aria-label="挂载用户名（可选）"]'),
      ).not.toBeNull(),
    );
    expect(
      mounted!.container.querySelector('input[aria-label="挂载密码（可选）"]'),
    ).not.toBeNull();
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
  });
});

describe("credential overlay dock collapses after selection", () => {
  function allTabs() {
    return useUi.getState().workspaces.flatMap((w) => w.panes.flatMap((p) => p.tabs));
  }

  it("clicking a credential row at a viewport width of ≤820px collapses the left dock and opens details", async () => {
    setViewportWidth(320);
    mounted = mountWithClient(createElement(CredentialsSidebar));
    await waitFor(() => expect(mounted!.container.textContent).toContain("db-prod"));

    const row = [...mounted!.container.querySelectorAll<HTMLButtonElement>(".nx-row")].find(
      (b) => b.textContent?.includes("db-prod"),
    );
    if (!row) throw new Error("credential row not found");
    click(row);

    await waitFor(() => expect(useUi.getState().leftOpen).toBe(false));
    await waitFor(() =>
      expect(allTabs().some((t) => t.kind === "credentials" && t.credId === "c1")).toBe(true),
    );
  });

  it("clicking a credential row in a wide viewport does not collapse the left dock", async () => {
    setViewportWidth(1280);
    mounted = mountWithClient(createElement(CredentialsSidebar));
    await waitFor(() => expect(mounted!.container.textContent).toContain("db-prod"));

    const row = [...mounted!.container.querySelectorAll<HTMLButtonElement>(".nx-row")].find(
      (b) => b.textContent?.includes("db-prod"),
    );
    if (!row) throw new Error("credential row not found");
    click(row);

    await waitFor(() =>
      expect(allTabs().some((t) => t.kind === "credentials")).toBe(true),
    );
    expect(useUi.getState().leftOpen).toBe(true);
  });
});

describe("narrow-screen layout for the credentials header and new-credential modal", () => {
  it("CredentialsPanel header keeps the lock status visible on narrow screens and the primary button is not clipped", async () => {
    mounted = mountWithClient(createElement(CredentialsPanel, { credId: "c1" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("已解锁"));

    const badges = [...mounted!.container.querySelectorAll(".nx-badge")].filter((b) =>
      ["已解锁", "已锁定", "未启用密码保护"].includes(b.textContent?.trim() ?? ""),
    );
    expect(badges.length).toBeGreaterThan(0);
    // The lock state matters on a phone too: the header wraps instead of hiding the badge.
    for (const badge of badges) {
      expect(badge.className).not.toContain("hidden");
    }
    const title = [...mounted!.container.querySelectorAll("span")].find(
      (s) => s.textContent?.trim() === "凭据",
    );
    expect(title?.className ?? "").toContain("whitespace-nowrap");
    const header = title?.closest("div.flex");
    expect(header?.className ?? "").toContain("flex-wrap");
    expect(header?.className ?? "").toContain("min-h-[38px]");
    const create = [...mounted!.container.querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.textContent?.trim() === "新建凭据",
    );
    expect(create?.className ?? "").toContain("shrink-0");
  });

  it("NewCredentialModal type icons hide on narrow screens and the footer remains visible", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mounted = mount(
      createElement(
        QueryClientProvider,
        { client },
        createElement(NewCredentialModal, { onClose: () => undefined, onSaved: () => undefined }),
      ),
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


describe("device session panels render directly", () => {
  const DEVICE_SESSIONS = [
    { id: "s-dev", assetId: "device/d-1", assetName: "edge-gateway", kind: "device", status: "connected" },
  ];

  it("FileTree renders a normal file tree for device sessions without changing the left-panel width shell", async () => {
    useUi.setState({ sessions: DEVICE_SESSIONS as never, leftWidth: 300 });
    mounted = mountWithClient(createElement(FileTree, { sessionId: "s-dev" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("notes.txt"));

    const root = mounted!.container.firstElementChild as HTMLElement;
    expect(root.tagName).toBe("ASIDE");
    expect(root.style.width).toBe("300px");
    expect(mounted!.container.textContent).not.toContain("设备会话不支持");
  });

  it("FileBrowser renders a normal nx-pane panel for device sessions", async () => {
    useUi.setState({ sessions: DEVICE_SESSIONS as never });
    mounted = mountWithClient(createElement(FileBrowser, { sessionId: "s-dev" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("notes.txt"));

    const root = mounted!.container.firstElementChild as HTMLElement;
    expect(root.className).toContain("nx-pane");
    expect(mounted!.container.textContent).not.toContain("设备会话不支持");
  });
});
