// Helper metadata, binary manifests, package-only validation and reproducibility checks.
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileDigest } from "./privacy-runtime.mjs";
import { readHostHelperProtocolMetadata } from "../../.github/scripts/helper-metadata.mjs";
import { canExecuteHelperMetadata, helperTarget } from "../../.github/scripts/helper-matrix.mjs";
import { binaryManifestProblems, createBinaryManifest, helperMetadataManifestProblems, loadManifest } from "./build-manifest.mjs";
import { COMMIT, HOST_ARCH, HOST_OS, RELEASE_REF, ROOT, SOURCE_DATE_EPOCH, VERSION, die, log, option, output, run } from "./build-context.mjs";
import { buildEnvironment, compileEnvironment } from "./build-targets.mjs";
import { desktopMinisignPublicKeyDigest } from "./build-ldflags.mjs";
import { privacyRuntimeDirectory } from "./build-privacy.mjs";
import { sha256 } from "./build-report.mjs";

let cachedHelperProtocolMetadata;
const cachedHelperBinaryMetadata = new Map();

export function helperProtocolMetadata() {
  if (cachedHelperProtocolMetadata) return cachedHelperProtocolMetadata;
  try {
    cachedHelperProtocolMetadata = readHostHelperProtocolMetadata({
      cwd: ROOT,
      env: { ...buildEnvironment(HOST_OS, HOST_ARCH, "helper"), SOURCE_DATE_EPOCH: String(SOURCE_DATE_EPOCH), TZ: "UTC" },
    });
  } catch (error) {
    die(error.message);
  }
  return cachedHelperProtocolMetadata;
}

export function helperBinaryMetadata(file, goos, goarch) {
  if (cachedHelperBinaryMetadata.has(file)) return cachedHelperBinaryMetadata.get(file);
  const target = helperTarget(goos, goarch);
  if (!canExecuteHelperMetadata({ os: HOST_OS, arch: HOST_ARCH }, target)) {
    throw new Error(`cannot execute final ${goos}/${goarch} helper --metadata on ${HOST_OS}/${HOST_ARCH}; a matching real host is required`);
  }
  const raw = output(file, ["--metadata"], { allowFailure: true, timeout: 30000 });
  if (raw === null) throw new Error(`cannot execute final ${goos}/${goarch} helper --metadata on the matching host`);
  let metadata;
  try {
    metadata = JSON.parse(raw);
  } catch (error) {
    throw new Error(`final ${goos}/${goarch} helper --metadata is not JSON: ${error.message}`);
  }
  const expected = helperProtocolMetadata();
  const artifactSHA256 = sha256(file);
  if (metadata.version !== VERSION || metadata.commit !== COMMIT || metadata.protocol !== expected.protocol ||
      Object.hasOwn(metadata, "features") || metadata.sha256 !== artifactSHA256) {
    throw new Error(`final ${goos}/${goarch} helper metadata does not match this build and artifact sha256: ${JSON.stringify(metadata)}`);
  }
  const verified = {
    protocol: metadata.protocol,
    sha256: metadata.sha256,
    binary_metadata_verified: true,
    metadata_host: { os: HOST_OS, arch: HOST_ARCH },
  };
  cachedHelperBinaryMetadata.set(file, verified);
  return verified;
}

export function writeBinaryManifest(file, { id, kind, goos, goarch, tags, cgo, stripped }) {
  const target = kind === "helper" ? helperTarget(goos, goarch) : null;
  const manifest = createBinaryManifest({
    file,
    id,
    kind,
    goos,
    goarch,
    goarm: target?.goarm || "",
    tags: tags ? tags.split(",") : [],
    cgo,
    stripped,
    version: VERSION,
    commit: COMMIT,
    source_date_epoch: Number(SOURCE_DATE_EPOCH),
  });
  if (kind === "desktop" || kind === "server") manifest.privacy_runtime_sha256 = fileDigest(path.join(privacyRuntimeDirectory(file, goos, goarch), "runtime.json"));
  if (kind === "desktop") {
    manifest.source_ref = RELEASE_REF;
    manifest.updater_public_key_sha256 = desktopMinisignPublicKeyDigest();
  }
  if (kind === "helper") {
    if (canExecuteHelperMetadata({ os: HOST_OS, arch: HOST_ARCH }, target)) {
      try {
        Object.assign(manifest, helperBinaryMetadata(file, goos, goarch));
      } catch (error) {
        manifest.binary_metadata_verified = false;
        log(`helper binary metadata verification failed: ${error.message}`);
      }
    } else {
      Object.assign(manifest, { binary_metadata_verified: false, metadata_status: "not-run", cross_compiled: true, untested: true });
      log(`helper final ${goos}/${goarch} --metadata not executed on ${HOST_OS}/${HOST_ARCH}; recorded as untested cross-compiled output`);
    }
  }
  const destination = path.resolve(ROOT, option("binary-manifest", `${file}.manifest.json`));
  fs.mkdirSync(path.dirname(destination), { recursive: true });
  fs.writeFileSync(destination, `${JSON.stringify(manifest, null, 2)}\n`);
  log(`${id} binary manifest: ${destination}`);
}

