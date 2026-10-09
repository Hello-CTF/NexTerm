#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { resolveMakensis } from "./lib/makensis.mjs";
import { resolveSpawnSpec } from "./lib/spawn-spec.mjs";
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
 *   node scripts/build.mjs desktop --release --os=darwin --arch=arm64 --package
 *   node scripts/build.mjs desktop --release --package --package-only --consume-dist=DIR \\
 *     --os=darwin --arch=arm64 --require-evidence
 *   node scripts/build.mjs server --release --os=linux --arch=amd64
 *   node scripts/build.mjs report --kind=server-archive --os=linux --arch=amd64 \\
 *     --flavor=full --file=target/release-assets/NexTerm-server_<version>_linux_amd64.tar.gz --require-evidence
 `;

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const WAILS_VERSION = "v3.0.0-beta.27";
const argv = process.argv.slice(2);
const command = argv[0] && !argv[0].startsWith("-") ? argv.shift() : "debug";
const KNOWN_OPTIONS = new Set([
  "arch", "assets-dir", "base", "binary-manifest", "cgo", "consume-dist", "custom-go-baseline",
  "dist-manifest", "file", "flavor", "id", "kind", "os", "out", "output", "package",
  "package-only", "release", "repro-check", "require-evidence", "rust-official-baseline",
  "skip-frontend",
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
if (!VERSION || !/^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/.test(VERSION)) {
  die(`release version is not a semver version: ${VERSION}`);
}
const packageVersion = JSON.parse(fs.readFileSync(path.join(ROOT, "package.json"), "utf8")).version;
if (packageVersion !== WAILS_VERSION_PLACEHOLDER) die(`package.json version ${packageVersion} diverges from the wails.json placeholder ${WAILS_VERSION_PLACEHOLDER}`);
let SOURCE_DATE_EPOCH = process.env.SOURCE_DATE_EPOCH || "";
const COMMIT = output("git", ["rev-parse", "HEAD"], { allowFailure: true })?.trim() || "unknown";
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
    "windows/amd64": ["desktop", "server"],
    "windows/arm64": ["desktop"],
    "darwin/amd64": ["desktop", "server"],
    "darwin/arm64": ["desktop", "server"],
    "linux/amd64": ["desktop", "server"],
    "linux/arm64": ["desktop", "server"],
  };
  if (!allowed[`${goos}/${goarch}`]?.includes(kind)) {
    die(`unsupported ${kind} target ${goos}/${goarch}; desktop is Windows/macOS/Linux amd64+arm64 and server is Linux amd64+arm64`);
  }
}

function buildEnvironment(goos, goarch, kind) {
  const cgo = kind === "desktop" && (goos === "darwin" || goos === "linux") ? "1" : "0";
  const env = {
    ...process.env,
    GOOS: goos,
    GOARCH: goarch,
    CGO_ENABLED: cgo,
    GOTOOLCHAIN: "go1.26.8",
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

function verifyBindings() {
  const committed = path.join(ROOT, "src/ipc/bindings");
  if (!fs.existsSync(committed)) die("src/ipc/bindings is missing; generate and review bindings before enabling drift checks");
  const temporary = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-bindings-"));
  try {
    run("wails3", ["generate", "bindings", "-ts", "-i", "-d", temporary, "./cmd/nexterm-desktop"], {
      env: { ...process.env, GOTOOLCHAIN: "go1.26.8" },
    });
    const differences = compareDirectories(committed, temporary);
    if (differences.length) die(`Wails bindings drift detected:\n${differences.join("\n")}`);
    log("Wails bindings: no drift");
  } finally {
    fs.rmSync(temporary, { recursive: true, force: true });
  }
}

function ldflags(kind, goos) {
  const flags = [
    "-s",
    "-w",
    `-X github.com/Hello-CTF/NexTerm/internal/version.Version=${VERSION}`,
    `-X github.com/Hello-CTF/NexTerm/internal/version.Commit=${COMMIT}`,
  ];
  if (kind === "desktop" && goos === "windows") flags.push("-H windowsgui");
  return flags.join(" ");
}

function defaultBinaryPath(kind, goos, goarch) {
  const suffix = goos === "windows" ? ".exe" : "";
  return path.join(ROOT, "target/go-build", `nexterm-${kind}-${goos}-${goarch}${suffix}`);
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
  const destination = path.resolve(ROOT, option("binary-manifest", `${file}.manifest.json`));
  fs.mkdirSync(path.dirname(destination), { recursive: true });
  fs.writeFileSync(destination, `${JSON.stringify(manifest, null, 2)}\n`);
  log(`${id} binary manifest: ${destination}`);
}

function validateBinaryForPackaging(outputPath, kind, goos, goarch) {
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
  if (problems.length) die(`--package-only binary manifest validation failed:\n${problems.map((problem) => `  - ${problem}`).join("\n")}`);
  log(`${kind}-${goos}-${goarch}: reusing verified production binary sha256:${manifest.artifact.sha256}`);
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
    if (kind !== "desktop") die("server packages are produced by scripts/pack-linux-server.sh");
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
  try {
    if (packageOnly) {
      validateBinaryForPackaging(outputPath, kind, goos, goarch);
    } else {
      run("go", args, { env: buildEnvironment(goos, goarch, kind) });
      if (flag("repro-check")) {
        const reproduction = `${outputPath}.repro`;
        const reproductionArgs = [...args];
        reproductionArgs[reproductionArgs.indexOf("-o") + 1] = reproduction;
        run("go", reproductionArgs, { env: buildEnvironment(goos, goarch, kind) });
        if (sha256(outputPath) !== sha256(reproduction)) die(`${kind} binary is not reproducible: ${outputPath} != ${reproduction}`);
        fs.rmSync(reproduction, { force: true });
        log(`${kind} ${goos}/${goarch} reproducibility: ${sha256(outputPath)}`);
      }
    }
  } finally {
    if (kind === "desktop" && release) fs.rmSync(embeddedRoot, { recursive: true, force: true });
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
    if (kind !== "desktop") die("server packages are produced by scripts/pack-linux-server.sh");
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
    return { format: "pe", architecture, static: null };
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
  try {
    inspected = inspectBinary(file);
    result.push(assertion("binary-format", inspected.format === ({ windows: "pe", darwin: "mach-o", linux: "elf" })[goos], JSON.stringify(inspected)));
    result.push(assertion("binary-architecture", inspected.architecture === goarch, `expected ${goos}/${goarch}; actual ${inspected.format}/${inspected.architecture}`));
    if (kind === "server" && goos === "linux") result.push(assertion("static-linux-server", inspected.static === true, "ELF PT_DYNAMIC must be absent"));
  } catch (error) {
    result.push(assertion("binary-format", false, String(error)));
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
  return { assertions: result, inspected };
}

function packageAssertions(kind, file, goos, goarch) {
  const data = fs.readFileSync(file);
  if (kind === "desktop-nsis") return [assertion("nsis-container", data[0] === 0x4d && data[1] === 0x5a, "NSIS output is not a PE executable")];
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
      const required = ["usr/bin/nexterm-desktop", "usr/share/applications/nexterm.desktop", "usr/share/icons/hicolor/256x256/apps/nexterm.png", "usr/share/doc/nexterm/copyright"];
      checks.push(assertion("linux-desktop-deb-members", goos === "linux" && listing !== null && required.every((member) => members.has(member)), `required members: ${required.join(", ")}`));
    } finally {
      fs.rmSync(work, { recursive: true, force: true });
    }
    return checks;
  }
  if (kind === "server-archive") {
    const checks = [assertion("gzip-container", data[0] === 0x1f && data[1] === 0x8b, "server archive is not gzip compressed")];
    const root = `NexTerm-server_${VERSION}_linux_${goarch}`;
    const required = [
      "nexterm-server", "nexterm-server.service", "nexterm-onlyserver.service",
      "nexterm.env.example", "onlyserver.env.example", "LICENSE", "README.md", "web/index.html",
    ].map((member) => `${root}/${member}`);
    const listing = output("tar", ["-tzf", file], { allowFailure: true });
    const members = new Set(listing?.split(/\r?\n/) || []);
    checks.push(assertion(
      "full-server-archive-members",
      goos === "linux" && listing !== null && required.every((member) => members.has(member)),
      `required members: ${required.join(", ")}`,
    ));
    return checks;
  }
  if (kind === "sync-archive") return [assertion("separate-sync-archive-removed", false, "restricted --sync-only is a runtime mode of the full archive, not a separate artifact")];
  if (kind === "desktop-dmg") return [assertion("dmg-container", data.length > 512, "DMG was already validated with hdiutil")];
  return [];
}

function officialRustBaseline(id) {
  const relative = option("rust-official-baseline", ".github/baselines/rust-official-v0.2.1.json");
  const absolute = path.resolve(ROOT, relative);
  if (!fs.existsSync(absolute)) return null;
  const document = JSON.parse(fs.readFileSync(absolute, "utf8"));
  const entry = document.artifacts?.[id];
  if (!entry?.size_bytes) return null;
  return {
    source: `${relative}#${id} (${document.release?.tag} ${entry.file}${entry.inner_path ? `#${entry.inner_path}` : ""})`,
    url: `${document.release?.base_url}/${entry.file}`,
    sha256: entry.sha256,
    provenance: "official-release",
    historical: false,
    size_bytes: entry.size_bytes,
    contents_difference: entry.contents_difference,
  };
}

