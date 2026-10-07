#!/usr/bin/env node
// 纯键盘真实浏览器验收（M91）：所有交互只通过 CDP Input.dispatchKeyEvent /
// Input.insertText 完成 —— 不调用 element.click()、element.focus() 或表单提交；
// 断言只读取 DOM/ARIA 属性与焦点位置。
//
// 运行：node src/test/keyboard-acceptance.mjs
// 需要本机 Chrome/Chromium（CHROME_PATH 可覆盖）与 pnpm（启动 vite dev server）。
// 报告与截图写入 target/acceptance-keyboard/。
import { spawn, spawnSync } from "node:child_process";
import assert from "node:assert/strict";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { startVite } from "./lib/acceptance-process.mjs";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const OUT = path.join(ROOT, "target/acceptance-keyboard");
const VITE_PORT = Number(process.env.NEXTERM_VITE_PORT || 1421);
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

const KEYS = {
  Tab: { key: "Tab", code: "Tab", vk: 9 },
  Enter: { key: "Enter", code: "Enter", vk: 13 },
  Space: { key: " ", code: "Space", vk: 32 },
  ArrowLeft: { key: "ArrowLeft", code: "ArrowLeft", vk: 37 },
  ArrowUp: { key: "ArrowUp", code: "ArrowUp", vk: 38 },
  ArrowRight: { key: "ArrowRight", code: "ArrowRight", vk: 39 },
  ArrowDown: { key: "ArrowDown", code: "ArrowDown", vk: 40 },
  Escape: { key: "Escape", code: "Escape", vk: 27 },
};

// 实测（headless Chromium 154 / macOS）：nativeVirtualKeyCode 会触发 IME “Unidentified”
// 键盘事件风暴；Enter 只有带 text 的 keyDown 才会触发原生 <button> 的点击合成，
// rawKeyDown 只能到达 JS keydown 监听器（React 处理器能收到，但按钮不会被激活）。
async function press(page, name) {
  const def = KEYS[name];
  if (!def) throw new Error(`unknown key: ${name}`);
  const base = { key: def.key, code: def.code, windowsVirtualKeyCode: def.vk };
  if (name === "Enter") {
    await page.send("Input.dispatchKeyEvent", { type: "keyDown", ...base, text: "\r" });
  } else {
    await page.send("Input.dispatchKeyEvent", { type: "rawKeyDown", ...base });
  }
  await page.send("Input.dispatchKeyEvent", { type: "keyUp", ...base });
  await sleep(40);
}

async function pressCtrl(page, letter, vk) {
  const code = `Key${letter.toUpperCase()}`;
  const base = { key: letter, code, windowsVirtualKeyCode: vk, modifiers: 2 };
  await page.send("Input.dispatchKeyEvent", { type: "rawKeyDown", ...base });
  await page.send("Input.dispatchKeyEvent", { type: "keyUp", ...base });
  await sleep(60);
}

// 从当前焦点出发，仅按 Tab 直到 predicate(activeElement) 为真；返回按键次数。
async function tabUntil(page, predicate, max = 200) {
  for (let i = 0; i < max; i++) {
    const hit = await page.evaluate(`(${predicate})(document.activeElement)`);
    if (hit) return i;
    await press(page, "Tab");
  }
  throw new Error(`Tab traversal exhausted after ${max}: ${predicate}`);
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
}

