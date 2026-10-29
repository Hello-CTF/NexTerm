// Artifact assertions for binaries, helpers and release packages.
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { resolveTar } from "./tar.mjs";
import { canExecuteHelperMetadata, helperArchiveMembers, helperTarget, helperTargets, missingHelperArchiveMembers } from "../../.github/scripts/helper-matrix.mjs";
import { helperMetadataManifestProblems, loadManifest } from "./build-manifest.mjs";
import { HOST_ARCH, HOST_OS, LINUX_DESKTOP_ENTRY, ROOT, VERSION, archiveTool, option, output } from "./build-context.mjs";
import { defaultBinaryPath } from "./build-targets.mjs";
import { distManifest } from "./build-frontend.mjs";
import { helperBinaryMetadata, helperProtocolMetadata } from "./build-evidence.mjs";
import { privacyRuntimeAssertions, privacyRuntimeDirectory } from "./build-privacy.mjs";
import { glibcPortableProblems, inspectBinary } from "./build-inspect.mjs";
import { sha256, zipMembers } from "./build-report.mjs";
import { debVersion, linuxDesktopDepends } from "./build-package.mjs";

export function assertion(id, passed, detail) {
  return { id, status: passed ? "passed" : "failed", detail };
}

export function binaryAssertions({ kind, goos, goarch, file, cgo, stripped, requireEmbedded }) {
  const result = [];
  const target = kind === "helper" ? helperTarget(goos, goarch) : null;
  const expectedGOARCH = target?.goarch || goarch;
  let inspected;
  let helperMetadata;
  try {
    inspected = inspectBinary(file);
    result.push(assertion("binary-format", inspected.format === ({ windows: "pe", darwin: "mach-o", linux: "elf" })[goos], JSON.stringify(inspected)));
    result.push(assertion("binary-architecture", inspected.architecture === expectedGOARCH, `expected ${goos}/${goarch} (GOARCH=${expectedGOARCH}); actual ${inspected.format}/${inspected.architecture}`));
    if (kind === "desktop" && goos === "windows") result.push(assertion("windows-icon-resource", inspected.resourceRva > 0, inspected.resourceRva > 0 ? `PE resource directory RVA 0x${inspected.resourceRva.toString(16)}` : "PE resource directory is empty; the .syso icon resource was not linked"));
    if (kind === "agent" && goos === "linux") result.push(assertion("static-linux-agent", inspected.static === true, "ELF PT_DYNAMIC must be absent"));
    if (kind === "server" && goos === "linux") {
      const problems = glibcPortableProblems(inspected, goarch);
      result.push(assertion("glibc-portable-linux-server", problems.length === 0, problems.length === 0 ? "static, or glibc-only NEEDED with the standard interpreter and no GLIBC_ version requirement above 2.17" : problems.join("; ")));
    }
    if (kind === "helper" && goos === "linux") result.push(assertion("static-linux-helper", inspected.static === true, "ELF PT_DYNAMIC must be absent"));
  } catch (error) {
    result.push(assertion("binary-format", false, String(error)));
  }
  if (kind === "helper") {
    if (canExecuteHelperMetadata({ os: HOST_OS, arch: HOST_ARCH }, target)) {
      try {
        helperMetadata = helperBinaryMetadata(file, goos, goarch);
        result.push(assertion("helper-binary-metadata", true,
            `executed final ${goos}/${goarch} helper --metadata on matching real host ${HOST_OS}/${HOST_ARCH}: protocol=${helperMetadata.protocol}; sha256=${helperMetadata.sha256}`));
      } catch (error) {
        result.push(assertion("helper-binary-metadata", false, String(error)));
      }
    } else {
      result.push({
        id: "helper-binary-metadata",
        status: "not-run",
        detail: `not executed: host ${HOST_OS}/${HOST_ARCH} does not exactly match final target ${goos}/${goarch}; matching real-host execution is required`,
      });
    }
  }
  if (kind === "desktop" || kind === "server") result.push(...privacyRuntimeAssertions(privacyRuntimeDirectory(file, goos, goarch), goos, goarch));
  const moduleInfo = output("go", ["version", "-m", file], { allowFailure: true });
  result.push(assertion("cgo-boundary", Boolean(moduleInfo?.includes(`CGO_ENABLED=${cgo}`)), `expected CGO_ENABLED=${cgo} in go version -m`));
  if (target?.goarm) result.push(assertion("goarm", moduleInfo?.includes(`GOARM=${target.goarm}`) === true, `expected GOARM=${target.goarm} in go version -m`));
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
  const expectedMetadata = helperProtocolMetadata();
  for (const target of helperTargets()) {
    const packaged = path.join(directory, relativeDirectory, target.name);
    const input = path.resolve(ROOT, option("helpers-dir", "target/go-build"), target.name);
    const label = `packaged-helper-${target.os}-${target.arch}`;
    try {
      const packagedStat = fs.lstatSync(packaged);
      if (!packagedStat.isFile()) throw new Error(`${target.name} is not a regular file`);
      const inputStat = fs.statSync(input);
      if (!inputStat.isFile()) throw new Error(`packaging input ${target.name} is not a regular file`);
      const inspected = inspectBinary(packaged);
      const packagedDigest = sha256(packaged);
      const inputDigest = sha256(input);
      const expectedFormat = { windows: "pe", darwin: "mach-o", linux: "elf" }[target.os];
      checks.push(assertion(`${label}-binary`, inspected.format === expectedFormat && inspected.architecture === target.goarch && (target.os !== "linux" || inspected.static === true),
          `expected ${target.os}/${target.arch} ${expectedFormat}/GOARCH=${target.goarch}${target.os === "linux" ? " static" : ""}; actual ${JSON.stringify(inspected)}`));
      if (target.goarm) {
        const moduleInfo = output("go", ["version", "-m", packaged], { allowFailure: true });
        checks.push(assertion(`${label}-goarm`, moduleInfo?.includes(`GOARM=${target.goarm}`) === true, `expected GOARM=${target.goarm} in go version -m`));
      }
      checks.push(assertion(`${label}-sha256`, packagedDigest === inputDigest,
          `package SHA256=${packagedDigest}; input SHA256=${inputDigest}`));
      const manifest = loadManifest(`${input}.manifest.json`);
      const evidenceProblems = helperMetadataManifestProblems({ manifest, goos: target.os, goarch: target.arch, protocol: expectedMetadata.protocol });
      const evidenceDetail = evidenceProblems.length > 0 ? evidenceProblems.join("; ") : manifest.binary_metadata_verified === true
        ? `verified metadata protocol=${manifest.protocol}; sha256=${manifest.sha256}`
        : "untested: cross-compiled; metadata not-run";
      checks.push(assertion(`${label}-evidence`, evidenceProblems.length === 0, evidenceDetail));
      if (checkMode) checks.push(assertion(`${label}-executable`, (packagedStat.mode & 0o111) !== 0,
          `package mode=${(packagedStat.mode & 0o777).toString(8)}; executable bit required`));
    } catch (error) {
      checks.push(assertion(`${label}-inspect`, false, String(error)));
    }
  }
  return checks;
}

