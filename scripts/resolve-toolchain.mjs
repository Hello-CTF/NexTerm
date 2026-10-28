#!/usr/bin/env node

// Resolves the ONNX Runtime version once so every job of a workflow run builds
// with the same runtime. NEXTERM_ORT_VERSION, when set, pins a specific
// release. Prints "ort_version=<v>" for $GITHUB_OUTPUT.
import { pathToFileURL } from "node:url";
import path from "node:path";
import { resolveVersion } from "./ai-privacy-runtime.mjs";

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  process.stdout.write(`ort_version=${await resolveVersion()}\n`);
}
