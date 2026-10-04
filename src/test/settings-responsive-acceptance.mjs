#!/usr/bin/env node
import { spawn, spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const OUT = path.join(ROOT, "target/settings-responsive");
const SHOTS = path.join(OUT, "shots");
const VITE_PORT = Number(process.env.NEXTERM_VITE_PORT || 1424);
const VITE = `http://127.0.0.1:${VITE_PORT}`;
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

function startVite() {
  const command = globalThis.process.platform === "win32" ? "pnpm.cmd" : "pnpm";
  const process = spawn(command, ["exec", "vite", "--host", "127.0.0.1", "--port", String(VITE_PORT), "--strictPort"], {
    cwd: ROOT,
    env: { ...globalThis.process.env, NODE_OPTIONS: "" },
    stdio: ["ignore", "pipe", "pipe"],
  });
  return waitHttp(VITE, process).then(() => process);
}

function startServer() {
  const bin = path.join(OUT, "nexterm-server");
  const built = spawnSync("go", ["build", "-o", bin, "./cmd/nexterm-server"], { cwd: ROOT, stdio: "inherit" });
  if (built.status !== 0) throw new Error("go build ./cmd/nexterm-server failed");
  return (async () => {
    const port = await freePort();
    const data = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-r120-data-"));
    const process = spawn(bin, ["--listen", `127.0.0.1:${port}`, "--data-dir", data], {
      cwd: ROOT,
      env: { ...globalThis.process.env, NEXTERM_MASTER_KEY: "r120-acceptance-master-key" },
      stdio: ["ignore", "pipe", "pipe"],
    });
    await waitHttp(`http://127.0.0.1:${port}/healthz`, process);
    return { process, api: `http://127.0.0.1:${port}`, data };
  })();
}

async function screenshot(page, name) {
  const shot = await page.send("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(path.join(SHOTS, name), Buffer.from(shot.data, "base64"));
  return name;
}

async function setViewport(page, width, height, { coarse = false } = {}) {
  await page.send("Emulation.setDeviceMetricsOverride", {
    width,
    height,
    deviceScaleFactor: 1,
    mobile: width <= 560,
  });
  await page.send("Emulation.setTouchEmulationEnabled", { enabled: coarse, maxTouchPoints: coarse ? 5 : 1 });
  await page.send("Emulation.setEmulatedMedia", {
    features: [
      { name: "pointer", value: coarse ? "coarse" : "fine" },
      { name: "any-pointer", value: coarse ? "coarse" : "fine" },
    ],
  });
  await sleep(250);
}

const READ_OVERFLOW = `(() => {
  const de = document.documentElement;
  const panes = [...document.querySelectorAll(".nx-pane")];
  return {
    innerWidth: window.innerWidth,
    docScrollWidth: de.scrollWidth,
    bodyScrollWidth: document.body.scrollWidth,
    panes: panes.map((p) => ({ scrollWidth: p.scrollWidth, clientWidth: p.clientWidth })),
  };
})()`;

function assertNoOverflow(sample, label) {
  assert.ok(sample.docScrollWidth <= sample.innerWidth, `${label}: document scrollWidth ${sample.docScrollWidth} > innerWidth ${sample.innerWidth}`);
  assert.ok(sample.bodyScrollWidth <= sample.innerWidth, `${label}: body scrollWidth ${sample.bodyScrollWidth} > innerWidth ${sample.innerWidth}`);
  for (const pane of sample.panes) {
    assert.ok(pane.scrollWidth <= pane.clientWidth + 1, `${label}: pane scrollWidth ${pane.scrollWidth} > clientWidth ${pane.clientWidth}`);
  }
}

const READ_GRID = `(() => {
  const grid = [...document.querySelectorAll("div")].find(
    (d) => d.className.includes("grid-cols") && d.querySelector(".nx-kbd"),
  );
  if (!grid) return { found: false };
  const cols = getComputedStyle(grid).gridTemplateColumns;
  return { found: true, cols, tracks: cols.split(" ").length };
})()`;

const READ_CONNECTIVITY = `(() => {
  const btn = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("连通性测试"));
  if (!btn) return { found: false };
  const card = btn.closest(".nx-card");
  const br = btn.getBoundingClientRect();
  const cr = card.getBoundingClientRect();
  return {
    found: true,
    overflowsCard: br.right > cr.right + 1 || br.left < cr.left - 1,
    clipped: btn.scrollWidth > btn.clientWidth + 1,
    btnRight: Math.round(br.right),
    cardRight: Math.round(cr.right),
    height: Math.round(br.height),
  };
})()`;

const READ_MEMORY_ROW = `(() => {
  const span = [...document.querySelectorAll("span[title]")].find((s) =>
    /^[0-9A-HJKMNP-TV-Z]{26}$/.test(s.getAttribute("title") || ""),
  );
  if (!span) return { found: false };
  const card = span.closest(".nx-card");
  const sr = span.getBoundingClientRect();
  const cr = card.getBoundingClientRect();
  return {
    found: true,
    idTitle: span.getAttribute("title"),
    idText: span.textContent,
    titleMatches: span.getAttribute("title") === span.textContent,
    overflowsCard: sr.right > cr.right + 1,
    wraps: getComputedStyle(span.closest("button")).flexWrap,
  };
})()`;

const READ_KNOWNHOST_ROW = `(() => {
  const btn = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("撤销信任"));
  if (!btn) return { found: false };
  const card = btn.closest(".nx-card");
  const br = btn.getBoundingClientRect();
  const cr = card.getBoundingClientRect();
  const hostSpan = [...document.querySelectorAll("span[title]")].find((s) =>
    (s.getAttribute("title") || "").includes("example-internal"),
  );
  return {
    found: true,
    overflowsCard: br.right > cr.right + 1 || br.left < cr.left - 1,
    btnRight: Math.round(br.right),
    cardRight: Math.round(cr.right),
    hostTitle: hostSpan ? hostSpan.getAttribute("title") : null,
    hostText: hostSpan ? hostSpan.textContent : null,
  };
})()`;

const READ_CRON_ROW = `(() => {
  const btn = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "注销");
  if (!btn) return { found: false };
  const card = btn.closest(".nx-card");
  const br = btn.getBoundingClientRect();
  const cr = card.getBoundingClientRect();
  return {
    found: true,
    overflowsCard: br.right > cr.right + 1 || br.left < cr.left - 1,
    btnRight: Math.round(br.right),
    cardRight: Math.round(cr.right),
  };
})()`;

const READ_AUDIT = `(() => {
  const pane = [...document.querySelectorAll(".nx-pane")].find((p) => p.textContent?.includes("审计日志"));
  if (!pane) return { found: false };
  const toolbar = pane.querySelector(".nx-toolbar");
  const refresh = [...toolbar.querySelectorAll("button")].find((b) => b.textContent?.includes("刷新"));
  const rr = refresh.getBoundingClientRect();
  const cells = [...pane.querySelectorAll("tbody tr td:nth-child(5)")].map((td) => td.textContent?.trim());
  return {
    found: true,
    flexWrap: getComputedStyle(toolbar).flexWrap,
    refreshRight: Math.round(rr.right),
    refreshLeft: Math.round(rr.left),
    innerWidth: window.innerWidth,
    reachable: rr.right <= window.innerWidth && rr.left >= 0,
    exitCells: cells.slice(0, 6),
    rowCount: pane.querySelectorAll("tbody tr").length,
  };
})()`;

const LONG_HOST = "r120-acceptance-host-with-a-very-long-hostname.example-internal.company.com";
const LONG_FINGERPRINT = `SHA256:${"b".repeat(43)}`;
const LONG_TOPIC = "operations-r120-long-topic-name-for-narrow-layout-verification";
const LONG_TOKEN = `SHA256:${"a1B2".repeat(11)}`;
const LONG_MEM_CONTENT = `值班备案指纹 ${LONG_TOKEN} 与主机 ${LONG_HOST}：长不换行字符串必须折行不能溢出卡片。`;
const LONG_RULE = "curl-fsSL-http://mirror.example-internal.company.com/nexterm/install/pipeline-setup-v2.sh|sh";
const LONG_JOB_NAME = "r120 磁盘巡检任务（长名称用于验证窄屏折行）";

async function bootWeb(page, api, theme) {
  const { identifier } = await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `
      try { localStorage.clear(); } catch {}
      window.__NEXTERM_TRANSPORT__ = "web";
      try { localStorage.setItem("nexterm.theme.v1", ${JSON.stringify(theme)}); } catch {}
    `,
  });
  try {
    await page.navigate(`${VITE}/?api=${api}`);
    await page.waitFor("!!document.querySelector('.nx-app')");
    await page.waitFor(`(async () => { const { useUi } = await import('/src/app/store.ts'); return useUi.getState().workspaces !== undefined; })()`);
  } finally {
    await page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier });
  }
}

