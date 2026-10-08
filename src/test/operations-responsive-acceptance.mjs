#!/usr/bin/env node
import { spawn, spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { startVite } from "./lib/acceptance-process.mjs";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const OUT = path.join(ROOT, "target/acceptance-operations-responsive");
const VITE_PORT = Number(process.env.NEXTERM_VITE_PORT || 0) || (await freePort());
const VITE = `http://127.0.0.1:${VITE_PORT}`;
const results = new Map();
const harnessErrors = [];

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
  try {
    process.kill(-process.pid, "SIGTERM");
  } catch {
    try { process.kill("SIGTERM"); } catch {}
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
  return CDP.connect((await response.json()).webSocketDebuggerUrl);
}

async function screenshot(page, name) {
  const shot = await page.send("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(path.join(OUT, name), Buffer.from(shot.data, "base64"));
  return name;
}

async function setViewport(page, vp) {
  await page.send("Emulation.setDeviceMetricsOverride", {
    width: vp.width,
    height: vp.height,
    deviceScaleFactor: 1,
    mobile: Boolean(vp.touch),
  });
  if (vp.touch) {
    await page.send("Emulation.setTouchEmulationEnabled", { enabled: true, maxTouchPoints: 5 });
  } else {
    await page.send("Emulation.setTouchEmulationEnabled", { enabled: false });
  }
}

async function boot(page, { theme = "dark" } = {}) {
  const { identifier } = await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `
      try { localStorage.clear(); } catch {}
      try { localStorage.setItem("nexterm.theme.v1", ${JSON.stringify(theme)}); } catch {}
    `,
  });
  try {
    await page.navigate(`${VITE}/?demo=1`);
    await page.waitFor(
      "Boolean([...document.querySelectorAll('.xterm')].some((el) => el.getClientRects().length > 0))",
    );
  } finally {
    await page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier });
  }
}

const RECT_HELPER = `
function __nxRect(el) {
  if (!el) return null;
  const r = el.getBoundingClientRect();
  return { left: r.left, right: r.right, top: r.top, bottom: r.bottom, width: r.width, height: r.height };
}
function __nxHit(el) {
  if (!el) return { ok: false, reason: "missing" };
  const r = el.getBoundingClientRect();
  if (r.width <= 0 || r.height <= 0) return { ok: false, reason: "zero-size" };
  const cx = r.left + r.width / 2;
  const cy = r.top + r.height / 2;
  if (cx < 0 || cy < 0 || cx >= window.innerWidth || cy >= window.innerHeight) {
    return { ok: false, reason: "center-outside-viewport(" + cx + "," + cy + ")" };
  }
  const hit = document.elementFromPoint(cx, cy);
  const ok = hit === el || Boolean(hit && el.contains(hit));
  return { ok, hit: hit ? hit.tagName + "|" + String(hit.className).slice(0, 60) : null };
}
function __nxHitWhat(el) {
  if (!el) return null;
  const r = el.getBoundingClientRect();
  const cx = Math.min(Math.max(r.left + r.width / 2, 1), window.innerWidth - 1);
  const cy = Math.min(Math.max(r.top + r.height / 2, 1), window.innerHeight - 1);
  const hit = document.elementFromPoint(cx, cy);
  if (!hit) return null;
  return hit.tagName + "|" + String(hit.className).slice(0, 80) + "|" + (hit.textContent ?? "").slice(0, 40);
}
function __nxActivePane() {
  return [...document.querySelectorAll(".nx-pane")].find((p) => p.getClientRects().length > 0) ?? null;
}
`;

async function openAssetTree(page) {
  await page.evaluate(`(() => {
    const btn = document.querySelector('button[aria-label="资产"]');
    if (!btn) throw new Error("assets rail button not found");
    btn.click();
  })()`);
  await page.waitFor(`Boolean(document.querySelector('button[title="新建资产"]'))`);
}

