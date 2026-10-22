/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ read: vi.fn(), write: vi.fn() }));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return { ...actual, fsApi: { ...actual.fsApi, read: mocks.read, write: mocks.write } };
});

import { loadFileEditorSaveChange } from "../../features/files/FileEditor";

beforeEach(() => {
  vi.clearAllMocks();
});

describe("FileEditor save-time disk baseline", () => {
  it("returns the current disk text and bytes for the diff and guarded write", async () => {
    mocks.read.mockResolvedValue({ path: "/srv/shop/compose.yml", size: 7, contentBase64: btoa("current") });
    const change = await loadFileEditorSaveChange({
      sessionId: "s1",
      path: "/srv/shop/compose.yml",
      editorBase: "current",
      content: "edited",
      encoding: "auto",
    });
    expect(change).toEqual({
      sessionId: "s1",
      path: "/srv/shop/compose.yml",
      previous: "current",
      baseContentBase64: btoa("current"),
      content: "edited",
    });
    expect(mocks.read).toHaveBeenCalledWith("s1", "/srv/shop/compose.yml");
    expect(mocks.write).not.toHaveBeenCalled();
  });

  it("rejects external modification instead of overwriting it", async () => {
    mocks.read.mockResolvedValue({ path: "/srv/shop/compose.yml", size: 8, contentBase64: btoa("external") });
    await expect(loadFileEditorSaveChange({
      sessionId: "s1",
      path: "/srv/shop/compose.yml",
      editorBase: "opening",
      content: "edited",
      encoding: "auto",
    })).rejects.toThrow("外部修改");
    expect(mocks.write).not.toHaveBeenCalled();
  });
});
