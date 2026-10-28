import assert from "node:assert/strict";
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { helperTargets } from "./helper-matrix.mjs";

function fixture(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-lazycat-runtime-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  for (const directory of ["lazycat/image", "lazycat/injects", "scripts/lib", ".github/scripts", "node_modules", "dist", "helpers", "bin", "payload/ai-runtime/licenses"]) fs.mkdirSync(path.join(root, directory), { recursive: true });
  for (const file of ["lazycat/image/build-server.sh", "lazycat/image/Dockerfile", "lazycat/image/entrypoint.sh", "scripts/lib/privacy-runtime.mjs"]) fs.copyFileSync(file, path.join(root, file));
  fs.writeFileSync(path.join(root, "dist/index.html"), "<html>fixture</html>");
  fs.writeFileSync(path.join(root, "lazycat/injects/fixture.js"), "");
  fs.writeFileSync(path.join(root, "helpers.txt"), helperTargets().map(target => `${target.os}|${target.arch}|${target.name}`).join("\n") + "\n");
  for (const target of helperTargets()) fs.writeFileSync(path.join(root, "helpers", target.name), "helper", { mode: 0o755 });
  const payload = path.join(root, "payload");
  fs.writeFileSync(path.join(payload, "nexterm-server-linux-amd64"), "server", { mode: 0o755 });
  const files = {};
  for (const name of ["libonnxruntime.so", "licenses/ASTRLINK-LICENSE", "licenses/ASTRLINK-NOTICE.md", "licenses/ONNX-RUNTIME-LICENSE", "licenses/ONNX-RUNTIME-THIRD-PARTY-NOTICES"]) {
    fs.writeFileSync(path.join(payload, "ai-runtime", name), name);
    files[name] = crypto.createHash("sha256").update(name).digest("hex");
  }
  const manifest = JSON.stringify({ abi: 2, platform: "linux", arch: "amd64", files });
  fs.writeFileSync(path.join(payload, "ai-runtime/runtime.json"), manifest);
  fs.writeFileSync(path.join(payload, "nexterm-server-linux-amd64.manifest.json"), JSON.stringify({ privacy_runtime_sha256: crypto.createHash("sha256").update(manifest).digest("hex") }));
  const scripts = {
    node: `#!/bin/sh
case "$1" in
  */helper-matrix.mjs) cat "$FIXTURE_ROOT/helpers.txt" ;;
  */verify-reused-binary.mjs) printf '%s\\n' "$*" >> "$FIXTURE_ROOT/verified" ;;
  scripts/build.mjs)
    if [ "$2" = server ]; then
      printf '%s\\n' "$*" > "$FIXTURE_ROOT/server-build-args"
      mkdir -p "$FIXTURE_ROOT/target/go-build"
      cp -R "$FIXTURE_ROOT/payload/." "$FIXTURE_ROOT/target/go-build/"
    fi
    exit 0 ;;
  *) exec "$REAL_NODE" "$@" ;;
esac
`,
    file: `#!/bin/sh
case "$2" in
  */nexterm-server) printf 'ELF 64-bit x86-64 dynamically linked\\n' ;;
  *linux-amd64) printf 'ELF 64-bit x86-64 statically linked\\n' ;;
  *linux-arm64) printf 'ELF 64-bit aarch64 statically linked\\n' ;;
  *linux-armv7) printf 'ELF 32-bit ARM statically linked\\n' ;;
  *linux-riscv64) printf 'ELF 64-bit RISC-V statically linked\\n' ;;
  *linux-loong64) printf 'ELF 64-bit LoongArch statically linked\\n' ;;
esac
`,
    go: `#!/bin/sh
case "$3" in */nexterm-server) printf 'CGO_ENABLED=1\\n' ;; *) printf 'CGO_ENABLED=0\\n' ;; esac
`,
  };
  for (const [name, content] of Object.entries(scripts)) fs.writeFileSync(path.join(root, "bin", name), content, { mode: 0o755 });
  return { root, run: () => spawnSync("bash", [path.join(root, "lazycat/image/build-server.sh")], { cwd: root, encoding: "utf8", env: { ...process.env, PATH: `${root}/bin:${process.env.PATH}`, REAL_NODE: process.execPath, FIXTURE_ROOT: root, NEXTERM_HELPERS_DIR: path.join(root, "helpers"), LZC_ARCH: "amd64", LZC_VERSION: "0.0.0", NEXTERM_RELEASE_VERSION: "", SOURCE_DATE_EPOCH: "1" } }) };
}

test("Lazycat stages the verified CGO server runtime and retains static helper checks", (t) => {
  const { root, run } = fixture(t);
  const result = run();
  assert.equal(result.status, 0, result.stderr + result.stdout);
  const runtime = path.join(root, "lazycat/image-context/ai-runtime");
  assert.equal(fs.readFileSync(path.join(runtime, "libonnxruntime.so"), "utf8"), "libonnxruntime.so");
  assert.equal(fs.readFileSync(path.join(runtime, "licenses/ASTRLINK-LICENSE"), "utf8"), "licenses/ASTRLINK-LICENSE");
  assert.match(fs.readFileSync(path.join(root, "verified"), "utf8"), /server-linux-amd64 linux amd64 1/);
  assert.match(fs.readFileSync(path.join(root, "server-build-args"), "utf8"), /server --release --os=linux --arch=amd64/);
});

test("Lazycat refuses a corrupted runtime before creating the container payload", (t) => {
  const { root, run } = fixture(t);
  fs.writeFileSync(path.join(root, "payload/ai-runtime/libonnxruntime.so"), "corrupt");
  const result = run();
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /checksum mismatch/);
  assert.equal(fs.existsSync(path.join(root, "lazycat/image-context")), false);
});

test("Lazycat still rejects dynamically linked SSH helpers", (t) => {
  const { root, run } = fixture(t);
  const tool = path.join(root, "bin/file");
  fs.writeFileSync(tool, fs.readFileSync(tool, "utf8").replace("ELF 64-bit aarch64 statically linked", "ELF 64-bit aarch64 dynamically linked"));
  const result = run();
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /nexterm-helper-linux-arm64 is not statically linked/);
});
