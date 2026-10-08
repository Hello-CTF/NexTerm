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
const OUT = path.join(ROOT, "target/acceptance-ai-responsive");
const VITE_PORT = Number(process.env.NEXTERM_VITE_PORT || 1423);
const VITE = `http://127.0.0.1:${VITE_PORT}`;
const MOCK_MARKER = /export async function mockInvoke/;
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
    this.listeners = new Map();
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
    if (!this.listeners.has(method)) this.listeners.set(method, []);
    this.listeners.get(method).push(handler);
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
      await sleep(120);
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
  const page = await CDP.connect((await response.json()).webSocketDebuggerUrl);
  wireEventListeners(page);
  return page;
}

function wireEventListeners(page) {
  const socket = page.socket;
  socket.addEventListener("message", (event) => {
    const message = JSON.parse(String(event.data));
    if (message.id) return;
    const handlers = page.listeners?.get(message.method) ?? [];
    for (const handler of handlers) handler(message.params ?? {});
  });
}

function injectAiEventPush(source) {
  if (!MOCK_MARKER.test(source)) {
    throw new Error("mock.ts 注入点未命中：vite 变换后的模块不再包含 'export async function mockInvoke'");
  }
  return source.replace(MOCK_MARKER, "async function __nxOrigMockInvoke") + `
export async function mockInvoke(cmd, args) {
  return __nxOrigMockInvoke(cmd, args);
}
export function __pushAiEventForTest(evt) {
  return (async () => {
    for (let i = 0; i < 50; i++) {
      const ch = [...aiChannels.values()].at(-1);
      if (ch) {
        pushEvent(ch, evt);
        return;
      }
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
    throw new Error("no active ai channel");
  })();
}
`;
}

async function enableMockInterception(page) {
  await page.send("Fetch.enable", {
    patterns: [{ urlPattern: `*/src/demo/mock.ts*`, requestStage: "Response" }],
  });
  page.on("Fetch.requestPaused", async (params) => {
    const passThrough = async () => {
      try {
        await page.send("Fetch.continueRequest", { requestId: params.requestId });
      } catch {}
    };
    try {
      if (params.responseStatusCode !== 200) {
        await passThrough();
        return;
      }
      const body = await page.send("Fetch.getResponseBody", { requestId: params.requestId });
      const source = Buffer.from(body.body, body.base64Encoded ? "base64" : "utf8").toString("utf8");
      if (!MOCK_MARKER.test(source)) {
        harnessErrors.push("mock interception: 200 响应未命中注入点（放行）");
        await passThrough();
        return;
      }
      const injected = injectAiEventPush(source);
      await page.send("Fetch.fulfillRequest", {
        requestId: params.requestId,
        responseCode: params.responseStatusCode,
        responseHeaders: params.responseHeaders,
        body: Buffer.from(injected, "utf8").toString("base64"),
      });
    } catch (error) {
      harnessErrors.push(`mock interception: ${String(error?.stack || error)}`);
      await passThrough();
    }
  });
}

