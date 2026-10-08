#!/usr/bin/env node
// 真实 Chromium 验收 (task acceptance:browser): 只保留 jsdom 无法覆盖的行为 ——
// 真实焦点与纯键盘导航、320/390px 面板级溢出、elementFromPoint/触摸命中与 toast
// 避让、真实 reload 后会话恢复与终端历史重放、WebSocket 早期帧/重放/断线重连、
// RPC 非重试与 5xx 抖动恢复、auth=on CSRF、服务端重启后的终端历史、AI 流式重连、
// 主机密钥变更不自动信任。jsdom 可覆盖的行为不进本脚本; 物理设备与真实部署缺口
// 以 REAL_TARGET_GAPS 显式记录, 不算通过。
import { spawn, spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import fs from "node:fs";
import http from "node:http";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const OUT = path.join(ROOT, "target/acceptance-browser");
const VITE_PORT = Number(process.env.NEXTERM_VITE_PORT || 1420);
const VITE = `http://127.0.0.1:${VITE_PORT}`;
const results = new Map();
const harnessErrors = [];
const EXPECTED = [
  "layout-360", "layout-560", "layout-820", "panel-no-overflow-320-390", "coarse-pointer-keys", "soft-keyboard-focus-viewport-resize",
  "keyboard-focus-navigation", "touch-hit-toast-avoidance",
  "ws-early-frame", "ws-replay", "ws-reconnect-replay",
  "rpc-401-non-retry", "rpc-403-non-retry", "rpc-404-non-retry", "rpc-network-5xx-jitter-recovery",
  "rpc-auth-on-csrf", "files-auth-on-csrf",
  "transcript-survives-server-restart", "reload-restores-terminal-history", "ai-stream-reconnect-replay", "hostkey-changed-no-auto-trust",
];
const REAL_TARGET_GAPS = [
  { id: "ios-soft-keyboard", status: "evidence-gap", reason: "a physical iOS/iPadOS device and its native soft keyboard are not available to headless Chromium" },
  { id: "android-soft-keyboard", status: "evidence-gap", reason: "a physical Android device and its native soft keyboard are not available to headless Chromium" },
  { id: "mobile-safari", status: "evidence-gap", reason: "Chromium is real-browser evidence, not Mobile Safari evidence" },
  { id: "lazycat-pages-real-targets", status: "evidence-gap", reason: "LazyCat box and deployed Pages target acceptance belong to M47; desktop/server builds do not cover them" },
  { id: "linux-server-real-deployment", status: "evidence-gap", reason: "a real always-on Linux host with domain, TLS and a reverse proxy is not available to a local headless run" },
  { id: "lazycat-arm64-real-box", status: "evidence-gap", reason: "ARM64 LazyCat hardware and the LZC_ARCH=arm64 real-box installation belong to M47; local desktop/server builds do not cover them" },
];

fs.mkdirSync(OUT, { recursive: true });

function record(id, status, detail = {}) {
  results.set(id, { id, status, ...detail });
  console.warn(`${status === "passed" ? "PASS" : status === "failed" ? "FAIL" : "NOT RUN"} ${id}`);
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

export function stop(process) {
  if (!process || process.exitCode !== null) return;
  process.kill("SIGTERM");
}

async function stopAndWait(process) {
  if (!process || process.exitCode !== null) return;
  const exited = new Promise((resolve) => process.once("exit", resolve));
  process.kill("SIGTERM");
  await exited;
}

function chromeExecutable() {
  const candidates = [
    process.env.CHROME_PATH,
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
    "/Applications/Chromium.app/Contents/MacOS/Chromium",
    "google-chrome",
    "chromium",
    "chromium-browser",
    "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe",
    "C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe",
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
    this.listeners = new Map();
    socket.addEventListener("message", (event) => {
      const message = JSON.parse(String(event.data));
      if (message.id) {
        const pending = this.pending.get(message.id);
        if (!pending) return;
        this.pending.delete(message.id);
        if (message.error) pending.reject(new Error(`${pending.method}: ${message.error.message}`));
        else pending.resolve(message.result || {});
      } else if (message.method) {
        for (const listener of this.listeners.get(message.method) || []) listener(message.params || {});
      }
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

  on(method, listener) {
    const listeners = this.listeners.get(method) || new Set();
    listeners.add(listener);
    this.listeners.set(method, listeners);
    return () => listeners.delete(listener);
  }

  send(method, params = {}) {
    const id = ++this.sequence;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject, method });
      this.socket.send(JSON.stringify({ id, method, params }));
      setTimeout(() => {
        if (this.pending.has(id)) {
          this.pending.delete(id);
          reject(new Error(`CDP send timeout: ${method} ${JSON.stringify(params).slice(0, 120)}`));
        }
      }, 30_000);
    });
  }

  async evaluate(expression) {
    const response = await this.send("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true, userGesture: true });
    if (response.exceptionDetails) {
      throw new Error(await this.describeException(response.exceptionDetails));
    }
    return response.result?.value;
  }

  async describeException(details) {
    const exception = details.exception ?? {};
    let headline;
    if (typeof exception.value === "string") {
      headline = exception.value;
    } else if (exception.value !== undefined && exception.value !== null && typeof exception.value === "object") {
      headline = JSON.stringify(exception.value);
    } else {
      headline = exception.description || details.text || exception.className || "page exception";
    }
    const lines = [headline];
    if (exception.objectId && exception.value === undefined) {
      try {
        const properties = await this.send("Runtime.getProperties", { objectId: exception.objectId, ownProperties: true });
        const preview = (properties.result || [])
          .filter((property) => property.value !== undefined)
          .slice(0, 12)
          .map((property) => `${property.name}=${JSON.stringify(property.value.value ?? property.value.description ?? property.value.type)}`);
        if (preview.length > 0) lines.push(`thrown value properties: { ${preview.join(", ")} }`);
      } catch {}
    }
    for (const frame of details.stackTrace?.callFrames ?? []) {
      lines.push(`    at ${frame.functionName || "<anonymous>"} (${frame.url}:${frame.lineNumber + 1}:${frame.columnNumber + 1})`);
    }
    return lines.join("\n");
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
    return { process, port, profile, version: await response.json(), stderr: () => stderr };
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

let cachedServerBinary = null;

function serverBinary() {
  if (globalThis.process.env.NEXTERM_SERVER_BIN) return globalThis.process.env.NEXTERM_SERVER_BIN;
  if (cachedServerBinary) return cachedServerBinary;
  const goos = { darwin: "darwin", linux: "linux", win32: "windows" }[globalThis.process.platform];
  const goarch = { arm64: "arm64", x64: "amd64" }[globalThis.process.arch];
  const binary = path.join(ROOT, "target/go-build/nexterm-server-browser");
  const built = spawnSync(globalThis.process.execPath, ["scripts/build.mjs", "server", "--release", `--os=${goos}`, `--arch=${goarch}`, `--out=${binary}`], { cwd: ROOT, stdio: "inherit" });
  if (built.status !== 0) throw new Error("Go server build for browser acceptance failed");
  cachedServerBinary = binary;
  return binary;
}

export async function startServerOn(dataDir) {
  const binary = serverBinary();
  const port = await freePort();
  const env = { ...globalThis.process.env, NEXTERM_MASTER_KEY: "real-browser-e2e-master" };
  const log = fs.openSync(path.join(OUT, "server.log"), "a");
  const process = spawn(binary, ["--listen", `127.0.0.1:${port}`, "--data-dir", dataDir, "--auth=loopback"], {
    cwd: ROOT,
    env,
    stdio: ["ignore", log, log],
  });
  await waitHttp(`http://127.0.0.1:${port}/healthz`, process);
  return { process, port, origin: `http://127.0.0.1:${port}`, data: dataDir };
}

export async function startServer() {
  return startServerOn(fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-browser-server-")));
}

async function waitInitCode(logPath, process, timeout = 15_000) {
  const marker = "一次性初始化码: ";
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (process.exitCode !== null && process.exitCode !== undefined) throw new Error(`auth=on server exited ${process.exitCode}`);
    const text = fs.existsSync(logPath) ? fs.readFileSync(logPath, "utf8") : "";
    const line = text.split("\n").find((entry) => entry.includes(marker));
    if (line) return line.split(marker, 2)[1].trim();
    await sleep(200);
  }
  throw new Error(`auth=on init code not found in ${logPath}`);
}

async function startAuthOnServer() {
  const binary = serverBinary();
  const port = await freePort();
  const data = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-browser-auth-on-"));
  const env = { ...globalThis.process.env, NEXTERM_MASTER_KEY: "real-browser-auth-on-e2e-master" };
  const logPath = path.join(OUT, "server-auth-on.log");
  const log = fs.openSync(logPath, "w");
  const process = spawn(binary, ["--listen", `127.0.0.1:${port}`, "--data-dir", data, "--auth=on"], {
    cwd: ROOT,
    env,
    stdio: ["ignore", log, log],
  });
  await waitHttp(`http://127.0.0.1:${port}/healthz`, process);
  const initCode = await waitInitCode(logPath, process);
  return { process, port, origin: `http://127.0.0.1:${port}`, data, initCode };
}

async function metrics(page, width, height, coarse) {
  await page.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile: width <= 560 });
  if (coarse) {
    await page.send("Emulation.setTouchEmulationEnabled", { enabled: true, maxTouchPoints: 5 });
    await page.send("Emulation.setEmulatedMedia", { features: [{ name: "pointer", value: "coarse" }, { name: "any-pointer", value: "coarse" }] });
  } else {
    await page.send("Emulation.setTouchEmulationEnabled", { enabled: false, maxTouchPoints: 1 });
    await page.send("Emulation.setEmulatedMedia", { features: [{ name: "pointer", value: "fine" }, { name: "any-pointer", value: "fine" }] });
  }
  await sleep(350);
}

async function layoutAcceptance(page) {
  await page.navigate(`${VITE}/?demo=1`);
  await page.waitFor("document.querySelector('#root')?.children.length > 0 && document.querySelector('.xterm')");
  for (const width of [360, 560, 820]) {
    await pass(`layout-${width}`, async () => {
      await metrics(page, width, 740, false);
      const evidence = await page.evaluate(`(async () => {
        const { workspaceViewport } = await import('/src/features/terminal/workspaceLayout.ts');
        const model = workspaceViewport(${width}, false);
        const keys = document.querySelector('.nx-terminal-keys');
        const keyDisplay = keys ? getComputedStyle(keys).display : 'missing';
        return {
          model,
          innerWidth,
          scrollWidth: document.documentElement.scrollWidth,
          noHorizontalOverflow: document.documentElement.scrollWidth <= innerWidth + 1,
          keyDisplay,
          keysExpected: model.terminalKeys,
          keysVisible: Boolean(keys) && keyDisplay !== 'none',
          rootText: document.querySelector('#root').innerText.slice(0, 120),
        };
      })()`);
      assert.equal(evidence.innerWidth, width);
      assert.equal(evidence.noHorizontalOverflow, true, JSON.stringify(evidence));
      assert.equal(evidence.model.overlaySidebars, true);
      assert.equal(evidence.model.compact, width <= 560);
      assert.equal(evidence.keysVisible, evidence.keysExpected, JSON.stringify(evidence));
      const screenshot = await page.send("Page.captureScreenshot", { format: "png" });
      fs.writeFileSync(path.join(OUT, `layout-${width}.png`), Buffer.from(screenshot.data, "base64"));
      return { evidence, screenshot: `layout-${width}.png` };
    });
  }
  await pass("coarse-pointer-keys", async () => {
    await metrics(page, 820, 740, true);
    const evidence = await page.evaluate(`(() => {
      const keys = document.querySelector('.nx-terminal-keys');
      return { coarse: matchMedia('(pointer: coarse)').matches, keysVisible: Boolean(keys) && getComputedStyle(keys).display !== 'none' };
    })()`);
    assert.deepEqual(evidence, { coarse: true, keysVisible: true });
    return { evidence };
  });
  await pass("soft-keyboard-focus-viewport-resize", async () => {
    await metrics(page, 360, 740, true);
    const focused = await page.evaluate(`(() => {
      const button = document.querySelector('.nx-terminal-keyboard');
      if (!button) return { button: false };
      button.click();
      const active = document.activeElement;
      return { button: true, focused: active && (active.tagName === 'TEXTAREA' || active.classList.contains('xterm-helper-textarea')) };
    })()`);
    assert.deepEqual(focused, { button: true, focused: true });
    await metrics(page, 360, 420, true);
    const resized = await page.evaluate(`({ height: innerHeight, noHorizontalOverflow: document.documentElement.scrollWidth <= innerWidth + 1 })`);
    assert.deepEqual(resized, { height: 420, noHorizontalOverflow: true });
    return { evidence: { focused, resized }, scope: "real Chromium focus + viewport resize; physical soft keyboard remains an explicit gap" };
  });
}

const KEYS = {
  Tab: { key: "Tab", code: "Tab", vk: 9 },
  Enter: { key: "Enter", code: "Enter", vk: 13 },
  ArrowLeft: { key: "ArrowLeft", code: "ArrowLeft", vk: 37 },
  ArrowRight: { key: "ArrowRight", code: "ArrowRight", vk: 39 },
};

// Enter 必须带 text 的 keyDown: rawKeyDown 只能到达 JS 监听器, 不会触发原生按钮的点击合成。
async function press(page, name) {
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

async function pressCtrl(page, letter, vk) {
  const base = { key: letter, code: `Key${letter.toUpperCase()}`, windowsVirtualKeyCode: vk, modifiers: 2 };
  await page.send("Input.dispatchKeyEvent", { type: "rawKeyDown", ...base });
  await page.send("Input.dispatchKeyEvent", { type: "keyUp", ...base });
  await sleep(60);
}

async function pressCtrlShift(page, letter, vk) {
  const base = { key: letter.toUpperCase(), code: `Key${letter.toUpperCase()}`, windowsVirtualKeyCode: vk, modifiers: 10 };
  await page.send("Input.dispatchKeyEvent", { type: "rawKeyDown", ...base });
  await page.send("Input.dispatchKeyEvent", { type: "keyUp", ...base });
  await sleep(60);
}

async function tabUntil(page, predicate, max = 200) {
  for (let i = 0; i < max; i++) {
    const hit = await page.evaluate(`(${predicate})(document.activeElement)`);
    if (hit) return i;
    await press(page, "Tab");
  }
  throw new Error(`Tab traversal exhausted after ${max}: ${predicate}`);
}

const WS_TABLIST = `[role="tablist"][aria-label="工作区"]`;
const PANE_TABLIST = `[role="tablist"][aria-label="标签页"]`;

async function bootDemo(page) {
  await page.navigate(`${VITE}/?demo=1`);
  await page.waitFor(`Boolean(document.querySelector('${WS_TABLIST} [role="tab"]') && document.querySelector('.xterm'))`);
}

// 纯键盘真实浏览器验收: 所有交互只通过 CDP Input 域完成, 不调用 element.click()/focus()。
async function keyboardAcceptance(page) {
  await metrics(page, 1280, 900, false);
  await bootDemo(page);
  await pass("keyboard-focus-navigation", async () => {
    const isWsTab = `(el) => el?.getAttribute?.('role') === 'tab' && el.closest('[role="tablist"]')?.getAttribute('aria-label') === '工作区'`;
    const tabPresses = await tabUntil(page, isWsTab);
    const before = await page.evaluate(`document.querySelectorAll('${WS_TABLIST} [role="tab"]').length`);
    assert.equal(before, 1, "demo should boot with exactly one workspace");

    await pressCtrl(page, "t", 84);
    await page.waitFor(`document.querySelectorAll('${WS_TABLIST} [role="tab"]').length === 2`);

    await press(page, "ArrowRight");
    const afterFirst = await page.evaluate(`(() => {
      const selected = document.querySelector('${WS_TABLIST} [role="tab"][aria-selected="true"]');
      return { title: selected?.textContent ?? null, focusFollows: document.activeElement === selected };
    })()`);
    assert.notEqual(afterFirst.title, null);
    assert.equal(afterFirst.focusFollows, true, "focus must follow selection");

    await press(page, "ArrowRight");
    const afterSecond = await page.evaluate(`(() => {
      const selected = document.querySelector('${WS_TABLIST} [role="tab"][aria-selected="true"]');
      return { title: selected?.textContent ?? null, focusFollows: document.activeElement === selected, tabindex: selected?.tabIndex };
    })()`);
    assert.notEqual(afterSecond.title, afterFirst.title, "ArrowRight must move the selection");
    assert.equal(afterSecond.focusFollows, true, "focus must follow selection");
    assert.equal(afterSecond.tabindex, 0);

    await press(page, "ArrowLeft");
    const back = await page.evaluate(`document.querySelector('${WS_TABLIST} [role="tab"][aria-selected="true"]')?.textContent ?? null`);
    assert.equal(back, afterFirst.title, "ArrowLeft must switch back");

    await pressCtrlShift(page, "p", 80);
    await page.waitFor(`Boolean(document.querySelector('[role="dialog"] [role="combobox"]'))`);
    await page.send("Input.insertText", { text: "设置" });
    await page.waitFor(`[...document.querySelectorAll('[role="option"],[role="listbox"] *')].some((el) => el.textContent?.includes('设置'))`);
    await press(page, "Enter");
    await page.waitFor(`[...document.querySelectorAll('${PANE_TABLIST} [role="tab"]')].some((t) => t.getAttribute('aria-selected') === 'true' && t.offsetParent !== null && t.textContent?.includes('设置'))`);

    const isSettingsTab = `(el) => el?.getAttribute?.('role') === 'tab' && el.getAttribute('aria-selected') === 'true' && el.offsetParent !== null && el.textContent?.includes('设置')`;
    const closeTabPresses = await tabUntil(page, isSettingsTab);
    await press(page, "Tab");
    const closeFocus = await page.evaluate(`(() => {
      const el = document.activeElement;
      return { tag: el?.tagName, label: el?.getAttribute?.('aria-label') ?? null };
    })()`);
    assert.equal(closeFocus.tag, "BUTTON");
    assert.equal(closeFocus.label, "关闭标签 设置");

    await press(page, "Enter");
    await page.waitFor(`![...document.querySelectorAll('${PANE_TABLIST} [role="tab"]')].some((t) => t.offsetParent !== null && t.textContent?.includes('设置'))`);
    return { evidence: { tabPresses, afterFirst, afterSecond, back, closeTabPresses, closeFocus } };
  });
}

async function narrowPanelAcceptance(page) {
  await bootDemo(page);
  await pass("panel-no-overflow-320-390", async () => {
    const evidence = {};
    for (const width of [320, 390]) {
      await metrics(page, width, 740, false);
      evidence[width] = await page.evaluate(`(() => {
        const panels = [...document.querySelectorAll('.nx-pane, [role="tabpanel"]')]
          .filter((el) => el.getBoundingClientRect().width > 0);
        return {
          innerWidth,
          docScrollWidth: document.documentElement.scrollWidth,
          bodyScrollWidth: document.body.scrollWidth,
          panelCount: panels.length,
          overflowing: panels
            .filter((el) => el.scrollWidth > el.clientWidth + 1)
            .map((el) => ({ cls: String(el.className).slice(0, 60), scrollWidth: el.scrollWidth, clientWidth: el.clientWidth })),
        };
      })()`);
      const sample = evidence[width];
      assert.ok(sample.panelCount > 0, `${width}: no panels rendered: ${JSON.stringify(sample)}`);
      assert.ok(sample.docScrollWidth <= sample.innerWidth + 1, `${width}: document overflow: ${JSON.stringify(sample)}`);
      assert.ok(sample.bodyScrollWidth <= sample.innerWidth + 1, `${width}: body overflow: ${JSON.stringify(sample)}`);
      assert.deepEqual(sample.overflowing, [], `${width}: panel-level overflow: ${JSON.stringify(sample)}`);
      const screenshot = await page.send("Page.captureScreenshot", { format: "png" });
      fs.writeFileSync(path.join(OUT, `panel-no-overflow-${width}.png`), Buffer.from(screenshot.data, "base64"));
    }
    return { evidence };
  });
}

async function touchAcceptance(page) {
  await bootDemo(page);
  await metrics(page, 390, 844, true);
  await pass("touch-hit-toast-avoidance", async () => {
    await page.evaluate(`(async () => {
      const { useUi } = await import('/src/app/store.ts');
      for (let i = 1; i <= 16; i += 1) {
        useUi.getState().pushToast('error', '错误 ' + i + '：' + '磁盘写入失败，请检查连接与权限后重试。'.repeat(8));
      }
      return true;
    })()`);
    await page.waitFor(`document.querySelectorAll('.nx-toasts button').length >= 16`);
    const evidence = await page.evaluate(`(() => {
      const keys = [...document.querySelectorAll('.nx-terminal-key')].filter((el) => {
        const r = el.getBoundingClientRect();
        return r.width > 0 && r.height > 0;
      });
      const blockedKeys = keys.filter((el) => {
        const r = el.getBoundingClientRect();
        const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
        return !(hit && (hit === el || el.contains(hit)));
      }).map((el) => el.getAttribute('aria-label'));
      const toasts = document.querySelector('.nx-toasts').getBoundingClientRect();
      const keysBar = document.querySelector('.nx-terminal-keys').getBoundingClientRect();
      const status = document.querySelector('.nx-statusbar').getBoundingClientRect();
      const overlap = (a, b) => !(a.right <= b.left + 0.5 || b.right <= a.left + 0.5 || a.bottom <= b.top + 0.5 || b.bottom <= a.top + 0.5);
      return {
        keyCount: keys.length,
        blockedKeys,
        minKeyW: Math.min(...keys.map((el) => el.getBoundingClientRect().width)),
        minKeyH: Math.min(...keys.map((el) => el.getBoundingClientRect().height)),
        overlapKeys: overlap(toasts, keysBar),
        overlapStatus: overlap(toasts, status),
      };
    })()`);
    assert.ok(evidence.keyCount >= 4, `too few terminal keys: ${JSON.stringify(evidence)}`);
    assert.deepEqual(evidence.blockedKeys, [], `elementFromPoint cannot reach key centers: ${JSON.stringify(evidence)}`);
    assert.equal(evidence.overlapKeys, false, `toast stack overlaps key bar: ${JSON.stringify(evidence)}`);
    assert.equal(evidence.overlapStatus, false, `toast stack overlaps status bar: ${JSON.stringify(evidence)}`);

    const ctrl = await page.evaluate(`(() => {
      const el = [...document.querySelectorAll('.nx-terminal-key')].find((b) => b.getAttribute('aria-label') === 'Ctrl 修饰键');
      if (!el) return null;
      const r = el.getBoundingClientRect();
      return { x: r.left + r.width / 2, y: r.top + r.height / 2, pressed: el.getAttribute('aria-pressed') };
    })()`);
    assert.ok(ctrl, "Ctrl key missing");
    assert.equal(ctrl.pressed, "false");
    await page.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x: ctrl.x, y: ctrl.y, radiusX: 4, radiusY: 4, force: 1 }] });
    await sleep(40);
    await page.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
    await page.waitFor(`document.querySelector('.nx-terminal-key[aria-label="Ctrl 修饰键"]')?.getAttribute('aria-pressed') === 'true'`, 5_000);
    const screenshot = await page.send("Page.captureScreenshot", { format: "png" });
    fs.writeFileSync(path.join(OUT, "touch-toast-avoidance.png"), Buffer.from(screenshot.data, "base64"));
    return { evidence: { ...evidence, ctrlTapToggled: true }, screenshot: "touch-toast-avoidance.png" };
  });
}

