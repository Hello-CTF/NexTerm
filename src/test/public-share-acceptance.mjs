#!/usr/bin/env node
// M152 公开分享 viewer 响应式验收: 真实 headless Chromium + vite dev server,
// 后端是本进程内的 fake WS server (仅复述 M138 viewer 合同: 文本 ready/error +
// 二进制终端字节), 不是生产后端证据; 生产合同证据归 scripts/e2e-sharing-local.py。
import { spawn, spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import crypto from "node:crypto";
import fs from "node:fs";
import http from "node:http";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { startVite as startViteProcess } from "./lib/acceptance-process.mjs";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const OUT = path.join(ROOT, "target/acceptance-public-share");
const VITE_PORT = Number(process.env.NEXTERM_VITE_PORT || 1433);
const VITE = `http://127.0.0.1:${VITE_PORT}`;
const results = new Map();
const harnessErrors = [];

// 共享 /tmp 可能被其他任务填满: vite 依赖预构建与 Chrome profile 的临时写入会
// 失败 (ENOENT/ENOSPC)。本脚本与子进程一律使用私有临时目录; 路径必须短,
// Chrome 的 SingletonSocket 受 Unix socket 路径长度限制。
const PRIVATE_TMP = path.join(os.homedir(), ".cache", "nexterm-acceptance-tmp");
fs.mkdirSync(PRIVATE_TMP, { recursive: true });
process.env.TMPDIR = PRIVATE_TMP;

fs.mkdirSync(OUT, { recursive: true });

function record(id, status, detail = {}) {
  results.set(id, { id, status, ...detail });
  console.warn(`${status === "passed" ? "PASS" : "FAILED"} ${id}`);
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
    this.eventHandlers = new Map();
    socket.addEventListener("message", (event) => {
      const message = JSON.parse(String(event.data));
      if (message.id) {
        const pending = this.pending.get(message.id);
        if (!pending) return;
        this.pending.delete(message.id);
        if (message.error) pending.reject(new Error(`${pending.method}: ${message.error.message}`));
        else pending.resolve(message.result || {});
        return;
      }
      const handlers = this.eventHandlers.get(message.method);
      if (handlers) for (const handler of handlers) handler(message.params);
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
    return { process, port, profile, version: await response.json() };
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

function viteCacheDir() {
  return path.join(ROOT, "node_modules/.vite");
}

// vite dev 与 vitest 共享 node_modules/.vite: dev server 的依赖预构建元数据指向
// 退出即清理的 /tmp 临时产物, 会让随后的 vitest 运行 ENOENT。验收前后各清一次。
function wipeViteCache() {
  fs.rmSync(viteCacheDir(), { recursive: true, force: true });
}

function startVite() {
  wipeViteCache();
  return startViteProcess({ root: ROOT, port: VITE_PORT });
}

const WS_GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11";

function encodeFrame(opcode, payload) {
  const length = payload.length;
  let header;
  if (length < 126) {
    header = Buffer.from([0x80 | opcode, length]);
  } else if (length < 65536) {
    header = Buffer.alloc(4);
    header[0] = 0x80 | opcode;
    header[1] = 126;
    header.writeUInt16BE(length, 2);
  } else {
    header = Buffer.alloc(10);
    header[0] = 0x80 | opcode;
    header[1] = 127;
    header.writeBigUInt64BE(BigInt(length), 2);
  }
  return Buffer.concat([header, payload]);
}

class FakeShareConn {
  constructor(socket, token) {
    this.socket = socket;
    this.token = token;
    this.received = [];
    this.inputText = "";
    this.onInput = null;
    this.buffer = Buffer.alloc(0);
    this.fragments = [];
    this.fragmentOpcode = 0;
    socket.on("data", (chunk) => this.consume(chunk));
    socket.on("error", () => {});
  }

  consume(chunk) {
    this.buffer = Buffer.concat([this.buffer, chunk]);
    for (;;) {
      const frame = this.readFrame();
      if (!frame) return;
      this.handleFrame(frame);
    }
  }

  readFrame() {
    const buf = this.buffer;
    if (buf.length < 2) return null;
    const opcode = buf[0] & 0x0f;
    const masked = (buf[1] & 0x80) !== 0;
    let length = buf[1] & 0x7f;
    let offset = 2;
    if (length === 126) {
      if (buf.length < 4) return null;
      length = buf.readUInt16BE(2);
      offset = 4;
    } else if (length === 127) {
      if (buf.length < 10) return null;
      length = Number(buf.readBigUInt64BE(2));
      offset = 10;
    }
    let mask = null;
    if (masked) {
      if (buf.length < offset + 4) return null;
      mask = buf.subarray(offset, offset + 4);
      offset += 4;
    }
    if (buf.length < offset + length) return null;
    const payload = Buffer.from(buf.subarray(offset, offset + length));
    if (mask) for (let i = 0; i < payload.length; i++) payload[i] ^= mask[i % 4];
    this.buffer = buf.subarray(offset + length);
    return { opcode, payload };
  }

  handleFrame({ opcode, payload }) {
    if (opcode === 0x0) {
      this.fragments.push(payload);
      return;
    }
    if (opcode === 0x1 || opcode === 0x2) {
      this.fragments = [payload];
      this.fragmentOpcode = opcode;
      this.deliver(opcode, payload);
      return;
    }
    if (opcode === 0x8) {
      this.socket.end(encodeFrame(0x8, payload.subarray(0, 2)));
      return;
    }
    if (opcode === 0x9) {
      this.socket.write(encodeFrame(0xa, payload));
    }
  }

  deliver(opcode, payload) {
    this.received.push({ opcode, payload });
    this.inputText += payload.toString("utf8");
    this.onInput?.(payload);
  }

  sendText(frame) {
    this.socket.write(encodeFrame(0x1, Buffer.from(JSON.stringify(frame), "utf8")));
  }

  sendBinary(payload) {
    this.socket.write(encodeFrame(0x2, payload));
  }

  close(code, reason) {
    const payload = Buffer.alloc(2 + Buffer.byteLength(reason));
    payload.writeUInt16BE(code, 0);
    payload.write(reason, 2);
    this.socket.end(encodeFrame(0x8, payload));
  }

  destroyAbnormal() {
    this.socket.destroy();
  }
}

function startFakeShareServer() {
  const connections = [];
  const server = http.createServer((req, res) => {
    res.writeHead(426, { "Content-Type": "text/plain" });
    res.end("websocket upgrade required");
  });
  server.on("upgrade", (req, socket) => {
    const key = req.headers["sec-websocket-key"];
    const accept = crypto.createHash("sha1").update(`${key}${WS_GUID}`).digest("base64");
    socket.write(
      "HTTP/1.1 101 Switching Protocols\r\n" +
        "Upgrade: websocket\r\n" +
        "Connection: Upgrade\r\n" +
        `Sec-WebSocket-Accept: ${accept}\r\n\r\n`,
    );
    const match = /^\/share\/public\/([^/?]+)/.exec(req.url || "");
    const token = match ? decodeURIComponent(match[1]) : "";
    const conn = new FakeShareConn(socket, token);
    connections.push(conn);
    if (token === "expired-token") {
      conn.sendText({ type: "error", code: "expired", message: "分享授权已过期" });
      conn.close(1008, "share terminated");
      return;
    }
    if (token === "offline-token") {
      conn.sendText({ type: "error", code: "disconnected", message: "设备代理离线" });
      conn.close(1008, "share terminated");
      return;
    }
    const permission = token === "rw-token" ? "read_write" : "read";
    conn.sendText({ type: "ready", session_id: "fake-session-1", permission, expires_at: Date.now() + 3600_000 });
    conn.sendBinary(Buffer.from("PUBLIC-SHARE-READY-42\r\n$ "));
    conn.onInput = (payload) => {
      if (permission === "read_write") {
        conn.sendBinary(Buffer.concat([Buffer.from("echo:"), payload]));
      }
    };
    if (token === "ended-token") setTimeout(() => conn.close(1000, "share ended"), 300);
    if (token === "drop-token") setTimeout(() => conn.destroyAbnormal(), 300);
  });
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address();
      resolve({
        port,
        connections,
        forToken: (token) => connections.filter((conn) => conn.token === token),
        close: () => new Promise((done) => server.close(() => done())),
      });
    });
  });
}

