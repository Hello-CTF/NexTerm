import { readFileSync, writeFileSync } from 'node:fs';

// Points the generated project's Go archive phase at archive-xcode.sh.

const file = process.argv[2];
if (!file) throw new Error('Usage: node patch-xcode.mjs <project.pbxproj>');
let project = readFileSync(file, 'utf8');
const archiveScript = 'set -eu\n/bin/sh "${PROJECT_DIR}/../scripts/archive-xcode.sh"\n';
const archivePhase = /(name = "Prebuild: Wails Go Archive";[\s\S]*?shellScript = )"(?:[^"\\]|\\.)*";/;
if (!archivePhase.test(project)) throw new Error('Missing Wails archive build phase');
project = project.replace(archivePhase, (_, prefix) => `${prefix}${JSON.stringify(archiveScript)};`);
writeFileSync(file, project);
