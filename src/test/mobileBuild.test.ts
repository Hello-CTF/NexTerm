import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const ci = readFileSync(new URL("../../.github/workflows/ci.yml", import.meta.url), "utf8");
const taskfile = readFileSync(new URL("../../Taskfile.yml", import.meta.url), "utf8");
const androidTaskfile = readFileSync(new URL("../../build/android/Taskfile.yml", import.meta.url), "utf8");
const iosTaskfile = readFileSync(new URL("../../build/ios/Taskfile.yml", import.meta.url), "utf8");
const commonTaskfile = readFileSync(new URL("../../build/Taskfile.yml", import.meta.url), "utf8");
const gradle = readFileSync(new URL("../../build/android/app/build.gradle", import.meta.url), "utf8");
const config = readFileSync(new URL("../../build/config.yml", import.meta.url), "utf8");
const info = readFileSync(new URL("../../build/ios/Info.plist", import.meta.url), "utf8");

describe("mobile build wiring", () => {
  it("runs Android and iOS package tasks in CI and uploads their outputs", () => {
    expect(ci).toContain("wails3 task ${{ matrix.platform }}:package");
    expect(ci).toContain("name: mobile-${{ matrix.platform }}");
    expect(ci).toContain("bin/${{ matrix.platform == 'android' && 'nexterm.apk' || 'nexterm.app' }}");
    expect(ci).toContain("runner: ubuntu-latest");
    expect(ci).toContain("runner: macos-15");
  });

  it("uses the existing frontend build and keeps versions as placeholders", () => {
    for (const source of [androidTaskfile, iosTaskfile]) {
      expect(source).toContain("task: common:build:frontend");
    }
    expect(commonTaskfile).toContain("cp -R dist cmd/nexterm-desktop/dist");
    expect(ci).toContain("DIST_SOURCE: verified-dist/dist");
    expect(taskfile).toContain("includes:\n  android: ./build/android/Taskfile.yml\n  ios: ./build/ios/Taskfile.yml");
    expect(config.match(/version: "0\.0\.0"/g)).toHaveLength(2);
    expect(gradle).toContain('applicationId "com.nexterm.desktop"');
    expect(gradle).toContain('versionName System.getenv("NEXTERM_RELEASE_VERSION") ?: "0.0.0"');
  });

  it("keeps iOS 15 and iPad orientations enabled", () => {
    expect(info).toContain("<key>MinimumOSVersion</key>\n    <string>15.0</string>");
    expect(info).toContain("<key>UISupportedInterfaceOrientations~ipad</key>");
    expect(info).toContain("<string>UIInterfaceOrientationLandscapeLeft</string>");
    expect(info).toContain("<string>UIInterfaceOrientationLandscapeRight</string>");
    expect(info).toContain("<string>Copyright © NexTerm contributors</string>");
    expect(info).not.toContain("My Company");
  });
});
