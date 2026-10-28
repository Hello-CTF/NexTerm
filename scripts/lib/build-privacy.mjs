// Per-target privacy runtime staging and assertions.
import fs from "node:fs";
import path from "node:path";
import { verifyRuntime } from "./privacy-runtime.mjs";
import { loadManifest } from "./build-manifest.mjs";
import { ROOT, die, option, run } from "./build-context.mjs";
import { inspectBinary } from "./build-inspect.mjs";
import { assertion } from "./build-assertions.mjs";

// Staged per target so builds for another os/arch never overwrite each other;
// packages and archives map it back to the plain ai-runtime name.
export function privacyRuntimeDirectory(binary, goos, goarch) {
  if (!goos || !goarch) die(`privacy runtime target is missing for ${binary}`);
  return path.join(path.dirname(binary), `ai-runtime-${goos}-${goarch}`);
}

export function preparePrivacyRuntime(binary, goos, goarch, packageOnly) {
  const directory = privacyRuntimeDirectory(binary, goos, goarch);
  if (!packageOnly) {
    fs.rmSync(directory, { recursive: true, force: true });
    run(process.execPath, ["scripts/ai-privacy-runtime.mjs", `--os=${goos}`, `--arch=${goarch}`, `--output=${directory}`]);
  }
  const expected = packageOnly ? loadManifest(path.resolve(ROOT, option("binary-manifest", `${binary}.manifest.json`))).privacy_runtime_sha256 : undefined;
  if (packageOnly && !expected) die("producing binary manifest lacks privacy runtime evidence");
  verifyRuntime(directory, goos, goarch, expected);
}

export function copyPrivacyRuntime(binary, destination, goos, goarch) {
  const manifest = loadManifest(path.resolve(ROOT, option("binary-manifest", `${binary}.manifest.json`)));
  if (!manifest.privacy_runtime_sha256) die("producing binary manifest lacks privacy runtime evidence");
  verifyRuntime(privacyRuntimeDirectory(binary, goos, goarch), goos, goarch, manifest.privacy_runtime_sha256);
  fs.cpSync(privacyRuntimeDirectory(binary, goos, goarch), destination, { recursive: true });
}

export function privacyRuntimeAssertions(directory, goos, goarch, libraryDirectory = directory) {
  try {
    const { manifest, library, sizeBytes, unavailable } = verifyRuntime(directory, goos, goarch, undefined, libraryDirectory);
    if (unavailable) return [{ ...assertion("privacy-runtime", true, `privacy runtime unavailable for ${goos}/${goarch}: ${unavailable}`), evidence: { unavailable, onnx_runtime: manifest.onnxRuntime ?? null } }];
    const file = path.join(libraryDirectory, library);
    const binary = inspectBinary(file);
    if (binary.architecture !== goarch || binary.format !== ({ darwin: "mach-o", linux: "elf", windows: "pe" })[goos]) throw new Error(`Privacy runtime architecture mismatch: ${library}`);
    const evidence = { library, onnx_runtime: manifest.onnxRuntime ?? null, size_bytes: sizeBytes, sha256: manifest.files[library] };
    const detail = `ONNX Runtime ${evidence.onnx_runtime} ${library}: ${sizeBytes} bytes, architecture, SHA256 and third-party licenses verified`;
    return [{ ...assertion("privacy-runtime", true, detail), evidence }];
  } catch (error) {
    return [assertion("privacy-runtime", false, error.message)];
  }
}
