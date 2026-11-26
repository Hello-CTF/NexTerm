import { describe, expect, it } from "vitest";
import { diffLineText, hasVisibleChange, simpleDiff } from "../features/ai/diff";

const text = (lines: string[]) => lines.join("\n") + "\n";
const render = (before: string, after: string) => simpleDiff(before, after).map(diffLineText);

describe("simpleDiff", () => {
  it("shows two separate edits as two hunks instead of one replaced block", () => {
    const before = Array.from({ length: 20 }, (_, i) => `line ${i}`);
    const after = [...before];
    after[2] = "changed 2";
    after[17] = "changed 17";
    expect(render(text(before), text(after))).toEqual([
      "  line 0",
      "  line 1",
      "- line 2",
      "+ changed 2",
      "  line 3",
      "  line 4",
      "  …",
      "  line 15",
      "  line 16",
      "- line 17",
      "+ changed 17",
      "  line 18",
      "  line 19",
    ]);
  });

  it("keeps a moved-around insertion minimal", () => {
    expect(render(text(["a", "b", "c"]), text(["a", "x", "b", "c", "y"]))).toEqual(["  a", "+ x", "  b", "  c", "+ y"]);
  });

  it("reports no visible change for identical text and for a trailing newline only", () => {
    expect(hasVisibleChange(simpleDiff("a\nb\n", "a\nb\n"))).toBe(false);
    expect(hasVisibleChange(simpleDiff("a\nb", "a\nb\n"))).toBe(false);
  });

  it("handles creation and deletion of a whole file", () => {
    expect(render("", text(["a", "b"]))).toEqual(["+ a", "+ b"]);
    expect(render(text(["a", "b"]), "")).toEqual(["- a", "- b"]);
  });

  it("falls back to one replaced block when the edit is too large to search", () => {
    const before = Array.from({ length: 3000 }, (_, i) => `old ${i}`);
    const after = Array.from({ length: 3000 }, (_, i) => `new ${i}`);
    const lines = simpleDiff(text(before), text(after));
    expect(lines.filter((l) => l.kind === "del")).toHaveLength(3000);
    expect(lines.filter((l) => l.kind === "add")).toHaveLength(3000);
    expect(lines.findIndex((l) => l.kind === "add")).toBe(3000);
  });
});

describe("myers edit script", () => {
  const lcs = (a: string[], b: string[]) => {
    const dp = Array.from({ length: a.length + 1 }, () => new Array<number>(b.length + 1).fill(0));
    for (let i = a.length - 1; i >= 0; i--)
      for (let j = b.length - 1; j >= 0; j--) dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
    return dp[0][0];
  };

  it("rebuilds both sides and keeps the longest common subsequence", async () => {
    const { myers } = await import("../features/ai/diff");
    let seed = 7;
    const next = () => (seed = (seed * 1103515245 + 12345) % 2147483648) / 2147483648;
    for (let round = 0; round < 500; round++) {
      const a = Array.from({ length: Math.floor(next() * 12) }, () => "abc"[Math.floor(next() * 3)]);
      const b = Array.from({ length: Math.floor(next() * 12) }, () => "abc"[Math.floor(next() * 3)]);
      const ops = myers(a, b)!;
      expect(ops.filter((o) => o.kind !== "add").map((o) => o.text)).toEqual(a);
      expect(ops.filter((o) => o.kind !== "del").map((o) => o.text)).toEqual(b);
      expect(ops.filter((o) => o.kind === "same")).toHaveLength(lcs(a, b));
    }
  });
});
