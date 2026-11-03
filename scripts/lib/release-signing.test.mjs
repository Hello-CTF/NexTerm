import assert from "node:assert/strict";
import test from "node:test";
import crypto from "node:crypto";
import { agentManifestKeyProblems, minisignPublicKeyDigest, normalizeMinisignPublicKey, releaseSigningKeyProblem } from "./release-signing.mjs";

const KEY = "RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3";

test("agent release build without a public key is a problem", () => {
  for (const releaseVersion of ["1.4.0", "1.4.0-rc.3"]) {
    const problem = releaseSigningKeyProblem({ kind: "agent", releaseVersion, publicKey: "" });
    assert.match(problem, /NEXTERM_MINISIGN_PUBLIC_KEY/);
  }
});

test("agent release build with a public key is fine", () => {
  assert.equal(releaseSigningKeyProblem({ kind: "agent", releaseVersion: "1.4.0", publicKey: KEY }), null);
});

test("empty or placeholder versions do not need a key", () => {
  for (const releaseVersion of ["", undefined, "0.0.0"]) {
    assert.equal(releaseSigningKeyProblem({ kind: "agent", releaseVersion, publicKey: "" }), null);
  }
});

test("other kinds are not checked", () => {
  for (const kind of ["desktop", "server", "helper"]) {
    assert.equal(releaseSigningKeyProblem({ kind, releaseVersion: "1.4.0", publicKey: "" }), null);
  }
});

const DIGEST = crypto.createHash("sha256").update(KEY).digest("hex");

test("normalizeMinisignPublicKey accepts bare and file forms and empty input", () => {
  assert.deepEqual(normalizeMinisignPublicKey(""), { key: "", problem: null });
  assert.deepEqual(normalizeMinisignPublicKey(undefined), { key: "", problem: null });
  assert.deepEqual(normalizeMinisignPublicKey(` ${KEY} \n`), { key: KEY, problem: null });
  assert.equal(normalizeMinisignPublicKey(`untrusted comment: x\n${KEY}\n`).key, KEY);
});

test("normalizeMinisignPublicKey rejects malformed keys", () => {
  for (const raw of ["a\nb\nc", "short", "A".repeat(56)]) {
    const result = normalizeMinisignPublicKey(raw);
    assert.equal(result.key, "");
    assert.ok(result.problem);
  }
});

test("minisignPublicKeyDigest hashes the key or returns empty", () => {
  assert.equal(minisignPublicKeyDigest(KEY), DIGEST);
  assert.equal(minisignPublicKeyDigest(""), "");
});

test("agent manifest key check: release version", () => {
  const base = { id: "agent-linux-amd64", releaseVersion: "1.4.0" };
  assert.deepEqual(agentManifestKeyProblems({ ...base, manifest: { updater_public_key_sha256: DIGEST }, expectedDigest: DIGEST }), []);
  assert.match(agentManifestKeyProblems({ ...base, manifest: { updater_public_key_sha256: DIGEST }, expectedDigest: "" }).join(), /requires NEXTERM_MINISIGN_PUBLIC_KEY/);
  assert.ok(agentManifestKeyProblems({ ...base, manifest: { updater_public_key_sha256: "" }, expectedDigest: DIGEST }).length > 0);
  assert.ok(agentManifestKeyProblems({ ...base, manifest: {}, expectedDigest: DIGEST }).length > 0);
  assert.ok(agentManifestKeyProblems({ ...base, manifest: { updater_public_key_sha256: "00" }, expectedDigest: DIGEST }).length > 0);
});

test("agent manifest key check: non-release version", () => {
  const base = { id: "agent-linux-amd64", releaseVersion: "0.0.0" };
  assert.deepEqual(agentManifestKeyProblems({ ...base, manifest: {}, expectedDigest: "" }), []);
  assert.deepEqual(agentManifestKeyProblems({ ...base, manifest: { updater_public_key_sha256: DIGEST }, expectedDigest: DIGEST }), []);
  assert.ok(agentManifestKeyProblems({ ...base, manifest: { updater_public_key_sha256: "00" }, expectedDigest: DIGEST }).length > 0);
});

test("agent manifest key check ignores other ids", () => {
  for (const id of ["server-linux-amd64", "helper-linux-amd64", "desktop-linux-amd64"]) {
    assert.deepEqual(agentManifestKeyProblems({ id, manifest: {}, releaseVersion: "1.4.0", expectedDigest: "" }), []);
  }
});
