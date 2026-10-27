/** @vitest-environment jsdom */
// Synced scheduled jobs bound to other devices are listed read-only with their device and can be removed everywhere.
import { afterEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, flush, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  cronForeignList: vi.fn(),
  cronForeignRemove: vi.fn(),
  ask: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  syncApi: { cronForeignList: mocks.cronForeignList, cronForeignRemove: mocks.cronForeignRemove },
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

import { ForeignCronSection } from "../../features/settings/syncForeignCron";

let mounted: MountedView | undefined;

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.clearAllMocks();
});

const JOB = { id: "job-1", name: "disk report", prompt: "report disk usage", schedule: "0 9 * * *", timezone: "UTC", enabled: true, deviceId: "dev-a", deviceName: "laptop" };

describe("ForeignCronSection", () => {
  it("shows jobs of other devices with the device that runs them and removes one after confirmation", async () => {
    mocks.cronForeignList.mockResolvedValueOnce([JOB]).mockResolvedValueOnce([]);
    mocks.cronForeignRemove.mockResolvedValue(undefined);
    mocks.ask.mockResolvedValue(true);
    mounted = mount(createElement(ForeignCronSection));
    await flush();
    expect(mounted.container.textContent).toContain("disk report");
    expect(mounted.container.textContent).toContain("运行于 laptop");

    click(mounted.container.querySelector("button")!);
    await flush();
    await flush();
    expect(mocks.cronForeignRemove).toHaveBeenCalledWith("job-1");
    expect(mounted.container.textContent).not.toContain("disk report");
  });

  it("keeps the job when removal is not confirmed and renders nothing without foreign jobs", async () => {
    mocks.cronForeignList.mockResolvedValue([JOB]);
    mocks.ask.mockResolvedValue(false);
    mounted = mount(createElement(ForeignCronSection));
    await flush();
    click(mounted.container.querySelector("button")!);
    await flush();
    expect(mocks.cronForeignRemove).not.toHaveBeenCalled();
    mounted.unmount();

    mocks.cronForeignList.mockResolvedValue([]);
    mounted = mount(createElement(ForeignCronSection));
    await flush();
    expect(mounted.container.textContent).toBe("");
  });
});