export function validateBinaryForPackaging(outputPath, kind, goos, goarch, reuseEvidence) {
  if (!fs.existsSync(outputPath) || !fs.statSync(outputPath).isFile()) {
    die(`--package-only requires the existing production binary: ${outputPath}`);
  }
  const manifestPath = path.resolve(ROOT, option("binary-manifest", `${outputPath}.manifest.json`));
  let manifest;
  try {
    manifest = loadManifest(manifestPath);
  } catch (error) {
    die(`--package-only: ${error.message}`);
  }
  const problems = binaryManifestProblems({
    manifest,
    file: outputPath,
    expect: {
      id: `${kind}-${goos}-${goarch}`,
      kind,
      goos,
      goarch,
      goarm: kind === "helper" ? helperTarget(goos, goarch).goarm : "",
      tags: ["production"],
      cgo: buildEnvironment(goos, goarch, kind).CGO_ENABLED,
      stripped: true,
      version: VERSION,
      commit: COMMIT,
    },
  });
  if (kind === "desktop") {
    if (manifest.source_ref !== reuseEvidence.sourceRef) {
      problems.push(`binary manifest source_ref ${JSON.stringify(manifest.source_ref)} does not match ${reuseEvidence.sourceRef}`);
    }
    if (manifest.updater_public_key_sha256 !== reuseEvidence.updaterPublicKeySHA256) {
      problems.push(`binary manifest updater public key SHA-256 ${JSON.stringify(manifest.updater_public_key_sha256)} does not match ${reuseEvidence.updaterPublicKeySHA256}`);
    }
  }
  if (kind === "helper") {
    const expected = helperProtocolMetadata();
    problems.push(...helperMetadataManifestProblems({ manifest, goos, goarch, protocol: expected.protocol }));
  }
  if (problems.length) die(`--package-only binary manifest validation failed:\n${problems.map((problem) => `  - ${problem}`).join("\n")}`);
  if (kind === "helper" && manifest.binary_metadata_verified === false) {
    log(`${kind}-${goos}-${goarch}: reusing untested cross-compiled helper; metadata not-run; sha256:${manifest.artifact.sha256}`);
  } else {
    log(`${kind}-${goos}-${goarch}: reusing verified production binary sha256:${manifest.artifact.sha256}`);
  }
}

export function reproduceBinary(args, outputPath, kind, goos, goarch) {
  const temporary = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-go-repro-"));
  const checkout = path.join(temporary, "checkout");
  const cache = path.join(temporary, "fresh-go-cache");
  const reproduction = `${outputPath}.repro`;
  try {
    fs.mkdirSync(checkout, { recursive: true });
    for (const name of ["go.mod", "go.sum"]) {
      fs.copyFileSync(path.join(ROOT, name), path.join(checkout, name));
    }
    for (const name of ["cmd", "internal"]) {
      fs.cpSync(path.join(ROOT, name), path.join(checkout, name), { recursive: true });
    }
    const reproductionArgs = [...args];
    reproductionArgs[reproductionArgs.indexOf("-o") + 1] = reproduction;
    run("go", reproductionArgs, {
      cwd: checkout,
      env: { ...compileEnvironment(goos, goarch, kind), GOCACHE: cache },
    });
    if (sha256(outputPath) !== sha256(reproduction)) die(`${kind} binary is not reproducible across checkout paths with a fresh GOCACHE: ${outputPath} != ${reproduction}`);
    log(`${kind} ${goos}/${goarch} cross-checkout reproducibility with fresh GOCACHE: ${sha256(outputPath)}`);
  } finally {
    fs.rmSync(temporary, { recursive: true, force: true });
  }
}
