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
const OUT = path.join(ROOT, "target/host-key-acceptance");
const VITE_PORT = Number(process.env.NEXTERM_VITE_PORT || 20000 + Math.floor(Math.random() * 20000));
const VITE = `http://127.0.0.1:${VITE_PORT}`;
const SSH_USER = "acc";
const SSH_PASS = "acc-secret";
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

async function newPage(chrome, onError) {
  const response = await fetch(`http://127.0.0.1:${chrome.port}/json/new?about:blank`, { method: "PUT" });
  if (!response.ok) throw new Error(`cannot create Chrome target: ${response.status}`);
  const page = await CDP.connect((await response.json()).webSocketDebuggerUrl);
  await page.send("Runtime.enable");
  page.socket.addEventListener("message", (event) => {
    const message = JSON.parse(String(event.data));
    if (message.method === "Runtime.exceptionThrown") {
      const details = message.params.exceptionDetails;
      onError(`exception: ${details.exception?.description || details.text}`);
    }
    if (message.method === "Runtime.consoleAPICalled" && message.params.type === "error") {
      onError(`console.error: ${message.params.args.map((a) => a.value ?? a.description ?? "").join(" ")}`);
    }
  });
  return page;
}

const SSH_FIXTURE_DIR = path.join(OUT, "sshfixture");
const SSH_FIXTURE_BIN = path.join(SSH_FIXTURE_DIR, "sshfixture");

const SSH_FIXTURE_SOURCE = `package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"

	gossh "golang.org/x/crypto/ssh"
)

func loadOrCreateSigner(keyFile string) (gossh.Signer, error) {
	if data, err := os.ReadFile(keyFile); err == nil {
		block, _ := pem.Decode(data)
		if block != nil {
			if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
				if priv, ok := key.(ed25519.PrivateKey); ok {
					return gossh.NewSignerFromKey(priv)
				}
			}
		}
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return nil, err
	}
	return gossh.NewSignerFromKey(priv)
}

func main() {
	addr, keyFile, user, pass := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	signer, err := loadOrCreateSigner(keyFile)
	if err != nil {
		panic(err)
	}
	config := &gossh.ServerConfig{
		PasswordCallback: func(metadata gossh.ConnMetadata, password []byte) (*gossh.Permissions, error) {
			if metadata.User() == user && string(password) == pass {
				return nil, nil
			}
			return nil, fmt.Errorf("denied")
		},
	}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		panic(err)
	}
	fmt.Printf("READY %s %s\\n", listener.Addr().String(), gossh.FingerprintSHA256(signer.PublicKey()))
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go handle(conn, config)
	}
}

func handle(conn net.Conn, config *gossh.ServerConfig) {
	serverConn, channels, requests, err := gossh.NewServerConn(conn, config)
	if err != nil {
		conn.Close()
		return
	}
	defer serverConn.Close()
	go gossh.DiscardRequests(requests)
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(gossh.UnknownChannelType, "unsupported")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go serveSession(channel, channelRequests)
	}
}

func serveSession(channel gossh.Channel, requests <-chan *gossh.Request) {
	defer channel.Close()
	for request := range requests {
		switch request.Type {
		case "pty-req", "window-change":
			if request.WantReply {
				request.Reply(true, nil)
			}
		case "shell":
			request.Reply(true, nil)
			channel.Write([]byte("nexterm-acc-shell ready\\r\\n"))
			go func() {
				buffer := make([]byte, 4096)
				for {
					n, err := channel.Read(buffer)
					if err != nil {
						return
					}
					if _, err := channel.Write(append([]byte("echo:"), buffer[:n]...)); err != nil {
						return
					}
				}
			}()
		case "exec":
			request.Reply(true, nil)
			channel.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{0}))
			return
		default:
			request.Reply(false, nil)
		}
	}
}
`;

