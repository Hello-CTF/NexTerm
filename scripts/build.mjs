#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { resolveMakensis } from "./lib/makensis.mjs";
import { commitLabel, sourceState } from "./lib/mobile.mjs";
import { resolveSpawnSpec } from "./lib/spawn-spec.mjs";
import { parseReleaseVersion, parseSemVer } from "./lib/semver.mjs";
import {
  binaryManifestProblems,
  createBinaryManifest,
  createDistManifest,
  distManifestProblems,
  loadManifest,
  treeHash,
} from "./lib/build-manifest.mjs";

const HELP_TEXT = `#!/usr/bin/env node
/**
 * Go/Wails delivery entry point. Rust/Cargo/Tauri are deliberately not part of
 * this path. Artifact versions come from NEXTERM_RELEASE_VERSION (set from the
 * release tag in CI); the wails.json/package.json versions are 0.0.0 placeholders.
 *
 *   node scripts/build.mjs                         # frontend + host debug desktop
 *   node scripts/build.mjs release                 # reproducible frontend + native package
 *   node scripts/build.mjs frontend --repro-check
 *   node scripts/build.mjs frontend --consume-dist=DIR --dist-manifest=PATH
 *   node scripts/build.mjs bindings
 *   node scripts/build.mjs desktop-ldflags --os=linux
 *   node scripts/build.mjs write-minisign-public-key --out=PATH
 *   node scripts/build.mjs desktop --release --os=darwin --arch=arm64 --package
 *   node scripts/build.mjs desktop --release --package --package-only --consume-dist=DIR \\
 *     --os=darwin --arch=arm64 --require-evidence
 * Package-only reuse also requires the exact NEXTERM_RELEASE_REF tag and updater public key.
 *   node scripts/build.mjs server --release --os=linux --arch=amd64
 *   node scripts/build.mjs agent --release --os=linux --arch=amd64
 *   node scripts/build.mjs helper --release --repro-check --os=linux --arch=arm64
 *   node scripts/build.mjs report --kind=server-archive --os=linux --arch=amd64 \\
 *     --file=target/release-assets/NexTerm-server_<version>_linux_amd64.tar.gz --require-evidence
 `;

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const goMod = fs.readFileSync(path.join(ROOT, "go.mod"), "utf8");
const WAILS_MODULE = "github.com/wailsapp/wails/v3/cmd/wails3";
const WAILS_VERSION = goMod.match(/^\s*github\.com\/wailsapp\/wails\/v3\s+(v\S+)\s*$/m)?.[1];
const GO_TOOLCHAIN = goMod.match(/^toolchain\s+(go\d+\.\d+\.\d+)\s*$/m)?.[1];
if (!WAILS_VERSION || !GO_TOOLCHAIN) die("go.mod must declare the Wails module and an exact Go toolchain");
const RSRC_VERSION = "v0.10.2";
const argv = process.argv.slice(2);
const command = argv[0] && !argv[0].startsWith("-") ? argv.shift() : "debug";
const KNOWN_OPTIONS = new Set([
  "arch", "assets-dir", "base", "binary", "binary-manifest", "cgo", "consume-dist",
  "dist-manifest", "file", "helpers-dir", "id", "kind", "os", "out", "output", "package",
  "package-only", "release", "repro-check", "require-evidence",
  "skip-frontend", "write",
]);
const options = {};
for (const arg of argv) {
  if (!arg.startsWith("--")) die(`unknown positional argument: ${arg}`);
  const [name, ...rest] = arg.slice(2).split("=");
  if (!KNOWN_OPTIONS.has(name)) die(`unknown option: --${name}`);
  options[name] = rest.length ? rest.join("=") : "true";
}

const wailsConfig = JSON.parse(fs.readFileSync(path.join(ROOT, "wails.json"), "utf8"));
const WAILS_VERSION_PLACEHOLDER = wailsConfig.info?.version;
const VERSION = process.env.NEXTERM_RELEASE_VERSION || WAILS_VERSION_PLACEHOLDER;
const VERSION_INFO = process.env.NEXTERM_RELEASE_VERSION ? parseReleaseVersion(VERSION) : parseSemVer(VERSION);
if (!VERSION_INFO) {
  die(`release version is not a semver version: ${VERSION}`);
}
const packageJSON = JSON.parse(fs.readFileSync(path.join(ROOT, "package.json"), "utf8"));
const packageVersion = packageJSON.version;
if (packageVersion !== WAILS_VERSION_PLACEHOLDER) die(`package.json version ${packageVersion} diverges from the wails.json placeholder ${WAILS_VERSION_PLACEHOLDER}`);
let SOURCE_DATE_EPOCH = process.env.SOURCE_DATE_EPOCH || "";
const { commit: SOURCE_COMMIT, dirty: DIRTY } = sourceState({ cwd: ROOT });
const COMMIT = commitLabel({ commit: SOURCE_COMMIT, dirty: DIRTY });
const RELEASE_REF = (process.env.NEXTERM_RELEASE_REF || "").trim();
SOURCE_DATE_EPOCH ||= output("git", ["show", "-s", "--format=%ct", "HEAD"], { allowFailure: true })?.trim();
if (!SOURCE_DATE_EPOCH || !/^\d+$/.test(SOURCE_DATE_EPOCH)) {
  die("SOURCE_DATE_EPOCH is required when the git commit timestamp is unavailable");
}

const HOST_OS = { darwin: "darwin", linux: "linux", win32: "windows" }[process.platform];
const HOST_ARCH = { arm64: "arm64", x64: "amd64" }[process.arch];

function log(message) {
  console.log(message);
}

function die(message) {
  console.error(`\nbuild: ${message}`);
  process.exit(1);
}

function run(command, args, { cwd = ROOT, env = process.env, allowFailure = false, quiet = false, timeout = 0 } = {}) {
  const spec = resolveSpawnSpec(command, args, { platform: process.platform, env, execPath: process.execPath });
  if (!quiet) log(`\n$ ${spec.command} ${spec.args.join(" ")}`);
  const result = spawnSync(spec.command, spec.args, {
    cwd,
    env: { ...env, SOURCE_DATE_EPOCH: String(SOURCE_DATE_EPOCH), TZ: "UTC" },
    stdio: quiet ? "pipe" : "inherit",
    encoding: quiet ? "utf8" : undefined,
    timeout: timeout || undefined,
    ...spec.options,
  });
  if (result.error && !allowFailure) die(`${command}: ${result.error.message}`);
  if ((result.status !== 0 || result.signal) && !allowFailure) {
    die(`${command} exited with ${result.status ?? result.signal}`);
  }
  return result;
}

function output(command, args, options = {}) {
  const result = run(command, args, { ...options, quiet: true });
  return result.status === 0 ? (result.stdout || "") : null;
}

function option(name, fallback) {
  return options[name] === undefined ? fallback : options[name];
}

function flag(name) {
  return options[name] === "true" || options[name] === "1";
}

function targetOS() {
  return option("os", HOST_OS);
}

function targetArch() {
  return option("arch", HOST_ARCH);
}

function assertTarget(kind, goos, goarch) {
  const allowed = {
    "windows/amd64": ["desktop", "server", "agent"],
    "windows/arm64": ["desktop", "agent"],
    "darwin/amd64": ["desktop", "server", "agent"],
    "darwin/arm64": ["desktop", "server", "agent"],
    "linux/amd64": ["desktop", "server", "agent", "helper"],
    "linux/arm64": ["desktop", "server", "agent", "helper"],
  };
  if (!allowed[`${goos}/${goarch}`]?.includes(kind)) {
    die(`unsupported ${kind} target ${goos}/${goarch}; desktop is Windows/macOS/Linux amd64+arm64, server is Linux amd64+arm64, agent is Windows/macOS/Linux amd64+arm64, and helper is Linux amd64+arm64`);
  }
}

function buildEnvironment(goos, goarch, kind) {
  const cgo = kind === "desktop" && (goos === "darwin" || goos === "linux") ? "1" : "0";
  const env = {
    ...process.env,
    GOOS: goos,
    GOARCH: goarch,
    CGO_ENABLED: cgo,
    GOTOOLCHAIN: GO_TOOLCHAIN,
  };
  if (goos === "darwin") {
    env.MACOSX_DEPLOYMENT_TARGET = "12.0";
    env.CGO_CFLAGS = "-mmacosx-version-min=12.0";
    env.CGO_LDFLAGS = "-mmacosx-version-min=12.0";
  }
  return env;
}