async function connectAsset(page, name) {
  await openAssetTree(page);
  const inline = `button[aria-label="连接 ${name}"]`;
  const more = `button[aria-label="更多操作 ${name}"]`;
  await page.waitFor(`Boolean(document.querySelector('${inline}') || document.querySelector('${more}'))`);
  if (await page.evaluate(`Boolean(document.querySelector('${inline}'))`)) {
    await page.evaluate(`document.querySelector('${inline}').click()`);
  } else {
    await page.evaluate(`document.querySelector('${more}').click()`);
    await page.waitFor(`Boolean(document.querySelector('.nx-menu'))`);
    await page.evaluate(`[...document.querySelectorAll('.nx-menu-item')].find((b) => b.textContent?.includes('连接'))?.click()`);
  }
  await sleep(250);
  await page.evaluate(`(() => {
    const backdrop = document.querySelector(".nx-dock-backdrop");
    if (backdrop) backdrop.click();
  })()`);
  for (let i = 0; i < 5; i++) {
    const clicked = await page.evaluate(`(() => {
      const btns = [...document.querySelectorAll(".nx-toasts button")];
      btns.forEach((b) => b.click());
      return btns.length;
    })()`);
    if (!clicked) break;
    await sleep(150);
  }
}

async function checkRedis(page, vp, label) {
  await connectAsset(page, "redis-cache");
  await page.waitFor(`document.body.textContent.includes("products:all")`);
  await page.evaluate(`(() => {
    const row = [...document.querySelectorAll('.nx-row')].find((r) => r.textContent?.includes('products:all'));
    if (!row) throw new Error("redis key row not found");
    row.click();
  })()`);
  await page.waitFor(`document.body.textContent.includes("秒后过期")`);
  const m = await page.evaluate(`(() => {
    ${RECT_HELPER}
    const pane = [...document.querySelectorAll(".nx-pane")].find((p) => p.textContent.includes("命令台"));
    const pre = pane ? pane.querySelector("pre.nx-pre") : null;
    const cmdInput = pane ? pane.querySelector('input[aria-label="Redis 命令"]') : null;
    return {
      innerWidth: window.innerWidth,
      detail: __nxRect(pre),
      cmdInput: __nxRect(cmdInput),
      cmdHit: __nxHit(cmdInput),
      cmdHitWhat: __nxHitWhat(cmdInput),
    };
  })()`);
  assert.ok(m.detail, `${label}: redis detail <pre> not found`);
  assert.ok(m.detail.width > 0, `${label}: redis detail width must be nonzero, got ${JSON.stringify(m.detail)}`);
  assert.ok(m.detail.height > 0, `${label}: redis detail height must be nonzero, got ${JSON.stringify(m.detail)}`);
  assert.ok(m.detail.right <= m.innerWidth + 1, `${label}: redis detail overflows viewport: ${JSON.stringify(m.detail)}`);
  assert.equal(m.cmdHit.ok, true, `${label}: redis command input not strictly hittable: ${JSON.stringify(m)}`);
  await screenshot(page, `redis-${label}.png`);
  return { evidence: m };
}

