import { describe, expect, it } from "vitest";
import { findConflict, uniqueRemoteName } from "../../features/files/transferQueue";
import type { FileEntryDto } from "../../ipc/types";

function entry(name: string, kind = "file"): FileEntryDto {
  return {
    name,
    path: `~/${name}`,
    kind,
    size: 10,
    mode: "644",
    owner: "u",
    group: "u",
    mtime: 1,
    symlinkTarget: null,
  };
}

describe("uniqueRemoteName（保留两者自动重命名）", () => {
  it("在扩展名前插入序号", () => {
    expect(uniqueRemoteName("a.txt", new Set(["a.txt"]))).toBe("a (1).txt");
    expect(uniqueRemoteName("a.tar.gz", new Set(["a.tar.gz"]))).toBe("a.tar (1).gz");
  });

  it("无扩展名与点文件追加序号", () => {
    expect(uniqueRemoteName("README", new Set(["README"]))).toBe("README (1)");
    expect(uniqueRemoteName(".env", new Set([".env"]))).toBe(".env (1)");
  });

  it("序号被占用则继续递增", () => {
    const taken = new Set(["a.txt", "a (1).txt", "a (2).txt"]);
    expect(uniqueRemoteName("a.txt", taken)).toBe("a (3).txt");
  });

  it("无冲突时原名可用（调用方保证只在冲突时调用）", () => {
    expect(uniqueRemoteName("b.txt", new Set(["a.txt"]))).toBe("b (1).txt");
  });
});

describe("findConflict", () => {
  it("按名称找同名项，找不到返回 undefined", () => {
    const siblings = [entry("a.txt"), entry("sub", "dir")];
    expect(findConflict("a.txt", siblings)?.name).toBe("a.txt");
    expect(findConflict("sub", siblings)?.kind).toBe("dir");
    expect(findConflict("b.txt", siblings)).toBeUndefined();
  });
});