function distManifest() {
  const dist = path.join(ROOT, "dist");
  const index = path.join(dist, "index.html");
  if (!fs.existsSync(index) || !fs.statSync(index).isFile()) die("dist/index.html is missing; build the real frontend first");
  const html = fs.readFileSync(index, "utf8");
  const scripts = [...html.matchAll(/(?:src|href)=["']([^"']*assets\/[^"']+)["']/g)].map((match) => {
    const asset = match[1];
    return asset.slice(asset.indexOf("assets/"));
  });
  if (!scripts.some((asset) => asset.endsWith(".js"))) die("dist/index.html does not reference a hashed assets/*.js bundle");
  for (const asset of scripts) {
    if (!fs.existsSync(path.join(dist, asset))) die(`frontend asset referenced by index.html is missing: ${asset}`);
  }
  return { dist, index, html, scripts };
}

function treeHashOrDie(root) {
  try {
    return treeHash(root);
  } catch (error) {
    die(error.message);
  }
}

function distManifestPath() {
  return path.resolve(ROOT, option("dist-manifest", "target/dist-manifest.json"));
}

function writeDistManifest() {
  const destination = distManifestPath();
  const manifest = createDistManifest(path.join(ROOT, "dist"), { version: VERSION, commit: COMMIT });
  fs.mkdirSync(path.dirname(destination), { recursive: true });
  fs.writeFileSync(destination, `${JSON.stringify(manifest, null, 2)}\n`);
  log(`frontend manifest: ${destination} (tree sha256:${manifest.tree_hash})`);
}

function consumeDist() {
  const source = path.resolve(ROOT, option("consume-dist", ""));
  if (!fs.existsSync(source) || !fs.statSync(source).isDirectory()) die(`--consume-dist directory does not exist: ${source}`);
  let manifest;
  try {
    manifest = loadManifest(distManifestPath());
  } catch (error) {
    die(`--consume-dist: ${error.message}`);
  }
  const problems = distManifestProblems(source, manifest, { version: VERSION, commit: COMMIT });
  if (problems.length) die(`verified dist consumption failed:\n${problems.map((problem) => `  - ${problem}`).join("\n")}`);
  const destination = path.join(ROOT, "dist");
  if (source !== destination) {
    fs.rmSync(destination, { recursive: true, force: true });
    fs.cpSync(source, destination, { recursive: true });
  }
  if (treeHashOrDie(destination) !== manifest.tree_hash) die("restored dist tree hash differs from the verified manifest");
  distManifest();
  log(`frontend dist consumed from ${source} (tree sha256:${manifest.tree_hash})`);
}

function buildFrontend({ repro = false, base = "" } = {}) {
  const viteArgs = ["exec", "vite", "build"];
  if (base) viteArgs.push(`--base=${base}`);
  run("pnpm", ["exec", "tsc", "-p", "tsconfig.json", "--noEmit"], { env: { ...process.env, NODE_OPTIONS: "" } });
  run("pnpm", viteArgs, { env: { ...process.env, NODE_OPTIONS: "" } });
  const first = treeHashOrDie(path.join(ROOT, "dist"));
  distManifest();
  if (repro) {
    run("pnpm", viteArgs, { env: { ...process.env, NODE_OPTIONS: "" } });
    const second = treeHashOrDie(path.join(ROOT, "dist"));
    if (first !== second) die(`frontend is not reproducible: ${first} != ${second}`);
    log(`frontend reproducibility: sha256:${second}`);
  } else {
    log(`frontend assets: sha256:${first}`);
  }
  writeDistManifest();
  return first;
}

function compareDirectories(expected, actual, prefix = "") {
  const expectedEntries = fs.readdirSync(expected, { withFileTypes: true });
  const actualEntries = fs.readdirSync(actual, { withFileTypes: true });
  const names = new Set([...expectedEntries, ...actualEntries].map((entry) => entry.name));
  const differences = [];
  for (const name of [...names].sort()) {
    const relative = path.join(prefix, name);
    const left = path.join(expected, name);
    const right = path.join(actual, name);
    if (!fs.existsSync(left) || !fs.existsSync(right)) {
      differences.push(`${relative}: only in ${fs.existsSync(left) ? "committed bindings" : "generated bindings"}`);
      continue;
    }
    const leftStat = fs.statSync(left);
    const rightStat = fs.statSync(right);
    if (leftStat.isDirectory() && rightStat.isDirectory()) differences.push(...compareDirectories(left, right, relative));
    else if (!leftStat.isFile() || !rightStat.isFile() || !fs.readFileSync(left).equals(fs.readFileSync(right))) {
      differences.push(`${relative}: content differs`);
    }
  }
  return differences;
}

let wailsVerified = false;

function runWails(args, options = {}) {
  const env = { ...process.env, GOTOOLCHAIN: GO_TOOLCHAIN, ...options.env };
  if (!wailsVerified) {
    const result = run("go", ["run", `${WAILS_MODULE}@${WAILS_VERSION}`, "version"], { env, quiet: true });
    const outputLines = `${result.stdout || ""}${result.stderr || ""}`.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
    const actual = outputLines.at(-1) || "unavailable";
    if (actual !== WAILS_VERSION) die(`Wails CLI version ${actual} does not match go.mod ${WAILS_VERSION}`);
    wailsVerified = true;
    log(`Wails CLI: ${actual} (pinned by go.mod)`);
  }
  return run("go", ["run", `${WAILS_MODULE}@${WAILS_VERSION}`, ...args], { ...options, env });
}

function replaceBindings(generated, committed) {
  const backup = `${committed}.previous-${process.pid}`;
  const hadCommitted = fs.existsSync(committed);
  if (hadCommitted) fs.renameSync(committed, backup);
  try {
    fs.cpSync(generated, committed, { recursive: true });
    if (hadCommitted) fs.rmSync(backup, { recursive: true, force: true });
  } catch (error) {
    fs.rmSync(committed, { recursive: true, force: true });
    if (hadCommitted) fs.renameSync(backup, committed);
    throw error;
  }
}

function verifyBindings() {
  const committed = path.join(ROOT, "src/ipc/bindings");
  const write = flag("write");
  if (!write && !fs.existsSync(committed)) die("src/ipc/bindings is missing; generate and review bindings before enabling drift checks");
  const temporary = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-bindings-"));
  try {
    runWails(["generate", "bindings", "-ts", "-i", "-d", temporary, "./cmd/nexterm-desktop"]);
    if (write) {
      replaceBindings(temporary, committed);
      log(`Wails bindings generated with ${WAILS_VERSION}`);
      return;
    }
    const differences = compareDirectories(committed, temporary);
    if (differences.length) die(`Wails bindings drift detected:\n${differences.join("\n")}`);
    log("Wails bindings: no drift");
  } finally {
    fs.rmSync(temporary, { recursive: true, force: true });
  }
}

function desktopMinisignPublicKey() {
  const configured = (process.env.NEXTERM_MINISIGN_PUBLIC_KEY || "").trim();
  if (!configured) return "";
  const lines = configured.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
  if (lines.length === 2 && lines[0].startsWith("untrusted comment:")) lines.shift();
  if (lines.length !== 1) die("NEXTERM_MINISIGN_PUBLIC_KEY must be one Base64 public key or a two-line minisign public key file");
  const encoded = lines[0];
  if (!/^[A-Za-z0-9+/]{56}$/.test(encoded)) die("NEXTERM_MINISIGN_PUBLIC_KEY must contain one 56-character Base64 minisign public key");
  const decoded = Buffer.from(encoded, "base64");
  if (decoded.length !== 42 || decoded.subarray(0, 2).toString() !== "Ed" || decoded.toString("base64") !== encoded) {
    die("NEXTERM_MINISIGN_PUBLIC_KEY is not an Ed25519 minisign public key");
  }
  return encoded;
}

function desktopMinisignPublicKeyDigest() {
  const publicKey = desktopMinisignPublicKey();
  return publicKey ? crypto.createHash("sha256").update(publicKey).digest("hex") : "";
}