function rustBaseline(id) {
  const official = officialRustBaseline(id);
  if (official) return official;
  return { status: "unavailable", reason: `no like-for-like recorded Rust baseline for ${id}` };
}

function customGoBaseline(id) {
  const relative = option("custom-go-baseline", ".github/baselines/custom-go.json");
  const absolute = path.resolve(ROOT, relative);
  if (!fs.existsSync(absolute)) return { status: "unavailable", reason: `custom-Go baseline does not exist: ${relative}` };
  const document = JSON.parse(fs.readFileSync(absolute, "utf8"));
  const entry = document.artifacts?.[id];
  if (!entry || !Number.isInteger(entry.size_bytes) || entry.size_bytes <= 0 || entry.like_for_like !== true || entry.feature_set !== "full-pre-eino") {
    return { status: "unavailable", reason: `no full like-for-like pre-Eino custom-Go measurement for ${id}` };
  }
  return { source: `${relative}#${id}`, size_bytes: entry.size_bytes };
}

function sizeComparisons(id, bytes) {
  const rust = rustBaseline(id);
  if (rust.size_bytes) {
    rust.go_bytes = bytes;
    rust.delta_bytes = bytes - rust.size_bytes;
    rust.go_to_rust_ratio = bytes / rust.size_bytes;
    rust.smaller = bytes < rust.size_bytes;
    rust.status = "measured";
  }
  rust.informational = true;
  rust.rule = "historical reference only; never a pass/fail gate";
  const custom = customGoBaseline(id);
  if (Number.isInteger(custom.size_bytes) && custom.size_bytes > 0) {
    custom.release_bytes = bytes;
    custom.eino_delta_bytes = bytes - custom.size_bytes;
    custom.status = "measured";
  }
  custom.informational = true;
  custom.rule = "full-Eino size impact reporting only; never a pass/fail gate";
  return { rust, custom_go: custom };
}