async function screenshot(page, name) {
  const shot = await page.send("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(path.join(OUT, name), Buffer.from(shot.data, "base64"));
  return name;
}

async function setViewport(page, { width, height, touch }) {
  await page.send("Emulation.setDeviceMetricsOverride", {
    width,
    height,
    deviceScaleFactor: 2,
    mobile: touch,
  });
  await page.send("Emulation.setTouchEmulationEnabled", { enabled: touch, maxTouchPoints: touch ? 5 : 1 });
}

async function bootShare(page, fake, token, viewport) {
  await setViewport(page, viewport);
  const { identifier } = await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `
      // 验收只关心布局/文案/输入, 强制 DOM renderer: WebGL 渲染不出 .xterm-rows。
      const original = HTMLCanvasElement.prototype.getContext;
      HTMLCanvasElement.prototype.getContext = function (type, ...args) {
        if (String(type).includes("webgl")) return null;
        return original.call(this, type, ...args);
      };
    `,
  });
  try {
    await page.navigate(`${VITE}/share/public/${token}?api=http://127.0.0.1:${fake.port}`);
    await page.waitFor("Boolean(document.querySelector('.xterm .xterm-rows'))");
  } finally {
    await page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier });
  }
}

async function noPageOverflow(page) {
  return page.evaluate(
    "document.documentElement.scrollWidth <= window.innerWidth + 1 && document.body.scrollWidth <= window.innerWidth + 1",
  );
}

