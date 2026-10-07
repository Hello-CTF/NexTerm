#!/usr/bin/env node
// M158 设置分享卡片响应式验收: 真实 headless Chromium + vite dev server,
// 后端是本进程内的 fake HTTP server (仅复述 M127/M138/M149 合同形状:
// /auth/status+/auth/me 会话, /fleet/devices 角色过滤, /share/host-shares
// 收 recipient_username 并大小写不敏感解析, /share/links), 不是生产后端证据;
// 生产合同证据归 Go 测试与 scripts/e2e-sharing-local.py。
// 覆盖: 列表渲染 (方向/权限/状态), 超管与普通 owner 同一创建合同 (只读默认/
// 显式读写/有界 TTL/CSRF), 吊销确认流, 公开链接零创建入口, 普通 owner 路径
// 零 /admin/users, 320/390/768 响应式无溢出, demo 显式不可用且零分享 HTTP。
import { spawn, spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import fs from "node:fs";
import http from "node:http";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { startVite as startViteProcess } from "./lib/acceptance-process.mjs";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const OUT = path.join(ROOT, "target/settings-sharing");
const SHOTS = path.join(OUT, "shots");
const VITE_PORT = Number(process.env.NEXTERM_VITE_PORT || 1471);
const VITE = `http://127.0.0.1:${VITE_PORT}`;
const results = new Map();
const harnessErrors = [];
const pageErrors = [];

// 共享 /tmp 可能被其他任务填满: vite 依赖预构建与 Chrome profile 的临时写入会
// 失败 (ENOENT/ENOSPC)。本脚本与子进程一律使用私有临时目录; 路径必须短,
// Chrome 的 SingletonSocket 受 Unix socket 路径长度限制。
const PRIVATE_TMP = path.join(os.homedir(), ".cache", "nexterm-acceptance-tmp");
fs.mkdirSync(PRIVATE_TMP, { recursive: true });
process.env.TMPDIR = PRIVATE_TMP;

fs.mkdirSync(SHOTS, { recursive: true });

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

  on(method, handler) {
    if (!this.eventHandlers.has(method)) this.eventHandlers.set(method, []);
    this.eventHandlers.get(method).push(handler);
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
  const page = await CDP.connect((await response.json()).webSocketDebuggerUrl);
  await page.send("Runtime.enable");
  page.on("Runtime.exceptionThrown", (params) => {
    const d = params.exceptionDetails;
    pageErrors.push(`${d?.text || ""} ${d?.exception?.description || ""}`.slice(0, 300));
  });
  return page;
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

// ---------- fake 后端 (仅复述真实合同形状) ----------

const NOW = Date.now();

const FAKE_ADMIN = {
  id: "u-root",
  username: "root",
  display_name: "Root",
  role: "superadmin",
  state: "active",
  must_change_password: false,
  created_at: NOW - 90 * 24 * 3600_000,
  updated_at: NOW - 90 * 24 * 3600_000,
  last_login_at: NOW - 3600_000,
};

const FAKE_ALICE = {
  ...FAKE_ADMIN,
  id: "u-alice",
  username: "alice",
  display_name: "Alice",
  role: "user",
};

const FAKE_BOB_DISABLED = { ...FAKE_ALICE, id: "u-bob", username: "bob", display_name: "Bob", state: "disabled" };

const AGENT = {
  platform: "linux",
  app_version: "0.2.2",
  desired_autostart: true,
  terminal_enabled: true,
  current_url: "https://nexterm.example.com",
  service_state: { installed: true, enabled: true, active: true, last_reconcile_at: NOW - 60_000 },
  last_seen_at: NOW - 30_000,
};

const FAKE_DEVICES = [
  {
    id: "d-1",
    name: "web-01",
    kind: "agent",
    created_at: NOW - 86400_000,
    last_seen_at: NOW - 30_000,
    revoked_at: 0,
    owner: { id: "u-root", username: "root" },
    agent: AGENT,
  },
  {
    id: "d-2",
    name: "web-02",
    kind: "agent",
    created_at: NOW - 86400_000,
    last_seen_at: NOW - 30_000,
    revoked_at: 0,
    owner: { id: "u-alice", username: "alice" },
    agent: AGENT,
  },
];

function startFakeBackend() {
  const state = {
    currentUser: FAKE_ADMIN,
    shares: [
      {
        id: "hs-1",
        owner_id: "u-root",
        owner_username: "root",
        device_id: "d-1",
        recipient_id: "u-alice",
        recipient_username: "alice",
        permission: "read",
        created_at: NOW - 3600_000,
        expires_at: NOW + 3600_000,
      },
      {
        id: "hs-2",
        owner_id: "u-alice",
        owner_username: "alice",
        device_id: "d-2",
        recipient_id: "u-root",
        recipient_username: "root",
        permission: "read_write",
        created_at: NOW - 7200_000,
        expires_at: NOW + 7200_000,
      },
    ],
    links: [
      {
        id: "l-1",
        owner_id: "u-root",
        device_id: "d-1",
        session_id: "01J4Z8Y7K2M8N3P6Q9R1T5V9X2",
        permission: "read",
        created_at: NOW - 1800_000,
        expires_at: NOW + 1800_000,
        last_accessed_at: NOW - 300_000,
      },
      {
        id: "l-2",
        owner_id: "u-root",
        device_id: "d-1",
        session_id: "01J4Z8Y7K2M8N3P6Q9R1T5V9X3",
        permission: "read",
        created_at: NOW - 86400_000,
        expires_at: NOW - 3600_000,
        revoked_at: NOW - 600_000,
      },
    ],
    nextId: 1,
  };
  const users = [FAKE_ADMIN, FAKE_ALICE, FAKE_BOB_DISABLED];
  const isAdmin = () => state.currentUser.role === "superadmin";
  const requests = [];
  const shareRequests = () => requests.filter((r) => r.url.startsWith("/share/"));

  const cors = {
    "Access-Control-Allow-Origin": "*",
    "Access-Control-Allow-Methods": "GET,POST,PUT,DELETE,OPTIONS",
    "Access-Control-Allow-Headers": "content-type,x-nexterm-csrf",
    "Cache-Control": "no-store",
  };

  const server = http.createServer((req, res) => {
    const url = new URL(req.url || "/", "http://127.0.0.1");
    const send = (status, payload) => {
      res.writeHead(status, { "Content-Type": "application/json; charset=utf-8", ...cors });
      res.end(JSON.stringify(payload));
    };
    if (req.method === "OPTIONS") {
      res.writeHead(204, cors);
      res.end();
      return;
    }
    let body = "";
    req.on("data", (chunk) => { body += chunk; });
    req.on("end", () => {
      let parsed = null;
      if (body.length > 0) {
        try {
          parsed = JSON.parse(body);
        } catch {
          send(400, { error: { code: "bad_param", message: "请求体不是合法 JSON" } });
          return;
        }
      }
      requests.push({ method: req.method, url: url.pathname, body: parsed, csrf: req.headers["x-nexterm-csrf"] || null });

      // 验收专用会话开关: 切换 /auth/me 返回的用户, 用于普通 owner 路径。
      if (url.pathname === "/__test/session" && req.method === "POST") {
        const target = users.find((u) => u.username === parsed?.username);
        if (!target) {
          send(404, { error: { code: "not_found", message: "no such fake user" } });
          return;
        }
        state.currentUser = target;
        send(200, { ok: true });
        return;
      }
      if (url.pathname === "/auth/status" && req.method === "GET") {
        send(200, { initialized: true, registration_open: false, auth: "on" });
        return;
      }
      if (url.pathname === "/auth/me" && req.method === "GET") {
        send(200, { user: state.currentUser, csrf_token: "acceptance-csrf" });
        return;
      }
      if (url.pathname === "/fleet/devices" && req.method === "GET") {
        // 与线上一致的服务端角色过滤: 普通用户只看自己的设备。
        const devices = isAdmin() ? FAKE_DEVICES : FAKE_DEVICES.filter((d) => d.owner?.id === state.currentUser.id);
        send(200, { devices });
        return;
      }
      if (url.pathname === "/admin/users" && req.method === "GET") {
        send(200, { users });
        return;
      }
      if (url.pathname === "/admin/settings" && req.method === "GET") {
        send(200, { registration_open: false, public_base_url: "" });
        return;
      }
      if (url.pathname === "/share/host-shares" && req.method === "GET") {
        // 与线上一致: 普通用户只看自己授予/接收的分享, 超管看全部。
        const shares = isAdmin()
          ? state.shares
          : state.shares.filter((s) => s.owner_id === state.currentUser.id || s.recipient_id === state.currentUser.id);
        send(200, { shares });
        return;
      }
      if (url.pathname === "/share/host-shares" && req.method === "POST") {
        const b = parsed || {};
        if (typeof b.device_id !== "string" || typeof b.recipient_username !== "string" || typeof b.write !== "boolean" ||
            typeof b.ttl_ms !== "number" || b.ttl_ms < 60_000 || b.ttl_ms > 30 * 24 * 60 * 60_000) {
          send(400, { error: { code: "bad_param", message: "请求体字段不合法" } });
          return;
        }
        // 与线上一致的解析语义: 用户名大小写不敏感精确解析; 无效 404, 禁用 403。
        const recipient = users.find((u) => u.username.toLowerCase() === b.recipient_username.toLowerCase());
        if (!recipient) {
          send(404, { error: { code: "not_found", message: "未找到: 用户" } });
          return;
        }
        if (recipient.state !== "active") {
          send(403, { error: { code: "forbidden", message: "接收者账号不可用" } });
          return;
        }
        const device = FAKE_DEVICES.find((d) => d.id === b.device_id);
        if (!device || (device.owner?.id !== state.currentUser.id && !isAdmin())) {
          send(403, { error: { code: "forbidden", message: "设备不属于该用户" } });
          return;
        }
        if (recipient.id === device.owner?.id) {
          send(400, { error: { code: "bad_param", message: "不能向设备 owner 本人分享" } });
          return;
        }
        const share = {
          id: `hs-fake-${state.nextId++}`,
          owner_id: state.currentUser.id,
          owner_username: state.currentUser.username,
          device_id: b.device_id,
          recipient_id: recipient.id,
          recipient_username: recipient.username,
          permission: b.write ? "read_write" : "read",
          created_at: Date.now(),
          expires_at: Date.now() + b.ttl_ms,
        };
        state.shares.push(share);
        send(200, share);
        return;
      }
      const hostRevoke = /^\/share\/host-shares\/([^/]+)\/revoke$/.exec(url.pathname);
      if (hostRevoke && req.method === "POST") {
        const share = state.shares.find((s) => s.id === decodeURIComponent(hostRevoke[1]));
        if (!share) {
          send(404, { error: { code: "not_found", message: "未找到: 主机分享" } });
          return;
        }
        share.revoked_at = Date.now();
        send(200, { ok: true });
        return;
      }
      if (url.pathname === "/share/links" && req.method === "GET") {
        const links = isAdmin() ? state.links : state.links.filter((l) => l.owner_id === state.currentUser.id);
        send(200, { links });
        return;
      }
      const linkRevoke = /^\/share\/links\/([^/]+)\/revoke$/.exec(url.pathname);
      if (linkRevoke && req.method === "POST") {
        const link = state.links.find((l) => l.id === decodeURIComponent(linkRevoke[1]));
        if (!link) {
          send(404, { error: { code: "not_found", message: "未找到: 分享链接" } });
          return;
        }
        link.revoked_at = Date.now();
        send(200, { ok: true });
        return;
      }
      if (url.pathname === "/rpc" && req.method === "POST") {
        send(200, { ok: false, error: { code: "unsupported", message: "acceptance fake 不实现 /rpc" } });
        return;
      }
      send(404, { error: { code: "not_found", message: `${req.method} ${url.pathname}` } });
    });
  });

  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address();
      resolve({
        port,
        state,
        requests,
        shareRequests,
        close: () => new Promise((done) => server.close(() => done())),
      });
    });
  });
}