async function checkRedisConsole(page, label) {
  await connectAsset(page, "redis-cache");
  await page.waitFor(`document.body.textContent.includes("products:all")`);

  const cmdPane = `[...document.querySelectorAll(".nx-pane")].find((p) => p.textContent.includes("命令台"))`;
  const hitCmdInput = `(() => {
    ${RECT_HELPER}
    const pane = ${cmdPane};
    const input = pane ? pane.querySelector('input[aria-label="Redis 命令"]') : null;
    return { rect: __nxRect(input), hit: __nxHit(input) };
  })()`;

  const beforeSelect = await page.evaluate(hitCmdInput);
  assert.equal(beforeSelect.hit.ok, true, `${label}: redis command input not hittable before key selection: ${JSON.stringify(beforeSelect)}`);

  await page.evaluate(`(() => {
    const row = [...document.querySelectorAll('.nx-row')].find((r) => r.textContent?.includes('products:all'));
    if (!row) throw new Error("redis key row not found");
    row.click();
  })()`);
  await page.waitFor(`document.body.textContent.includes("秒后过期")`);

  const guidance = await page.evaluate(`(() => {
    const pane = ${cmdPane};
    return pane ? pane.textContent : "";
  })()`);
  assert.ok(guidance.includes("执行前会要求确认"), `${label}: dangerous-command guidance must be retained`);
  assert.ok(guidance.includes("不支持引号"), `${label}: argument-split guidance must be retained`);

  const runCommand = async (text) => {
    await page.evaluate(`(() => {
      const pane = ${cmdPane};
      const input = pane.querySelector('input[aria-label="Redis 命令"]');
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set;
      setter.call(input, ${JSON.stringify(text)});
      input.dispatchEvent(new Event("input", { bubbles: true }));
      const btn = [...pane.querySelectorAll("button")].find((b) => b.textContent?.trim() === "执行");
      if (!btn) throw new Error("执行 button not found");
      btn.click();
    })()`);
  };

  await runCommand("PING");
  await page.waitFor(`(() => {
    const pre = ${cmdPane}.querySelector('pre[role="status"]');
    return Boolean(pre && pre.textContent.includes("PONG"));
  })()`);

  const afterRun = await page.evaluate(`(() => {
    ${RECT_HELPER}
    const pane = ${cmdPane};
    const input = pane.querySelector('input[aria-label="Redis 命令"]');
    const out = pane.querySelector('pre[role="status"]');
    const region = out ? out.closest(".overflow-auto") : null;
    const outRect = __nxRect(out);
    const regionRect = __nxRect(region);
    const visible = Boolean(outRect && regionRect && outRect.bottom <= regionRect.bottom + 1 && outRect.bottom > regionRect.top);
    return { rect: __nxRect(input), hit: __nxHit(input), outRect, regionRect, visible };
  })()`);
  assert.equal(afterRun.hit.ok, true, `${label}: redis command input not hittable after command output rendered: ${JSON.stringify(afterRun)}`);
  assert.equal(afterRun.visible, true, `${label}: command output must be visible inside the console scroll region: ${JSON.stringify(afterRun)}`);

  await runCommand("FLUSHALL");
  await page.waitFor(`Boolean(document.querySelector(".nx-overlay .nx-modal"))`);
  const gate = await page.evaluate(`(() => {
    ${RECT_HELPER}
    const modal = document.querySelector(".nx-overlay .nx-modal");
    const cancel = [...modal.querySelectorAll("button")].find((b) => b.textContent?.trim() === "取消");
    return { text: modal.textContent, rect: __nxRect(modal), innerHeight: window.innerHeight, cancelHit: __nxHit(cancel) };
  })()`);
  assert.ok(gate.text.includes("FLUSHALL"), `${label}: dangerous-command confirm dialog must name the command: ${JSON.stringify(gate)}`);
  assert.ok(gate.rect.top >= 0 && gate.rect.bottom <= gate.innerHeight, `${label}: confirm dialog must fit the viewport: ${JSON.stringify(gate.rect)}`);
  assert.equal(gate.cancelHit.ok, true, `${label}: confirm dialog cancel must be hittable: ${JSON.stringify(gate)}`);
  await page.evaluate(`(() => {
    const modal = document.querySelector(".nx-overlay .nx-modal");
    [...modal.querySelectorAll("button")].find((b) => b.textContent?.trim() === "取消").click();
  })()`);
  await page.waitFor(`!document.querySelector(".nx-overlay .nx-modal")`);
  const afterCancel = await page.evaluate(`(() => {
    const pre = ${cmdPane}.querySelector('pre[role="status"]');
    return pre ? pre.textContent : "";
  })()`);
  assert.ok(afterCancel.includes("PONG") && !afterCancel.includes("未内置"), `${label}: cancelled FLUSHALL must not execute: ${JSON.stringify(afterCancel)}`);

  await page.evaluate(`(() => {
    const pane = ${cmdPane};
    const btn = [...pane.querySelectorAll("button")].find((b) => b.textContent?.trim() === "改过期时间");
    if (!btn) throw new Error("改过期时间 button not found");
    btn.click();
  })()`);
  await page.waitFor(`Boolean(document.querySelector(".nx-overlay .nx-modal input"))`);
  const ttl = await page.evaluate(`(() => {
    ${RECT_HELPER}
    const modal = document.querySelector(".nx-overlay .nx-modal");
    const input = modal.querySelector("input");
    const cancel = [...modal.querySelectorAll("button")].find((b) => b.textContent?.trim() === "取消");
    return { text: modal.textContent, value: input.value, rect: __nxRect(modal), innerHeight: window.innerHeight, cancelHit: __nxHit(cancel) };
  })()`);
  assert.ok(ttl.text.includes("填 0 会立即删除该键"), `${label}: TTL dialog must keep the delete warning: ${JSON.stringify(ttl)}`);
  assert.ok(/^\d+$/.test(ttl.value), `${label}: TTL dialog must prefill the current TTL: ${JSON.stringify(ttl)}`);
  assert.ok(ttl.rect.top >= 0 && ttl.rect.bottom <= ttl.innerHeight, `${label}: TTL dialog must fit the viewport: ${JSON.stringify(ttl.rect)}`);
  assert.equal(ttl.cancelHit.ok, true, `${label}: TTL dialog cancel must be hittable: ${JSON.stringify(ttl)}`);
  await page.evaluate(`(() => {
    const modal = document.querySelector(".nx-overlay .nx-modal");
    [...modal.querySelectorAll("button")].find((b) => b.textContent?.trim() === "取消").click();
  })()`);
  await page.waitFor(`!document.querySelector(".nx-overlay .nx-modal")`);

  await screenshot(page, `redis-console-${label}.png`);
  return { evidence: { beforeSelect, afterRun, gate: gate.rect, ttl: ttl.rect } };
}

