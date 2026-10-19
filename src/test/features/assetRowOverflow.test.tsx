/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  groupList: vi.fn(),
  snippetList: vi.fn(),
  listCredentials: vi.fn(),
  connect: vi.fn(),
  assetDelete: vi.fn(),
  ask: vi.fn(),
  pickKeyFile: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask, pickKeyFile: mocks.pickKeyFile };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      list: mocks.assetList,
      search: vi.fn(),
      update: vi.fn(),
      delete: mocks.assetDelete,
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
    terminalApi: { write: vi.fn() },
    dbApi: {},
  };
});

import { AssetTree } from "../../features/explorer/AssetTree";
import { useUi } from "../../app/store";

const GROUP = {
  id: "g1",
  parentId: null,
  name: "production",
  sort: 0,
  createdAt: 1,
  updatedAt: 1,
};

const WEB = {
  id: "a1",
  groupId: null,
  kind: "ssh",
  name: "web-1",
  host: "10.0.0.8",
  port: 22,
  username: "root",
  authKind: "password",
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

const LOCAL = {
  ...WEB,
  id: "a2",
  name: "local terminal",
  kind: "local",
  host: "",
  port: 0,
  builtin: true,
};

const GROUPED = { ...WEB, id: "a3", groupId: "g1", name: "db-1" };

function stubPointer(coarse: boolean): void {
  vi.stubGlobal(
    "matchMedia",
    (query: string) =>
      ({
        matches: coarse && query.includes("pointer: coarse"),
        media: query,
        onchange: null,
        addEventListener: () => {},
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
      }) as MediaQueryList,
  );
}

function mountWithClient(node: ReactNode): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function rowByName(container: HTMLElement, name: string): HTMLElement {
  const row = [...container.querySelectorAll<HTMLElement>('[role="treeitem"][title]')].find(
    (r) => (r.getAttribute("title") ?? "").startsWith(name),
  );
  if (!row) throw new Error(`Asset row not found: ${name}`);
  return row;
}

function menuLabels(): string[] {
  return [...document.querySelectorAll('[role="menuitem"]')].map(
    (b) => b.querySelector(".nx-menu-label")?.textContent ?? b.textContent ?? "",
  );
}

function openOverflow(row: HTMLElement): void {
  const more = row.querySelector<HTMLButtonElement>(".nx-row-more");
  if (!more) throw new Error("overflow button not found");
  click(more);
}

function setRowWidths(row: HTMLElement, available: number, nameWidth: number, identityWidth: number): void {
  const area = row.querySelector<HTMLElement>(".nx-row-name");
  const name = row.querySelector<HTMLElement>(".nx-asset-row-name-measure");
  const identity = row.querySelector<HTMLElement>(".nx-asset-row-identity-measure");
  if (!area || !name || !identity) throw new Error("Asset identity layout nodes not found");
  Object.defineProperty(area, "clientWidth", { configurable: true, value: available });
  name.getBoundingClientRect = () => ({ width: nameWidth }) as DOMRect;
  identity.getBoundingClientRect = () => ({ width: identityWidth }) as DOMRect;
  act(() => window.dispatchEvent(new Event("resize")));
}

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.resetAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  mocks.assetList.mockResolvedValue([{ ...WEB }, { ...LOCAL }, { ...GROUPED }]);
  mocks.groupList.mockResolvedValue([{ ...GROUP }]);
  mocks.listCredentials.mockResolvedValue([]);
  mocks.snippetList.mockResolvedValue([]);
  mocks.ask.mockResolvedValue(true);
  mocks.connect.mockResolvedValue({
    id: "s1",
    assetId: "a1",
    name: "web-1",
    kind: "ssh",
    status: "connected",
    tabs: [],
    createdAt: 0,
  });
  useUi.setState({ leftOpen: true, leftWidth: 248, pushToast: mocks.toast, workspaces: [], sessions: [] });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
});

describe("asset row actions on fine pointers", () => {
  it("keeps the hover action strip and hides the overflow button", async () => {
    stubPointer(false);
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));

    const row = rowByName(mounted!.container, "web-1");
    const labels = [...row.querySelectorAll<HTMLButtonElement>(".nx-row-actions button")].map(
      (b) => b.getAttribute("aria-label"),
    );
    expect(labels).toEqual(["连接 web-1", "编辑 web-1", "删除 web-1", "更多操作 web-1"]);
    expect(row.querySelector(".nx-row-more")).toBeNull();
    expect(row.querySelector(".nx-asset-row-name")?.textContent).toBe("web-1");
    expect(row.querySelector(".nx-asset-row-identity")?.textContent).toBe("root@10.0.0.8:22");
    expect(row.querySelector(".nx-row-target")).toBeNull();
    expect(row.getAttribute("title")).toContain("10.0.0.8:22");
  });

  it("keeps the simple clone/hide menu on the strip's more button", async () => {
    stubPointer(false);
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));

    const more = rowByName(mounted!.container, "web-1").querySelector<HTMLButtonElement>(
      'button[aria-label="更多操作 web-1"]',
    );
    if (!more) throw new Error("more button not found");
    click(more);
    await flush();
    expect(menuLabels()).toEqual(["克隆", "复制 SSH 命令", "复制链接", "隐藏"]);
  });

  it("uses the full overflow menu when the sidebar becomes too narrow for the action strip", async () => {
    stubPointer(false);
    useUi.setState({ leftWidth: 180 });
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));

    const row = rowByName(mounted!.container, "web-1");
    expect(row.querySelector(".nx-row-actions")).toBeNull();
    openOverflow(row);
    await flush();
    expect(menuLabels()).toEqual(["连接", "编辑", "删除", "克隆", "复制 SSH 命令", "复制链接", "隐藏"]);

    act(() => useUi.setState({ leftWidth: 248 }));
    expect([...row.querySelectorAll<HTMLButtonElement>(".nx-row-actions button")].map((button) => button.getAttribute("aria-label"))).toEqual([
      "连接 web-1", "编辑 web-1", "删除 web-1", "更多操作 web-1",
    ]);
  });
});