// ---------- 页面操作 ----------

async function screenshot(page, name) {
  const shot = await page.send("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(path.join(SHOTS, name), Buffer.from(shot.data, "base64"));
  return name;
}

async function setViewport(page, width, height) {
  await page.send("Emulation.setDeviceMetricsOverride", {
    width,
    height,
    deviceScaleFactor: 1,
    mobile: width <= 560,
  });
  await page.send("Emulation.setTouchEmulationEnabled", { enabled: false, maxTouchPoints: 1 });
  await sleep(250);
}

async function bootWeb(page, api) {
  const { identifier } = await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `
      try { localStorage.clear(); } catch {}
      window.__NEXTERM_TRANSPORT__ = "web";
      try { localStorage.setItem("nexterm.theme.v1", "dark"); } catch {}
    `,
  });
  try {
    await page.navigate(`${VITE}/?api=${api}`);
    await page.waitFor("!!document.querySelector('.nx-app')");
    // AuthGate refresh: /auth/status -> /auth/me (fake 会话) -> gate ready
    await page.waitFor(
      `(async () => { const { useAuth } = await import('/src/features/auth/store.ts'); return useAuth.getState().user?.username === "root"; })()`,
    );
  } finally {
    await page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier });
  }
}

async function openSettingsTab(page) {
  await page.evaluate(`(async () => {
    const { useUi } = await import('/src/app/store.ts');
    useUi.getState().addTab({ id: 'settings-share158', kind: 'settings', title: '设置', closable: true });
    useUi.getState().setActiveTab('settings-share158');
    return true;
  })()`);
  await page.waitFor(`document.body.textContent.includes("主机分享")`);
  await page.waitFor(`document.body.textContent.includes("web-01")`);
  await sleep(400);
}