async function buildSSHFixture() {
  fs.mkdirSync(SSH_FIXTURE_DIR, { recursive: true });
  fs.writeFileSync(path.join(SSH_FIXTURE_DIR, "main.go"), SSH_FIXTURE_SOURCE);
  fs.writeFileSync(
    path.join(SSH_FIXTURE_DIR, "go.mod"),
    "module nextermaccssh\n\ngo 1.26.0\n\nrequire golang.org/x/crypto v0.57.0\n",
  );
  const tidy = spawnSync("go", ["mod", "tidy"], {
    cwd: SSH_FIXTURE_DIR,
    env: { ...globalThis.process.env, GOFLAGS: "-mod=mod" },
    encoding: "utf8",
  });
  if (tidy.status !== 0) throw new Error(`ssh fixture go mod tidy failed: ${tidy.stderr}`);
  const built = spawnSync("go", ["build", "-o", SSH_FIXTURE_BIN, "."], {
    cwd: SSH_FIXTURE_DIR,
    env: { ...globalThis.process.env, GOFLAGS: "-mod=mod" },
    encoding: "utf8",
  });
  if (built.status !== 0) throw new Error(`ssh fixture build failed: ${built.stderr}`);
}

async function startSSHFixture(port, keyFile) {
  const process = spawn(SSH_FIXTURE_BIN, [`127.0.0.1:${port}`, keyFile, SSH_USER, SSH_PASS], {
    stdio: ["ignore", "pipe", "pipe"],
  });
  let stdout = "";
  let stderr = "";
  let spawnError = null;
  process.stdout.on("data", (chunk) => { stdout += chunk; });
  process.stderr.on("data", (chunk) => { stderr += chunk; });
  process.on("error", (error) => { spawnError = error; });
  const deadline = Date.now() + 15_000;
  while (Date.now() < deadline) {
    const match = stdout.match(/READY (\S+) (\S+)/);
    if (match) return { process, addr: match[1], fingerprint: match[2] };
    if (spawnError) throw new Error(`ssh fixture spawn failed: ${spawnError}`);
    if (process.exitCode !== null) throw new Error(`ssh fixture exited ${process.exitCode}: ${stderr.slice(-500)}`);
    await sleep(100);
  }
  stop(process);
  throw new Error(`ssh fixture did not report READY; stdout=${stdout.slice(-200)} stderr=${stderr.slice(-500)}`);
}

async function startServer(dataDir) {
  const goos = { darwin: "darwin", linux: "linux", win32: "windows" }[globalThis.process.platform];
  const goarch = { arm64: "arm64", x64: "amd64" }[globalThis.process.arch];
  const binary = path.join(ROOT, "target/go-build/nexterm-server-browser");
  if (!fs.existsSync(binary)) {
    const built = spawnSync(globalThis.process.execPath, ["scripts/build.mjs", "server", "--release", `--os=${goos}`, `--arch=${goarch}`, `--out=${binary}`], { cwd: ROOT, stdio: "inherit" });
    if (built.status !== 0) throw new Error("Go server build for host-key acceptance failed");
  }
  const port = await freePort();
  const log = fs.openSync(path.join(OUT, "server.log"), "a");
  const process = spawn(binary, ["--listen", `127.0.0.1:${port}`, "--data-dir", dataDir], {
    cwd: ROOT,
    env: { ...globalThis.process.env, NEXTERM_MASTER_KEY: "host-key-acceptance-master" },
    stdio: ["ignore", log, log],
  });
  await waitHttp(`http://127.0.0.1:${port}/healthz`, process);
  return { process, port, origin: `http://127.0.0.1:${port}` };
}

const dialogText = `document.querySelector('[role="alertdialog"] .nx-modal-body')?.textContent ?? ""`;
const dialogPresent = `Boolean(document.querySelector('[role="alertdialog"]'))`;
const clickDialogButton = (label) => `(() => {
  const btn = [...document.querySelectorAll('[role="alertdialog"] .nx-modal-footer button')].find((b) => b.textContent.trim() === ${JSON.stringify(label)});
  if (!btn) return false;
  btn.click();
  return true;
})()`;
const toastText = `[...document.querySelectorAll(".nx-toasts button")].map((b) => b.textContent).join("\\n")`;
const wsTabCount = `document.querySelectorAll('[role="tablist"][aria-label="工作区"] [role="tab"]').length`;
const VISIBLE_PANEL = ".nx-workspace-main [role='tabpanel']:not(.hidden)";
const rpcSessions = `(async () => (await import('${VITE}/src/ipc/commands.ts')).sessionApi.list())()`;
const rpcAssetConnected = (assetId) => `(async () => {
  const list = await (${rpcSessions});
  const mine = list.filter((s) => s.assetId === ${JSON.stringify(assetId)});
  return mine.length === 1 && mine[0].status === "connected";
})()`;
const rpcKnownFingerprint = (port) => `(async () => {
  const { assetApi } = await import('${VITE}/src/ipc/commands.ts');
  const list = await assetApi.knownHostList();
  const hit = list.find((k) => k.host === "127.0.0.1" && k.port === ${port});
  return hit ? hit.fingerprint : null;
})()`;
const rpcLayoutHasTerminal = `(async () => {
  const { layoutApi } = await import('${VITE}/src/ipc/commands.ts');
  const dto = await layoutApi.get();
  return dto.data != null && JSON.stringify(dto.data).includes('"tabId":"');
})()`;

