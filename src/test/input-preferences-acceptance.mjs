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
const OUT = path.join(ROOT, "target/acceptance-input-preferences");
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

const MOD_CTRL = 2;
const MOD_SHIFT = 8;

async function press(page, name) {
  const KEYS = {
    Escape: { key: "Escape", code: "Escape", vk: 27 },
    Enter: { key: "Enter", code: "Enter", vk: 13 },
  };
  const def = KEYS[name];
  if (!def) throw new Error(`unknown key: ${name}`);
  const base = { key: def.key, code: def.code, windowsVirtualKeyCode: def.vk };
  if (name === "Enter") {
    await page.send("Input.dispatchKeyEvent", { type: "keyDown", ...base, text: "\r" });
  } else {
    await page.send("Input.dispatchKeyEvent", { type: "rawKeyDown", ...base });
  }
  await page.send("Input.dispatchKeyEvent", { type: "keyUp", ...base });
  await sleep(40);
}

async function pressCombo(page, key, code, vk, modifiers) {
  const base = { key, code, windowsVirtualKeyCode: vk, modifiers };
  await page.send("Input.dispatchKeyEvent", { type: "rawKeyDown", ...base });
  await page.send("Input.dispatchKeyEvent", { type: "keyUp", ...base });
  await sleep(60);
}

const pressCtrl = (page, letter, vk) =>
  pressCombo(page, letter, `Key${letter.toUpperCase()}`, vk, MOD_CTRL);
const pressCtrlShift = (page, letter, vk) =>
  pressCombo(page, letter.toUpperCase(), `Key${letter.toUpperCase()}`, vk, MOD_CTRL | MOD_SHIFT);
const pressCtrlDigit = (page, digit) =>
  pressCombo(page, String(digit), `Digit${digit}`, 48 + digit, MOD_CTRL);

async function clickSelector(page, selector) {
  const point = await page.evaluate(`(() => {
    const el = document.querySelector(${JSON.stringify(selector)});
    if (!el) return null;
    el.scrollIntoView({ block: "center" });
    const rect = el.getBoundingClientRect();
    return { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 };
  })()`);
  if (!point) throw new Error(`selector not found: ${selector}`);
  for (const type of ["mousePressed", "mouseReleased"]) {
    await page.send("Input.dispatchMouseEvent", { type, x: point.x, y: point.y, button: "left", clickCount: 1 });
  }
  await sleep(80);
}

async function clickButtonText(page, text) {
  const point = await page.evaluate(`(() => {
    const el = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === ${JSON.stringify(text)});
    if (!el) return null;
    el.scrollIntoView({ block: "center" });
    const rect = el.getBoundingClientRect();
    return { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 };
  })()`);
  if (!point) throw new Error(`button not found: ${text}`);
  for (const type of ["mousePressed", "mouseReleased"]) {
    await page.send("Input.dispatchMouseEvent", { type, x: point.x, y: point.y, button: "left", clickCount: 1 });
  }
  await sleep(80);
}

async function dragSelect(page, from, to) {
  const rect = await page.evaluate(`(() => {
    const el = document.querySelector('.xterm');
    if (!el) return null;
    const r = el.getBoundingClientRect();
    return { left: r.left, top: r.top, width: r.width, height: r.height };
  })()`);
  if (!rect) throw new Error(".xterm not found for drag");
  const x1 = rect.left + rect.width * from[0];
  const y1 = rect.top + rect.height * from[1];
  const x2 = rect.left + rect.width * to[0];
  const y2 = rect.top + rect.height * to[1];
  await page.send("Input.dispatchMouseEvent", { type: "mousePressed", x: x1, y: y1, button: "left", clickCount: 1 });
  const steps = 6;
  for (let i = 1; i <= steps; i++) {
    await page.send("Input.dispatchMouseEvent", {
      type: "mouseMoved",
      x: x1 + ((x2 - x1) * i) / steps,
      y: y1 + ((y2 - y1) * i) / steps,
      button: "left",
    });
    await sleep(30);
  }
  await page.send("Input.dispatchMouseEvent", { type: "mouseReleased", x: x2, y: y2, button: "left", clickCount: 1 });
  await sleep(80);
}

