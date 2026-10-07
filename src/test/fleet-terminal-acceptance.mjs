#!/usr/bin/env node
// FLEET156 设备远程终端响应式验收: 真实 headless Chromium + vite dev server。
// 后端是本进程内的 fake bridge (HTTP + WS): WS 侧仅复述 supervisor v2 二进制合同
// (hello/create/attach/output/input/resize/killSession/detach, 与
// internal/supervisor/protocol.go 对齐), 不是生产后端证据; 真实后端证据归
// scripts/e2e-device-local.py (M136 桥接 + FLEET156 state digest 下发)。
// 覆盖: 320/390/1280 响应式、真实按键输入回显、协议 resize、结束终端确认、
// 断线重连续传 (expect incarnation + backlog 重放)、exit 状态、策略态按钮缺席、
// state digest 不落地。
import { spawn, spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import crypto from "node:crypto";
import fs from "node:fs";
import http from "node:http";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const OUT = path.join(ROOT, "target/fleet-terminal-acceptance");
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
    record(id, "failed", {
      error: String(error?.stack || error),
      bridgeState: {
        hellos: sessionStore.hellos.length,
        creates: sessionStore.creates.length,
        attaches: sessionStore.attaches.length,
        inputs: sessionStore.inputs.length,
        resizes: sessionStore.resizes.length,
        killSessions: sessionStore.killSessions.length,
        liveConns: liveConns.length,
      },
    });
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

function wipeViteCache() {
  fs.rmSync(viteCacheDir(), { recursive: true, force: true });
}

// startVite 用 freePort + strictPort: 端口由本次进程独占, 不可能接到旧服务;
// 若端口仍被抢, 子进程立即退出, waitHttp 的存活检查会直接判失败。
let viteUrl = "";

async function startVite() {
  wipeViteCache();
  const port = await freePort();
  const command = globalThis.process.platform === "win32" ? "pnpm.cmd" : "pnpm";
  const process = spawn(command, ["exec", "vite", "--host", "127.0.0.1", "--port", String(port), "--strictPort"], {
    cwd: ROOT,
    env: { ...globalThis.process.env, NODE_OPTIONS: "" },
    stdio: ["ignore", "pipe", "pipe"],
  });
  const url = `http://127.0.0.1:${port}`;
  await waitHttp(url, process);
  viteUrl = url;
  return process;
}

// ---------- fake bridge (NOT production backend evidence) ----------

const WS_GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11";
const SUPERVISOR = {
  hello: 1, helloAck: 2, error: 3, create: 4, created: 5, attach: 8, attached: 9,
  input: 10, inputAck: 11, resize: 12, ok: 13, kill: 14, detach: 18, output: 19, exit: 20, fatal: 21, killSession: 22,
};
const DIGEST = "digest-fleet-terminal-fake";
const SESSION = {
  id: "fake-session-1",
  created_at: "2026-10-07T05:34:56.123456789+08:00",
  incarnation: "fake-inc-1",
  cols: 80,
  rows: 24,
  dead: false,
};

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
    this.lineBuffer = "";
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
    // 二进制消息即 supervisor 字节流的一段; 本 fake 假设一帧一消息 (验收侧可控)
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
        // create 携带客户端生成的稳定 id+attempt, Created 回显同一 id
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
        if (msg.expect_incarnation && msg.expect_incarnation !== SESSION.incarnation) {
          this.sendJSON(SUPERVISOR.error, { code: "identity", message: "fake bridge: identity" });
          return;
        }
        sessionStore.attaches.push(msg);
        this.attached = true;
        this.nextSeq = 0n;
        this.sendJSON(SUPERVISOR.attached, { ...SESSION, id: msg.id });
        // backlog 从 seq 0 全量重放
        this.sendOutput(Buffer.from(sessionStore.buffer));
        return;
      }
      case SUPERVISOR.input: {
        const text = body.toString("utf8");
        sessionStore.inputs.push(text);
        this.sendJSON(SUPERVISOR.inputAck, { written: body.length });
        if (!this.attached) return;
        // 输入逐键到达 (xterm onData 按字符发), DROP/exit 按行判定;
        // 回显把 \r 换成 \r\n (与真实终端回车换行一致, 否则后一行覆盖前一行)
        this.lineBuffer += text;
        this.broadcastOutput(Buffer.from(text.replaceAll("\r", "\r\n")));
        if (this.lineBuffer.includes("DROP")) {
          // 生产 pipeBridge 语义: 任一侧结束都以 1000 正常关闭对端 WS
          // (internal/fleet/server/ws.go), 断开/重连路径必须覆盖 clean 1000。
          const payload = Buffer.alloc(2 + Buffer.byteLength("bridge closed"));
          payload.writeUInt16BE(1000, 0);
          payload.write("bridge closed", 2);
          this.socket.end(encodeFrame(0x8, payload));
          return;
        }
        if (this.lineBuffer.includes("\r") || this.lineBuffer.includes("\n")) {
          if (this.lineBuffer.includes("exit")) this.sendJSON(SUPERVISOR.exit, { code: 0 });
          this.lineBuffer = "";
        }
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
    // 32KiB 分片对齐 outputChunkSize
    for (let offset = 0; offset < data.length; offset += 32 * 1024) {
      const chunk = data.subarray(offset, Math.min(offset + 32 * 1024, data.length));
      this.socket.write(supervisorOutput(this.nextSeq, chunk));
      this.nextSeq += BigInt(chunk.length);
    }
  }

  broadcastOutput(data) {
    sessionStore.buffer = Buffer.concat([sessionStore.buffer, data]);
    for (const conn of liveConns) {
      if (conn.attached) conn.sendOutput(data);
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
      name: "fleet-term-box",
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
    {
      id: "d-2",
      name: "fleet-term-no-term",
      kind: "agent",
      created_at: NOW - 86400_000,
      last_seen_at: NOW - 20_000,
      revoked_at: 0,
      agent: {
        platform: "linux",
        app_version: "0.2.2",
        desired_autostart: true,
        terminal_enabled: false,
        current_url: "https://nexterm.example.com",
        service_state: { installed: true, enabled: true, active: true, last_reconcile_at: NOW - 60_000 },
        last_seen_at: NOW - 20_000,
        state_digest: DIGEST,
      },
    },
  ],
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
  csrf_token: "csrf-fleet-terminal-fake",
};

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
    if (pathName === "/fleet/base-urls") return corsJSON(res, { base_urls: [] });
    if (pathName === "/fleet/devices/d-1/metrics") return corsJSON(res, { samples: [] });
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
      // 验收只关心布局/文案/输入, 强制 DOM renderer: WebGL 渲染不出 .xterm-rows。
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
    useUi.getState().addTab({ id: 'devices-fleet156', kind: 'devices', title: '设备管理', closable: true });
    useUi.getState().setActiveTab('devices-fleet156');
    return true;
  })()`);
  await page.waitFor(`document.body.textContent.includes("fleet-term-box")`);
}

async function clickDeviceTerminalButton(page, deviceName) {
  const clicked = await page.evaluate(`(() => {
    const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes(${JSON.stringify(deviceName)}));
    if (!card) return "no-card";
    const button = [...card.querySelectorAll("button")].find((b) => b.textContent?.trim() === "终端");
    if (!button) return "no-button";
    if (button.disabled) return "disabled";
    button.click();
    return "clicked";
  })()`);
  if (clicked !== "clicked") throw new Error(`terminal button not clickable for ${deviceName}: ${clicked}`);
}

async function typeText(page, text) {
  // xterm 只把按键转发给聚焦的 textarea; 先聚焦再打字 (与真实用户点击终端一致)。
  const focused = await page.evaluate(`(() => {
    const textarea = document.querySelector('.xterm-helper-textarea');
    if (!textarea) return "no-textarea";
    textarea.focus();
    return document.activeElement === textarea ? "focused" : "focus-failed:" + String(document.activeElement?.className);
  })()`);
  if (focused !== "focused") throw new Error(`xterm textarea focus failed: ${focused}`);
  for (const ch of text) {
    if (ch === "\n") {
      await page.send("Input.dispatchKeyEvent", { type: "rawKeyDown", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13, nativeVirtualKeyCode: 13 });
      await page.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13, nativeVirtualKeyCode: 13 });
    } else {
      await page.send("Input.dispatchKeyEvent", { type: "keyDown", key: ch, text: ch });
      await page.send("Input.dispatchKeyEvent", { type: "keyUp", key: ch });
    }
  }
}

async function noPageOverflow(page) {
  return page.evaluate(
    "document.documentElement.scrollWidth <= window.innerWidth + 1 && document.body.scrollWidth <= window.innerWidth + 1",
  );
}

const terminalRowsText = "document.querySelector('.xterm .xterm-rows')?.innerText ?? ''";

// 终端就绪的精确判定: 「结束终端」按钮只在 ready 出现; 状态栏常驻 "0 已连接" 会污染模糊匹配。
const TERMINAL_READY = `[...document.querySelectorAll("button")].some((b) => b.textContent?.includes("结束终端"))`;

// waitState 在 node 侧轮询 fake bridge 的记录 (页面内的 waitFor 拿不到进程内状态)。
async function waitState(predicate, what, timeout = 10_000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (predicate()) return;
    await sleep(100);
  }
  throw new Error(`timed out waiting for ${what}`);
}

async function terminalAcceptance(page, fake) {
  await pass("open-terminal-320", async () => {
    await bootApp(page, fake, { width: 320, height: 568, touch: true });
    await openDevicesTab(page);
    await clickDeviceTerminalButton(page, "fleet-term-box");
    await page.waitFor(TERMINAL_READY);
    await page.waitFor(`(${terminalRowsText}).includes("fake-shell-ready")`);
    await waitState(() => sessionStore.creates.length >= 1 && sessionStore.attaches.length >= 1, "create+attach bridges");
    // React StrictMode 开发期双挂载会各开一次 (与 XtermView 的 dev 双 attach 同理),
    // 因此只钉合同下限: 每条桥接都完成 hello 协商, create 带尺寸。
    assert.ok(sessionStore.hellos.length >= 2, `create/attach 各走一条桥接 (hello x>=2), got ${sessionStore.hellos.length}`);
    for (const hello of sessionStore.hellos) {
      assert.equal(hello.version, 2, "hello version=2");
      assert.equal(hello.state_digest, DIGEST, "hello 携带设备列表下发的 state digest");
    }
    assert.ok(sessionStore.creates[0].cols >= 2 && sessionStore.creates[0].rows >= 1, "create 携带终端尺寸");
    assert.equal(await noPageOverflow(page), true, "320px 下页面不得横向溢出");
    await screenshot(page, "terminal-320.png");
  });

  await pass("terminal-input-echo", async () => {
    await typeText(page, "echo hi-42\n");
    await page.waitFor(`(${terminalRowsText}).includes("echo hi-42")`);
    // 输入逐键到达, 按拼接后的字节流判定
    await waitState(() => sessionStore.inputs.join("").includes("echo hi-42"), "input frame");
  });

  await pass("terminal-resize-protocol", async () => {
    const before = sessionStore.resizes.length;
    await setViewport(page, { width: 390, height: 844, touch: true });
    await waitState(() => sessionStore.resizes.length > before, "protocol resize frame");
    const resize = sessionStore.resizes[sessionStore.resizes.length - 1];
    assert.ok(resize.cols >= 2 && resize.rows >= 1, `resize 帧尺寸合法: ${JSON.stringify(resize)}`);
    await screenshot(page, "terminal-390.png");
  });

  await pass("kill-confirm", async () => {
    const before = sessionStore.killSessions.length;
    await page.evaluate(`(() => {
      const button = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("结束终端"));
      if (!button) throw new Error("kill button not found");
      button.click();
      return true;
    })()`);
    await page.waitFor(`document.body.textContent.includes("结束设备") && document.body.textContent.includes("确定")`);
    await page.evaluate(`(() => {
      const button = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "确定");
      if (!button) throw new Error("confirm button not found");
      button.click();
      return true;
    })()`);
    await page.waitFor(`document.body.textContent.includes("已结束")`);
    await waitState(() => sessionStore.killSessions.length > before, "killSession frame");
    const kill = sessionStore.killSessions[sessionStore.killSessions.length - 1];
    assert.equal(kill.id, sessionStore.sessionId, "killSession 带会话 id");
    assert.equal(kill.expect_incarnation, "fake-inc-1", "killSession 带期望 incarnation");
    await screenshot(page, "kill-confirmed.png");
  });

  await pass("reconnect-resume", async () => {
    // 已结束的标签上点「新开终端」重开 (fake 会话仍存活, 走全新 create+attach)
    await page.evaluate(`(() => {
      const button = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("新开终端"));
      if (!button) throw new Error("restart button not found");
      button.click();
      return true;
    })()`);
    await page.waitFor(TERMINAL_READY);
    await typeText(page, "before-drop\n");
    await page.waitFor(`(${terminalRowsText}).includes("before-drop")`);
    const attachesBefore = sessionStore.attaches.length;
    await typeText(page, "DROP\n");
    await page.waitFor(`document.body.textContent.includes("连接已断开")`);
    await page.evaluate(`(() => {
      const button = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("重新连接"));
      if (!button) throw new Error("reconnect button not found");
      button.click();
      return true;
    })()`);
    await page.waitFor(TERMINAL_READY);
    await waitState(() => sessionStore.attaches.length > attachesBefore, "resume attach");
    const attach = sessionStore.attaches[sessionStore.attaches.length - 1];
    assert.equal(attach.expect_incarnation, "fake-inc-1", "resume attach 带期望 incarnation");
    assert.ok(typeof attach.expect_created_at_unix_nano === "number", "resume attach 带期望 created_at");
    // backlog 从 seq 0 重放: 断线前的输出重新出现在屏幕上
    await page.waitFor(`(${terminalRowsText}).includes("before-drop")`);
    await screenshot(page, "reconnected-390.png");
  });

  await pass("exit-state", async () => {
    await typeText(page, "exit\n");
    await page.waitFor(`document.body.textContent.includes("已结束") && document.body.textContent.includes("新开终端")`);
    await screenshot(page, "exited-390.png");
  });

  await pass("policy-no-button", async () => {
    const state = await page.evaluate(`(() => {
      const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("fleet-term-no-term"));
      if (!card) return "no-card";
      const button = [...card.querySelectorAll("button")].find((b) => b.textContent?.trim() === "终端");
      return button ? "has-button" : "no-button";
    })()`);
    assert.equal(state, "no-button", "关闭终端访问的设备不得显示「终端」按钮");
  });

  await pass("digest-not-persisted", async () => {
    const stored = await page.evaluate(
      "JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage } })",
    );
    assert.equal(stored.includes(DIGEST), false, `state digest 不得写入 web storage: ${stored}`);
  });

  await pass("desktop-1280", async () => {
    await setViewport(page, { width: 1280, height: 800, touch: false });
    await sleep(400);
    await page.waitFor(`!!document.querySelector('.xterm .xterm-rows')`);
    // 只考核可见工具栏: 隐藏标签页的按钮矩形为零, 不属于可达性问题
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
    await screenshot(page, "terminal-1280.png");
  });
}

// selftest: 不起浏览器, 直接用 node WS 客户端驱动 fake bridge 验证其合同实现。
async function selfTest() {
  const fake = await startFakeBackend();
  const ws = new WebSocket(`ws://127.0.0.1:${fake.port}/fleet/devices/d-1/bridge`);
  ws.binaryType = "arraybuffer";
  const frames = [];
  const parser = new (class {
    buffer = Buffer.alloc(0);
    push(chunk) {
      this.buffer = Buffer.concat([this.buffer, chunk]);
      const out = [];
      for (;;) {
        if (this.buffer.length < 5) return out;
        const length = this.buffer.readUInt32BE(0);
        if (this.buffer.length < 5 + length) return out;
        out.push({ kind: this.buffer[4], payload: this.buffer.subarray(5, 5 + length) });
        this.buffer = this.buffer.subarray(5 + length);
      }
    }
  })();
  const done = new Promise((resolve, reject) => {
    ws.addEventListener("error", reject);
    ws.addEventListener("message", (event) => {
      for (const frame of parser.push(Buffer.from(event.data))) frames.push(frame);
    });
    ws.addEventListener("open", () => {
      const send = (kind, message) => {
        const payload = Buffer.from(JSON.stringify(message), "utf8");
        const header = Buffer.alloc(5);
        header.writeUInt32BE(payload.length, 0);
        header[4] = kind;
        ws.send(Buffer.concat([header, payload]));
      };
      const sendRaw = (kind, payload) => {
        const header = Buffer.alloc(5);
        header.writeUInt32BE(payload.length, 0);
        header[4] = kind;
        ws.send(Buffer.concat([header, payload]));
      };
      send(SUPERVISOR.hello, { version: 2, state_digest: DIGEST });
      send(SUPERVISOR.create, { command: [], cols: 80, rows: 24 });
      send(SUPERVISOR.attach, { id: SESSION.id, expect_incarnation: SESSION.incarnation });
      sendRaw(SUPERVISOR.input, Buffer.from("ls\r", "utf8"));
    });
    setTimeout(resolve, 800);
  });
  await done;
  console.warn("hellos:", sessionStore.hellos.length, "creates:", sessionStore.creates.length, "attaches:", sessionStore.attaches.length);
  console.warn("frames back:", frames.map((f) => f.kind).join(","));
  await fake.close();
}

