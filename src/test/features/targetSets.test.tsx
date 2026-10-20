/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import {
  SavedTargetSets,
  type SavedTargetSet,
  type SavedTargetSetsApi,
} from "../../features/execbatch/SavedTargetSets";
import {
  click,
  clickButton,
  deferred,
  flush,
  flushUntil,
  mount,
  setInputValue,
  type MountedView,
} from "./reactTestUtils";

const sets: SavedTargetSet[] = [
  { id: "set-1", name: "web", sessionIds: ["s1", "s2"], createdAt: 1, updatedAt: 1 },
  { id: "set-2", name: "db", sessionIds: ["s1", "gone"], createdAt: 2, updatedAt: 2 },
];

function fakeApi(overrides: Partial<SavedTargetSetsApi> = {}): SavedTargetSetsApi {
  return {
    list: vi.fn().mockResolvedValue(sets.map((set) => ({ ...set }))),
    create: vi.fn().mockImplementation((name: string, sessionIds: string[]) =>
      Promise.resolve({ id: "set-new", name, sessionIds, createdAt: 3, updatedAt: 3 }),
    ),
    update: vi.fn().mockResolvedValue(undefined),
    delete: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  };
}

function setsElement(
  api: SavedTargetSetsApi,
  options: { selectedIds?: string[]; availableTargetIds?: string[]; onSelect?: (ids: string[]) => void } = {},
) {
  return createElement(SavedTargetSets, {
    api,
    availableTargetIds: options.availableTargetIds ?? ["s1", "s2"],
    selectedIds: options.selectedIds ?? ["s1", "s2"],
    onSelect: options.onSelect ?? (() => {}),
  });
}

function mountSets(
  api: SavedTargetSetsApi,
  options: { selectedIds?: string[]; availableTargetIds?: string[]; onSelect?: (ids: string[]) => void } = {},
): MountedView {
  return mount(setsElement(api, options));
}

function rerenderSets(
  view: MountedView,
  api: SavedTargetSetsApi,
  options: { selectedIds?: string[]; availableTargetIds?: string[]; onSelect?: (ids: string[]) => void } = {},
): void {
  act(() => {
    view.root.render(setsElement(api, options));
  });
}

async function mountLoaded(api: SavedTargetSetsApi, options = {}): Promise<MountedView> {
  const view = mountSets(api, options);
  await flushUntil(() => view.container.textContent?.includes("web") ?? false);
  return view;
}

let mounted: MountedView | null = null;
afterEach(() => {
  mounted?.unmount();
  mounted = null;
});

