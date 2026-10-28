import path from "node:path";
import { normalizePath } from "vite";

const ARTIFACT_DIRS = [".tower", ".claude/worktrees", "target", "target-alt", ".tmp"];

// Rules must be anchored to the resolved root itself: a bare "**/.tower/**" would also ignore
// <root>/src/main.tsx when the root itself has a .tower ancestor (tower worktree), silently cutting off source watching.
export function acceptanceWatchIgnored(root) {
  const anchored = normalizePath(path.resolve(root));
  return ARTIFACT_DIRS.flatMap((dir) => [`${anchored}/${dir}`, `${anchored}/${dir}/**`]);
}
