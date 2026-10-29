import { describe, expect, it } from "vitest";
import {
  joinPastePath,
  normalizePasteDir,
  pasteDirSettings,
  terminalPastePath,
  validatePasteDirOptions,
} from "../../features/terminal/pasteUploadDir";

describe("paste upload directory options", () => {
  it("normalises absolute POSIX directories", () => {
    expect(normalizePasteDir("")).toEqual({ ok: true, value: "" });
    expect(normalizePasteDir("/")).toEqual({ ok: true, value: "/" });
    expect(normalizePasteDir(" /srv//up/./ ")).toEqual({ ok: true, value: "/srv/up" });
  });

  it.each(["tmp", "/srv/../etc", "/tmp\n", "/a\u202eb", "C:\\Temp"])("rejects %j", (input) => {
    expect(normalizePasteDir(input).ok).toBe(false);
  });

  it("requires an upload directory when a terminal path is set", () => {
    expect(validatePasteDirOptions("", "/tmp").ok).toBe(false);
    expect(validatePasteDirOptions("/", "/tmp/")).toEqual({ ok: true, uploadDir: "/", terminalDir: "/tmp" });
  });

  it("defaults to /tmp and maps a chroot upload directory to the shell path", () => {
    const fallback = pasteDirSettings({});
    expect(fallback).toEqual({ ok: true, settings: { uploadDir: "/tmp", terminalDir: "/tmp", custom: false, flat: false } });

    const resolved = pasteDirSettings({ pasteUploadDir: "/", pasteTerminalDir: "/tmp" });
    if (!resolved.ok) throw new Error(resolved.error);
    const dir = joinPastePath(resolved.settings.uploadDir, "nexterm-paste-x");
    expect(dir).toBe("/nexterm-paste-x");
    expect(terminalPastePath(resolved.settings, `${dir}/a.png`)).toBe("/tmp/nexterm-paste-x/a.png");

    const nested = pasteDirSettings({ pasteUploadDir: "/upload", pasteTerminalDir: "/home/jail/upload" });
    if (!nested.ok) throw new Error(nested.error);
    expect(terminalPastePath(nested.settings, "/upload/nexterm-paste-x/a.png")).toBe(
      "/home/jail/upload/nexterm-paste-x/a.png",
    );
    expect(terminalPastePath(nested.settings, "/uploads/a.png")).toBe("/uploads/a.png");

    const same = pasteDirSettings({ pasteUploadDir: "/data/" });
    expect(same).toEqual({ ok: true, settings: { uploadDir: "/data", terminalDir: "/data", custom: true, flat: false } });
  });

  it("reads the option that skips the random directory, with or without an upload directory", () => {
    expect(pasteDirSettings({ pasteNoRandomDir: true })).toEqual({
      ok: true,
      settings: { uploadDir: "/tmp", terminalDir: "/tmp", custom: false, flat: true },
    });
    const mapped = pasteDirSettings({ pasteUploadDir: "/", pasteTerminalDir: "/tmp", pasteNoRandomDir: true });
    if (!mapped.ok) throw new Error(mapped.error);
    expect(mapped.settings.flat).toBe(true);
    expect(joinPastePath(mapped.settings.uploadDir, "a.png")).toBe("/a.png");
    expect(terminalPastePath(mapped.settings, "/a.png")).toBe("/tmp/a.png");
    expect(pasteDirSettings({ pasteNoRandomDir: false })).toEqual(pasteDirSettings({}));
    expect(pasteDirSettings({ pasteNoRandomDir: null })).toEqual(pasteDirSettings({}));
  });

  it("reports invalid stored options instead of guessing", () => {
    expect(pasteDirSettings({ pasteNoRandomDir: "yes" }).ok).toBe(false);
    expect(pasteDirSettings({ pasteUploadDir: 42 }).ok).toBe(false);
    expect(pasteDirSettings({ pasteUploadDir: "relative" }).ok).toBe(false);
    expect(pasteDirSettings({ pasteTerminalDir: "/tmp" }).ok).toBe(false);
  });
});
