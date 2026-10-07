#!/usr/bin/env node
import { spawn, spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { startVite as startViteProcess } from "./lib/acceptance-process.mjs";

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
  const config = path.join(OUT, "vite.acceptance.config.ts");
  fs.writeFileSync(
    config,
    [
      'import baseConfig from "../../vite.config";',
      'import { defineConfig, type Plugin } from "vite";',
      "",
      'const marker = "if (WEB) return <WebSyncConsole />;";',
      "",
      "const disableWebSyncConsole: Plugin = {",
      '  name: "acceptance-disable-web-sync-console",',
      '  enforce: "pre",',
      "  transform(code, id) {",
      '    if (!id.replace(/\\\\/g, "/").endsWith("src/features/settings/SyncCard.tsx")) return;',
      "    const occurrences = code.split(marker).length - 1;",
      "    if (occurrences !== 1) {",
      '      throw new Error(`acceptance plugin expected exactly 1 WebSyncConsole marker in SyncCard.tsx, found ${occurrences}`);',
      "    }",
      '    return code.replace(marker, "if (false) return <WebSyncConsole />;");',
      "  },",
      "};",
      "",
      "export default defineConfig({",
      "  ...baseConfig,",
      "  plugins: [...(baseConfig.plugins ?? []), disableWebSyncConsole],",
      "});",
      "",
    ].join("\n"),
  );
  return startViteProcess({ root: ROOT, port: VITE_PORT, config, logFile: path.join(OUT, "vite.log") });
}

