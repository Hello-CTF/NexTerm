#!/usr/bin/env node
// Chooses which published GitHub releases the Retention workflow deletes. Tags always stay; only the release and its
// assets go. Self-hosted servers pin exact versions: install-device.sh downloads the agent and SHA256SUMS of the
// server's own version, and agents upgrade to the server's version from the release URL. So older stable releases
// that people may still run must keep their assets.
//
// Kept: drafts, releases whose tag is not a vX.Y.Z or vX.Y.Z-rc.N release version, the ids passed with --keep (the
// GitHub latest release and the one just published), the newest patch of every major.minor line, the newest
// KEEP_RECENT stable releases, stable releases published within KEEP_DAYS, and the newest release candidate while it is
// newer than the newest stable release. Everything else is pruned: older stable patches, every release candidate once a
// stable release of the same or a newer version ships, and earlier release candidates once a newer one is published.
//
// Input on stdin: the releases as one JSON array or as one JSON object per line with id, tag_name, draft and
// published_at. Output on stdout: the ids to delete, one per line. Each decision is logged on stderr.
import fs from "node:fs";
import { pathToFileURL } from "node:url";
import { parseReleaseVersion } from "../../scripts/lib/semver.mjs";

export const KEEP_RECENT = 10;
export const KEEP_DAYS = 365;
const DAY_MS = 24 * 60 * 60 * 1000;

function versionOf(tag) {
  if (typeof tag !== "string" || !tag.startsWith("v")) return null;
  const parsed = parseReleaseVersion(tag.slice(1));
  if (!parsed) return null;
  return {
    core: [Number(parsed.major), Number(parsed.minor), Number(parsed.patch)],
    rc: parsed.prerelease.length > 0 ? Number(parsed.prerelease[1]) : null,
  };
}

// compareVersions orders release versions ascending; x.y.z-rc.N sorts before x.y.z.
export function compareVersions(a, b) {
  for (let i = 0; i < 3; i++) if (a.core[i] !== b.core[i]) return a.core[i] - b.core[i];
  return (a.rc ?? Infinity) - (b.rc ?? Infinity);
}

function compareCore(a, b) {
  for (let i = 0; i < 3; i++) if (a.core[i] !== b.core[i]) return a.core[i] - b.core[i];
  return 0;
}

// planRetention returns { keep, remove }, each a list of { id, tag, reason }, ordered newest version first.
export function planRetention(releases, { keepIds = [], now = Date.now(), keepRecent = KEEP_RECENT, keepDays = KEEP_DAYS } = {}) {
  const pinned = new Set(keepIds.map(String).filter(Boolean));
  const keep = [];
  const remove = [];
  const versioned = [];
  for (const release of releases) {
    const entry = { id: release.id, tag: release.tag_name };
    const version = versionOf(release.tag_name);
    if (release.draft) keep.push({ ...entry, reason: "draft" });
    else if (!version) keep.push({ ...entry, reason: "tag is not a release version" });
    else versioned.push({ ...entry, version, published: Date.parse(release.published_at ?? "") });
  }
  versioned.sort((a, b) => compareVersions(b.version, a.version) || String(b.id).localeCompare(String(a.id)));

  const stable = versioned.filter((release) => release.version.rc === null);
  const newestStable = stable[0]?.version;
  const newestCandidate = versioned.find((release) => release.version.rc !== null);
  const recent = new Set(stable.slice(0, keepRecent));
  const lineHeads = new Set();
  const seenLines = new Set();
  for (const release of stable) {
    const line = `${release.version.core[0]}.${release.version.core[1]}`;
    if (!seenLines.has(line)) {
      seenLines.add(line);
      lineHeads.add(release);
    }
  }
  const cutoff = now - keepDays * DAY_MS;

  for (const release of versioned) {
    const { version, published, ...entry } = release;
    let reason = null;
    if (pinned.has(String(release.id))) reason = "latest or just published";
    else if (version.rc !== null) {
      if (release === newestCandidate && (!newestStable || compareCore(version, newestStable) > 0)) {
        reason = "newest release candidate, newer than the latest stable";
      }
    } else if (lineHeads.has(release)) reason = "newest patch of its minor line";
    else if (recent.has(release)) reason = `one of the newest ${keepRecent} stable releases`;
    else if (!Number.isFinite(published) || published >= cutoff) reason = `published within ${keepDays} days`;
    if (reason) keep.push({ ...entry, reason });
    else remove.push({ ...entry, reason: version.rc !== null ? "superseded release candidate" : "older stable patch" });
  }
  return { keep, remove };
}

export function parseReleases(text) {
  const trimmed = text.trim();
  if (!trimmed) return [];
  if (trimmed.startsWith("[")) return JSON.parse(trimmed);
  return trimmed.split("\n").filter((line) => line.trim()).map((line) => JSON.parse(line));
}

function main() {
  const args = process.argv.slice(2);
  const keepIds = [];
  for (let i = 0; i < args.length; i++) {
    if (args[i] === "--keep" && i + 1 < args.length) keepIds.push(args[++i]);
    else throw new Error("usage: release-retention.mjs [--keep <release id>]... < releases.json");
  }
  const { keep, remove } = planRetention(parseReleases(fs.readFileSync(0, "utf8")), { keepIds });
  for (const release of keep) process.stderr.write(`keep ${release.tag} (${release.id}): ${release.reason}\n`);
  for (const release of remove) process.stderr.write(`delete ${release.tag} (${release.id}): ${release.reason}\n`);
  process.stdout.write(remove.map((release) => `${release.id}\n`).join(""));
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) main();
