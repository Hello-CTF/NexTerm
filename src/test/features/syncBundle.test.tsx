/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, clickButton, flush, flushUntil, mount, type MountedView } from "./reactTestUtils";

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
      readBundleFile: mocks.readBundleFile,
      writeBundleFile: mocks.writeBundleFile,
    },
  };
});
vi.mock("../../ipc/bundleFiles", () => ({
  pickBundleBuffer: mocks.pickBundleBuffer,
  saveBundleBytes: mocks.saveBundleBytes,
}));
vi.mock("../../ipc/wails", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/wails")>();
  return {
    ...actual,
    openWailsFile: mocks.openWailsFile,
    saveWailsFile: mocks.saveWailsFile,
  };
});

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
  groups: [{ id: "g9", parentId: null, name: "bundle group", sort: 0, createdAt: 1, updatedAt: 2 }],
  assets: [
    { id: "a1", groupId: null, kind: "ssh", name: "web-01 (old)", host: "10.0.0.1", port: 22, username: "deploy", authKind: "password", keyPath: null, credId: "c1", optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 50, deletedAt: null },
    { id: "a9", groupId: "g9", kind: "ssh", name: "new-01", host: "10.0.0.9", port: 22, username: "root", authKind: "password", keyPath: null, credId: null, optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 60, deletedAt: null },
  ],
  creds: [{ id: "c1", name: "production password", kind: "password", secret: "sup3r-s3cret" }],
};

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.digest.mockResolvedValue(DIGEST);
  mocks.saveWailsFile.mockResolvedValue("/home/user/nexterm-assets.nxbm");
  mocks.writeBundleFile.mockResolvedValue({ encrypted: false });
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
  clickButton(mounted!.container, "导出资产包");
  await flushUntil(() => text().includes("web-01"));
}

function openImport() {
  clickButton(mounted!.container, "导入资产包");
}

function exportableRow(name: string): HTMLElement {
  const row = [...mounted!.container.querySelectorAll("label")].find((l) =>
    l.textContent?.includes(name),
  );
  if (!row) throw new Error(`row ${name} not found`);
  return row;
}

function pickDesktopBundle(fileName: string, payload: string) {
  mocks.openWailsFile.mockResolvedValue(`/home/user/${fileName}`);
  mocks.readBundleFile.mockResolvedValue(payload);
}

describe("SyncBundleCard export", () => {
  it("excludes credentials by default, calls exportAssets(ids, false), and omits every secret from the summary", async () => {
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
    clickButton(mounted!.container, "导出资产包（.nxbm）");
    await flushUntil(() => text().includes("已导出"));
    expect(mocks.exportAssets).toHaveBeenCalledWith(["a1"], false);
    expect(mocks.writeBundleFile).toHaveBeenCalledOnce();
    const [, savedText, password] = mocks.writeBundleFile.mock.calls[0] as [string, string, string];
    expect(savedText).not.toContain("sup3r-s3cret");
    expect(password).toBe("");
    expect(text()).toContain("凭据 0");
  });

  it("shows a security warning after credentials are selected and exports with withCreds=true", async () => {
    mocks.exportAssets.mockResolvedValue({
      protocol: 1,
      origin: "local",
      exportedAt: Date.now(),
      groups: [],
      assets: [{ id: "a1" }],
      creds: [{ id: "c1", name: "production password", kind: "password", secret: "sup3r-s3cret" }],
      warnings: [],
    });
    await mountCard();
    expect(text()).toContain("导出凭据（密码、内联私钥和 API Key 会随资产包写入）");
    expect(text()).toContain("路径引用仅保存路径，目标环境必须能访问同一路径");
    const credsToggle = [...mounted!.container.querySelectorAll("label")].find((l) =>
      l.textContent?.includes("导出凭据"),
    )!;
    click(credsToggle.querySelector('input[type="checkbox"]')!);
    await flushUntil(() => text().includes("资产包将包含敏感凭据信息"));
    expect(text()).toContain("任何拿到文件的人都能读取密码、内联私钥和 API Key");
    expect(text()).not.toContain("私钥文件也会内联到资产包中");
    click(exportableRow("web-01").querySelector('input[type="checkbox"]')!);
    clickButton(mounted!.container, "导出资产包（.nxbm）");
    await flushUntil(() => text().includes("已导出"));
    expect(mocks.exportAssets).toHaveBeenCalledWith(["a1"], true);
    expect(text()).toContain("凭据 1");
    expect(text()).not.toContain("sup3r-s3cret");
  });

  it("shows export failures inline", async () => {
    mocks.exportAssets.mockRejectedValue(new Error("vault is locked"));
    await mountCard();
    click(exportableRow("web-01").querySelector('input[type="checkbox"]')!);
    clickButton(mounted!.container, "导出资产包（.nxbm）");
    await flushUntil(() => text().includes("vault is locked"));
    expect(text()).not.toContain("已导出（.nxbm 容器）");
  });
});

