#!/usr/bin/env node
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const version = process.env.NEXTERM_RELEASE_VERSION || JSON.parse(fs.readFileSync(path.join(ROOT, "wails.json"), "utf8")).info.version;
const packageNames = [
  `NexTerm_${version}_x64-setup.exe`,
  `NexTerm_${version}_arm64-setup.exe`,
  `NexTerm_${version}_aarch64.dmg`,
  `NexTerm_${version}_x86_64.dmg`,
  ...["amd64", "arm64"].flatMap((arch) => [
    `NexTerm-desktop_${version}_linux_${arch}.deb`,
    `NexTerm-server_${version}_linux_${arch}.tar.gz`,
  ]),
];
const evidenceNames = ["SHA256SUMS"];
const expectedNames = [...packageNames, ...evidenceNames];
const retriableStatuses = new Set([429, 500, 502, 503, 504]);
const requestTimeoutMs = 600_000;

function parseArgs(argv) {
  const options = { concurrency: 8, maxAttempts: 3, retryBaseMs: 1000, notesFile: null, directory: null };
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    if (arg === "--concurrency") options.concurrency = Number(argv[index += 1]);
    else if (arg === "--max-attempts") options.maxAttempts = Number(argv[index += 1]);
    else if (arg === "--retry-base-ms") options.retryBaseMs = Number(argv[index += 1]);
    else if (arg === "--notes-file") options.notesFile = argv[index += 1];
    else if (arg.startsWith("--")) throw new Error(`unknown option: ${arg}`);
    else if (options.directory === null) options.directory = arg;
    else throw new Error(`unexpected argument: ${arg}`);
  }
  if (!Number.isInteger(options.concurrency) || options.concurrency < 1) throw new Error("--concurrency must be a positive integer");
  if (!Number.isInteger(options.maxAttempts) || options.maxAttempts < 1) throw new Error("--max-attempts must be a positive integer");
  if (!Number.isFinite(options.retryBaseMs) || options.retryBaseMs < 0) throw new Error("--retry-base-ms must be a non-negative number");
  return options;
}

function requireEnv(name) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}

const options = parseArgs(process.argv.slice(2));
const directory = path.resolve(ROOT, options.directory || "candidate");
const tag = requireEnv("GITHUB_REF_NAME");
const prerelease = tag === `v${version}` && version.includes("-");
const repository = requireEnv("GITHUB_REPOSITORY");
const token = process.env.GH_TOKEN || process.env.GITHUB_TOKEN;
if (!token) throw new Error("GH_TOKEN or GITHUB_TOKEN is required");
const apiBase = (process.env.GITHUB_API_URL || "https://api.github.com").replace(/\/+$/, "");

