/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  clickButton,
  flush,
  mount,
  setInputValue,
  waitFor,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  search: vi.fn(),
  searchMeta: vi.fn(),
  update: vi.fn(),
  groupList: vi.fn(),
  groupUpdate: vi.fn(),
  groupDelete: vi.fn(),
  snippetList: vi.fn(),
  listCredentials: vi.fn(),
  probe: vi.fn(),
  assetDelete: vi.fn(),
  sessionDisconnect: vi.fn(),
  sessionList: vi.fn(),
  ask: vi.fn(),
  choice: vi.fn(),
  pickKeyFile: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask, askChoice: mocks.choice, pickKeyFile: mocks.pickKeyFile };
});
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      list: mocks.list,
      search: mocks.search,
      searchMeta: mocks.searchMeta,
      update: mocks.update,
      delete: mocks.assetDelete,
      groupList: mocks.groupList,
      groupUpdate: mocks.groupUpdate,
      groupDelete: mocks.groupDelete,
      snippetList: mocks.snippetList,
      readKeyFile: vi.fn(),
    },
    vaultApi: {
      listCredentials: mocks.listCredentials,
      setCredential: vi.fn(),
    },
    sessionApi: {
      probe: mocks.probe,
      connect: vi.fn(),
      disconnect: mocks.sessionDisconnect,
      list: mocks.sessionList,
    },
    terminalApi: { write: vi.fn() },
    dbApi: {},
  };
});

import { AssetTree, AssetEditor } from "../../features/explorer/AssetTree";
import { useUi } from "../../app/store";
import type { Asset } from "../../ipc/commands";

const originalCloseWorkspace = useUi.getState().closeWorkspace;

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

function mountWithClient(node: ReactNode): MountedView {
  mounted?.unmount();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

// Actions that moved into the header or row menus are reached by opening the owning menu first.
const MENU_ROUTES: Record<string, { opener: "group" | "asset" | "view"; item: string }> = {
  重命名分组: { opener: "group", item: "重命名分组" },
  删除分组: { opener: "group", item: "删除分组…" },
  删除: { opener: "asset", item: "删除资产…" },
  批量维护: { opener: "view", item: "批量维护" },
  紧凑显示资产: { opener: "view", item: "紧凑显示" },
  显示资产主机身份: { opener: "view", item: "显示主机地址" },
};

function buttonByTitle(container: ParentNode, title: string): HTMLButtonElement {
  const route = MENU_ROUTES[title];
  if (route) {
    const opener =
      route.opener === "view"
        ? container.querySelector<HTMLButtonElement>('button[aria-label="视图"]')
        : route.opener === "group"
          ? container.querySelector<HTMLButtonElement>('[role="treeitem"][aria-expanded] button[aria-label^="更多操作 "]')
          : container.querySelector<HTMLButtonElement>('[role="treeitem"]:not([aria-expanded]) button[aria-label^="更多操作 "]');
    if (!opener) throw new Error(`Menu opener not found for: ${title}`);
    click(opener);
    const item = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find((b) =>
      b.textContent?.includes(route.item),
    );
    if (!item) throw new Error(`Menu item not found: ${route.item}`);
    return item;
  }
  const button = [...container.querySelectorAll("button")].find(
    (candidate) => candidate.title === title,
  );
  if (!button) throw new Error(`Button not found by title: ${title}`);
  return button;
}

function keyDown(target: EventTarget, key: string, init: KeyboardEventInit = {}): KeyboardEvent {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.resetAllMocks();
  document.body.replaceChildren();
  document.body.style.overflow = "";
  mocks.list.mockResolvedValue([]);
  mocks.search.mockResolvedValue([]);
  mocks.searchMeta.mockImplementation((query) => mocks.search(query).then((rows: Asset[]) => ({ assets: rows, total: rows.length, truncated: false })));
  mocks.groupList.mockResolvedValue([{ ...GROUP }]);
  mocks.listCredentials.mockResolvedValue([]);
  mocks.snippetList.mockResolvedValue([]);
  mocks.sessionDisconnect.mockResolvedValue(undefined);
  mocks.sessionList.mockResolvedValue([]);
  mocks.ask.mockResolvedValue(true);
  mocks.choice.mockResolvedValue(null);
  useUi.setState({ leftOpen: true, pushToast: mocks.toast, workspaces: [], sessions: [] });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  useUi.setState({ closeWorkspace: originalCloseWorkspace });
  document.body.style.overflow = "";
});