async function seedWorkspace(page) {
  const out = await page.evaluate(`(async () => {
    try {
      const { useUi, openTerminalTab } = await import('/src/app/store.ts');
      const { sessionApi } = await import('/src/ipc/commands.ts');
      const s = await sessionApi.connectLocal();
      const list = useUi.getState().sessions;
      useUi.getState().setSessions([...list.filter((x) => x.id !== s.id), s]);
      await openTerminalTab(s);
      return "ok";
    } catch (e) { return JSON.stringify(e).slice(0, 300); }
  })()`);
  assert.equal(out, "ok", `workspace seed failed: ${out}`);
}

async function seedSettingsData(page) {
  const out = await page.evaluate(`(async () => {
    const out = {};
    try {
      const { aiApi, assetApi } = await import('/src/ipc/commands.ts');
      const { cronApi } = await import('/src/ipc/cron.ts');
      const { memoryApi } = await import('/src/ipc/memory.ts');
      const scope = { tenant: "local", subject: "default" };
      const conv = await aiApi.conversationCreate("R120 布局验收会话：标题故意取得很长用来测试换行");
      out.convId = conv.id;
      await assetApi.knownHostAccept(${JSON.stringify(LONG_HOST)}, 2222, "ssh-ed25519", ${JSON.stringify(LONG_FINGERPRINT)});
      out.kh = "ok";
      const job = await cronApi.register({
        sessionId: conv.id,
        name: ${JSON.stringify(LONG_JOB_NAME)},
        prompt: "检查各分区磁盘使用率并汇报，提示词故意写得比较长用来验证任务卡片在窄屏下的折行",
        schedule: "0 2 * * *",
        timezone: "UTC",
        timeoutMs: 60000,
      });
      out.cronId = job && job.id;
      const mem = await memoryApi.create(scope, ${JSON.stringify(LONG_TOPIC)}, ${JSON.stringify(LONG_MEM_CONTENT)}, "reject");
      out.memId = mem && mem.id;
      await aiApi.setPermission({ mode: "read_write", dangerRules: [${JSON.stringify(LONG_RULE)}] });
      out.rules = "ok";
    } catch (e) { out.seedErr = String(e && e.message ? e.message : e).slice(0, 300); }
    return out;
  })()`);
  assert.ok(out.convId, `conversation seed failed: ${JSON.stringify(out)}`);
  assert.equal(out.kh, "ok", `known host seed failed: ${JSON.stringify(out)}`);
  assert.ok(out.cronId, `cron register failed: ${JSON.stringify(out)}`);
  assert.ok(out.memId, `memory create failed: ${JSON.stringify(out)}`);
  assert.equal(out.rules, "ok", `rules seed failed: ${JSON.stringify(out)}`);
  return out;
}

