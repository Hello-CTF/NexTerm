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
const OUT = path.join(ROOT, "target/acceptance-files-responsive");
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

async function setViewport(page, { width, height, touch }) {
  await page.send("Emulation.setDeviceMetricsOverride", {
    width,
    height,
    deviceScaleFactor: 2,
    mobile: touch,
  });
  await page.send("Emulation.setTouchEmulationEnabled", { enabled: touch, maxTouchPoints: touch ? 5 : 1 });
  await page.send("Emulation.setEmulatedMedia", {
    features: touch
      ? [
          { name: "pointer", value: "coarse" },
          { name: "hover", value: "none" },
        ]
      : [],
  });
}

async function dismissToasts(page) {
  for (let i = 0; i < 5; i++) {
    const found = await page.evaluate(`(() => {
      const buttons = [...document.querySelectorAll(".nx-toasts button")];
      for (const btn of buttons) btn.click();
      return buttons.length > 0;
    })()`);
    if (!found) return;
    await sleep(150);
  }
}

async function boot(page, { width, height, touch, theme = null }) {
  await setViewport(page, { width, height, touch });
  const { identifier } = await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `
      try { localStorage.clear(); } catch {}
      try { localStorage.setItem("nexterm.theme.v1", ${JSON.stringify(theme)}); } catch {}
    `,
  });
  try {
    await page.navigate(`${VITE}/?demo=1`);
    await page.waitFor(
      "Boolean(document.querySelector('[role=\"tablist\"][aria-label=\"工作区\"] [role=\"tab\"]') && document.querySelector('.xterm'))",
    );
    await sleep(1200);
    await dismissToasts(page);
    await sleep(600);
    await dismissToasts(page);
  } finally {
    await page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier });
  }
}

async function tap(page, x, y) {
  await page.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x, y }] });
  await sleep(50);
  await page.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
  await sleep(150);
}

async function rectOf(page, selector) {
  return page.evaluate(`(() => {
    const el = document.querySelector(${JSON.stringify(selector)});
    if (!el) return null;
    const r = el.getBoundingClientRect();
    return { left: r.left, right: r.right, top: r.top, bottom: r.bottom, width: r.width, height: r.height };
  })()`);
}

async function tapSelector(page, selector) {
  const rect = await rectOf(page, selector);
  if (!rect || rect.width === 0 || rect.height === 0) {
    throw new Error(`cannot tap zero-size element: ${selector}`);
  }
  await tap(page, (rect.left + rect.right) / 2, (rect.top + rect.bottom) / 2);
}

async function tapButtonText(page, text, rootSelector = null) {
  const rect = await page.evaluate(`(() => {
    const root = ${rootSelector ? `document.querySelector(${JSON.stringify(rootSelector)})` : "document"};
    if (!root) return null;
    const el = [...root.querySelectorAll("button, [role='menuitem']")].find(
      (b) => b.textContent?.trim() === ${JSON.stringify(text)} || b.textContent?.trim().startsWith(${JSON.stringify(text)}),
    );
    if (!el) return null;
    const r = el.getBoundingClientRect();
    return { left: r.left, right: r.right, top: r.top, bottom: r.bottom, width: r.width, height: r.height };
  })()`);
  if (!rect || rect.width === 0 || rect.height === 0) {
    throw new Error(`cannot tap button text: ${text}`);
  }
  await tap(page, (rect.left + rect.right) / 2, (rect.top + rect.bottom) / 2);
}

async function noPageOverflow(page, label, requestedWidth) {
  const m = await page.evaluate(`(() => ({
    inner: window.innerWidth,
    doc: document.documentElement.scrollWidth,
    body: document.body.scrollWidth,
  }))()`);
  assert.ok(
    m.inner <= requestedWidth,
    `${label}: layout viewport expanded inner=${m.inner} > requested=${requestedWidth}`,
  );
  assert.ok(
    m.doc <= m.inner && m.body <= m.inner,
    `${label}: page-level horizontal overflow doc=${m.doc} body=${m.body} > inner=${m.inner}`,
  );
  return m;
}

async function openFileBrowserTab(page) {
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
}

async function openDock(page, ariaLabel) {
  await page.evaluate(`(() => {
    const btn = document.querySelector('button[aria-label="${ariaLabel}"]');
    if (!btn) throw new Error("rail button not found: ${ariaLabel}");
    btn.click();
  })()`);
}