async function checkMysql(page, vp, label) {
  await connectAsset(page, "db-prod");
  await page.waitFor(
    `Boolean([...document.querySelectorAll('.nx-pane')].some((p) => p.getClientRects().length > 0 && p.textContent.includes('orders')))`,
  );
  const runVisible = await page.evaluate(`(() => {
    ${RECT_HELPER}
    const pane = [...document.querySelectorAll(".nx-pane")].find((p) => p.getClientRects().length > 0 && p.textContent.includes("MySQL"));
    const btn = pane ? [...pane.querySelectorAll(".nx-toolbar button")].find((b) => b.textContent?.includes("运行")) : null;
    if (!btn) return { found: false };
    const toolbar = btn.closest(".nx-toolbar");
    return { found: true, innerWidth: window.innerWidth, btn: __nxRect(btn), toolbar: __nxRect(toolbar), hit: __nxHit(btn) };
  })()`);
  assert.equal(runVisible.found, true, `${label}: MySQL run button not found`);
  assert.ok(runVisible.btn.right <= runVisible.innerWidth + 1, `${label}: run button offscreen: ${JSON.stringify(runVisible)}`);
  assert.ok(runVisible.btn.right <= runVisible.toolbar.right + 1, `${label}: run button escapes toolbar: ${JSON.stringify(runVisible)}`);
  assert.equal(runVisible.hit.ok, true, `${label}: run button not strictly hittable: ${JSON.stringify(runVisible)}`);
  await page.evaluate(`(() => {
    const pane = [...document.querySelectorAll(".nx-pane")].find((p) => p.getClientRects().length > 0 && p.textContent.includes("MySQL"));
    const btn = [...pane.querySelectorAll(".nx-toolbar button")].find((b) => b.textContent?.includes("运行"));
    btn.click();
  })()`);
  await page.waitFor(
    `Boolean([...document.querySelectorAll('.nx-pane')].some((p) => p.getClientRects().length > 0 && p.querySelector('.nx-table')))`,
  );
  const m = await page.evaluate(`(() => {
    ${RECT_HELPER}
    const pane = [...document.querySelectorAll(".nx-pane")].find((p) => p.getClientRects().length > 0 && p.querySelector(".nx-table"));
    const table = pane.querySelector(".nx-table");
    let el = table;
    while (el && !String(el.className).includes("overflow-auto")) el = el.parentElement;
    return { innerHeight: window.innerHeight, results: __nxRect(el), tableRows: table.querySelectorAll("tbody tr").length };
  })()`);
  assert.ok(m.results, `${label}: results container not found`);
  assert.ok(m.results.height > 0, `${label}: MySQL results height must be nonzero: ${JSON.stringify(m)}`);
  assert.ok(m.tableRows > 0, `${label}: MySQL results must contain rows: ${JSON.stringify(m)}`);
  await screenshot(page, `mysql-${label}.png`);
  return { evidence: { runVisible, results: m } };
}

