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
const OUT = path.join(ROOT, "target/acceptance-small-screen-actions");
const VITE_PORT = Number(process.env.NEXTERM_VITE_PORT || 1423);
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

async function clickAt(page, x, y) {
  await page.send("Input.dispatchMouseEvent", { type: "mousePressed", x, y, button: "left", clickCount: 1 });
  await page.send("Input.dispatchMouseEvent", { type: "mouseReleased", x, y, button: "left", clickCount: 1 });
  await sleep(80);
}

async function tapSelector(page, selector) {
  const point = await page.evaluate(`(() => {
    const el = document.querySelector(${JSON.stringify(selector)});
    if (!el) return null;
    const r = el.getBoundingClientRect();
    return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
  })()`);
  if (!point) throw new Error(`selector not found: ${selector}`);
  await clickAt(page, point.x, point.y);
}

async function pressKey(page, { key, code, vk, text }) {
  const base = { key, code, windowsVirtualKeyCode: vk, nativeVirtualKeyCode: vk };
  if (text) await page.send("Input.dispatchKeyEvent", { type: "keyDown", ...base, text });
  else await page.send("Input.dispatchKeyEvent", { type: "rawKeyDown", ...base });
  await page.send("Input.dispatchKeyEvent", { type: "keyUp", ...base });
  await sleep(80);
}

async function screenshot(page, name) {
  const shot = await page.send("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(path.join(OUT, name), Buffer.from(shot.data, "base64"));
  return name;
}

async function setViewport(page, { width, height, mobile = false, dsf = 2 }) {
  await page.send("Emulation.setDeviceMetricsOverride", {
    width,
    height,
    deviceScaleFactor: dsf,
    mobile,
    screenWidth: width,
    screenHeight: height,
  });
  if (mobile) {
    await page.send("Emulation.setTouchEmulationEnabled", { enabled: true, maxTouchPoints: 5 });
  } else {
    await page.send("Emulation.setTouchEmulationEnabled", { enabled: false });
  }
}

async function boot(page, { theme, viewport }) {
  const { identifier: seedId } = await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source:
      theme === undefined
        ? `try { localStorage.removeItem("nexterm.theme.v1"); } catch {}`
        : `try { localStorage.setItem("nexterm.theme.v1", ${JSON.stringify(theme)}); } catch {}`,
  });
  try {
    if (viewport) await setViewport(page, viewport);
    await page.navigate(`${VITE}/?demo=1`);
    await page.waitFor(
      "Boolean(document.querySelector('[role=\"tablist\"][aria-label=\"工作区\"] [role=\"tab\"]') && document.querySelector('.xterm'))",
    );
  } finally {
    await page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier: seedId });
  }
}

const PHONE = { name: "390x844", width: 390, height: 844, mobile: true };
const DESKTOP = { name: "1440x900", width: 1440, height: 900, mobile: false };

async function assertNoHorizontalOverflow(page, label) {
  const evidence = await page.evaluate(`(() => ({
    innerWidth: window.innerWidth,
    docScrollWidth: document.scrollingElement.scrollWidth,
    bodyScrollWidth: document.body.scrollWidth,
    theme: document.documentElement.dataset.nxTheme ?? null,
  }))()`);
  assert.ok(
    evidence.docScrollWidth <= evidence.innerWidth + 1 && evidence.bodyScrollWidth <= evidence.innerWidth + 1,
    `${label}: page-level horizontal overflow: ${JSON.stringify(evidence)}`,
  );
  return evidence;
}

async function openAssetDock(page) {
  await page.evaluate(`document.querySelector('.nx-rail button[aria-label="资产"]').click()`);
  await page.waitFor(`Boolean(document.querySelector('[role="tree"] [role="treeitem"] .nx-row-target'))`);
  await sleep(150);
}

