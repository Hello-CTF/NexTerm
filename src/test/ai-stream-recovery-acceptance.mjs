#!/usr/bin/env node
import { spawn, spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import fs from "node:fs";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { freePort, stopProcess, waitHttp } from "./lib/acceptance-process.mjs";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const OUT = path.join(ROOT, "target/acceptance-ai-stream-recovery");
const results = new Map();
const harnessErrors = [];
const EXPECTED = [
  "ai-stream-live",
  "ai-stream-reconnect-replay",
  "ai-stream-cancel-terminal",
  "ai-stream-retry-after-failure",
];

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

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

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
    return { process, port, version: await response.json() };
  } catch (error) {
    stopProcess(process);
    throw new Error(`${error.message}; Chrome stderr=${stderr.slice(-1000)}`);
  }
}

async function newPage(chrome) {
  const response = await fetch(`http://127.0.0.1:${chrome.port}/json/new?about:blank`, { method: "PUT" });
  if (!response.ok) throw new Error(`cannot create Chrome target: ${response.status}`);
  return CDP.connect((await response.json()).webSocketDebuggerUrl);
}

async function rpc(origin, cmd, args) {
  const response = await fetch(`${origin}/rpc`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ cmd, args }),
  });
  const body = await response.json();
  if (!body || body.ok !== true) throw new Error(`rpc ${cmd} failed: ${JSON.stringify(body?.error ?? body)}`);
  return body.data;
}

async function startStub() {
  const port = await freePort();
  const state = { failBudget: 3, requests: [] };
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
      if (text.includes("fail-once") && state.failBudget > 0) {
        state.failBudget -= 1;
        res.writeHead(500, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ error: { message: "stub forced failure" } }));
        return;
      }
      const slow = text.includes("slow");
      const marker = text.includes("取消") ? "NXR188C" : slow ? "NXR188S" : "NXR188Q";
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
  await new Promise((resolve) => server.listen(port, "127.0.0.1", resolve));
  return { server, port, state, origin: `http://127.0.0.1:${port}` };
}

async function startServer() {
  const goos = { darwin: "darwin", linux: "linux", win32: "windows" }[globalThis.process.platform];
  const goarch = { arm64: "arm64", x64: "amd64" }[globalThis.process.arch];
  const binary = globalThis.process.env.NEXTERM_SERVER_BIN || path.join(ROOT, "target/go-build/nexterm-server-r188-browser");
  if (!globalThis.process.env.NEXTERM_SERVER_BIN) {
    const built = spawnSync(globalThis.process.execPath, ["scripts/build.mjs", "server", "--release", `--os=${goos}`, `--arch=${goarch}`, `--out=${binary}`], { cwd: ROOT, stdio: "inherit" });
    if (built.status !== 0) throw new Error("Go server build for AI stream acceptance failed");
  }
  const port = await freePort();
  const data = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-r188-server-"));
  const log = fs.openSync(path.join(OUT, "server.log"), "w");
  const process = spawn(binary, ["--listen", `127.0.0.1:${port}`, "--data-dir", data, "--web-root", path.join(ROOT, "dist")], {
    cwd: ROOT,
    env: { ...globalThis.process.env, NEXTERM_MASTER_KEY: "r188-browser-master" },
    stdio: ["ignore", log, log],
  });
  await waitHttp(`http://127.0.0.1:${port}/healthz`, process);
  return { process, port, origin: `http://127.0.0.1:${port}` };
}

const LOG = `document.querySelector('div[role="log"]')`;
const COMPOSER = `document.querySelector('textarea[aria-label="消息输入"]')`;

async function openAiSidebar(page) {
  const ready = await page.evaluate(`Boolean(${COMPOSER} && ${COMPOSER}.getBoundingClientRect().width > 0)`);
  if (!ready) {
    await page.evaluate(`(() => {
      const rail = document.querySelector('button[aria-label="AI 助手"]');
      if (!rail) throw new Error("AI rail button missing");
      rail.click();
    })()`);
  }
  await page.waitFor(`Boolean(${COMPOSER} && ${COMPOSER}.getBoundingClientRect().width > 0 && document.querySelector('.nx-send-btn:not(.nx-send-btn-stop)'))`);
}

async function typeComposer(page, text) {
  await page.waitFor(`Boolean(document.querySelector('.nx-send-btn:not(.nx-send-btn-stop)'))`);
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
}

async function logText(page) {
  return page.evaluate(`(${LOG}?.textContent) ?? ""`);
}

async function countInLog(page, needle) {
  return page.evaluate(`((${LOG}?.textContent) ?? "").split(${JSON.stringify(needle)}).length - 1`);
}

