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

describe("uniqueRemoteName (automatic keep-both renaming)", () => {
  it("inserts a sequence number before the extension", () => {
    expect(uniqueRemoteName("a.txt", new Set(["a.txt"]))).toBe("a (1).txt");
    expect(uniqueRemoteName("a.tar.gz", new Set(["a.tar.gz"]))).toBe("a.tar (1).gz");
  });

  it("appends a sequence number to extensionless names and dotfiles", () => {
    expect(uniqueRemoteName("README", new Set(["README"]))).toBe("README (1)");
    expect(uniqueRemoteName(".env", new Set([".env"]))).toBe(".env (1)");
  });

  it("continues incrementing when sequence numbers are taken", () => {
    const taken = new Set(["a.txt", "a (1).txt", "a (2).txt"]);
    expect(uniqueRemoteName("a.txt", taken)).toBe("a (3).txt");
  });

  it("handles names without conflicts (callers guarantee invocation only on conflicts)", () => {
    expect(uniqueRemoteName("b.txt", new Set(["a.txt"]))).toBe("b (1).txt");
  });
});

describe("findConflict", () => {
  it("finds same-named entries and returns undefined when absent", () => {
    const siblings = [entry("a.txt"), entry("sub", "dir")];
    expect(findConflict("a.txt", siblings)?.name).toBe("a.txt");
    expect(findConflict("sub", siblings)?.kind).toBe("dir");
    expect(findConflict("b.txt", siblings)).toBeUndefined();
  });
});

describe("conflictCaseInsensitive (case-insensitive target detection)", () => {
  beforeEach(() => {
    setWindowsPlatform(false);
    setMacPlatform(false);
  });

  afterEach(() => {
    setWindowsPlatform(false);
    setMacPlatform(false);
  });

  it("always folds case for winrm sessions (NTFS semantics)", () => {
    expect(conflictCaseInsensitive("winrm")).toBe(true);
  });

  it("keeps ssh/docker/device sessions case-sensitive regardless of the desktop OS", () => {
    setWindowsPlatform(true);
    setMacPlatform(true);
    expect(conflictCaseInsensitive("ssh")).toBe(false);
    expect(conflictCaseInsensitive("docker")).toBe(false);
    expect(conflictCaseInsensitive("device")).toBe(false);
  });

  it("follows the desktop OS for local sessions: fold on Windows or macOS, but not elsewhere", () => {
    expect(conflictCaseInsensitive("local")).toBe(false);
    setWindowsPlatform(true);
    expect(conflictCaseInsensitive("local")).toBe(true);
    setWindowsPlatform(false);
    setMacPlatform(true);
    expect(conflictCaseInsensitive("local")).toBe(true);
  });
});

describe("conflict matching by session type", () => {
  it("treats names differing only by case as conflicts for winrm", () => {
    const ci = conflictCaseInsensitive("winrm");
    const siblings = [entry("a.txt"), entry("Sub", "dir")];
    expect(findConflict("A.txt", siblings, ci)?.name).toBe("a.txt");
    expect(findConflict("sub", siblings, ci)?.kind).toBe("dir");
    expect(findConflict("b.txt", siblings, ci)).toBeUndefined();
  });

  it("does not treat names differing only by case as conflicts for ssh", () => {
    const ci = conflictCaseInsensitive("ssh");
    expect(findConflict("A.txt", [entry("a.txt")], ci)).toBeUndefined();
  });
});

describe("keep-both renaming on case-insensitive targets", () => {
  it("folds takenNames writes and lookups for the target", () => {
    const folded = takenNames(["ReadMe.md"], true);
    expect(folded.has("readme.md")).toBe(true);
    folded.add("NEW.txt");
    expect(folded.has("new.TXT")).toBe(true);
    const exact = takenNames(["ReadMe.md"], false);
    expect(exact.has("readme.md")).toBe(false);
    expect(exact.has("ReadMe.md")).toBe(true);
  });

  it("never generates a name that collides with an existing entry only by case", () => {
    const taken = takenNames(["A.TXT", "a (1).txt"], true);
    expect(uniqueRemoteName("a.txt", taken)).toBe("a (2).txt");
    taken.add("a (2).txt");
    expect(uniqueRemoteName("a.txt", taken)).toBe("a (3).txt");
  });

  it("preserves case-sensitive target semantics", () => {
    const taken = takenNames(["a.txt"], false);
    expect(uniqueRemoteName("A.TXT", taken)).toBe("A (1).TXT");
  });
});
