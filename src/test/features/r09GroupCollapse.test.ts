/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it } from "vitest";

import { PROJECT_GROUP_COLLAPSE_LIMIT } from "../../app/projectFocus";
import { useGroupCollapse } from "../../features/explorer/groupCollapse";

const ACCOUNT_A = "account-a";
const ACCOUNT_B = "account-b";
const storageKey = (accountId: string) => `nexterm:collapsed-groups-v1:${encodeURIComponent(accountId)}`;

beforeEach(() => {
  window.localStorage.clear();
  useGroupCollapse.getState().resetForTest();
  useGroupCollapse.getState().ensureAccount(ACCOUNT_A);
});

describe("per-account project-group collapse", () => {
  it("keeps collapsed groups per account and reloads them when switching back", () => {
    useGroupCollapse.getState().toggle("g1");
    expect(useGroupCollapse.getState().collapsedIds).toEqual(["g1"]);
    expect(window.localStorage.getItem(storageKey(ACCOUNT_A))).toContain("g1");

    useGroupCollapse.getState().ensureAccount(ACCOUNT_B);
    expect(useGroupCollapse.getState().collapsedIds).toEqual([]);
    expect(useGroupCollapse.getState().accountId).toBe(ACCOUNT_B);

    useGroupCollapse.getState().toggle("g2");
    useGroupCollapse.getState().ensureAccount(ACCOUNT_A);
    expect(useGroupCollapse.getState().collapsedIds).toEqual(["g1"]);
    useGroupCollapse.getState().ensureAccount(ACCOUNT_B);
    expect(useGroupCollapse.getState().collapsedIds).toEqual(["g2"]);
  });

  it("toggles a group off again within one account", () => {
    useGroupCollapse.getState().toggle("g1");
    useGroupCollapse.getState().toggle("g1");
    expect(useGroupCollapse.getState().collapsedIds).toEqual([]);
    expect(window.localStorage.getItem(storageKey(ACCOUNT_A))).not.toContain('"g1"');
  });

  it("bounds the collapsed list at the reviewed limit, evicting the oldest", () => {
    for (let index = 0; index < PROJECT_GROUP_COLLAPSE_LIMIT + 5; index += 1) {
      useGroupCollapse.getState().toggle(`g${index}`);
    }
    const collapsed = useGroupCollapse.getState().collapsedIds;
    expect(collapsed).toHaveLength(PROJECT_GROUP_COLLAPSE_LIMIT);
    expect(collapsed).not.toContain("g0");
    expect(collapsed[collapsed.length - 1]).toBe(`g${PROJECT_GROUP_COLLAPSE_LIMIT + 4}`);
  });

  it("degrades to an empty list for corrupt persisted state", () => {
    window.localStorage.setItem(storageKey(ACCOUNT_A), "{not json");
    useGroupCollapse.getState().resetForTest();

    useGroupCollapse.getState().ensureAccount(ACCOUNT_A);

    expect(useGroupCollapse.getState().collapsedIds).toEqual([]);
    expect(useGroupCollapse.getState().accountId).toBe(ACCOUNT_A);
  });

  it("drops non-string and duplicate persisted entries on load", () => {
    window.localStorage.setItem(
      storageKey(ACCOUNT_A),
      JSON.stringify({ v: 1, collapsed: ["g1", 7, "g1", "", "g2"] }),
    );
    useGroupCollapse.getState().resetForTest();

    useGroupCollapse.getState().ensureAccount(ACCOUNT_A);

    expect(useGroupCollapse.getState().collapsedIds).toEqual(["g1", "g2"]);
  });
});