const READ_SHARE_CARD = `(() => {
  const card = [...document.querySelectorAll(".nx-card")].find((c) => {
    const title = c.querySelector(".nx-card-title");
    return title && title.textContent?.trim() === "分享";
  });
  if (!card) return { found: false };
  const rows = [...card.querySelectorAll("div.border-b")];
  const cr = card.getBoundingClientRect();
  const overflowingRows = rows.filter((r) => r.scrollWidth > r.clientWidth + 1).length;
  return {
    found: true,
    text: card.textContent || "",
    overflowsViewport: cr.right > window.innerWidth + 1 || cr.left < -1,
    rowCount: rows.length,
    overflowingRows,
    freeTextInputs: card.querySelectorAll("input:not([type=checkbox])").length,
    linkCreateButtons: [...card.querySelectorAll("button")].filter((b) => /创建链接|新建链接|生成链接/.test(b.textContent || "")).length,
    mentionsToken: (card.textContent || "").toLowerCase().includes("token"),
  };
})()`;

const READ_OVERFLOW = `(() => {
  const de = document.documentElement;
  const panes = [...document.querySelectorAll(".nx-pane")];
  return {
    innerWidth: window.innerWidth,
    docScrollWidth: de.scrollWidth,
    bodyScrollWidth: document.body.scrollWidth,
    panes: panes.map((p) => ({ scrollWidth: p.scrollWidth, clientWidth: p.clientWidth })),
  };
})()`;

