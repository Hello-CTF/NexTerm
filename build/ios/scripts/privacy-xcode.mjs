import { readFileSync, writeFileSync } from 'node:fs';

const file = process.argv[2];
if (!file) throw new Error('Usage: node privacy-xcode.mjs <project.pbxproj>');
let project = readFileSync(file, 'utf8');
const archiveScript = 'set -eu\n/bin/sh "${PROJECT_DIR}/../scripts/archive-xcode.sh"\n';
const archivePhase = /(name = "Prebuild: Wails Go Archive";[\s\S]*?shellScript = )"(?:[^"\\]|\\.)*";/;
if (!archivePhase.test(project)) throw new Error('Missing Wails archive build phase');
project = project.replace(archivePhase, (_, prefix) => `${prefix}${JSON.stringify(archiveScript)};`);
const id = 'A19A11A00000000000000001';
if (!project.includes(id)) {
  const anchor = '/* End PBXShellScriptBuildPhase section */';
  if (!project.includes(anchor) || !project.includes('buildPhases = (')) throw new Error('Unsupported Xcode project structure');
  project = project.replace(/(buildPhases = \([\s\S]*?)(\n\s*\);)/, `$1\n\t\t\t\t${id} /* Embed privacy runtime */,$2`);
  const script = 'set -eu\n/bin/sh "${PROJECT_DIR}/../scripts/privacy-xcode.sh"\n';
  project = project.replace(anchor, `${id} /* Embed privacy runtime */ = {
    isa = PBXShellScriptBuildPhase;
    buildActionMask = 2147483647;
    alwaysOutOfDate = 1;
    files = ();
    inputPaths = ();
    outputPaths = ();
    name = "Embed privacy runtime";
    runOnlyForDeploymentPostprocessing = 0;
    shellPath = /bin/sh;
    shellScript = ${JSON.stringify(script)};
  };
${anchor}`);
}
writeFileSync(file, project);
