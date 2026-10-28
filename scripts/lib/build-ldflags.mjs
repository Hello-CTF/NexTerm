// Updater public key handling, release reuse evidence and Go linker flags.
import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { parseReleaseVersion } from "./semver.mjs";
import { COMMIT, RELEASE_REF, ROOT, VERSION, die, log, option } from "./build-context.mjs";

function desktopMinisignPublicKey() {
  const configured = (process.env.NEXTERM_MINISIGN_PUBLIC_KEY || "").trim();
  if (!configured) return "";
  const lines = configured.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
  if (lines.length === 2 && lines[0].startsWith("untrusted comment:")) lines.shift();
  if (lines.length !== 1) die("NEXTERM_MINISIGN_PUBLIC_KEY must be one Base64 public key or a two-line minisign public key file");
  const encoded = lines[0];
  if (!/^[A-Za-z0-9+/]{56}$/.test(encoded)) die("NEXTERM_MINISIGN_PUBLIC_KEY must contain one 56-character Base64 minisign public key");
  const decoded = Buffer.from(encoded, "base64");
  if (decoded.length !== 42 || decoded.subarray(0, 2).toString() !== "Ed" || decoded.toString("base64") !== encoded) {
    die("NEXTERM_MINISIGN_PUBLIC_KEY is not an Ed25519 minisign public key");
  }
  return encoded;
}

export function desktopMinisignPublicKeyDigest() {
  const publicKey = desktopMinisignPublicKey();
  return publicKey ? crypto.createHash("sha256").update(publicKey).digest("hex") : "";
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
  if (kind === "desktop") {
    const publicKey = desktopMinisignPublicKey();
    if (publicKey) flags.push(`-X github.com/Hello-CTF/NexTerm/internal/update.defaultMinisignPublicKey=${publicKey}`);
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
