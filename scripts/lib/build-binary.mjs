// Per-kind binary build flow (desktop, server, agent, helper), helper matrix and report command.
import fs from "node:fs";
import path from "node:path";
import { helperTargets } from "../../.github/scripts/helper-matrix.mjs";
import { ROOT, RSRC_VERSION, command, die, flag, option, options, run, targetArch, targetOS } from "./build-context.mjs";
import { assertTarget, buildEnvironment, compileEnvironment, defaultBinaryPath } from "./build-targets.mjs";
import { buildFrontend, consumeDist, distManifest, treeHashOrDie } from "./build-frontend.mjs";
import { desktopReuseEvidence, ldflags } from "./build-ldflags.mjs";
import { reproduceBinary, validateBinaryForPackaging, writeBinaryManifest } from "./build-evidence.mjs";
import { preparePrivacyRuntime } from "./build-privacy.mjs";
import { requireReportPassed, writeArtifactReport } from "./build-report.mjs";
import { packageDesktop } from "./build-package.mjs";

function stageWindowsResource(goarch) {
  const icon = path.join(ROOT, "public/brand/icon.ico");
  if (!fs.existsSync(icon)) die("Windows icon.ico asset is missing");
  const syso = path.join(ROOT, "cmd/nexterm-desktop", `rsrc_windows_${goarch}.syso`);
  fs.rmSync(syso, { force: true });
  run("go", ["run", `github.com/akavel/rsrc@${RSRC_VERSION}`, "-arch", goarch, "-ico", icon, "-o", syso]);
  return syso;
}

export function buildBinary(kind) {
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
      run("go", args, { env: compileEnvironment(goos, goarch, kind) });
      if (flag("repro-check")) {
        reproduceBinary(args, outputPath, kind, goos, goarch);
        fs.rmSync(`${outputPath}.repro`, { force: true });
      }
    }
  } finally {
    if (kind === "desktop" && release) fs.rmSync(embeddedRoot, { recursive: true, force: true });
    if (windowsResource) fs.rmSync(windowsResource, { force: true });
  }
  if (kind === "desktop" || kind === "server") preparePrivacyRuntime(outputPath, goos, goarch, packageOnly);
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

export function reportOnly() {
  const file = path.resolve(ROOT, option("file", ""));
  const goos = targetOS();
  const goarch = targetArch();
  const kind = option("kind", "server-archive");
  const id = option("id", `${kind}-${goos}-${goarch}`);
  if (!options.file) die("report requires --file=PATH");
  writeArtifactReport({ id, kind, goos, goarch, file, cgo: option("cgo", "0"), requireEvidence: flag("require-evidence"), stripped: true });
}

export function buildHelpers() {
  for (const name of ["os", "arch", "out", "binary-manifest", "output"]) {
    if (options[name] !== undefined) die(`helpers builds the full matrix and does not accept --${name}; use helper for one target`);
  }
  if (flag("package") || flag("package-only")) die("helpers produces binaries only; package each desktop/server with verified matching-host helper evidence");
  for (const target of helperTargets()) {
    options.os = target.os;
    options.arch = target.arch;
    options.out = path.join(option("helpers-dir", "target/go-build"), target.name);
    buildBinary("helper");
  }
  delete options.out;
}
