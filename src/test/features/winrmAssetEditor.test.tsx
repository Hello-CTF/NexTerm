/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { chooseSelectOption, clickButton, flush, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    create: vi.fn(),
    update: vi.fn(),
    groupList: vi.fn(),
    listAssets: vi.fn(),
    listCredentials: vi.fn(),
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
      setCredential: vi.fn(),
    },
    sessionApi: {
      probe: vi.fn(),
      connect: vi.fn(),
    },
    terminalApi: {},
  };
});

vi.mock("../../features/fleet/clients", () => ({
  fleetClient: () => ({ devices: async () => ({ devices: [] }) }),
}));

import { AssetEditor } from "../../features/explorer/AssetTree";
import { optionsForKindChange, readWinRMAuthOptions, writeWinRMAuthOptions } from "../../features/explorer/assetKindFields";
import { useUi } from "../../app/store";
import type { Asset } from "../../ipc/commands";

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.groupList.mockResolvedValue([]);
  mocks.listAssets.mockResolvedValue([]);
  mocks.listCredentials.mockResolvedValue([]);
  mocks.update.mockResolvedValue({ id: "win-1" });
  useUi.setState({ pushToast: mocks.toast, sessions: [] });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function winrmAsset(options: Record<string, unknown>): Asset {
  return {
    id: "win-1",
    groupId: null,
    kind: "winrm",
    name: "win01",
    host: "win01.corp.example.com",
    port: 5985,
    username: "alice@corp.example.com",
    authKind: "password",
    keyPath: null,
    credId: null,
    options,
    tags: "",
    note: "",
    sort: 0,
    createdAt: 0,
    updatedAt: 0,
    deletedAt: null,
    builtin: false,
  };
}

function mountEditor(initial: Asset) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(AssetEditor, { kind: "asset", initial, onClose: vi.fn(), onSaved: vi.fn() }),
    ),
  );
}

function field(label: string): HTMLInputElement {
  const input = mounted!.container.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`);
  if (!input) throw new Error(`field ${label} not found`);
  return input;
}

async function save(): Promise<Record<string, unknown>> {
  const button = clickButton(mounted!.container, "保存");
  await flushUntil(() => button.textContent?.trim() === "保存" && !button.disabled);
  expect(mocks.update).toHaveBeenCalledOnce();
  return mocks.update.mock.calls[0]![0] as Record<string, unknown>;
}

describe("WinRM authentication options", () => {
  it("reads NTLM as the default and keeps a stored Basic choice", () => {
    expect(readWinRMAuthOptions({}).auth).toBe("ntlm");
    expect(readWinRMAuthOptions({ auth: "Kerberos", kerberosRealm: "CORP.EXAMPLE.COM" })).toEqual({
      auth: "kerberos",
      kerberosRealm: "CORP.EXAMPLE.COM",
      kerberosKdc: "",
      kerberosSpn: "",
    });
    expect(readWinRMAuthOptions({ auth: "basic" }).auth).toBe("basic");
  });

  it("writes the Kerberos settings only while Kerberos is selected", () => {
    const values = { auth: "kerberos" as const, kerberosRealm: " corp.example.com ", kerberosKdc: "dc1, dc2", kerberosSpn: "" };
    expect(writeWinRMAuthOptions({ tls: true }, values)).toEqual({
      tls: true,
      auth: "kerberos",
      kerberosRealm: "corp.example.com",
      kerberosKdc: "dc1, dc2",
    });
    expect(writeWinRMAuthOptions({ tls: true, auth: "kerberos", kerberosKdc: "dc1" }, { ...values, auth: "ntlm" })).toEqual({ tls: true });
  });

  it("drops the WinRM options when the asset becomes another kind", () => {
    expect(optionsForKindChange("winrm", "ssh", { auth: "kerberos", kerberosRealm: "CORP", wakeMac: "AA:BB:CC:DD:EE:FF" })).toEqual({
      wakeMac: "AA:BB:CC:DD:EE:FF",
    });
  });
});

describe("AssetEditor WinRM authentication", () => {
  it("saves Kerberos with its realm, KDC and SPN and keeps unrelated options", async () => {
    mountEditor(winrmAsset({ tls: true }));
    await flush();
    expect(mounted!.container.querySelector('input[aria-label="Kerberos 域"]')).toBeNull();
    chooseSelectOption(mounted!.container, "WinRM 认证协议", "Kerberos");
    await flush();
    setInputValue(field("Kerberos 域"), "corp.example.com");
    clickButton(mounted!.container, "Kerberos 高级设置");
    await flush();
    setInputValue(field("KDC 地址"), "dc1.corp.example.com");
    setInputValue(field("服务主体名称（SPN）"), "HTTP/win01.corp.example.com");
    const payload = await save();
    expect(payload.options).toEqual({
      tls: true,
      auth: "kerberos",
      kerberosRealm: "corp.example.com",
      kerberosKdc: "dc1.corp.example.com",
      kerberosSpn: "HTTP/win01.corp.example.com",
    });
  });

  it("switching back to NTLM removes the Kerberos settings", async () => {
    mountEditor(winrmAsset({ auth: "kerberos", kerberosRealm: "CORP.EXAMPLE.COM", kerberosSpn: "HTTP/win01" }));
    await flush();
    expect(field("Kerberos 域").value).toBe("CORP.EXAMPLE.COM");
    // A stored override opens the advanced settings so it stays visible.
    expect(field("服务主体名称（SPN）").value).toBe("HTTP/win01");
    chooseSelectOption(mounted!.container, "WinRM 认证协议", "NTLM");
    await flush();
    const payload = await save();
    expect(payload.options).toEqual({});
  });
});
