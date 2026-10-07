#!/usr/bin/env node
import { spawn, spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

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
  "rpc-auth-on-csrf", "files-auth-on-csrf",
];
const REAL_TARGET_GAPS = [
  { id: "ios-soft-keyboard", status: "evidence-gap", reason: "a physical iOS/iPadOS device and its native soft keyboard are not available to headless Chromium" },
  { id: "android-soft-keyboard", status: "evidence-gap", reason: "a physical Android device and its native soft keyboard are not available to headless Chromium" },
  { id: "mobile-safari", status: "evidence-gap", reason: "Chromium is real-browser evidence, not Mobile Safari evidence" },
  { id: "lazycat-pages-real-targets", status: "evidence-gap", reason: "LazyCat box and deployed Pages target acceptance belong to M47; desktop/server builds do not cover them" },
  { id: "linux-server-real-deployment", status: "evidence-gap", reason: "a real always-on Linux host with domain, TLS and a reverse proxy is not available to a local headless run" },
  { id: "lazycat-arm64-real-box", status: "evidence-gap", reason: "ARM64 LazyCat hardware and the LZC_ARCH=arm64 real-box installation belong to M47; local desktop/server builds do not cover them" },
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

export function stop(process) {
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
      setTimeout(() => {
        if (this.pending.has(id)) {
          this.pending.delete(id);
          reject(new Error(`CDP send timeout: ${method} ${JSON.stringify(params).slice(0, 120)}`));
        }
      }, 30_000);
    });
  }

  async evaluate(expression) {
    const response = await this.send("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true, userGesture: true });
    if (response.exceptionDetails) {
      throw new Error(await this.describeException(response.exceptionDetails));
    }
    return response.result?.value;
  }

  async describeException(details) {
    const exception = details.exception ?? {};
    let headline;
    if (typeof exception.value === "string") {
      headline = exception.value;
    } else if (exception.value !== undefined && exception.value !== null && typeof exception.value === "object") {
      headline = JSON.stringify(exception.value);
    } else {
      headline = exception.description || details.text || exception.className || "page exception";
    }
    const lines = [headline];
    if (exception.objectId && exception.value === undefined) {
      try {
        const properties = await this.send("Runtime.getProperties", { objectId: exception.objectId, ownProperties: true });
        const preview = (properties.result || [])
          .filter((property) => property.value !== undefined)
          .slice(0, 12)
          .map((property) => `${property.name}=${JSON.stringify(property.value.value ?? property.value.description ?? property.value.type)}`);
        if (preview.length > 0) lines.push(`thrown value properties: { ${preview.join(", ")} }`);
      } catch {}
    }
    for (const frame of details.stackTrace?.callFrames ?? []) {
      lines.push(`    at ${frame.functionName || "<anonymous>"} (${frame.url}:${frame.lineNumber + 1}:${frame.columnNumber + 1})`);
    }
    return lines.join("\n");
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

let cachedServerBinary = null;

function serverBinary() {
  if (globalThis.process.env.NEXTERM_SERVER_BIN) return globalThis.process.env.NEXTERM_SERVER_BIN;
  if (cachedServerBinary) return cachedServerBinary;
  const goos = { darwin: "darwin", linux: "linux", win32: "windows" }[globalThis.process.platform];
  const goarch = { arm64: "arm64", x64: "amd64" }[globalThis.process.arch];
  const binary = path.join(ROOT, "target/go-build/nexterm-server-browser");
  const built = spawnSync(globalThis.process.execPath, ["scripts/build.mjs", "server", "--release", `--os=${goos}`, `--arch=${goarch}`, `--out=${binary}`], { cwd: ROOT, stdio: "inherit" });
  if (built.status !== 0) throw new Error("Go server build for browser acceptance failed");
  cachedServerBinary = binary;
  return binary;
}

export async function startServer() {
  const binary = serverBinary();
  const port = await freePort();
  const data = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-browser-server-"));
  const env = { ...globalThis.process.env, NEXTERM_MASTER_KEY: "real-browser-e2e-master" };
  const log = fs.openSync(path.join(OUT, "server.log"), "w");
  const process = spawn(binary, ["--listen", `127.0.0.1:${port}`, "--data-dir", data, "--auth=loopback"], {
    cwd: ROOT,
    env,
    stdio: ["ignore", log, log],
  });
  await waitHttp(`http://127.0.0.1:${port}/healthz`, process);
  return { process, port, origin: `http://127.0.0.1:${port}`, data };
}

async function waitInitCode(logPath, process, timeout = 15_000) {
  const marker = "一次性初始化码: ";
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (process.exitCode !== null && process.exitCode !== undefined) throw new Error(`auth=on server exited ${process.exitCode}`);
    const text = fs.existsSync(logPath) ? fs.readFileSync(logPath, "utf8") : "";
    const line = text.split("\n").find((entry) => entry.includes(marker));
    if (line) return line.split(marker, 2)[1].trim();
    await sleep(200);
  }
  throw new Error(`auth=on init code not found in ${logPath}`);
}

async function startAuthOnServer() {
  const binary = serverBinary();
  const port = await freePort();
  const data = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-browser-auth-on-"));
  const env = { ...globalThis.process.env, NEXTERM_MASTER_KEY: "real-browser-auth-on-e2e-master" };
  const logPath = path.join(OUT, "server-auth-on.log");
  const log = fs.openSync(logPath, "w");
  const process = spawn(binary, ["--listen", `127.0.0.1:${port}`, "--data-dir", data, "--auth=on"], {
    cwd: ROOT,
    env,
    stdio: ["ignore", log, log],
  });
  await waitHttp(`http://127.0.0.1:${port}/healthz`, process);
  const initCode = await waitInitCode(logPath, process);
  return { process, port, origin: `http://127.0.0.1:${port}`, data, initCode };
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
    state.keeper = events.createBinaryChannel(() => {});
    await commands.terminalApi.attachTab(state.tabId, state.keeper, 0);
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
    const dropped = await page.evaluate(`(() => {
      let n = 0;
      const channelId = __nxAcceptance.events.channelIdOf(__nxAcceptance.channel);
      for (const ws of window.__nxSockets) {
        if ((ws.readyState === 0 || ws.readyState === 1) && ws.url.includes('/ws/channel/' + channelId)) { ws.close(); n += 1; }
      }
      return n;
    })()`);
    await page.waitFor(`__nxAcceptance.reopened > 0 && __nxAcceptance.reconnectAttached > 0 && __nxAcceptance.text.includes(__nxAcceptance.marker)`, 35_000);
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

async function authOnRpcAcceptance(page, authServer) {
  await pass("rpc-auth-on-csrf", async () => {
    await page.navigate(`${authServer.origin}/healthz`);
    await page.evaluate(`window.__NEXTERM_TRANSPORT__ = 'web'; true`);
    const session = await page.evaluate(`(async () => {
      const { authApi } = await import('${VITE}/src/ipc/authApi.ts');
      const created = await authApi.init({
        code: ${JSON.stringify(authServer.initCode)},
        username: "acceptance",
        password: "acceptance-pw-123",
        dekEnvelope: new Uint8Array([1, 2, 3]),
        kdfSalt: new Uint8Array([4, 5, 6]),
        kdfParams: '{"t":3,"m":65536,"p":4}',
        recoveryEnvelope: new Uint8Array([7, 8, 9]),
        recoveryHash: "acceptance-recovery-hash",
      });
      return { username: created.user.username, csrf: created.csrf_token.length > 0 };
    })()`);
    assert.deepEqual(session, { username: "acceptance", csrf: true });

    const control = await page.evaluate(`(async () => {
      const res = await fetch('/rpc', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ cmd: 'asset_list', args: null }),
      });
      const body = await res.json().catch(() => null);
      return { status: res.status, code: body?.error?.code ?? null, message: body?.error?.message ?? '' };
    })()`);
    assert.equal(control.status, 403, JSON.stringify(control));
    assert.match(control.message, /CSRF/, JSON.stringify(control));

    const evidence = await page.evaluate(`(async () => {
      const commands = await import('${VITE}/src/ipc/commands.ts');
      const { setCsrfToken } = await import('${VITE}/src/ipc/authApi.ts');
      const assets = await commands.assetApi.list();
      const group = await commands.assetApi.groupCreate('acceptance-' + Date.now());
      setCsrfToken('stale-token');
      const assetsAfterStale = await commands.assetApi.list();
      return {
        assetListIsArray: Array.isArray(assets),
        groupCreated: typeof group.id === 'string' && group.id.length > 0,
        staleTokenRefreshed: Array.isArray(assetsAfterStale),
      };
    })()`);
    assert.deepEqual(evidence, { assetListIsArray: true, groupCreated: true, staleTokenRefreshed: true });
    return { evidence: { ...evidence, negativeControl: control } };
  });
}

async function authOnFilesAcceptance(page, authServer) {
  await pass("files-auth-on-csrf", async () => {
    await page.navigate(`${authServer.origin}/healthz`);
    await page.evaluate(`window.__NEXTERM_TRANSPORT__ = 'web'; true`);
    const session = await page.evaluate(`(async () => {
      const { authApi, getCsrfToken } = await import('${VITE}/src/ipc/authApi.ts');
      // rpc-auth-on-csrf 已建账号时直接登录, 否则用一次性初始化码补建。
      let created;
      try {
        created = await authApi.login('acceptance', 'acceptance-pw-123');
      } catch {
        created = await authApi.init({
          code: ${JSON.stringify(authServer.initCode)},
          username: "acceptance",
          password: "acceptance-pw-123",
          dekEnvelope: new Uint8Array([1, 2, 3]),
          kdfSalt: new Uint8Array([4, 5, 6]),
          kdfParams: '{"t":3,"m":65536,"p":4}',
          recoveryEnvelope: new Uint8Array([7, 8, 9]),
          recoveryHash: "acceptance-recovery-hash",
        });
      }
      return { username: created.user.username, csrf: (getCsrfToken() ?? '').length > 0 };
    })()`);
    assert.deepEqual(session, { username: "acceptance", csrf: true });

    const control = await page.evaluate(`(async () => {
      const stage = await fetch('/files/blob?name=control.bin', { method: 'POST', body: new Uint8Array([1, 2, 3]) });
      const stageBody = await stage.json().catch(() => null);
      const removal = await fetch('/files/blob?id=01J0000000000000000000000', { method: 'DELETE' });
      const removeBody = await removal.json().catch(() => null);
      return {
        stageStatus: stage.status,
        stageMessage: stageBody?.error?.message ?? '',
        removeStatus: removal.status,
        removeMessage: removeBody?.error?.message ?? '',
      };
    })()`);
    assert.equal(control.stageStatus, 403, JSON.stringify(control));
    assert.match(control.stageMessage, /CSRF/, JSON.stringify(control));
    assert.equal(control.removeStatus, 403, JSON.stringify(control));
    assert.match(control.removeMessage, /CSRF/, JSON.stringify(control));

    const evidence = await page.evaluate(`(async () => {
      const webFiles = await import('${VITE}/src/ipc/webFiles.ts');
      const { setCsrfToken } = await import('${VITE}/src/ipc/authApi.ts');
      const png = () => new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])], 'acceptance.png', { type: 'image/png' });
      // headless 无文件选择器: 遮蔽 showSaveFilePicker 走"不可用"分支, 只验预留/删除传输。
      try { Object.defineProperty(window, 'showSaveFilePicker', { value: undefined, configurable: true }); } catch {}

      const staged = await webFiles.stageFile(png());
      const image = await webFiles.uploadImage(png());
      const anonymous = await fetch(image.url, { credentials: 'omit' });
      const publicBytes = anonymous.ok ? (await anonymous.arrayBuffer()).byteLength : 0;
      const limits = await webFiles.fetchImageService();
      const reservedPath = await webFiles.requestSaveTarget('acceptance-target.bin');

      setCsrfToken('stale-token');
      await webFiles.dropStaged(staged.path);
      await webFiles.dropStaged(reservedPath);
      const afterDelete = await fetch('/files/blob?id=' + encodeURIComponent(staged.id));
      const reservedState = await webFiles.deliverStaged(reservedPath);
      return {
        stagedId: staged.id,
        imageUrl: image.url,
        publicDownloadStatus: anonymous.status,
        publicBytes,
        limitsReported: limits !== null && limits.maxBytes > 0 && limits.ownerQuotaBytes > 0 && limits.ttlSeconds > 0,
        reservedPath: typeof reservedPath === 'string' && reservedPath.includes('acceptance-target.bin'),
        deletedGone: afterDelete.status === 404,
        reservedState,
      };
    })()`);
    assert.equal(evidence.publicDownloadStatus, 200, JSON.stringify(evidence));
    assert.equal(evidence.publicBytes, 8, JSON.stringify(evidence));
    assert.equal(evidence.limitsReported, true, JSON.stringify(evidence));
    assert.equal(evidence.reservedPath, true, JSON.stringify(evidence));
    assert.equal(evidence.deletedGone, true, JSON.stringify(evidence));
    assert.equal(evidence.reservedState, "not-staged", JSON.stringify(evidence));
    return { evidence: { ...evidence, negativeControl: control } };
  });
}

async function main() {
  let vite;
  let server;
  let authServer;
  let chrome;
  let page;
  try {
    [vite, server, authServer, chrome] = await Promise.all([startVite(), startServer(), startAuthOnServer(), startChrome()]);
    page = await newPage(chrome);
    try { await layoutAcceptance(page); } catch (error) { harnessErrors.push(`layout harness: ${error.stack || error}`); }
    try { await wsAcceptance(page, server); } catch (error) { harnessErrors.push(`WS harness: ${error.stack || error}`); }
    try { await rpcAcceptance(page); } catch (error) { harnessErrors.push(`RPC harness: ${error.stack || error}`); }
    await page.evaluate(`(async () => {
      const s = window.__nxAcceptance;
      if (!s) return true;
      try { if (s.channel) { await s.commands.terminalApi.detach(s.tabId, s.events.channelIdOf(s.channel)); s.events.disposeChannel(s.channel); } } catch {}
      try { if (s.keeper) { await s.commands.terminalApi.detach(s.tabId, s.events.channelIdOf(s.keeper)); s.events.disposeChannel(s.keeper); } } catch {}
      try { if (s.session) await s.commands.sessionApi.disconnect(s.session.id); } catch {}
      return true;
    })()`).catch(() => {});
    try { await authOnRpcAcceptance(page, authServer); } catch (error) { harnessErrors.push(`auth=on RPC harness: ${error.stack || error}`); }
    try { await authOnFilesAcceptance(page, authServer); } catch (error) { harnessErrors.push(`auth=on files harness: ${error.stack || error}`); }
  } catch (error) {
    harnessErrors.push(String(error?.stack || error));
  } finally {
    if (page) page.close();
    stop(chrome?.process);
    stop(authServer?.process);
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
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  await main();
}
