/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { commandRecencyScores, recordCommandUse } from "../../app/jump/commandRecency";

const DAY = 24 * 60 * 60 * 1000;

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("command recency", () => {
  it("scores unused commands 0 and recent, frequent use higher", () => {
    const now = 100 * DAY;
    recordCommandUse("old", now - 30 * DAY);
    recordCommandUse("recent", now - DAY);
    recordCommandUse("frequent", now - DAY);
    recordCommandUse("frequent", now - DAY);
    const score = commandRecencyScores(now);
    expect(score("never")).toBe(0);
    expect(score("old")).toBeGreaterThan(0);
    expect(score("recent")).toBeGreaterThan(score("old"));
    expect(score("frequent")).toBeGreaterThan(score("recent"));
  });

  it("ignores malformed storage and survives storage that throws", () => {
    localStorage.setItem("nexterm.commandRecency.v1", "{not json");
    expect(commandRecencyScores()("x")).toBe(0);
    localStorage.setItem("nexterm.commandRecency.v1", JSON.stringify({ x: { count: "1", at: 1 }, y: [1] }));
    expect(commandRecencyScores()("x")).toBe(0);

    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    expect(() => recordCommandUse("x")).not.toThrow();
    expect(commandRecencyScores()("x")).toBe(0);
  });

  it("keeps a bounded number of the most recent commands", () => {
    for (let index = 0; index < 205; index += 1) recordCommandUse(`c${index}`, index);
    const score = commandRecencyScores(1000);
    expect(score("c0")).toBe(0);
    expect(score("c204")).toBeGreaterThan(0);
  });
});
