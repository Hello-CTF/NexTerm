#!/usr/bin/env node
// M116 全局响应式真实 Chromium 验收：320/360/390/短横屏 568x320/768/200% 缩放等效(640x400)
// × 明暗双主题 × 触摸仿真；含 visualViewport 收缩模拟软键盘（resizes-visual 行为），
// 断言 root/App 尺寸稳定、底部区域避让、聚焦控件可达。
//
// 运行：node src/test/global-responsive-acceptance.mjs
// 需要本机 Chrome/Chromium（CHROME_PATH 可覆盖）与 pnpm（启动 vite dev server）。
// 报告与截图写入 target/acceptance-global-responsive/。
// 证据边界：headless 无法真实弹出 OS 软键盘，IME 场景为 visualViewport/VirtualKeyboard
// 合成事件等效模拟（与 M98/M104 相同方法），不声称真机验证。
import { spawn, spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const OUT = path.join(ROOT, "target/acceptance-global-responsive");
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

async function screenshot(page, name) {
  const shot = await page.send("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(path.join(OUT, name), Buffer.from(shot.data, "base64"));
  return name;
}

async function setViewport(page, { width, height, mobile = false, dsf = 2 }) {
  await page.send("Emulation.setDeviceMetricsOverride", {
    width,
    height,
    deviceScaleFactor: dsf,
    mobile,
    screenWidth: width,
    screenHeight: height,
  });
  if (mobile) {
    await page.send("Emulation.setTouchEmulationEnabled", { enabled: true, maxTouchPoints: 5 });
  } else {
    await page.send("Emulation.setTouchEmulationEnabled", { enabled: false });
  }
}

async function boot(page, { theme, viewport, vvPatch = false } = {}) {
  const { identifier: seedId } = await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source:
      theme === undefined
        ? `try { localStorage.removeItem("nexterm.theme.v1"); } catch {}`
        : `try { localStorage.setItem("nexterm.theme.v1", ${JSON.stringify(theme)}); } catch {}`,
  });
  const unseed = () => page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier: seedId });
  let unpatch = null;
  if (vvPatch) {
    const { identifier } = await page.send("Page.addScriptToEvaluateOnNewDocument", {
      source: `(() => {
        const fake = new EventTarget();
        Object.defineProperties(fake, {
          height: { get: () => window.__vvHeight ?? window.innerHeight },
          width: { get: () => window.innerWidth },
          offsetTop: { get: () => window.__vvOffsetTop ?? 0 },
          offsetLeft: { get: () => 0 },
          pageTop: { get: () => window.__vvOffsetTop ?? 0 },
          pageLeft: { get: () => 0 },
          scale: { get: () => 1 },
        });
        Object.defineProperty(window, "visualViewport", { configurable: true, get: () => fake });
      })()`,
    });
    unpatch = () => page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier });
  }
  try {
    if (viewport) await setViewport(page, viewport);
    await page.navigate(`${VITE}/?demo=1`);
    await page.waitFor(
      "Boolean(document.querySelector('[role=\"tablist\"][aria-label=\"工作区\"] [role=\"tab\"]') && document.querySelector('.xterm'))",
    );
  } finally {
    await unseed?.();
    await unpatch?.();
  }
}

async function simulateKeyboard(page, height, offsetTop = 0) {
  await page.evaluate(`(() => {
    window.__vvHeight = ${height};
    window.__vvOffsetTop = ${offsetTop};
    window.visualViewport.dispatchEvent(new Event("resize"));
  })()`);
  await sleep(250);
}

async function restoreKeyboard(page) {
  await page.evaluate(`(() => {
    window.__vvHeight = window.innerHeight;
    window.__vvOffsetTop = 0;
    window.visualViewport.dispatchEvent(new Event("resize"));
  })()`);
  await sleep(250);
}

async function vkAvailable(page) {
  return page.evaluate(`("virtualKeyboard" in navigator)`);
}

async function showKeyboard(page, height) {
  if (await vkAvailable(page)) {
    await page.evaluate(`(() => {
      const vk = navigator.virtualKeyboard;
      Object.defineProperty(vk, "boundingRect", {
        configurable: true,
        value: { top: ${height}, height: window.innerHeight - ${height}, width: window.innerWidth, left: 0, right: window.innerWidth, bottom: window.innerHeight, x: 0, y: ${height} },
      });
      vk.dispatchEvent(new Event("geometrychanged"));
    })()`);
  } else {
    await simulateKeyboard(page, height);
  }
  await sleep(250);
}

