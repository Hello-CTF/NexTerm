/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

import { PROJECT_GROUP_COLLAPSE_LIMIT } from "../../app/projectFocus";
import { useGroupCollapse } from "../../features/explorer/groupCollapse";

const LEGACY_KEY = "nexterm.collapsedAssetGroups.v1";
const storageKey = (accountId: string) => `nexterm:collapsed-groups-v1:${encodeURIComponent(accountId)}`;

beforeEach(() => {
  window.localStorage.clear();
  useGroupCollapse.getState().resetForTest();
});

describe("legacy global collapse migration", () => {
  it("migrates valid legacy ids to the first signed-in account and removes the legacy key", () => {
    window.localStorage.setItem(LEGACY_KEY, JSON.stringify(["g1", "g2"]));

    useGroupCollapse.getState().ensureAccount("account-a");

    expect(useGroupCollapse.getState().collapsedIds).toEqual(["g1", "g2"]);
    expect(window.localStorage.getItem(LEGACY_KEY)).toBeNull();
    expect(JSON.parse(window.localStorage.getItem(storageKey("account-a")) ?? "null")).toEqual({
      v: 1,
      collapsed: ["g1", "g2"],
    });
  });

  it("does not resurrect the legacy list for a second account", () => {
    window.localStorage.setItem(LEGACY_KEY, JSON.stringify(["g1"]));

    useGroupCollapse.getState().ensureAccount("account-a");
    useGroupCollapse.getState().resetForTest();
    useGroupCollapse.getState().ensureAccount("account-b");

    expect(useGroupCollapse.getState().collapsedIds).toEqual([]);
    expect(window.localStorage.getItem(LEGACY_KEY)).toBeNull();
  });

  it("keeps the legacy mixed-item semantics: non-string entries are dropped", () => {
    window.localStorage.setItem(LEGACY_KEY, JSON.stringify([1, "g2", null, "g2"]));

    useGroupCollapse.getState().ensureAccount("account-a");

    expect(useGroupCollapse.getState().collapsedIds).toEqual(["g2"]);
  });

  it("keeps the legacy malformed-payload semantics: degrades to an empty list and is consumed", () => {
    window.localStorage.setItem(LEGACY_KEY, "not-json");

    useGroupCollapse.getState().ensureAccount("account-a");

    expect(useGroupCollapse.getState().collapsedIds).toEqual([]);
    expect(window.localStorage.getItem(LEGACY_KEY)).toBeNull();
    useGroupCollapse.getState().resetForTest();
    useGroupCollapse.getState().ensureAccount("account-b");
    expect(useGroupCollapse.getState().collapsedIds).toEqual([]);
  });

  it("bounds the migrated legacy list at the reviewed limit", () => {
    const legacy = Array.from({ length: PROJECT_GROUP_COLLAPSE_LIMIT + 5 }, (_, index) => `g${index}`);
    window.localStorage.setItem(LEGACY_KEY, JSON.stringify(legacy));

    useGroupCollapse.getState().ensureAccount("account-a");

    const collapsed = useGroupCollapse.getState().collapsedIds;
    expect(collapsed).toHaveLength(PROJECT_GROUP_COLLAPSE_LIMIT);
    expect(collapsed).not.toContain("g0");
    expect(collapsed[collapsed.length - 1]).toBe(`g${PROJECT_GROUP_COLLAPSE_LIMIT + 4}`);
  });

  it("prefers the per-account key over the legacy list and leaves the legacy key for the account that still needs it", () => {
    window.localStorage.setItem(storageKey("account-a"), JSON.stringify({ v: 1, collapsed: ["g9"] }));
    window.localStorage.setItem(LEGACY_KEY, JSON.stringify(["g1"]));

    useGroupCollapse.getState().ensureAccount("account-a");

    expect(useGroupCollapse.getState().collapsedIds).toEqual(["g9"]);
    expect(window.localStorage.getItem(LEGACY_KEY)).not.toBeNull();

    useGroupCollapse.getState().resetForTest();
    useGroupCollapse.getState().ensureAccount("account-b");
    expect(useGroupCollapse.getState().collapsedIds).toEqual(["g1"]);
    expect(window.localStorage.getItem(LEGACY_KEY)).toBeNull();
  });

  it("keeps the legacy key when the per-account write fails and migrates on a later success", () => {
    window.localStorage.setItem(LEGACY_KEY, JSON.stringify(["g1"]));
    const setItem = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("quota exceeded", "QuotaExceededError");
    });

    useGroupCollapse.getState().ensureAccount("account-a");

    // The in-memory state is still usable this session, but the migration is not
    // consumed: the legacy key stays and no per-account value is claimed on disk.
    expect(useGroupCollapse.getState().collapsedIds).toEqual(["g1"]);
    expect(window.localStorage.getItem(LEGACY_KEY)).not.toBeNull();
    expect(window.localStorage.getItem(storageKey("account-a"))).toBeNull();

    setItem.mockRestore();
    useGroupCollapse.getState().resetForTest();
    useGroupCollapse.getState().ensureAccount("account-a");

    expect(JSON.parse(window.localStorage.getItem(storageKey("account-a")) ?? "null")).toEqual({
      v: 1,
      collapsed: ["g1"],
    });
    expect(window.localStorage.getItem(LEGACY_KEY)).toBeNull();
  });

  it("keeps reload and toggle working on the migrated per-account state", () => {
    window.localStorage.setItem(LEGACY_KEY, JSON.stringify(["g1", "g2"]));
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
