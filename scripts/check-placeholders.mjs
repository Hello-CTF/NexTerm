#!/usr/bin/env node
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const wails = JSON.parse(fs.readFileSync(path.join(ROOT, "wails.json"), "utf8")).info?.version;
const pkg = JSON.parse(fs.readFileSync(path.join(ROOT, "package.json"), "utf8")).version;
const lazyCat = fs.readFileSync(path.join(ROOT, "lazycat/package.yml"), "utf8");
const buildConfig = fs.readFileSync(path.join(ROOT, "build/config.yml"), "utf8");
const problems = [];
if (wails !== "0.0.0") problems.push(`wails.json info.version=${wails}`);
if (pkg !== "0.0.0") problems.push(`package.json version=${pkg}`);
if (!/^version:\s*0\.0\.0\s*$/m.test(lazyCat)) problems.push("lazycat/package.yml version is not 0.0.0");
const buildVersions = [...buildConfig.matchAll(/^  version:\s*"([^"]+)"\s*$/gm)].map((match) => match[1]);
if (buildVersions.length !== 2 || buildVersions.some((version) => version !== "0.0.0")) {
  problems.push(`build/config.yml info/ios versions must both be 0.0.0; got ${JSON.stringify(buildVersions)}`);
}
if (problems.length) {
  console.error(`placeholder guard failed:\n${problems.map((problem) => `  - ${problem}`).join("\n")}`);
  process.exitCode = 1;
} else {
  console.log("version placeholders: wails.json, package.json, lazycat/package.yml and build/config.yml are 0.0.0");
}