describe("GroupRenameDialog accessible overlay", () => {
  async function openRenameDialog(): Promise<HTMLElement> {
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("production"));
    click(buttonByTitle(mounted!.container, "重命名分组"));
    const modal = mounted!.container.querySelector<HTMLElement>('[role="dialog"][aria-modal="true"]');
    if (!modal) throw new Error("Rename dialog not found");
    return modal;
  }

  it("labels the dialog, focuses the name input with selection, and wraps Tab", async () => {
    const modal = await openRenameDialog();
    const title = document.getElementById(modal.getAttribute("aria-labelledby") ?? "");
    expect(title?.textContent).toBe("重命名分组");

    const input = modal.querySelector<HTMLInputElement>("input.nx-input");
    if (!input) throw new Error("Rename input not found");
    expect(document.activeElement).toBe(input);
    expect(input.selectionStart).toBe(0);
    expect(input.selectionEnd).toBe("production".length);

    const cancel = [...modal.querySelectorAll("button")].find((b) => b.textContent?.trim() === "取消");
    const save = [...modal.querySelectorAll("button")].find((b) => b.textContent?.trim() === "保存");
    if (!cancel || !save) throw new Error("Footer buttons not found");
    act(() => save.focus());
    keyDown(save, "Tab");
    expect(document.activeElement).toBe(input);
    keyDown(input, "Tab", { shiftKey: true });
    expect(document.activeElement).toBe(save);
  });

  it("closes a clean dialog with Escape without asking", async () => {
    const modal = await openRenameDialog();
    keyDown(modal, "Escape");
    await flush();
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mounted!.container.querySelector(".nx-modal")).toBeNull();
  });

  it("asks at warning level before discarding a dirty rename", async () => {
    const modal = await openRenameDialog();
    const input = modal.querySelector<HTMLInputElement>("input.nx-input");
    if (!input) throw new Error("Rename input not found");
    setInputValue(input, "production environment");

    mocks.ask.mockResolvedValueOnce(false);
    keyDown(modal, "Escape");
    await flush();
    expect(mocks.ask).toHaveBeenCalledExactlyOnceWith(
      "放弃未保存的修改？",
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mounted!.container.querySelector(".nx-modal")).not.toBeNull();

    keyDown(modal, "Escape");
    await flush();
    expect(mocks.ask).toHaveBeenCalledTimes(2);
    expect(mounted!.container.querySelector(".nx-modal")).toBeNull();
  });
});

