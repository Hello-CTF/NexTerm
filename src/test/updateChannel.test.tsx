/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, flushUntil, mount, type MountedView } from "./features/reactTestUtils";
import type { UpdateStatusDto } from "../ipc/types";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    check: vi.fn(),
    install: vi.fn(),
    restart: vi.fn(),
    getChannel: vi.fn(),
    setChannel: vi.fn(),
    confirmDialog: vi.fn(),
  };
});

vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    appUpdateApi: {
      check: mocks.check,
      install: mocks.install,
      restart: mocks.restart,
      getChannel: mocks.getChannel,
      setChannel: mocks.setChannel,
    },
  };
});

vi.mock("../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ui/dialogs")>();
  return { ...actual, confirmDialog: mocks.confirmDialog };
});

import { UpdateCard } from "../features/settings/UpdateCard";
import { UpdateBanner } from "../app/UpdateBanner";
import { effectiveReleaseChannel, useUpdateStore } from "../app/useUpdate";

function makeStatus(patch: Partial<UpdateStatusDto> = {}): UpdateStatusDto {
  return {
    available: false,
    currentVersion: "1.3.6",
    version: "",
    notes: "",
    prerelease: false,
    assetName: "",
    assetSize: 0,
    canInstall: true,
    unavailableReason: "",
    channel: "",
    releaseChannel: "stable",
    runningPrerelease: false,
    ...patch,
  };
}

function resetStore(status: UpdateStatusDto | null): void {
  useUpdateStore.setState({
    status,
    checking: false,
    checkError: null,
    install: { active: false, phase: null, transferred: 0, total: 0, error: null, taskId: null },
    installedVersion: null,
    restarting: false,
    restartError: null,
    skippedVersion: null,
    remindedVersion: null,
    releaseChannel: null,
    channelSaving: false,
    channelError: null,
  });
}

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  window.localStorage.clear();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function channelButton(label: string): HTMLButtonElement {
  const group = document.querySelector('[aria-label="更新通道"]');
  const button = [...(group?.querySelectorAll("button") ?? [])].find((item) => item.textContent === label);
  if (!button) throw new Error(`channel button ${label} not found`);
  return button;
}