async function pushAiEvent(page, evt) {
  await page.evaluate(`(async () => {
    const m = await import("/src/demo/mock.ts");
    m.__pushAiEventForTest(${JSON.stringify(evt)});
  })()`);
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

async function screenshot(page, name) {
  const shot = await page.send("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(path.join(OUT, name), Buffer.from(shot.data, "base64"));
  return name;
}

async function boot(page) {
  await page.navigate(`${VITE}/?demo=1`);
  await page.waitFor(
    "Boolean(document.querySelector('[role=\"tablist\"][aria-label=\"工作区\"] [role=\"tab\"]') && document.querySelector('.xterm'))",
  );
}

async function openAi(page) {
  const visible = await page.evaluate(`(() => {
    const dock = document.querySelector(".nx-right-dock");
    const ta = document.querySelector('textarea[aria-label="消息输入"]');
    return Boolean(dock && !dock.className.includes("is-hidden") && ta && ta.getBoundingClientRect().width > 0);
  })()`);
  if (!visible) {
    await page.evaluate(`(() => {
      const rail = document.querySelector('button[aria-label="AI 助手"]');
      if (!rail) throw new Error("AI rail button missing");
      rail.click();
    })()`);
  }
  await page.waitFor(`(() => {
    const dock = document.querySelector(".nx-right-dock");
    const ta = document.querySelector('textarea[aria-label="消息输入"]');
    const idle = document.querySelector(".nx-send-btn:not(.nx-send-btn-stop)");
    return Boolean(dock && !dock.className.includes("is-hidden") && ta && ta.getBoundingClientRect().width > 0 && idle);
  })()`);
}

const COMPOSER = `document.querySelector('textarea[aria-label="消息输入"]')`;
const LOG = `document.querySelector('div[role="log"]')`;

async function typeComposer(page, text) {
  await page.evaluate(`(() => {
    const ta = ${COMPOSER};
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
  await sleep(60);
}

async function setTheme(page, theme) {
  await page.evaluate(`(() => { document.documentElement.dataset.nxTheme = ${JSON.stringify(theme)}; })()`);
  await sleep(120);
}

const CONTRAST_HELPERS = `
function oklabToSrgb(L, a, b) {
  const l_ = L + 0.3963377774 * a + 0.2158037573 * b;
  const m_ = L - 0.1055613458 * a - 0.0638541728 * b;
  const s_ = L - 0.0894841775 * a - 1.291485548 * b;
  const l = l_ ** 3;
  const m = m_ ** 3;
  const s = s_ ** 3;
  const toGamma = (v) => {
    const clamped = Math.min(1, Math.max(0, v));
    return Math.round(255 * (clamped <= 0.0031308 ? 12.92 * clamped : 1.055 * clamped ** (1 / 2.4) - 0.055));
  };
  return {
    r: toGamma(4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s),
    g: toGamma(-1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s),
    b: toGamma(-0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s),
  };
}
function parseColor(value) {
  let m = String(value).match(/rgba?\\(([^)]+)\\)/);
  if (m) {
    const parts = m[1].split(",").map((s) => Number(s.trim()));
    return { r: parts[0], g: parts[1], b: parts[2], a: parts.length > 3 ? parts[3] : 1 };
  }
  m = String(value).match(/color\\(srgb\\s+([\\d.]+)\\s+([\\d.]+)\\s+([\\d.]+)(?:\\s*\\/\\s*([\\d.]+))?\\)/);
  if (m) {
    return {
      r: Math.round(Number(m[1]) * 255),
      g: Math.round(Number(m[2]) * 255),
      b: Math.round(Number(m[3]) * 255),
      a: m[4] === undefined ? 1 : Number(m[4]),
    };
  }
  m = String(value).match(/oklab\\(\\s*(-?[\\d.]+)\\s+(-?[\\d.]+)\\s+(-?[\\d.]+)(?:\\s*\\/\\s*([\\d.]+))?\\)/);
  if (m) {
    return { ...oklabToSrgb(Number(m[1]), Number(m[2]), Number(m[3])), a: m[4] === undefined ? 1 : Number(m[4]) };
  }
  return null;
}
function luminance({ r, g, b }) {
  const channels = [r, g, b].map((v) => {
    const s = v / 255;
    return s <= 0.04045 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
  });
  return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2];
}
function blendOver(fg, bg) {
  return {
    r: fg.r * fg.a + bg.r * (1 - fg.a),
    g: fg.g * fg.a + bg.g * (1 - fg.a),
    b: fg.b * fg.a + bg.b * (1 - fg.a),
    a: 1,
  };
}
function effectiveBg(el) {
  const layers = [];
  for (let node = el; node; node = node.parentElement) layers.push(getComputedStyle(node).backgroundColor);
  layers.reverse();
  let bg = { r: 0, g: 0, b: 0, a: 1 };
  for (const layer of layers) {
    const c = parseColor(layer);
    if (c && c.a > 0) bg = blendOver(c, bg);
  }
  return bg;
}
function contrastOf(el) {
  const fg = parseColor(getComputedStyle(el).color);
  const bg = effectiveBg(el);
  const lf = luminance(fg);
  const lb = luminance(bg);
  return (Math.max(lf, lb) + 0.05) / (Math.min(lf, lb) + 0.05);
}
`;

async function composerReachable(page, vw, vh) {
  return page.evaluate(`(() => {
    const btn = document.querySelector('.nx-send-btn');
    if (!btn) return { send: false };
    const r = btn.getBoundingClientRect();
    return {
      send: true,
      right: Math.round(r.right * 10) / 10,
      bottom: Math.round(r.bottom * 10) / 10,
      visible: r.width >= 20 && r.height >= 20 && r.left >= 0 && r.right <= innerWidth && r.top >= 0 && r.bottom <= innerHeight,
      pageOverflowX: document.documentElement.scrollWidth - innerWidth,
    };
  })()`).then((ev) => {
    assert.equal(ev.send, true, "send button missing");
    assert.equal(ev.visible, true, `send button offscreen at ${vw}x${vh}: ${JSON.stringify(ev)}`);
    assert.ok(ev.pageOverflowX <= 1, `page horizontal overflow ${ev.pageOverflowX}px at ${vw}x${vh}`);
    return ev;
  });
}

async function drainRun(page) {
  await page.evaluate(`document.querySelector(".nx-send-btn-stop")?.click(); true`);
  await page.waitFor(`Boolean(document.querySelector('.nx-send-btn:not(.nx-send-btn-stop)'))`, 10_000);
}

async function landscapeProbe(page) {
  await page.evaluate(`(() => {
    const log = ${LOG};
    const jump = document.querySelector('button[aria-label="回到最新输出"]');
    if (jump) jump.click();
    log.scrollTop = log.scrollHeight;
  })()`);
  await sleep(150);
  return page.evaluate(`(() => {
    const allow = [...document.querySelectorAll("button")].find((b) => b.textContent.trim() === "允许一次");
    const stop = document.querySelector(".nx-send-btn-stop");
    const ta = ${COMPOSER};
    const log = ${LOG};
    const aside = document.querySelector("aside");
    const rect = (el) => { const r = el.getBoundingClientRect(); return { top: Math.round(r.top * 10) / 10, bottom: Math.round(r.bottom * 10) / 10, w: Math.round(r.width * 10) / 10, h: Math.round(r.height * 10) / 10 }; };
    const bodyPre = allow ? allow.closest(".nx-alert").querySelector("pre.overflow-auto, pre") : null;
    return {
      vh: innerHeight,
      asideBottom: Math.round(aside.getBoundingClientRect().bottom * 10) / 10,
      asideOverflowY: aside.scrollHeight - aside.clientHeight,
      logViewport: log.clientHeight,
      logScrollable: log.scrollHeight > log.clientHeight,
      allow: allow ? rect(allow) : null,
      body: bodyPre ? { clientHeight: bodyPre.clientHeight, scrollHeight: bodyPre.scrollHeight } : null,
      stop: stop ? rect(stop) : null,
      composer: rect(ta),
      pageOverflowX: document.documentElement.scrollWidth - innerWidth,
    };
  })()`);
}

function assertLandscape(ev, label) {
  assert.ok(ev.logViewport >= 60, `${label}: log viewport ${ev.logViewport}px < 60: ${JSON.stringify(ev)}`);
  assert.equal(ev.logScrollable, true, `${label}: log not scrollable: ${JSON.stringify(ev)}`);
  assert.ok(ev.body && ev.body.clientHeight >= 40, `${label}: confirm body viewport ${JSON.stringify(ev.body)} < 40`);
  assert.ok(ev.allow && ev.allow.w >= 20 && ev.allow.bottom <= ev.asideBottom, `${label}: allow button offscreen ${JSON.stringify(ev.allow)}`);
  assert.ok(ev.stop && ev.stop.w >= 20 && ev.stop.bottom <= ev.asideBottom, `${label}: stop button off aside ${JSON.stringify(ev.stop)}`);
  assert.ok(ev.composer.w >= 100 && ev.composer.bottom <= ev.asideBottom, `${label}: composer off aside ${JSON.stringify(ev.composer)}`);
  assert.ok(ev.asideOverflowY <= 1, `${label}: aside overflow ${ev.asideOverflowY}px: ${JSON.stringify(ev)}`);
  assert.ok(ev.pageOverflowX <= 1, `${label}: page horizontal overflow ${ev.pageOverflowX}px`);
}

async function acceptance(page) {
  await pass("composer-320-dark", async () => {
    await metrics(page, 320, 568, false);
    await setTheme(page, "dark");
    await openAi(page);
    await typeComposer(page, "你好");
    await sleep(150);
    const ev = await composerReachable(page, 320, 568);
    const shot = await screenshot(page, "composer-320-dark.png");
    return { ev, shot };
  });

  await pass("composer-320-dark-model-dropdown", async () => {
    await page.evaluate(`(() => {
      const chip = document.querySelector('button.nx-chip');
      if (!chip) throw new Error("model chip missing");
      chip.click();
    })()`);
    await page.waitFor(`Boolean(document.querySelector('.nx-menu-title'))`);
    const ev = await page.evaluate(`(() => {
      const dd = document.querySelector('.nx-menu-title').parentElement;
      const r = dd.getBoundingClientRect();
      return { left: Math.round(r.left), right: Math.round(r.right), vw: innerWidth, onscreen: r.left >= 0 && r.right <= innerWidth };
    })()`);
    assert.equal(ev.onscreen, true, `dropdown offscreen: ${JSON.stringify(ev)}`);
    const shot = await screenshot(page, "model-dropdown-320-dark.png");
    await page.evaluate(`document.body.dispatchEvent(new MouseEvent("mousedown", { bubbles: true })); true`);
    await sleep(150);
    return { ev, shot };
  });

  await pass("composer-360-dark", async () => {
    await metrics(page, 360, 640, false);
    await sleep(200);
    const ev = await composerReachable(page, 360, 640);
    return { ev };
  });

  await pass("long-content-wrap-390", async () => {
    await metrics(page, 390, 844, false);
    await openAi(page);
    const token = "a1b2c3d4e5".repeat(22);
    await typeComposer(page, token);
    await sendViaEnter(page);
    await page.waitFor(`Boolean(document.querySelector('div[role="log"] pre.break-words'))`);
    await pushAiEvent(page, { type: "planSubmitted", plan: "## 目标\n重启 mysql-prod，尽量缩短服务中断时间。\n\n## 步骤\n1. 确认状态" });
    await pushAiEvent(page, {
      type: "todos",
      items: [
        { content: "确认目标主机与当前状态", status: "completed" },
        { content: "采集运行数据", status: "in_progress" },
        { content: "执行修复并复查", status: "pending" },
      ],
    });
    await page.waitFor(`[...document.querySelectorAll("span")].some((s) => s.textContent.trim() === "方案已提交")`);
    const ev = await page.evaluate(`(() => {
      const log = document.querySelector('div[role="log"]');
      const pre = log.querySelector('pre.break-words');
      return {
        logOverflowX: log.scrollWidth - log.clientWidth,
        preOverflowX: pre.scrollWidth - pre.clientWidth,
        text: pre.textContent.length,
      };
    })()`);
    assert.ok(ev.logOverflowX <= 1, `log horizontal overflow ${ev.logOverflowX}px`);
    assert.ok(ev.preOverflowX <= 1, `bubble horizontal overflow ${ev.preOverflowX}px`);
    assert.equal(ev.text, 220);
    const shot = await screenshot(page, "long-content-390.png");
    return { ev, shot };
  });

  for (const theme of ["dark", "light"]) {
    await pass(`contrast-${theme}-390`, async () => {
      await setTheme(page, theme);
      await page.waitFor(`[...document.querySelectorAll('div[role="status"], div[role="log"] *')].some((n) => n.textContent && n.textContent.includes('本轮已完成'))`, 20_000);
      await page.waitFor(`[...document.querySelectorAll("span")].some((s) => s.textContent.trim() === "方案已提交")`, 20_000);
      await page.waitFor(`[...document.querySelectorAll("span")].some((s) => s.textContent.trim() === "✓")`, 10_000);
      const ev = await page.evaluate(`(() => {
        ${CONTRAST_HELPERS}
        const out = {};
        const bubblePre = document.querySelector('div[role="log"] pre.break-words');
        if (bubblePre) out.userBubble = contrastOf(bubblePre);
        const summary = [...document.querySelectorAll('summary')].find((s) => s.textContent.includes("思考过程"));
        if (summary) out.reasoningSummary = contrastOf(summary);
        const outcome = [...document.querySelectorAll('div[role="status"]')].find((n) => n.textContent.includes("本轮已完成"));
        if (outcome) out.outcome = contrastOf(outcome);
        const planTitle = [...document.querySelectorAll("span")].find((s) => s.textContent.trim() === "方案已提交");
        if (planTitle) out.planTitle = contrastOf(planTitle);
        const todoCheck = [...document.querySelectorAll("span")].find((s) => s.textContent.trim() === "✓");
        if (todoCheck) out.todoCheck = contrastOf(todoCheck);
        return out;
      })()`);
      for (const [key, value] of Object.entries(ev)) {
        assert.ok(value >= 4.5, `${key} contrast ${value.toFixed(2)} < 4.5 in ${theme}`);
      }
      assert.ok(ev.userBubble && ev.outcome && ev.planTitle && ev.todoCheck, `missing probes: ${JSON.stringify(ev)}`);
      const shot = await screenshot(page, `contrast-${theme}-390.png`);
      return { ev, shot };
    });
  }

  await pass("markdown-highlight-code-390", async () => {
    await metrics(page, 390, 844, false);
    await openAi(page);
    await setTheme(page, "dark");
    const mdCount = await page.evaluate(`document.querySelectorAll('div[role="log"] .nx-md').length`);
    await typeComposer(page, "看看概况");
    await sendViaEnter(page);
    await page.waitFor(
      `document.querySelectorAll('div[role="log"] .nx-md').length > ${mdCount} && Boolean(document.querySelector(".nx-send-btn-stop"))`,
      30_000,
    );
    await pushAiEvent(page, {
      type: "delta",
      text: `\n\n| 指标 | 值 |\n| --- | --- |\n| load | 2.14 |\n| mem | 61% |\n\n${"long-".repeat(400)}\n`,
    });
    await page.waitFor(`Boolean(document.querySelector('div[role="log"] .nx-md-pre'))`, 30_000);
    await page.waitFor(`document.querySelectorAll('div[role="log"] .nx-md-pre-body [class*="nx-tok-"]').length > 0`, 20_000);
    await page.waitFor(`[...document.querySelectorAll('div[role="log"] .nx-md-p')].some((p) => p.textContent.includes("工具结果"))`, 30_000);
    await page.waitFor(`[...document.querySelectorAll('div[role="log"] .nx-md-table')].some((t) => t.textContent.includes("2.14"))`, 10_000);
    const dark = await page.evaluate(`(() => {
      ${CONTRAST_HELPERS}
      const pre = [...document.querySelectorAll('div[role="log"] .nx-md-pre')].at(-1);
      const body = pre.querySelector(".nx-md-pre-body");
      const tokens = [...body.querySelectorAll('[class*="nx-tok-"]')];
      return {
        lang: pre.querySelector(".nx-md-pre-lang").textContent,
        code: body.textContent,
        tokenCount: tokens.length,
        minContrast: Math.min(...tokens.map((t) => contrastOf(t))),
        tableRows: [...document.querySelectorAll('div[role="log"] .nx-md-table')].at(-1).querySelectorAll("tbody tr").length,
        overflowX: document.documentElement.scrollWidth - innerWidth,
      };
    })()`);
    assert.equal(dark.lang, "bash");
    assert.ok(dark.code.includes("$ uptime") && dark.code.includes("$ df -h"), `streamed code incomplete: ${dark.code}`);
    assert.ok(dark.tokenCount > 0, "no highlight tokens after streaming");
    assert.ok(dark.minContrast >= 4.5, `dark token contrast ${dark.minContrast.toFixed(2)} < 4.5`);
    assert.equal(dark.tableRows, 2, `table rows ${dark.tableRows}`);
    assert.ok(dark.overflowX <= 1, `page horizontal overflow ${dark.overflowX}px`);
    await page.evaluate(`(() => {
      window.__nxCopied = null;
      Object.defineProperty(navigator, "clipboard", { value: { writeText: (t) => { window.__nxCopied = t; return Promise.resolve(); } }, configurable: true });
    })()`);
    await page.evaluate(`(() => {
      const pre = [...document.querySelectorAll('div[role="log"] .nx-md-pre')].at(-1);
      pre.querySelector('button[title="复制这段"]').click();
    })()`);
    await page.waitFor(`typeof window.__nxCopied === "string" && window.__nxCopied.includes("$ uptime")`, 10_000);
    await setTheme(page, "light");
    await sleep(150);
    const light = await page.evaluate(`(() => {
      ${CONTRAST_HELPERS}
      const tokens = [...document.querySelectorAll('div[role="log"] .nx-md-pre-body [class*="nx-tok-"]')];
      return Math.min(...tokens.map((t) => contrastOf(t)));
    })()`);
    assert.ok(light >= 4.5, `light token contrast ${light.toFixed(2)} < 4.5`);
    const shot = await screenshot(page, "markdown-highlight-390.png");
    return { dark, light, shot };
  });

  await pass("short-landscape-confirm-568x320", async () => {
    await metrics(page, 568, 320, false);
    await setTheme(page, "dark");
    await openAi(page);
    await typeComposer(page, "重启 mysql-prod");
    await sendViaEnter(page);
    await page.waitFor(`[...document.querySelectorAll("button")].some((b) => b.textContent.trim() === "允许一次")`, 20_000);
    try {
      const ev = await landscapeProbe(page);
      const shot = await screenshot(page, "short-landscape-confirm-568x320.png");
      assertLandscape(ev, "confirm");
      return { ev, shot };
    } catch (error) {
      await drainRun(page);
      throw error;
    }
  });

  await pass("short-landscape-confirm-todos-568x320", async () => {
    try {
      await pushAiEvent(page, {
        type: "todos",
        items: [
          { content: "确认目标主机与当前状态", status: "completed" },
          { content: "采集运行数据（CPU / 内存 / 磁盘）", status: "completed" },
          { content: "定位瓶颈并给出方案", status: "in_progress" },
          { content: "执行修复并复查", status: "pending" },
        ],
      });
      await page.waitFor(`document.querySelector('div[role="log"]').textContent.includes("任务清单")`);
      const ev = await landscapeProbe(page);
      const shot = await screenshot(page, "short-landscape-confirm-todos-568x320.png");
      assertLandscape(ev, "confirm+todos");
      return { ev, shot };
    } catch (error) {
      await drainRun(page);
      throw error;
    }
  });

  await pass("short-landscape-confirm-todos-question-568x320", async () => {
    try {
      await pushAiEvent(page, {
        type: "questionRequired",
        id: "q-1",
        question: { question: "重启前需要先备份吗？", options: ["先备份", "直接重启"] },
        confirmationNonce: "nq-1",
      });
      await page.waitFor(`Boolean(document.querySelector('textarea[aria-label="回答 AI 的问题"]'))`);
      const ev = await landscapeProbe(page);
      const question = await page.evaluate(`(() => {
        const log = ${LOG};
        const ta = document.querySelector('textarea[aria-label="回答 AI 的问题"]');
        ta.scrollIntoView({ block: "nearest" });
        const r = ta.getBoundingClientRect();
        const logRect = log.getBoundingClientRect();
        return {
          textareaVisible: r.top >= logRect.top - 1 && r.bottom <= logRect.bottom + 1 && r.width > 100,
          questionTextVisible: [...document.querySelectorAll("form.nx-alert div")].some((d) => d.textContent.includes("重启前需要先备份吗")),
        };
      })()`);
      assert.equal(question.textareaVisible, true, `question textarea not revealable in log viewport: ${JSON.stringify(question)}`);
      assert.equal(question.questionTextVisible, true, "question text missing");
      const shot = await screenshot(page, "short-landscape-confirm-todos-question-568x320.png");
      assertLandscape(ev, "confirm+todos+question");
      return { ev, question, shot };
    } finally {
      await drainRun(page);
    }
  });

  await pass("touch-targets-390-coarse", async () => {
    await metrics(page, 390, 844, true);
    await openAi(page);
    await typeComposer(page, "你好");
    await sleep(150);
    const ev = await page.evaluate(`(() => {
      const targets = {};
      const pick = (name, el) => {
        if (!el) { targets[name] = null; return; }
        const r = el.getBoundingClientRect();
        targets[name] = { w: Math.round(r.width * 10) / 10, h: Math.round(r.height * 10) / 10 };
      };
      pick("historyBtn", document.querySelector('button[title="历史会话"]'));
      pick("newBtn", document.querySelector('button[title="新建会话"]'));
      pick("collapseBtn", [...document.querySelectorAll("button")].find((b) => (b.title || "").startsWith("收起 AI 侧栏")));
      pick("modelChip", document.querySelector("button.nx-chip"));
      pick("permBtn", [...document.querySelectorAll("button")].find((b) => (b.title || "").startsWith("AI 权限")));
      pick("takeoverBtn", [...document.querySelectorAll("button")].find((b) => (b.title || "").startsWith("终端接管")));
      pick("sendBtn", document.querySelector(".nx-send-btn"));
      pick("usageRing", document.querySelector('button[title="查看用量明细"]'));
      targets.coarse = matchMedia("(pointer: coarse)").matches;
      targets.composerFont = getComputedStyle(${COMPOSER}).fontSize;
      return targets;
    })()`);
    assert.equal(ev.coarse, true, "pointer:coarse not active");
    for (const [key, value] of Object.entries(ev)) {
      if (!value || typeof value !== "object") continue;
      assert.ok(value.w >= 24 && value.h >= 24, `${key} touch target ${value.w}x${value.h} < 24px`);
    }
    assert.equal(ev.composerFont, "16px");
    const shot = await screenshot(page, "touch-targets-390-coarse.png");
    return { ev, shot };
  });

  await pass("usage-ring-focus-details-390", async () => {
    await openAi(page);
    await typeComposer(page, "你好");
    await sendViaEnter(page);
    const RING_BTN = `document.querySelector('button[title="查看用量明细"]')`;
    await page.waitFor(`((${RING_BTN})?.querySelector('span[role="img"]')?.getAttribute("aria-label") || "").includes("上下文占用")`, 10_000);
    const ev = await page.evaluate(`(() => {
      const btn = ${RING_BTN};
      if (!btn) return { ring: false };
      btn.focus();
      const tooltip = btn.parentElement.querySelector("span.hidden");
      const shown = tooltip && getComputedStyle(tooltip).display !== "none";
      return {
        ring: true,
        focused: document.activeElement === btn,
        shown,
        expanded: btn.getAttribute("aria-expanded"),
        label: btn.querySelector('span[role="img"]').getAttribute("aria-label"),
      };
    })()`);
    assert.equal(ev.ring, true);
    assert.equal(ev.focused, true, "usage ring button not focusable");
    assert.equal(ev.shown, true, "tooltip not shown on focus");
    assert.equal(ev.expanded, "false");
    assert.ok(ev.label.includes("上下文占用"), `aria-label incomplete: ${ev.label}`);
    await page.evaluate(`(() => { ${RING_BTN}.click(); })()`);
    await page.waitFor(`[...document.querySelectorAll("span")].some((s) => s.textContent.includes("对话 15 次"))`, 10_000);
    const details = await page.evaluate(`(() => {
      const btn = ${RING_BTN};
      return { expanded: btn.getAttribute("aria-expanded"), text: btn.parentElement.textContent };
    })()`);
    assert.equal(details.expanded, "true", "details not expanded after click");
    assert.ok(details.text.includes("用量明细"), `details panel missing: ${details.text}`);
    assert.ok(details.text.includes("对话 15 次"), `details missing chat totals: ${details.text}`);
    assert.ok(details.text.includes("标题 8 次"), `details missing title totals: ${details.text}`);
    assert.ok(details.text.includes("单独计，不入对话总量"), "details missing title accounting note");
    await page.evaluate(`(() => { ${RING_BTN}.click(); })()`);
    await page.waitFor(`!([...document.querySelectorAll("span")].some((s) => s.textContent.trim() === "用量明细"))`, 10_000);
    await page.evaluate(`document.querySelector(".nx-send-btn-stop")?.click(); true`);
    await page.waitFor(`Boolean(document.querySelector('.nx-send-btn:not(.nx-send-btn-stop)'))`, 10_000);
    return { ev, details };
  });

  await pass("ime-composing-enter-no-send-390", async () => {
    await metrics(page, 390, 844, false);
    await openAi(page);
    await typeComposer(page, "ni'h");
    const ev = await page.evaluate(`(() => {
      const ta = ${COMPOSER};
      ta.focus();
      ta.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true, data: "ni" }));
      ta.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", isComposing: true, bubbles: true, cancelable: true }));
      const afterComposing = { value: ta.value, stop: Boolean(document.querySelector(".nx-send-btn-stop")) };
      ta.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true, data: "你" }));
      const ev229 = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
      Object.defineProperty(ev229, "keyCode", { value: 229 });
      ta.dispatchEvent(ev229);
      const after229 = { value: ta.value, stop: Boolean(document.querySelector(".nx-send-btn-stop")) };
      return { afterComposing, after229 };
    })()`);
    assert.equal(ev.afterComposing.value, "ni'h", "composing Enter cleared the composer");
    assert.equal(ev.afterComposing.stop, false, "composing Enter started a run");
    assert.equal(ev.after229.value, "ni'h", "keyCode 229 Enter cleared the composer");
    assert.equal(ev.after229.stop, false, "keyCode 229 Enter started a run");
    const shotGuard = await screenshot(page, "ime-guard-390.png");
    await sendViaEnter(page);
    await page.waitFor(`Boolean(document.querySelector(".nx-send-btn-stop"))`, 10_000);
    const sent = await page.evaluate(`({ value: ${COMPOSER}.value, stop: Boolean(document.querySelector(".nx-send-btn-stop")) })`);
    assert.equal(sent.value, "", "plain Enter did not send");
    assert.equal(sent.stop, true, "plain Enter did not start a run");
    const shot = await screenshot(page, "ime-enter-390.png");
    await page.evaluate(`document.querySelector(".nx-send-btn-stop")?.click(); true`);
    await page.waitFor(`Boolean(document.querySelector('.nx-send-btn:not(.nx-send-btn-stop)'))`, 10_000);
    return { ev, sent, shotGuard, shot };
  });

  await pass("keyboard-inset-m116-contract-390", async () => {
    const before = await page.evaluate(`({ root: document.querySelector("#root").getBoundingClientRect().height, inner: innerHeight })`);
    await page.evaluate(`(() => {
      const style = document.createElement("style");
      style.textContent = ".nx-statusbar{transform:translateY(calc(-1 * var(--nx-kb-inset, 0px)))}";
      document.head.append(style);
      document.documentElement.style.setProperty("--nx-kb-inset", "344px");
    })()`);
    await sleep(250);
    const ev = await page.evaluate(`(() => {
      const composerBox = ${COMPOSER}.closest("div.shrink-0");
      const btn = document.querySelector(".nx-send-btn");
      const r = btn.getBoundingClientRect();
      const log = ${LOG};
      const aside = document.querySelector("aside");
      const asideBottom = aside.getBoundingClientRect().bottom;
      return {
        paddingBottom: getComputedStyle(composerBox).paddingBottom,
        sendBottom: Math.round(r.bottom * 10) / 10,
        sendW: Math.round(r.width),
        asideBottom: Math.round(asideBottom * 10) / 10,
        statusbarTop: Math.round((asideBottom - 344) * 10) / 10,
        logViewport: log.clientHeight,
        asideOverflowY: aside.scrollHeight - aside.clientHeight,
        root: document.querySelector("#root").getBoundingClientRect().height,
        inner: innerHeight,
      };
    })()`);
    assert.equal(ev.paddingBottom, "354px", `composer padding-bottom ${ev.paddingBottom} != 354px`);
    assert.ok(ev.sendW >= 20 && ev.sendBottom <= ev.statusbarTop + 11, `send not above floating statusbar: ${JSON.stringify(ev)}`);
    assert.ok(ev.logViewport >= 40, `log crushed to ${ev.logViewport}px under real inset`);
    assert.ok(ev.asideOverflowY <= 1, `aside overflow ${ev.asideOverflowY}px under real inset`);
    assert.equal(ev.root, before.root, "root height changed");
    assert.equal(ev.inner, before.inner, "innerHeight changed");
    const shot = await screenshot(page, "keyboard-inset-m116-390.png");
    await page.evaluate(`document.documentElement.style.removeProperty("--nx-kb-inset"); true`);
    return { before, ev, shot };
  });

  await pass("model-panel-320-single-column", async () => {
    await metrics(page, 320, 568, false);
    await setTheme(page, "dark");
    await openAi(page);
    await page.evaluate(`(() => {
      const chip = document.querySelector("button.nx-chip");
      chip.click();
    })()`);
    await page.waitFor(`Boolean(document.querySelector('.nx-menu-title'))`);
    await page.evaluate(`(() => {
      const manage = [...document.querySelectorAll("button")].find((b) => b.textContent.includes("管理模型"));
      manage.click();
    })()`);
    await page.waitFor(`Boolean(document.querySelector('.nx-modal input[type="number"]'))`);
    const shot = await screenshot(page, "model-panel-320.png");
    const ev = await page.evaluate(`(() => {
      const modal = document.querySelector(".nx-modal");
      const body = modal.querySelector(".nx-modal-body");
      const temp = modal.querySelector('input[type="number"]');
      const listBox = [...modal.querySelectorAll(".overflow-y-auto")][0];
      const listRect = listBox.getBoundingClientRect();
      const tempRect = temp.getBoundingClientRect();
      return {
        stacked: listRect.bottom <= tempRect.top + 1,
        tempWidth: Math.round(tempRect.width),
        bodyOverflowX: body.scrollWidth - body.clientWidth,
        modalOverflowX: modal.scrollWidth - modal.clientWidth,
        vw: innerWidth,
      };
    })()`);
    assert.equal(ev.stacked, true, `model panel not stacked at 320px: ${JSON.stringify(ev)}`);
    assert.ok(ev.tempWidth >= 120, `temperature input squeezed to ${ev.tempWidth}px`);
    assert.ok(ev.bodyOverflowX <= 1 && ev.modalOverflowX <= 1, `modal horizontal overflow: ${JSON.stringify(ev)}`);
    return { ev, shot };
  });
}

async function main() {
  const vite = await startVite({ root: ROOT, port: VITE_PORT });
  const chrome = await startChrome();
  const page = await newPage(chrome);
  try {
    await enableMockInterception(page);
    await boot(page);
    await acceptance(page);
  } finally {
    page.close();
    stop(chrome.process);
    await vite?.stop();
  }
  const failed = [...results.values()].filter((r) => r.status === "failed");
  const report = { at: new Date().toISOString(), harnessErrors, results: Object.fromEntries(results) };
  fs.writeFileSync(path.join(OUT, "report.json"), JSON.stringify(report, null, 2));
  try {
    fs.rmSync(chrome.profile, { recursive: true, force: true, maxRetries: 8, retryDelay: 250 });
  } catch {}
  console.warn(`\n${results.size - failed.length}/${results.size} passed; report at ${path.join(OUT, "report.json")}`);
  if (harnessErrors.length > 0) console.warn(`harness errors: ${harnessErrors.length}`);
  if (failed.length > 0) process.exitCode = 1;
}

await main();
