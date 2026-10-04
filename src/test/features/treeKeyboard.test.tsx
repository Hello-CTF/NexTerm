/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  assetSearch: vi.fn(),
  groupList: vi.fn(),
  snippetList: vi.fn(),
  listCredentials: vi.fn(),
  connect: vi.fn(),
  fsList: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      list: mocks.assetList,
      search: mocks.assetSearch,
      update: vi.fn(),
      delete: vi.fn(),
      groupList: mocks.groupList,
      groupUpdate: vi.fn(),
      groupDelete: vi.fn(),
      snippetList: mocks.snippetList,
      snippetCreate: vi.fn(),
      snippetUpdate: vi.fn(),
      snippetDelete: vi.fn(),
      readKeyFile: vi.fn(),
    },
    vaultApi: {
      listCredentials: mocks.listCredentials,
      status: vi.fn().mockResolvedValue({ initialized: true, unlocked: true }),
      setCredential: vi.fn(),
    },
    sessionApi: {
      connect: mocks.connect,
      probe: vi.fn(),
      list: vi.fn().mockResolvedValue([]),
      connectLocal: vi.fn(),
      reconnect: vi.fn(),
    },
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
    terminalApi: { write: vi.fn() },
    dbApi: {},
  };
});

import { AssetTree } from "../../features/explorer/AssetTree";
import { FileTree } from "../../features/files/FileTree";
import { useUi } from "../../app/store";
import type { FileEntryDto } from "../../ipc/types";

const GROUP = {
  id: "g1",
  parentId: null,
  name: "生产",
  sort: 0,
  createdAt: 1,
  updatedAt: 1,
};

const ASSET_UNGROUPED = {
  id: "a1",
  groupId: null,
  kind: "ssh",
  name: "web-1",
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

const ASSET_GROUPED = { ...ASSET_UNGROUPED, id: "a2", groupId: "g1", name: "db-1" };

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

const LOGS_ENTRIES: FileEntryDto[] = [
  {
    name: "app.log",
    path: "~/logs/app.log",
    kind: "file",
    size: 64,
    mode: "-rw-r--r--",
    owner: "root",
    group: "root",
    mtime: 1,
    symlinkTarget: null,
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

function treeitems(container: ParentNode): HTMLElement[] {
  return [...container.querySelectorAll<HTMLElement>('[role="treeitem"]')];
}

function itemByText(container: ParentNode, text: string): HTMLElement {
  const matches = treeitems(container).filter((candidate) =>
    candidate.textContent?.includes(text),
  );
  const item = matches.at(-1);
  if (!item) throw new Error(`treeitem not found: ${text}`);
  return item;
}

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.resetAllMocks();
  document.body.replaceChildren();
  mocks.assetList.mockResolvedValue([{ ...ASSET_UNGROUPED }, { ...ASSET_GROUPED }]);
  mocks.assetSearch.mockResolvedValue([]);
  mocks.groupList.mockResolvedValue([{ ...GROUP }]);
  mocks.snippetList.mockResolvedValue([]);
  mocks.listCredentials.mockResolvedValue([]);
  mocks.connect.mockResolvedValue({
    id: "s-new",
    assetId: "a1",
    name: "web-1",
    kind: "ssh",
    status: "connected",
  });
  mocks.fsList.mockImplementation((_sessionId: string, dir: string) =>
    Promise.resolve(dir === "~" ? HOME_ENTRIES : dir === "~/logs" ? LOGS_ENTRIES : []),
  );
  useUi.setState({
    leftOpen: true,
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
});

describe("AssetTree keyboard navigation", () => {
  it("exposes tree/treeitem roles with levels and expanded state", async () => {
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));

    expect(mounted!.container.querySelector('[role="tree"][aria-label="资产"]')).not.toBeNull();
    const group = itemByText(mounted!.container, "生产");
    expect(group.getAttribute("aria-expanded")).toBe("true");
    expect(group.getAttribute("aria-level")).toBe("1");
    const asset = itemByText(mounted!.container, "web-1");
    expect(asset.getAttribute("aria-level")).toBe("1");
    expect(asset.tabIndex).toBe(0);
    const grouped = itemByText(mounted!.container, "db-1");
    expect(grouped.getAttribute("aria-level")).toBe("2");
  });

  it("expands and collapses groups with arrows, Enter and Space", async () => {
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("db-1"));

    const group = itemByText(mounted!.container, "生产");
    act(() => group.focus());
    keyDown(group, "ArrowLeft");
    expect(group.getAttribute("aria-expanded")).toBe("false");
    expect(() => itemByText(mounted!.container, "db-1")).toThrow();

    keyDown(group, "ArrowRight");
    expect(group.getAttribute("aria-expanded")).toBe("true");
    await waitFor(() => expect(mounted!.container.textContent).toContain("db-1"));

    keyDown(group, "Enter");
    expect(group.getAttribute("aria-expanded")).toBe("false");
    keyDown(group, " ");
    expect(group.getAttribute("aria-expanded")).toBe("true");
  });

  it("connects an asset with Enter and Space from the keyboard", async () => {
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));

    const asset = itemByText(mounted!.container, "web-1");
    act(() => asset.focus());
    keyDown(asset, "Enter");
    await waitFor(() => expect(mocks.connect).toHaveBeenCalledWith("a1"));

    keyDown(asset, " ");
    await waitFor(() => expect(mocks.connect).toHaveBeenCalledTimes(2));
    expect(useUi.getState().workspaces.length).toBeGreaterThan(0);
  });

  it("moves focus between rows with ArrowDown and ArrowUp", async () => {
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("db-1"));

    const asset = itemByText(mounted!.container, "web-1");
    act(() => asset.focus());
    keyDown(asset, "ArrowDown");
    expect(document.activeElement).toBe(itemByText(mounted!.container, "生产"));
    keyDown(document.activeElement as HTMLElement, "ArrowDown");
    expect(document.activeElement).toBe(itemByText(mounted!.container, "db-1"));
    keyDown(document.activeElement as HTMLElement, "ArrowUp");
    expect(document.activeElement).toBe(itemByText(mounted!.container, "生产"));
  });

  it("labels row actions and reveals them on focus-within", async () => {
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));

    const asset = itemByText(mounted!.container, "web-1");
    expect(asset.className).toContain("focus-within:");
    const actions = [...asset.querySelectorAll<HTMLButtonElement>(".nx-row-actions button")];
    expect(actions.map((b) => b.getAttribute("aria-label"))).toEqual([
      "连接 web-1",
      "编辑 web-1",
      "删除 web-1",
      "更多操作 web-1",
    ]);
    const headerButtons = [...mounted!.container.querySelectorAll<HTMLButtonElement>(".nx-icon-btn")];
    expect(headerButtons.every((b) => b.getAttribute("aria-label"))).toBe(true);
  });

  it("never swallows Enter pressed on a row action button", async () => {
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));

    const asset = itemByText(mounted!.container, "web-1");
    const connect = asset.querySelector<HTMLButtonElement>('button[aria-label="连接 web-1"]');
    if (!connect) throw new Error("connect button not found");
    act(() => connect.focus());

    // 行的 Enter 处理不得吞掉按钮上的按键，否则原生 click 无法合成。
    const event = keyDown(connect, "Enter");
    expect(event.defaultPrevented).toBe(false);
    expect(mocks.connect).not.toHaveBeenCalled();
  });
});