function sha256(file) {
  return crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex");
}

function writeArtifactReport({ id, kind, goos, goarch, file, cgo = "0", stripped = true, requireEmbedded = false, requireEvidence = false }) {
  if (!fs.existsSync(file) || !fs.statSync(file).isFile()) die(`artifact does not exist: ${file}`);
  const bytes = fs.statSync(file).size;
  const rawBinary = kind === "desktop" || kind === "server";
  const inspected = rawBinary
    ? binaryAssertions({ kind, goos, goarch, file, cgo, stripped, requireEmbedded })
    : { assertions: packageAssertions(kind, file, goos, goarch), inspected: null };
  const comparisons = sizeComparisons(id, bytes);
  const assertionsFailed = inspected.assertions.length === 0 || inspected.assertions.some((item) => item.status === "failed");
  const report = {
    schema_version: 2,
    id,
    kind,
    platform: { os: goos, arch: goarch, cgo: cgo === "1" ? "enabled-only-for-wails-platform-layer" : "disabled" },
    version: VERSION,
    commit: COMMIT,
    source_date_epoch: Number(SOURCE_DATE_EPOCH),
    artifact: { path: path.relative(ROOT, file), size_bytes: bytes, sha256: sha256(file), stripped },
    build: {
      go: output("go", ["env", "GOVERSION"], { allowFailure: true })?.trim() || "unknown",
      wails_cli: WAILS_VERSION,
      node: process.version,
      pnpm: output("pnpm", ["--version"], { allowFailure: true })?.trim() || "unknown",
      toolchain_pin: "CI pins Go 1.26.8, Node 24 and pnpm 11; local Node/pnpm drift must not change committed lockfile artifacts",
      module_mode: "-mod=readonly",
      reproducible_flags: ["-trimpath", "-buildvcs=false", "-ldflags=-s -w + version/commit"],
      runtime_metadata: "Go pclntab and build info are preserved for stack symbolization; no UPX and no manual symbol-table or pclntab stripping",
      dependency_accounting: "the complete composed Go module, including Eino where imported; no feature-removing build tags",
    },
    format: inspected.inspected,
    assertions: inspected.assertions,
    comparisons,
    status: assertionsFailed ? "failed" : "passed",
    feature_parity_claim: false,
    real_target_acceptance_claim: false,
  };
  const destination = path.resolve(ROOT, option("output", `${file}.artifact.json`));
  fs.mkdirSync(path.dirname(destination), { recursive: true });
  fs.writeFileSync(destination, `${JSON.stringify(report, null, 2)}\n`);
  log(`${id}: ${bytes} bytes; Rust=${comparisons.rust.status}; custom-Go=${comparisons.custom_go.status}; report=${destination}`);
  if (assertionsFailed) die(`${id} has failed artifact assertions; see ${destination}`);
  if (requireEvidence) requireReportPassed(report);
  return report;
}

