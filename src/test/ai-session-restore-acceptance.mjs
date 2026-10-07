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
const OUT = path.join(ROOT, "target/acceptance-ai-session-restore");
const VITE_PORT = Number(process.env.NEXTERM_VITE_PORT || 1424);
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
export function __silenceAiStreamForTest() {
  return (async () => {
    for (let i = 0; i < 50; i++) {
      const entry = [...aiChannels.entries()].at(-1);
      if (entry) {
        cancelledAiJobs.add(entry[0]);
        return entry[0];
      }
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
    throw new Error("no active ai channel");
  })();
}
export function __lastAiChannelJob() {
  const entry = [...aiChannels.entries()].at(-1);
  return entry ? entry[0] : null;
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

async function drainRun(page) {
  await page.evaluate(`document.querySelector(".nx-send-btn-stop")?.click(); true`);
  await page.waitFor(`Boolean(document.querySelector('.nx-send-btn:not(.nx-send-btn-stop)'))`, 10_000);
}

async function screenshot(page, name) {
  const shot = await page.send("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(path.join(OUT, name), Buffer.from(shot.data, "base64"));
  return name;
}

async function acceptance(page) {
  await pass("reload-restores-conversation", async () => {
    await boot(page);
    await page.evaluate(`localStorage.setItem("nexterm.ai.conversation.v1", "conv-1"); true`);
    await boot(page);
    await openAi(page);
    await page.waitFor(`${LOG}.textContent.includes("api-server 一直 502")`, 15_000);
    const ev = await page.evaluate(`(() => {
      const log = ${LOG};
      return {
        restored: log.textContent.includes("api-server 一直 502"),
        blank: log.textContent.includes("命令与输出全程留痕"),
        stored: localStorage.getItem("nexterm.ai.conversation.v1"),
      };
    })()`);
    assert.equal(ev.restored, true, "persisted conversation messages not restored");
    assert.equal(ev.blank, false, "sidebar shows a blank replacement session after reload");
    assert.equal(ev.stored, "conv-1");
    const shot = await screenshot(page, "reload-restores-conversation.png");
    return { ev, shot };
  });

  await pass("send-after-reload-continues-restored-conversation", async () => {
    await boot(page);
    await page.evaluate(`localStorage.setItem("nexterm.ai.conversation.v1", "conv-1"); true`);
    await boot(page);
    await openAi(page);
    await page.waitFor(`${LOG}.textContent.includes("api-server 一直 502")`, 15_000);
    await typeComposer(page, "接着排查");
    await sendViaEnter(page);
    await page.waitFor(`Boolean(document.querySelector(".nx-send-btn-stop"))`, 10_000);
    const shot = await screenshot(page, "send-after-reload.png");
    await drainRun(page);
    const ev = await page.evaluate(`(() => ({
      logHasNew: ${LOG}.textContent.includes("接着排查"),
      stored: localStorage.getItem("nexterm.ai.conversation.v1"),
    }))()`);
    assert.equal(ev.logHasNew, true);
    assert.equal(ev.stored, "conv-1", "send after reload created a replacement conversation");
    await page.evaluate(`localStorage.removeItem("nexterm.ai.conversation.v1"); true`);
    return { ev, shot };
  });

  await pass("reload-restores-takeover-banner", async () => {
    await boot(page);
    await page.evaluate(`localStorage.setItem("nexterm.takeover.v1", JSON.stringify({
      tabId: "tab-demo", jobId: "job-demo", token: "tok-demo",
      task: "安装 nginx 并启动", allowWrite: true, startedAt: Date.now(),
    })); true`);
    await boot(page);
    await page.waitFor(`Boolean(document.querySelector(".nx-takeover-banner"))`, 15_000);
    const ev = await page.evaluate(`(() => {
      const banner = document.querySelector(".nx-takeover-banner");
      return { text: banner ? banner.textContent : "", stored: Boolean(localStorage.getItem("nexterm.takeover.v1")) };
    })()`);
    assert.ok(ev.text.includes("AI 正在操作此终端"), `banner text missing: ${ev.text}`);
    assert.ok(ev.text.includes("安装 nginx 并启动"), `banner task missing: ${ev.text}`);
    await sleep(1200);
    const alive = await page.evaluate(`Boolean(document.querySelector(".nx-takeover-banner"))`);
    assert.equal(alive, true, "banner cleared although the demo terminal shows no takeover-end marker");
    const shot = await screenshot(page, "reload-restores-takeover-banner.png");
    await page.evaluate(`localStorage.removeItem("nexterm.takeover.v1"); true`);
    return { ev, shot };
  });

  await pass("hitl-waiting-phase-visible", async () => {
    await boot(page);
    await openAi(page);
    await typeComposer(page, "重启 mysql-prod");
    await sendViaEnter(page);
    await page.waitFor(`[...document.querySelectorAll("button")].some((b) => b.textContent.trim() === "允许一次")`, 20_000);
    const ev = await page.evaluate(`(() => ({
      waiting: document.body.textContent.includes("等待你确认后继续"),
      composer: ${COMPOSER} ? true : false,
    }))()`);
    assert.equal(ev.waiting, true, "HITL waiting phase not visible while a confirmation card is pending");
    const shot = await screenshot(page, "hitl-waiting-phase.png");
    await page.evaluate(`(() => {
      const btn = [...document.querySelectorAll("button")].find((b) => b.textContent.trim() === "允许一次");
      btn.click();
    })()`);
    await sleep(400);
    const cleared = await page.evaluate(`!document.body.textContent.includes("等待你确认后继续")`);
    assert.equal(cleared, true, "HITL waiting phase stuck after the confirmation was submitted");
    return { ev, shot };
  });

  await pass("markdown-streaming-tail-stable", async () => {
    await drainRun(page);
    const lastJob = await page.evaluate(`(async () => (await import("/src/demo/mock.ts")).__lastAiChannelJob())()`);
    await typeComposer(page, "画个表");
    await sendViaEnter(page);
    await page.waitFor(`Boolean(document.querySelector(".nx-send-btn-stop"))`, 10_000);
    await page.waitFor(`(async () => {
      const m = await import("/src/demo/mock.ts");
      const job = m.__lastAiChannelJob();
      return job !== null && job !== ${JSON.stringify(lastJob)};
    })()`, 10_000);
    await page.evaluate(`(async () => {
      const m = await import("/src/demo/mock.ts");
      m.__silenceAiStreamForTest();
    })()`);
    await pushAiEvent(page, { type: "delta", text: "结果如下：\n\n| 容器 | 状态 |" });
    await page.waitFor(`${LOG}.textContent.includes("| 容器 | 状态 |")`, 10_000);
    const mid = await page.evaluate(`(() => ({
      hasTable: Boolean(${LOG}.querySelector("table")),
      text: ${LOG}.textContent.includes("| 容器 | 状态 |"),
    }))()`);
    assert.equal(mid.text, true, "streamed table header text missing from the log");
    assert.equal(mid.hasTable, false, "table structure flipped in before the separator line completed");
    await pushAiEvent(page, { type: "delta", text: "\n| --- | --- |\n| mysql | 运行中 |\n" });
    await page.waitFor(`Boolean(${LOG}.querySelector("table"))`, 10_000);
    const done = await page.evaluate(`(() => {
      const table = ${LOG}.querySelector("table");
      return { hasTable: Boolean(table), row: table ? table.textContent.includes("mysql") : false };
    })()`);
    assert.equal(done.hasTable, true, "table did not render after the separator line completed");
    assert.equal(done.row, true, "table row missing after completion");
    const shot = await screenshot(page, "markdown-streaming-tail.png");
    await pushAiEvent(page, { type: "done", answer: "表画完了" });
    await sleep(300);
    return { mid, done, shot };
  });
}

async function main() {
  const vite = await startVite({ root: ROOT, port: VITE_PORT });
  const chrome = await startChrome();
  const page = await newPage(chrome);
  try {
    await enableMockInterception(page);
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
