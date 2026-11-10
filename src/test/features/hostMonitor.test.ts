/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ sample: vi.fn() }));
vi.mock("../../ipc/hostMonitorApi", () => ({ hostMonitorApi: { sample: mocks.sample } }));

import {
  acquireHostMonitor,
  DEFAULT_INTERVAL_MS,
  getHostMonitorState,
  MAX_RETRY_MS,
  retryDelay,
} from "../../features/terminal/hostMonitor";

function ok(cpu: number | null, sampledAt: number, rxRate?: number) {
  return {
    status: "ok",
    sampledAt,
    intervalMs: DEFAULT_INTERVAL_MS,
    cpuPercent: cpu,
    memory: { totalBytes: 100, usedBytes: 25, availableBytes: 75, swapTotalBytes: 0, swapUsedBytes: 0 },
    network: [{ name: "eth0", rxBytes: 10, txBytes: 10, rxRate, txRate: rxRate }],
    disks: [],
  };
}

async function flush() {
  await vi.advanceTimersByTimeAsync(0);
}

describe("host monitor poller", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    mocks.sample.mockReset();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("samples at the interval, keeps history and stops on release", async () => {
    mocks.sample.mockResolvedValueOnce(ok(null, 1)).mockResolvedValueOnce(ok(12.5, 2, 1000)).mockResolvedValue(ok(20, 3, 2000));
    const release = acquireHostMonitor("s1");
    await flush();
    expect(mocks.sample).toHaveBeenCalledTimes(1);
    expect(mocks.sample).toHaveBeenCalledWith("s1");
    expect(getHostMonitorState("s1").history).toHaveLength(1);

    await vi.advanceTimersByTimeAsync(DEFAULT_INTERVAL_MS);
    const state = getHostMonitorState("s1");
    expect(mocks.sample).toHaveBeenCalledTimes(2);
    expect(state.snapshot?.cpuPercent).toBe(12.5);
    expect(state.history.map((p) => p.cpu)).toEqual([null, 12.5]);
    expect(state.history[1].mem).toBe(25);
    expect(state.history[1].net.eth0.rx).toBe(1000);

    release();
    expect(getHostMonitorState("s1").snapshot).toBeNull();
    await vi.advanceTimersByTimeAsync(DEFAULT_INTERVAL_MS * 5);
    expect(mocks.sample).toHaveBeenCalledTimes(2);
  });

  it("shares one poller between views of a session", async () => {
    mocks.sample.mockResolvedValue(ok(1, 1));
    const a = acquireHostMonitor("s2");
    const b = acquireHostMonitor("s2");
    await flush();
    expect(mocks.sample).toHaveBeenCalledTimes(1);
    a();
    await vi.advanceTimersByTimeAsync(DEFAULT_INTERVAL_MS);
    expect(mocks.sample).toHaveBeenCalledTimes(2);
    b();
    await vi.advanceTimersByTimeAsync(DEFAULT_INTERVAL_MS * 3);
    expect(mocks.sample).toHaveBeenCalledTimes(2);
  });

  it("stops asking an unsupported host", async () => {
    mocks.sample.mockResolvedValue({ status: "unsupported", reason: "Darwin", sampledAt: 0, intervalMs: 0, network: [], disks: [] });
    const release = acquireHostMonitor("s3");
    await flush();
    await vi.advanceTimersByTimeAsync(MAX_RETRY_MS * 2);
    expect(mocks.sample).toHaveBeenCalledTimes(1);
    expect(getHostMonitorState("s3").snapshot?.status).toBe("unsupported");
    release();
  });

  it("waits the server's retry delay after a host failure", async () => {
    mocks.sample
      .mockResolvedValueOnce({ status: "error", reason: "x", sampledAt: 0, intervalMs: DEFAULT_INTERVAL_MS, retryAfterMs: 12_000, network: [], disks: [] })
      .mockResolvedValue(ok(5, 1));
    const release = acquireHostMonitor("s4");
    await flush();
    await vi.advanceTimersByTimeAsync(11_000);
    expect(mocks.sample).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1_000);
    expect(mocks.sample).toHaveBeenCalledTimes(2);
    expect(getHostMonitorState("s4").snapshot?.status).toBe("ok");
    release();
  });

  it("backs off exponentially when the request fails", async () => {
    mocks.sample.mockRejectedValue(new Error("forbidden"));
    const release = acquireHostMonitor("s5");
    await flush();
    expect(getHostMonitorState("s5").error).toContain("forbidden");
    await vi.advanceTimersByTimeAsync(retryDelay(1));
    expect(mocks.sample).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(retryDelay(2) - 1);
    expect(mocks.sample).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(mocks.sample).toHaveBeenCalledTimes(3);
    expect(retryDelay(30)).toBe(MAX_RETRY_MS);
    release();
  });
});