function desktopReuseEvidence() {
  if (!process.env.NEXTERM_RELEASE_VERSION || !parseReleaseVersion(VERSION)) {
    die("--package-only requires a non-placeholder NEXTERM_RELEASE_VERSION; master placeholder artifacts cannot be reused for a tag");
  }
  const expectedRef = `refs/tags/v${VERSION}`;
  if (RELEASE_REF !== expectedRef) {
    die(`--package-only requires NEXTERM_RELEASE_REF=${expectedRef}, got ${JSON.stringify(RELEASE_REF)}`);
  }
  const updaterPublicKeySHA256 = desktopMinisignPublicKeyDigest();
  if (!updaterPublicKeySHA256) die("--package-only requires NEXTERM_MINISIGN_PUBLIC_KEY for updater equivalence evidence");
  return { sourceRef: RELEASE_REF, updaterPublicKeySHA256 };
}

function ldflags(kind, goos) {
  const flags = [
    "-s",
    "-w",
    `-X github.com/Hello-CTF/NexTerm/internal/version.Version=${VERSION}`,
    `-X github.com/Hello-CTF/NexTerm/internal/version.Commit=${COMMIT}`,
  ];
  if (kind === "desktop") {
    const publicKey = desktopMinisignPublicKey();
    if (publicKey) flags.push(`-X github.com/Hello-CTF/NexTerm/internal/update.defaultMinisignPublicKey=${publicKey}`);
  }
  if (kind === "desktop" && goos === "windows") flags.push("-H windowsgui");
  return flags.join(" ");
}

function writeMinisignPublicKey() {
  const destination = path.resolve(ROOT, option("out", ""));
  if (!option("out")) die("write-minisign-public-key requires --out=PATH");
  const publicKey = desktopMinisignPublicKey();
  if (!publicKey) die("NEXTERM_MINISIGN_PUBLIC_KEY is required");
  fs.mkdirSync(path.dirname(destination), { recursive: true });
  fs.writeFileSync(destination, `untrusted comment: NexTerm release minisign public key\n${publicKey}\n`, { mode: 0o644 });
  log(`minisign public key file: ${destination}`);
}

function stageWindowsResource(goarch) {
  const icon = path.join(ROOT, "public/brand/icon.ico");
  if (!fs.existsSync(icon)) die("Windows icon.ico asset is missing");
  const syso = path.join(ROOT, "cmd/nexterm-desktop", `rsrc_windows_${goarch}.syso`);
  fs.rmSync(syso, { force: true });
  run("go", ["run", `github.com/akavel/rsrc@${RSRC_VERSION}`, "-arch", goarch, "-ico", icon, "-o", syso]);
  return syso;
}

function defaultBinaryPath(kind, goos, goarch) {
  const suffix = goos === "windows" ? ".exe" : "";
  return path.join(ROOT, "target/go-build", `nexterm-${kind}-${goos}-${goarch}${suffix}`);
}

let cachedHelperProtocolMetadata;
const cachedHelperBinaryMetadata = new Map();

function helperProtocolMetadata() {
  if (cachedHelperProtocolMetadata) return cachedHelperProtocolMetadata;
  const raw = output("go", ["run", "-mod=readonly", "./cmd/nexterm-helper", "--metadata"], {
    env: buildEnvironment(HOST_OS, HOST_ARCH, "helper"),
  });
  let metadata;
  try {
    metadata = JSON.parse(raw);
  } catch (error) {
    die(`helper metadata is not valid JSON: ${error.message}`);
  }
  if (!Number.isInteger(metadata.protocol) || metadata.protocol <= 0) die(`helper metadata has invalid protocol ${metadata.protocol}`);
  if (!Array.isArray(metadata.features) || metadata.features.some((feature) => typeof feature !== "string" || feature === "")) {
    die("helper metadata features must be non-empty strings");
  }
  cachedHelperProtocolMetadata = { protocol: metadata.protocol, features: [...metadata.features].sort() };
  return cachedHelperProtocolMetadata;
}

function helperBinaryMetadata(file, goarch) {
  if (cachedHelperBinaryMetadata.has(file)) return cachedHelperBinaryMetadata.get(file);
  const native = HOST_OS === "linux" && HOST_ARCH === goarch;
  const raw = native
    ? output(file, ["--metadata"], { allowFailure: true, timeout: 30000 })
    : output("docker", ["run", "--rm", "--platform", `linux/${goarch}`,
      "--mount", `type=bind,source=${path.resolve(file)},target=/nexterm-helper,readonly`,
      "alpine:3.21", "/nexterm-helper", "--metadata"], { allowFailure: true, timeout: 60000 });
  if (raw === null) throw new Error(`cannot execute final Linux/${goarch} helper --metadata; build on a matching Linux host or start Docker`);
  let metadata;
  try {
    metadata = JSON.parse(raw);
  } catch (error) {
    throw new Error(`final Linux/${goarch} helper --metadata is not JSON: ${error.message}`);
  }
  const expected = helperProtocolMetadata();
  const features = Array.isArray(metadata.features) ? [...metadata.features].sort() : null;
  if (metadata.version !== VERSION || metadata.commit !== COMMIT || metadata.protocol !== expected.protocol ||
      features === null || features.some((feature) => typeof feature !== "string" || feature === "") ||
      JSON.stringify(features) !== JSON.stringify(expected.features)) {
    throw new Error(`final Linux/${goarch} helper metadata does not match this build: ${JSON.stringify(metadata)}`);
  }
  const verified = { protocol: metadata.protocol, features, binary_metadata_verified: true };
  cachedHelperBinaryMetadata.set(file, verified);
  return verified;
}

function writeBinaryManifest(file, { id, kind, goos, goarch, tags, cgo, stripped }) {
  const manifest = createBinaryManifest({
    file,
    id,
    kind,
    goos,
    goarch,
    tags: tags ? tags.split(",") : [],
    cgo,
    stripped,
    version: VERSION,
    commit: COMMIT,
    source_date_epoch: Number(SOURCE_DATE_EPOCH),
  });
  if (kind === "desktop") {
    manifest.source_ref = RELEASE_REF;
    manifest.updater_public_key_sha256 = desktopMinisignPublicKeyDigest();
  }
  if (kind === "helper") {
    try {
      Object.assign(manifest, helperBinaryMetadata(file, goarch));
    } catch (error) {
      Object.assign(manifest, helperProtocolMetadata(), { binary_metadata_verified: false });
      log(`helper binary metadata verification failed: ${error.message}`);
    }
  }
  const destination = path.resolve(ROOT, option("binary-manifest", `${file}.manifest.json`));
  fs.mkdirSync(path.dirname(destination), { recursive: true });
  fs.writeFileSync(destination, `${JSON.stringify(manifest, null, 2)}\n`);
  log(`${id} binary manifest: ${destination}`);
}

function validateBinaryForPackaging(outputPath, kind, goos, goarch, reuseEvidence) {
  if (!fs.existsSync(outputPath) || !fs.statSync(outputPath).isFile()) {
    die(`--package-only requires the existing production binary: ${outputPath}`);
  }
  const manifestPath = path.resolve(ROOT, option("binary-manifest", `${outputPath}.manifest.json`));
  let manifest;
  try {
    manifest = loadManifest(manifestPath);
  } catch (error) {
    die(`--package-only: ${error.message}`);
  }
  const problems = binaryManifestProblems({
    manifest,
    file: outputPath,
    expect: {
      id: `${kind}-${goos}-${goarch}`,
      kind,
      goos,
      goarch,
      tags: ["production"],
      cgo: buildEnvironment(goos, goarch, kind).CGO_ENABLED,
      stripped: true,
      version: VERSION,
      commit: COMMIT,
    },
  });
  if (kind === "desktop") {
    if (manifest.source_ref !== reuseEvidence.sourceRef) {
      problems.push(`binary manifest source_ref ${JSON.stringify(manifest.source_ref)} does not match ${reuseEvidence.sourceRef}`);
    }
    if (manifest.updater_public_key_sha256 !== reuseEvidence.updaterPublicKeySHA256) {
      problems.push(`binary manifest updater public key SHA-256 ${JSON.stringify(manifest.updater_public_key_sha256)} does not match ${reuseEvidence.updaterPublicKeySHA256}`);
    }
  }
  if (problems.length) die(`--package-only binary manifest validation failed:\n${problems.map((problem) => `  - ${problem}`).join("\n")}`);
  if (kind === "helper") {
    const expected = helperProtocolMetadata();
    if (manifest.binary_metadata_verified !== true || manifest.protocol !== expected.protocol ||
        JSON.stringify(manifest.features) !== JSON.stringify(expected.features)) {
      die(`--package-only helper manifest lacks matching final-binary --metadata evidence: ${manifestPath}`);
    }
  }
  log(`${kind}-${goos}-${goarch}: reusing verified production binary sha256:${manifest.artifact.sha256}`);
}

