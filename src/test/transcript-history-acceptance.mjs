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
const OUT = path.join(ROOT, "target/acceptance-transcript-history");
const VITE_PORT = Number(process.env.NEXTERM_VITE_PORT || 1425);
const VITE = `http://127.0.0.1:${VITE_PORT}`;
const MARKER = `nexterm-transcript-marker-${Date.now().toString(36)}`;
const AFTER_MARKER = `nexterm-after-history-${Date.now().toString(36)}`;
const results = new Map();
const harnessErrors = [];
const pageErrors = [];

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
      if (!message.id) {
        for (const listener of this.listeners.get(message.method) ?? []) listener(message.params);
        return;
      }
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

  on(method, listener) {
    const listeners = this.listeners.get(method) ?? [];
    listeners.push(listener);
    this.listeners.set(method, listeners);
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
  page.on("Runtime.exceptionThrown", (params) => {
    const d = params.exceptionDetails;
    pageErrors.push(`${d?.text || ""} ${d?.exception?.description || ""}`.slice(0, 300));
  });
  return page;
}

function buildServer() {
  const binary = path.join(OUT, "nexterm-server");
  const result = spawnSync("go", [
    "build", "-mod=readonly", "-buildvcs=false", "-o", binary, "./cmd/nexterm-server",
  ], {
    cwd: ROOT,
    env: { ...globalThis.process.env, CGO_ENABLED: "0" },
    encoding: "utf8",
  });
  if (result.status !== 0) {
    throw new Error(`server build failed: ${result.stderr?.slice(-2000)}`);
  }
  return binary;
}

async function startServer(binary, dataDir) {
  const port = await freePort();
  const process = spawn(binary, ["serve", "--listen", `127.0.0.1:${port}`, "--data-dir", dataDir, "--auth=loopback"], {
    cwd: ROOT,
    stdio: ["ignore", "pipe", "pipe"],
  });
  let log = "";
  process.stdout.on("data", (chunk) => { log += chunk; });
  process.stderr.on("data", (chunk) => { log += chunk; });
  try {
    await waitHttp(`http://127.0.0.1:${port}/healthz`, process, 45_000);
  } catch (error) {
    stop(process);
    throw new Error(`${error.message}; server log=${log.slice(-1500)}`);
  }
  return { process, port, log: () => log };
}

async function rpc(serverPort, cmd, args = {}) {
  const response = await fetch(`http://127.0.0.1:${serverPort}/rpc`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ cmd, args }),
  });
  const body = await response.json();
  if (!body.ok) throw new Error(`rpc ${cmd} failed: ${JSON.stringify(body.error)}`);
  return body.data;
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

const PANEL = `[data-testid="transcript-history"]`;

async function boot(page, api) {
  const { identifier } = await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `
      try { localStorage.clear(); } catch {}
      window.__NEXTERM_TRANSPORT__ = "web";
    `,
  });
  try {
    await page.navigate(`${VITE}/?api=${api}`);
    await page.waitFor("!!document.querySelector('.nx-app')", 45_000);
    await page.waitFor(
      `(async () => { const { useUi } = await import('/src/app/store.ts'); return useUi.getState().workspaces !== undefined; })()`,
      30_000,
    );
  } finally {
    await page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier });
  }
}

