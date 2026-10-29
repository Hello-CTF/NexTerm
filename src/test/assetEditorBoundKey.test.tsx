/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { flush, mount, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  groupList: vi.fn(),
  listAssets: vi.fn(),
  listCredentials: vi.fn(),
}));
vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      create: vi.fn(),
      update: vi.fn(),
      groupList: mocks.groupList,
      groupCreate: vi.fn(),
      groupUpdate: vi.fn(),
      groupDelete: vi.fn(),
      list: mocks.listAssets,
      readKeyFile: vi.fn(),
      auditQuery: vi.fn(),
    },
    vaultApi: { listCredentials: mocks.listCredentials, setCredential: vi.fn() },
    sessionApi: { probe: vi.fn() },
    terminalApi: {},
  };
});

import { AssetEditor } from "../features/explorer/AssetTree";
import type { Asset } from "../ipc/commands";

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.groupList.mockResolvedValue([]);
  mocks.listAssets.mockResolvedValue([]);
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("editing an asset bound to a vault private key", () => {
  it("selects the bound key once the credential list has loaded", async () => {
    mocks.listCredentials.mockResolvedValue([
      { id: "cred-a", name: "other", kind: "private_key", source: "vault", usedBy: [] },
      { id: "cred-1", name: "prod-key", kind: "private_key", source: "vault", usedBy: ["a1"] },
    ]);
    const initial = {
      id: "a1", kind: "ssh", name: "web", host: "10.0.0.1", port: 22, username: "root",
      authKind: "key", credId: "cred-1", tags: "", note: "",
    } as unknown as Asset;
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mounted = mount(
      createElement(
        QueryClientProvider,
        { client },
        createElement(AssetEditor, { kind: "asset", initial, onClose: vi.fn(), onSaved: vi.fn() }),
      ),
    );
    await flush();
    await flush();
    const select = mounted.container.querySelector<HTMLButtonElement>('button[aria-label="选择私钥凭据"]');
    expect(select?.textContent).toContain("prod-key");
  });
});
