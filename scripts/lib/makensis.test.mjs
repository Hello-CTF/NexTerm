/**
 * Executable tests for the deterministic makensis discovery used by
 * scripts/build.mjs packageWindows. Run with: node --test scripts/lib/
 *
 * The resolver is a pure function over injected env/fs, so the Windows
 * scenarios run on any host. The fixtures reproduce release run 37149307116:
 * the windows-latest job had a PATH without makensis but the Chocolatey
 * nsis.install layout at ProgramFiles(x86)\NSIS, while the windows-11-arm
 * job resolved makensis straight from PATH (image preinstall).
 */

import assert from "node:assert/strict";
import test from "node:test";

import { resolveMakensis } from "./makensis.mjs";

/** Case-insensitive existsSync stub mirroring Windows file semantics. */
function fakeWindowsFs(existingPaths) {
  const existing = new Set(existingPaths.map((entry) => entry.toLowerCase()));
  return (candidate) => existing.has(String(candidate).toLowerCase());
}

const NSIS_X86 = "C:\\Program Files (x86)\\NSIS\\makensis.exe";

test("a makensis.exe on PATH resolves with the PATH source", () => {
  const resolved = resolveMakensis({
    env: { PATH: "C:\\Windows\\system32;C:\\Program Files (x86)\\NSIS" },
    existsSync: fakeWindowsFs([NSIS_X86]),
  });
  assert.deepEqual(resolved, { command: NSIS_X86, source: "PATH" });
});

test("a lowercase Path env key is honored like PATH", () => {
  const resolved = resolveMakensis({
    env: { Path: "C:\\tools\\nsis" },
    existsSync: fakeWindowsFs(["C:\\tools\\nsis\\makensis.exe"]),
  });
  assert.deepEqual(resolved, { command: "C:\\tools\\nsis\\makensis.exe", source: "PATH" });
});

test("the windows-latest release scenario resolves the nsis.install default without a PATH hit", () => {
  // Run 37149307116 windows/amd64: choco deployed nsis 3.11 to
  // C:\Program Files (x86)\NSIS, the job PATH never learned about it.
  const resolved = resolveMakensis({
    env: {
      PATH: "C:\\Windows\\system32;C:\\Windows;C:\\Program Files\\nodejs",
      "ProgramFiles(x86)": "C:\\Program Files (x86)",
      ProgramFiles: "C:\\Program Files",
      ChocolateyInstall: "C:\\ProgramData\\chocolatey",
    },
    existsSync: fakeWindowsFs([NSIS_X86]),
  });
  assert.deepEqual(resolved, {
    command: NSIS_X86,
    source: "NSIS default install under ProgramFiles(x86)",
  });
});

test("the windows-11-arm release scenario keeps resolving from PATH", () => {
  // Run 37149307116 windows/arm64 passed because the image preinstalls NSIS
  // and PATH already carries its directory; that path must keep winning.
  const resolved = resolveMakensis({
    env: {
      PATH: "C:\\Program Files (x86)\\NSIS;C:\\Windows\\system32",
      "ProgramFiles(x86)": "C:\\Program Files (x86)",
    },
    existsSync: fakeWindowsFs([NSIS_X86]),
  });
  assert.deepEqual(resolved, { command: NSIS_X86, source: "PATH" });
});

test("a PATH hit wins over the NSIS default install directory", () => {
  const resolved = resolveMakensis({
    env: {
      PATH: "D:\\custom\\nsis;C:\\Windows\\system32",
      "ProgramFiles(x86)": "C:\\Program Files (x86)",
    },
    existsSync: fakeWindowsFs(["D:\\custom\\nsis\\makensis.exe", NSIS_X86]),
  });
  assert.deepEqual(resolved, { command: "D:\\custom\\nsis\\makensis.exe", source: "PATH" });
});

test("the native ProgramFiles NSIS layout is used when ProgramFiles(x86) has none", () => {
  const resolved = resolveMakensis({
    env: {
      PATH: "C:\\Windows\\system32",
      "ProgramFiles(x86)": "C:\\Program Files (x86)",
      ProgramFiles: "C:\\Program Files",
    },
    existsSync: fakeWindowsFs(["C:\\Program Files\\NSIS\\makensis.exe"]),
  });
  assert.deepEqual(resolved, {
    command: "C:\\Program Files\\NSIS\\makensis.exe",
    source: "NSIS default install under ProgramFiles",
  });
});

test("a Chocolatey bin shim is the last resort before failing", () => {
  const resolved = resolveMakensis({
    env: {
      PATH: "C:\\Windows\\system32",
      "ProgramFiles(x86)": "C:\\Program Files (x86)",
      ProgramFiles: "C:\\Program Files",
      ChocolateyInstall: "D:\\choco",
    },
    existsSync: fakeWindowsFs(["D:\\choco\\bin\\makensis.exe"]),
  });
  assert.deepEqual(resolved, { command: "D:\\choco\\bin\\makensis.exe", source: "Chocolatey bin shim" });
});

test("ChocolateyInstall unset falls back to the documented C:\\ProgramData\\chocolatey default", () => {
  const resolved = resolveMakensis({
    env: { PATH: "C:\\Windows\\system32" },
    existsSync: fakeWindowsFs(["C:\\ProgramData\\chocolatey\\bin\\makensis.exe"]),
  });
  assert.deepEqual(resolved, {
    command: "C:\\ProgramData\\chocolatey\\bin\\makensis.exe",
    source: "Chocolatey bin shim",
  });
});

test("missing makensis fails with every probed location in the message", () => {
  assert.throws(
    () => resolveMakensis({
      env: {
        PATH: "C:\\Windows\\system32;D:\\tools",
        "ProgramFiles(x86)": "C:\\Program Files (x86)",
        ProgramFiles: "C:\\Program Files",
      },
      existsSync: fakeWindowsFs([]),
    }),
    (error) => {
      assert.match(error.message, /makensis was not found/);
      assert.match(error.message, /C:\\Windows\\system32\\makensis\.exe/);
      assert.match(error.message, /D:\\tools\\makensis\.exe/);
      assert.match(error.message, /C:\\Program Files \(x86\)\\NSIS\\makensis\.exe/);
      assert.match(error.message, /C:\\Program Files\\NSIS\\makensis\.exe/);
      assert.match(error.message, /C:\\ProgramData\\chocolatey\\bin\\makensis\.exe/);
      return true;
    },
  );
});

test("an empty or unset PATH does not crash the fallback probes", () => {
  const resolved = resolveMakensis({
    env: { "ProgramFiles(x86)": "C:\\Program Files (x86)" },
    existsSync: fakeWindowsFs([NSIS_X86]),
  });
  assert.deepEqual(resolved, {
    command: NSIS_X86,
    source: "NSIS default install under ProgramFiles(x86)",
  });
});