function requireReportPassed(report) {
  const assertionsPassed = Array.isArray(report.assertions) && report.assertions.length > 0 && report.assertions.every((item) => item.status === "passed");
  const ready = report.status === "passed" && assertionsPassed;
  if (!ready) {
    die(`${report.id} is not release-ready (${report.status}); every artifact needs passing file/content/static/cgo checks, while Rust and custom-Go size comparisons stay informational and never gate release`);
  }
}

function packageDesktop(binary, goos, goarch) {
  if (goos !== HOST_OS) die(`native ${goos} packaging cannot run on ${HOST_OS}`);
  if (goos === "darwin") return packageDarwin(binary, goarch);
  if (goos === "windows") return packageWindows(binary, goarch);
  if (goos === "linux") return packageLinuxDesktop(binary, goarch);
  die(`no desktop package is contracted for ${goos}`);
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
  fs.copyFileSync(path.join(ROOT, "public/brand/nexterm-mark-256.png"), path.join(dataDir, "usr/share/icons/hicolor/256x256/apps/nexterm.png"));
  fs.copyFileSync(path.join(ROOT, "LICENSE"), path.join(dataDir, "usr/share/doc/nexterm/copyright"));
  fs.writeFileSync(path.join(dataDir, "usr/share/applications/nexterm.desktop"), "[Desktop Entry]\nType=Application\nName=NexTerm\nComment=SSH / SFTP / Docker / database operations terminal\nExec=nexterm-desktop\nIcon=nexterm\nTerminal=false\nCategories=System;TerminalEmulator;\n");
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
  const tarFlags = ["--sort=name", `--mtime=@${SOURCE_DATE_EPOCH}`, "--owner=0", "--group=0", "--numeric-owner"];
  run("tar", [...tarFlags, "-czf", path.join(work, "control.tar.gz"), "-C", path.join(work, "control"), "control"]);
  run("tar", [...tarFlags, "-czf", path.join(work, "data.tar.gz"), "-C", dataDir, "usr"]);
  fs.writeFileSync(path.join(work, "debian-binary"), "2.0\n");
  const deb = path.join(assets, `${name}.deb`);
  fs.rmSync(deb, { force: true });
  run("ar", ["rD", deb, path.join(work, "debian-binary"), path.join(work, "control.tar.gz"), path.join(work, "data.tar.gz")]);
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
  const icon = path.join(ROOT, "public/brand/icon.icns");
  if (!fs.existsSync(icon)) die("macOS icon.icns asset is missing");
  fs.copyFileSync(icon, path.join(app, "Contents/Resources/icon.icns"));
  const plist = `<?xml version="1.0" encoding="UTF-8"?>\n<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">\n<plist version="1.0"><dict>\n<key>CFBundleDisplayName</key><string>NexTerm</string>\n<key>CFBundleExecutable</key><string>NexTerm</string>\n<key>CFBundleIdentifier</key><string>${wailsConfig.info.productIdentifier}</string>\n<key>CFBundleIconFile</key><string>icon.icns</string>\n<key>CFBundleName</key><string>NexTerm</string>\n<key>CFBundlePackageType</key><string>APPL</string>\n<key>CFBundleShortVersionString</key><string>${VERSION}</string>\n<key>CFBundleVersion</key><string>${VERSION}</string>\n<key>LSMinimumSystemVersion</key><string>12.0</string>\n<key>NSHighResolutionCapable</key><true/>\n</dict></plist>\n`;
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

// 公证是可选增强:只有配置了 App Store Connect API 密钥(notarytool key 认证)才提交;
// 自签证书无法通过公证,未配置凭据时保持 ad-hoc 分发现状。
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
  run("wails3", ["generate", "webview2bootstrapper", "-dir", work]);
  const bootstrapper = path.join(work, "MicrosoftEdgeWebview2Setup.exe");
  if (!fs.existsSync(bootstrapper)) die("Wails did not generate the WebView2 bootstrapper");
  const icon = path.join(ROOT, "public/brand/icon.ico");
  if (!fs.existsSync(icon)) die("Windows icon.ico asset is missing");
  const setupArch = goarch === "amd64" ? "x64" : "arm64";
  const installer = path.join(assets, `NexTerm_${VERSION}_${setupArch}-setup.exe`);
  const defines = [
    `NEXTERM_BINARY=${binary}`,
    `NEXTERM_VERSION=${VERSION}`,
    `NEXTERM_NUMERIC_VERSION=${VERSION.split(/[-+]/)[0]}.0`,
    `NEXTERM_OUT=${installer}`,
    `NEXTERM_ICON=${icon}`,
    `NEXTERM_WEBVIEW2=${bootstrapper}`,
    `NEXTERM_EXE_NAME=${path.basename(binary)}`,
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
  const flavor = option("flavor", "full");
  const defaultID = kind === "desktop" || kind === "server"
    ? `${kind}-${goos}-${goarch}`
    : kind === "server-archive"
      ? `server-archive-${flavor}-${goos}-${goarch}`
      : `${kind}-${goos}-${goarch}`;
  const id = option("id", defaultID);
  if (kind === "server-archive" && flavor !== "full") die("only --flavor=full is published; --sync-only remains a runtime mode, not an archive");
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
    options.package = "true";
    options["repro-check"] = "true";
    options["require-evidence"] = "true";
    options.os = HOST_OS;
    options.arch = HOST_ARCH;
    options["skip-frontend"] = "true";
    buildBinary("desktop");
    break;
  }
  case "help":
  case "--help":
    console.log(HELP_TEXT);
    break;
  default:
    die(`unknown command ${command}; use debug, release, frontend, bindings, desktop, server, report, or help`);
}