describe("SyncBundleCard import", () => {
  it("shows an error for invalid JSON without calling sync_import", async () => {
    pickDesktopBundle("broken.nxbm", "not-json{");
    await mountCard();
    openImport();
    clickButton(mounted!.container, "选择资产包文件…");
    await flushUntil(() => text().includes("不是合法的 JSON"));
    expect(mocks.importBundle).not.toHaveBeenCalled();
  });

  it("shows an error for an unsupported protocol version without calling sync_import", async () => {
    pickDesktopBundle("future.nxbm", JSON.stringify({ protocol: 99, groups: [], assets: [], creds: [] }));
    await mountCard();
    openImport();
    clickButton(mounted!.container, "选择资产包文件…");
    await flushUntil(() => text().includes("不支持的资产包协议版本"));
    expect(mocks.importBundle).not.toHaveBeenCalled();
  });

  it("shows an error for a malformed structure where assets is not an array without calling sync_import", async () => {
    pickDesktopBundle("bad-shape.nxbm", JSON.stringify({ protocol: 1, assets: "nope" }));
    await mountCard();
    openImport();
    clickButton(mounted!.container, "选择资产包文件…");
    await flushUntil(() => text().includes("assets 必须是数组"));
    expect(mocks.importBundle).not.toHaveBeenCalled();
  });

  it("previews origin, counts, and credential safety for a valid bundle without rendering secrets, then shows ImportReport after confirmation", async () => {
    pickDesktopBundle("bundle.nxbm", JSON.stringify(BUNDLE));
    mocks.importBundle.mockResolvedValue({
      groupsCreated: 1,
      groupsUpdated: 0,
      assetsCreated: 1,
      assetsUpdated: 1,
      credsCreated: 0,
      credsUpdated: 1,
      credsDeleted: 0,
      snippetsCreated: 0,
      snippetsUpdated: 0,
      skippedNewer: 0,
      refused: 0,
      warnings: ["asset a9 references missing group g9; the reference was cleared"],
    });
    await mountCard();
    openImport();
    clickButton(mounted!.container, "选择资产包文件…");
    await flushUntil(() => text().includes("确认导入"));
    expect(text()).toContain("other-device");
    expect(text()).toContain("资产 2");
    expect(text()).toContain("凭据 1");
    expect(text()).toContain("production password");
    expect(text()).toContain("1 条本机已存在");
    expect(text()).not.toContain("sup3r-s3cret");

    clickButton(mounted!.container, "确认导入 2 条资产");
    await flushUntil(() => text().includes("导入结果"));
    expect(mocks.importBundle).toHaveBeenCalledWith(BUNDLE, false);
    expect(text()).toContain("资产：新建 1 / 更新 1");
    expect(text()).toContain("凭据：新建 0 / 更新 1");
    expect(text()).toContain("reference was cleared");
  });

  it("clears dangling credential references only when the local credential with the same ID is also missing", async () => {
    const dangling = {
      ...BUNDLE,
      assets: BUNDLE.assets.map((asset) => (asset.id === "a1" ? { ...asset, credId: "c-missing" } : asset)),
    };
    pickDesktopBundle("dangling.nxbm", JSON.stringify(dangling));
    await mountCard();
    openImport();
    clickButton(mounted!.container, "选择资产包文件…");
    await flushUntil(() => text().includes("确认导入"));

    expect(text()).toContain("1 条资产引用的凭据不在包内");
    expect(text()).toContain("若本机也没有同 ID 凭据，导入后引用将被清除");
    expect(text()).not.toContain("导入后这些引用会被清除");
  });

  it("passes force=true when newer local entries must be overwritten", async () => {
    pickDesktopBundle("bundle.nxbm", JSON.stringify(BUNDLE));
    mocks.importBundle.mockResolvedValue({
      groupsCreated: 0,
      groupsUpdated: 0,
      assetsCreated: 0,
      assetsUpdated: 2,
      credsCreated: 0,
      credsUpdated: 1,
      credsDeleted: 0,
      snippetsCreated: 0,
      snippetsUpdated: 0,
      skippedNewer: 0,
      refused: 0,
      warnings: [],
    });
    await mountCard();
    openImport();
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

  it("treats the import as a successful write when only a group changes and every asset is skipped", async () => {
    pickDesktopBundle(
      "groups-only.nxbm",
      JSON.stringify({
        protocol: 1,
        origin: "other-device",
        exportedAt: Date.now(),
        groups: [{ id: "g1", parentId: null, name: "groups only", sort: 0, createdAt: 1, updatedAt: 2 }],
        assets: [{ ...BUNDLE.assets[0], updatedAt: 50 }],
        creds: [],
      }),
    );
    mocks.importBundle.mockResolvedValue({
      groupsCreated: 0,
      groupsUpdated: 1,
      assetsCreated: 0,
      assetsUpdated: 0,
      credsCreated: 0,
      credsUpdated: 0,
      credsDeleted: 0,
      snippetsCreated: 0,
      snippetsUpdated: 0,
      skippedNewer: 1,
      refused: 0,
      warnings: ["asset a1 is newer locally and was skipped; use force sync to overwrite"],
    });
    await mountCard();
    openImport();
    clickButton(mounted!.container, "选择资产包文件…");
    await flushUntil(() => text().includes("确认导入 1 条资产"));
    clickButton(mounted!.container, "确认导入 1 条资产");
    await flushUntil(() => text().includes("导入结果"));
    const successToast = mocks.toast.mock.calls.find(
      (c) => c[0] === "success" && String(c[1]).includes("导入完成"),
    );
    expect(successToast).toBeTruthy();
    expect(String(successToast?.[1])).toContain("更新 1");
    const reportBox = [...mounted!.container.querySelectorAll(".nx-alert")].find((el) =>
      el.textContent?.includes("导入结果"),
    );
    expect(reportBox?.className).not.toContain("nx-alert-danger");
  });

  it("warns in the preview that a bundle with deletion markers will mark local assets deleted", async () => {
    const withTombstone = {
      ...BUNDLE,
      assets: BUNDLE.assets.map((a) => (a.id === "a1" ? { ...a, deletedAt: 123 } : a)),
    };
    pickDesktopBundle("bundle.nxbm", JSON.stringify(withTombstone));
    await mountCard();
    openImport();
    clickButton(mounted!.container, "选择资产包文件…");
    await flushUntil(() => text().includes("删除标记"));
    expect(text()).toContain("本机对应资产会被一并标记删除");
  });
});

describe("SyncBundleCard section references", () => {
  it("refers to the account sync section above rather than calling it asset sync", async () => {
    await mountCard();
    expect(text()).toContain("与上方「账号同步」的推送和拉取互不影响");
    expect(text()).not.toContain("「资产同步」");
  });

  it("shows only the description and export/import buttons by default, expanding each area on demand", async () => {
    mounted = withClient(createElement(SyncBundleCard));
    await flushUntil(() => text().includes("与上方「账号同步」的推送和拉取互不影响"));
    expect(mounted!.container.querySelector("#bundle-password")).toBeNull();
    expect(
      [...mounted!.container.querySelectorAll("button")].some(
        (b) => b.textContent?.trim() === "选择资产包文件…",
      ),
    ).toBe(false);

    clickButton(mounted!.container, "导出资产包");
    await flushUntil(() => text().includes("web-01"));
    expect(mounted!.container.querySelector("#bundle-password")).not.toBeNull();

    clickButton(mounted!.container, "导入资产包");
    await flush();
    expect(
      [...mounted!.container.querySelectorAll("button")].some(
        (b) => b.textContent?.trim() === "选择资产包文件…",
      ),
    ).toBe(true);
  });
});