async function openSettingsTab(page) {
  await page.evaluate(`(async () => {
    const { useUi } = await import('/src/app/store.ts');
    useUi.getState().addTab({ id: 'settings-r120', kind: 'settings', title: '设置', closable: true });
    useUi.getState().setActiveTab('settings-r120');
    return true;
  })()`);
  await page.waitFor(`document.body.textContent.includes("撤销信任")`);
  await page.waitFor(`document.body.textContent.includes("注销")`);
  await page.waitFor(`[...document.querySelectorAll("span[title]")].some((s) => /^[0-9A-HJKMNP-TV-Z]{26}$/.test(s.getAttribute("title") || ""))`);
  await sleep(400);
}

async function settingsMatrix(page, label, { coarse = false } = {}) {
  const widths = [
    [320, 720],
    [360, 760],
    [390, 780],
    [568, 320],
    [768, 900],
    [384, 720],
    [560, 800],
  ];
  for (const [width, height] of widths) {
    await pass(`${label}-no-overflow-${width}x${height}${coarse ? "-coarse" : ""}`, async () => {
      await setViewport(page, width, height, { coarse });
      const sample = await page.evaluate(READ_OVERFLOW);
      assertNoOverflow(sample, `${label} ${width}x${height}`);
      const shot = await screenshot(page, `${label}-${width}x${height}${coarse ? "-coarse" : ""}.png`);
      return { evidence: { sample: { innerWidth: sample.innerWidth, docScrollWidth: sample.docScrollWidth }, shot } };
    });
  }
}

