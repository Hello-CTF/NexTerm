import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { requiresHelperExecutionEvidence } from "../../.github/scripts/helper-metadata.mjs";

function digest(content) {
  return crypto.createHash("sha256").update(content).digest("hex");
}

function hashTree(root) {
  const hash = crypto.createHash("sha256");
  const files = [];
  const visit = (directory) => {
    for (const entry of fs.readdirSync(directory, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
      const absolute = path.join(directory, entry.name);
      const relative = path.relative(root, absolute).replaceAll(path.sep, "/");
      if (entry.isDirectory()) visit(absolute);
      else if (entry.isFile()) {
        const content = fs.readFileSync(absolute);
        hash.update(relative);
        hash.update("\0");
        hash.update(content);
        hash.update("\0");
        files.push({ path: relative, size_bytes: content.length, sha256: digest(content) });
      } else throw new Error(`frontend output contains a non-regular file: ${relative}`);
    }
  };
  visit(root);
  return { tree_hash: hash.digest("hex"), files };
}

export function treeHash(root) {
  return hashTree(root).tree_hash;
}

export function createDistManifest(root, { version, commit }) {
  const { tree_hash: treeHashValue, files } = hashTree(root);
  return {
    schema_version: 1,
    kind: "frontend-dist",
    version,
    commit,
    tree_hash: treeHashValue,
    files,
  };
}

export function distManifestProblems(root, manifest, { version, commit }) {
  if (!manifest || typeof manifest !== "object" || Array.isArray(manifest)) return ["dist manifest is not a JSON object"];
  const problems = [];
  if (manifest.schema_version !== 1) problems.push(`dist manifest schema_version must be 1, got ${JSON.stringify(manifest.schema_version)}`);
  if (manifest.kind !== "frontend-dist") problems.push(`dist manifest kind must be frontend-dist, got ${JSON.stringify(manifest.kind)}`);
  if (manifest.version !== version) problems.push(`dist manifest version ${JSON.stringify(manifest.version)} does not match ${version}`);
  if (manifest.commit !== commit) problems.push(`dist manifest commit ${JSON.stringify(manifest.commit)} does not match ${commit}`);
  if (!Array.isArray(manifest.files)) {
    problems.push("dist manifest files must be an array");
    return problems;
  }
  let actual;
  try {
    actual = hashTree(root);
  } catch (error) {
    problems.push(error.message);
    return problems;
  }
  const actualByPath = new Map(actual.files.map((file) => [file.path, file]));
  const expectedPaths = new Set();
  for (const entry of manifest.files) {
    if (!entry || typeof entry.path !== "string") {
      problems.push(`dist manifest contains a file entry without a path: ${JSON.stringify(entry)}`);
      continue;
    }
    expectedPaths.add(entry.path);
    const actualFile = actualByPath.get(entry.path);
    if (!actualFile) {
      problems.push(`dist file is missing: ${entry.path}`);
      continue;
    }
    if (actualFile.size_bytes !== entry.size_bytes || actualFile.sha256 !== entry.sha256) {
      problems.push(`dist file does not match the manifest: ${entry.path} (expected sha256:${entry.sha256} size:${entry.size_bytes}, actual sha256:${actualFile.sha256} size:${actualFile.size_bytes})`);
    }
  }
  for (const file of actual.files) {
    if (!expectedPaths.has(file.path)) problems.push(`dist contains a file absent from the manifest: ${file.path}`);
  }
  if (actual.tree_hash !== manifest.tree_hash) {
    problems.push(`dist tree hash mismatch: expected sha256:${manifest.tree_hash}, actual sha256:${actual.tree_hash}`);
  }
  return problems;
}

export function createBinaryManifest({ file, id, kind: _kind, goos, goarch, goarm = "", tags, cgo, stripped, version, commit, source_date_epoch: sourceDateEpoch }) {
  const content = fs.readFileSync(file);
  return {
    schema_version: 1,
    kind: "go-binary",
    id,
    platform: { os: goos, arch: goarch, cgo, ...(goarm ? { goarm } : {}) },
    tags: [...tags].sort(),
    stripped,
    version,
    commit,
    source_date_epoch: sourceDateEpoch,
    artifact: { size_bytes: content.length, sha256: digest(content) },
  };
}

export function binaryManifestProblems({ manifest, file, expect }) {
  if (!manifest || typeof manifest !== "object" || Array.isArray(manifest)) return ["binary manifest is not a JSON object"];
  const problems = [];
  if (manifest.schema_version !== 1) problems.push(`binary manifest schema_version must be 1, got ${JSON.stringify(manifest.schema_version)}`);
  if (manifest.kind !== "go-binary") problems.push(`binary manifest kind must be go-binary, got ${JSON.stringify(manifest.kind)}`);
  if (manifest.id !== expect.id) problems.push(`binary manifest id ${JSON.stringify(manifest.id)} does not match ${expect.id}`);
  const platform = manifest.platform && typeof manifest.platform === "object" ? manifest.platform : {};
  if (platform.os !== expect.goos) problems.push(`binary manifest os ${JSON.stringify(platform.os)} does not match ${expect.goos}`);
  if (platform.arch !== expect.goarch) problems.push(`binary manifest arch ${JSON.stringify(platform.arch)} does not match ${expect.goarch}`);
  if (platform.cgo !== expect.cgo) problems.push(`binary manifest cgo ${JSON.stringify(platform.cgo)} does not match ${expect.cgo}`);
  const expectedGOARM = expect.goarm || "";
  const actualGOARM = platform.goarm || "";
  if (actualGOARM !== expectedGOARM) problems.push(`binary manifest GOARM ${JSON.stringify(actualGOARM)} does not match ${JSON.stringify(expectedGOARM)}`);
  const expectedTags = [...expect.tags].sort();
  if (!Array.isArray(manifest.tags)) {
    problems.push("binary manifest tags must be an array");
  } else {
    const tags = [...manifest.tags].sort();
    if (tags.join(",") !== expectedTags.join(",")) {
      problems.push(`binary manifest tags ${JSON.stringify(manifest.tags)} do not match ${JSON.stringify(expectedTags)}`);
    }
  }
  if (manifest.stripped !== expect.stripped) problems.push(`binary manifest stripped ${JSON.stringify(manifest.stripped)} does not match ${JSON.stringify(expect.stripped)}`);
  if (manifest.version !== expect.version) problems.push(`binary manifest version ${JSON.stringify(manifest.version)} does not match ${expect.version}`);
  if (manifest.commit !== expect.commit) problems.push(`binary manifest commit ${JSON.stringify(manifest.commit)} does not match ${expect.commit}`);
  if (!fs.existsSync(file) || !fs.statSync(file).isFile()) {
    problems.push(`binary does not exist: ${file}`);
    return problems;
  }
  const content = fs.readFileSync(file);
  if (manifest.artifact?.size_bytes !== content.length) problems.push(`binary size mismatch: manifest ${manifest.artifact?.size_bytes} bytes, actual ${content.length} bytes`);
  if (manifest.artifact?.sha256 !== digest(content)) problems.push(`binary sha256 mismatch: manifest ${manifest.artifact?.sha256}, actual ${digest(content)}`);
  return problems;
}

export function helperMetadataManifestProblems({ manifest, goos, goarch, protocol }) {
  if (!manifest || typeof manifest !== "object" || Array.isArray(manifest)) return ["helper metadata manifest is not a JSON object"];
  const problems = [];
  const representative = requiresHelperExecutionEvidence(goos, goarch);
  if (manifest.binary_metadata_verified !== true) {
    if (representative) {
      problems.push(`representative helper ${goos}/${goarch} requires matching-host verified metadata`);
      return problems;
    }
    if (manifest.binary_metadata_verified !== false) {
      problems.push("non-representative helper metadata must be verified or a well-formed not-run record");
      return problems;
    }
    if (manifest.metadata_status !== "not-run") problems.push("not-run helper metadata_status must be not-run");
    if (manifest.cross_compiled !== true) problems.push("not-run helper must record cross_compiled=true");
    if (manifest.untested !== true) problems.push("not-run helper must record untested=true");
    for (const name of ["protocol", "features", "sha256", "metadata_host"]) {
      if (Object.hasOwn(manifest, name)) {
        problems.push("not-run helper record must not contain execution fields");
        break;
      }
    }
    return problems;
  }
  if (Object.hasOwn(manifest, "metadata_status") || Object.hasOwn(manifest, "cross_compiled") || Object.hasOwn(manifest, "untested")) {
    problems.push("verified helper metadata contains mixed verified/not-run state");
  }
  if (!Number.isInteger(manifest.protocol) || manifest.protocol <= 0) {
    problems.push(`helper metadata protocol is invalid: ${JSON.stringify(manifest.protocol)}`);
  } else if (protocol !== undefined && manifest.protocol !== protocol) {
    problems.push(`helper metadata protocol ${manifest.protocol} does not match ${protocol}`);
  }
  if (Object.hasOwn(manifest, "features")) {
    problems.push("helper metadata must not list features; the protocol number defines the helper baseline");
  }
  if (manifest.metadata_host?.os !== goos || manifest.metadata_host?.arch !== goarch) {
    problems.push(`helper metadata execution host ${JSON.stringify(manifest.metadata_host)} does not match ${goos}/${goarch}`);
  }
  if (!/^[0-9a-f]{64}$/.test(manifest.sha256 || "")) {
    problems.push("helper metadata sha256 must be 64 lowercase hex characters");
  } else if (manifest.sha256 !== manifest.artifact?.sha256) {
    problems.push("helper metadata sha256 does not match artifact sha256");
  }
  return problems;
}

export function loadManifest(file) {
  if (!fs.existsSync(file) || !fs.statSync(file).isFile()) throw new Error(`manifest does not exist: ${file}`);
  try {
    return JSON.parse(fs.readFileSync(file, "utf8"));
  } catch (error) {
    throw new Error(`manifest is not valid JSON (${error.message}): ${file}`);
  }
}