describe("asset name priority in narrow sidebars", () => {
  it("updates root and grouped asset identities as available row width changes", async () => {
    stubPointer(false);
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("db-1"));

    const root = rowByName(mounted!.container, "web-1");
    const grouped = rowByName(mounted!.container, "db-1");
    setRowWidths(root, 180, 75, 110);
    setRowWidths(grouped, 320, 75, 110);
    expect(root.querySelector(".nx-asset-row-name")?.textContent).toBe("web-1");
    expect(root.querySelector(".nx-asset-row-identity")).toBeNull();
    expect(grouped.querySelector(".nx-asset-row-name")?.textContent).toBe("db-1");
    expect(grouped.querySelector(".nx-asset-row-identity")?.textContent).toBe("root@10.0.0.8:22");

    setRowWidths(root, 320, 75, 110);
    setRowWidths(grouped, 180, 75, 110);
    expect(root.querySelector(".nx-asset-row-identity")?.textContent).toBe("root@10.0.0.8:22");
    expect(grouped.querySelector(".nx-asset-row-identity")).toBeNull();
  });

  it("shows host identities for same-name assets without hovering, including grouped rows", async () => {
    stubPointer(false);
    mocks.assetList.mockResolvedValue([
      { ...WEB, name: "shared" },
      { ...GROUPED, name: "shared", host: "10.0.0.18", username: "deploy" },
    ]);
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.querySelectorAll('[role="treeitem"][title^="shared"]')).toHaveLength(2));

    const rows = [...mounted!.container.querySelectorAll<HTMLElement>('[role="treeitem"][title^="shared"]')];
    expect(rows.map((row) => row.querySelector(".nx-asset-row-name")?.textContent)).toEqual(["shared", "shared"]);
    expect(rows.map((row) => row.querySelector(".nx-asset-row-identity")?.textContent)).toEqual([
      "root@10.0.0.8:22",
      "deploy@10.0.0.18:22",
    ]);
    expect(rows.every((row) => row.querySelector(".nx-row-target") === null)).toBe(true);
  });

  it("shows identities after names only when space remains, and allows compact rows", async () => {
    stubPointer(false);
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));

    const row = rowByName(mounted!.container, "web-1");
    expect(row.querySelector(".nx-asset-row-identity")?.textContent).toBe("root@10.0.0.8:22");
    setRowWidths(row, 131, 36, 89);
    expect(row.querySelector(".nx-asset-row-name")?.textContent).toBe("web-1");
    expect(row.querySelector(".nx-asset-row-identity")).toBeNull();
    setRowWidths(row, 199, 36, 89);
    expect(row.querySelector(".nx-asset-row-identity")?.textContent).toBe("root@10.0.0.8:22");
    const compact = mounted!.container.querySelector<HTMLButtonElement>('button[aria-label="紧凑显示资产"]');
    if (!compact) throw new Error("Compact display button not found");
    click(compact);
    expect(row.querySelector(".nx-asset-row-name")?.textContent).toBe("web-1");
    expect(row.querySelector(".nx-asset-row-identity")).toBeNull();

    const detailed = mounted!.container.querySelector<HTMLButtonElement>('button[aria-label="显示资产主机身份"]');
    if (!detailed) throw new Error("Detailed display button not found");
    click(detailed);
    setRowWidths(row, 199, 36, 89);
    expect(row.querySelector(".nx-asset-row-identity")?.textContent).toBe("root@10.0.0.8:22");
  });

  it.each([
    "生产环境华东核心交易集群主数据库不可中断管理员入口与灾备切换专用节点",
    "ProductionPrimaryPostgreSQLClusterWithAnExceptionallyLongUnbrokenAssetName0123456789",
  ])("gives the full name priority over host details without truncation: %s", async (name) => {
    stubPointer(false);
    mocks.assetList.mockResolvedValue([{ ...WEB, name }]);
    useUi.setState({ leftWidth: 180 });
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain(name));

    const row = rowByName(mounted!.container, name);
    const label = row.querySelector(".nx-asset-row-name");
    expect(label?.textContent).toBe(name);
    expect(label?.className).toContain("nx-asset-row-name");
    expect(label?.className).not.toContain("truncate");
    expect(label?.parentElement?.className).toContain("flex-1");
    setRowWidths(row, 131, 220, 89);
    expect(row.querySelector(".nx-asset-row-identity")).toBeNull();
    expect(row.querySelector(".nx-row-target")).toBeNull();
    expect(row.querySelector(".nx-row-more")).not.toBeNull();
    expect((mounted!.container.firstElementChild as HTMLElement).style.width).toBe("180px");
  });
});