if (!fs.existsSync(directory) || !fs.statSync(directory).isDirectory()) {
  console.error(`release-upload: candidate directory does not exist: ${directory}`);
  process.exit(1);
}
const missing = expectedNames.filter((name) => !fs.existsSync(path.join(directory, name)));
const unexpected = fs.readdirSync(directory).filter((name) => /\.(?:exe|dmg|deb|tar\.gz)$/.test(name) && !packageNames.includes(name));
if (missing.length || unexpected.length) {
  if (missing.length) console.error(`release-upload: missing candidate files: ${missing.join(", ")}`);
  if (unexpected.length) console.error(`release-upload: unexpected candidate files: ${unexpected.join(", ")}`);
  process.exit(1);
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function send(url, init = {}) {
  return fetch(url, {
    ...init,
    headers: {
      Authorization: `Bearer ${token}`,
      Accept: "application/vnd.github+json",
      "X-GitHub-Api-Version": "2022-11-28",
      ...(init.headers || {}),
    },
    signal: AbortSignal.timeout(requestTimeoutMs),
  });
}

function backoffDelay(attempt, retryAfter) {
  if (retryAfter !== null && retryAfter !== undefined && retryAfter !== "") {
    const retryAfterMs = Number(retryAfter);
    if (Number.isFinite(retryAfterMs) && retryAfterMs >= 0) return retryAfterMs * 1000;
  }
  return options.retryBaseMs * 2 ** (attempt - 1);
}

async function sendWithRetry(url, init = {}) {
  for (let attempt = 1; ; attempt += 1) {
    let response;
    try {
      response = await send(url, init);
    } catch (error) {
      if (attempt >= options.maxAttempts) throw new Error(`${init.method || "GET"} ${url}: ${error.message} (attempts=${attempt})`);
      await sleep(backoffDelay(attempt, null));
      continue;
    }
    if (retriableStatuses.has(response.status) && attempt < options.maxAttempts) {
      await sleep(backoffDelay(attempt, response.headers.get("retry-after")));
      continue;
    }
    return { response, attempts: attempt };
  }
}

async function listAssets(releaseId) {
  const { response } = await sendWithRetry(`${apiBase}/repos/${repository}/releases/${releaseId}/assets?per_page=100`);
  if (response.status !== 200) throw new Error(`list assets for release ${releaseId}: HTTP ${response.status}: ${(await response.text()).slice(0, 300)}`);
  return response.json();
}

async function deleteAsset(assetId) {
  const { response } = await sendWithRetry(`${apiBase}/repos/${repository}/releases/assets/${assetId}`, { method: "DELETE" });
  if (response.status !== 204 && response.status !== 404) throw new Error(`delete asset ${assetId}: HTTP ${response.status}: ${(await response.text()).slice(0, 300)}`);
}

async function findReleaseByTag(tagName) {
  const perPage = 100;
  for (let page = 1; page <= 10; page += 1) {
    const { response } = await sendWithRetry(`${apiBase}/repos/${repository}/releases?per_page=${perPage}&page=${page}`);
    if (response.status !== 200) throw new Error(`list releases page ${page}: HTTP ${response.status}: ${(await response.text()).slice(0, 300)}`);
    const releases = await response.json();
    const match = releases.find((item) => item.tag_name === tagName);
    if (match) return match;
    if (releases.length < perPage) return null;
  }
  throw new Error(`release ${tagName} not found in the first 1000 listed releases`);
}

async function runPool(items, limit, worker) {
  const results = new Array(items.length);
  let next = 0;
  async function lane() {
    while (next < items.length) {
      const index = next;
      next += 1;
      results[index] = await worker(items[index]);
    }
  }
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, lane));
  return results;
}

