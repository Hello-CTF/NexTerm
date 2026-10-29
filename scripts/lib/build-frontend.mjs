// Frontend build, dist manifest and verified dist consumption.
import fs from "node:fs";
import path from "node:path";
import { createDistManifest, distManifestProblems, loadManifest, treeHash } from "./build-manifest.mjs";
import { COMMIT, DIRTY_PATHS, ROOT, VERSION, die, log, option, run } from "./build-context.mjs";

export function distManifest() {
  const dist = path.join(ROOT, "dist");
  const index = path.join(dist, "index.html");
  if (!fs.existsSync(index) || !fs.statSync(index).isFile()) die("dist/index.html is missing; build the real frontend first");
  const html = fs.readFileSync(index, "utf8");
  const scripts = [...html.matchAll(/(?:src|href)=["']([^"']*assets\/[^"']+)["']/g)].map((match) => {
    const asset = match[1];
    return asset.slice(asset.indexOf("assets/"));
  });
  if (!scripts.some((asset) => asset.endsWith(".js"))) die("dist/index.html does not reference a hashed assets/*.js bundle");
  for (const asset of scripts) {
    if (!fs.existsSync(path.join(dist, asset))) die(`frontend asset referenced by index.html is missing: ${asset}`);
  }
  return { dist, index, html, scripts };
}

export function treeHashOrDie(root) {
  try {
    return treeHash(root);
  } catch (error) {
    die(error.message);
  }
}

function distManifestPath() {
  return path.resolve(ROOT, option("dist-manifest", "target/dist-manifest.json"));
}

function writeDistManifest() {
  const destination = distManifestPath();
  const manifest = createDistManifest(path.join(ROOT, "dist"), { version: VERSION, commit: COMMIT });
  fs.mkdirSync(path.dirname(destination), { recursive: true });
  fs.writeFileSync(destination, `${JSON.stringify(manifest, null, 2)}\n`);
  log(`frontend manifest: ${destination} (tree sha256:${manifest.tree_hash})`);
}

export function consumeDist() {
  const source = path.resolve(ROOT, option("consume-dist", ""));
  if (!fs.existsSync(source) || !fs.statSync(source).isDirectory()) die(`--consume-dist directory does not exist: ${source}`);
  let manifest;
  try {
    manifest = loadManifest(distManifestPath());
  } catch (error) {
    die(`--consume-dist: ${error.message}`);
  }
  const problems = distManifestProblems(source, manifest, { version: VERSION, commit: COMMIT });
  if (problems.length) {
    const dirtyDetail = DIRTY_PATHS.length ? `\n  source tree changes when the build started:\n${DIRTY_PATHS.map((line) => `    ${line}`).join("\n")}` : "";
    die(`verified dist consumption failed:\n${problems.map((problem) => `  - ${problem}`).join("\n")}${dirtyDetail}`);
  }
  const destination = path.join(ROOT, "dist");
  if (source !== destination) {
    fs.rmSync(destination, { recursive: true, force: true });
    fs.cpSync(source, destination, { recursive: true });
  }
  if (treeHashOrDie(destination) !== manifest.tree_hash) die("restored dist tree hash differs from the verified manifest");
  distManifest();
  log(`frontend dist consumed from ${source} (tree sha256:${manifest.tree_hash})`);
}

export function buildFrontend({ repro = false, base = "" } = {}) {
  const viteArgs = ["exec", "vite", "build"];
  if (base) viteArgs.push(`--base=${base}`);
  run("pnpm", ["exec", "tsc", "-p", "tsconfig.json", "--noEmit"], { env: { ...process.env, NODE_OPTIONS: "" } });
  run("pnpm", viteArgs, { env: { ...process.env, NODE_OPTIONS: "" } });
  const first = treeHashOrDie(path.join(ROOT, "dist"));
  distManifest();
  if (repro) {
    run("pnpm", viteArgs, { env: { ...process.env, NODE_OPTIONS: "" } });
    const second = treeHashOrDie(path.join(ROOT, "dist"));
    if (first !== second) die(`frontend is not reproducible: ${first} != ${second}`);
    log(`frontend reproducibility: sha256:${second}`);
  } else {
    log(`frontend assets: sha256:${first}`);
  }
  writeDistManifest();
  return first;
}
