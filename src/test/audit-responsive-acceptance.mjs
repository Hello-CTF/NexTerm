#!/usr/bin/env node
import { spawn, spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const OUT = path.join(ROOT, "target/audit-responsive");
const SHOTS = path.join(OUT, "shots");
const VITE_PORT = Number(process.env.NEXTERM_VITE_PORT || 1426);
const VITE = `http://127.0.0.1:${VITE_PORT}`;
const AUDIT_URL = `${VITE}/src/test/audit-responsive-harness.html`;
const LONG_ERROR = `E${"x".repeat(200)}`;
const results = new Map();
const harnessErrors = [];
const pageErrors = [];

fs.mkdirSync(SHOTS, { recursive: true });

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

async function waitHttp(url, process, timeout = 60_000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (process?.exitCode !== null && process?.exitCode !== undefined) throw new Error(`${url}: process exited ${process.exitCode}`);
    try {
      const response = await fetch(url);
      if (response.ok) return response;
    } catch {}
    await sleep(150);
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

  async waitFor(expression, timeout = 60_000) {
    const deadline = Date.now() + timeout;
    let last;
    while (Date.now() < deadline) {
      try {
        last = await this.evaluate(expression);
        if (last) return last;
      } catch (error) {
        last = String(error);
      }
      await sleep(150);
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
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-audit-chrome-"));
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
  await page.send("Runtime.enable");
  page.on("Runtime.exceptionThrown", (params) => {
    const d = params.exceptionDetails;
    pageErrors.push(`${d?.text || ""} ${d?.exception?.description || ""}`.slice(0, 300));
  });
  return page;
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

async function screenshot(page, name) {
  const shot = await page.send("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(path.join(SHOTS, name), Buffer.from(shot.data, "base64"));
  return name;
}

async function setViewport(page, width, height) {
  await page.send("Emulation.setDeviceMetricsOverride", {
    width,
    height,
    deviceScaleFactor: 1,
    mobile: width <= 560,
  });
  await sleep(250);
}

const READ_LAYOUT = `(() => {
  const pane = document.querySelector(".nx-pane");
  if (!pane) return { found: false };
  const read = (el) => el ? {
    scrollWidth: el.scrollWidth,
    clientWidth: el.clientWidth,
    left: Math.round(el.getBoundingClientRect().left),
    right: Math.round(el.getBoundingClientRect().right),
  } : null;
  const bar = [...document.querySelectorAll("div")].find(
    (d) => d.className.includes("flex-wrap") && d.textContent?.includes("已显示"),
  );
  const button = bar
    ? [...bar.querySelectorAll("button")].find((b) => ["加载更多", "重试"].includes(b.textContent?.trim()))
    : null;
  const errorSpan = [...document.querySelectorAll("span")].find((s) => s.textContent?.includes("加载更多失败"));
  return {
    found: true,
    innerWidth: window.innerWidth,
    docScrollWidth: document.documentElement.scrollWidth,
    pane: read(pane),
    barPresent: !!bar,
    bar: read(bar),
    button: read(button),
    error: errorSpan ? { ...read(errorSpan), text: errorSpan.textContent } : null,
    skeletonRows: document.querySelectorAll('tbody tr[aria-hidden="true"]').length,
  };
})()`;

function assertNoOverflow(sample, label) {
  assert.ok(sample.found, `${label}: audit pane not found`);
  assert.ok(sample.docScrollWidth <= sample.innerWidth, `${label}: document scrollWidth ${sample.docScrollWidth} > innerWidth ${sample.innerWidth}`);
  assert.ok(
    sample.pane.scrollWidth <= sample.pane.clientWidth + 1,
    `${label}: pane scrollWidth ${sample.pane.scrollWidth} > clientWidth ${sample.pane.clientWidth}`,
  );
}

function assertLayout(sample, { bar, error, label }) {
  assertNoOverflow(sample, label);
  assert.equal(sample.barPresent, bar, `${label}: pagination bar presence mismatch: ${JSON.stringify(sample)}`);
  if (bar) {
    assert.ok(
      sample.bar.scrollWidth <= sample.bar.clientWidth + 1,
      `${label}: pagination bar overflows: ${JSON.stringify(sample.bar)}`,
    );
    assert.ok(sample.bar.right <= sample.pane.right + 1, `${label}: bar exceeds pane: ${JSON.stringify(sample.bar)}`);
    assert.ok(sample.button, `${label}: pagination control button missing`);
    assert.ok(
      sample.button.right <= sample.bar.right + 1 && sample.button.left >= sample.bar.left - 1,
      `${label}: control button clipped by bar: ${JSON.stringify(sample.button)}`,
    );
  }
  if (error) {
    assert.ok(sample.error, `${label}: load-more error span missing`);
    assert.ok(
      sample.error.scrollWidth <= sample.error.clientWidth + 1,
      `${label}: error span self-overflows: ${JSON.stringify(sample.error)}`,
    );
    assert.ok(
      sample.error.right <= sample.bar.right + 1,
      `${label}: error text exceeds bar: ${JSON.stringify(sample.error)}`,
    );
  }
}

async function auditChecks(page) {
  for (const [width, height] of [[320, 720], [390, 780], [1280, 900]]) {
    await pass(`audit-no-overflow-${width}`, async () => {
      await setViewport(page, width, height);
      await page.evaluate(`(() => {
        const h = window.__auditHarness;
        h.reset({ total: 250, pages: [{ offset: 0, rows: h.makeRows(100, 1) }], gateFirstPage: true });
        h.mount();
      })()`);

      await page.waitFor(`document.querySelectorAll('tbody tr[aria-hidden="true"]').length === 8`);
      const skeleton = await page.evaluate(READ_LAYOUT);
      assertLayout(skeleton, { bar: false, error: false, label: `audit skeleton ${width}` });
      assert.equal(skeleton.skeletonRows, 8, `audit skeleton ${width}: expected 8 skeleton rows`);

      await page.evaluate(`window.__auditHarness.releaseFirstPage()`);
      await page.waitFor(
        `[...document.querySelectorAll("div")].some((d) => d.className.includes("flex-wrap") && d.textContent?.includes("已显示"))`,
      );
      const healthy = await page.evaluate(READ_LAYOUT);
      assertLayout(healthy, { bar: true, error: false, label: `audit pagination ${width}` });

      const clicked = await page.evaluate(`(() => {
        const h = window.__auditHarness;
        h.state.failNextMoreWith = ${JSON.stringify(LONG_ERROR)};
        return h.clickButton("加载更多");
      })()`);
      assert.ok(clicked, `audit ${width}: load-more button not clickable`);
      await page.waitFor(
        `[...document.querySelectorAll("span")].some((s) => s.textContent?.includes("加载更多失败"))`,
      );
      const errored = await page.evaluate(READ_LAYOUT);
      assertLayout(errored, { bar: true, error: true, label: `audit load-more error ${width}` });
      assert.ok(
        errored.error.text.includes(LONG_ERROR),
        `audit ${width}: full error text must stay readable, got ${errored.error.text.length} chars`,
      );
      const shot = await screenshot(page, `audit-error-${width}.png`);
      await page.evaluate(`window.__auditHarness.unmount()`);
      return {
        evidence: {
          skeleton: { pane: skeleton.pane, docScrollWidth: skeleton.docScrollWidth },
          healthy: { bar: healthy.bar, button: healthy.button },
          errored: { bar: errored.bar, error: { ...errored.error, text: `${errored.error.text.length} chars` } },
          shot,
        },
      };
    });
  }
}

let vite;
let chrome;
try {
  vite = await startVite();
  chrome = await startChrome();
  const page = await newPage(chrome);
  await page.navigate(AUDIT_URL);
  await page.waitFor(`!!window.__auditHarness`);
  await auditChecks(page);
  page.close();
} catch (error) {
  harnessErrors.push(String(error?.stack || error));
} finally {
  stop(chrome?.process);
  stop(vite);
}

const checks = [...results.values()];
const failed = checks.filter((check) => check.status !== "passed");
for (const message of pageErrors) harnessErrors.push(`unexpected page error: ${message}`);
const report = {
  schema_version: 1,
  status: failed.length || harnessErrors.length ? "failed" : "passed",
  browser: chrome?.version || { status: "unavailable" },
  execution: {
    real_browser: true,
    headless: true,
    jsdom: false,
    transport: "harness page with assetApi stub (no server)",
    matrix: "320/390/1280 widths; skeleton, healthy pagination bar, long load-more error states",
  },
  checks,
  harness_errors: harnessErrors,
  page_errors_unexpected: pageErrors,
};
fs.writeFileSync(path.join(OUT, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
console.warn(
  `audit responsive acceptance: ${checks.filter((check) => check.status === "passed").length}/${checks.length} checks passed; report=${path.join(OUT, "report.json")}`,
);
if (failed.length || harnessErrors.length) process.exit(1);