function reproduceBinary(args, outputPath, kind, goos, goarch) {
  const temporary = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-go-repro-"));
  const checkout = path.join(temporary, "checkout");
  const cache = path.join(temporary, "fresh-go-cache");
  const reproduction = `${outputPath}.repro`;
  try {
    fs.mkdirSync(checkout, { recursive: true });
    for (const name of ["go.mod", "go.sum"]) {
      fs.copyFileSync(path.join(ROOT, name), path.join(checkout, name));
    }
    for (const name of ["cmd", "internal"]) {
      fs.cpSync(path.join(ROOT, name), path.join(checkout, name), { recursive: true });
    }
    const reproductionArgs = [...args];
    reproductionArgs[reproductionArgs.indexOf("-o") + 1] = reproduction;
    run("go", reproductionArgs, {
      cwd: checkout,
      env: { ...buildEnvironment(goos, goarch, kind), GOCACHE: cache },
    });
    if (sha256(outputPath) !== sha256(reproduction)) die(`${kind} binary is not reproducible across checkout paths with a fresh GOCACHE: ${outputPath} != ${reproduction}`);
    log(`${kind} ${goos}/${goarch} cross-checkout reproducibility with fresh GOCACHE: ${sha256(outputPath)}`);
  } finally {
    fs.rmSync(temporary, { recursive: true, force: true });
  }
}

function buildBinary(kind) {
  const goos = targetOS();
  const goarch = targetArch();
  const release = flag("release") || command === "release";
  assertTarget(kind, goos, goarch);
  if (!["windows", "darwin", "linux"].includes(goos)) die(`unsupported GOOS: ${goos}`);
  const outputPath = path.resolve(ROOT, option("out", defaultBinaryPath(kind, goos, goarch)));
  const packageOnly = flag("package-only");
  if (packageOnly) {
    if (!flag("package")) die("--package-only requires --package");
    if (!release) die("--package-only requires --release");
    if (kind !== "desktop") die("server and agent packages are produced by scripts/pack-linux-server.sh and scripts/pack-agent.sh");
    if (flag("repro-check")) die("--package-only performs no compilation; --repro-check belongs to the producing job");
    if (!option("consume-dist")) die("--package-only requires --consume-dist=DIR so embedded-asset assertions run against the verified frontend");
  }
  if (kind !== "desktop" && option("consume-dist")) die("--consume-dist is only meaningful for the frontend command and desktop builds");
  if (flag("skip-frontend") && option("consume-dist")) die("--consume-dist cannot combine with --skip-frontend");
  if (kind === "desktop" && !flag("skip-frontend")) {
    if (option("consume-dist")) consumeDist();
    else buildFrontend({ repro: release });
  }
  if (kind === "desktop") distManifest();
  fs.mkdirSync(path.dirname(outputPath), { recursive: true });
  const tags = release ? "production" : "";
  const args = ["build", "-mod=readonly", "-trimpath", "-buildvcs=false"];
  if (tags) args.push("-tags", tags);
  args.push("-ldflags", ldflags(kind, goos), "-o", outputPath, `./cmd/nexterm-${kind}`);
  const embeddedRoot = path.join(ROOT, "cmd/nexterm-desktop/dist");
  if (kind === "desktop" && release && !packageOnly) {
    fs.rmSync(embeddedRoot, { recursive: true, force: true });
    fs.cpSync(path.join(ROOT, "dist"), embeddedRoot, { recursive: true });
    if (treeHashOrDie(path.join(ROOT, "dist")) !== treeHashOrDie(embeddedRoot)) die("staged desktop embed assets differ from dist");
  }
  const windowsResource = kind === "desktop" && goos === "windows" && !packageOnly ? stageWindowsResource(goarch) : null;
  try {
    if (packageOnly) {
      validateBinaryForPackaging(outputPath, kind, goos, goarch, desktopReuseEvidence());
    } else {
      run("go", args, { env: buildEnvironment(goos, goarch, kind) });
      if (flag("repro-check")) {
        reproduceBinary(args, outputPath, kind, goos, goarch);
        fs.rmSync(`${outputPath}.repro`, { force: true });
      }
    }
  } finally {
    if (kind === "desktop" && release) fs.rmSync(embeddedRoot, { recursive: true, force: true });
    if (windowsResource) fs.rmSync(windowsResource, { force: true });
  }
  if (!fs.existsSync(outputPath)) die(`Go linker did not produce ${outputPath}`);
  if (goos !== "windows") fs.chmodSync(outputPath, 0o755);
  if (!packageOnly) {
    writeBinaryManifest(outputPath, {
      id: `${kind}-${goos}-${goarch}`,
      kind,
      goos,
      goarch,
      tags,
      cgo: buildEnvironment(goos, goarch, kind).CGO_ENABLED,
      stripped: release,
    });
  }
  const report = writeArtifactReport({
    id: `${kind}-${goos}-${goarch}`,
    kind,
    goos,
    goarch,
    file: outputPath,
    cgo: buildEnvironment(goos, goarch, kind).CGO_ENABLED,
    stripped: release,
    requireEmbedded: kind === "desktop" && release,
  });
  if (flag("package")) {
    if (!release) die("--package requires --release");
    if (kind !== "desktop") die("server and agent packages are produced by scripts/pack-linux-server.sh and scripts/pack-agent.sh");
    const packaged = packageDesktop(outputPath, goos, goarch);
    const packageKind = { darwin: "desktop-dmg", windows: "desktop-nsis", linux: "desktop-linux-deb" }[goos];
    writeArtifactReport({
      id: `${packageKind}-${goos}-${goarch}`,
      kind: packageKind,
      goos,
      goarch,
      file: packaged,
      cgo: buildEnvironment(goos, goarch, kind).CGO_ENABLED,
      stripped: true,
      requireEvidence: flag("require-evidence"),
    });
  } else if (flag("require-evidence")) {
    requireReportPassed(report);
  }
  return { outputPath, report };
}

function inspectBinary(file) {
  const data = fs.readFileSync(file);
  if (data.length >= 0x40 && data[0] === 0x4d && data[1] === 0x5a) {
    const offset = data.readUInt32LE(0x3c);
    if (data.toString("ascii", offset, offset + 4) !== "PE\0\0") throw new Error("invalid PE header");
    const machine = data.readUInt16LE(offset + 4);
    const architecture = { 0x8664: "amd64", 0xaa64: "arm64" }[machine];
    const optionalOffset = offset + 24;
    const magic = data.readUInt16LE(optionalOffset);
    const directoryTableOffset = { 0x10b: 96, 0x20b: 112 }[magic];
    if (directoryTableOffset === undefined) throw new Error("unknown PE optional header magic");
    const resourceRva = data.readUInt32LE(optionalOffset + directoryTableOffset + 2 * 8);
    return { format: "pe", architecture, static: null, resourceRva };
  }
  if (data.length >= 20 && data.readUInt32LE(0) === 0xfeedfacf) {
    const cpu = data.readUInt32LE(4);
    const architecture = { 0x01000007: "amd64", 0x0100000c: "arm64" }[cpu];
    return { format: "mach-o", architecture, static: null };
  }
  if (data.length >= 0x40 && data[0] === 0x7f && data.toString("ascii", 1, 4) === "ELF") {
    if (data[4] !== 2) throw new Error("only 64-bit ELF release binaries are supported");
    const littleEndian = data[5] === 1;
    const read16 = (offset) => (littleEndian ? data.readUInt16LE(offset) : data.readUInt16BE(offset));
    const read64 = (offset) => (littleEndian ? data.readBigUInt64LE(offset) : data.readBigUInt64BE(offset));
    const machine = read16(18);
    const architecture = { 62: "amd64", 183: "arm64" }[machine];
    const phoff = Number(read64(0x20));
    const phentsize = read16(0x36);
    const phnum = read16(0x38);
    let dynamic = false;
    for (let index = 0; index < phnum; index += 1) {
      const type = littleEndian ? data.readUInt32LE(phoff + index * phentsize) : data.readUInt32BE(phoff + index * phentsize);
      if (type === 2) dynamic = true;
    }
    return { format: "elf", architecture, static: !dynamic };
  }
  throw new Error("unknown binary format");
}

