/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { FrontendPerfReport } from "../../ipc/diagnosticsApi";

const mocks = vi.hoisted(() => ({ status: vi.fn(), reportFrontend: vi.fn() }));
vi.mock("../../ipc/diagnosticsApi", () => ({ diagnosticsApi: mocks }));
vi.mock("../../app/store", () => ({ useUi: { subscribe: vi.fn(() => () => undefined) } }));

import {
  PROBE_WINDOW_MS,
  WindowStats,
  mutationLabel,
  percentile,
  shortAgent,
  startFrontendProbe,
  startFrontendProbeIfCapturing,
  type ProbeEnv,
} from "../../features/diagnostics/frontendProbe";
import { noteEvent, noteIpcCall } from "../../ipc/probeHooks";

describe("WindowStats", () => {
  it("summarises lag, frames, counters and resets for the next window", () => {
    const stats = new WindowStats();
    for (const lag of [1, 2, 3, 4, 100]) stats.noteLag(lag);
    for (const t of [0, 16, 32, 100]) stats.noteFrame(t);
    stats.noteIpc("terminal_list");
    stats.noteIpc("terminal_list");
    stats.noteIpc("sync_status");
    stats.noteEvent("session://status");
    stats.noteStoreSet(["sessions", "helperProgress"]);
    stats.noteStoreSet(["sessions"]);
    stats.noteMutations(5, "#root");
    stats.noteMutations(2, "div.nx-pane");
    stats.noteLongTask(80);

    const report = stats.snapshot({ windowMs: 2000, hidden: false, domNodes: 1200, xterms: 2, animations: 3, heapUsedMB: 31.26 });
    expect(report).toMatchObject({
      kind: "window", windowMs: 2000, lagMaxMs: 100, lagP95Ms: 100, fps: 2, maxFrameGapMs: 68,
      mutations: 7, storeSets: 2, longTasks: 1, longTaskMsMax: 80, domNodes: 1200, xterms: 2, animations: 3,
      heapUsedMB: 31.3, hidden: false,
    });
    expect(report.ipc).toEqual([{ name: "terminal_list", value: 2 }, { name: "sync_status", value: 1 }]);
    expect(report.storeKeys?.[0]).toEqual({ name: "sessions", value: 2 });
    expect(report.domChurn?.[0]).toEqual({ name: "#root", value: 5 });
    expect(report.events).toEqual([{ name: "session://status", value: 1 }]);

    const next = stats.snapshot({ windowMs: 1000, hidden: true });
    expect(next).toMatchObject({ lagMaxMs: 0, fps: 0, mutations: 0, storeSets: 0, hidden: true, ipc: [], domChurn: [] });
    expect("domNodes" in next).toBe(false);
  });

  it("does not report a frame gap across a hidden period", () => {
    const stats = new WindowStats();
    stats.noteFrame(0);
    stats.resetFrameClock();
    stats.noteFrame(60_000);
    expect(stats.snapshot({ windowMs: 1000, hidden: false }).maxFrameGapMs).toBe(0);
  });

  it("caps the top lists", () => {
    const stats = new WindowStats();
    for (let i = 0; i < 30; i++) stats.noteIpc(`cmd_${i}`);
    expect(stats.snapshot({ windowMs: 1000, hidden: false }).ipc).toHaveLength(8);
  });
});

