/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  get: vi.fn(),
  connectAsset: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: { ...actual.assetApi, get: mocks.get },
  };
});

vi.mock("../../app/store", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../app/store")>();
  return { ...actual, connectAsset: mocks.connectAsset };
});

import {
  assetDeepLink,
  copyAssetDeepLink,
  openDeepLink,
  parseDeepLinkAssetId,
} from "../../app/deepLink";
import { useUi } from "../../app/store";
import type { Asset } from "../../ipc/commands";

function assetOf(extra: Partial<Asset> & { id: string; name: string }): Asset {
  return {
    groupId: null,
    kind: "ssh",
    host: "10.0.0.8",
    port: 22,
    username: "root",
    authKind: "password",
    keyPath: null,
    credId: null,
    options: {},
    tags: "",
    note: "",
    sort: 0,
    createdAt: 1,
    updatedAt: 1,
    deletedAt: null,
    builtin: false,
    ...extra,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  useUi.setState({ pushToast: mocks.toast });
});

describe("parseDeepLinkAssetId", () => {
  it("解析 nexterm://connect/<id>", () => {
    expect(parseDeepLinkAssetId("nexterm://connect/a-1")).toBe("a-1");
    expect(parseDeepLinkAssetId("nexterm://connect/a-1/")).toBe("a-1");
    expect(parseDeepLinkAssetId("nexterm://connect/a%20b")).toBe("a b");
    expect(parseDeepLinkAssetId("  nexterm://connect/a-1  ")).toBe("a-1");
  });

  it("拒绝其他形状", () => {
    expect(parseDeepLinkAssetId("")).toBeNull();
    expect(parseDeepLinkAssetId("nexterm://connect/")).toBeNull();
    expect(parseDeepLinkAssetId("nexterm://open/a-1")).toBeNull();
    expect(parseDeepLinkAssetId("nexterm://connect/a-1/extra")).toBeNull();
    expect(parseDeepLinkAssetId("https://connect/a-1")).toBeNull();
    expect(parseDeepLinkAssetId("nexterm://connect/%ZZ")).toBeNull();
  });
});

describe("openDeepLink", () => {
  it("资产存在时打开对应工作区", async () => {
    mocks.get.mockResolvedValue(assetOf({ id: "a-1", name: "web-1", credId: "cred-1" }));
    await openDeepLink("nexterm://connect/a-1");
    expect(mocks.get).toHaveBeenCalledWith("a-1");
    expect(mocks.connectAsset).toHaveBeenCalledWith({
      id: "a-1",
      name: "web-1",
      kind: "ssh",
      credId: "cred-1",
    });
  });

  it("资产不存在时提示且不连接", async () => {
    mocks.get.mockRejectedValue({ code: "not_found", message: "missing" });
    await openDeepLink("nexterm://connect/nope");
    expect(mocks.connectAsset).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("不存在"));
  });

  it("资产已删除时提示且不连接", async () => {
    mocks.get.mockResolvedValue(assetOf({ id: "a-1", name: "web-1", deletedAt: 5 }));
    await openDeepLink("nexterm://connect/a-1");
    expect(mocks.connectAsset).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("不存在"));
  });

  it("链接无法识别时提示", async () => {
    await openDeepLink("nexterm://wat");
    expect(mocks.get).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("无法识别"));
  });
});

describe("copyAssetDeepLink", () => {
  it("写入 nexterm:// 链接到剪贴板", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    expect(assetDeepLink("a-1")).toBe("nexterm://connect/a-1");
    copyAssetDeepLink("a-1");
    expect(writeText).toHaveBeenCalledWith("nexterm://connect/a-1");
    await vi.waitFor(() =>
      expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("已复制")),
    );
  });

  it("剪贴板不可用时提示失败", () => {
    Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true });
    copyAssetDeepLink("a-1");
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("剪贴板不可用"));
  });
});
