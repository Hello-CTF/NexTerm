/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, flushUntil, mount, setSelectValue, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    digest: vi.fn(),
    exportAssets: vi.fn(),
    importBundle: vi.fn(),
    readBundleFile: vi.fn(),
    writeBundleFile: vi.fn(),
    pickBundleFile: vi.fn(),
    saveBundleFile: vi.fn(),
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
  pickBundleFile: mocks.pickBundleFile,
  saveBundleFile: mocks.saveBundleFile,
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

const NXBM_HEADER_ENCRYPTED = "NXBM\u0001\u0001\u0000\u0000\u0000\u0003";
const NXBM_HEADER_PLAINTEXT = "NXBM\u0001\u0000\u0000\u0000\u0000\u0003";

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: mocks.toast });
  mocks.digest.mockResolvedValue(DIGEST);
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
  it("网页版加密格式不可选但可见；未主动选择格式前不出现明文警告、导出保持禁用", async () => {
    await mountCard();
    const select = mounted!.container.querySelector<HTMLSelectElement>("#bundle-format")!;
    const encryptedOption = [...select.options].find((o) => o.value === "encrypted")!;
    expect(encryptedOption.disabled).toBe(true);
    expect(encryptedOption.textContent).toContain("仅桌面版");
    expect(text()).toContain("加密导出（.nxbm）仅在桌面版 NexTerm 可用");
    expect(text()).not.toContain("将以明文导出");

    setSelectValue(select, "plaintext");
    await flushUntil(() => text().includes("将以明文导出"));
  });

  it("选中 flag=1 的 NXBM 加密容器：明确提示加密与桌面版导入", async () => {
    mocks.pickBundleFile.mockResolvedValue({ name: "backup.nxbm", text: `${NXBM_HEADER_ENCRYPTED}garbage` });
    await mountCard();
    openImport();
    clickButton(mounted!.container, "选择资产包文件…");
    await flushUntil(() => text().includes("这是加密的资产包"));
    expect(text()).toContain("请在桌面版 NexTerm 中导入");
    expect(mocks.importBundle).not.toHaveBeenCalled();
  });

  it("选中 flag=0 的 NXBM 明文容器：不误报为加密，提示去桌面版读取", async () => {
    mocks.pickBundleFile.mockResolvedValue({ name: "plain.nxbm", text: `${NXBM_HEADER_PLAINTEXT}{"protocol":1}` });
    await mountCard();
    openImport();
    clickButton(mounted!.container, "选择资产包文件…");
    await flushUntil(() => text().includes("资产包容器"));
    expect(text()).not.toContain("这是加密的资产包");
    expect(text()).toContain("请在桌面版 NexTerm 中导入");
    expect(text()).not.toContain("不是合法的 JSON");
    expect(mocks.importBundle).not.toHaveBeenCalled();
  });

  it("旧版 .json 资产包在网页版照常预览导入", async () => {
    mocks.pickBundleFile.mockResolvedValue({
      name: "legacy.json",
      text: JSON.stringify({
        protocol: 1,
        origin: "old-device",
        exportedAt: Date.now(),
        groups: [],
        assets: [
          { id: "a9", groupId: null, kind: "ssh", name: "legacy-01", host: "10.9.9.9", port: 22, username: "root", authKind: "password", keyPath: null, credId: null, optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 2, deletedAt: null },
        ],
        creds: [],
      }),
    });
    await mountCard();
    openImport();
    clickButton(mounted!.container, "选择资产包文件…");
    await flushUntil(() => text().includes("确认导入 1 条资产"));
    expect(text()).toContain("old-device");
  });
});
