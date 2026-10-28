// Native desktop packaging: Linux deb, macOS dmg, Windows NSIS installer.
import fs from "node:fs";
import path from "node:path";
import { runtimeLibrary, fileDigest } from "./privacy-runtime.mjs";
import { resolveMakensis } from "./makensis.mjs";
import { helperTargets } from "../../.github/scripts/helper-matrix.mjs";
import { loadManifest } from "./build-manifest.mjs";
import { HOST_OS, LINUX_DESKTOP_ENTRY, ROOT, SOURCE_DATE_EPOCH, VERSION, VERSION_INFO, archiveTool, die, log, option, run, wailsConfig } from "./build-context.mjs";
import { runWails } from "./build-wails.mjs";
import { validateBinaryForPackaging } from "./build-evidence.mjs";
import { copyPrivacyRuntime, privacyRuntimeDirectory } from "./build-privacy.mjs";
import { inspectBinary } from "./build-inspect.mjs";

export function packageDesktop(binary, goos, goarch) {
  if (goos !== HOST_OS) die(`native ${goos} packaging cannot run on ${HOST_OS}`);
  for (const target of helperTargets()) verifiedHelper(target);
  if (goos === "darwin") return packageDarwin(binary, goarch);
  if (goos === "windows") return packageWindows(binary, goarch);
  if (goos === "linux") return packageLinuxDesktop(binary, goarch);
  die(`no desktop package is contracted for ${goos}`);
}

function verifiedHelper(target) {
  const file = path.resolve(ROOT, option("helpers-dir", "target/go-build"), target.name);
  validateBinaryForPackaging(file, "helper", target.os, target.arch);
  return file;
}

function copyHelpers(directory) {
  for (const target of helperTargets()) {
    const destination = path.join(directory, target.name);
    fs.copyFileSync(verifiedHelper(target), destination);
    fs.chmodSync(destination, 0o755);
  }
}

function copyDarwinHelpers(app) {
  const resources = path.join(app, "Contents/Resources/helpers");
  const macos = path.join(app, "Contents/MacOS");
  fs.mkdirSync(resources, { recursive: true });
  copyHelpers(resources);
  for (const target of helperTargets()) fs.symlinkSync(`../Resources/helpers/${target.name}`, path.join(macos, target.name));
}

export function debVersion(version) {
  return version.replace("-", "~");
}

// The cgo desktop links the glibc of its Debian 13 build host, so the package
// declares the newest GLIBC_ symbol version the binary requires and dpkg rejects
// older systems. GTK has no symbol versions; Wails calls GTK 4.14 API
// (gdk_monitor_get_scale), so that floor is declared explicitly.
export function linuxDesktopDepends(binary) {
  let floor = null;
  for (const { version } of inspectBinary(binary).versionRequirements ?? []) {
    const match = /^GLIBC_(\d+)\.(\d+)(?:\.(\d+))?$/.exec(version);
    if (!match) continue;
    const parts = match.slice(1).map((part) => Number(part ?? 0));
    if (!floor || (parts[0] - floor[0] || parts[1] - floor[1] || parts[2] - floor[2]) > 0) floor = parts;
  }
  const libc = floor ? `libc6 (>= ${(floor[2] ? floor : floor.slice(0, 2)).join(".")})` : null;
  return [libc, "libgtk-4-1 (>= 4.14)", "libwebkitgtk-6.0-4t64 | libwebkitgtk-6.0-4"].filter(Boolean).join(", ");
}