async function checkDocker(page, vp, label) {
  await connectAsset(page, "官网 docker");
  await page.waitFor(
    `Boolean([...document.querySelectorAll('.nx-pane')].some((p) => p.getClientRects().length > 0 && p.querySelector('table tbody tr td:last-child button')))`,
  );
  const m = await page.evaluate(`(() => {
    ${RECT_HELPER}
    const pane = __nxActivePane();
    const td = pane.querySelector("table tbody tr td:last-child");
    const wrapper = td.closest(".overflow-auto");
    const buttons = [...td.querySelectorAll("button")];
    return {
      innerWidth: window.innerWidth,
      td: __nxRect(td),
      wrapper: __nxRect(wrapper),
      buttons: buttons.map((b) => ({ title: b.title, rect: __nxRect(b), hit: __nxHit(b) })),
      sticky: getComputedStyle(td).position,
    };
  })()`);
  assert.equal(m.sticky, "sticky", `${label}: container action cell must be sticky: ${JSON.stringify(m)}`);
  assert.ok(m.td.width > 0 && m.td.right <= m.wrapper.right + 1, `${label}: action cell not pinned into view: ${JSON.stringify(m)}`);
  assert.ok(m.td.right <= m.innerWidth + 1, `${label}: action cell offscreen: ${JSON.stringify(m)}`);
  assert.ok(m.buttons.length >= 6, `${label}: all six row actions must render: ${JSON.stringify(m.buttons)}`);
  for (const b of m.buttons) {
    assert.ok(
      b.rect.left >= -1 && b.rect.right <= m.innerWidth + 1,
      `${label}: row action ${b.title} outside viewport: ${JSON.stringify(b)}`,
    );
    assert.equal(b.hit.ok, true, `${label}: row action ${b.title} not strictly hittable: ${JSON.stringify(b)}`);
  }
  await screenshot(page, `docker-${label}.png`);

  await page.evaluate(`(() => {
    ${RECT_HELPER}
    const pane = __nxActivePane();
    pane.querySelector('button[title="详情 / 统计 / 文件"]').click();
  })()`);
  await page.waitFor(
    `Boolean([...document.querySelectorAll('.nx-pane')].some((p) => p.getClientRects().length > 0 && p.querySelector('.nx-toolbar .nx-segment')))`,
  );
  const tabs = await page.evaluate(`(() => {
    ${RECT_HELPER}
    const pane = __nxActivePane();
    const seg = pane.querySelector(".nx-toolbar .nx-segment");
    const items = [...seg.querySelectorAll("button")];
    return {
      innerWidth: window.innerWidth,
      seg: __nxRect(seg),
      items: items.map((b) => ({ label: b.textContent?.trim(), rect: __nxRect(b), hit: __nxHit(b) })),
    };
  })()`);
  assert.ok(tabs.seg.right <= tabs.innerWidth + 1, `${label}: insight tabs offscreen: ${JSON.stringify(tabs)}`);
  for (const item of tabs.items) {
    assert.ok(
      item.rect.left >= -1 && item.rect.right <= tabs.innerWidth + 1,
      `${label}: insight tab ${item.label} outside viewport: ${JSON.stringify(item)}`,
    );
    assert.equal(item.hit.ok, true, `${label}: insight tab ${item.label} not strictly hittable: ${JSON.stringify(item)}`);
  }

  await page.evaluate(`(() => {
    ${RECT_HELPER}
    const pane = __nxActivePane();
    const btn = [...pane.querySelectorAll(".nx-toolbar .nx-segment button")].find((b) => b.textContent?.includes("统计"));
    btn.click();
  })()`);
  await page.waitFor(
    `Boolean([...document.querySelectorAll('.nx-pane')].some((p) => p.getClientRects().length > 0 && p.querySelector('table tbody td')))`,
  );
  const stats = await page.evaluate(`(() => {
    ${RECT_HELPER}
    const pane = __nxActivePane();
    const table = pane.querySelector("table");
    const wrapper = table.closest(".overflow-auto");
    const firstTh = table.querySelector("thead th:first-child");
    const firstTd = table.querySelector("tbody td:first-child");
    const before = { scrollWidth: wrapper.scrollWidth, clientWidth: wrapper.clientWidth };
    wrapper.scrollLeft = wrapper.scrollWidth;
    const pinned = __nxRect(firstTd);
    const wrapRect = __nxRect(wrapper);
    const thText = firstTh.textContent?.trim() ?? null;
    const thHit = __nxHit(firstTh);
    wrapper.scrollLeft = 0;
    return { before, pinned, wrapRect, thText, thHit, nameSticky: getComputedStyle(firstTd).position, cols: table.querySelectorAll("thead th").length };
  })()`);
  assert.equal(stats.cols, 7, `${label}: stats table must keep all 7 diagnostic columns: ${JSON.stringify(stats)}`);
  assert.equal(stats.thText, "容器", `${label}: stats pinned header text must stay 容器: ${JSON.stringify(stats)}`);
  assert.equal(stats.thHit.ok, true, `${label}: stats pinned header not strictly hittable after horizontal scroll: ${JSON.stringify(stats)}`);
  if (stats.before.clientWidth >= 620) {
    assert.ok(
      stats.before.scrollWidth <= stats.before.clientWidth + 1,
      `${label}: stats table must fit without horizontal scroll at ${stats.before.clientWidth}px wrapper: ${JSON.stringify(stats.before)}`,
    );
  } else {
    assert.ok(stats.before.scrollWidth > stats.before.clientWidth, `${label}: stats table should scroll at ${stats.before.clientWidth}px wrapper: ${JSON.stringify(stats.before)}`);
    assert.equal(stats.nameSticky, "sticky", `${label}: stats name column must be sticky: ${JSON.stringify(stats)}`);
    assert.ok(
      Math.abs(stats.pinned.left - stats.wrapRect.left) <= 1,
      `${label}: stats name column must stay pinned while scrolling: ${JSON.stringify(stats)}`,
    );
  }
  await screenshot(page, `insight-stats-${label}.png`);
  return { evidence: { actions: m, tabs, stats } };
}

