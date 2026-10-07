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
const OUT = path.join(ROOT, "target/sync-bundle-acceptance");
const DOWNLOADS = path.join(OUT, "downloads");
const FIXTURES = path.join(OUT, "fixtures");
const VITE_PORT = Number(process.env.NEXTERM_VITE_PORT || 1426);
const VITE = `http://127.0.0.1:${VITE_PORT}`;
const results = new Map();
const harnessErrors = [];

fs.mkdirSync(DOWNLOADS, { recursive: true });
fs.mkdirSync(FIXTURES, { recursive: true });

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
  await page.send("Runtime.enable");
  return page;
}

const CLICK_BUTTON = (label) => `(() => {
  const btn = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === ${JSON.stringify(label)});
  if (!btn) return false;
  btn.click();
  return true;
})()`;

const SET_PLAINTEXT_FORMAT = `(() => {
  const select = document.querySelector("#bundle-format");
  if (!select) return false;
  const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value").set;
  setter.call(select, "plaintext");
  select.dispatchEvent(new Event("input", { bubbles: true }));
  select.dispatchEvent(new Event("change", { bubbles: true }));
  return true;
})()`;

const BODY_HAS = (snippet) => `document.body.textContent.includes(${JSON.stringify(snippet)})`;

async function bootDemoSettings(page) {
  await page.navigate(`${VITE}/?demo=1`);
  await page.waitFor("!!document.querySelector('.nx-app')");
  await page.evaluate(`(async () => {
    const { useUi } = await import('/src/app/store.ts');
    useUi.getState().addTab({ id: "settings", kind: "settings", title: "设置", closable: true });
    return true;
  })()`);
  await page.waitFor(BODY_HAS("资产包（文件导入 / 导出）"));
  await page.waitFor(BODY_HAS("web-01"));
  await page.evaluate(`(() => {
    try { Object.defineProperty(window, "showSaveFilePicker", { value: undefined, configurable: true }); } catch {}
    return typeof window.showSaveFilePicker;
  })()`);
}

async function pickFileThroughChooser(page, filePath) {
  const opened = new Promise((resolve) => {
    const handler = (params) => resolve(params);
    page.on("Page.fileChooserOpened", handler);
  });
  await page.send("Page.setInterceptFileChooserDialog", { enabled: true });
  const clicked = await page.evaluate(CLICK_BUTTON("选择资产包文件…"));
  assert.ok(clicked, "import button not found");
  const chooser = await Promise.race([opened, sleep(15_000).then(() => null)]);
  assert.ok(chooser?.backendNodeId, "file chooser did not open");
  await page.send("DOM.setFileInputFiles", { files: [filePath], backendNodeId: chooser.backendNodeId });
}