async function wsAcceptance(page, server) {
  await page.navigate(`${server.origin}/healthz`);
  await page.evaluate(`window.__NEXTERM_TRANSPORT__ = 'web'; true`);
  await page.evaluate(`(() => {
    const registry = window.__nxSockets = [];
    const Orig = window.WebSocket;
    window.WebSocket = function (url, protocols) {
      const ws = protocols === undefined ? new Orig(url) : new Orig(url, protocols);
      registry.push(ws);
      return ws;
    };
    window.WebSocket.prototype = Orig.prototype;
    for (const key of ["CONNECTING", "OPEN", "CLOSING", "CLOSED"]) {
      Object.defineProperty(window.WebSocket, key, { value: Orig[key] });
    }
    return true;
  })()`);
  await page.evaluate(`(async () => {
    const commands = await import('${VITE}/src/ipc/commands.ts');
    const events = await import('${VITE}/src/ipc/events.ts');
    const state = window.__nxAcceptance = { commands, events, frames: 0, text: '', reopened: 0, reconnectAttached: 0 };
    state.channel = events.createBinaryChannel((bytes) => {
      state.frames += 1;
      state.text += new TextDecoder().decode(bytes);
    });
    state.session = await commands.sessionApi.connectLocal();
    state.tabId = await commands.terminalApi.attach(state.session.id, 80, 24, state.channel);
    state.keeper = events.createBinaryChannel(() => {});
    await commands.terminalApi.attachTab(state.tabId, state.keeper, 0);
    const markerNonce = Date.now();
    state.marker = 'nx-early-' + markerNonce;
    await commands.terminalApi.write(state.tabId, new TextEncoder().encode("printf 'nx-early-%s\\n' '" + markerNonce + "'\\r"));
    return true;
  })()`);
  await pass("ws-early-frame", async () => {
    const evidence = await page.waitFor(`window.__nxAcceptance.text.includes(window.__nxAcceptance.marker) && window.__nxAcceptance.frames > 0`);
    assert.equal(evidence, true);
    return { evidence: await page.evaluate(`({ frames: __nxAcceptance.frames, marker: __nxAcceptance.marker })`) };
  });
  await pass("ws-replay", async () => {
    await page.evaluate(`(async () => {
      const s = __nxAcceptance;
      await s.commands.terminalApi.detach(s.tabId, s.events.channelIdOf(s.channel));
      s.events.disposeChannel(s.channel);
      s.text = ''; s.frames = 0;
      s.channel = s.events.createBinaryChannel((bytes) => { s.frames += 1; s.text += new TextDecoder().decode(bytes); });
      await s.commands.terminalApi.attachTab(s.tabId, s.channel, 65536);
      return true;
    })()`);
    await page.waitFor(`__nxAcceptance.text.includes(__nxAcceptance.marker)`);
    return { evidence: await page.evaluate(`({ replayFrames: __nxAcceptance.frames })`) };
  });
  await pass("ws-reconnect-replay", async () => {
    await page.evaluate(`(() => {
      const s = __nxAcceptance;
      s.text = ''; s.frames = 0;
      s.offReopen = s.events.onChannelReopen(s.channel, async () => {
        s.reopened += 1;
        await s.commands.terminalApi.attachTab(s.tabId, s.channel, 65536);
        s.reconnectAttached += 1;
      });
      return true;
    })()`);
    const dropped = await page.evaluate(`(() => {
      let n = 0;
      const channelId = __nxAcceptance.events.channelIdOf(__nxAcceptance.channel);
      for (const ws of window.__nxSockets) {
        if ((ws.readyState === 0 || ws.readyState === 1) && ws.url.includes('/ws/channel/' + channelId)) { ws.close(); n += 1; }
      }
      return n;
    })()`);
    await page.waitFor(`__nxAcceptance.reopened > 0 && __nxAcceptance.reconnectAttached > 0 && __nxAcceptance.text.includes(__nxAcceptance.marker)`, 35_000);
    const evidence = await page.evaluate(`({ reopened: __nxAcceptance.reopened, reconnectAttached: __nxAcceptance.reconnectAttached })`);
    return { evidence: { ...evidence, droppedSockets: dropped } };
  });
}

