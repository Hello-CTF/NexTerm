import { describe, expect, it } from "vitest";
import {
  compileLogFilters,
  exportMergedLines,
  filterMergedLines,
  highlightSegments,
  MergedLogBuffer,
  type MergedLogLine,
} from "../../features/logs/mergedLog";

function line(sequence: number, text: string, host = "host-a"): MergedLogLine {
  return {
    sequence,
    sourceId: host,
    sessionId: host,
    host,
    text,
    stderr: false,
    timeMs: sequence,
  };
}

describe("MergedLogBuffer", () => {
  it("sorts the merged stream by sequence and exports host prefixes", () => {
    const buffer = new MergedLogBuffer(3);
    buffer.append(line(2, "second", "host-b"));
    buffer.append(line(1, "first", "host-a"));
    buffer.append(line(3, "third", "host-b"));
    buffer.append(line(3, "duplicate", "host-b"));

    expect(buffer.visibleLines().map((entry) => entry.sequence)).toEqual([1, 2, 3]);
    expect(exportMergedLines(buffer.retainedLines())).toBe(
      "[host-a] first\n[host-b] second\n[host-b] third\n",
    );
  });

  it("drops the oldest lines after sorting and accumulates the drop count", () => {
    const buffer = new MergedLogBuffer(2);
    buffer.append(line(1, "one"));
    buffer.append(line(3, "three"));
    buffer.append(line(2, "two"));
    expect(buffer.visibleLines().map((entry) => entry.sequence)).toEqual([2, 3]);
    expect(buffer.droppedCount).toBe(1);

    buffer.append(line(4, "four"));
    expect(buffer.visibleLines().map((entry) => entry.sequence)).toEqual([3, 4]);
    expect(buffer.droppedCount).toBe(2);
  });

  it("freezes the visible snapshot while retaining input during pause", () => {
    const buffer = new MergedLogBuffer(2);
    buffer.append(line(1, "before"));
    buffer.setPaused(true);
    buffer.append(line(2, "during"));

    expect(buffer.visibleLines().map((entry) => entry.text)).toEqual(["before"]);
    expect(buffer.retainedLines().map((entry) => entry.text)).toEqual(["before", "during"]);

    buffer.setPaused(false);
    expect(buffer.visibleLines().map((entry) => entry.text)).toEqual(["before", "during"]);
    buffer.append(line(3, "after"));
    expect(buffer.visibleLines().map((entry) => entry.text)).toEqual(["during", "after"]);
    expect(buffer.droppedCount).toBe(1);
  });
});

describe("merged log regex filters and highlighting", () => {
  const lines = [
    line(1, "ERROR database"),
    line(2, "debug ERROR"),
    line(3, "INFO ready"),
    line(4, "WARN disk"),
  ];

  it("applies include and exclude regexes and highlights every include match", () => {
    const filters = compileLogFilters("ERROR|WARN", "debug");
    expect(filterMergedLines(lines, filters).map((entry) => entry.text)).toEqual([
      "ERROR database",
      "WARN disk",
    ]);
    expect(highlightSegments("ERROR then WARN", filters.highlight)).toEqual([
      { text: "ERROR", matched: true },
      { text: " then ", matched: false },
      { text: "WARN", matched: true },
    ]);
  });

  it("handles zero-width expressions without looping and makes invalid regex visible", () => {
    const zeroWidth = compileLogFilters("(?:)", "");
    expect(highlightSegments("abc", zeroWidth.highlight).map((segment) => segment.text).join("")).toBe("abc");

    const invalid = compileLogFilters("[", "");
    expect(invalid.error).toBeTruthy();
    expect(filterMergedLines(lines, invalid)).toHaveLength(lines.length);
  });
});
