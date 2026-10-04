#!/usr/bin/env node
// M92 五态与表单真实浏览器验收：错误/重试 vs 真空、保存防重、label 关联、
// 弹层 Escape/Tab 陷阱、明暗双主题 warning 对比度。
// 错误注入：CDP Fetch 域拦截 vite 的 /src/demo/mock.ts 模块响应，包一层
// 可控开关（__NEXTERM_FAIL__ / __NEXTERM_EMPTY__ / __NEXTERM_SLOW__）——
// 不改动任何产品代码。报告与截图写入 target/acceptance-states-forms/。
//
// 运行：node src/test/states-forms-acceptance.mjs
// 需要本机 Chrome/Chromium（CHROME_PATH 可覆盖）与 pnpm（启动 vite dev server）。
import { spawn, spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const OUT = path.join(ROOT, "target/acceptance-states-forms");
const VITE_PORT = Number(process.env.NEXTERM_VITE_PORT || 1422);
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

function startVite() {
  const command = globalThis.process.platform === "win32" ? "pnpm.cmd" : "pnpm";
  const process = spawn(command, ["exec", "vite", "--host", "127.0.0.1", "--port", String(VITE_PORT), "--strictPort"], {
    cwd: ROOT,
    env: { ...globalThis.process.env, NODE_OPTIONS: "" },
    stdio: ["ignore", "pipe", "pipe"],
  });
  return waitHttp(VITE, process).then(() => process);
}

const KEYS = {
  Tab: { key: "Tab", code: "Tab", vk: 9 },
  Enter: { key: "Enter", code: "Enter", vk: 13 },
  Escape: { key: "Escape", code: "Escape", vk: 27 },
};

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
  const code = `Key${letter.toUpperCase()}`;
  const base = { key: letter, code, windowsVirtualKeyCode: vk, modifiers: 2 };
  await page.send("Input.dispatchKeyEvent", { type: "rawKeyDown", ...base });
  await page.send("Input.dispatchKeyEvent", { type: "keyUp", ...base });
  await sleep(60);
}

