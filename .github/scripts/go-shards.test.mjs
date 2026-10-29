import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import test from "node:test";
import { assignShards, relative, runsWithoutRace, splitForRace, weightOf } from "./go-shards.mjs";

const weights = JSON.parse(fs.readFileSync(new URL("../go-test-weights.json", import.meta.url), "utf8"));
const listed = spawnSync("go", ["list", "./..."], { encoding: "utf8" });
const packages = listed.status === 0 ? listed.stdout.split("\n").filter(Boolean).map(relative) : [];

test("every package lands in exactly one shard", () => {
  assert.ok(packages.length > 50, "go list ./... returned too few packages");
  for (const race of [false, true]) {
    for (const count of [1, 3, 5]) {
      const seen = assignShards(packages, weights, count, race).flatMap((shard) => shard.packages).sort();
      assert.deepEqual(seen, [...packages].sort(), `race=${race} count=${count}`);
    }
  }
});

test("the split does not depend on the order of the package list", () => {
  const forward = assignShards(packages, weights, 4, true).map((shard) => shard.packages);
  const reversed = assignShards([...packages].reverse(), weights, 4, true).map((shard) => shard.packages);
  assert.deepEqual(reversed, forward);
});

test("the shards are balanced to within the heaviest package", () => {
  for (const race of [false, true]) {
    const shards = assignShards(packages, weights, 4, race);
    const loads = shards.map((shard) => shard.weight);
    const heaviest = Math.max(...packages.map((pkg) => weightOf(pkg, weights, race)));
    assert.ok(Math.max(...loads) - Math.min(...loads) <= heaviest, `race=${race} loads=${loads}`);
  }
});

test("tooling packages run without the race detector, the rest with it", () => {
  assert.equal(runsWithoutRace("scripts/installer"), true);
  assert.equal(runsWithoutRace("internal/session"), false);
  const split = splitForRace(["scripts/installer", "internal/session"], true);
  assert.deepEqual(split, { race: ["internal/session"], plain: ["scripts/installer"] });
  assert.deepEqual(splitForRace(["scripts/installer", "internal/session"], false), {
    race: [],
    plain: ["scripts/installer", "internal/session"],
  });
});

test("a package missing from the weight file gets the default weight", () => {
  assert.deepEqual([weightOf("internal/new", weights, false), weightOf("internal/new", weights, true)], [3, 8]);
});

test("the weight file names only packages that exist", () => {
  const missing = Object.keys(weights).filter((pkg) => !packages.includes(pkg));
  assert.deepEqual(missing, [], "refresh .github/go-test-weights.json after removing or renaming packages");
});
