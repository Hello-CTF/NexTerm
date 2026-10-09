/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, clickButton, flushUntil, mount, type MountedView } from "./features/reactTestUtils";
import { buildPlaintextNxbm } from "../ipc/nxbm";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    digest: vi.fn(),
    exportAssets: vi.fn(),
    importBundle: vi.fn(),
    readBundleFile: vi.fn(),
    writeBundleFile: vi.fn(),
    pickBundleBuffer: vi.fn(),
    saveBundleBytes: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  sessionApi: {},
  terminalApi: {},
  vaultApi: {},
  syncApi: {
    digest: mocks.digest,
    exportAssets: mocks.exportAssets,
    importBundle: mocks.importBundle,
    readBundleFile: mocks.readBundleFile,
    writeBundleFile: mocks.writeBundleFile,
  },
}));
vi.mock("../ipc/bundleFiles", () => ({
  pickBundleBuffer: mocks.pickBundleBuffer,
  saveBundleBytes: mocks.saveBundleBytes,
}));

import { SyncBundleCard } from "../features/settings/SyncBundleCard";
import { useUi } from "../app/store";

const DIGEST = {
  origin: "local",
  protocol: 1,
  appVersion: "test",
  desktop: false,
  assets: [
    { id: "a1", name: "web-01", kind: "ssh", host: "10.0.0.1", username: "deploy", updatedAt: 100, deletedAt: null, hasCred: true, groupId: null },
  ],
};

const EXPORTED_BUNDLE = {
  protocol: 1,
  origin: "local",
  exportedAt: Date.now(),
  groups: [],
  assets: [{ id: "a1" }],
  creds: [],
  warnings: [],
};

function encryptedNxbm(): ArrayBuffer {
  const header = new Uint8Array(64);
  header.set([0x4e, 0x58, 0x42, 0x4d, 1, 1], 0);
  header[18] = 16;
  header[35] = 12;
  const body = new Uint8Array([1, 2, 3, 4]);
  const out = new Uint8Array(68);
  out.set(header, 0);
  out.set(body, 64);
  return out.buffer;
}

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: mocks.toast });
  mocks.digest.mockResolvedValue(DIGEST);
  mocks.exportAssets.mockResolvedValue(EXPORTED_BUNDLE);
  mocks.saveBundleBytes.mockResolvedValue(true);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function withClient(node: React.ReactElement): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function text(): string {
  return mounted?.container.textContent ?? "";
}

async function mountCard() {
  mounted = withClient(createElement(SyncBundleCard));
  clickButton(mounted!.container, "导出资产包");
  await flushUntil(() => text().includes("web-01"));
}

function openImport() {
  clickButton(mounted!.container, "导入资产包");
}

describe("SyncBundleCard 网页版资产包", () => {
  it("网页版导出为未加密 .nxbm 容器，不出现口令输入", async () => {
    await mountCard();
    expect(text()).toContain("浏览器导出为未加密的 .nxbm 容器");
    expect(mounted!.container.querySelector("#bundle-password")).toBeNull();

    const row = [...mounted!.container.querySelectorAll("label")].find((l) => l.textContent?.includes("web-01"))!;
    click(row.querySelector('input[type="checkbox"]')!);
    clickButton(mounted!.container, "导出资产包 (.nxbm)");
    await flushUntil(() => text().includes("已导出"));

    expect(mocks.saveBundleBytes).toHaveBeenCalledOnce();
    const [name, bytes] = mocks.saveBundleBytes.mock.calls[0] as [string, Uint8Array];
    expect(name).toContain(".nxbm");
    expect(String.fromCharCode(...bytes.slice(0, 4))).toBe("NXBM");
    expect(bytes[5] & 1).toBe(0);
    expect(mocks.writeBundleFile).not.toHaveBeenCalled();
  });

  it("选中加密的 .nxbm 容器：明确提示去桌面版导入", async () => {
    mocks.pickBundleBuffer.mockResolvedValue({ name: "backup.nxbm", buffer: encryptedNxbm() });
    await mountCard();
    openImport();
    clickButton(mounted!.container, "选择资产包文件…");
    await flushUntil(() => text().includes("这是加密的 .nxbm 资产包"));
    expect(text()).toContain("请在桌面版 NexTerm 中导入");
    expect(mocks.importBundle).not.toHaveBeenCalled();
  });

  it("未加密的 .nxbm 容器在网页版照常预览导入", async () => {
    mocks.pickBundleBuffer.mockResolvedValue({
      name: "plain.nxbm",
      buffer: buildPlaintextNxbm(
        JSON.stringify({
          protocol: 1,
          origin: "old-device",
          exportedAt: Date.now(),
          groups: [],
          assets: [
            { id: "a9", groupId: null, kind: "ssh", name: "legacy-01", host: "10.9.9.9", port: 22, username: "root", authKind: "password", keyPath: null, credId: null, optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 2, deletedAt: null },
          ],
          creds: [],
        }),
      ).buffer,
    });
    await mountCard();
    openImport();
    clickButton(mounted!.container, "选择资产包文件…");
    await flushUntil(() => text().includes("确认导入 1 条资产"));
    expect(text()).toContain("old-device");
  });

  it("非 .nxbm 文件明确拒绝", async () => {
    mocks.pickBundleBuffer.mockResolvedValue({
      name: "legacy.json",
      buffer: new TextEncoder().encode(JSON.stringify({ protocol: 1 })).buffer as ArrayBuffer,
    });
    await mountCard();
    openImport();
    clickButton(mounted!.container, "选择资产包文件…");
    await flushUntil(() => text().includes("只支持 .nxbm 资产包文件"));
    expect(mocks.importBundle).not.toHaveBeenCalled();
  });
});
