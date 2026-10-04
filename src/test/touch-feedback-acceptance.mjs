#!/usr/bin/env node
// 运行：node src/test/touch-feedback-acceptance.mjs
import { spawn, spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const OUT = path.join(ROOT, "target/acceptance-touch-feedback");
const VITE_PORT = Number(process.env.NEXTERM_VITE_PORT || 1431);
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
    socket.addEventListener("message", (event) => {
      const message = JSON.parse(String(event.data));
      if (!message.id) return;
      const pending = this.pending.get(message.id);
      if (!pending) return;
      this.pending.delete(message.id);
      if (message.error) pending.reject(new Error(`${pending.method}: ${message.error.message}`));
      else pending.resolve(message.result || {});
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
  fs.writeFileSync(path.join(OUT, name), Buffer.from(shot.data, "base64"));
  return name;
}

async function setViewport(page, { width, height, coarse }) {
  await page.send("Emulation.setDeviceMetricsOverride", {
    width,
    height,
    deviceScaleFactor: 1,
    mobile: coarse,
    screenWidth: width,
    screenHeight: height,
  });
  if (coarse) {
    await page.send("Emulation.setTouchEmulationEnabled", { enabled: true, maxTouchPoints: 5 });
    await page.send("Emulation.setEmulatedMedia", { features: [{ name: "pointer", value: "coarse" }, { name: "any-pointer", value: "coarse" }] });
  } else {
    await page.send("Emulation.setTouchEmulationEnabled", { enabled: false, maxTouchPoints: 1 });
    await page.send("Emulation.setEmulatedMedia", { features: [{ name: "pointer", value: "fine" }, { name: "any-pointer", value: "fine" }] });
  }
  await sleep(350);
}

async function boot(page, { theme, width, height, coarse }) {
  const { identifier } = await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `try { localStorage.setItem("nexterm.theme.v1", ${JSON.stringify(theme)}); } catch {}`,
  });
  try {
    await setViewport(page, { width, height, coarse });
    await page.navigate(`${VITE}/?demo=1`);
    await page.waitFor(
      "Boolean(document.querySelector('[role=\"tablist\"][aria-label=\"工作区\"] [role=\"tab\"]') && document.querySelector('.xterm'))",
    );
    const themed = await page.evaluate(`document.documentElement.dataset.nxTheme ?? null`);
    assert.equal(themed, theme, `theme not applied: ${themed}`);
  } finally {
    await page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier });
  }
}

const HIT_TOLERANCE = 0.5;
const MIN_HIT = 44 - HIT_TOLERANCE;

const measureRects = `((selector) => [...document.querySelectorAll(selector)].map((el) => {
  const r = el.getBoundingClientRect();
  return { w: r.width, h: r.height, left: r.left, top: r.top, right: r.right, bottom: r.bottom };
}))`;

const textareaVisible = `(() => {
  const input = document.querySelector('.nx-right-dock textarea');
  if (!input) return false;
  const r = input.getBoundingClientRect();
  return r.width > 0 && r.height > 0;
})()`;

async function ensureAiDock(page) {
  const open = await page.evaluate(textareaVisible);
  if (!open) {
    await page.evaluate(`document.querySelector('.nx-rail-btn[aria-label="AI 助手"]').click()`);
  }
  await page.waitFor(textareaVisible);
}