function packageLinuxDesktop(binary, goarch) {
  const assets = path.resolve(ROOT, option("assets-dir", "target/release-assets"));
  const name = `nexterm-desktop_linux_${goarch}_${VERSION}`;
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
  copyPrivacyRuntime(binary, path.join(dataDir, "usr/bin/ai-runtime"), "linux", goarch);
  fs.copyFileSync(path.join(ROOT, "public/brand/nexterm-mark-256.png"), path.join(dataDir, "usr/share/icons/hicolor/256x256/apps/nexterm.png"));
  fs.copyFileSync(path.join(ROOT, "LICENSE"), path.join(dataDir, "usr/share/doc/nexterm/copyright"));
  fs.writeFileSync(path.join(dataDir, LINUX_DESKTOP_ENTRY), `[Desktop Entry]\nType=Application\nName=NexTerm\nComment=SSH / SFTP / Docker / database operations terminal\nExec=nexterm-desktop %u\nIcon=nexterm\nTerminal=false\nCategories=System;TerminalEmulator;\nMimeType=x-scheme-handler/nexterm;\nStartupWMClass=${wailsConfig.info.productIdentifier}\n`);
  fs.writeFileSync(path.join(work, "control", "control"), `Package: nexterm\nVersion: ${debVersion(VERSION)}\nSection: net\nPriority: optional\nArchitecture: ${goarch}\nMaintainer: Probius <https://github.com/Hello-CTF/NexTerm>\nHomepage: https://github.com/Hello-CTF/NexTerm\nDepends: ${linuxDesktopDepends(binary)}\nRecommends: fonts-noto-color-emoji\nDescription: Browser-based SSH / SFTP / Docker / database operations terminal\n Terminal processes, sessions and workspace layout stay on your own server;\n credentials are encrypted with a vault key you control.\n`);
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
  for (const target of helperTargets()) fs.chmodSync(path.join(dataDir, "usr/bin", target.name), 0o755);
  run(archiveTool(), ["tar", path.join(work, "control.tar.gz"), path.join(work, "control"), "control", String(SOURCE_DATE_EPOCH)]);
  run(archiveTool(), ["tar", path.join(work, "data.tar.gz"), dataDir, "usr", String(SOURCE_DATE_EPOCH)]);
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
  copyDarwinHelpers(app);
  const runtimeDirectory = path.join(app, "Contents/Frameworks/ai-runtime");
  const runtimeResources = path.join(app, "Contents/Resources/ai-runtime");
  copyPrivacyRuntime(binary, runtimeResources, "darwin", goarch);
  const runtimeAvailable = fs.existsSync(path.join(runtimeResources, runtimeLibrary("darwin")));
  if (runtimeAvailable) {
    fs.mkdirSync(runtimeDirectory, { recursive: true });
    fs.renameSync(path.join(runtimeResources, runtimeLibrary("darwin")), path.join(runtimeDirectory, runtimeLibrary("darwin")));
  }
  const icon = path.join(ROOT, "public/brand/icon.icns");
  if (!fs.existsSync(icon)) die("macOS icon.icns asset is missing");
  fs.copyFileSync(icon, path.join(app, "Contents/Resources/icon.icns"));
  const plist = `<?xml version="1.0" encoding="UTF-8"?>\n<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">\n<plist version="1.0"><dict>\n<key>CFBundleDisplayName</key><string>NexTerm</string>\n<key>CFBundleExecutable</key><string>NexTerm</string>\n<key>CFBundleIdentifier</key><string>${wailsConfig.info.productIdentifier}</string>\n<key>CFBundleIconFile</key><string>icon.icns</string>\n<key>CFBundleName</key><string>NexTerm</string>\n<key>CFBundlePackageType</key><string>APPL</string>\n<key>CFBundleShortVersionString</key><string>${VERSION}</string>\n<key>CFBundleURLTypes</key><array><dict>\n<key>CFBundleURLName</key><string>${wailsConfig.info.productIdentifier}</string>\n<key>CFBundleURLSchemes</key><array><string>nexterm</string></array>\n</dict></array>\n<key>CFBundleVersion</key><string>${VERSION}</string>\n<key>LSMinimumSystemVersion</key><string>12.0</string>\n<key>NSHighResolutionCapable</key><true/>\n</dict></plist>\n`;
  fs.writeFileSync(path.join(app, "Contents/Info.plist"), plist);
  run("plutil", ["-lint", path.join(app, "Contents/Info.plist")]);
  const signIdentity = process.env.NEXTERM_MACOS_SIGN_IDENTITY || "-";
  const signArgs = ["--force", "--sign", signIdentity];
  // The hardened runtime enforces library validation; the entitlement lets the app dlopen its bundled ONNX Runtime.
  if (signIdentity !== "-") signArgs.push("--options", "runtime", "--timestamp", "--entitlements", path.join(ROOT, "build/darwin/entitlements.plist"));
  run("codesign", [...signArgs, "--deep", app]);
  // Signing changes Mach-O bytes; retain provenance and record the signed hashes.
  const runtimeManifestPath = path.join(runtimeResources, "runtime.json");
  const runtimeManifest = loadManifest(runtimeManifestPath);
  runtimeManifest.unsigned_manifest_sha256 = fileDigest(runtimeManifestPath);
  if (runtimeAvailable) runtimeManifest.files[runtimeLibrary("darwin")] = fileDigest(path.join(runtimeDirectory, runtimeLibrary("darwin")));
  fs.writeFileSync(runtimeManifestPath, `${JSON.stringify(runtimeManifest, null, 2)}\n`);
  run("codesign", [...signArgs, app]);
  run("codesign", ["--verify", "--deep", "--strict", app]);
  fs.symlinkSync("/Applications", path.join(work, "Applications"));
  fs.mkdirSync(assets, { recursive: true });
  const dmg = path.join(assets, `nexterm-desktop_macos_${goarch}_${VERSION}.dmg`);
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
  const installer = path.join(assets, `nexterm-desktop_windows_${goarch}_${VERSION}.exe`);
  const defines = [
    `NEXTERM_BINARY=${binary}`,
    `NEXTERM_AI_RUNTIME=${privacyRuntimeDirectory(binary, "windows", goarch)}`,
    `NEXTERM_VERSION=${VERSION}`,
    `NEXTERM_NUMERIC_VERSION=${VERSION_INFO.core}.0`,
    `NEXTERM_OUT=${installer}`,
    `NEXTERM_ICON=${icon}`,
    `NEXTERM_WEBVIEW2=${bootstrapper}`,
    `NEXTERM_EXE_NAME=${path.basename(binary)}`,
    ...helperTargets().map((target) => `NEXTERM_HELPER_${target.os.toUpperCase()}_${target.arch.toUpperCase()}=${verifiedHelper(target)}`),
  ];
  const makensis = resolveMakensis({ env: process.env });
  log(`makensis: ${makensis.command} (${makensis.source})`);
  run(makensis.command, [...defines.map((define) => `-D${define}`), path.join(ROOT, ".github/packaging/windows/NexTerm.nsi")]);
  if (!fs.existsSync(installer)) die("makensis did not produce the contracted installer");
  return installer;
}