describe("UpdateCard update channel", () => {
  it("shows both channels with stable selected by default", () => {
    resetStore(makeStatus());
    mounted = mount(createElement(UpdateCard));
    expect(channelButton("稳定版").getAttribute("aria-pressed")).toBe("true");
    expect(channelButton("RC").getAttribute("aria-pressed")).toBe("false");
  });

  it("asks for confirmation before RC, saves and rechecks", async () => {
    resetStore(makeStatus());
    mocks.confirmDialog.mockResolvedValue(true);
    mocks.setChannel.mockResolvedValue({ channel: "rc" });
    mocks.check.mockResolvedValue(
      makeStatus({ available: true, version: "1.4.0-rc.1", prerelease: true, releaseChannel: "rc", assetSize: 1024 }),
    );
    mounted = mount(createElement(UpdateCard));
    click(channelButton("RC"));
    await flushUntil(() => mocks.check.mock.calls.length > 0);
    expect(mocks.confirmDialog).toHaveBeenCalledTimes(1);
    expect(String(mocks.confirmDialog.mock.calls[0]?.[0])).toContain("候选版本");
    expect(mocks.setChannel).toHaveBeenCalledWith("rc");
    await flushUntil(() => (document.body.textContent ?? "").includes("RC 候选版本"));
    expect(document.body.textContent).toContain("v1.4.0-rc.1");
    expect(channelButton("RC").getAttribute("aria-pressed")).toBe("true");
  });

  it("does nothing when the RC confirmation is declined", async () => {
    resetStore(makeStatus());
    mocks.confirmDialog.mockResolvedValue(false);
    mounted = mount(createElement(UpdateCard));
    click(channelButton("RC"));
    await flushUntil(() => mocks.confirmDialog.mock.calls.length > 0);
    await Promise.resolve();
    expect(mocks.setChannel).not.toHaveBeenCalled();
    expect(mocks.check).not.toHaveBeenCalled();
    expect(channelButton("稳定版").getAttribute("aria-pressed")).toBe("true");
  });

  it("switches back to stable without a confirmation and rechecks", async () => {
    resetStore(makeStatus({ releaseChannel: "rc" }));
    mocks.setChannel.mockResolvedValue({ channel: "stable" });
    mocks.check.mockResolvedValue(makeStatus());
    mounted = mount(createElement(UpdateCard));
    click(channelButton("稳定版"));
    await flushUntil(() => mocks.check.mock.calls.length > 0);
    expect(mocks.confirmDialog).not.toHaveBeenCalled();
    expect(mocks.setChannel).toHaveBeenCalledWith("stable");
    await flushUntil(() => !useUpdateStore.getState().checking && !useUpdateStore.getState().channelSaving);
  });

  it("explains the no-downgrade rule on stable while running an RC", () => {
    resetStore(makeStatus({ currentVersion: "1.4.0-rc.1", runningPrerelease: true }));
    mounted = mount(createElement(UpdateCard));
    expect(document.body.textContent).toContain("不会降级");
    expect(document.body.textContent).toContain("更新的稳定版");
  });

  it("omits the no-downgrade note on a stable build", () => {
    resetStore(makeStatus());
    mounted = mount(createElement(UpdateCard));
    expect(document.body.textContent).not.toContain("不会降级");
  });

  it("surfaces a save failure and keeps the previous channel", async () => {
    resetStore(makeStatus());
    mocks.confirmDialog.mockResolvedValue(true);
    mocks.setChannel.mockRejectedValue(new Error("磁盘已满"));
    mounted = mount(createElement(UpdateCard));
    click(channelButton("RC"));
    await flushUntil(() => (document.body.textContent ?? "").includes("更新通道保存失败"));
    expect(mocks.check).not.toHaveBeenCalled();
    expect(channelButton("稳定版").getAttribute("aria-pressed")).toBe("true");
  });

  it("hides the control where updates are delivered elsewhere (iOS, Android)", () => {
    resetStore(makeStatus({ channel: "app-store", canInstall: false }));
    mounted = mount(createElement(UpdateCard));
    expect(document.querySelector('[aria-label="更新通道"]')).toBeNull();
  });
});

describe("update channel store", () => {
  it("a forced recheck supersedes a stale in-flight check", async () => {
    resetStore(null);
    let releaseStale: (status: UpdateStatusDto) => void = () => {};
    mocks.check
      .mockImplementationOnce(() => new Promise<UpdateStatusDto>((resolve) => { releaseStale = resolve; }))
      .mockResolvedValueOnce(makeStatus({ releaseChannel: "rc", available: true, version: "1.4.0-rc.1" }));
    const stale = useUpdateStore.getState().check();
    await useUpdateStore.getState().check(true);
    releaseStale(makeStatus({ releaseChannel: "stable" }));
    await stale;
    expect(useUpdateStore.getState().status?.releaseChannel).toBe("rc");
    expect(useUpdateStore.getState().checking).toBe(false);
  });

  it("effectiveReleaseChannel prefers the checked status and defaults to stable", () => {
    expect(effectiveReleaseChannel({ status: null, releaseChannel: null })).toBe("stable");
    expect(effectiveReleaseChannel({ status: null, releaseChannel: "rc" })).toBe("rc");
    expect(effectiveReleaseChannel({ status: makeStatus({ releaseChannel: "rc" }), releaseChannel: "stable" })).toBe("rc");
    expect(effectiveReleaseChannel({ status: makeStatus({ releaseChannel: "bogus" }), releaseChannel: null })).toBe("stable");
  });
});

describe("UpdateBanner RC marker", () => {
  it("marks an RC update", () => {
    resetStore(makeStatus({ available: true, version: "1.4.0-rc.1", prerelease: true, releaseChannel: "rc" }));
    mounted = mount(createElement(UpdateBanner, { onOpenSettings: () => {} }));
    expect(document.body.textContent).toContain("RC 候选版本");
  });

  it("does not mark a stable update", () => {
    resetStore(makeStatus({ available: true, version: "1.4.0" }));
    mounted = mount(createElement(UpdateBanner, { onOpenSettings: () => {} }));
    expect(document.body.textContent).not.toContain("RC");
  });
});