async function completeSetupGate(page, server) {
  const gate = await page.waitFor(`(async () => {
    const { useAuth } = await import('/src/features/auth/store.ts');
    const value = useAuth.getState().gate;
    return value === "loading" ? false : value;
  })()`, 30_000);
  if (gate !== "setup") return;
  const match = server.log().match(/一次性初始化码: (\S+)/);
  if (!match) throw new Error("setup gate shown but no one-time init code in server log");
  const filled = await page.evaluate(`(() => {
    const inputs = [...document.querySelectorAll('.fixed.inset-0.z-50 form input.nx-input')];
    const values = ${JSON.stringify([match[1], "acc-admin", "acceptance-admin-pass", "acceptance-admin-pass"])};
    if (inputs.length !== values.length) return false;
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set;
    inputs.forEach((input, index) => {
      setter.call(input, values[index]);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    return true;
  })()`);
  if (!filled) throw new Error("setup gate form fields not found");
  await page.evaluate(`(() => {
    const button = [...document.querySelectorAll('.fixed.inset-0.z-50 form button')].find((b) => b.textContent.includes("创建超级管理员"));
    if (!button || button.disabled) throw new Error("创建超级管理员 button not ready");
    button.click();
  })()`);
  await page.waitFor(`[...document.querySelectorAll('.fixed.inset-0.z-50 button')].some((b) => b.textContent.trim() === "复制")`, 90_000);
  await page.evaluate(`(() => {
    const copy = [...document.querySelectorAll('.fixed.inset-0.z-50 button')].find((b) => b.textContent.trim() === "复制");
    copy.click();
  })()`);
  await page.waitFor(`(() => {
    const done = [...document.querySelectorAll('.fixed.inset-0.z-50 button')].find((b) => b.textContent.includes("我已安全保存"));
    return Boolean(done && !done.disabled);
  })()`, 10_000);
  await page.evaluate(`(() => {
    const done = [...document.querySelectorAll('.fixed.inset-0.z-50 button')].find((b) => b.textContent.includes("我已安全保存"));
    done.click();
  })()`);
  await page.waitFor(`(async () => {
    const { useAuth } = await import('/src/features/auth/store.ts');
    const state = useAuth.getState();
    return state.gate === "ready" && !state.pendingRecoveryKey;
  })()`, 30_000);
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

async function waitForTranscriptText(page, serverPort, marker, timeout = 20_000) {
  const deadline = Date.now() + timeout;
  let last = "not checked";
  while (Date.now() < deadline) {
    const found = await page.evaluate(`(async () => {
      const list = await (await fetch(${JSON.stringify(`http://127.0.0.1:${serverPort}/rpc`)}, {
        method: "POST", headers: { "content-type": "application/json" },
        body: JSON.stringify({ cmd: "transcript_list", args: { assetId: "01J0NEXTERMLOCALDEVICE0001" } }),
      })).json();
      if (!list.ok || !list.data.length) return "no transcript yet";
      for (const summary of list.data) {
        const search = await (await fetch(${JSON.stringify(`http://127.0.0.1:${serverPort}/rpc`)}, {
          method: "POST", headers: { "content-type": "application/json" },
          body: JSON.stringify({ cmd: "transcript_search", args: { id: summary.id, query: ${JSON.stringify(marker)} } }),
        })).json();
        if (search.ok && search.data.length > 0) return "found";
      }
      return "no match yet";
    })()`);
    if (found === "found") return;
    last = found;
    await sleep(200);
  }
  throw new Error(`transcript text ${marker} not recorded: ${last}`);
}

async function typeIntoTerminal(page, serverPort, text, marker) {
  let lastError = "not attempted";
  for (let attempt = 0; attempt < 3; attempt++) {
    try {
      await focusTerminal(page);
      await typeText(page, text);
      await pressEnter(page);
      await waitForTranscriptText(page, serverPort, marker, 8_000);
      return;
    } catch (error) {
      lastError = String(error?.message || error);
    }
  }
  throw new Error(`typing never reached the terminal: ${lastError}`);
}

async function openHistoryPanel(page) {
  await clickSelector(page, 'button[aria-label="终端历史"]');
  await page.waitFor(`Boolean(document.querySelector(${JSON.stringify(PANEL)}))`);
}

async function transcriptHistoryAcceptance(page, server) {
  let dataDir;
  await pass("boot-connect-type-and-record", async () => {
    dataDir = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-transcript-"));
    const started = await startServer(server.binary, dataDir);
    server.process = started.process;
    server.port = started.port;
    await boot(page, `http://127.0.0.1:${server.port}`);
    await completeSetupGate(page, started);
    await seedTerminal(page);
    await typeIntoTerminal(page, server.port, `echo ${MARKER}`, MARKER);
  });

  await pass("history-panel-lists-live-session", async () => {
    await openHistoryPanel(page);
    await page.waitFor(`document.querySelector(${JSON.stringify(PANEL)})?.textContent?.includes("进行中")`, 20_000);
    await page.waitFor(`document.querySelectorAll(${JSON.stringify(`${PANEL} tbody tr`)}).length >= 1`);
  });

  await pass("reader-shows-recorded-output-ansistripped", async () => {
    await clickSelector(page, `${PANEL} tbody tr`);
    await page.waitFor(
      `document.querySelector(${JSON.stringify(`${PANEL} pre`)})?.textContent?.includes(${JSON.stringify(MARKER)})`,
      20_000,
    );
  });

  await pass("search-finds-recorded-output", async () => {
    await clickSelector(page, `${PANEL} input[aria-label="搜索终端记录"]`);
    await typeText(page, MARKER);
    await pressEnter(page);
    await page.waitFor(
      `document.querySelector(${JSON.stringify(PANEL)})?.textContent?.includes(${JSON.stringify(MARKER)})`,
      20_000,
    );
  });

  await pass("live-terminal-undisturbed-after-viewer-use", async () => {
    await clickSelector(page, '[role="tablist"][aria-label="标签页"] [role="tab"]');
    await page.waitFor(
      `document.querySelector('[role="tablist"][aria-label="标签页"] [role="tab"]')?.getAttribute("aria-selected") === "true"`,
      10_000,
    );
    await typeIntoTerminal(page, server.port, `echo ${AFTER_MARKER}`, AFTER_MARKER);
  });

  await pass("disconnect-ends-transcript-honestly", async () => {
    const sessions = await rpc(server.port, "session_list");
    assert.ok(sessions.length >= 1, "expected a connected session");
    await rpc(server.port, "session_disconnect", { sessionId: sessions[0].id });
    await openHistoryPanel(page);
    await page.waitFor(
      `document.querySelector(${JSON.stringify(PANEL)})?.textContent?.includes("已结束")`,
      20_000,
    );
  });

  await pass("history-survives-server-restart", async () => {
    const exited = new Promise((resolve) => server.process.once("exit", resolve));
    stop(server.process);
    await exited;
    const started = await startServer(server.binary, dataDir);
    server.process = started.process;
    server.port = started.port;
    await boot(page, `http://127.0.0.1:${server.port}`);
    await openHistoryPanel(page);
    await page.waitFor(
      `document.querySelector(${JSON.stringify(PANEL)})?.textContent?.includes("已结束")`,
      20_000,
    );
    await page.evaluate(`(() => {
      const rows = [...document.querySelectorAll(${JSON.stringify(`${PANEL} tbody tr`)})];
      const row = rows.find((candidate) => candidate.textContent?.includes("已结束")) ?? rows[0];
      row?.click();
    })()`);
    await page.waitFor(
      `document.querySelector(${JSON.stringify(`${PANEL} pre`)})?.textContent?.includes(${JSON.stringify(MARKER)})`,
      20_000,
    );
  });

  await pass("deleted-asset-history-still-reachable", async () => {
    const created = await rpc(server.port, "asset_create", {
      args: { kind: "ssh", name: "e2e-deleted-host", host: "192.0.2.60", port: 22, username: "root", authKind: "password" },
    });
    const { DatabaseSync } = await import("node:sqlite");
    const database = new DatabaseSync(path.join(dataDir, "data.db"));
    const transcriptId = "01JTRANSCRIPTDELETED000000";
    const now = Date.now();
    const output = `${MARKER}\r\n`;
    database.prepare(
      "INSERT INTO transcript(id, session_id, asset_id, asset_name, asset_kind, started_at, ended_at, bytes, chunks, truncated) VALUES(?,?,?,?,?,?,?,?,?,0)",
    ).run(transcriptId, "session-deleted", created.id, "e2e-deleted-host", "ssh", now - 60000, now, output.length, 1);
    database.prepare(
      "INSERT INTO transcript_chunk(transcript_id, seq, tab_id, ts, data) VALUES(?,?,?,?,?)",
    ).run(transcriptId, 0, "tab-1", now - 60000, Buffer.from(output));
    database.close();
    await rpc(server.port, "asset_delete", { id: created.id });
    await boot(page, `http://127.0.0.1:${server.port}`);
    await openHistoryPanel(page);
    await page.waitFor(
      `(() => {
        const select = document.querySelector(${JSON.stringify(`${PANEL} select[aria-label="选择主机"]`)});
        return Boolean(select && [...select.options].some((option) =>
          option.value === ${JSON.stringify(created.id)} && option.textContent?.includes("已删除")));
      })()`,
      15_000,
    );
    await page.evaluate(`(() => {
      const select = document.querySelector(${JSON.stringify(`${PANEL} select[aria-label="选择主机"]`)});
      const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value").set;
      setter.call(select, ${JSON.stringify(created.id)});
      select.dispatchEvent(new Event("change", { bubbles: true }));
    })()`);
    await page.waitFor(
      `document.querySelector(${JSON.stringify(PANEL)})?.textContent?.includes("主机已删除")`,
      20_000,
    );
    await page.evaluate(`(() => {
      const rows = [...document.querySelectorAll(${JSON.stringify(`${PANEL} tbody tr`)})];
      (rows.find((candidate) => candidate.textContent?.includes("已结束")) ?? rows[0])?.click();
    })()`);
    await page.waitFor(
      `document.querySelector(${JSON.stringify(`${PANEL} pre`)})?.textContent?.includes(${JSON.stringify(MARKER)})`,
      20_000,
    );
  });

  await pass("empty-state-for-host-without-history", async () => {
    const created = await rpc(server.port, "asset_create", {
      args: { kind: "ssh", name: "e2e-empty-host", host: "192.0.2.55", port: 22, username: "root", authKind: "password" },
    });
    await boot(page, `http://127.0.0.1:${server.port}`);
    await openHistoryPanel(page);
    await page.waitFor(
      `(() => {
        const select = document.querySelector(${JSON.stringify(`${PANEL} select[aria-label="选择主机"]`)});
        return Boolean(select && [...select.options].some((option) => option.value === ${JSON.stringify(created.id)}));
      })()`,
      15_000,
    );
    await page.evaluate(`(() => {
      const select = document.querySelector(${JSON.stringify(`${PANEL} select[aria-label="选择主机"]`)});
      const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value").set;
      setter.call(select, ${JSON.stringify(created.id)});
      select.dispatchEvent(new Event("change", { bubbles: true }));
    })()`);
    await page.waitFor(
      `document.querySelector(${JSON.stringify(PANEL)})?.textContent?.includes("该主机还没有终端历史")`,
      20_000,
    );
  });

  await page.send("Page.captureScreenshot", { format: "png" }).then((shot) => {
    fs.writeFileSync(path.join(OUT, "transcript-history-final.png"), Buffer.from(shot.data, "base64"));
  });
}

let vite;
let chrome;
let page;
const server = { binary: "", process: null, port: 0 };
try {
  server.binary = buildServer();
  [vite, chrome] = await Promise.all([startVite({ root: ROOT, port: VITE_PORT }), startChrome()]);
  page = await newPage(chrome);
  await transcriptHistoryAcceptance(page, server);
} catch (error) {
  harnessErrors.push(String(error?.stack || error));
} finally {
  if (page) page.close();
  stop(server.process);
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
    real_server: true,
    cgo_enabled: false,
    note: "真实 nexterm-server（CGO_ENABLED=0 构建）+ 真实 Chrome；终端输出经生产 feed 路径写入 SQLite，再由历史面板读取",
  },
  checks,
  harness_errors: harnessErrors,
  page_errors: pageErrors,
};
fs.writeFileSync(path.join(OUT, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
console.warn(`transcript history acceptance: ${checks.filter((check) => check.status === "passed").length}/${checks.length} checks passed; report=${path.join(OUT, "report.json")}`);
if (failed.length || harnessErrors.length) process.exit(1);