async function bodyHasButton(page, text) {
  return page.evaluate(
    `[...document.querySelectorAll('button')].some((b) => b.textContent?.trim() === ${JSON.stringify(text)})`,
  );
}

async function clickButton(page, text) {
  const clicked = await page.evaluate(`(() => {
    const button = [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === ${JSON.stringify(text)});
    if (!button) return false;
    button.click();
    return true;
  })()`);
  if (!clicked) throw new Error(`button not found: ${text}`);
}

async function waitForConnectionCount(fake, token, minimum, timeout = 10_000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    const count = fake.forToken(token).length;
    if (count >= minimum) return count;
    await sleep(100);
  }
  throw new Error(`timed out waiting for ${minimum} connections on ${token}`);
}

async function waitForConnectionInput(fake, token, text, timeout = 10_000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (fake.forToken(token).some((conn) => conn.inputText.includes(text))) return;
    await sleep(100);
  }
  throw new Error(`timed out waiting for input ${JSON.stringify(text)} on ${token}`);
}

async function focusTerminal(page) {
  await page.waitFor("Boolean(document.querySelector('.xterm-helper-textarea'))");
  await page.evaluate("document.querySelector('.xterm-helper-textarea').focus()");
}

async function typeText(page, text) {
  for (const ch of text) {
    if (ch === "\n") {
      await page.send("Input.dispatchKeyEvent", { type: "rawKeyDown", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13, nativeVirtualKeyCode: 13 });
      await page.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13, nativeVirtualKeyCode: 13 });
    } else {
      await page.send("Input.dispatchKeyEvent", { type: "keyDown", key: ch, text: ch });
      await page.send("Input.dispatchKeyEvent", { type: "keyUp", key: ch });
    }
    await sleep(25);
  }
}