const CLIPBOARD_STUB = `
  window.__copies = [];
  window.__copyReject = false;
  const __writeText = (text) => {
    window.__copies.push(String(text));
    if (window.__copyReject) return Promise.reject(new DOMException("Write permission denied", "NotAllowedError"));
    return Promise.resolve();
  };
  const __clipboard = { writeText: __writeText, readText: () => Promise.resolve("") };
  try {
    Object.defineProperty(navigator, "clipboard", { value: __clipboard, configurable: true });
  } catch {
    try { Object.defineProperty(Navigator.prototype, "clipboard", { value: __clipboard, configurable: true }); } catch {}
  }
`;

async function boot(page, { clearStorage = true } = {}) {
  const source = [
    clearStorage ? "try { localStorage.clear(); } catch {}" : "",
    'window.__NEXTERM_TRANSPORT__ = "web";',
    CLIPBOARD_STUB,
  ].join("\n");
  const { identifier } = await page.send("Page.addScriptToEvaluateOnNewDocument", { source });
  try {
    await page.navigate(`${VITE}/?demo=1`);
    await page.waitFor(
      "Boolean(document.querySelector('[role=\"tablist\"][aria-label=\"工作区\"] [role=\"tab\"]') && document.querySelector('.xterm'))",
    );
    await page.waitFor("Array.isArray(window.__copies)");
  } finally {
    await page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier });
  }
}

async function openSettingsViaPalette(page) {
  await pressCtrl(page, "k", 75);
  await page.waitFor(`Boolean(document.querySelector('[role="dialog"] [role="combobox"]'))`);
  await page.send("Input.insertText", { text: "设置" });
  await page.waitFor(
    `[...document.querySelectorAll('[role="option"],[role="listbox"] *')].some((el) => el.textContent?.includes('设置'))`,
  );
  await press(page, "Enter");
  await page.waitFor(
    `[...document.querySelectorAll('[role="tablist"][aria-label="标签页"] [role="tab"]')].some((t) => t.getAttribute('aria-selected') === 'true' && t.offsetParent !== null && t.textContent?.includes('设置'))`,
  );
  await page.waitFor(`Boolean(document.querySelector('[data-shortcut-row]'))`);
}

async function openViaPalette(page, text, tabText) {
  await pressCtrl(page, "k", 75);
  await page.waitFor(`Boolean(document.querySelector('[role="dialog"] [role="combobox"]'))`);
  await page.send("Input.insertText", { text });
  await page.waitFor(
    `[...document.querySelectorAll('[role="option"],[role="listbox"] *')].some((el) => el.textContent?.includes(${JSON.stringify(tabText)}))`,
  );
  await press(page, "Enter");
  await page.waitFor(
    `[...document.querySelectorAll('[role="tablist"][aria-label="标签页"] [role="tab"]')].some((t) => t.getAttribute('aria-selected') === 'true' && t.offsetParent !== null && t.textContent?.includes(${JSON.stringify(tabText)}))`,
  );
}

const selectedPaneTab = `(() => {
  const tabs = [...document.querySelectorAll('[role="tablist"][aria-label="标签页"] [role="tab"]')]
    .filter((t) => t.offsetParent !== null);
  const selected = tabs.find((t) => t.getAttribute('aria-selected') === 'true');
  return selected?.textContent ?? null;
})()`;

const rowKbd = (id) => `(() => {
  const row = document.querySelector('[data-shortcut-row="${id}"]');
  return row?.querySelector('.nx-kbd')?.textContent ?? null;
})()`;

const toastText = `(() => {
  const el = document.querySelector('.nx-toasts');
  return el?.textContent ?? '';
})()`;

