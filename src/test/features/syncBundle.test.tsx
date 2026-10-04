/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, clickButton, flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    digest: vi.fn(),
    exportAssets: vi.fn(),
    importBundle: vi.fn(),
    pickBundleFile: vi.fn(),
    saveBundleFile: vi.fn(),
    toast: vi.fn(),
  };
});
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    syncApi: {
      digest: mocks.digest,
      exportAssets: mocks.exportAssets,
      importBundle: mocks.importBundle,
      linkGet: vi.fn(),
      linkSet: vi.fn(),
      remoteDigest: vi.fn(),
      push: vi.fn(),
      pull: vi.fn(),
      token: vi.fn(),
      rotateToken: vi.fn(),
      readBundleFile: vi.fn(),
      writeBundleFile: vi.fn(),
    },
  };
});
vi.mock("../../ipc/bundleFiles", () => ({
  pickBundleFile: mocks.pickBundleFile,
  saveBundleFile: mocks.saveBundleFile,
}));

import { SyncBundleCard } from "../../features/settings/SyncBundleCard";
import { useUi } from "../../app/store";

const DIGEST = {
  origin: "local",
  protocol: 1,
  appVersion: "test",
  desktop: true,
  assets: [
    { id: "a1", name: "web-01", kind: "ssh", host: "10.0.0.1", username: "deploy", updatedAt: 100, deletedAt: null, hasCred: true, groupId: null },
    { id: "a2", name: "db-01", kind: "mysql", host: "10.0.0.2", username: null, updatedAt: 90, deletedAt: null, hasCred: false, groupId: null },
  ],
};

const BUNDLE = {
  protocol: 1,
  origin: "other-device",
  exportedAt: 1_700_000_000_000,
  groups: [{ id: "g9", parentId: null, name: "包分组", sort: 0, createdAt: 1, updatedAt: 2 }],
  assets: [
    { id: "a1", groupId: null, kind: "ssh", name: "web-01（旧）", host: "10.0.0.1", port: 22, username: "deploy", authKind: "password", keyPath: null, credId: "c1", optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 50, deletedAt: null },
    { id: "a9", groupId: "g9", kind: "ssh", name: "new-01", host: "10.0.0.9", port: 22, username: "root", authKind: "password", keyPath: null, credId: null, optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 60, deletedAt: null },
  ],
  creds: [{ id: "c1", name: "生产口令", kind: "password", secret: "sup3r-s3cret" }],
};

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.digest.mockResolvedValue(DIGEST);
  mocks.saveBundleFile.mockResolvedValue(true);
  useUi.setState({ pushToast: mocks.toast, sessions: [], appDialog: null });
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

function exportableRow(name: string): HTMLElement {
  const row = [...mounted!.container.querySelectorAll("label")].find((l) =>
    l.textContent?.includes(name),
  );
  if (!row) throw new Error(`row ${name} not found`);
  return row;
}

describe("SyncBundleCard 导出", () => {
  it("默认不含凭据：导出调用 exportAssets(ids, false)，摘要不出现任何秘密", async () => {
    mocks.exportAssets.mockResolvedValue({
      protocol: 1,
      origin: "local",
      exportedAt: Date.now(),
      groups: [],
      assets: [{ id: "a1" }],
      creds: [],
      warnings: [],
    });
    await mountCard();
    click(exportableRow("web-01").querySelector('input[type="checkbox"]')!);
    clickButton(mounted!.container, "导出为 JSON 文件");
    await flushUntil(() => text().includes("导出完成"));
    expect(mocks.exportAssets).toHaveBeenCalledWith(["a1"], false);
    expect(mocks.saveBundleFile).toHaveBeenCalledOnce();
    const [, savedText] = mocks.saveBundleFile.mock.calls[0] as [string, string];
    expect(savedText).not.toContain("sup3r-s3cret");
    expect(text()).toContain("凭据 0");
  });

  it("勾选凭据后出现安全警告，导出以 withCreds=true 调用", async () => {
    mocks.exportAssets.mockResolvedValue({
      protocol: 1,
      origin: "local",
      exportedAt: Date.now(),
      groups: [],
      assets: [{ id: "a1" }],
      creds: [{ id: "c1", name: "生产口令", kind: "password", secret: "sup3r-s3cret" }],
      warnings: [],
    });
    await mountCard();
    const credsToggle = [...mounted!.container.querySelectorAll("label")].find((l) =>
      l.textContent?.includes("导出凭据"),
    )!;
    click(credsToggle.querySelector('input[type="checkbox"]')!);
    await flushUntil(() => text().includes("可还原的凭据明文"));
    click(exportableRow("web-01").querySelector('input[type="checkbox"]')!);
    clickButton(mounted!.container, "导出为 JSON 文件");
    await flushUntil(() => text().includes("导出完成"));
    expect(mocks.exportAssets).toHaveBeenCalledWith(["a1"], true);
    expect(text()).toContain("凭据 1");
    expect(text()).not.toContain("sup3r-s3cret");
  });

  it("导出失败时内联报错", async () => {
    mocks.exportAssets.mockRejectedValue(new Error("凭据库已锁定"));
    await mountCard();
    click(exportableRow("web-01").querySelector('input[type="checkbox"]')!);
    clickButton(mounted!.container, "导出为 JSON 文件");
    await flushUntil(() => text().includes("凭据库已锁定"));
    expect(text()).not.toContain("导出完成");
  });
});

