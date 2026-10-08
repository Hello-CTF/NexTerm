import { describe, expect, it } from "vitest";
import { acceptanceWatchIgnored } from "./acceptance-watch-ignores.mjs";

// 锚定规则的匹配模型:去掉结尾的 /** 后做前缀匹配(裸规则为精确目录本身)。
function matchesAnchored(pattern: string, target: string): boolean {
  const prefix = pattern.endsWith("/**") ? pattern.slice(0, -3) : pattern;
  return target === prefix || target.startsWith(`${prefix}/`);
}

describe("acceptanceWatchIgnored", () => {
  it("锚定 resolved root:root 自带 .tower 祖先时 src 不被命中,root 自己的产物目录被命中", () => {
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
