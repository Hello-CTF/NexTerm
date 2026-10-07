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
const OUT = path.join(ROOT, "target/acceptance-db-lifecycle");
const VITE_PORT = Number(process.env.NEXTERM_VITE_PORT || 20000 + Math.floor(Math.random() * 20000));
const VITE = `http://127.0.0.1:${VITE_PORT}`;
const MYSQL_PASSWORD = "nexterm-acc-mysql";
const EXPECTED = [
  "mysql-tab-close-disconnects",
  "mysql-tab-close-failure-retains-and-retry",
  "redis-tab-close-disconnects",
  "redis-workspace-close-disconnects",
];
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
  try {
    process.kill(-process.pid, "SIGTERM");
  } catch {
    process.kill("SIGTERM");
  }
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

function requireDocker() {
  const found = spawnSync("which", ["docker"], { encoding: "utf8" });
  if (found.status !== 0) throw new Error("docker is required for real MySQL/Redis lifecycle acceptance (this is a failure, not a skip)");
  const info = spawnSync("docker", ["info", "--format", "{{.ServerVersion}}"], { encoding: "utf8" });
  if (info.status !== 0) throw new Error(`docker daemon is unavailable: ${info.stderr}`);
}

function dockerExec(name, args, timeout = 30_000) {
  const ran = spawnSync("docker", ["exec", name, ...args], { encoding: "utf8", timeout });
  if (ran.error) throw new Error(`docker exec ${name} ${args[0]}: ${ran.error}`);
  return { status: ran.status, stdout: ran.stdout ?? "", stderr: ran.stderr ?? "" };
}

async function startContainer(name, containerPort, args) {
  const runArgs = ["run", "--detach", "--rm", "--name", name, "--publish", `127.0.0.1::${containerPort}`, ...args];
  const ran = spawnSync("docker", runArgs, { encoding: "utf8", timeout: 120_000 });
  if (ran.status !== 0) throw new Error(`docker run ${name}: ${ran.stderr}`);
  const port = await (async () => {
    const deadline = Date.now() + 15_000;
    let last = "";
    while (Date.now() < deadline) {
      const inspected = spawnSync("docker", ["port", name, `${containerPort}/tcp`], { encoding: "utf8" });
      last = inspected.stdout ?? "";
      const line = last.trim().split("\n")[0];
      if (line) {
        const parsed = Number(line.split(":").pop());
        if (parsed > 0) return parsed;
      }
      await sleep(200);
    }
    throw new Error(`docker port ${name}: ${last}`);
  })();
  return { name, port };
}

async function waitMysql(name) {
  const deadline = Date.now() + 240_000;
  let last = "";
  while (Date.now() < deadline) {
    const probe = dockerExec(name, ["mysql", "-uroot", `-p${MYSQL_PASSWORD}`, "-N", "-e", "SELECT 1"]);
    if (probe.status === 0) return;
    last = probe.stderr;
    await sleep(500);
  }
  throw new Error(`MySQL did not become ready: ${last}`);
}

async function waitRedis(name) {
  const deadline = Date.now() + 60_000;
  let last = "";
  while (Date.now() < deadline) {
    const probe = dockerExec(name, ["redis-cli", "ping"]);
    if (probe.status === 0 && probe.stdout.trim() === "PONG") return;
    last = probe.stderr || probe.stdout;
    await sleep(250);
  }
  throw new Error(`Redis did not become ready: ${last}`);
}

function mysqlThreadsConnected(name) {
  const probe = dockerExec(name, ["mysql", "-uroot", `-p${MYSQL_PASSWORD}`, "-N", "-e", "SHOW GLOBAL STATUS LIKE 'Threads_connected'"]);
  if (probe.status !== 0) throw new Error(`Threads_connected probe: ${probe.stderr}`);
  const value = Number(probe.stdout.trim().split(/\s+/).pop());
  if (!Number.isInteger(value)) throw new Error(`Threads_connected parse: ${probe.stdout}`);
  return value;
}

function redisConnectedClients(name) {
  const probe = dockerExec(name, ["redis-cli", "INFO", "clients"]);
  if (probe.status !== 0) throw new Error(`connected_clients probe: ${probe.stderr}`);
  const matched = probe.stdout.match(/connected_clients:(\d+)/);
  if (!matched) throw new Error(`connected_clients parse: ${probe.stdout.slice(0, 200)}`);
  return Number(matched[1]);
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
  page.socket.addEventListener("message", (event) => {
    const message = JSON.parse(String(event.data));
    if (message.method === "Runtime.exceptionThrown") {
      const details = message.params.exceptionDetails;
      pageErrors.push(`exception: ${details.exception?.description || details.text}`);
    }
    if (message.method === "Runtime.consoleAPICalled" && message.params.type === "error") {
      pageErrors.push(`console.error: ${message.params.args.map((a) => a.value ?? a.description ?? "").join(" ")}`);
    }
  });
  return page;
}