async function screenshot(page, name) {
  const shot = await page.send("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(path.join(OUT, name), Buffer.from(shot.data, "base64"));
  return name;
}

async function inputPreferenceAcceptance(page) {
  await pass("editor-default-rows", async () => {
    await boot(page);
    await openSettingsViaPalette(page);
    const evidence = await page.evaluate(`(() => {
      const rows = [...document.querySelectorAll('[data-shortcut-row]')];
      const labels = rows.map((r) => r.querySelector('.nx-kbd')?.textContent ?? null);
      const autoCopy = document.querySelector('input[aria-label="选中自动复制"]');
      const conflictBadges = document.querySelectorAll('[data-shortcut-row] .nx-badge-amber').length;
      return { rowCount: rows.length, labels, autoCopyChecked: autoCopy?.checked ?? null, conflictBadges };
    })()`);
    const mod = evidence.labels[0]?.startsWith("⌘") ? "⌘" : "Ctrl";
    assert.equal(evidence.rowCount, 11, JSON.stringify(evidence));
    assert.ok(evidence.labels.includes(`${mod}+Shift+P`), JSON.stringify(evidence.labels));
    assert.ok(evidence.labels.includes(`${mod}+1…9`), JSON.stringify(evidence.labels));
    assert.ok(evidence.labels.includes(`${mod}+F`), JSON.stringify(evidence.labels));
    assert.equal(evidence.autoCopyChecked, false, "selection auto-copy must default to off");
    assert.equal(evidence.conflictBadges, 0, "defaults must not conflict");
    return { evidence: { ...evidence, mod } };
  });

  await pass("rebind-capture-persist-reload-reset", async () => {
    await boot(page);
    await openSettingsViaPalette(page);

    const initial = await page.evaluate(rowKbd("commandPalette"));
    const mod = initial?.startsWith("⌘") ? "⌘" : "Ctrl";
    await clickSelector(page, '[data-shortcut-row="commandPalette"] button[aria-label="修改快捷键：命令面板"]');
    await page.waitFor(`Boolean(document.querySelector('input[aria-label="捕获快捷键：命令面板"]'))`);
    await pressCtrlShift(page, "o", 79);
    await page.waitFor(`document.querySelector('[data-shortcut-row="commandPalette"] .nx-kbd')?.textContent === ${JSON.stringify(`${mod}+Shift+O`)}`);

    await page.send("Page.navigate", { url: `${VITE}/?demo=1` });
    await page.waitFor("document.readyState === 'complete'");
    await page.waitFor(
      "Boolean(document.querySelector('[role=\"tablist\"][aria-label=\"工作区\"] [role=\"tab\"]') && document.querySelector('.xterm'))",
    );
    await openSettingsViaPalette(page);
    const persisted = await page.evaluate(rowKbd("commandPalette"));
    assert.equal(persisted, `${mod}+Shift+O`, "rebound shortcut must persist across reloads");

    await pressCtrlShift(page, "o", 79);
    await page.waitFor(`Boolean(document.querySelector('[role="dialog"] [role="combobox"]'))`);
    await press(page, "Escape");
    await page.waitFor(`!document.querySelector('[role="dialog"] [role="combobox"]')`);

    await pressCtrlShift(page, "p", 80);
    await sleep(200);
    const oldOpened = await page.evaluate(`Boolean(document.querySelector('[role="dialog"] [role="combobox"]'))`);
    assert.equal(oldOpened, false, "old binding must no longer open the palette");

    await clickSelector(page, '[data-shortcut-row="commandPalette"] button[aria-label="恢复默认快捷键：命令面板"]');
    await page.waitFor(`document.querySelector('[data-shortcut-row="commandPalette"] .nx-kbd')?.textContent === ${JSON.stringify(`${mod}+Shift+P`)}`);
    await pressCtrlShift(page, "p", 80);
    await page.waitFor(`Boolean(document.querySelector('[role="dialog"] [role="combobox"]'))`);
    await press(page, "Escape");
    return { evidence: { mod, persisted } };
  });

  await pass("tab-navigation-ctrl-digits", async () => {
    await boot(page);
    await openViaPalette(page, "设置", "设置");
    await openViaPalette(page, "审计", "审计日志");
    const tabs = await page.evaluate(
      `[...document.querySelectorAll('[role="tablist"][aria-label="标签页"] [role="tab"]')].filter((t) => t.offsetParent !== null).map((t) => t.textContent)`,
    );
    assert.ok(tabs.length >= 3, `expected at least 3 pane tabs, got ${JSON.stringify(tabs)}`);

    await pressCtrlDigit(page, 2);
    await page.waitFor(`(${selectedPaneTab})?.includes('设置')`);
    await pressCtrlDigit(page, 3);
    await page.waitFor(`(${selectedPaneTab})?.includes('审计日志')`);
    await pressCtrlDigit(page, 1);
    await page.waitFor(`!(${selectedPaneTab})?.includes('设置')`);

    const afterFirst = await page.evaluate(selectedPaneTab);
    await pressCtrlDigit(page, 9);
    await sleep(150);
    const afterNinth = await page.evaluate(selectedPaneTab);
    assert.equal(afterNinth, afterFirst, "digit beyond tab count must not switch");
    return { evidence: { tabs, afterFirst } };
  });

  await pass("palette-hints-follow-bindings", async () => {
    await boot(page);
    await openSettingsViaPalette(page);

    await clickSelector(page, '[data-shortcut-row="quickConnect"] button[aria-label="修改快捷键：快速连接"]');
    await page.waitFor(`Boolean(document.querySelector('input[aria-label="捕获快捷键：快速连接"]'))`);
    await pressCombo(page, "q", "KeyQ", 81, 1);
    await page.waitFor(`document.querySelector('[data-shortcut-row="quickConnect"] .nx-kbd')?.textContent === 'Alt+Q'`);

    await clickSelector(page, '[data-shortcut-row="toggleSplit"] button[aria-label="修改快捷键：上下分屏 / 取消分屏"]');
    await page.waitFor(`Boolean(document.querySelector('input[aria-label="捕获快捷键：上下分屏 / 取消分屏"]'))`);
    await pressCombo(page, "Backspace", "Backspace", 8, 0);
    await page.waitFor(`document.querySelector('[data-shortcut-row="toggleSplit"] .nx-kbd')?.textContent === '未绑定'`);

    await pressCtrl(page, "k", 75);
    await page.waitFor(`Boolean(document.querySelector('[role="dialog"] [role="combobox"]'))`);
    const evidence = await page.evaluate(`(() => {
      const options = [...document.querySelectorAll('[role="option"]')];
      const hintOf = (label) => {
        const option = options.find((o) => o.textContent?.includes(label));
        return option?.querySelector('.nx-command-hint')?.textContent ?? null;
      };
      return {
        quickConnect: hintOf('快速连接'),
        split: hintOf('上下分屏'),
        localTerminal: hintOf('打开本地终端'),
      };
    })()`);
    assert.equal(evidence.quickConnect, "Alt+Q · 最近使用优先", JSON.stringify(evidence));
    assert.equal(evidence.split, null, `unbound split hint must disappear: ${JSON.stringify(evidence)}`);
    await press(page, "Escape");
    await page.waitFor(`!document.querySelector('[role="dialog"] [role="combobox"]')`);

    await clickButtonText(page, "全部恢复默认");
    await page.waitFor(`[...document.querySelectorAll('[data-shortcut-row] .nx-kbd')].some((k) => k.textContent === 'Ctrl+Shift+K' || k.textContent === '⌘+Shift+K')`);
    return { evidence };
  });

  await pass("auto-copy-disabled-by-default", async () => {
    await boot(page);
    await sleep(600);
    await dragSelect(page, [0.6, 0.35], [0.1, 0.03]);
    await sleep(800);
    const evidence = await page.evaluate(`(() => ({
      copies: window.__copies.length,
      toast: ${toastText},
    }))()`);
    assert.equal(evidence.copies, 0, "auto-copy must not write while disabled");
    assert.ok(!evidence.toast.includes("自动复制"), `unexpected toast: ${evidence.toast}`);
    return { evidence };
  });

  await pass("auto-copy-enabled-success-toast", async () => {
    await boot(page);
    await openSettingsViaPalette(page);
    await clickSelector(page, 'input[aria-label="选中自动复制"]');
    await page.waitFor(`document.querySelector('input[aria-label="选中自动复制"]').checked === true`);

    await pressCtrlDigit(page, 1);
    await page.waitFor(`document.querySelector('.xterm') !== null`);
    await sleep(600);
    await dragSelect(page, [0.6, 0.35], [0.1, 0.03]);
    await page.waitFor(`window.__copies.length >= 1`, 10_000);
    await page.waitFor(`(${toastText}).includes('已自动复制')`, 10_000);
    const evidence = await page.evaluate(`(() => ({
      copies: window.__copies.slice(),
      toast: ${toastText},
    }))()`);
    assert.ok(evidence.copies.every((text) => text.length > 0), JSON.stringify(evidence.copies));
    return { evidence };
  });

  await pass("auto-copy-failure-reports-permission-honestly", async () => {
    await page.evaluate(`(() => { window.__copyReject = true; window.__copies.length = 0; })()`);
    await dragSelect(page, [0.5, 0.3], [0.05, 0.05]);
    await page.waitFor(`(${toastText}).includes('剪贴板权限被拒绝')`, 10_000);
    const evidence = await page.evaluate(`(() => ({
      toast: ${toastText},
    }))()`);
    assert.ok(evidence.toast.includes("自动复制选中内容失败"), evidence.toast);
    return { evidence };
  });

  await pass("responsive-390-editor-usable", async () => {
    await boot(page);
    await page.send("Emulation.setDeviceMetricsOverride", {
      width: 390,
      height: 760,
      deviceScaleFactor: 2,
      mobile: true,
    });
    await openSettingsViaPalette(page);
    const evidence = await page.evaluate(`(() => {
      const grid = [...document.querySelectorAll("div")].find(
        (d) => d.className.includes("grid-cols") && d.querySelector(".nx-kbd"),
      );
      const cols = grid ? getComputedStyle(grid).gridTemplateColumns : null;
      const editButtons = [...document.querySelectorAll('[data-shortcut-row] button[aria-label^="修改快捷键"]')];
      const reachable = editButtons.every((b) => {
        const r = b.getBoundingClientRect();
        return r.width > 0 && r.right <= window.innerWidth + 1 && r.left >= -1;
      });
      return {
        cols,
        tracks: cols ? cols.split(" ").length : 0,
        docScrollWidth: document.documentElement.scrollWidth,
        innerWidth: window.innerWidth,
        reachable,
        editButtonCount: editButtons.length,
      };
    })()`);
    assert.equal(evidence.tracks, 1, `390px must be single column: ${JSON.stringify(evidence)}`);
    assert.ok(evidence.docScrollWidth <= evidence.innerWidth, JSON.stringify(evidence));
    assert.equal(evidence.editButtonCount, 11);
    assert.equal(evidence.reachable, true, JSON.stringify(evidence));
    const shot = await screenshot(page, "responsive-390-settings.png");
    return { evidence: { ...evidence, shot } };
  });
}

let vite;
let chrome;
let page;
try {
  [vite, chrome] = await Promise.all([startVite({ root: ROOT, port: VITE_PORT }), startChrome()]);
  page = await newPage(chrome);
  await inputPreferenceAcceptance(page);
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
    note: "交互仅通过 CDP Input.dispatchKeyEvent / Input.dispatchMouseEvent / Input.insertText；剪贴板为注入记录桩，断言读取 DOM 与桩数据",
  },
  checks,
  harness_errors: harnessErrors,
};
fs.writeFileSync(path.join(OUT, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
console.warn(`input preferences acceptance: ${checks.filter((check) => check.status === "passed").length}/${checks.length} checks passed; report=${path.join(OUT, "report.json")}`);
if (failed.length || harnessErrors.length) process.exit(1);
