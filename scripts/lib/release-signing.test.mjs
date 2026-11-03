import assert from "node:assert/strict";
import test from "node:test";
import { releaseSigningKeyProblem } from "./release-signing.mjs";

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
