import { describe, expect, it } from "vitest";
import type { ExecBatchResult } from "../../ipc/execBatchApi";
import {
  analyzeDriftResults,
  driftDiffLineText,
  driftFileCommand,
  driftTextDiff,
} from "../../features/execbatch/drift";

function result(sessionId: string, stdout: string, patch: Partial<ExecBatchResult> = {}): ExecBatchResult {
  return {
    sessionId,
    sessionName: sessionId,
    status: "success",
    stdout,
    stderr: "",
    exitCode: 0,
    durationMs: 1,
    truncated: false,
    ...patch,
  };
}

describe("drift output analysis", () => {
  it("groups identical content including empty output and keeps failures outside groups", async () => {
    const analysis = await analyzeDriftResults([
      result("a", ""),
      result("b", ""),
      result("c", "same\n"),
      result("d", "same\n"),
      result("e", "different\n"),
      result("failed", "", { status: "transport_error", error: "offline", exitCode: null }),
    ]);
    expect(analysis.groups).toHaveLength(3);
    expect(analysis.groups[0]).toMatchObject({ content: "", truncated: false, binary: false });
    expect(analysis.groups[0].results.map((item) => item.sessionId)).toEqual(["a", "b"]);
    expect(analysis.groups[1].results.map((item) => item.sessionId)).toEqual(["c", "d"]);
    expect(analysis.groups[0].hash).toMatch(/^[0-9a-f]{64}$/);
    expect(new Set(analysis.groups.map((group) => group.hash)).size).toBe(3);
    expect(analysis.failures.map((item) => item.sessionId)).toEqual(["failed"]);
  });

  it("does not claim full equality from the same truncated prefix, including large output", async () => {
    const large = "x".repeat(70_000);
    const analysis = await analyzeDriftResults([
      result("complete", large),
      result("partial-a", large, { truncated: true }),
      result("partial-b", large, { truncated: true }),
    ]);
    expect(analysis.groups).toHaveLength(2);
    expect(analysis.groups[0].truncated).toBe(false);
    expect(analysis.groups[1]).toMatchObject({ truncated: true, content: large });
    expect(analysis.groups[1].results.map((item) => item.sessionId)).toEqual(["partial-a", "partial-b"]);
    expect(analysis.groups[0].id).not.toBe(analysis.groups[1].id);
  });

  it("marks binary output for grouping without treating it as text", async () => {
    const analysis = await analyzeDriftResults([
      result("a", "header\0payload"),
      result("b", "header\0payload"),
      result("c", "plain text"),
    ]);
    expect(analysis.groups[0].binary).toBe(true);
    expect(analysis.groups[0].results).toHaveLength(2);
    expect(analysis.groups[1].binary).toBe(false);
  });
});

describe("drift diff and file command", () => {
  it("handles empty, added, removed and changed lines", () => {
    expect(driftTextDiff("", "")).toEqual([]);
    expect(driftTextDiff("", "new").map(driftDiffLineText)).toEqual(["+ new [末行无换行]"]);
    expect(driftTextDiff("old", "").map(driftDiffLineText)).toEqual(["- old [末行无换行]"]);
    expect(driftTextDiff("same\nold\n", "same\nnew\n").map(driftDiffLineText)).toEqual([
      "  same",
      "- old",
      "+ new",
    ]);
  });

  it("keeps a final-newline-only drift visible", () => {
    expect(driftTextDiff("value", "value\n").map(driftDiffLineText)).toEqual([
      "- value [末行无换行]",
      "+ value",
    ]);
    expect(driftTextDiff("value\n", "value").map(driftDiffLineText)).toEqual([
      "- value",
      "+ value [末行无换行]",
    ]);
  });

  it("quotes file paths instead of evaluating them", () => {
    expect(driftFileCommand("/etc/a file")).toBe("cat < '/etc/a file'");
    expect(driftFileCommand("~/odd'$(touch pwned)")).toBe(`cat < "$HOME"/'odd'"'"'$(touch pwned)'`);
    expect(() => driftFileCommand("/tmp/a\nid")).toThrow();
    expect(() => driftFileCommand(" ")).toThrow();
  });
});