describe("FileTree keyboard navigation", () => {
  async function mountTree(): Promise<void> {
    mounted = mountWithClient(createElement(FileTree, { sessionId: "s1" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("notes.txt"));
  }

  it("exposes tree roles, levels, selection and expanded state", async () => {
    await mountTree();
    expect(mounted!.container.querySelector('[role="tree"][aria-label="文件"]')).not.toBeNull();
    const dir = itemByText(mounted!.container, "logs");
    expect(dir.getAttribute("aria-expanded")).toBe("false");
    expect(dir.getAttribute("aria-level")).toBe("1");
    expect(dir.getAttribute("aria-selected")).toBe("false");
    const caret = dir.querySelector<HTMLButtonElement>(".nx-tree-caret");
    expect(caret?.getAttribute("aria-label")).toBe("展开 logs");
    expect(caret?.getAttribute("aria-expanded")).toBe("false");
  });

  it("expands with ArrowRight and collapses with ArrowLeft", async () => {
    await mountTree();
    const dir = itemByText(mounted!.container, "logs");
    act(() => dir.focus());

    keyDown(dir, "ArrowRight");
    await waitFor(() => expect(mounted!.container.textContent).toContain("app.log"));
    expect(dir.getAttribute("aria-expanded")).toBe("true");
    expect(mocks.fsList).toHaveBeenCalledWith("s1", "~/logs");

    keyDown(dir, "ArrowLeft");
    expect(dir.getAttribute("aria-expanded")).toBe("false");
    expect(mounted!.container.textContent).not.toContain("app.log");
  });

  it("selects with Space and opens files with Enter", async () => {
    await mountTree();
    const file = itemByText(mounted!.container, "notes.txt");
    act(() => file.focus());

    keyDown(file, " ");
    expect(file.getAttribute("aria-selected")).toBe("true");

    keyDown(file, "Enter");
    await waitFor(() => {
      const tabs = useUi.getState().workspaces[0]?.panes[0]?.tabs ?? [];
      expect(tabs.some((t) => t.kind === "files" && t.path === "~/notes.txt")).toBe(true);
    });
  });

  it("enters a directory with Enter and marks the current crumb", async () => {
    await mountTree();
    const dir = itemByText(mounted!.container, "logs");
    act(() => dir.focus());

    keyDown(dir, "Enter");
    await waitFor(() =>
      expect(mounted!.container.querySelector('[aria-current="location"]')?.textContent).toBe(
        "logs",
      ),
    );
  });

  it("labels every icon-only control in header and path bar", async () => {
    await mountTree();
    const controls = [
      ...mounted!.container.querySelectorAll<HTMLButtonElement>(
        ".nx-icon-btn, .nx-tree-caret",
      ),
    ];
    expect(controls.length).toBeGreaterThan(0);
    expect(controls.every((b) => b.getAttribute("aria-label"))).toBe(true);
    const row = itemByText(mounted!.container, "notes.txt");
    expect(row.className).toContain("focus-within:");
    const labels = [...row.querySelectorAll<HTMLButtonElement>(".nx-row-actions button")].map(
      (b) => b.getAttribute("aria-label"),
    );
    expect(labels).toEqual(["下载 notes.txt", "删除 notes.txt", "更多操作 notes.txt"]);
  });
});
