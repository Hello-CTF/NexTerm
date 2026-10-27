import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, existsSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { resolve, join } from 'node:path';
import { spawnSync } from 'node:child_process';
import test from 'node:test';

for (const [platform, expected] of [['iphoneos', 'device'], ['iphonesimulator', 'simulator']]) {
  test(`Xcode embeds and signs ${platform} inference frameworks`, () => {
    const root = mkdtempSync(join(tmpdir(), 'nexterm-xcode-'));
    try {
      const tools = join(root, 'tools');
      mkdirSync(tools);
      const project = join(root, 'build/ios/xcode');
      mkdirSync(project, { recursive: true });
      writeFileSync(join(tools, 'node'), `#!/bin/sh
set -eu
printf '%s\\n' "$@" > "$TEST_LOG"
for arg in "$@"; do case "$arg" in --output=*) output="\${arg#--output=}";; esac; done
for name in NexTermPrivacy onnxruntime; do
 mkdir -p "$output/$name.framework"
 printf 'binary' > "$output/$name.framework/$name"
done
`, { mode: 0o755 });
      writeFileSync(join(tools, 'codesign'), '#!/bin/sh\nprintf "%s\\n" "$@" >> "$TEST_SIGN_LOG"\n', { mode: 0o755 });
      const result = spawnSync('/bin/sh', [resolve('build/ios/scripts/privacy-xcode.sh')], {
        encoding: 'utf8',
        env: { ...process.env, PATH: `${tools}:${process.env.PATH}`, PROJECT_DIR: project,
          PLATFORM_NAME: platform, DERIVED_FILE_DIR: join(root, 'derived'), TARGET_BUILD_DIR: join(root, 'built'),
          FRAMEWORKS_FOLDER_PATH: 'NexTerm.app/Frameworks', CODE_SIGNING_ALLOWED: 'YES', EXPANDED_CODE_SIGN_IDENTITY: 'test-identity',
          TEST_LOG: join(root, 'args'), TEST_SIGN_LOG: join(root, 'sign') },
      });
      assert.equal(result.status, 0, result.stderr);
      assert.ok(readFileSync(join(root, 'args'), 'utf8').includes(`--ios-platform=${expected}`));
      for (const name of ['NexTermPrivacy', 'onnxruntime']) {
        assert.ok(existsSync(join(root, `built/NexTerm.app/Frameworks/${name}.framework/${name}`)));
        assert.ok(readFileSync(join(root, 'sign'), 'utf8').includes(`${name}.framework`));
      }
    } finally { rmSync(root, { recursive: true, force: true }); }
  });
}
