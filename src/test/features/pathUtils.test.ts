import { describe, expect, it } from "vitest";
import {
  crumbsOf,
  directoryCommandFor,
  isRoot,
  joinPath,
  normalizeTypedPath,
  parentOf,
  resolveRemotePath,
} from "../../features/files/pathUtils";

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
