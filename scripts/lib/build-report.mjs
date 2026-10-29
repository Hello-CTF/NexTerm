// Artifact report writing and release-readiness gate.
import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { helperTarget } from "../../.github/scripts/helper-matrix.mjs";
import { COMMIT, DIRTY, GO_TOOLCHAIN, ROOT, RSRC_VERSION, SOURCE_COMMIT, SOURCE_DATE_EPOCH, VERSION, WAILS_VERSION, die, log, option, output, packageJSON } from "./build-context.mjs";
import { binaryAssertions, packageAssertions } from "./build-assertions.mjs";

export function zipMembers(data) {
  const eocdMin = 22;
  if (data.length < eocdMin) return null;
  const searchFloor = Math.max(0, data.length - (eocdMin + 0xffff));
  let eocd = -1;
  for (let index = data.length - eocdMin; index >= searchFloor; index -= 1) {
    if (data.readUInt32LE(index) === 0x06054b50) {
      eocd = index;
      break;
    }
  }
  if (eocd < 0) return null;
  const count = data.readUInt16LE(eocd + 10);
  let offset = data.readUInt32LE(eocd + 16);
  const names = [];
  for (let index = 0; index < count; index += 1) {
    if (offset + 46 > data.length || data.readUInt32LE(offset) !== 0x02014b50) return null;
    const nameLength = data.readUInt16LE(offset + 28);
    const extraLength = data.readUInt16LE(offset + 30);
    const commentLength = data.readUInt16LE(offset + 32);
    names.push(data.toString("utf8", offset + 46, offset + 46 + nameLength));
    offset += 46 + nameLength + extraLength + commentLength;
  }
  return names;
}

export function sha256(file) {
  return crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex");
}

export function writeArtifactReport({ id, kind, goos, goarch, file, cgo = "0", stripped = true, requireEmbedded = false, requireEvidence = false }) {
  if (!fs.existsSync(file) || !fs.statSync(file).isFile()) die(`artifact does not exist: ${file}`);
  const bytes = fs.statSync(file).size;
  const rawBinary = kind === "desktop" || kind === "server" || kind === "agent" || kind === "helper";
  const inspected = rawBinary
      ? binaryAssertions({ kind, goos, goarch, file, cgo, stripped, requireEmbedded })
      : { assertions: packageAssertions(kind, file, goos, goarch), inspected: null };
  const assertionsFailed = inspected.assertions.length === 0 || inspected.assertions.some((item) => item.status === "failed");
  const metadataPending = inspected.assertions.some((item) => item.status === "not-run");
  const target = kind === "helper" ? helperTarget(goos, goarch) : null;
  const report = {
    schema_version: 2,
    id,
    kind,
    platform: { os: goos, arch: goarch, cgo: cgo === "1" ? "enabled-only-for-wails-platform-layer" : "disabled", ...(target?.goarm ? { goarm: target.goarm } : {}) },
    version: VERSION,
    commit: COMMIT,
    source_commit: SOURCE_COMMIT,
    dirty_source_tree: DIRTY,
    source_date_epoch: Number(SOURCE_DATE_EPOCH),
    artifact: { path: path.relative(ROOT, file), size_bytes: bytes, sha256: sha256(file), stripped },
    build: {
      go: output("go", ["env", "GOVERSION"], { allowFailure: true })?.trim() || "unknown",
      wails_cli: WAILS_VERSION,
      windows_resource: `rsrc ${RSRC_VERSION} embeds public/brand/icon.ico as rsrc_windows_<arch>.syso for windows desktop binaries`,
      node: process.version,
      pnpm: output("pnpm", ["--version"], { allowFailure: true })?.trim() || "unknown",
      toolchain_pin: `${GO_TOOLCHAIN} from go.mod; Node 24+; ${packageJSON.packageManager || "pnpm version supplied by CI"}; Wails ${WAILS_VERSION} pinned for generation`,
      module_mode: "-mod=readonly",
      reproducible_flags: ["-trimpath", "-buildvcs=false", "-ldflags=-s -w + version/commit"],
      runtime_metadata: "Go pclntab and build info are preserved for stack symbolization; no UPX and no manual symbol-table or pclntab stripping",
      dependency_accounting: "the complete composed Go module, including Eino where imported; no feature-removing build tags",
    },
    format: inspected.inspected,
    assertions: inspected.assertions,
    status: assertionsFailed ? "failed" : metadataPending ? "cross-compiled" : "passed",
  };
  if (kind === "helper" && metadataPending) Object.assign(report, { metadata_status: "not-run", untested: true });
  if (kind === "helper" && inspected.helperMetadata) Object.assign(report, inspected.helperMetadata);
  const destination = path.resolve(ROOT, option("output", `${file}.artifact.json`));
  fs.mkdirSync(path.dirname(destination), { recursive: true });
  fs.writeFileSync(destination, `${JSON.stringify(report, null, 2)}\n`);
  log(`${id}: ${bytes} bytes; report=${destination}`);
  if (assertionsFailed) die(`${id} has failed artifact assertions; see ${destination}`);
  if (requireEvidence) requireReportPassed(report);
  return report;
}

export function requireReportPassed(report) {
  const assertionsPassed = Array.isArray(report.assertions) && report.assertions.length > 0 && report.assertions.every((item) => item.status === "passed");
  const ready = report.status === "passed" && assertionsPassed;
  if (!ready) {
    die(`${report.id} is not release-ready (${report.status}); every artifact needs passing file/content/static/cgo checks and required representative helper metadata evidence`);
  }
}
