import { describe, expect, it } from "vitest";
import {
  applyAiEvent,
  beginRun,
  createConversation,
  safeTokenCount,
} from "../../features/ai/conversation";

describe("safeTokenCount", () => {
  it("clamps to non-negative safe integers", () => {
    expect(safeTokenCount(0)).toBe(0);
    expect(safeTokenCount(42)).toBe(42);
    expect(safeTokenCount(4.9)).toBe(4);
    expect(safeTokenCount(-3)).toBe(0);
    expect(safeTokenCount("12")).toBe(12);
    expect(safeTokenCount("abc")).toBe(0);
    expect(safeTokenCount(Number.NaN)).toBe(0);
    expect(safeTokenCount(Number.POSITIVE_INFINITY)).toBe(0);
    expect(safeTokenCount(undefined)).toBe(0);
    expect(safeTokenCount(null)).toBe(0);
  });

  it("clamps values beyond the safe-integer range", () => {
    expect(safeTokenCount(Number.MAX_SAFE_INTEGER)).toBe(Number.MAX_SAFE_INTEGER);
    expect(safeTokenCount(Number.MAX_SAFE_INTEGER + 1)).toBe(Number.MAX_SAFE_INTEGER);
    expect(safeTokenCount(1e30)).toBe(Number.MAX_SAFE_INTEGER);
  });
});

describe("usage event clamping", () => {
  it("stores clamped usage values on the conversation", () => {
    let state = beginRun(createConversation(), 1);
    state = applyAiEvent(state, 1, {
      type: "usage",
      promptTokens: 1e30,
      completionTokens: -7,
      cachedTokens: "9",
      contextWindow: Number.POSITIVE_INFINITY,
    }).state;
    expect(state.usage).toEqual({
      promptTokens: Number.MAX_SAFE_INTEGER,
      completionTokens: 0,
      cachedTokens: 9,
      contextWindow: 0,
    });
  });
});
