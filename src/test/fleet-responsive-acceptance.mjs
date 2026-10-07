#!/usr/bin/env node
// FLEET149 设备管理响应式验收: 真实 headless Chrome (CDP) + vite dev server。
// 后端 HTTP 在 CDP Fetch 边界按 internal/fleet/server/http.go 的真实合同伪造:
// 浏览器端请求不带 credentials, 跨源 (?api=) 下真实 cookie 会话无法成立,
// 与 settings-responsive-acceptance 伪造 /rpc 同理; 线路形状由
// src/test/features/fleet-api.test.ts 钉住。覆盖 320/390/桌面 + coarse + 明暗 + demo 显式不可用态。
import { spawn, spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const OUT = path.join(ROOT, "target/fleet-responsive");
const SHOTS = path.join(OUT, "shots");
const VITE_PORT = Number(process.env.NEXTERM_VITE_PORT || 1459);
const VITE = `http://127.0.0.1:${VITE_PORT}`;
const API = "http://fleet.acceptance.invalid";
const results = new Map();
const harnessErrors = [];
const pageErrors = [];
const apiRequests = [];

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

async function waitHttp(url, process, timeout = 60_000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (process?.exitCode !== null && process?.exitCode !== undefined) throw new Error(`${url}: process exited ${process.exitCode}`);
    try {
      const response = await fetch(url);
      if (response.ok) return response;
    } catch {}
    await sleep(150);
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

  async waitFor(expression, timeout = 60_000) {
    const deadline = Date.now() + timeout;
    let last;
    while (Date.now() < deadline) {
      try {
        last = await this.evaluate(expression);
        if (last) return last;
      } catch (error) {
        last = String(error);
      }
      await sleep(150);
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

function startVite() {
  const command = globalThis.process.platform === "win32" ? "pnpm.cmd" : "pnpm";
  const process = spawn(command, ["exec", "vite", "--host", "127.0.0.1", "--port", String(VITE_PORT), "--strictPort"], {
    cwd: ROOT,
    env: { ...globalThis.process.env, NODE_OPTIONS: "" },
    stdio: ["ignore", "pipe", "pipe"],
  });
  return waitHttp(VITE, process).then(() => process);
}

async function screenshot(page, name) {
  const shot = await page.send("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(path.join(SHOTS, name), Buffer.from(shot.data, "base64"));
  return name;
}

async function setViewport(page, width, height, { coarse = false } = {}) {
  await page.send("Emulation.setDeviceMetricsOverride", {
    width,
    height,
    deviceScaleFactor: 1,
    mobile: width <= 560,
  });
  await page.send("Emulation.setTouchEmulationEnabled", { enabled: coarse, maxTouchPoints: coarse ? 5 : 1 });
  await page.send("Emulation.setEmulatedMedia", {
    features: [
      { name: "pointer", value: coarse ? "coarse" : "fine" },
      { name: "any-pointer", value: coarse ? "coarse" : "fine" },
    ],
  });
  await sleep(250);
}

const READ_OVERFLOW = `(() => {
  const de = document.documentElement;
  return {
    innerWidth: window.innerWidth,
    docScrollWidth: de.scrollWidth,
    bodyScrollWidth: document.body.scrollWidth,
  };
})()`;

function assertNoOverflow(sample, label) {
  assert.ok(sample.docScrollWidth <= sample.innerWidth, `${label}: document scrollWidth ${sample.docScrollWidth} > innerWidth ${sample.innerWidth}`);
  assert.ok(sample.bodyScrollWidth <= sample.innerWidth, `${label}: body scrollWidth ${sample.bodyScrollWidth} > innerWidth ${sample.innerWidth}`);
}

const NOW = Date.now();
const DEVICES = {
  devices: [
    {
      id: "d-1",
      name: "fleet-acceptance-web-01-长设备名用于验证窄屏折行",
      kind: "agent",
      created_at: NOW - 86400_000,
      last_seen_at: NOW - 20_000,
      revoked_at: 0,
      owner: { id: "u-1", username: "root" },
      agent: {
        platform: "linux",
        app_version: "0.2.2",
        desired_autostart: true,
        terminal_enabled: true,
        current_url: "https://nexterm.example.com",
        service_state: { installed: true, enabled: true, active: true, last_reconcile_at: NOW - 60_000 },
        last_seen_at: NOW - 20_000,
      },
    },
    {
      id: "d-2",
      name: "fleet-acceptance-db-02",
      kind: "agent",
      created_at: NOW - 172800_000,
      last_seen_at: NOW - 7200_000,
      revoked_at: 0,
      owner: { id: "u-1", username: "root" },
      agent: {
        platform: "windows",
        app_version: "0.2.2",
        desired_autostart: false,
        terminal_enabled: false,
        current_url: "http://10.0.0.8:8080",
        service_state: { installed: true, enabled: false, active: false, last_reconcile_at: NOW - 7200_000, last_error: "服务启动失败: 依赖的 supervisor helper 未运行" },
        last_seen_at: NOW - 7200_000,
      },
    },
    {
      id: "d-3",
      name: "browser-01",
      kind: "browser",
      created_at: NOW - 3600_000,
      last_seen_at: NOW - 60_000,
      revoked_at: 0,
    },
    {
      id: "d-4",
      name: "old-laptop",
      kind: "agent",
      created_at: NOW - 259200_000,
      last_seen_at: NOW - 86400_000,
      revoked_at: NOW - 3600_000,
      owner: { id: "u-2", username: "alice" },
      agent: {
        platform: "darwin",
        app_version: "0.2.1",
        desired_autostart: true,
        terminal_enabled: true,
        current_url: "https://nexterm.example.com",
        service_state: { installed: true, enabled: true, active: false },
        last_seen_at: NOW - 86400_000,
      },
    },
  ],
};
const BASE_URLS = {
  base_urls: [
    { url: "https://nexterm.example.com" },
    { url: "http://10.0.0.8:8080", insecure: true },
    { url: "https://nexterm.example.com/mirror;v2/$(id)" },
  ],
};
const METRICS = {
  samples: [
    { ts: NOW - 120_000, cpu_pct: 8.5, mem_used: 4294967296, mem_total: 17179869184, disk_used: 64424509440, disk_total: 214748364800, uptime_s: 3610 },
    { ts: NOW - 60_000, cpu_pct: 12.5, mem_used: 8589934592, mem_total: 17179869184, disk_used: 107374182400, disk_total: 214748364800, uptime_s: 93784 },
  ],
};
const SESSION_ME = {
  user: {
    id: "u-root",
    username: "root",
    display_name: "Root",
    role: "superadmin",
    state: "active",
    must_change_password: false,
    created_at: 1,
    updated_at: 1,
    last_login_at: 1,
  },
  csrf_token: "csrf-fleet-accept",
};

function jsonResponse(payload) {
  return {
    responseCode: 200,
    responseHeaders: [
      { name: "content-type", value: "application/json" },
      { name: "access-control-allow-origin", value: "*" },
    ],
    body: Buffer.from(JSON.stringify(payload), "utf8").toString("base64"),
  };
}

function preflightResponse() {
  return {
    responseCode: 204,
    responseHeaders: [
      { name: "access-control-allow-origin", value: "*" },
      { name: "access-control-allow-methods", value: "GET,POST,PUT,DELETE,OPTIONS" },
      { name: "access-control-allow-headers", value: "content-type,x-nexterm-csrf" },
      { name: "access-control-max-age", value: "600" },
    ],
  };
}

async function installApiFabrication(page) {
  await page.send("Fetch.enable", {
    patterns: [
      { urlPattern: "*/auth/*" },
      { urlPattern: "*/fleet/*" },
      { urlPattern: "*/device/enroll-codes*" },
      { urlPattern: "*/rpc*" },
    ],
  });
  page.on("Fetch.requestPaused", async (params) => {
    const passthrough = async () => {
      try {
        await page.send("Fetch.continueRequest", { requestId: params.requestId });
      } catch {}
    };
    try {
      const url = new URL(params.request.url);
      if (url.origin !== API) {
        await passthrough();
        return;
      }
      const method = params.request.method;
      const pathName = url.pathname;
      if (method === "OPTIONS") {
        await page.send("Fetch.fulfillRequest", { requestId: params.requestId, ...preflightResponse() });
        return;
      }
      apiRequests.push(`${method} ${pathName}`);
      if (pathName === "/auth/status" && method === "GET") {
        await page.send("Fetch.fulfillRequest", { requestId: params.requestId, ...jsonResponse({ initialized: true, registration_open: false, auth: "on" }) });
        return;
      }
      if (pathName === "/auth/me" && method === "GET") {
        await page.send("Fetch.fulfillRequest", { requestId: params.requestId, ...jsonResponse(SESSION_ME) });
        return;
      }
      if (pathName === "/fleet/devices" && method === "GET") {
        await page.send("Fetch.fulfillRequest", { requestId: params.requestId, ...jsonResponse(DEVICES) });
        return;
      }
      if (pathName === "/fleet/base-urls" && method === "GET") {
        await page.send("Fetch.fulfillRequest", { requestId: params.requestId, ...jsonResponse(BASE_URLS) });
        return;
      }
      if (pathName === "/fleet/base-urls" && method === "PUT") {
        const body = JSON.parse(params.request.postData || "{}");
        await page.send("Fetch.fulfillRequest", { requestId: params.requestId, ...jsonResponse(body) });
        return;
      }
      if (pathName === "/fleet/devices/d-1/metrics" && method === "GET") {
        await page.send("Fetch.fulfillRequest", { requestId: params.requestId, ...jsonResponse(METRICS) });
        return;
      }
      if (pathName === "/device/enroll-codes" && method === "POST") {
        await page.send("Fetch.fulfillRequest", {
          requestId: params.requestId,
          ...jsonResponse({ code: "fleet-accept-enroll-code-9f3ac2", expires_at: Date.now() + 900_000 }),
        });
        return;
      }
      if (/\/fleet\/devices\/[^/]+\/(revoke|autostart)$/.test(pathName) && method === "POST") {
        const body = JSON.parse(params.request.postData || "{}");
        await page.send("Fetch.fulfillRequest", {
          requestId: params.requestId,
          ...jsonResponse({ ok: true, ...("desired" in body ? { desired_autostart: body.desired } : {}) }),
        });
        return;
      }
      if (pathName === "/rpc" && method === "POST") {
        let cmd = null;
        try {
          cmd = JSON.parse(params.request.postData || "{}").cmd;
        } catch {}
        const data =
          cmd === "session_list" ? [] :
          cmd === "vault_status" ? { initialized: true, unlocked: true } :
          cmd === "asset_list" ? [] :
          cmd === "group_list" ? [] :
          cmd === "layout_get" ? { revision: 0, updatedAt: 0, data: null } :
          cmd === "layout_put" ? { saved: true, revision: 1, conflict: false } :
          null;
        await page.send("Fetch.fulfillRequest", { requestId: params.requestId, ...jsonResponse({ ok: true, data }) });
        return;
      }
      await page.send("Fetch.fulfillRequest", {
        requestId: params.requestId,
        responseCode: 404,
        responseHeaders: [
          { name: "content-type", value: "application/json" },
          { name: "access-control-allow-origin", value: "*" },
        ],
        body: Buffer.from(JSON.stringify({ error: { code: "not_found", message: `${method} ${pathName}` } }), "utf8").toString("base64"),
      });
    } catch (error) {
      harnessErrors.push(`api fabrication: ${String(error?.stack || error)}`);
      await passthrough();
    }
  });
}

async function bootWeb(page, theme) {
  const { identifier } = await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `
      try { localStorage.clear(); } catch {}
      window.__NEXTERM_TRANSPORT__ = "web";
      try { localStorage.setItem("nexterm.theme.v1", ${JSON.stringify(theme)}); } catch {}
    `,
  });
  try {
    await page.navigate(`${VITE}/?api=${API}`);
    await page.waitFor("!!document.querySelector('.nx-app')");
    await page.waitFor(`(async () => { const { useUi } = await import('/src/app/store.ts'); return useUi.getState().workspaces !== undefined; })()`);
  } finally {
    await page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier });
  }
}

async function openDevicesTab(page) {
  await page.evaluate(`(async () => {
    const { useUi } = await import('/src/app/store.ts');
    useUi.getState().addTab({ id: 'devices-fleet149', kind: 'devices', title: '设备管理', closable: true });
    useUi.getState().setActiveTab('devices-fleet149');
    return true;
  })()`);
  await page.waitFor(`document.body.textContent.includes("fleet-acceptance-web-01")`);
  await sleep(400);
}

const READ_TOOLBAR = `(() => {
  const buttons = [...document.querySelectorAll(".nx-pane .nx-toolbar button")];
  return buttons.map((b) => {
    const r = b.getBoundingClientRect();
    return { text: b.textContent?.trim(), right: Math.round(r.right), reachable: r.right <= window.innerWidth && r.left >= 0 };
  });
})()`;

const READ_ENROLL = `(() => {
  const code = [...document.querySelectorAll("code")].find((c) => c.textContent?.includes("fleet-accept-enroll-code"));
  if (!code) return { found: false };
  const commands = [...document.querySelectorAll("code")]
    .filter((c) => c.textContent?.includes("agent enroll"))
    .map((c) => ({ text: c.textContent, wraps: c.scrollWidth <= c.clientWidth + 1, inside: c.getBoundingClientRect().right <= window.innerWidth + 1 }));
  const install = [...document.querySelectorAll("code")].find((c) => c.textContent?.includes("agent install"));
  return {
    found: true,
    codeText: code.textContent,
    commands,
    installText: install?.textContent || "",
  };
})()`;

const READ_METRICS = `(() => {
  const pane = document.querySelector(".nx-pane");
  if (!pane) return { found: false };
  const text = pane.textContent || "";
  return {
    found: text.includes("CPU") && text.includes("运行时长"),
    cpu: text.includes("12.5%"),
    mem: text.includes("8.0 GiB / 16.0 GiB"),
    uptime: text.includes("1 天 2 小时"),
  };
})()`;

const READ_BASE_URL_ROWS = `(() => {
  const codes = [...document.querySelectorAll("code")].filter((c) => c.textContent?.startsWith("http"));
  return codes.map((c) => {
    const row = c.closest("div");
    const r = row.getBoundingClientRect();
    return { url: c.textContent, inside: r.right <= window.innerWidth + 1 && r.left >= -1 };
  });
})()`;

const READ_REVOKE_DIALOG = `(() => {
  const dialog = document.querySelector("[role='dialog'], .nx-dialog, [class*='dialog']");
  const buttons = [...document.querySelectorAll("button")].map((b) => b.textContent?.trim());
  return {
    bodyHasCopy: document.body.textContent.includes("吊销后设备凭证立即失效"),
    hasConfirm: buttons.includes("确定"),
    hasCancel: buttons.includes("取消"),
  };
})()`;

async function enrollFlowChecks(page) {
  await pass("C-enroll-issue-320", async () => {
    await setViewport(page, 320, 720);
    await page.evaluate(`(() => {
      const btn = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("接入新设备"));
      if (!btn) throw new Error("enroll open button not found");
      btn.click();
    })()`);
    await page.waitFor(`[...document.querySelectorAll("button")].some((b) => b.textContent?.trim() === "签发接入码")`);
    await page.evaluate(`[...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "签发接入码").click()`);
    await page.waitFor(`document.body.textContent.includes("fleet-accept-enroll-code")`);
    const state = await page.evaluate(READ_ENROLL);
    assert.ok(state.found, `enroll code not visible: ${JSON.stringify(state)}`);
    assert.equal(state.codeText, "fleet-accept-enroll-code-9f3ac2", `code mismatch: ${JSON.stringify(state)}`);
    const dataDir = '"${NEXTERM_DATA_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/NexTerm}"';
    const commandTexts = state.commands.map((c) => c.text);
    assert.ok(
      commandTexts.some((t) => t.includes(`nexterm-server agent enroll --server 'https://nexterm.example.com' --code 'fleet-accept-enroll-code-9f3ac2' --data-dir ${dataDir}`)),
      `enroll command must use the released nexterm-server CLI with per-user data dir: ${JSON.stringify(state)}`,
    );
    assert.ok(
      commandTexts.some((t) => t.includes(`nexterm-server agent enroll --server 'http://10.0.0.8:8080' --code 'fleet-accept-enroll-code-9f3ac2' --insecure --data-dir ${dataDir}`)),
      `insecure base URL must carry --insecure: ${JSON.stringify(state)}`,
    );
    assert.ok(
      commandTexts.some((t) => t.includes("--server 'https://nexterm.example.com/mirror;v2/$(id)'")),
      `shell-special URL path must be single-quoted: ${JSON.stringify(state)}`,
    );
    assert.ok(
      state.installText.includes(`nexterm-server agent install --data-dir ${dataDir}`),
      `install command must share the same per-user data dir: ${JSON.stringify(state)}`,
    );
    assert.ok(
      !commandTexts.some((t) => t.includes("/var/lib")) && !state.installText.includes("/var/lib"),
      `root-owned system dir must not appear: ${JSON.stringify(state)}`,
    );
    assert.ok(
      [...commandTexts, state.installText].every((t) => {
        const dir = t.split("--data-dir ")[1] ?? "";
        return (
          dir.startsWith('"') &&
          dir.endsWith('"') &&
          dir.indexOf("NEXTERM_DATA_DIR") >= 0 &&
          dir.indexOf("NEXTERM_DATA_DIR") < dir.indexOf("XDG_DATA_HOME") &&
          dir.indexOf("XDG_DATA_HOME") < dir.indexOf("$HOME")
        );
      }),
      `data dir must keep NEXTERM_DATA_DIR > XDG_DATA_HOME > $HOME expansion order in one quoted arg: ${JSON.stringify(commandTexts)}`,
    );
    for (const command of state.commands) {
      assert.equal(command.wraps, true, `enroll command overflows: ${JSON.stringify(command)}`);
      assert.equal(command.inside, true, `enroll command outside viewport: ${JSON.stringify(command)}`);
    }
    const sample = await page.evaluate(READ_OVERFLOW);
    assertNoOverflow(sample, "enroll 320");
    const shot = await screenshot(page, "C-enroll-320.png");
    return { evidence: { state: { codeText: state.codeText, commandTexts, installText: state.installText }, shot } };
  });
}

async function deviceChecks(page) {
  await pass("D-metrics-expand-390", async () => {
    await setViewport(page, 390, 780);
    await page.evaluate(`(() => {
      const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("fleet-acceptance-web-01"));
      const btn = [...card.querySelectorAll("button")].find((b) => b.textContent?.trim() === "指标");
      if (!btn) throw new Error("metrics button not found");
      btn.click();
    })()`);
    await page.waitFor(`document.body.textContent.includes("运行时长")`);
    const state = await page.evaluate(READ_METRICS);
    assert.ok(state.found, `metrics not rendered: ${JSON.stringify(state)}`);
    assert.equal(state.cpu, true, `cpu value missing: ${JSON.stringify(state)}`);
    assert.equal(state.mem, true, `mem value missing: ${JSON.stringify(state)}`);
    assert.equal(state.uptime, true, `uptime missing: ${JSON.stringify(state)}`);
    const sample = await page.evaluate(READ_OVERFLOW);
    assertNoOverflow(sample, "metrics 390");
    const shot = await screenshot(page, "D-metrics-390.png");
    return { evidence: { state, shot } };
  });

  await pass("E-autostart-mutation-posted", async () => {
    const before = apiRequests.length;
    await setViewport(page, 1280, 800);
    await page.evaluate(`(() => {
      const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("fleet-acceptance-web-01"));
      const box = card.querySelector('input[type="checkbox"]');
      if (!box) throw new Error("autostart checkbox not found");
      box.click();
    })()`);
    await page.waitFor(`document.body.textContent.includes("期望自启动设为关闭")`);
    const sent = apiRequests.slice(before).filter((r) => r === "POST /fleet/devices/d-1/autostart");
    assert.equal(sent.length, 1, `autostart POST not captured: ${JSON.stringify(apiRequests.slice(before))}`);
    return { evidence: { sent } };
  });

  await pass("E-revoke-dialog-honest-copy", async () => {
    await page.evaluate(`(() => {
      const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("fleet-acceptance-db-02"));
      const btn = [...card.querySelectorAll("button")].find((b) => b.textContent?.includes("吊销"));
      if (!btn) throw new Error("revoke button not found");
      btn.click();
    })()`);
    await page.waitFor(`document.body.textContent.includes("吊销后设备凭证立即失效")`);
    const state = await page.evaluate(READ_REVOKE_DIALOG);
    assert.equal(state.bodyHasCopy, true, "revoke dialog must state irreversible effect");
    assert.equal(state.hasConfirm, true, "revoke dialog needs 确定");
    assert.equal(state.hasCancel, true, "revoke dialog needs 取消");
    const shot = await screenshot(page, "E-revoke-dialog.png");
    await page.evaluate(`(() => {
      const btn = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "取消");
      btn?.click();
    })()`);
    return { evidence: { state, shot } };
  });

  await pass("E-base-url-rows-320", async () => {
    await setViewport(page, 320, 720);
    const rows = await page.evaluate(READ_BASE_URL_ROWS);
    assert.ok(rows.length >= 2, `base url rows expected: ${JSON.stringify(rows)}`);
    for (const row of rows) {
      assert.equal(row.inside, true, `base url row outside viewport: ${JSON.stringify(row)}`);
    }
    const shot = await screenshot(page, "E-base-urls-320.png");
    return { evidence: { rows, shot } };
  });
}

async function matrixChecks(page, label) {
  for (const [width, height] of [[320, 720], [390, 780], [1280, 800]]) {
    await pass(`${label}-no-overflow-${width}x${height}`, async () => {
      await setViewport(page, width, height);
      const sample = await page.evaluate(READ_OVERFLOW);
      assertNoOverflow(sample, `${label} ${width}x${height}`);
      const toolbar = await page.evaluate(READ_TOOLBAR);
      for (const button of toolbar) {
        assert.equal(button.reachable, true, `${label} ${width}: toolbar button unreachable: ${JSON.stringify(button)}`);
      }
      const shot = await screenshot(page, `${label}-${width}x${height}.png`);
      return { evidence: { innerWidth: sample.innerWidth, docScrollWidth: sample.docScrollWidth, shot } };
    });
  }
  await pass(`${label}-no-overflow-320-coarse`, async () => {
    await setViewport(page, 320, 720, { coarse: true });
    const sample = await page.evaluate(READ_OVERFLOW);
    assertNoOverflow(sample, `${label} 320 coarse`);
    const shot = await screenshot(page, `${label}-320-coarse.png`);
    return { evidence: { shot } };
  });
}

let vite;
let chrome;
try {
  vite = await startVite();
  chrome = await startChrome();
  console.warn(`vite=${VITE} api=${API} (fabricated at CDP boundary)`);

  const pageA = await newPage(chrome);
  await installApiFabrication(pageA);
  await bootWeb(pageA, "dark");
  record("A-boot", "passed", { evidence: { requests: apiRequests.length } });
  await openDevicesTab(pageA);
  await matrixChecks(pageA, "A-dark");
  await enrollFlowChecks(pageA);
  await deviceChecks(pageA);

  await bootWeb(pageA, "light");
  await openDevicesTab(pageA);
  for (const [width, height] of [[320, 720], [390, 780]]) {
    await pass(`A-light-no-overflow-${width}`, async () => {
      await setViewport(pageA, width, height);
      const sample = await pageA.evaluate(READ_OVERFLOW);
      assertNoOverflow(sample, `light ${width}`);
      const shot = await screenshot(pageA, `A-light-${width}.png`);
      return { evidence: { innerWidth: sample.innerWidth, docScrollWidth: sample.docScrollWidth, shot } };
    });
  }
  pageA.close();

  const pageB = await newPage(chrome);
  const demoFleetRequests = [];
  await pageB.send("Fetch.enable", { patterns: [{ urlPattern: "*/fleet/*" }, { urlPattern: "*/device/enroll-codes*" }] });
  pageB.on("Fetch.requestPaused", async (params) => {
    const pathName = new URL(params.request.url).pathname;
    if (pathName.startsWith("/fleet/") || pathName === "/device/enroll-codes") {
      demoFleetRequests.push(`${params.request.method} ${pathName}`);
    }
    try {
      await pageB.send("Fetch.continueRequest", { requestId: params.requestId });
    } catch {}
  });
  await pageB.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `try { localStorage.clear(); } catch {}`,
  });
  await pageB.navigate(`${VITE}/?demo=1`);
  await pageB.waitFor("!!document.querySelector('.nx-app')");
  await pageB.waitFor(`(async () => { const { useUi } = await import('/src/app/store.ts'); return useUi.getState().workspaces !== undefined; })()`);
  await pageB.evaluate(`(async () => {
    const { useUi } = await import('/src/app/store.ts');
    useUi.getState().addTab({ id: 'devices-fleet149-demo', kind: 'devices', title: '设备管理', closable: true });
    useUi.getState().setActiveTab('devices-fleet149-demo');
    return true;
  })()`);
  await pageB.waitFor(`document.body.textContent.includes("演示模式没有设备管理")`);
  await pass("B-demo-explicit-unsupported", async () => {
    await setViewport(pageB, 320, 720);
    assert.equal(
      demoFleetRequests.length,
      0,
      `demo transport must not fire fleet requests: ${JSON.stringify(demoFleetRequests)}`,
    );
    const text = await pageB.evaluate(`document.body.textContent.includes("不会伪造设备列表")`);
    assert.equal(text, true, "demo unsupported copy missing");
    const sample = await pageB.evaluate(READ_OVERFLOW);
    assertNoOverflow(sample, "demo unsupported 320");
    const shot = await screenshot(pageB, "B-demo-unsupported-320.png");
    return { evidence: { demoFleetRequests, shot } };
  });
  pageB.close();
} catch (error) {
  harnessErrors.push(String(error?.stack || error));
} finally {
  stop(chrome?.process);
  stop(vite);
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
    transport: "web transport against CDP-fabricated fleet/auth HTTP contract; demo transport for unsupported-state check",
    matrix: "320/390/1280 + coarse pointer + dark/light",
  },
  checks,
  harness_errors: harnessErrors,
  page_errors_allowed: pageErrors.filter(isAllowedPageError),
  page_errors_unexpected: unexpectedPageErrors,
  api_requests: apiRequests,
};
fs.writeFileSync(path.join(OUT, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
console.warn(`fleet responsive acceptance: ${checks.filter((check) => check.status === "passed").length}/${checks.length} checks passed; report=${path.join(OUT, "report.json")}`);
if (failed.length || harnessErrors.length) process.exit(1);
