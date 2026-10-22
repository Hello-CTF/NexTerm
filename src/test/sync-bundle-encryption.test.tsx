/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, clickButton, flush, flushUntil, mount, setInputValue, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    digest: vi.fn(),
    exportAssets: vi.fn(),
    importBundle: vi.fn(),
    readBundleFile: vi.fn(),
    writeBundleFile: vi.fn(),
    pickBundleBuffer: vi.fn(),
    saveBundleBytes: vi.fn(),
    openWailsFile: vi.fn(),
    saveWailsFile: vi.fn(),
    promptText: vi.fn(),
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
vi.mock("../ipc/wails", () => ({
  openWailsFile: mocks.openWailsFile,
  saveWailsFile: mocks.saveWailsFile,
}));
vi.mock("../ui/dialogs", () => ({ promptText: mocks.promptText }));

import { SyncBundleCard } from "../features/settings/SyncBundleCard";
import { useUi } from "../app/store";

const DIGEST = {
  origin: "local",
  protocol: 1,
  appVersion: "test",
  desktop: true,
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

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: mocks.toast });
  mocks.digest.mockResolvedValue(DIGEST);
  mocks.exportAssets.mockResolvedValue(EXPORTED_BUNDLE);
  mocks.saveWailsFile.mockResolvedValue("/home/user/nexterm-assets.nxbm");
  mocks.writeBundleFile.mockResolvedValue({ encrypted: true });
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

function exportRow(name: string): HTMLElement {
  const row = [...mounted!.container.querySelectorAll("label")].find((l) => l.textContent?.includes(name));
  if (!row) throw new Error(`row ${name} not found`);
  return row;
}

function exportButton(): HTMLButtonElement {
  const btn = [...mounted!.container.querySelectorAll("button")].find(
    (b) => b.textContent?.trim() === "导出资产包（.nxbm）",
  ) as HTMLButtonElement | undefined;
  if (!btn) throw new Error("export button not found");
  return btn;
}

describe("SyncBundleCard single .nxbm export format", () => {
  it("enables export after selecting assets without offering a format choice", async () => {
    await mountCard();
    expect(exportButton().disabled).toBe(true);
    click(exportRow("web-01").querySelector('input[type="checkbox"]')!);
    expect(exportButton().disabled).toBe(false);
    expect(text()).toContain("未设置口令");
    expect(mounted!.container.querySelector("#bundle-password")).not.toBeNull();
  });

  it("disables export for mismatched passphrases and enables it once they match", async () => {
    await mountCard();
    click(exportRow("web-01").querySelector('input[type="checkbox"]')!);
    setInputValue(mounted!.container.querySelector<HTMLInputElement>("#bundle-password")!, "correct-horse");
    setInputValue(mounted!.container.querySelector<HTMLInputElement>("#bundle-password2")!, "battery-staple");
    expect(text()).toContain("两次输入的导出口令不一致");
    expect(exportButton().disabled).toBe(true);

    setInputValue(mounted!.container.querySelector<HTMLInputElement>("#bundle-password2")!, "correct-horse");
    await flushUntil(() => !exportButton().disabled);
  });

  it("writes password-protected exports as .nxbm with the password and marks the summary as encrypted", async () => {
    await mountCard();
    click(exportRow("web-01").querySelector('input[type="checkbox"]')!);
    setInputValue(mounted!.container.querySelector<HTMLInputElement>("#bundle-password")!, "correct-horse");
    setInputValue(mounted!.container.querySelector<HTMLInputElement>("#bundle-password2")!, "correct-horse");
    click(exportButton());
    await flushUntil(() => text().includes("已加密导出"));

    expect(mocks.saveWailsFile).toHaveBeenCalledOnce();
    expect(String(mocks.saveWailsFile.mock.calls[0]?.[0])).toContain(".nxbm");
    expect(mocks.writeBundleFile).toHaveBeenCalledOnce();
    const [path, content, password] = mocks.writeBundleFile.mock.calls[0] as [string, string, string];
    expect(path).toBe("/home/user/nexterm-assets.nxbm");
    expect(JSON.parse(content).protocol).toBe(1);
    expect(password).toBe("correct-horse");
    expect(mocks.saveBundleBytes).not.toHaveBeenCalled();
    expect(mounted!.container.querySelector<HTMLInputElement>("#bundle-password")!.value).toBe("");
  });

  it("exports an unencrypted .nxbm container when the passphrase is blank", async () => {
    await mountCard();
    click(exportRow("web-01").querySelector('input[type="checkbox"]')!);
    click(exportButton());
    await flushUntil(() => text().includes("已导出"));

    expect(mocks.writeBundleFile).toHaveBeenCalledOnce();
    const [, content, password] = mocks.writeBundleFile.mock.calls[0] as [string, string, string];
    expect(JSON.parse(content).protocol).toBe(1);
    expect(password).toBe("");
    expect(text()).toContain("已导出");
  });
});

