/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement, type ReactNode } from "react";
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
  list: vi.fn(),
  search: vi.fn(),
  create: vi.fn(),
  update: vi.fn(),
  assetDelete: vi.fn(),
  groupList: vi.fn(),
  groupUpdate: vi.fn(),
  groupDelete: vi.fn(),
  snippetList: vi.fn(),
  listCredentials: vi.fn(),
  setCredential: vi.fn(),
  revealCredential: vi.fn(),
  probe: vi.fn(),
  write: vi.fn(),
  ask: vi.fn(),
  pickKeyFile: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask, pickKeyFile: mocks.pickKeyFile };
});
vi.mock("../../ipc/webFiles", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/webFiles")>();
  return {
    ...actual,
    browserFilesAvailable: () => false,
    pickBrowserFile: vi.fn(),
  };
});
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      list: mocks.list,
      search: mocks.search,
      create: mocks.create,
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
      setCredential: mocks.setCredential,
      revealCredential: mocks.revealCredential,
    },
    sessionApi: { probe: mocks.probe, connect: vi.fn(), list: vi.fn() },
    terminalApi: { write: mocks.write },
    dbApi: {},
  };
});

import { AssetTree } from "../../features/explorer/AssetTree";
import { useAssetVisibility } from "../../features/explorer/assetVisibility";
import { useUi } from "../../app/store";
import type { Asset } from "../../ipc/commands";

const GROUP = {
  id: "g1",
  parentId: null,
  name: "生产",
  sort: 0,
  createdAt: 1,
  updatedAt: 1,
};

function assetOf(extra: Partial<Asset> & { id: string; name: string }): Asset {
  return {
    groupId: "g1",
    kind: "ssh",
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
    ...extra,
  };
}

const WEB = assetOf({ id: "a1", name: "web-1", credId: "cred-1" });
const BUILD = assetOf({
  id: "a2",
  name: "build-1",
  authKind: "key",
  keyPath: "/keys/ci_ed25519",
  credId: null,
  host: "10.0.0.40",
  username: "ci",
});

function mountWithClient(node: ReactNode): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function rowByText(container: HTMLElement, text: string): HTMLElement {
  const row = [...container.querySelectorAll<HTMLElement>('[role="treeitem"][title]')].find(
    (r) => (r.getAttribute("title") ?? "").startsWith(text),
  );
  if (!row) throw new Error(`Asset row not found: ${text}`);
  return row;
}

function openRowMenu(container: HTMLElement, name: string): void {
  click(
    [...container.querySelectorAll("button")].find(
      (b) => b.getAttribute("aria-label") === `更多操作 ${name}`,
    ) ?? (() => { throw new Error(`More button not found: ${name}`); })(),
  );
}

function clickMenuItem(container: ParentNode, label: string): void {
  const item = [...container.querySelectorAll('[role="menuitem"]')].find((b) =>
    b.textContent?.includes(label),
  );
  if (!item) throw new Error(`Menu item not found: ${label}`);
  click(item);
}

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  useAssetVisibility.setState({ hiddenIds: [], showHidden: false });
  mocks.list.mockResolvedValue([{ ...WEB }, { ...BUILD }]);
  mocks.search.mockResolvedValue([{ ...WEB }]);
  mocks.groupList.mockResolvedValue([{ ...GROUP }]);
  mocks.listCredentials.mockResolvedValue([]);
  mocks.snippetList.mockResolvedValue([]);
  mocks.ask.mockResolvedValue(true);
  mocks.create.mockImplementation((args: Partial<Asset> & { kind: string; name: string }) =>
    Promise.resolve(assetOf({ id: "a-new", ...args })),
  );
  useUi.setState({ leftOpen: true, pushToast: mocks.toast });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  useAssetVisibility.setState({ hiddenIds: [], showHidden: false });
});