// READ_OVERFLOW_CULPRITS 在 pane 溢出时列出横向探出 pane 内容盒的元素并标注是否
// 属于分享卡片 (inShareCard); truncate 截断与自带滚动的容器不算探出。
const READ_OVERFLOW_CULPRITS = `(() => {
  const card = [...document.querySelectorAll(".nx-card")].find((c) => {
    const title = c.querySelector(".nx-card-title");
    return title && title.textContent?.trim() === "分享";
  });
  const out = [];
  for (const pane of document.querySelectorAll(".nx-pane")) {
    if (pane.scrollWidth <= pane.clientWidth + 1) continue;
    const pr = pane.getBoundingClientRect();
    for (const el of pane.querySelectorAll("*")) {
      const r = el.getBoundingClientRect();
      const style = getComputedStyle(el);
      if (style.overflowX === "auto" || style.overflowX === "scroll") continue;
      if (String(el.className).includes("truncate")) continue;
      if (r.right > pr.right + 1 || r.left < pr.left - 1) {
        out.push({
          tag: el.tagName,
          cls: String(el.className).slice(0, 140),
          text: (el.textContent || "").slice(0, 60),
          rectWidth: Math.round(r.width),
          inShareCard: card ? card.contains(el) : false,
        });
        if (out.length >= 10) return out;
      }
    }
  }
  return out;
})()`;