async function rpcAcceptance(page) {
  for (const status of [401, 403, 404]) {
    await pass(`rpc-${status}-non-retry`, async () => {
      let attempts = 0;
      const off = page.on("Fetch.requestPaused", (event) => {
        if (!event.request.url.includes("/rpc") || event.request.method !== "POST") {
          page.send("Fetch.continueRequest", { requestId: event.requestId }).catch(() => {});
          return;
        }
        attempts += 1;
        const code = status === 404 ? "not_found" : "forbidden";
        const body = Buffer.from(JSON.stringify({ ok: false, error: { code, message: `browser-${status}` } })).toString("base64");
        page.send("Fetch.fulfillRequest", {
          requestId: event.requestId, responseCode: status,
          responseHeaders: [{ name: "Content-Type", value: "application/json" }, { name: "Access-Control-Allow-Origin", value: "*" }],
          body,
        }).catch(() => {});
      });
      await page.send("Fetch.enable", { patterns: [{ urlPattern: "*/rpc" }] });
      const observed = await page.evaluate(`(async () => { try { await __nxAcceptance.commands.systemApi.platform(); return 'unexpected-success'; } catch (error) { return error.code || 'internal'; } })()`);
      await sleep(400);
      await page.send("Fetch.disable");
      off();
      assert.equal(attempts, 1, `HTTP ${status} was retried`);
      assert.equal(observed, status === 404 ? "not_found" : "forbidden");
      return { evidence: { attempts, observed } };
    });
  }
  await pass("rpc-network-5xx-jitter-recovery", async () => {
    const attempts = { network: 0, server500: 0 };
    let mode = "network";
    const off = page.on("Fetch.requestPaused", (event) => {
      if (!event.request.url.includes("/rpc") || event.request.method !== "POST") {
        page.send("Fetch.continueRequest", { requestId: event.requestId }).catch(() => {});
        return;
      }
      if (mode === "network") {
        attempts.network += 1;
        page.send("Fetch.failRequest", { requestId: event.requestId, errorReason: "ConnectionReset" }).catch(() => {});
      } else {
        attempts.server500 += 1;
        const body = Buffer.from(JSON.stringify({ ok: false, error: { code: "internal", message: "5xx jitter" } })).toString("base64");
        page.send("Fetch.fulfillRequest", {
          requestId: event.requestId, responseCode: 500,
          responseHeaders: [{ name: "Content-Type", value: "application/json" }, { name: "Access-Control-Allow-Origin", value: "*" }], body,
        }).catch(() => {});
      }
    });
    await page.send("Fetch.enable", { patterns: [{ urlPattern: "*/rpc" }] });
    const networkError = await page.evaluate(`(async () => { try { await __nxAcceptance.commands.systemApi.platform(); return 'unexpected'; } catch (error) { return error.code || 'internal'; } })()`);
    mode = "server500";
    const serverError = await page.evaluate(`(async () => { try { await __nxAcceptance.commands.systemApi.platform(); return 'unexpected'; } catch (error) { return error.code || 'internal'; } })()`);
    await sleep(400);
    await page.send("Fetch.disable");
    off();
    const recovered = await page.evaluate(`__nxAcceptance.commands.systemApi.platform()`);
    assert.deepEqual(attempts, { network: 1, server500: 1 });
    assert.equal(networkError, "internal");
    assert.equal(serverError, "internal");
    assert.equal(typeof recovered, "string");
    return { evidence: { attempts, networkError, serverError, recovered } };
  });
}

