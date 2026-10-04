#!/usr/bin/env node
// 手机终端特殊键真实浏览器验收（M115）：窄屏 + 触摸仿真，所有交互只通过
// CDP Input.dispatchTouchEvent / Input.insertText 完成 —— 不调用 element.click()。
// 字节级断言依赖页面内探针：demo 模式的 terminal_write 会把写入字节交给
// TextDecoder.decode，探针在应用脚本之前包装该方法并记录每次解码结果，
// 因此断言的是真正写入终端的字节，而不是按钮是否存在。
//
// 运行：node src/test/terminal-keys-acceptance.mjs
// 需要本机 Chrome/Chromium（CHROME_PATH 可覆盖）与 pnpm（启动 vite dev server）。
// 报告与截图写入 target/acceptance-terminal-keys/。
import { spawn, spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const OUT = path.join(ROOT, "target/acceptance-terminal-keys");
const VITE_PORT = Number(process.env.NEXTERM_VITE_PORT || 1422);
const VITE = `http://127.0.0.1:${VITE_PORT}`;
const STORAGE_KEY = "nexterm.terminalKeys.v1";
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

// 在应用脚本运行之前包装 TextDecoder.prototype.decode：demo 后端 terminal_write
// 对每个写入字节串调用一次 decode，探针据此记录真正写入终端的字节。
const PROBE_SOURCE = `(() => {
  window.__nxWrites = [];
  const original = TextDecoder.prototype.decode;
  TextDecoder.prototype.decode = function (...args) {
    const out = original.apply(this, args);
    try { window.__nxWrites.push(out); } catch {}
    return out;
  };
})()`;

async function enableMobile(page) {
  await page.send("Emulation.setDeviceMetricsOverride", {
    width: 390,
    height: 844,
    deviceScaleFactor: 2,
    mobile: true,
  });
  await page.send("Emulation.setTouchEmulationEnabled", { enabled: true, maxTouchPoints: 5 });
  await page.send("Page.addScriptToEvaluateOnNewDocument", { source: PROBE_SOURCE });
}

async function tapAt(page, x, y) {
  const point = { x, y, id: 1, radiusX: 2, radiusY: 2, force: 1 };
  await page.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [point] });
  await sleep(30);
  await page.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [point] });
  await sleep(80);
}

async function tapSelector(page, selector) {
  const rect = await page.evaluate(`(() => {
    const el = document.querySelector(${JSON.stringify(selector)});
    if (!el) return null;
    el.scrollIntoView({ block: "center", inline: "center" });
    const r = el.getBoundingClientRect();
    return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
  })()`);
  if (!rect) throw new Error(`element not found for tap: ${selector}`);
  await sleep(60);
  await tapAt(page, rect.x, rect.y);
}

const keyButton = (label) => `.nx-terminal-keys button[aria-label="${label}"]`;

const writes = (page) => page.evaluate("window.__nxWrites");

async function writesCount(page) {
  const list = await writes(page);
  return list.length;
}

// 写入是异步落地的（React onClick → sendData → terminal_write → 探针），
// 断言前必须等它们出现，而不是靠固定 sleep。
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

// 等写入计数稳定（没有新写入持续 quiet 毫秒），用于消除在途写入对偏移量的污染。
async function settleWrites(page, quiet = 300, timeout = 6000) {
  const deadline = Date.now() + timeout;
  let last = -1;
  let stableSince = Date.now();
  while (Date.now() < deadline) {
    const count = await writesCount(page);
    if (count !== last) {
      last = count;
      stableSince = Date.now();
    } else if (Date.now() - stableSince >= quiet) {
      return count;
    }
    await sleep(60);
  }
  throw new Error("writes never settled");
}

