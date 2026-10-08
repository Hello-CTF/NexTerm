import path from "node:path";
import { normalizePath } from "vite";

const ARTIFACT_DIRS = [".tower", "target", "target-alt", ".tmp"];

// 规则必须锚定 resolved root 本身:裸 "**/.tower/**" 在 root 自带 .tower 祖先时
// (tower worktree)会把 <root>/src/main.tsx 也判成忽略,源码监听被静默掐断。
export function acceptanceWatchIgnored(root) {
  const anchored = normalizePath(path.resolve(root));
  return ARTIFACT_DIRS.flatMap((dir) => [`${anchored}/${dir}`, `${anchored}/${dir}/**`]);
}
