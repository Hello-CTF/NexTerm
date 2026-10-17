#!/usr/bin/env node
import fs from "node:fs";
import path from "node:path";
import crypto from "node:crypto";

const [source, binary] = process.argv.slice(2);
if (!source || !binary) throw new Error("usage: stage-server-evidence.mjs <download directory> <staged binary>");
const evidence = path.join(source, "e2e-sync");
const report = JSON.parse(fs.readFileSync(path.join(evidence, "report.json"), "utf8"));
if (report.status !== "passed" || report.failed !== 0) throw new Error("server e2e report is not a clean pass");
const manifest = JSON.parse(fs.readFileSync(`${binary}.manifest.json`, "utf8"));
const actual = crypto.createHash("sha256").update(fs.readFileSync(binary)).digest("hex");
if (actual !== manifest.artifact.sha256) throw new Error("server binary sha256 does not match the CI manifest");
fs.cpSync(evidence, "target/e2e-sync", { recursive: true });
