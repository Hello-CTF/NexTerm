/** @vitest-environment jsdom */
import { describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ saveBrowserBlob: vi.fn(async (_name: string, _blob: Blob) => true) }));
vi.mock("../../ipc/webFiles", () => ({ saveBrowserBlob: mocks.saveBrowserBlob }));

import { relativeSegments, saveRemoteFiles } from "../../features/remote/remoteFileSave";

describe("remote file save", () => {
  it("keeps the copied folder structure and refuses paths that would leave the save folder", () => {
    expect(relativeSegments({ name: "a.txt", size: 1 })).toEqual(["a.txt"]);
    expect(relativeSegments({ name: "b.txt", path: "docs\\2026", size: 1 })).toEqual(["docs", "2026", "b.txt"]);
    expect(relativeSegments({ name: "c.txt", path: "..\\up", size: 1 })).toBeNull();
    expect(relativeSegments({ name: "..", size: 1 })).toBeNull();
    expect(relativeSegments({ name: "d.txt", path: "C:\\Windows", size: 1 })).toBeNull();
  });

  it("offers each file as a browser download and reports the ones that failed", async () => {
    const download = vi.fn(async (index: number) => {
      if (index === 2) throw new Error("transfer failed");
      return new Blob([`file ${index}`]);
    });
    const outcome = await saveRemoteFiles(
      [
        { name: "a.txt", size: 6 },
        { name: "dir", size: 0, isDirectory: true },
        { name: "b.txt", size: 6 },
      ],
      download,
    );
    expect(download.mock.calls.map(([index]) => index)).toEqual([0, 2]);
    expect(mocks.saveBrowserBlob.mock.calls.map(([name]) => name)).toEqual(["a.txt"]);
    expect(outcome.status).toBe("done");
    expect(outcome.note).toContain("b.txt");
  });

  it("has nothing to save for a copy of folders only", async () => {
    expect(await saveRemoteFiles([{ name: "dir", size: 0, isDirectory: true }], vi.fn())).toEqual({ status: "empty" });
  });
});