async function startServer() {
  const goos = { darwin: "darwin", linux: "linux", win32: "windows" }[globalThis.process.platform];
  const goarch = { arm64: "arm64", x64: "amd64" }[globalThis.process.arch];
  const binary = globalThis.process.env.NEXTERM_SERVER_BIN || path.join(ROOT, "target/go-build/nexterm-server-db-lifecycle");
  if (!globalThis.process.env.NEXTERM_SERVER_BIN) {
    const built = spawnSync(globalThis.process.execPath, ["scripts/build.mjs", "server", "--release", `--os=${goos}`, `--arch=${goarch}`, `--out=${binary}`], { cwd: ROOT, stdio: "inherit" });
    if (built.status !== 0) throw new Error("Go server build for db lifecycle acceptance failed");
  }
  const port = await freePort();
  const data = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-db-lifecycle-server-"));
  const log = fs.openSync(path.join(OUT, "server.log"), "w");
  const process = spawn(binary, ["--listen", `127.0.0.1:${port}`, "--data-dir", data], {
    cwd: ROOT,
    env: { ...globalThis.process.env, NEXTERM_MASTER_KEY: "db-lifecycle-acceptance-master" },
    stdio: ["ignore", log, log],
  });
  await waitHttp(`http://127.0.0.1:${port}/healthz`, process);
  return { process, port, origin: `http://127.0.0.1:${port}`, data };
}

async function boot(page, server) {
  await page.send("Page.addScriptToEvaluateOnNewDocument", { source: `window.__NEXTERM_TRANSPORT__ = "web";` });
  await page.navigate(`${VITE}/?api=${encodeURIComponent(server.origin)}`);
  await page.waitFor(`Boolean(document.querySelector('[role="tablist"][aria-label="工作区"]'))`);
  await page.evaluate(`(async () => { window.__nxCommands = await import("/src/ipc/commands.ts"); return true; })()`);
  await sleep(600);
}

async function openDbTab(page, { kind, host, port, username, password, database, title }) {
  return page.evaluate(`(async () => {
    const commands = await import("/src/ipc/commands.ts");
    const store = await import("/src/app/store.ts");
    const inline = { kind: ${JSON.stringify(kind)}, host: ${JSON.stringify(host)}, port: ${JSON.stringify(port)} };
    if (${JSON.stringify(username ?? null)} !== null) inline.username = ${JSON.stringify(username ?? null)};
    if (${JSON.stringify(password ?? null)} !== null) inline.password = ${JSON.stringify(password ?? null)};
    if (${JSON.stringify(database ?? null)} !== null) inline.database = ${JSON.stringify(database ?? null)};
    const { connId } = await commands.dbApi.connect(undefined, inline);
    const state = store.useUi.getState();
    state.ensureWorkspace({ kind: "db", connId, dbKind: ${JSON.stringify(kind)}, title: ${JSON.stringify(title)} });
    state.addTab({ id: "acc-" + connId, kind: "db", title: ${JSON.stringify(title)}, connId, dbKind: ${JSON.stringify(kind)}, closable: true });
    return connId;
  })()`);
}

const tabPresent = (title) => `[...document.querySelectorAll('[role="tablist"][aria-label="标签页"] [role="tab"]')].some((t) => t.textContent.includes(${JSON.stringify(title)}))`;

async function clickTabClose(page, title) {
  const clicked = await page.evaluate(`(() => {
    const tab = [...document.querySelectorAll('[role="tablist"][aria-label="标签页"] [role="tab"]')].find((t) => t.textContent.includes(${JSON.stringify(title)}));
    const btn = tab && tab.querySelector('button[aria-label^="关闭标签"]');
    if (!btn) return false;
    btn.click();
    return true;
  })()`);
  if (!clicked) throw new Error(`tab close button not found: ${title}`);
}

async function clickWorkspaceClose(page, title) {
  const clicked = await page.evaluate(`(() => {
    const tab = [...document.querySelectorAll('[role="tablist"][aria-label="工作区"] [role="tab"]')].find((t) => t.textContent.includes(${JSON.stringify(title)}));
    const btn = tab && tab.querySelector('button[aria-label^="关闭工作区"]');
    if (!btn) return false;
    btn.click();
    return true;
  })()`);
  if (!clicked) throw new Error(`workspace close button not found: ${title}`);
}

