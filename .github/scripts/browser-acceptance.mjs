#!/usr/bin/env node
import { spawn, spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const OUT = path.join(ROOT, "target/acceptance-browser");
const VITE_PORT = Number(process.env.NEXTERM_VITE_PORT || 1420);
const VITE = `http://127.0.0.1:${VITE_PORT}`;
const results = new Map();
const harnessErrors = [];
const EXPECTED = [
  "layout-360", "layout-560", "layout-820", "coarse-pointer-keys", "soft-keyboard-focus-viewport-resize",
  "ws-early-frame", "ws-replay", "ws-reconnect-replay",
  "rpc-401-non-retry", "rpc-403-non-retry", "rpc-404-non-retry", "rpc-network-5xx-jitter-recovery",
];
const REAL_TARGET_GAPS = [
  { id: "ios-soft-keyboard", status: "evidence-gap", reason: "a physical iOS/iPadOS device and its native soft keyboard are not available to headless Chromium" },
  { id: "android-soft-keyboard", status: "evidence-gap", reason: "a physical Android device and its native soft keyboard are not available to headless Chromium" },
  { id: "mobile-safari", status: "evidence-gap", reason: "Chromium is real-browser evidence, not Mobile Safari evidence" },
  { id: "lazycat-pages-real-targets", status: "evidence-gap", reason: "LazyCat box and deployed Pages target acceptance belong to M47; desktop/server builds do not cover them" },
];

fs.mkdirSync(OUT, { recursive: true });

function record(id, status, detail = {}) {
  results.set(id, { id, status, ...detail });
  console.warn(`${status === "passed" ? "PASS" : status === "failed" ? "FAIL" : "NOT RUN"} ${id}`);
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

function stop(process) {
  if (!process || process.exitCode !== null) return;
  process.kill("SIGTERM");
}

function chromeExecutable() {
  const candidates = [
    process.env.CHROME_PATH,
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
    "/Applications/Chromium.app/Contents/MacOS/Chromium",
    "google-chrome",
    "chromium",
    "chromium-browser",
    "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe",
    "C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe",
  ].filter(Boolean);
  for (const candidate of candidates) {
    if (candidate.includes("/") || candidate.includes("\\")) {
      if (fs.existsSync(candidate)) return candidate;
    } else {
      const found = spawnSync("which", [candidate], { encoding: "utf8" });
      if (found.status === 0) return found.stdout.trim();
    }
  }
  throw new Error("no real Chrome/Chromium executable found; set CHROME_PATH (this is a failure, not a skip)");
}

class CDP {
  constructor(socket) {
    this.socket = socket;
    this.sequence = 0;
    this.pending = new Map();
    this.listeners = new Map();
    socket.addEventListener("message", (event) => {
      const message = JSON.parse(String(event.data));
      if (message.id) {
        const pending = this.pending.get(message.id);
        if (!pending) return;
        this.pending.delete(message.id);
        if (message.error) pending.reject(new Error(`${pending.method}: ${message.error.message}`));
        else pending.resolve(message.result || {});
      } else if (message.method) {
        for (const listener of this.listeners.get(message.method) || []) listener(message.params || {});
      }
    });
    socket.addEventListener("close", () => {
      for (const pending of this.pending.values()) pending.reject(new Error("CDP socket closed"));
      this.pending.clear();
    });
  }

  static async connect(url) {
    const socket = new WebSocket(url);
    await new Promise((resolve, reject) => {
      socket.addEventListener("open", resolve, { once: true });
      socket.addEventListener("error", reject, { once: true });
    });
    return new CDP(socket);
  }

  on(method, listener) {
    const listeners = this.listeners.get(method) || new Set();
    listeners.add(listener);
    this.listeners.set(method, listeners);
    return () => listeners.delete(listener);
  }

  send(method, params = {}) {
    const id = ++this.sequence;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject, method });
      this.socket.send(JSON.stringify({ id, method, params }));
    });
  }

  async evaluate(expression) {
    const response = await this.send("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true, userGesture: true });
    if (response.exceptionDetails) {
      const exception = response.exceptionDetails.exception;
      const value = typeof exception?.value === "object" ? JSON.stringify(exception.value) : exception?.value;
      throw new Error(exception?.description || value || JSON.stringify(response.exceptionDetails));
    }
    return response.result?.value;
  }

  async waitFor(expression, timeout = 30_000) {
    const deadline = Date.now() + timeout;
    let last;
    while (Date.now() < deadline) {
      try {
        last = await this.evaluate(expression);
        if (last) return last;
      } catch (error) {
        last = String(error);
      }
      await sleep(100);
    }
    throw new Error(`browser condition timed out: ${expression}; last=${JSON.stringify(last)}`);
  }

  async navigate(url) {
    await this.send("Page.enable");
    await this.send("Page.navigate", { url });
    await this.waitFor("document.readyState === 'complete'");
  }

  close() {
    this.socket.close();
  }
}