async function screenshot(page, name) {
  const shot = await page.send("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(path.join(OUT, name), Buffer.from(shot.data, "base64"));
  return name;
}

const MOCK_MARKER = /export\s+async\s+function\s+mockInvoke/;

function injectMockSwitch(source) {
  if (!MOCK_MARKER.test(source)) {
    throw new Error("mock.ts 注入点未命中：vite 变换后的模块不再包含 'export async function mockInvoke'");
  }
  return source.replace(MOCK_MARKER, "async function __nxOrigMockInvoke") + `
export async function mockInvoke(cmd, args) {
  window.__nxMockInjected = true;
  window.__nxCallCounts = window.__nxCallCounts || {};
  window.__nxCallCounts[cmd] = (window.__nxCallCounts[cmd] || 0) + 1;
  const fail = window.__NEXTERM_FAIL__ || [];
  if (fail.includes(cmd)) throw new Error("验收注入失败: " + cmd);
  if ((window.__NEXTERM_EMPTY__ || []).includes(cmd)) return [];
  const slow = window.__NEXTERM_SLOW__ || 0;
  if (slow) await new Promise((resolve) => setTimeout(resolve, slow));
  return __nxOrigMockInvoke(cmd, args);
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
        harnessErrors.push("mock interception: 200 响应未命中注入点（放行，__nxMockInjected 断言会兜底）");
        await passThrough();
        return;
      }
      const injected = injectMockSwitch(source);
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

async function boot(page, { fail = [], theme = null } = {}) {
  const { identifier } = await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `
      try { localStorage.clear(); } catch {}
      window.__NEXTERM_FAIL__ = ${JSON.stringify(fail)};
      window.__NEXTERM_EMPTY__ = [];
      window.__NEXTERM_SLOW__ = 0;
      try { localStorage.setItem("nexterm.theme.v1", ${JSON.stringify(theme)}); } catch {}
    `,
  });
  try {
    await page.navigate(`${VITE}/?demo=1`);
    await page.waitFor(
      "Boolean(document.querySelector('[role=\"tablist\"][aria-label=\"工作区\"] [role=\"tab\"]') && document.querySelector('.xterm'))",
    );
    await page.waitFor("window.__nxMockInjected === true");
  } finally {
    await page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier });
  }
}

async function setSwitches(page, { fail, empty, slow }) {
  await page.evaluate(`(() => {
    if (${JSON.stringify(fail ?? null)} !== null) window.__NEXTERM_FAIL__ = ${JSON.stringify(fail ?? [])};
    if (${JSON.stringify(empty ?? null)} !== null) window.__NEXTERM_EMPTY__ = ${JSON.stringify(empty ?? [])};
    if (${JSON.stringify(slow ?? null)} !== null) window.__NEXTERM_SLOW__ = ${JSON.stringify(slow ?? 0)};
  })()`);
}

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
  let m = String(value).match(/rgba?\(([^)]+)\)/);
  if (m) {
    const parts = m[1].split(",").map((s) => Number(s.trim()));
    return { r: parts[0], g: parts[1], b: parts[2], a: parts.length > 3 ? parts[3] : 1 };
  }
  m = String(value).match(/color\(srgb\s+([\d.]+)\s+([\d.]+)\s+([\d.]+)(?:\s*\/\s*([\d.]+))?\)/);
  if (m) {
    return {
      r: Math.round(Number(m[1]) * 255),
      g: Math.round(Number(m[2]) * 255),
      b: Math.round(Number(m[3]) * 255),
      a: m[4] === undefined ? 1 : Number(m[4]),
    };
  }
  m = String(value).match(/oklab\(\s*(-?[\d.]+)\s+(-?[\d.]+)\s+(-?[\d.]+)(?:\s*\/\s*([\d.]+))?\)/);
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

function contrast(fg, bg) {
  const lf = luminance(fg);
  const lb = luminance(bg);
  const hi = Math.max(lf, lb);
  const lo = Math.min(lf, lb);
  return (hi + 0.05) / (lo + 0.05);
}

function blendOver(fg, bg) {
  return {
    r: fg.r * fg.a + bg.r * (1 - fg.a),
    g: fg.g * fg.a + bg.g * (1 - fg.a),
    b: fg.b * fg.a + bg.b * (1 - fg.a),
    a: 1,
  };
}

const READ_WARNING_BADGE = `(() => {
  const spans = [...document.querySelectorAll("span")].filter(
    (s) => s.textContent?.trim() === "未使用" && s.closest(".nx-row"),
  );
  if (!spans.length) return null;
  const el = spans[0];
  const row = el.closest(".nx-row");
  const layers = [];
  let node = el;
  while (node) {
    layers.push(getComputedStyle(node).backgroundColor);
    node = node.parentElement;
  }
  return {
    color: getComputedStyle(el).color,
    layers,
    selected: row?.classList.contains("is-selected") ?? false,
    fontSize: getComputedStyle(el).fontSize,
  };
})()`;

function compositeBackground(layers) {
  let bg = null;
  for (let i = layers.length - 1; i >= 0; i--) {
    const c = parseColor(layers[i]);
    if (!c || c.a === 0) continue;
    if (!bg) {
      bg = { r: c.r, g: c.g, b: c.b, a: 1 };
      continue;
    }
    bg = blendOver(c, bg);
  }
  return bg;
}

async function measureWarningBadge(page, { select }) {
  if (select) {
    await page.evaluate(`(() => {
      const spans = [...document.querySelectorAll("span")].filter(
        (s) => s.textContent?.trim() === "未使用" && s.closest(".nx-row"),
      );
      spans[0].closest(".nx-row").click();
    })()`);
    await sleep(150);
  }
  const sample = await page.evaluate(READ_WARNING_BADGE);
  if (!sample) throw new Error("未使用 badge not found");
  const fg = parseColor(sample.color);
  const bg = compositeBackground(sample.layers);
  if (!fg || !bg) throw new Error(`cannot parse colors: ${JSON.stringify(sample)}`);
  const effectiveFg = fg.a < 1 ? blendOver(fg, bg) : fg;
  return {
    color: sample.color,
    bg: `rgb(${Math.round(bg.r)}, ${Math.round(bg.g)}, ${Math.round(bg.b)})`,
    selected: sample.selected,
    fontSize: sample.fontSize,
    layers: sample.layers,
    ratio: contrast(effectiveFg, bg),
  };
}

async function openAssetTree(page) {
  await page.evaluate(`(() => {
    const btn = document.querySelector('button[aria-label="资产"]');
    if (!btn) throw new Error("assets rail button not found");
    btn.click();
  })()`);
  await page.waitFor(`Boolean(document.querySelector('button[title="新建资产"]'))`);
}

async function statesFormsAcceptance(page) {
  await enableMockInterception(page);

  await pass("docker-error-retry-vs-empty", async () => {
    await boot(page, { fail: ["docker_ps"] });
    await openAssetTree(page);
    await page.evaluate(`(() => {
      const btn = document.querySelector('button[aria-label="连接 官网 docker"]');
      if (!btn) throw new Error("docker asset connect button not found");
      btn.click();
    })()`);
    await page.waitFor(`document.body.textContent.includes("容器列表加载失败")`);
    const errorState = await page.evaluate(`(() => ({
      hasRetry: [...document.querySelectorAll("button")].some((b) => b.textContent?.trim() === "重试"),
      showsEmptyHint: document.body.textContent.includes("这台主机上还没有容器"),
      errorText: document.body.textContent.match(/容器列表加载失败 · [^重]*/)?.[0] ?? null,
    }))()`);
    assert.equal(errorState.hasRetry, true, "error state must offer retry");
    assert.equal(errorState.showsEmptyHint, false, "failure must not masquerade as empty");
    assert.ok(errorState.errorText?.includes("验收注入失败"), `real error text expected: ${JSON.stringify(errorState)}`);

    await setSwitches(page, { fail: [] });
    await page.evaluate(`[...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "重试").click()`);
    await page.waitFor(`document.body.textContent.includes("api-server")`);
    const recovered = await page.evaluate(`!document.body.textContent.includes("容器列表加载失败")`);
    assert.equal(recovered, true);

    await setSwitches(page, { empty: ["docker_ps"] });
    await page.evaluate(`[...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "刷新").click()`);
    await page.waitFor(`document.body.textContent.includes("这台主机上还没有容器")`);
    const emptyState = await page.evaluate(`!document.body.textContent.includes("容器列表加载失败")`);
    assert.equal(emptyState, true, "true empty must not show the error");
    return { evidence: { errorState, emptyState } };
  });

  await pass("audit-error-retry-vs-empty", async () => {
    await boot(page, { fail: ["audit_query"] });
    await pressCtrl(page, "k", 75);
    await page.waitFor(`Boolean(document.querySelector('[role="dialog"] [role="combobox"]'))`);
    await page.send("Input.insertText", { text: "审计" });
    await page.waitFor(`[...document.querySelectorAll('[role="option"],[role="listbox"] *')].some((el) => el.textContent?.includes('审计'))`);
    await press(page, "Enter");
    await page.waitFor(`document.body.textContent.includes("审计记录加载失败")`);
    const errorState = await page.evaluate(`(() => ({
      hasRetry: [...document.querySelectorAll("button")].some((b) => b.textContent?.trim() === "重试"),
      showsEmptyHint: document.body.textContent.includes("暂无记录"),
    }))()`);
    assert.equal(errorState.hasRetry, true);
    assert.equal(errorState.showsEmptyHint, false, "audit failure must not read as 暂无记录");

    await setSwitches(page, { fail: [] });
    await page.evaluate(`[...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "重试").click()`);
    await page.waitFor(`document.body.textContent.includes("共 ") && !document.body.textContent.includes("审计记录加载失败")`);
    const recoveredRows = await page.evaluate(`document.body.textContent.match(/共 (\\d+) 条/)?.[1] ?? null`);
    assert.ok(recoveredRows && Number(recoveredRows) > 0, `retry should render audit rows: ${recoveredRows}`);

    await setSwitches(page, { empty: ["audit_query"] });
    await page.evaluate(`[...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "刷新").click()`);
    await page.waitFor(`document.body.textContent.includes("暂无记录")`);
    const emptyState = await page.evaluate(`!document.body.textContent.includes("审计记录加载失败")`);
    assert.equal(emptyState, true, "true empty must not show the error");
    return { evidence: { errorState, recoveredRows, emptyState } };
  });

  await pass("credentials-error-retry", async () => {
    await boot(page, { fail: ["vault_list_credentials"] });
    await page.evaluate(`(() => {
      const btn = [...document.querySelectorAll("button")].find((b) => b.getAttribute("aria-label") === "凭据" || b.title === "凭据库（左栏查看）");
      if (!btn) throw new Error("credentials rail button not found");
      btn.click();
    })()`);
    await page.waitFor(`document.body.textContent.includes("凭据列表加载失败")`);
    const errorState = await page.evaluate(`(() => ({
      hasRetry: [...document.querySelectorAll("button")].some((b) => b.textContent?.trim() === "重试"),
      showsEmptyHint: document.body.textContent.includes("暂无凭据"),
    }))()`);
    assert.equal(errorState.hasRetry, true);
    assert.equal(errorState.showsEmptyHint, false);

    await setSwitches(page, { fail: [] });
    await page.evaluate(`[...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "重试").click()`);
    await page.waitFor(`document.body.textContent.includes("条凭据")`);
    return { evidence: { errorState } };
  });

  await pass("warning-contrast-both-themes", async () => {
    const measurements = {};
    for (const theme of ["dark", "light"]) {
      await boot(page, { theme });
      await page.evaluate(`(() => {
        const btn = [...document.querySelectorAll("button")].find((b) => b.getAttribute("aria-label") === "凭据" || b.title === "凭据库（左栏查看）");
        if (!btn) throw new Error("credentials rail button not found");
        btn.click();
      })()`);
      await page.waitFor(`[...document.querySelectorAll("span")].some((s) => s.textContent?.trim() === "未使用")`);
      measurements[`${theme}Canvas`] = await measureWarningBadge(page, { select: false });
      measurements[`${theme}Selected`] = await measureWarningBadge(page, { select: true });
    }
    for (const [label, m] of Object.entries(measurements)) {
      assert.ok(
        m.ratio >= 4.5,
        `${label}: 未使用 contrast ${m.ratio.toFixed(2)}:1 < 4.5:1 (fg=${m.color} bg=${m.bg} font=${m.fontSize})`,
      );
    }
    assert.notEqual(
      measurements.darkCanvas.color,
      measurements.lightCanvas.color,
      "warning foreground must differ between themes",
    );
    await screenshot(page, "warning-contrast-light.png");
    return { evidence: measurements };
  });

  await pass("credential-modal-labels-focus-escape-tab", async () => {
    await boot(page);
    await page.evaluate(`(() => {
      const btn = [...document.querySelectorAll("button")].find((b) => b.getAttribute("aria-label") === "凭据" || b.title === "凭据库（左栏查看）");
      if (!btn) throw new Error("credentials rail button not found");
      btn.click();
    })()`);
    await page.waitFor(`[...document.querySelectorAll("button")].some((b) => b.title === "新建凭据")`);
    await page.evaluate(`document.querySelector('button[title="新建凭据"]').click()`);
    await page.waitFor(`Boolean(document.querySelector('.nx-modal[role="dialog"]'))`);

    const semantics = await page.evaluate(`(() => {
      const modal = document.querySelector('.nx-modal[role="dialog"]');
      const labels = [...modal.querySelectorAll("label")];
      const nameLabel = labels.find((l) => l.textContent?.trim() === "名称");
      const valueLabel = labels.find((l) => l.textContent?.trim() === "值");
      const nameInput = nameLabel ? modal.querySelector('input[id="' + nameLabel.htmlFor + '"]') : null;
      const valueInput = valueLabel ? modal.querySelector('input[id="' + valueLabel.htmlFor + '"]') : null;
      return {
        ariaModal: modal.getAttribute("aria-modal"),
        labelledBy: modal.getAttribute("aria-labelledby"),
        nameAssociated: Boolean(nameInput),
        valueAssociated: Boolean(valueInput),
        valueAutocomplete: valueInput?.autocomplete ?? null,
        focusInName: document.activeElement === nameInput,
      };
    })()`);
    assert.equal(semantics.ariaModal, "true");
    assert.ok(semantics.labelledBy, "dialog must be labelled");
    assert.equal(semantics.nameAssociated, true, "名称 label must be associated");
    assert.equal(semantics.valueAssociated, true, "值 label must be associated");
    assert.equal(semantics.valueAutocomplete, "off");
    assert.equal(semantics.focusInName, true, "initial focus must land in the name input");

    await press(page, "Escape");
    const closedByEscape = await page.evaluate(`!document.querySelector('.nx-modal[role="dialog"]')`);
    assert.equal(closedByEscape, true, "Escape must close the credential modal");

    await page.evaluate(`document.querySelector('button[title="新建凭据"]').click()`);
    await page.waitFor(`Boolean(document.querySelector('.nx-modal[role="dialog"]'))`);
    const trap = await page.evaluate(`(() => {
      const modal = document.querySelector('.nx-modal[role="dialog"]');
      const focusables = [...modal.querySelectorAll("button:not([disabled]), input:not([disabled]), textarea:not([disabled]), select:not([disabled])")];
      focusables[focusables.length - 1].focus();
      return { lastText: document.activeElement?.textContent?.trim() ?? null, count: focusables.length };
    })()`);
    await press(page, "Tab");
    const wrapped = await page.evaluate(`(() => {
      const modal = document.querySelector('.nx-modal[role="dialog"]');
      return { inside: modal.contains(document.activeElement), tag: document.activeElement?.tagName };
    })()`);
    assert.equal(wrapped.inside, true, `Tab must wrap inside the modal (from ${JSON.stringify(trap)})`);
    return { evidence: { semantics, trap, wrapped } };
  });

  await pass("mysql-error-retry", async () => {
    await boot(page, { fail: ["db_schemas"] });
    await openAssetTree(page);
    await page.evaluate(`(() => {
      const btn = document.querySelector('button[aria-label="连接 db-prod"]');
      if (!btn) throw new Error("mysql asset connect button not found");
      btn.click();
    })()`);
    await page.waitFor(`document.body.textContent.includes("数据库列表加载失败")`);
    const errorState = await page.evaluate(`(() => ({
      hasRetry: [...document.querySelectorAll("button")].some((b) => b.textContent?.trim() === "重试"),
      showsEmptyHint: document.body.textContent.includes("这个库里没有表"),
    }))()`);
    assert.equal(errorState.hasRetry, true);
    assert.equal(errorState.showsEmptyHint, false);

    await setSwitches(page, { fail: [] });
    await page.evaluate(`[...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "重试").click()`);
    await page.waitFor(`document.body.textContent.includes("orders")`);
    return { evidence: { errorState } };
  });

  await pass("asset-editor-saving-guard", async () => {
    await boot(page);
    await openAssetTree(page);
    await page.evaluate(`document.querySelector('button[title="新建资产"]').click()`);
    await page.waitFor(`Boolean(document.querySelector('.nx-modal'))`);
    const nameInput = await page.evaluate(`(() => {
      const modal = document.querySelector('.nx-modal');
      const input = modal.querySelector("input.nx-input");
      input.focus();
      return input.id || true;
    })()`);
    assert.ok(nameInput);
    await page.send("Input.insertText", { text: "web-99" });
    await setSwitches(page, { slow: 900 });
    await page.evaluate(`(() => {
      const modal = document.querySelector('.nx-modal');
      const btn = [...modal.querySelectorAll("button")].find((b) => b.textContent?.trim() === "保存");
      btn.click();
      btn.click();
      btn.click();
    })()`);
    await sleep(150);
    const inFlight = await page.evaluate(`(() => {
      const modal = document.querySelector('.nx-modal');
      const btn = [...modal.querySelectorAll("button")].find((b) => b.textContent?.trim() === "保存中…" || b.textContent?.trim() === "保存");
      return { disabled: btn?.disabled, text: btn?.textContent?.trim() };
    })()`);
    assert.equal(inFlight.disabled, true, "save button must disable while saving");
    assert.equal(inFlight.text, "保存中…");
    await page.waitFor(`(window.__nxCallCounts?.asset_create ?? 0) >= 1`);
    const calls = await page.evaluate(`window.__nxCallCounts?.asset_create ?? 0`);
    assert.equal(calls, 1, `double-click must not duplicate asset_create (got ${calls})`);
    await page.waitFor(`!document.querySelector('.nx-modal')`, 15_000);
    await page.waitFor(`document.body.textContent.includes("web-99")`, 15_000);
    const savedToast = await page.evaluate(`document.body.textContent.includes("web-99")`);
    assert.equal(savedToast, true, "asset should appear in the tree after the slow save lands");
    await setSwitches(page, { slow: 0 });
    return { evidence: { inFlight, calls } };
  });

  await screenshot(page, "states-forms-final.png");
}

let vite;
let chrome;
let page;
try {
  [vite, chrome] = await Promise.all([startVite(), startChrome()]);
  page = await newPage(chrome);
  await statesFormsAcceptance(page);
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
    error_injection: "CDP Fetch interception of /src/demo/mock.ts with __NEXTERM_FAIL__/__NEXTERM_EMPTY__/__NEXTERM_SLOW__ switches; no product code changed",
  },
  checks,
  harness_errors: harnessErrors,
};
fs.writeFileSync(path.join(OUT, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
console.warn(`states/forms acceptance: ${checks.filter((check) => check.status === "passed").length}/${checks.length} checks passed; report=${path.join(OUT, "report.json")}`);
if (failed.length || harnessErrors.length) process.exit(1);