async function filesResponsiveAcceptance(page) {
  await pass("filebrowser-320-columns-toolbar-touch-menu", async () => {
    await boot(page, { width: 320, height: 568, touch: true, theme: "dark" });
    await openFileBrowserTab(page);
    await page.waitFor(`[...document.querySelectorAll('.nx-pane .font-mono')].some((el) => el.textContent?.includes('projects'))`);

    const columns = await page.evaluate(`(() => {
      const panel = document.querySelector('[role="tabpanel"]:not(.hidden)');
      const rows = [...panel.querySelectorAll('.cursor-pointer')];
      const first = rows.find((r) => r.textContent?.includes('projects')) ?? rows[0];
      const name = first?.querySelector('.truncate.font-mono');
      const headerCells = [...panel.querySelectorAll('.border-b span')].map((s) => ({
        text: s.textContent?.trim(),
        width: s.getBoundingClientRect().width,
      }));
      return {
        nameWidth: name?.getBoundingClientRect().width ?? 0,
        headerWidths: headerCells,
      };
    })()`);
    assert.ok(columns.nameWidth > 60, `name column must keep real width at 320px: ${JSON.stringify(columns)}`);
    const sizeHeader = columns.headerWidths.find((c) => c.text === "大小");
    const mtimeHeader = columns.headerWidths.find((c) => c.text === "修改时间");
    assert.equal(sizeHeader?.width, 0, "大小 column must be hidden ≤560px");
    assert.equal(mtimeHeader?.width, 0, "修改时间 column must be hidden ≤560px");

    const more = await rectOf(page, '[role="tabpanel"]:not(.hidden) .nx-toolbar button[aria-label="更多操作"]');
    assert.ok(more && more.left >= 0 && more.right <= 320, `toolbar ⋯ must be on-screen: ${JSON.stringify(more)}`);
    const uploadVisible = await page.evaluate(`(() => {
      const panel = document.querySelector('[role="tabpanel"]:not(.hidden)');
      const btn = [...panel.querySelectorAll('.nx-toolbar button')].find((b) => b.textContent?.trim() === "上传");
      return btn ? btn.getBoundingClientRect().width > 0 : false;
    })()`);
    assert.equal(uploadVisible, false, "低频文字按钮 ≤560px 应收起（功能在 ⋯ 菜单内）");

    await noPageOverflow(page, "FileBrowser@320", 320);

    const rowMoreSelector = '[role="tabpanel"]:not(.hidden) .cursor-pointer button[aria-label^="更多操作 "]';
    await tapSelector(page, rowMoreSelector);
    await page.waitFor(`Boolean(document.querySelector('[role="menu"]'))`);
    const menuInfo = await page.evaluate(`(() => ({
      items: [...document.querySelectorAll('[role="menuitem"]')].map((b) => b.textContent?.trim()),
      inViewport: (() => {
        const r = document.querySelector('[role="menu"]').getBoundingClientRect();
        return r.left >= 0 && r.right <= window.innerWidth;
      })(),
    }))()`);
    assert.ok(menuInfo.items.some((l) => l?.includes("重命名")), `menu must offer 重命名: ${JSON.stringify(menuInfo)}`);
    assert.ok(menuInfo.items.some((l) => l?.includes("权限…")), `menu must offer 权限…: ${JSON.stringify(menuInfo)}`);
    assert.equal(menuInfo.inViewport, true, "menu must be viewport-clamped");
    await tapButtonText(page, "重命名", '[role="menu"]');
    await page.waitFor(`Boolean(document.querySelector('.nx-modal input'))`, 10_000);
    await page.evaluate(`(() => {
      const modal = document.querySelector('.nx-modal');
      modal.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    })()`);
    await page.waitFor(`!document.querySelector('.nx-modal input')`);
    await screenshot(page, "filebrowser-320-menu.png");
    return { evidence: { columns, more, menuItems: menuInfo.items } };
  });

  await pass("filetree-320-breadcrumb-scroll-and-touch-more", async () => {
    await boot(page, { width: 320, height: 568, touch: true, theme: "dark" });
    await openDock(page, "文件树");
    await page.waitFor(`Boolean(document.querySelector('[role="tree"][aria-label="文件"] [role="treeitem"]'))`);

    for (const name of ["projects", "api-server", "src"]) {
      await page.evaluate(`(() => {
        const row = [...document.querySelectorAll('[role="treeitem"]')].find((r) => r.textContent?.trim().startsWith("${name}"));
        if (!row) throw new Error("tree row not found: ${name}");
        row.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
      })()`);
      await sleep(250);
    }
    await page.waitFor(`document.querySelector('[aria-current="location"]')?.textContent === "src"`);
    const crumb = await page.evaluate(`(() => {
      const box = document.querySelector('[aria-current="location"]')?.closest(".overflow-x-auto");
      return box ? { scrollLeft: box.scrollLeft, scrollWidth: box.scrollWidth, clientWidth: box.clientWidth } : null;
    })()`);
    assert.ok(crumb && crumb.scrollWidth > crumb.clientWidth, `deep path must overflow the crumb box: ${JSON.stringify(crumb)}`);
    assert.ok(crumb.scrollLeft > 0, `crumb box must scroll to the current location: ${JSON.stringify(crumb)}`);

    const treeItem = '[role="tree"][aria-label="文件"] [role="treeitem"]';
    await page.waitFor(`[...document.querySelectorAll('${treeItem}')].some((r) => r.textContent?.includes("index.ts"))`);
    await tapSelector(page, `${treeItem} button[aria-label="更多操作 index.ts"]`);
    await page.waitFor(`Boolean(document.querySelector('[role="menu"]'))`);
    const items = await page.evaluate(`[...document.querySelectorAll('[role="menuitem"]')].map((b) => b.textContent?.trim())`);
    assert.ok(items.some((l) => l?.includes("校验值…")), `touch ⋯ must reach 校验值…: ${JSON.stringify(items)}`);
    await tapButtonText(page, "校验值…", '[role="menu"]');
    await page.waitFor(`Boolean(document.querySelector('.nx-modal'))`, 10_000);
    await page.evaluate(`(() => {
      document.querySelector('.nx-modal').dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    })()`);
    await page.waitFor(`!document.querySelector('.nx-modal')`);
    await noPageOverflow(page, "FileTree dock@320", 320);
    await screenshot(page, "filetree-320-crumbs.png");
    return { evidence: { crumb, menuItems: items } };
  });

  await pass("files-row-actions-hover-focus-geometry-320-390-desktop", async () => {
    const evidence = {};
    const treeScope = '.nx-left-dock [role="tree"][aria-label="文件"]';
    const browserScope = '[role="tabpanel"]:not(.hidden) [role="tree"][aria-label="文件"]';
    const geometryExpr = (scope, name) => `(() => {
      const root = document.querySelector(${JSON.stringify(scope)});
      if (!root) return null;
      const rows = [...root.querySelectorAll('[role="treeitem"]')];
      const row = rows.find((r) => r.textContent?.includes(${JSON.stringify(name)})) ?? rows[0];
      if (!row) return null;
      const strip = row.querySelector('.nx-row-actions');
      const del = row.querySelector('.nx-row-actions button[aria-label^="删除 "]');
      const rect = (el) => {
        if (!el) return null;
        const r = el.getBoundingClientRect();
        return { top: r.top, bottom: r.bottom, left: r.left, right: r.right, width: r.width, height: r.height };
      };
      return {
        row: rect(row),
        name: rect(row.querySelector('.nx-row-name')),
        next: rect(row.nextElementSibling),
        stripVisibility: strip ? getComputedStyle(strip).visibility : null,
        del: rect(del),
      };
    })()`;
    const assertStable = (before, after, label) => {
      assert.ok(before && after, `${label}: 行未找到`);
      for (const key of ["row", "name", "next"]) {
        const b = before[key];
        const a = after[key];
        if (!b || !a) continue;
        for (const m of ["top", "bottom", "left", "right", "width", "height"]) {
          assert.ok(
            Math.abs(a[m] - b[m]) <= 0.5,
            `${label}: ${key}.${m} 在操作按钮显示前后移动了 ${b[m]} -> ${a[m]}`,
          );
        }
      }
    };
    const scrollRowIntoView = async (scope, name) => {
      await page.evaluate(`(() => {
        const root = document.querySelector(${JSON.stringify(scope)});
        const rows = [...root.querySelectorAll('[role="treeitem"]')];
        const row = rows.find((r) => r.textContent?.includes(${JSON.stringify(name)})) ?? rows[0];
        row.scrollIntoView({ block: "center" });
      })()`);
      await sleep(150);
    };
    const hoverRow = async (scope, name) => {
      const point = await page.evaluate(`(() => {
        const root = document.querySelector(${JSON.stringify(scope)});
        const rows = [...root.querySelectorAll('[role="treeitem"]')];
        const row = rows.find((r) => r.textContent?.includes(${JSON.stringify(name)})) ?? rows[0];
        const r = row.getBoundingClientRect();
        return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
      })()`);
      await page.send("Input.dispatchMouseEvent", { type: "mouseMoved", x: point.x, y: point.y });
      await sleep(200);
    };
    const unhover = async () => {
      await page.send("Input.dispatchMouseEvent", { type: "mouseMoved", x: 2, y: 2 });
      await sleep(200);
    };
    const focusRow = async (scope, name) => {
      await page.evaluate(`(() => {
        const root = document.querySelector(${JSON.stringify(scope)});
        const rows = [...root.querySelectorAll('[role="treeitem"]')];
        const row = rows.find((r) => r.textContent?.includes(${JSON.stringify(name)})) ?? rows[0];
        row.focus();
      })()`);
      await sleep(150);
    };

    for (const spec of [
      { width: 320, height: 568 },
      { width: 390, height: 844 },
      { width: 1280, height: 800 },
    ]) {
      const label = `${spec.width}`;
      await boot(page, { width: spec.width, height: spec.height, touch: false, theme: "dark" });
      await openDock(page, "文件树");
      await page.waitFor(`Boolean(document.querySelector('${treeScope} [role="treeitem"]'))`);
      await scrollRowIntoView(treeScope, "console.log");

      const treeBefore = await page.evaluate(geometryExpr(treeScope, "console.log"));
      assert.equal(treeBefore.stripVisibility, "hidden", `FileTree@${label}: 静止状态操作条应隐藏（仅占位）`);
      await hoverRow(treeScope, "console.log");
      const treeHover = await page.evaluate(geometryExpr(treeScope, "console.log"));
      assertStable(treeBefore, treeHover, `FileTree@${label} hover`);
      assert.equal(treeHover.stripVisibility, "visible", `FileTree@${label}: hover 必须显示操作按钮，不允许靠隐藏规避`);
      assert.ok(treeHover.del && treeHover.del.height >= 20, `FileTree@${label}: 删除按钮必须有真实点击区域`);
      const rowCenter = (treeHover.row.top + treeHover.row.bottom) / 2;
      const delCenter = (treeHover.del.top + treeHover.del.bottom) / 2;
      assert.ok(
        Math.abs(rowCenter - delCenter) <= 1,
        `FileTree@${label}: 删除按钮与文本应垂直居中对齐 row=${rowCenter} del=${delCenter}`,
      );
      await screenshot(page, `files-hover-geometry-tree-${label}.png`);
      await unhover();
      const treeAfter = await page.evaluate(geometryExpr(treeScope, "console.log"));
      assertStable(treeBefore, treeAfter, `FileTree@${label} unhover`);
      assert.equal(treeAfter.stripVisibility, "hidden", `FileTree@${label}: 移开后操作条应收起`);

      await focusRow(treeScope, "console.log");
      const treeFocus = await page.evaluate(geometryExpr(treeScope, "console.log"));
      assertStable(treeBefore, treeFocus, `FileTree@${label} focus`);
      assert.equal(treeFocus.stripVisibility, "visible", `FileTree@${label}: focus-within 必须显示操作按钮`);
      await page.evaluate(`document.activeElement && document.activeElement.blur()`);

      if (spec.width <= 820) {
        await page.evaluate(`document.querySelector('.nx-dock-backdrop')?.click()`);
        await page.waitFor(`!document.querySelector('.nx-dock-backdrop')`);
      }
      await openFileBrowserTab(page);
      await page.waitFor(`[...document.querySelectorAll('${browserScope} [role="treeitem"]')].some((r) => r.textContent?.includes("console.log"))`);
      await scrollRowIntoView(browserScope, "console.log");

      const browserBefore = await page.evaluate(geometryExpr(browserScope, "console.log"));
      assert.equal(browserBefore.stripVisibility, "hidden", `FileBrowser@${label}: 静止状态操作条应隐藏（仅占位）`);
      await hoverRow(browserScope, "console.log");
      const browserHover = await page.evaluate(geometryExpr(browserScope, "console.log"));
      assertStable(browserBefore, browserHover, `FileBrowser@${label} hover`);
      await focusRow(browserScope, "console.log");
      const browserFocus = await page.evaluate(geometryExpr(browserScope, "console.log"));
      assertStable(browserBefore, browserFocus, `FileBrowser@${label} focus`);
      assert.equal(browserFocus.stripVisibility, "visible", `FileBrowser@${label}: focus-within 必须显示操作按钮`);
      assert.ok(browserFocus.name && browserFocus.name.width > 60, `FileBrowser@${label}: 名称列必须保留真实宽度`);
      await screenshot(page, `files-hover-geometry-browser-${label}.png`);
      await page.evaluate(`document.activeElement && document.activeElement.blur()`);
      await unhover();

      if (spec.width <= 390) {
        evidence[label] = { page: await noPageOverflow(page, `files-geometry@${label}`, spec.width) };
      }
      evidence[label] = {
        ...(evidence[label] ?? {}),
        tree: { before: treeBefore, hover: treeHover, focus: treeFocus },
        browser: { before: browserBefore, focus: browserFocus },
      };
    }
    return { evidence };
  });

  await pass("fileeditor-320-save-reachable-and-saves", async () => {
    await boot(page, { width: 320, height: 568, touch: true, theme: "dark" });
    await openFileBrowserTab(page);
    await page.waitFor(`[...document.querySelectorAll('[role="tabpanel"]:not(.hidden) .font-mono')].some((el) => el.textContent?.includes('console.log'))`);
    await page.evaluate(`(() => {
      const panel = document.querySelector('[role="tabpanel"]:not(.hidden)');
      const row = [...panel.querySelectorAll('.cursor-pointer')].find((r) => r.textContent?.includes("console.log"));
      row.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    })()`);
    await page.waitFor(`Boolean(document.querySelector('.cm-content'))`, 15_000);
    await page.evaluate(`document.querySelector('.cm-content').focus()`);
    await page.send("Input.insertText", { text: "# touch edit\n" });
    await page.waitFor(`[...document.querySelectorAll('.nx-badge')].some((b) => b.textContent?.includes("未保存"))`);

    const save = await rectOf(page, '[role="tabpanel"]:not(.hidden) .nx-toolbar button.nx-btn-primary');
    assert.ok(save && save.width > 0 && save.left >= 0 && save.right <= 320, `保存按钮必须首屏可见: ${JSON.stringify(save)}`);
    await tapSelector(page, '[role="tabpanel"]:not(.hidden) .nx-toolbar button.nx-btn-primary');
    await page.waitFor(`document.body.textContent.includes("已保存")`, 10_000);
    const stillDirty = await page.evaluate(`[...document.querySelectorAll('.nx-badge')].some((b) => b.textContent?.includes("未保存"))`);
    assert.equal(stillDirty, false, "保存后未保存徽标必须消失");
    await noPageOverflow(page, "FileEditor@320", 320);
    await screenshot(page, "fileeditor-320-save.png");
    return { evidence: { save } };
  });

  await pass("mount-320-no-page-overflow-create-reachable", async () => {
    await boot(page, { width: 320, height: 568, touch: true, theme: "dark" });
    await openDock(page, "磁盘挂载");
    await page.waitFor(`[...document.querySelectorAll('button')].some((b) => b.textContent?.trim() === "断开")`);
    await noPageOverflow(page, "MountPanel@320", 320);

    const disconnect = await page.evaluate(`(() => {
      const panel = document.querySelector('[role="tabpanel"]:not(.hidden)');
      const btn = [...panel.querySelectorAll('button')].find((b) => b.textContent?.trim() === "断开");
      const r = btn.getBoundingClientRect();
      return { left: r.left, right: r.right, width: r.width };
    })()`);
    assert.ok(disconnect.width > 0 && disconnect.left >= 0 && disconnect.right <= 320, `断开按钮必须屏内可达: ${JSON.stringify(disconnect)}`);

    await dismissToasts(page);
    await tapSelector(page, '[role="tabpanel"]:not(.hidden) input[aria-label="远端路径"]');
    await page.send("Input.insertText", { text: "user@host:/data" });
    const createBtn = await page.evaluate(`(() => {
      const panel = document.querySelector('[role="tabpanel"]:not(.hidden)');
      const btn = [...panel.querySelectorAll('button')].find((b) => b.textContent?.trim() === "挂载");
      const r = btn.getBoundingClientRect();
      return { left: r.left, right: r.right, top: r.top, bottom: r.bottom, width: r.width };
    })()`);
    assert.ok(createBtn.width > 0 && createBtn.right <= 320 && createBtn.bottom <= 568, `挂载按钮必须可达: ${JSON.stringify(createBtn)}`);
    await dismissToasts(page);
    await tapSelector(page, '[role="tabpanel"]:not(.hidden) .nx-btn-primary');
    await page.waitFor(`document.body.textContent.includes("已挂载")`, 10_000);
    await screenshot(page, "mount-320.png");
    return { evidence: { disconnect, createBtn } };
  });

  await pass("credentials-overlay-close-and-headers-fit-320-360-390", async () => {
    const evidence = {};
    for (const spec of [
      { width: 320, height: 568, theme: "dark" },
      { width: 360, height: 740, theme: "light" },
      { width: 390, height: 844, theme: "dark" },
    ]) {
      const label = `${spec.width}-${spec.theme}`;
      await boot(page, { width: spec.width, height: spec.height, touch: true, theme: spec.theme });
      await openDock(page, "凭据");
      await page.waitFor(`Boolean(document.querySelector('.nx-dock-backdrop'))`);
      await page.waitFor(`[...document.querySelectorAll('.nx-row')].some((r) => r.textContent?.includes("db-prod"))`);
      await tapSelector(page, ".nx-row");
      await page.waitFor(`!document.querySelector('.nx-dock-backdrop')`);
      await page.waitFor(`document.body.textContent.includes("凭据名称") || document.body.textContent.includes("新建凭据")`);

      const panelHeader = await page.evaluate(`(() => {
        const panel = document.querySelector('[role="tabpanel"]:not(.hidden)');
        const btn = [...panel.querySelectorAll('button')].find((b) => b.textContent?.trim() === "新建凭据");
        if (!btn) return null;
        const r = btn.getBoundingClientRect();
        return { left: r.left, right: r.right, width: r.width };
      })()`);
      assert.ok(
        panelHeader && panelHeader.width > 0 && panelHeader.left >= 0 && panelHeader.right <= spec.width,
        `新建凭据按钮必须屏内可达@${label}: ${JSON.stringify(panelHeader)}`,
      );
      evidence[label] = { panelHeader, panel: await noPageOverflow(page, `CredentialsPanel@${label}`, spec.width) };

      await openDock(page, "凭据");
      await page.waitFor(`Boolean(document.querySelector('.nx-dock-backdrop'))`);
      await dismissToasts(page);
      await tapSelector(page, ".nx-segment .nx-segment-item");
      await page.waitFor(`!document.querySelector('.nx-dock-backdrop')`);
      const viewPanelSelector = '[role="tabpanel"]:not(.hidden)[id$="tab-credentials-view"]';
      await page.waitFor(`Boolean(document.querySelector('${viewPanelSelector}'))`);
      const viewHeader = await page.evaluate(`(() => {
        const panel = document.querySelector('${viewPanelSelector}');
        if (!panel) return null;
        const read = (el) => {
          if (!el) return null;
          const r = el.getBoundingClientRect();
          return { left: r.left, right: r.right, width: r.width };
        };
        return {
          copy: read([...panel.querySelectorAll('button')].find((b) => b.textContent?.trim() === "复制")),
          refresh: read(panel.querySelector('button[title="重新读取"]')),
          textSeg: read([...panel.querySelectorAll('.nx-segment-item')].find((b) => b.textContent?.trim() === "文本")),
          jsonSeg: read([...panel.querySelectorAll('.nx-segment-item')].find((b) => b.textContent?.trim() === "JSON")),
        };
      })()`);
      for (const [name, rect] of Object.entries(viewHeader ?? {})) {
        assert.ok(
          rect && rect.width > 0 && rect.left >= 0 && rect.right <= spec.width,
          `凭据视图 ${name} 必须屏内可达@${label}: ${JSON.stringify(viewHeader)}`,
        );
      }
      evidence[label].viewHeader = viewHeader;
      evidence[label].view = await noPageOverflow(page, `CredentialsView@${label}`, spec.width);

      await dismissToasts(page);
      await tapButtonText(page, "复制", viewPanelSelector);
      await page.waitFor(`document.body.textContent.includes("已复制") || document.body.textContent.includes("复制失败")`, 10_000);
      await screenshot(page, `credentials-${label}-view.png`);
    }
    return { evidence };
  });

  await pass("new-credential-320-footer-visible-and-creates", async () => {
    await boot(page, { width: 320, height: 568, touch: true, theme: "dark" });
    await openDock(page, "凭据");
    await page.waitFor(`[...document.querySelectorAll('.nx-row')].some((r) => r.textContent?.includes("db-prod"))`);
    await tapSelector(page, ".nx-row");
    await page.waitFor(`!document.querySelector('.nx-dock-backdrop')`);
    await tapButtonText(page, "新建凭据");
    await page.waitFor(`Boolean(document.querySelector('.nx-modal[role="dialog"]'))`);

    await tapButtonText(page, "私钥", ".nx-modal");
    await sleep(150);
    await tapButtonText(page, "存入凭据库", ".nx-modal");
    await sleep(150);
    await tapButtonText(page, "粘贴内容", ".nx-modal");
    await sleep(150);

    const footer = await page.evaluate(`(() => {
      const footer = document.querySelector('.nx-modal-footer');
      const btn = [...footer.querySelectorAll('button')].find((b) => b.textContent?.trim() === "保存");
      const r = btn.getBoundingClientRect();
      const modal = document.querySelector('.nx-modal').getBoundingClientRect();
      return { top: r.top, bottom: r.bottom, modalBottom: modal.bottom, innerHeight: window.innerHeight };
    })()`);
    assert.ok(footer.bottom <= footer.innerHeight, `保存按钮必须常驻可视区: ${JSON.stringify(footer)}`);

    await tapSelector(page, ".nx-modal input");
    await page.send("Input.insertText", { text: "touch-key" });
    await tapSelector(page, ".nx-modal textarea");
    await page.send("Input.insertText", { text: "fake-key-material" });
    await tapSelector(page, ".nx-modal-footer .nx-btn-primary");
    await page.waitFor(`document.body.textContent.includes("已创建凭据")`, 10_000);
    await screenshot(page, "new-credential-320.png");
    return { evidence: { footer } };
  });

  await pass("matrix-360-390-landscape-no-page-overflow", async () => {
    const evidence = {};
    for (const spec of [
      { width: 360, height: 740, theme: "light" },
      { width: 390, height: 844, theme: "dark" },
      { width: 568, height: 320, theme: "light" },
    ]) {
      const label = `${spec.width}x${spec.height}-${spec.theme}`;
      await boot(page, { width: spec.width, height: spec.height, touch: true, theme: spec.theme });
      await openFileBrowserTab(page);
      await page.waitFor(`[...document.querySelectorAll('[role="tabpanel"]:not(.hidden) .font-mono')].some((el) => el.textContent?.includes('projects'))`);
      const nameWidth = await page.evaluate(`(() => {
        const panel = document.querySelector('[role="tabpanel"]:not(.hidden)');
        const el = [...panel.querySelectorAll('.font-mono')].find((n) => n.textContent?.includes('projects'));
        return el?.getBoundingClientRect().width ?? 0;
      })()`);
      assert.ok(nameWidth > 60, `FileBrowser 名称列必须 >60px@${label}: ${nameWidth}`);
      evidence[label] = { nameWidth, fileBrowser: await noPageOverflow(page, `FileBrowser@${label}`, spec.width) };
      await openDock(page, "磁盘挂载");
      await page.waitFor(`[...document.querySelectorAll('button')].some((b) => b.textContent?.trim() === "断开") || document.body.textContent.includes("本机当前没有挂载点")`);
      evidence[label].mount = await noPageOverflow(page, `MountPanel@${label}`, spec.width);
      await screenshot(page, `matrix-${label}.png`);
    }
    return { evidence };
  });
}

let vite;
let chrome;
let page;
try {
  [vite, chrome] = await Promise.all([startVite({ root: ROOT, port: VITE_PORT }), startChrome()]);
  page = await newPage(chrome);
  await filesResponsiveAcceptance(page);
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
    matrix: "320x568 / 360x740 / 390x844 / 568x320 touch emulation + 320/390/1280 fine-pointer hover/focus geometry, dark+light",
  },
  checks,
  harness_errors: harnessErrors,
};
fs.writeFileSync(path.join(OUT, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
console.warn(`files-responsive acceptance: ${checks.filter((check) => check.status === "passed").length}/${checks.length} checks passed; report=${path.join(OUT, "report.json")}`);
if (failed.length || harnessErrors.length) process.exit(1);