async function darkSettingsChecks(page) {
  await pass("A-grid-single-column-narrow", async () => {
    for (const width of [320, 360, 390]) {
      await setViewport(page, width, 760);
      const grid = await page.evaluate(READ_GRID);
      assert.ok(grid.found, `${width}: shortcut grid not found`);
      assert.equal(grid.tracks, 1, `${width}: expected single column, got ${grid.cols}`);
    }
    return { evidence: { widths: [320, 360, 390] } };
  });

  await pass("A-grid-two-column-768", async () => {
    await setViewport(page, 768, 900);
    const grid = await page.evaluate(READ_GRID);
    assert.ok(grid.found, "768: shortcut grid not found");
    assert.equal(grid.tracks, 2, `768: expected two columns, got ${grid.cols}`);
    return { evidence: { cols: grid.cols } };
  });

  await pass("A-connectivity-button-inside-card-320", async () => {
    await setViewport(page, 320, 720);
    const state = await page.evaluate(READ_CONNECTIVITY);
    assert.ok(state.found, "connectivity button not found");
    assert.equal(state.overflowsCard, false, `button overflows card: ${JSON.stringify(state)}`);
    assert.equal(state.clipped, false, `button text clipped: ${JSON.stringify(state)}`);
    return { evidence: state };
  });

  await pass("A-memory-ulid-row-320", async () => {
    const state = await page.evaluate(READ_MEMORY_ROW);
    assert.ok(state.found, "memory ULID span not found");
    assert.equal(state.titleMatches, true, `title must carry the full ULID: ${JSON.stringify(state)}`);
    assert.equal(state.overflowsCard, false, `ULID span overflows card: ${JSON.stringify(state)}`);
    assert.equal(state.wraps, "wrap", `row must wrap: ${JSON.stringify(state)}`);
    return { evidence: state };
  });

  await pass("A-memory-expand-token-no-overflow-320", async () => {
    await setViewport(page, 320, 720);
    await page.evaluate(`(() => {
      const span = [...document.querySelectorAll("span[title]")].find((s) =>
        /^[0-9A-HJKMNP-TV-Z]{26}$/.test(s.getAttribute("title") || ""),
      );
      if (!span) throw new Error("ULID span not found");
      span.closest("button").click();
    })()`);
    await page.waitFor(`Boolean(document.querySelector("pre"))`);
    const state = await page.evaluate(`(() => {
      const pre = document.querySelector("pre");
      const cs = getComputedStyle(pre);
      return {
        preScrollWidth: pre.scrollWidth,
        preClientWidth: pre.clientWidth,
        overflowWrap: cs.overflowWrap,
        hasLongToken: pre.textContent.includes(${JSON.stringify(LONG_TOKEN)}),
      };
    })()`);
    assert.equal(state.hasLongToken, true, `expanded pre must contain the seeded long token: ${JSON.stringify(state)}`);
    assert.equal(state.overflowWrap, "break-word", `pre must break long tokens: ${JSON.stringify(state)}`);
    assert.ok(state.preScrollWidth <= state.preClientWidth + 1, `expanded pre overflows: ${JSON.stringify(state)}`);
    const sample = await page.evaluate(READ_OVERFLOW);
    assertNoOverflow(sample, "memory expand 320");
    const shot = await screenshot(page, "A-memory-expand-320.png");
    await page.evaluate(`(() => {
      const span = [...document.querySelectorAll("span[title]")].find((s) =>
        /^[0-9A-HJKMNP-TV-Z]{26}$/.test(s.getAttribute("title") || ""),
      );
      span.closest("button").click();
    })()`);
    await page.waitFor(`!document.querySelector("pre")`);
    return { evidence: { state, shot } };
  });

  await pass("A-knownhost-row-320", async () => {
    const state = await page.evaluate(READ_KNOWNHOST_ROW);
    assert.ok(state.found, "revoke button not found");
    assert.equal(state.overflowsCard, false, `revoke button overflows card: ${JSON.stringify(state)}`);
    assert.ok(state.hostTitle?.includes("example-internal"), `host title must carry full host:port: ${JSON.stringify(state)}`);
    return { evidence: state };
  });

  await pass("A-cron-row-320", async () => {
    const state = await page.evaluate(READ_CRON_ROW);
    assert.ok(state.found, "cron unregister button not found");
    assert.equal(state.overflowsCard, false, `unregister button overflows card: ${JSON.stringify(state)}`);
    return { evidence: state };
  });
}

