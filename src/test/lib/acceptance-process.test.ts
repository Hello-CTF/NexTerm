import { spawn, type ChildProcess } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import { freePort, startVite } from "./acceptance-process.mjs";

const HELPER_URL = new URL("./acceptance-process.mjs", import.meta.url).href;

const FAKE_VITE = `import http from "node:http";
const port = Number(process.argv[process.argv.indexOf("--port") + 1]);
http.createServer((req, res) => { res.writeHead(200); res.end("ok"); }).listen(port, "127.0.0.1");
`;

const FAILING_VITE = `process.stderr.write("fake vite boom\\n");
process.exit(1);
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
let driverBin = "";
const liveDrivers: ChildProcess[] = [];
const liveVites: { stop(): Promise<void> }[] = [];

beforeAll(() => {
  fixtureDir = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-acceptance-process-"));
  fakeViteBin = path.join(fixtureDir, "fake-vite.mjs");
  failingViteBin = path.join(fixtureDir, "failing-vite.mjs");
  driverBin = path.join(fixtureDir, "driver.mjs");
  fs.writeFileSync(fakeViteBin, FAKE_VITE);
  fs.writeFileSync(failingViteBin, FAILING_VITE);
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
