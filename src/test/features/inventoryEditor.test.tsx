/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { clickButton, mountWithClient, setInputValue, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  create: vi.fn(),
  update: vi.fn(),
  groupList: vi.fn(),
  listAssets: vi.fn(),
  listCredentials: vi.fn(),
  setCredential: vi.fn(),
  ask: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      ...actual.assetApi,
      create: mocks.create,
      update: mocks.update,
      groupList: mocks.groupList,
      list: mocks.listAssets,
      readKeyFile: vi.fn(),
    },
    vaultApi: { ...actual.vaultApi, listCredentials: mocks.listCredentials, setCredential: mocks.setCredential },
  };
});
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask };
});
vi.mock("../../features/credentials/useVaultInitGate", () => ({
  useVaultInitGate: () => ({
    ensureVaultInit: vi.fn().mockResolvedValue(true),
    ensureVaultReady: vi.fn().mockResolvedValue(true),
    vaultInitGate: null,
    vaultStatus: { initialized: true, unlocked: true },
  }),
}));

import { AssetEditor } from "../../features/explorer/AssetTree";
import { cloneAsset } from "../../features/explorer/assetClone";
import { useUi } from "../../app/store";
import type { Asset } from "../../ipc/commands";

const MANAGED: Asset = {
  id: "a-managed",
  groupId: "g1",
  kind: "ssh",
  name: "web-source",
  host: "10.0.0.8",
  port: 22,
  username: "deploy",
  authKind: "key",
  keyPath: "/source/id_ed25519",
  credId: null,
  options: {
    inventory: { source: "ssh-config", key: "host/web-source", options: ["inventoryTarget", "jumpAssetId"] },
    inventoryTarget: { type: "ssh-config", alias: "web-source" },
    jumpAssetId: "jump-1",
    startupCommand: "uptime",
  },
  tags: "old",
  note: "old note",
  sort: 0,
  createdAt: 1,
  updatedAt: 1,
  deletedAt: null,
  builtin: false,
};

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.groupList.mockResolvedValue([
    { id: "g1", parentId: null, name: "source", sort: 0, createdAt: 1, updatedAt: 1 },
    { id: "g2", parentId: null, name: "other", sort: 0, createdAt: 1, updatedAt: 1 },
  ]);
  mocks.listAssets.mockResolvedValue([]);
  mocks.listCredentials.mockResolvedValue([]);
  mocks.update.mockResolvedValue(MANAGED);
  mocks.create.mockImplementation((input: Partial<Asset>) => Promise.resolve({ ...MANAGED, ...input }));
  mocks.ask.mockResolvedValue(true);
  useUi.setState({ pushToast: mocks.toast, sessions: [] });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("managed asset editing and cloning", () => {
  it("disables source fields and saves only user-owned changes", async () => {
    mounted = mountWithClient(createElement(AssetEditor, { kind: "asset", initial: MANAGED, onClose: vi.fn(), onSaved: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("由 SSH 配置文件 同步"));
    const name = mounted!.container.querySelector<HTMLInputElement>('input[placeholder="web-01"]')!;
    const host = mounted!.container.querySelector<HTMLInputElement>('input[placeholder="1.2.3.4"]')!;
    const tags = mounted!.container.querySelector<HTMLInputElement>('input[placeholder="用逗号分隔，如 production, 欧洲"]')!;
    const note = mounted!.container.querySelector<HTMLTextAreaElement>('textarea[placeholder="备注（可选）"]')!;
    expect(name.disabled).toBe(true);
    expect(host.matches(":disabled")).toBe(true);
    expect(tags.disabled).toBe(false);
    expect(note.disabled).toBe(false);
    setInputValue(tags, "prod, web");
    setInputValue(note, "user note");
    clickButton(mounted!.container, "保存");
    await waitFor(() => expect(mocks.update).toHaveBeenCalled());
    const patch = mocks.update.mock.calls[0]![0] as Record<string, unknown>;
    expect(patch).toMatchObject({
      id: "a-managed",
      kind: "ssh",
      name: "web-source",
      groupId: "g1",
      host: "10.0.0.8",
      port: 22,
      username: "deploy",
      authKind: "key",
      keyPath: "/source/id_ed25519",
      tags: "prod, web",
      note: "user note",
    });
    expect(patch.options).toMatchObject({
      inventory: { source: "ssh-config", key: "host/web-source" },
      jumpAssetId: "jump-1",
      startupCommand: "uptime",
    });
    expect(mocks.setCredential).not.toHaveBeenCalled();
  });

  it("clones as a manual asset without source ownership metadata", async () => {
    await cloneAsset(MANAGED);
    expect(mocks.create).toHaveBeenCalled();
    const input = mocks.create.mock.calls[0]![0] as Partial<Asset>;
    expect(input.name).toBe("web-source 副本");
    expect(input.options).toBeDefined();
    expect(input.options).not.toHaveProperty("inventory");
    expect(input.options).not.toHaveProperty("inventoryTarget");
    expect(input.options).toMatchObject({ jumpAssetId: "jump-1", startupCommand: "uptime" });
  });
});