async function touchAcceptance(page) {
  for (const theme of ["dark", "light"]) {
    await pass(`hit-rail-coarse-${theme}`, async () => {
      await boot(page, { theme, width: 390, height: 844, coarse: true });
      const evidence = await page.evaluate(`(() => {
        const rects = ${measureRects}('.nx-rail-btn');
        return { coarse: matchMedia('(pointer: coarse)').matches, count: rects.length, minW: Math.min(...rects.map((r) => r.w)), minH: Math.min(...rects.map((r) => r.h)) };
      })()`);
      assert.equal(evidence.coarse, true);
      assert.ok(evidence.count >= 10, `rail buttons missing: ${JSON.stringify(evidence)}`);
      assert.ok(evidence.minW >= MIN_HIT && evidence.minH >= MIN_HIT, `rail hit area below 44px: ${JSON.stringify(evidence)}`);
      await screenshot(page, `rail-coarse-${theme}.png`);
      return { evidence };
    });

    await pass(`hit-keys-coarse-${theme}`, async () => {
      const evidence = await page.evaluate(`(() => {
        const bar = document.querySelector('.nx-terminal-keys');
        if (!bar) return { bar: false };
        const rects = ${measureRects}('.nx-terminal-key');
        const br = bar.getBoundingClientRect();
        return { bar: true, visible: getComputedStyle(bar).display !== 'none', count: rects.length, minW: Math.min(...rects.map((r) => r.w)), minH: Math.min(...rects.map((r) => r.h)), barHeight: br.height };
      })()`);
      assert.equal(evidence.bar, true, "terminal keys bar missing");
      assert.equal(evidence.visible, true, "terminal keys bar hidden on coarse pointer");
      assert.ok(evidence.count >= 4, `too few keys: ${JSON.stringify(evidence)}`);
      assert.ok(evidence.minW >= MIN_HIT && evidence.minH >= MIN_HIT, `key hit area below 44px: ${JSON.stringify(evidence)}`);
      return { evidence };
    });

    await pass(`hit-row-actions-coarse-${theme}`, async () => {
      await page.evaluate(`document.querySelector('.nx-rail-btn[aria-label="资产"]').click()`);
      await page.waitFor("Boolean(document.querySelector('.nx-row .nx-row-actions .nx-icon-btn-sm'))");
      const evidence = await page.evaluate(`(() => {
        const rects = ${measureRects}('.nx-row-actions .nx-icon-btn-sm');
        const container = document.querySelector('.nx-row .nx-row-actions');
        return { count: rects.length, minW: Math.min(...rects.map((r) => r.w)), minH: Math.min(...rects.map((r) => r.h)), display: getComputedStyle(container).display };
      })()`);
      assert.ok(evidence.count >= 2, `row actions missing: ${JSON.stringify(evidence)}`);
      assert.equal(evidence.display, "flex", "row actions must stay visible on coarse pointers");
      assert.ok(evidence.minW >= MIN_HIT && evidence.minH >= MIN_HIT, `row action hit area below 44px: ${JSON.stringify(evidence)}`);
      await screenshot(page, `row-actions-coarse-${theme}.png`);
      return { evidence };
    });

    await pass(`toast-avoids-keys-and-statusbar-${theme}`, async () => {
      await page.evaluate(`document.querySelector('.nx-tab-new').click()`);
      await page.waitFor("Boolean(document.querySelector('.nx-toasts button'))");
      const evidence = await page.evaluate(`(() => {
        const t = document.querySelector('.nx-toasts').getBoundingClientRect();
        const keys = document.querySelector('.nx-terminal-keys').getBoundingClientRect();
        const status = document.querySelector('.nx-statusbar').getBoundingClientRect();
        const hit = (a, b) => !(a.right <= b.left + 0.5 || b.right <= a.left + 0.5 || a.bottom <= b.top + 0.5 || b.bottom <= a.top + 0.5);
        return { toastBottom: t.bottom, keysTop: keys.top, statusTop: status.top, overlapKeys: hit(t, keys), overlapStatus: hit(t, status) };
      })()`);
      assert.equal(evidence.overlapKeys, false, `toast overlaps mobile key bar: ${JSON.stringify(evidence)}`);
      assert.equal(evidence.overlapStatus, false, `toast overlaps status bar: ${JSON.stringify(evidence)}`);
      return { evidence };
    });

    await pass(`toast-avoids-ai-input-narrow-${theme}`, async () => {
      await page.evaluate(`document.querySelector('.nx-tab-new').click()`);
      await page.waitFor("Boolean(document.querySelector('.nx-toasts button'))");
      await ensureAiDock(page);
      const evidence = await page.evaluate(`(() => {
        const t = document.querySelector('.nx-toasts').getBoundingClientRect();
        const input = document.querySelector('.nx-right-dock textarea').getBoundingClientRect();
        const header = document.querySelector('header').getBoundingClientRect();
        const dockOpen = !document.querySelector('.nx-right-dock').classList.contains('is-hidden');
        const hit = (a, b) => !(a.right <= b.left + 0.5 || b.right <= a.left + 0.5 || a.bottom <= b.top + 0.5 || b.bottom <= a.top + 0.5);
        return { overlapInput: hit(t, input), toastTop: t.top, toastLeft: t.left, toastRight: t.right, headerBottom: header.bottom, dockOpen, innerWidth };
      })()`);
      assert.equal(evidence.dockOpen, true, `AI dock not open: ${JSON.stringify(evidence)}`);
      assert.equal(evidence.overlapInput, false, `toast overlaps AI input on narrow screen: ${JSON.stringify(evidence)}`);
      assert.ok(evidence.toastTop >= evidence.headerBottom - 0.5, `toast not below header: ${JSON.stringify(evidence)}`);
      assert.ok(evidence.toastLeft >= 0 && evidence.toastRight <= evidence.innerWidth + 0.5, `toast off-screen: ${JSON.stringify(evidence)}`);
      await screenshot(page, `toast-ai-narrow-${theme}.png`);
      return { evidence };
    });

    await pass(`status-icon-${theme}`, async () => {
      await page.waitFor("Boolean(document.querySelector('.nx-ws-status[role=\"img\"]'))");
      const evidence = await page.evaluate(`(() => {
        const el = document.querySelector('.nx-ws-status[role="img"]');
        return { label: el.getAttribute('aria-label'), hasSvg: Boolean(el.querySelector('svg')), cls: el.getAttribute('class') };
      })()`);
      assert.equal(evidence.hasSvg, true, "workspace status must render an icon");
      assert.equal(evidence.label, "已连接", `unexpected status label: ${JSON.stringify(evidence)}`);
      assert.match(evidence.cls, /is-ok/, `status tone class missing: ${JSON.stringify(evidence)}`);
      return { evidence };
    });

    await pass(`hit-rail-fine-compact-${theme}`, async () => {
      await boot(page, { theme, width: 1280, height: 800, coarse: false });
      const evidence = await page.evaluate(`(() => {
        const rects = ${measureRects}('.nx-rail-btn');
        const bar = document.querySelector('.nx-terminal-keys');
        const actions = document.querySelector('.nx-row .nx-row-actions');
        return {
          coarse: matchMedia('(pointer: coarse)').matches,
          maxW: Math.max(...rects.map((r) => r.w)),
          maxH: Math.max(...rects.map((r) => r.h)),
          keysDisplay: bar ? getComputedStyle(bar).display : "none",
          rowActionsDisplay: actions ? getComputedStyle(actions).display : "none",
        };
      })()`);
      assert.equal(evidence.coarse, false);
      assert.ok(evidence.maxW <= 36 && evidence.maxH <= 36, `desktop rail no longer compact: ${JSON.stringify(evidence)}`);
      assert.equal(evidence.keysDisplay, "none", "keys bar must stay hidden on fine pointers");
      assert.equal(evidence.rowActionsDisplay, "none", "row actions must stay hover-only on fine pointers");
      return { evidence };
    });

    await pass(`toast-avoids-ai-input-wide-${theme}`, async () => {
      await page.evaluate(`document.querySelector('.nx-tab-new').click()`);
      await page.waitFor("Boolean(document.querySelector('.nx-toasts button'))");
      await ensureAiDock(page);
      const evidence = await page.evaluate(`(() => {
        const t = document.querySelector('.nx-toasts').getBoundingClientRect();
        const dock = document.querySelector('.nx-right-dock').getBoundingClientRect();
        const input = document.querySelector('.nx-right-dock textarea').getBoundingClientRect();
        const dockOpen = !document.querySelector('.nx-right-dock').classList.contains('is-hidden');
        const hit = (a, b) => !(a.right <= b.left + 0.5 || b.right <= a.left + 0.5 || a.bottom <= b.top + 0.5 || b.bottom <= a.top + 0.5);
        return { toastRight: t.right, dockLeft: dock.left, overlapInput: hit(t, input), dockOpen };
      })()`);
      assert.equal(evidence.dockOpen, true, `AI dock not open: ${JSON.stringify(evidence)}`);
      assert.ok(evidence.toastRight <= evidence.dockLeft + 0.5, `toast not left of docked AI sidebar: ${JSON.stringify(evidence)}`);
      assert.equal(evidence.overlapInput, false, `toast overlaps AI input on wide screen: ${JSON.stringify(evidence)}`);
      await screenshot(page, `toast-ai-wide-${theme}.png`);
      return { evidence };
    });

    await pass(`keyboard-focus-order-${theme}`, async () => {
      await page.evaluate(`document.querySelector('.nx-rail-btn').focus()`);
      await page.send("Input.dispatchKeyEvent", { type: "rawKeyDown", key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 });
      await page.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 });
      await sleep(120);
      const evidence = await page.evaluate(`(() => {
        const buttons = [...document.querySelectorAll('.nx-rail-btn')];
        return { index: buttons.indexOf(document.activeElement), label: document.activeElement?.getAttribute('aria-label') ?? null };
      })()`);
      assert.equal(evidence.index, 1, `Tab did not move focus to the next rail button: ${JSON.stringify(evidence)}`);
      return { evidence };
    });
  }
}

