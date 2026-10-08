import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { acceptanceWatchIgnored } from "./acceptance-watch-ignores.mjs";
import acceptanceConfig from "./acceptance-vite.config.mjs";
import { startVite } from "./acceptance-process.mjs";

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../..");

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

async function waitFor(predicate: () => boolean, timeout: number): Promise<void> {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (predicate()) return;
    await sleep(100);
  }
  throw new Error("waitFor timed out");
}

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

  it("shipped acceptance config 使用锚定规则的 helper 输出", () => {
    expect(acceptanceConfig.optimizeDeps.entries).toEqual(["index.html"]);
    expect(acceptanceConfig.server.watch.ignored).toEqual(acceptanceWatchIgnored(REPO_ROOT));
  });

  it("真实 vite watcher:.tower 祖先下的 root 仍 watch src,root 自己的 .tower 被忽略", async () => {
    const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-watch-regress-"));
    const root = path.join(tmp, ".tower", "worktrees", "wt-fake");
    fs.mkdirSync(path.join(root, "src"), { recursive: true });
    fs.mkdirSync(path.join(root, ".tower"), { recursive: true });
    fs.writeFileSync(path.join(root, "index.html"), "<!doctype html><html><body></body></html>\n");
    fs.writeFileSync(path.join(root, "src", "main.tsx"), "export const probe = 1;\n");
    fs.writeFileSync(path.join(root, ".tower", "probe.ts"), "export const artifact = 1;\n");
    fs.writeFileSync(
      path.join(root, "vite.config.mjs"),
      `export default { optimizeDeps: { entries: [] }, server: { watch: { ignored: ${JSON.stringify(
        acceptanceWatchIgnored(root),
      )} } } };\n`,
    );
    const vite = await startVite({
      root,
      config: path.join(root, "vite.config.mjs"),
      viteBin: path.join(REPO_ROOT, "node_modules", "vite", "bin", "vite.js"),
    });
    try {
      const messages: string[] = [];
      const ws = new WebSocket(`ws://127.0.0.1:${vite.port}/`, "vite-hmr");
      ws.addEventListener("message", (event) => messages.push(String(event.data)));
      await waitFor(() => messages.length > 0, 10_000);

      const mainRes = await fetch(`${vite.origin}/src/main.tsx`);
      expect(mainRes.status).toBe(200);
      fs.writeFileSync(path.join(root, "src", "main.tsx"), "export const probe = 2;\n");
      await waitFor(() => messages.some((m) => m.includes("main.tsx") || m.includes("full-reload")), 10_000);

      const artifactRes = await fetch(`${vite.origin}/.tower/probe.ts`);
      expect(artifactRes.status).toBe(200);
      const before = messages.length;
      fs.writeFileSync(path.join(root, ".tower", "probe.ts"), "export const artifact = 2;\n");
      await sleep(4000);
      const fresh = messages.slice(before);
      expect(fresh.some((m) => m.includes("probe.ts") || m.includes("full-reload"))).toBe(false);
      ws.close();
    } finally {
      await vite.stop();
      fs.rmSync(tmp, { recursive: true, force: true });
    }
  }, 60_000);
});