async function acceptance(page, server, stub) {
  await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `
      window.__NEXTERM_TRANSPORT__ = 'web';
      window.__nxSockets = [];
      (() => {
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
      })();
    `,
  });
  await page.navigate(`${server.origin}/`);
  await page.waitFor(`Boolean(document.querySelector('button[aria-label="AI 助手"]') || ${COMPOSER})`);
  await openAiSidebar(page);

  await rpc(server.origin, "ai_model_save", {
    profile: {
      id: "r188-stub",
      name: "R188 Stub",
      baseUrl: `${stub.origin}/v1`,
      apiKey: "stub-key",
      model: "stub-model",
      temperature: 0.3,
      contextWindow: 32768,
      proxy: null,
      stream: true,
    },
  });
  await rpc(server.origin, "ai_model_activate", { id: "r188-stub" });

  await pass("ai-stream-live", async () => {
    await typeComposer(page, "快速回答");
    await sendViaEnter(page);
    await page.waitFor(`(${LOG}.textContent).includes("NXR188Q-0") && (${LOG}.textContent).includes("NXR188Q-1")`);
    await page.waitFor(`(${LOG}.textContent).includes("本轮已完成")`);
    return { evidence: { text: (await logText(page)).slice(-160) } };
  });

  await pass("ai-stream-reconnect-replay", async () => {
    const completedBefore = await countInLog(page, "本轮已完成");
    await typeComposer(page, "slow 流式恢复");
    await sendViaEnter(page);
    await page.waitFor(`(${LOG}.textContent).includes("NXR188S-0")`);
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
      await page.waitFor(`((${LOG}.textContent).split(${JSON.stringify(marker)}).length - 1) === 1`);
    }
    await page.waitFor(`((${LOG}.textContent).split("本轮已完成").length - 1) > ${completedBefore}`);
    return { evidence: { droppedSockets: dropped, text: (await logText(page)).slice(-200) } };
  });

  await pass("ai-stream-cancel-terminal", async () => {
    await typeComposer(page, "slow 取消分类");
    await sendViaEnter(page);
    await page.waitFor(`(${LOG}.textContent).includes("NXR188C-0")`);
    await page.evaluate(`document.querySelector(".nx-send-btn-stop")?.click(); true`);
    await page.waitFor(`(${LOG}.textContent).includes("已停止本轮")`);
    const text = await logText(page);
    assert.ok(!text.includes("本轮出错"), `cancel rendered as error: ${text.slice(-200)}`);
    return { evidence: { text: text.slice(-160) } };
  });

  await pass("ai-stream-retry-after-failure", async () => {
    const completedBefore = await countInLog(page, "本轮已完成");
    await typeComposer(page, "fail-once 重试联动");
    await sendViaEnter(page);
    await page.waitFor(`(${LOG}.textContent).includes("本轮出错")`);
    const hasRetry = await page.evaluate(`[...document.querySelectorAll("button")].some((b) => b.textContent.trim() === "重试")`);
    assert.equal(hasRetry, true, "retry action missing on retryable failure");
    await page.evaluate(`(() => {
      const button = [...document.querySelectorAll("button")].find((b) => b.textContent.trim() === "重试");
      button.click();
    })()`);
    await page.waitFor(`((${LOG}.textContent).split("本轮已完成").length - 1) > ${completedBefore}`);
    const failRequests = stub.state.requests.filter((text) => text.includes("fail-once"));
    assert.ok(failRequests.length >= 2, `retry did not resend the original message: ${JSON.stringify(stub.state.requests)}`);
    return { evidence: { failRequests: failRequests.length, text: (await logText(page)).slice(-160) } };
  });
}

let server;
let stub;
let chrome;
let page;
try {
  const built = spawnSync(globalThis.process.execPath, ["scripts/build.mjs", "frontend"], { cwd: ROOT, stdio: "inherit" });
  if (built.status !== 0) throw new Error("frontend build for AI stream acceptance failed");
  [server, stub, chrome] = await Promise.all([startServer(), startStub(), startChrome()]);
  page = await newPage(chrome);
  await acceptance(page, server, stub);
} catch (error) {
  harnessErrors.push(String(error?.stack || error));
} finally {
  if (page) page.close();
  stopProcess(chrome?.process);
  if (stub?.server) stub.server.close();
  stopProcess(server?.process);
}

for (const id of EXPECTED) {
  if (!results.has(id)) record(id, "not-run-dependency-failed", { reason: "the required harness did not complete; this is not a pass" });
}
const checks = [...results.values()];
const failed = checks.filter((check) => check.status !== "passed");
const report = {
  schema_version: 1,
  status: failed.length || harnessErrors.length ? "failed" : "passed",
  browser: chrome?.version || { status: "unavailable" },
  execution: { real_browser: true, headless: true, jsdom: false },
  checks,
  harness_errors: harnessErrors,
  skip_as_pass: false,
};
fs.writeFileSync(path.join(OUT, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
console.warn(`ai stream recovery acceptance: ${checks.filter((check) => check.status === "passed").length}/${EXPECTED.length} checks passed; report=${path.join(OUT, "report.json")}`);
if (failed.length || harnessErrors.length) globalThis.process.exitCode = 1;