function assertion(id, passed, detail) {
  return { id, status: passed ? "passed" : "failed", detail };
}

function binaryAssertions({ kind, goos, goarch, file, cgo, stripped, requireEmbedded }) {
  const result = [];
  let inspected;
  let helperMetadata;
  try {
    inspected = inspectBinary(file);
    result.push(assertion("binary-format", inspected.format === ({ windows: "pe", darwin: "mach-o", linux: "elf" })[goos], JSON.stringify(inspected)));
    result.push(assertion("binary-architecture", inspected.architecture === goarch, `expected ${goos}/${goarch}; actual ${inspected.format}/${inspected.architecture}`));
    if (kind === "desktop" && goos === "windows") result.push(assertion("windows-icon-resource", inspected.resourceRva > 0, inspected.resourceRva > 0 ? `PE resource directory RVA 0x${inspected.resourceRva.toString(16)}` : "PE resource directory is empty; the .syso icon resource was not linked"));
    if ((kind === "server" || kind === "agent") && goos === "linux") result.push(assertion("static-linux-server", inspected.static === true, "ELF PT_DYNAMIC must be absent"));
    if (kind === "helper" && goos === "linux") result.push(assertion("static-linux-helper", inspected.static === true, "ELF PT_DYNAMIC must be absent"));
  } catch (error) {
    result.push(assertion("binary-format", false, String(error)));
  }
  if (kind === "helper" && goos === "linux") {
    try {
      helperMetadata = helperBinaryMetadata(file, goarch);
      result.push(assertion("helper-binary-metadata", true,
          `executed final Linux/${goarch} helper --metadata: protocol=${helperMetadata.protocol}; features=${helperMetadata.features.join(",")}`));
    } catch (error) {
      result.push(assertion("helper-binary-metadata", false, String(error)));
    }
  }
  const moduleInfo = output("go", ["version", "-m", file], { allowFailure: true });
  result.push(assertion("cgo-boundary", Boolean(moduleInfo?.includes(`CGO_ENABLED=${cgo}`)), `expected CGO_ENABLED=${cgo} in go version -m`));
  if (stripped) {
    const binary = fs.readFileSync(file);
    const hasDwarf = binary.includes(Buffer.from(".debug_info")) || binary.includes(Buffer.from(".zdebug_info"));
    result.push(assertion("stripped-release", !hasDwarf, hasDwarf ? "DWARF debug info marker present" : "no DWARF debug info marker"));
  }
  if (requireEmbedded) {
    const { index, scripts } = distManifest();
    const binary = fs.readFileSync(file);
    result.push(assertion("embedded-index", binary.includes(fs.readFileSync(index)), "standalone desktop must contain the exact dist/index.html bytes"));
    for (const asset of scripts.filter((name) => name.endsWith(".js"))) {
      result.push(assertion("embedded-script", binary.includes(fs.readFileSync(path.join(ROOT, "dist", asset))), asset));
    }
  }
  return { assertions: result, inspected, helperMetadata };
}

function packagedHelperAssertions(directory, relativeDirectory, checkMode) {
  const checks = [];
  for (const arch of ["amd64", "arm64"]) {
    const name = `nexterm-helper-linux-${arch}`;
    const packaged = path.join(directory, relativeDirectory, name);
    const input = path.resolve(ROOT, option("helpers-dir", "target/go-build"), name);
    const label = `packaged-helper-${arch}`;
    try {
      const packagedStat = fs.lstatSync(packaged);
      if (!packagedStat.isFile()) throw new Error(`${name} is not a regular file`);
      const inputStat = fs.statSync(input);
      if (!inputStat.isFile()) throw new Error(`packaging input ${name} is not a regular file`);
      const inspected = inspectBinary(packaged);
      const packagedDigest = sha256(packaged);
      const inputDigest = sha256(input);
      checks.push(assertion(`${label}-elf`, inspected.format === "elf" && inspected.architecture === arch && inspected.static === true,
          `expected static Linux ELF/${arch}; actual ${JSON.stringify(inspected)}`));
      checks.push(assertion(`${label}-sha256`, packagedDigest === inputDigest,
          `package SHA256=${packagedDigest}; input SHA256=${inputDigest}`));
      if (checkMode) checks.push(assertion(`${label}-executable`, (packagedStat.mode & 0o111) !== 0,
          `package mode=${(packagedStat.mode & 0o777).toString(8)}; executable bit required`));
    } catch (error) {
      checks.push(assertion(`${label}-inspect`, false, String(error)));
    }
  }
  return checks;
}

function sevenZipCommand() {
  const roots = [process.env.ProgramFiles, process.env.ProgramW6432, process.env["ProgramFiles(x86)"],
    "C:\\Program Files", "C:\\Program Files (x86)"].filter(Boolean);
  for (const root of roots) {
    const candidate = path.join(root, "7-Zip", "7z.exe");
    if (fs.existsSync(candidate)) return candidate;
  }
  for (const command of ["7z", "7zz"]) {
    if (output(command, ["i"], { allowFailure: true }) !== null) return command;
  }
  return null;
}

function agentArchiveBinaryAssertions(kind, file, goos, goarch) {
  const member = goos === "windows" ? "nexterm-agent.exe" : "nexterm-agent";
  const input = path.resolve(ROOT, option("binary", defaultBinaryPath("agent", goos, goarch)));
  const work = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-agent-assert-"));
  const checks = [];
  try {
    const extracted = kind === "agent-archive"
      ? output("tar", ["-xzf", path.resolve(file), "-C", work, member], { allowFailure: true })
      : output("python3", ["-m", "zipfile", "-e", path.resolve(file), work], { allowFailure: true });
    checks.push(assertion(`${kind}-binary-extract`, extracted !== null, `extract ${member} from the final archive`));
    if (extracted === null) return checks;
    const packaged = path.join(work, member);
    const packagedValid = fs.existsSync(packaged) && fs.statSync(packaged).isFile();
    const inputValid = fs.existsSync(input) && fs.statSync(input).isFile();
    checks.push(assertion(`${kind}-binary-input`, packagedValid && inputValid, `packaged=${packaged}; input=${input}`));
    if (!packagedValid || !inputValid) return checks;
    const packagedDigest = sha256(packaged);
    const inputDigest = sha256(input);
    checks.push(assertion(`${kind}-binary-sha256`, packagedDigest === inputDigest, `package SHA256=${packagedDigest}; input SHA256=${inputDigest}`));
    const binary = binaryAssertions({ kind: "agent", goos, goarch, file: packaged, cgo: "0", stripped: true });
    for (const item of binary.assertions) {
      checks.push(assertion(`${kind}-binary-${item.id}`, item.status === "passed", item.detail));
    }
  } catch (error) {
    checks.push(assertion(`${kind}-binary-inspect`, false, String(error)));
  } finally {
    fs.rmSync(work, { recursive: true, force: true });
  }
  return checks;
}