async function authOnRpcAcceptance(page, authServer) {
  await pass("rpc-auth-on-csrf", async () => {
    await page.navigate(`${authServer.origin}/healthz`);
    await page.evaluate(`window.__NEXTERM_TRANSPORT__ = 'web'; true`);
    const session = await page.evaluate(`(async () => {
      const { authApi } = await import('${VITE}/src/ipc/authApi.ts');
      const created = await authApi.init({
        code: ${JSON.stringify(authServer.initCode)},
        username: "acceptance",
        password: "acceptance-pw-123",
        dekEnvelope: new Uint8Array([1, 2, 3]),
        kdfSalt: new Uint8Array([4, 5, 6]),
        kdfParams: '{"t":3,"m":65536,"p":4}',
        recoveryEnvelope: new Uint8Array([7, 8, 9]),
        recoveryHash: "acceptance-recovery-hash",
      });
      return { username: created.user.username, csrf: created.csrf_token.length > 0 };
    })()`);
    assert.deepEqual(session, { username: "acceptance", csrf: true });

    const control = await page.evaluate(`(async () => {
      const res = await fetch('/rpc', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ cmd: 'asset_list', args: null }),
      });
      const body = await res.json().catch(() => null);
      return { status: res.status, code: body?.error?.code ?? null, message: body?.error?.message ?? '' };
    })()`);
    assert.equal(control.status, 403, JSON.stringify(control));
    assert.match(control.message, /CSRF/, JSON.stringify(control));

    const evidence = await page.evaluate(`(async () => {
      const commands = await import('${VITE}/src/ipc/commands.ts');
      const { setCsrfToken } = await import('${VITE}/src/ipc/authApi.ts');
      const assets = await commands.assetApi.list();
      const group = await commands.assetApi.groupCreate('acceptance-' + Date.now());
      setCsrfToken('stale-token');
      const assetsAfterStale = await commands.assetApi.list();
      return {
        assetListIsArray: Array.isArray(assets),
        groupCreated: typeof group.id === 'string' && group.id.length > 0,
        staleTokenRefreshed: Array.isArray(assetsAfterStale),
      };
    })()`);
    assert.deepEqual(evidence, { assetListIsArray: true, groupCreated: true, staleTokenRefreshed: true });
    return { evidence: { ...evidence, negativeControl: control } };
  });
}

