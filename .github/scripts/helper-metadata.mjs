import { spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const HOST_OS = { darwin: "darwin", linux: "linux", win32: "windows" }[process.platform];
const HOST_ARCH = { arm: "arm", arm64: "arm64", loong64: "loong64", riscv64: "riscv64", x64: "amd64" }[process.arch];
const REPRESENTATIVE_TARGETS = new Set(["linux/amd64", "windows/amd64", "darwin/arm64"]);

export function requiresHelperExecutionEvidence(goos, goarch) {
  return REPRESENTATIVE_TARGETS.has(`${goos}/${goarch}`);
}

export function readHostHelperProtocolMetadata({ cwd = ROOT, env = process.env } = {}) {
  const goMod = fs.readFileSync(path.join(cwd, "go.mod"), "utf8");
  const toolchain = goMod.match(/^toolchain\s+(go\d+\.\d+\.\d+)\s*$/m)?.[1];
  if (!toolchain) throw new Error("go.mod must declare an exact Go toolchain");
  if (!HOST_OS || !HOST_ARCH) throw new Error(`unsupported host for helper source metadata: ${process.platform}/${process.arch}`);
  const commandEnv = { ...env, GOOS: HOST_OS, GOARCH: HOST_ARCH, CGO_ENABLED: "0", GOTOOLCHAIN: toolchain };
  delete commandEnv.GOARM;
  const result = spawnSync("go", ["run", "-mod=readonly", "./cmd/nexterm-helper", "--metadata"], {
    cwd,
    env: commandEnv,
    encoding: "utf8",
  });
  if (result.error) throw new Error(`cannot run host helper source metadata: ${result.error.message}`);
  if (result.status !== 0 || result.signal) {
    throw new Error(`host helper source metadata exited with ${result.status ?? result.signal}: ${(result.stderr || "").trim()}`);
  }
  let metadata;
  try {
    metadata = JSON.parse(result.stdout);
  } catch (error) {
    throw new Error(`host helper source metadata is not valid JSON: ${error.message}`);
  }
  if (!Number.isInteger(metadata.protocol) || metadata.protocol <= 0) throw new Error(`host helper source metadata has invalid protocol ${metadata.protocol}`);
  if (!Array.isArray(metadata.features) || metadata.features.length === 0 || metadata.features.some((feature) => typeof feature !== "string" || feature === "")) {
    throw new Error("host helper source metadata features must be non-empty strings");
  }
  return { protocol: metadata.protocol, features: [...metadata.features].sort() };
}