function packageAssertions(kind, file, goos, goarch) {
  const data = fs.readFileSync(file);
  if (kind === "desktop-nsis") {
    const checks = [assertion("nsis-container", data[0] === 0x4d && data[1] === 0x5a, "NSIS output is not a PE executable")];
    const sevenZip = sevenZipCommand();
    if (!sevenZip) {
      checks.push(assertion("nsis-extract", false, "7-Zip CLI is required to inspect installed NSIS contents"));
      return checks;
    }
    const work = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-nsis-assert-"));
    try {
      const extracted = output(sevenZip, ["x", "-y", `-o${work}`, path.resolve(file)], { allowFailure: true });
      checks.push(assertion("nsis-extract", extracted !== null, "7-Zip must extract the NSIS installer"));
      if (extracted !== null) checks.push(...packagedHelperAssertions(work, "", false));
    } finally {
      fs.rmSync(work, { recursive: true, force: true });
    }
    return checks;
  }
  if (kind === "desktop-linux-deb") {
    const checks = [assertion("deb-container", data.subarray(0, 8).toString("ascii") === "!<arch>\n", "Linux desktop package is not an ar archive")];
    const work = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-deb-assert-"));
    try {
      const extracted = output("ar", ["x", path.resolve(file)], { cwd: work, allowFailure: true });
      if (extracted === null) {
        checks.push(assertion("linux-desktop-deb-extract", false, "ar extraction failed; the package is not a installable deb"));
        return checks;
      }
      const control = output("tar", ["-xzOf", path.join(work, "control.tar.gz"), "control"], { allowFailure: true }) ?? "";
      checks.push(assertion(
          "linux-desktop-deb-control",
          control.includes("Package: nexterm\n") && control.includes(`Version: ${debVersion(VERSION)}\n`) && control.includes(`Architecture: ${goarch}\n`),
          "control file must carry the package name, release version and architecture",
      ));
      const listing = output("tar", ["-tzf", path.join(work, "data.tar.gz")], { allowFailure: true });
      const members = new Set(listing?.split(/\r?\n/) || []);
      const required = ["usr/bin/nexterm-desktop", "usr/bin/nexterm-helper-linux-amd64", "usr/bin/nexterm-helper-linux-arm64", "usr/share/applications/nexterm.desktop", "usr/share/icons/hicolor/256x256/apps/nexterm.png", "usr/share/doc/nexterm/copyright"];
      checks.push(assertion("linux-desktop-deb-members", goos === "linux" && listing !== null && required.every((member) => members.has(member)), `required members: ${required.join(", ")}`));
      const unpacked = output("tar", ["-xzf", path.join(work, "data.tar.gz"), "-C", work,
        "usr/bin/nexterm-helper-linux-amd64", "usr/bin/nexterm-helper-linux-arm64"], { allowFailure: true });
      checks.push(assertion("linux-desktop-deb-helper-extract", unpacked !== null, "both helpers must extract from data.tar.gz"));
      if (unpacked !== null) checks.push(...packagedHelperAssertions(work, "usr/bin", true));
    } finally {
      fs.rmSync(work, { recursive: true, force: true });
    }
    return checks;
  }
  if (kind === "server-archive") {
    const checks = [assertion("gzip-container", data[0] === 0x1f && data[1] === 0x8b, "server archive is not gzip compressed")];
    const root = `NexTerm-server_${VERSION}_linux_${goarch}`;
    const required = [
      "nexterm-server", "nexterm-helper-linux-amd64", "nexterm-helper-linux-arm64", "nexterm-server.service", "nexterm-onlyserver.service",
      "nexterm.env.example", "onlyserver.env.example", "LICENSE", "README.md", "web/index.html",
    ].map((member) => `${root}/${member}`);
    const listing = output("tar", ["-tzf", file], { allowFailure: true });
    const members = new Set(listing?.split(/\r?\n/) || []);
    checks.push(assertion(
        "full-server-archive-members",
        goos === "linux" && listing !== null && required.every((member) => members.has(member)),
        `required members: ${required.join(", ")}`,
    ));
    const work = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-server-assert-"));
    try {
      const extracted = output("tar", ["-xzf", path.resolve(file), "-C", work,
        `${root}/nexterm-helper-linux-amd64`, `${root}/nexterm-helper-linux-arm64`], { allowFailure: true });
      checks.push(assertion("server-archive-helper-extract", extracted !== null, "both helpers must extract from the server archive"));
      if (extracted !== null) checks.push(...packagedHelperAssertions(work, root, true));
    } finally {
      fs.rmSync(work, { recursive: true, force: true });
    }
    return checks;
  }
  if (kind === "desktop-dmg") {
    const checks = [assertion("dmg-container", data.length > 512, "DMG was already validated with hdiutil")];
    const work = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-dmg-assert-"));
    const mountpoint = path.join(work, "mounted");
    fs.mkdirSync(mountpoint);
    let mounted = false;
    let detached = false;
    try {
      mounted = output("hdiutil", ["attach", "-readonly", "-nobrowse", "-quiet", "-mountpoint", mountpoint, path.resolve(file)], { allowFailure: true }) !== null;
      checks.push(assertion("dmg-mount", mounted, "hdiutil must mount the generated DMG"));
      if (mounted) checks.push(...packagedHelperAssertions(mountpoint, "NexTerm.app/Contents/MacOS", true));
    } finally {
      if (mounted) {
        detached = output("hdiutil", ["detach", "-quiet", mountpoint], { allowFailure: true }) !== null;
        checks.push(assertion("dmg-detach", detached, "hdiutil must detach the inspected DMG"));
      }
      if (!mounted || detached) fs.rmSync(work, { recursive: true, force: true });
    }
    return checks;
  }
  if (kind === "agent-archive") {
    const checks = [assertion("gzip-container", data[0] === 0x1f && data[1] === 0x8b, "agent archive is not gzip compressed")];
    const listing = output("tar", ["-tzf", file], { allowFailure: true });
    const members = listing?.split(/\r?\n/).filter((name) => name !== "") || [];
    checks.push(assertion(
        "agent-archive-members",
        (goos === "linux" || goos === "darwin") && listing !== null && members.length === 1 && members[0] === "nexterm-agent",
        `tar root must contain only nexterm-agent; members: ${members.join(", ")}`,
    ));
    checks.push(...agentArchiveBinaryAssertions(kind, file, goos, goarch));
    return checks;
  }
  if (kind === "agent-zip") {
    const members = zipMembers(data);
    const checks = [
      assertion("zip-container", data[0] === 0x50 && data[1] === 0x4b && data[2] === 0x03 && data[3] === 0x04, "agent package is not a zip archive"),
      assertion(
          "agent-zip-members",
          goos === "windows" && members !== null && members.length === 1 && members[0] === "nexterm-agent.exe",
          `zip root must contain only nexterm-agent.exe; members: ${members === null ? "unreadable central directory" : members.join(", ")}`,
      ),
    ];
    checks.push(...agentArchiveBinaryAssertions(kind, file, goos, goarch));
    return checks;
  }
  return [];
}

function zipMembers(data) {
  const eocdMin = 22;
  if (data.length < eocdMin) return null;
  const searchFloor = Math.max(0, data.length - (eocdMin + 0xffff));
  let eocd = -1;
  for (let index = data.length - eocdMin; index >= searchFloor; index -= 1) {
    if (data.readUInt32LE(index) === 0x06054b50) {
      eocd = index;
      break;
    }
  }
  if (eocd < 0) return null;
  const count = data.readUInt16LE(eocd + 10);
  let offset = data.readUInt32LE(eocd + 16);
  const names = [];
  for (let index = 0; index < count; index += 1) {
    if (offset + 46 > data.length || data.readUInt32LE(offset) !== 0x02014b50) return null;
    const nameLength = data.readUInt16LE(offset + 28);
    const extraLength = data.readUInt16LE(offset + 30);
    const commentLength = data.readUInt16LE(offset + 32);
    names.push(data.toString("utf8", offset + 46, offset + 46 + nameLength));
    offset += 46 + nameLength + extraLength + commentLength;
  }
  return names;
}

function sha256(file) {
  return crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex");
}

