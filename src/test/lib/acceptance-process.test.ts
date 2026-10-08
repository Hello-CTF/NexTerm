import { spawn, type ChildProcess } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import repoConfig from "../../../vite.config.ts";
import { acceptanceWatchIgnored } from "./acceptance-watch-ignores.mjs";
import { freePort, startVite } from "./acceptance-process.mjs";

const HELPER_URL = new URL("./acceptance-process.mjs", import.meta.url).href;
const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../..");

const FAKE_VITE = `import http from "node:http";
const port = Number(process.argv[process.argv.indexOf("--port") + 1]);
http.createServer((req, res) => { res.writeHead(200); res.end("ok"); }).listen(port, "127.0.0.1");
`;

const FAILING_VITE = `process.stderr.write("fake vite boom\\n");
process.exit(1);
`;

const SILENT_VITE = `import fs from "node:fs";
import net from "node:net";
const port = Number(process.argv[process.argv.indexOf("--port") + 1]);
const marker = process.env.NX_ACCEPTANCE_TEST_MARKER;
fs.writeFileSync(marker + ".pid", String(process.pid));
net.createServer(() => {}).listen(port, "127.0.0.1");
process.on("SIGTERM", () => {
  fs.writeFileSync(marker, "term");
  process.exit(0);
});
`;

const DRIVER = `import { startVite } from ${JSON.stringify(HELPER_URL)};
const mode = process.argv[2];
const vite = await startVite({ viteBin: process.argv[3], readyTimeout: 20000 });
process.stdout.write("READY " + vite.process.pid + "\\n", () => {
  if (mode === "exit") process.exit(0);
});
if (mode === "signal") setInterval(() => {}, 1000);
`;

let fixtureDir = "";
let fakeViteBin = "";
let failingViteBin = "";
let silentViteBin = "";
let driverBin = "";
const liveDrivers: ChildProcess[] = [];
const liveVites: { stop(): Promise<void> }[] = [];

beforeAll(() => {
  fixtureDir = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-acceptance-process-"));
  fakeViteBin = path.join(fixtureDir, "fake-vite.mjs");
  failingViteBin = path.join(fixtureDir, "failing-vite.mjs");
  silentViteBin = path.join(fixtureDir, "silent-vite.mjs");
  driverBin = path.join(fixtureDir, "driver.mjs");
  fs.writeFileSync(fakeViteBin, FAKE_VITE);
  fs.writeFileSync(failingViteBin, FAILING_VITE);
  fs.writeFileSync(silentViteBin, SILENT_VITE);
  fs.writeFileSync(driverBin, DRIVER);
});

afterAll(() => {
  fs.rmSync(fixtureDir, { recursive: true, force: true });
});

afterEach(async () => {
  for (const vite of liveVites.splice(0)) await vite.stop();
  for (const driver of liveDrivers.splice(0)) {
    if (driver.exitCode === null && driver.signalCode === null) driver.kill("SIGKILL");
  }
});

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

async function listenOn(port: number): Promise<net.Server> {
  const server = net.createServer();
  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(port, "127.0.0.1", () => resolve());
  });
  return server;
}

async function waitPidGone(pid: number, timeout = 10_000): Promise<void> {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    try {
      process.kill(pid, 0);
    } catch {
      return;
    }
    await sleep(100);
  }
  throw new Error(`process ${pid} still alive`);
}

function waitExit(child: ChildProcess, timeout = 10_000): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`child ${child.pid} did not exit`)), timeout);
    child.once("exit", () => {
      clearTimeout(timer);
      resolve();
    });
  });
}

async function spawnDriver(mode: "signal" | "exit"): Promise<{ driver: ChildProcess; vitePid: number }> {
  const driver = spawn(process.execPath, [driverBin, mode, fakeViteBin], { stdio: ["ignore", "pipe", "pipe"] });
  liveDrivers.push(driver);
  const vitePid = await new Promise<number>((resolve, reject) => {
    let buffer = "";
    const timer = setTimeout(() => reject(new Error(`driver ready timeout: ${buffer}`)), 20_000);
    driver.stdout!.on("data", (chunk) => {
      buffer += chunk;
      const match = buffer.match(/READY (\d+)/);
      if (match) {
        clearTimeout(timer);
        resolve(Number(match[1]));
      }
    });
    driver.once("exit", (code) => {
      clearTimeout(timer);
      reject(new Error(`driver exited before ready: ${code}; ${buffer}`));
    });
  });
  return { driver, vitePid };
}