if (process.argv.includes("--selftest")) {
  await selfTest();
  process.exit(0);
}

// startupFailureSelftest: 回归 P2-4 — Chrome 启动失败时, 本次启动的 Vite 必须
// 被回收, 不残留旧端口服务 (strictPort + freePort 已使接管旧服务不可能)。
async function startupFailureSelftest() {
  let viteProcess = null;
  let failure = null;
  let viteExited = false;
  try {
    viteProcess = await startVite();
    viteProcess.once("exit", () => {
      viteExited = true;
    });
    const savedPath = process.env.CHROME_PATH;
    process.env.CHROME_PATH = "/nonexistent/chrome-for-selftest";
    try {
      await startChrome();
      failure = "startChrome unexpectedly succeeded";
    } finally {
      if (savedPath === undefined) delete process.env.CHROME_PATH;
      else process.env.CHROME_PATH = savedPath;
    }
  } catch (error) {
    failure = failure ?? String(error?.message || error);
  } finally {
    stop(viteProcess);
    if (viteProcess && !viteExited) {
      await Promise.race([
        new Promise((resolve) => viteProcess.once("exit", resolve)),
        sleep(3000),
      ]);
    }
    wipeViteCache();
  }
  if (failure === null) failure = "startChrome did not fail as expected";
  if (!viteExited) {
    console.warn("FAILED startup-failure-selftest: vite process still alive");
    process.exit(1);
  }
  console.warn(`PASS startup-failure-selftest (chrome start failed as expected: ${failure.slice(0, 80)}; vite reaped)`);
  process.exit(0);
}

if (process.argv.includes("--startup-failure-selftest")) {
  await startupFailureSelftest();
}

let vite;
let chrome;
let page;
let fake;
try {
  fake = await startFakeBackend();
  // 分步启动并立即登记句柄: 任一步失败时 finally 都能回收已启动的进程,
  // 不再出现 Promise.all 一边成功一边泄漏的窗口。
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
  await terminalAcceptance(page, fake);
} catch (error) {
  harnessErrors.push(String(error?.stack || error));
} finally {
  if (page) page.close();
  stop(chrome?.process);
  stop(vite);
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
  backend: "fake in-process bridge speaking the supervisor v2 contract; NOT production backend evidence (real evidence: scripts/e2e-device-local.py)",
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
console.warn(`fleet-terminal acceptance: ${checks.filter((check) => check.status === "passed").length}/${checks.length} checks passed; report=${path.join(OUT, "report.json")}`);
if (failed.length || harnessErrors.length) process.exit(1);