describe("SavedTargetSets", () => {
  it("renders saved sets with online counts", async () => {
    const api = fakeApi();
    mounted = await mountLoaded(api);
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("web");
    expect(text).toContain("db");
    expect(text).toContain("在线 2/2");
    expect(text).toContain("在线 1/2");
    expect(api.list).toHaveBeenCalledTimes(1);
  });

  it("shows an empty state when no sets are saved", async () => {
    const api = fakeApi({ list: vi.fn().mockResolvedValue([]) });
    mounted = mountSets(api);
    await flushUntil(() => mounted?.container.textContent?.includes("暂无保存的目标集") ?? false);
  });

  it("surfaces a load failure", async () => {
    const api = fakeApi({ list: vi.fn().mockRejectedValue(new Error("boom")) });
    mounted = mountSets(api);
    await flushUntil(() => mounted?.container.querySelector("[role='alert']") !== null);
    expect(mounted.container.querySelector("[role='alert']")?.textContent).toContain("boom");
  });

  it("saves the current selection as a new set with a trimmed name", async () => {
    const api = fakeApi();
    mounted = await mountLoaded(api, { selectedIds: ["s1", "s2"] });
    const input = mounted.container.querySelector<HTMLInputElement>("input[aria-label='目标集名称']");
    if (!input) throw new Error("name input missing");
    setInputValue(input, "  cache  ");
    clickButton(mounted.container, "保存当前选择");
    await flush();
    expect(api.create).toHaveBeenCalledWith("cache", ["s1", "s2"]);
    expect(api.list).toHaveBeenCalledTimes(2);
    expect(input.value).toBe("");
  });

  it("keeps save disabled without a selection or a name", async () => {
    const api = fakeApi();
    mounted = await mountLoaded(api, { selectedIds: [] });
    const save = [...mounted.container.querySelectorAll("button")].find(
      (button) => button.textContent?.trim() === "保存当前选择",
    );
    expect(save?.disabled).toBe(true);
    const input = mounted.container.querySelector<HTMLInputElement>("input[aria-label='目标集名称']");
    if (!input) throw new Error("name input missing");
    setInputValue(input, "   ");
    expect(save?.disabled).toBe(true);
  });

  it("renames a set while keeping its sessions", async () => {
    const api = fakeApi();
    mounted = await mountLoaded(api);
    clickButton(mounted.container, "重命名");
    const renameInput = mounted.container.querySelector<HTMLInputElement>("input[aria-label='重命名目标集 web']");
    if (!renameInput) throw new Error("rename input missing");
    expect(renameInput.value).toBe("web");
    setInputValue(renameInput, "  web primary  ");
    clickButton(mounted.container, "保存名称");
    await flush();
    expect(api.update).toHaveBeenCalledWith("set-1", "web primary", ["s1", "s2"]);
    expect(api.list).toHaveBeenCalledTimes(2);
    expect(mounted.container.querySelector("input[aria-label='重命名目标集 web']")).toBeNull();
  });

  it("cancels renaming without touching the store", async () => {
    const api = fakeApi();
    mounted = await mountLoaded(api);
    clickButton(mounted.container, "重命名");
    clickButton(mounted.container, "取消");
    expect(api.update).not.toHaveBeenCalled();
    expect(mounted.container.textContent).toContain("web");
  });

  it("overwrites a set with the current selection", async () => {
    const api = fakeApi();
    mounted = await mountLoaded(api, { selectedIds: ["s2"] });
    clickButton(mounted.container, "覆盖");
    await flush();
    expect(api.update).toHaveBeenCalledWith("set-1", "web", ["s2"]);
    expect(api.list).toHaveBeenCalledTimes(2);
  });

  it("deletes a set only after confirmation", async () => {
    const api = fakeApi();
    mounted = await mountLoaded(api);
    clickButton(mounted.container, "删除");
    expect(api.delete).not.toHaveBeenCalled();
    clickButton(mounted.container, "确认删除");
    await flush();
    expect(api.delete).toHaveBeenCalledWith("set-1");
    expect(api.list).toHaveBeenCalledTimes(2);
  });

  it("applies a set intersected with the currently available targets", async () => {
    const api = fakeApi();
    const onSelect = vi.fn();
    mounted = await mountLoaded(api, { onSelect });
    const apply = [...mounted.container.querySelectorAll("button")].find(
      (button) => button.getAttribute("aria-label") === "选择目标集 db",
    );
    if (!apply) throw new Error("apply button missing");
    click(apply);
    expect(onSelect).toHaveBeenCalledWith(["s1"]);
  });

  it("surfaces mutation failures without reloading", async () => {
    const api = fakeApi({ create: vi.fn().mockRejectedValue(new Error("disk on fire")) });
    mounted = await mountLoaded(api);
    const input = mounted.container.querySelector<HTMLInputElement>("input[aria-label='目标集名称']");
    if (!input) throw new Error("name input missing");
    setInputValue(input, "cache");
    clickButton(mounted.container, "保存当前选择");
    await flushUntil(() => mounted?.container.querySelector("[role='alert']") !== null);
    expect(mounted.container.querySelector("[role='alert']")?.textContent).toContain("disk on fire");
    expect(api.list).toHaveBeenCalledTimes(1);
  });

  it("hides the previous owner's sets on the first frame after an API switch", async () => {
    const listB = deferred<SavedTargetSet[]>();
    const apiA = fakeApi();
    const apiB = fakeApi({ list: vi.fn(() => listB.promise) });
    mounted = await mountLoaded(apiA);
    expect(mounted.container.textContent).toContain("web");

    rerenderSets(mounted, apiB);
    expect(mounted.container.textContent).not.toContain("web");
    expect(mounted.container.textContent).toContain("加载中");

    await act(async () => {
      listB.resolve([]);
    });
    expect(mounted.container.textContent).toContain("暂无保存的目标集");
  });

  it("discards a stale list response that resolves after an API switch", async () => {
    const listA = deferred<SavedTargetSet[]>();
    const apiA = fakeApi({ list: vi.fn(() => listA.promise) });
    const apiB = fakeApi();
    mounted = mountSets(apiA);
    rerenderSets(mounted, apiB);
    await flushUntil(() => mounted?.container.textContent?.includes("web") ?? false);

    await act(async () => {
      listA.resolve([{ id: "a1", name: "owner-a-set", sessionIds: ["s1"], createdAt: 1, updatedAt: 1 }]);
    });
    expect(mounted.container.textContent).toContain("web");
    expect(mounted.container.textContent).not.toContain("owner-a-set");
  });

  it("discards a stale list failure that resolves after an API switch", async () => {
    const listA = deferred<SavedTargetSet[]>();
    const apiA = fakeApi({ list: vi.fn(() => listA.promise) });
    const apiB = fakeApi();
    mounted = mountSets(apiA);
    rerenderSets(mounted, apiB);
    await flushUntil(() => mounted?.container.textContent?.includes("web") ?? false);

    await act(async () => {
      listA.reject(new Error("stale failure"));
    });
    expect(mounted.container.querySelector("[role='alert']")).toBeNull();
    expect(mounted.container.textContent).toContain("web");
  });

  it("keeps a newly saved set when the stale pre-mutation list resolves late", async () => {
    const mountList = deferred<SavedTargetSet[]>();
    const reloadList = deferred<SavedTargetSet[]>();
    let listCalls = 0;
    const saved: SavedTargetSet = { id: "set-new", name: "cache", sessionIds: ["s1"], createdAt: 2, updatedAt: 2 };
    const api = fakeApi({
      list: vi.fn(() => {
        listCalls += 1;
        return listCalls === 1 ? mountList.promise : reloadList.promise;
      }),
      create: vi.fn().mockResolvedValue(saved),
    });
    mounted = mountSets(api, { selectedIds: ["s1"] });
    const input = mounted.container.querySelector<HTMLInputElement>("input[aria-label='目标集名称']");
    if (!input) throw new Error("name input missing");
    setInputValue(input, "cache");
    clickButton(mounted.container, "保存当前选择");
    await flushUntil(() => listCalls === 2);

    await act(async () => {
      reloadList.resolve([saved]);
    });
    expect(mounted.container.textContent).toContain("cache");

    await act(async () => {
      mountList.resolve([{ id: "set-1", name: "web", sessionIds: ["s1"], createdAt: 1, updatedAt: 1 }]);
    });
    expect(mounted.container.textContent).toContain("cache");
    expect(mounted.container.textContent).not.toContain("web");
  });
});
