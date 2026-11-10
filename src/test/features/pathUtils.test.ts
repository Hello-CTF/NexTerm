import { describe, expect, it } from "vitest";
import {
  baseName,
  crumbsOf,
  directoryCommandFor,
  isRoot,
  joinPath,
  norm,
  normalizeTypedPath,
  parentOf,
  resolveRemotePath,
  resolveTypedPath,
} from "../../features/files/pathUtils";

describe("POSIX backslash names", () => {
  it("keeps a backslash as a filename character on POSIX paths", () => {
    expect(norm(String.raw`/home/u/a\b`, "posix")).toBe(String.raw`/home/u/a\b`);
    expect(norm(String.raw`/home/u/a\b`)).toBe(String.raw`/home/u/a\b`);
    expect(baseName(String.raw`/home/u/a\b`)).toBe(String.raw`a\b`);
    expect(parentOf(String.raw`/home/u/a\b`)).toBe("/home/u");
    expect(joinPath("/home/u", String.raw`a\b`)).toBe(String.raw`/home/u/a\b`);
  });

  it("still treats a backslash as a separator on Windows paths", () => {
    expect(norm(String.raw`C:\Users\a`)).toBe("C:/Users/a");
    expect(norm(String.raw`\Users\a`, "windows")).toBe("/Users/a");
  });
});

describe("typed path resolution", () => {
  it("resolves relative input against the shown directory", () => {
    expect(resolveTypedPath("src", "/home/u/project", "posix")).toEqual({ ok: true, path: "/home/u/project/src" });
    expect(resolveTypedPath("./src/", "/home/u", "posix")).toEqual({ ok: true, path: "/home/u/src" });
    expect(resolveTypedPath("../etc", "/home/u", "posix")).toEqual({ ok: true, path: "/home/etc" });
    expect(resolveTypedPath("../../..", "/home/u", "posix")).toEqual({ ok: true, path: "/" });
    expect(resolveTypedPath("logs", "D:/", "windows")).toEqual({ ok: true, path: "D:/logs" });
    expect(resolveTypedPath(String.raw`a\b`, "/srv", "posix")).toEqual({ ok: true, path: String.raw`/srv/a\b` });
  });

  it("keeps absolute input and reports what cannot be resolved", () => {
    expect(resolveTypedPath("/etc/", "/home/u", "posix")).toEqual({ ok: true, path: "/etc" });
    expect(resolveTypedPath("D:relative", "C:/", "windows")).toEqual({ ok: false, reason: "driveRelative" });
    expect(resolveTypedPath("logs", null, "windows")).toEqual({ ok: false, reason: "noBase" });
    expect(resolveTypedPath("   ", "/", "posix")).toBeNull();
  });

  it("expands home forms only when the backend reported a home path", () => {
    expect(resolveTypedPath("~/docs", "/", "posix", "/home/u")).toEqual({ ok: true, path: "/home/u/docs" });
    expect(resolveTypedPath("~", "/", "posix", "/home/u")).toEqual({ ok: true, path: "/home/u" });
    expect(resolveTypedPath("$HOME/docs", "/", "posix", "/home/u")).toEqual({ ok: true, path: "/home/u/docs" });
    expect(resolveTypedPath("${HOME}", "/", "posix", "/home/u")).toEqual({ ok: true, path: "/home/u" });
    expect(resolveTypedPath(String.raw`%USERPROFILE%\Desktop`, "C:/", "windows", "C:/Users/u")).toEqual({
      ok: true,
      path: "C:/Users/u/Desktop",
    });
    expect(resolveTypedPath("~/docs", "/", "posix", null)).toEqual({ ok: true, path: "~/docs" });
    expect(resolveTypedPath("$HOME", "/srv", "posix", null)).toEqual({ ok: true, path: "/srv/$HOME" });
    expect(resolveTypedPath("%USERPROFILE%", "/", "posix", "/home/u")).toEqual({ ok: true, path: "/%USERPROFILE%" });
  });
});

describe("Windows path handling", () => {
  it("normalizes directory separators before joining", () => {
    expect(joinPath("C:\\", "x")).toBe("C:/x");
    expect(joinPath(String.raw`C:\Users\name`, "a b")).toBe("C:/Users/name/a b");
  });

  it("preserves absolute drive roots and POSIX/home roots", () => {
    expect(normalizeTypedPath("C:/")).toBe("C:/");
    expect(normalizeTypedPath("C:\\")).toBe("C:/");
    expect(normalizeTypedPath("C:////")).toBe("C:/");
    expect(normalizeTypedPath("C:/Users/")).toBe("C:/Users");
    expect(normalizeTypedPath("/")).toBe("/");
    expect(normalizeTypedPath("~")).toBe("~");
  });

  it("keeps parent and breadcrumb navigation on the drive root", () => {
    expect(parentOf("C:/Users")).toBe("C:/");
    expect(parentOf("C:/")).toBeNull();
    expect(crumbsOf(String.raw`C:\Users\name`)).toEqual([
      { label: "C:", path: "C:/" },
      { label: "Users", path: "C:/Users" },
      { label: "name", path: "C:/Users/name" },
    ]);
  });

  it("resolves relative paths against a Windows root without making them drive-relative", () => {
    expect(resolveRemotePath("C:/", "./logs/a.log")).toBe("C:/logs/a.log");
    expect(resolveRemotePath("C:\\", "logs/a.log")).toBe("C:/logs/a.log");
  });
});

