/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  status: vi.fn(),
  toast: vi.fn(),
  seeded: {
    tabId: "tab-a",
    jobId: "job-a",
    token: "token-a",
    task: "install nginx",
    allowWrite: true,
    startedAt: 1,
    paused: true,
  },
}));

vi.hoisted(() => {
  localStorage.setItem("nexterm.takeover.v1", JSON.stringify(mocks.seeded));
});

vi.mock("../../ipc/commands", () => ({
  aiApi: {
    takeoverCancel: vi.fn(),
    takeoverPause: vi.fn(),
    takeoverResume: vi.fn(),
    takeoverExit: vi.fn(),
    takeoverStatus: mocks.status,
  },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: {},
}));

import { TakeoverBanner } from "../../app/TakeoverBanner";
import { useUi } from "../../app/store";

describe("TakeoverBanner paused localStorage restoration", () => {
  let view: MountedView | null = null;

  beforeEach(() => {
    vi.clearAllMocks();
    mocks.status.mockResolvedValue({ active: true, jobId: "job-a", phase: "paused" });
    useUi.setState({ pushToast: mocks.toast });
  });

  afterEach(() => {
    view?.unmount();
    view = null;
    useUi.getState().setTakeover(null);
  });

  it("restores the paused flag and offers continue without clearing persistence", async () => {
    view = mount(createElement(TakeoverBanner));

    await flushUntil(() => (view!.container.textContent ?? "").includes("AI 接管已暂停"));
    expect([...view.container.querySelectorAll("button")].some((button) => button.textContent === "继续")).toBe(true);
    expect(JSON.parse(localStorage.getItem("nexterm.takeover.v1")!).paused).toBe(true);
    expect(mocks.status).toHaveBeenCalledWith("tab-a", "token-a");
    expect(useUi.getState().takeover).not.toBeNull();
  });
});
