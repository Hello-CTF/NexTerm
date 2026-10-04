#!/usr/bin/env node
import { spawn, spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const OUT = path.join(ROOT, "target/acceptance-close-background");
const VITE_PORT = Number(process.env.NEXTERM_VITE_PORT || 20000 + Math.floor(Math.random() * 20000));
const VITE = `http://127.0.0.1:${VITE_PORT}`;
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

function startVite() {
  const command = globalThis.process.platform === "win32" ? "pnpm.cmd" : "pnpm";
  const process = spawn(command, ["exec", "vite", "--host", "127.0.0.1", "--port", String(VITE_PORT), "--strictPort"], {
    cwd: ROOT,
    env: { ...globalThis.process.env, NODE_OPTIONS: "" },
    stdio: ["ignore", "pipe", "pipe"],
    detached: globalThis.process.platform !== "win32",
  });
  return waitHttp(VITE, process).then(() => process);
}

const VISIBLE = ".nx-workspace-main [role='tabpanel']:not(.hidden)";
const paneTabCount = `document.querySelectorAll("${VISIBLE} [role='tablist'][aria-label='标签页'] [role='tab']").length`;
const bgCount = `(() => {
  const panel = document.querySelector("${VISIBLE}");
  if (!panel) return -1;
  if (panel.textContent.includes("读取中…")) return -1;
  const empty = panel.querySelector(".nx-table-empty");
  if (empty && empty.textContent.includes("没有在后台运行的终端")) return 0;
  if (!panel.querySelector(".nx-table")) return -1;
  return panel.querySelectorAll(".nx-table tbody tr").length;
})()`;
const toastText = `[...document.querySelectorAll(".nx-toasts button")].map((b) => b.textContent).join("\\n")`;
const dialogText = `document.querySelector('[role="alertdialog"] .nx-modal-body')?.textContent ?? ""`;
const clickByText = (label) => `(() => {
  const btn = [...document.querySelectorAll(".nx-menu .nx-menu-item")].find((b) => b.textContent.includes(${JSON.stringify(label)}));
  if (!btn) return false;
  btn.click();
  return true;
})()`;
const clickDialogButton = (label) => `(() => {
  const btn = [...document.querySelectorAll('[role="alertdialog"] .nx-modal-footer button')].find((b) => b.textContent.trim() === ${JSON.stringify(label)});
  if (!btn) return false;
  btn.click();
  return true;
})()`;

async function clickSelector(page, selector) {
  const ok = await page.evaluate(`(() => {
    const el = document.querySelector(${JSON.stringify(selector)});
    if (!el) return false;
    el.click();
    return true;
  })()`);
  if (!ok) throw new Error(`selector not found: ${selector}`);
}

async function rightClickSelector(page, selector) {
  const rect = await page.evaluate(`(() => {
    const el = document.querySelector(${JSON.stringify(selector)});
    if (!el) return null;
    const r = el.getBoundingClientRect();
    return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
  })()`);
  if (!rect) throw new Error(`selector not found: ${selector}`);
  await page.send("Input.dispatchMouseEvent", { type: "mousePressed", x: rect.x, y: rect.y, button: "right", buttons: 2, clickCount: 1 });
  await page.send("Input.dispatchMouseEvent", { type: "mouseReleased", x: rect.x, y: rect.y, button: "right", buttons: 0, clickCount: 1 });
  await page.waitFor(`Boolean(document.querySelector(".nx-menu"))`);
}

async function pressCtrl(page, letter, vk) {
  const code = `Key${letter.toUpperCase()}`;
  const base = { key: letter, code, windowsVirtualKeyCode: vk, modifiers: 2 };
  await page.send("Input.dispatchKeyEvent", { type: "rawKeyDown", ...base });
  await page.send("Input.dispatchKeyEvent", { type: "keyUp", ...base });
  await sleep(60);
}

async function pressCtrlBackslash(page) {
  const base = { key: "\\", code: "Backslash", windowsVirtualKeyCode: 220, modifiers: 2 };
  await page.send("Input.dispatchKeyEvent", { type: "rawKeyDown", ...base });
  await page.send("Input.dispatchKeyEvent", { type: "keyUp", ...base });
  await sleep(60);
}

async function screenshot(page, name) {
  const shot = await page.send("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(path.join(OUT, name), Buffer.from(shot.data, "base64"));
  return name;
}

async function boot(page) {
  await page.navigate(`${VITE}/?demo=1`);
  await page.waitFor(
    "Boolean(document.querySelector('[role=\"tablist\"][aria-label=\"工作区\"] [role=\"tab\"]') && document.querySelector('.xterm'))",
  );
  await sleep(600);
}

async function openBackgroundPanel(page) {
  await clickSelector(page, '.nx-rail button[aria-label="后台会话"]');
  await page.waitFor(`Boolean(document.querySelector("${VISIBLE} .nx-table"))`);
}

async function activateTerminalTab(page) {
  await clickSelector(page, `${VISIBLE} [role='tablist'][aria-label='标签页'] [role='tab']`);
}

async function clickTakeover(page) {
  return page.evaluate(`(() => {
    const panel = document.querySelector("${VISIBLE}");
    const btn = panel ? [...panel.querySelectorAll("button")].find((b) => b.textContent.includes("接管")) : null;
    if (!btn) return false;
    btn.click();
    return true;
  })()`);
}

async function activateBackgroundTab(page) {
  return page.evaluate(`(() => {
    const tab = [...document.querySelectorAll("${VISIBLE} [role='tablist'][aria-label='标签页'] [role='tab']")].find((t) => t.textContent.includes("后台会话"));
    if (tab) tab.click();
    return Boolean(tab);
  })()`);
}

async function killSeedBackgroundTab(page) {
  await openBackgroundPanel(page);
  await page.waitFor(`${bgCount} === 1`);
  await clickSelector(page, `${VISIBLE} .nx-table button[title="停止这个终端里的进程"]`);
  await page.waitFor(`Boolean(document.querySelector('[role="alertdialog"]'))`);
  await page.evaluate(clickDialogButton("确定"));
  await page.waitFor(`${bgCount} === 0`);
}

async function closeBackgroundAcceptance(page) {
  await pass("close-x-detach-takeover", async () => {
    await boot(page);
    await killSeedBackgroundTab(page);
    await activateTerminalTab(page);
    await clickSelector(page, `${VISIBLE} [role='tablist'][aria-label='标签页'] [role='tab'] button[aria-label^="关闭标签"]`);
    await page.waitFor(`${paneTabCount} === 1`);
    assert.equal(await page.evaluate(`Boolean(document.querySelector('[role="alertdialog"]'))`), false, "close of a running terminal must not ask");
    await page.waitFor(`${toastText}.includes("已转入后台")`);
    await page.waitFor(`${bgCount} === 1`);
    const tookOver = await clickTakeover(page);
    assert.equal(tookOver, true, "takeover button must exist in the background list");
    await page.waitFor(`${bgCount} === 0`);
    await page.waitFor(`${paneTabCount} === 2`);
    return { evidence: { toast: "已转入后台", takeover: "detached tab restored, background empty" } };
  });

  await pass("close-ctrl-w-detaches", async () => {
    await boot(page);
    await killSeedBackgroundTab(page);
    await activateTerminalTab(page);
    await pressCtrl(page, "w", 87);
    await page.waitFor(`${paneTabCount} === 1`);
    assert.equal(await page.evaluate(`Boolean(document.querySelector('[role="alertdialog"]'))`), false);
    await page.waitFor(`${bgCount} === 1`);
  });

  await pass("context-menu-close-detaches", async () => {
    await boot(page);
    await killSeedBackgroundTab(page);
    await rightClickSelector(page, `${VISIBLE} [role='tablist'][aria-label='标签页'] [role='tab']`);
    const hasKill = await page.evaluate(`[...document.querySelectorAll(".nx-menu .nx-menu-item")].some((b) => b.textContent.includes("结束进程"))`);
    assert.equal(hasKill, true, "running terminal tab menu must offer the kill danger action");
    assert.equal(await page.evaluate(clickByText("关闭标签")), true);
    await page.waitFor(`${paneTabCount} === 1`);
    await page.waitFor(`${bgCount} === 1`);
  });

  await pass("kill-from-tab-menu-confirms", async () => {
    await boot(page);
    await killSeedBackgroundTab(page);
    const tab = `${VISIBLE} [role='tablist'][aria-label='标签页'] [role='tab']`;
    await rightClickSelector(page, tab);
    assert.equal(await page.evaluate(clickByText("结束进程")), true);
    await page.waitFor(`Boolean(document.querySelector('[role="alertdialog"]'))`);
    const message = await page.evaluate(dialogText);
    assert.ok(message.includes("结束"), `kill confirm must mention termination: ${message}`);
    assert.equal(await page.evaluate(clickDialogButton("取消")), true);
    await page.waitFor(`!document.querySelector('[role="alertdialog"]')`);
    assert.equal(await page.evaluate(paneTabCount), 2, "cancel must keep the tab");

    await rightClickSelector(page, tab);
    await page.evaluate(clickByText("结束进程"));
    await page.waitFor(`Boolean(document.querySelector('[role="alertdialog"]'))`);
    await page.evaluate(clickDialogButton("确定"));
    await page.waitFor(`${paneTabCount} === 1`);
    await page.waitFor(`${bgCount} === 0`);
    return { evidence: { message } };
  });

  await pass("workspace-close-detaches-batch", async () => {
    await boot(page);
    await killSeedBackgroundTab(page);
    await clickSelector(page, `${VISIBLE} button[aria-label="新建终端标签"]`);
    await page.waitFor(`${paneTabCount} === 3`);
    await page.waitFor("document.querySelectorAll('.xterm').length >= 2");
    await sleep(600);
    await clickSelector(page, 'button[aria-label^="关闭工作区"]');
    await page.waitFor(`document.querySelectorAll('[role="tablist"][aria-label="工作区"] [role="tab"]').length === 0`);
    assert.equal(await page.evaluate(`Boolean(document.querySelector('[role="alertdialog"]'))`), false, "workspace close must not ask about terminals");
    await openBackgroundPanel(page);
    await page.waitFor(`${bgCount} === 2`);
    return { evidence: { detached: 2 } };
  });

  await pass("docker-exec-close-kills", async () => {
    await boot(page);
    await killSeedBackgroundTab(page);
    await clickSelector(page, '.nx-rail button[aria-label="容器"]');
    await page.waitFor(`Boolean(document.querySelector('button[title="进入容器终端"]'))`);
    await clickSelector(page, 'button[title="进入容器终端"]');
    await page.waitFor(`${paneTabCount} === 4`);
    await sleep(600);
    await clickSelector(page, `${VISIBLE} [role='tablist'][aria-label='标签页'] [role='tab']:last-of-type button[aria-label^="关闭标签"]`);
    await page.waitFor(`Boolean(document.querySelector('[role="alertdialog"]'))`);
    const message = await page.evaluate(dialogText);
    assert.ok(message.includes("不支持转入后台"), `exec close must not fake a background entry: ${message}`);
    await page.evaluate(clickDialogButton("确定"));
    await page.waitFor(`${paneTabCount} === 3`);
    await page.waitFor(`${bgCount} === 0`);
    return { evidence: { message } };
  });

  await pass("disconnect-confirms-with-count", async () => {
    await boot(page);
    await killSeedBackgroundTab(page);
    await activateTerminalTab(page);
    await rightClickSelector(page, `${VISIBLE} .nx-terminal-body .relative`);
    assert.equal(await page.evaluate(clickByText("断开连接")), true);
    await page.waitFor(`Boolean(document.querySelector('[role="alertdialog"]'))`);
    const message = await page.evaluate(dialogText);
    assert.ok(message.includes("1 个正在运行的终端"), `disconnect confirm must carry the terminal count: ${message}`);
    await page.evaluate(clickDialogButton("取消"));
    await page.waitFor(`!document.querySelector('[role="alertdialog"]')`);
    const status = await page.evaluate(`document.querySelector("${VISIBLE} .nx-toolbar .nx-badge")?.textContent ?? ""`);
    assert.ok(status.includes("已连接"), `cancel must keep the session connected: ${status}`);

    await rightClickSelector(page, `${VISIBLE} .nx-terminal-body .relative`);
    await page.evaluate(clickByText("断开连接"));
    await page.waitFor(`Boolean(document.querySelector('[role="alertdialog"]'))`);
    await page.evaluate(clickDialogButton("确定"));
    await page.waitFor(`${toastText}.includes("已断开连接")`);
    return { evidence: { message } };
  });

  await pass("palette-kill-confirms", async () => {
    await boot(page);
    await killSeedBackgroundTab(page);
    await activateTerminalTab(page);
    await pressCtrl(page, "k", 75);
    await page.waitFor(`Boolean(document.querySelector('[role="dialog"] [role="combobox"]'))`);
    await page.send("Input.insertText", { text: "结束当前终端进程" });
    await page.waitFor(`[...document.querySelectorAll('[role="option"]')].some((el) => el.textContent.includes("结束当前终端进程"))`);
    await page.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13, text: "\r" });
    await page.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13 });
    await page.waitFor(`Boolean(document.querySelector('[role="alertdialog"]'))`);
    await page.evaluate(clickDialogButton("确定"));
    await page.waitFor(`${paneTabCount} === 1`);
    await page.waitFor(`${bgCount} === 0`);
  });

  await pass("workspace-close-exec-confirms", async () => {
    await boot(page);
    await killSeedBackgroundTab(page);
    await clickSelector(page, '.nx-rail button[aria-label="容器"]');
    await page.waitFor(`Boolean(document.querySelector('button[title="进入容器终端"]'))`);
    await clickSelector(page, 'button[title="进入容器终端"]');
    await page.waitFor(`${paneTabCount} === 4`);
    await sleep(600);
    await clickSelector(page, 'button[aria-label^="关闭工作区"]');
    await page.waitFor(`Boolean(document.querySelector('[role="alertdialog"]'))`);
    const message = await page.evaluate(dialogText);
    assert.ok(message.includes("1 个") && message.includes("容器 exec"), `workspace exec confirm must carry count and consequence: ${message}`);
    await page.evaluate(clickDialogButton("取消"));
    await page.waitFor(`!document.querySelector('[role="alertdialog"]')`);
    assert.equal(await page.evaluate(`document.querySelectorAll('[role="tablist"][aria-label="工作区"] [role="tab"]').length`), 1, "cancel must keep the workspace");
    assert.equal(await page.evaluate(paneTabCount), 4, "cancel must keep every tab");
    await activateBackgroundTab(page);
    await page.waitFor(`${bgCount} === 0`);

    await clickSelector(page, 'button[aria-label^="关闭工作区"]');
    await page.waitFor(`Boolean(document.querySelector('[role="alertdialog"]'))`);
    await page.evaluate(clickDialogButton("确定"));
    await page.waitFor(`document.querySelectorAll('[role="tablist"][aria-label="工作区"] [role="tab"]').length === 0`);
    await openBackgroundPanel(page);
    await page.waitFor(`${bgCount} === 1`);
    return { evidence: { message } };
  });

  await pass("unsplit-exec-confirms", async () => {
    await boot(page);
    await killSeedBackgroundTab(page);
    await pressCtrlBackslash(page);
    await page.waitFor(`document.querySelectorAll("${VISIBLE} .nx-tabstrip.is-sub").length === 2`);
    await sleep(600);
    await clickSelector(page, '.nx-rail button[aria-label="容器"]');
    await page.waitFor(`Boolean(document.querySelector('button[title="进入容器终端"]'))`);
    await clickSelector(page, 'button[title="进入容器终端"]');
    await page.waitFor(`document.querySelectorAll("${VISIBLE} [role='tablist'][aria-label='标签页'] [role='tab']").length === 5`);
    await sleep(600);
    await pressCtrlBackslash(page);
    await page.waitFor(`Boolean(document.querySelector('[role="alertdialog"]'))`);
    const message = await page.evaluate(dialogText);
    assert.ok(message.includes("容器 exec"), `unsplit exec confirm must name the consequence: ${message}`);
    await page.evaluate(clickDialogButton("取消"));
    await page.waitFor(`!document.querySelector('[role="alertdialog"]')`);
    assert.equal(await page.evaluate(`document.querySelectorAll("${VISIBLE} .nx-tabstrip.is-sub").length`), 2, "cancel must keep the split");
    await activateBackgroundTab(page);
    await page.waitFor(`${bgCount} === 0`);

    await pressCtrlBackslash(page);
    await page.waitFor(`Boolean(document.querySelector('[role="alertdialog"]'))`);
    await page.evaluate(clickDialogButton("确定"));
    await page.waitFor(`document.querySelectorAll("${VISIBLE} .nx-tabstrip.is-sub").length === 1`);
    await page.waitFor(`${bgCount} === 1`);
    return { evidence: { message } };
  });

  await pass("disconnect-counts-detached", async () => {
    await boot(page);
    await killSeedBackgroundTab(page);
    await activateTerminalTab(page);
    await clickSelector(page, `${VISIBLE} [role='tablist'][aria-label='标签页'] [role='tab'] button[aria-label^="关闭标签"]`);
    await page.waitFor(`${paneTabCount} === 1`);
    await page.waitFor(`${bgCount} === 1`);
    await clickSelector(page, `${VISIBLE} button[aria-label="新建终端标签"]`);
    await page.waitFor(`${paneTabCount} === 2`);
    await sleep(600);
    await rightClickSelector(page, `${VISIBLE} .nx-terminal-body .relative`);
    await page.evaluate(clickByText("断开连接"));
    await page.waitFor(`Boolean(document.querySelector('[role="alertdialog"]'))`);
    const message = await page.evaluate(dialogText);
    assert.ok(message.includes("2 个正在运行的终端"), `disconnect count must include the detached background terminal: ${message}`);
    await page.evaluate(clickDialogButton("取消"));
    await page.waitFor(`!document.querySelector('[role="alertdialog"]')`);
    return { evidence: { message } };
  });

  await pass("winrm-close-confirms", async () => {
    await boot(page);
    await killSeedBackgroundTab(page);
    await pressCtrl(page, "k", 75);
    await page.waitFor(`Boolean(document.querySelector('[role="dialog"] [role="combobox"]'))`);
    await page.send("Input.insertText", { text: "win-2019" });
    await page.waitFor(`[...document.querySelectorAll('[role="option"]')].some((el) => el.textContent.includes("win-2019"))`);
    await page.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13, text: "\r" });
    await page.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13 });
    await page.waitFor(`document.querySelectorAll('[role="tablist"][aria-label="工作区"] [role="tab"]').length === 2`);
    await sleep(800);
    await rightClickSelector(page, `${VISIBLE} [role='tablist'][aria-label='标签页'] [role='tab']`);
    const menuCopy = await page.evaluate(`(() => {
      const item = [...document.querySelectorAll(".nx-menu .nx-menu-item")].find((b) => b.textContent.includes("关闭标签"));
      return item?.textContent ?? "";
    })()`);
    assert.ok(
      menuCopy.includes("结束 WinRM 非交互进程") && !menuCopy.includes("转入后台"),
      `winrm tab menu must not promise a background entry: ${menuCopy}`,
    );
    assert.equal(await page.evaluate(clickByText("关闭标签")), true);
    await page.waitFor(`Boolean(document.querySelector('[role="alertdialog"]'))`);
    const message = await page.evaluate(dialogText);
    assert.ok(message.includes("WinRM 非交互") && message.includes("不支持转入后台"), `winrm close must confirm instead of faking background: ${message}`);
    await page.evaluate(clickDialogButton("取消"));
    await page.waitFor(`!document.querySelector('[role="alertdialog"]')`);
    assert.equal(await page.evaluate(paneTabCount), 1, "cancel must keep the winrm tab");
    await clickSelector(page, `${VISIBLE} [role='tablist'][aria-label='标签页'] [role='tab'] button[aria-label^="关闭标签"]`);
    await page.waitFor(`Boolean(document.querySelector('[role="alertdialog"]'))`);
    await page.evaluate(clickDialogButton("确定"));
    await page.waitFor(`${paneTabCount} === 0`);
    return { evidence: { message } };
  });

  await screenshot(page, "close-background-final.png");
}

