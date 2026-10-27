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
  hostDeepLink,
  openDeepLink,
  parseDeepLinkAssetId,
  parseHostDeepLinkId,
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
  it("parses nexterm://connect/<id>", () => {
    expect(parseDeepLinkAssetId("nexterm://connect/a-1")).toBe("a-1");
    expect(parseDeepLinkAssetId("nexterm://connect/a-1/")).toBe("a-1");
    expect(parseDeepLinkAssetId("nexterm://connect/a%20b")).toBe("a b");
    expect(parseDeepLinkAssetId("  nexterm://connect/a-1  ")).toBe("a-1");
  });

  it("rejects other shapes", () => {
    expect(parseDeepLinkAssetId("")).toBeNull();
    expect(parseDeepLinkAssetId("nexterm://connect/")).toBeNull();
    expect(parseDeepLinkAssetId("nexterm://open/a-1")).toBeNull();
    expect(parseDeepLinkAssetId("nexterm://connect/a-1/extra")).toBeNull();
    expect(parseDeepLinkAssetId("https://connect/a-1")).toBeNull();
    expect(parseDeepLinkAssetId("nexterm://connect/%ZZ")).toBeNull();
  });
});

describe("openDeepLink", () => {
  it("opens the corresponding workspace when the asset exists", async () => {
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

  it("shows an error and does not connect when the asset is missing", async () => {
    mocks.get.mockRejectedValue({ code: "not_found", message: "missing" });
    await openDeepLink("nexterm://connect/nope");
    expect(mocks.connectAsset).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith(
      "error",
      expect.stringContaining("打开链接对应的资产失败，请确认资产仍存在后重试"),
    );
  });

  it("shows an error and does not connect when the asset is deleted", async () => {
    mocks.get.mockResolvedValue(assetOf({ id: "a-1", name: "web-1", deletedAt: 5 }));
    await openDeepLink("nexterm://connect/a-1");
    expect(mocks.connectAsset).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith(
      "error",
      expect.stringContaining("打开链接对应的资产失败，请确认资产仍存在后重试"),
    );
  });

  it("shows an error for an unrecognized link", async () => {
    await openDeepLink("nexterm://wat");
    expect(mocks.get).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("无法识别"));
  });
});

describe("copyAssetDeepLink", () => {
  it("writes the nexterm:// link to the clipboard", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    expect(assetDeepLink("a-1")).toBe("nexterm://connect/a-1");
    copyAssetDeepLink("a-1");
    expect(writeText).toHaveBeenCalledWith("nexterm://connect/a-1");
    await vi.waitFor(() =>
      expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("已复制")),
    );
  });

  it("shows a retry error when copying fails", async () => {
    Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true });
    copyAssetDeepLink("a-1");
    await vi.waitFor(() =>
      expect(mocks.toast).toHaveBeenCalledWith("error", "复制失败，请重试"),
    );
  });
});

describe("host detail links", () => {
  function hostTabs() {
    return useUi.getState().workspaces.flatMap((w) => w.panes.flatMap((p) => p.tabs)).filter((t) => t.kind === "host");
  }

  it("round-trips asset and device ids", () => {
    expect(hostDeepLink("a-1")).toBe("nexterm://host/a-1");
    expect(parseHostDeepLinkId(hostDeepLink("a-1"))).toBe("a-1");
    expect(parseHostDeepLinkId(hostDeepLink("device/d-1"))).toBe("device/d-1");
    expect(parseHostDeepLinkId("nexterm://host/")).toBeNull();
    expect(parseHostDeepLinkId("nexterm://host/a/b")).toBeNull();
    expect(parseHostDeepLinkId("nexterm://connect/a-1")).toBeNull();
    expect(parseDeepLinkAssetId(hostDeepLink("a-1"))).toBeNull();
  });

  it("opens the detail page without connecting", async () => {
    useUi.setState({ workspaces: [], activeWorkspaceId: null });
    await openDeepLink(hostDeepLink("device/d-1"));
    expect(hostTabs().map((t) => t.assetId)).toEqual(["device/d-1"]);
    expect(mocks.connectAsset).not.toHaveBeenCalled();
    expect(mocks.get).not.toHaveBeenCalled();

    // A second link retargets the one detail tab.
    await openDeepLink(hostDeepLink("a-2"));
    expect(hostTabs().map((t) => t.assetId)).toEqual(["a-2"]);
    expect(useUi.getState().workspaces.every((w) => w.kind === "tools")).toBe(true);
  });

  it("caps the host id length", () => {
    expect(parseHostDeepLinkId(hostDeepLink("a".repeat(256)))).toBe("a".repeat(256));
    expect(parseHostDeepLinkId(hostDeepLink("a".repeat(257)))).toBeNull();
  });

  it.each([["nexterm://host/"], ["nexterm://host/%ZZ"], [`nexterm://host/${"a".repeat(300)}`]])(
    "shows a toast and opens no page for %s",
    async (link) => {
      useUi.setState({ workspaces: [], activeWorkspaceId: null });
      await openDeepLink(link);
      expect(hostTabs()).toEqual([]);
      expect(mocks.toast).toHaveBeenCalledWith("error", "无效的主机链接");
      expect(mocks.get).not.toHaveBeenCalled();
    },
  );
});