const startedAt = performance.now();
const resolveStarted = performance.now();
let release = await findReleaseByTag(tag);
if (release) {
  console.warn(`release-upload: resolved existing release ${release.id} for ${tag} (draft=${release.draft}, prerelease=${release.prerelease})`);
} else {
  const notesFile = path.resolve(ROOT, options.notesFile || path.join(options.directory || "candidate", "release-body.md"));
  if (!fs.existsSync(notesFile)) {
    console.error(`release-upload: notes file does not exist: ${notesFile}`);
    process.exit(1);
  }
  const created = await sendWithRetry(`${apiBase}/repos/${repository}/releases`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      tag_name: tag,
      name: `NexTerm ${tag}`,
      body: fs.readFileSync(notesFile, "utf8"),
      draft: true,
      prerelease,
    }),
  });
  if (created.response.status !== 201) {
    console.error(`release-upload: create release ${tag}: HTTP ${created.response.status}: ${(await created.response.text()).slice(0, 300)}`);
    process.exit(1);
  }
  release = await created.response.json();
  console.warn(`release-upload: created draft release ${release.id} for ${tag} (attempts=${created.attempts})`);
}
const resolveSeconds = (performance.now() - resolveStarted) / 1000;
const uploadBase = release.upload_url.replace(/\{.*$/, "");

const clobberStarted = performance.now();
const stale = (await listAssets(release.id)).filter((asset) => expectedNames.includes(asset.name));
await runPool(stale, options.concurrency, async (asset) => {
  await deleteAsset(asset.id);
  console.warn(`release-upload: deleted stale asset ${asset.name} (${asset.id})`);
});
const clobberSeconds = (performance.now() - clobberStarted) / 1000;

const uploadStarted = performance.now();
const results = await runPool(expectedNames, options.concurrency, async (name) => {
  const fileStarted = performance.now();
  let attempts = 0;
  try {
    const bytes = fs.readFileSync(path.join(directory, name));
    const target = `${uploadBase}?name=${encodeURIComponent(name)}`;
    let recoveredConflict = false;
    for (;;) {
      const { response, attempts: used } = await sendWithRetry(target, {
        method: "POST",
        headers: { "Content-Type": "application/octet-stream" },
        body: bytes,
      });
      attempts += used;
      if (response.status === 201) {
        const seconds = (performance.now() - fileStarted) / 1000;
        console.warn(`release-upload: uploaded ${name} bytes=${bytes.length} attempts=${attempts} seconds=${seconds.toFixed(2)}`);
        return { name, ok: true, bytes: bytes.length, attempts, seconds };
      }
      if (response.status === 422 && !recoveredConflict) {
        recoveredConflict = true;
        const conflict = (await listAssets(release.id)).find((asset) => asset.name === name);
        if (conflict) {
          await deleteAsset(conflict.id);
          console.warn(`release-upload: deleted conflicting asset ${name} (${conflict.id}) after 422`);
        }
        continue;
      }
      const seconds = (performance.now() - fileStarted) / 1000;
      const detail = `HTTP ${response.status}: ${(await response.text()).slice(0, 300)}`;
      console.error(`release-upload: failed ${name} attempts=${attempts} seconds=${seconds.toFixed(2)}: ${detail}`);
      return { name, ok: false, attempts, seconds, error: detail };
    }
  } catch (error) {
    const seconds = (performance.now() - fileStarted) / 1000;
    console.error(`release-upload: failed ${name} attempts=${attempts} seconds=${seconds.toFixed(2)}: ${error.message}`);
    return { name, ok: false, attempts, seconds, error: error.message };
  }
});
const uploadSeconds = (performance.now() - uploadStarted) / 1000;

const failed = results.filter((result) => !result.ok);
if (failed.length) {
  console.error(`release-upload: ${failed.length}/${expectedNames.length} uploads failed: ${failed.map((result) => result.name).join(", ")}`);
  process.stdout.write(`${JSON.stringify({ tag, release_id: release.id, concurrency: options.concurrency, failed }, null, 2)}\n`);
  process.exit(1);
}

const verifyStarted = performance.now();
const uploadedNames = new Set((await listAssets(release.id)).map((asset) => asset.name));
const stillMissing = expectedNames.filter((name) => !uploadedNames.has(name));
if (stillMissing.length) {
  console.error(`release-upload: release ${release.id} is missing uploaded assets: ${stillMissing.join(", ")}`);
  process.exit(1);
}
const finalRelease = await (await sendWithRetry(`${apiBase}/repos/${repository}/releases/${release.id}`)).response.json();
const verifySeconds = (performance.now() - verifyStarted) / 1000;
const totalSeconds = (performance.now() - startedAt) / 1000;

console.warn(`release-upload: uploaded ${expectedNames.length} assets in ${uploadSeconds.toFixed(2)}s (resolve ${resolveSeconds.toFixed(2)}s, clobber ${clobberSeconds.toFixed(2)}s, verify ${verifySeconds.toFixed(2)}s, total ${totalSeconds.toFixed(2)}s)`);
process.stdout.write(`${JSON.stringify({
  tag,
  release_id: release.id,
  draft: finalRelease.draft,
  prerelease: finalRelease.prerelease,
  concurrency: options.concurrency,
  deleted_assets: stale.length,
  files: results.map(({ name, bytes, attempts, seconds }) => ({ name, bytes, attempts, seconds: Number(seconds.toFixed(3)) })),
  failed: [],
  seconds: {
    resolve: Number(resolveSeconds.toFixed(3)),
    clobber: Number(clobberSeconds.toFixed(3)),
    upload: Number(uploadSeconds.toFixed(3)),
    verify: Number(verifySeconds.toFixed(3)),
    total: Number(totalSeconds.toFixed(3)),
  },
}, null, 2)}\n`);
