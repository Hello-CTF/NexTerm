#!/usr/bin/env node
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import fs from "node:fs";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const SCRIPT = path.join(ROOT, ".github", "scripts", "release-upload.mjs");
const VERSION = JSON.parse(fs.readFileSync(path.join(ROOT, "wails.json"), "utf8")).info.version;
const PACKAGE_NAMES = [
  `NexTerm_${VERSION}_x64-setup.exe`,
  `NexTerm_${VERSION}_arm64-setup.exe`,
  `NexTerm_${VERSION}_aarch64.dmg`,
  `NexTerm_${VERSION}_x86_64.dmg`,
  ...["amd64", "arm64"].flatMap((arch) => [
    `NexTerm-desktop_${VERSION}_linux_${arch}.tar.gz`,
    `NexTerm-server_${VERSION}_linux_${arch}.tar.gz`,
  ]),
];
const EVIDENCE_NAMES = ["SHA256SUMS", "release-evidence.json", "real-target-gaps.json"];
const EXPECTED_NAMES = [...PACKAGE_NAMES, ...EVIDENCE_NAMES];
const RELEASE_ID = 4242;

function makeCandidate(t, { omit = [], extra = {} } = {}) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-release-upload-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  for (const name of EXPECTED_NAMES) {
    if (omit.includes(name)) continue;
    fs.writeFileSync(path.join(directory, name), `stub ${name}\n`);
  }
  for (const [name, content] of Object.entries(extra)) fs.writeFileSync(path.join(directory, name), content);
  fs.writeFileSync(path.join(directory, "release-body.md"), "stub notes\n");
  return directory;
}

function startStub(t, overrides = {}) {
  const state = {
    requests: [],
    assetInFlight: 0,
    assetPeak: 0,
    assetAttempts: new Map(),
    deletedAssetIds: [],
    releaseCreated: null,
    currentAssets: (overrides.existingAssets || []).map((asset) => ({ ...asset })),
  };
  let nextAssetId = 9000;
  const behavior = async (req, respond, body) => {
    const url = new URL(req.url, "http://127.0.0.1");
    const releaseDocument = () => ({
      id: RELEASE_ID,
      draft: true,
      prerelease: false,
      upload_url: `${respond.base}/upload/${RELEASE_ID}/assets{?name,label}`,
    });
    if (req.method === "GET" && url.pathname === "/repos/stub/repo/releases/tags/v9.9.9-stub") {
      if (overrides.existingRelease === false) return respond(404, { message: "Not Found" });
      return respond(200, releaseDocument());
    }
    if (req.method === "POST" && url.pathname === "/repos/stub/repo/releases") {
      state.releaseCreated = JSON.parse(body.toString("utf8"));
      return respond(201, releaseDocument());
    }
    if (req.method === "POST" && url.pathname === `/upload/${RELEASE_ID}/assets`) {
      const name = url.searchParams.get("name");
      const attempt = (state.assetAttempts.get(name) ?? 0) + 1;
      state.assetAttempts.set(name, attempt);
      const custom = await overrides.assetResponse?.(name, attempt, state);
      const record = () => {
        if (!overrides.dropFromListing?.includes(name)) state.currentAssets.push({ id: nextAssetId += 1, name });
      };
      if (custom) {
        const status = typeof custom === "number" ? custom : custom.status;
        const payload = typeof custom === "number" ? { message: `stub ${custom}` } : custom.payload;
        const headers = typeof custom === "number" ? {} : custom.headers;
        if (status === 201) record();
        return respond(status, payload, headers);
      }
      record();
      return respond(201, { id: nextAssetId, name, state: "uploaded" });
    }
    if (req.method === "GET" && url.pathname === `/repos/stub/repo/releases/${RELEASE_ID}/assets`) {
      return respond(200, state.currentAssets);
    }
    if (req.method === "DELETE" && url.pathname.startsWith("/repos/stub/repo/releases/assets/")) {
      const assetId = Number(url.pathname.split("/").pop());
      state.deletedAssetIds.push(assetId);
      state.currentAssets = state.currentAssets.filter((asset) => asset.id !== assetId);
      return respond(204);
    }
    if (req.method === "GET" && url.pathname === `/repos/stub/repo/releases/${RELEASE_ID}`) {
      return respond(200, releaseDocument());
    }
    return respond(500, { error: `unrouted ${req.method} ${url.pathname}` });
  };
  const server = http.createServer((req, res) => {
    const chunks = [];
    req.on("data", (chunk) => chunks.push(chunk));
    req.on("end", () => {
      const body = Buffer.concat(chunks);
      const isAssetPost = req.method === "POST" && req.url.startsWith("/upload/");
      if (isAssetPost) {
        state.assetInFlight += 1;
        state.assetPeak = Math.max(state.assetPeak, state.assetInFlight);
      }
      state.requests.push({ method: req.method, url: req.url, at: Date.now(), body });
      let responded = false;
      const respond = (status, payload, headers = {}) => {
        if (responded) return;
        responded = true;
        if (isAssetPost) state.assetInFlight -= 1;
        res.writeHead(status, { "Content-Type": "application/json", ...headers });
        res.end(payload === undefined ? "" : JSON.stringify(payload));
      };
      respond.base = `http://127.0.0.1:${server.address().port}`;
      behavior(req, respond, body).catch((error) => respond(500, { error: error.message }));
    });
  });
  return new Promise((resolve) => {
    server.listen(0, "127.0.0.1", () => {
      t.after(() => server.close());
      resolve({ base: `http://127.0.0.1:${server.address().port}`, state });
    });
  });
}

