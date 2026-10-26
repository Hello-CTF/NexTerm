import { describe, expect, it, vi } from "vitest";
import {
  ContextLossBudget,
  HiddenWebglRetention,
  MAX_HIDDEN_WEBGL_CONTEXTS,
  createWebglController,
  isSoftwareRenderer,
  probeRenderer,
  shouldLoadWebgl,
  type GpuAccelerationMode,
  type RendererProbe,
  type WebglClock,
} from "../../features/terminal/webglPolicy";

class FakeWebglAddon {
  static instances: FakeWebglAddon[] = [];
  disposed = false;
  atlasClears = 0;
  private lossListener: (() => void) | null = null;

  constructor() {
    FakeWebglAddon.instances.push(this);
  }

  onContextLoss(listener: () => void) {
    this.lossListener = listener;
    return { dispose: () => (this.lossListener = null) };
  }

  clearTextureAtlas() {
    this.atlasClears += 1;
  }

  dispose() {
    this.disposed = true;
  }

  loseContext() {
    this.lossListener?.();
  }
}

class FakeClock implements WebglClock {
  time = 0;
  private timers = new Map<number, { at: number; callback: () => void }>();
  private seq = 0;

  now() {
    return this.time;
  }

  setTimeout(callback: () => void, ms: number) {
    this.seq += 1;
    this.timers.set(this.seq, { at: this.time + ms, callback });
    return this.seq;
  }

  clearTimeout(handle: unknown) {
    this.timers.delete(handle as number);
  }

  advance(ms: number) {
    this.time += ms;
    for (const [id, timer] of [...this.timers]) {
      if (timer.at <= this.time) {
        this.timers.delete(id);
        timer.callback();
      }
    }
  }
}

const hardware: RendererProbe = { webgl: true, software: false, renderer: "ANGLE (NVIDIA GeForce RTX 4070)" };
const software: RendererProbe = { webgl: true, software: true, renderer: "llvmpipe (LLVM 17.0.6, 256 bits)" };

function fakeHost() {
  return { loadAddon: vi.fn(), refresh: vi.fn(), rows: 24 };
}

async function settle(): Promise<void> {
  for (let i = 0; i < 5; i += 1) await Promise.resolve();
}

function setup(
  overrides: {
    mode?: GpuAccelerationMode;
    probe?: RendererProbe;
    visible?: boolean;
    retention?: HiddenWebglRetention;
    load?: () => Promise<typeof FakeWebglAddon>;
  } = {},
) {
  const host = fakeHost();
  const clock = new FakeClock();
  const onFallback = vi.fn();
  const probe = vi.fn(() => overrides.probe ?? hardware);
  const controller = createWebglController({
    host,
    load: overrides.load ?? (() => Promise.resolve(FakeWebglAddon)),
    mode: overrides.mode ?? "auto",
    visible: overrides.visible,
    clock,
    probe,
    retention: overrides.retention ?? new HiddenWebglRetention(),
    onFallback,
  });
  const attached = () => host.loadAddon.mock.calls.map(([addon]) => addon as FakeWebglAddon);
  const live = () => attached().filter((addon) => !addon.disposed);
  return { host, clock, onFallback, probe, controller, attached, live };
}

describe("renderer probe", () => {
  it("classifies software renderers", () => {
    for (const identity of [
      "Google Inc. (Google) ANGLE (Google, Vulkan 1.3.0 (SwiftShader Device (Subzero)), SwiftShader driver)",
      "Mesa llvmpipe (LLVM 15.0.7, 256 bits)",
      "Mesa softpipe",
      "Red Hat virgl",
      "Microsoft Basic Render Driver",
      "VMware SVGA3D",
    ]) {
      expect(isSoftwareRenderer(identity)).toBe(true);
    }
    expect(isSoftwareRenderer("ANGLE (Apple, ANGLE Metal Renderer: Apple M2, Unspecified Version)")).toBe(false);
    expect(isSoftwareRenderer("Mesa Intel(R) UHD Graphics 620 (KBL GT2)")).toBe(false);
  });

  it("reads the unmasked renderer through WEBGL_debug_renderer_info and frees the probe context", () => {
    const loseContext = vi.fn();
    const gl = {
      getExtension: (name: string) =>
        name === "WEBGL_debug_renderer_info"
          ? { UNMASKED_RENDERER_WEBGL: 1, UNMASKED_VENDOR_WEBGL: 2 }
          : { loseContext },
      getParameter: (parameter: number) => (parameter === 1 ? "llvmpipe (LLVM 17)" : "Mesa"),
    };
    expect(probeRenderer(() => ({ getContext: () => gl }))).toEqual({
      webgl: true,
      software: true,
      renderer: "llvmpipe (LLVM 17)",
    });
    expect(loseContext).toHaveBeenCalledTimes(1);
  });

  it("treats a masked identity as hardware and a missing context as no WebGL", () => {
    const masked = { getExtension: () => null, getParameter: () => null };
    expect(probeRenderer(() => ({ getContext: () => masked }))).toEqual({ webgl: true, software: false, renderer: null });
    expect(probeRenderer(() => ({ getContext: () => null }))).toEqual({ webgl: false, software: false, renderer: null });
    expect(
      probeRenderer(() => {
        throw new Error("canvas unavailable");
      }),
    ).toEqual({ webgl: false, software: false, renderer: null });
    // jsdom has no WebGL2RenderingContext, so the default probe never creates a canvas.
    expect(probeRenderer().webgl).toBe(false);
  });

  it("decides by mode", () => {
    expect(shouldLoadWebgl("off", () => hardware)).toBe(false);
    expect(shouldLoadWebgl("on", () => software)).toBe(true);
    expect(shouldLoadWebgl("auto", () => hardware)).toBe(true);
    expect(shouldLoadWebgl("auto", () => software)).toBe(false);
    expect(shouldLoadWebgl("auto", () => ({ webgl: false, software: false, renderer: null }))).toBe(false);
  });
});