async function startChrome() {
  const port = await freePort();
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-chrome-"));
  const args = [
    "--headless=new", "--no-first-run", "--disable-dev-shm-usage", "--disable-background-networking",
    `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`, "--window-size=1280,900", "about:blank",
  ];
  if (globalThis.process.platform === "linux") args.unshift("--no-sandbox");
  const process = spawn(chromeExecutable(), args, { stdio: ["ignore", "pipe", "pipe"] });
  let stderr = "";
  process.stderr.on("data", (chunk) => { stderr += chunk; });
  try {
    const response = await waitHttp(`http://127.0.0.1:${port}/json/version`, process);
    return { process, port, profile, version: await response.json(), stderr: () => stderr };
  } catch (error) {
    stop(process);
    throw new Error(`${error.message}; Chrome stderr=${stderr.slice(-1000)}`);
  }
}

async function newPage(chrome) {
  const response = await fetch(`http://127.0.0.1:${chrome.port}/json/new?about:blank`, { method: "PUT" });
  if (!response.ok) throw new Error(`cannot create Chrome target: ${response.status}`);
  return CDP.connect((await response.json()).webSocketDebuggerUrl);
}

function startVite() {
  const command = globalThis.process.platform === "win32" ? "pnpm.cmd" : "pnpm";
  const process = spawn(command, ["exec", "vite", "--host", "127.0.0.1", "--port", String(VITE_PORT), "--strictPort"], {
    cwd: ROOT,
    env: { ...globalThis.process.env, NODE_OPTIONS: "" },
    stdio: ["ignore", "pipe", "pipe"],
  });
  return waitHttp(VITE, process).then(() => process);
}

async function startServer() {
  const goos = { darwin: "darwin", linux: "linux", win32: "windows" }[globalThis.process.platform];
  const goarch = { arm64: "arm64", x64: "amd64" }[globalThis.process.arch];
  const binary = globalThis.process.env.NEXTERM_SERVER_BIN || path.join(ROOT, "target/go-build/nexterm-server-browser");
  if (!globalThis.process.env.NEXTERM_SERVER_BIN) {
    const built = spawnSync(globalThis.process.execPath, ["scripts/build.mjs", "server", "--release", `--os=${goos}`, `--arch=${goarch}`, `--out=${binary}`], { cwd: ROOT, stdio: "inherit" });
    if (built.status !== 0) throw new Error("Go server build for browser acceptance failed");
  }
  const port = await freePort();
  const data = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-browser-server-"));
  const log = fs.openSync(path.join(OUT, "server.log"), "w");
  const process = spawn(binary, ["--listen", `127.0.0.1:${port}`, "--data-dir", data], {
    cwd: ROOT,
    env: { ...globalThis.process.env, NEXTERM_MASTER_KEY: "real-browser-e2e-master" },
    stdio: ["ignore", log, log],
  });
  await waitHttp(`http://127.0.0.1:${port}/healthz`, process);
  return { process, port, origin: `http://127.0.0.1:${port}`, data };
}

async function metrics(page, width, height, coarse) {
  await page.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile: width <= 560 });
  if (coarse) {
    await page.send("Emulation.setTouchEmulationEnabled", { enabled: true, maxTouchPoints: 5 });
    await page.send("Emulation.setEmulatedMedia", { features: [{ name: "pointer", value: "coarse" }, { name: "any-pointer", value: "coarse" }] });
  } else {
    await page.send("Emulation.setTouchEmulationEnabled", { enabled: false, maxTouchPoints: 1 });
    await page.send("Emulation.setEmulatedMedia", { features: [{ name: "pointer", value: "fine" }, { name: "any-pointer", value: "fine" }] });
  }
  await sleep(350);
}

