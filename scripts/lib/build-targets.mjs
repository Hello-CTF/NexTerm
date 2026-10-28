// Target matrix validation and Go build environments.
import path from "node:path";
import { helperTarget, helperTargets } from "../../.github/scripts/helper-matrix.mjs";
import { GO_TOOLCHAIN, ROOT, die } from "./build-context.mjs";

export function assertTarget(kind, goos, goarch) {
  if (kind === "helper") {
    if (!helperTarget(goos, goarch)) die(`unsupported helper target ${goos}/${goarch}; expected one of ${helperTargets().map((target) => `${target.os}/${target.arch}`).join(", ")}`);
    return;
  }
  const allowed = {
    "windows/amd64": ["desktop", "server", "agent"],
    "windows/arm64": ["desktop", "agent"],
    "darwin/amd64": ["desktop", "server", "agent"],
    "darwin/arm64": ["desktop", "server", "agent"],
    "linux/amd64": ["desktop", "server", "agent"],
    "linux/arm64": ["desktop", "server", "agent"],
  };
  if (!allowed[`${goos}/${goarch}`]?.includes(kind)) {
    die(`unsupported ${kind} target ${goos}/${goarch}; desktop is Windows/macOS/Linux amd64+arm64, server is Linux amd64+arm64, and agent is Windows/macOS/Linux amd64+arm64`);
  }
}

export function buildEnvironment(goos, goarch, kind) {
  const cgo = kind === "desktop" && (goos === "darwin" || goos === "linux") ? "1" : "0";
  const target = kind === "helper" ? helperTarget(goos, goarch) : null;
  const env = {
    ...process.env,
    GOOS: goos,
    GOARCH: target?.goarch || goarch,
    CGO_ENABLED: cgo,
    GOTOOLCHAIN: GO_TOOLCHAIN,
  };
  if (target?.goarm) env.GOARM = target.goarm;
  else delete env.GOARM;
  if (goos === "darwin") {
    env.MACOSX_DEPLOYMENT_TARGET = "12.0";
    env.CGO_CFLAGS = "-mmacosx-version-min=12.0";
    env.CGO_LDFLAGS = "-mmacosx-version-min=12.0";
  }
  return env;
}

export function defaultBinaryPath(kind, goos, goarch) {
  if (kind === "helper") return path.join(ROOT, "target/go-build", helperTarget(goos, goarch).name);
  const suffix = goos === "windows" ? ".exe" : "";
  return path.join(ROOT, "target/go-build", `nexterm-${kind}-${goos}-${goarch}${suffix}`);
}

export function compileEnvironment(goos, goarch, kind) {
  const env = buildEnvironment(goos, goarch, kind);
  return env;
}