describe("AssetEditor accessible overlay", () => {
  function mountEditor() {
    const onClose = vi.fn();
    mounted = mountWithClient(
      createElement(AssetEditor, { kind: "asset", onClose, onSaved: vi.fn() }),
    );
    return onClose;
  }

  it("labels the dialog, focuses the name input, and wraps Tab", async () => {
    mountEditor();
    await flush();
    const modal = mounted!.container.querySelector<HTMLElement>('[role="dialog"][aria-modal="true"]');
    if (!modal) throw new Error("Editor dialog not found");
    const title = document.getElementById(modal.getAttribute("aria-labelledby") ?? "");
    expect(title?.textContent).toBe("新建资产");

    const nameInput = modal.querySelector<HTMLInputElement>('input[placeholder="web-01"]');
    if (!nameInput) throw new Error("Name input not found");
    expect(document.activeElement).toBe(nameInput);

    const focusables = [...modal.querySelectorAll<HTMLElement>("button, input, select, textarea")].filter(
      (el) => !el.hasAttribute("disabled"),
    );
    const first = focusables[0];
    const last = focusables[focusables.length - 1];
    act(() => last.focus());
    keyDown(last, "Tab");
    expect(document.activeElement).toBe(first);
    keyDown(first, "Tab", { shiftKey: true });
    expect(document.activeElement).toBe(last);
  });

  it("closes a clean editor with Escape or overlay click without asking", async () => {
    const onClose = mountEditor();
    await flush();
    const modal = mounted!.container.querySelector<HTMLElement>(".nx-modal");
    if (!modal) throw new Error("Editor dialog not found");
    keyDown(modal, "Escape");
    await flush();
    expect(onClose).toHaveBeenCalledOnce();
    expect(mocks.ask).not.toHaveBeenCalled();

    const onClose2 = mountEditor();
    await flush();
    const overlay = mounted!.container.querySelector<HTMLElement>(".nx-overlay");
    if (!overlay) throw new Error("Overlay not found");
    click(overlay);
    await flush();
    expect(onClose2).toHaveBeenCalledOnce();
    expect(mocks.ask).not.toHaveBeenCalled();
  });

  it("asks before discarding a dirty editor and keeps it open on decline", async () => {
    const onClose = mountEditor();
    await flush();
    const nameInput = mounted!.container.querySelector<HTMLInputElement>('input[placeholder="web-01"]');
    if (!nameInput) throw new Error("Name input not found");
    setInputValue(nameInput, "web-01");

    mocks.ask.mockResolvedValueOnce(false);
    const overlay = mounted!.container.querySelector<HTMLElement>(".nx-overlay");
    if (!overlay) throw new Error("Overlay not found");
    click(overlay);
    await flush();
    expect(mocks.ask).toHaveBeenCalledExactlyOnceWith(
      "放弃未保存的修改？",
      expect.objectContaining({ kind: "warning" }),
    );
    expect(onClose).not.toHaveBeenCalled();
    expect(mounted!.container.querySelector(".nx-modal")).not.toBeNull();

    clickButton(mounted!.container, "取消");
    await flush();
    expect(mocks.ask).toHaveBeenCalledTimes(2);
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("asks before closing after only selecting an icon", async () => {
    const onClose = mountEditor();
    await flush();
    clickButton(mounted!.container, "选择图标");
    const choice = mounted!.container.querySelector<HTMLButtonElement>('button[aria-label^="选择图标 "]');
    if (!choice) throw new Error("Icon choice not found");
    click(choice);

    mocks.ask.mockResolvedValueOnce(false);
    const modal = mounted!.container.querySelector<HTMLElement>(".nx-modal");
    if (!modal) throw new Error("Editor dialog not found");
    keyDown(modal, "Escape");
    await flush();
    expect(mocks.ask).toHaveBeenCalledExactlyOnceWith(
      "放弃未保存的修改？",
      expect.objectContaining({ kind: "warning" }),
    );
    expect(onClose).not.toHaveBeenCalled();
  });

  it("asks before closing after only restoring the default icon", async () => {
    const onClose = vi.fn();
    mounted = mountWithClient(
      createElement(AssetEditor, {
        kind: "asset",
        initial: { ...WEB, kind: "ssh" as const, options: { icon: "server" } },
        onClose,
        onSaved: vi.fn(),
      }),
    );
    await flush();
    clickButton(mounted!.container, "恢复默认");

    mocks.ask.mockResolvedValueOnce(false);
    const modal = mounted!.container.querySelector<HTMLElement>(".nx-modal");
    if (!modal) throw new Error("Editor dialog not found");
    keyDown(modal, "Escape");
    await flush();
    expect(mocks.ask).toHaveBeenCalledExactlyOnceWith(
      "放弃未保存的修改？",
      expect.objectContaining({ kind: "warning" }),
    );
    expect(onClose).not.toHaveBeenCalled();
  });
});

describe("asset form field accessible names and group placeholder (M205 finding 12/13)", () => {
  it("gives the new credential password input an accessible name and placeholder", async () => {
    mounted = mountWithClient(
      createElement(AssetEditor, { kind: "asset", onClose: vi.fn(), onSaved: vi.fn() }),
    );
    await flush();
    const passwordInput = mounted.container.querySelector<HTMLInputElement>(
      'input[aria-label="凭据密码"]',
    );
    expect(passwordInput).not.toBeNull();
    expect(passwordInput!.type).toBe("password");
    expect(passwordInput!.placeholder).toBe("密码");
  });

  it("uses the group-specific placeholder for a new group name instead of the asset example", async () => {
    mounted = mountWithClient(
      createElement(AssetEditor, { kind: "group", onClose: vi.fn(), onSaved: vi.fn() }),
    );
    await flush();
    const nameInput = mounted.container.querySelector<HTMLInputElement>(
      'input[placeholder="生产环境"]',
    );
    expect(nameInput).not.toBeNull();
    expect(mounted.container.querySelector('input[placeholder="web-01"]')).toBeNull();
  });

  it("offers SSH import and external host sync from the single 新建 menu", async () => {
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("production"));
    const create = [...mounted.container.querySelectorAll("button")].find((b) => b.textContent?.trim() === "新建") as HTMLButtonElement;
    click(create);
    const labels = [...document.querySelectorAll('[role="menuitem"]')].map((b) => b.textContent ?? "");
    expect(labels.some((t) => t.includes("导入 SSH 配置"))).toBe(true);
    expect(labels.some((t) => t.includes("同步外部主机"))).toBe(true);
    expect(labels.some((t) => t.includes("新建分组"))).toBe(true);
  });

});

describe("asset list load failure", () => {
  it("shows an error with retry instead of the empty state, and recovers", async () => {
    mocks.list
      .mockRejectedValueOnce({ code: "internal", message: "db locked" })
      .mockResolvedValueOnce([{ ...WEB }]);
    mounted = mountWithClient(createElement(AssetTree));

    await waitFor(() => expect(mounted!.container.textContent).toContain("资产列表加载失败"));
    const alert = mounted!.container.querySelector('[role="alert"]');
    expect(alert?.textContent).toContain("db locked");
    expect(mounted!.container.textContent).not.toContain("还没有资产");

    clickButton(mounted!.container, "重试");
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));
    expect(mocks.list).toHaveBeenCalledTimes(2);
    expect(mounted!.container.querySelector('[role="alert"]')).toBeNull();
  });

  it("shows a search failure with retry, keeps the query, and recovers", async () => {
    mocks.list.mockResolvedValue([{ ...WEB }]);
    mocks.groupList.mockResolvedValue([]);
    mocks.search
      .mockRejectedValueOnce({ code: "internal", message: "search down" })
      .mockResolvedValueOnce([{ ...WEB }]);
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));

    const searchInput = mounted!.container.querySelector<HTMLInputElement>(
      'input[placeholder="搜索资产名称、地址或用户"]',
    );
    if (!searchInput) throw new Error("Search input not found");
    setInputValue(searchInput, "web");

    await waitFor(() =>
      expect(mounted!.container.textContent).toContain("搜索失败"),
    );
    expect(mounted!.container.textContent).not.toContain("没有匹配的资产");
    expect(searchInput.value).toBe("web");

    clickButton(mounted!.container, "重试");
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));
    expect(mocks.search).toHaveBeenCalledTimes(2);
    expect(searchInput.value).toBe("web");
    expect(mounted!.container.querySelector('[role="alert"]')).toBeNull();
  });

  it("shows the total and an explicit truncation hint for capped search results", async () => {
    const rows = Array.from({ length: 101 }, (_, index) => ({ ...WEB, id: `bulk-${index}`, name: `bulk-${index}` }));
    mocks.list.mockResolvedValue(rows);
    mocks.searchMeta.mockResolvedValue({ assets: rows.slice(0, 100), total: 101, truncated: true });
    mocks.groupList.mockResolvedValue([]);
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mocks.list).toHaveBeenCalled());
    await flush();
    const input = mounted.container.querySelector<HTMLInputElement>('input[placeholder="搜索资产名称、地址或用户"]')!;
    setInputValue(input, "bulk");
    await waitFor(() => expect(mounted!.container.textContent).toContain("找到 101 个匹配"));
    expect(mounted.container.textContent).toContain("请输入更具体的关键字");
    expect(mounted.container.textContent).toContain("仅显示前 100 个");
  });

  it("uses authoritative search metadata without running a second client matcher", async () => {
    mocks.list.mockResolvedValue([{ ...WEB, name: "💥" }]);
    mocks.searchMeta.mockResolvedValue({ assets: [{ ...WEB, name: "💥" }], total: 101, truncated: true });
    mocks.groupList.mockResolvedValue([]);
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mocks.list).toHaveBeenCalled());
    const input = mounted.container.querySelector<HTMLInputElement>('input[placeholder="搜索资产名称、地址或用户"]')!;
    setInputValue(input, "__");
    await waitFor(() => expect(mounted!.container.textContent).toContain("找到 101 个匹配"));
    expect(mounted.container.textContent).toContain("仅显示前 1 个");
    expect(mocks.search).not.toHaveBeenCalled();
  });

  it("refreshes active search after deleting an asset across the truncation boundary", async () => {
    mocks.list.mockResolvedValue([{ ...WEB }]);
    mocks.groupList.mockResolvedValue([]);
    mocks.searchMeta
      .mockResolvedValueOnce({ assets: [{ ...WEB }], total: 101, truncated: true })
      .mockResolvedValueOnce({ assets: [], total: 100, truncated: false });
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));
    const input = mounted.container.querySelector<HTMLInputElement>('input[placeholder="搜索资产名称、地址或用户"]')!;
    setInputValue(input, "web");
    await waitFor(() => expect(mocks.searchMeta).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(mounted!.container.textContent).toContain("找到 101 个匹配"));
    click(buttonByTitle(mounted!.container, "删除"));
    await waitFor(() => expect(mocks.assetDelete).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(mocks.searchMeta).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(mounted!.container.textContent).toContain("找到 100 个匹配"));
    expect(mounted.container.textContent).not.toContain("请输入更具体的关键字");
  });

  it("refreshes active search after renaming and deleting a group", async () => {
    mocks.list.mockResolvedValue([{ ...WEB, groupId: "g1" }]);
    mocks.searchMeta.mockResolvedValue({ assets: [{ ...WEB, groupId: "g1" }], total: 1, truncated: false });
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("production"));
    const input = mounted.container.querySelector<HTMLInputElement>('input[placeholder="搜索资产名称、地址或用户"]')!;
    setInputValue(input, "production");
    await waitFor(() => expect(mocks.searchMeta).toHaveBeenCalledTimes(1));

    click(buttonByTitle(mounted!.container, "重命名分组"));
    const renameInput = mounted.container.querySelector<HTMLInputElement>('.nx-modal input.nx-input')!;
    setInputValue(renameInput, "renamed");
    clickButton(mounted!.container, "保存");
    await waitFor(() => expect(mocks.groupUpdate).toHaveBeenCalled());
    await waitFor(() => expect(mocks.searchMeta).toHaveBeenCalledTimes(2));

    click(buttonByTitle(mounted!.container, "删除分组"));
    await waitFor(() => expect(mocks.groupDelete).toHaveBeenCalled());
    await waitFor(() => expect(mocks.searchMeta).toHaveBeenCalledTimes(3));
  });
});