async function clickButton(page, text) {
  const clicked = await page.evaluate(`(() => {
    const button = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === ${JSON.stringify(text)});
    if (!button) return false;
    button.click();
    return true;
  })()`);
  if (!clicked) throw new Error(`button not found: ${text}`);
}

async function clickRowButton(page, rowText, buttonText) {
  const clicked = await page.evaluate(`(() => {
    const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("主机分享") && c.textContent?.includes("公开链接"));
    if (!card) return "no-card";
    const row = [...card.querySelectorAll("div.border-b")].find((d) => (d.textContent || "").includes(${JSON.stringify(rowText)}) && !(d.textContent || "").includes("已吊销"));
    if (!row) return "no-row";
    const button = [...row.querySelectorAll("button")].find((b) => (b.textContent || "").trim() === ${JSON.stringify(buttonText)});
    if (!button) return "no-button";
    button.click();
    return "ok";
  })()`);
  if (clicked !== "ok") throw new Error(`row button not found: ${rowText} / ${buttonText} (${clicked})`);
}

async function setSelect(page, label, value) {
  const done = await page.evaluate(`(() => {
    const select = document.querySelector('select[aria-label=' + ${JSON.stringify(label)} + ']');
    if (!select) return "no-select";
    const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value").set;
    setter.call(select, ${JSON.stringify(value)});
    select.dispatchEvent(new Event("change", { bubbles: true }));
    return "ok";
  })()`);
  if (done !== "ok") throw new Error(`select not found: ${label}`);
}

async function setRecipientUsername(page, value) {
  const done = await page.evaluate(`(() => {
    const input = [...document.querySelectorAll(".nx-card input:not([type=checkbox])")].find((i) => (i.placeholder || "").includes("接收者的用户名"));
    if (!input) return "no-input";
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set;
    setter.call(input, ${JSON.stringify(value)});
    input.dispatchEvent(new Event("input", { bubbles: true }));
    return "ok";
  })()`);
  if (done !== "ok") throw new Error(`recipient username input not found (${done})`);
}

async function confirmDialog(page) {
  await page.waitFor(`[...document.querySelectorAll("button")].some((b) => b.textContent?.trim() === "确定")`);
  await clickButton(page, "确定");
}

// ---------- 检查 ----------