describe("SyncBundleCard 导入", () => {
  it("非法 JSON：显示错误且不调用 sync_import", async () => {
    mocks.pickBundleFile.mockResolvedValue({ name: "broken.json", text: "not-json{" });
    await mountCard();
    clickButton(mounted!.container, "选择资产包文件…");
    await flushUntil(() => text().includes("不是合法的 JSON"));
    expect(mocks.importBundle).not.toHaveBeenCalled();
  });

  it("协议版本不符：显示错误且不调用 sync_import", async () => {
    mocks.pickBundleFile.mockResolvedValue({
      name: "future.json",
      text: JSON.stringify({ protocol: 99, groups: [], assets: [], creds: [] }),
    });
    await mountCard();
    clickButton(mounted!.container, "选择资产包文件…");
    await flushUntil(() => text().includes("不支持的资产包协议版本"));
    expect(mocks.importBundle).not.toHaveBeenCalled();
  });

  it("结构损坏（assets 不是数组）：显示错误且不调用 sync_import", async () => {
    mocks.pickBundleFile.mockResolvedValue({
      name: "bad-shape.json",
      text: JSON.stringify({ protocol: 1, assets: "nope" }),
    });
    await mountCard();
    clickButton(mounted!.container, "选择资产包文件…");
    await flushUntil(() => text().includes("assets 必须是数组"));
    expect(mocks.importBundle).not.toHaveBeenCalled();
  });

  it("合法包：预览来源/计数/凭据安全提示（不渲染秘密），确认后展示 ImportReport", async () => {
    mocks.pickBundleFile.mockResolvedValue({ name: "bundle.json", text: JSON.stringify(BUNDLE) });
    mocks.importBundle.mockResolvedValue({
      groupsCreated: 1,
      groupsUpdated: 0,
      assetsCreated: 1,
      assetsUpdated: 1,
      credsCreated: 0,
      credsUpdated: 1,
      skippedNewer: 0,
      refused: 0,
      warnings: ["资产 a9 引用的分组 g9 不存在，已清除该引用"],
    });
    await mountCard();
    clickButton(mounted!.container, "选择资产包文件…");
    await flushUntil(() => text().includes("确认导入"));
    expect(text()).toContain("other-device");
    expect(text()).toContain("资产 2");
    expect(text()).toContain("凭据 1");
    expect(text()).toContain("生产口令");
    expect(text()).toContain("1 条本机已存在");
    expect(text()).not.toContain("sup3r-s3cret");

    clickButton(mounted!.container, "确认导入 2 条资产");
    await flushUntil(() => text().includes("导入结果"));
    expect(mocks.importBundle).toHaveBeenCalledWith(BUNDLE, false);
    expect(text()).toContain("资产 新建 1 / 更新 1");
    expect(text()).toContain("凭据 新建 0 / 更新 1");
    expect(text()).toContain("已清除该引用");
  });

  it("强制覆盖较新条目：force 以 true 传递", async () => {
    mocks.pickBundleFile.mockResolvedValue({ name: "bundle.json", text: JSON.stringify(BUNDLE) });
    mocks.importBundle.mockResolvedValue({
      groupsCreated: 0,
      groupsUpdated: 0,
      assetsCreated: 0,
      assetsUpdated: 2,
      credsCreated: 0,
      credsUpdated: 1,
      skippedNewer: 0,
      refused: 0,
      warnings: [],
    });
    await mountCard();
    clickButton(mounted!.container, "选择资产包文件…");
    await flushUntil(() => text().includes("确认导入"));
    const forceToggle = [...mounted!.container.querySelectorAll("label")].find((l) =>
      l.textContent?.includes("强制覆盖较新的本机条目"),
    )!;
    click(forceToggle.querySelector('input[type="checkbox"]')!);
    clickButton(mounted!.container, "确认导入 2 条资产");
    await flushUntil(() => text().includes("导入结果"));
    expect(mocks.importBundle).toHaveBeenCalledWith(BUNDLE, true);
  });

  it("含删除标记的包：预览提示会标记删除本机资产", async () => {
    const withTombstone = {
      ...BUNDLE,
      assets: BUNDLE.assets.map((a) => (a.id === "a1" ? { ...a, deletedAt: 123 } : a)),
    };
    mocks.pickBundleFile.mockResolvedValue({ name: "bundle.json", text: JSON.stringify(withTombstone) });
    await mountCard();
    clickButton(mounted!.container, "选择资产包文件…");
    await flushUntil(() => text().includes("删除标记"));
    expect(text()).toContain("本机对应资产会被一并标记删除");
  });
});