async function authOnFilesAcceptance(page, authServer) {
  await pass("files-auth-on-csrf", async () => {
    await page.navigate(`${authServer.origin}/healthz`);
    await page.evaluate(`window.__NEXTERM_TRANSPORT__ = 'web'; true`);
    const session = await page.evaluate(`(async () => {
      const { authApi, getCsrfToken } = await import('${VITE}/src/ipc/authApi.ts');
      // rpc-auth-on-csrf 已建账号时直接登录, 否则用一次性初始化码补建。
      let created;
      try {
        created = await authApi.login('acceptance', 'acceptance-pw-123');
      } catch {
        created = await authApi.init({
          code: ${JSON.stringify(authServer.initCode)},
          username: "acceptance",
          password: "acceptance-pw-123",
          dekEnvelope: new Uint8Array([1, 2, 3]),
          kdfSalt: new Uint8Array([4, 5, 6]),
          kdfParams: '{"t":3,"m":65536,"p":4}',
          recoveryEnvelope: new Uint8Array([7, 8, 9]),
          recoveryHash: "acceptance-recovery-hash",
        });
      }
      return { username: created.user.username, csrf: (getCsrfToken() ?? '').length > 0 };
    })()`);
    assert.deepEqual(session, { username: "acceptance", csrf: true });

    const control = await page.evaluate(`(async () => {
      const stage = await fetch('/files/blob?name=control.bin', { method: 'POST', body: new Uint8Array([1, 2, 3]) });
      const stageBody = await stage.json().catch(() => null);
      const removal = await fetch('/files/blob?id=01J0000000000000000000000', { method: 'DELETE' });
      const removeBody = await removal.json().catch(() => null);
      return {
        stageStatus: stage.status,
        stageMessage: stageBody?.error?.message ?? '',
        removeStatus: removal.status,
        removeMessage: removeBody?.error?.message ?? '',
      };
    })()`);
    assert.equal(control.stageStatus, 403, JSON.stringify(control));
    assert.match(control.stageMessage, /CSRF/, JSON.stringify(control));
    assert.equal(control.removeStatus, 403, JSON.stringify(control));
    assert.match(control.removeMessage, /CSRF/, JSON.stringify(control));

    const evidence = await page.evaluate(`(async () => {
      const webFiles = await import('${VITE}/src/ipc/webFiles.ts');
      const { setCsrfToken } = await import('${VITE}/src/ipc/authApi.ts');
      const png = () => new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])], 'acceptance.png', { type: 'image/png' });
      // headless 无文件选择器: 遮蔽 showSaveFilePicker 走"不可用"分支, 只验预留/删除传输。
      try { Object.defineProperty(window, 'showSaveFilePicker', { value: undefined, configurable: true }); } catch {}

      const staged = await webFiles.stageFile(png());
      const image = await webFiles.uploadImage(png());
      const anonymous = await fetch(image.url, { credentials: 'omit' });
      const publicBytes = anonymous.ok ? (await anonymous.arrayBuffer()).byteLength : 0;
      const limits = await webFiles.fetchImageService();
      const reservedPath = await webFiles.requestSaveTarget('acceptance-target.bin');

      setCsrfToken('stale-token');
      await webFiles.dropStaged(staged.path);
      await webFiles.dropStaged(reservedPath);
      const afterDelete = await fetch('/files/blob?id=' + encodeURIComponent(staged.id));
      const reservedState = await webFiles.deliverStaged(reservedPath);
      return {
        stagedId: staged.id,
        imageUrl: image.url,
        publicDownloadStatus: anonymous.status,
        publicBytes,
        limitsReported: limits !== null && limits.maxBytes > 0 && limits.ownerQuotaBytes > 0 && limits.ttlSeconds > 0,
        reservedPath: typeof reservedPath === 'string' && reservedPath.includes('acceptance-target.bin'),
        deletedGone: afterDelete.status === 404,
        reservedState,
      };
    })()`);
    assert.equal(evidence.publicDownloadStatus, 200, JSON.stringify(evidence));
    assert.equal(evidence.publicBytes, 8, JSON.stringify(evidence));
    assert.equal(evidence.limitsReported, true, JSON.stringify(evidence));
    assert.equal(evidence.reservedPath, true, JSON.stringify(evidence));
    assert.equal(evidence.deletedGone, true, JSON.stringify(evidence));
    assert.equal(evidence.reservedState, "not-staged", JSON.stringify(evidence));
    return { evidence: { ...evidence, negativeControl: control } };
  });
}

async function rpc(origin, cmd, args = {}) {
  const response = await fetch(`${origin}/rpc`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ cmd, args }),
  });
  const body = await response.json();
  if (!body || body.ok !== true) throw new Error(`rpc ${cmd} failed: ${JSON.stringify(body?.error ?? body)}`);
  return body.data;
}

async function bootApp(page, apiOrigin, extraInit = "") {
  const { identifier } = await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `
      try { localStorage.clear(); } catch {}
      window.__NEXTERM_TRANSPORT__ = "web";
      ${extraInit}
    `,
  });
  await page.navigate(`${VITE}/?api=${encodeURIComponent(apiOrigin)}`);
  await page.waitFor("Boolean(document.querySelector('.nx-app'))", 45_000);
  return identifier;
}

async function typeText(page, text) {
  await page.send("Input.insertText", { text });
}

async function pressEnter(page) {
  await page.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13, text: "\r" });
  await page.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13 });
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

async function seedTerminal(page) {
  const outcome = await page.evaluate(`(async () => {
    try {
      const { useUi, openTerminalTab } = await import('/src/app/store.ts');
      const { sessionApi } = await import('/src/ipc/commands.ts');
      const s = await sessionApi.connectLocal();
      const list = useUi.getState().sessions;
      useUi.getState().setSessions([...list.filter((x) => x.id !== s.id), s]);
      await openTerminalTab(s);
      return "ok";
    } catch (e) {
      return JSON.stringify(e).slice(0, 300);
    }
  })()`);
  if (outcome !== "ok") throw new Error(`seedTerminal failed: ${outcome}`);
  await page.waitFor(`Boolean(document.querySelector('.xterm'))`, 30_000);
}

async function focusTerminal(page) {
  await page.waitFor(`Boolean(document.querySelector('.xterm'))`, 15_000);
  const point = await page.evaluate(`(() => {
    const panes = [...document.querySelectorAll('.xterm')];
    const visible = panes.find((pane) => {
      const rect = pane.getBoundingClientRect();
      return rect.width > 10 && rect.height > 10;
    });
    if (!visible) return null;
    const rect = visible.getBoundingClientRect();
    return { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 };
  })()`);
  if (!point) throw new Error("no visible .xterm pane to focus (terminal size disturbed?)");
  for (const type of ["mousePressed", "mouseReleased"]) {
    await page.send("Input.dispatchMouseEvent", { type, x: point.x, y: point.y, button: "left", clickCount: 1 });
  }
  await page.waitFor(`document.activeElement?.classList?.contains("xterm-helper-textarea") === true`, 10_000);
}

async function waitForTranscriptText(port, marker, timeout = 20_000) {
  const origin = `http://127.0.0.1:${port}`;
  const deadline = Date.now() + timeout;
  let last = "not checked";
  while (Date.now() < deadline) {
    try {
      const list = await rpc(origin, "transcript_list", { assetId: "01J0NEXTERMLOCALDEVICE0001" });
      let found = false;
      for (const summary of list) {
        const search = await rpc(origin, "transcript_search", { id: summary.id, query: marker });
        if (search.length > 0) { found = true; break; }
      }
      if (found) return;
      last = `no match yet (${list.length} transcripts)`;
    } catch (error) {
      last = String(error?.message || error);
    }
    await sleep(200);
  }
  throw new Error(`transcript text ${marker} not recorded: ${last}`);
}

async function typeIntoTerminal(page, port, text, marker) {
  let lastError = "not attempted";
  for (let attempt = 0; attempt < 3; attempt++) {
    try {
      await focusTerminal(page);
      await typeText(page, text);
      await pressEnter(page);
      await waitForTranscriptText(port, marker, 8_000);
      return;
    } catch (error) {
      lastError = String(error?.message || error);
    }
  }
  throw new Error(`typing never reached the terminal: ${lastError}`);
}

const TRANSCRIPT_PANEL = `[data-testid="transcript-history"]`;

async function openHistoryPanel(page) {
  await clickSelector(page, 'button[aria-label="终端历史"]');
  await page.waitFor(`Boolean(document.querySelector(${JSON.stringify(TRANSCRIPT_PANEL)}))`);
}

async function transcriptAcceptance(page) {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-browser-transcript-"));
  const marker = `nx-transcript-${Date.now()}`;
  let server = await startServerOn(dataDir);
  const injections = [];
  try {
    injections.push(await bootApp(page, server.origin));
    await page.waitFor(
      `(async () => { const { useUi } = await import('/src/app/store.ts'); return useUi.getState().workspaces !== undefined; })()`,
      30_000,
    );
    await pass("transcript-survives-server-restart", async () => {
      await seedTerminal(page);
      await typeIntoTerminal(page, server.port, `echo ${marker}`, marker);
      const sessions = await rpc(server.origin, "session_list");
      assert.ok(sessions.length >= 1, "expected a connected session");
      await rpc(server.origin, "session_disconnect", { sessionId: sessions[0].id });

      const exited = new Promise((resolve) => server.process.once("exit", resolve));
      stop(server.process);
      await exited;
      server = await startServerOn(dataDir);

      injections.push(await bootApp(page, server.origin));
      await openHistoryPanel(page);
      await page.waitFor(
        `document.querySelector(${JSON.stringify(TRANSCRIPT_PANEL)})?.textContent?.includes("已结束")`,
        20_000,
      );
      await page.evaluate(`(() => {
        const rows = [...document.querySelectorAll(${JSON.stringify(`${TRANSCRIPT_PANEL} tbody tr`)})];
        const row = rows.find((candidate) => candidate.textContent?.includes("已结束")) ?? rows[0];
        row?.click();
      })()`);
      await page.waitFor(
        `document.querySelector(${JSON.stringify(`${TRANSCRIPT_PANEL} pre`)})?.textContent?.includes(${JSON.stringify(marker)})`,
        20_000,
      );
      return { evidence: { marker, restartedOnSameDataDir: true } };
    });
  } finally {
    for (const identifier of injections) {
      await page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier }).catch(() => {});
    }
    stop(server.process);
  }
}