describe("asset delete failure", () => {
  it("asks at warning level and surfaces the failure inline with a retry", async () => {
    mocks.list.mockResolvedValue([{ ...WEB }]);
    mocks.groupList.mockResolvedValue([]);
    mocks.assetDelete
      .mockRejectedValueOnce({ code: "internal", message: "disk on fire" })
      .mockResolvedValueOnce(undefined);
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));

    click(buttonByTitle(mounted!.container, "删除"));
    await waitFor(() => expect(mocks.assetDelete).toHaveBeenCalledTimes(1));
    expect(mocks.ask).toHaveBeenCalledExactlyOnceWith(
      expect.stringContaining("web-1"),
      expect.objectContaining({ kind: "warning" }),
    );
    await flush();
    expect(mounted!.container.textContent).toContain("删除失败：内部错误：disk on fire");
    expect(mocks.toast).not.toHaveBeenCalledWith("info", "已删除资产「web-1」");

    clickButton(mounted!.container, "重试");
    await waitFor(() => expect(mocks.assetDelete).toHaveBeenCalledTimes(2));
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    await flush();
    expect(mounted!.container.textContent).not.toContain("disk on fire");
    expect(mocks.toast).toHaveBeenCalledWith("info", "已删除资产「web-1」");
  });

  it("shows the failure inline for an asset inside a group", async () => {
    mocks.list.mockResolvedValue([{ ...WEB, id: "a2", groupId: "g1", name: "db-1" }]);
    mocks.assetDelete.mockRejectedValueOnce({ code: "internal", message: "disk on fire" });
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("db-1"));

    click(buttonByTitle(mounted!.container, "删除"));
    await waitFor(() => expect(mocks.assetDelete).toHaveBeenCalledTimes(1));
    await flush();
    expect(mounted!.container.textContent).toContain("删除失败：内部错误：disk on fire");

    clickButton(mounted!.container, "知道了");
    await flush();
    expect(mounted!.container.textContent).not.toContain("disk on fire");
  });
});

