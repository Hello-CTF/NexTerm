#!/usr/bin/env node
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

async function clickAt(page, x, y) {
  await page.send("Input.dispatchMouseEvent", { type: "mousePressed", x, y, button: "left", clickCount: 1 });
  await page.send("Input.dispatchMouseEvent", { type: "mouseReleased", x, y, button: "left", clickCount: 1 });
  await sleep(80);
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

async function boot(page, { theme, viewport, vvPatch = false, extraInit = null } = {}) {
  const { identifier: seedId } = await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source:
      theme === undefined
        ? `try { localStorage.removeItem("nexterm.theme.v1"); } catch {}`
        : `try { localStorage.setItem("nexterm.theme.v1", ${JSON.stringify(theme)}); } catch {}`,
  });
  const unseed = () => page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier: seedId });
  let unpatch = null;
  if (vvPatch || extraInit) {
    const { identifier } = await page.send("Page.addScriptToEvaluateOnNewDocument", {
      source: `${vvPatch ? `(() => {
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
      })();` : ""}${extraInit ?? ""}`,
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
    const xtermGeometry = `(() => {
      const screen = document.querySelector(".xterm-screen");
      const viewport = document.querySelector(".xterm-viewport");
      if (!screen || !viewport) return null;
      const s = screen.getBoundingClientRect();
      const v = viewport.getBoundingClientRect();
      return { w: Math.round(s.width), h: Math.round(s.height), vw: Math.round(v.width), vh: Math.round(v.height) };
    })()`;
    const before = await page.evaluate(`(() => ({
      rootHeight: document.querySelector("#root").getBoundingClientRect().height,
      appHeight: document.querySelector(".nx-app").getBoundingClientRect().height,
      innerHeight: window.innerHeight,
      inset: getComputedStyle(document.documentElement).getPropertyValue("--nx-kb-inset").trim(),
      xterm: ${xtermGeometry},
    }))()`);
    assert.equal(before.inset, "0px", JSON.stringify(before));
    assert.ok(before.xterm, "xterm must be rendered");
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
        xterm: ${xtermGeometry},
      };
    })()`);
    assert.equal(after.rootHeight, before.rootHeight, `root height changed under keyboard: ${JSON.stringify({ before, after })}`);
    assert.equal(after.appHeight, before.appHeight, `app height changed under keyboard: ${JSON.stringify({ before, after })}`);
    assert.equal(after.innerHeight, before.innerHeight, `layout viewport resized (resizes-content?): ${JSON.stringify({ before, after })}`);
    assert.equal(after.inset, "344px");
    assert.deepEqual(after.xterm, before.xterm, `xterm geometry changed under keyboard: ${JSON.stringify({ before, after })}`);
    assert.ok(after.statusTextBottom <= after.visibleBottom, `statusbar covered by keyboard: ${JSON.stringify(after)}`);
    assert.ok(after.keysBottom <= after.visibleBottom, `terminal keys covered by keyboard: ${JSON.stringify(after)}`);
    await screenshot(page, "ime-simulated-390.png");
    await hideKeyboard(page);
    await page.waitFor(`getComputedStyle(document.documentElement).getPropertyValue("--nx-kb-inset").trim() === "0px"`);
    const restored = await page.evaluate(xtermGeometry);
    assert.deepEqual(restored, before.xterm, `xterm geometry changed after keyboard hide: ${JSON.stringify({ before, restored })}`);
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

  await pass("ime-vk-setter-throws", async () => {
    const vp = matrix.find((v) => v.name === "390x844");
    const fault = `(() => {
      const fake = new EventTarget();
      Object.defineProperty(fake, "overlaysContent", {
        configurable: true,
        get: () => false,
        set: () => { throw new DOMException("synthetic fault", "InvalidStateError"); },
      });
      Object.defineProperty(fake, "boundingRect", { configurable: true, get: () => ({ top: window.innerHeight, height: 0 }) });
      Object.defineProperty(navigator, "virtualKeyboard", { configurable: true, get: () => fake });
    })();`;
    await boot(page, { viewport: vp, vvPatch: true, extraInit: fault });
    await page.waitFor("Boolean(document.querySelector('.nx-app'))");
    await simulateKeyboard(page, 500);
    const evidence = await page.evaluate(`(() => ({
      appAlive: Boolean(document.querySelector(".nx-app .nx-statusbar")),
      rootChildren: document.querySelector("#root").childElementCount,
      inset: getComputedStyle(document.documentElement).getPropertyValue("--nx-kb-inset").trim(),
    }))()`);
    assert.equal(evidence.appAlive, true, `app must survive a throwing VK setter: ${JSON.stringify(evidence)}`);
    assert.ok(evidence.rootChildren > 0, `root must not be emptied: ${JSON.stringify(evidence)}`);
    assert.equal(evidence.inset, "344px", `vv baseline must take over after setter fault: ${JSON.stringify(evidence)}`);
    await restoreKeyboard(page);
    return { evidence };
  });

  await pass("ime-vk-geometry-throws", async () => {
    const vp = matrix.find((v) => v.name === "390x844");
    const fault = `(() => {
      const fake = new EventTarget();
      let content = false;
      Object.defineProperty(fake, "overlaysContent", { configurable: true, get: () => content, set: (v) => { content = v; } });
      Object.defineProperty(fake, "boundingRect", { configurable: true, get: () => { throw new DOMException("synthetic fault", "InvalidStateError"); } });
      Object.defineProperty(navigator, "virtualKeyboard", { configurable: true, get: () => fake });
    })();`;
    await boot(page, { viewport: vp, vvPatch: true, extraInit: fault });
    await page.waitFor("Boolean(document.querySelector('.nx-app'))");
    await simulateKeyboard(page, 500);
    const evidence = await page.evaluate(`(() => ({
      appAlive: Boolean(document.querySelector(".nx-app .nx-statusbar")),
      rootChildren: document.querySelector("#root").childElementCount,
      inset: getComputedStyle(document.documentElement).getPropertyValue("--nx-kb-inset").trim(),
    }))()`);
    assert.equal(evidence.appAlive, true, `app must survive a throwing VK geometry read: ${JSON.stringify(evidence)}`);
    assert.ok(evidence.rootChildren > 0, `root must not be emptied: ${JSON.stringify(evidence)}`);
    assert.equal(evidence.inset, "344px", `vv baseline must take over after geometry fault: ${JSON.stringify(evidence)}`);
    await restoreKeyboard(page);
    return { evidence };
  });

  await pass("file-split-guard-568x320", async () => {
    await boot(page, { viewport: matrix.find((v) => v.name === "568x320-landscape") });
    await page.evaluate(`document.querySelector('.nx-rail button[aria-label="文件树"]').click()`);
    await page.waitFor("Boolean(document.querySelector('.nx-left-dock [role=\"treeitem\"]'))");
    await page.evaluate(`(() => {
      const row = [...document.querySelectorAll('.nx-left-dock [role="treeitem"]')].find((el) => el.textContent.includes(".bashrc"));
      const rect = row.getBoundingClientRect();
      row.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: rect.left + 10, clientY: rect.top + rect.height / 2 }));
    })()`);
    await page.waitFor("Boolean(document.querySelector('.nx-menu'))");
    await page.evaluate(`(() => {
      const item = [...document.querySelectorAll(".nx-menu-item")].find((b) => b.textContent.includes("在下方编辑"));
      item.click();
    })()`);
    await page.waitFor("Boolean([...document.querySelectorAll('.nx-tab')].some((t) => t.textContent.includes('.bashrc')))");
    const evidence = await page.evaluate(`(() => ({
      paneCount: document.querySelectorAll(".nx-tabstrip.is-sub").length,
      splitDisabled: document.querySelector('.nx-tabstrip.is-sub button[aria-label="上下分屏"]')?.disabled ?? null,
      fileTabActive: document.querySelector(".nx-tab.is-active")?.textContent.includes(".bashrc") ?? false,
      toasts: [...document.querySelectorAll(".nx-toasts button")].map((b) => b.textContent),
    }))()`);
    assert.equal(evidence.paneCount, 1, `lower-pane creation must be blocked at 320px height: ${JSON.stringify(evidence)}`);
    assert.equal(evidence.splitDisabled, true, JSON.stringify(evidence));
    assert.equal(evidence.fileTabActive, true, `file must open in the current pane: ${JSON.stringify(evidence)}`);
    assert.ok(
      evidence.toasts.some((t) => t?.includes("已在当前栏打开")),
      `fallback must be explained: ${JSON.stringify(evidence)}`,
    );
    await screenshot(page, "file-split-guard-568x320.png");
    return { evidence };
  });

  await pass("toast-mixed-split-terminal-390", async () => {
    const vp = matrix.find((v) => v.name === "390x844");
    await boot(page, { viewport: vp });
    await page.evaluate(`document.querySelector('.nx-tabstrip.is-sub button[aria-label="上下分屏"]').click()`);
    await page.waitFor("document.querySelectorAll('.nx-tabstrip.is-sub').length === 2");
    await page.evaluate(`(() => {
      const upperTab = document.querySelectorAll('.nx-tabstrip.is-sub [role="tab"]')[0];
      upperTab.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true }));
      upperTab.click();
    })()`);
    await page.evaluate(`document.querySelector('.nx-rail button[aria-label="设置"]').click()`);
    await page.waitFor("Boolean([...document.querySelectorAll('.nx-tab.is-active')].some((t) => t.textContent.includes('设置')))");
    const layout = await page.evaluate(`(() => {
      const settingsActive = [...document.querySelectorAll('.nx-tabstrip.is-sub')].map((strip) =>
        strip.querySelector('.nx-tab.is-active')?.textContent ?? "",
      );
      return { settingsActive };
    })()`);
    assert.ok(layout.settingsActive.some((t) => t.includes("设置")), JSON.stringify(layout));
    await page.evaluate(`document.querySelector(".nx-tabstrip.is-top .nx-tab-new").click()`);
    await page.waitFor("Boolean(document.querySelector('.nx-toasts button'))");
    await page.evaluate(`document.querySelector(".nx-dock-backdrop").click()`);
    const evidence = await page.evaluate(`(() => {
      const toast = document.querySelector(".nx-toasts button");
      const keysList = [...document.querySelectorAll(".nx-terminal-keys")].filter((k) => k.getBoundingClientRect().height > 0);
      const lower = keysList.sort((a, b) => b.getBoundingClientRect().top - a.getBoundingClientRect().top)[0];
      const ctrlKey = [...lower.querySelectorAll(".nx-terminal-key")].find((k) => k.textContent.trim() === "Ctrl");
      const t = toast.getBoundingClientRect();
      const k = lower.getBoundingClientRect();
      const c = ctrlKey.getBoundingClientRect();
      const hit = document.elementFromPoint(c.left + c.width / 2, c.top + c.height / 2);
      return {
        appFlag: document.querySelector(".nx-app")?.dataset.nxKeys ?? null,
        toastBottom: Math.round(t.bottom),
        keysTop: Math.round(k.top),
        hitIsKey: hit === ctrlKey || ctrlKey.contains(hit),
      };
    })()`);
    assert.equal(evidence.appFlag, "true", `keys avoidance must consider non-active terminal pane: ${JSON.stringify(evidence)}`);
    assert.ok(evidence.toastBottom <= evidence.keysTop, `toast covers lower pane keys: ${JSON.stringify(evidence)}`);
    assert.equal(evidence.hitIsKey, true, `lower pane keys unclickable under toast: ${JSON.stringify(evidence)}`);
    await screenshot(page, "toast-mixed-split-390.png");
    return { evidence };
  });

  await pass("ime-keys-clickable-390", async () => {
    const vp = matrix.find((v) => v.name === "390x844");
    await boot(page, { viewport: vp, vvPatch: true });
    await showKeyboard(page, 500);
    const before = await page.evaluate(`(() => {
      const ctrlKey = [...document.querySelectorAll(".nx-terminal-key")].find((k) => k.textContent.trim() === "Ctrl");
      const bar = document.querySelector(".nx-terminal-keys");
      const statusBar = document.querySelector(".nx-statusbar");
      statusBar.scrollLeft = statusBar.scrollWidth;
      const link = [...document.querySelectorAll(".nx-statusbar .nx-link")].find((b) => b.textContent.includes("审计"));
      const b = bar.getBoundingClientRect();
      const c = ctrlKey.getBoundingClientRect();
      const l = link.getBoundingClientRect();
      const stack = document.elementsFromPoint(c.left + c.width / 2, c.top + c.height / 2);
      const linkStack = document.elementsFromPoint(l.left + l.width / 2, l.top + l.height / 2);
      return {
        ctrl: { x: c.left + c.width / 2, y: c.top + c.height / 2 },
        link: { x: l.left + l.width / 2, y: l.top + l.height / 2 },
        barTop: Math.round(b.top),
        barBottom: Math.round(b.bottom),
        topHitIsKey: stack[0] === ctrlKey || ctrlKey.contains(stack[0]),
        linkTopHit: linkStack[0] === link || link.contains(linkStack[0]),
        stackHead: stack.slice(0, 3).map((el) => String(el.className?.baseVal ?? el.className ?? el.tagName)),
      };
    })()`);
    assert.ok(before.barTop >= 0 && before.barBottom <= 500, `keys bar outside visible region: ${JSON.stringify(before)}`);
    assert.equal(before.topHitIsKey, true, `xterm hit layer above keys: ${JSON.stringify(before)}`);
    assert.equal(before.linkTopHit, true, `statusbar link covered: ${JSON.stringify(before)}`);
    await clickAt(page, before.ctrl.x, before.ctrl.y);
    const pressed = await page.evaluate(`[...document.querySelectorAll(".nx-terminal-key")].find((k) => k.textContent.trim() === "Ctrl")?.getAttribute("aria-pressed")`);
    assert.equal(pressed, "true", "CDP click on Ctrl must toggle aria-pressed");
    await clickAt(page, before.ctrl.x, before.ctrl.y);
    await clickAt(page, before.link.x, before.link.y);
    await page.waitFor("Boolean([...document.querySelectorAll('.nx-tab')].some((t) => t.textContent.includes('审计日志')))");
    await screenshot(page, "ime-keys-clickable-390.png");
    await restoreKeyboard(page);
    return { evidence: { before, pressed } };
  });

  await pass("ime-short-landscape-keys-568x320", async () => {
    const vp = { name: "568x320-touch", width: 568, height: 320, mobile: true };
    for (const inset of [200, 240]) {
      await boot(page, { viewport: vp, vvPatch: true });
      await showKeyboard(page, 320 - inset);
      const evidence = await page.evaluate(`(() => {
        const ctrlKey = [...document.querySelectorAll(".nx-terminal-key")].find((k) => k.textContent.trim() === "Ctrl");
        const bar = document.querySelector(".nx-terminal-keys");
        const statusBar = document.querySelector(".nx-statusbar");
        statusBar.scrollLeft = statusBar.scrollWidth;
        const link = [...document.querySelectorAll(".nx-statusbar .nx-link")].find((b) => b.textContent.includes("审计"));
        const b = bar.getBoundingClientRect();
        const c = ctrlKey.getBoundingClientRect();
        const l = link.getBoundingClientRect();
        const stack = document.elementsFromPoint(c.left + c.width / 2, c.top + c.height / 2);
        const linkStack = document.elementsFromPoint(l.left + l.width / 2, l.top + l.height / 2);
        return {
          visibleBottom: ${320 - inset},
          barTop: Math.round(b.top),
          barBottom: Math.round(b.bottom),
          barHeight: Math.round(b.height),
          ctrl: { x: c.left + c.width / 2, y: c.top + c.height / 2 },
          link: { x: l.left + l.width / 2, y: l.top + l.height / 2 },
          topHitIsKey: stack[0] === ctrlKey || ctrlKey.contains(stack[0]),
          linkTopHit: linkStack[0] === link || link.contains(linkStack[0]),
        };
      })()`);
      assert.ok(evidence.barTop >= 0, `keys bar clipped above viewport: ${JSON.stringify(evidence)}`);
      assert.ok(evidence.barHeight >= 40, `keys bar clipped: ${JSON.stringify(evidence)}`);
      assert.ok(evidence.barBottom <= evidence.visibleBottom, `keys bar under keyboard: ${JSON.stringify(evidence)}`);
      assert.equal(evidence.topHitIsKey, true, `keys not clickable at inset ${inset}: ${JSON.stringify(evidence)}`);
      assert.equal(evidence.linkTopHit, true, `statusbar link not clickable at inset ${inset}: ${JSON.stringify(evidence)}`);
      await clickAt(page, evidence.ctrl.x, evidence.ctrl.y);
      const pressed = await page.evaluate(`[...document.querySelectorAll(".nx-terminal-key")].find((k) => k.textContent.trim() === "Ctrl")?.getAttribute("aria-pressed")`);
      assert.equal(pressed, "true", `CDP click on Ctrl must toggle aria-pressed at inset ${inset}`);
      await clickAt(page, evidence.ctrl.x, evidence.ctrl.y);
      await clickAt(page, evidence.link.x, evidence.link.y);
      await page.waitFor("Boolean([...document.querySelectorAll('.nx-tab')].some((t) => t.textContent.includes('审计日志')))");
      await screenshot(page, `ime-short-landscape-keys-568x320-inset-${inset}.png`);
    }
    return { evidence: { insets: [200, 240] } };
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

  await textScaleAcceptance(page);
}

const textScaleMatrix = [
  { name: "320x568", width: 320, height: 568, mobile: true, dsf: 1.6 },
  { name: "390x844", width: 390, height: 844, mobile: true, dsf: 1.6 },
  { name: "560x800", width: 560, height: 800, mobile: true, dsf: 1.6 },
  { name: "820x1180", width: 820, height: 1180, mobile: false, dsf: 1.6 },
  { name: "1280x900-desktop", width: 1280, height: 900, mobile: false, dsf: 1.6 },
];

const appearanceSeed = (terminalTheme) =>
  `try { localStorage.setItem("nexterm.appearance.v1", JSON.stringify({ uiFontPreset: 13, uiFontScale: 1.25, terminalFontSize: 13, terminalTheme: ${JSON.stringify(terminalTheme)} })); } catch {}`;

const appearanceSeedFull = (overrides) =>
  `try { localStorage.setItem("nexterm.appearance.v1", JSON.stringify({ uiFontPreset: 13, uiFontScale: 1, terminalFontSize: 13, terminalTheme: "dark", ...${JSON.stringify(overrides)} })); } catch {}`;

const TEXT_SNAPSHOT = `(() => {
  const out = [];
  for (const el of document.querySelectorAll("#root *")) {
    if (el.closest(".xterm")) continue;
    if (!el.getClientRects().length) continue;
    const hasText = [...el.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim());
    if (!hasText) continue;
    out.push({
      key: out.length + "|" + el.tagName + "|" + String(el.className).slice(0, 50),
      size: parseFloat(getComputedStyle(el).fontSize),
    });
  }
  return out;
})()`;

async function textScaleAcceptance(page) {
  for (const vp of textScaleMatrix) {
    await pass(`text200-no-hoverflow-${vp.name}`, async () => {
      await boot(page, { viewport: vp, extraInit: appearanceSeed("dark") });
      const evidence = await page.evaluate(`(() => ({
        innerWidth: window.innerWidth,
        docScrollWidth: document.scrollingElement.scrollWidth,
        bodyScrollWidth: document.body.scrollWidth,
        bodyFontSize: getComputedStyle(document.body).fontSize,
        termTheme: document.documentElement.dataset.nxTermTheme ?? null,
        uiTheme: document.documentElement.dataset.nxTheme ?? null,
      }))()`);
      assert.equal(evidence.bodyFontSize, "16.25px", `in-app 1.25x on the 13px preset must compose (1.25 x OS 1.6 = 200% text): ${JSON.stringify(evidence)}`);
      assert.equal(evidence.termTheme, "dark", `terminal theme must stay dark by default: ${JSON.stringify(evidence)}`);
      assert.ok(
        evidence.docScrollWidth <= evidence.innerWidth + 1 && evidence.bodyScrollWidth <= evidence.innerWidth + 1,
        `page-level horizontal overflow at 200% text: ${JSON.stringify(evidence)}`,
      );
      if (vp.name === "320x568" || vp.name === "1280x900-desktop") {
        await screenshot(page, `text200-${vp.name}.png`);
      }
      return { evidence };
    });
  }

  for (const name of ["390x844", "1280x900-desktop"]) {
    await pass(`text200-light-terminal-${name}`, async () => {
      const vp = textScaleMatrix.find((v) => v.name === name);
      await boot(page, { viewport: vp, extraInit: appearanceSeed("light") });
      const evidence = await page.evaluate(`(() => ({
        innerWidth: window.innerWidth,
        docScrollWidth: document.scrollingElement.scrollWidth,
        bodyScrollWidth: document.body.scrollWidth,
        termTheme: document.documentElement.dataset.nxTermTheme ?? null,
        xtermRendered: Boolean(document.querySelector(".xterm-screen")),
      }))()`);
      assert.equal(evidence.termTheme, "light", JSON.stringify(evidence));
      assert.equal(evidence.xtermRendered, true, JSON.stringify(evidence));
      assert.ok(
        evidence.docScrollWidth <= evidence.innerWidth + 1 && evidence.bodyScrollWidth <= evidence.innerWidth + 1,
        `page-level horizontal overflow with light terminal at 200% text: ${JSON.stringify(evidence)}`,
      );
      await screenshot(page, `text200-light-terminal-${name}.png`);
      return { evidence };
    });
  }

  const desktopVp = textScaleMatrix.find((v) => v.name === "1280x900-desktop");

  await pass("text-factor-covers-all-text", async () => {
    await boot(page, { viewport: desktopVp, extraInit: appearanceSeedFull({}) });
    await sleep(400);
    const base = await page.evaluate(TEXT_SNAPSHOT);
    const baseMap = new Map(base.map((e) => [e.key, e.size]));

    const scenarios = [
      { name: "scale1.25", seed: { uiFontScale: 1.25 }, min: 1.2, max: 1.3 },
      { name: "preset14.5", seed: { uiFontPreset: 14.5 }, min: 1.1, max: 1.13 },
      { name: "preset12", seed: { uiFontPreset: 12 }, min: 0.91, max: 0.94 },
    ];
    const evidence = { baseTotal: base.length, scenarios: {} };
    for (const scenario of scenarios) {
      await boot(page, { viewport: desktopVp, extraInit: appearanceSeedFull(scenario.seed) });
      await sleep(400);
      const snap = await page.evaluate(TEXT_SNAPSHOT);
      const missed = [];
      let matched = 0;
      for (const el of snap) {
        const before = baseMap.get(el.key);
        if (before === undefined) continue;
        matched++;
        const ratio = el.size / before;
        if (ratio < scenario.min || ratio > scenario.max) {
          missed.push(`${el.key} ${before}->${el.size} (${ratio.toFixed(3)})`);
        }
      }
      evidence.scenarios[scenario.name] = { matched, missed: missed.slice(0, 8) };
      assert.ok(matched >= 40, `${scenario.name}: only ${matched} matched elements, snapshot unstable`);
      assert.deepEqual(missed, [], `${scenario.name}: text elements not following the factor`);
    }
    return { evidence };
  });

  await pass("text-factor-representative-elements", async () => {
    const representativeSizes = `(() => {
      const byText = (selector, text) => [...document.querySelectorAll(selector)].find(
        (el) => el.childNodes.length && [...el.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim() === text),
      );
      const treeRow = document.querySelector('.nx-left-dock [role="treeitem"]');
      const xs = [...document.querySelectorAll("#root *")].find((el) => el.classList?.contains("text-xs"));
      const entries = {
        fileTreeRow: treeRow ? parseFloat(getComputedStyle(treeRow).fontSize) : null,
        tab: document.querySelector(".nx-tab") ? parseFloat(getComputedStyle(document.querySelector(".nx-tab")).fontSize) : null,
        toolbarTitle: byText("span", "NexTerm") ? parseFloat(getComputedStyle(byText("span", "NexTerm")).fontSize) : null,
        textXsSection: xs ? parseFloat(getComputedStyle(xs).fontSize) : null,
      };
      return entries;
    })()`;
    const openTree = `document.querySelector('.nx-rail button[aria-label="文件树"]').click()`;

    await boot(page, { viewport: desktopVp, extraInit: appearanceSeedFull({}) });
    await page.evaluate(openTree);
    await page.waitFor('Boolean(document.querySelector(\'.nx-left-dock [role="treeitem"\'))');
    const base = await page.evaluate(representativeSizes);

    await boot(page, { viewport: desktopVp, extraInit: appearanceSeedFull({ uiFontScale: 1.25 }) });
    await page.evaluate(openTree);
    await page.waitFor('Boolean(document.querySelector(\'.nx-left-dock [role="treeitem"\'))');
    const scaled = await page.evaluate(representativeSizes);

    const expected = { scale: 1.25, preset: 14.5 / 13 };
    const evidence = { base, scaled };
    for (const key of Object.keys(base)) {
      assert.ok(base[key] !== null, `${key} missing on the default workspace`);
      const ratio = scaled[key] / base[key];
      assert.ok(
        Math.abs(ratio - expected.scale) <= 0.02,
        `${key}: expected ${expected.scale}x, got ${ratio} (${base[key]}->${scaled[key]})`,
      );
    }
    await boot(page, { viewport: desktopVp, extraInit: appearanceSeedFull({ uiFontPreset: 14.5 }) });
    await page.evaluate(openTree);
    await page.waitFor('Boolean(document.querySelector(\'.nx-left-dock [role="treeitem"\'))');
    const preset = await page.evaluate(representativeSizes);
    evidence.preset = preset;
    for (const key of Object.keys(base)) {
      const ratio = preset[key] / base[key];
      assert.ok(
        Math.abs(ratio - expected.preset) <= 0.02,
        `${key}: expected preset ratio ${expected.preset}, got ${ratio} (${base[key]}->${preset[key]})`,
      );
    }
    await screenshot(page, "text-factor-representative-1280.png");
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
    fs.rmSync(chrome.profile, { recursive: true, force: true, maxRetries: 8, retryDelay: 250 });
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
