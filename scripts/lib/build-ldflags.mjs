// Updater public key handling, release reuse evidence and Go linker flags.
import fs from "node:fs";
import path from "node:path";
import { parseReleaseVersion } from "./semver.mjs";
import { minisignPublicKeyDigest, normalizeMinisignPublicKey, releaseSigningKeyProblem } from "./release-signing.mjs";
import { COMMIT, RELEASE_REF, ROOT, VERSION, die, log, option } from "./build-context.mjs";

function desktopMinisignPublicKey() {
  const { key, problem } = normalizeMinisignPublicKey(process.env.NEXTERM_MINISIGN_PUBLIC_KEY);
  if (problem) die(problem);
  return key;
}

export function desktopMinisignPublicKeyDigest() {
  return minisignPublicKeyDigest(desktopMinisignPublicKey());
}

export function desktopReuseEvidence() {
  if (!process.env.NEXTERM_RELEASE_VERSION || !parseReleaseVersion(VERSION)) {
    die("--package-only requires a non-placeholder NEXTERM_RELEASE_VERSION; master placeholder artifacts cannot be reused for a tag");
  }
  const expectedRef = `refs/tags/v${VERSION}`;
  if (RELEASE_REF !== expectedRef) {
    die(`--package-only requires NEXTERM_RELEASE_REF=${expectedRef}, got ${JSON.stringify(RELEASE_REF)}`);
  }
  const updaterPublicKeySHA256 = desktopMinisignPublicKeyDigest();
  if (!updaterPublicKeySHA256) die("--package-only requires NEXTERM_MINISIGN_PUBLIC_KEY for updater equivalence evidence");
  return { sourceRef: RELEASE_REF, updaterPublicKeySHA256 };
}

export function ldflags(kind, goos) {
  const flags = [
    "-s",
    "-w",
    `-X github.com/Hello-CTF/NexTerm/internal/version.Version=${VERSION}`,
    `-X github.com/Hello-CTF/NexTerm/internal/version.Commit=${COMMIT}`,
  ];
  if (kind === "desktop" || kind === "agent") {
    const publicKey = desktopMinisignPublicKey();
    if (kind === "agent") {
      const problem = releaseSigningKeyProblem({ kind, releaseVersion: process.env.NEXTERM_RELEASE_VERSION, publicKey });
      if (problem) die(problem);
    }
    if (publicKey) flags.push(`-X github.com/Hello-CTF/NexTerm/internal/releasesig.EmbeddedPublicKey=${publicKey}`);
  }
  if (kind === "desktop" && goos === "windows") flags.push("-H windowsgui");
  return flags.join(" ");
}

export function writeMinisignPublicKey() {
  const destination = path.resolve(ROOT, option("out", ""));
  if (!option("out")) die("write-minisign-public-key requires --out=PATH");
  const publicKey = desktopMinisignPublicKey();
  if (!publicKey) die("NEXTERM_MINISIGN_PUBLIC_KEY is required");
  fs.mkdirSync(path.dirname(destination), { recursive: true });
  fs.writeFileSync(destination, `untrusted comment: NexTerm release minisign public key\n${publicKey}\n`, { mode: 0o644 });
  log(`minisign public key file: ${destination}`);
}
