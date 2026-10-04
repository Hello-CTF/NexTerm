#!/usr/bin/env node
import { spawn, spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const OUT = path.join(ROOT, "target/acceptance-terminal-interaction");
const VITE_PORT = Number(process.env.NEXTERM_VITE_PORT || 1424);
const VITE = `http://127.0.0.1:${VITE_PORT}`;
const results = new Map();
const harnessErrors = [];

fs.mkdirSync(OUT, { recursive: true });

function record(id, status, detail = {}) {
  results.set(id, { id, status, ...detail });
  console.warn(`${status === "passed" ? "PASS" : "FAILED"} ${id}`);
  writeReport();
}

function writeReport() {
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
      note: "交互仅通过 CDP Input.dispatchKeyEvent / Input.dispatchMouseEvent / Input.insertText；断言读取 DOM 与注入的 TextDecoder/TextEncoder 字节日志",
    },
    checks,
    harness_errors: harnessErrors,
  };
  fs.writeFileSync(path.join(OUT, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
}

async function pass(id, page, fn) {
  try {
    const detail = (await fn()) || {};
    record(id, "passed", typeof detail === "object" ? detail : { detail });
  } catch (error) {
    let diagnostics = null;
    let tail = [];
    try {
      diagnostics = await domDiagnostics(page);
      tail = (await writes(page)).slice(-12);
      await screenshot(page, `failure-${id}.png`);
    } catch {}
    record(id, "failed", {
      error: `${String(error?.stack || error)}\ndiagnostics=${JSON.stringify(diagnostics)}\nwritesTail=${JSON.stringify(tail)}`,
    });
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
      const timer = setTimeout(() => {
        if (this.pending.has(id)) {
          this.pending.delete(id);
          reject(new Error(`${method}: CDP call timed out`));
        }
      }, 30_000);
      this.pending.set(id, {
        method,
        resolve: (value) => {
          clearTimeout(timer);
          resolve(value);
        },
        reject: (error) => {
          clearTimeout(timer);
          reject(error);
        },
      });
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
    `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`, "--window-size=390,844", "about:blank",
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

const PROBE_SOURCE = `(() => {
  window.__nxWrites = [];
  window.__nxEncoded = [];
  window.__wheelEvents = [];
  window.__ctxEvents = 0;
  window.__ctxLast = null;
  const originalDecode = TextDecoder.prototype.decode;
  TextDecoder.prototype.decode = function (...args) {
    const out = originalDecode.apply(this, args);
    try { window.__nxWrites.push(out); } catch {}
    return out;
  };
  document.addEventListener("wheel", (ev) => {
    try {
      window.__wheelEvents.push({ deltaY: ev.deltaY, deltaMode: ev.deltaMode, prevented: ev.defaultPrevented });
    } catch {}
  }, true);
  document.addEventListener("contextmenu", (ev) => {
    try {
      window.__ctxEvents += 1;
      window.__ctxLast = { prevented: ev.defaultPrevented, target: ev.target?.className?.toString?.().slice(0, 60) };
    } catch {}
  }, true);
  const INJECTS = [
    ["nx-alt-on", "\\x1b[?1049h"],
    ["nx-alt-off", "\\x1b[?1049l"],
    ["nx-bp-on", "\\x1b[?2004h"],
    ["nx-bp-off", "\\x1b[?2004l"],
    ["nx-mouse-on", "\\x1b[?1000h"],
    ["nx-mouse-off", "\\x1b[?1000l"],
    ["nx-ck-on", "\\x1b[?1h"],
    ["nx-ck-off", "\\x1b[?1l"],
  ];
  const originalEncode = TextEncoder.prototype.encode;
  TextEncoder.prototype.encode = function (...args) {
    let out = originalEncode.apply(this, args);
    try {
      const text = typeof args[0] === "string" ? args[0] : "";
      window.__nxEncoded.push(text);
      if (text.endsWith("\\n")) {
        const hit = INJECTS.find(([sentinel]) => text.includes(sentinel));
        if (hit) {
          const extra = originalEncode.call(this, hit[1]);
          const merged = new Uint8Array(out.length + extra.length);
          merged.set(out);
          merged.set(extra, out.length);
          out = merged;
        }
      }
    } catch {}
    return out;
  };
})()`;

async function pressKey(page, { key, code, vk, modifiers = 0, text }) {
  const base = { key, code, windowsVirtualKeyCode: vk, modifiers };
  if (text !== undefined) {
    await page.send("Input.dispatchKeyEvent", { type: "keyDown", ...base, text });
  } else {
    await page.send("Input.dispatchKeyEvent", { type: "rawKeyDown", ...base });
  }
  await page.send("Input.dispatchKeyEvent", { type: "keyUp", ...base });
  await sleep(30);
}

async function typeText(page, text) {
  await page.send("Input.insertText", { text });
  await sleep(60);
}

async function pressEnter(page) {
  await pressKey(page, { key: "Enter", code: "Enter", vk: 13, text: "\r" });
  await sleep(150);
}

async function runCommand(page, command) {
  await typeText(page, command);
  await pressEnter(page);
}

async function waitForPrompt(page) {
  await page.waitFor(`(() => {
    const e = window.__nxEncoded;
    return e.length > 0 && e[e.length - 1].includes("$ ");
  })()`, 10_000);
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

const writes = (page) => page.evaluate("window.__nxWrites");

async function writesCount(page) {
  const list = await writes(page);
  return list.length;
}

async function waitWrites(page, predicate, timeout = 6000) {
  const deadline = Date.now() + timeout;
  let last = [];
  while (Date.now() < deadline) {
    last = await writes(page);
    if (predicate(last)) return last;
    await sleep(50);
  }
  throw new Error(`writes condition timed out; last=${JSON.stringify(last)}`);
}

async function boot(page) {
  await page.send("Emulation.setDeviceMetricsOverride", {
    width: 390,
    height: 844,
    deviceScaleFactor: 2,
    mobile: true,
  });
  const { identifier } = await page.send("Page.addScriptToEvaluateOnNewDocument", { source: PROBE_SOURCE });
  try {
    await page.navigate(`${VITE}/?demo=1`);
  } finally {
    await page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier });
  }
  await page.waitFor(
    "Boolean(document.querySelector('.xterm') && document.querySelector('.nx-terminal-keys'))",
  );
  await clickSelector(page, ".xterm");
  await page.waitFor(`document.activeElement?.classList?.contains("xterm-helper-textarea") === true`);
  await page.waitFor(`window.__nxEncoded.join("").includes("$ ")`, 15_000);
}

async function wheel(page, deltaY) {
  const point = await page.evaluate(`(() => {
    const el = document.querySelector('.xterm');
    const rect = el.getBoundingClientRect();
    return { x: rect.left + rect.width * 0.25, y: rect.top + rect.height / 2 };
  })()`);
  await page.send("Input.dispatchMouseEvent", {
    type: "mouseWheel",
    x: point.x,
    y: point.y,
    deltaX: 0,
    deltaY,
  });
  await sleep(120);
}

async function selectViaSearch(page, text) {
  await clickSelector(page, '.nx-toolbar button[aria-label="更多终端操作"]');
  await page.waitFor(`Boolean(document.querySelector('[role="menu"]'))`);
  await clickMenuItem(page, "搜索终端内容");
  await page.waitFor(`Boolean(document.querySelector('input[placeholder="搜索终端内容…"]'))`);
  await page.evaluate(`(() => {
    const input = document.querySelector('input[placeholder="搜索终端内容…"]');
    const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set;
    setter.call(input, "");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  })()`);
  await typeText(page, text);
  await pressKey(page, { key: "Enter", code: "Enter", vk: 13, text: "\r" });
  await sleep(250);
  await pressKey(page, { key: "Escape", code: "Escape", vk: 27 });
  await sleep(150);
}

async function openTerminalMenu(page) {
  const point = await page.evaluate(`(() => {
    const el = document.querySelector('.xterm');
    const rect = el.getBoundingClientRect();
    return { x: rect.left + rect.width * 0.25, y: rect.top + rect.height / 2 };
  })()`);
  for (const type of ["mousePressed", "mouseReleased"]) {
    await page.send("Input.dispatchMouseEvent", { type, x: point.x, y: point.y, button: "right", clickCount: 1 });
  }
  await page.waitFor(`Boolean(document.querySelector('[role="menu"]'))`);
}

async function clickMenuItem(page, label) {
  const clicked = await page.evaluate(`(() => {
    const item = [...document.querySelectorAll('[role="menuitem"]')].find((el) => el.textContent.includes(${JSON.stringify(label)}));
    if (!item) return false;
    item.click();
    return true;
  })()`);
  if (!clicked) throw new Error(`menu item not found: ${label}`);
  await sleep(150);
}

async function pasteSelectionViaMenu(page) {
  await openTerminalMenu(page);
  const title = await page.evaluate(
    `document.querySelector('[role="menu"]')?.textContent ?? ""`,
  );
  await clickMenuItem(page, "粘贴选中文本");
  return title;
}

async function domDiagnostics(page) {
  return page.evaluate(`(() => {
    const viewport = document.querySelector(".xterm-viewport");
    const screen = document.querySelector(".xterm-screen");
    return {
      innerHeight: window.innerHeight,
      innerWidth: window.innerWidth,
      screenHeight: screen?.style.height ?? null,
      screenWidth: screen?.style.width ?? null,
      viewportScrollTop: viewport?.scrollTop ?? null,
      viewportScrollHeight: viewport?.scrollHeight ?? null,
      viewportClientHeight: viewport?.clientHeight ?? null,
      xtermCount: document.querySelectorAll(".xterm").length,
      buttonTexts: [...document.querySelectorAll("button")].map((b) => b.textContent).filter((t) => t.includes("回到底部")),
      lastRowTexts: [...(document.querySelector(".xterm-rows")?.children ?? [])].slice(-3).map((el) => el.textContent?.slice(0, 60)),
      ctxEvents: window.__ctxEvents ?? null,
      ctxLast: window.__ctxLast ?? null,
      wheelEvents: window.__wheelEvents ?? null,
    };
  })()`);
}

async function setViewportHeight(page, height) {
  await page.send("Emulation.setDeviceMetricsOverride", {
    width: 390,
    height,
    deviceScaleFactor: 2,
    mobile: false,
  });
}

const SCREEN_HEIGHT = `parseFloat(document.querySelector('.xterm-screen')?.style.height || "0")`;

async function screenshot(page, name) {
  const shot = await page.send("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(path.join(OUT, name), Buffer.from(shot.data, "base64"));
  return name;
}

async function pasteSelectionAcceptance(page) {
  await pass("paste-selection-raw-by-default", page, async () => {
    await boot(page);
    await selectViaSearch(page, "deploy");
    const mark = await writesCount(page);
    const menuText = await pasteSelectionViaMenu(page);
    assert.match(menuText, /deploy/, `the search must select the prompt word; menu=${menuText}`);

    const list = await waitWrites(page, (l) => l.length > mark);
    const slice = list.slice(mark);
    assert.equal(slice.length, 1, `expected a single raw paste write, got ${JSON.stringify(slice)}`);
    assert.equal(slice[0], "deploy", "paste must preserve raw bytes when bracketed paste is off");
    return { evidence: { slice } };
  });

  await pass("paste-selection-bracketed-when-mode-on", page, async () => {
    await boot(page);
    await runCommand(page, "echo nx-bp-on");
    await selectViaSearch(page, "deploy");
    const mark = await writesCount(page);
    await pasteSelectionViaMenu(page);
    const list = await waitWrites(page, (l) => l.length > mark);
    const slice = list.slice(mark);
    assert.equal(slice.length, 1, `expected a single wrapped paste write, got ${JSON.stringify(slice)}`);
    assert.equal(
      slice[0],
      "\x1b[200~deploy\x1b[201~",
      "paste must be wrapped in bracketed-paste markers when the application enabled the mode",
    );

    await pressKey(page, { key: "c", code: "KeyC", vk: 67, modifiers: 2, text: "\x03" });
    await sleep(150);
    await runCommand(page, "echo nx-bp-off");
    await selectViaSearch(page, "deploy");
    const mark2 = await writesCount(page);
    await pasteSelectionViaMenu(page);
    const list2 = await waitWrites(page, (l) => l.length > mark2);
    assert.equal(
      list2[list2.length - 1],
      "deploy",
      "paste must fall back to raw bytes after the mode is disabled",
    );
    return { evidence: { wrapped: slice[0] } };
  });
}

async function wheelArrowAcceptance(page) {
  await pass("alt-screen-wheel-arrows-with-mouse-off", page, async () => {
    await boot(page);
    await runCommand(page, "echo nx-alt-on");

    let mark = await writesCount(page);
    await wheel(page, -120);
    let list = await waitWrites(page, (l) => l.length > mark);
    assert.match(
      list[list.length - 1],
      /^(?:\x1b\[A)+$/,
      "wheel up on the alternate buffer must send arrow-up keys",
    );

    await wheel(page, 120);
    list = await writes(page);
    assert.match(list[list.length - 1], /^(?:\x1b\[B)+$/, "wheel down must send arrow-down keys");

    await runCommand(page, "echo nx-ck-on");
    mark = await writesCount(page);
    await wheel(page, -120);
    list = await waitWrites(page, (l) => l.length > mark);
    assert.match(
      list[list.length - 1],
      /^(?:\x1bOA)+$/,
      "application cursor keys mode must produce SS3 arrows",
    );
    await runCommand(page, "echo nx-ck-off");

    await runCommand(page, "echo nx-mouse-on");
    mark = await writesCount(page);
    await wheel(page, -120);
    await sleep(400);
    const after = (await writes(page)).slice(mark);
    assert.equal(
      after.some((w) => w.includes("\x1b[A") || w.includes("\x1bOA")),
      false,
      "mouse reporting must keep the wheel away from the arrow fallback",
    );

    await runCommand(page, "echo nx-mouse-off");
    await runCommand(page, "echo nx-alt-off");
    mark = await writesCount(page);
    await wheel(page, -120);
    await sleep(400);
    const normal = (await writes(page)).slice(mark);
    assert.equal(
      normal.some((w) => w.includes("\x1b[A") || w.includes("\x1bOA")),
      false,
      "the normal buffer must keep scrolling without arrow writes",
    );
    return { evidence: { mouseOffWrites: after.length, normalWrites: normal.length } };
  });
}

async function scrollAffordanceAcceptance(page) {
  await pass("scroll-affordance-returns-to-bottom", page, async () => {
    await boot(page);
    for (let i = 0; i < 6; i += 1) {
      await runCommand(page, "help");
    }
    await waitForPrompt(page);
    await wheel(page, -120);
    await wheel(page, -120);

    const BUTTON = `[...document.querySelectorAll("button")].find((b) => b.textContent.includes("回到底部"))`;
    await page.waitFor(`Boolean(${BUTTON})`);
    const before = await page.evaluate(`(() => {
      const btn = ${BUTTON};
      const bar = document.querySelector(".nx-terminal-keys");
      const b = btn.getBoundingClientRect();
      const k = bar.getBoundingClientRect();
      return { text: btn.textContent, bottom: b.bottom, keysTop: k.top, viewport: window.innerHeight };
    })()`);
    assert.ok(
      before.bottom <= before.keysTop + 1,
      `the affordance must not cover the terminal keys bar: ${JSON.stringify(before)}`,
    );
    const linesOf = (text) => Number(text.match(/(\d+)\s*行/)?.[1] ?? 0);
    assert.ok(linesOf(before.text) > 0, `the affordance must count the lines below: ${before.text}`);

    await wheel(page, -120);
    await wheel(page, -120);
    const after = await page.evaluate(`(${BUTTON})?.textContent ?? ""`);
    assert.ok(
      linesOf(after) > linesOf(before.text),
      `scrolling further up must grow the affordance: ${before.text} -> ${after}`,
    );

    await page.evaluate(`(${BUTTON}).click()`);
    await page.waitFor(`!(${BUTTON})`);
    const atBottom = await page.evaluate(`(() => {
      const viewport = document.querySelector(".xterm-viewport");
      return viewport.scrollTop >= viewport.scrollHeight - viewport.clientHeight - 2;
    })()`);
    assert.equal(atBottom, true, "clicking the affordance must return the viewport to the bottom");
    const mark = await writesCount(page);
    await typeText(page, "q");
    await waitWrites(page, (l) => l.length > mark);
    return { evidence: { before: before.text, after } };
  });
}

async function resizeStormAcceptance(page) {
  await pass("resize-storm-settles-on-the-exact-final-geometry", page, async () => {
    await boot(page);
    await page.waitFor(`(${SCREEN_HEIGHT}) > 0`);
    const original = await page.evaluate(SCREEN_HEIGHT);

    await setViewportHeight(page, 640);
    await page.waitFor(`(${SCREEN_HEIGHT}) > 0 && (${SCREEN_HEIGHT}) < ${original}`, 3000);

    for (const height of [700, 620, 760, 600, 720, 660, 780, 640, 800, 680, 844]) {
      await setViewportHeight(page, height);
      await sleep(30);
    }

    await page.waitFor(`(${SCREEN_HEIGHT}) === ${original}`, 5000);
    const settled = await page.evaluate(SCREEN_HEIGHT);
    assert.equal(settled, original, "the final local geometry must be exact after the storm");

    const mark = await writesCount(page);
    await typeText(page, "q");
    await waitWrites(page, (l) => l.length > mark);
    return { evidence: { original, settled } };
  });
}

let vite;
let chrome;
try {
  [vite, chrome] = await Promise.all([startVite(), startChrome()]);
  for (const check of [
    pasteSelectionAcceptance,
    wheelArrowAcceptance,
    scrollAffordanceAcceptance,
    resizeStormAcceptance,
  ]) {
    const checkPage = await newPage(chrome);
    try {
      await check(checkPage);
    } finally {
      checkPage.close();
    }
  }
  const shotPage = await newPage(chrome);
  try {
    await shotPage.navigate(`${VITE}/?demo=1`);
    await shotPage.waitFor("Boolean(document.querySelector('.xterm'))");
    await screenshot(shotPage, "terminal-interaction-final.png");
  } finally {
    shotPage.close();
  }
} catch (error) {
  harnessErrors.push(String(error?.stack || error));
} finally {
  stop(chrome?.process);
  stop(vite);
}

writeReport();
const checks = [...results.values()];
const failed = checks.filter((check) => check.status !== "passed");
console.warn(`terminal interaction acceptance: ${checks.filter((check) => check.status === "passed").length}/${checks.length} checks passed; report=${path.join(OUT, "report.json")}`);
if (failed.length || harnessErrors.length) process.exit(1);