describe("asset deletion with an open workspace", () => {
  function openAssetWorkspace() {
    useUi.setState({
      workspaces: [{
        id: "ws-a1",
        kind: "session",
        title: "web-1",
        assetId: "a1",
        panes: [{ id: "p1", tabs: [], activeTabId: null }],
        activePaneId: "p1",
        closable: true,
      }],
    });
  }

  async function showAsset() {
    mocks.list.mockResolvedValue([{ ...WEB }]);
    openAssetWorkspace();
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));
  }

  it("offers an explicit choice and leaves the asset untouched when cancelled", async () => {
    await showAsset();
    click(buttonByTitle(mounted!.container, "删除"));
    await waitFor(() => expect(mocks.choice).toHaveBeenCalledOnce());
    expect(mocks.choice).toHaveBeenCalledWith(
      expect.stringContaining("有 1 个工作区正在使用"),
      expect.objectContaining({
        level: "warning",
        choices: expect.arrayContaining([
          expect.objectContaining({ key: "keep" }),
          expect.objectContaining({ key: "close" }),
        ]),
      }),
    );
    expect(mocks.assetDelete).not.toHaveBeenCalled();
    expect(useUi.getState().workspaces).toHaveLength(1);
  });

  it("keeps the open workspace when that option is chosen", async () => {
    mocks.choice.mockResolvedValueOnce("keep");
    const close = vi.fn(async () => true);
    useUi.setState({ closeWorkspace: close });
    await showAsset();
    click(buttonByTitle(mounted!.container, "删除"));
    await waitFor(() => expect(mocks.assetDelete).toHaveBeenCalledWith("a1"));
    expect(close).not.toHaveBeenCalled();
    await waitFor(() => expect(useUi.getState().workspaces[0]).toMatchObject({ id: "ws-a1", deletedTarget: true }));
  });

  it("disconnects live sessions without closing the workspace", async () => {
    mocks.choice.mockResolvedValueOnce("disconnect");
    const close = vi.fn(async () => true);
    useUi.setState({ closeWorkspace: close });
    await showAsset();
    act(() => {
      useUi.setState({
        sessions: [{ id: "s1", assetId: "a1", name: "web-1", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }],
      });
    });
    click(buttonByTitle(mounted!.container, "删除"));
    await waitFor(() => expect(mocks.sessionDisconnect).toHaveBeenCalledWith("s1"));
    expect(mocks.choice).toHaveBeenCalledWith(
      expect.stringContaining("有 1 个后台会话正在使用"),
      expect.objectContaining({
        choices: expect.arrayContaining([
          expect.objectContaining({ key: "disconnect" }),
          expect.objectContaining({ key: "close" }),
        ]),
      }),
    );
    await waitFor(() => expect(mocks.assetDelete).toHaveBeenCalledWith("a1"));
    expect(close).not.toHaveBeenCalled();
    expect(useUi.getState().workspaces[0]).toMatchObject({ id: "ws-a1", deletedTarget: true });
  });

  it("does not delete when closing the workspace is cancelled", async () => {
    mocks.choice.mockResolvedValueOnce("close");
    const close = vi.fn(async () => false);
    useUi.setState({ closeWorkspace: close });
    await showAsset();
    click(buttonByTitle(mounted!.container, "删除"));
    await waitFor(() => expect(close).toHaveBeenCalledWith("ws-a1"));
    expect(mocks.assetDelete).not.toHaveBeenCalled();
    expect(useUi.getState().workspaces).toHaveLength(1);
  });

  it("reports deletion failure after closing and retries without another choice", async () => {
    mocks.choice.mockResolvedValueOnce("close");
    mocks.assetDelete.mockRejectedValueOnce({ code: "internal", message: "disk on fire" });
    const close = vi.fn(async () => {
      useUi.setState({ workspaces: [] });
      return true;
    });
    useUi.setState({ closeWorkspace: close });
    await showAsset();
    click(buttonByTitle(mounted!.container, "删除"));
    await waitFor(() => expect(mocks.assetDelete).toHaveBeenCalledTimes(1));
    await flush();
    expect(mounted!.container.textContent).toContain("已关闭的工作区可在资产列表中重新打开");
    clickButton(mounted!.container, "重试");
    await waitFor(() => expect(mocks.assetDelete).toHaveBeenCalledTimes(2));
    expect(mocks.choice).toHaveBeenCalledTimes(1);
    expect(close).toHaveBeenCalledTimes(1);
  });
});

describe("theme-aware hover feedback", () => {
  it("footer entries use a theme-aware hover class, not white 5%", async () => {
    mounted = mountWithClient(createElement(AssetTree));
    await flush();
    for (const title of ["打开命令片段：保存常用命令，插入或在当前终端执行"]) {
      const button = buttonByTitle(mounted!.container, title);
      expect(button.className).toContain("hover:bg-neutral-800/70");
      expect(button.className).not.toContain("hover:bg-white/[.05]");
    }
  });
});