async function dblclickAsset(page, name) {
  await page.waitFor(
    `[...document.querySelectorAll('[role="treeitem"]')].some((el) => el.textContent.includes(${JSON.stringify(name)}))`,
    15_000,
  );
  const ok = await page.evaluate(`(() => {
    const row = [...document.querySelectorAll('[role="treeitem"]')].find((el) => el.textContent.includes(${JSON.stringify(name)}));
    if (!row) return false;
    row.dispatchEvent(new MouseEvent("dblclick", { bubbles: true, cancelable: true }));
    return true;
  })()`);
  if (!ok) throw new Error(`asset row not found: ${name}`);
}

async function clickRailNewTerminal(page) {
  const ok = await page.evaluate(`(() => {
    const el = document.querySelector('.nx-rail button[aria-label="新建终端"]');
    if (!el) return false;
    el.click();
    return true;
  })()`);
  if (!ok) throw new Error("rail 新建终端 not found");
}

async function waitLayoutSaved(page) {
  await page.waitFor(rpcLayoutHasTerminal, 15_000);
}

async function restartWithNewHostKey(state) {
  stop(state.ssh.process);
  fs.rmSync(state.keyFile, { force: true });
  state.ssh = await startSSHFixture(state.sshPort, state.keyFile);
  stop(state.server.process);
  state.server = await startServer(state.dataDir);
}

