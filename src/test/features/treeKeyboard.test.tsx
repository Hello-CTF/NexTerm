/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { mount, setInputValue, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  assetSearch: vi.fn(),
  groupList: vi.fn(),
  snippetList: vi.fn(),
  listCredentials: vi.fn(),
  connect: vi.fn(),
  fsList: vi.fn(),
  toast: vi.fn(),
  fleetDevices: vi.fn(),
  fleetAccount: vi.fn(),
}));

vi.mock("../../features/fleet/clients", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../features/fleet/clients")>()),
  fleetClient: () => ({ devices: mocks.fleetDevices }),
}));
vi.mock("../../features/fleet/useFleetAccount", () => ({ useFleetAccount: mocks.fleetAccount }));
vi.mock("../../ipc/events", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ipc/events")>()),
  listenEvent: () => Promise.resolve(() => {}),
}));
vi.mock("../../ipc/webTransport", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ipc/webTransport")>()),
  onEventsResync: () => () => {},
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      list: mocks.assetList,
      search: mocks.assetSearch,
      searchMeta: async (query: string) => {
        const assets = await mocks.assetSearch(query);
        return { assets, total: assets.length, truncated: false };
      },
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
vi.mock("@tanstack/react-virtual", () => ({
  defaultRangeExtractor: (range: { startIndex: number; endIndex: number }) =>
    Array.from({ length: range.endIndex - range.startIndex + 1 }, (_, index) => range.startIndex + index),
  useVirtualizer: ({
    count,
    rangeExtractor,
  }: {
    count: number;
    rangeExtractor: (range: { startIndex: number; endIndex: number; overscan: number; count: number }) => number[];
  }) => {
    const indexes = count === 0 ? [] : rangeExtractor({ startIndex: 0, endIndex: Math.min(count - 1, 7), overscan: 0, count });
    return {
      getVirtualItems: () => indexes.map((index) => ({ index, start: index * 62, size: 62, key: index })),
      getTotalSize: () => count * 62,
      measureElement: () => {},
      scrollToIndex: () => {},
    };
  },
}));

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
  localStorage.clear();
  mocks.assetList.mockResolvedValue([{ ...ASSET_UNGROUPED }, { ...ASSET_GROUPED }]);
  mocks.assetSearch.mockResolvedValue([]);
  mocks.groupList.mockResolvedValue([{ ...GROUP }]);
  mocks.snippetList.mockResolvedValue([]);
  mocks.listCredentials.mockResolvedValue([]);
  mocks.fleetAccount.mockReturnValue({ loading: false, userId: null, isAdmin: false });
  mocks.fleetDevices.mockResolvedValue({ devices: [] });
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
    await waitFor(() => expect(useUi.getState().activeWorkspaceId).not.toBeNull());
    expect(mocks.connect).toHaveBeenCalledTimes(1);
    expect(useUi.getState().workspaces.filter((w) => w.assetId === "a1")).toHaveLength(1);
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
    expect(asset.className).toContain("nx-row-reserve-actions");
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

    const event = keyDown(connect, "Enter");
    expect(event.defaultPrevented).toBe(false);
    expect(mocks.connect).not.toHaveBeenCalled();
  });

  it("restores the focused asset after clearing search from the input", async () => {
    mocks.assetSearch.mockResolvedValue([{ ...ASSET_GROUPED }]);
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("db-1"));

    const search = mounted!.container.querySelector<HTMLInputElement>(
      'input[placeholder="搜索资产 / 主机 / 用户"]',
    );
    if (!search) throw new Error("Search input not found");
    setInputValue(search, "db-1");
    await waitFor(() => expect(mocks.assetSearch).toHaveBeenCalledWith("db-1"));
    await waitFor(() => {
      itemByText(mounted!.container, "db-1");
    });
    const asset = itemByText(mounted!.container, "db-1");
    act(() => asset.focus());

    act(() => search.focus());
    setInputValue(search, "");

    await waitFor(() => expect(document.activeElement).toBe(itemByText(mounted!.container, "db-1")));
  });

  it("windows large trees without narrowing full-range search or grouped row actions", async () => {
    const groups = Array.from({ length: 60 }, (_, index) => ({
      ...GROUP,
      id: `g-${index}`,
      name: `group-${index}`,
    }));
    const assets = groups.map((group, index) => ({
      ...ASSET_GROUPED,
      id: `a-${index}`,
      groupId: group.id,
      name: index === groups.length - 1 ? "needle-59" : `asset-${index}`,
    }));
    mocks.assetList.mockResolvedValue(assets);
    mocks.groupList.mockResolvedValue(groups);
    mocks.assetSearch.mockImplementation(async (query: string) =>
      assets.filter((asset) => asset.name.includes(query)),
    );
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(treeitems(mounted!.container).length).toBeGreaterThan(0));
    expect(treeitems(mounted!.container).length).toBeLessThan(groups.length);

    const search = mounted!.container.querySelector<HTMLInputElement>(
      'input[placeholder="搜索资产 / 主机 / 用户"]',
    );
    if (!search) throw new Error("Search input not found");
    setInputValue(search, "needle-59");
    await waitFor(() => expect(mocks.assetSearch).toHaveBeenCalledWith("needle-59"));
    await waitFor(() => {
      itemByText(mounted!.container, "needle-59");
    });

    const target = itemByText(mounted!.container, "needle-59");
    expect(treeitems(mounted!.container).length).toBeLessThan(groups.length);
    expect(target.getAttribute("aria-level")).toBe("2");
    expect(target.closest("[role='group']")).not.toBeNull();
    expect(
      [...target.querySelectorAll<HTMLButtonElement>(".nx-row-actions button")].map((button) =>
        button.getAttribute("aria-label"),
      ),
    ).toEqual(["连接 needle-59", "编辑 needle-59", "删除 needle-59", "更多操作 needle-59"]);
  });

  it("windows assets inside one large group while preserving search and hierarchy", async () => {
    const targetName = "needle-99-完整资产名称-ProductionPrimaryClusterWithALongName";
    const assets = Array.from({ length: 100 }, (_, index) => ({
      ...ASSET_GROUPED,
      id: `single-${index}`,
      name: index === 99 ? targetName : `single-${index}`,
    }));
    mocks.assetList.mockResolvedValue(assets);
    mocks.groupList.mockResolvedValue([{ ...GROUP }]);
    mocks.assetSearch.mockImplementation(async (query: string) =>
      assets.filter((asset) => asset.name.includes(query)),
    );
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("single-0"));

    const assetRows = () => treeitems(mounted!.container).filter((row) => row.getAttribute("aria-level") === "2");
    expect(assetRows().length).toBeLessThan(assets.length);

    const search = mounted!.container.querySelector<HTMLInputElement>(
      'input[placeholder="搜索资产 / 主机 / 用户"]',
    );
    if (!search) throw new Error("Search input not found");
    setInputValue(search, "needle-99");
    await waitFor(() => {
      itemByText(mounted!.container, targetName);
    });
    const target = itemByText(mounted!.container, targetName);
    expect(target.getAttribute("aria-level")).toBe("2");
    expect(target.closest("[role='group']")).not.toBeNull();
    expect(target.querySelector(".nx-asset-row-name")?.textContent).toBe(targetName);
    expect(target.querySelector(".nx-asset-row-name")?.className).not.toContain("truncate");
    expect(target.querySelector(".nx-asset-row-identity")?.textContent).toBe("root@10.0.0.8:22");
    expect(target.querySelectorAll(".nx-row-actions button")).toHaveLength(4);
  });

  it.each([
    { assetCount: 39, visibleRowCount: 40 },
    { assetCount: 40, visibleRowCount: 41 },
  ])(
    "preserves direct DOM at 40 visible rows and windows at 41, counting the device row: $assetCount assets + 1 device = $visibleRowCount rows",
    async ({ assetCount, visibleRowCount }) => {
      const assets = Array.from({ length: assetCount }, (_, index) => ({
        ...ASSET_UNGROUPED,
        id: `boundary-${index}`,
        name: `boundary-${index}`,
      }));
      const now = Date.now();
      mocks.assetList.mockResolvedValue(assets);
      mocks.groupList.mockResolvedValue([]);
      mocks.fleetAccount.mockReturnValue({ loading: false, userId: "u-boundary", isAdmin: false });
      mocks.fleetDevices.mockResolvedValue({
        devices: [
          {
            id: "d-boundary",
            name: "boundary-device",
            kind: "agent",
            created_at: 1,
            last_seen_at: now,
            revoked_at: 0,
            agent: {
              platform: "linux",
              app_version: "0.0.0",
              desired_autostart: true,
              terminal_enabled: true,
              current_url: "",
              service_state: { installed: true, enabled: true, active: true, last_reconcile_at: now },
              last_seen_at: now,
            },
          },
        ],
      });
      mounted = mountWithClient(createElement(AssetTree));
      await waitFor(() => expect(mounted!.container.textContent).toContain("boundary-0"));
      await waitFor(() => expect(mounted!.container.textContent).toContain("boundary-device"));

      const tree = mounted!.container.querySelector<HTMLElement>("[role='tree']");
      if (!tree) throw new Error("Asset tree not found");
      const rows = treeitems(tree);
      const assetRows = rows.filter((row) => row.querySelector(".nx-asset-row-name") !== null);
      if (visibleRowCount === 40) {
        expect(rows).toHaveLength(visibleRowCount);
        expect(rows.every((row) => row.parentElement === tree)).toBe(true);
        expect(assetRows).toHaveLength(assetCount);
        expect(assetRows.every((row) => !row.hasAttribute("data-asset-id"))).toBe(true);
        expect(tree.querySelector("[data-index]")).toBeNull();
      } else {
        expect(rows.length).toBeLessThan(visibleRowCount);
        expect(assetRows.length).toBeLessThan(assetCount);
        expect(tree.querySelector("[data-index]")).not.toBeNull();
      }
    },
  );
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

  it("opens the row menu with the ContextMenu key and Shift+F10, Escape dismisses", async () => {
    await mountTree();
    const dir = itemByText(mounted!.container, "logs");
    act(() => dir.focus());

    keyDown(dir, "ContextMenu");
    await waitFor(() =>
      expect(mounted!.container.querySelector('[role="menu"]')).not.toBeNull(),
    );
    expect(dir.getAttribute("aria-selected")).toBe("true");
    expect(
      [...mounted!.container.querySelectorAll('[role="menuitem"]')].some((b) =>
        b.textContent?.includes("打包下载当前目录"),
      ),
    ).toBe(true);

    keyDown(document.activeElement as HTMLElement, "Escape");
    await waitFor(() => expect(mounted!.container.querySelector('[role="menu"]')).toBeNull());

    act(() => dir.focus());
    keyDown(dir, "F10", { shiftKey: true });
    await waitFor(() =>
      expect(mounted!.container.querySelector('[role="menu"]')).not.toBeNull(),
    );
    expect(dir.getAttribute("aria-selected")).toBe("true");
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
    expect(row.className).toContain("nx-row-reserve-actions");
    const labels = [...row.querySelectorAll<HTMLButtonElement>(".nx-row-actions button")].map(
      (b) => b.getAttribute("aria-label"),
    );
    expect(labels).toEqual(["下载 notes.txt", "删除 notes.txt", "更多操作 notes.txt"]);
  });
});