let chrome;
let vite;
try {
  vite = await startVite({ root: ROOT, port: VITE_PORT });
  chrome = await startChrome();
  const page = await newPage(chrome);
  await page.send("Page.setDownloadBehavior", { behavior: "allow", downloadPath: DOWNLOADS });
  await bootDemoSettings(page);

  await pass("bundle-export-without-credentials", async () => {
    await page.evaluate(`(() => {
      const row = [...document.querySelectorAll("label")].find((l) => l.textContent?.includes("web-01"));
      row.querySelector('input[type="checkbox"]').click();
      return true;
    })()`);
    const formatChosen = await page.evaluate(SET_PLAINTEXT_FORMAT);
    assert.ok(formatChosen, "plaintext format option not chosen");
    await page.waitFor(BODY_HAS("将以明文导出"));
    await page.waitFor(`(() => {
      const btn = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "导出为明文 JSON 文件");
      return !!btn && !btn.disabled;
    })()`);
    const clicked = await page.evaluate(CLICK_BUTTON("导出为明文 JSON 文件"));
    assert.ok(clicked, "export button not found");
    await page.waitFor(BODY_HAS("导出完成"));
    assert.ok(await page.evaluate(BODY_HAS("凭据 0")), "summary must show zero credentials");
    const deadline = Date.now() + 15_000;
    let file;
    while (Date.now() < deadline) {
      const found = fs.readdirSync(DOWNLOADS).filter((f) => f.endsWith(".json") && !f.endsWith(".crdownload"));
      if (found.length > 0) {
        file = path.join(DOWNLOADS, found[0]);
        break;
      }
      await sleep(200);
    }
    assert.ok(file, "exported bundle did not land in the download directory");
    const bundle = JSON.parse(fs.readFileSync(file, "utf8"));
    assert.equal(bundle.protocol, 1);
    assert.ok(bundle.assets.some((a) => a.name === "web-01"), "exported bundle must contain web-01");
    assert.equal(bundle.creds.length, 0, "credential-free export must not carry credentials");
    const raw = fs.readFileSync(file, "utf8");
    assert.ok(!raw.includes("Xk9#web01$pw"), "demo password leaked into credential-free bundle");
    return { file: path.basename(file), assets: bundle.assets.length };
  });

  await pass("bundle-export-credential-warning", async () => {
    await page.evaluate(`(() => {
      const label = [...document.querySelectorAll("label")].find((l) => l.textContent?.includes("导出凭据"));
      label.querySelector('input[type="checkbox"]').click();
      return true;
    })()`);
    await page.waitFor(BODY_HAS("可还原的凭据明文"));
    return {};
  });

  const broken = path.join(FIXTURES, "broken.json");
  fs.writeFileSync(broken, "this is not json{");
  await pass("bundle-import-rejects-invalid-json", async () => {
    await pickFileThroughChooser(page, broken);
    await page.waitFor(BODY_HAS("不是合法的 JSON"));
    assert.ok(!(await page.evaluate(BODY_HAS("导入结果"))), "invalid bundle must not reach sync_import");
    return {};
  });

  const wrongProtocol = path.join(FIXTURES, "future.json");
  fs.writeFileSync(wrongProtocol, JSON.stringify({ protocol: 99, groups: [], assets: [], creds: [] }));
  await pass("bundle-import-rejects-wrong-protocol", async () => {
    await pickFileThroughChooser(page, wrongProtocol);
    await page.waitFor(BODY_HAS("不支持的资产包协议版本"));
    return {};
  });

  const valid = path.join(FIXTURES, "valid-bundle.json");
  fs.writeFileSync(valid, JSON.stringify({
    protocol: 1,
    origin: "acceptance-fixture",
    exportedAt: Date.now(),
    groups: [],
    assets: [{
      id: "a-acceptance", groupId: null, kind: "ssh", name: "acceptance-01", host: "10.8.8.8",
      port: 22, username: "root", authKind: "password", keyPath: null, credId: "c-acceptance",
      optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: Date.now(), deletedAt: null,
    }],
    creds: [{ id: "c-acceptance", name: "验收凭据", kind: "password", secret: "acceptance-secret-value" }],
  }));
  await pass("bundle-import-preview-and-report", async () => {
    await pickFileThroughChooser(page, valid);
    await page.waitFor(BODY_HAS("确认导入 1 条资产"));
    assert.ok(await page.evaluate(BODY_HAS("acceptance-fixture")), "preview must show the bundle origin");
    assert.ok(await page.evaluate(BODY_HAS("包含 1 条凭据")), "preview must warn about bundled credentials");
    const body = await page.evaluate("document.body.textContent");
    assert.ok(!body.includes("acceptance-secret-value"), "preview must not render credential secrets");
    const clicked = await page.evaluate(CLICK_BUTTON("确认导入 1 条资产"));
    assert.ok(clicked, "confirm import button not found");
    await page.waitFor(BODY_HAS("导入结果"));
    assert.ok(await page.evaluate(BODY_HAS("资产 新建 1")), "report must count the created asset");
    assert.ok(await page.evaluate(BODY_HAS("凭据 新建 1")), "report must count the created credential");
    return {};
  });

  await pass("bundle-import-reflected-in-export-list", async () => {
    await page.waitFor(BODY_HAS("acceptance-01"));
    return {};
  });

  page.close();
} catch (error) {
  harnessErrors.push(String(error?.stack || error));
} finally {
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
    transport: "demo (mockInvoke) driving the real settings UI",
  },
  checks,
  harness_errors: harnessErrors,
};
fs.writeFileSync(path.join(OUT, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
console.warn(`sync bundle acceptance: ${checks.filter((check) => check.status === "passed").length}/${checks.length} checks passed; report=${path.join(OUT, "report.json")}`);
if (failed.length || harnessErrors.length) process.exit(1);
