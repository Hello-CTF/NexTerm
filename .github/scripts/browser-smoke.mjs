#!/usr/bin/env node
// 真实 Chromium 冒烟 (task smoke:browser): 不依赖演示模式 —— 构建真实前端 dist,
// 以 --auth=loopback 启动真实 nexterm-server 服务 SPA, 用 CDP 直连系统 Chrome 验收
// jsdom 无法覆盖的最小行为集: 真实浏览器启动进工作台、纯键盘 Tab 焦点导航、
// /ws/events 真实帧(经 /rpc layout_put 触发 layout://changed)、真实服务端 reload 恢复。
import { spawn, spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const OUT = path.join(ROOT, "target/browser-smoke");
const results = new Map();

fs.mkdirSync(OUT, { recursive: true });

function record(id, status, detail = {}) {
  results.set(id, { id, status, ...detail });
  console.warn(`${status === "passed" ? "PASS" : "FAIL"} ${id}`);
}

async function pass(id, fn) {
  try {
    const detail = (await fn()) || {};
    record(id, "passed", typeof detail === "object" ? detail : { detail });
  } catch (error) {
    record(id, "failed", { error: String(error?.stack || error) });
  }
}

function freePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address();
      server.close(() => resolve(port));
    });
  });
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function waitHttp(url, process, timeout = 30_000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (process?.exitCode !== null && process?.exitCode !== undefined) throw new Error(`${url}: process exited ${process.exitCode}`);
    try {
      const response = await fetch(url);
      if (response.ok) return response;
    } catch {}
    await sleep(100);
  }
  throw new Error(`timed out waiting for ${url}`);
}

async function stop(process) {
  if (!process || process.exitCode !== null) return;
  process.kill("SIGTERM");
  const exited = await Promise.race([
    new Promise((resolve) => process.once("exit", () => resolve(true))),
    new Promise((resolve) => setTimeout(() => resolve(false), 5000)),
  ]);
  if (!exited) {
    process.kill("SIGKILL");
    await Promise.race([
      new Promise((resolve) => process.once("exit", () => resolve(true))),
      new Promise((resolve) => setTimeout(() => resolve(false), 2000)),
    ]);
  }
}

function chromeExecutable() {
  const candidates = [
    process.env.CHROME_PATH,
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
    "/usr/bin/google-chrome",
    "google-chrome",
    "chromium",
    "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe",
  ].filter(Boolean);
  for (const candidate of candidates) {
    const result = spawnSync(candidate, ["--version"], { stdio: "pipe" });
    if (result.status === 0) return candidate;
  }
  throw new Error("no real Chrome/Chromium executable found; set CHROME_PATH (this is a failure, not a skip)");
}

class CDP {
  constructor(socket) {
    this.socket = socket;
    this.seq = 0;
    this.pending = new Map();
    socket.addEventListener("message", (event) => {
      const message = JSON.parse(event.data);
      if (message.id && this.pending.has(message.id)) {
        const { resolve, reject } = this.pending.get(message.id);
        this.pending.delete(message.id);
        if (message.error) reject(new Error(`${message.error.message} (${message.error.code})`));
        else resolve(message.result);
      }
    });
    socket.addEventListener("close", () => {
      for (const pending of this.pending.values()) pending.reject(new Error("CDP socket closed"));
      this.pending.clear();
    });
  }

  static connect(url) {
    const socket = new WebSocket(url);
    return new Promise((resolve, reject) => {
      socket.addEventListener("open", () => resolve(new CDP(socket)), { once: true });
      socket.addEventListener("error", () => reject(new Error(`cannot connect CDP: ${url}`)), { once: true });
    });
  }

  send(method, params = {}) {
    const id = ++this.seq;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`CDP send timeout: ${method} ${JSON.stringify(params).slice(0, 120)}`));
      }, 30_000);
      this.pending.set(id, {
        resolve: (value) => {
          clearTimeout(timer);
          resolve(value);
        },
        reject: (error) => {
          clearTimeout(timer);
          reject(error);
        },
      });
      this.socket.send(JSON.stringify({ id, method, params }));
    });
  }

  async evaluate(expression) {
    const result = await this.send("Runtime.evaluate", { expression, returnByValue: true });
    if (result.exceptionDetails) throw new Error(`page evaluation failed: ${JSON.stringify(exceptionDetailsText(result)).slice(0, 300)}`);
    return result.result?.value;
  }

  close() {
    this.socket.close();
  }
}