function writeArtifactReport({ id, kind, goos, goarch, file, cgo = "0", stripped = true, requireEmbedded = false, requireEvidence = false }) {
  if (!fs.existsSync(file) || !fs.statSync(file).isFile()) die(`artifact does not exist: ${file}`);
  const bytes = fs.statSync(file).size;
  const rawBinary = kind === "desktop" || kind === "server" || kind === "agent" || kind === "helper";
  const inspected = rawBinary
      ? binaryAssertions({ kind, goos, goarch, file, cgo, stripped, requireEmbedded })
      : { assertions: packageAssertions(kind, file, goos, goarch), inspected: null };
  const assertionsFailed = inspected.assertions.length === 0 || inspected.assertions.some((item) => item.status === "failed");
  const report = {
    schema_version: 2,
    id,
    kind,
    platform: { os: goos, arch: goarch, cgo: cgo === "1" ? "enabled-only-for-wails-platform-layer" : "disabled" },
    version: VERSION,
    commit: COMMIT,
    source_commit: SOURCE_COMMIT,
    dirty_source_tree: DIRTY,
    source_date_epoch: Number(SOURCE_DATE_EPOCH),
    artifact: { path: path.relative(ROOT, file), size_bytes: bytes, sha256: sha256(file), stripped },
    build: {
      go: output("go", ["env", "GOVERSION"], { allowFailure: true })?.trim() || "unknown",
      wails_cli: WAILS_VERSION,
      windows_resource: `rsrc ${RSRC_VERSION} embeds public/brand/icon.ico as rsrc_windows_<arch>.syso for windows desktop binaries`,
      node: process.version,
      pnpm: output("pnpm", ["--version"], { allowFailure: true })?.trim() || "unknown",
      toolchain_pin: `${GO_TOOLCHAIN} from go.mod; Node 24+; ${packageJSON.packageManager || "pnpm version supplied by CI"}; Wails ${WAILS_VERSION} pinned for generation`,
      module_mode: "-mod=readonly",
      reproducible_flags: ["-trimpath", "-buildvcs=false", "-ldflags=-s -w + version/commit"],
      runtime_metadata: "Go pclntab and build info are preserved for stack symbolization; no UPX and no manual symbol-table or pclntab stripping",
      dependency_accounting: "the complete composed Go module, including Eino where imported; no feature-removing build tags",
    },
    format: inspected.inspected,
    assertions: inspected.assertions,
    status: assertionsFailed ? "failed" : "passed",
  };
  if (kind === "helper" && inspected.helperMetadata) Object.assign(report, inspected.helperMetadata);
  const destination = path.resolve(ROOT, option("output", `${file}.artifact.json`));
  fs.mkdirSync(path.dirname(destination), { recursive: true });
  fs.writeFileSync(destination, `${JSON.stringify(report, null, 2)}\n`);
  log(`${id}: ${bytes} bytes; report=${destination}`);
  if (assertionsFailed) die(`${id} has failed artifact assertions; see ${destination}`);
  if (requireEvidence) requireReportPassed(report);
  return report;
}

function requireReportPassed(report) {
  const assertionsPassed = Array.isArray(report.assertions) && report.assertions.length > 0 && report.assertions.every((item) => item.status === "passed");
  const ready = report.status === "passed" && assertionsPassed;
  if (!ready) {
    die(`${report.id} is not release-ready (${report.status}); every artifact needs passing file/content/static/cgo checks`);
  }
}

function packageDesktop(binary, goos, goarch) {
  if (goos !== HOST_OS) die(`native ${goos} packaging cannot run on ${HOST_OS}`);
  for (const arch of ["amd64", "arm64"]) verifiedHelper(arch);
  if (goos === "darwin") return packageDarwin(binary, goarch);
  if (goos === "windows") return packageWindows(binary, goarch);
  if (goos === "linux") return packageLinuxDesktop(binary, goarch);
  die(`no desktop package is contracted for ${goos}`);
}

function verifiedHelper(arch) {
  const file = path.resolve(ROOT, option("helpers-dir", "target/go-build"), `nexterm-helper-linux-${arch}`);
  validateBinaryForPackaging(file, "helper", "linux", arch);
  return file;
}

function copyHelpers(directory) {
  for (const arch of ["amd64", "arm64"]) {
    const target = path.join(directory, `nexterm-helper-linux-${arch}`);
    fs.copyFileSync(verifiedHelper(arch), target);
    fs.chmodSync(target, 0o755);
  }
}

function debVersion(version) {
  return version.replace("-", "~");
}

function packageLinuxDesktop(binary, goarch) {
  const assets = path.resolve(ROOT, option("assets-dir", "target/release-assets"));
  const name = `NexTerm-desktop_${VERSION}_linux_${goarch}`;
  const work = path.join(ROOT, "target/package-work/linux-desktop", goarch);
  const dataDir = path.join(work, "data");
  fs.rmSync(work, { recursive: true, force: true });
  fs.mkdirSync(path.join(dataDir, "usr/bin"), { recursive: true });
  fs.mkdirSync(path.join(dataDir, "usr/share/applications"), { recursive: true });
  fs.mkdirSync(path.join(dataDir, "usr/share/icons/hicolor/256x256/apps"), { recursive: true });
  fs.mkdirSync(path.join(dataDir, "usr/share/doc/nexterm"), { recursive: true });
  fs.mkdirSync(path.join(work, "control"), { recursive: true });
  fs.mkdirSync(assets, { recursive: true });
  fs.copyFileSync(binary, path.join(dataDir, "usr/bin/nexterm-desktop"));
  copyHelpers(path.join(dataDir, "usr/bin"));
  fs.copyFileSync(path.join(ROOT, "public/brand/nexterm-mark-256.png"), path.join(dataDir, "usr/share/icons/hicolor/256x256/apps/nexterm.png"));
  fs.copyFileSync(path.join(ROOT, "LICENSE"), path.join(dataDir, "usr/share/doc/nexterm/copyright"));
  fs.writeFileSync(path.join(dataDir, "usr/share/applications/nexterm.desktop"), "[Desktop Entry]\nType=Application\nName=NexTerm\nComment=SSH / SFTP / Docker / database operations terminal\nExec=nexterm-desktop %u\nIcon=nexterm\nTerminal=false\nCategories=System;TerminalEmulator;\nMimeType=x-scheme-handler/nexterm;\n");
  fs.writeFileSync(path.join(work, "control", "control"), `Package: nexterm\nVersion: ${debVersion(VERSION)}\nSection: net\nPriority: optional\nArchitecture: ${goarch}\nMaintainer: Probius <https://github.com/Hello-CTF/NexTerm>\nHomepage: https://github.com/Hello-CTF/NexTerm\nDepends: libgtk-4-1, libwebkitgtk-6.0-4t64 | libwebkitgtk-6.0-4\nDescription: Browser-based SSH / SFTP / Docker / database operations terminal\n Terminal processes, sessions and workspace layout stay on your own server;\n credentials are encrypted with a vault key you control.\n`);
  const normalizeModes = (directory) => {
    for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
      const target = path.join(directory, entry.name);
      if (entry.isDirectory()) {
        fs.chmodSync(target, 0o755);
        normalizeModes(target);
      } else {
        fs.chmodSync(target, 0o644);
      }
    }
  };
  normalizeModes(dataDir);
  fs.chmodSync(path.join(dataDir, "usr/bin/nexterm-desktop"), 0o755);
  for (const arch of ["amd64", "arm64"]) fs.chmodSync(path.join(dataDir, "usr/bin", `nexterm-helper-linux-${arch}`), 0o755);
  const portableArchive = path.join(ROOT, "scripts/lib/portable-archive.py");
  run("python3", [portableArchive, "tar", path.join(work, "control.tar.gz"), path.join(work, "control"), "control", String(SOURCE_DATE_EPOCH)]);
  run("python3", [portableArchive, "tar", path.join(work, "data.tar.gz"), dataDir, "usr", String(SOURCE_DATE_EPOCH)]);
  fs.writeFileSync(path.join(work, "debian-binary"), "2.0\n");
  const deb = path.join(assets, `${name}.deb`);
  fs.rmSync(deb, { force: true });
  run("ar", ["rD", deb, path.join(work, "debian-binary"), path.join(work, "control.tar.gz"), path.join(work, "data.tar.gz")]);
  fs.chmodSync(deb, 0o644);
  return deb;
}

