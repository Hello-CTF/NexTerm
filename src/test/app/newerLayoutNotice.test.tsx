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
import { noteNewerLayout } from "../../app/newerLayoutNotice";

let view: MountedView | null = null;

afterEach(() => {
  view?.unmount();
  view = null;
  noteNewerLayout(null);
});

describe("newer layout notice", () => {
  it("keeps a newer-format notice on screen until sync can resume", () => {
    view = mount(createElement(AppBanners, { ws: null, onOpenSettings: () => undefined, onPickOtherAsset: () => undefined }));
    act(() => noteNewerLayout(5));
    const alert = view.container.querySelector('[role="alert"]')!;
    expect(alert.textContent).toContain("格式 v5");
    expect(alert.querySelector("button")).toBeNull();
    act(() => noteNewerLayout(null));
    expect(view.container.querySelector('[role="alert"]')).toBeNull();
  });
});