async function publicShareAcceptance(page, fake) {
  for (const spec of [
    { id: "ro-320-layout", width: 320, height: 568, touch: true },
    { id: "ro-390-layout", width: 390, height: 844, touch: true },
    { id: "ro-desktop-layout", width: 1280, height: 800, touch: false },
  ]) {
    await pass(spec.id, async () => {
      await bootShare(page, fake, "ro-token", spec);
      await page.waitFor("[...document.querySelectorAll('header span')].some((el) => el.textContent?.trim() === '只读')");
      await page.waitFor("document.querySelector('.xterm .xterm-rows')?.innerText.includes('PUBLIC-SHARE-READY-42')");
      assert.equal(await noPageOverflow(page), true, `页面不允许横向溢出@${spec.width}`);
      assert.equal(await page.evaluate("document.querySelector('.xterm').getBoundingClientRect().width > 250"), true, "终端必须可见");
      await screenshot(page, `${spec.id}.png`);
      return { width: spec.width };
    });
  }

  await pass("ro-input-blocked", async () => {
    await bootShare(page, fake, "ro-token", { width: 390, height: 844, touch: true });
    await page.waitFor("document.querySelector('.xterm .xterm-rows')?.innerText.includes('PUBLIC-SHARE-READY-42')");
    const before = fake.forToken("ro-token").reduce((sum, conn) => sum + conn.received.length, 0);
    await focusTerminal(page);
    await typeText(page, "evil\n");
    await sleep(400);
    const after = fake.forToken("ro-token").reduce((sum, conn) => sum + conn.received.length, 0);
    assert.equal(after, before, "只读分享不得向服务端发送任何输入帧");
  });

  await pass("rw-input-echo", async () => {
    await bootShare(page, fake, "rw-token", { width: 1280, height: 800, touch: false });
    await page.waitFor("[...document.querySelectorAll('header span')].some((el) => el.textContent?.trim() === '可读写')");
    await page.waitFor("document.querySelector('.xterm .xterm-rows')?.innerText.includes('PUBLIC-SHARE-READY-42')");
    await focusTerminal(page);
    await typeText(page, "echo hi\n");
    // xterm 逐键产生 onData, 服务端逐帧回显 "echo:<单键>"; 整串断言只看服务端聚合输入。
    await waitForConnectionInput(fake, "rw-token", "echo hi\r");
    await page.waitFor("document.querySelector('.xterm .xterm-rows')?.innerText.includes('echo:h')");
    const binary = fake.forToken("rw-token").some((conn) => conn.received.some((frame) => frame.opcode === 0x2 && frame.payload.length > 0));
    assert.equal(binary, true, "read_write 输入必须以二进制帧到达服务端");
  });

  await pass("expired-copy", async () => {
    await bootShare(page, fake, "expired-token", { width: 390, height: 844, touch: true });
    await page.waitFor("document.body.innerText.includes('分享链接已过期')");
    assert.equal(await bodyHasButton(page, "重新连接"), false, "过期状态不提供重试");
    await screenshot(page, "expired-copy.png");
  });

  await pass("offline-retry", async () => {
    await bootShare(page, fake, "offline-token", { width: 1280, height: 800, touch: false });
    await page.waitFor("document.body.innerText.includes('设备当前离线')");
    const before = fake.forToken("offline-token").length;
    await clickButton(page, "重新连接");
    const after = await waitForConnectionCount(fake, "offline-token", before + 1);
    assert.ok(after > before, `重试必须建立新连接: ${before} -> ${after}`);
  });

  await pass("ended-copy", async () => {
    await bootShare(page, fake, "ended-token", { width: 390, height: 844, touch: true });
    await page.waitFor("document.body.innerText.includes('分享已结束')");
    assert.equal(await bodyHasButton(page, "重新连接"), false, "正常结束状态不提供重试");
    await screenshot(page, "ended-copy.png");
  });

  await pass("drop-reconnect", async () => {
    await bootShare(page, fake, "drop-token", { width: 1280, height: 800, touch: false });
    await page.waitFor("document.body.innerText.includes('连接已断开')");
    const before = fake.forToken("drop-token").length;
    await clickButton(page, "重新连接");
    const after = await waitForConnectionCount(fake, "drop-token", before + 1);
    assert.ok(after > before, `断线重连必须建立新连接: ${before} -> ${after}`);
  });

  await pass("no-token-persisted", async () => {
    await bootShare(page, fake, "ro-token", { width: 390, height: 844, touch: true });
    await page.waitFor("document.querySelector('.xterm .xterm-rows')?.innerText.includes('PUBLIC-SHARE-READY-42')");
    const stored = await page.evaluate(
      "JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage } })",
    );
    assert.equal(stored.includes("ro-token"), false, `token 不得写入 web storage: ${stored}`);
  });
}

let vite;
let chrome;
let page;
let fake;
try {
  fake = await startFakeShareServer();
  [vite, chrome] = await Promise.all([startVite(), startChrome()]);
  page = await newPage(chrome);
  await publicShareAcceptance(page, fake);
} catch (error) {
  harnessErrors.push(String(error?.stack || error));
} finally {
  if (page) page.close();
  stop(chrome?.process);
  await vite?.stop();
  if (fake) await fake.close();
  if (chrome?.profile) fs.rmSync(chrome.profile, { recursive: true, force: true });
  wipeViteCache();
}

const checks = [...results.values()];
const failed = checks.filter((check) => check.status !== "passed");
const report = {
  schema_version: 1,
  status: failed.length || harnessErrors.length ? "failed" : "passed",
  browser: chrome?.version || { status: "unavailable" },
  backend: "fake in-process WS server speaking the M138 viewer contract; NOT production backend evidence",
  execution: {
    real_browser: true,
    headless: true,
    jsdom: false,
    matrix: "320x568 / 390x844 / 1280x800, touch + desktop",
  },
  checks,
  harness_errors: harnessErrors,
};
fs.writeFileSync(path.join(OUT, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
console.warn(`public-share acceptance: ${checks.filter((check) => check.status === "passed").length}/${checks.length} checks passed; report=${path.join(OUT, "report.json")}`);
if (failed.length || harnessErrors.length) process.exit(1);