function exceptionDetailsText(result) {
  return result.exceptionDetails?.exception?.description ?? result.exceptionDetails?.text ?? result.exceptionDetails;
}

async function startChrome() {
  const port = await freePort();
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-smoke-chrome-"));
  const args = [
    "--headless=new", "--no-first-run", "--disable-dev-shm-usage", "--disable-background-networking",
    `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`, "--window-size=1280,900", "about:blank",
  ];
  if (process.platform === "linux") args.unshift("--no-sandbox");
  const child = spawn(chromeExecutable(), args, { stdio: ["ignore", "pipe", "pipe"] });
  let stderr = "";
  child.stderr.on("data", (chunk) => { stderr += chunk; });
  try {
    const response = await waitHttp(`http://127.0.0.1:${port}/json/version`, child);
    return { process: child, port, profile, version: await response.json(), stderr: () => stderr };
  } catch (error) {
    stop(child);
    throw new Error(`${error.message}; Chrome stderr=${stderr.slice(-1000)}`);
  }
}

async function newPage(chrome) {
  const response = await fetch(`http://127.0.0.1:${chrome.port}/json/new?about:blank`, { method: "PUT" });
  if (!response.ok) throw new Error(`cannot create Chrome target: ${response.status}`);
  return CDP.connect((await response.json()).webSocketDebuggerUrl);
}

const WS_RECORDER = `(() => {
  const log = [];
  Object.defineProperty(window, "__nxWsLog", { value: log, configurable: true });
  const Orig = window.WebSocket;
  window.WebSocket = function (url, protocols) {
    const ws = protocols === undefined ? new Orig(url) : new Orig(url, protocols);
    const entry = { url: String(url), opened: false, frames: 0, texts: [] };
    log.push(entry);
    ws.addEventListener("open", () => { entry.opened = true; });
    ws.addEventListener("message", (event) => {
      entry.frames += 1;
      if (typeof event.data === "string" && entry.texts.length < 20) entry.texts.push(event.data.slice(0, 300));
    });
    return ws;
  };
  window.WebSocket.prototype = Orig.prototype;
  for (const key of ["CONNECTING", "OPEN", "CLOSING", "CLOSED"]) {
    Object.defineProperty(window.WebSocket, key, { value: Orig[key] });
  }
  return true;
})()`;

function buildFrontend() {
  const run = spawnSync(process.execPath, [path.join(ROOT, "scripts/build.mjs"), "frontend"], {
    cwd: ROOT,
    stdio: "inherit",
  });
  if (run.status !== 0) throw new Error(`frontend build failed (exit ${run.status})`);
}

function buildServer(binary) {
  const run = spawnSync("go", ["build", "-o", binary, "./cmd/nexterm-server"], { cwd: ROOT, stdio: "inherit" });
  if (run.status !== 0) throw new Error(`server build failed (exit ${run.status})`);
}

async function startServer(binary) {
  const port = await freePort();
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-smoke-server-"));
  const keyFile = path.join(dataDir, "master.key");
  fs.writeFileSync(keyFile, `smoke-master-${Date.now().toString(36)}\n`, { mode: 0o600 });
  const logPath = path.join(OUT, "server.log");
  const log = fs.openSync(logPath, "w");
  const child = spawn(binary, [
    "--listen", `127.0.0.1:${port}`,
    "--data-dir", dataDir,
    "--auth=loopback",
    "--master-key-file", keyFile,
    "--web-root", path.join(ROOT, "dist"),
  ], { stdio: ["ignore", log, log] });
  await waitHttp(`http://127.0.0.1:${port}/auth/status`, child);
  return { process: child, origin: `http://127.0.0.1:${port}`, dataDir, logPath };
}

async function rpc(origin, cmd, args) {
  const response = await fetch(`${origin}/rpc`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ cmd, args: args === undefined ? null : args }),
  });
  const body = await response.json().catch(() => null);
  if (!body?.ok) throw new Error(`rpc ${cmd} failed: HTTP ${response.status} ${JSON.stringify(body)}`);
  return body.data;
}

async function waitFor(page, expression, timeout = 30_000, label = expression) {
  const deadline = Date.now() + timeout;
  for (;;) {
    // 导航/reload 期间执行上下文会被销毁, evaluate 抛错属正常, 继续轮询到超时。
    try {
      const value = await page.evaluate(expression);
      if (value) return value;
    } catch {}
    if (Date.now() >= deadline) throw new Error(`timed out waiting for ${label}`);
    await sleep(100);
  }
}

