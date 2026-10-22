#!/usr/bin/env node
// Mobile build source-state CLI shared by the Android and iOS Taskfiles.
//
//   node scripts/mobile-source.mjs commit
//   node scripts/mobile-source.mjs ldflags --platform=android|ios [--production=true|false]
//   node scripts/mobile-source.mjs stamp [--config=build/config.yml] [--out=build/config.stamp.yml]
//   node scripts/mobile-source.mjs config [--config=build/config.yml] [--out=build/config.stamp.yml]
//
// `stamp` writes the release version into an ignored generated config
// (build/config.stamp.yml) instead of the tracked build/config.yml, so a
// stamped release tree stays clean and the commit embedded by `ldflags`
// matches the frontend dist manifest exactly. `config` prints the config the
// wails generators should consume: the stamp when present, the tracked config
// otherwise.
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  MOBILE_CONFIG_PATH,
  MOBILE_STAMP_CONFIG_PATH,
  commitLabel,
  mobileLdflags,
  mobileVersion,
  resolveMobileConfigPath,
  sourceState,
  stampMobileConfig,
} from "./lib/mobile.mjs";

const HELP = `usage:
  node scripts/mobile-source.mjs commit
  node scripts/mobile-source.mjs ldflags --platform=android|ios [--production=true|false]
  node scripts/mobile-source.mjs stamp [--config=${MOBILE_CONFIG_PATH}] [--out=${MOBILE_STAMP_CONFIG_PATH}]
  node scripts/mobile-source.mjs config [--config=${MOBILE_CONFIG_PATH}] [--out=${MOBILE_STAMP_CONFIG_PATH}]`;

function main() {
  const [command, ...rest] = process.argv.slice(2);
  const options = {};
  for (const arg of rest) {
    if (!arg.startsWith("--")) throw new Error(`unknown argument: ${arg}\n${HELP}`);
    const [name, ...value] = arg.slice(2).split("=");
    options[name] = value.length ? value.join("=") : "true";
  }
  const root = process.cwd();
  const configPath = options.config || MOBILE_CONFIG_PATH;
  const stampPath = options.out || MOBILE_STAMP_CONFIG_PATH;
  switch (command) {
    case "commit":
      process.stdout.write(`${commitLabel(sourceState({ cwd: root }))}\n`);
      break;
    case "ldflags": {
      if (!options.platform) throw new Error(`ldflags requires --platform=android|ios\n${HELP}`);
      const production = options.production === "true" || options.production === "1";
      process.stdout.write(`${mobileLdflags({
        platform: options.platform,
        production,
        version: mobileVersion(process.env),
        commit: commitLabel(sourceState({ cwd: root })),
      })}\n`);
      break;
    }
    case "stamp": {
      const version = process.env.NEXTERM_RELEASE_VERSION || "";
      if (!version) {
        // A stale stamp must not leak into unrelated local builds.
        fs.rmSync(path.resolve(root, stampPath), { force: true });
        process.stdout.write(`mobile stamp: NEXTERM_RELEASE_VERSION is unset; ${stampPath} left absent\n`);
        break;
      }
      const count = stampMobileConfig({
        configPath: path.resolve(root, configPath),
        outPath: path.resolve(root, stampPath),
        version,
      });
      process.stdout.write(`${stampPath}: stamped ${count} version field${count === 1 ? "" : "s"} with ${version}; tracked ${configPath} is untouched\n`);
      break;
    }
    case "config":
      process.stdout.write(`${resolveMobileConfigPath({ root, configPath, stampPath })}\n`);
      break;
    default:
      throw new Error(`unknown command ${command || "(missing)"}\n${HELP}`);
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    main();
  } catch (error) {
    console.error(`mobile-source: ${error.message}`);
    process.exitCode = 1;
  }
}