async function renderChecks(page, fake) {
  await pass("A-card-render", async () => {
    const state = await page.evaluate(READ_SHARE_CARD);
    assert.ok(state.found, "share card not found");
    assert.ok(state.text.includes("2 条主机分享"), `host share count: ${state.text.slice(0, 120)}`);
    assert.ok(state.text.includes("2 条公开链接"), `link count: ${state.text.slice(0, 120)}`);
    assert.ok(state.text.includes("授予给 alice"), "granted direction missing");
    assert.ok(state.text.includes("接收自 alice"), "received direction missing");
    assert.ok(state.text.includes("只读") && state.text.includes("读写"), "permission badges missing");
    assert.ok(state.text.includes("有效") && state.text.includes("已吊销"), "state badges missing");
    assert.ok(state.text.includes("会话 01J4Z8Y7…"), "session short id missing");
    assert.equal(state.freeTextInputs, 0, `no free-text input allowed (no fabricated session_id): ${JSON.stringify(state)}`);
    assert.equal(state.linkCreateButtons, 0, "public-link creation entry must not exist");
    assert.equal(state.mentionsToken, false, "token must never be displayed");
    return { evidence: { rowCount: state.rowCount } };
  });

  await pass("A-create-read-default", async () => {
    const before = fake.requests.length;
    await clickButton(page, "新建主机分享");
    await page.waitFor(`!!document.querySelector('select[aria-label="分享主机"]')`);
    await setSelect(page, "分享主机", "d-1");
    await setRecipientUsername(page, "alice");
    await clickButton(page, "创建分享");
    await page.waitFor(`document.body.textContent.includes("3 条主机分享")`);
    const create = fake.requests.find((r) => r.method === "POST" && r.url === "/share/host-shares");
    assert.ok(create, "create request not seen by fake");
    assert.deepEqual(create.body, { device_id: "d-1", recipient_username: "alice", write: false, ttl_ms: 86_400_000 });
    assert.equal(create.csrf, "acceptance-csrf", "create must carry the session CSRF header");
    const after = fake.requests.slice(before).filter((r) => r.method === "GET" && r.url === "/share/host-shares");
    assert.ok(after.length >= 1, "list must reload after create");
    return { evidence: { body: create.body, csrf: create.csrf } };
  });

  await pass("A-create-read-write-7d", async () => {
    await clickButton(page, "新建主机分享");
    await page.waitFor(`!!document.querySelector('select[aria-label="分享主机"]')`);
    await setSelect(page, "分享主机", "d-2");
    await setRecipientUsername(page, "root");
    await setSelect(page, "分享有效期", String(7 * 24 * 60 * 60_000));
    await page.evaluate(`(() => {
      const box = [...document.querySelectorAll('input[type="checkbox"]')].find((i) => i.closest("label")?.textContent?.includes("允许读写"));
      if (!box) throw new Error("write checkbox not found");
      box.click();
      return true;
    })()`);
    await clickButton(page, "创建分享");
    await page.waitFor(`document.body.textContent.includes("4 条主机分享")`);
    const creates = fake.requests.filter((r) => r.method === "POST" && r.url === "/share/host-shares");
    const second = creates[creates.length - 1];
    assert.deepEqual(second.body, { device_id: "d-2", recipient_username: "root", write: true, ttl_ms: 604_800_000 });
    return { evidence: { body: second.body } };
  });

  await pass("A-revoke-host-share", async () => {
    await clickRowButton(page, "授予给 alice", "吊销");
    await confirmDialog(page);
    await page.waitFor(`(() => {
      const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("主机分享") && c.textContent?.includes("公开链接"));
      if (!card) return false;
      const row = [...card.querySelectorAll("div.border-b")].find((d) => (d.textContent || "").includes("授予给 alice"));
      return !!row && (row.textContent || "").includes("已吊销");
    })()`);
    const revoke = fake.requests.find((r) => r.method === "POST" && r.url === "/share/host-shares/hs-1/revoke");
    assert.ok(revoke, "revoke request not seen by fake");
    assert.equal(revoke.csrf, "acceptance-csrf", "revoke must carry the session CSRF header");
    return { evidence: { url: revoke.url, csrf: revoke.csrf } };
  });

  await pass("A-revoke-link", async () => {
    await clickRowButton(page, "最近访问", "吊销");
    await confirmDialog(page);
    await page.waitFor(`(() => {
      const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("主机分享") && c.textContent?.includes("公开链接"));
      if (!card) return false;
      const row = [...card.querySelectorAll("div.border-b")].find((d) => (d.textContent || "").includes("最近访问"));
      return !!row && (row.textContent || "").includes("已吊销");
    })()`);
    const revoke = fake.requests.find((r) => r.method === "POST" && r.url === "/share/links/l-1/revoke");
    assert.ok(revoke, "link revoke request not seen by fake");
    return { evidence: { url: revoke.url } };
  });

  await pass("A-no-link-creation-shortcut", async () => {
    const created = fake.requests.filter((r) => r.method === "POST" && r.url === "/share/links");
    assert.equal(created.length, 0, `POST /share/links must never be called: ${JSON.stringify(created)}`);
    const state = await page.evaluate(READ_SHARE_CARD);
    assert.equal(state.linkCreateButtons, 0);
    assert.equal(state.freeTextInputs, 0);
    return { evidence: { shareRequests: fake.shareRequests().length } };
  });
}