describe("asset row actions on coarse pointers", () => {
  it("collapses row actions into one overflow button and keeps the name first", async () => {
    stubPointer(true);
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));

    const row = rowByName(mounted!.container, "web-1");
    expect(row.querySelector(".nx-row-actions")).toBeNull();
    const more = row.querySelector<HTMLButtonElement>(".nx-row-more");
    expect(more?.getAttribute("aria-label")).toBe("更多操作 web-1");
    expect(row.querySelector(".nx-asset-row-name")?.textContent).toBe("web-1");
    expect(row.querySelector(".nx-asset-row-identity")?.textContent).toBe("root@10.0.0.8:22");
    expect(row.querySelector(".nx-row-target")).toBeNull();
    expect(row.getAttribute("title")).toContain("10.0.0.8:22");
    expect(mocks.connect).not.toHaveBeenCalled();
  });

  it("opens a full menu with connect, edit, delete, clone and hide", async () => {
    stubPointer(true);
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));

    openOverflow(rowByName(mounted!.container, "web-1"));
    await flush();
    expect(menuLabels()).toEqual(["连接", "编辑", "删除", "克隆", "复制 SSH 命令", "复制链接", "隐藏"]);
    await waitFor(() => expect(document.activeElement?.textContent).toContain("连接"));
  });

  it("connects only through the explicit menu item", async () => {
    stubPointer(true);
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));

    const row = rowByName(mounted!.container, "web-1");
    click(row.querySelector(".nx-row-name") as HTMLElement);
    await flush();
    expect(mocks.connect).not.toHaveBeenCalled();

    openOverflow(row);
    await flush();
    const connectItem = [...document.querySelectorAll('[role="menuitem"]')].find((b) =>
      b.textContent?.includes("连接"),
    );
    if (!connectItem) throw new Error("connect item not found");
    click(connectItem);
    await waitFor(() => expect(mocks.connect).toHaveBeenCalledWith("a1"));
    expect(document.querySelector('[role="menu"]')).toBeNull();
  });

  it("keeps delete behind the menu plus confirmation", async () => {
    stubPointer(true);
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));

    openOverflow(rowByName(mounted!.container, "web-1"));
    await flush();
    mocks.ask.mockResolvedValueOnce(false);
    const deleteItem = [...document.querySelectorAll('[role="menuitem"]')].find((b) =>
      b.textContent?.includes("删除"),
    );
    if (!deleteItem) throw new Error("delete item not found");
    click(deleteItem);
    await flush();
    expect(mocks.ask).toHaveBeenCalledWith(expect.stringContaining("web-1"), expect.anything());
    expect(mocks.assetDelete).not.toHaveBeenCalled();
  });

  it("omits delete for builtin assets", async () => {
    stubPointer(true);
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("local terminal"));

    openOverflow(rowByName(mounted!.container, "local terminal"));
    await flush();
    expect(menuLabels()).toEqual(["连接", "编辑", "克隆", "复制链接", "隐藏"]);
  });

  it("closes the menu with Escape", async () => {
    stubPointer(true);
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));

    openOverflow(rowByName(mounted!.container, "web-1"));
    await flush();
    expect(document.querySelector('[role="menu"]')).not.toBeNull();
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    await flush();
    expect(document.querySelector('[role="menu"]')).toBeNull();
    expect(mocks.connect).not.toHaveBeenCalled();
  });

  it("offers the full action set on context menu as well", async () => {
    stubPointer(true);
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));

    const row = rowByName(mounted!.container, "web-1");
    act(() => {
      row.dispatchEvent(
        new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 20, clientY: 20 }),
      );
    });
    await flush();
    expect(menuLabels()).toEqual(["连接", "编辑", "删除", "克隆", "复制 SSH 命令", "复制链接", "隐藏"]);
  });

  it("collapses group row actions into an overflow menu as well", async () => {
    stubPointer(true);
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("production"));

    const groupRow = [...mounted!.container.querySelectorAll<HTMLElement>('[role="treeitem"]')].find(
      (r) => r.textContent?.includes("production") && r.querySelector(".nx-count"),
    );
    if (!groupRow) throw new Error("group row not found");
    expect(groupRow.querySelector(".nx-row-actions")).toBeNull();
    openOverflow(groupRow);
    await flush();
    expect(menuLabels()).toEqual(["在分组内新建资产", "新建子分组", "重命名分组", "删除分组"]);

    const rename = [...document.querySelectorAll('[role="menuitem"]')].find((b) =>
      b.textContent?.includes("重命名分组"),
    );
    if (!rename) throw new Error("rename item not found");
    click(rename);
    await waitFor(() => expect(mounted!.container.querySelector(".nx-modal")).not.toBeNull());
  });
});
