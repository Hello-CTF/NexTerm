/** @vitest-environment jsdom */
// WEB preview links: PreviewShareMenu dispatch keeps the previewApi direct connection - creation via
// createPreviewLink (sessionId is the tabId, default write:false read-only, checking "allow control"
// sends write:true, the checkbox resets after success), the URL is assembled with previewPublicUrl, the list filters
// directly by tabId and shows read-only/controllable badges by permission, revoke goes through revokePreviewLink;
// the desktop relay entry is not touched.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, clickButton, flushUntil, mountWithClient, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  createPreviewLink: vi.fn(),
  listPreviewLinks: vi.fn(),
  revokePreviewLink: vi.fn(),
  remoteCreate: vi.fn(),
  remoteActive: vi.fn(),
}));

vi.mock("../../features/sharing/previewApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../features/sharing/previewApi")>();
  return {
    ...actual,
    createPreviewLink: mocks.createPreviewLink,
    listPreviewLinks: mocks.listPreviewLinks,
    revokePreviewLink: mocks.revokePreviewLink,
  };
});
vi.mock("../../ipc/remoteApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/remoteApi")>();
  return {
    ...actual,
    remotePreviewApi: { ...actual.remotePreviewApi, create: mocks.remoteCreate, active: mocks.remoteActive },
  };
});

import { PreviewShareMenu } from "../../features/sharing/PreviewShareMenu";

const NOW = Date.now();

let mounted: MountedView | undefined;

beforeEach(() => {
  mocks.createPreviewLink.mockResolvedValue({
    id: "pl-1",
    owner_id: "u-1",
    session_id: "tab-1",
    created_at: NOW,
    expires_at: NOW + 3_600_000,
    token: "tok-web",
  });
  mocks.listPreviewLinks.mockResolvedValue({ links: [] });
  mocks.revokePreviewLink.mockResolvedValue({ ok: true });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.clearAllMocks();
});

describe("PreviewShareMenu WEB dispatch", () => {
  it("creates directly through previewApi and builds the public URL with previewPublicUrl, defaulting write to false", async () => {
    mounted = mountWithClient(createElement(PreviewShareMenu, { tabId: "tab-1", sessionName: "web-01" }));
    const container = mounted.container;
    clickButton(container, "预览链接");
    await flushUntil(() => container.querySelector("select") !== null);
    clickButton(container, "创建链接");
    await flushUntil(
      () => container.textContent?.includes(`${window.location.origin}/share/preview/tok-web`) === true,
    );
    expect(mocks.createPreviewLink).toHaveBeenCalledWith({ sessionId: "tab-1", ttlMs: 3_600_000, write: false });
    expect(mocks.remoteCreate).not.toHaveBeenCalled();
  });

  it("sends write:true after checking allow control and resets the checkbox after success", async () => {
    mounted = mountWithClient(createElement(PreviewShareMenu, { tabId: "tab-1", sessionName: "web-01" }));
    const container = mounted.container;
    clickButton(container, "预览链接");
    await flushUntil(() => container.querySelector("select") !== null);
    const checkbox = [...container.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')].find((candidate) =>
      candidate.closest("label")?.textContent?.includes("允许控制"),
    );
    expect(checkbox).toBeDefined();
    expect(checkbox!.disabled).toBe(false);
    expect(checkbox!.checked).toBe(false);
    click(checkbox!);
    expect(checkbox!.checked).toBe(true);
    clickButton(container, "创建链接");
    await flushUntil(
      () => container.textContent?.includes(`${window.location.origin}/share/preview/tok-web`) === true,
    );
    expect(mocks.createPreviewLink).toHaveBeenCalledWith({ sessionId: "tab-1", ttlMs: 3_600_000, write: true });
    expect(checkbox!.checked).toBe(false);
  });

  it("shows read-only or controllable badges for active links by permission, defaulting missing or unknown values to read-only", async () => {
    mocks.listPreviewLinks.mockResolvedValue({
      links: [
        {
          id: "l-1",
          owner_id: "u-1",
          session_id: "tab-1",
          created_at: NOW - 60_000,
          expires_at: NOW + 3_600_000,
          permission: "read_write",
        },
        { id: "l-2", owner_id: "u-1", session_id: "tab-1", created_at: NOW - 60_000, expires_at: NOW + 3_600_000 },
        {
          id: "l-3",
          owner_id: "u-1",
          session_id: "tab-1",
          created_at: NOW - 60_000,
          expires_at: NOW + 3_600_000,
          permission: "owner",
        },
      ],
    });
    mounted = mountWithClient(createElement(PreviewShareMenu, { tabId: "tab-1", sessionName: "web-01" }));
    const container = mounted.container;
    clickButton(container, "预览链接");
    await flushUntil(() => container.textContent?.includes("进行中的预览") === true);
    const badges = [...container.querySelectorAll("span")].map((span) => span.textContent?.trim());
    expect(badges.filter((text) => text === "可控制")).toHaveLength(1);
    expect(badges.filter((text) => text === "只读")).toHaveLength(2);
  });

  it("filters the list directly by tabId without querying desktop relay state", async () => {
    mocks.listPreviewLinks.mockResolvedValue({
      links: [
        { id: "l-1", owner_id: "u-1", session_id: "tab-1", created_at: NOW - 60_000, expires_at: NOW + 3_600_000 },
        { id: "l-2", owner_id: "u-1", session_id: "tab-2", created_at: NOW - 60_000, expires_at: NOW + 3_600_000 },
      ],
    });
    mounted = mountWithClient(createElement(PreviewShareMenu, { tabId: "tab-1", sessionName: "web-01" }));
    const container = mounted.container;
    clickButton(container, "预览链接");
    await flushUntil(() => container.textContent?.includes("进行中的预览") === true);
    const revokes = [...container.querySelectorAll("button")].filter((b) => b.textContent?.trim() === "吊销");
    expect(revokes).toHaveLength(1);
    expect(mocks.remoteActive).not.toHaveBeenCalled();

    clickButton(container, "吊销");
    await flushUntil(() => mocks.revokePreviewLink.mock.calls.length === 1);
    expect(mocks.revokePreviewLink).toHaveBeenCalledWith("l-1");
  });
});
