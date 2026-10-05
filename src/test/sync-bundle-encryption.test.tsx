/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, clickButton, flushUntil, mount, setInputValue, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    digest: vi.fn(),
    exportAssets: vi.fn(),
    importBundle: vi.fn(),
    readBundleFile: vi.fn(),
    writeBundleFile: vi.fn(),
    pickBundleFile: vi.fn(),
    saveBundleFile: vi.fn(),
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
  pickBundleFile: mocks.pickBundleFile,
  saveBundleFile: mocks.saveBundleFile,
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
  mocks.saveBundleFile.mockResolvedValue(true);
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
  await flushUntil(() => text().includes("web-01"));
}

function exportRow(name: string): HTMLElement {
  const row = [...mounted!.container.querySelectorAll("label")].find((l) => l.textContent?.includes(name));
  if (!row) throw new Error(`row ${name} not found`);
  return row;
}

function exportButton(): HTMLButtonElement {
  const btn = [...mounted!.container.querySelectorAll("button")].find(
    (b) => b.textContent?.trim() === "导出为 JSON 文件",
  ) as HTMLButtonElement | undefined;
  if (!btn) throw new Error("export button not found");
  return btn;
}

function checkEncrypt(): void {
  const toggle = [...mounted!.container.querySelectorAll("label")].find((l) =>
    l.textContent?.includes("加密资产包"),
  )!;
  click(toggle.querySelector('input[type="checkbox"]')!);
}

describe("SyncBundleCard 明文导出风险提示", () => {
  it("未加密时常驻明文风险警告；勾选加密后警告消失并出现口令输入", async () => {
    await mountCard();
    expect(text()).toContain("将以明文导出");
    expect(text()).toContain("未设置导出口令");
    expect(mounted!.container.querySelector("#bundle-password")).toBeNull();

    checkEncrypt();
    await flushUntil(() => mounted!.container.querySelector("#bundle-password") !== null);
    expect(text()).not.toContain("将以明文导出");
    expect(text()).toContain("口令无法找回");
    expect(mounted!.container.querySelector("#bundle-password2")).not.toBeNull();
  });

  it("口令不一致时禁用导出并提示；一致后放行", async () => {
    await mountCard();
    checkEncrypt();
    await flushUntil(() => mounted!.container.querySelector("#bundle-password") !== null);
    click(exportRow("web-01").querySelector('input[type="checkbox"]')!);
    setInputValue(mounted!.container.querySelector<HTMLInputElement>("#bundle-password")!, "correct-horse");
    setInputValue(mounted!.container.querySelector<HTMLInputElement>("#bundle-password2")!, "battery-staple");
    expect(text()).toContain("两次输入的导出口令不一致");
    expect(exportButton().disabled).toBe(true);

    setInputValue(mounted!.container.querySelector<HTMLInputElement>("#bundle-password2")!, "correct-horse");
    await flushUntil(() => !exportButton().disabled);
  });
});

describe("SyncBundleCard 加密导出", () => {
  it("加密导出走 .nxbm 容器写入并携带口令，摘要标注已加密", async () => {
    await mountCard();
    checkEncrypt();
    await flushUntil(() => mounted!.container.querySelector("#bundle-password") !== null);
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
    expect(mocks.saveBundleFile).not.toHaveBeenCalled();
    expect(text()).toContain("已加密导出");
    expect(mounted!.container.querySelector<HTMLInputElement>("#bundle-password")!.value).toBe("");
  });

  it("明文导出不经过加密写入路径", async () => {
    await mountCard();
    click(exportRow("web-01").querySelector('input[type="checkbox"]')!);
    click(exportButton());
    await flushUntil(() => text().includes("导出完成"));
    expect(mocks.saveBundleFile).toHaveBeenCalledOnce();
    expect(String(mocks.saveBundleFile.mock.calls[0]?.[0])).toContain(".json");
    expect(mocks.writeBundleFile).not.toHaveBeenCalled();
    expect(mocks.saveWailsFile).not.toHaveBeenCalled();
    expect(text()).toContain("导出完成");
  });
});

describe("SyncBundleCard 加密导入", () => {
  it("桌面端读到加密包时提示输入口令并用口令重读；旧版 JSON 导入不受影响", async () => {
    mocks.pickBundleFile.mockRejectedValue({ code: "bad_param", message: "资产包已加密，请提供口令" });
    mocks.promptText.mockResolvedValue("correct-horse");
    mocks.openWailsFile.mockResolvedValue("/home/user/backup.nxbm");
    mocks.readBundleFile.mockResolvedValue(
      JSON.stringify({ protocol: 1, origin: "other", exportedAt: Date.now(), groups: [], assets: [], creds: [] }),
    );
    await mountCard();
    clickButton(mounted!.container, "选择资产包文件…");
    await flushUntil(() => text().includes("确认导入 0 条资产"));

    expect(mocks.promptText).toHaveBeenCalledOnce();
    expect(mocks.openWailsFile).toHaveBeenCalledOnce();
    expect(mocks.readBundleFile).toHaveBeenCalledWith("/home/user/backup.nxbm", "correct-horse");
    expect(text()).toContain("backup.nxbm");
  });

  it("口令错误时展示解密失败原因", async () => {
    mocks.pickBundleFile.mockRejectedValue({ code: "bad_param", message: "资产包已加密，请提供口令" });
    mocks.promptText.mockResolvedValue("wrong-password");
    mocks.openWailsFile.mockResolvedValue("/home/user/backup.nxbm");
    mocks.readBundleFile.mockRejectedValue({ code: "decrypt", message: "资产包解密失败：口令错误或数据已被篡改" });
    await mountCard();
    clickButton(mounted!.container, "选择资产包文件…");
    await flushUntil(() => text().includes("口令错误或数据已被篡改"));
    expect(mocks.importBundle).not.toHaveBeenCalled();
  });

  it("取消口令输入时安静返回，不报错也不导入", async () => {
    mocks.pickBundleFile.mockRejectedValue({ code: "bad_param", message: "资产包已加密，请提供口令" });
    mocks.promptText.mockResolvedValue(null);
    await mountCard();
    clickButton(mounted!.container, "选择资产包文件…");
    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(text()).not.toContain("口令错误");
    expect(mocks.openWailsFile).not.toHaveBeenCalled();
    expect(mocks.importBundle).not.toHaveBeenCalled();
  });

  it("桌面端导入提示同时声明支持 .json 与 .nxbm", async () => {
    await mountCard();
    expect(text()).toContain("支持旧版 .json 资产包与 .nxbm 加密资产包");
  });
});

describe("SyncBundleCard 口令行窄屏结构", () => {
  it("口令行可换行、输入框可收缩", async () => {
    await mountCard();
    checkEncrypt();
    await flushUntil(() => mounted!.container.querySelector("#bundle-password") !== null);
    const row = mounted!.container.querySelector<HTMLInputElement>("#bundle-password")!.closest("div")!;
    expect(row.className).toContain("flex-wrap");
    expect(mounted!.container.querySelector<HTMLInputElement>("#bundle-password")!.className).toContain("min-w-0");
    const label = mounted!.container.querySelector('label[for="bundle-password"]')!;
    expect(label.className).toContain("shrink-0");
  });
});