function packagedDarwinHelperLinkAssertions(mountpoint) {
  const checks = [];
  for (const target of helperTargets()) {
    const label = `packaged-helper-${target.os}-${target.arch}-resource-link`;
    const link = path.join(mountpoint, "NexTerm.app/Contents/MacOS", target.name);
    const payload = path.join(mountpoint, "NexTerm.app/Contents/Resources/helpers", target.name);
    const expected = `../Resources/helpers/${target.name}`;
    try {
      const stat = fs.lstatSync(link);
      const actual = stat.isSymbolicLink() ? fs.readlinkSync(link) : "not a symbolic link";
      checks.push(assertion(label, stat.isSymbolicLink() && actual === expected && fs.realpathSync(link) === fs.realpathSync(payload), `expected ${expected}; actual ${actual}`));
    } catch (error) {
      checks.push(assertion(label, false, String(error)));
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
      ? output(resolveTar(), ["-xzf", path.resolve(file), "-C", work, member], { allowFailure: true })
      : output(archiveTool(), ["extract", path.resolve(file), work], { allowFailure: true });
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

export function packageAssertions(kind, file, goos, goarch) {
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
      if (extracted !== null) {
        checks.push(...packagedHelperAssertions(work, "", false));
        checks.push(...privacyRuntimeAssertions(path.join(work, "ai-runtime"), goos, goarch));
      }
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
      const control = output(resolveTar(), ["-xzOf", path.join(work, "control.tar.gz"), "control"], { allowFailure: true }) ?? "";
      checks.push(assertion(
          "linux-desktop-deb-control",
          control.includes("Package: nexterm\n") && control.includes(`Version: ${debVersion(VERSION)}\n`) && control.includes(`Architecture: ${goarch}\n`),
          "control file must carry the package name, release version and architecture",
      ));
      const listing = output(resolveTar(), ["-tzf", path.join(work, "data.tar.gz")], { allowFailure: true });
      const members = new Set(listing?.split(/\r?\n/) || []);
      const required = ["usr/bin/nexterm-desktop", LINUX_DESKTOP_ENTRY, "usr/share/icons/hicolor/256x256/apps/nexterm.png", "usr/share/doc/nexterm/copyright"];
      const missingHelpers = missingHelperArchiveMembers(members, "usr/bin/");
      checks.push(assertion("linux-desktop-deb-members", goos === "linux" && listing !== null && missingHelpers.length === 0 && required.every((member) => members.has(member)), `required members: ${[...required, ...helperArchiveMembers("usr/bin/")].join(", ")}; missing helpers: ${missingHelpers.join(", ") || "none"}`));
      const unpacked = output(resolveTar(), ["-xzf", path.join(work, "data.tar.gz"), "-C", work,
        ...helperArchiveMembers("usr/bin/"), "usr/bin/ai-runtime", "usr/bin/nexterm-desktop"], { allowFailure: true });
      checks.push(assertion("linux-desktop-deb-helper-extract", unpacked !== null, "all nine helpers must extract from data.tar.gz"));
      if (unpacked !== null) {
        const depends = linuxDesktopDepends(path.join(work, "usr/bin/nexterm-desktop"));
        checks.push(assertion("linux-desktop-deb-depends", control.includes(`\nDepends: ${depends}\n`), `control Depends must match the packaged binary: ${depends}`));
        checks.push(...packagedHelperAssertions(work, "usr/bin", true));
        checks.push(...privacyRuntimeAssertions(path.join(work, "usr/bin/ai-runtime"), goos, goarch));
      }
    } finally {
      fs.rmSync(work, { recursive: true, force: true });
    }
    return checks;
  }
  if (kind === "server-archive") {
    const checks = [assertion("gzip-container", data[0] === 0x1f && data[1] === 0x8b, "server archive is not gzip compressed")];
    const root = `nexterm-server_linux_${goarch}_${VERSION}`;
    const required = [
      "nexterm-server", "nexterm-server.service", "nexterm-onlyserver.service",
      "nexterm.env.example", "onlyserver.env.example", "LICENSE", "README.md", "web/index.html",
    ].map((member) => `${root}/${member}`);
    const listing = output(resolveTar(), ["-tzf", file], { allowFailure: true });
    const members = new Set(listing?.split(/\r?\n/) || []);
    const missingHelpers = missingHelperArchiveMembers(members, `${root}/`);
    checks.push(assertion(
        "full-server-archive-members",
        goos === "linux" && listing !== null && missingHelpers.length === 0 && required.every((member) => members.has(member)),
        `required members: ${[...required, ...helperArchiveMembers(`${root}/`)].join(", ")}; missing helpers: ${missingHelpers.join(", ") || "none"}`,
    ));
    const work = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-server-assert-"));
    try {
      const extracted = output(resolveTar(), ["-xzf", path.resolve(file), "-C", work,
        ...helperArchiveMembers(`${root}/`), `${root}/ai-runtime`], { allowFailure: true });
      checks.push(assertion("server-archive-helper-extract", extracted !== null, "all nine helpers must extract from the server archive"));
      if (extracted !== null) {
        checks.push(...packagedHelperAssertions(work, root, true));
        checks.push(...privacyRuntimeAssertions(path.join(work, root, "ai-runtime"), goos, goarch));
      }
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
      if (mounted) {
        checks.push(...packagedHelperAssertions(mountpoint, "NexTerm.app/Contents/Resources/helpers", true));
        checks.push(...packagedDarwinHelperLinkAssertions(mountpoint));
        checks.push(...privacyRuntimeAssertions(path.join(mountpoint, "NexTerm.app/Contents/Resources/ai-runtime"), goos, goarch, path.join(mountpoint, "NexTerm.app/Contents/Frameworks/ai-runtime")));
      }
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
    const listing = output(resolveTar(), ["-tzf", file], { allowFailure: true });
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