// 每个检查都从全新加载开始：关闭标签等操作会把焦点送进 xterm（xterm 按设计吃掉
// Tab），一旦焦点落进终端，纯 Tab 遍历就再也无法离开。重新加载让焦点回到 body，
// 既保持“只用键盘交互”，又避免检查之间的焦点串扰。
async function keyboardAcceptance(page) {
  await pass("tabstrip-semantics", async () => {
    await boot(page);
    const evidence = await page.evaluate(`(() => {
      const wsList = document.querySelector('[role="tablist"][aria-label="工作区"]');
      const paneList = document.querySelector('[role="tablist"][aria-label="标签页"]');
      if (!wsList || !paneList) return { ok: false, reason: "tablist missing" };
      const wsTabs = [...wsList.querySelectorAll('[role="tab"]')];
      const paneTabs = [...paneList.querySelectorAll('[role="tab"]')];
      const selected = wsTabs.filter((t) => t.getAttribute('aria-selected') === 'true');
      const panel = selected[0] && document.getElementById(selected[0].getAttribute('aria-controls'));
      return {
        ok: true,
        wsTabCount: wsTabs.length,
        paneTabCount: paneTabs.length,
        singleSelected: selected.length === 1,
        rovingTabindex: wsTabs.every((t) => t.tabIndex === (t.getAttribute('aria-selected') === 'true' ? 0 : -1)),
        panelRole: panel?.getAttribute('role') ?? null,
        panelLabelledBy: panel?.getAttribute('aria-labelledby') ?? null,
        wsCloseButtons: [...wsList.querySelectorAll('button[aria-label^="关闭工作区"]')].length,
        tabCloseButtons: [...paneList.querySelectorAll('button[aria-label^="关闭标签"]')].length,
        railButtonsLabeled: [...document.querySelectorAll('.nx-rail button')].every((b) => b.getAttribute('aria-label')),
        railCurrent: document.querySelector('.nx-rail button[aria-current]')?.getAttribute('aria-label') ?? null,
      };
    })()`);
    assert.equal(evidence.ok, true);
    assert.equal(evidence.singleSelected, true);
    assert.equal(evidence.rovingTabindex, true, JSON.stringify(evidence));
    assert.equal(evidence.panelRole, "tabpanel");
    assert.equal(evidence.wsCloseButtons, evidence.wsTabCount);
    assert.equal(evidence.tabCloseButtons, evidence.paneTabCount);
    assert.equal(evidence.railButtonsLabeled, true);
    assert.ok(evidence.railCurrent, "rail should expose aria-current");
    return { evidence };
  });

  await pass("toast-shadow-follows-theme", async () => {
    // 回归 R2：shadow-pop 工具类把暗色值静态展开进生成 CSS，亮主题不生效。
    // 这里用真实浏览器分别按暗/亮主题加载，断言 toast 计算出的 box-shadow
    // 不仅两主题不同，而且各自与本主题运行时的 --shadow-pop token 颜色一致。
    const readShadow = `(() => {
      const btn = document.querySelector('.nx-toasts button');
      if (!btn) return null;
      const root = getComputedStyle(document.documentElement);
      return {
        theme: document.documentElement.dataset.nxTheme ?? "dark",
        token: root.getPropertyValue("--shadow-pop").trim(),
        boxShadow: getComputedStyle(btn).boxShadow,
      };
    })()`;
    const loadWithTheme = async (theme) => {
      const { identifier } = await page.send("Page.addScriptToEvaluateOnNewDocument", {
        source: `try { localStorage.setItem("nexterm.theme.v1", ${JSON.stringify(theme)}); } catch {}`,
      });
      try {
        await page.navigate(`${VITE}/?demo=1`);
        // toast 3.5s 自动消失：出现后立即读取。
        await page.waitFor("Boolean(document.querySelector('.nx-toasts button'))");
        return await page.evaluate(readShadow);
      } finally {
        await page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier });
      }
    };
    // Tailwind box-shadow 复合值含 inset/ring 占位层（rgba(0,0,0,0) 0px 0px 0px 0px），
    // 比较前过滤全透占位层，只比真实阴影颜色。
    const colorsOf = (s) =>
      (String(s).match(/rgba?\([^)]*\)/g) ?? [])
        .map((c) => {
          const n = c.match(/[\d.]+/g).map(Number);
          return `rgba(${n[0]},${n[1]},${n[2]},${n.length > 3 ? n[3] : 1})`;
        })
        .filter((c) => c !== "rgba(0,0,0,0)");

    const dark = await loadWithTheme("dark");
    const light = await loadWithTheme("light");
    assert.ok(dark && light, "toast should render in both themes");
    assert.equal(dark.theme, "dark");
    assert.equal(light.theme, "light");
    assert.ok(dark.boxShadow && dark.boxShadow !== "none");
    assert.notEqual(dark.boxShadow, light.boxShadow, "toast box-shadow must differ between themes");
    assert.deepEqual(colorsOf(dark.boxShadow), colorsOf(dark.token), `dark toast must consume the dark token: ${JSON.stringify(dark)}`);
    assert.deepEqual(colorsOf(light.boxShadow), colorsOf(light.token), `light toast must consume the light token: ${JSON.stringify(light)}`);
    return { evidence: { dark, light } };
  });

  await pass("workspace-tab-arrow-switch", async () => {
    await boot(page);
    const isWsTab = `(el) => el?.getAttribute?.('role') === 'tab' && el.closest('[role="tablist"]')?.getAttribute('aria-label') === '工作区'`;
    const tabs = await tabUntil(page, isWsTab);
    const before = await page.evaluate(`document.querySelectorAll('[role="tablist"][aria-label="工作区"] [role="tab"]').length`);
    assert.equal(before, 1, "demo should boot with exactly one workspace");

    await pressCtrl(page, "t", 84);
    await page.waitFor(`document.querySelectorAll('[role="tablist"][aria-label="工作区"] [role="tab"]').length === 2`);
    const afterNew = await page.evaluate(`(() => {
      const selected = document.querySelector('[role="tablist"][aria-label="工作区"] [role="tab"][aria-selected="true"]');
      return { title: selected?.textContent ?? null, focusedIsSelected: document.activeElement === selected };
    })()`);

    // Ctrl+T 后焦点仍停在原工作区标签上：第一次 ArrowRight 选中新工作区（焦点跟随），
    // 第二次 ArrowRight 循环回第一个工作区 —— 两次选择必须不同，证明方向键真的在切换。
    await press(page, "ArrowRight");
    const afterFirst = await page.evaluate(`(() => {
      const selected = document.querySelector('[role="tablist"][aria-label="工作区"] [role="tab"][aria-selected="true"]');
      return { title: selected?.textContent ?? null, focusFollows: document.activeElement === selected };
    })()`);
    assert.notEqual(afterFirst.title, null);
    assert.equal(afterFirst.focusFollows, true, "focus must follow selection");

    await press(page, "ArrowRight");
    const afterSecond = await page.evaluate(`(() => {
      const tabs = [...document.querySelectorAll('[role="tablist"][aria-label="工作区"] [role="tab"]')];
      const selected = tabs.find((t) => t.getAttribute('aria-selected') === 'true');
      return {
        title: selected?.textContent ?? null,
        focusFollows: document.activeElement === selected,
        tabindex: selected?.tabIndex,
      };
    })()`);
    assert.notEqual(afterSecond.title, afterFirst.title, "ArrowRight must move the selection");
    assert.equal(afterSecond.focusFollows, true, "focus must follow selection");
    assert.equal(afterSecond.tabindex, 0);

    await press(page, "ArrowLeft");
    const back = await page.evaluate(`document.querySelector('[role="tablist"][aria-label="工作区"] [role="tab"][aria-selected="true"]')?.textContent`);
    assert.equal(back, afterFirst.title, "ArrowLeft must switch back");
    return { evidence: { tabPressesToWsTab: tabs, afterNew, afterFirst, afterSecond, back } };
  });

  await pass("palette-open-tab-and-keyboard-close", async () => {
    await boot(page);
    const paneTabsBefore = await page.evaluate(`document.querySelectorAll('[role="tablist"][aria-label="标签页"] [role="tab"]').length`);

    await pressCtrl(page, "k", 75);
    await page.waitFor(`Boolean(document.querySelector('[role="dialog"] [role="combobox"]'))`);
    await page.send("Input.insertText", { text: "设置" });
    await page.waitFor(`[...document.querySelectorAll('[role="option"],[role="listbox"] *')].some((el) => el.textContent?.includes('设置'))`);
    await press(page, "Enter");
    // 每个工作区都有自己的「标签页」tablist（隐藏工作区也在 DOM 里），必须只看可见的那个。
    await page.waitFor(`[...document.querySelectorAll('[role="tablist"][aria-label="标签页"] [role="tab"]')].some((t) => t.getAttribute('aria-selected') === 'true' && t.offsetParent !== null && t.textContent?.includes('设置'))`);
    const afterOpen = await page.evaluate(`document.querySelectorAll('[role="tablist"][aria-label="标签页"] [role="tab"]').length`);
    assert.equal(afterOpen, paneTabsBefore + 1, "settings tab should open via palette");

    const isSettingsTab = `(el) => el?.getAttribute?.('role') === 'tab' && el.getAttribute('aria-selected') === 'true' && el.offsetParent !== null && el.textContent?.includes('设置')`;
    const presses = await tabUntil(page, isSettingsTab);
    await press(page, "Tab");
    const closeFocus = await page.evaluate(`(() => {
      const el = document.activeElement;
      return { tag: el?.tagName, label: el?.getAttribute?.('aria-label') ?? null };
    })()`);
    assert.equal(closeFocus.tag, "BUTTON");
    assert.equal(closeFocus.label, "关闭标签 设置");

    await press(page, "Enter");
    await page.waitFor(`document.querySelectorAll('[role="tablist"][aria-label="标签页"] [role="tab"]').length === ${paneTabsBefore}`);
    return { evidence: { paneTabsBefore, afterOpen, tabPresses: presses, closeFocus } };
  });

  await pass("asset-tree-keyboard", async () => {
    await boot(page);
    const isRailAssets = `(el) => el?.tagName === 'BUTTON' && el.getAttribute?.('aria-label') === '资产'`;
    await tabUntil(page, isRailAssets);
    await press(page, "Enter");
    await page.waitFor(`Boolean(document.querySelector('[role="tree"][aria-label="资产"]'))`);
    await page.waitFor(`document.querySelectorAll('[role="tree"][aria-label="资产"] [role="treeitem"]').length > 0`);

    const semantics = await page.evaluate(`(() => {
      const items = [...document.querySelectorAll('[role="tree"][aria-label="资产"] [role="treeitem"]')];
      const groups = items.filter((el) => el.hasAttribute('aria-expanded'));
      return {
        itemCount: items.length,
        groupCount: groups.length,
        allFocusable: items.every((el) => el.tabIndex === 0),
        levels: items.map((el) => el.getAttribute('aria-level')),
        labeledActions: [...document.querySelectorAll('[role="tree"] .nx-row-actions button')].every((b) => b.getAttribute('aria-label')),
        focusWithinRule: [...document.querySelectorAll('[role="tree"] .nx-row, [role="tree"] [role="treeitem"]')].some((el) => el.className.includes('focus-within:')),
      };
    })()`);
    assert.ok(semantics.itemCount > 0, "asset tree should render treeitems");
    assert.ok(semantics.groupCount > 0, "demo groups should expose aria-expanded");
    assert.equal(semantics.allFocusable, true);
    assert.equal(semantics.labeledActions, true);
    assert.equal(semantics.focusWithinRule, true);

    const isFirstGroup = `(el) => el?.getAttribute?.('role') === 'treeitem' && el.hasAttribute('aria-expanded')`;
    await tabUntil(page, isFirstGroup);
    const expandedBefore = await page.evaluate(`document.activeElement.getAttribute('aria-expanded')`);
    await press(page, "ArrowLeft");
    const collapsed = await page.evaluate(`document.activeElement.getAttribute('aria-expanded')`);
    assert.notEqual(collapsed, expandedBefore, "ArrowLeft must collapse the group");
    await press(page, "ArrowRight");
    const expanded = await page.evaluate(`document.activeElement.getAttribute('aria-expanded')`);
    assert.equal(expanded, expandedBefore, "ArrowRight must re-expand the group");

    await press(page, "ArrowDown");
    const moved = await page.evaluate(`(() => {
      const el = document.activeElement;
      return { role: el?.getAttribute?.('role'), text: el?.textContent?.slice(0, 30) };
    })()`);
    assert.equal(moved.role, "treeitem", "ArrowDown must move focus to the next row");

    const wsBefore = await page.evaluate(`document.querySelectorAll('[role="tablist"][aria-label="工作区"] [role="tab"]').length`);
    const tabsBefore = await page.evaluate(`[...document.querySelectorAll('[role="tablist"][aria-label="标签页"] [role="tab"]')].length`);
    await press(page, "Enter");
    await page.waitFor(
      `document.querySelectorAll('[role="tablist"][aria-label="工作区"] [role="tab"]').length > ${wsBefore}` +
      ` || [...document.querySelectorAll('[role="tablist"][aria-label="标签页"] [role="tab"]')].length > ${tabsBefore}`,
      15_000,
    );
    const after = await page.evaluate(`(() => ({
      workspaces: document.querySelectorAll('[role="tablist"][aria-label="工作区"] [role="tab"]').length,
      tabs: [...document.querySelectorAll('[role="tablist"][aria-label="标签页"] [role="tab"]')].length,
    }))()`);
    return { evidence: { semantics, expandedBefore, collapsed, expanded, moved, wsBefore, tabsBefore, after } };
  });

  await pass("file-tree-keyboard", async () => {
    await boot(page);
    // 演示模式启动即连接 web-01，connectAsset 会把左栏切到文件树。
    await page.waitFor(`Boolean(document.querySelector('[role="tree"][aria-label="文件"]'))`);
    await page.waitFor(`document.querySelectorAll('[role="tree"][aria-label="文件"] [role="treeitem"]').length > 0`);
    const semantics = await page.evaluate(`(() => {
      const items = [...document.querySelectorAll('[role="tree"][aria-label="文件"] [role="treeitem"]')];
      const dirs = items.filter((el) => el.hasAttribute('aria-expanded'));
      return {
        itemCount: items.length,
        dirCount: dirs.length,
        levels: items.map((el) => el.getAttribute('aria-level')),
        crumbCurrent: document.querySelector('.nx-path-crumb[aria-current]')?.getAttribute('aria-current') ?? null,
      };
    })()`);
    assert.ok(semantics.itemCount > 0, "file tree should render treeitems");
    assert.ok(semantics.dirCount > 0, "demo fs should contain directories");
    assert.equal(semantics.crumbCurrent, "location");

    const isFirstRow = `(el) => el?.getAttribute?.('role') === 'treeitem'`;
    await tabUntil(page, isFirstRow);
    const first = await page.evaluate(`(() => {
      const el = document.activeElement;
      return { text: el?.textContent?.trim().slice(0, 30), expanded: el?.getAttribute?.('aria-expanded') };
    })()`);

    let toggled = null;
    if (first.expanded !== null) {
      await press(page, "ArrowRight");
      const open = await page.evaluate(`document.activeElement.getAttribute('aria-expanded')`);
      assert.equal(open, "true", "ArrowRight must expand a collapsed directory");
      await press(page, "ArrowLeft");
      const closed = await page.evaluate(`document.activeElement.getAttribute('aria-expanded')`);
      assert.equal(closed, "false", "ArrowLeft must collapse it back");
      toggled = { open, closed };
    }

    await press(page, "Space");
    const selected = await page.evaluate(`document.activeElement.getAttribute('aria-selected')`);
    assert.equal(selected, "true", "Space must select the row");

    await press(page, "ArrowDown");
    const moved = await page.evaluate(`(() => {
      const el = document.activeElement;
      return { role: el?.getAttribute?.('role'), text: el?.textContent?.trim().slice(0, 30) };
    })()`);
    assert.equal(moved.role, "treeitem", "ArrowDown must move focus to the next row");
    assert.notEqual(moved.text, first.text);
    return { evidence: { semantics, first, toggled, selected, moved } };
  });

  await screenshot(page, "keyboard-final.png");
}

let vite;
let chrome;
let page;
try {
  [vite, chrome] = await Promise.all([startVite({ root: ROOT, port: VITE_PORT }), startChrome()]);
  page = await newPage(chrome);
  await keyboardAcceptance(page);
} catch (error) {
  harnessErrors.push(String(error?.stack || error));
} finally {
  if (page) page.close();
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
    keyboard_only: true,
    note: "所有交互仅通过 Input.dispatchKeyEvent / Input.insertText；未调用 element.click() 或 element.focus()",
  },
  checks,
  harness_errors: harnessErrors,
};
fs.writeFileSync(path.join(OUT, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
console.warn(`keyboard acceptance: ${checks.filter((check) => check.status === "passed").length}/${checks.length} checks passed; report=${path.join(OUT, "report.json")}`);
if (failed.length || harnessErrors.length) process.exit(1);
