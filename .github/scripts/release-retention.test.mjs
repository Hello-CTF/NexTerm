import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { compareVersions, parseReleases, planRetention } from "./release-retention.mjs";

const NOW = Date.parse("2026-11-09T00:00:00Z");
const DAY = 24 * 60 * 60 * 1000;
let nextId = 1;
const release = (tag, daysAgo, extra = {}) => ({
  id: nextId++,
  tag_name: tag,
  draft: false,
  prerelease: tag.includes("-rc."),
  published_at: new Date(NOW - daysAgo * DAY).toISOString(),
  ...extra,
});
const tagsOf = (list) => list.map((entry) => entry.tag).sort();
const plan = (releases, options = {}) => planRetention(releases, { now: NOW, ...options });

test("the newest patch of every minor line survives however old it is", () => {
  const releases = [
    release("v1.0.0", 900),
    release("v1.0.1", 890),
    release("v1.1.0", 800),
    release("v1.1.3", 700),
    release("v1.1.2", 710),
  ];
  const { remove } = plan(releases, { keepRecent: 1, keepDays: 30 });
  assert.deepEqual(tagsOf(remove), ["v1.0.0", "v1.1.0", "v1.1.2"]);
});

test("the newest stable releases survive even when they are old patches", () => {
  const releases = Array.from({ length: 15 }, (_, patch) => release(`v2.3.${patch}`, 1000 - patch));
  const { keep, remove } = plan(releases, { keepRecent: 10, keepDays: 30 });
  assert.deepEqual(tagsOf(keep), Array.from({ length: 10 }, (_, i) => `v2.3.${i + 5}`).sort());
  assert.deepEqual(tagsOf(remove), ["v2.3.0", "v2.3.1", "v2.3.2", "v2.3.3", "v2.3.4"]);
});

test("stable releases published within the retention window survive", () => {
  const releases = [release("v3.0.0", 400), release("v3.0.1", 200), release("v3.0.2", 100), release("v3.0.3", 1)];
  const { remove } = plan(releases, { keepRecent: 1, keepDays: 365 });
  assert.deepEqual(tagsOf(remove), ["v3.0.0"]);
});

test("only the newest release candidate newer than the latest stable survives", () => {
  const releases = [
    release("v1.2.0-rc.1", 50),
    release("v1.2.0-rc.2", 45),
    release("v1.2.0", 40),
    release("v1.3.0-rc.1", 5),
    release("v1.3.0-rc.2", 2),
  ];
  const { keep, remove } = plan(releases);
  assert.deepEqual(tagsOf(remove), ["v1.2.0-rc.1", "v1.2.0-rc.2", "v1.3.0-rc.1"]);
  assert.deepEqual(tagsOf(keep), ["v1.2.0", "v1.3.0-rc.2"]);
});

test("publishing a release candidate deletes the previous release candidate only", () => {
  const releases = [release("v1.0.0", 30), release("v1.1.0-rc.1", 3), release("v1.1.0-rc.2", 0)];
  const current = releases[2].id;
  const { keep, remove } = plan(releases, { keepIds: [String(current)] });
  assert.deepEqual(tagsOf(remove), ["v1.1.0-rc.1"]);
  assert.deepEqual(tagsOf(keep), ["v1.0.0", "v1.1.0-rc.2"]);
});

test("publishing a stable release deletes its release candidates and keeps older stable releases", () => {
  const releases = [
    release("v1.0.0", 60),
    release("v1.0.1", 30),
    release("v1.1.0-rc.1", 10),
    release("v1.1.0-rc.2", 5),
    release("v1.1.0", 0),
  ];
  const current = releases[4].id;
  const { keep, remove } = plan(releases, { keepIds: [String(current)] });
  assert.deepEqual(tagsOf(remove), ["v1.1.0-rc.1", "v1.1.0-rc.2"]);
  assert.deepEqual(tagsOf(keep), ["v1.0.0", "v1.0.1", "v1.1.0"]);
});

test("only the newest release candidate is kept before any stable release exists", () => {
  const { keep, remove } = plan([release("v0.1.0-rc.1", 500), release("v0.1.0-rc.2", 400)]);
  assert.deepEqual(tagsOf(remove), ["v0.1.0-rc.1"]);
  assert.deepEqual(tagsOf(keep), ["v0.1.0-rc.2"]);
});

test("pinned ids, drafts and unrecognised tags are never deleted", () => {
  const old = release("v1.0.0", 900);
  const releases = [
    old,
    release("v1.0.1", 899),
    release("v1.0.1-rc.1", 900),
    release("v9.9.9", 0, { draft: true, published_at: null }),
    release("nightly", 1000),
    release("v1.0.0-beta.1", 1000),
  ];
  const { keep, remove } = plan(releases, { keepIds: [String(old.id)], keepRecent: 1, keepDays: 30 });
  assert.deepEqual(tagsOf(remove), ["v1.0.1-rc.1"]);
  assert.ok(tagsOf(keep).includes("v1.0.0"));
  assert.ok(tagsOf(keep).includes("v9.9.9"));
  assert.ok(tagsOf(keep).includes("nightly"));
  assert.ok(tagsOf(keep).includes("v1.0.0-beta.1"));
});

test("the plan does not depend on the order of the release list", () => {
  const releases = Array.from({ length: 30 }, (_, i) => release(`v1.${Math.floor(i / 6)}.${i % 6}`, 1200 - i * 30));
  const forward = plan(releases, { keepRecent: 4 });
  const reversed = plan([...releases].reverse(), { keepRecent: 4 });
  assert.deepEqual(tagsOf(reversed.remove), tagsOf(forward.remove));
  assert.equal(forward.keep.length + forward.remove.length, releases.length);
});

test("versions compare numerically with release candidates before their final", () => {
  const order = ["v1.2.0-rc.1", "v1.2.0-rc.10", "v1.2.0", "v1.10.0"];
  const parse = (tag) => {
    const [core, rc] = tag.slice(1).split("-rc.");
    return { core: core.split(".").map(Number), rc: rc ? Number(rc) : null };
  };
  const sorted = [...order].reverse().sort((a, b) => compareVersions(parse(a), parse(b)));
  assert.deepEqual(sorted, order);
});

test("the command reads JSON lines and prints the ids to delete", () => {
  const releases = [release("v1.0.0-rc.1", 900), release("v1.0.0", 899)];
  const input = releases.map((entry) => JSON.stringify(entry)).join("\n");
  assert.deepEqual(parseReleases(input), releases);
  const script = fileURLToPath(new URL("./release-retention.mjs", import.meta.url));
  const run = spawnSync(process.execPath, [script, "--keep", String(releases[1].id)], { input, encoding: "utf8" });
  assert.equal(run.status, 0, run.stderr);
  assert.equal(run.stdout, `${releases[0].id}\n`);
});