function runUploader(stub, directory, args = []) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [SCRIPT, directory, "--notes-file", path.join(directory, "release-body.md"), ...args], {
      cwd: ROOT,
      env: {
        ...process.env,
        GITHUB_API_URL: stub.base,
        GITHUB_REPOSITORY: "stub/repo",
        GITHUB_REF_NAME: "v9.9.9-stub",
        GH_TOKEN: "stub-token",
      },
    });
    let stdout = "";
    let stderr = "";
    const timer = setTimeout(() => {
      child.kill("SIGKILL");
      reject(new Error(`uploader timed out: ${stderr.slice(-400)}`));
    }, 120_000);
    child.stdout.on("data", (chunk) => { stdout += chunk; });
    child.stderr.on("data", (chunk) => { stderr += chunk; });
    child.on("error", (error) => {
      clearTimeout(timer);
      reject(error);
    });
    child.on("close", (status, signal) => {
      clearTimeout(timer);
      resolve({ status, signal, stdout, stderr });
    });
  });
}

function summaryOf(result) {
  return JSON.parse(result.stdout);
}

test("gates a missing package before any request", async (t) => {
  const stub = await startStub(t);
  const directory = makeCandidate(t, { omit: [PACKAGE_NAMES[0]] });
  const result = await runUploader(stub, directory);
  assert.notEqual(result.status, 0);
  assert.equal(stub.state.requests.length, 0);
  assert.match(result.stderr, new RegExp(PACKAGE_NAMES[0].replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  assert.match(result.stderr, /missing candidate files/);
});

test("gates an unexpected package before any request", async (t) => {
  const stub = await startStub(t);
  const directory = makeCandidate(t, { extra: { "NexTerm_9.9.9_evil.exe": "stub\n" } });
  const result = await runUploader(stub, directory);
  assert.notEqual(result.status, 0);
  assert.equal(stub.state.requests.length, 0);
  assert.match(result.stderr, /unexpected candidate files/);
  assert.match(result.stderr, /NexTerm_9\.9\.9_evil\.exe/);
});

test("creates the draft release and uploads all 11 assets", async (t) => {
  const stub = await startStub(t, { existingRelease: false });
  const directory = makeCandidate(t);
  const result = await runUploader(stub, directory);
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(stub.state.releaseCreated, {
    tag_name: "v9.9.9-stub",
    name: "NexTerm v9.9.9-stub",
    body: "stub notes\n",
    draft: true,
    prerelease: false,
  });
  const uploaded = [...stub.state.assetAttempts.keys()].sort();
  assert.deepEqual(uploaded, [...EXPECTED_NAMES].sort());
  const sample = stub.state.requests.find((entry) => entry.url.includes("name=SHA256SUMS"));
  assert.equal(sample.method, "POST");
  assert.equal(sample.body.toString("utf8"), "stub SHA256SUMS\n");
  const summary = summaryOf(result);
  assert.equal(summary.tag, "v9.9.9-stub");
  assert.equal(summary.release_id, RELEASE_ID);
  assert.equal(summary.draft, true);
  assert.equal(summary.prerelease, false);
  assert.equal(summary.deleted_assets, 0);
  assert.deepEqual(summary.failed, []);
  assert.equal(summary.files.length, 11);
  for (const file of summary.files) {
    assert.equal(file.attempts, 1, file.name);
    assert.ok(file.bytes > 0, file.name);
    assert.ok(file.seconds > 0, file.name);
  }
  const uploadedLines = result.stderr.split("\n").filter((line) => line.includes("release-upload: uploaded ") && line.includes(" bytes="));
  assert.equal(uploadedLines.length, 11);
});

test("replaces same-name assets on the existing draft without creating", async (t) => {
  const existingAssets = EXPECTED_NAMES.map((name, index) => ({ id: 100 + index, name }));
  const stub = await startStub(t, { existingAssets });
  const directory = makeCandidate(t);
  const result = await runUploader(stub, directory);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(stub.state.releaseCreated, null);
  assert.deepEqual([...stub.state.deletedAssetIds].sort((a, b) => a - b), existingAssets.map((asset) => asset.id));
  assert.equal(stub.state.assetAttempts.size, 11);
  const summary = summaryOf(result);
  assert.equal(summary.deleted_assets, 11);
  assert.equal(summary.draft, true);
});

test("bounds in-flight uploads to the configured concurrency", async (t) => {
  const stub = await startStub(t, {
    assetResponse: () => new Promise((resolve) => setTimeout(() => resolve(201), 150)),
  });
  const directory = makeCandidate(t);
  const result = await runUploader(stub, directory, ["--concurrency", "4"]);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(stub.state.assetPeak, 4);
  assert.equal(stub.state.assetAttempts.size, 11);
  const summary = summaryOf(result);
  assert.equal(summary.concurrency, 4);
});

test("bounded uploads finish measurably faster than sequential", async (t) => {
  const stub = await startStub(t, {
    assetResponse: () => new Promise((resolve) => setTimeout(() => resolve(201), 150)),
  });
  const directory = makeCandidate(t);
  const sequentialStart = Date.now();
  const sequential = await runUploader(stub, directory, ["--concurrency", "1"]);
  const sequentialMs = Date.now() - sequentialStart;
  assert.equal(sequential.status, 0, sequential.stderr);
  const parallelStart = Date.now();
  const parallel = await runUploader(stub, directory, ["--concurrency", "4"]);
  const parallelMs = Date.now() - parallelStart;
  assert.equal(parallel.status, 0, parallel.stderr);
  console.warn(`release-upload stub timings: sequential=${sequentialMs}ms parallel4=${parallelMs}ms ratio=${(parallelMs / sequentialMs).toFixed(3)}`);
  assert.ok(
    parallelMs < sequentialMs * 0.7 && parallelMs < sequentialMs - 400,
    `sequential=${sequentialMs}ms parallel=${parallelMs}ms`,
  );
});

test("retries a transient 503 and succeeds", async (t) => {
  const flaky = PACKAGE_NAMES[0];
  const stub = await startStub(t, {
    assetResponse: (name, attempt) => (name === flaky && attempt === 1 ? 503 : 201),
  });
  const directory = makeCandidate(t);
  const result = await runUploader(stub, directory, ["--retry-base-ms", "200"]);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(stub.state.assetAttempts.get(flaky), 2);
  const attempts = stub.state.requests.filter((entry) => entry.url.includes(`name=${encodeURIComponent(flaky)}`));
  assert.ok(attempts[1].at - attempts[0].at >= 150, `backoff gap=${attempts[1].at - attempts[0].at}ms`);
  const summary = summaryOf(result);
  const file = summary.files.find((entry) => entry.name === flaky);
  assert.equal(file.attempts, 2);
});

test("fails after retry exhaustion and names the failed asset", async (t) => {
  const broken = PACKAGE_NAMES[1];
  const stub = await startStub(t, {
    assetResponse: (name) => (name === broken ? 503 : 201),
  });
  const directory = makeCandidate(t);
  const result = await runUploader(stub, directory, ["--max-attempts", "2", "--retry-base-ms", "10"]);
  assert.notEqual(result.status, 0);
  assert.equal(stub.state.assetAttempts.get(broken), 2);
  assert.equal(stub.state.assetAttempts.size, 11);
  assert.match(result.stderr, new RegExp(`failed ${broken.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}`));
  assert.match(result.stderr, /1\/11 uploads failed/);
  const summary = summaryOf(result);
  assert.equal(summary.failed.length, 1);
  assert.equal(summary.failed[0].name, broken);
  assert.equal(summary.files, undefined);
});

test("does not retry a permanent 400", async (t) => {
  const rejected = PACKAGE_NAMES[2];
  const stub = await startStub(t, {
    assetResponse: (name) => (name === rejected ? 400 : 201),
  });
  const directory = makeCandidate(t);
  const result = await runUploader(stub, directory, ["--max-attempts", "3", "--retry-base-ms", "10"]);
  assert.notEqual(result.status, 0);
  assert.equal(stub.state.assetAttempts.get(rejected), 1);
});

test("recovers from a 422 conflict by deleting the stale asset and retrying", async (t) => {
  const raced = PACKAGE_NAMES[3];
  const stub = await startStub(t, {
    assetResponse: (name, attempt, state) => {
      if (name === raced && attempt === 1) {
        state.currentAssets.push({ id: 777, name });
        return 422;
      }
      return 201;
    },
  });
  const directory = makeCandidate(t);
  const result = await runUploader(stub, directory);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(stub.state.assetAttempts.get(raced), 2);
  const sequence = stub.state.requests
    .map((entry, index) => ({ entry, index }))
    .filter(({ entry }) => entry.url.includes(`name=${encodeURIComponent(raced)}`)
      || (entry.method === "DELETE" && entry.url === "/repos/stub/repo/releases/assets/777")
      || entry.url.startsWith(`/repos/stub/repo/releases/${RELEASE_ID}/assets`));
  const kinds = sequence.map(({ entry }) => `${entry.method} ${entry.url.split("?")[0]}`);
  const conflictIndex = kinds.indexOf(`POST /upload/${RELEASE_ID}/assets`);
  const deleteIndex = kinds.indexOf("DELETE /repos/stub/repo/releases/assets/777");
  const retryIndex = kinds.indexOf(`POST /upload/${RELEASE_ID}/assets`, conflictIndex + 1);
  assert.ok(conflictIndex >= 0 && deleteIndex > conflictIndex && retryIndex > deleteIndex, kinds.join("\n"));
  assert.ok(stub.state.deletedAssetIds.includes(777));
});

test("fails when the final asset listing misses an uploaded file", async (t) => {
  const lost = EVIDENCE_NAMES[0];
  const stub = await startStub(t, { dropFromListing: [lost] });
  const directory = makeCandidate(t);
  const result = await runUploader(stub, directory);
  assert.notEqual(result.status, 0);
  assert.equal(stub.state.assetAttempts.size, 11);
  assert.match(result.stderr, /missing uploaded assets/);
  assert.match(result.stderr, new RegExp(lost.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
});

test("honors Retry-After before retrying", async (t) => {
  const throttled = PACKAGE_NAMES[4];
  const stub = await startStub(t, {
    assetResponse: (name, attempt) => (name === throttled && attempt === 1
      ? { status: 503, headers: { "Retry-After": "1" } }
      : 201),
  });
  const directory = makeCandidate(t);
  const result = await runUploader(stub, directory, ["--retry-base-ms", "10"]);
  assert.equal(result.status, 0, result.stderr);
  const attempts = stub.state.requests.filter((entry) => entry.url.includes(`name=${encodeURIComponent(throttled)}`));
  assert.equal(attempts.length, 2);
  const gapMs = attempts[1].at - attempts[0].at;
  assert.ok(gapMs >= 900, `gap=${gapMs}ms`);
});