function startAiStub() {
  const state = { requests: [] };
  return freePort().then((port) => new Promise((resolve, reject) => {
    const server = http.createServer((req, res) => {
      if (!req.url.endsWith("/chat/completions")) {
        res.writeHead(404, { "Content-Type": "application/json" });
        res.end("{}");
        return;
      }
      let body = "";
      req.on("data", (chunk) => { body += chunk; });
      req.on("end", () => {
        let text = "";
        try {
          const parsed = JSON.parse(body);
          text = (parsed.messages ?? []).map((m) => (typeof m.content === "string" ? m.content : "")).join("\n");
        } catch {}
        state.requests.push(text);
        const slow = text.includes("slow");
        const marker = slow ? "NXR188S" : "NXR188Q";
        const chunks = slow ? 6 : 2;
        res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-cache", Connection: "keep-alive" });
        let index = 0;
        const write = () => {
          if (index >= chunks) {
            res.write('data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}]}\n\ndata: [DONE]\n\n');
            res.end();
            return;
          }
          res.write(`data: ${JSON.stringify({ choices: [{ delta: { content: `${marker}-${index} ` }, index: 0 }] })}\n\n`);
          index += 1;
          setTimeout(write, slow ? 600 : 25);
        };
        write();
      });
    });
    server.once("error", reject);
    const sockets = new Set();
    server.on("connection", (socket) => {
      sockets.add(socket);
      socket.on("close", () => sockets.delete(socket));
    });
    server.listen(port, "127.0.0.1", () => resolve({
      server,
      port,
      state,
      origin: `http://127.0.0.1:${port}`,
      close: () => {
        for (const socket of sockets) socket.destroy();
        server.close();
      },
    }));
  }));
}

const AI_LOG = `document.querySelector('div[role="log"]')`;
const AI_COMPOSER = `document.querySelector('textarea[aria-label="消息输入"]')`;

async function openAiSidebar(page) {
  const ready = await page.evaluate(`Boolean(${AI_COMPOSER} && ${AI_COMPOSER}.getBoundingClientRect().width > 0)`);
  if (!ready) {
    await page.evaluate(`(() => {
      const rail = document.querySelector('button[aria-label="AI 助手"]');
      if (!rail) throw new Error("AI rail button missing");
      rail.click();
    })()`);
  }
  await page.waitFor(`Boolean(${AI_COMPOSER} && ${AI_COMPOSER}.getBoundingClientRect().width > 0 && document.querySelector('.nx-send-btn:not(.nx-send-btn-stop)'))`);
}

async function typeComposer(page, text) {
  await page.waitFor(`Boolean(document.querySelector('.nx-send-btn:not(.nx-send-btn-stop)'))`);
  await page.evaluate(`(() => {
    const ta = ${AI_COMPOSER};
    ta.focus();
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value").set;
    setter.call(ta, ${JSON.stringify(text)});
    ta.dispatchEvent(new Event("input", { bubbles: true }));
  })()`);
}

async function sendViaEnter(page) {
  const base = { key: "Enter", code: "Enter", windowsVirtualKeyCode: 13 };
  await page.send("Input.dispatchKeyEvent", { type: "keyDown", ...base, text: "\r" });
  await page.send("Input.dispatchKeyEvent", { type: "keyUp", ...base });
}

async function aiLogText(page) {
  return page.evaluate(`(${AI_LOG}?.textContent) ?? ""`);
}

async function aiCountInLog(page, needle) {
  return page.evaluate(`((${AI_LOG}?.textContent) ?? "").split(${JSON.stringify(needle)}).length - 1`);
}

async function aiStreamAcceptance(page) {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-browser-aistream-"));
  const server = await startServerOn(dataDir);
  const stub = await startAiStub();
  const socketsInit = `(() => {
    window.__nxSockets = [];
    const Orig = window.WebSocket;
    window.WebSocket = function (url, protocols) {
      const ws = protocols === undefined ? new Orig(url) : new Orig(url, protocols);
      window.__nxSockets.push(ws);
      return ws;
    };
    window.WebSocket.prototype = Orig.prototype;
    for (const key of ["CONNECTING", "OPEN", "CLOSING", "CLOSED"]) {
      Object.defineProperty(window.WebSocket, key, { value: Orig[key] });
    }
  })(); true`;
  let injection = null;
  try {
    injection = await bootApp(page, server.origin, socketsInit);
    await rpc(server.origin, "ai_model_save", {
      profile: {
        id: "browser-stub",
        name: "Browser Stub",
        baseUrl: `${stub.origin}/v1`,
        apiKey: "stub-key",
        model: "stub-model",
        temperature: 0.3,
        contextWindow: 32768,
        proxy: null,
        stream: true,
      },
    });
    await rpc(server.origin, "ai_model_activate", { id: "browser-stub" });
    await pass("ai-stream-reconnect-replay", async () => {
      await openAiSidebar(page);
      await typeComposer(page, "快速回答");
      await sendViaEnter(page);
      await page.waitFor(`(${AI_LOG}.textContent).includes("NXR188Q-0") && (${AI_LOG}.textContent).includes("NXR188Q-1")`);
      await page.waitFor(`(${AI_LOG}.textContent).includes("本轮已完成")`);

      const completedBefore = await aiCountInLog(page, "本轮已完成");
      await typeComposer(page, "slow 流式恢复");
      await sendViaEnter(page);
      await page.waitFor(`(${AI_LOG}.textContent).includes("NXR188S-0")`);
      await sleep(700);
      const dropped = await page.evaluate(`(() => {
        let n = 0;
        for (const ws of window.__nxSockets) {
          if (ws.readyState === 0 || ws.readyState === 1) { ws.close(); n += 1; }
        }
        return n;
      })()`);
      assert.ok(dropped > 0, "no live WebSocket dropped");
      for (const marker of ["NXR188S-0", "NXR188S-1", "NXR188S-2", "NXR188S-3", "NXR188S-4", "NXR188S-5"]) {
        await page.waitFor(`((${AI_LOG}.textContent).split(${JSON.stringify(marker)}).length - 1) === 1`);
      }
      await page.waitFor(`((${AI_LOG}.textContent).split("本轮已完成").length - 1) > ${completedBefore}`);
      return { evidence: { droppedSockets: dropped, text: (await aiLogText(page)).slice(-200) } };
    });
  } finally {
    if (injection !== null) await page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier: injection }).catch(() => {});
    await stopAndWait(server.process);
    stub.close();
  }
}

const SSH_FIXTURE_DIR = path.join(OUT, "sshfixture");
const SSH_FIXTURE_BIN = path.join(SSH_FIXTURE_DIR, "sshfixture");
const SSH_FIXTURE_USER = "acc";
const SSH_FIXTURE_PASS = "acc-secret";

