#!/usr/bin/env node
// 文件命令 UX 真实浏览器验收（M138）：上传覆盖确认（取消/确认）、命令面板片段插入、
// 资产克隆、资产隐藏/取消隐藏。交互仅通过 CDP Input.* 完成，断言读取 DOM/ARIA 与
// 注入的 TextEncoder 输入日志。运行：node src/test/file-command-ux-acceptance.mjs
import { spawn, spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { startVite } from "./lib/acceptance-process.mjs";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const OUT = path.join(ROOT, "target/acceptance-file-command-ux");
const VITE_PORT = Number(process.env.NEXTERM_VITE_PORT || 1429);
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

const MODIFIER = { ctrl: 2, meta: 4 };

async function pressKey(page, { key, code, vk, modifiers = 0, text }) {
  const base = { key, code, windowsVirtualKeyCode: vk, modifiers };
  if (text !== undefined) {
    await page.send("Input.dispatchKeyEvent", { type: "keyDown", ...base, text });
  } else {
    await page.send("Input.dispatchKeyEvent", { type: "rawKeyDown", ...base });
  }
  await page.send("Input.dispatchKeyEvent", { type: "keyUp", ...base });
  await sleep(60);
}

async function clickSelector(page, selector) {
  const point = await page.evaluate(`(() => {
    const el = document.querySelector(${JSON.stringify(selector)});
    if (!el) return null;
    const rect = el.getBoundingClientRect();
    return { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 };
  })()`);
  if (!point) throw new Error(`selector not found: ${selector}`);
  for (const type of ["mousePressed", "mouseReleased"]) {
    await page.send("Input.dispatchMouseEvent", { type, x: point.x, y: point.y, button: "left", clickCount: 1 });
  }
  await sleep(120);
}

async function openAssetMoreMenu(page, name) {
  const row = `[role="treeitem"][title^="${name}"]`;
  await clickSelector(page, row);
  await page.waitFor(`(() => {
    const b = document.querySelector('button[aria-label="更多操作 ${name}"]');
    return Boolean(b) && getComputedStyle(b.parentElement).display !== "none";
  })()`);
  await clickSelector(page, `button[aria-label="更多操作 ${name}"]`);
}

async function clickMenuItemByText(page, text) {
  const point = await page.evaluate(`(() => {
    const el = [...document.querySelectorAll('[role="menuitem"]')].find((i) => i.textContent?.includes(${JSON.stringify(text)}));
    if (!el) return null;
    const rect = el.getBoundingClientRect();
    return { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 };
  })()`);
  if (!point) throw new Error(`menu item not found: ${text}`);
  for (const type of ["mousePressed", "mouseReleased"]) {
    await page.send("Input.dispatchMouseEvent", { type, x: point.x, y: point.y, button: "left", clickCount: 1 });
  }
  await sleep(120);
}

async function screenshot(page, name) {
  const shot = await page.send("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(path.join(OUT, name), Buffer.from(shot.data, "base64"));
  return name;
}

const ENCODE_LOG = `(() => { window.__nexTermEncoded ??= []; return window.__nexTermEncoded; })()`;
const PALETTE_INPUT = `document.querySelector('[role="dialog"] [role="combobox"]')`;
const WARN_DIALOG = `[role="alertdialog"]`;
const INFO_DIALOG = `.nx-modal[role="dialog"]`;
const TOASTS = `document.querySelector(".nx-toasts")?.textContent ?? ""`;

async function boot(page) {
  const { identifier } = await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `(() => {
      const original = TextEncoder.prototype.encode;
      window.__nexTermEncoded = [];
      TextEncoder.prototype.encode = function (input) {
        const result = original.call(this, input);
        window.__nexTermEncoded.push(typeof input === "string" ? input : "");
        return result;
      };
    })()`,
  });
  try {
    await page.navigate(`${VITE}/?demo=1`);
    await page.waitFor(
      "Boolean(document.querySelector('[role=\"tablist\"][aria-label=\"工作区\"] [role=\"tab\"]') && document.querySelector('.xterm'))",
    );
  } finally {
    await page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier });
  }
}