async function responsiveChecks(page) {
  for (const [width, height] of [[320, 720], [390, 780], [768, 900]]) {
    await pass(`B-no-overflow-${width}`, async () => {
      await setViewport(page, width, height);
      const sample = await page.evaluate(READ_OVERFLOW);
      const card = await page.evaluate(READ_SHARE_CARD);
      assert.ok(card.found, "share card not found");
      assert.equal(card.overflowsViewport, false, `share card overflows viewport: ${JSON.stringify(card)}`);
      assert.equal(card.overflowingRows, 0, `share rows overflow: ${JSON.stringify(card)}`);
      const docOverflow = sample.docScrollWidth > sample.innerWidth || sample.bodyScrollWidth > sample.innerWidth;
      const paneOverflow = sample.panes.some((p) => p.scrollWidth > p.clientWidth + 1);
      let foreign = [];
      if (docOverflow || paneOverflow) {
        const culprits = await page.evaluate(READ_OVERFLOW_CULPRITS);
        const mine = culprits.filter((c) => c.inShareCard);
        // 分享卡片自身探出一律失败; 无法归因的溢出同样失败, 不静默放过。
        assert.equal(mine.length, 0, `${width}: share card elements overflow: ${JSON.stringify(mine)}`);
        assert.ok(culprits.length > 0, `${width}: overflow seen but no culprit identified: ${JSON.stringify(sample)}`);
        foreign = culprits;
      }
      const shot = await screenshot(page, `B-sharing-${width}.png`);
      return { evidence: { innerWidth: sample.innerWidth, docScrollWidth: sample.docScrollWidth, rows: card.rowCount, shot, foreignOverflow: foreign } };
    });
  }
}

