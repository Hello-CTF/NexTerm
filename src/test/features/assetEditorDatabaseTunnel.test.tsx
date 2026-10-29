/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { chooseSelectOption, click, clickButton, flush, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    create: vi.fn(),
    update: vi.fn(),
    groupList: vi.fn(),
    listAssets: vi.fn(),
    listCredentials: vi.fn(),
    setCredential: vi.fn(),
    probe: vi.fn(),
    connect: vi.fn(),
    toast: vi.fn(),
  };
});
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      create: mocks.create,
      update: mocks.update,
      groupList: mocks.groupList,
      groupCreate: vi.fn(),
      groupUpdate: vi.fn(),
      groupDelete: vi.fn(),
      list: mocks.listAssets,
      readKeyFile: vi.fn(),
      auditQuery: vi.fn(),
    },
    vaultApi: {
      listCredentials: mocks.listCredentials,
      setCredential: mocks.setCredential,
    },
    sessionApi: {
      probe: mocks.probe,
      connect: mocks.connect,
    },
    terminalApi: {},
  };
});

import { AssetEditor } from "../../features/explorer/AssetTree";
import { useUi } from "../../app/store";
import type { Asset } from "../../ipc/commands";

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.groupList.mockResolvedValue([]);
  mocks.listAssets.mockResolvedValue([]);
  mocks.listCredentials.mockResolvedValue([]);
  mocks.create.mockResolvedValue({ id: "saved-asset" });
  mocks.update.mockResolvedValue({ id: "saved-asset" });
  mocks.setCredential.mockResolvedValue({ id: "cred-1" });
  useUi.setState({ pushToast: mocks.toast, sessions: [] });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function makeAsset(overrides: Partial<Asset> = {}): Asset {
  return {
    id: "db-1",
    groupId: null,
    kind: "postgres",
    name: "orders-db",
    host: "db.internal",
    port: 5432,
    username: "postgres",
    authKind: "password",
    keyPath: null,
    credId: null,
    options: {},
    tags: "",
    note: "",
    sort: 0,
    createdAt: 0,
    updatedAt: 0,
    deletedAt: null,
    builtin: false,
    ...overrides,
  };
}

function sshAsset(id: string, name: string): Asset {
  return makeAsset({
    id,
    kind: "ssh",
    name,
    host: `${id}.internal`,
    port: 22,
    username: "root",
  });
}

function mountEditor(initial?: Asset) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(AssetEditor, { kind: "asset", initial, onClose: vi.fn(), onSaved: vi.fn() }),
    ),
  );
}

function queryViaSelect(): HTMLButtonElement | null {
  return mounted!.container.querySelector<HTMLButtonElement>('button[aria-label="通过 SSH 资产连接"][aria-haspopup="listbox"]');
}

function viaSelect(): HTMLButtonElement {
  const select = queryViaSelect();
  if (!select) throw new Error("via asset select not found");
  return select;
}

// viaOptions opens the SSH asset select, reads its option labels and closes it again.
function viaOptions(): string[] {
  const trigger = viaSelect();
  click(trigger);
  const texts = [...mounted!.container.querySelectorAll('[role="listbox"] [role="option"]')].map((option) => option.textContent ?? "");
  click(trigger);
  return texts;
}

function chooseKind(label: string): void {
  chooseSelectOption(mounted!.container, "类型", label);
}

function chooseVia(label: string): void {
  chooseSelectOption(mounted!.container, "通过 SSH 资产连接", label);
}

function fillRequiredFields(name = "orders-db"): void {
  setInputValue(mounted!.container.querySelector("input.nx-input") as HTMLInputElement, name);
  setInputValue(mounted!.container.querySelector('input[placeholder="1.2.3.4"]') as HTMLInputElement, "db.internal");
}

async function save(): Promise<void> {
  const button = clickButton(mounted!.container, "保存");
  await flushUntil(() => button.textContent?.trim() === "保存" && !button.disabled);
}

function createdPayload(): Record<string, unknown> {
  expect(mocks.create).toHaveBeenCalledOnce();
  return mocks.create.mock.calls[0]![0] as Record<string, unknown>;
}

function updatedPayload(): Record<string, unknown> {
  expect(mocks.update).toHaveBeenCalledOnce();
  return mocks.update.mock.calls[0]![0] as Record<string, unknown>;
}