async function isMacPlatform(page) {
  return page.evaluate(`/Mac/i.test(navigator.platform ?? "") || /Macintosh/.test(navigator.userAgent)`);
}

async function openPalette(page) {
  const mac = await isMacPlatform(page);
  await pressKey(page, { key: "k", code: "KeyK", vk: 75, modifiers: mac ? MODIFIER.meta : MODIFIER.ctrl });
  await page.waitFor(`Boolean(${PALETTE_INPUT})`);
}

async function uploadOverwriteAcceptance(page) {
  await pass("upload-overwrite-cancel-keeps-remote-intact", async () => {
    await boot(page);
    await page.waitFor(`Boolean(document.querySelector('button[title="上传到当前目录"]'))`);
    await page.waitFor(`document.body.textContent?.includes("console.log")`);

    await clickSelector(page, 'button[title="新建文件夹"]');
    await page.waitFor(`Boolean(document.querySelector(".nx-modal input.nx-input"))`);
    await page.send("Input.insertText", { text: "nginx-access.log" });
    await pressKey(page, { key: "Enter", code: "Enter", vk: 13, text: "\r" });
    await page.waitFor(`!document.querySelector(".nx-modal input.nx-input")`);
    await page.waitFor(`document.body.textContent?.includes("nginx-access.log")`);

    await clickSelector(page, 'button[title="上传到当前目录"]');
    await page.waitFor(`Boolean(document.querySelector('${WARN_DIALOG}'))`);
    const dialogText = await page.evaluate(`document.querySelector('${WARN_DIALOG}')?.textContent ?? ""`);
    assert.match(dialogText, /nginx-access\.log/, "confirmation must name the remote path");
    assert.match(dialogText, /覆盖/, "confirmation must say overwrite explicitly");
    assert.match(dialogText, /保持原样/, "confirmation must state cancel keeps the file intact");

    await clickSelector(page, `${WARN_DIALOG} .nx-btn-ghost`);
    await page.waitFor(`(${TOASTS}).includes("已取消上传")`);
    await sleep(2600);
    const started = await page.evaluate(`(${TOASTS}).includes("开始上传")`);
    assert.equal(started, false, "cancel must not start the upload");
    const success = await page.evaluate(`(${TOASTS}).includes("已上传到")`);
    assert.equal(success, false, "cancel must not report a completed upload");
    return { evidence: { dialogText: dialogText.slice(0, 160) } };
  });

  await pass("upload-overwrite-confirm-starts-upload", async () => {
    await clickSelector(page, 'button[title="上传到当前目录"]');
    await page.waitFor(`Boolean(document.querySelector('${WARN_DIALOG}'))`);
    await clickSelector(page, `${WARN_DIALOG} .nx-btn-danger-solid`);
    await page.waitFor(`(${TOASTS}).includes("开始上传")`, 10_000);
    await page.waitFor(`(${TOASTS}).includes("已上传到")`, 15_000);
    return {};
  });

  await screenshot(page, "upload-overwrite.png");
}

async function paletteSnippetAcceptance(page) {
  await pass("palette-snippet-inserts-into-terminal", async () => {
    await boot(page);
    const mark = await page.evaluate(`(${ENCODE_LOG}).length`);
    await openPalette(page);
    await page.send("Input.insertText", { text: "磁盘水位" });
    await page.waitFor(`[...document.querySelectorAll('[role="option"]')].some((o) => o.textContent?.includes("插入片段 磁盘水位"))`);
    await pressKey(page, { key: "Enter", code: "Enter", vk: 13, text: "\r" });
    await page.waitFor(`!${PALETTE_INPUT}`);
    await page.waitFor(`(${TOASTS}).includes("已插入")`);
    const written = await page.evaluate(`(${ENCODE_LOG}).slice(${mark}).join("")`);
    assert.ok(
      written.includes("df -h && du -sh /data/*"),
      `snippet body must be written to the terminal input, got: ${JSON.stringify(written.slice(0, 120))}`,
    );
    return { evidence: { written: written.slice(0, 80) } };
  });
}