describe("host monitor poller while the page is hidden", () => {
  let hidden = false;
  beforeEach(() => {
    vi.useFakeTimers();
    mocks.sample.mockReset();
    hidden = false;
    Object.defineProperty(document, "hidden", { configurable: true, get: () => hidden });
  });
  afterEach(() => {
    vi.useRealTimers();
    Object.defineProperty(document, "hidden", { configurable: true, get: () => false });
  });

  function setHidden(value: boolean) {
    hidden = value;
    document.dispatchEvent(new Event("visibilitychange"));
  }

  it("runs nothing while hidden and resumes once when visible again", async () => {
    mocks.sample.mockResolvedValue(ok(1, 1));
    const release = acquireHostMonitor("h1");
    await flush();
    expect(mocks.sample).toHaveBeenCalledTimes(1);

    setHidden(true);
    await vi.advanceTimersByTimeAsync(DEFAULT_INTERVAL_MS * 10);
    expect(mocks.sample).toHaveBeenCalledTimes(1);

    setHidden(false);
    // A focus event right after the visibility change must not start a second sample.
    window.dispatchEvent(new Event("focus"));
    await flush();
    expect(mocks.sample).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(DEFAULT_INTERVAL_MS);
    expect(mocks.sample).toHaveBeenCalledTimes(3);
    release();
  });

  it("does not start while hidden", async () => {
    mocks.sample.mockResolvedValue(ok(1, 1));
    hidden = true;
    const release = acquireHostMonitor("h2");
    await vi.advanceTimersByTimeAsync(DEFAULT_INTERVAL_MS * 3);
    expect(mocks.sample).not.toHaveBeenCalled();
    setHidden(false);
    await flush();
    expect(mocks.sample).toHaveBeenCalledTimes(1);
    release();
  });

  it("keeps a backoff after becoming visible", async () => {
    mocks.sample.mockRejectedValue(new Error("boom"));
    const release = acquireHostMonitor("h3");
    await flush();
    expect(mocks.sample).toHaveBeenCalledTimes(1);
    setHidden(true);
    setHidden(false);
    await flush();
    expect(mocks.sample).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(retryDelay(1));
    expect(mocks.sample).toHaveBeenCalledTimes(2);
    release();
  });
});

describe("host monitor preference", () => {
  beforeEach(() => {
    localStorage.clear();
    vi.resetModules();
  });

  it("defaults to off and rejects invalid stored values", async () => {
    localStorage.setItem("nexterm.terminalClose.v1", JSON.stringify({ hostMonitor: "yes" }));
    const { getTerminalPrefs } = await import("../../app/preferences");
    expect(getTerminalPrefs().hostMonitor).toBe("off");
  });

  it("stores and reloads on", async () => {
    const first = await import("../../app/preferences");
    first.setTerminalHostMonitor("on");
    vi.resetModules();
    const second = await import("../../app/preferences");
    expect(second.getTerminalPrefs().hostMonitor).toBe("on");
  });
});
