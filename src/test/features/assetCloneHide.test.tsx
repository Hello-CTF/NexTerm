/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  flush,
  mount,
  setInputValue,
  waitForRendered,
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
      searchMeta: async (query: string) => {
        const assets = await mocks.search(query);
        return { assets, total: assets.length, truncated: false };
      },
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
  name: "production",
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

// The show-hidden toggle lives in the "视图" menu; opening it returns the matching item if present.
function viewMenuItem(container: ParentNode, label: string): HTMLButtonElement | undefined {
  const opener = container.querySelector<HTMLButtonElement>('button[aria-label="视图"]');
  if (!opener) throw new Error("View menu button not found");
  click(opener);
  return [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find((b) =>
    b.textContent?.includes(label),
  );
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
  mocks.assetDelete.mockResolvedValue(undefined);
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

describe("asset cloning", () => {
  beforeEach(async () => {
    mounted = mountWithClient(createElement(AssetTree));
    await waitForRendered(() => expect(rowByText(mounted!.container, "web-1")).toBeTruthy());
  });

  it("creates an asset that shares credential references after confirmation without copying credential contents", async () => {
    openRowMenu(mounted!.container, "web-1");
    clickMenuItem(mounted!.container, "克隆");
    await waitForRendered(() => expect(mocks.create).toHaveBeenCalled());

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
    await waitForRendered(() => expect(mocks.list.mock.calls.length).toBeGreaterThan(1));
  });

  it("shares private key path references unchanged", async () => {
    openRowMenu(mounted!.container, "build-1");
    clickMenuItem(mounted!.container, "克隆");
    await waitForRendered(() => expect(mocks.create).toHaveBeenCalled());
    expect(mocks.create).toHaveBeenCalledWith(
      expect.objectContaining({ name: "build-1 副本", keyPath: "/keys/ci_ed25519", credId: null }),
    );
  });

  it("does not create an asset when cancelled", async () => {
    mocks.ask.mockResolvedValueOnce(false);
    openRowMenu(mounted!.container, "web-1");
    clickMenuItem(mounted!.container, "克隆");
    await flush();
    expect(mocks.create).not.toHaveBeenCalled();
  });
});

describe("asset hiding and unhiding", () => {
  beforeEach(async () => {
    mounted = mountWithClient(createElement(AssetTree));
    await waitForRendered(() => expect(rowByText(mounted!.container, "web-1")).toBeTruthy());
  });

  it("hiding only affects visibility: removes the row, preserves data, and allows recovery", async () => {
    openRowMenu(mounted!.container, "web-1");
    clickMenuItem(mounted!.container, "在列表中隐藏");
    await waitForRendered(() => {
      if (!viewMenuItem(mounted!.container, "显示已隐藏的资产")) throw new Error("Show-hidden toggle not found yet");
    });
    expect(
      [...mounted!.container.querySelectorAll('[role="treeitem"]')].some((r) =>
        r.textContent?.includes("web-1"),
      ),
    ).toBe(false);
    expect(mocks.assetDelete).not.toHaveBeenCalled();
    expect(mocks.update).not.toHaveBeenCalled();
    expect(JSON.parse(localStorage.getItem("nexterm.hiddenAssets.v1") ?? "[]")).toContain("a1");

    click(viewMenuItem(mounted!.container, "显示已隐藏的资产")!);
    let row: HTMLElement | undefined;
    await waitForRendered(() => {
      row = rowByText(mounted!.container, "web-1");
    });
    expect(row?.textContent).toContain("已隐藏");
    expect(row?.className).toContain("opacity-55");

    openRowMenu(mounted!.container, "web-1");
    clickMenuItem(mounted!.container, "取消隐藏");
    await waitForRendered(() =>
      expect(rowByText(mounted!.container, "web-1").textContent).not.toContain("已隐藏"),
    );
    expect(viewMenuItem(mounted!.container, "已隐藏的资产")).toBeUndefined();
    expect(JSON.parse(localStorage.getItem("nexterm.hiddenAssets.v1") ?? "[]")).not.toContain("a1");
  });

  it("search also filters hidden assets and reveals them when hidden assets are shown", async () => {
    act(() => {
      useAssetVisibility.getState().hide("a1");
    });
    const input = mounted!.container.querySelector<HTMLInputElement>("input.nx-input");
    if (!input) throw new Error("Search input not found");
    setInputValue(input, "web");
    await waitForRendered(() => expect(mocks.search).toHaveBeenCalledWith("web"));
    await flush();
    expect(mounted!.container.textContent).toContain("没有匹配的资产");

    act(() => {
      useAssetVisibility.getState().setShowHidden(true);
    });
    await waitForRendered(() => expect(rowByText(mounted!.container, "web-1")).toBeTruthy());
  });

  it("deleting a hidden asset clears its visibility record and removes the eye count", async () => {
    act(() => {
      useAssetVisibility.getState().hide("a1");
      useAssetVisibility.getState().setShowHidden(true);
    });
    mounted?.unmount();
    mounted = mountWithClient(createElement(AssetTree));
    await waitForRendered(() => expect(rowByText(mounted!.container, "web-1")).toBeTruthy());
    expect(viewMenuItem(mounted!.container, "已隐藏的资产")).toBeTruthy();

    openRowMenu(mounted!.container, "web-1");
    clickMenuItem(mounted!.container, "删除资产…");
    await waitForRendered(() => expect(mocks.assetDelete).toHaveBeenCalledWith("a1"));

    expect(useAssetVisibility.getState().hiddenIds).not.toContain("a1");
    expect(JSON.parse(localStorage.getItem("nexterm.hiddenAssets.v1") ?? "[]")).not.toContain("a1");
    expect(viewMenuItem(mounted!.container, "已隐藏的资产")).toBeUndefined();
  });
});
