#!/usr/bin/env node
import { spawn, spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const OUT = path.join(ROOT, "target/acceptance-terminal-search");
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

function startVite() {
  const command = globalThis.process.platform === "win32" ? "pnpm.cmd" : "pnpm";
  const process = spawn(command, ["exec", "vite", "--host", "127.0.0.1", "--port", String(VITE_PORT), "--strictPort"], {
    cwd: ROOT,
    env: { ...globalThis.process.env, NODE_OPTIONS: "" },
    stdio: ["ignore", "pipe", "pipe"],
  });
  return waitHttp(VITE, process).then(() => process);
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

async function pressEscape(page) {
  await pressKey(page, { key: "Escape", code: "Escape", vk: 27 });
}

async function typeText(page, text) {
  for (const ch of text) {
    const upper = ch.toUpperCase();
    await pressKey(page, { key: ch, code: `Key${upper}`, vk: upper.charCodeAt(0), text: ch });
  }
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
  await sleep(80);
}

async function screenshot(page, name) {
  const shot = await page.send("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(path.join(OUT, name), Buffer.from(shot.data, "base64"));
  return name;
}

const ENCODE_LOG = `(() => { window.__nexTermEncoded ??= []; return window.__nexTermEncoded; })()`;
const SEARCH_INPUT = `document.querySelector('input[placeholder="搜索终端内容…"]')`;

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

async function focusTerminal(page) {
  await clickSelector(page, ".xterm");
  await page.waitFor(`document.activeElement?.classList?.contains("xterm-helper-textarea") === true`);
  await page.waitFor(`(${ENCODE_LOG}).join("").includes("$ ")`, 15_000);
}

async function encodedSlice(page, from) {
  return page.evaluate(`(${ENCODE_LOG}).slice(${from})`);
}

async function terminalSearchAcceptance(page) {
  await pass("mod-f-opens-and-focuses-terminal-search", async () => {
    await boot(page);
    const mac = await isMacPlatform(page);
    await focusTerminal(page);
    const mark = await page.evaluate(`(${ENCODE_LOG}).length`);

    await pressKey(page, { key: "f", code: "KeyF", vk: 70, modifiers: mac ? MODIFIER.meta : MODIFIER.ctrl });
    await page.waitFor(`Boolean(${SEARCH_INPUT})`);
    const focused = await page.evaluate(`document.activeElement === ${SEARCH_INPUT}`);
    assert.equal(focused, true, "search input must receive focus");
    const afterOpen = await encodedSlice(page, mark);
    assert.equal(afterOpen.includes("\x06"), false, "mod+F must not be sent to the PTY as input");

    await page.send("Input.insertText", { text: "help" });
    await pressKey(page, { key: "Enter", code: "Enter", vk: 13, text: "\r" });
    const afterFind = await encodedSlice(page, mark);
    assert.equal(afterFind.join("").includes("help"), false, "typing in the search box must not leak into the terminal");

    await pressEscape(page);
    await page.waitFor(`!${SEARCH_INPUT}`);
    return { evidence: { mac } };
  });

  await pass("wrong-modifier-does-not-open-search", async () => {
    await boot(page);
    const mac = await isMacPlatform(page);
    await focusTerminal(page);
    const mark = await page.evaluate(`(${ENCODE_LOG}).length`);

    await pressKey(page, { key: "f", code: "KeyF", vk: 70, modifiers: mac ? MODIFIER.ctrl : MODIFIER.meta });
    await sleep(300);
    const opened = await page.evaluate(`Boolean(${SEARCH_INPUT})`);
    assert.equal(opened, false, "the non-platform modifier must keep its terminal/browser meaning");
    if (mac) {
      const slice = await encodedSlice(page, mark);
      assert.equal(slice.includes("\x06"), true, "Ctrl+F on macOS must still reach the PTY as ^F");
    }
    return { evidence: { mac } };
  });

  await pass("terminal-input-and-global-shortcuts-intact", async () => {
    await boot(page);
    const mac = await isMacPlatform(page);
    await focusTerminal(page);
    const mark = await page.evaluate(`(${ENCODE_LOG}).length`);

    await typeText(page, "nexterm");
    const typed = await encodedSlice(page, mark);
    assert.match(
      typed.join(""),
      /n.*e.*x.*t.*e.*r.*m/,
      "typed text must reach the terminal in order",
    );

    await pressKey(page, { key: "k", code: "KeyK", vk: 75, modifiers: mac ? MODIFIER.meta : MODIFIER.ctrl });
    await page.waitFor(`Boolean(document.querySelector('[role="dialog"] [role="combobox"]'))`);
    await pressEscape(page);
    await page.waitFor(`!document.querySelector('[role="dialog"] [role="combobox"]')`);
    return { evidence: { mac } };
  });

  await screenshot(page, "terminal-search-final.png");
}

let vite;
let chrome;
let page;
try {
  [vite, chrome] = await Promise.all([startVite(), startChrome()]);
  page = await newPage(chrome);
  await terminalSearchAcceptance(page);
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
    note: "交互仅通过 CDP Input.dispatchKeyEvent / Input.dispatchMouseEvent / Input.insertText；断言读取 DOM/焦点与注入的 TextEncoder 输入日志",
  },
  checks,
  harness_errors: harnessErrors,
};
fs.writeFileSync(path.join(OUT, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
console.warn(`terminal search acceptance: ${checks.filter((check) => check.status === "passed").length}/${checks.length} checks passed; report=${path.join(OUT, "report.json")}`);
if (failed.length || harnessErrors.length) process.exit(1);