async function screenshot(page, name) {
  const shot = await page.send("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(path.join(OUT, name), Buffer.from(shot.data, "base64"));
  return name;
}


async function seedAsset(page, serverOrigin, sshPort) {
  await page.navigate(`${serverOrigin}/healthz`);
  await page.evaluate(`window.__NEXTERM_TRANSPORT__ = 'web'; true`);
  return page.evaluate(`(async () => {
    const commands = await import('${VITE}/src/ipc/commands.ts');
    const vault = await commands.vaultApi.status();
    if (!vault.initialized || !vault.unlocked) throw new Error('vault not ready: ' + JSON.stringify(vault));
    const credential = await commands.vaultApi.setCredential('acc-ssh', 'password', ${JSON.stringify(SSH_PASS)});
    const asset = await commands.assetApi.create({
      kind: 'ssh',
      name: 'acc-ssh',
      host: '127.0.0.1',
      port: ${sshPort},
      username: ${JSON.stringify(SSH_USER)},
      authKind: 'password',
      credId: credential.id,
    });
    return { assetId: asset.id, credId: credential.id };
  })()`);
}

async function openApp(page, serverOrigin) {
  await page.navigate(`${VITE}/?api=${encodeURIComponent(serverOrigin)}`);
  await page.waitFor(`Boolean(document.querySelector('.nx-rail'))`);
}

async function hostKeyAcceptance(page, state) {
  await pass("hostkey-pending-cancel", async () => {
    await openApp(page, state.server.origin);
    await dblclickAsset(page, "acc-ssh");
    await page.waitFor(dialogPresent);
    const text = await page.evaluate(dialogText);
    assert.ok(text.includes("首次连接 127.0.0.1:"), `pending dialog must introduce the first connect: ${text}`);
    assert.ok(text.includes(state.ssh.fingerprint), `pending dialog must show the presented fingerprint: ${text}`);
    const shot = await screenshot(page, "hostkey-pending.png");

    assert.equal(await page.evaluate(clickDialogButton("取消")), true);
    await page.waitFor(`!document.querySelector('[role="alertdialog"]')`);
    await page.waitFor(`${toastText}.includes("已取消连接")`);
    assert.equal(await page.evaluate(wsTabCount), 0, "cancel must not create a workspace");
    const sessions = await page.evaluate(`(async () => (await import('${VITE}/src/ipc/commands.ts')).sessionApi.list())()`);
    assert.equal(sessions.length, 0, "cancel must not create a session");
    return { evidence: { dialog: text.split("\n")[0], screenshot: shot } };
  });

  await pass("hostkey-pending-accept", async () => {
    await dblclickAsset(page, "acc-ssh");
    await page.waitFor(dialogPresent);
    assert.equal(await page.evaluate(clickDialogButton("确定")), true);
    await page.waitFor(`${wsTabCount} === 1`);
    await page.waitFor(`Boolean(document.querySelector('.xterm'))`);
    const shot = await screenshot(page, "hostkey-connected.png");
    await page.waitFor(rpcAssetConnected(state.assetId), 20_000);
    return { evidence: { screenshot: shot } };
  });

  await pass("hostkey-changed-warn", async () => {
    await waitLayoutSaved(page);
    await restartWithNewHostKey(state);
    await openApp(page, state.server.origin);
    await page.waitFor(`${wsTabCount} >= 1`, 15_000);

    await clickRailNewTerminal(page);
    await page.waitFor(dialogPresent);
    const text = await page.evaluate(dialogText);
    const knownFp = await page.evaluate(rpcKnownFingerprint(state.sshPort));
    assert.ok(text.includes("主机密钥已变更 127.0.0.1:"), `changed dialog must warn: ${text}`);
    assert.ok(knownFp && text.includes(knownFp), `changed dialog must show the previous fingerprint ${knownFp}: ${text}`);
    assert.ok(text.includes(state.ssh.fingerprint), `changed dialog must show the new fingerprint: ${text}`);
    const shot = await screenshot(page, "hostkey-changed.png");

    assert.equal(await page.evaluate(clickDialogButton("取消")), true);
    await page.waitFor(`!document.querySelector('[role="alertdialog"]')`);
    await page.waitFor(`${toastText}.includes("已取消重连")`);
    let sessions = await page.evaluate(rpcSessions);
    const assetSessions = sessions.filter((s) => s.assetId === state.assetId);
    assert.equal(assetSessions.length, 0, `cancel must not reconnect the asset: ${JSON.stringify(sessions)}`);

    await clickRailNewTerminal(page);
    await page.waitFor(dialogPresent);
    assert.equal(await page.evaluate(clickDialogButton("确定")), true);
    await page.waitFor(rpcAssetConnected(state.assetId), 20_000);
    return { evidence: { dialog: text.split("\n")[0], screenshot: shot } };
  });

  await pass("hostkey-no-auto-trust-after-restart", async () => {
    await waitLayoutSaved(page);
    await restartWithNewHostKey(state);
    await openApp(page, state.server.origin);
    await page.waitFor(`${wsTabCount} >= 1`, 15_000);

    const sessionsBefore = await page.evaluate(rpcSessions);
    const assetSessionsBefore = sessionsBefore.filter((s) => s.assetId === state.assetId);
    assert.equal(assetSessionsBefore.length, 0, `changed-key host must not auto-reconnect after restart: ${JSON.stringify(sessionsBefore)}`);

    await clickRailNewTerminal(page);
    await page.waitFor(dialogPresent);
    const text = await page.evaluate(dialogText);
    const knownFp = await page.evaluate(rpcKnownFingerprint(state.sshPort));
    assert.ok(text.includes("主机密钥已变更 127.0.0.1:"), `restored layout must still warn about the changed key: ${text}`);
    assert.ok(knownFp && text.includes(knownFp), `dialog must show the previous fingerprint ${knownFp}: ${text}`);
    assert.ok(text.includes(state.ssh.fingerprint), `dialog must show the new fingerprint: ${text}`);
    const shot = await screenshot(page, "hostkey-restart-guard.png");

    assert.equal(await page.evaluate(clickDialogButton("确定")), true);
    await page.waitFor(rpcAssetConnected(state.assetId), 20_000);
    return { evidence: { dialog: text.split("\n")[0], screenshot: shot } };
  });

  await pass("hostkey-reconnect-menu-fallback", async () => {
    await page.evaluate(`(async () => {
      const commands = await import('${VITE}/src/ipc/commands.ts');
      const sessions = await commands.sessionApi.list();
      const mine = sessions.find((s) => s.assetId === ${JSON.stringify(state.assetId)});
      if (!mine) throw new Error("no asset session to disconnect");
      await commands.sessionApi.disconnect(mine.id);
      return true;
    })()`);
    await page.waitFor(`document.querySelector("${VISIBLE_PANEL}")?.textContent.includes("已断开") ?? false`, 15_000);
    let menuOpen = false;
    for (let attempt = 0; attempt < 3 && !menuOpen; attempt++) {
      menuOpen = await page.evaluate(`(() => {
        const el = document.querySelector(${JSON.stringify(`${VISIBLE_PANEL} .nx-terminal-body .relative`)});
        if (!el) return false;
        const r = el.getBoundingClientRect();
        el.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: r.left + r.width / 2, clientY: r.top + r.height / 2 }));
        return true;
      })()`);
      if (!menuOpen) {
        await sleep(400);
        continue;
      }
      try {
        await page.waitFor(`Boolean(document.querySelector(".nx-menu"))`, 3_000);
      } catch {
        menuOpen = false;
        await sleep(400);
      }
    }
    if (!menuOpen) throw new Error("terminal context menu did not open");
    const clicked = await page.evaluate(`(() => {
      const item = [...document.querySelectorAll(".nx-menu .nx-menu-item")].find((b) => b.textContent.includes("重新连接"));
      if (!item) return false;
      item.click();
      return true;
    })()`);
    assert.equal(clicked, true, "menu item 重新连接 missing");
    await page.waitFor(`${toastText}.includes("正在重连")`, 15_000);
    assert.equal(await page.evaluate(dialogPresent), false, "probe-less backend must fall back without a dialog");
    return { evidence: { note: "session_probe_host_key lands with M157; until the rebase the menu reconnect must degrade to the plain reconnect" } };
  });
}

let vite;
let chrome;
let page;
const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-host-key-server-"));
const keyFile = path.join(OUT, "ssh_host_key");
const state = { dataDir, keyFile, sshPort: 0, ssh: null, server: null, assetId: "" };
try {
  state.sshPort = await freePort();
  await buildSSHFixture();
  [vite, chrome] = await Promise.all([startVite({ root: ROOT, port: VITE_PORT }), startChrome()]);
  state.ssh = await startSSHFixture(state.sshPort, keyFile);
  state.server = await startServer(dataDir);

  const seeder = await newPage(chrome, () => {});
  const seeded = await seedAsset(seeder, state.server.origin, state.sshPort);
  state.assetId = seeded.assetId;
  seeder.close();
  console.warn(`seeded asset ${seeded.assetId} against 127.0.0.1:${state.sshPort}`);

  page = await newPage(chrome, (error) => pageErrors.push(error));
  await page.send("Page.addScriptToEvaluateOnNewDocument", { source: `window.__NEXTERM_TRANSPORT__ = 'web';` });
  await hostKeyAcceptance(page, state);
} catch (error) {
  harnessErrors.push(String(error?.stack || error));
} finally {
  if (page) page.close();
  stop(chrome?.process);
  await vite?.stop();
  stop(state.server?.process);
  stop(state.ssh?.process);
}

const checks = [...results.values()];
const failed = checks.filter((check) => check.status !== "passed");
const unexpectedPageErrors = pageErrors.filter((e) => !e.includes("syncScrollArea") && !e.includes("sftp") && !e.includes("SFTP"));
const report = {
  schema_version: 1,
  status: failed.length || harnessErrors.length || unexpectedPageErrors.length ? "failed" : "passed",
  browser: chrome?.version || { status: "unavailable" },
  execution: {
    real_browser: true,
    headless: true,
    jsdom: false,
    note: "真实 Chromium + 真实 nexterm-server + 一次性 Go SSH fixture：pending/changed 指纹弹窗的取消/接受、重启后不得自动信任新密钥、菜单重连在 probe 命令缺席时的回退；失效终端弹层路径被 durable 恢复拦截（见 findings，bad_param 而非 not_found），该路径由 vitest 覆盖",
  },
  checks,
  harness_errors: harnessErrors,
  page_errors: pageErrors.slice(-20),
  unexpected_page_errors: unexpectedPageErrors,
};
fs.writeFileSync(path.join(OUT, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
console.warn(`host-key acceptance: ${checks.filter((check) => check.status === "passed").length}/${checks.length} checks passed; report=${path.join(OUT, "report.json")}`);
if (unexpectedPageErrors.length) {
  console.warn(`unexpected page errors: ${unexpectedPageErrors.length}; first=${unexpectedPageErrors[0]}`);
}
if (failed.length || harnessErrors.length || unexpectedPageErrors.length) process.exit(1);
process.exit(0);
