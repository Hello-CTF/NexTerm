#!/usr/bin/env node
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { startServer, stop } from "./browser-acceptance.mjs";

function tempDir(t) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-acceptance-harness-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  return directory;
}

function fakeServer(t, script) {
  const file = path.join(tempDir(t), "nexterm-server.cjs");
  fs.writeFileSync(file, `#!/usr/bin/env node\n${script}\n`, { mode: 0o755 });
  return file;
}

function useFakeServer(t, file) {
  const previous = process.env.NEXTERM_SERVER_BIN;
  process.env.NEXTERM_SERVER_BIN = file;
  t.after(() => {
    if (previous === undefined) delete process.env.NEXTERM_SERVER_BIN;
    else process.env.NEXTERM_SERVER_BIN = previous;
  });
}

const HEALTHY_FAKE = `
const fs = require("node:fs");
const http = require("node:http");
const args = process.argv.slice(2);
fs.writeFileSync(process.env.NX_FAKE_ARGV_FILE, JSON.stringify(args));
const listen = args[args.indexOf("--listen") + 1] || "";
const port = Number(listen.split(":")[1]);
const server = http.createServer((req, res) => {
  if (req.url === "/healthz") { res.writeHead(200); res.end("ok"); return; }
  res.writeHead(404); res.end();
});
server.listen(port, "127.0.0.1");
process.on("SIGTERM", () => process.exit(0));
`;

test("startServer launches --auth=loopback on a loopback listener and carries no token", async (t) => {
  const directory = tempDir(t);
  const argvFile = path.join(directory, "argv.json");
  process.env.NX_FAKE_ARGV_FILE = argvFile;
  t.after(() => { delete process.env.NX_FAKE_ARGV_FILE; });
  useFakeServer(t, fakeServer(t, HEALTHY_FAKE));
  const server = await startServer();
  t.after(() => stop(server.process));
  assert.equal(server.origin, `http://127.0.0.1:${server.port}`);
  assert.equal("token" in server, false, "loopback startup must not produce a static sync token");
  const argv = JSON.parse(fs.readFileSync(argvFile, "utf8"));
  assert.ok(argv.includes("--auth=loopback"), `spawn must request loopback auth: ${JSON.stringify(argv)}`);
  assert.equal(argv[argv.indexOf("--listen") + 1], `127.0.0.1:${server.port}`);
  assert.equal(argv[argv.indexOf("--data-dir") + 1], server.data);
  const health = await fetch(`${server.origin}/healthz`);
  assert.equal(health.status, 200, "readiness probe must succeed without any token");
  stop(server.process);
  await new Promise((resolve) => server.process.once("exit", resolve));
  assert.equal(server.process.exitCode, 0, "stop must terminate the spawned server via SIGTERM");
});

test("startServer rejects when the server process exits before healthz", async (t) => {
  useFakeServer(t, fakeServer(t, "process.exit(3);"));
  await assert.rejects(startServer(), /process exited 3/);
});

test("browser acceptance keeps the removed token CLI and static token out", () => {
  const source = fs.readFileSync(new URL("./browser-acceptance.mjs", import.meta.url), "utf8");
  assert.ok(!source.includes("server-token.mjs"), "browser-acceptance.mjs must not import the removed token helper");
  assert.ok(!source.includes("setServerToken"), "browser acceptance must run without a static sync token");
  assert.equal(fs.existsSync(new URL("./server-token.mjs", import.meta.url)), false, "server-token.mjs must stay deleted while nothing references it");
});
