#!/usr/bin/env node
import { androidVersionCode, parseReleaseVersion, releaseVersionProblem } from "./lib/semver.mjs";

const args = process.argv.slice(2);
const value = args.length === 2 && args[0] === "--android-json" ? args[1] : args.length === 1 ? args[0] : null;
if (value === null) {
  console.error(`check-semver: usage: check-semver.mjs [--android-json] <version>, got ${JSON.stringify(args)}`);
  process.exit(1);
}
const problem = releaseVersionProblem(value);
if (problem) {
  console.error(`check-semver: ${problem}, got ${JSON.stringify(value)}`);
  process.exit(1);
}
if (args[0] === "--android-json") {
  const parsed = parseReleaseVersion(value);
  let versionCode;
  try {
    versionCode = androidVersionCode(parsed);
  } catch (error) {
    console.error(`check-semver: ${error.message}`);
    process.exit(1);
  }
  process.stdout.write(`${JSON.stringify({ versionName: parsed.version, versionCode })}\n`);
}
