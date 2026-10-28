#!/usr/bin/env node
import { HOST_ARCH, HOST_OS, VERSION, command, die, flag, option, options, targetOS } from "./lib/build-context.mjs";
import { buildFrontend, consumeDist } from "./lib/build-frontend.mjs";
import { verifyBindings } from "./lib/build-wails.mjs";
import { ldflags, writeMinisignPublicKey } from "./lib/build-ldflags.mjs";
import { buildBinary, buildHelpers, reportOnly } from "./lib/build-binary.mjs";

const HELP_TEXT = `#!/usr/bin/env node
/**
 * Go/Wails delivery entry point. Desktop and server builds stage the latest stable
 * ONNX Runtime 1.x library (NEXTERM_ORT_VERSION pins one) in ai-runtime/ (no model
 * weights are bundled). Only the Linux and macOS desktops use cgo (Wails); every other
 * binary is CGO_ENABLED=0. The Linux server loads ONNX Runtime through purego, so it is
 * dynamically linked but glibc-portable (libc/libdl/libpthread only, no GLIBC_ version
 * requirements); the Linux agent and helpers stay fully static.
 * Artifact versions come from NEXTERM_RELEASE_VERSION (set from the release
 * tag in CI); the wails.json/package.json versions are 0.0.0 placeholders.
 *
 *   node scripts/build.mjs                         # frontend + host debug desktop
 *   node scripts/build.mjs release                 # frontend + desktop with nine helpers and representative evidence
 *   node scripts/build.mjs helpers --release       # all nine targets; executes metadata only on matching hosts
 *   node scripts/build.mjs frontend --repro-check
 *   node scripts/build.mjs frontend --consume-dist=DIR --dist-manifest=PATH
 *   node scripts/build.mjs bindings
 *   node scripts/build.mjs desktop-ldflags --os=linux
 *   node scripts/build.mjs write-minisign-public-key --out=PATH
 *   node scripts/build.mjs desktop --release --os=darwin --arch=arm64 --package
 *   node scripts/build.mjs desktop --release --package --package-only --consume-dist=DIR \\
 *     --os=darwin --arch=arm64 --require-evidence
 * Package-only reuse also requires the exact NEXTERM_RELEASE_REF tag and updater public key.
 *   node scripts/build.mjs server --release --os=linux --arch=amd64
 *   node scripts/build.mjs agent --release --os=linux --arch=amd64
 *   node scripts/build.mjs helper --release --repro-check --os=linux --arch=arm64
 *   node scripts/build.mjs report --kind=server-archive --os=linux --arch=amd64 \\
 *     --file=target/release-assets/nexterm-server_linux_amd64_<version>.tar.gz --require-evidence
 `;


switch (command) {
  case "frontend":
    if (option("consume-dist")) {
      if (flag("repro-check")) die("--consume-dist replaces the frontend build; --repro-check belongs to the producing job");
      consumeDist();
    } else {
      buildFrontend({ repro: flag("repro-check"), base: option("base", "") });
    }
    break;
  case "bindings":
    verifyBindings();
    break;
  case "desktop":
  case "server":
  case "agent":
  case "helper":
    buildBinary(command);
    break;
  case "helpers":
    buildHelpers();
    break;
  case "report":
    reportOnly();
    break;
  case "debug":
    buildFrontend();
    options.release = "false";
    options.os = HOST_OS;
    options.arch = HOST_ARCH;
    options["skip-frontend"] = "true";
    buildBinary("desktop");
    break;
  case "release": {
    if (flag("skip-frontend")) die("release does not allow --skip-frontend; reproducible real assets are mandatory");
    const tag = process.env.GITHUB_REF_NAME;
    if (tag?.startsWith("v") && tag !== `v${VERSION}`) die(`tag ${tag} does not match release version v${VERSION} (set NEXTERM_RELEASE_VERSION to the tag version)`);
    buildFrontend({ repro: true });
    verifyBindings();
    options.release = "true";
    options["repro-check"] = "true";
    options["require-evidence"] = "true";
    options["skip-frontend"] = "true";
    options.package = "true";
    options.os = HOST_OS;
    options.arch = HOST_ARCH;
    buildBinary("desktop");
    break;
  }
  case "desktop-ldflags":
    console.log(ldflags("desktop", targetOS()));
    break;
  case "write-minisign-public-key":
    writeMinisignPublicKey();
    break;
  case "help":
  case "--help":
    console.log(HELP_TEXT);
    break;
  default:
    die(`unknown command ${command}; use debug, release, frontend, bindings, desktop, server, agent, helper, helpers, report, desktop-ldflags, write-minisign-public-key, or help`);
}