async function auditChecks(page) {
  for (const [width, height] of [[320, 720], [390, 780], [768, 900]]) {
    await pass(`B-audit-toolbar-reachable-${width}`, async () => {
      await setViewport(page, width, height);
      const state = await page.evaluate(READ_AUDIT);
      assert.ok(state.found, "audit pane not found");
      assert.equal(state.flexWrap, "wrap", `toolbar must wrap: ${JSON.stringify(state)}`);
      assert.equal(state.reachable, true, `refresh button not reachable: ${JSON.stringify(state)}`);
      assert.ok(state.rowCount > 0, "audit rows expected in demo transport");
      assert.ok(state.exitCells.some((c) => c?.includes("✓")), `non-color exit indicator expected: ${JSON.stringify(state.exitCells)}`);
      const sample = await page.evaluate(READ_OVERFLOW);
      assertNoOverflow(sample, `audit ${width}`);
      const shot = await screenshot(page, `B-audit-${width}.png`);
      return { evidence: { state: { flexWrap: state.flexWrap, reachable: state.reachable, exitCells: state.exitCells }, shot } };
    });
  }
}

async function syncClientChecks(chrome, api) {
  const page = await newPage(chrome);
  await page.send("Network.enable");
  await page.send("Network.setCacheDisabled", { cacheDisabled: true });
  const fabrications = {
    sync_link_get: null,
    sync_link_set: { url: "https://sync.example.com", tokenKind: "server", token: "saved-token", insecure: false, verifiedAt: 1, lastError: null },
    sync_digest: {
      origin: "local",
      assets: [
        { id: "a1", name: "web-01", kind: "ssh", host: LONG_HOST, updatedAt: 2, deletedAt: null, hasCred: true },
      ],
    },
    sync_remote_digest: { origin: "remote", assets: [] },
  };
  await page.send("Fetch.enable", {
    patterns: [{ urlPattern: "*/src/features/settings/SyncCard.tsx*", requestStage: "Response" }, { urlPattern: "*/rpc" }],
  });
  page.on("Fetch.requestPaused", async (params) => {
    const passthrough = async () => {
      try {
        await page.send("Fetch.continueRequest", { requestId: params.requestId });
      } catch {}
    };
    try {
      if (params.request.url.includes("/src/features/settings/SyncCard.tsx")) {
        if (params.responseStatusCode !== 200) {
          await passthrough();
          return;
        }
        const body = await page.send("Fetch.getResponseBody", { requestId: params.requestId });
        const source = Buffer.from(body.body, body.base64Encoded ? "base64" : "utf8").toString("utf8");
        const marker = "const isServer = WEB;";
        if (!source.includes(marker)) {
          harnessErrors.push("SyncCard interception missed isServer marker");
          await passthrough();
          return;
        }
        await page.send("Fetch.fulfillRequest", {
          requestId: params.requestId,
          responseCode: 200,
          responseHeaders: params.responseHeaders,
          body: Buffer.from(source.replace(marker, "const isServer = false;"), "utf8").toString("base64"),
        });
        return;
      }
      if (params.request.url.includes("/rpc") && params.request.method === "POST") {
        let cmd = null;
        try {
          cmd = JSON.parse(params.request.postData || "{}").cmd;
        } catch {}
        if (cmd && Object.hasOwn(fabrications, cmd)) {
          await page.send("Fetch.fulfillRequest", {
            requestId: params.requestId,
            responseCode: 200,
            responseHeaders: [
              { name: "content-type", value: "application/json" },
              { name: "access-control-allow-origin", value: "*" },
            ],
            body: Buffer.from(JSON.stringify({ ok: true, data: fabrications[cmd] }), "utf8").toString("base64"),
          });
          return;
        }
        await passthrough();
        return;
      }
      await passthrough();
    } catch (error) {
      harnessErrors.push(`sync interception: ${String(error?.stack || error)}`);
      await passthrough();
    }
  });

  const { identifier } = await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `
      try { localStorage.clear(); } catch {}
      window.__NEXTERM_TRANSPORT__ = "web";
      try { localStorage.setItem("nexterm.theme.v1", "dark"); } catch {}
    `,
  });
  try {
    await page.navigate(`${VITE}/?api=${api}`);
    await page.waitFor("!!document.querySelector('.nx-app')");
  } finally {
    await page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier });
  }
  await seedWorkspace(page);
  await page.evaluate(`(async () => {
    const { useUi } = await import('/src/app/store.ts');
    useUi.getState().addTab({ id: 'settings-r120c', kind: 'settings', title: '设置', closable: true });
    useUi.getState().setActiveTab('settings-r120c');
    return true;
  })()`);
  await page.waitFor(`Boolean(document.querySelector("#sync-url"))`);
  await page.evaluate(`(() => {
    const set = (el, value) => {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set;
      setter.call(el, value);
      el.dispatchEvent(new Event("input", { bubbles: true }));
    };
    set(document.querySelector("#sync-url"), "https://sync.example.com");
    set(document.querySelector("#sync-token"), "fresh-token");
  })()`);
  await page.waitFor(`[...document.querySelectorAll("button")].some((b) => b.textContent?.trim() === "保存并测试连接" && !b.disabled)`);
  await page.evaluate(`[...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "保存并测试连接").click()`);
  await page.waitFor(`[...document.querySelectorAll("span[title]")].some((s) => s.getAttribute("title") === ${JSON.stringify(LONG_HOST)})`);
  await sleep(300);

  await pass("C-sync-client-cred-row-320", async () => {
    await setViewport(page, 320, 720);
    const state = await page.evaluate(`(() => {
      const hostSpan = [...document.querySelectorAll("span[title]")].find((s) => s.getAttribute("title") === ${JSON.stringify(LONG_HOST)});
      if (!hostSpan) return { found: false };
      const row = hostSpan.closest("label");
      const badge = row.querySelector(".nx-badge");
      const rr = row.getBoundingClientRect();
      const br = badge.getBoundingClientRect();
      return {
        found: true,
        rowScrollWidth: row.scrollWidth,
        rowClientWidth: row.clientWidth,
        badgeRight: Math.round(br.right),
        rowRight: Math.round(rr.right),
        badgeClipped: br.right > rr.right + 1,
        rowOverflows: row.scrollWidth > row.clientWidth + 1,
        credVisible: row.textContent.includes("带凭据"),
        hostTitle: hostSpan.getAttribute("title"),
        badgeText: badge.textContent,
      };
    })()`);
    assert.ok(state.found, "sync compare row with long host not found");
    assert.equal(state.rowOverflows, false, `compare row horizontally overflows: ${JSON.stringify(state)}`);
    assert.equal(state.badgeClipped, false, `status badge clipped: ${JSON.stringify(state)}`);
    assert.equal(state.credVisible, true, `cred marker must be visible: ${JSON.stringify(state)}`);
    assert.equal(state.hostTitle, LONG_HOST);
    const sample = await page.evaluate(READ_OVERFLOW);
    assertNoOverflow(sample, "sync client 320");
    await page.evaluate(`(() => {
      const hostSpan = [...document.querySelectorAll("span[title]")].find((s) => s.getAttribute("title") === ${JSON.stringify(LONG_HOST)});
      hostSpan.closest("label").scrollIntoView({ block: "center" });
    })()`);
    await sleep(150);
    const shot = await screenshot(page, "C-sync-client-cred-row-320.png");
    return { evidence: { state, shot } };
  });
  page.close();
}