async function pressTab(page) {
  const base = { key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 };
  await page.send("Input.dispatchKeyEvent", { type: "rawKeyDown", ...base });
  await page.send("Input.dispatchKeyEvent", { type: "keyUp", ...base });
  await sleep(60);
}

const ACTIVE_ELEMENT = `(() => {
  const el = document.activeElement;
  if (!el) return "none";
  const label = [el.tagName.toLowerCase(), el.id ? "#" + el.id : "", typeof el.className === "string" && el.className ? "." + el.className.split(/\\s+/).filter(Boolean).slice(0, 2).join(".") : ""].join("");
  return label;
})()`;

async function main() {
  const binary = path.join(OUT, process.platform === "win32" ? "nexterm-server.exe" : "nexterm-server");
  buildFrontend();
  buildServer(binary);

  let server;
  let chrome;
  let page;
  try {
    server = await startServer(binary);
    chrome = await startChrome();
    page = await newPage(chrome);
    await page.send("Page.enable");
    await page.send("Page.addScriptToEvaluateOnNewDocument", { source: WS_RECORDER });

    await pass("spa-boot-workspace", async () => {
      await page.send("Page.navigate", { url: `${server.origin}/` });
      await waitFor(page, `!!document.querySelector(".nx-workspace-body")`, 30_000, "workspace shell");
      const transport = await page.evaluate(`window.__NEXTERM_TRANSPORT__ ?? ""`);
      assert.equal(transport, "web", "SPA should boot with the web transport marker");
      const title = await page.evaluate(`document.title`);
      return { transport, title };
    });

    await pass("keyboard-tab-focus", async () => {
      const seen = [];
      for (let i = 0; i < 4; i++) {
        await pressTab(page);
        seen.push(await page.evaluate(ACTIVE_ELEMENT));
      }
      const moved = seen.filter((entry) => entry && entry !== "body" && entry !== "none");
      assert.ok(moved.length >= 2, `Tab should move real focus through the page, got ${JSON.stringify(seen)}`);
      assert.ok(new Set(moved).size >= 2, `focus should reach at least two distinct elements, got ${JSON.stringify(seen)}`);
      return { focusTrail: seen };
    });

    await pass("ws-events-frame", async () => {
      await waitFor(
        page,
        `(window.__nxWsLog ?? []).some((entry) => entry.url.includes("/ws/events") && entry.opened)`,
        15_000,
        "/ws/events socket open",
      );
      const layout = await rpc(server.origin, "layout_get");
      assert.ok(typeof layout.revision === "number", `layout_get should return a revision, got ${JSON.stringify(layout)}`);
      const saved = await rpc(server.origin, "layout_put", { args: { data: JSON.stringify(layout.data ?? null), revision: layout.revision } });
      assert.equal(saved.saved, true, `layout_put should save, got ${JSON.stringify(saved)}`);
      await waitFor(
        page,
        `(window.__nxWsLog ?? []).some((entry) => entry.url.includes("/ws/events") && entry.texts.some((text) => text.includes("layout://changed")))`,
        15_000,
        "layout://changed frame on /ws/events",
      );
      return { revision: saved.revision };
    });

    await pass("reload-restores-workspace", async () => {
      await page.send("Page.reload", { ignoreCache: true });
      await waitFor(page, `!!document.querySelector(".nx-workspace-body")`, 30_000, "workspace shell after reload");
      await waitFor(
        page,
        `(window.__nxWsLog ?? []).some((entry) => entry.url.includes("/ws/events") && entry.opened)`,
        15_000,
        "/ws/events reconnect after reload",
      );
      return {};
    });
  } finally {
    if (page) page.close();
    await stop(chrome?.process);
    await stop(server?.process);
    if (chrome?.profile) fs.rmSync(chrome.profile, { recursive: true, force: true });
    if (server?.dataDir) fs.rmSync(server.dataDir, { recursive: true, force: true });
  }

  const report = {
    checks: [...results.values()],
    browser: chrome?.version ?? { status: "unavailable" },
    serverLog: server?.logPath,
  };
  fs.writeFileSync(path.join(OUT, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
  const failed = [...results.values()].filter((entry) => entry.status !== "passed");
  console.warn(`\n通过 ${results.size - failed.length} 项, 失败 ${failed.length} 项`);
  if (failed.length > 0) {
    for (const entry of failed) console.warn(`FAILED ${entry.id}: ${entry.error}`);
    process.exitCode = 1;
  }
}

await main();