const toastText = `[...document.querySelectorAll(".nx-toasts button")].map((b) => b.textContent).join("\\n")`;

async function rpcErrorOf(page, expression) {
  return page.evaluate(`(async () => {
    try {
      await ${expression};
      return null;
    } catch (error) {
      return { code: error.code || "internal", message: String(error.message || error) };
    }
  })()`);
}

async function screenshot(page, name) {
  const shot = await page.send("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(path.join(OUT, name), Buffer.from(shot.data, "base64"));
  return name;
}

async function pollUntil(fn, want, label, timeout = 15_000) {
  const deadline = Date.now() + timeout;
  let last;
  while (Date.now() < deadline) {
    last = fn();
    if (want(last)) return last;
    await sleep(300);
  }
  throw new Error(`${label}: last=${last}`);
}

async function dbLifecycleAcceptance(page, mysql, redis) {
  await boot(page, server);

  await pass("mysql-tab-close-disconnects", async () => {
    const baseline = mysqlThreadsConnected(mysql.name);
    const connId = await openDbTab(page, {
      kind: "mysql", host: "127.0.0.1", port: mysql.port, username: "root",
      password: MYSQL_PASSWORD, title: "acc-mysql",
    });
    await page.waitFor(tabPresent("acc-mysql"));
    const withConn = await pollUntil(() => mysqlThreadsConnected(mysql.name), (v) => v >= baseline + 1, "MySQL connect did not add a server connection");
    await clickTabClose(page, "acc-mysql");
    await page.waitFor(`!${tabPresent("acc-mysql")}`);
    const after = await pollUntil(() => mysqlThreadsConnected(mysql.name), (v) => v <= baseline, "MySQL connection survived tab close");
    const rpcError = await rpcErrorOf(page, `window.__nxCommands.dbApi.schemas(${JSON.stringify(connId)})`);
    assert.equal(rpcError?.code, "not_found", `server must forget the connection: ${JSON.stringify(rpcError)}`);
    const shot = await screenshot(page, "mysql-tab-close.png");
    return { evidence: { baseline, withConn, after, rpcError, screenshot: shot } };
  });

  await pass("mysql-tab-close-failure-retains-and-retry", async () => {
    const baseline = mysqlThreadsConnected(mysql.name);
    await openDbTab(page, {
      kind: "mysql", host: "127.0.0.1", port: mysql.port, username: "root",
      password: MYSQL_PASSWORD, title: "acc-mysql-retry",
    });
    await page.waitFor(tabPresent("acc-mysql-retry"));
    await pollUntil(() => mysqlThreadsConnected(mysql.name), (v) => v >= baseline + 1, "MySQL connect did not add a server connection");

    await page.send("Fetch.enable", { patterns: [{ urlPattern: "*/rpc" }] });
    const off = page.on("Fetch.requestPaused", (event) => {
      if (event.request.method === "POST" && event.request.url.includes("/rpc") && (event.request.postData || "").includes("db_disconnect")) {
        const body = Buffer.from(JSON.stringify({ ok: false, error: { code: "internal", message: "acc forced disconnect failure" } })).toString("base64");
        page.send("Fetch.fulfillRequest", {
          requestId: event.requestId, responseCode: 500,
          responseHeaders: [{ name: "Content-Type", value: "application/json" }, { name: "Access-Control-Allow-Origin", value: "*" }],
          body,
        }).catch(() => {});
        return;
      }
      page.send("Fetch.continueRequest", { requestId: event.requestId }).catch(() => {});
    });

    await clickTabClose(page, "acc-mysql-retry");
    await page.waitFor(`${toastText}.includes("数据库断开失败：内部错误：acc forced disconnect failure")`);
    assert.equal(await page.evaluate(tabPresent("acc-mysql-retry")), true, "failed disconnect must retain the tab");
    const retainedConn = mysqlThreadsConnected(mysql.name);
    assert.ok(retainedConn >= baseline + 1, `server connection must survive a failed close: ${retainedConn}`);

    await page.send("Fetch.disable");
    off();
    await clickTabClose(page, "acc-mysql-retry");
    await page.waitFor(`!${tabPresent("acc-mysql-retry")}`);
    const after = await pollUntil(() => mysqlThreadsConnected(mysql.name), (v) => v <= baseline, "MySQL connection survived retried tab close");
    const shot = await screenshot(page, "mysql-tab-close-retry.png");
    return { evidence: { baseline, retainedConn, after, toast: "数据库断开失败：内部错误：acc forced disconnect failure", screenshot: shot } };
  });

  await pass("redis-tab-close-disconnects", async () => {
    const baseline = redisConnectedClients(redis.name);
    const connId = await openDbTab(page, { kind: "redis", host: "127.0.0.1", port: redis.port, title: "acc-redis" });
    await page.waitFor(tabPresent("acc-redis"));
    const withConn = await pollUntil(() => redisConnectedClients(redis.name), (v) => v >= baseline + 1, "Redis connect did not add a server connection");
    await clickTabClose(page, "acc-redis");
    await page.waitFor(`!${tabPresent("acc-redis")}`);
    const after = await pollUntil(() => redisConnectedClients(redis.name), (v) => v <= baseline, "Redis connection survived tab close");
    const rpcError = await rpcErrorOf(page, `window.__nxCommands.dbApi.redisScan(${JSON.stringify(connId)}, 0, "*", 10)`);
    assert.equal(rpcError?.code, "not_found", `server must forget the connection: ${JSON.stringify(rpcError)}`);
    const shot = await screenshot(page, "redis-tab-close.png");
    return { evidence: { baseline, withConn, after, rpcError, screenshot: shot } };
  });

  await pass("redis-workspace-close-disconnects", async () => {
    const baseline = redisConnectedClients(redis.name);
    const connId = await openDbTab(page, { kind: "redis", host: "127.0.0.1", port: redis.port, title: "acc-redis-ws" });
    await page.waitFor(tabPresent("acc-redis-ws"));
    await pollUntil(() => redisConnectedClients(redis.name), (v) => v >= baseline + 1, "Redis connect did not add a server connection");
    await clickWorkspaceClose(page, "acc-redis-ws");
    await page.waitFor(`!${tabPresent("acc-redis-ws")}`);
    const after = await pollUntil(() => redisConnectedClients(redis.name), (v) => v <= baseline, "Redis connection survived workspace close");
    const rpcError = await rpcErrorOf(page, `window.__nxCommands.dbApi.redisScan(${JSON.stringify(connId)}, 0, "*", 10)`);
    assert.equal(rpcError?.code, "not_found", `server must forget the connection: ${JSON.stringify(rpcError)}`);
    const shot = await screenshot(page, "redis-workspace-close.png");
    return { evidence: { baseline, after, rpcError, screenshot: shot } };
  });
}

requireDocker();
const stamp = Date.now();
const mysqlName = `nexterm-acc-mysql-${stamp}`;
const redisName = `nexterm-acc-redis-${stamp}`;
let vite;
let server;
let chrome;
let page;
let mysql;
let redis;
try {
  [mysql, redis] = await Promise.all([
    startContainer(mysqlName, "3306", [
      "--env", `MYSQL_ROOT_PASSWORD=${MYSQL_PASSWORD}`,
      "--env", "MYSQL_ROOT_HOST=%",
      "mysql:8.4",
    ]),
    startContainer(redisName, "6379", ["redis:7.4", "redis-server", "--appendonly", "no"]),
  ]);
  await Promise.all([waitMysql(mysqlName), waitRedis(redisName)]);
  [vite, server, chrome] = await Promise.all([startVite({ root: ROOT, port: VITE_PORT }), startServer(), startChrome()]);
  page = await newPage(chrome);
  await dbLifecycleAcceptance(page, mysql, redis);
} catch (error) {
  harnessErrors.push(String(error?.stack || error));
} finally {
  if (page) page.close();
  stop(chrome?.process);
  stop(server?.process);
  await vite?.stop();
  for (const name of [mysqlName, redisName]) {
    spawnSync("docker", ["rm", "--force", name], { encoding: "utf8", timeout: 30_000 });
  }
}

for (const id of EXPECTED) {
  if (!results.has(id)) record(id, "not-run-dependency-failed", { reason: "the required harness did not complete; this is not a pass or an approved skip" });
}
const checks = [...results.values()];
const failed = checks.filter((check) => check.status !== "passed");
const report = {
  schema_version: 1,
  status: failed.length || harnessErrors.length ? "failed" : "passed",
  browser: chrome?.version || { status: "unavailable" },
  databases: {
    mysql: mysql ? { image: "mysql:8.4", port: mysql.port } : { status: "unavailable" },
    redis: redis ? { image: "redis:7.4", port: redis.port } : { status: "unavailable" },
  },
  execution: { real_browser: true, real_server: true, real_databases: true, headless: true, jsdom: false },
  checks,
  harness_errors: harnessErrors,
  page_errors: pageErrors,
  skip_as_pass: false,
};
fs.writeFileSync(path.join(OUT, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
console.warn(`db lifecycle acceptance: ${checks.filter((check) => check.status === "passed").length}/${EXPECTED.length} checks passed; report=${path.join(OUT, "report.json")}`);
if (failed.length || harnessErrors.length) process.exit(1);