async function boot(page) {
  await page.navigate(`${VITE}/?demo=1`);
  await page.waitFor(
    "Boolean(document.querySelector('.xterm') && document.querySelector('.nx-terminal-keys'))",
  );
  // 等 demo 会话 attach、按键条进入最终布局。
  await page.waitFor("document.querySelectorAll('.nx-terminal-keys > button').length >= 4");
  // 终端 attach 完成前 sendData 会被静默丢弃：循环点 Esc（shell 对裸 ESC 无副作用），
  // 直到探针观察到真实写入，证明 触摸→按键条→terminal_write 全链路已就绪。
  let ready = false;
  for (let i = 0; i < 20 && !ready; i++) {
    await tapSelector(page, keyButton("Escape"));
    ready = await waitWrites(page, (list) => list.includes("\x1b"), 1200).then(
      () => true,
      () => false,
    );
  }
  if (!ready) throw new Error("terminal never accepted input: no terminal_write observed after tapping Esc");
  // 就绪循环可能在途一次额外 Esc 写入：等计数稳定后再开始断言。
  await settleWrites(page);
}

async function screenshot(page, name) {
  const shot = await page.send("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(path.join(OUT, name), Buffer.from(shot.data, "base64"));
  return name;
}

async function terminalKeysAcceptance(page) {
  await pass("narrow-bar-visible-and-reachable", async () => {
    await boot(page);
    const evidence = await page.evaluate(`(() => {
      const bar = document.querySelector('.nx-terminal-keys');
      const style = getComputedStyle(bar);
      const trailing = bar.querySelector('.sticky.right-0');
      const rect = (el) => {
        const r = el.getBoundingClientRect();
        return { left: r.left, right: r.right, width: r.width };
      };
      return {
        display: style.display,
        overflow: bar.scrollWidth > bar.clientWidth,
        trailing: trailing ? rect(trailing) : null,
        viewport: window.innerWidth,
        keyboardLabelled: Boolean(bar.querySelector('button[aria-label="聚焦终端并打开系统键盘"]')),
        configLabelled: Boolean(bar.querySelector('button[aria-label="配置终端按键"]')),
        modifierPressed: [...bar.querySelectorAll('button[aria-pressed]')].map((b) => b.getAttribute('aria-label')),
      };
    })()`);
    assert.equal(evidence.display, "flex", "keys bar must be visible on a narrow viewport");
    assert.equal(evidence.overflow, true, "default keys should overflow the narrow bar (scrollable)");
    assert.ok(evidence.trailing, "trailing keyboard/config group must exist");
    assert.ok(
      evidence.trailing.right <= evidence.viewport + 1 && evidence.trailing.left >= 0,
      `trailing group must stay reachable without scrolling: ${JSON.stringify(evidence)}`,
    );
    assert.equal(evidence.keyboardLabelled, true);
    assert.equal(evidence.configLabelled, true);
    assert.deepEqual(evidence.modifierPressed, ["Ctrl 修饰键", "Alt 修饰键"]);
    return { evidence };
  });

  await pass("default-keys-write-exact-bytes", async () => {
    await boot(page);
    const before = await writesCount(page);
    await tapSelector(page, keyButton("向上翻页"));
    await tapSelector(page, keyButton("Ctrl+L（清屏）"));
    await tapSelector(page, keyButton("Ctrl+R（历史搜索）"));
    await tapSelector(page, keyButton("Escape"));
    const list = await waitWrites(page, (l) => l.length >= before + 4);
    const delta = list.slice(before, before + 4);
    assert.deepEqual(delta, ["\x1b[5~", "\x0c", "\x12", "\x1b"], "each tap writes exactly one sequence");

    const cBefore = list.filter((w) => w === "c").length;
    await tapSelector(page, keyButton("字母 C；Ctrl 激活时为 Ctrl+C"));
    const after = await waitWrites(page, (l) => l.filter((w) => w === "c").length > cBefore);
    assert.ok(after.filter((w) => w === "c").length > cBefore, "plain C must write c");
    return { evidence: { delta } };
  });

  await pass("sticky-modifier-composition", async () => {
    await boot(page);
    const ctrl = keyButton("Ctrl 修饰键");
    const alt = keyButton("Alt 修饰键");
    const c = keyButton("字母 C；Ctrl 激活时为 Ctrl+C");

    let mark = await writesCount(page);
    await tapSelector(page, ctrl);
    await sleep(250);
    assert.equal(await writesCount(page), mark, "tapping Ctrl alone must not write anything");

    await tapSelector(page, c);
    let list = await waitWrites(page, (l) => l.length >= mark + 1);
    assert.deepEqual(list.slice(mark, mark + 1), ["\x03"], "Ctrl+C must write exactly \\x03");

    mark = list.length;
    await tapSelector(page, c);
    list = await waitWrites(page, (l) => l.length >= mark + 1);
    assert.deepEqual(list.slice(mark, mark + 1), ["c"], "Ctrl is one-shot: next C writes plain c");

    mark = list.length;
    await tapSelector(page, alt);
    await sleep(250);
    assert.equal(await writesCount(page), mark, "tapping Alt alone must not write anything");
    await tapSelector(page, c);
    list = await waitWrites(page, (l) => l.length >= mark + 1);
    assert.deepEqual(list.slice(mark, mark + 1), ["\x1bc"], "Alt+C must write exactly \\x1bc");

    mark = list.length;
    await tapSelector(page, ctrl);
    await tapSelector(page, alt);
    await tapSelector(page, c);
    list = await waitWrites(page, (l) => l.length >= mark + 1);
    assert.deepEqual(list.slice(mark, mark + 1), ["\x1b\x03"], "Ctrl+Alt+C must write exactly \\x1b\\x03");

    mark = list.length;
    await tapSelector(page, alt);
    await tapSelector(page, keyButton("上方向键"));
    list = await waitWrites(page, (l) => l.length >= mark + 1);
    assert.deepEqual(list.slice(mark, mark + 1), ["\x1b\x1b[A"], "Alt+Up must write exactly \\x1b\\x1b[A");

    const pressed = await page.evaluate(`[...document.querySelectorAll('.nx-terminal-keys button[aria-pressed]')].map((b) => b.getAttribute('aria-pressed'))`);
    assert.deepEqual(pressed, ["false", "false"], "modifiers reset after use");
    return { evidence: { finalWrites: list.slice(-6) } };
  });

  await pass("config-enable-reorder-persist-reset", async () => {
    await boot(page);
    await tapSelector(page, keyButton("配置终端按键"));
    await page.waitFor("Boolean(document.querySelector('[role=\"dialog\"][aria-modal=\"true\"]'))");

    const noEditor = await page.evaluate(`(() => {
      const dialog = document.querySelector('[role="dialog"][aria-modal="true"]');
      return {
        textInputs: dialog.querySelectorAll('input[type="text"], textarea').length,
        checkboxes: dialog.querySelectorAll('input[type="checkbox"]').length,
        label: dialog.getAttribute('aria-label'),
      };
    })()`);
    assert.equal(noEditor.textInputs, 0, "config must not offer arbitrary escape-sequence editing");
    assert.ok(noEditor.checkboxes >= 15, `all catalog keys must be configurable: ${JSON.stringify(noEditor)}`);
    assert.equal(noEditor.label, "终端按键配置");

    // 停用 PgUp 并把 Esc 下移一位。
    await tapSelector(page, 'input[aria-label="在按键条中显示「向上翻页」"]');
    await tapSelector(page, 'button[aria-label="下移 Escape"]');
    await tapSelector(page, '[role="dialog"] button.nx-btn-primary');
    await page.waitFor("!document.querySelector('[role=\"dialog\"]')");

    const afterEdit = await page.evaluate(`(() => ({
      labels: [...document.querySelectorAll('.nx-terminal-keys > button')].map((b) => b.textContent?.trim()),
      stored: localStorage.getItem(${JSON.stringify(STORAGE_KEY)}),
    }))()`);
    assert.ok(!afterEdit.labels.includes("PgUp"), "disabled key must leave the bar");
    assert.deepEqual(afterEdit.labels.slice(2, 5), ["Tab", "Esc", "↑"], "reorder must apply to the bar");
    const stored = JSON.parse(afterEdit.stored);
    assert.deepEqual(stored.slice(0, 3), ["tab", "escape", "up"]);
    assert.ok(!stored.includes("pageup"));

    // 重新加载：配置必须仍在（localStorage 持久化，无服务器同步）。
    await boot(page);
    const afterReload = await page.evaluate(`[...document.querySelectorAll('.nx-terminal-keys > button')].map((b) => b.textContent?.trim())`);
    assert.ok(!afterReload.includes("PgUp"), "disabled key must stay disabled after reload");
    assert.deepEqual(afterReload.slice(2, 5), ["Tab", "Esc", "↑"], "order must persist after reload");

    // 重置默认：恢复并清除存储。
    await tapSelector(page, keyButton("配置终端按键"));
    await page.waitFor("Boolean(document.querySelector('[role=\"dialog\"]'))");
    await page.evaluate(`(() => {
      const dialog = document.querySelector('[role="dialog"]');
      const btn = [...dialog.querySelectorAll('button')].find((b) => b.textContent?.trim() === '重置默认');
      const r = btn.getBoundingClientRect();
      return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
    })()`).then((rect) => tapAt(page, rect.x, rect.y));
    await tapSelector(page, '[role="dialog"] button.nx-btn-primary');
    await page.waitFor("!document.querySelector('[role=\"dialog\"]')");
    const afterReset = await page.evaluate(`(() => ({
      labels: [...document.querySelectorAll('.nx-terminal-keys > button')].map((b) => b.textContent?.trim()),
      stored: localStorage.getItem(${JSON.stringify(STORAGE_KEY)}),
    }))()`);
    assert.equal(afterReset.stored, null, "reset must clear the stored config");
    assert.deepEqual(afterReset.labels.slice(2, 5), ["Esc", "Tab", "↑"]);
    assert.ok(afterReset.labels.includes("PgUp"), "reset must restore default keys");

    await boot(page);
    const finalLabels = await page.evaluate(`[...document.querySelectorAll('.nx-terminal-keys > button')].map((b) => b.textContent?.trim())`);
    assert.ok(finalLabels.includes("PgUp"), "defaults must survive reload after reset");
    return { evidence: { afterEdit: afterEdit.labels, afterReload, afterReset: afterReset.labels } };
  });

  await pass("keyboard-focus-and-ime-path-intact", async () => {
    await boot(page);
    await tapSelector(page, keyButton("聚焦终端并打开系统键盘"));
    await page.waitFor("Boolean(document.activeElement && document.activeElement.closest('.xterm'))");

    const mark = await writesCount(page);
    await page.send("Input.insertText", { text: "echo m115\r" });
    const list = await waitWrites(page, (l) =>
      l.slice(mark).some((w) => w.includes("echo m115")),
    );
    const delta = list.slice(mark);
    assert.ok(
      delta.some((w) => w.includes("echo m115")),
      `typed text must reach the terminal through the normal input path: ${JSON.stringify(delta)}`,
    );
    return { evidence: { delta } };
  });

  await screenshot(page, "terminal-keys-final.png");
}

let vite;
let chrome;
let page;
try {
  [vite, chrome] = await Promise.all([startVite(), startChrome()]);
  page = await newPage(chrome);
  await enableMobile(page);
  await terminalKeysAcceptance(page);
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
    touch_emulation: true,
    viewport: "390x844",
    note: "交互仅通过 Input.dispatchTouchEvent / Input.insertText；字节断言来自页面内 TextDecoder 探针记录的 terminal_write 负载",
  },
  checks,
  harness_errors: harnessErrors,
};
fs.writeFileSync(path.join(OUT, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
console.warn(`terminal keys acceptance: ${checks.filter((check) => check.status === "passed").length}/${checks.length} checks passed; report=${path.join(OUT, "report.json")}`);
if (failed.length || harnessErrors.length) process.exit(1);
