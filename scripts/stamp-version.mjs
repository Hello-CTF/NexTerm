#!/usr/bin/env node
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { parseReleaseVersion } from "./lib/semver.mjs";

export function stampVersion(files, version) {
  if (!parseReleaseVersion(version || "")) {
    throw new Error(`release version must be a non-placeholder semver: ${version || "missing"}`);
  }
  const updates = files.map((file) => {
    const text = fs.readFileSync(file, "utf8");
    let count = 0;
    const updated = text.replace(/(version:\s*")0\.0\.0(")/g, (_match, prefix, suffix) => {
      count += 1;
      return `${prefix}${version}${suffix}`;
    });
    if (count === 0) throw new Error(`version stamp replaced nothing in ${file}`);
    return { file, updated, count };
  });
  for (const update of updates) {
    fs.writeFileSync(update.file, update.updated);
    console.log(`${update.file}: stamped ${update.count} version field${update.count === 1 ? "" : "s"} with ${version}`);
  }
}

function main() {
  const files = process.argv.slice(2);
  if (!files.length) throw new Error("usage: NEXTERM_RELEASE_VERSION=x.y.z node scripts/stamp-version.mjs FILE [...]");
  stampVersion(files, process.env.NEXTERM_RELEASE_VERSION);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    main();
  } catch (error) {
    console.error(`stamp-version: ${error.message}`);
    process.exitCode = 1;
  }
}