async function hideKeyboard(page) {
  if (await vkAvailable(page)) {
    await page.evaluate(`(() => {
      const vk = navigator.virtualKeyboard;
      Object.defineProperty(vk, "boundingRect", {
        configurable: true,
        value: { top: window.innerHeight, height: 0, width: window.innerWidth, left: 0, right: window.innerWidth, bottom: window.innerHeight, x: 0, y: window.innerHeight },
      });
      vk.dispatchEvent(new Event("geometrychanged"));
    })()`);
  } else {
    await restoreKeyboard(page);
  }
  await sleep(250);
}

const matrix = [
  { name: "320x568", width: 320, height: 568, mobile: true },
  { name: "360x640", width: 360, height: 640, mobile: true },
  { name: "390x844", width: 390, height: 844, mobile: true },
  { name: "568x320-landscape", width: 568, height: 320, mobile: false },
  { name: "768x1024", width: 768, height: 1024, mobile: false },
  { name: "640x400-zoom200-equiv", width: 640, height: 400, mobile: false },
];

async function responsiveAcceptance(page) {
  for (const theme of ["dark", "light"]) {
    for (const vp of matrix) {
      await pass(`matrix-no-hoverflow-${vp.name}-${theme}`, async () => {
        await boot(page, { theme, viewport: vp });
        const evidence = await page.evaluate(`(() => ({
          innerWidth: window.innerWidth,
          docScrollWidth: document.scrollingElement.scrollWidth,
          bodyScrollWidth: document.body.scrollWidth,
          theme: document.documentElement.dataset.nxTheme ?? null,
        }))()`);
        assert.equal(evidence.theme, theme);
        assert.ok(
          evidence.docScrollWidth <= evidence.innerWidth + 1 && evidence.bodyScrollWidth <= evidence.innerWidth + 1,
          `page-level horizontal overflow: ${JSON.stringify(evidence)}`,
        );
        if (vp.name === "320x568" || vp.name === "568x320-landscape") {
          await screenshot(page, `home-${vp.name}-${theme}.png`);
        }
        return { evidence };
      });
    }
  }

  await pass("rail-scrolls-at-short-height", async () => {
    await boot(page, { viewport: matrix.find((v) => v.name === "568x320-landscape") });
    const evidence = await page.evaluate(`(() => {
      const rail = document.querySelector(".nx-rail");
      const buttons = [...rail.querySelectorAll(".nx-rail-btn")];
      const before = buttons.map((b) => Math.round(b.getBoundingClientRect().height));
      rail.scrollTop = rail.scrollHeight;
      const last = rail.querySelector(".nx-rail > *:last-child");
      const railRect = rail.getBoundingClientRect();
      const lastRect = last.getBoundingClientRect();
      return {
        minButtonHeight: Math.min(...before),
        railScrollable: rail.scrollHeight > rail.clientHeight + 1,
        lastReachable: lastRect.bottom <= railRect.bottom + 1 && lastRect.top >= railRect.top - 1,
        buttonCount: buttons.length,
      };
    })()`);
    assert.ok(evidence.buttonCount >= 12, JSON.stringify(evidence));
    assert.ok(evidence.minButtonHeight >= 24, `rail buttons compressed: ${JSON.stringify(evidence)}`);
    assert.equal(evidence.railScrollable, true, `rail must scroll instead of compressing: ${JSON.stringify(evidence)}`);
    assert.equal(evidence.lastReachable, true, `rail bottom items unreachable: ${JSON.stringify(evidence)}`);
    await screenshot(page, "rail-568x320-scrolled.png");
    return { evidence };
  });

  for (const name of ["320x568", "390x844"]) {
    await pass(`tabstrip-actions-reachable-${name}`, async () => {
      const vp = matrix.find((v) => v.name === name);
      await boot(page, { viewport: vp });
      const evidence = await page.evaluate(`(() => {
        const strip = document.querySelector(".nx-tabstrip.is-sub");
        const scroll = strip.querySelector(".nx-tabstrip-scroll");
        const actions = [...strip.querySelectorAll(":scope > button")];
        return {
          innerWidth: window.innerWidth,
          actionRights: actions.map((b) => Math.round(b.getBoundingClientRect().right)),
          splitDisabled: strip.querySelector('button[aria-label="上下分屏"]')?.disabled ?? null,
          scrollWithin: scroll.getBoundingClientRect().right <= strip.getBoundingClientRect().right + 1,
        };
      })()`);
      assert.ok(evidence.actionRights.length >= 3, JSON.stringify(evidence));
      for (const right of evidence.actionRights) {
        assert.ok(right <= evidence.innerWidth, `tabstrip action off-screen: ${JSON.stringify(evidence)}`);
      }
      assert.equal(evidence.scrollWithin, true);
      return { evidence };
    });
  }

  await pass("toast-above-terminal-keys", async () => {
    await boot(page, { viewport: matrix.find((v) => v.name === "390x844") });
    await page.waitFor("Boolean(document.querySelector('.nx-toasts button'))");
    const evidence = await page.evaluate(`(() => {
      const toast = document.querySelector(".nx-toasts button");
      const keys = document.querySelector(".nx-terminal-keys");
      const ctrlKey = [...document.querySelectorAll(".nx-terminal-key")].find((k) => k.textContent.trim() === "Ctrl");
      const t = toast.getBoundingClientRect();
      const k = keys.getBoundingClientRect();
      const c = ctrlKey.getBoundingClientRect();
      const hit = document.elementFromPoint(c.left + c.width / 2, c.top + c.height / 2);
      return {
        toastBottom: Math.round(t.bottom),
        keysTop: Math.round(k.top),
        keysVisible: k.height > 0 && k.bottom <= window.innerHeight + 1,
        hitIsKey: hit === ctrlKey || ctrlKey.contains(hit),
        appFlag: document.querySelector(".nx-app")?.dataset.nxKeys ?? null,
      };
    })()`);
    assert.equal(evidence.appFlag, "true", JSON.stringify(evidence));
    assert.equal(evidence.keysVisible, true, JSON.stringify(evidence));
    assert.ok(evidence.toastBottom <= evidence.keysTop, `toast covers keys: ${JSON.stringify(evidence)}`);
    assert.equal(evidence.hitIsKey, true, `keys unclickable under toast: ${JSON.stringify(evidence)}`);
    await screenshot(page, "toast-above-keys-390.png");
    return { evidence };
  });

  await pass("toast-above-statusbar-landscape", async () => {
    await boot(page, { viewport: matrix.find((v) => v.name === "568x320-landscape") });
    await page.waitFor("Boolean(document.querySelector('.nx-toasts button'))");
    const evidence = await page.evaluate(`(() => {
      const toast = document.querySelector(".nx-toasts button");
      const status = document.querySelector(".nx-statusbar");
      const keys = document.querySelector(".nx-terminal-keys");
      const t = toast.getBoundingClientRect();
      const s = status.getBoundingClientRect();
      return {
        toastBottom: Math.round(t.bottom),
        statusTop: Math.round(s.top),
        keysShown: keys ? keys.getBoundingClientRect().height > 0 : false,
      };
    })()`);
    assert.equal(evidence.keysShown, false, "keys bar should be hidden at 568px fine pointer");
    assert.ok(evidence.toastBottom <= evidence.statusTop, `toast covers statusbar: ${JSON.stringify(evidence)}`);
    return { evidence };
  });

  await pass("toast-below-overlay", async () => {
    await boot(page, { viewport: matrix.find((v) => v.name === "390x844") });
    await page.waitFor("Boolean(document.querySelector('.nx-toasts button'))");
    await page.evaluate(`document.querySelector('button[aria-label^="命令面板"]').click()`);
    await page.waitFor("Boolean(document.querySelector('.nx-overlay .nx-command-modal'))");
    const evidence = await page.evaluate(`(() => {
      const toast = document.querySelector(".nx-toasts button");
      const overlay = document.querySelector(".nx-overlay");
      const t = toast.getBoundingClientRect();
      const hit = document.elementFromPoint(t.left + t.width / 2, t.top + t.height / 2);
      return {
        toastZ: Number(getComputedStyle(document.querySelector(".nx-toasts")).zIndex),
        overlayZ: Number(getComputedStyle(overlay).zIndex),
        hitInsideOverlay: overlay.contains(hit),
        hitIsToast: hit === toast || toast.contains(hit),
      };
    })()`);
    assert.ok(evidence.toastZ < evidence.overlayZ, `toast must layer below overlays: ${JSON.stringify(evidence)}`);
    assert.equal(evidence.hitIsToast, false, `toast clickable above modal: ${JSON.stringify(evidence)}`);
    assert.equal(evidence.hitInsideOverlay, true, JSON.stringify(evidence));
    return { evidence };
  });

  for (const name of ["568x320-landscape", "640x400-zoom200-equiv", "768x1024"]) {
    await pass(`statusbar-single-line-${name}`, async () => {
      await boot(page, { viewport: matrix.find((v) => v.name === name) });
      const evidence = await page.evaluate(`(() => {
        const bar = document.querySelector(".nx-statusbar");
        const clipped = bar.scrollHeight > bar.clientHeight + 1;
        bar.scrollLeft = bar.scrollWidth;
        const last = bar.querySelector("button.nx-link:last-of-type") ?? bar.lastElementChild;
        const barRect = bar.getBoundingClientRect();
        const lastRect = last.getBoundingClientRect();
        return {
          clipped,
          barHeight: Math.round(barRect.height),
          lastReachable: lastRect.right <= barRect.right + 1,
          scrollable: bar.scrollWidth > bar.clientWidth + 1,
        };
      })()`);
      assert.equal(evidence.clipped, false, `statusbar wraps/clips: ${JSON.stringify(evidence)}`);
      assert.equal(evidence.lastReachable, true, `statusbar tail unreachable: ${JSON.stringify(evidence)}`);
      return { evidence };
    });
  }

  await pass("palette-short-landscape", async () => {
    await boot(page, { viewport: matrix.find((v) => v.name === "568x320-landscape") });
    await page.evaluate(`document.querySelector('button[aria-label^="命令面板"]').click()`);
    await page.waitFor("Boolean(document.querySelector('.nx-command-modal'))");
    const evidence = await page.evaluate(`(() => {
      const modal = document.querySelector(".nx-command-modal");
      const input = modal.querySelector(".nx-command-input");
      const help = modal.querySelector(".nx-command-help");
      const list = modal.querySelector('[role="listbox"]');
      const m = modal.getBoundingClientRect();
      const i = input.getBoundingClientRect();
      const h = help.getBoundingClientRect();
      return {
        innerHeight: window.innerHeight,
        modalTop: Math.round(m.top),
        modalBottom: Math.round(m.bottom),
        inputTop: Math.round(i.top),
        helpBottom: Math.round(h.bottom),
        listScrollable: list.scrollHeight >= list.clientHeight,
        modalDisplay: getComputedStyle(modal).display,
      };
    })()`);
    assert.equal(evidence.modalDisplay, "flex", JSON.stringify(evidence));
    assert.ok(evidence.inputTop >= 0, `palette input off-screen: ${JSON.stringify(evidence)}`);
    assert.ok(
      evidence.helpBottom <= evidence.innerHeight,
      `palette help footer off-screen: ${JSON.stringify(evidence)}`,
    );
    await screenshot(page, "palette-568x320.png");
    return { evidence };
  });

  await pass("split-guard-at-insufficient-height", async () => {
    await boot(page, { viewport: matrix.find((v) => v.name === "568x320-landscape") });
    const short = await page.evaluate(`(() => {
      const btn = document.querySelector('.nx-tabstrip.is-sub button[aria-label="上下分屏"]');
      return { disabled: btn?.disabled ?? null, title: btn?.getAttribute("title") ?? null };
    })()`);
    assert.equal(short.disabled, true, `split must be blocked at 320px height: ${JSON.stringify(short)}`);
    await boot(page, { viewport: matrix.find((v) => v.name === "390x844") });
    const tall = await page.evaluate(`(() => {
      const btn = document.querySelector('.nx-tabstrip.is-sub button[aria-label="上下分屏"]');
      return { disabled: btn?.disabled ?? null };
    })()`);
    assert.equal(tall.disabled, false, `split must stay available at 844px height: ${JSON.stringify(tall)}`);
    return { evidence: { short, tall } };
  });

  await pass("ime-inset-root-stable", async () => {
    const vp = matrix.find((v) => v.name === "390x844");
    await boot(page, { viewport: vp, vvPatch: true });
    const vk = await vkAvailable(page);
    const before = await page.evaluate(`(() => ({
      rootHeight: document.querySelector("#root").getBoundingClientRect().height,
      appHeight: document.querySelector(".nx-app").getBoundingClientRect().height,
      innerHeight: window.innerHeight,
      inset: getComputedStyle(document.documentElement).getPropertyValue("--nx-kb-inset").trim(),
    }))()`);
    assert.equal(before.inset, "0px", JSON.stringify(before));
    await showKeyboard(page, 500);
    await page.waitFor(`getComputedStyle(document.documentElement).getPropertyValue("--nx-kb-inset").trim() === "344px"`);
    const after = await page.evaluate(`(() => {
      const status = document.querySelector(".nx-statusbar");
      const keys = document.querySelector(".nx-terminal-keys");
      const statusText = status.querySelector("span");
      const s = statusText.getBoundingClientRect();
      const k = keys.getBoundingClientRect();
      return {
        rootHeight: document.querySelector("#root").getBoundingClientRect().height,
        appHeight: document.querySelector(".nx-app").getBoundingClientRect().height,
        innerHeight: window.innerHeight,
        inset: getComputedStyle(document.documentElement).getPropertyValue("--nx-kb-inset").trim(),
        statusTextBottom: Math.round(s.bottom),
        keysBottom: Math.round(k.bottom),
        visibleBottom: 500,
      };
    })()`);
    assert.equal(after.rootHeight, before.rootHeight, `root height changed under keyboard: ${JSON.stringify({ before, after })}`);
    assert.equal(after.appHeight, before.appHeight, `app height changed under keyboard: ${JSON.stringify({ before, after })}`);
    assert.equal(after.innerHeight, before.innerHeight, `layout viewport resized (resizes-content?): ${JSON.stringify({ before, after })}`);
    assert.equal(after.inset, "344px");
    assert.ok(after.statusTextBottom <= after.visibleBottom, `statusbar covered by keyboard: ${JSON.stringify(after)}`);
    assert.ok(after.keysBottom <= after.visibleBottom, `terminal keys covered by keyboard: ${JSON.stringify(after)}`);
    await screenshot(page, "ime-simulated-390.png");
    await hideKeyboard(page);
    await page.waitFor(`getComputedStyle(document.documentElement).getPropertyValue("--nx-kb-inset").trim() === "0px"`);
    return { evidence: { vkAvailable: vk, before, after } };
  });

  await pass("ime-focused-control-reachable", async () => {
    const vp = matrix.find((v) => v.name === "390x844");
    await boot(page, { viewport: vp, vvPatch: true });
    await page.evaluate(`document.querySelector('button[aria-label^="命令面板"]').click()`);
    await page.waitFor("Boolean(document.querySelector('.nx-command-modal'))");
    await showKeyboard(page, 500);
    const evidence = await page.evaluate(`(() => {
      const modal = document.querySelector(".nx-command-modal");
      const input = document.querySelector(".nx-command-input");
      const m = modal.getBoundingClientRect();
      const i = input.getBoundingClientRect();
      return {
        inputBottom: Math.round(i.bottom),
        modalBottom: Math.round(m.bottom),
        visibleBottom: 500,
        focused: document.activeElement === input,
      };
    })()`);
    assert.equal(evidence.focused, true, JSON.stringify(evidence));
    assert.ok(evidence.inputBottom <= evidence.visibleBottom, `focused input covered: ${JSON.stringify(evidence)}`);
    assert.ok(evidence.modalBottom <= evidence.visibleBottom, `modal under keyboard: ${JSON.stringify(evidence)}`);
    await hideKeyboard(page);
    return { evidence };
  });

  await pass("coarse-targets", async () => {
    await boot(page, { viewport: matrix.find((v) => v.name === "390x844") });
    const evidence = await page.evaluate(`(() => {
      const rect = (el) => (el ? el.getBoundingClientRect() : null);
      const railBtn = rect(document.querySelector(".nx-rail-btn"));
      const key = rect(document.querySelector(".nx-terminal-key"));
      const splitBtn = rect(document.querySelector('.nx-tabstrip.is-sub button[aria-label="上下分屏"]'));
      const close = rect(document.querySelector(".nx-tab.is-active .nx-tab-close"));
      return {
        coarse: window.matchMedia("(pointer: coarse)").matches,
        rail: railBtn && { w: Math.round(railBtn.width), h: Math.round(railBtn.height) },
        key: key && { w: Math.round(key.width), h: Math.round(key.height) },
        splitBtn: splitBtn && { w: Math.round(splitBtn.width), h: Math.round(splitBtn.height) },
        tabClose: close && { w: Math.round(close.width), h: Math.round(close.height) },
      };
    })()`);
    assert.equal(evidence.coarse, true, "touch emulation must yield coarse pointer");
    for (const [name, r] of Object.entries({ rail: evidence.rail, key: evidence.key, splitBtn: evidence.splitBtn, tabClose: evidence.tabClose })) {
      assert.ok(r, `${name} missing`);
      assert.ok(r.w >= 24 && r.h >= 24, `${name} below 24px: ${JSON.stringify(evidence)}`);
    }
    return { evidence };
  });

  await pass("dialog-footer-reachable-320", async () => {
    await boot(page, { viewport: matrix.find((v) => v.name === "320x568") });
    await page.evaluate(`(() => {
      const tab = document.querySelector('.nx-tabstrip.is-sub [role="tab"]');
      const rect = tab.getBoundingClientRect();
      tab.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: rect.left + 8, clientY: rect.bottom - 4 }));
    })()`);
    await page.waitFor("Boolean(document.querySelector('.nx-menu'))");
    await page.evaluate(`(() => {
      const item = [...document.querySelectorAll(".nx-menu-item")].find((b) => b.textContent.includes("重命名"));
      item.click();
    })()`);
    await page.waitFor("Boolean(document.querySelector('.nx-overlay .nx-modal'))");
    const evidence = await page.evaluate(`(() => {
      const modal = document.querySelector(".nx-overlay .nx-modal");
      const footer = modal.querySelector(".nx-modal-footer");
      const body = modal.querySelector(".nx-modal-body");
      const f = footer.getBoundingClientRect();
      const m = modal.getBoundingClientRect();
      return {
        innerHeight: window.innerHeight,
        footerBottom: Math.round(f.bottom),
        modalBottom: Math.round(m.bottom),
        bodyOverflowY: getComputedStyle(body).overflowY,
        modalDisplay: getComputedStyle(modal).display,
        inputFocused: document.activeElement === modal.querySelector("input, textarea"),
      };
    })()`);
    assert.equal(evidence.modalDisplay, "flex", JSON.stringify(evidence));
    assert.equal(evidence.bodyOverflowY, "auto", JSON.stringify(evidence));
    assert.equal(evidence.inputFocused, true, JSON.stringify(evidence));
    assert.ok(evidence.footerBottom <= evidence.innerHeight, `dialog footer off-screen: ${JSON.stringify(evidence)}`);
    await page.evaluate(`(() => {
      const btns = [...document.querySelectorAll(".nx-overlay .nx-modal-footer button")];
      btns.find((b) => b.textContent.includes("取消"))?.click();
    })()`);
    return { evidence };
  });
}

