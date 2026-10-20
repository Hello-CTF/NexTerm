/** @vitest-environment jsdom */
import { describe, expect, it } from "vitest";

import {
  PROJECT_GROUP_COLLAPSE_LIMIT,
  isProjectGroupCollapsed,
  parseProjectGroupCollapse,
  sanitizeProjectGroupCollapse,
  serializeProjectGroupCollapse,
  toggleProjectGroupCollapse,
} from "../../app/projectFocus";

describe("project group collapse state", () => {
  it("toggles groups between collapsed and expanded with the most recent toggle last", () => {
    let collapsed = toggleProjectGroupCollapse([], "g-1");
    collapsed = toggleProjectGroupCollapse(collapsed, "g-2");
    expect(collapsed).toEqual(["g-1", "g-2"]);
    expect(isProjectGroupCollapsed(collapsed, "g-1")).toBe(true);
    expect(isProjectGroupCollapsed(collapsed, "g-3")).toBe(false);

    collapsed = toggleProjectGroupCollapse(collapsed, "g-1");
    expect(collapsed).toEqual(["g-2"]);
    expect(isProjectGroupCollapsed(collapsed, "g-1")).toBe(false);
  });

  it("never mutates the input and ignores empty group ids", () => {
    const input = ["g-1"];
    const toggled = toggleProjectGroupCollapse(input, "g-2");
    expect(toggled).not.toBe(input);
    expect(input).toEqual(["g-1"]);
    expect(toggleProjectGroupCollapse(input, "")).toEqual(input);
  });

  it("evicts the oldest collapsed groups beyond the bound", () => {
    let collapsed: string[] = [];
    for (let index = 0; index < PROJECT_GROUP_COLLAPSE_LIMIT; index += 1) {
      collapsed = toggleProjectGroupCollapse(collapsed, `g-${index}`);
    }
    expect(collapsed).toHaveLength(PROJECT_GROUP_COLLAPSE_LIMIT);
    collapsed = toggleProjectGroupCollapse(collapsed, `g-${PROJECT_GROUP_COLLAPSE_LIMIT}`);
    expect(collapsed).toHaveLength(PROJECT_GROUP_COLLAPSE_LIMIT);
    expect(collapsed[0]).toBe("g-1");
    expect(collapsed[PROJECT_GROUP_COLLAPSE_LIMIT - 1]).toBe(`g-${PROJECT_GROUP_COLLAPSE_LIMIT}`);
    expect(isProjectGroupCollapsed(collapsed, "g-0")).toBe(false);
  });

  it("re-toggling an existing group keeps the bound without growing the list", () => {
    let collapsed: string[] = [];
    for (let index = 0; index < PROJECT_GROUP_COLLAPSE_LIMIT; index += 1) {
      collapsed = toggleProjectGroupCollapse(collapsed, `g-${index}`);
    }
    const expanded = toggleProjectGroupCollapse(collapsed, "g-5");
    expect(expanded).toHaveLength(PROJECT_GROUP_COLLAPSE_LIMIT - 1);
    expect(isProjectGroupCollapsed(expanded, "g-5")).toBe(false);
  });
});

describe("project group collapse serialization", () => {
  it("sanitizes invalid input: non-arrays, non-strings, duplicates keep the latest occurrence", () => {
    expect(sanitizeProjectGroupCollapse(null)).toEqual([]);
    expect(sanitizeProjectGroupCollapse("g-1")).toEqual([]);
    expect(sanitizeProjectGroupCollapse([1, null, "", "g-1", "g-2", "g-1"])).toEqual(["g-2", "g-1"]);
  });

  it("caps sanitized state at the bound keeping the most recent toggles", () => {
    const oversize = Array.from({ length: PROJECT_GROUP_COLLAPSE_LIMIT + 3 }, (_, index) => `g-${index}`);
    const sanitized = sanitizeProjectGroupCollapse(oversize);
    expect(sanitized).toHaveLength(PROJECT_GROUP_COLLAPSE_LIMIT);
    expect(sanitized[0]).toBe("g-3");
    expect(sanitized[PROJECT_GROUP_COLLAPSE_LIMIT - 1]).toBe(`g-${PROJECT_GROUP_COLLAPSE_LIMIT + 2}`);
  });

  it("round-trips collapse state deterministically through serialize and parse", () => {
    const collapsed = ["g-1", "g-2"];
    const text = serializeProjectGroupCollapse(collapsed);
    expect(text).toBe(serializeProjectGroupCollapse(collapsed));
    expect(parseProjectGroupCollapse(text)).toEqual(collapsed);
    expect(parseProjectGroupCollapse(text)).toEqual(sanitizeProjectGroupCollapse(collapsed));
  });

  it("parses corrupt or foreign payloads to an empty state", () => {
    expect(parseProjectGroupCollapse("not json")).toEqual([]);
    expect(parseProjectGroupCollapse("[]")).toEqual([]);
    expect(parseProjectGroupCollapse('{"v":2,"collapsed":["g-1"]}')).toEqual([]);
    expect(parseProjectGroupCollapse('{"v":1,"collapsed":"g-1"}')).toEqual([]);
  });
});
