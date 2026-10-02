import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  available: vi.fn(),
  pickBrowserFile: vi.fn(),
  pickKeyFile: vi.fn(),
  stageFile: vi.fn(),
}));
vi.mock("../../ipc/webFiles", () => ({
  browserFilesAvailable: mocks.available,
  pickBrowserFile: mocks.pickBrowserFile,
  stageFile: mocks.stageFile,
}));
vi.mock("../../ui/dialogs", () => ({ pickKeyFile: mocks.pickKeyFile }));

import { pickInlineKeyFile, resolveInlineKeyContent } from "../../features/credentials/keyStaging";

describe("inline private-key selection", () => {
  beforeEach(() => vi.clearAllMocks());

  it("keeps browser inline imports in the browser instead of staging a server copy", async () => {
    mocks.available.mockReturnValue(true);
    mocks.pickBrowserFile.mockResolvedValue({
      name: "id_ed25519",
      size: 100,
      text: async () => "INLINE KEY",
    });
    await expect(pickInlineKeyFile()).resolves.toEqual({
      path: "id_ed25519",
      content: "INLINE KEY",
    });
    expect(mocks.stageFile).not.toHaveBeenCalled();
    expect(mocks.pickKeyFile).not.toHaveBeenCalled();
  });

  it("keeps the existing 64KB private-key limit before reading browser content", async () => {
    mocks.available.mockReturnValue(true);
    const text = vi.fn(async () => "NOT A KEY");
    mocks.pickBrowserFile.mockResolvedValue({ name: "large.bin", size: 64 * 1024 + 1, text });
    await expect(pickInlineKeyFile()).rejects.toThrow("文件超过 64KB，不是私钥");
    expect(text).not.toHaveBeenCalled();
    expect(mocks.stageFile).not.toHaveBeenCalled();
  });

  it("retains the desktop native path flow", async () => {
    mocks.available.mockReturnValue(false);
    mocks.pickKeyFile.mockResolvedValue("/keys/id_rsa");
    await expect(pickInlineKeyFile()).resolves.toEqual({ path: "/keys/id_rsa", content: null });
  });

  it("uses browser content even when empty and only reads native paths as fallback", async () => {
    const read = vi.fn(async () => "NATIVE KEY");
    await expect(resolveInlineKeyContent({ path: "id", content: "" }, read)).resolves.toBe("");
    expect(read).not.toHaveBeenCalled();
    await expect(resolveInlineKeyContent({ path: "/keys/id", content: null }, read)).resolves.toBe(
      "NATIVE KEY",
    );
    expect(read).toHaveBeenCalledWith("/keys/id");
  });
});
