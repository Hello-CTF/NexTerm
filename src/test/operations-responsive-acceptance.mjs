#!/usr/bin/env node
// M118 运维面板小屏响应式真实浏览器验收：Redis 详情非零、MySQL 结果区非零、
// 运行/容器/转发操作可达、ContainerInsight 页签与统计表、触控目标 ≥24px、明暗双主题。
// 视口：320×568 / 360×640 / 390×844 / 568×320 矮横屏 / 768×1024 / 640×480（≈200% 缩放）。
// 演示数据经 vite dev 的 demo 模式提供，不改动任何产品代码。报告与截图写入
// target/acceptance-operations-responsive/。
//
// 运行：node src/test/operations-responsive-acceptance.mjs
// 需要本机 Chrome/Chromium（CHROME_PATH 可覆盖）与 pnpm（启动 vite dev server）。
import { spawn, spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

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

function startVite(port) {
  const command = globalThis.process.platform === "win32" ? "pnpm.cmd" : "pnpm";
  const process = spawn(command, ["exec", "vite", "--host", "127.0.0.1", "--port", String(port), "--strictPort"], {
    cwd: ROOT,
    env: { ...globalThis.process.env, NODE_OPTIONS: "" },
    stdio: ["ignore", "pipe", "pipe"],
    detached: true,
  });
  return waitHttp(`http://127.0.0.1:${port}`, process).then(() => process);
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
  if (!el) return false;
  const r = el.getBoundingClientRect();
  if (r.width <= 0 || r.height <= 0) return false;
  const cx = Math.min(Math.max(r.left + r.width / 2, 1), window.innerWidth - 1);
  const cy = Math.min(Math.max(r.top + r.height / 2, 1), window.innerHeight - 1);
  const hit = document.elementFromPoint(cx, cy);
  return hit === el || el.contains(hit) || Boolean(hit && hit.contains(el));
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
  await page.waitFor(`Boolean(document.querySelector('button[aria-label="连接 ${name}"]'))`);
  await page.evaluate(`document.querySelector('button[aria-label="连接 ${name}"]').click()`);
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
  await page.waitFor(`document.body.textContent.includes("TTL")`);
  const m = await page.evaluate(`(() => {
    ${RECT_HELPER}
    const pane = [...document.querySelectorAll(".nx-pane")].find((p) => p.textContent.includes("命令台"));
    const pre = pane ? pane.querySelector("pre.nx-pre") : null;
    const cmdInput = pane ? [...pane.querySelectorAll("input")].find((el) => !el.getAttribute("aria-label")) : null;
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
  assert.ok(m.cmdHit, `${label}: redis command input not hittable: ${JSON.stringify(m)}`);
  await screenshot(page, `redis-${label}.png`);
  return { evidence: m };
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
  assert.equal(runVisible.hit, true, `${label}: run button not hittable: ${JSON.stringify(runVisible)}`);
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
      buttonCount: buttons.length,
      allButtonsHit: buttons.every((b) => __nxHit(b)),
      sticky: getComputedStyle(td).position,
    };
  })()`);
  assert.equal(m.sticky, "sticky", `${label}: container action cell must be sticky: ${JSON.stringify(m)}`);
  assert.ok(m.td.width > 0 && m.td.right <= m.wrapper.right + 1, `${label}: action cell not pinned into view: ${JSON.stringify(m)}`);
  assert.ok(m.td.right <= m.innerWidth + 1, `${label}: action cell offscreen: ${JSON.stringify(m)}`);
  assert.ok(m.buttonCount >= 6, `${label}: all six row actions must render: ${JSON.stringify(m)}`);
  assert.equal(m.allButtonsHit, true, `${label}: every row action must be hittable without scrolling: ${JSON.stringify(m)}`);
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
      labels: items.map((b) => b.textContent?.trim()),
      allHit: items.every((b) => __nxHit(b)),
    };
  })()`);
  assert.ok(tabs.seg.right <= tabs.innerWidth + 1, `${label}: insight tabs offscreen: ${JSON.stringify(tabs)}`);
  assert.equal(tabs.allHit, true, `${label}: insight tabs not hittable: ${JSON.stringify(tabs)}`);

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
    const firstTd = table.querySelector("tbody td:first-child");
    const before = { scrollWidth: wrapper.scrollWidth, clientWidth: wrapper.clientWidth };
    wrapper.scrollLeft = 120;
    const pinned = __nxRect(firstTd);
    const wrapRect = __nxRect(wrapper);
    wrapper.scrollLeft = 0;
    return { before, pinned, wrapRect, nameSticky: getComputedStyle(firstTd).position, cols: table.querySelectorAll("thead th").length };
  })()`);
  assert.equal(stats.cols, 7, `${label}: stats table must keep all 7 diagnostic columns: ${JSON.stringify(stats)}`);
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
  assert.equal(m.hit, true, `${label}: forward stop button not hittable: ${JSON.stringify(m)}`);
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
}

let vite;
let chrome;
let page;
try {
  [vite, chrome] = await Promise.all([startVite(VITE_PORT), startChrome()]);
  page = await newPage(chrome);
  await operationsResponsiveAcceptance(page);
} catch (error) {
  harnessErrors.push(String(error?.stack || error));
} finally {
  if (page) page.close();
  stop(chrome?.process);
  stop(vite);
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
    viewports: VIEWPORTS.map((vp) => vp.label).concat(["390x844-touch", "320x568-light", "390x844-light"]),
  },
  checks,
  harness_errors: harnessErrors,
};
fs.writeFileSync(path.join(OUT, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
console.warn(`operations responsive acceptance: ${checks.filter((check) => check.status === "passed").length}/${checks.length} checks passed; report=${path.join(OUT, "report.json")}`);
if (failed.length || harnessErrors.length) process.exit(1);