async function smallScreenAcceptance(page) {
  for (const theme of ["dark", "light"]) {
    await pass(`asset-rows-overflow-menu-390-${theme}`, async () => {
      await boot(page, { theme, viewport: PHONE });
      const overflow = await assertNoHorizontalOverflow(page, `390-${theme}`);
      assert.equal(overflow.theme, theme);
      await openAssetDock(page);

      const evidence = await page.evaluate(`(() => {
        const rows = [...document.querySelectorAll('[role="tree"] [role="treeitem"]')];
        return rows.map((row) => {
          const name = row.querySelector(".nx-row-name");
          const more = row.querySelector(".nx-row-more");
          const strip = row.querySelector(".nx-row-actions");
          const nr = name?.getBoundingClientRect();
          const mr = more?.getBoundingClientRect();
          return {
            text: (name?.textContent ?? row.textContent ?? "").slice(0, 24),
            nameWidth: nr ? Math.round(nr.width) : null,
            moreSize: mr ? { w: Math.round(mr.width), h: Math.round(mr.height) } : null,
            moreOnScreen: !!mr && mr.left >= 0 && mr.right <= window.innerWidth,
            stripPresent: Boolean(strip),
          };
        });
      })()`);
      assert.ok(evidence.length >= 2, `expected asset tree rows: ${JSON.stringify(evidence)}`);
      for (const row of evidence) {
        assert.ok(row.nameWidth !== null && row.nameWidth > 0, `asset name collapsed: ${JSON.stringify(row)}`);
        assert.ok(row.moreSize && row.moreSize.w >= 24 && row.moreSize.h >= 24, `overflow target too small: ${JSON.stringify(row)}`);
        assert.equal(row.moreOnScreen, true, `overflow button off-screen: ${JSON.stringify(row)}`);
        assert.equal(row.stripPresent, false, `desktop action strip rendered on coarse pointer: ${JSON.stringify(row)}`);
      }
      await screenshot(page, `asset-rows-390-${theme}.png`);
      return { evidence };
    });

    await pass(`asset-overflow-menu-actions-390-${theme}`, async () => {
      await boot(page, { theme, viewport: PHONE });
      await openAssetDock(page);
      const tabsBefore = await page.evaluate(`document.querySelectorAll('[role="tablist"][aria-label="工作区"] [role="tab"]').length`);

      await tapSelector(page, '[role="tree"] [role="treeitem"] .nx-row-name');
      await sleep(200);
      const tabsAfterTap = await page.evaluate(`document.querySelectorAll('[role="tablist"][aria-label="工作区"] [role="tab"]').length`);
      assert.equal(tabsAfterTap, tabsBefore, `single tap on the row must not connect: ${tabsBefore} -> ${tabsAfterTap}`);

      const opened = await page.evaluate(`(() => {
        const row = [...document.querySelectorAll('[role="tree"] [role="treeitem"]')].find(
          (r) => r.querySelector(":scope > .nx-row-more") && r.querySelector(":scope > .nx-row-target"),
        );
        if (!row) return false;
        row.querySelector(":scope > .nx-row-more").scrollIntoView({ block: "center" });
        row.querySelector(":scope > .nx-row-more").click();
        return true;
      })()`);
      assert.equal(opened, true, "no asset row with an address found");
      await page.waitFor(`Boolean(document.querySelector('[role="menu"]'))`);

      const evidence = await page.evaluate(`(() => {
        const menu = document.querySelector('[role="menu"]');
        const r = menu.getBoundingClientRect();
        const labels = [...menu.querySelectorAll('[role="menuitem"]')].map(
          (b) => b.querySelector(".nx-menu-label")?.textContent ?? "",
        );
        return {
          labels,
          left: Math.round(r.left),
          right: Math.round(r.right),
          innerWidth: window.innerWidth,
        };
      })()`);
      for (const label of ["连接", "编辑", "删除", "克隆", "隐藏"]) {
        assert.ok(evidence.labels.includes(label), `menu missing ${label}: ${JSON.stringify(evidence)}`);
      }
      assert.ok(evidence.left >= 0 && evidence.right <= evidence.innerWidth, `menu off-screen: ${JSON.stringify(evidence)}`);
      await screenshot(page, `asset-overflow-menu-390-${theme}.png`);

      await pressKey(page, { key: "Escape", code: "Escape", vk: 27 });
      await page.waitFor(`!document.querySelector('[role="menu"]')`);
      const tabsAfterMenu = await page.evaluate(`document.querySelectorAll('[role="tablist"][aria-label="工作区"] [role="tab"]').length`);
      assert.equal(tabsAfterMenu, tabsBefore, `opening and dismissing the menu must not connect: ${JSON.stringify(tabsAfterMenu)}`);
      return { evidence };
    });

    await pass(`terminal-toolbar-overflow-390-${theme}`, async () => {
      await boot(page, { theme, viewport: PHONE });
      await assertNoHorizontalOverflow(page, `390-toolbar-${theme}`);
      const evidence = await page.evaluate(`(() => {
        const toolbar = document.querySelector('.nx-toolbar');
        const visible = (el) => {
          if (!el) return false;
          const r = el.getBoundingClientRect();
          return r.width > 0 && r.height > 0 && r.right <= window.innerWidth + 1;
        };
        const byText = (text) => [...toolbar.querySelectorAll('button')].find((b) => b.textContent.includes(text));
        return {
          search: visible(toolbar.querySelector('button[title^="搜索终端内容"]')),
          encoding: visible(toolbar.querySelector('select')),
          record: visible(byText("录制")),
          blocks: visible(byText("命令块")),
          prev: visible(toolbar.querySelector('button[title="定位到上一条命令"]')),
          next: visible(toolbar.querySelector('button[title="定位到下一条命令"]')),
          more: visible(toolbar.querySelector('button[aria-label="更多终端操作"]')),
        };
      })()`);
      assert.deepEqual(
        { search: evidence.search, encoding: evidence.encoding, record: evidence.record, blocks: evidence.blocks, prev: evidence.prev, next: evidence.next },
        { search: false, encoding: false, record: false, blocks: false, prev: false, next: false },
        `inline toolbar controls must collapse into the overflow at 390px: ${JSON.stringify(evidence)}`,
      );
      assert.equal(evidence.more, true, `overflow button must be visible: ${JSON.stringify(evidence)}`);

      await tapSelector(page, '.nx-toolbar button[aria-label="更多终端操作"]');
      await page.waitFor(`Boolean(document.querySelector('[role="menu"]'))`);
      const menu = await page.evaluate(`(() => {
        const box = document.querySelector('[role="menu"]');
        const r = box.getBoundingClientRect();
        const labels = [...box.querySelectorAll('[role="menuitem"]')]
          .filter((el) => !el.closest('.nx-submenu'))
          .map((b) => b.querySelector('.nx-menu-label')?.textContent ?? '');
        return { labels, left: Math.round(r.left), right: Math.round(r.right), innerWidth: window.innerWidth };
      })()`);
      for (const label of ["搜索终端内容", "录制输出", "定位到上一条命令", "定位到下一条命令", "命令块", "字符编码"]) {
        assert.ok(menu.labels.includes(label), `toolbar menu missing ${label}: ${JSON.stringify(menu)}`);
      }
      assert.ok(menu.left >= 0 && menu.right <= menu.innerWidth, `toolbar menu off-screen: ${JSON.stringify(menu)}`);
      await screenshot(page, `terminal-toolbar-menu-390-${theme}.png`);

      await page.evaluate(`(() => {
        const item = [...document.querySelectorAll('[role="menuitem"]')].find((b) => b.textContent.includes('命令块'));
        item.click();
      })()`);
      await page.waitFor(`Boolean(document.querySelector('.nx-command-blocks'))`);
      await screenshot(page, `terminal-command-blocks-390-${theme}.png`);
      return { evidence, menu };
    });

    await pass(`files-rows-overflow-390-${theme}`, async () => {
      await boot(page, { theme, viewport: PHONE });
      await assertNoHorizontalOverflow(page, `390-files-${theme}`);

      await page.evaluate(`document.querySelector('.nx-rail button[aria-label="文件树"]').click()`);
      await page.waitFor(`Boolean(document.querySelector('[role="tree"][aria-label="文件"] [role="treeitem"] .nx-row-more'))`);
      await sleep(150);
      const treeEvidence = await page.evaluate(`(() => {
        const rows = [...document.querySelectorAll('[role="tree"][aria-label="文件"] [role="treeitem"]')];
        return rows.map((row) => {
          const name = row.querySelector('.nx-row-name');
          const more = row.querySelector('.nx-row-more');
          const nr = name?.getBoundingClientRect();
          const mr = more?.getBoundingClientRect();
          return {
            nameWidth: nr ? Math.round(nr.width) : null,
            moreSize: mr ? { w: Math.round(mr.width), h: Math.round(mr.height) } : null,
            moreOnScreen: !!mr && mr.left >= 0 && mr.right <= window.innerWidth,
            stripPresent: Boolean(row.querySelector('.nx-row-actions')),
          };
        });
      })()`);
      assert.ok(treeEvidence.length >= 2, `expected file tree rows: ${JSON.stringify(treeEvidence)}`);
      for (const row of treeEvidence) {
        assert.ok(row.nameWidth !== null && row.nameWidth > 0, `file name collapsed: ${JSON.stringify(row)}`);
        assert.ok(row.moreSize && row.moreSize.w >= 24 && row.moreSize.h >= 24, `overflow target too small: ${JSON.stringify(row)}`);
        assert.equal(row.moreOnScreen, true, `overflow button off-screen: ${JSON.stringify(row)}`);
        assert.equal(row.stripPresent, false, `desktop action strip rendered on coarse pointer: ${JSON.stringify(row)}`);
      }

      const opened = await page.evaluate(`(() => {
        const row = [...document.querySelectorAll('[role="tree"][aria-label="文件"] [role="treeitem"]')].find(
          (r) => r.querySelector(':scope > .nx-row-more') && !r.querySelector(':scope > .nx-tree-caret'),
        );
        if (!row) return false;
        row.querySelector(':scope > .nx-row-more').click();
        return true;
      })()`);
      assert.equal(opened, true, "no file row with an overflow button");
      await page.waitFor(`Boolean(document.querySelector('[role="menu"]'))`);
      const treeMenu = await page.evaluate(`(() => {
        const box = document.querySelector('[role="menu"]');
        const r = box.getBoundingClientRect();
        const labels = [...box.querySelectorAll('[role="menuitem"]')].map(
          (b) => b.querySelector('.nx-menu-label')?.textContent ?? '',
        );
        return { labels, left: Math.round(r.left), right: Math.round(r.right), innerWidth: window.innerWidth };
      })()`);
      for (const label of ["下载当前文件", "重命名", "权限…", "校验值…", "删除"]) {
        assert.ok(treeMenu.labels.includes(label), `file tree menu missing ${label}: ${JSON.stringify(treeMenu)}`);
      }
      assert.ok(treeMenu.left >= 0 && treeMenu.right <= treeMenu.innerWidth, `file tree menu off-screen: ${JSON.stringify(treeMenu)}`);
      await screenshot(page, `filetree-overflow-390-${theme}.png`);
      await pressKey(page, { key: "Escape", code: "Escape", vk: 27 });
      await page.waitFor(`!document.querySelector('[role="menu"]')`);

      await page.evaluate(`(() => {
        const backdrop = document.querySelector('.nx-dock-backdrop');
        if (backdrop) backdrop.click();
      })()`);
      await page.send("Input.dispatchKeyEvent", { type: "rawKeyDown", key: "p", code: "KeyP", windowsVirtualKeyCode: 80, modifiers: 10 });
      await page.send("Input.dispatchKeyEvent", { type: "keyUp", key: "p", code: "KeyP", windowsVirtualKeyCode: 80, modifiers: 10 });
      await sleep(150);
      await page.waitFor(`Boolean(document.querySelector('[role="dialog"] [role="combobox"], .nx-modal [role="combobox"]'))`);
      await page.send("Input.insertText", { text: "文件" });
      await page.waitFor(`[...document.querySelectorAll('[role="option"],[role="listbox"] *')].some((el) => el.textContent?.includes('打开宽幅文件浏览器'))`);
      await page.evaluate(`(() => {
        const option = [...document.querySelectorAll('[role="option"],[role="listbox"] *')].find((el) => el.textContent?.includes('打开宽幅文件浏览器'));
        (option.closest('[role="option"]') ?? option).click();
      })()`);
      await page.waitFor(`Boolean(document.querySelector('.nx-pane .nx-toolbar input.font-mono'))`);
      await sleep(200);
      const browserEvidence = await page.evaluate(`(() => {
        const panel = document.querySelector('[role="tabpanel"]:not(.hidden)');
        const rows = [...panel.querySelectorAll('.cursor-pointer')];
        return rows.slice(0, 10).map((row) => {
          const name = row.querySelector('.nx-row-name');
          const more = row.querySelector('.nx-row-more');
          const nr = name?.getBoundingClientRect();
          const mr = more?.getBoundingClientRect();
          return {
            nameWidth: nr ? Math.round(nr.width) : null,
            moreOnScreen: !!mr && mr.left >= 0 && mr.right <= window.innerWidth,
            stripPresent: Boolean(row.querySelector('.nx-row-actions')),
          };
        });
      })()`);
      assert.ok(browserEvidence.length >= 2, `expected file browser rows: ${JSON.stringify(browserEvidence)}`);
      for (const row of browserEvidence) {
        assert.ok(row.nameWidth !== null && row.nameWidth > 0, `browser file name collapsed: ${JSON.stringify(row)}`);
        assert.equal(row.moreOnScreen, true, `browser overflow button off-screen: ${JSON.stringify(row)}`);
        assert.equal(row.stripPresent, false, `browser hover strip rendered on coarse pointer: ${JSON.stringify(row)}`);
      }
      await screenshot(page, `filebrowser-rows-390-${theme}.png`);
      return { treeEvidence, treeMenu, browserEvidence };
    });

    await pass(`statusbar-actions-reachable-390-${theme}`, async () => {
      await boot(page, { theme, viewport: PHONE });
      await assertNoHorizontalOverflow(page, `390-statusbar-${theme}`);
      const evidence = await page.evaluate(`(() => {
        const bar = document.querySelector('.nx-statusbar');
        const links = [...bar.querySelectorAll('button.nx-link')];
        const audit = links.find((b) => b.textContent.includes('审计'));
        const settings = links.find((b) => b.textContent.includes('设置'));
        const probe = (el) => {
          const r = el.getBoundingClientRect();
          const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
          return {
            left: Math.round(r.left),
            right: Math.round(r.right),
            onScreen: r.left >= 0 && r.right <= window.innerWidth,
            hit: hit === el || el.contains(hit),
          };
        };
        return {
          barScrollable: bar.scrollWidth > bar.clientWidth + 1,
          info: bar.querySelector('.nx-statusbar-info')?.textContent ?? '',
          audit: probe(audit),
          settings: probe(settings),
        };
      })()`);
      assert.equal(evidence.barScrollable, false, `statusbar must not need scrolling: ${JSON.stringify(evidence)}`);
      assert.ok(evidence.info.includes("个会话") && evidence.info.includes("已连接"), `status info missing: ${JSON.stringify(evidence)}`);
      assert.equal(evidence.audit.onScreen, true, `audit action off-screen: ${JSON.stringify(evidence)}`);
      assert.equal(evidence.audit.hit, true, `audit action not clickable: ${JSON.stringify(evidence)}`);
      assert.equal(evidence.settings.onScreen, true, `settings action off-screen: ${JSON.stringify(evidence)}`);
      assert.equal(evidence.settings.hit, true, `settings action not clickable: ${JSON.stringify(evidence)}`);
      await screenshot(page, `statusbar-390-${theme}.png`);

      await tapSelector(page, '.nx-statusbar button.nx-link');
      await page.waitFor(`[...document.querySelectorAll('.nx-tab')].some((t) => t.textContent.includes('审计日志'))`);
      return { evidence };
    });

    await pass(`desktop-arrangement-1440-${theme}`, async () => {
      await boot(page, { theme, viewport: DESKTOP });
      const overflow = await assertNoHorizontalOverflow(page, `1440-${theme}`);
      assert.equal(overflow.theme, theme);
      const evidence = await page.evaluate(`(() => {
        const toolbar = document.querySelector('.nx-toolbar');
        const visible = (el) => {
          if (!el) return false;
          const r = el.getBoundingClientRect();
          return r.width > 0 && r.height > 0 && r.right <= window.innerWidth + 1;
        };
        const byText = (text) => [...toolbar.querySelectorAll('button')].find((b) => b.textContent.includes(text));
        return {
          search: visible(toolbar.querySelector('button[title^="搜索终端内容"]')),
          encoding: visible(toolbar.querySelector('select')),
          record: visible(byText("录制")),
          blocks: visible(byText("命令块")),
          more: visible(toolbar.querySelector('button[aria-label="更多终端操作"]')),
        };
      })()`);
      assert.deepEqual(
        { search: evidence.search, encoding: evidence.encoding, record: evidence.record, blocks: evidence.blocks },
        { search: true, encoding: true, record: true, blocks: true },
        `desktop toolbar must keep every inline control: ${JSON.stringify(evidence)}`,
      );
      assert.equal(evidence.more, false, `overflow button must stay hidden on desktop: ${JSON.stringify(evidence)}`);

      const statusLinks = await page.evaluate(`(() => {
        const links = [...document.querySelectorAll('.nx-statusbar button.nx-link')];
        return links.map((el) => {
          const r = el.getBoundingClientRect();
          return { text: el.textContent, onScreen: r.left >= 0 && r.right <= window.innerWidth };
        });
      })()`);
      assert.deepEqual(statusLinks.map((s) => s.text), ["审计", "设置"], JSON.stringify(statusLinks));
      assert.ok(statusLinks.every((s) => s.onScreen), JSON.stringify(statusLinks));

      await page.evaluate(`document.querySelector('.nx-rail button[aria-label="资产"]').click()`);
      await page.waitFor(`Boolean(document.querySelector('[role="tree"] [role="treeitem"]'))`);
      const rows = await page.evaluate(`(() => {
        return [...document.querySelectorAll('[role="tree"] [role="treeitem"]')].map((row) => {
          const name = row.querySelector('.nx-row-name');
          const nr = name?.getBoundingClientRect();
          return {
            nameWidth: nr ? Math.round(nr.width) : null,
            stripPresent: Boolean(row.querySelector('.nx-row-actions')),
            morePresent: Boolean(row.querySelector('.nx-row-more')),
          };
        });
      })()`);
      assert.ok(rows.length >= 2, JSON.stringify(rows));
      for (const row of rows) {
        assert.ok(row.nameWidth !== null && row.nameWidth > 0, `desktop asset name collapsed: ${JSON.stringify(row)}`);
        assert.equal(row.stripPresent, true, `desktop hover strip missing: ${JSON.stringify(row)}`);
        assert.equal(row.morePresent, false, `coarse overflow button rendered on fine pointer: ${JSON.stringify(row)}`);
      }
      await screenshot(page, `asset-rows-1440-${theme}.png`);
      return { evidence, statusLinks, rows };
    });
  }

  await pass("terminal-toolbar-keyboard-390", async () => {
    await boot(page, { theme: "dark", viewport: PHONE });
    await page.evaluate(`document.querySelector('.nx-toolbar button[aria-label="更多终端操作"]').focus()`);
    await pressKey(page, { key: "Enter", code: "Enter", vk: 13, text: "\r" });
    await page.waitFor(`Boolean(document.querySelector('[role="menu"]'))`);
    await page.waitFor(
      `(document.activeElement?.querySelector?.('.nx-menu-label')?.textContent ?? '').includes('搜索终端内容')`,
    );

    await pressKey(page, { key: "ArrowDown", code: "ArrowDown", vk: 40 });
    await page.waitFor(
      `(() => {
        const el = document.activeElement;
        const label = el?.querySelector?.('.nx-menu-label')?.textContent ?? '';
        return el?.getAttribute?.('role') === 'menuitem' && label.length > 0 && !label.includes('搜索终端内容');
      })()`,
    );
    const afterDown = await page.evaluate(
      `document.activeElement?.querySelector?.('.nx-menu-label')?.textContent ?? ''`,
    );

    await pressKey(page, { key: "ArrowUp", code: "ArrowUp", vk: 38 });
    await page.waitFor(
      `(document.activeElement?.querySelector?.('.nx-menu-label')?.textContent ?? '').includes('搜索终端内容')`,
    );

    await pressKey(page, { key: "Enter", code: "Enter", vk: 13, text: "\r" });
    await page.waitFor(`Boolean(document.querySelector('input[placeholder="搜索终端内容…"]'))`);
    await page.waitFor(
      `document.activeElement === document.querySelector('input[placeholder="搜索终端内容…"]')`,
    );
    await pressKey(page, { key: "Escape", code: "Escape", vk: 27 });
    await page.waitFor(`!document.querySelector('input[placeholder="搜索终端内容…"]')`);
    return { keyboard: "enter/arrows/enter/escape", afterDown };
  });

  await pass("asset-overflow-keyboard-390", async () => {
    await boot(page, { theme: "dark", viewport: PHONE });
    await openAssetDock(page);
    await page.evaluate(`(() => {
      const row = [...document.querySelectorAll('[role="tree"] [role="treeitem"]')].find((r) => r.querySelector('.nx-row-more'));
      row.querySelector('.nx-row-more').focus();
    })()`);
    await pressKey(page, { key: "Enter", code: "Enter", vk: 13, text: "\r" });
    await page.waitFor(`Boolean(document.querySelector('[role="menu"]'))`);
    await page.waitFor(
      `(document.activeElement?.querySelector?.('.nx-menu-label')?.textContent ?? '').includes('连接')`,
    );
    await pressKey(page, { key: "Escape", code: "Escape", vk: 27 });
    await page.waitFor(`!document.querySelector('[role="menu"]')`);
    return { keyboard: "enter/escape" };
  });
}

async function main() {
  const vite = await startVite({ root: ROOT, port: VITE_PORT });
  const chrome = await startChrome();
  const page = await newPage(chrome);
  try {
    await smallScreenAcceptance(page);
  } catch (error) {
    harnessErrors.push(String(error?.stack || error));
  } finally {
    page.close();
    stop(chrome.process);
    await vite?.stop();
    fs.rmSync(chrome.profile, { recursive: true, force: true, maxRetries: 8, retryDelay: 250 });
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