async function checkForward(page, vp, label) {
  await page.evaluate(`document.querySelector('button[aria-label="端口转发"]').click()`);
  await page.waitFor(
    `Boolean([...document.querySelectorAll('.nx-pane')].some((p) => p.getClientRects().length > 0 && p.querySelector('table tbody tr td:last-child button')))`,
  );
  const m = await page.evaluate(`(() => {
    ${RECT_HELPER}
    const pane = __nxActivePane();
    const td = pane.querySelector("table tbody tr td:last-child");
    const wrapper = td.closest(".overflow-auto");
    const btn = td.querySelector("button");
    return {
      innerWidth: window.innerWidth,
      td: __nxRect(td),
      wrapper: __nxRect(wrapper),
      btn: __nxRect(btn),
      hit: __nxHit(btn),
      sticky: getComputedStyle(td).position,
    };
  })()`);
  assert.equal(m.sticky, "sticky", `${label}: forward action cell must be sticky: ${JSON.stringify(m)}`);
  assert.ok(m.td.right <= m.wrapper.right + 1, `${label}: forward stop action not pinned into view: ${JSON.stringify(m)}`);
  assert.ok(m.btn.right <= m.innerWidth + 1, `${label}: forward stop button offscreen: ${JSON.stringify(m)}`);
  assert.equal(m.hit.ok, true, `${label}: forward stop button not strictly hittable: ${JSON.stringify(m)}`);
  await screenshot(page, `forward-${label}.png`);
  return { evidence: m };
}

async function checkTouchTargets(page, label) {
  const coarse = await page.evaluate(`matchMedia("(pointer: coarse)").matches`);
  assert.equal(coarse, true, `${label}: touch emulation must produce a coarse pointer`);
  await connectAsset(page, "官网 docker");
  await page.waitFor(
    `Boolean([...document.querySelectorAll('.nx-pane')].some((p) => p.getClientRects().length > 0 && p.querySelector('table tbody tr td:last-child button')))`,
  );
  const docker = await page.evaluate(`(() => {
    ${RECT_HELPER}
    const pane = __nxActivePane();
    const btns = [...pane.querySelectorAll("table tbody .nx-icon-btn-sm")];
    const checks = [...pane.querySelectorAll("table .nx-check")];
    return {
      buttons: btns.map((b) => __nxRect(b)),
      checks: checks.map((c) => __nxRect(c)),
    };
  })()`);
  assert.ok(docker.buttons.length >= 6, `${label}: expected row action buttons: ${JSON.stringify(docker)}`);
  for (const b of docker.buttons) {
    assert.ok(b.width >= 24 && b.height >= 24, `${label}: row icon buttons must be ≥24px: ${JSON.stringify(b)}`);
  }
  for (const c of docker.checks) {
    assert.ok(c.width >= 24 && c.height >= 24, `${label}: checkbox hit area must be ≥24px: ${JSON.stringify(c)}`);
  }
  await connectAsset(page, "redis-cache");
  await page.waitFor(`document.body.textContent.includes("products:all")`);
  const redis = await page.evaluate(`(() => {
    ${RECT_HELPER}
    const pane = [...document.querySelectorAll(".nx-pane")].find((p) => p.textContent.includes("命令台"));
    const scan = pane ? [...pane.querySelectorAll("button")].find((b) => b.textContent?.trim() === "SCAN") : null;
    const row = pane ? [...pane.querySelectorAll(".nx-row")].find((r) => r.textContent?.includes("products:all")) : null;
    return { scan: __nxRect(scan), row: __nxRect(row) };
  })()`);
  assert.ok(redis.scan && redis.scan.height >= 24 && redis.scan.width >= 24, `${label}: SCAN button must be ≥24px: ${JSON.stringify(redis.scan)}`);
  assert.ok(redis.row && redis.row.height >= 24, `${label}: redis key row must be ≥24px tall: ${JSON.stringify(redis.row)}`);
  await page.evaluate(`document.querySelector('button[aria-label="端口转发"]').click()`);
  await page.waitFor(
    `Boolean([...document.querySelectorAll('.nx-pane')].some((p) => p.getClientRects().length > 0 && p.querySelector('table tbody tr td:last-child button')))`,
  );
  const forward = await page.evaluate(`(() => {
    ${RECT_HELPER}
    const pane = __nxActivePane();
    const btn = pane.querySelector("table tbody tr td:last-child button");
    return __nxRect(btn);
  })()`);
  assert.ok(forward.height >= 24 && forward.width >= 24, `${label}: forward stop button must be ≥24px: ${JSON.stringify(forward)}`);
  await screenshot(page, `touch-${label}.png`);
  return { evidence: { docker, redis, forward } };
}

