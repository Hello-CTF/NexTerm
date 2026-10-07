#!/usr/bin/env node
// FLEET168 一键安装指令 + 设备终端公开链接创建响应式验收: 真实 headless
// Chromium + vite dev server。后端是本进程内的 fake (HTTP + WS bridge): HTTP
// 侧按 internal/fleet/server/sharing_http.go 与 http.go 的 /share/links、
// /fleet/*、/healthz 真实合同伪造, WS 侧仅复述 supervisor v2 二进制合同
// (hello/create/attach/input/resize/killSession/detach) — 全部明确标注为
// fake, 不是生产后端证据; 真实后端证据归 scripts/e2e-device-local.py 与
// scripts/e2e-sharing-local.py。覆盖: 320/390/1280 响应式、一键安装指令
// (真实 healthz 版本 + 既有命令保留 + 版本缺失隐藏)、公开链接创建 (默认
// 只读/显式 read_write/TTL 边界/一次性 URL/错误态/账号切换取消在途创建/
// token 不落 web storage)。
import { spawn, spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import crypto from "node:crypto";
import fs from "node:fs";
import http from "node:http";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { freePort, startVite as startViteProcess, stopProcess, waitHttp } from "./lib/acceptance-process.mjs";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const OUT = path.join(ROOT, "target/fleet-install-share-acceptance");
const results = new Map();
const harnessErrors = [];
const pageErrors = [];

// 共享 /tmp 可能被其他任务填满: 本脚本与子进程一律使用私有临时目录。
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
    try {
      const shot = await globalThis.__page?.send("Page.captureScreenshot", { format: "png" });
      if (shot) fs.writeFileSync(path.join(OUT, `failed-${id}.png`), Buffer.from(shot.data, "base64"));
      const text = await globalThis.__page?.evaluate("document.body.textContent.slice(0, 1200)");
      if (text) fs.writeFileSync(path.join(OUT, `failed-${id}.txt`), String(text));
    } catch {}
    record(id, "failed", { error: String(error?.stack || error) });
  }
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function waitPortClosed(port, timeout = 10_000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const open = await new Promise((resolve) => {
      const socket = net.connect({ host: "127.0.0.1", port });
      socket.once("connect", () => {
        socket.destroy();
        resolve(true);
      });
      socket.once("error", () => resolve(false));
    });
    if (!open) return true;
    if (Date.now() >= deadline) return false;
    await sleep(100);
  }
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

  on(method, handler) {
    const list = this.eventHandlers.get(method) ?? [];
    list.push(handler);
    this.eventHandlers.set(method, list);
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
    stopProcess(process);
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

function wipeViteCache() {
  fs.rmSync(viteCacheDir(), { recursive: true, force: true });
}

let viteUrl = "";

async function startVite(options = {}) {
  wipeViteCache();
  const handle = await startViteProcess({ root: ROOT, ...options });
  viteUrl = handle.origin;
  return handle;
}

// ---------- fake backend (NOT production backend evidence) ----------

const WS_GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11";
const SUPERVISOR = {
  hello: 1, helloAck: 2, error: 3, create: 4, created: 5, attach: 8, attached: 9,
  input: 10, inputAck: 11, resize: 12, ok: 13, kill: 14, detach: 18, output: 19, exit: 20, fatal: 21, killSession: 22,
};
const DIGEST = "digest-fleet-install-share-fake";
const SESSION = {
  id: "fake-session-168",
  created_at: "2026-10-07T05:34:56.123456789+08:00",
  incarnation: "fake-inc-168",
  cols: 80,
  rows: 24,
  dead: false,
};
const LINK_TOKEN = "fake-one-time-token-168";
const ENROLL_CODE = "fake-enroll-168";

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

function supervisorFrame(kind, payload) {
  const header = Buffer.alloc(5);
  header.writeUInt32BE(payload.length, 0);
  header[4] = kind;
  return encodeFrame(0x2, Buffer.concat([header, payload]));
}

function supervisorJSON(kind, message) {
  return supervisorFrame(kind, Buffer.from(JSON.stringify(message), "utf8"));
}

function supervisorOutput(seq, data) {
  const payload = Buffer.alloc(8 + data.length);
  payload.writeBigUInt64BE(seq, 0);
  data.copy(payload, 8);
  return supervisorFrame(SUPERVISOR.output, payload);
}

// sessionStore 记录会话的全部输出字节, attach 时从 seq 0 重放 (对齐 supervisor
// attachment 的 replay 语义); 每个 attach 连接维护自己的发送序号。
const sessionStore = {
  buffer: Buffer.from("fake-shell-ready $ "),
  sessionId: SESSION.id,
  hellos: [],
  creates: [],
  inputs: [],
  resizes: [],
  killSessions: [],
  attaches: [],
};

class FakeBridgeConn {
  constructor(socket, deviceId) {
    this.socket = socket;
    this.deviceId = deviceId;
    this.buffer = Buffer.alloc(0);
    this.nextSeq = 0n;
    this.attached = false;
    socket.on("data", (chunk) => this.consume(chunk));
    socket.on("error", () => {});
  }

  consume(chunk) {
    this.buffer = Buffer.concat([this.buffer, chunk]);
    for (;;) {
      const frame = this.readWSFrame();
      if (!frame) return;
      this.handleWSFrame(frame);
    }
  }

  readWSFrame() {
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

  handleWSFrame({ opcode, payload }) {
    if (opcode === 0x8) {
      this.socket.end(encodeFrame(0x8, payload.subarray(0, 2)));
      return;
    }
    if (opcode === 0x9) {
      this.socket.write(encodeFrame(0xa, payload));
      return;
    }
    if (opcode !== 0x2) return;
    if (payload.length < 5) return;
    const length = payload.readUInt32BE(0);
    if (payload.length < 5 + length) return;
    const kind = payload[4];
    const body = payload.subarray(5, 5 + length);
    this.handleSupervisor(kind, body);
  }

  handleSupervisor(kind, body) {
    switch (kind) {
      case SUPERVISOR.hello: {
        const hello = JSON.parse(body.toString("utf8"));
        sessionStore.hellos.push(hello);
        if (hello.version !== 2 || hello.state_digest !== DIGEST) {
          this.sendJSON(SUPERVISOR.error, { code: "state_mismatch", message: "fake bridge: digest mismatch" });
          this.socket.end(encodeFrame(0x8, Buffer.from([0x03, 0xf0])));
          return;
        }
        this.sendJSON(SUPERVISOR.helloAck, { version: 2 });
        return;
      }
      case SUPERVISOR.create: {
        const msg = JSON.parse(body.toString("utf8"));
        sessionStore.creates.push(msg);
        sessionStore.sessionId = msg.id ?? SESSION.id;
        this.sendJSON(SUPERVISOR.created, { ...SESSION, id: sessionStore.sessionId });
        return;
      }
      case SUPERVISOR.attach: {
        const msg = JSON.parse(body.toString("utf8"));
        if (msg.id !== sessionStore.sessionId) {
          this.sendJSON(SUPERVISOR.error, { code: "not_found", message: "fake bridge: no such session" });
          return;
        }
        sessionStore.attaches.push(msg);
        this.attached = true;
        this.nextSeq = 0n;
        this.sendJSON(SUPERVISOR.attached, { ...SESSION, id: msg.id });
        this.sendOutput(Buffer.from(sessionStore.buffer));
        return;
      }
      case SUPERVISOR.input: {
        const text = body.toString("utf8");
        sessionStore.inputs.push(text);
        this.sendJSON(SUPERVISOR.inputAck, { written: body.length });
        return;
      }
      case SUPERVISOR.resize: {
        const msg = JSON.parse(body.toString("utf8"));
        sessionStore.resizes.push(msg);
        this.sendJSON(SUPERVISOR.ok, {});
        return;
      }
      case SUPERVISOR.kill:
      case SUPERVISOR.killSession: {
        const msg = JSON.parse(body.toString("utf8"));
        sessionStore.killSessions.push(msg);
        this.sendJSON(SUPERVISOR.ok, {});
        return;
      }
      case SUPERVISOR.detach:
        this.sendJSON(SUPERVISOR.ok, {});
        this.socket.end(encodeFrame(0x8, Buffer.from([0x03, 0xe8])));
        return;
      default:
        this.sendJSON(SUPERVISOR.error, { code: "protocol", message: `fake bridge: unexpected ${kind}` });
    }
  }

  sendOutput(data) {
    for (let offset = 0; offset < data.length; offset += 32 * 1024) {
      const chunk = data.subarray(offset, Math.min(offset + 32 * 1024, data.length));
      this.socket.write(supervisorOutput(this.nextSeq, chunk));
      this.nextSeq += BigInt(chunk.length);
    }
  }

  sendJSON(kind, message) {
    this.socket.write(supervisorJSON(kind, message));
  }
}

const liveConns = [];

const NOW = Date.now();
const DEVICES = {
  devices: [
    {
      id: "d-1",
      name: "fleet-share-box",
      kind: "agent",
      created_at: NOW - 86400_000,
      last_seen_at: NOW - 20_000,
      revoked_at: 0,
      agent: {
        platform: "linux",
        app_version: "0.2.2",
        desired_autostart: true,
        terminal_enabled: true,
        current_url: "https://nexterm.example.com",
        service_state: { installed: true, enabled: true, active: true, last_reconcile_at: NOW - 60_000 },
        last_seen_at: NOW - 20_000,
        state_digest: DIGEST,
      },
    },
  ],
};

const BASE_URLS = {
  base_urls: [{ url: "https://nexterm.example.com" }, { url: "http://10.0.0.8:8080", insecure: true }],
};

const SESSION_ME = {
  user: {
    id: "u-1",
    username: "alice",
    display_name: "Alice",
    role: "superadmin",
    state: "active",
    must_change_password: false,
    created_at: 1,
    updated_at: 1,
    last_login_at: 1,
  },
  csrf_token: "csrf-fleet-install-share-fake",
};

// shareBehavior 控制 POST /share/links: mode=ok 立即成功, fail 返回 500,
// hang 挂起在途创建 (账号切换取消路径, 由验收显式 release 放行晚到响应),
// 由验收用例逐个切换。
const shareBehavior = { mode: "ok", release: null };
const shareCreates = [];

// healthBehavior.mode: ok 给真实版本, fail 模拟 /healthz 不可用。
const healthBehavior = { mode: "ok" };

function corsJSON(res, payload, status = 200) {
  const body = JSON.stringify(payload);
  res.writeHead(status, {
    "content-type": "application/json; charset=utf-8",
    "access-control-allow-origin": "*",
    "access-control-allow-methods": "GET,POST,PUT,DELETE,OPTIONS",
    "access-control-allow-headers": "content-type,x-nexterm-csrf",
    "cache-control": "no-store",
  });
  res.end(body);
}

async function startFakeBackend() {
  const server = http.createServer(async (req, res) => {
    if (req.method === "OPTIONS") {
      res.writeHead(204, {
        "access-control-allow-origin": "*",
        "access-control-allow-methods": "GET,POST,PUT,DELETE,OPTIONS",
        "access-control-allow-headers": "content-type,x-nexterm-csrf",
        "access-control-max-age": "600",
      });
      res.end();
      return;
    }
    const pathName = new URL(req.url || "/", "http://x").pathname;
    if (pathName === "/auth/status") return corsJSON(res, { initialized: true, registration_open: false, auth: "on" });
    if (pathName === "/auth/me") return corsJSON(res, SESSION_ME);
    if (pathName === "/fleet/devices") return corsJSON(res, DEVICES);
    if (pathName === "/fleet/base-urls") return corsJSON(res, BASE_URLS);
    if (pathName === "/fleet/devices/d-1/metrics") return corsJSON(res, { samples: [] });
    if (pathName === "/healthz") {
      if (healthBehavior.mode === "fail") {
        return corsJSON(res, { error: { code: "internal", message: "fake: healthz down" } }, 500);
      }
      return corsJSON(res, { ok: true, service: "nexterm-server", version: "0.2.2" });
    }
    if (pathName === "/device/enroll-codes" && req.method === "POST") {
      return corsJSON(res, { code: ENROLL_CODE, expires_at: Date.now() + 900_000 });
    }
    if (pathName === "/share/links" && req.method === "POST") {
      let raw = "";
      for await (const chunk of req) raw += chunk;
      const body = JSON.parse(raw || "{}");
      shareCreates.push(body);
      if (shareBehavior.mode === "hang") {
        // 账号切换取消路径: 响应挂起直到验收 release (晚到响应) 或 socket 关闭。
        await new Promise((resolve) => {
          shareBehavior.release = resolve;
          res.once("close", resolve);
        });
        shareBehavior.release = null;
      }
      if (shareBehavior.mode === "fail") {
        return corsJSON(res, { error: { code: "internal", message: "fake: 数据库错误" } }, 500);
      }
      return corsJSON(res, {
        id: `link-${shareCreates.length}`,
        owner_id: "u-1",
        device_id: body.device_id,
        session_id: body.session_id,
        permission: body.write ? "read_write" : "read",
        created_at: Date.now(),
        expires_at: Date.now() + body.ttl_ms,
        token: LINK_TOKEN,
      });
    }
    if (/^\/share\/links\/[^/]+\/revoke$/.test(pathName) && req.method === "POST") {
      return corsJSON(res, { ok: true });
    }
    if (pathName === "/rpc") {
      let raw = "";
      for await (const chunk of req) raw += chunk;
      let cmd = "";
      try {
        cmd = JSON.parse(raw || "{}").cmd;
      } catch {}
      const data =
        cmd === "session_list" ? [] :
        cmd === "vault_status" ? { initialized: true, unlocked: true } :
        cmd === "asset_list" ? [] :
        cmd === "group_list" ? [] :
        cmd === "layout_get" ? { revision: 0, updatedAt: 0, data: null } :
        cmd === "layout_put" ? { saved: true, revision: 1, conflict: false } :
        null;
      return corsJSON(res, { ok: true, data });
    }
    return corsJSON(res, { error: { code: "not_found", message: `fake: ${req.method} ${pathName}` } }, 404);
  });
  const liveSockets = new Set();
  server.on("upgrade", (req, socket) => {
    liveSockets.add(socket);
    socket.on("close", () => liveSockets.delete(socket));
    const key = req.headers["sec-websocket-key"];
    const accept = crypto.createHash("sha1").update(`${key}${WS_GUID}`).digest("base64");
    socket.write(
      "HTTP/1.1 101 Switching Protocols\r\n" +
        "Upgrade: websocket\r\n" +
        "Connection: Upgrade\r\n" +
        `Sec-WebSocket-Accept: ${accept}\r\n\r\n`,
    );
    const bridge = /^\/fleet\/devices\/([^/]+)\/bridge/.exec(req.url || "");
    if (bridge) {
      const conn = new FakeBridgeConn(socket, decodeURIComponent(bridge[1]));
      liveConns.push(conn);
      socket.on("close", () => {
        const index = liveConns.indexOf(conn);
        if (index >= 0) liveConns.splice(index, 1);
      });
      return;
    }
    // /ws/events 等其余升级: 接受后挂起不应答 (验收不依赖事件流)
  });
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address();
      resolve({
        port,
        close: () =>
          new Promise((done) => {
            for (const socket of liveSockets) socket.destroy();
            server.close(() => done());
          }),
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

async function bootApp(page, fake, viewport) {
  await setViewport(page, viewport);
  const { identifier } = await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `
      try { localStorage.clear(); } catch {}
      window.__NEXTERM_TRANSPORT__ = "web";
      const original = HTMLCanvasElement.prototype.getContext;
      HTMLCanvasElement.prototype.getContext = function (type, ...args) {
        if (String(type).includes("webgl")) return null;
        return original.call(this, type, ...args);
      };
    `,
  });
  try {
    await page.navigate(`${viteUrl}/?api=http://127.0.0.1:${fake.port}`);
    await page.waitFor("!!document.querySelector('.nx-app')");
  } finally {
    await page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier });
  }
}

async function openDevicesTab(page) {
  await page.evaluate(`(async () => {
    const { useUi } = await import('/src/app/store.ts');
    useUi.getState().addTab({ id: 'devices-fleet168', kind: 'devices', title: '设备管理', closable: true });
    useUi.getState().setActiveTab('devices-fleet168');
    return true;
  })()`);
  await page.waitFor(`document.body.textContent.includes("fleet-share-box")`);
}

async function clickButton(page, text) {
  const clicked = await page.evaluate(`(() => {
    const button = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === ${JSON.stringify(text)});
    if (!button) return "no-button";
    if (button.disabled) return "disabled";
    button.click();
    return "clicked";
  })()`);
  if (clicked !== "clicked") throw new Error(`button not clickable: ${text} (${clicked})`);
}

async function noPageOverflow(page) {
  return page.evaluate(
    "document.documentElement.scrollWidth <= window.innerWidth + 1 && document.body.scrollWidth <= window.innerWidth + 1",
  );
}

// TERMINAL_READY: 「结束终端」按钮只在 ready 出现。
const TERMINAL_READY = `[...document.querySelectorAll("button")].some((b) => b.textContent?.includes("结束终端"))`;

async function openDeviceTerminal(page) {
  const clicked = await page.evaluate(`(() => {
    const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("fleet-share-box"));
    if (!card) return "no-card";
    const button = [...card.querySelectorAll("button")].find((b) => b.textContent?.trim() === "终端");
    if (!button) return "no-button";
    if (button.disabled) return "disabled";
    button.click();
    return "clicked";
  })()`);
  if (clicked !== "clicked") throw new Error(`terminal button not clickable: ${clicked}`);
  await page.waitFor(TERMINAL_READY);
}

async function selectShareTtl(page, ms) {
  const selected = await page.evaluate(`(() => {
    const select = document.querySelector('select[aria-label="链接有效期"]');
    if (!select) return "no-select";
    const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value").set;
    setter.call(select, ${JSON.stringify(String(ms))});
    select.dispatchEvent(new Event("change", { bubbles: true }));
    return "ok";
  })()`);
  if (selected !== "ok") throw new Error(`ttl select failed: ${selected}`);
}

async function toggleShareWrite(page) {
  const toggled = await page.evaluate(`(() => {
    const box = [...document.querySelectorAll('input[type="checkbox"]')].find((c) => c.closest("label")?.textContent?.includes("允许读写"));
    if (!box) return "no-checkbox";
    box.click();
    return "ok";
  })()`);
  if (toggled !== "ok") throw new Error(`write toggle failed: ${toggled}`);
}

async function installShareAcceptance(page, fake) {
  await pass("install-one-liner-320", async () => {
    await bootApp(page, fake, { width: 320, height: 568, touch: true });
    await openDevicesTab(page);
    await clickButton(page, "接入新设备");
    await page.waitFor(`!!document.querySelector('button[aria-expanded="true"]')`);
    await clickButton(page, "签发接入码");
    await page.waitFor(`document.body.textContent.includes(${JSON.stringify(ENROLL_CODE)})`);
    // 一键安装指令: 真实 /healthz 版本 + 每个接入地址一行, --insecure 跟随配置
    const oneLiners = await page.evaluate(`[...document.querySelectorAll("code")]
      .map((c) => c.textContent ?? "")
      .filter((c) => c.includes("install-device.sh"))`);
    assert.equal(oneLiners.length, 2, `每个接入地址一行一键安装指令: ${JSON.stringify(oneLiners)}`);
    assert.ok(
      oneLiners[0] ===
        "curl -fsSL 'https://nexterm.example.com/install-device.sh' | sh -s -- " +
          "--server 'https://nexterm.example.com' --code 'fake-enroll-168' --version '0.2.2'",
      `一键安装指令构造: ${oneLiners[0]}`,
    );
    assert.ok(oneLiners[1].includes("--server 'http://10.0.0.8:8080'"), "第二接入地址");
    assert.ok(oneLiners[1].includes("--insecure"), "insecure 接入地址追加 --insecure");
    // 既有命令 (已装二进制) 保留, 且绝不声称安装成功
    const text = await page.evaluate("document.body.textContent");
    assert.ok(text.includes("nexterm-server agent enroll"), "enroll 命令保留");
    assert.ok(text.includes("nexterm-server agent install"), "install 命令保留");
    assert.ok(text.includes("不会替你安装") && text.includes("不会感知安装是否成功"), "不声称安装成功");
    assert.equal(await noPageOverflow(page), true, "320px 下页面不得横向溢出");
    await screenshot(page, "install-one-liner-320.png");
  });

  await pass("install-one-liner-hidden-without-version", async () => {
    healthBehavior.mode = "fail";
    await bootApp(page, fake, { width: 390, height: 844, touch: true });
    await openDevicesTab(page);
    await clickButton(page, "接入新设备");
    await page.waitFor(`!!document.querySelector('button[aria-expanded="true"]')`);
    await clickButton(page, "签发接入码");
    await page.waitFor(`document.body.textContent.includes(${JSON.stringify(ENROLL_CODE)})`);
    const leaked = await page.evaluate(`[...document.querySelectorAll("code")]
      .map((c) => c.textContent ?? "")
      .some((c) => c.includes("install-device.sh") || c.includes("--version"))`);
    assert.equal(leaked, false, "版本不可用时一键安装指令整段隐藏, 不伪造版本");
    const text = await page.evaluate("document.body.textContent");
    assert.ok(text.includes("nexterm-server agent enroll"), "既有 enroll 命令不受版本缺失影响");
    healthBehavior.mode = "ok";
    await screenshot(page, "install-one-liner-hidden-390.png");
  });

  await pass("share-create-default-readonly-320", async () => {
    await bootApp(page, fake, { width: 320, height: 568, touch: true });
    await openDevicesTab(page);
    await openDeviceTerminal(page);
    await clickButton(page, "分享");
    await page.waitFor(`!!document.querySelector('select[aria-label="链接有效期"]')`);
    const createsBefore = shareCreates.length;
    await clickButton(page, "创建公开链接");
    await page.waitFor(`document.body.textContent.includes(${JSON.stringify(LINK_TOKEN)})`);
    assert.equal(shareCreates.length, createsBefore + 1, "创建走真实 POST /share/links");
    const body = shareCreates[shareCreates.length - 1];
    assert.equal(body.device_id, "d-1", "device_id");
    assert.equal(body.session_id, sessionStore.sessionId, "session_id 绑定当前 attached 会话");
    assert.equal(body.write, false, "默认只读");
    assert.equal(body.ttl_ms, 3_600_000, "默认 1 小时 (服务端 defaultLinkTTL)");
    const text = await page.evaluate("document.body.textContent");
    assert.ok(text.includes(`http://127.0.0.1:${fake.port}/share/public/${LINK_TOKEN}`), "一次性公开 URL 展示");
    assert.ok(text.includes("只显示这一次"), "一次性提示");
    assert.ok(text.includes("设置 → 分享") && text.includes("吊销"), "吊销指引");
    const stored = await page.evaluate("JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage } })");
    assert.equal(stored.includes(LINK_TOKEN), false, `token 不得写入 web storage: ${stored}`);
    assert.equal(await noPageOverflow(page), true, "320px 下页面不得横向溢出");
    await screenshot(page, "share-created-320.png");
  });

  await pass("share-create-readwrite-ttl-390", async () => {
    // 上一个用例已创建一次: 重新打开面板 (一次性 URL 已随面板关闭清除)
    await clickButton(page, "完成");
    await clickButton(page, "分享");
    await page.waitFor(`!!document.querySelector('select[aria-label="链接有效期"]')`);
    await setViewport(page, { width: 390, height: 844, touch: true });
    await selectShareTtl(page, 24 * 60 * 60_000);
    await toggleShareWrite(page);
    const createsBefore = shareCreates.length;
    await clickButton(page, "创建公开链接");
    await page.waitFor(`document.body.textContent.includes("读写")`);
    assert.equal(shareCreates.length, createsBefore + 1, "read_write 创建到达 fake");
    const body = shareCreates[shareCreates.length - 1];
    assert.equal(body.write, true, "显式 read_write");
    assert.equal(body.ttl_ms, 86_400_000, "显式 TTL (24 小时, 服务端边界内)");
    assert.equal(await noPageOverflow(page), true, "390px 下页面不得横向溢出");
    await screenshot(page, "share-readwrite-390.png");
  });

  await pass("share-create-error-state", async () => {
    shareBehavior.mode = "fail";
    await clickButton(page, "完成");
    await clickButton(page, "分享");
    await page.waitFor(`!!document.querySelector('select[aria-label="链接有效期"]')`);
    await clickButton(page, "创建公开链接");
    await page.waitFor(`document.body.textContent.includes("创建公开链接失败")`);
    const text = await page.evaluate("document.body.textContent");
    assert.ok(text.includes("数据库错误"), "服务端错误 message 展示");
    assert.equal(text.includes("/share/public/"), false, "失败时不得展示任何 URL");
    shareBehavior.mode = "ok";
    await screenshot(page, "share-error-390.png");
  });

  await pass("share-create-cancelled-on-account-switch", async () => {
    shareBehavior.mode = "hang";
    await clickButton(page, "分享"); // 关闭错误面板
    await clickButton(page, "分享"); // 重新打开
    await page.waitFor(`!!document.querySelector('select[aria-label="链接有效期"]')`);
    await clickButton(page, "创建公开链接");
    await page.waitFor(`document.body.textContent.includes("创建中…")`);
    // 账号切换: DeviceTerminalBoundary (M156) 关闭全部 deviceTerminal 标签,
    // 视图卸载; 在途创建的晚到响应不得写入 (token 永不出现)。
    await page.evaluate(`(async () => {
      const { useAuth } = await import('/src/features/auth/store.ts');
      useAuth.setState({ user: { ...useAuth.getState().user, id: "u-2", username: "bob" } });
      return true;
    })()`);
    await page.waitFor(`![...document.querySelectorAll("button")].some((b) => b.textContent?.includes("结束终端"))`);
    // 放行晚到响应: 视图已卸载, 一次性 URL/token 不得出现在新账号视图或 web storage。
    shareBehavior.mode = "ok";
    shareBehavior.release?.();
    await sleep(400);
    const text = await page.evaluate("document.body.textContent");
    assert.equal(text.includes(LINK_TOKEN), false, `账号切换后 token 不得出现: ${text.slice(0, 200)}`);
    const stored = await page.evaluate("JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage } })");
    assert.equal(stored.includes(LINK_TOKEN), false, "token 不得写入 web storage");
    // 还原账号, 后续用例不受影响
    await page.evaluate(`(async () => {
      const { useAuth } = await import('/src/features/auth/store.ts');
      useAuth.setState({ user: { ...useAuth.getState().user, id: "u-1", username: "alice" } });
      return true;
    })()`);
  });

  await pass("desktop-1280", async () => {
    await setViewport(page, { width: 1280, height: 800, touch: false });
    await sleep(400);
    const reachable = await page.evaluate(`(() => {
      const buttons = [...document.querySelectorAll(".nx-toolbar")]
        .filter((t) => t.offsetParent !== null)
        .flatMap((t) => [...t.querySelectorAll("button")]);
      return buttons.length > 0 && buttons.every((b) => {
        const r = b.getBoundingClientRect();
        return r.right <= window.innerWidth && r.left >= 0 && r.width > 0;
      });
    })()`);
    assert.equal(reachable, true, "桌面宽度下工具栏按钮必须可达");
    assert.equal(await noPageOverflow(page), true, "1280px 下页面不得横向溢出");
    await screenshot(page, "share-desktop-1280.png");
  });
}

let vite;
let chrome;
let page;
let fake;
try {
  fake = await startFakeBackend();
  vite = await startVite();
  chrome = await startChrome();
  page = await newPage(chrome);
  globalThis.__page = page;
  page.on("Runtime.consoleAPICalled", (params) => {
    if (params.type === "error" || params.type === "warning" || params.type === "log") pageErrors.push(`console.${params.type}: ${params.args?.map((a) => a.value ?? a.description ?? "").join(" ")}`);
  });
  page.on("Runtime.exceptionThrown", (params) => {
    pageErrors.push(params.exceptionDetails?.exception?.description || JSON.stringify(params.exceptionDetails));
  });
  await page.send("Runtime.enable");
  await installShareAcceptance(page, fake);
} catch (error) {
  harnessErrors.push(String(error?.stack || error));
} finally {
  if (page) page.close();
  stopProcess(chrome?.process);
  if (vite) {
    await vite.stop();
    if (!(await waitPortClosed(vite.port, 10_000))) {
      harnessErrors.push(`vite port ${vite.port} still open after stop`);
    }
  }
  if (fake) await fake.close();
  if (chrome?.process) {
    await Promise.race([
      new Promise((resolve) => chrome.process.once("exit", resolve)),
      sleep(3000),
    ]);
  }
  if (chrome?.profile) {
    try {
      fs.rmSync(chrome.profile, { recursive: true, force: true, maxRetries: 8, retryDelay: 250 });
    } catch (error) {
      harnessErrors.push(`profile cleanup: ${String(error?.message || error)}`);
    }
  }
  wipeViteCache();
}

const checks = [...results.values()];
const failed = checks.filter((check) => check.status !== "passed");
if (pageErrors.length) harnessErrors.push(...pageErrors.map((e) => `page exception: ${e}`));
const report = {
  schema_version: 1,
  status: failed.length || harnessErrors.length ? "failed" : "passed",
  browser: chrome?.version || { status: "unavailable" },
  backend: "fake in-process HTTP + WS bridge speaking the /share/links, /fleet/*, /healthz and supervisor v2 contracts; NOT production backend evidence (real evidence: scripts/e2e-device-local.py, scripts/e2e-sharing-local.py)",
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
console.warn(`fleet-install-share acceptance: ${checks.filter((check) => check.status === "passed").length}/${checks.length} checks passed; report=${path.join(OUT, "report.json")}`);
