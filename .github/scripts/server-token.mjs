#!/usr/bin/env node
import { spawnSync } from "node:child_process";

export function fetchServerSyncToken(binary, dataDir, env) {
  const result = spawnSync(binary, ["token", "--data-dir", dataDir], { env, encoding: "utf8" });
  if (result.error) {
    throw new Error(`nexterm-server token could not be spawned: ${result.error.code ?? "unknown error code"}`);
  }
  if (result.status !== 0) {
    throw new Error(`nexterm-server token failed with exit status ${result.status}${result.signal ? ` (signal ${result.signal})` : ""}`);
  }
  const stdout = result.stdout ?? "";
  const token = stdout.endsWith("\n") ? stdout.slice(0, -1) : stdout;
  if (!token || token.includes("\n") || token.includes("\r") || token.trim() !== token) {
    throw new Error("nexterm-server token stdout must be a single non-empty line");
  }
  return token;
}