async function plainOwnerChecks(page, fake) {
  const adminUsersBefore = fake.requests.filter((r) => r.url === "/admin/users").length;
  // 切换 fake 会话为普通用户 alice 并刷新门状态: 卡片应按账号隔离重置并以 alice 视角重载。
  await page.evaluate(`(async () => {
    const response = await fetch(${JSON.stringify(`http://127.0.0.1:${fake.port}/__test/session`)}, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ username: "alice" }),
    });
    if (!response.ok) throw new Error("session switch failed: " + response.status);
    const { useAuth } = await import('/src/features/auth/store.ts');
    await useAuth.getState().refresh();
    return true;
  })()`);
  // alice 视角: hs-1/hs-fake-1 (接收自 root, 设备 d-1 不在她的设备列表里, 行内显示短 id)
  // 与 hs-2 (授予给 root) 共 3 条; 设备下拉里只有 web-02 (服务端角色过滤的复述)。
  await page.waitFor(`document.body.textContent.includes("3 条主机分享")`);
  await page.waitFor(`!document.body.textContent.includes("web-01")`);

  await pass("D-plain-owner-create-by-username", async () => {
    await clickButton(page, "新建主机分享");
    await page.waitFor(`!!document.querySelector('select[aria-label="分享主机"]')`);
    await setSelect(page, "分享主机", "d-2");
    await setRecipientUsername(page, "root");
    await clickButton(page, "创建分享");
    await page.waitFor(`document.body.textContent.includes("4 条主机分享")`);
    const creates = fake.requests.filter((r) => r.method === "POST" && r.url === "/share/host-shares");
    const last = creates[creates.length - 1];
    assert.deepEqual(last.body, { device_id: "d-2", recipient_username: "root", write: false, ttl_ms: 86_400_000 });
    assert.equal(last.csrf, "acceptance-csrf", "plain owner create must carry the session CSRF header");
    return { evidence: { body: last.body, csrf: last.csrf } };
  });

  // 普通用户路径全程不访问 /admin/users (分享卡片不依赖用户目录)。
  await pass("D-plain-owner-no-admin-directory", async () => {
    const after = fake.requests.filter((r) => r.url === "/admin/users").length;
    assert.equal(after, adminUsersBefore, `plain owner path must not call /admin/users: before=${adminUsersBefore} after=${after}`);
    return { evidence: { adminUsersBefore: adminUsersBefore, adminUsersAfter: after } };
  });
}

async function demoChecks(chrome, fake) {
  const shareCountBefore = fake.shareRequests().length;
  const page = await newPage(chrome);
  try {
    await page.send("Page.addScriptToEvaluateOnNewDocument", {
      source: `try { localStorage.clear(); } catch {}`,
    });
    await page.navigate(`${VITE}/?demo=1`);
    await page.waitFor("!!document.querySelector('.nx-app')");
    await page.evaluate(`(async () => {
      const { useUi } = await import('/src/app/store.ts');
      useUi.getState().addTab({ id: 'settings-share158-demo', kind: 'settings', title: '设置', closable: true });
      useUi.getState().setActiveTab('settings-share158-demo');
      return true;
    })()`);
    await page.waitFor(`document.body.textContent.includes("演示模式不可用")`);
    await sleep(300);
    await pass("C-demo-unsupported-zero-share-http", async () => {
      const state = await page.evaluate(READ_SHARE_CARD);
      assert.ok(state.found, "share card not found in demo");
      assert.ok(state.text.includes("演示模式不可用"), `demo badge missing: ${state.text.slice(0, 120)}`);
      assert.ok(state.text.includes("不会伪造分享列表"), "demo explanation missing");
      assert.equal(state.freeTextInputs, 0);
      const shot = await screenshot(page, "C-demo-sharing.png");
      return { evidence: { shot } };
    });
    // demo 页已登录超管也不得有分享 HTTP: fake 的分享请求计数不得增长。
    await pass("C-demo-no-share-requests", async () => {
      const after = fake.shareRequests().length;
      assert.equal(after, shareCountBefore, `demo page must not fire share HTTP: before=${shareCountBefore} after=${after}`);
      return { evidence: { shareRequestsBefore: shareCountBefore, shareRequestsAfter: after } };
    });
  } finally {
    page.close();
  }
}

let vite;
let chrome;
let fake;
try {
  fake = await startFakeBackend();
  [vite, chrome] = await Promise.all([startVite(), startChrome()]);
  console.warn(`vite=${VITE} fake=http://127.0.0.1:${fake.port}`);

  const page = await newPage(chrome);
  await bootWeb(page, `http://127.0.0.1:${fake.port}`);
  await openSettingsTab(page);
  await renderChecks(page, fake);
  await responsiveChecks(page);
  await plainOwnerChecks(page, fake);
  page.close();

  await demoChecks(chrome, fake);
} catch (error) {
  harnessErrors.push(String(error?.stack || error));
} finally {
  stop(chrome?.process);
  await fake?.close();
  await vite?.stop();
  wipeViteCache();
}

const checks = [...results.values()];
const failed = checks.filter((check) => check.status !== "passed");
const isAllowedPageError = (message) =>
  message.includes("Cannot read properties of undefined (reading 'dimensions')") &&
  message.includes("@xterm_xterm");
const unexpectedPageErrors = pageErrors.filter((message) => !isAllowedPageError(message));
for (const message of unexpectedPageErrors) harnessErrors.push(`unexpected page error: ${message}`);
const report = {
  schema_version: 1,
  status: failed.length || harnessErrors.length ? "failed" : "passed",
  browser: chrome?.version || { status: "unavailable" },
  execution: {
    real_browser: true,
    headless: true,
    jsdom: false,
    transport: "vite dev server + in-process fake HTTP backend (M127/M138/M149 合同形状复述, 非生产后端证据)",
    matrix: "320/390/768 响应式 + 超管/普通 owner 创建 (recipient_username) 与吊销交互 + demo 零分享 HTTP",
  },
  checks,
  harness_errors: harnessErrors,
  page_errors_allowed: pageErrors.filter(isAllowedPageError),
  page_errors_unexpected: unexpectedPageErrors,
};
fs.writeFileSync(path.join(OUT, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
console.warn(`settings sharing acceptance: ${checks.filter((check) => check.status === "passed").length}/${checks.length} checks passed; report=${path.join(OUT, "report.json")}`);
if (failed.length || harnessErrors.length) process.exit(1);