async function main() {
  const vite = await startVite();
  const chrome = await startChrome();
  const page = await newPage(chrome);
  try {
    await touchAcceptance(page);
  } catch (error) {
    harnessErrors.push(String(error?.stack || error));
  } finally {
    page.close();
    stop(chrome.process);
    stop(vite);
    await new Promise((resolve) => {
      if (chrome.process.exitCode !== null) return resolve();
      chrome.process.once("exit", resolve);
      setTimeout(resolve, 3000);
    });
    fs.rmSync(chrome.profile, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 });
  }

  const summary = {
    generatedAt: new Date().toISOString(),
    chrome: chrome.version["User-Agent"] ?? null,
    results: Object.fromEntries(results),
    harnessErrors,
  };
  fs.writeFileSync(path.join(OUT, "report.json"), JSON.stringify(summary, null, 2));
  const failed = [...results.values()].filter((r) => r.status !== "passed");
  console.warn(`\n${results.size - failed.length}/${results.size} passed; report at ${path.join(OUT, "report.json")}`);
  if (failed.length > 0 || harnessErrors.length > 0) {
    for (const f of failed) console.warn(`FAILED ${f.id}: ${f.error?.split("\n")[0]}`);
    for (const e of harnessErrors) console.warn(`HARNESS ${e.split("\n")[0]}`);
    process.exit(1);
  }
}

await main();