let vite;
let chrome;
let server;
try {
  [vite, server] = await Promise.all([startVite(), startServer()]);
  chrome = await startChrome();
  console.warn(`vite=${VITE} server=${server.api}`);

  const pageA = await newPage(chrome);
  await bootWeb(pageA, server.api, "dark");
  await seedWorkspace(pageA);
  const seed = await seedSettingsData(pageA);
  record("A-seed", "passed", { evidence: seed });
  await openSettingsTab(pageA);
  await darkSettingsChecks(pageA);
  await settingsMatrix(pageA, "A-dark", { coarse: false });
  await settingsMatrix(pageA, "A-dark", { coarse: true });

  await bootWeb(pageA, server.api, "light");
  await seedWorkspace(pageA);
  await openSettingsTab(pageA);
  for (const width of [320, 390]) {
    await pass(`A-light-no-overflow-${width}`, async () => {
      await setViewport(pageA, width, 760);
      const sample = await pageA.evaluate(READ_OVERFLOW);
      assertNoOverflow(sample, `light ${width}`);
      const shot = await screenshot(pageA, `A-light-${width}.png`);
      return { evidence: { innerWidth: sample.innerWidth, docScrollWidth: sample.docScrollWidth, shot } };
    });
  }
  pageA.close();

  const pageB = await newPage(chrome);
  await pageB.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `try { localStorage.clear(); localStorage.setItem("nexterm.theme.v1", "dark"); } catch {}`,
  });
  await pageB.navigate(`${VITE}/?demo=1`);
  await pageB.waitFor("!!document.querySelector('.nx-app')");
  await pageB.waitFor(`(async () => { const { useUi } = await import('/src/app/store.ts'); return useUi.getState().workspaces !== undefined; })()`);
  await pageB.evaluate(`(async () => {
    const { useUi } = await import('/src/app/store.ts');
    useUi.getState().addTab({ id: 'audit-r120', kind: 'audit', title: '审计日志', closable: true });
    useUi.getState().setActiveTab('audit-r120');
    return true;
  })()`);
  await pageB.waitFor(`document.body.textContent.includes("共 ")`);
  await sleep(400);
  await auditChecks(pageB);
  pageB.close();

  await syncClientChecks(chrome, server.api);
} catch (error) {
  harnessErrors.push(String(error?.stack || error));
} finally {
  stop(chrome?.process);
  stop(server?.process);
  stop(vite);
}

