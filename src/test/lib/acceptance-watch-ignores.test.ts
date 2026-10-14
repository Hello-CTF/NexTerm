import { describe, expect, it } from "vitest";
import { acceptanceWatchIgnored } from "./acceptance-watch-ignores.mjs";

// Matching model for anchored rules: strip the trailing /** then prefix-match (a bare rule matches the exact directory itself).
function matchesAnchored(pattern: string, target: string): boolean {
  const prefix = pattern.endsWith("/**") ? pattern.slice(0, -3) : pattern;
  return target === prefix || target.startsWith(`${prefix}/`);
}

describe("acceptanceWatchIgnored", () => {
  it("anchored resolved root: src is not matched when root has a .tower ancestor, while the root's own artifact directories are matched", () => {
    const root = "/fake/.tower/worktrees/wt-fake";
    const patterns = acceptanceWatchIgnored(root);
    expect(patterns).toEqual([
      `${root}/.tower`,
      `${root}/.tower/**`,
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
  });
});