describe("ContextLossBudget", () => {
  it("counts only losses inside the rolling window", () => {
    const budget = new ContextLossBudget(3, 60_000);
    expect(budget.record(0)).toBe(1);
    expect(budget.record(30_000)).toBe(2);
    expect(budget.record(61_000)).toBe(2);
    expect(budget.exhausted(budget.record(62_000))).toBe(true);
    budget.reset();
    expect(budget.record(63_000)).toBe(1);
  });
});

describe("WebGL controller", () => {
  it("does not load before start and attaches once started", async () => {
    const { controller, attached } = setup();
    await settle();
    expect(attached()).toHaveLength(0);
    controller.start();
    await settle();
    expect(attached()).toHaveLength(1);
  });

  it("falls back to DOM at once on context loss and re-attaches after a backoff", async () => {
    const { controller, attached, live, clock, host } = setup();
    controller.start();
    await settle();
    attached()[0].loseContext();
    expect(attached()[0].disposed).toBe(true);
    expect(live()).toHaveLength(0);
    // The DOM renderer repaints the whole viewport immediately, so the pane is never blank.
    expect(host.refresh).toHaveBeenCalledWith(0, 23);
    clock.advance(999);
    await settle();
    expect(attached()).toHaveLength(1);
    clock.advance(1);
    await settle();
    expect(attached()).toHaveLength(2);
    expect(live()).toHaveLength(1);
    // A re-attached renderer starts from a cleared atlas.
    expect(attached()[1].atlasClears).toBe(1);
  });

  it("stays on DOM with a single notice after three losses inside 60 s", async () => {
    const { controller, attached, live, clock, onFallback } = setup();
    controller.start();
    await settle();
    attached()[0].loseContext();
    clock.advance(1_000);
    await settle();
    attached()[1].loseContext();
    clock.advance(2_000);
    await settle();
    attached()[2].loseContext();
    expect(onFallback).toHaveBeenCalledTimes(1);
    clock.advance(600_000);
    controller.setVisible(false);
    controller.setVisible(true);
    await settle();
    expect(attached()).toHaveLength(3);
    expect(live()).toHaveLength(0);
    expect(onFallback).toHaveBeenCalledTimes(1);
  });

  it("keeps retrying when losses are spread beyond the window", async () => {
    const { controller, attached, live, clock, onFallback } = setup();
    controller.start();
    await settle();
    for (let i = 0; i < 6; i += 1) {
      attached()[i].loseContext();
      clock.advance(40_000);
      await settle();
    }
    expect(attached()).toHaveLength(7);
    expect(live()).toHaveLength(1);
    expect(onFallback).not.toHaveBeenCalled();
  });

  it("does not load WebGL on a software renderer in auto mode but does in on mode", async () => {
    const auto = setup({ probe: software });
    auto.controller.start();
    await settle();
    expect(auto.attached()).toHaveLength(0);

    const forced = setup({ probe: software, mode: "on" });
    forced.controller.start();
    await settle();
    expect(forced.attached()).toHaveLength(1);
    expect(forced.probe).not.toHaveBeenCalled();
  });

  it("never loads in off mode and detaches when switched off", async () => {
    const off = setup({ mode: "off" });
    off.controller.start();
    await settle();
    expect(off.attached()).toHaveLength(0);

    const { controller, attached, live } = setup();
    controller.start();
    await settle();
    controller.setMode("off");
    expect(attached()[0].disposed).toBe(true);
    expect(live()).toHaveLength(0);
  });

  it("resets the fallback latch when the GPU setting changes", async () => {
    const { controller, attached, live, clock, onFallback } = setup();
    controller.start();
    await settle();
    for (let i = 0; i < 3; i += 1) {
      attached()[i].loseContext();
      clock.advance(1_000 * 2 ** i);
      await settle();
    }
    expect(live()).toHaveLength(0);
    expect(onFallback).toHaveBeenCalledTimes(1);
    controller.setMode("on");
    await settle();
    expect(live()).toHaveLength(1);
    // Same mode again is not a change and keeps the renderer.
    controller.setMode("on");
    await settle();
    expect(live()).toHaveLength(1);
  });

  it("stays on DOM when the addon cannot create a GL context or fails to import", async () => {
    const { controller, host } = setup();
    host.loadAddon.mockImplementation(() => {
      throw new Error("no webgl");
    });
    controller.start();
    await settle();
    expect(host.loadAddon).toHaveBeenCalledTimes(1);
    expect(FakeWebglAddon.instances.at(-1)?.disposed).toBe(true);
    controller.setVisible(false);
    controller.setVisible(true);
    await settle();
    expect(host.loadAddon).toHaveBeenCalledTimes(1);

    const failed = setup({ load: () => Promise.reject(new Error("chunk load failed")) });
    failed.controller.start();
    await settle();
    expect(failed.attached()).toHaveLength(0);
  });

  it("ignores a late addon after dispose", async () => {
    let resolve: (addon: typeof FakeWebglAddon) => void = () => undefined;
    const { controller, attached } = setup({ load: () => new Promise((done) => (resolve = done)) });
    controller.start();
    controller.dispose();
    resolve(FakeWebglAddon);
    await settle();
    expect(attached()).toHaveLength(0);
  });

  it("clears the atlas of a retained renderer on reveal", async () => {
    const { controller, attached, host } = setup();
    controller.start();
    await settle();
    controller.setVisible(false);
    host.refresh.mockClear();
    controller.setVisible(true);
    expect(attached()).toHaveLength(1);
    expect(attached()[0].atlasClears).toBe(1);
    expect(host.refresh).toHaveBeenCalledWith(0, 23);
  });
});

