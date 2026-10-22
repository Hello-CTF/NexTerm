/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { mount, setInputValue, setSelectValue, waitFor, type MountedView } from "./reactTestUtils";
import { ALL_PROJECTS, projectFocusForGroup } from "../../app/projectFocus";
import { useUi } from "../../app/store";
import type { Asset, AssetGroup } from "../../ipc/commands";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  assetSearch: vi.fn(),
  groupList: vi.fn(),
  snippetList: vi.fn(),
}));

vi.mock("../../features/fleet/DeviceTreeSection", () => ({
  DeviceTreeSection: () => <div>设备区</div>,
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
      listCredentials: vi.fn(async () => []),
      status: vi.fn(async () => ({ initialized: true, unlocked: true })),
      setCredential: vi.fn(),
    },
    sessionApi: {
      connect: vi.fn(),
      probe: vi.fn(),
      list: vi.fn(async () => []),
      connectLocal: vi.fn(),
      reconnect: vi.fn(),
    },
    terminalApi: { write: vi.fn() },
    dbApi: {},
  };
});

import { AssetTree } from "../../features/explorer/AssetTree";

function group(id: string, parentId: string | null = null): AssetGroup {
  return { id, parentId, name: `group-${id}`, sort: 0, createdAt: 1, updatedAt: 1 };
}

function asset(id: string, groupId: string | null): Asset {
  return {
    id,
    groupId,
    kind: "ssh",
    name: id,
    host: "127.0.0.1",
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
}

const groups = [group("p1"), group("child", "p1"), group("p2")];
const assets = [
  asset("root-asset", "p1"),
  asset("child-asset", "child"),
  asset("other-asset", "p2"),
  asset("free-asset", null),
];
const projectOne = projectFocusForGroup("p1");

let mounted: MountedView | undefined;

function mountTree(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(AssetTree)));
}

function assetNames(container: ParentNode): string[] {
  return [...container.querySelectorAll(".nx-asset-row-name")].map((node) => node.textContent);
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  window.localStorage.clear();
  mocks.assetList.mockResolvedValue(assets);
  mocks.assetSearch.mockImplementation(async (query: string) => assets.filter((item) => item.name.includes(query)));
  mocks.groupList.mockResolvedValue(groups);
  mocks.snippetList.mockResolvedValue([]);
  useUi.setState({
    leftOpen: true,
    activeProject: ALL_PROJECTS,
    projectInventory: null,
    workspaces: [],
    activeWorkspaceId: null,
    sessions: [],
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("AssetTree project focus", () => {
  it("filters assets, search results, group nodes, and devices with all and ungrouped options", async () => {
    mounted = mountTree();
    await waitFor(() => expect(assetNames(mounted!.container)).toHaveLength(4));
    const selector = mounted.container.querySelector<HTMLSelectElement>('select[aria-label="项目筛选"]');
    if (!selector) throw new Error("Project selector not found");
    expect([...selector.options].map((option) => option.textContent)).toEqual([
      "全部",
      "未分组",
      "group-p1",
      "group-p2",
    ]);
    expect(mounted.container.querySelector("[role='tree']")?.textContent).toContain("设备区");

    setSelectValue(selector, projectOne);
    await waitFor(() => expect(assetNames(mounted!.container)).toEqual(["root-asset", "child-asset"]));
    expect(mounted.container.querySelector("[role='tree']")?.textContent).toContain("group-child");
    expect(mounted.container.querySelector("[role='tree']")?.textContent).not.toContain("group-p2");
    expect(mounted.container.querySelector("[role='tree']")?.textContent).not.toContain("设备区");
    expect(useUi.getState().activeProject).toBe(projectOne);

    const search = mounted.container.querySelector<HTMLInputElement>('input[aria-label="搜索资产 / 主机 / 用户"]');
    if (!search) throw new Error("Asset search not found");
    setInputValue(search, "asset");
    await waitFor(() => expect(mocks.assetSearch).toHaveBeenCalledWith("asset"));
    await waitFor(() => expect(assetNames(mounted!.container)).toEqual(["root-asset", "child-asset"]));
    setInputValue(search, "");

    setSelectValue(selector, "ungrouped");
    await waitFor(() => expect(assetNames(mounted!.container)).toEqual(["free-asset"]));
    expect(mounted.container.querySelector("[role='tree']")?.textContent).not.toContain("group-p1");
    expect(mounted.container.querySelector("[role='tree']")?.textContent).toContain("设备区");

    setSelectValue(selector, "all");
    await waitFor(() => expect(assetNames(mounted!.container)).toHaveLength(4));
    expect(assetNames(mounted.container)).toEqual(["free-asset", "root-asset", "child-asset", "other-asset"]);
  });
});