describe("UNC path handling", () => {
  it("treats a share as a root boundary, never stripped to //server or /", () => {
    expect(isRoot("//server/share")).toBe(true);
    expect(isRoot(String.raw`\\server\share`)).toBe(true);
    expect(isRoot("//server")).toBe(false);
    expect(isRoot("//server/share/dir")).toBe(false);
    expect(parentOf("//server/share")).toBeNull();
    expect(parentOf("//server/share/dir")).toBe("//server/share");
    expect(parentOf(String.raw`\\server\share\dir\file.txt`)).toBe("//server/share/dir");
    expect(parentOf("//server")).toBe("/");
  });

  it("builds breadcrumbs from the share root", () => {
    expect(crumbsOf(String.raw`\\server\share\dir\file.txt`)).toEqual([
      { label: "//server/share", path: "//server/share" },
      { label: "dir", path: "//server/share/dir" },
      { label: "file.txt", path: "//server/share/dir/file.txt" },
    ]);
    expect(crumbsOf("//server/share")).toEqual([{ label: "//server/share", path: "//server/share" }]);
  });

  it("normalizes typed UNC paths like drive roots", () => {
    expect(normalizeTypedPath(String.raw`\\server\share`)).toBe("//server/share");
    expect(normalizeTypedPath("//server/share/")).toBe("//server/share");
    expect(normalizeTypedPath("//server/share/dir/")).toBe("//server/share/dir");
  });

  it("keeps POSIX and drive-letter paths unaffected", () => {
    expect(crumbsOf("/etc/nginx")).toEqual([
      { label: "/", path: "/" },
      { label: "etc", path: "/etc" },
      { label: "nginx", path: "/etc/nginx" },
    ]);
    expect(parentOf("/etc")).toBe("/");
    expect(isRoot("/")).toBe(true);
    expect(isRoot("C:/")).toBe(true);
    expect(parentOf("C:/Users")).toBe("C:/");
    expect(crumbsOf("//server")).toEqual([
      { label: "/", path: "/" },
      { label: "server", path: "/server" },
    ]);
  });
});

describe("target-aware path normalization", () => {
  it("treats a bare drive as a root only for a Windows target", () => {
    expect(normalizeTypedPath("D:", "windows")).toBe("D:/");
    expect(normalizeTypedPath("d:\\", "windows")).toBe("d:/");
    expect(normalizeTypedPath("D:", "posix")).toBe("D:");
    expect(normalizeTypedPath("D:", "unknown")).toBe("D:");
  });

  it("does not disguise drive-relative input as an absolute path", () => {
    expect(normalizeTypedPath("D:relative", "windows")).toBeNull();
    expect(resolveRemotePath("C:/", "D:relative", "windows")).toBe("D:relative");
    expect(resolveRemotePath("C:/", "D:", "windows")).toBe("D:/");
    expect(normalizeTypedPath("D:relative", "posix")).toBe("D:relative");
    expect(resolveRemotePath("/home/user", "D:relative", "posix")).toBe(
      "/home/user/D:relative",
    );
  });

  it("preserves spaces, quotes, Unicode, brackets, UNC, and home paths", () => {
    const unc = String.raw`\\server\share\空间 ' (盘)`;
    expect(normalizeTypedPath(unc, "windows")).toBe("//server/share/空间 ' (盘)");
    expect(crumbsOf("//server/share/空间 ' (盘)")).toEqual([
      { label: "//server/share", path: "//server/share" },
      { label: "空间 ' (盘)", path: "//server/share/空间 ' (盘)" },
    ]);
    expect(crumbsOf("D:/Project .run/temp '中文' (x)")).toEqual([
      { label: "D:", path: "D:/" },
      { label: "Project .run", path: "D:/Project .run" },
      { label: "temp '中文' (x)", path: "D:/Project .run/temp '中文' (x)" },
    ]);
    expect(resolveRemotePath("D:/", "~/ child's (文件)", "windows")).toBe(
      "~/ child's (文件)",
    );
  });
});

describe("directory command generation", () => {
  it("uses literal PowerShell paths for wildcard-like names, quotes, and UNC", () => {
    expect(directoryCommandFor("D:/Project [x]/child's dir", "powershell")).toEqual({
      command: "Set-Location -LiteralPath 'D:/Project [x]/child''s dir'",
      failure: null,
    });
    expect(directoryCommandFor("//server/share/child's [x]", "powershell")).toEqual({
      command: "Set-Location -LiteralPath '//server/share/child''s [x]'",
      failure: null,
    });
  });

  it("doubles typographic single quotes so a directory name cannot close the PowerShell literal", () => {
    for (const quote of ["\u2018", "\u2019", "\u201A", "\u201B"]) {
      expect(directoryCommandFor(`/x${quote}; calc; ${quote}y`, "powershell")).toEqual({
        command: `Set-Location -LiteralPath '/x${quote}${quote}; calc; ${quote}${quote}y'`,
        failure: null,
      });
    }
  });

  it("preserves POSIX single-quote escaping", () => {
    expect(directoryCommandFor("/tmp/child's [x]", "posix")).toEqual({
      command: String.raw`cd '/tmp/child'\''s [x]'`,
      failure: null,
    });
  });

  it("does not generate commands for cmd or unknown shells", () => {
    for (const path of ["C:/Users/test", "//server/share/dir"]) {
      expect(directoryCommandFor(path, "cmd")).toEqual({ command: null, failure: "unsupported" });
    }
    expect(directoryCommandFor("D:/Project", "unknown")).toEqual({
      command: null,
      failure: "unknown",
    });
  });

  it("rejects control characters before any shell can inject a command", () => {
    for (const control of ["\n", "\r", "\0", "\t", "\x1b"]) {
      for (const shell of ["powershell", "posix", "cmd", "unknown"] as const) {
        expect(directoryCommandFor(`D:/safe${control}unsafe`, shell)).toEqual({
          command: null,
          failure: "control",
        });
      }
    }
  });
});
