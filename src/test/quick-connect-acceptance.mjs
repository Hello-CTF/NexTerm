#!/usr/bin/env node
// M158 quick-connect 真实浏览器验收：WEB transport + CDP Fetch 拦截 /rpc  mock
// 后端（不触碰 src/demo），全部交互只通过 Input.dispatchKeyEvent /
// Input.insertText 完成，断言只读取 DOM/ARIA 属性。
//
// 运行：node src/test/quick-connect-acceptance.mjs
// 需要本机 Chrome/Chromium（CHROME_PATH 可覆盖）与 pnpm（启动 vite dev server）。
// 报告与截图写入 target/acceptance-quick-connect/。
import { spawn, spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const OUT = path.join(ROOT, "target/acceptance-quick-connect");
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
    this.listeners = new Map();
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
      if (message.method) {
        for (const handler of this.listeners.get(message.method) ?? []) handler(message.params ?? {});
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

  on(method, handler) {
    if (!this.listeners.has(method)) this.listeners.set(method, []);
    this.listeners.get(method).push(handler);
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

  async waitFor(expression, timeout = 15_000) {
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

async function pressChord(page, key, code, vk, modifiers) {
  const base = { key, code, windowsVirtualKeyCode: vk, modifiers };
  await page.send("Input.dispatchKeyEvent", { type: "rawKeyDown", ...base });
  await page.send("Input.dispatchKeyEvent", { type: "keyUp", ...base });
  await sleep(80);
}

const openQuickConnect = (page) => pressChord(page, "K", "KeyK", 75, 10);
const openPalette = (page) => pressChord(page, "P", "KeyP", 80, 10);

async function insertText(page, text) {
  await page.send("Input.insertText", { text });
  await sleep(60);
}

async function replaceText(page, text) {
  await pressChord(page, "a", "KeyA", 65, 2);
  await insertText(page, text);
}

async function screenshot(page, name) {
  const shot = await page.send("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(path.join(OUT, name), Buffer.from(shot.data, "base64"));
  return name;
}

function assetOf(extra) {
  return {
    groupId: null,
    kind: "ssh",
    host: null,
    port: null,
    username: "root",
    authKind: "password",
    keyPath: null,
    credId: null,
    options: {},
    tags: "",
    note: "",
    sort: 0,
    createdAt: 1,
    updatedAt: 1,
    deletedAt: null,
    builtin: false,
    ...extra,
  };
}

const ASSETS = [
  assetOf({ id: "a-bad", name: "bad-01", host: "10.0.0.66", port: 22 }),
  assetOf({ id: "a-db", name: "db-01", kind: "mysql", host: "10.0.0.9", port: 3306 }),
  assetOf({ id: "a-web", name: "web-01", host: "10.0.0.8", port: 22 }),
  assetOf({ id: "a-local", name: "当前设备", kind: "local", builtin: true }),
];

const PROBES = {
  "a-web": { reachable: true, durationMs: 11 },
  "a-db": { reachable: false, error: "dial tcp 10.0.0.9:3306: i/o timeout", durationMs: 2500 },
  "a-bad": { reachable: true, durationMs: 8 },
};

const rpcState = {
  connectCalls: {},
  probeCalls: [],
  sessions: {},
};

async function handleRpc(body) {
  const args = (body.args && body.args.args) || body.args || {};
  switch (body.cmd) {
    case "asset_list":
      return ASSETS;
    case "group_list":
      return [];
    case "snippet_list":
      return [{ id: "sn1", name: "磁盘水位", body: "df -h", groupId: null, sort: 1 }];
    case "vault_status":
      return { initialized: true, mode: "dpapi", unlocked: true, autoLockMinutes: 0 };
    case "session_list":
      return Object.values(rpcState.sessions);
    case "layout_get":
      return { revision: 0, updatedAt: 0, data: null };
    case "asset_probe_batch": {
      rpcState.probeCalls.push(args.assetIds);
      return {
        results: args.assetIds
          .filter((id) => PROBES[id])
          .map((id) => ({ assetId: id, ...PROBES[id] })),
      };
    }
    case "session_connect": {
      const assetId = args.assetId;
      rpcState.connectCalls[assetId] = (rpcState.connectCalls[assetId] ?? 0) + 1;
      const asset = ASSETS.find((a) => a.id === assetId);
      if (assetId === "a-bad") {
        return { error: { code: "io", message: "dial tcp 10.0.0.66:22: connect: connection refused" } };
      }
      if (assetId === "a-web") await sleep(700);
      const info = {
        id: `s-${assetId}`,
        assetId,
        name: asset.name,
        kind: asset.kind,
        status: "connected",
        tabs: [],
        createdAt: Date.now(),
      };
      rpcState.sessions[info.id] = info;
      return info;
    }
    default:
      return null;
  }
}

async function boot(page) {
  await page.send("Page.enable");
  await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `
      window.__NEXTERM_TRANSPORT__ = "web";
      try {
        localStorage.setItem(
          "nexterm.connectHistory.v1",
          JSON.stringify({ "a-db": { count: 1, at: Date.now() } }),
        );
      } catch {}
    `,
  });
  await page.send("Fetch.enable", { patterns: [{ urlPattern: "*/rpc", requestStage: "Request" }] });
  page.on("Fetch.requestPaused", (params) => {
    void (async () => {
      let payload;
      try {
        const body = params.request.postData ? JSON.parse(params.request.postData) : {};
        const result = await handleRpc(body);
        payload =
          result && typeof result === "object" && "error" in result
            ? { ok: false, error: result.error }
            : { ok: true, data: result ?? null };
      } catch (error) {
        harnessErrors.push(`rpc mock: ${String(error?.stack || error)}`);
        payload = { ok: false, error: { code: "internal", message: "mock failure" } };
      }
      try {
        await page.send("Fetch.fulfillRequest", {
          requestId: params.requestId,
          responseCode: 200,
          responseHeaders: [{ name: "content-type", value: "application/json" }],
          body: Buffer.from(JSON.stringify(payload)).toString("base64"),
        });
      } catch (error) {
        harnessErrors.push(`rpc fulfill: ${String(error?.stack || error)}`);
      }
    })();
  });
  await page.navigate(`${VITE}/`);
  await page.waitFor(`Boolean(document.querySelector('[role="tablist"][aria-label="工作区"]'))`);
}

const OPTION_NAMES = `[...document.querySelectorAll('[role="option"]')].map((o) => o.querySelector('.min-w-0')?.textContent)`;
const QUICK_COMBOBOX = `Boolean(document.querySelector('[aria-label="资产搜索"]'))`;

async function waitForConnectCalls(page, assetId, count, timeout = 8_000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if ((rpcState.connectCalls[assetId] ?? 0) >= count) return;
    await sleep(100);
  }
  throw new Error(`session_connect ${assetId} calls ${rpcState.connectCalls[assetId] ?? 0} < ${count}`);
}

async function quickConnectAcceptance(page) {
  await boot(page);

  await pass("overlay-open-search-reachability", async () => {
    await openQuickConnect(page);
    await page.waitFor(QUICK_COMBOBOX);
    const order = await page.evaluate(OPTION_NAMES);
    assert.deepEqual(order, ["db-01", "bad-01", "web-01", "当前设备"]);

    await page.waitFor(
      `[...document.querySelectorAll('[role="option"]')].some((o) => o.querySelector('[aria-label="不可达"]'))`,
    );
    const dots = await page.evaluate(`(() => {
      const row = (name) => [...document.querySelectorAll('[role="option"]')].find((o) => o.textContent.includes(name));
      return {
        db: row("db-01")?.querySelector('[aria-label="不可达"]')?.getAttribute("title") ?? null,
        web: row("web-01")?.querySelector('[aria-label="可达"]')?.getAttribute("title") ?? null,
        localBadge: row("当前设备")?.textContent.includes("本机") ?? false,
      };
    })()`);
    assert.ok(dots.db && dots.db.includes("i/o timeout"), JSON.stringify(dots));
    assert.ok(dots.web && dots.web.includes("11ms"), JSON.stringify(dots));
    assert.equal(dots.localBadge, true);

    await insertText(page, "web");
    await page.waitFor(`[...document.querySelectorAll('[role="option"]')].length === 1`);
    const filtered = await page.evaluate(OPTION_NAMES);
    assert.deepEqual(filtered, ["web-01"]);
    await screenshot(page, "overlay-search.png");
    await press(page, "Escape");
    await page.waitFor(`!${QUICK_COMBOBOX}`);
    return { order, dots };
  });

  await pass("connect-inflight-guard-and-recents", async () => {
    await openQuickConnect(page);
    await page.waitFor(QUICK_COMBOBOX);
    await insertText(page, "web-01");
    await page.waitFor(`[...document.querySelectorAll('[role="option"]')].length === 1`);

    await press(page, "Enter");
    await sleep(150);
    await press(page, "Enter");
    await sleep(300);
    assert.equal(rpcState.connectCalls["a-web"], 1, "duplicate Enter must not start a second connect");
    const busy = await page.evaluate(
      `[...document.querySelectorAll('[role="option"]')].some((o) => o.textContent.includes("连接中"))`,
    );
    assert.equal(busy, true, "row should show in-flight hint");

    await page.waitFor(`!${QUICK_COMBOBOX}`, 10_000);
    assert.equal(rpcState.connectCalls["a-web"], 1);
    await page.waitFor(
      `[...document.querySelectorAll('[role="tab"]')].some((t) => t.textContent.includes("web-01"))`,
    );

    await openQuickConnect(page);
    await page.waitFor(QUICK_COMBOBOX);
    const order = await page.evaluate(OPTION_NAMES);
    assert.equal(order[0], "web-01", "just-connected asset should rank first");
    await press(page, "Escape");
    await page.waitFor(`!${QUICK_COMBOBOX}`);
    return { order };
  });

  await pass("connect-failure-inline-retry", async () => {
    await openQuickConnect(page);
    await page.waitFor(QUICK_COMBOBOX);
    await insertText(page, "bad-01");
    await page.waitFor(`[...document.querySelectorAll('[role="option"]')].length === 1`);

    await press(page, "Enter");
    await page.waitFor(
      `[...document.querySelectorAll('[role="alert"]')].some((el) => el.textContent.includes("connection refused"))`,
    );
    assert.equal(await page.evaluate(QUICK_COMBOBOX), true, "overlay stays open on failure");

    await press(page, "Enter");
    await waitForConnectCalls(page, "a-bad", 2);
    await page.waitFor(
      `[...document.querySelectorAll('[role="alert"]')].some((el) => el.textContent.includes("connection refused"))`,
    );
    await screenshot(page, "overlay-connect-error.png");
    await press(page, "Escape");
    await page.waitFor(`!${QUICK_COMBOBOX}`);
    return { calls: rpcState.connectCalls["a-bad"] };
  });

  await pass("palette-probe-action-and-dot", async () => {
    const beforePalette = rpcState.probeCalls.length;
    await openPalette(page);
    await page.waitFor(`Boolean(document.querySelector('[aria-label="命令或资产搜索"]'))`);
    await page.waitFor(
      `[...document.querySelectorAll('[role="option"]')].some((o) => o.textContent.includes("连接 web-01"))`,
    );
    await (async () => {
      const deadline = Date.now() + 8_000;
      while (rpcState.probeCalls.length <= beforePalette && Date.now() < deadline) await sleep(100);
      assert.ok(rpcState.probeCalls.length > beforePalette, "palette should probe on mount");
    })();
    const dot = await page.evaluate(`(() => {
      const row = [...document.querySelectorAll('[role="option"]')].find((o) => o.textContent.includes("连接 web-01"));
      return row?.querySelector('[aria-label="可达"]')?.getAttribute("title") ?? null;
    })()`);
    assert.ok(dot && dot.includes("11ms"), `palette connect row should carry reachability dot: ${dot}`);

    const probeCallsBefore = rpcState.probeCalls.length;
    await insertText(page, "探测 web-01");
    await page.waitFor(
      `[...document.querySelectorAll('[role="option"]')].some((o) => o.textContent.includes("探测 web-01"))`,
    );
    await press(page, "Enter");
    await page.waitFor(
      `Boolean(document.querySelector('.nx-toasts') && document.querySelector('.nx-toasts').textContent.includes("可达"))`,
    );
    assert.equal(rpcState.probeCalls.length, probeCallsBefore + 1);
    const lastProbe = rpcState.probeCalls[rpcState.probeCalls.length - 1];
    assert.deepEqual(lastProbe, ["a-web"]);

    await page.waitFor(`!Boolean(document.querySelector('[aria-label="命令或资产搜索"]'))`);
    await openPalette(page);
    await page.waitFor(`Boolean(document.querySelector('[aria-label="命令或资产搜索"]'))`);
    await replaceText(page, "快速连接");
    await page.waitFor(
      `[...document.querySelectorAll('[role="option"]')].some((o) => o.textContent.includes("快速连接"))`,
    );
    await press(page, "Enter");
    await page.waitFor(QUICK_COMBOBOX);
    await press(page, "Escape");
    await page.waitFor(`!${QUICK_COMBOBOX}`);
    return { dot };
  });

  await pass("overlay-small-viewport", async () => {
    await page.send("Emulation.setDeviceMetricsOverride", {
      width: 480,
      height: 400,
      deviceScaleFactor: 1,
      mobile: false,
    });
    try {
      await openQuickConnect(page);
      await page.waitFor(QUICK_COMBOBOX);
      const metrics = await page.evaluate(`(() => {
        const rect = (el) => {
          const b = el.getBoundingClientRect();
          return { top: b.top, bottom: b.bottom, left: b.left, right: b.right };
        };
        const modal = document.querySelector('.nx-command-modal');
        const input = document.querySelector('[aria-label="资产搜索"]');
        const list = document.querySelector('[role="listbox"]');
        return {
          innerW: window.innerWidth,
          innerH: window.innerHeight,
          modal: rect(modal),
          input: rect(input),
          listScrollable: list.scrollHeight >= list.clientHeight,
        };
      })()`);
      assert.ok(metrics.modal.bottom <= metrics.innerH, JSON.stringify(metrics));
      assert.ok(metrics.modal.right <= metrics.innerW, JSON.stringify(metrics));
      assert.ok(metrics.input.top >= 0 && metrics.input.bottom <= metrics.innerH, JSON.stringify(metrics));
      assert.equal(metrics.listScrollable, true);
      await screenshot(page, "overlay-small-viewport.png");
      await press(page, "Escape");
      await page.waitFor(`!${QUICK_COMBOBOX}`);
      return { metrics };
    } finally {
      await page.send("Emulation.clearDeviceMetricsOverride");
    }
  });

  await screenshot(page, "quick-connect-final.png");
}

let vite;
let chrome;
let page;
try {
  [vite, chrome] = await Promise.all([startVite(), startChrome()]);
  page = await newPage(chrome);
  await quickConnectAcceptance(page);
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
    keyboard_only: true,
    mocked_backend: "CDP Fetch interception of /rpc (WEB transport); src/demo untouched",
    note: "所有交互仅通过 Input.dispatchKeyEvent / Input.insertText；未调用 element.click() 或 element.focus()",
  },
  checks,
  harness_errors: harnessErrors,
};
fs.writeFileSync(path.join(OUT, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
console.warn(`quick-connect acceptance: ${checks.filter((check) => check.status === "passed").length}/${checks.length} checks passed; report=${path.join(OUT, "report.json")}`);
if (failed.length || harnessErrors.length) process.exit(1);
