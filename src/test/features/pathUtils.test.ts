import { describe, expect, it } from "vitest";
import { crumbsOf, joinPath, normalizeTypedPath, parentOf, resolveRemotePath } from "../../features/files/pathUtils";

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
