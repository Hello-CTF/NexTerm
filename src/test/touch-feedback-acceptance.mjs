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

async function screenshot(page, name) {
  const shot = await page.send("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(path.join(OUT, name), Buffer.from(shot.data, "base64"));
  return name;
}

async function tapAt(page, x, y) {
  await page.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x, y, radiusX: 4, radiusY: 4, force: 1 }] });
  await sleep(40);
  await page.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
  await sleep(150);
}

async function clickAt(page, x, y) {
  await page.send("Input.dispatchMouseEvent", { type: "mousePressed", x, y, button: "left", clickCount: 1 });
  await sleep(40);
  await page.send("Input.dispatchMouseEvent", { type: "mouseReleased", x, y, button: "left", clickCount: 1 });
  await sleep(150);
}

async function floodErrorToasts(page) {
  await page.evaluate(`(async () => {
    const { useUi } = await import('/src/app/store.ts');
    for (let i = 1; i <= 16; i += 1) {
      useUi.getState().pushToast('error', '错误 ' + i + '：' + '磁盘写入失败，请检查连接与权限后重试。'.repeat(8));
    }
  })()`);
  await page.waitFor("document.querySelectorAll('.nx-toasts button').length >= 16");
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

async function boot(page, { theme, width, height, coarse, layout = null }) {
  const { identifier } = await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source:
      `try { localStorage.setItem("nexterm.theme.v1", ${JSON.stringify(theme)}); ` +
      (layout
        ? `localStorage.setItem("nexterm.layout.v1", ${JSON.stringify(JSON.stringify(layout))}); `
        : `localStorage.removeItem("nexterm.layout.v1"); `) +
      `} catch {}`,
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
      await page.waitFor("Boolean(document.querySelector('.nx-row .nx-row-more'))");
      const evidence = await page.evaluate(`(() => {
        const rects = ${measureRects}('.nx-row .nx-row-more');
        const container = document.querySelector('.nx-row .nx-row-more');
        return { count: rects.length, minW: Math.min(...rects.map((r) => r.w)), minH: Math.min(...rects.map((r) => r.h)), display: getComputedStyle(container).display };
      })()`);
      assert.ok(evidence.count >= 2, `row overflow triggers missing: ${JSON.stringify(evidence)}`);
      assert.notEqual(evidence.display, "none", "row overflow trigger must stay visible on coarse pointers");
      assert.ok(evidence.minW >= MIN_HIT && evidence.minH >= MIN_HIT, `row overflow trigger hit area below 44px: ${JSON.stringify(evidence)}`);
      await page.evaluate(`[...document.querySelectorAll('.nx-row')].find((r) => r.querySelector('.nx-row-more') && r.hasAttribute('draggable') && !r.textContent.includes('本机'))?.querySelector('.nx-row-more').click()`);
      await page.waitFor("Boolean(document.querySelector('.nx-menu'))");
      const menuText = await page.evaluate(`document.querySelector('.nx-menu').textContent`);
      for (const label of ["连接", "编辑", "删除", "克隆"]) {
        assert.ok(menuText.includes(label), `coarse row menu missing ${label}: ${menuText}`);
      }
      await page.evaluate(`window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))`);
      await page.waitFor("!document.querySelector('.nx-menu')");
      await screenshot(page, `row-actions-coarse-${theme}.png`);
      return { evidence };
    });

    await pass(`hit-row-actions-visible-bounds-${theme}`, async () => {
      const evidence = await page.evaluate(`(() => {
        const tree = document.querySelector('.nx-left-dock [role="tree"]');
        const tr = tree.getBoundingClientRect();
        const buttons = [...tree.querySelectorAll('.nx-row-more')];
        const bad = buttons.filter((b) => {
          const r = b.getBoundingClientRect();
          return r.width < 43.5 || r.height < 43.5 || r.left < tr.left - 0.5 || r.right > tr.right + 0.5;
        }).map((b) => b.getAttribute('aria-label'));
        return {
          total: buttons.length,
          bad,
          treeScrollWidth: tree.scrollWidth,
          treeClientWidth: tree.clientWidth,
        };
      })()`);
      assert.ok(evidence.total >= 2, `expected row overflow triggers: ${JSON.stringify(evidence)}`);
      assert.deepEqual(evidence.bad, [], `row overflow triggers clipped or undersized inside the dock: ${JSON.stringify(evidence)}`);
      assert.ok(evidence.treeScrollWidth <= evidence.treeClientWidth + 1, `tree horizontally overflows: ${JSON.stringify(evidence)}`);
      return { evidence };
    });

    await pass(`hit-attribution-elementfrompoint-${theme}`, async () => {
      const evidence = await page.evaluate(`(() => {
        const hasTrigger = (r) => Boolean(r.querySelector('.nx-row-more'));
        const assetRows = [...document.querySelectorAll('.nx-row')].filter(hasTrigger);
        let rowA = null;
        let rowB = null;
        for (const r of assetRows) {
          const next = r.nextElementSibling;
          if (next && next.classList.contains('nx-row') && hasTrigger(next)) {
            rowA = r;
            rowB = next;
            break;
          }
        }
        if (!rowA || !rowB) return { rows: assetRows.length, adjacent: false };
        const owner = (x, y) => {
          const el = document.elementFromPoint(x, y);
          const btn = el ? el.closest('button') : null;
          return btn ? btn.getAttribute('aria-label') : null;
        };
        const btnA = rowA.querySelector('.nx-row-more');
        const btnB = rowB.querySelector('.nx-row-more');
        const r0 = btnA.getBoundingClientRect();
        const r2 = btnB.getBoundingClientRect();
        return {
          rows: assetRows.length,
          adjacent: true,
          l0: btnA.getAttribute('aria-label'),
          l2: btnB.getAttribute('aria-label'),
          own0: owner(r0.left + r0.width / 2, r0.top + r0.height / 2),
          own2: owner(r2.left + r2.width / 2, r2.top + r2.height / 2),
          edgeRightOut: owner(r0.right + 1, r0.top + r0.height / 2),
          edgeLeftIn: owner(r0.left + 1, r0.top + r0.height / 2),
          edgeRightIn: owner(r0.right - 1, r0.top + r0.height / 2),
          rowGapOwner: owner(r0.left + r0.width / 2, (r0.bottom + r2.top) / 2),
          bottomIn: owner(r0.left + r0.width / 2, r0.bottom - 1),
          topIn: owner(r2.left + r2.width / 2, r2.top + 1),
        };
      })()`);
      assert.ok(evidence.rows >= 2 && evidence.adjacent, `need two adjacent asset rows: ${JSON.stringify(evidence)}`);
      assert.equal(evidence.own0, evidence.l0, `trigger center owned by ${evidence.own0}`);
      assert.equal(evidence.own2, evidence.l2, `trigger center owned by ${evidence.own2}`);
      assert.equal(evidence.edgeRightOut, null, `1px outside the trigger owned by ${evidence.edgeRightOut}`);
      assert.equal(evidence.edgeLeftIn, evidence.l0);
      assert.equal(evidence.edgeRightIn, evidence.l0);
      assert.equal(evidence.rowGapOwner, null, `gap between rows owned by ${evidence.rowGapOwner}`);
      assert.equal(evidence.bottomIn, evidence.l0);
      assert.equal(evidence.topIn, evidence.l2);
      return { evidence };
    });

    await pass(`hit-attribution-touch-${theme}`, async () => {
      const before = await page.evaluate(`(() => {
        const trigger = (r) => r.querySelector('.nx-row-more');
        const assetRows = [...document.querySelectorAll('.nx-row')].filter((r) => trigger(r) && r.hasAttribute('draggable') && !r.textContent.includes('本机'));
        let rowA = null;
        let rowB = null;
        for (const r of assetRows) {
          const next = r.nextElementSibling;
          if (next && next.classList.contains('nx-row') && trigger(next)) {
            rowA = r;
            rowB = next;
            break;
          }
        }
        const center = (el) => {
          const rect = el.getBoundingClientRect();
          return { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 };
        };
        const picks = [rowA, rowB].filter(Boolean).map((r) => ({
          name: r.querySelector('.nx-row-name')?.textContent ?? '',
          more: center(trigger(r)),
        }));
        return {
          count: picks.length,
          picks,
          tabs: document.querySelectorAll('[role="tablist"][aria-label="工作区"] [role="tab"]').length,
        };
      })()`);
      assert.equal(before.count, 2, `need two asset rows: ${JSON.stringify(before)}`);

      const modalName = () => page.evaluate(`document.querySelector('.nx-modal input')?.value ?? null`);
      const closeModal = async () => {
        await page.evaluate(`[...document.querySelectorAll('.nx-modal-footer button')].find((b) => b.textContent.includes('取消'))?.click()`);
        await page.waitFor("!document.querySelector('.nx-modal')");
      };
      const openMenu = async (pick) => {
        await tapAt(page, pick.more.x, pick.more.y);
        await page.waitFor("Boolean(document.querySelector('.nx-menu'))");
      };
      const tapMenuItem = async (label) => {
        await page.evaluate(`[...document.querySelectorAll('.nx-menu-item')].find((b) => b.textContent.includes(${JSON.stringify(label)}))?.click()`);
      };
      const closeMenu = async () => {
        await page.evaluate(`window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))`);
        await page.waitFor("!document.querySelector('.nx-menu')");
      };

      await openMenu(before.picks[0]);
      let menuText = await page.evaluate(`document.querySelector('.nx-menu').textContent`);
      assert.ok(menuText.includes("克隆"), `row menu must contain clone: ${menuText}`);
      await tapMenuItem("编辑");
      await page.waitFor("Boolean(document.querySelector('.nx-modal'))");
      assert.equal(await modalName(), before.picks[0].name, "menu edit must open row A's editor");
      let cross = await page.evaluate(`({ menu: Boolean(document.querySelector('.nx-menu')), tabs: document.querySelectorAll('[role="tablist"][aria-label="工作区"] [role="tab"]').length })`);
      assert.equal(cross.menu, false, "edit selection must close the row menu");
      assert.equal(cross.tabs, before.tabs, "edit selection must not connect");
      await closeModal();

      await openMenu(before.picks[1]);
      await tapMenuItem("编辑");
      await page.waitFor("Boolean(document.querySelector('.nx-modal'))");
      assert.equal(await modalName(), before.picks[1].name, "menu edit must open row B's editor");
      await closeModal();

      await openMenu(before.picks[0]);
      await tapMenuItem("删除");
      await page.waitFor("Boolean(document.querySelector('.nx-modal'))");
      const confirmText = await page.evaluate(`document.querySelector('.nx-modal').textContent`);
      assert.ok(confirmText.includes(`删除资产「${before.picks[0].name}」`), `delete selection must confirm the tapped asset: ${confirmText}`);
      cross = await page.evaluate(`({ menu: Boolean(document.querySelector('.nx-menu')), tabs: document.querySelectorAll('[role="tablist"][aria-label="工作区"] [role="tab"]').length })`);
      assert.equal(cross.menu, false, "delete selection must close the row menu");
      await closeModal();

      await openMenu(before.picks[1]);
      menuText = await page.evaluate(`document.querySelector('.nx-menu').textContent`);
      const menuModal = await page.evaluate(`Boolean(document.querySelector('.nx-modal'))`);
      assert.ok(menuText.includes("克隆"), `more tap must open the row menu: ${menuText}`);
      assert.equal(menuModal, false, "more tap must not open a dialog");
      await closeMenu();

      await openMenu(before.picks[0]);
      await tapMenuItem("连接");
      await page.waitFor(`document.querySelectorAll('[role="tablist"][aria-label="工作区"] [role="tab"]').length > ${before.tabs}`);
      cross = await page.evaluate(`({ modal: Boolean(document.querySelector('.nx-modal')), menu: Boolean(document.querySelector('.nx-menu')) })`);
      assert.equal(cross.modal, false, "connect selection must not open the editor");
      assert.equal(cross.menu, false, "connect selection must close the row menu");
      return { evidence: { taps: 5, tabsBefore: before.tabs } };
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

    await pass(`toast-flood-${theme}`, async () => {
      await floodErrorToasts(page);
      const evidence = await page.evaluate(`(() => {
        const container = document.querySelector('.nx-toasts');
        const cr = container.getBoundingClientRect();
        const toasts = [...container.querySelectorAll('button')];
        const newest = toasts[toasts.length - 1];
        const nr = newest.getBoundingClientRect();
        const oldest = toasts[0].getBoundingClientRect();
        const cx = nr.left + nr.width / 2;
        const cy = nr.top + nr.height / 2;
        const input = document.querySelector('.nx-right-dock textarea');
        const ir = input.getBoundingClientRect();
        const ix = ir.left + ir.width / 2;
        const iy = ir.top + ir.height / 2;
        const inputOwner = document.elementFromPoint(ix, iy);
        return {
          count: toasts.length,
          containerHeight: cr.height,
          maxHeight: parseFloat(getComputedStyle(container).maxHeight),
          newestText: newest.textContent.slice(0, 10),
          newestTop: nr.top,
          newestBottom: nr.bottom,
          newestCenter: { x: cx, y: cy },
          newestHittable: newest.contains(document.elementFromPoint(cx, cy)),
          oldestClipped: oldest.top < cr.top - 0.5,
          containerBottom: cr.bottom,
          inputTop: ir.top,
          inputCenter: { x: ix, y: iy },
          inputOwner: inputOwner ? (inputOwner === input || input.contains(inputOwner)) : false,
          innerHeight,
        };
      })()`);
      assert.ok(evidence.count >= 16, `expected 16 toasts: ${JSON.stringify(evidence)}`);
      assert.ok(evidence.containerHeight <= evidence.maxHeight + 0.5, `toast container exceeds max-height: ${JSON.stringify(evidence)}`);
      assert.match(evidence.newestText, /错误 16/, `newest toast is not the last pushed: ${JSON.stringify(evidence)}`);
      assert.ok(evidence.newestTop >= 0 && evidence.newestBottom <= evidence.innerHeight, `newest toast off-screen: ${JSON.stringify(evidence)}`);
      assert.equal(evidence.newestHittable, true, `newest toast not hittable at its center: ${JSON.stringify(evidence)}`);
      assert.equal(evidence.oldestClipped, true, `oldest toast should be clipped to keep the newest: ${JSON.stringify(evidence)}`);
      assert.ok(evidence.containerBottom <= evidence.inputTop - 8, `toast stack must stay above the AI input: ${JSON.stringify(evidence)}`);
      assert.equal(evidence.inputOwner, true, `AI input covered at its center: ${JSON.stringify(evidence)}`);
      await tapAt(page, evidence.newestCenter.x, evidence.newestCenter.y);
      await page.waitFor("![...document.querySelectorAll('.nx-toasts button')].some((b) => b.textContent.includes('错误 16'))");
      const remaining = await page.evaluate(`document.querySelectorAll('.nx-toasts button').length`);
      assert.equal(remaining, evidence.count - 1, "tapping the newest toast must dismiss exactly it");
      await tapAt(page, evidence.inputCenter.x, evidence.inputCenter.y);
      const focused = await page.evaluate(`document.activeElement === document.querySelector('.nx-right-dock textarea')`);
      assert.equal(focused, true, "tap must move focus into the AI input");
      return { evidence: { ...evidence, remaining } };
    });

    await pass(`hit-row-actions-min-dock-${theme}`, async () => {
      await boot(page, { theme, width: 390, height: 844, coarse: true, layout: { leftWidth: 180, rightWidth: 352 } });
      await page.evaluate(`document.querySelector('.nx-rail-btn[aria-label="资产"]').click()`);
      await page.waitFor("Boolean(document.querySelector('.nx-row .nx-row-more'))");
      const bounds = await page.evaluate(`(() => {
        const tree = document.querySelector('.nx-left-dock [role="tree"]');
        const tr = tree.getBoundingClientRect();
        const buttons = [...tree.querySelectorAll('.nx-row-more')];
        const bad = buttons.filter((b) => {
          const r = b.getBoundingClientRect();
          return r.width < 43.5 || r.height < 43.5 || r.left < tr.left - 0.5 || r.right > tr.right + 0.5;
        }).map((b) => b.getAttribute('aria-label'));
        return { total: buttons.length, bad, treeScrollWidth: tree.scrollWidth, treeClientWidth: tree.clientWidth };
      })()`);
      assert.ok(bounds.total >= 2, `expected row overflow triggers: ${JSON.stringify(bounds)}`);
      assert.deepEqual(bounds.bad, [], `min-width dock clips row overflow triggers: ${JSON.stringify(bounds)}`);
      assert.ok(bounds.treeScrollWidth <= bounds.treeClientWidth + 1, `tree horizontally overflows at LEFT_MIN: ${JSON.stringify(bounds)}`);

      const picks = await page.evaluate(`(() => {
        const row = [...document.querySelectorAll('.nx-row')].find((r) => r.querySelector('.nx-row-more') && r.hasAttribute('draggable') && !r.textContent.includes('本机'));
        if (!row) return null;
        const owner = (x, y) => {
          const el = document.elementFromPoint(x, y);
          const b = el ? el.closest('button') : null;
          return b ? b.getAttribute('aria-label') : null;
        };
        const trigger = row.querySelector('.nx-row-more');
        const r = trigger.getBoundingClientRect();
        const center = { x: r.left + r.width / 2, y: r.top + r.height / 2 };
        return {
          name: row.querySelector('.nx-row-name')?.textContent ?? '',
          label: trigger.getAttribute('aria-label'),
          centerOwner: owner(center.x, center.y),
          more: center,
          tabs: document.querySelectorAll('[role="tablist"][aria-label="工作区"] [role="tab"]').length,
        };
      })()`);
      assert.ok(picks, `need an asset row at LEFT_MIN: ${JSON.stringify(picks)}`);
      assert.equal(picks.centerOwner, picks.label, `trigger center mis-owned at LEFT_MIN: ${JSON.stringify(picks)}`);

      const modalName = () => page.evaluate(`document.querySelector('.nx-modal input')?.value ?? null`);
      const closeModal = async () => {
        await page.evaluate(`[...document.querySelectorAll('.nx-modal-footer button')].find((b) => b.textContent.includes('取消'))?.click()`);
        await page.waitFor("!document.querySelector('.nx-modal')");
      };
      const dockOpen = () => page.evaluate(`!document.querySelector('.nx-left-dock')?.classList.contains('is-hidden') && Boolean(document.querySelector('.nx-left-dock [role="tree"]'))`);
      const openMenu = async () => {
        await tapAt(page, picks.more.x, picks.more.y);
        await page.waitFor("Boolean(document.querySelector('.nx-menu'))");
      };
      const tapMenuItem = async (label) => {
        await page.evaluate(`[...document.querySelectorAll('.nx-menu-item')].find((b) => b.textContent.includes(${JSON.stringify(label)}))?.click()`);
      };
      const closeMenu = async () => {
        await page.evaluate(`window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))`);
        await page.waitFor("!document.querySelector('.nx-menu')");
      };

      await openMenu();
      await tapMenuItem("编辑");
      await page.waitFor("Boolean(document.querySelector('.nx-modal'))");
      assert.equal(await modalName(), picks.name, "min-dock menu edit must open the tapped asset's editor");
      await closeModal();

      await openMenu();
      await tapMenuItem("删除");
      await page.waitFor("Boolean(document.querySelector('.nx-modal'))");
      const confirmText = await page.evaluate(`document.querySelector('.nx-modal').textContent`);
      assert.ok(confirmText.includes(`删除资产「${picks.name}」`), `min-dock delete selection must confirm the tapped asset: ${confirmText}`);
      await closeModal();

      await openMenu();
      const menuState = await page.evaluate(`({ text: document.querySelector('.nx-menu').textContent, modal: Boolean(document.querySelector('.nx-modal')) })`);
      assert.ok(menuState.text.includes("克隆"), `min-dock more tap must open the row menu: ${menuState.text}`);
      assert.equal(menuState.modal, false, "min-dock more tap must not open a dialog");
      await closeMenu();
      assert.equal(await dockOpen(), true, "min-dock more tap must not collapse the sidebar");

      await openMenu();
      await tapMenuItem("连接");
      await page.waitFor(`document.querySelectorAll('[role="tablist"][aria-label="工作区"] [role="tab"]').length > ${picks.tabs}`);
      const cross = await page.evaluate(`({ modal: Boolean(document.querySelector('.nx-modal')), menu: Boolean(document.querySelector('.nx-menu')) })`);
      assert.equal(cross.modal, false, "min-dock connect selection must not open the editor");
      assert.equal(cross.menu, false, "min-dock connect selection must close the row menu");
      return { evidence: { bounds, name: picks.name } };
    });

    await pass(`hit-filetree-retap-${theme}`, async () => {
      await boot(page, { theme, width: 390, height: 844, coarse: true });
      await page.evaluate(`document.querySelector('.nx-rail-btn[aria-label="文件树"]').click()`);
      await page.waitFor("Boolean(document.querySelector('.nx-left-dock [role=\"tree\"] .nx-row-more'))");
      const triggerCenter = async (name) => page.evaluate(`(() => {
        const row = [...document.querySelectorAll('.nx-left-dock [role="tree"] .nx-row')].find((r) => r.textContent.includes('${name}'));
        const trigger = row?.querySelector('.nx-row-more');
        if (!trigger) return null;
        const r = trigger.getBoundingClientRect();
        if (r.top < 0 || r.bottom > innerHeight) return null;
        return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
      })()`);
      const retap = async (name) => {
        const center = await triggerCenter(name);
        assert.ok(center, `${name} overflow trigger not visible`);
        await tapAt(page, center.x, center.y);
        await page.waitFor("Boolean(document.querySelector('.nx-menu'))");
        await page.evaluate(`window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))`);
        await page.waitFor("!document.querySelector('.nx-menu')");
        await tapAt(page, center.x, center.y);
        await sleep(120);
        await tapAt(page, center.x, center.y);
        await sleep(300);
      };
      const before = await page.evaluate(`({
        tabs: document.querySelectorAll('[role="tablist"][aria-label="工作区"] [role="tab"]').length,
      })`);
      await retap(".bashrc");
      const afterFile = await page.evaluate(`({
        tabs: document.querySelectorAll('[role="tablist"][aria-label="工作区"] [role="tab"]').length,
        stillHome: [...document.querySelectorAll('.nx-left-dock [role="tree"] .nx-row')].some((r) => r.textContent.includes('.bashrc')),
      })`);
      assert.equal(afterFile.tabs, before.tabs, `file retap must not open a tab: ${JSON.stringify({ before, afterFile })}`);
      assert.equal(afterFile.stillHome, true, "file retap must not navigate the tree");
      await retap("backups");
      const afterDir = await page.evaluate(`({
        stillHome: [...document.querySelectorAll('.nx-left-dock [role="tree"] .nx-row')].some((r) => r.textContent.includes('.bashrc')),
      })`);
      assert.equal(afterDir.stillHome, true, "dir retap must not navigate into the directory");
      return { evidence: { before, afterFile, afterDir } };
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

    await pass(`toast-ai-dock-shrunk-${theme}`, async () => {
      await boot(page, { theme, width: 1100, height: 800, coarse: false, layout: { leftWidth: 248, rightWidth: 760 } });
      await ensureAiDock(page);
      await floodErrorToasts(page);
      const evidence = await page.evaluate(`(() => {
        const t = document.querySelector('.nx-toasts').getBoundingClientRect();
        const dock = document.querySelector('.nx-right-dock').getBoundingClientRect();
        const input = document.querySelector('.nx-right-dock textarea');
        const ir = input.getBoundingClientRect();
        const ix = ir.left + ir.width / 2;
        const iy = ir.top + ir.height / 2;
        const placement = document.querySelector('.nx-app').getAttribute('data-nx-ai');
        const hit = (a, b) => !(a.right <= b.left + 0.5 || b.right <= a.left + 0.5 || a.bottom <= b.top + 0.5 || b.bottom <= a.top + 0.5);
        return {
          placement,
          toastLeft: t.left,
          toastRight: t.right,
          toastWidth: t.width,
          dockLeft: dock.left,
          overlapInput: hit(t, ir),
          inputCenter: { x: ix, y: iy },
          inputOwner: (() => { const el = document.elementFromPoint(ix, iy); return el === input || input.contains(el); })(),
          innerWidth,
        };
      })()`);
      assert.equal(evidence.placement, "dock", `760px dock at 1100px must keep dock placement: ${JSON.stringify(evidence)}`);
      assert.ok(evidence.toastLeft >= 0, `toast pushed off-screen: ${JSON.stringify(evidence)}`);
      assert.ok(evidence.toastRight <= evidence.dockLeft + 0.5, `toast overlaps the dock: ${JSON.stringify(evidence)}`);
      assert.ok(evidence.toastWidth <= 380, `toast wider than the cap: ${JSON.stringify(evidence)}`);
      assert.equal(evidence.overlapInput, false, `dock-mode toast must not cover the AI input: ${JSON.stringify(evidence)}`);
      assert.equal(evidence.inputOwner, true, `AI input covered at its center: ${JSON.stringify(evidence)}`);
      await clickAt(page, evidence.inputCenter.x, evidence.inputCenter.y);
      const focused = await page.evaluate(`document.activeElement === document.querySelector('.nx-right-dock textarea')`);
      assert.equal(focused, true, "click must move focus into the AI input");
      return { evidence };
    });

    await pass(`toast-ai-dock-fallback-${theme}`, async () => {
      await boot(page, { theme, width: 900, height: 800, coarse: false, layout: { leftWidth: 248, rightWidth: 760 } });
      await ensureAiDock(page);
      await floodErrorToasts(page);
      const evidence = await page.evaluate(`(() => {
        const t = document.querySelector('.nx-toasts').getBoundingClientRect();
        const input = document.querySelector('.nx-right-dock textarea');
        const ir = input.getBoundingClientRect();
        const ix = ir.left + ir.width / 2;
        const iy = ir.top + ir.height / 2;
        const header = document.querySelector('header').getBoundingClientRect();
        const placement = document.querySelector('.nx-app').getAttribute('data-nx-ai');
        return {
          placement,
          toastLeft: t.left,
          toastRight: t.right,
          toastTop: t.top,
          toastBottom: t.bottom,
          headerBottom: header.bottom,
          inputTop: ir.top,
          inputCenter: { x: ix, y: iy },
          inputOwner: (() => { const el = document.elementFromPoint(ix, iy); return el === input || input.contains(el); })(),
          innerWidth,
        };
      })()`);
      assert.equal(evidence.placement, "left", `760px dock at 900px must fall back to reachable placement: ${JSON.stringify(evidence)}`);
      assert.ok(evidence.toastLeft >= 0 && evidence.toastRight <= evidence.innerWidth + 0.5, `toast off-screen: ${JSON.stringify(evidence)}`);
      assert.ok(evidence.toastTop >= evidence.headerBottom - 0.5, `fallback toast must sit below the header: ${JSON.stringify(evidence)}`);
      assert.ok(evidence.toastBottom <= evidence.inputTop - 8, `fallback stack must stay above the AI input: ${JSON.stringify(evidence)}`);
      assert.equal(evidence.inputOwner, true, `AI input covered at its center: ${JSON.stringify(evidence)}`);
      await clickAt(page, evidence.inputCenter.x, evidence.inputCenter.y);
      const focused = await page.evaluate(`document.activeElement === document.querySelector('.nx-right-dock textarea')`);
      assert.equal(focused, true, "click must move focus into the AI input");
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
  const vite = await startVite({ root: ROOT, port: VITE_PORT });
  const chrome = await startChrome();
  const page = await newPage(chrome);
  try {
    await touchAcceptance(page);
  } catch (error) {
    harnessErrors.push(String(error?.stack || error));
  } finally {
    page.close();
    stop(chrome.process);
    await vite?.stop();
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