function startServer() {
  const bin = path.join(OUT, "nexterm-server");
  const built = spawnSync("go", ["build", "-o", bin, "./cmd/nexterm-server"], { cwd: ROOT, stdio: "inherit" });
  if (built.status !== 0) throw new Error("go build ./cmd/nexterm-server failed");
  return (async () => {
    const port = await freePort();
    const data = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-r120-data-"));
    const process = spawn(bin, ["--listen", `127.0.0.1:${port}`, "--data-dir", data, "--auth=loopback"], {
      cwd: ROOT,
      env: { ...globalThis.process.env, NEXTERM_MASTER_KEY: "r120-acceptance-master-key" },
      stdio: ["ignore", "pipe", "pipe"],
    });
    const logStream = fs.createWriteStream(path.join(OUT, "server.log"));
    process.stdout.pipe(logStream);
    process.stderr.pipe(logStream);
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

const READ_CRON_PROFILE = `(() => {
  const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("定时任务"));
  if (!card) return { found: false };
  const text = card.textContent || "";
  const cr = card.getBoundingClientRect();
  return {
    found: text.includes("档案「r120 验收档案"),
    deleted: text.includes("档案已删除"),
    unavailable: text.includes("档案密钥不可用"),
    saved: text.includes("档案密钥已保存"),
    leaked: text.includes("r120-acceptance-key"),
    overflowsViewport: cr.right > window.innerWidth + 1,
  };
})()`;

const READ_CRON_PROFILE_SELECT = `(() => {
  const select = document.querySelector('select[aria-label="模型档案"]');
  if (!select) return { found: false };
  const sr = select.getBoundingClientRect();
  const options = [...select.options].map((o) => o.textContent || "");
  return {
    found: true,
    value: select.value,
    options,
    savedOption: options.find((o) => o.includes("r120 验收档案") && o.includes(" · ")) ?? null,
    leaked: options.some((o) => o.includes("r120-acceptance-key")),
    overflowsViewport: sr.right > window.innerWidth + 1,
  };
})()`;

const READ_FONT_SCALE_SEGMENT = `(() => {
  const seg = [...document.querySelectorAll(".nx-segment")].find(
    (s) => s.getAttribute("aria-label") === "界面文字倍率",
  );
  if (!seg) return { found: false };
  const card = seg.closest(".nx-card");
  const sr = seg.getBoundingClientRect();
  const cr = card.getBoundingClientRect();
  const items = [...seg.querySelectorAll(".nx-segment-item")];
  const rows = new Set(items.map((it) => Math.round(it.getBoundingClientRect().top)));
  return {
    found: true,
    itemCount: items.length,
    labels: items.map((it) => it.textContent),
    segScrollWidth: seg.scrollWidth,
    segClientWidth: seg.clientWidth,
    overflows: seg.scrollWidth > seg.clientWidth + 1,
    insideCard: sr.left >= cr.left - 1 && sr.right <= cr.right + 1,
    flexWrap: getComputedStyle(seg).flexWrap,
    rows: rows.size,
    minItemHeight: Math.round(Math.min(...items.map((it) => it.getBoundingClientRect().height))),
  };
})()`;

const READ_APPEARANCE_SIBLINGS = `(() => {
  const out = {};
  for (const label of ["界面主题", "界面字号", "终端与编辑器主题"]) {
    const seg = [...document.querySelectorAll(".nx-segment")].find(
      (s) => s.getAttribute("aria-label") === label,
    );
    if (!seg) {
      out[label] = { found: false };
      continue;
    }
    const card = seg.closest(".nx-card");
    const sr = seg.getBoundingClientRect();
    const cr = card.getBoundingClientRect();
    const items = [...seg.querySelectorAll(".nx-segment-item")];
    const rows = new Set(items.map((it) => Math.round(it.getBoundingClientRect().top)));
    out[label] = {
      found: true,
      rows: rows.size,
      flexWrap: getComputedStyle(seg).flexWrap,
      overflows: seg.scrollWidth > seg.clientWidth + 1,
      insideCard: sr.left >= cr.left - 1 && sr.right <= cr.right + 1,
    };
  }
  const reset = [...document.querySelectorAll("button")].find(
    (b) => b.getAttribute("aria-label") === "恢复默认主题与外观",
  );
  if (reset) {
    const card = reset.closest(".nx-card");
    const br = reset.getBoundingClientRect();
    const cr = card.getBoundingClientRect();
    out.reset = { found: true, insideCard: br.left >= cr.left - 1 && br.right <= cr.right + 1 };
  } else {
    out.reset = { found: false };
  }
  return out;
})()`;

const READ_FONT_SCALE_PREF = `(() => {
  const seg = [...document.querySelectorAll(".nx-segment")].find(
    (s) => s.getAttribute("aria-label") === "界面文字倍率",
  );
  const pressed = [...seg.querySelectorAll(".nx-segment-item")].map((it) => ({
    label: it.textContent,
    pressed: it.getAttribute("aria-pressed") === "true",
  }));
  return {
    pressed,
    uiScale: document.documentElement.style.getPropertyValue("--nx-ui-scale"),
  };
})()`;

const CLICK_FONT_SCALE_STEP = (step) => `(() => {
  const seg = [...document.querySelectorAll(".nx-segment")].find(
    (s) => s.getAttribute("aria-label") === "界面文字倍率",
  );
  const item = [...seg.querySelectorAll(".nx-segment-item")].find((it) => it.textContent === ${JSON.stringify(step)});
  if (!item) throw new Error("scale step not found: " + ${JSON.stringify(step)});
  item.click();
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
      const { aiApi, assetApi, modelApi } = await import('/src/ipc/commands.ts');
      const { cronApi } = await import('/src/ipc/cron.ts');
      const { memoryApi } = await import('/src/ipc/memory.ts');
      const scope = { tenant: "local", subject: "default" };
      const conv = await aiApi.conversationCreate("R120 布局验收会话：标题故意取得很长用来测试换行");
      out.convId = conv.id;
      await assetApi.knownHostAccept(${JSON.stringify(LONG_HOST)}, 2222, "ssh-ed25519", ${JSON.stringify(LONG_FINGERPRINT)});
      out.kh = "ok";
      const savedProfile = await modelApi.save({
        id: "",
        name: "r120 验收档案（长名称验证窄屏折行）",
        baseUrl: "https://api.example.com",
        apiKey: "r120-acceptance-key",
        model: "r120-acceptance-model",
        temperature: 0.3,
        contextWindow: 32768,
        proxy: null,
        stream: true,
        fallbackModel: null,
      });
      out.profileId = savedProfile && savedProfile.id;
      const job = await cronApi.register({
        sessionId: conv.id,
        name: ${JSON.stringify(LONG_JOB_NAME)},
        prompt: "检查各分区磁盘使用率并汇报，提示词故意写得比较长用来验证任务卡片在窄屏下的折行",
        schedule: "0 2 * * *",
        timezone: "UTC",
        timeoutMs: 60000,
        modelProfileId: out.profileId,
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
  assert.ok(out.profileId, `model profile seed failed: ${JSON.stringify(out)}`);
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

  await pass("A-cron-profile-row-320", async () => {
    await setViewport(page, 320, 720);
    const state = await page.evaluate(READ_CRON_PROFILE);
    assert.ok(state.found, `cron row must show the saved profile name: ${JSON.stringify(state)}`);
    assert.equal(state.deleted, false, `saved profile must not be flagged deleted: ${JSON.stringify(state)}`);
    assert.equal(state.unavailable, false, `saved profile must not be flagged unavailable: ${JSON.stringify(state)}`);
    assert.equal(state.saved, true, `saved profile must present the masked key as a saved credential: ${JSON.stringify(state)}`);
    assert.equal(state.leaked, false, `cron card must not leak key material: ${JSON.stringify(state)}`);
    assert.equal(state.overflowsViewport, false, `cron card overflows viewport: ${JSON.stringify(state)}`);
    const shot = await screenshot(page, "A-cron-profile-row-320.png");
    return { evidence: { shot } };
  });

  await pass("A-cron-profile-selector-320", async () => {
    await setViewport(page, 320, 720);
    await page.evaluate(`(() => {
      const btn = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "注册定时任务");
      if (!btn) throw new Error("register button not found");
      btn.click();
    })()`);
    await page.waitFor(`!!document.querySelector('select[aria-label="模型档案"]')`);
    const state = await page.evaluate(READ_CRON_PROFILE_SELECT);
    assert.ok(state.found, "cron profile select not found");
    assert.equal(state.value, "", `default must be the explicit follow-active option: ${JSON.stringify(state)}`);
    assert.ok(
      state.options.some((o) => o.includes("跟随当前激活档案")),
      `follow-active default option missing: ${JSON.stringify(state.options)}`,
    );
    assert.ok(
      state.options.some((o) => o.includes("r120 验收档案")),
      `saved profile missing from selector options: ${JSON.stringify(state.options)}`,
    );
    assert.ok(
      state.savedOption?.includes("（密钥已保存）"),
      `saved profile option must present the masked key as saved: ${JSON.stringify(state.savedOption)}`,
    );
    assert.equal(
      state.savedOption?.includes("密钥不可用"),
      false,
      `saved profile option must not claim the key is unavailable: ${JSON.stringify(state.savedOption)}`,
    );
    assert.equal(state.leaked, false, `profile options must not leak key material: ${JSON.stringify(state.options)}`);
    assert.equal(state.overflowsViewport, false, `profile select overflows viewport: ${JSON.stringify(state)}`);
    const sample = await page.evaluate(READ_OVERFLOW);
    assertNoOverflow(sample, "cron profile selector 320");
    const shot = await screenshot(page, "A-cron-profile-selector-320.png");
    await page.evaluate(`(() => {
      const btn = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "取消");
      if (!btn) throw new Error("cancel button not found");
      btn.click();
    })()`);
    await page.waitFor(`!document.querySelector('select[aria-label="模型档案"]')`);
    return { evidence: { options: state.options, shot } };
  });
}

async function appearanceFontScaleChecks(page) {
  await pass("A-fontscale-segment-fits-320", async () => {
    await setViewport(page, 320, 720);
    const state = await page.evaluate(READ_FONT_SCALE_SEGMENT);
    assert.ok(state.found, "font-scale segment not found");
    assert.equal(state.flexWrap, "wrap", `font-scale segment must wrap: ${JSON.stringify(state)}`);
    assert.equal(state.overflows, false, `font-scale segment overflows horizontally: ${JSON.stringify(state)}`);
    assert.equal(state.insideCard, true, `font-scale segment escapes its card: ${JSON.stringify(state)}`);
    assert.equal(state.itemCount, 5, `expected 5 scale steps: ${JSON.stringify(state)}`);
    assert.deepEqual(state.labels, ["100%", "125%", "150%", "175%", "200%"]);
    const sample = await page.evaluate(READ_OVERFLOW);
    assertNoOverflow(sample, "font-scale 320");
    const shot = await screenshot(page, "A-fontscale-segment-320.png");
    return { evidence: { state, shot } };
  });

  await pass("A-fontscale-segment-fits-390", async () => {
    await setViewport(page, 390, 780);
    const state = await page.evaluate(READ_FONT_SCALE_SEGMENT);
    assert.ok(state.found, "font-scale segment not found");
    assert.equal(state.overflows, false, `font-scale segment overflows at 390: ${JSON.stringify(state)}`);
    assert.equal(state.insideCard, true, `font-scale segment escapes its card at 390: ${JSON.stringify(state)}`);
    return { evidence: state };
  });

  await pass("A-fontscale-segment-single-row-desktop", async () => {
    await setViewport(page, 1280, 900);
    const state = await page.evaluate(READ_FONT_SCALE_SEGMENT);
    assert.ok(state.found, "font-scale segment not found");
    assert.equal(state.rows, 1, `desktop must stay single-row: ${JSON.stringify(state)}`);
    assert.equal(state.overflows, false, `font-scale segment overflows at desktop: ${JSON.stringify(state)}`);
    return { evidence: state };
  });

  await pass("A-fontscale-segment-touch-target-coarse-320", async () => {
    await setViewport(page, 320, 720, { coarse: true });
    const state = await page.evaluate(READ_FONT_SCALE_SEGMENT);
    assert.ok(state.found, "font-scale segment not found");
    assert.equal(state.overflows, false, `font-scale segment overflows with coarse pointer: ${JSON.stringify(state)}`);
    assert.ok(state.minItemHeight >= 44, `touch target must stay >= 44px: ${JSON.stringify(state)}`);
    await setViewport(page, 320, 720, { coarse: false });
    return { evidence: state };
  });

  await pass("A-appearance-sibling-segments-single-row-320", async () => {
    await setViewport(page, 320, 720);
    const state = await page.evaluate(READ_APPEARANCE_SIBLINGS);
    for (const label of ["界面主题", "界面字号", "终端与编辑器主题"]) {
      const seg = state[label];
      assert.ok(seg?.found, `${label} segment not found`);
      assert.equal(seg.rows, 1, `${label} must stay single-row at 320: ${JSON.stringify(seg)}`);
      assert.equal(seg.overflows, false, `${label} overflows at 320: ${JSON.stringify(seg)}`);
      assert.equal(seg.insideCard, true, `${label} escapes its card at 320: ${JSON.stringify(seg)}`);
    }
    assert.ok(state.reset?.found, "appearance reset button not found");
    assert.equal(state.reset.insideCard, true, `reset button escapes its card at 320: ${JSON.stringify(state.reset)}`);
    return { evidence: state };
  });

  await pass("A-fontscale-segment-pref-behavior-320", async () => {
    await setViewport(page, 320, 720);
    try {
      await page.evaluate(CLICK_FONT_SCALE_STEP("125%"));
      await page.waitFor(`document.documentElement.style.getPropertyValue("--nx-ui-scale") === "1.25"`);
      const after125 = await page.evaluate(READ_FONT_SCALE_PREF);
      assert.deepEqual(
        after125.pressed.filter((p) => p.pressed).map((p) => p.label),
        ["125%"],
        `only 125% must be active: ${JSON.stringify(after125)}`,
      );
      const segState = await page.evaluate(READ_FONT_SCALE_SEGMENT);
      assert.equal(segState.overflows, false, `segment must not overflow at 125% text scale: ${JSON.stringify(segState)}`);
      assert.equal(segState.insideCard, true, `segment must stay inside card at 125% text scale: ${JSON.stringify(segState)}`);
    } finally {
      await page.evaluate(CLICK_FONT_SCALE_STEP("100%"));
      await page.waitFor(`document.documentElement.style.getPropertyValue("--nx-ui-scale") === "1"`);
    }
    const restored = await page.evaluate(READ_FONT_SCALE_PREF);
    assert.deepEqual(
      restored.pressed.filter((p) => p.pressed).map((p) => p.label),
      ["100%"],
      `scale must restore to 100%: ${JSON.stringify(restored)}`,
    );
    return { evidence: { restored } };
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

// M197: 配对码添加设备的完整路径(demo transport,真实前端代码 + 真实 demo 假后端校验)。
// 签发配对码 → 登出 → 登录门「用配对码添加设备」→ enroll 登记 → 统一登录绑定设备 → 设备列表可见;
// 无效码如实报错不进入登录步;320px 下门表单不溢出。
const SET_INPUT = `(el, value) => {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set;
  setter.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
}`;

async function enrollChecks(chrome) {
  const page = await newPage(chrome);
  await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `try { localStorage.clear(); localStorage.setItem("nexterm.theme.v1", "dark"); } catch {}`,
  });
  await page.navigate(`${VITE}/?demo=1`);
  await page.waitFor("!!document.querySelector('.nx-app')");
  await page.waitFor(`(async () => { const { useUi } = await import('/src/app/store.ts'); return useUi.getState().workspaces !== undefined; })()`);
  await page.evaluate(`(async () => {
    const { useUi } = await import('/src/app/store.ts');
    useUi.getState().addTab({ id: 'settings-enroll', kind: 'settings', title: '设置', closable: true });
    useUi.getState().setActiveTab('settings-enroll');
    return true;
  })()`);
  await page.waitFor(`document.body.textContent.includes("登录设备")`);
  await sleep(400);

  const ENROLL_DEVICE_NAME = "验收浏览器-m197";
  // 种子设备快照: 后续断言 enroll 登录只更新返回设备、不触碰任何种子设备。
  const seededSnapshot = await page.evaluate(`(async () => {
    const { demoAuthRequest } = await import('/src/demo/auth.ts');
    const r = await demoAuthRequest("GET", "/auth/devices");
    return r.devices;
  })()`);
  assert.ok(Array.isArray(seededSnapshot) && seededSnapshot.length >= 3, "seed devices snapshot failed");

  await pass("D-enroll-issue-code", async () => {
    const code = await page.evaluate(`(async () => {
      const btn = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "添加设备");
      if (!btn) throw new Error("添加设备 button not found");
      btn.click();
      const deadline = Date.now() + 10_000;
      while (Date.now() < deadline) {
        const el = [...document.querySelectorAll("code")].find((c) => c.textContent?.startsWith("demo-enroll-"));
        if (el) return el.textContent;
        await new Promise((r) => setTimeout(r, 100));
      }
      throw new Error("enroll code not rendered");
    })()`);
    assert.ok(code.startsWith("demo-enroll-"), `unexpected demo enroll code: ${code}`);
    return { evidence: { code } };
  });

  const enrollCode = await page.evaluate(`(() => {
    const el = [...document.querySelectorAll("code")].find((c) => c.textContent?.startsWith("demo-enroll-"));
    return el ? el.textContent : null;
  })()`);
  assert.ok(enrollCode, "enroll code must still be displayed before logout");

  // 登出(走真实 store.logout → demo /auth/logout),登录门出现
  await page.evaluate(`(async () => {
    const { useAuth } = await import('/src/features/auth/store.ts');
    await useAuth.getState().logout();
    return true;
  })()`);
  await page.waitFor(`document.body.textContent.includes("用配对码添加设备")`);

  await pass("D-enroll-full-flow", async () => {
    await page.evaluate(`(() => {
      const btn = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("用配对码添加设备"));
      if (!btn) throw new Error("用配对码添加设备 entry not found");
      btn.click();
    })()`);
    await page.waitFor(`document.body.textContent.includes("15 分钟内有效")`);
    const deviceName = ENROLL_DEVICE_NAME;
    await page.evaluate(`(() => {
      const gate = document.querySelector(".fixed.inset-0");
      if (!gate) throw new Error("auth gate overlay not found");
      const inputs = gate.querySelectorAll("input");
      if (inputs.length !== 2) throw new Error("enroll form inputs mismatch: " + inputs.length);
      const set = ${SET_INPUT};
      set(inputs[0], ${JSON.stringify(enrollCode)});
      set(inputs[1], ${JSON.stringify(deviceName)});
    })()`);
    await page.evaluate(`(() => {
      const btn = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("添加并继续"));
      if (!btn) throw new Error("添加并继续 button not found");
      btn.click();
    })()`);
    await page.waitFor(`document.body.textContent.includes("设备已登记")`);
    await page.evaluate(`(() => {
      const gate = document.querySelector(".fixed.inset-0");
      const inputs = gate.querySelectorAll("input");
      const set = ${SET_INPUT};
      set(inputs[0], "demo");
      set(inputs[1], "demo-pass-123");
    })()`);
    await page.evaluate(`(() => {
      const btn = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("登录并完成添加"));
      if (!btn) throw new Error("登录并完成添加 button not found");
      btn.click();
    })()`);
    // 统一登录成功后门关闭,设备出现在登录设备列表
    await page.waitFor(`!document.querySelector(".fixed.inset-0")`);
    await page.waitFor(`document.body.textContent.includes(${JSON.stringify(deviceName)})`);
    const state = await page.evaluate(`(() => {
      const cards = [...document.querySelectorAll(".nx-card")];
      const card = cards.find((c) => c.textContent?.includes("登录设备"));
      if (!card) return { found: false };
      const rows = [...card.querySelectorAll("div")].filter((d) => d.textContent?.includes(${JSON.stringify(deviceName)}));
      return {
        found: true,
        rowCount: rows.length,
        active: rows.some((d) => d.textContent?.includes("生效中")),
      };
    })()`);
    assert.ok(state.found, "登录设备 card not found after enroll login");
    assert.ok(state.rowCount > 0, `enrolled device row missing: ${JSON.stringify(state)}`);
    assert.equal(state.active, true, `enrolled device must be active: ${JSON.stringify(state)}`);

    // 不能只看设备名和「生效中」: 必须核对后端列表 id 唯一、enroll 返回的设备才是被登录更新的那台。
    const backend = await page.evaluate(`(async () => {
      const { demoAuthRequest } = await import('/src/demo/auth.ts');
      const r = await demoAuthRequest("GET", "/auth/devices");
      return r.devices;
    })()`);
    const ids = backend.map((d) => d.id);
    assert.equal(new Set(ids).size, ids.length, `device ids must be unique: ${ids}`);
    const enrolledRows = backend.filter((d) => d.name === deviceName);
    assert.equal(enrolledRows.length, 1, `exactly one enrolled device expected: ${JSON.stringify(backend)}`);
    const enrolledDevice = enrolledRows[0];
    for (const seeded of seededSnapshot) {
      assert.ok(enrolledDevice.id !== seeded.id, `enrolled id must not collide with seed ${seeded.id}`);
    }
    assert.ok(enrolledDevice.last_seen_at > 0, `login must touch the enrolled device: ${JSON.stringify(enrolledDevice)}`);
    for (const seeded of seededSnapshot) {
      const current = backend.find((d) => d.id === seeded.id);
      assert.equal(current?.last_seen_at, seeded.last_seen_at, `seed ${seeded.id} last_seen must be untouched by enroll login`);
    }
    const shot = await screenshot(page, "D-enroll-device-listed.png");
    return { evidence: { deviceName, enrolledId: enrolledDevice.id, state, shot } };
  });

  await pass("D-enroll-revoke-target", async () => {
    // 吊销必须只命中 enroll 返回的设备: 种子 d-demo-1/2 保持生效, d-demo-3 保持种子态已吊销。
    const revoked = await page.evaluate(`(async () => {
      const { demoAuthRequest } = await import('/src/demo/auth.ts');
      const before = (await demoAuthRequest("GET", "/auth/devices")).devices;
      const target = before.find((d) => d.name === ${JSON.stringify(ENROLL_DEVICE_NAME)});
      if (!target) throw new Error("enrolled device not found in demo backend");
      await demoAuthRequest("DELETE", "/auth/devices/" + encodeURIComponent(target.id));
      const after = (await demoAuthRequest("GET", "/auth/devices")).devices;
      return { targetId: target.id, after };
    })()`);
    const enrolledRow = revoked.after.find((d) => d.id === revoked.targetId);
    assert.ok(enrolledRow?.revoked_at > 0, `revoke must hit the enrolled device: ${JSON.stringify(revoked)}`);
    for (const id of ["d-demo-1", "d-demo-2"]) {
      assert.equal(revoked.after.find((d) => d.id === id)?.revoked_at, 0, `${id} must stay active after targeted revoke`);
    }
    assert.ok(revoked.after.find((d) => d.id === "d-demo-3")?.revoked_at > 0, "d-demo-3 must stay revoked as seeded");

    // UI 刷新后行状态如实翻转: enroll 的行已吊销, 种子「这台浏览器」仍生效中。
    await page.evaluate(`(() => {
      const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("登录设备"));
      const btn = [...card.querySelectorAll("button")].find((b) => b.textContent?.includes("刷新"));
      btn.click();
    })()`);
    await page.waitFor(`(() => {
      const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("登录设备"));
      if (!card) return false;
      const span = [...card.querySelectorAll("span")].find((s) => s.textContent?.trim() === ${JSON.stringify(ENROLL_DEVICE_NAME)});
      return !!span?.parentElement?.textContent?.includes("已吊销");
    })()`);
    const rows = await page.evaluate(`(() => {
      const card = [...document.querySelectorAll(".nx-card")].find((c) => c.textContent?.includes("登录设备"));
      const rowText = (name) => {
        const span = [...card.querySelectorAll("span")].find((s) => s.textContent?.trim() === name);
        return span?.parentElement?.textContent ?? null;
      };
      return { enrolled: rowText(${JSON.stringify(ENROLL_DEVICE_NAME)}), seeded: rowText("这台浏览器") };
    })()`);
    assert.ok(rows.seeded?.includes("生效中"), `seeded device must stay active in UI: ${JSON.stringify(rows)}`);
    const shot = await screenshot(page, "D-enroll-revoked.png");
    return { evidence: { targetId: revoked.targetId, rows, shot } };
  });

  await pass("D-enroll-invalid-code", async () => {
    await page.evaluate(`(async () => {
      const { useAuth } = await import('/src/features/auth/store.ts');
      await useAuth.getState().logout();
      return true;
    })()`);
    await page.waitFor(`document.body.textContent.includes("用配对码添加设备")`);
    await page.evaluate(`(() => {
      const btn = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("用配对码添加设备"));
      btn.click();
    })()`);
    await page.waitFor(`document.body.textContent.includes("15 分钟内有效")`);
    await page.evaluate(`(() => {
      const gate = document.querySelector(".fixed.inset-0");
      const inputs = gate.querySelectorAll("input");
      const set = ${SET_INPUT};
      set(inputs[0], "demo-enroll-bogus-code");
    })()`);
    await page.evaluate(`(() => {
      const btn = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("添加并继续"));
      btn.click();
    })()`);
    await page.waitFor(`document.body.textContent.includes("设备注册码无效或已过期")`);
    const stillEnrollStep = await page.evaluate(`!document.body.textContent.includes("设备已登记")`);
    assert.equal(stillEnrollStep, true, "invalid code must not advance to the login step");
    return { evidence: { error: "设备注册码无效或已过期" } };
  });

  await pass("D-enroll-form-no-overflow-320", async () => {
    await setViewport(page, 320, 720);
    const sample = await page.evaluate(READ_OVERFLOW);
    assertNoOverflow(sample, "enroll form 320");
    const shot = await screenshot(page, "D-enroll-form-320.png");
    await setViewport(page, 1280, 900);
    return { evidence: { shot } };
  });

  page.close();
}

async function syncClientChecks(chrome, api) {
  const served = await (await fetch(`${VITE}/src/features/settings/SyncCard.tsx`)).text();
  assert.ok(served.includes("WebSyncConsole"), "served SyncCard module must contain WebSyncConsole");
  assert.equal(
    served.includes("if (WEB)"),
    false,
    "acceptance vite plugin did not disable the WebSyncConsole branch in the served SyncCard module",
  );
  const page = await newPage(chrome);
  await page.send("Network.enable");
  await page.send("Network.setCacheDisabled", { cacheDisabled: true });
  const fabrications = {
    sync_link_get: { url: "", username: "", insecure: false, hasPassword: false, verifiedAt: 0, lastError: "" },
    sync_link_set: { url: "https://sync.example.com", username: "alice", insecure: false, hasPassword: true, verifiedAt: 1, lastError: null },
    sync_status: { configured: true, loggedIn: true, username: "alice", seq: 7, verifiedAt: 1, lastError: "" },
    sync_now: { pulled: 2, applied: 1, pullSkipped: 1, decryptFailed: 0, pushed: 3, conflicts: 0, head: "head-1", seq: 7, warnings: [] },
  };

  const { identifier } = await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `
      try { localStorage.clear(); } catch {}
      window.__NEXTERM_TRANSPORT__ = "web";
      try { localStorage.setItem("nexterm.theme.v1", "dark"); } catch {}
      (() => {
        const fabrications = ${JSON.stringify(fabrications)};
        const origFetch = window.fetch.bind(window);
        window.fetch = async (input, init) => {
          try {
            const url = typeof input === "string" ? input : input.url;
            const method = String(init?.method || "GET").toUpperCase();
            if (url.includes("/rpc") && method === "POST") {
              const cmd = JSON.parse(String(init?.body || "{}")).cmd;
              if (cmd && Object.hasOwn(fabrications, cmd)) {
                return new Response(JSON.stringify({ ok: true, data: fabrications[cmd] }), {
                  status: 200,
                  headers: { "content-type": "application/json" },
                });
              }
            }
          } catch {}
          return origFetch(input, init);
        };
      })();
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
    set(document.querySelector("#sync-user"), "alice");
    set(document.querySelector("#sync-pass"), "fresh-password");
  })()`);
  await page.waitFor(`(() => {
    const card = document.querySelector("#sync-url")?.closest(".nx-card");
    return !!card && [...card.querySelectorAll("button")].some((b) => b.textContent?.trim() === "登录" && !b.disabled);
  })()`);
  await page.evaluate(`(() => {
    const card = document.querySelector("#sync-url")?.closest(".nx-card");
    [...card.querySelectorAll("button")].find((b) => b.textContent?.trim() === "登录").click();
  })()`);
  await page.waitFor(`[...document.querySelectorAll("button")].some((b) => b.textContent?.trim() === "立即同步" && !b.disabled)`);
  await sleep(300);

  await pass("C-sync-client-cred-row-320", async () => {
    await setViewport(page, 320, 720);
    const state = await page.evaluate(`(() => {
      const card = document.querySelector("#sync-url")?.closest(".nx-card");
      if (!card) return { found: false };
      const rows = [...card.querySelectorAll("div")].filter((d) => d.querySelector("#sync-url, #sync-user, #sync-pass"));
      const overflowing = rows.filter((d) => d.scrollWidth > d.clientWidth + 1).length;
      return {
        found: true,
        rowCount: rows.length,
        overflowing,
        hasUrl: Boolean(card.querySelector("#sync-url")),
        hasUser: Boolean(card.querySelector("#sync-user")),
        hasPass: Boolean(card.querySelector("#sync-pass")),
      };
    })()`);
    assert.ok(state.found, "sync link card not found");
    assert.equal(state.hasUrl, true, `url input missing: ${JSON.stringify(state)}`);
    assert.equal(state.hasUser, true, `username input missing: ${JSON.stringify(state)}`);
    assert.equal(state.hasPass, true, `password input missing: ${JSON.stringify(state)}`);
    assert.equal(state.overflowing, 0, `link form rows horizontally overflow: ${JSON.stringify(state)}`);
    const sample = await page.evaluate(READ_OVERFLOW);
    assertNoOverflow(sample, "sync client 320");
    await page.evaluate(`(() => {
      const card = document.querySelector("#sync-url")?.closest(".nx-card");
      card?.scrollIntoView({ block: "center" });
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
  await appearanceFontScaleChecks(pageA);
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

  await enrollChecks(chrome);

  await syncClientChecks(chrome, server.api);
} catch (error) {
  harnessErrors.push(String(error?.stack || error));
} finally {
  stop(chrome?.process);
  stop(server?.process);
  await vite?.stop();
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
    transport: "real nexterm-server (Go) for settings cards; demo transport for audit rows and the enroll pairing-code flow",
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
