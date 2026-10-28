import path from "node:path";
import { describe, expect, it } from "vitest";
import { normalizePath } from "vite";
import { acceptanceWatchIgnored } from "./acceptance-watch-ignores.mjs";

// Matching model for anchored rules: strip the trailing /** then prefix-match (a bare rule matches the exact directory itself).
function matchesAnchored(pattern: string, target: string): boolean {
  const prefix = pattern.endsWith("/**") ? pattern.slice(0, -3) : pattern;
  return target === prefix || target.startsWith(`${prefix}/`);
}

describe("acceptanceWatchIgnored", () => {
  it("anchored resolved root: src is not matched when root has a .tower ancestor, while the root's own artifact directories are matched", () => {
    // Resolve like the implementation does so the drive letter on Windows is
    // part of the expected root; the .tower ancestor survives resolution on
    // every platform, which is the behavior under test.
    const root = normalizePath(path.resolve("/fake/.tower/worktrees/wt-fake"));
    const patterns = acceptanceWatchIgnored(root);
    expect(patterns).toEqual([
      `${root}/.tower`,
      `${root}/.tower/**`,
      `${root}/.claude/worktrees`,
      `${root}/.claude/worktrees/**`,
      `${root}/target`,
      `${root}/target/**`,
      `${root}/target-alt`,
      `${root}/target-alt/**`,
      `${root}/.tmp`,
      `${root}/.tmp/**`,
    ]);
    for (const pattern of patterns) {
      expect(pattern.startsWith(`${root}/`)).toBe(true);
      expect(matchesAnchored(pattern, `${root}/src/main.tsx`)).toBe(false);
    }
    expect(patterns.some((p) => matchesAnchored(p, `${root}/.tower/worktrees/other/x.ts`))).toBe(true);
    expect(patterns.some((p) => matchesAnchored(p, `${root}/target/acceptance-x/report.json`))).toBe(true);
    expect(patterns.some((p) => matchesAnchored(p, `${root}/.claude/worktrees/agent-x/src/main.tsx`))).toBe(true);
    expect(patterns.some((p) => matchesAnchored(p, `${root}/.claude/settings.json`))).toBe(false);
  });
});
