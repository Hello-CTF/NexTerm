/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ preview: vi.fn(), required: vi.fn(), unlock: vi.fn(), status: vi.fn() }));
vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    assetApi: { ...actual.assetApi, inheritancePreview: mocks.preview, credentialRequired: mocks.required },
    vaultApi: { ...actual.vaultApi, status: mocks.status, unlock: mocks.unlock },
  };
});

import { inheritanceChangesOnMove, moveConfirmationText } from "../features/explorer/groupInheritance";
// eslint-disable-next-line no-restricted-imports -- the unlock gate is internal to the store and not re-exported
import { ensureVaultReadyFor } from "../app/store/connection";
import type { Asset } from "../ipc/commands";

const asset = { id: "a1", kind: "ssh", name: "web", groupId: "g1", username: null, authKind: null, keyPath: null, credId: null, options: {} } as unknown as Asset;
const field = (name: string, group: string, value: string) => ({ field: name, applied: true, value, groupId: group, groupName: group, reason: "" });

beforeEach(() => {
  vi.clearAllMocks();
});

describe("moving a host between groups", () => {
  it("names the groups and fields when the credential, authentication or jump would change", async () => {
    mocks.preview.mockImplementation(async (request: { groupId: string }) => ({
      error: "",
      fields: request.groupId === "g1" ? [field("auth", "g1", "密码 · a")] : [field("auth", "g2", "密钥 · b"), field("jump", "g2", "bastion")],
    }));
    const changes = await inheritanceChangesOnMove(asset, "g2");
    expect(changes).toEqual(["认证方式改为「密钥 · b」（来自 g2）", "跳板机改为「bastion」（来自 g2）"]);
    expect(moveConfirmationText([{ name: "web", targetName: "g2", changes }])).toContain("「web」移入 g2 后：认证方式改为");
  });

  it("is silent when nothing about the identity changes", async () => {
    mocks.preview.mockResolvedValue({ error: "", fields: [field("auth", "g1", "密码 · a")] });
    expect(await inheritanceChangesOnMove(asset, "g2")).toEqual([]);
  });

  it("reports leaving a group that supplied the authentication", async () => {
    mocks.preview.mockResolvedValue({ error: "", fields: [field("auth", "g1", "密码 · a")] });
    expect(await inheritanceChangesOnMove(asset, null)).toEqual(["认证方式不再继承自 g1"]);
  });

  it("does not ask for a move inside the same group, and never blocks when the preview fails", async () => {
    expect(await inheritanceChangesOnMove(asset, "g1")).toEqual([]);
    mocks.preview.mockRejectedValue(new Error("offline"));
    expect(await inheritanceChangesOnMove(asset, "g2")).toEqual([]);
  });
});

describe("vault prompt for an inheriting asset", () => {
  it("asks the backend whether the effective asset needs a credential", async () => {
    mocks.status.mockResolvedValue({ initialized: true, unlocked: true });
    mocks.required.mockResolvedValue(true);
    await expect(ensureVaultReadyFor({ id: "a1", name: "web", groupId: "g1", credId: null })).resolves.toBe(true);
    expect(mocks.required).toHaveBeenCalledWith("a1");
    expect(mocks.status).toHaveBeenCalled();
  });

  it("skips the question for an asset without a group", async () => {
    await expect(ensureVaultReadyFor({ id: "a1", name: "web", groupId: null, credId: null })).resolves.toBe(true);
    expect(mocks.required).not.toHaveBeenCalled();
  });
});
