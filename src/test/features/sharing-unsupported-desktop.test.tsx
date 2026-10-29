/** @vitest-environment jsdom */
// Desktop share card - logged out: shows the "log in to a server account to use" guidance card (button scrolls to the "Account Sync" section), no requests fired.
import { afterEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, flushUntil, mountWithClient, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return { fetch: vi.fn(), status: vi.fn() };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    syncApi: { ...actual.syncApi, status: mocks.status },
  };
});

import { ShareCard } from "../../features/settings/ShareCard";
import { onSettingsSection } from "../../features/settings/settingsNavigation";

let mounted: MountedView | undefined;

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  document.body.replaceChildren();
  vi.unstubAllGlobals();
});

describe("Share card - desktop logged out", () => {
  it("renders login guidance without requests and links the button to the Account Sync section", async () => {
    vi.stubGlobal("fetch", mocks.fetch);
    mocks.status.mockResolvedValue({ configured: false, loggedIn: false, seq: 0, verifiedAt: 0, lastError: "" });
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("登录服务端账号后可用") ?? false);

    expect(document.body.textContent).toContain("账号同步");
    expect(mocks.fetch).not.toHaveBeenCalled();

    const guide = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("前往"));
    expect(guide).toBeTruthy();
    // The settings view opens the section's group before scrolling.
    const requested = vi.fn();
    const stop = onSettingsSection(requested);
    click(guide as HTMLButtonElement);
    stop();
    expect(requested).toHaveBeenCalledWith("settings-account");
  });
});