const checks = [...results.values()];
const failed = checks.filter((check) => check.status !== "passed");
const isAllowedPageError = (message) =>
  message.includes("Cannot read properties of undefined (reading 'dimensions')") &&
  message.includes("@xterm_xterm");
const unexpectedPageErrors = pageErrors.filter((message) => !isAllowedPageError(message));
for (const message of unexpectedPageErrors) harnessErrors.push(`unexpected page error: ${message}`);
const report = {
  schema_version: 1,
  status: failed.length || harnessErrors.length ? "failed" : "passed",
  browser: chrome?.version || { status: "unavailable" },
  execution: {
    real_browser: true,
    headless: true,
    jsdom: false,
    transport: "real nexterm-server (Go) for settings cards; demo transport for audit rows",
    matrix: "320/360/390/568x320 landscape/768 + 200%-equivalent 384 & 560 + coarse pointer + dark/light",
  },
  checks,
  harness_errors: harnessErrors,
  page_errors_allowed: pageErrors.filter(isAllowedPageError),
  page_errors_unexpected: unexpectedPageErrors,
};
fs.writeFileSync(path.join(OUT, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
console.warn(`settings responsive acceptance: ${checks.filter((check) => check.status === "passed").length}/${checks.length} checks passed; report=${path.join(OUT, "report.json")}`);
if (failed.length || harnessErrors.length) process.exit(1);