async function paletteCloneAcceptance(page) {
  await pass("palette-clone-creates-shared-reference-copy", async () => {
    await boot(page);
    await openPalette(page);
    await page.send("Input.insertText", { text: "克隆资产 web-01" });
    await page.waitFor(`[...document.querySelectorAll('[role="option"]')].some((o) => o.textContent?.includes("克隆资产 web-01"))`);
    await pressKey(page, { key: "Enter", code: "Enter", vk: 13, text: "\r" });
    await page.waitFor(`!${PALETTE_INPUT}`);
    await page.waitFor(`Boolean(document.querySelector('${INFO_DIALOG}'))`);
    const dialogText = await page.evaluate(`document.querySelector('${INFO_DIALOG}')?.textContent ?? ""`);
    assert.match(dialogText, /web-01 副本/, "clone confirmation must name the copy");
    assert.match(dialogText, /不复制凭据内容/, "clone confirmation must state credentials are referenced, not copied");
    await clickSelector(page, `${INFO_DIALOG} .nx-btn-primary`);
    await page.waitFor(`(${TOASTS}).includes("已克隆为")`);
    await clickSelector(page, 'button[aria-label="资产"]');
    await page.waitFor(`Boolean(document.querySelector('[role="treeitem"][title^="web-01 副本"]'))`);
    return { evidence: { dialogText: dialogText.slice(0, 160) } };
  });
}

async function treeHideAcceptance(page) {
  await pass("asset-hide-and-unhide-is-presentation-only", async () => {
    await boot(page);
    await clickSelector(page, 'button[aria-label="资产"]');
    await page.waitFor(`Boolean(document.querySelector('[role="treeitem"][title^="web-01"]'))`);

    await openAssetMoreMenu(page, "web-01");
    await page.waitFor(`[...document.querySelectorAll('[role="menuitem"]')].some((i) => i.textContent?.includes("隐藏"))`);
    await clickMenuItemByText(page, "隐藏");
    await page.waitFor(`!document.querySelector('[role="treeitem"][title^="web-01"]')`);
    await page.waitFor(`Boolean(document.querySelector('button[aria-label^="显示已隐藏的资产"]'))`);

    await clickSelector(page, 'button[aria-label^="显示已隐藏的资产"]');
    await page.waitFor(`Boolean(document.querySelector('[role="treeitem"][title^="web-01"]'))`);
    const badge = await page.evaluate(
      `document.querySelector('[role="treeitem"][title^="web-01"]')?.textContent?.includes("已隐藏")`,
    );
    assert.equal(badge, true, "revealed hidden asset must carry the 已hidden badge");

    await openAssetMoreMenu(page, "web-01");
    await page.waitFor(`[...document.querySelectorAll('[role="menuitem"]')].some((i) => i.textContent?.includes("取消隐藏"))`);
    await clickMenuItemByText(page, "取消隐藏");
    await page.waitFor(`!document.querySelector('button[aria-label^="显示已隐藏的资产"]')`);
    const stillThere = await page.evaluate(
      `Boolean(document.querySelector('[role="treeitem"][title^="web-01"]'))`,
    );
    assert.equal(stillThere, true, "unhidden asset must stay visible");
    return {};
  });

  await screenshot(page, "asset-hide-unhide.png");
}

let vite;
let chrome;
let page;
try {
  [vite, chrome] = await Promise.all([startVite({ root: ROOT, port: VITE_PORT }), startChrome()]);
  page = await newPage(chrome);
  await uploadOverwriteAcceptance(page);
  await paletteSnippetAcceptance(page);
  await paletteCloneAcceptance(page);
  await treeHideAcceptance(page);
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
    note: "交互仅通过 CDP Input.dispatchKeyEvent / Input.dispatchMouseEvent / Input.insertText；断言读取 DOM/ARIA 与注入的 TextEncoder 输入日志",
  },
  checks,
  harness_errors: harnessErrors,
};
fs.writeFileSync(path.join(OUT, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
console.warn(`file command ux acceptance: ${checks.filter((check) => check.status === "passed").length}/${checks.length} checks passed; report=${path.join(OUT, "report.json")}`);
if (failed.length || harnessErrors.length) process.exit(1);
