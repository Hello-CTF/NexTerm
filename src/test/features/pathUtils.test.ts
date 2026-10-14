import { describe, expect, it } from "vitest";
import {
  crumbsOf,
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