describe("资产克隆", () => {
  beforeEach(async () => {
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(rowByText(mounted!.container, "web-1")).toBeTruthy());
  });

  it("确认后创建共享凭据引用的新资产，不复制凭据内容", async () => {
    openRowMenu(mounted!.container, "web-1");
    clickMenuItem(mounted!.container, "克隆");
    await waitFor(() => expect(mocks.create).toHaveBeenCalled());

    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("web-1 副本"),
      expect.objectContaining({ title: "克隆资产" }),
    );
    expect(mocks.create).toHaveBeenCalledWith(
      expect.objectContaining({
        kind: "ssh",
        name: "web-1 副本",
        groupId: "g1",
        host: "10.0.0.8",
        port: 22,
        username: "root",
        authKind: "password",
        credId: "cred-1",
      }),
    );
    expect(mocks.setCredential).not.toHaveBeenCalled();
    expect(mocks.revealCredential).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("web-1 副本"));
    await waitFor(() => expect(mocks.list.mock.calls.length).toBeGreaterThan(1));
  });

  it("私钥路径引用也原样共享", async () => {
    openRowMenu(mounted!.container, "build-1");
    clickMenuItem(mounted!.container, "克隆");
    await waitFor(() => expect(mocks.create).toHaveBeenCalled());
    expect(mocks.create).toHaveBeenCalledWith(
      expect.objectContaining({ name: "build-1 副本", keyPath: "/keys/ci_ed25519", credId: null }),
    );
  });

  it("取消则不创建", async () => {
    mocks.ask.mockResolvedValueOnce(false);
    openRowMenu(mounted!.container, "web-1");
    clickMenuItem(mounted!.container, "克隆");
    await flush();
    expect(mocks.create).not.toHaveBeenCalled();
  });
});

describe("资产隐藏 / 取消隐藏", () => {
  beforeEach(async () => {
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(rowByText(mounted!.container, "web-1")).toBeTruthy());
  });

  it("隐藏只是展示状态：行消失、数据保留、可找回", async () => {
    openRowMenu(mounted!.container, "web-1");
    clickMenuItem(mounted!.container, "隐藏");
    let eye: HTMLButtonElement | undefined;
    await waitFor(() => {
      eye = [...mounted!.container.querySelectorAll("button")].find((b) =>
        (b.getAttribute("aria-label") ?? "").startsWith("显示已隐藏的资产"),
      ) as HTMLButtonElement | undefined;
      if (!eye) throw new Error("Show-hidden toggle not found yet");
    });
    expect(
      [...mounted!.container.querySelectorAll('[role="treeitem"]')].some((r) =>
        r.textContent?.includes("web-1"),
      ),
    ).toBe(false);
    expect(mocks.assetDelete).not.toHaveBeenCalled();
    expect(mocks.update).not.toHaveBeenCalled();
    expect(JSON.parse(localStorage.getItem("nexterm.hiddenAssets.v1") ?? "[]")).toContain("a1");

    click(eye as HTMLButtonElement);
    let row: HTMLElement | undefined;
    await waitFor(() => {
      row = rowByText(mounted!.container, "web-1");
    });
    expect(row?.textContent).toContain("已隐藏");
    expect(row?.className).toContain("opacity-55");

    openRowMenu(mounted!.container, "web-1");
    clickMenuItem(mounted!.container, "取消隐藏");
    await waitFor(() =>
      expect(rowByText(mounted!.container, "web-1").textContent).not.toContain("已隐藏"),
    );
    expect(
      [...mounted!.container.querySelectorAll("button")].some((b) =>
        (b.getAttribute("aria-label") ?? "").startsWith("显示已隐藏的资产"),
      ),
    ).toBe(false);
    expect(JSON.parse(localStorage.getItem("nexterm.hiddenAssets.v1") ?? "[]")).not.toContain("a1");
  });

  it("搜索同样过滤隐藏资产，显示隐藏后可见", async () => {
    useAssetVisibility.getState().hide("a1");
    const input = mounted!.container.querySelector<HTMLInputElement>("input.nx-input");
    if (!input) throw new Error("Search input not found");
    setInputValue(input, "web");
    await waitFor(() => expect(mocks.search).toHaveBeenCalledWith("web"));
    await flush();
    expect(mounted!.container.textContent).toContain("没有匹配的资产");

    useAssetVisibility.getState().setShowHidden(true);
    await waitFor(() => expect(rowByText(mounted!.container, "web-1")).toBeTruthy());
  });
});