async function layoutAcceptance(page) {
  await page.navigate(`${VITE}/?demo=1`);
  await page.waitFor("document.querySelector('#root')?.children.length > 0 && document.querySelector('.xterm')");
  for (const width of [360, 560, 820]) {
    await pass(`layout-${width}`, async () => {
      await metrics(page, width, 740, false);
      const evidence = await page.evaluate(`(async () => {
        const { workspaceViewport } = await import('/src/features/terminal/workspaceLayout.ts');
        const model = workspaceViewport(${width}, false);
        const keys = document.querySelector('.nx-terminal-keys');
        const keyDisplay = keys ? getComputedStyle(keys).display : 'missing';
        return {
          model,
          innerWidth,
          scrollWidth: document.documentElement.scrollWidth,
          noHorizontalOverflow: document.documentElement.scrollWidth <= innerWidth + 1,
          keyDisplay,
          keysExpected: model.terminalKeys,
          keysVisible: Boolean(keys) && keyDisplay !== 'none',
          rootText: document.querySelector('#root').innerText.slice(0, 120),
        };
      })()`);
      assert.equal(evidence.innerWidth, width);
      assert.equal(evidence.noHorizontalOverflow, true, JSON.stringify(evidence));
      assert.equal(evidence.model.overlaySidebars, true);
      assert.equal(evidence.model.compact, width <= 560);
      assert.equal(evidence.keysVisible, evidence.keysExpected, JSON.stringify(evidence));
      const screenshot = await page.send("Page.captureScreenshot", { format: "png" });
      fs.writeFileSync(path.join(OUT, `layout-${width}.png`), Buffer.from(screenshot.data, "base64"));
      return { evidence, screenshot: `layout-${width}.png` };
    });
  }
  await pass("coarse-pointer-keys", async () => {
    await metrics(page, 820, 740, true);
    const evidence = await page.evaluate(`(() => {
      const keys = document.querySelector('.nx-terminal-keys');
      return { coarse: matchMedia('(pointer: coarse)').matches, keysVisible: Boolean(keys) && getComputedStyle(keys).display !== 'none' };
    })()`);
    assert.deepEqual(evidence, { coarse: true, keysVisible: true });
    return { evidence };
  });
  await pass("soft-keyboard-focus-viewport-resize", async () => {
    await metrics(page, 360, 740, true);
    const focused = await page.evaluate(`(() => {
      const button = document.querySelector('.nx-terminal-keyboard');
      if (!button) return { button: false };
      button.click();
      const active = document.activeElement;
      return { button: true, focused: active && (active.tagName === 'TEXTAREA' || active.classList.contains('xterm-helper-textarea')) };
    })()`);
    assert.deepEqual(focused, { button: true, focused: true });
    await metrics(page, 360, 420, true);
    const resized = await page.evaluate(`({ height: innerHeight, noHorizontalOverflow: document.documentElement.scrollWidth <= innerWidth + 1 })`);
    assert.deepEqual(resized, { height: 420, noHorizontalOverflow: true });
    return { evidence: { focused, resized }, scope: "real Chromium focus + viewport resize; physical soft keyboard remains an explicit gap" };
  });
}

