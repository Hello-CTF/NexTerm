/** @vitest-environment jsdom */
// Desktop preview links: PreviewShareMenu dispatches by TRANSPORT - creation goes through the preview_share_create
// relay (showing the backend-assembled url), list/revoke go through remoteSharingApi filtered by the shell id of the
// active relay of the current tab; the "allow control" checkbox is disabled on desktop (previews created by the desktop
// relay do not support control in the first phase), the creation signature stays (tabId, ttlMs); the login-gating hook
// of the TerminalPane entry returns false on logged-out desktop (hiding the entry).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { clickButton, flush, flushUntil, mountWithClient, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    status: vi.fn(),
    create: vi.fn(),
    active: vi.fn(),
    previewLinks: vi.fn(),
    revokePreviewLink: vi.fn(),
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return { ...actual, syncApi: { ...actual.syncApi, status: mocks.status } };
});
vi.mock("../../ipc/remoteApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/remoteApi")>();
  return {
    ...actual,
    remotePreviewApi: { ...actual.remotePreviewApi, create: mocks.create, active: mocks.active },
    remoteSharingApi: {
      ...actual.remoteSharingApi,
      previewLinks: mocks.previewLinks,
      revokePreviewLink: mocks.revokePreviewLink,
    },
  };
});

import { PreviewShareMenu } from "../../features/sharing/PreviewShareMenu";
import { usePreviewShareEnabled } from "../../features/terminal/TerminalPane";

const NOW = Date.now();

function linkOn(sessionId: string, id: string, overrides: Record<string, unknown> = {}) {
  return {
    id,
    owner_id: "u-1",
    session_id: sessionId,
    created_at: NOW - 60_000,
    expires_at: NOW + 3_600_000,
    ...overrides,
  };
}

let mounted: MountedView | undefined;

beforeEach(() => {
  mocks.status.mockResolvedValue({
    configured: true,
    loggedIn: true,
    username: "alice",
    userId: "u-1",
    head: "h-1",
    seq: 0,
    verifiedAt: 0,
    lastError: "",
  });
  mocks.create.mockResolvedValue({
    token: "tok-1",
    url: "https://sync.example.com/share/preview/tok-1",
    sessionId: "shell-1",
  });
  mocks.active.mockResolvedValue({ sessionId: "shell-1" });
  mocks.previewLinks.mockResolvedValue({ links: [] });
  mocks.revokePreviewLink.mockResolvedValue({ ok: true });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.clearAllMocks();
});

function mountMenu(): HTMLDivElement {
  mounted = mountWithClient(createElement(PreviewShareMenu, { tabId: "tab-1", sessionName: "web-01" }));
  clickButton(mounted.container, "预览链接");
  return mounted.container;
}

describe("PreviewShareMenu desktop dispatch", () => {
  it("creates through the preview_share_create relay and displays the backend-assembled URL", async () => {
    const container = mountMenu();
    await flushUntil(() => container.querySelector('button[aria-label="预览链接有效期"]') !== null);
    clickButton(container, "创建链接");
    await flushUntil(() => container.textContent?.includes("https://sync.example.com/share/preview/tok-1") === true);
    expect(mocks.create).toHaveBeenCalledWith("tab-1", 3_600_000);
  });

  it("disables the allow-control checkbox and explains that control is unsupported", async () => {
    const container = mountMenu();
    await flushUntil(() => mocks.active.mock.calls.length >= 1);
    await flush();
    const checkbox = [...container.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')].find((candidate) =>
      candidate.closest("label")?.textContent?.includes("允许控制"),
    );
    expect(checkbox).toBeDefined();
    expect(checkbox!.disabled).toBe(true);
    expect(checkbox!.checked).toBe(false);
    expect(container.textContent).toContain("桌面中继创建的预览暂不支持控制");
  });

  it("filters the list by the active relay shell ID for the current tab and revokes through remoteSharingApi", async () => {
    mocks.previewLinks.mockResolvedValue({
      links: [
        linkOn("shell-1", "l-1"),
        linkOn("shell-2", "l-2"),
        linkOn("shell-1", "l-3", { revoked_at: NOW - 1_000 }),
        linkOn("shell-1", "l-4", { expires_at: NOW - 1_000 }),
      ],
    });
    const container = mountMenu();
    await flushUntil(() => container.textContent?.includes("进行中的预览") === true);
    const revokes = [...container.querySelectorAll("button")].filter((b) => b.textContent?.trim() === "吊销");
    expect(revokes).toHaveLength(1);
    expect(mocks.active).toHaveBeenCalledWith("tab-1");

    clickButton(container, "吊销");
    await flushUntil(() => mocks.revokePreviewLink.mock.calls.length === 1);
    expect(mocks.revokePreviewLink).toHaveBeenCalledWith("l-1");
    await flushUntil(() => mocks.previewLinks.mock.calls.length >= 2);
  });

  it("lists no links when the current tab has no active relay", async () => {
    mocks.active.mockResolvedValue({});
    mocks.previewLinks.mockResolvedValue({ links: [linkOn("shell-9", "l-9")] });
    mounted = mountWithClient(createElement(PreviewShareMenu, { tabId: "tab-1", sessionName: "web-01" }));
    const container = mounted.container;
    const open = [...container.querySelectorAll("button")].find((button) => button.textContent?.trim() === "预览链接")!;
    await act(async () => {
      open.click();
      await flushUntil(() => mocks.active.mock.calls.length >= 1);
      await flush();
    });
    expect(mocks.previewLinks).not.toHaveBeenCalled();
    expect(container.textContent?.includes("进行中的预览")).toBe(false);
  });
});

describe("TerminalPane read-only preview login gate", () => {
  function GateProbe() {
    const enabled = usePreviewShareEnabled();
    return <span>{enabled ? "on" : "off"}</span>;
  }

  it("returns true for a logged-in desktop session", async () => {
    mounted = mountWithClient(createElement(GateProbe));
    await flushUntil(() => mounted!.container.textContent === "on");
  });

  it("returns false for a logged-out desktop session and hides the entry", async () => {
    mocks.status.mockResolvedValue({ configured: false, loggedIn: false, seq: 0, verifiedAt: 0, lastError: "" });
    mounted = mountWithClient(createElement(GateProbe));
    await flushUntil(() => mounted!.container.textContent === "off");
  });
});