describe("helpers", () => {
  it("computes percentiles", () => {
    expect(percentile([], 95)).toBe(0);
    expect(percentile([5], 95)).toBe(5);
    expect(percentile(Array.from({ length: 100 }, (_, i) => i + 1), 95)).toBe(95);
  });

  it("labels mutations by id or nx- class and never reads text", () => {
    document.body.innerHTML = '<div id="pane"><section class="foo nx-card"><span>secret host</span></section></div>';
    const span = document.querySelector("span");
    expect(mutationLabel(span)).toBe("section.nx-card");
    expect(mutationLabel(document.querySelector("section"))).toBe("section.nx-card");
    expect(mutationLabel(document.getElementById("pane"))).toBe("#pane");
    expect(mutationLabel(span?.firstChild ?? null)).toBe("section.nx-card");
    expect(mutationLabel(null)).toBe("body");
  });

  it("does not use an id built from user content as a label", () => {
    document.body.innerHTML = '<div id="/home/alice/prod.example.com"><section class="nx-card"><span>x</span></section></div><div id="row:r1:"><p>y</p></div>';
    expect(mutationLabel(document.querySelector("p"))).toBe("body");
    document.body.innerHTML = '<div id="/home/alice/prod.example.com"><p>y</p></div>';
    expect(mutationLabel(document.querySelector("p"))).toBe("body");
  });

  it("keeps only the engine tokens of the user agent", () => {
    const agent = shortAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15");
    expect(agent).toBe("AppleWebKit/605.1.15 Version/18.1");
    expect(agent.length).toBeLessThanOrEqual(64);
  });
});

function fakeEnv(report: ProbeEnv["report"]): ProbeEnv {
  return {
    report,
    now: () => performance.now(),
    hidden: () => false,
    domNodes: () => 10,
    xterms: () => 1,
    animations: () => 0,
    heapUsedMB: () => undefined,
    viewport: () => ({ width: 1200, height: 800 }),
    userAgent: () => "AppleWebKit/1 Version/2",
    requestFrame: () => 1,
    cancelFrame: () => undefined,
    observeMutations: () => null,
    observeLongTasks: () => null,
    subscribeStore: () => () => undefined,
  };
}

describe("startFrontendProbe", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    mocks.status.mockReset();
    mocks.reportFrontend.mockReset();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("sends a start line, then one window per interval, counting IPC names but not diagnostics calls", async () => {
    const sent: FrontendPerfReport[] = [];
    const stop = startFrontendProbe(fakeEnv((r) => (sent.push(r), Promise.resolve())));
    expect(sent[0]).toMatchObject({ kind: "start", agent: "AppleWebKit/1 Version/2", viewWidth: 1200, viewHeight: 800 });

    noteIpcCall("terminal_list");
    noteIpcCall("diagnostics_report_frontend");
    noteEvent("session://status");
    await vi.advanceTimersByTimeAsync(PROBE_WINDOW_MS);
    expect(sent).toHaveLength(2);
    expect(sent[1].kind).toBe("window");
    expect(sent[1].ipc).toEqual([{ name: "terminal_list", value: 1 }]);
    expect(sent[1].events).toEqual([{ name: "session://status", value: 1 }]);
    expect(sent[1].domNodes).toBe(10);

    await vi.advanceTimersByTimeAsync(PROBE_WINDOW_MS);
    expect(sent).toHaveLength(3);
    expect(sent[2].ipc).toEqual([]);

    stop();
    noteIpcCall("after_stop");
    await vi.advanceTimersByTimeAsync(PROBE_WINDOW_MS * 2);
    expect(sent).toHaveLength(3);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("stops itself after repeated send failures", async () => {
    const report = vi.fn(() => Promise.reject(new Error("no capture")));
    startFrontendProbe(fakeEnv(report));
    await vi.advanceTimersByTimeAsync(PROBE_WINDOW_MS * 5);
    expect(report.mock.calls.length).toBeLessThanOrEqual(3);
    expect(vi.getTimerCount()).toBe(0);
  });
});

describe("startFrontendProbeIfCapturing", () => {
  beforeEach(() => {
    mocks.status.mockReset();
  });

  it("starts nothing and sets no timers when no capture is active", async () => {
    vi.useFakeTimers();
    mocks.status.mockResolvedValue({ active: false });
    expect(await startFrontendProbeIfCapturing()).toBeNull();
    expect(vi.getTimerCount()).toBe(0);
    vi.useRealTimers();
  });

  it("ignores status failures", async () => {
    mocks.status.mockRejectedValue({ code: "forbidden" });
    expect(await startFrontendProbeIfCapturing()).toBeNull();
  });
});