const VIEWPORTS = [
  { label: "320x568", width: 320, height: 568 },
  { label: "360x640", width: 360, height: 640 },
  { label: "390x844", width: 390, height: 844 },
  { label: "568x320", width: 568, height: 320 },
  { label: "768x1024", width: 768, height: 1024 },
  { label: "640x480", width: 640, height: 480 },
];

async function operationsResponsiveAcceptance(page) {
  for (const vp of VIEWPORTS) {
    await setViewport(page, vp);
    await boot(page, { theme: "dark" });
    await pass(`redis@${vp.label}`, () => checkRedis(page, vp, vp.label));
    await pass(`mysql@${vp.label}`, () => checkMysql(page, vp, vp.label));
    await pass(`docker@${vp.label}`, () => checkDocker(page, vp, vp.label));
    await pass(`forward@${vp.label}`, () => checkForward(page, vp, vp.label));
  }

  for (const vp of [VIEWPORTS[0], VIEWPORTS[2]]) {
    await setViewport(page, vp);
    await boot(page, { theme: "light" });
    await pass(`redis@${vp.label}-light`, () => checkRedis(page, vp, `${vp.label}-light`));
    await pass(`mysql@${vp.label}-light`, () => checkMysql(page, vp, `${vp.label}-light`));
    await pass(`docker@${vp.label}-light`, () => checkDocker(page, vp, `${vp.label}-light`));
  }

  await setViewport(page, { ...VIEWPORTS[2], touch: true });
  await boot(page, { theme: "dark" });
  await pass("touch-targets@390x844", () => checkTouchTargets(page, "390x844-touch"));

  await setViewport(page, VIEWPORTS[3]);
  await boot(page, { theme: "dark" });
  await pass("redis-console@568x320", () => checkRedisConsole(page, "568x320"));
}

let vite;
let chrome;
let page;
try {
  [vite, chrome] = await Promise.all([startVite({ root: ROOT, port: VITE_PORT }), startChrome()]);
  page = await newPage(chrome);
  await operationsResponsiveAcceptance(page);
} catch (error) {
  harnessErrors.push(String(error?.stack || error));
} finally {
  if (page) page.close();
  stop(chrome?.process);
  await vite?.stop();
}

const checks = [...results.values()];
const failed = checks.filter((check) => check.status !== "passed");
const report = {
  schema_version: 1,
  status: failed.length || harnessErrors.length ? "failed" : "passed",
  browser: chrome?.version || { status: "unavailable" },
  execution: {
    real_browser: true,
    headless: true,
    jsdom: false,
    viewports: VIEWPORTS.map((vp) => vp.label).concat(["390x844-touch", "320x568-light", "390x844-light", "568x320-console"]),
  },
  checks,
  harness_errors: harnessErrors,
};
fs.writeFileSync(path.join(OUT, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
console.warn(`operations responsive acceptance: ${checks.filter((check) => check.status === "passed").length}/${checks.length} checks passed; report=${path.join(OUT, "report.json")}`);
if (failed.length || harnessErrors.length) process.exit(1);
