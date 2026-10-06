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
  update: vi.fn(),
  groupList: vi.fn(),
  groupUpdate: vi.fn(),
  groupDelete: vi.fn(),
  snippetList: vi.fn(),
  listCredentials: vi.fn(),
  probe: vi.fn(),
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
      list: mocks.list,
      search: mocks.search,
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
      list: vi.fn(),
    },
    terminalApi: { write: vi.fn() },
    dbApi: {},
  };
});

import { AssetTree, AssetEditor } from "../../features/explorer/AssetTree";
import { useUi } from "../../app/store";

const GROUP = {
  id: "g1",
  parentId: null,
  name: "生产",
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
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function buttonByTitle(container: ParentNode, title: string): HTMLButtonElement {
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
  mocks.groupList.mockResolvedValue([{ ...GROUP }]);
  mocks.listCredentials.mockResolvedValue([]);
  mocks.snippetList.mockResolvedValue([]);
  mocks.ask.mockResolvedValue(true);
  useUi.setState({ leftOpen: true, pushToast: mocks.toast, workspaces: [], sessions: [] });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  document.body.style.overflow = "";
});

describe("GroupRenameDialog accessible overlay", () => {
  async function openRenameDialog(): Promise<HTMLElement> {
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("生产"));
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
    expect(input.selectionEnd).toBe("生产".length);

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
    setInputValue(input, "生产环境");

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
});

describe("asset list load failure", () => {
  it("shows an error with retry instead of the empty state, and recovers", async () => {
    mocks.list
      .mockRejectedValueOnce({ code: "internal", message: "db locked" })
      .mockResolvedValueOnce([{ ...WEB }]);
    mounted = mountWithClient(createElement(AssetTree));

    await waitFor(() => expect(mounted!.container.textContent).toContain("加载失败：内部错误：db locked"));
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
      'input[placeholder="搜索资产 / 主机 / 用户"]',
    );
    if (!searchInput) throw new Error("Search input not found");
    setInputValue(searchInput, "web");

    await waitFor(() =>
      expect(mounted!.container.textContent).toContain("加载失败：内部错误：search down"),
    );
    expect(mounted!.container.textContent).not.toContain("没有匹配的资产");
    expect(searchInput.value).toBe("web");

    clickButton(mounted!.container, "重试");
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));
    expect(mocks.search).toHaveBeenCalledTimes(2);
    expect(searchInput.value).toBe("web");
    expect(mounted!.container.querySelector('[role="alert"]')).toBeNull();
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
    expect(mocks.toast).not.toHaveBeenCalledWith("info", "已删除");

    clickButton(mounted!.container, "重试");
    await waitFor(() => expect(mocks.assetDelete).toHaveBeenCalledTimes(2));
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    await flush();
    expect(mounted!.container.textContent).not.toContain("disk on fire");
    expect(mocks.toast).toHaveBeenCalledWith("info", "已删除");
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

describe("theme-aware hover feedback", () => {
  it("footer entries use a theme-aware hover class, not white 5%", async () => {
    mounted = mountWithClient(createElement(AssetTree));
    await flush();
    for (const title of ["命令片段（插入当前终端）", "凭据库（左栏查看）"]) {
      const button = buttonByTitle(mounted!.container, title);
      expect(button.className).toContain("hover:bg-neutral-800/70");
      expect(button.className).not.toContain("hover:bg-white/[.05]");
    }
  });
});