async function main() {
  const vite = await startVite();
  const chrome = await startChrome();
  const page = await newPage(chrome);
  try {
    await responsiveAcceptance(page);
  } catch (error) {
    harnessErrors.push(String(error?.stack || error));
  } finally {
    page.close();
    stop(chrome.process);
    stop(vite);
    fs.rmSync(chrome.profile, { recursive: true, force: true });
  }

  const summary = {
    generatedAt: new Date().toISOString(),
    chrome: chrome.version["User-Agent"] ?? null,
    note: "headless 模拟 IME 为 visualViewport/VirtualKeyboard 合成事件等效模拟，非真机软键盘",
    results: Object.fromEntries(results),
    harnessErrors,
  };
  fs.writeFileSync(path.join(OUT, "report.json"), JSON.stringify(summary, null, 2));
  const failed = [...results.values()].filter((r) => r.status !== "passed");
  console.warn(`\n${results.size - failed.length}/${results.size} passed; report at ${path.join(OUT, "report.json")}`);
  if (failed.length > 0 || harnessErrors.length > 0) {
    for (const f of failed) console.warn(`FAILED ${f.id}: ${f.error?.split("\n")[0]}`);
    for (const e of harnessErrors) console.warn(`HARNESS ${e.split("\n")[0]}`);
    process.exit(1);
  }
}

await main();