function packageDarwin(binary, goarch) {
  const assets = path.resolve(ROOT, option("assets-dir", "target/release-assets"));
  const work = path.join(ROOT, "target/package-work/darwin", goarch);
  fs.rmSync(work, { recursive: true, force: true });
  const app = path.join(work, "NexTerm.app");
  fs.mkdirSync(path.join(app, "Contents/MacOS"), { recursive: true });
  fs.mkdirSync(path.join(app, "Contents/Resources"), { recursive: true });
  fs.copyFileSync(binary, path.join(app, "Contents/MacOS/NexTerm"));
  fs.chmodSync(path.join(app, "Contents/MacOS/NexTerm"), 0o755);
  copyHelpers(path.join(app, "Contents/MacOS"));
  const icon = path.join(ROOT, "public/brand/icon.icns");
  if (!fs.existsSync(icon)) die("macOS icon.icns asset is missing");
  fs.copyFileSync(icon, path.join(app, "Contents/Resources/icon.icns"));
  const plist = `<?xml version="1.0" encoding="UTF-8"?>\n<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">\n<plist version="1.0"><dict>\n<key>CFBundleDisplayName</key><string>NexTerm</string>\n<key>CFBundleExecutable</key><string>NexTerm</string>\n<key>CFBundleIdentifier</key><string>${wailsConfig.info.productIdentifier}</string>\n<key>CFBundleIconFile</key><string>icon.icns</string>\n<key>CFBundleName</key><string>NexTerm</string>\n<key>CFBundlePackageType</key><string>APPL</string>\n<key>CFBundleShortVersionString</key><string>${VERSION}</string>\n<key>CFBundleURLTypes</key><array><dict>\n<key>CFBundleURLName</key><string>${wailsConfig.info.productIdentifier}</string>\n<key>CFBundleURLSchemes</key><array><string>nexterm</string></array>\n</dict></array>\n<key>CFBundleVersion</key><string>${VERSION}</string>\n<key>LSMinimumSystemVersion</key><string>12.0</string>\n<key>NSHighResolutionCapable</key><true/>\n</dict></plist>\n`;
  fs.writeFileSync(path.join(app, "Contents/Info.plist"), plist);
  run("plutil", ["-lint", path.join(app, "Contents/Info.plist")]);
  const signIdentity = process.env.NEXTERM_MACOS_SIGN_IDENTITY || "-";
  const signArgs = ["--force", "--deep", "--sign", signIdentity];
  if (signIdentity !== "-") signArgs.push("--options", "runtime", "--timestamp");
  run("codesign", [...signArgs, app]);
  run("codesign", ["--verify", "--deep", "--strict", app]);
  fs.symlinkSync("/Applications", path.join(work, "Applications"));
  fs.mkdirSync(assets, { recursive: true });
  const architecture = goarch === "arm64" ? "aarch64" : "x86_64";
  const dmg = path.join(assets, `NexTerm_${VERSION}_${architecture}.dmg`);
  fs.rmSync(dmg, { force: true });
  run("hdiutil", ["create", "-volname", "NexTerm", "-srcfolder", work, "-ov", "-format", "UDZO", dmg]);
  run("hdiutil", ["verify", dmg]);
  notarizeDmg(dmg);
  return dmg;
}

// Notarization is an optional enhancement: submit only when App Store Connect
// API credentials (notarytool key auth) are configured; self-signed
// certificates cannot pass notarization, so without credentials the existing
// ad-hoc distribution stays.
function notarizeDmg(dmg) {
  const keyPath = process.env.NEXTERM_MACOS_NOTARY_KEY_PATH;
  const keyId = process.env.NEXTERM_MACOS_NOTARY_KEY_ID;
  const issuer = process.env.NEXTERM_MACOS_NOTARY_ISSUER;
  if (!keyPath || !keyId || !issuer) return;
  run("xcrun", ["notarytool", "submit", dmg, "--key", keyPath, "--key-id", keyId, "--issuer", issuer, "--wait"]);
  run("xcrun", ["stapler", "staple", dmg]);
}

function packageWindows(binary, goarch) {
  const assets = path.resolve(ROOT, option("assets-dir", "target/release-assets"));
  const work = path.join(ROOT, "target/package-work/windows", goarch);
  fs.mkdirSync(work, { recursive: true });
  fs.mkdirSync(assets, { recursive: true });
  runWails(["generate", "webview2bootstrapper", "-dir", work]);
  const bootstrapper = path.join(work, "MicrosoftEdgeWebview2Setup.exe");
  if (!fs.existsSync(bootstrapper)) die("Wails did not generate the WebView2 bootstrapper");
  const icon = path.join(ROOT, "public/brand/icon.ico");
  if (!fs.existsSync(icon)) die("Windows icon.ico asset is missing");
  const setupArch = goarch === "amd64" ? "x64" : "arm64";
  const installer = path.join(assets, `NexTerm_${VERSION}_${setupArch}-setup.exe`);
  const defines = [
    `NEXTERM_BINARY=${binary}`,
    `NEXTERM_VERSION=${VERSION}`,
    `NEXTERM_NUMERIC_VERSION=${VERSION_INFO.core}.0`,
    `NEXTERM_OUT=${installer}`,
    `NEXTERM_ICON=${icon}`,
    `NEXTERM_WEBVIEW2=${bootstrapper}`,
    `NEXTERM_EXE_NAME=${path.basename(binary)}`,
    `NEXTERM_HELPER_AMD64=${verifiedHelper("amd64")}`,
    `NEXTERM_HELPER_ARM64=${verifiedHelper("arm64")}`,
  ];
  const makensis = resolveMakensis({ env: process.env });
  log(`makensis: ${makensis.command} (${makensis.source})`);
  run(makensis.command, [...defines.map((define) => `-D${define}`), path.join(ROOT, ".github/packaging/windows/NexTerm.nsi")]);
  if (!fs.existsSync(installer)) die("makensis did not produce the contracted installer");
  return installer;
}

function reportOnly() {
  const file = path.resolve(ROOT, option("file", ""));
  const goos = targetOS();
  const goarch = targetArch();
  const kind = option("kind", "server-archive");
  const id = option("id", `${kind}-${goos}-${goarch}`);
  if (!options.file) die("report requires --file=PATH");
  writeArtifactReport({ id, kind, goos, goarch, file, cgo: option("cgo", "0"), requireEvidence: flag("require-evidence"), stripped: true });
}

switch (command) {
  case "frontend":
    if (option("consume-dist")) {
      if (flag("repro-check")) die("--consume-dist replaces the frontend build; --repro-check belongs to the producing job");
      consumeDist();
    } else {
      buildFrontend({ repro: flag("repro-check"), base: option("base", "") });
    }
    break;
  case "bindings":
    verifyBindings();
    break;
  case "desktop":
  case "server":
  case "agent":
  case "helper":
    buildBinary(command);
    break;
  case "report":
    reportOnly();
    break;
  case "debug":
    buildFrontend();
    options.release = "false";
    options.os = HOST_OS;
    options.arch = HOST_ARCH;
    options["skip-frontend"] = "true";
    buildBinary("desktop");
    break;
  case "release": {
    if (flag("skip-frontend")) die("release does not allow --skip-frontend; reproducible real assets are mandatory");
    const tag = process.env.GITHUB_REF_NAME;
    if (tag?.startsWith("v") && tag !== `v${VERSION}`) die(`tag ${tag} does not match release version v${VERSION} (set NEXTERM_RELEASE_VERSION to the tag version)`);
    buildFrontend({ repro: true });
    verifyBindings();
    options.release = "true";
    options["repro-check"] = "true";
    options["require-evidence"] = "true";
    options["skip-frontend"] = "true";
    const requestedOutput = options.out;
    delete options.out;
    for (const arch of ["amd64", "arm64"]) {
      options.os = "linux";
      options.arch = arch;
      options.out = path.join(option("helpers-dir", "target/go-build"), `nexterm-helper-linux-${arch}`);
      buildBinary("helper");
    }
    if (requestedOutput === undefined) delete options.out;
    else options.out = requestedOutput;
    options.package = "true";
    options.os = HOST_OS;
    options.arch = HOST_ARCH;
    buildBinary("desktop");
    break;
  }
  case "desktop-ldflags":
    console.log(ldflags("desktop", targetOS()));
    break;
  case "write-minisign-public-key":
    writeMinisignPublicKey();
    break;
  case "help":
  case "--help":
    console.log(HELP_TEXT);
    break;
  default:
    die(`unknown command ${command}; use debug, release, frontend, bindings, desktop, server, agent, helper, report, desktop-ldflags, write-minisign-public-key, or help`);
}