describe("AssetEditor database SSH tunnel", () => {
  it("defaults to direct connection and omits viaAssetId when creating", async () => {
    mountEditor();
    chooseKind("PostgreSQL");
    await flush();
    expect(viaSelect().textContent).toContain("直连");

    fillRequiredFields();
    await save();
    expect(createdPayload()).toMatchObject({ kind: "postgres", options: {} });
    expect(mocks.connect).not.toHaveBeenCalled();
  });

  it("offers only SSH assets from the owner-scoped list and saves the selected ID", async () => {
    mocks.listAssets.mockResolvedValue([
      sshAsset("ssh-a", "gateway-a"),
      sshAsset("ssh-b", "gateway-b"),
      makeAsset({ id: "docker-1", kind: "docker", name: "docker-host" }),
      makeAsset({ id: "mysql-1", kind: "mysql", name: "mysql-db" }),
      makeAsset({ id: "local-1", kind: "local", name: "local", host: null, port: null }),
    ]);
    mountEditor();
    chooseKind("PostgreSQL");
    await flushUntil(() => viaOptions().length === 3);

    expect(mocks.listAssets).toHaveBeenCalled();
    const labels = viaOptions();
    expect(labels).toHaveLength(3);
    expect(labels[0]).toContain("直连");
    expect(labels[1]).toContain("gateway-a");
    expect(labels[2]).toContain("gateway-b");
    expect(mounted!.container.textContent).toContain("保存时会检查所选资产是否可用");
    chooseVia("gateway-b");
    fillRequiredFields();
    await save();
    expect(createdPayload().options).toEqual({ viaAssetId: "ssh-b" });
    expect(mocks.connect).not.toHaveBeenCalled();
  });

  it("loads an existing viaAssetId and preserves unknown options when changing the SSH asset", async () => {
    const options = {
      viaAssetId: "ssh-a",
      tls: { mode: "require" },
      futureOption: { nested: true },
    };
    mocks.listAssets.mockResolvedValue([sshAsset("ssh-a", "gateway-a"), sshAsset("ssh-b", "gateway-b")]);
    mountEditor(makeAsset({ options }));
    await flushUntil(() => viaOptions().length === 3);
    expect(viaSelect().textContent).toContain("gateway-a");

    chooseVia("gateway-b");
    await save();
    expect(updatedPayload()).toMatchObject({
      id: "db-1",
      kind: "postgres",
      options: { ...options, viaAssetId: "ssh-b" },
    });
  });

  it("clears viaAssetId without removing unknown options", async () => {
    mocks.listAssets.mockResolvedValue([sshAsset("ssh-a", "gateway-a")]);
    mountEditor(makeAsset({ options: { viaAssetId: "ssh-a", futureOption: "keep" } }));
    await flushUntil(() => viaSelect().textContent?.includes("gateway-a") === true);
    expect(viaSelect().textContent).toContain("gateway-a");

    chooseVia("直连");
    await save();
    expect(updatedPayload().options).toEqual({ futureOption: "keep" });
  });

  it("keeps an existing reference that is absent from the current owner-scoped choices", async () => {
    mocks.listAssets.mockResolvedValue([sshAsset("ssh-a", "gateway-a")]);
    mountEditor(makeAsset({ options: { viaAssetId: "missing-ssh", futureOption: "keep" } }));
    await flushUntil(() => viaOptions().length === 3);

    expect(viaSelect().textContent).toContain("不在可选列表");
    await save();
    expect(updatedPayload().options).toEqual({ viaAssetId: "missing-ssh", futureOption: "keep" });
  });

  for (const kind of ["mysql", "redis"] as const) {
    it(`offers and saves an SSH tunnel when creating ${kind}`, async () => {
      mocks.listAssets.mockResolvedValue([sshAsset("ssh-a", "gateway-a")]);
      mountEditor();
      chooseKind(kind === "mysql" ? "MySQL / MariaDB" : "Redis");
      await flushUntil(() => viaOptions().length === 2);

      chooseVia("gateway-a");
      fillRequiredFields();
      await save();
      expect(createdPayload()).toMatchObject({ kind, options: { viaAssetId: "ssh-a" } });
    });
  }

  it("loads a MySQL/MariaDB tunnel and preserves it when switching to PostgreSQL", async () => {
    mocks.listAssets.mockResolvedValue([sshAsset("ssh-a", "gateway-a")]);
    mountEditor(makeAsset({
      kind: "mysql",
      port: 3306,
      options: { viaAssetId: "ssh-a", futureOption: "keep" },
    }));
    await flushUntil(() => viaSelect().textContent?.includes("gateway-a") === true);
    expect(viaSelect().textContent).toContain("gateway-a");

    chooseKind("PostgreSQL");
    expect(viaSelect().textContent).toContain("gateway-a");
    await save();
    expect(updatedPayload()).toMatchObject({
      kind: "postgres",
      options: { viaAssetId: "ssh-a", futureOption: "keep" },
    });
  });

  it("removes viaAssetId after switching to a non-database asset", async () => {
    mocks.listAssets.mockResolvedValue([sshAsset("ssh-a", "gateway-a")]);
    mountEditor(makeAsset({ options: { viaAssetId: "ssh-a", futureOption: "keep" } }));
    await flushUntil(() => viaOptions().length === 2);

    chooseKind("SSH");
    expect(queryViaSelect()).toBeNull();
    await save();
    expect(updatedPayload()).toMatchObject({ kind: "ssh" });
    expect(updatedPayload().options).not.toHaveProperty("viaAssetId");
  });
});
