import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { setMacPlatform, setWindowsPlatform } from "../../app/platform";
import {
  conflictCaseInsensitive,
  findConflict,
  takenNames,
  uniqueRemoteName,
} from "../../features/files/transferQueue";
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

describe("conflictCaseInsensitive（大小写不敏感目标判定）", () => {
  beforeEach(() => {
    setWindowsPlatform(false);
    setMacPlatform(false);
  });

  afterEach(() => {
    setWindowsPlatform(false);
    setMacPlatform(false);
  });

  it("winrm 会话始终折叠大小写（NTFS 语义）", () => {
    expect(conflictCaseInsensitive("winrm")).toBe(true);
  });

  it("ssh/docker/device 会话保持大小写敏感，不受桌面系统影响", () => {
    setWindowsPlatform(true);
    setMacPlatform(true);
    expect(conflictCaseInsensitive("ssh")).toBe(false);
    expect(conflictCaseInsensitive("docker")).toBe(false);
    expect(conflictCaseInsensitive("device")).toBe(false);
  });

  it("local 会话跟随桌面系统：Windows 或 macOS 折叠，其余不折叠", () => {
    expect(conflictCaseInsensitive("local")).toBe(false);
    setWindowsPlatform(true);
    expect(conflictCaseInsensitive("local")).toBe(true);
    setWindowsPlatform(false);
    setMacPlatform(true);
    expect(conflictCaseInsensitive("local")).toBe(true);
  });
});

describe("按会话类型匹配冲突", () => {
  it("winrm：仅大小写不同的名称算冲突", () => {
    const ci = conflictCaseInsensitive("winrm");
    const siblings = [entry("a.txt"), entry("Sub", "dir")];
    expect(findConflict("A.txt", siblings, ci)?.name).toBe("a.txt");
    expect(findConflict("sub", siblings, ci)?.kind).toBe("dir");
    expect(findConflict("b.txt", siblings, ci)).toBeUndefined();
  });

  it("ssh：仅大小写不同的名称不算冲突", () => {
    const ci = conflictCaseInsensitive("ssh");
    expect(findConflict("A.txt", [entry("a.txt")], ci)).toBeUndefined();
  });
});

describe("大小写不敏感目标上的保留两者重命名", () => {
  it("takenNames 按目标折叠写入与查询", () => {
    const folded = takenNames(["ReadMe.md"], true);
    expect(folded.has("readme.md")).toBe(true);
    folded.add("NEW.txt");
    expect(folded.has("new.TXT")).toBe(true);
    const exact = takenNames(["ReadMe.md"], false);
    expect(exact.has("readme.md")).toBe(false);
    expect(exact.has("ReadMe.md")).toBe(true);
  });

  it("生成的名字永不与现有项仅大小写不同地撞名", () => {
    const taken = takenNames(["A.TXT", "a (1).txt"], true);
    expect(uniqueRemoteName("a.txt", taken)).toBe("a (2).txt");
    taken.add("a (2).txt");
    expect(uniqueRemoteName("a.txt", taken)).toBe("a (3).txt");
  });

  it("大小写敏感目标保持原语义", () => {
    const taken = takenNames(["a.txt"], false);
    expect(uniqueRemoteName("A.TXT", taken)).toBe("A (1).TXT");
  });
});
