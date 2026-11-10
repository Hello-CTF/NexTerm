/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, mountWithClient, waitForRendered, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({ sample: vi.fn(), metrics: vi.fn() }));
vi.mock("../../ipc/hostMonitorApi", () => ({ hostMonitorApi: { sample: mocks.sample } }));
vi.mock("../../features/fleet/clients", () => ({ fleetClient: () => ({ metrics: mocks.metrics }) }));

import { HostMonitorSection } from "../../features/terminal/HostMonitorSection";
import { setTerminalHostMonitor } from "../../app/preferences";
import { useUi, type Workspace } from "../../app/store";

let mounted: MountedView | undefined;

function workspace(assetId: string, sessionId = "s1"): Workspace {
  return { id: "ws", kind: "session", title: "web-01", sessionId, assetId, panes: [], activePaneId: "", closable: true } as unknown as Workspace;
}

function render(ws: Workspace, visible = true) {
  mounted = mountWithClient(createElement(HostMonitorSection, { workspace: ws, visible }));
  return mounted.container;
}

function setSession(kind: string, status = "connected") {
  useUi.setState({ sessions: [{ id: "s1", assetId: "a1", name: "web-01", kind, status, tabs: [], createdAt: 0 }] } as never);
}

const snapshot = {
  status: "ok",
  sampledAt: 1,
  intervalMs: 3000,
  cpuPercent: 42.5,
  cpuCount: 4,
  memory: { totalBytes: 4 * 1024 ** 3, usedBytes: 1024 ** 3, availableBytes: 3 * 1024 ** 3, swapTotalBytes: 0, swapUsedBytes: 0 },
  load: { one: 0.5, five: 0.25, fifteen: 0.1 },
  network: [{ name: "eth0", rxBytes: 10, txBytes: 10, rxRate: 2048, txRate: 1024 }],
  disks: [{ mount: "/", filesystem: "/dev/sda1", totalBytes: 100 * 1024 ** 3, usedBytes: 50 * 1024 ** 3, availableBytes: 50 * 1024 ** 3, usedPercent: 50 }],
  uptimeSeconds: 7200,
};

beforeEach(() => {
  localStorage.clear();
  mocks.sample.mockReset();
  mocks.metrics.mockReset();
  setTerminalHostMonitor("off");
  setSession("ssh");
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("HostMonitorSection", () => {
  it("is off by default and runs nothing on the host", async () => {
    const container = render(workspace("a1"));
    const toggle = container.querySelector<HTMLInputElement>("[data-testid='host-monitor'] input[type='checkbox']");
    expect(toggle?.checked).toBe(false);
    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(mocks.sample).not.toHaveBeenCalled();
  });

  it("samples an SSH session once turned on and shows the metrics", async () => {
    mocks.sample.mockResolvedValue(snapshot);
    const container = render(workspace("a1"));
    click(container.querySelector("[data-testid='host-monitor'] input[type='checkbox']")!);
    await waitForRendered(() => expect(container.querySelector("[data-testid='host-monitor-metrics']")).not.toBeNull());
    expect(mocks.sample).toHaveBeenCalledWith("s1");
    const text = container.textContent ?? "";
    expect(text).toContain("42.5%");
    expect(text).toContain("1.0 GiB / 4.0 GiB");
    expect(text).toContain("0.50 0.25 0.10");
    expect(text).toContain("2.0 KiB/s");
    expect(container.querySelector("[role='meter']")?.getAttribute("aria-valuenow")).toBe("50");
  });

  it("says when disks are no longer read", async () => {
    setTerminalHostMonitor("on");
    mocks.sample.mockResolvedValue({ ...snapshot, disks: [], disksUnavailable: true });
    const container = render(workspace("a1"));
    await waitForRendered(() => expect(container.querySelector("[data-testid='host-monitor-disks-unavailable']")).not.toBeNull());
    expect(container.querySelector("[role='meter']")).toBeNull();
  });

  it("shows an unsupported host instead of an error", async () => {
    setTerminalHostMonitor("on");
    mocks.sample.mockResolvedValue({ status: "unsupported", reason: "主机监控目前只支持 Linux 主机", platform: "Darwin", sampledAt: 0, intervalMs: 0, network: [], disks: [] });
    const container = render(workspace("a1"));
    await waitForRendered(() => expect(container.querySelector("[data-testid='host-monitor-unsupported']")).not.toBeNull());
    expect(container.textContent).toContain("Linux");
  });

  it("does not sample while the sidebar is hidden or the session is disconnected", async () => {
    setTerminalHostMonitor("on");
    render(workspace("a1"), false);
    mounted?.unmount();
    setSession("ssh", "disconnected");
    render(workspace("a1"));
    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(mocks.sample).not.toHaveBeenCalled();
  });

  it("uses the device metrics for a fleet device instead of probing", async () => {
    setTerminalHostMonitor("on");
    setSession("device");
    mocks.metrics.mockResolvedValue({ samples: [], latest: null });
    const container = render(workspace("device/dev-1"));
    await waitForRendered(() => expect(container.textContent).toContain("设备尚未上报指标"));
    expect(mocks.metrics).toHaveBeenCalledWith("dev-1");
    expect(mocks.sample).not.toHaveBeenCalled();
  });
});