const SSH_FIXTURE_SOURCE = `package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"

	gossh "golang.org/x/crypto/ssh"
)

func loadOrCreateSigner(keyFile string) (gossh.Signer, error) {
	if data, err := os.ReadFile(keyFile); err == nil {
		block, _ := pem.Decode(data)
		if block != nil {
			if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
				if priv, ok := key.(ed25519.PrivateKey); ok {
					return gossh.NewSignerFromKey(priv)
				}
			}
		}
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return nil, err
	}
	return gossh.NewSignerFromKey(priv)
}

func main() {
	addr, keyFile, user, pass := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	signer, err := loadOrCreateSigner(keyFile)
	if err != nil {
		panic(err)
	}
	config := &gossh.ServerConfig{
		PasswordCallback: func(metadata gossh.ConnMetadata, password []byte) (*gossh.Permissions, error) {
			if metadata.User() == user && string(password) == pass {
				return nil, nil
			}
			return nil, fmt.Errorf("denied")
		},
	}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		panic(err)
	}
	fmt.Printf("READY %s %s\\n", listener.Addr().String(), gossh.FingerprintSHA256(signer.PublicKey()))
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go handle(conn, config)
	}
}

func handle(conn net.Conn, config *gossh.ServerConfig) {
	serverConn, channels, requests, err := gossh.NewServerConn(conn, config)
	if err != nil {
		conn.Close()
		return
	}
	defer serverConn.Close()
	go gossh.DiscardRequests(requests)
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(gossh.UnknownChannelType, "unsupported")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go serveSession(channel, channelRequests)
	}
}

func serveSession(channel gossh.Channel, requests <-chan *gossh.Request) {
	defer channel.Close()
	for request := range requests {
		switch request.Type {
		case "pty-req", "window-change":
			if request.WantReply {
				request.Reply(true, nil)
			}
		case "shell":
			request.Reply(true, nil)
			channel.Write([]byte("nexterm-acc-shell ready\\r\\n"))
			go func() {
				buffer := make([]byte, 4096)
				for {
					n, err := channel.Read(buffer)
					if err != nil {
						return
					}
					if _, err := channel.Write(append([]byte("echo:"), buffer[:n]...)); err != nil {
						return
					}
				}
			}()
		case "exec":
			request.Reply(true, nil)
			channel.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{0}))
			return
		default:
			request.Reply(false, nil)
		}
	}
}
`;

async function buildSshFixture() {
  fs.mkdirSync(SSH_FIXTURE_DIR, { recursive: true });
  fs.writeFileSync(path.join(SSH_FIXTURE_DIR, "main.go"), SSH_FIXTURE_SOURCE);
  fs.writeFileSync(
    path.join(SSH_FIXTURE_DIR, "go.mod"),
    "module nextermaccssh\n\ngo 1.26.0\n\nrequire golang.org/x/crypto v0.57.0\n",
  );
  const tidy = spawnSync("go", ["mod", "tidy"], {
    cwd: SSH_FIXTURE_DIR,
    env: { ...globalThis.process.env, GOFLAGS: "-mod=mod" },
    encoding: "utf8",
  });
  if (tidy.status !== 0) throw new Error(`ssh fixture go mod tidy failed: ${tidy.stderr}`);
  const built = spawnSync("go", ["build", "-o", SSH_FIXTURE_BIN, "."], {
    cwd: SSH_FIXTURE_DIR,
    env: { ...globalThis.process.env, GOFLAGS: "-mod=mod" },
    encoding: "utf8",
  });
  if (built.status !== 0) throw new Error(`ssh fixture build failed: ${built.stderr}`);
}

async function startSshFixture(port, keyFile) {
  const process = spawn(SSH_FIXTURE_BIN, [`127.0.0.1:${port}`, keyFile, SSH_FIXTURE_USER, SSH_FIXTURE_PASS], {
    stdio: ["ignore", "pipe", "pipe"],
  });
  let stdout = "";
  let stderr = "";
  let spawnError = null;
  process.stdout.on("data", (chunk) => { stdout += chunk; });
  process.stderr.on("data", (chunk) => { stderr += chunk; });
  process.on("error", (error) => { spawnError = error; });
  const deadline = Date.now() + 15_000;
  while (Date.now() < deadline) {
    const match = stdout.match(/READY (\S+) (\S+)/);
    if (match) return { process, addr: match[1], fingerprint: match[2] };
    if (spawnError) throw new Error(`ssh fixture spawn failed: ${spawnError}`);
    if (process.exitCode !== null) throw new Error(`ssh fixture exited ${process.exitCode}: ${stderr.slice(-500)}`);
    await sleep(100);
  }
  stop(process);
  throw new Error(`ssh fixture did not report READY; stdout=${stdout.slice(-200)} stderr=${stderr.slice(-500)}`);
}

const HOSTKEY_DIALOG_TEXT = `document.querySelector('[role="alertdialog"] .nx-modal-body')?.textContent ?? ""`;
const HOSTKEY_DIALOG_PRESENT = `Boolean(document.querySelector('[role="alertdialog"]'))`;
const clickHostKeyDialogButton = (label) => `(() => {
  const btn = [...document.querySelectorAll('[role="alertdialog"] .nx-modal-footer button')].find((b) => b.textContent.trim() === ${JSON.stringify(label)});
  if (!btn) return false;
  btn.click();
  return true;
})()`;
const HOSTKEY_TOAST_TEXT = `[...document.querySelectorAll(".nx-toasts button")].map((b) => b.textContent).join("\\n")`;
const HOSTKEY_WS_TAB_COUNT = `document.querySelectorAll('[role="tablist"][aria-label="工作区"] [role="tab"]').length`;
const HOSTKEY_RPC_SESSIONS = `(async () => (await import('${VITE}/src/ipc/commands.ts')).sessionApi.list())()`;
const hostKeyAssetConnected = (assetId) => `(async () => {
  const list = await (${HOSTKEY_RPC_SESSIONS});
  const mine = list.filter((s) => s.assetId === ${JSON.stringify(assetId)});
  return mine.length === 1 && mine[0].status === "connected";
})()`;
const hostKeyKnownFingerprint = (port) => `(async () => {
  const { assetApi } = await import('${VITE}/src/ipc/commands.ts');
  const list = await assetApi.knownHostList();
  const hit = list.find((k) => k.host === "127.0.0.1" && k.port === ${port});
  return hit ? hit.fingerprint : null;
})()`;
const HOSTKEY_LAYOUT_HAS_TERMINAL = `(async () => {
  const { layoutApi } = await import('${VITE}/src/ipc/commands.ts');
  const dto = await layoutApi.get();
  return dto.data != null && JSON.stringify(dto.data).includes('"tabId":"');
})()`;

async function dblclickAssetRow(page, name) {
  await page.waitFor(
    `[...document.querySelectorAll('[role="treeitem"]')].some((el) => el.textContent.includes(${JSON.stringify(name)}))`,
    15_000,
  );
  const ok = await page.evaluate(`(() => {
    const row = [...document.querySelectorAll('[role="treeitem"]')].find((el) => el.textContent.includes(${JSON.stringify(name)}));
    if (!row) return false;
    row.dispatchEvent(new MouseEvent("dblclick", { bubbles: true, cancelable: true }));
    return true;
  })()`);
  if (!ok) throw new Error(`asset row not found: ${name}`);
}

async function clickRailNewTerminal(page) {
  const ok = await page.evaluate(`(() => {
    const el = document.querySelector('.nx-rail button[aria-label="新建终端"]');
    if (!el) return false;
    el.click();
    return true;
  })()`);
  if (!ok) throw new Error("rail 新建终端 not found");
}

async function seedHostKeyAsset(page, serverOrigin, sshPort) {
  await page.navigate(`${serverOrigin}/healthz`);
  await page.evaluate(`window.__NEXTERM_TRANSPORT__ = 'web'; true`);
  return page.evaluate(`(async () => {
    const commands = await import('${VITE}/src/ipc/commands.ts');
    const vault = await commands.vaultApi.status();
    if (!vault.initialized || !vault.unlocked) throw new Error('vault not ready: ' + JSON.stringify(vault));
    const credential = await commands.vaultApi.setCredential('acc-ssh', 'password', ${JSON.stringify(SSH_FIXTURE_PASS)});
    const asset = await commands.assetApi.create({
      kind: 'ssh',
      name: 'acc-ssh',
      host: '127.0.0.1',
      port: ${sshPort},
      username: ${JSON.stringify(SSH_FIXTURE_USER)},
      authKind: 'password',
      credId: credential.id,
    });
    return { assetId: asset.id, credId: credential.id };
  })()`);
}