describe("acceptance-process startVite", () => {
  it("starts vite.js through execPath and stops it on demand, freeing the port", async () => {
    const vite = await startVite({ viteBin: fakeViteBin, readyTimeout: 20_000 });
    liveVites.push(vite);
    expect(vite.process.spawnfile).toBe(process.execPath);
    expect((await fetch(vite.origin)).ok).toBe(true);
    const { port, process: child } = vite;
    await vite.stop();
    await waitPidGone(child.pid!);
    const server = await listenOn(port);
    server.close();
  }, 30_000);

  it("fails startup when the server exits and reports its stderr", async () => {
    await expect(startVite({ viteBin: failingViteBin, readyTimeout: 15_000 })).rejects.toThrow(
      /process exited 1[\s\S]*fake vite boom/,
    );
  }, 30_000);

  it("bounds each fetch by the remaining deadline and reaps the child when the server hangs", async () => {
    const marker = path.join(fixtureDir, "silent-term");
    process.env.NX_ACCEPTANCE_TEST_MARKER = marker;
    try {
      const started = Date.now();
      await expect(startVite({ viteBin: silentViteBin, readyTimeout: 2000 })).rejects.toThrow(/timed out waiting/);
      const elapsed = Date.now() - started;
      expect(elapsed).toBeGreaterThanOrEqual(1900);
      expect(elapsed).toBeLessThan(15_000);
      const deadline = Date.now() + 10_000;
      while (!fs.existsSync(marker) && Date.now() < deadline) await sleep(100);
      expect(fs.existsSync(marker)).toBe(true);
      await waitPidGone(Number(fs.readFileSync(`${marker}.pid`, "utf8")));
    } finally {
      delete process.env.NX_ACCEPTANCE_TEST_MARKER;
    }
  }, 30_000);

  it("rejects a busy port without adopting or killing the squatter", async () => {
    const port = await freePort();
    const squatter = await listenOn(port);
    try {
      await expect(startVite({ port, viteBin: fakeViteBin, readyTimeout: 15_000 })).rejects.toThrow(/already in use/);
      const socket = net.connect({ host: "127.0.0.1", port });
      await new Promise<void>((resolve, reject) => {
        socket.once("connect", () => resolve());
        socket.once("error", reject);
      });
      socket.destroy();
    } finally {
      squatter.close();
    }
  }, 30_000);

  it("keeps strict port ownership across concurrent starters", async () => {
    const first = await startVite({ viteBin: fakeViteBin, readyTimeout: 20_000 });
    liveVites.push(first);
    await expect(startVite({ port: first.port, viteBin: fakeViteBin, readyTimeout: 15_000 })).rejects.toThrow(
      /already in use/,
    );
    expect((await fetch(first.origin)).ok).toBe(true);
  }, 30_000);

  it("kills the owned vite when the parent receives SIGTERM", async () => {
    const { driver, vitePid } = await spawnDriver("signal");
    driver.kill("SIGTERM");
    await waitExit(driver);
    await waitPidGone(vitePid);
  }, 30_000);

  it("kills the owned vite when the parent exits", async () => {
    const { driver, vitePid } = await spawnDriver("exit");
    await waitExit(driver);
    await waitPidGone(vitePid);
  }, 30_000);
});

async function waitFor(predicate: () => boolean, timeout: number): Promise<void> {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (predicate()) return;
    await sleep(100);
  }
  throw new Error("waitFor timed out");
}

// M239: 产品 pnpm dev 直接吃仓库根 vite.config.ts, 必须与验收同一套有界行为。
// 结构断言钉住 optimizeDeps.entries 与 root 锚定 watch.ignored; 裸 "**/.tower/**"
// 规则通不过 startsWith(REPO_ROOT + "/") 断言 (M238 R1 回归形状)。
describe("repository dev vite config (pnpm dev)", () => {
  it("bounds dep scanning to the single HTML entry and anchors watch-ignores to the repo root", () => {
    expect(repoConfig.optimizeDeps?.entries).toEqual(["index.html"]);
    const ignored = repoConfig.server?.watch?.ignored as string[] | undefined;
    expect(ignored).toEqual(acceptanceWatchIgnored(REPO_ROOT));
    for (const pattern of ignored ?? []) {
      expect(pattern.startsWith(`${REPO_ROOT}/`)).toBe(true);
    }
  });

  // 真实 vite 二进制加载产品 config 的 watcher 行为: fake root 放在仓库 target 下
  // (向上自然解析仓库 node_modules, 免 symlink), 路径自带 .tower 祖先, 复刻 tower
  // worktree 场景。vite 打包 config 保留各模块真实 import.meta.url, 产品 config 的
  // 锚定因此落在仓库根 (结构断言已钉住该值); fake root 位于 target/ 下, 若直接
  // re-export 会把整棵 fake root 罩进仓库根的忽略区, 这里按产品 config 的同一
  // helper 重新锚定到 fake root。反事实: helper 若回退到裸 "**/.tower/**" 规则,
  // root 的 src 会被一并忽略, 下面 main.tsx 的 HMR 断言会失败。
  it("keeps source HMR alive under a .tower ancestor while ignoring the root's own artifact dirs", async () => {
    const regressDir = path.join(REPO_ROOT, "target", "m239-dev-config-regress");
    const root = path.join(regressDir, ".tower", "worktrees", "wt-fake");
    fs.mkdirSync(path.join(root, "src"), { recursive: true });
    fs.mkdirSync(path.join(root, ".tower"), { recursive: true });
    fs.writeFileSync(path.join(root, "index.html"), "<!doctype html><html><body></body></html>\n");
    fs.writeFileSync(path.join(root, "src", "main.tsx"), "export const probe = 1;\n");
    fs.writeFileSync(path.join(root, ".tower", "probe.ts"), "export const artifact = 1;\n");
    fs.writeFileSync(
      path.join(root, "vite.config.mjs"),
      `import base from ${JSON.stringify(path.join(REPO_ROOT, "vite.config.ts"))};\n` +
        `import { acceptanceWatchIgnored } from ${JSON.stringify(path.join(REPO_ROOT, "src", "test", "lib", "acceptance-watch-ignores.mjs"))};\n` +
        `export default { ...base, server: { ...base.server, watch: { ignored: acceptanceWatchIgnored(${JSON.stringify(root)}) } } };\n`,
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
      fs.rmSync(regressDir, { recursive: true, force: true });
    }
  }, 60_000);
});
