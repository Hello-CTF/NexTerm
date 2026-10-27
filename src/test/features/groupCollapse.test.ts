/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it } from "vitest";

import { PROJECT_GROUP_COLLAPSE_LIMIT } from "../../app/projectFocus";
import { useGroupCollapse } from "../../features/explorer/groupCollapse";

const storageKey = (accountId: string) => `nexterm:collapsed-groups-v1:${encodeURIComponent(accountId)}`;

beforeEach(() => {
  window.localStorage.clear();
  useGroupCollapse.getState().resetForTest();
});

describe("per-account group collapse", () => {
  it("starts empty for an account without stored state", () => {
    useGroupCollapse.getState().ensureAccount("account-a");

    expect(useGroupCollapse.getState().collapsedIds).toEqual([]);
  });

  it("loads the stored list for the account only", () => {
    window.localStorage.setItem(storageKey("account-a"), JSON.stringify({ v: 1, collapsed: ["g9"] }));

    useGroupCollapse.getState().ensureAccount("account-a");
    expect(useGroupCollapse.getState().collapsedIds).toEqual(["g9"]);

    useGroupCollapse.getState().resetForTest();
    useGroupCollapse.getState().ensureAccount("account-b");
    expect(useGroupCollapse.getState().collapsedIds).toEqual([]);
  });

  it("degrades a corrupt stored value to an empty list", () => {
    window.localStorage.setItem(storageKey("account-a"), "not-json");

    useGroupCollapse.getState().ensureAccount("account-a");

    expect(useGroupCollapse.getState().collapsedIds).toEqual([]);
  });

  it("bounds the stored list at the reviewed limit", () => {
    const stored = Array.from({ length: PROJECT_GROUP_COLLAPSE_LIMIT + 5 }, (_, index) => `g${index}`);
    window.localStorage.setItem(storageKey("account-a"), JSON.stringify({ v: 1, collapsed: stored }));

    useGroupCollapse.getState().ensureAccount("account-a");

    expect(useGroupCollapse.getState().collapsedIds.length).toBeLessThanOrEqual(PROJECT_GROUP_COLLAPSE_LIMIT);
  });

  it("keeps reload and toggle working", () => {
    window.localStorage.setItem(storageKey("account-a"), JSON.stringify({ v: 1, collapsed: ["g1", "g2"] }));
    useGroupCollapse.getState().ensureAccount("account-a");

    useGroupCollapse.getState().toggle("g1");
    expect(useGroupCollapse.getState().collapsedIds).toEqual(["g2"]);

    useGroupCollapse.getState().resetForTest();
    useGroupCollapse.getState().ensureAccount("account-a");
    expect(useGroupCollapse.getState().collapsedIds).toEqual(["g2"]);

    useGroupCollapse.getState().toggle("g3");
    expect(JSON.parse(window.localStorage.getItem(storageKey("account-a")) ?? "null")).toEqual({
      v: 1,
      collapsed: ["g2", "g3"],
    });
  });
});
