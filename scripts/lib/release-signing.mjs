// Pure release signing checks; must not import build-context.mjs (it reads the repo and env on import).
import crypto from "node:crypto";
import { parseReleaseVersion } from "./semver.mjs";

export function releaseSigningKeyProblem({ kind, releaseVersion, publicKey }) {
  if (kind !== "agent") return null;
  if (!releaseVersion || !parseReleaseVersion(releaseVersion)) return null;
  if (publicKey) return null;
  return "agent release builds require NEXTERM_MINISIGN_PUBLIC_KEY: without it the agent refuses every self-upgrade";
}

// Returns { key, problem }; empty input yields an empty key and no problem.
export function normalizeMinisignPublicKey(raw) {
  const configured = (raw || "").trim();
  if (!configured) return { key: "", problem: null };
  const lines = configured.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
  if (lines.length === 2 && lines[0].startsWith("untrusted comment:")) lines.shift();
  if (lines.length !== 1) return { key: "", problem: "NEXTERM_MINISIGN_PUBLIC_KEY must be one Base64 public key or a two-line minisign public key file" };
  const encoded = lines[0];
  if (!/^[A-Za-z0-9+/]{56}$/.test(encoded)) return { key: "", problem: "NEXTERM_MINISIGN_PUBLIC_KEY must contain one 56-character Base64 minisign public key" };
  const decoded = Buffer.from(encoded, "base64");
  if (decoded.length !== 42 || decoded.subarray(0, 2).toString() !== "Ed" || decoded.toString("base64") !== encoded) {
    return { key: "", problem: "NEXTERM_MINISIGN_PUBLIC_KEY is not an Ed25519 minisign public key" };
  }
  return { key: encoded, problem: null };
}

export function minisignPublicKeyDigest(key) {
  return key ? crypto.createHash("sha256").update(key).digest("hex") : "";
}

export function agentManifestKeyProblems({ id, manifest, releaseVersion, expectedDigest }) {
  if (!id || !id.startsWith("agent-")) return [];
  const problems = [];
  const actual = manifest?.updater_public_key_sha256 || "";
  if (releaseVersion && parseReleaseVersion(releaseVersion)) {
    if (!expectedDigest) {
      problems.push("verifying an agent release binary requires NEXTERM_MINISIGN_PUBLIC_KEY");
      return problems;
    }
    if (!actual) problems.push("agent binary manifest records no updater public key SHA-256");
  }
  if (expectedDigest && actual && actual !== expectedDigest) {
    problems.push(`agent binary manifest updater public key SHA-256 ${JSON.stringify(actual)} does not match ${expectedDigest}`);
  }
  return problems;
}