let vite;
let chrome;
let page;
try {
  [vite, chrome] = await Promise.all([startVite(), startChrome()]);
  page = await newPage(chrome);
  await closeBackgroundAcceptance(page);
} catch (error) {
  harnessErrors.push(String(error?.stack || error));
} finally {
  if (page) page.close();
  stop(chrome?.process);
  stop(vite);
}

const checks = [...results.values()];
const failed = checks.filter((check) => check.status !== "passed");
const unexpectedPageErrors = pageErrors.filter((e) => !e.includes("syncScrollArea"));
const report = {
  schema_version: 1,
  status: failed.length || harnessErrors.length || unexpectedPageErrors.length ? "failed" : "passed",
  browser: chrome?.version || { status: "unavailable" },
  execution: {
    real_browser: true,
    headless: true,
    jsdom: false,
    note: "demo 传输层下驱动真实 Chromium：关闭=detach 无确认、结束进程/断开连接/exec 关闭=二次确认、exec 无假后台入口",
  },
  checks,
  harness_errors: harnessErrors,
  page_errors: pageErrors.slice(-20),
  unexpected_page_errors: unexpectedPageErrors,
};
fs.writeFileSync(path.join(OUT, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
console.warn(`close/background acceptance: ${checks.filter((check) => check.status === "passed").length}/${checks.length} checks passed; report=${path.join(OUT, "report.json")}`);
if (unexpectedPageErrors.length) {
  console.warn(`unexpected page errors: ${unexpectedPageErrors.length}; first=${unexpectedPageErrors[0]}`);
}
if (failed.length || harnessErrors.length || unexpectedPageErrors.length) process.exit(1);
