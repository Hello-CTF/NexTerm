#!/usr/bin/env node
import { spawnSync } from "node:child_process";

const target = JSON.parse(process.env.NATIVE_TARGET);
for (const kind of target.kinds) {
  const args = ["scripts/build.mjs", kind, "--release", `--os=${target.os}`, `--arch=${target.arch}`];
  if (process.env.REPRO_CHECK === "true") args.push("--repro-check");
  if (kind === "desktop") args.push("--consume-dist=target/ci-work/verified-dist/dist", "--dist-manifest=target/ci-work/verified-dist/target/dist-manifest.json");
  if (kind === "helper" && target.representative) args.push("--require-evidence");
  const result = spawnSync(process.execPath, args, { stdio: "inherit" });
  if (result.error) throw result.error;
  if (result.status !== 0) process.exit(result.status || 1);
}