async function wsAcceptance(page, server) {
  await page.navigate(`${server.origin}/healthz`);
  await page.evaluate(`window.__NEXTERM_TRANSPORT__ = 'web'; true`);
  await page.evaluate(`(() => {
    const registry = window.__nxSockets = [];
    const Orig = window.WebSocket;
    window.WebSocket = function (url, protocols) {
      const ws = protocols === undefined ? new Orig(url) : new Orig(url, protocols);
      registry.push(ws);
      return ws;
    };
    window.WebSocket.prototype = Orig.prototype;
    for (const key of ["CONNECTING", "OPEN", "CLOSING", "CLOSED"]) {
      Object.defineProperty(window.WebSocket, key, { value: Orig[key] });
    }
    return true;
  })()`);
  await page.evaluate(`(async () => {
    const commands = await import('${VITE}/src/ipc/commands.ts');
    const events = await import('${VITE}/src/ipc/events.ts');
    const state = window.__nxAcceptance = { commands, events, frames: 0, text: '', reopened: 0, reconnectAttached: 0 };
    state.channel = events.createBinaryChannel((bytes) => {
      state.frames += 1;
      state.text += new TextDecoder().decode(bytes);
    });
    state.session = await commands.sessionApi.connectLocal();
    state.tabId = await commands.terminalApi.attach(state.session.id, 80, 24, state.channel);
    const markerNonce = Date.now();
    state.marker = 'nx-early-' + markerNonce;
    await commands.terminalApi.write(state.tabId, new TextEncoder().encode("printf 'nx-early-%s\\n' '" + markerNonce + "'\\r"));
    return true;
  })()`);
  await pass("ws-early-frame", async () => {
    const evidence = await page.waitFor(`window.__nxAcceptance.text.includes(window.__nxAcceptance.marker) && window.__nxAcceptance.frames > 0`);
    assert.equal(evidence, true);
    return { evidence: await page.evaluate(`({ frames: __nxAcceptance.frames, marker: __nxAcceptance.marker })`) };
  });
  await pass("ws-replay", async () => {
    await page.evaluate(`(async () => {
      const s = __nxAcceptance;
      await s.commands.terminalApi.detach(s.tabId, s.events.channelIdOf(s.channel));
      s.events.disposeChannel(s.channel);
      s.text = ''; s.frames = 0;
      s.channel = s.events.createBinaryChannel((bytes) => { s.frames += 1; s.text += new TextDecoder().decode(bytes); });
      await s.commands.terminalApi.attachTab(s.tabId, s.channel, 65536);
      return true;
    })()`);
    await page.waitFor(`__nxAcceptance.text.includes(__nxAcceptance.marker)`);
    return { evidence: await page.evaluate(`({ replayFrames: __nxAcceptance.frames })`) };
  });
  await pass("ws-reconnect-replay", async () => {
    await page.evaluate(`(() => {
      const s = __nxAcceptance;
      s.text = ''; s.frames = 0;
      s.offReopen = s.events.onChannelReopen(s.channel, async () => {
        s.reopened += 1;
        await s.commands.terminalApi.attachTab(s.tabId, s.channel, 65536);
        s.reconnectAttached += 1;
      });
      return true;
    })()`);
    await page.send("Network.enable");
    const dropped = await page.evaluate(`(() => {
      let n = 0;
      for (const ws of window.__nxSockets) {
        if (ws.readyState === 0 || ws.readyState === 1) { ws.close(); n += 1; }
      }
      return n;
    })()`);
    await page.send("Network.emulateNetworkConditions", { offline: true, latency: 180, downloadThroughput: 64 * 1024, uploadThroughput: 64 * 1024 });
    await sleep(1500);
    await page.send("Network.emulateNetworkConditions", { offline: false, latency: 180, downloadThroughput: 64 * 1024, uploadThroughput: 64 * 1024 });
    await page.waitFor(`__nxAcceptance.reopened > 0 && __nxAcceptance.reconnectAttached > 0 && __nxAcceptance.text.includes(__nxAcceptance.marker)`, 35_000);
    await page.send("Network.emulateNetworkConditions", { offline: false, latency: 0, downloadThroughput: -1, uploadThroughput: -1 });
    const evidence = await page.evaluate(`({ reopened: __nxAcceptance.reopened, reconnectAttached: __nxAcceptance.reconnectAttached })`);
    return { evidence: { ...evidence, droppedSockets: dropped } };
  });
}