describe("hidden-pane retention", () => {
  it("keeps WebGL on at most six hidden terminals out of twelve", async () => {
    const retention = new HiddenWebglRetention(MAX_HIDDEN_WEBGL_CONTEXTS);
    const panes: Array<ReturnType<typeof setup>> = [];
    for (let i = 0; i < 12; i += 1) panes.push(setup({ retention }));
    for (const pane of panes) pane.controller.start();
    await settle();
    expect(panes.every((pane) => pane.live().length === 1)).toBe(true);

    // Switching through the tabs hides every pane except the last one, in order.
    for (let i = 0; i < 11; i += 1) panes[i].controller.setVisible(false);
    expect(retention.size).toBe(MAX_HIDDEN_WEBGL_CONTEXTS);
    const liveCount = (list: Array<ReturnType<typeof setup>>) => list.filter((pane) => pane.live().length === 1).length;
    // The five longest-hidden panes were evicted back to DOM; the six most recent keep their context.
    expect(liveCount(panes.slice(0, 5))).toBe(0);
    expect(liveCount(panes.slice(5, 11))).toBe(6);
    expect(panes[11].live()).toHaveLength(1);

    // Revealing an evicted pane re-attaches WebGL with a cleared atlas.
    panes[11].controller.setVisible(false);
    panes[0].controller.setVisible(true);
    await settle();
    expect(panes[0].attached()).toHaveLength(2);
    expect(panes[0].live()[0].atlasClears).toBe(1);
    // Hiding the twelfth pane pushed the least-recently-visible retained pane out.
    expect(panes[5].live()).toHaveLength(0);
    expect(liveCount(panes.slice(6, 12))).toBe(6);
    expect(retention.size).toBe(MAX_HIDDEN_WEBGL_CONTEXTS);
  });

  it("does not attach a pane that starts hidden after eviction until it is revealed", async () => {
    const retention = new HiddenWebglRetention(1);
    const first = setup({ retention, visible: false });
    const second = setup({ retention, visible: false });
    first.controller.start();
    await settle();
    second.controller.start();
    await settle();
    expect(first.live()).toHaveLength(0);
    expect(second.live()).toHaveLength(1);
    first.controller.setMode("on");
    await settle();
    expect(first.live()).toHaveLength(0);
    first.controller.setVisible(true);
    await settle();
    expect(first.live()).toHaveLength(1);
  });

  it("releases a disposed pane's slot", async () => {
    const retention = new HiddenWebglRetention();
    const pane = setup({ retention });
    pane.controller.start();
    await settle();
    pane.controller.setVisible(false);
    expect(retention.size).toBe(1);
    pane.controller.dispose();
    expect(retention.size).toBe(0);
  });
});
