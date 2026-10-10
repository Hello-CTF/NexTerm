/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const KEY = "nexterm.collapsedAssetGroups.v1";

async function loadStore() {
  vi.resetModules();
  return import("../../features/explorer/groupCollapse");
}

beforeEach(() => {
  localStorage.clear();
});

describe("group collapse persistence", () => {
  it("starts expanded and toggles into localStorage", async () => {
    const { useGroupCollapse } = await loadStore();
    expect(useGroupCollapse.getState().collapsedIds).toEqual([]);

    useGroupCollapse.getState().toggle("g1");
    expect(useGroupCollapse.getState().collapsedIds).toEqual(["g1"]);
    expect(JSON.parse(localStorage.getItem(KEY) ?? "null")).toEqual(["g1"]);

    useGroupCollapse.getState().toggle("g1");
    expect(useGroupCollapse.getState().collapsedIds).toEqual([]);
    expect(JSON.parse(localStorage.getItem(KEY) ?? "null")).toEqual([]);
  });

  it("restores collapsed ids from localStorage", async () => {
    localStorage.setItem(KEY, JSON.stringify(["g9"]));
    const { useGroupCollapse } = await loadStore();
    expect(useGroupCollapse.getState().collapsedIds).toEqual(["g9"]);
  });

  it("ignores malformed stored values", async () => {
    localStorage.setItem(KEY, "not-json");
    const { useGroupCollapse } = await loadStore();
    expect(useGroupCollapse.getState().collapsedIds).toEqual([]);

    localStorage.setItem(KEY, JSON.stringify([1, "g2", null]));
    const again = await loadStore();
    expect(again.useGroupCollapse.getState().collapsedIds).toEqual(["g2"]);
  });
});