async function rpcAcceptance(page) {
  for (const status of [401, 403, 404]) {
    await pass(`rpc-${status}-non-retry`, async () => {
      let attempts = 0;
      const off = page.on("Fetch.requestPaused", (event) => {
        if (!event.request.url.includes("/rpc") || event.request.method !== "POST") {
          page.send("Fetch.continueRequest", { requestId: event.requestId }).catch(() => {});
          return;
        }
        attempts += 1;
        const code = status === 404 ? "not_found" : "forbidden";
        const body = Buffer.from(JSON.stringify({ ok: false, error: { code, message: `browser-${status}` } })).toString("base64");
        page.send("Fetch.fulfillRequest", {
          requestId: event.requestId, responseCode: status,
          responseHeaders: [{ name: "Content-Type", value: "application/json" }, { name: "Access-Control-Allow-Origin", value: "*" }],
          body,
        }).catch(() => {});
      });
      await page.send("Fetch.enable", { patterns: [{ urlPattern: "*/rpc" }] });
      const observed = await page.evaluate(`(async () => { try { await __nxAcceptance.commands.systemApi.platform(); return 'unexpected-success'; } catch (error) { return error.code || 'internal'; } })()`);
      await sleep(400);
      await page.send("Fetch.disable");
      off();
      assert.equal(attempts, 1, `HTTP ${status} was retried`);
      assert.equal(observed, status === 404 ? "not_found" : "forbidden");
      return { evidence: { attempts, observed } };
    });
  }
  await pass("rpc-network-5xx-jitter-recovery", async () => {
    const attempts = { network: 0, server500: 0 };
    let mode = "network";
    const off = page.on("Fetch.requestPaused", (event) => {
      if (!event.request.url.includes("/rpc") || event.request.method !== "POST") {
        page.send("Fetch.continueRequest", { requestId: event.requestId }).catch(() => {});
        return;
      }
      if (mode === "network") {
        attempts.network += 1;
        page.send("Fetch.failRequest", { requestId: event.requestId, errorReason: "ConnectionReset" }).catch(() => {});
      } else {
        attempts.server500 += 1;
        const body = Buffer.from(JSON.stringify({ ok: false, error: { code: "internal", message: "5xx jitter" } })).toString("base64");
        page.send("Fetch.fulfillRequest", {
          requestId: event.requestId, responseCode: 500,
          responseHeaders: [{ name: "Content-Type", value: "application/json" }, { name: "Access-Control-Allow-Origin", value: "*" }], body,
        }).catch(() => {});
      }
    });
    await page.send("Fetch.enable", { patterns: [{ urlPattern: "*/rpc" }] });
    const networkError = await page.evaluate(`(async () => { try { await __nxAcceptance.commands.systemApi.platform(); return 'unexpected'; } catch (error) { return error.code || 'internal'; } })()`);
    mode = "server500";
    const serverError = await page.evaluate(`(async () => { try { await __nxAcceptance.commands.systemApi.platform(); return 'unexpected'; } catch (error) { return error.code || 'internal'; } })()`);
    await sleep(400);
    await page.send("Fetch.disable");
    off();
    const recovered = await page.evaluate(`__nxAcceptance.commands.systemApi.platform()`);
    assert.deepEqual(attempts, { network: 1, server500: 1 });
    assert.equal(networkError, "internal");
    assert.equal(serverError, "internal");
    assert.equal(typeof recovered, "string");
    return { evidence: { attempts, networkError, serverError, recovered } };
  });
}

let vite;
let server;
let chrome;
let page;
try {
  [vite, server, chrome] = await Promise.all([startVite(), startServer(), startChrome()]);
  page = await newPage(chrome);
  try { await layoutAcceptance(page); } catch (error) { harnessErrors.push(`layout harness: ${error.stack || error}`); }
  try { await wsAcceptance(page, server); } catch (error) { harnessErrors.push(`WS harness: ${error.stack || error}`); }
  try { await rpcAcceptance(page); } catch (error) { harnessErrors.push(`RPC harness: ${error.stack || error}`); }
  await page.evaluate(`(async () => {
    const s = window.__nxAcceptance;
    if (!s) return true;
    try { if (s.channel) { await s.commands.terminalApi.detach(s.tabId, s.events.channelIdOf(s.channel)); s.events.disposeChannel(s.channel); } } catch {}
    try { if (s.session) await s.commands.sessionApi.disconnect(s.session.id); } catch {}
    return true;
  })()`).catch(() => {});
} catch (error) {
  harnessErrors.push(String(error?.stack || error));
} finally {
  if (page) page.close();
  stop(chrome?.process);
  stop(server?.process);
  stop(vite);
}

for (const id of EXPECTED) {
  if (!results.has(id)) record(id, "not-run-dependency-failed", { reason: "the required harness did not complete; this is not a pass or an approved skip" });
}
const checks = [...results.values()];
const failed = checks.filter((check) => check.status !== "passed");
const report = {
  schema_version: 1,
  status: failed.length || harnessErrors.length ? "failed" : "passed-with-explicit-real-target-gaps",
  browser: chrome?.version || { status: "unavailable" },
  execution: { real_browser: true, headless: true, jsdom: false, physical_device: false },
  checks,
  harness_errors: harnessErrors,
  evidence_gaps: REAL_TARGET_GAPS,
  skip_as_pass: false,
};
fs.writeFileSync(path.join(OUT, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
console.warn(`browser acceptance: ${checks.filter((check) => check.status === "passed").length}/${EXPECTED.length} automated checks passed; ${REAL_TARGET_GAPS.length} explicit real-target gaps; report=${path.join(OUT, "report.json")}`);
if (failed.length || harnessErrors.length) process.exit(1);
