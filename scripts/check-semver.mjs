#!/usr/bin/env node
import { parseReleaseVersion } from "./lib/semver.mjs";

const args = process.argv.slice(2);
if (args.length === 2 && args[0] === "--android-json") {
  const parsed = parseReleaseVersion(args[1]);
  if (!parsed) {
    console.error(`check-semver: expected a non-0.0.0 semantic version without a leading v, got ${JSON.stringify(args[1])}`);
    process.exit(1);
  }
  const major = Number(parsed.major);
  const minor = Number(parsed.minor);
  const patch = Number(parsed.patch);
  if (minor > 999 || patch > 999) {
    console.error("check-semver: Android versionCode requires minor and patch versions no greater than 999");
    process.exit(1);
  }
  const versionCode = major * 1000000 + minor * 1000 + patch;
  if (!Number.isSafeInteger(versionCode) || versionCode < 1 || versionCode > 2100000000) {
    console.error(`check-semver: Android versionCode ${versionCode} is outside the supported range 1..2100000000`);
    process.exit(1);
  }
  process.stdout.write(`${JSON.stringify({ versionName: parsed.version, versionCode })}\n`);
} else if (args.length !== 1 || !parseReleaseVersion(args[0])) {
  console.error(`check-semver: expected a non-0.0.0 semantic version without a leading v, got ${JSON.stringify(args)}`);
  process.exit(1);
}