describe("SyncBundleCard encrypted import", () => {
  it("prompts for a passphrase after reading an encrypted bundle on desktop and rereads the same file with it", async () => {
    mocks.openWailsFile.mockResolvedValue("/home/user/backup.nxbm");
    mocks.readBundleFile
      .mockRejectedValueOnce({ code: "bad_param", message: "资产包已加密，请提供口令" })
      .mockResolvedValue(
        JSON.stringify({ protocol: 1, origin: "other", exportedAt: Date.now(), groups: [], assets: [], creds: [] }),
      );
    mocks.promptText.mockResolvedValue("correct-horse");
    await mountCard();
    openImport();
    clickButton(mounted!.container, "选择资产包文件…");
    await flushUntil(() => text().includes("确认导入 0 条资产"));

    expect(mocks.promptText).toHaveBeenCalledOnce();
    expect(mocks.openWailsFile).toHaveBeenCalledOnce();
    expect(mocks.readBundleFile).toHaveBeenCalledTimes(2);
    expect(mocks.readBundleFile).toHaveBeenLastCalledWith("/home/user/backup.nxbm", "correct-horse");
    expect(text()).toContain("backup.nxbm");
  });

  it("shows the decryption failure reason for an incorrect passphrase", async () => {
    mocks.openWailsFile.mockResolvedValue("/home/user/backup.nxbm");
    mocks.readBundleFile
      .mockRejectedValueOnce({ code: "bad_param", message: "资产包已加密，请提供口令" })
      .mockRejectedValue({ code: "decrypt", message: "资产包解密失败：口令错误或数据已被篡改" });
    mocks.promptText.mockResolvedValue("wrong-password");
    await mountCard();
    openImport();
    clickButton(mounted!.container, "选择资产包文件…");
    await flushUntil(() => text().includes("口令错误或数据已被篡改"));
    expect(mocks.importBundle).not.toHaveBeenCalled();
  });

  it("returns quietly when passphrase entry is canceled without reporting an error or importing", async () => {
    mocks.openWailsFile.mockResolvedValue("/home/user/backup.nxbm");
    mocks.readBundleFile.mockRejectedValue({ code: "bad_param", message: "资产包已加密，请提供口令" });
    mocks.promptText.mockResolvedValue(null);
    await mountCard();
    openImport();
    clickButton(mounted!.container, "选择资产包文件…");
    await flush();
    expect(text()).not.toContain("口令错误");
    expect(mocks.readBundleFile).toHaveBeenCalledTimes(1);
    expect(mocks.importBundle).not.toHaveBeenCalled();
  });

  it("mentions only .nxbm in the desktop import hint", async () => {
    await mountCard();
    openImport();
    await flushUntil(() => text().includes("选择资产包文件"));
    expect(text()).toContain("仅支持 .nxbm 资产包");
    expect(text()).not.toContain(".json");
  });
});

describe("SyncBundleCard passphrase row responsive layout", () => {
  it("allows the passphrase row to wrap and its input to shrink", async () => {
    await mountCard();
    const row = mounted!.container.querySelector<HTMLInputElement>("#bundle-password")!.closest("div")!;
    expect(row.className).toContain("flex-wrap");
    expect(mounted!.container.querySelector<HTMLInputElement>("#bundle-password")!.className).toContain("min-w-0");
    const label = mounted!.container.querySelector('label[for="bundle-password"]')!;
    expect(label.className).toContain("shrink-0");
  });
});
