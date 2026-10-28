import { describe, expect, it } from "vitest";
import { installForegroundResume, type ForegroundResumeEnvironment, type ForegroundResumeReason } from "../../app/foregroundResume";

type Listener = (event: Event) => void;

function fakeEnvironment() {
  const documentListeners = new Map<string, Set<Listener>>();
  const windowListeners = new Map<string, Set<Listener>>();
  const add = (map: Map<string, Set<Listener>>) => (type: string, listener: EventListenerOrEventListenerObject) => {
    const set = map.get(type) ?? new Set<Listener>();
    set.add(listener as Listener);
    map.set(type, set);
  };
  const remove = (map: Map<string, Set<Listener>>) => (type: string, listener: EventListenerOrEventListenerObject) => {
    map.get(type)?.delete(listener as Listener);
  };
  const state = {
    now: 0,
    online: true,
    visibility: "visible" as DocumentVisibilityState,
    tick: null as (() => void) | null,
    intervalMs: 0,
    handle: 0,
    cleared: false,
  };
  const env: ForegroundResumeEnvironment = {
    document: {
      get visibilityState() {
        return state.visibility;
      },
      addEventListener: add(documentListeners),
      removeEventListener: remove(documentListeners),
    } as ForegroundResumeEnvironment["document"],
    window: {
      addEventListener: add(windowListeners),
      removeEventListener: remove(windowListeners),
    } as ForegroundResumeEnvironment["window"],
    isOnline: () => state.online,
    now: () => state.now,
    setInterval: (callback, ms) => {
      state.tick = callback;
      state.intervalMs = ms;
      state.handle += 1;
      return state.handle;
    },
    clearInterval: (handle) => {
      if (handle === state.handle) state.tick = null;
      state.cleared = true;
    },
  };
  const dispatch = (map: Map<string, Set<Listener>>, type: string, event: Event) => {
    for (const listener of map.get(type) ?? []) listener(event);
  };
  return {
    env,
    state,
    visibility(value: DocumentVisibilityState) {
      state.visibility = value;
      dispatch(documentListeners, "visibilitychange", new Event("visibilitychange"));
    },
    window(type: string, event: Event = new Event(type)) {
      dispatch(windowListeners, type, event);
    },
    listenerCount() {
      let count = 0;
      for (const set of [...documentListeners.values(), ...windowListeners.values()]) count += set.size;
      return count;
    },
  };
}

function install(fake: ReturnType<typeof fakeEnvironment>) {
  const reasons: ForegroundResumeReason[] = [];
  const dispose = installForegroundResume((reason) => reasons.push(reason), {}, fake.env);
  return { reasons, dispose };
}

describe("installForegroundResume", () => {
  it("fires when the page becomes visible but not when it is hidden", () => {
    const fake = fakeEnvironment();
    const { reasons } = install(fake);
    fake.visibility("hidden");
    expect(reasons).toEqual([]);
    fake.visibility("visible");
    expect(reasons).toEqual(["visible"]);
  });

  it("merges triggers within one second into the first", () => {
    const fake = fakeEnvironment();
    const { reasons } = install(fake);
    fake.visibility("visible");
    fake.state.now = 400;
    fake.window("online");
    fake.state.now = 999;
    fake.visibility("visible");
    expect(reasons).toEqual(["visible"]);
    fake.state.now = 1000;
    fake.window("online");
    expect(reasons).toEqual(["visible", "online"]);
  });

  it("does not probe while offline", () => {
    const fake = fakeEnvironment();
    const { reasons } = install(fake);
    fake.state.online = false;
    fake.visibility("visible");
    expect(reasons).toEqual([]);
    fake.state.online = true;
    fake.window("online");
    expect(reasons).toEqual(["online"]);
  });

  it("fires on a restored back/forward cache page only", () => {
    const fake = fakeEnvironment();
    const { reasons } = install(fake);
    fake.window("pageshow", Object.assign(new Event("pageshow"), { persisted: false }));
    expect(reasons).toEqual([]);
    fake.window("pageshow", Object.assign(new Event("pageshow"), { persisted: true }));
    expect(reasons).toEqual(["pageshow"]);
  });

  it("detects a wake from sleep by a late timer tick", () => {
    const fake = fakeEnvironment();
    const { reasons } = install(fake);
    expect(fake.state.intervalMs).toBe(5000);
    fake.state.now = 5000;
    fake.state.tick?.();
    fake.state.now = 34000;
    fake.state.tick?.();
    expect(reasons).toEqual([]);
    // Ten minutes asleep: the next tick arrives far later than scheduled.
    fake.state.now = 34000 + 10 * 60 * 1000;
    fake.state.tick?.();
    expect(reasons).toEqual(["wake"]);
  });

  it("stops the wake timer while hidden and restarts it from a fresh baseline", () => {
    const fake = fakeEnvironment();
    const { reasons } = install(fake);
    fake.visibility("hidden");
    expect(fake.state.tick).toBeNull();
    // Ten minutes in the background, then shown: one visible probe, not a wake from the stale baseline.
    fake.state.now = 10 * 60 * 1000;
    fake.visibility("visible");
    expect(reasons).toEqual(["visible"]);
    fake.state.now += 5000;
    fake.state.tick?.();
    expect(reasons).toEqual(["visible"]);
  });

  it("removes its listeners and timer on dispose", () => {
    const fake = fakeEnvironment();
    const { reasons, dispose } = install(fake);
    expect(fake.listenerCount()).toBe(3);
    dispose();
    expect(fake.listenerCount()).toBe(0);
    expect(fake.state.cleared).toBe(true);
    fake.visibility("visible");
    expect(reasons).toEqual([]);
  });
});
