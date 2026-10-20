/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ ask: vi.fn(), create: vi.fn() }));
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask };
});
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return { ...actual, assetApi: { ...actual.assetApi, create: mocks.create } };
});

import { cloneAsset } from "../../features/explorer/assetClone";
import { useUi } from "../../app/store";
import type { Asset } from "../../ipc/commands";

const ASSET: Asset = {
  id: "a",
  groupId: null,
  kind: "ssh",
  name: "A production",
  host: "a.internal",
  port: 22,
  username: "root",
  authKind: "key",
  keyPath: "/home/a/key",
  credId: "cred-a",
  options: { env: { FOO: "bar" } },
  tags: "prod",
  note: "A 的备注",
  sort: 0,
  createdAt: 1,
  updatedAt: 1,
  deletedAt: null,
  builtin: false,
};

beforeEach(() => {
  vi.clearAllMocks();
  useUi.setState({ accountEpoch: 0 });
});

describe("cloneAsset account isolation", () => {
  it("does not create the clone when A's confirmation resolves after an account switch", async () => {
    let resolveAsk!: (value: boolean) => void;
    mocks.ask.mockImplementation(() => new Promise((resolve) => {
      resolveAsk = resolve;
    }));

    const pending = cloneAsset(ASSET);
    useUi.getState().advanceAccountEpoch();
    resolveAsk(true);

    await expect(pending).resolves.toBeNull();
    expect(mocks.create).not.toHaveBeenCalled();
  });

  it("creates the clone when the account is unchanged", async () => {
    mocks.ask.mockResolvedValue(true);
    mocks.create.mockResolvedValue({ ...ASSET, id: "a-copy" });

    await expect(cloneAsset(ASSET)).resolves.toMatchObject({ id: "a-copy" });
    expect(mocks.create).toHaveBeenCalledWith(expect.objectContaining({
      name: "A production 副本",
      credId: "cred-a",
      keyPath: "/home/a/key",
    }));
  });
});