async function hostKeyAcceptance(page) {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-browser-hostkey-"));
  const keyFile = path.join(SSH_FIXTURE_DIR, "hostkey");
  await buildSshFixture();
  const sshPort = await freePort();
  let server = await startServerOn(dataDir);
  let ssh = null;
  const injection = await page.send("Page.addScriptToEvaluateOnNewDocument", { source: `window.__NEXTERM_TRANSPORT__ = 'web';` });
  try {
    ssh = await startSshFixture(sshPort, keyFile);
    const { assetId } = await seedHostKeyAsset(page, server.origin, sshPort);
    await pass("hostkey-changed-no-auto-trust", async () => {
      await page.navigate(`${VITE}/?api=${encodeURIComponent(server.origin)}`);
      await page.waitFor(`Boolean(document.querySelector('.nx-rail'))`);
      await dblclickAssetRow(page, "acc-ssh");
      await page.waitFor(HOSTKEY_DIALOG_PRESENT);
      const pendingText = await page.evaluate(HOSTKEY_DIALOG_TEXT);
      assert.ok(pendingText.includes("首次连接 127.0.0.1:"), `pending dialog must introduce the first connect: ${pendingText}`);
      assert.ok(pendingText.includes(ssh.fingerprint), `pending dialog must show the presented fingerprint: ${pendingText}`);
      assert.equal(await page.evaluate(clickHostKeyDialogButton("确定")), true);
      await page.waitFor(`${HOSTKEY_WS_TAB_COUNT} === 1`);
      await page.waitFor(`Boolean(document.querySelector('.xterm'))`);
      await page.waitFor(hostKeyAssetConnected(assetId), 20_000);
      await page.waitFor(HOSTKEY_LAYOUT_HAS_TERMINAL, 15_000);

      await stopAndWait(ssh.process);
      fs.rmSync(keyFile, { force: true });
      ssh = await startSshFixture(sshPort, keyFile);
      const exited = new Promise((resolve) => server.process.once("exit", resolve));
      stop(server.process);
      await exited;
      server = await startServerOn(dataDir);

      await page.navigate(`${VITE}/?api=${encodeURIComponent(server.origin)}`);
      await page.waitFor(`Boolean(document.querySelector('.nx-rail'))`);
      await page.waitFor(`${HOSTKEY_WS_TAB_COUNT} >= 1`, 15_000);
      const sessionsBefore = await page.evaluate(HOSTKEY_RPC_SESSIONS);
      assert.equal(
        sessionsBefore.filter((s) => s.assetId === assetId).length,
        0,
        `changed-key host must not auto-reconnect after restart: ${JSON.stringify(sessionsBefore)}`,
      );

      await clickRailNewTerminal(page);
      await page.waitFor(HOSTKEY_DIALOG_PRESENT);
      const changedText = await page.evaluate(HOSTKEY_DIALOG_TEXT);
      const knownFp = await page.evaluate(hostKeyKnownFingerprint(sshPort));
      assert.ok(changedText.includes("主机密钥已变更 127.0.0.1:"), `changed dialog must warn: ${changedText}`);
      assert.ok(knownFp && changedText.includes(knownFp), `changed dialog must show the previous fingerprint ${knownFp}: ${changedText}`);
      assert.ok(changedText.includes(ssh.fingerprint), `changed dialog must show the new fingerprint: ${changedText}`);

      assert.equal(await page.evaluate(clickHostKeyDialogButton("取消")), true);
      await page.waitFor(`!document.querySelector('[role="alertdialog"]')`);
      await page.waitFor(`${HOSTKEY_TOAST_TEXT}.includes("已取消重连")`);
      const afterCancel = await page.evaluate(HOSTKEY_RPC_SESSIONS);
      assert.equal(
        afterCancel.filter((s) => s.assetId === assetId).length,
        0,
        `cancel must not reconnect the asset: ${JSON.stringify(afterCancel)}`,
      );

      await clickRailNewTerminal(page);
      await page.waitFor(HOSTKEY_DIALOG_PRESENT);
      assert.equal(await page.evaluate(clickHostKeyDialogButton("确定")), true);
      await page.waitFor(hostKeyAssetConnected(assetId), 20_000);
      return { evidence: { pending: pendingText.split("\n")[0], changed: changedText.split("\n")[0], knownFingerprint: knownFp } };
    });
  } finally {
    await page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier: injection }).catch(() => {});
    if (ssh) stop(ssh.process);
    stop(server.process);
  }
}

async function reloadRestoreAcceptance(page) {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-browser-reload-"));
  const marker = `nx-reload-${Date.now()}`;
  const server = await startServerOn(dataDir);
  const injections = [];
  try {
    injections.push(await bootApp(page, server.origin));
    await page.waitFor(
      `(async () => { const { useUi } = await import('/src/app/store.ts'); return useUi.getState().workspaces !== undefined; })()`,
      30_000,
    );
    await pass("reload-restores-terminal-history", async () => {
      await seedTerminal(page);
      await typeIntoTerminal(page, server.port, `echo ${marker}`, marker);
      await page.waitFor(HOSTKEY_LAYOUT_HAS_TERMINAL, 15_000);

      injections.push(await bootApp(page, server.origin));
      const restored = await page.waitFor(`(() => {
        const rows = document.querySelector('.xterm-rows');
        if (!rows || !rows.textContent.includes(${JSON.stringify(marker)})) return false;
        return {
          workspaces: document.querySelectorAll('${WS_TABLIST} [role="tab"]').length,
          paneTabs: [...document.querySelectorAll('${PANE_TABLIST} [role="tab"]')].filter((t) => t.offsetParent !== null).length,
        };
      })()`, 45_000);
      assert.ok(restored.workspaces >= 1, `workspace not restored after reload: ${JSON.stringify(restored)}`);
      assert.ok(restored.paneTabs >= 1, `terminal tab not restored after reload: ${JSON.stringify(restored)}`);
      return { evidence: { marker, ...restored } };
    });
  } finally {
    for (const identifier of injections) {
      await page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier }).catch(() => {});
    }
    stop(server.process);
  }
}

async function main() {
  let vite;
  let server;
  let authServer;
  let chrome;
  let page;
  try {
    [vite, server, authServer, chrome] = await Promise.all([startVite(), startServer(), startAuthOnServer(), startChrome()]);
    page = await newPage(chrome);
    try { await layoutAcceptance(page); } catch (error) { harnessErrors.push(`layout harness: ${error.stack || error}`); }
    try { await narrowPanelAcceptance(page); } catch (error) { harnessErrors.push(`narrow panel harness: ${error.stack || error}`); }
    try { await keyboardAcceptance(page); } catch (error) { harnessErrors.push(`keyboard harness: ${error.stack || error}`); }
    try { await touchAcceptance(page); } catch (error) { harnessErrors.push(`touch harness: ${error.stack || error}`); }
    try { await wsAcceptance(page, server); } catch (error) { harnessErrors.push(`WS harness: ${error.stack || error}`); }
    try { await rpcAcceptance(page); } catch (error) { harnessErrors.push(`RPC harness: ${error.stack || error}`); }
    await page.evaluate(`(async () => {
      const s = window.__nxAcceptance;
      if (!s) return true;
      try { if (s.channel) { await s.commands.terminalApi.detach(s.tabId, s.events.channelIdOf(s.channel)); s.events.disposeChannel(s.channel); } } catch {}
      try { if (s.keeper) { await s.commands.terminalApi.detach(s.tabId, s.events.channelIdOf(s.keeper)); s.events.disposeChannel(s.keeper); } } catch {}
      try { if (s.session) await s.commands.sessionApi.disconnect(s.session.id); } catch {}
      return true;
    })()`).catch(() => {});
    try { await authOnRpcAcceptance(page, authServer); } catch (error) { harnessErrors.push(`auth=on RPC harness: ${error.stack || error}`); }
    try { await authOnFilesAcceptance(page, authServer); } catch (error) { harnessErrors.push(`auth=on files harness: ${error.stack || error}`); }
    try { await transcriptAcceptance(page); } catch (error) { harnessErrors.push(`transcript harness: ${error.stack || error}`); }
    try { await reloadRestoreAcceptance(page); } catch (error) { harnessErrors.push(`reload restore harness: ${error.stack || error}`); }
    try { await aiStreamAcceptance(page); } catch (error) { harnessErrors.push(`AI stream harness: ${error.stack || error}`); }
    try { await hostKeyAcceptance(page); } catch (error) { harnessErrors.push(`host-key harness: ${error.stack || error}`); }
  } catch (error) {
    harnessErrors.push(String(error?.stack || error));
  } finally {
    if (page) page.close();
    stop(chrome?.process);
    stop(authServer?.process);
    stop(server?.process);
    stop(vite);
  }

  for (const id of EXPECTED) {
    if (!results.has(id)) record(id, "not-run-dependency-failed", { reason: "the required harness did not complete; this is not a pass or an approved skip" });
  }
  const checks = [...results.values()];
  const failed = checks.filter((check) => check.status !== "passed");
  const report = {
    schema_version: 1,
    status: failed.length || harnessErrors.length ? "failed" : "passed-with-explicit-real-target-gaps",
    browser: chrome?.version || { status: "unavailable" },
    execution: { real_browser: true, headless: true, jsdom: false, physical_device: false },
    checks,
    harness_errors: harnessErrors,
    evidence_gaps: REAL_TARGET_GAPS,
    skip_as_pass: false,
  };
  fs.writeFileSync(path.join(OUT, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
  console.warn(`browser acceptance: ${checks.filter((check) => check.status === "passed").length}/${EXPECTED.length} automated checks passed; ${REAL_TARGET_GAPS.length} explicit real-target gaps; report=${path.join(OUT, "report.json")}`);
  if (failed.length || harnessErrors.length) process.exit(1);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  await main();
}
