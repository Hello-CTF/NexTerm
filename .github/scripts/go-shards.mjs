#!/usr/bin/env node
// Splits the module's Go packages across the shards of the Go test job so each runner gets about the same amount of
// work. A package is never split, so the slowest package is the floor for the job's wall time.
//
// The split is deterministic: every shard computes it from the same package list and weight file and keeps its own
// part. .github/go-test-weights.json holds each package's measured seconds as [plain, race]; a package missing from
// it gets a middling default until the file is refreshed from a CI log.
import fs from "node:fs";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath, pathToFileURL } from "node:url";

const MODULE = "github.com/Hello-CTF/NexTerm";
const DEFAULT_WEIGHT = [3, 8];
const WEIGHTS_FILE = path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "go-test-weights.json");

// Build and release tooling has no concurrency worth instrumenting, and its tests compile other programs, which the
// race detector slows down for nothing. Under the race run these packages are tested without it.
export function runsWithoutRace(pkg) {
  return pkg === "scripts" || pkg.startsWith("scripts/");
}

// relative turns an import path into the module-relative form the weight file uses.
export function relative(importPath) {
  return importPath === MODULE ? "." : importPath.startsWith(`${MODULE}/`) ? importPath.slice(MODULE.length + 1) : importPath;
}

export function weightOf(pkg, weights, race) {
  const entry = weights[pkg] ?? DEFAULT_WEIGHT;
  return race && !runsWithoutRace(pkg) ? entry[1] : entry[0];
}

// assignShards greedily gives the heaviest remaining package to the lightest shard.
export function assignShards(packages, weights, count, race) {
  const shards = Array.from({ length: count }, () => ({ weight: 0, packages: [] }));
  const ordered = [...packages].sort((a, b) => weightOf(b, weights, race) - weightOf(a, weights, race) || a.localeCompare(b));
  for (const pkg of ordered) {
    let target = shards[0];
    for (const shard of shards) if (shard.weight < target.weight) target = shard;
    target.weight += weightOf(pkg, weights, race);
    target.packages.push(pkg);
  }
  return shards;
}

// splitForRace separates a shard's packages into those tested with the race detector and those tested without it.
export function splitForRace(packages, race) {
  if (!race) return { race: [], plain: [...packages] };
  return { race: packages.filter((pkg) => !runsWithoutRace(pkg)), plain: packages.filter(runsWithoutRace) };
}

function listPackages() {
  const listed = spawnSync("go", ["list", "./..."], { encoding: "utf8" });
  if (listed.status !== 0) throw new Error(listed.stderr || "go list failed");
  return listed.stdout.split("\n").map((line) => line.trim()).filter(Boolean);
}

function main() {
  const args = process.argv.slice(2);
  const value = (flag) => {
    const index = args.indexOf(flag);
    return index >= 0 ? args[index + 1] : undefined;
  };
  const shard = Number(value("--shard"));
  const count = Number(value("--count"));
  const race = args.includes("--race");
  if (!Number.isInteger(count) || count < 1 || !Number.isInteger(shard) || shard < 0 || shard >= count) {
    throw new Error("usage: go-shards.mjs --shard <0..count-1> --count <n> [--race]");
  }
  const weights = JSON.parse(fs.readFileSync(WEIGHTS_FILE, "utf8"));
  const imports = listPackages();
  const byRelative = new Map(imports.map((importPath) => [relative(importPath), importPath]));
  const mine = assignShards([...byRelative.keys()], weights, count, race)[shard];
  const split = splitForRace(mine.packages, race);
  const full = (list) => list.map((pkg) => byRelative.get(pkg)).join(" ");
  process.stdout.write(`race=${full(split.race)}\nplain=${full(split.plain)}\n`);
  process.stderr.write(`shard ${shard + 1}/${count}: ${mine.packages.length} packages, about ${mine.weight}s of ${race ? "race" : "plain"} work\n`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) main();
