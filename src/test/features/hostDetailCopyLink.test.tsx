/** @vitest-environment jsdom */
// The host detail page offers "复制链接" only where the nexterm:// scheme is registered: desktop, not mobile.
import { afterEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { flushUntil, mountWithClient, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return { assetList: vi.fn(), mobile: false };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return { ...actual, assetApi: { ...actual.assetApi, list: mocks.assetList } };
});

vi.mock("../../app/platform", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../app/platform")>();
  return { ...actual, isMobile: () => mocks.mobile };
});

import { HostDetailView } from "../../features/hosts/HostDetailView";
import type { Asset } from "../../ipc/commands";

const WEB_HOST: Asset = {
  id: "a-web",
  name: "web-01",
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
};

let mounted: MountedView | undefined;

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

async function copyLinkShown(mobile: boolean): Promise<boolean> {
  mocks.mobile = mobile;
  mocks.assetList.mockResolvedValue([WEB_HOST]);
  mounted = mountWithClient(createElement(HostDetailView, { assetId: "a-web", visible: true }));
  await flushUntil(() => (mounted?.container.textContent ?? "").includes("web-01"));
  return [...mounted.container.querySelectorAll("button")].some((button) => button.textContent === "复制链接");
}

describe("host detail copy-link action", () => {
  it("is shown on desktop", async () => {
    expect(await copyLinkShown(false)).toBe(true);
  });

  it("is hidden on mobile", async () => {
    expect(await copyLinkShown(true)).toBe(false);
  });
});
