/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { mount, type MountedView } from "../features/reactTestUtils";

vi.mock("../../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));
vi.mock("../../app/UpdateBanner", () => ({ UpdateBanner: () => null }));
vi.mock("../../app/DekUnlockBanner", () => ({ DekUnlockBanner: () => null }));
vi.mock("../../features/terminal/WorkspaceRestoreBanner", () => ({ WorkspaceRestoreBanner: () => null }));
vi.mock("../../app/layout", () => ({
  layoutRestoreStatus: () => ({ pending: false, preview: false, count: 0 }),
  reviewPendingLayoutRestore: vi.fn(),
  subscribeLayoutRestore: () => () => undefined,
}));

import { AppBanners } from "../../app/shell/AppBanners";
import { dismissLayoutResetNotice, layoutResetNotice, noteLayoutReset } from "../../app/layoutResetNotice";

let view: MountedView | null = null;

afterEach(() => {
  view?.unmount();
  view = null;
  dismissLayoutResetNotice();
});

describe("layout reset notice", () => {
  it("lists every affected workspace once and goes away when dismissed", () => {
    view = mount(createElement(AppBanners, { ws: null, onOpenSettings: () => undefined, onPickOtherAsset: () => undefined }));
    expect(view.container.textContent).not.toContain("布局已重置");

    act(() => {
      noteLayoutReset(["prod", "staging"]);
      noteLayoutReset(["prod"]);
      noteLayoutReset([]);
    });
    const status = view.container.querySelector('[role="status"]')!;
    expect(status.textContent).toContain("布局已重置");
    expect(status.textContent).toContain("prod、staging");
    expect(layoutResetNotice()).toEqual(["prod", "staging"]);

    const button = [...status.querySelectorAll("button")].find((item) => item.textContent === "知道了")!;
    act(() => button.click());
    expect(view.container.textContent).not.toContain("布局已重置");
    expect(layoutResetNotice()).toBeNull();
  });
});
