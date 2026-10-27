import { describe, expect, it } from "vitest";
import { pruneSpans, splitByClass } from "../../features/terminal/streamClasses";

describe("stream output classes", () => {
  it("splits bytes into runs by class span, live in between", () => {
    const spans = [
      { from: 110, to: 115, kind: "replay" },
      { from: 102, to: 105, kind: "answered" },
      { from: 115, to: 118, kind: "replay" },
    ];
    expect(splitByClass(100, 20, spans)).toEqual([
      { offset: 0, length: 2, mode: "live" },
      { offset: 2, length: 3, mode: "answered" },
      { offset: 5, length: 5, mode: "live" },
      { offset: 10, length: 8, mode: "replay" },
      { offset: 18, length: 2, mode: "live" },
    ]);
  });

  it("clips spans to the delivered range and treats unknown kinds as live", () => {
    expect(splitByClass(10, 4, [{ from: 0, to: 12, kind: "replay" }, { from: 12, to: 20, kind: "future" }])).toEqual([
      { offset: 0, length: 2, mode: "replay" },
      { offset: 2, length: 2, mode: "live" },
    ]);
    expect(splitByClass(10, 4, [])).toEqual([{ offset: 0, length: 4, mode: "live" }]);
  });

  it("prunes delivered spans", () => {
    const spans = [
      { from: 0, to: 5, kind: "replay" },
      { from: 5, to: 9, kind: "answered" },
    ];
    expect(pruneSpans(spans, 5)).toEqual([{ from: 5, to: 9, kind: "answered" }]);
    expect(pruneSpans(spans, 4)).toBe(spans);
  });
});
