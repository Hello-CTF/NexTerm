/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { act, createElement } from "react";
import { deferred, flush, flushUntil, mount, type MountedView } from "./features/reactTestUtils";
import type {
  WatchApi,
  WatchEvent,
  WatchSummary,
  WatchTargetSummary,
  WatchToolData,
} from "../features/ai/watchTypes";
import { WatchPanel } from "../features/ai/WatchPanel";

const ownerA = "owner-a";
const ownerB = "owner-b";

function watch(overrides: Partial<WatchSummary> & { id: string }): WatchSummary {
  return {
    targetId: "target-1",
    name: overrides.id,
    condition: { type: "contains", pattern: "ready" },
    status: "armed",
    repeat: false,
    anchorSeq: 0,
    generation: 1,
    revision: 1,
    updatedAt: 1000,
    ...overrides,
  };
}

function target(overrides: Partial<WatchTargetSummary> & { targetId: string }): WatchTargetSummary {
  return { identity: "identity-1", state: "attached", ...overrides };
}

function event(overrides: Partial<WatchEvent> & { deliveryId: string; watchId: string }): WatchEvent {
  return {
    kind: "watchEvent",
    runId: `run-${overrides.deliveryId}`,
    targetId: "target-1",
    matchedFrom: 0,
    matchedTo: 5,
    excerpt: "ready",
    firedAt: 2000,
    ...overrides,
  };
}

class SyntheticApi implements WatchApi {
  watches = new Map<string, WatchSummary[]>();
  targets = new Map<string, WatchTargetSummary[]>();
  cancelCalls: Array<{ owner: string; watchId: string; expectedRevision: number }> = [];
  cancelError: Error | null = null;
  private listeners = new Map<string, (data: WatchToolData) => void>();

  listWatches(owner: string): Promise<WatchSummary[]> {
    return Promise.resolve(this.watches.get(owner) ?? []);
  }
  listTargets(owner: string): Promise<WatchTargetSummary[]> {
    return Promise.resolve(this.targets.get(owner) ?? []);
  }
  cancelWatch(owner: string, watchId: string, expectedRevision: number): Promise<void> {
    this.cancelCalls.push({ owner, watchId, expectedRevision });
    if (this.cancelError) return Promise.reject(this.cancelError);
    return Promise.resolve();
  }
  subscribeWatchToolData(owner: string, onData: (data: WatchToolData) => void): () => void {
    this.listeners.set(owner, onData);
    return () => {
      this.listeners.delete(owner);
    };
  }
  emit(owner: string, data: WatchToolData): void {
    this.listeners.get(owner)?.(data);
  }
  listener(owner: string): ((data: WatchToolData) => void) | undefined {
    return this.listeners.get(owner);
  }
}

let mounted: MountedView | undefined;
let api: SyntheticApi;

beforeEach(() => {
  document.body.replaceChildren();
  api = new SyntheticApi();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function mountPanel(owner: string): void {
  mounted = mount(createElement(WatchPanel, { api, owner }));
}

function renderPanel(owner: string): void {
  act(() => mounted?.root.render(createElement(WatchPanel, { api, owner })));
}

function text(): string {
  return mounted?.container.textContent ?? "";
}

describe("WatchPanel cancel revision", () => {
  it("passes the durable watch revision, not the target generation", async () => {
    api.watches.set(ownerA, [watch({ id: "w-a1", generation: 2, revision: 7 })]);
    mountPanel(ownerA);
    await flushUntil(() => text().includes("w-a1"));
    const button = [...mounted!.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("取消"),
    );
    expect(button).toBeTruthy();
    await act(async () => button!.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    await flushUntil(() => api.cancelCalls.length === 1);
    expect(api.cancelCalls[0]).toEqual({ owner: ownerA, watchId: "w-a1", expectedRevision: 7 });
  });

  it("surfaces a stale-snapshot CAS rejection instead of swallowing it", async () => {
    api.watches.set(ownerA, [watch({ id: "w-a1", revision: 1 })]);
    api.cancelError = new Error("revision conflict");
    mountPanel(ownerA);
    await flushUntil(() => text().includes("w-a1"));
    const button = [...mounted!.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("取消"),
    );
    await act(async () => button!.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    await flushUntil(() => text().includes("revision conflict"));
    expect(text()).toContain("revision conflict");
  });
});

describe("WatchPanel first-commit owner isolation", () => {
  it("the first committed frame after a switch shows none of the previous owner", async () => {
    api.watches.set(ownerA, [watch({ id: "w-a1" })]);
    api.watches.set(ownerB, [watch({ id: "w-b1" })]);
    mountPanel(ownerA);
    await flushUntil(() => text().includes("w-a1"));
    act(() => api.emit(ownerA, { channel: "tool", untrusted: true, event: event({ deliveryId: "d-a", watchId: "w-a1" }) }));
    await flushUntil(() => text().includes("d-a"));
    await flushUntil(() => text().includes("未读 1"));

    // Record every committed frame after the switch: none of them may show
    // the previous owner's rows, events or unread count.
    const frames: string[] = [];
    const observer = new MutationObserver(() => {
      frames.push(mounted!.container.textContent ?? "");
    });
    observer.observe(mounted!.container, { childList: true, subtree: true, characterData: true });
    renderPanel(ownerB);
    await flushUntil(() => text().includes("w-b1"));
    observer.disconnect();
    expect(frames.join("\n")).not.toContain("w-a1");
    expect(frames.join("\n")).not.toContain("d-a");
    expect(frames.join("\n")).not.toContain("未读 1");
    expect(text()).not.toContain("w-a1");
  });
});

describe("WatchPanel stale operations", () => {
  it("a deferred cancel for the old owner never reloads it over the new owner", async () => {
    api.watches.set(ownerA, [watch({ id: "w-a1" })]);
    api.watches.set(ownerB, [watch({ id: "w-b1" })]);
    const cancelGate = deferred<void>();
    api.cancelWatch = (owner, watchId, expectedRevision) => {
      api.cancelCalls.push({ owner, watchId, expectedRevision });
      return cancelGate.promise;
    };
    mountPanel(ownerA);
    await flushUntil(() => text().includes("w-a1"));
    const button = [...mounted!.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("取消"),
    );
    await act(async () => button!.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    await flushUntil(() => api.cancelCalls.length === 1);

    renderPanel(ownerB);
    await flushUntil(() => text().includes("w-b1"));
    // The old owner's cancel completes now; it must not touch owner B.
    await act(async () => cancelGate.resolve());
    await flush();
    expect(text()).toContain("w-b1");
    expect(text()).not.toContain("w-a1");
  });

  it("an already-queued old-owner subscription callback is rejected after the switch", async () => {
    api.watches.set(ownerA, [watch({ id: "w-a1" })]);
    api.watches.set(ownerB, [watch({ id: "w-b1" })]);
    mountPanel(ownerA);
    await flushUntil(() => text().includes("w-a1"));
    const queued = api.listener(ownerA);
    expect(queued).toBeTruthy();

    renderPanel(ownerB);
    await flushUntil(() => text().includes("w-b1"));
    // A delivery queued under owner A fires after the switch; it must be
    // rejected by the per-effect epoch, not appended to owner B.
    act(() => queued!({ channel: "tool", untrusted: true, event: event({ deliveryId: "d-stale", watchId: "w-a1" }) }));
    await flush();
    expect(text()).not.toContain("d-stale");
    expect(text()).not.toContain("未读 1");
  });
});

describe("WatchPanel state evidence", () => {
  it("shows an evidence-less gone target as unknown, never as gone", async () => {
    api.watches.set(ownerA, [watch({ id: "w-a1" })]);
    api.targets.set(ownerA, [target({ targetId: "target-1", state: "gone" })]);
    mountPanel(ownerA);
    await flushUntil(() => text().includes("target-1"));
    expect(text()).toContain("未知");
    expect(text()).not.toContain("已消失");
  });

  it("shows an evidence-less replaced target as unknown", async () => {
    api.watches.set(ownerA, [watch({ id: "w-a1" })]);
    api.targets.set(ownerA, [target({ targetId: "target-1", state: "replaced" })]);
    mountPanel(ownerA);
    await flushUntil(() => text().includes("target-1"));
    expect(text()).toContain("未知");
    expect(text()).not.toContain("已被替换");
  });

  it("shows an evidence-less exited target as unknown", async () => {
    api.watches.set(ownerA, [watch({ id: "w-a1" })]);
    api.targets.set(ownerA, [target({ targetId: "target-1", state: "exited" })]);
    mountPanel(ownerA);
    await flushUntil(() => text().includes("target-1"));
    expect(text()).toContain("未知");
    expect(text()).not.toContain("已退出");
  });

  it("shows a gone target with its evidence", async () => {
    api.watches.set(ownerA, [watch({ id: "w-a1" })]);
    api.targets.set(ownerA, [target({ targetId: "target-1", state: "gone", evidence: "session exited" })]);
    mountPanel(ownerA);
    await flushUntil(() => text().includes("已消失"));
    expect(text()).toContain("session exited");
  });

  it("shows an evidence-less disconnected target as disconnected with no evidence", async () => {
    api.watches.set(ownerA, [watch({ id: "w-a1" })]);
    api.targets.set(ownerA, [target({ targetId: "target-1", state: "disconnected" })]);
    mountPanel(ownerA);
    await flushUntil(() => text().includes("已断连"));
    expect(text()).toContain("无证据");
    expect(text()).not.toContain("已消失");
  });

  it("shows an idle armed watch as waiting, never as done", async () => {
    api.watches.set(ownerA, [watch({ id: "w-a1", status: "armed" })]);
    mountPanel(ownerA);
    await flushUntil(() => text().includes("等待中"));
    expect(text()).not.toContain("已完成");
  });

  it("shows an unverifiable watch with its evidence", async () => {
    api.watches.set(ownerA, [watch({ id: "w-a1", status: "unverifiable", lastError: "output gap" })]);
    mountPanel(ownerA);
    await flushUntil(() => text().includes("不可验证"));
    expect(text()).toContain("output gap");
  });
});

describe("WatchPanel duplicate deliveries", () => {
  it("shows duplicate deliveries but counts them once", async () => {
    api.watches.set(ownerA, [watch({ id: "w-a1" })]);
    mountPanel(ownerA);
    await flushUntil(() => text().includes("w-a1"));
    const data: WatchToolData = { channel: "tool", untrusted: true, event: event({ deliveryId: "d-1", watchId: "w-a1" }) };
    act(() => api.emit(ownerA, data));
    await flushUntil(() => text().includes("d-1"));
    act(() => api.emit(ownerA, data));
    await flushUntil(() => text().includes("重复投递"));
    expect(mounted!.container.querySelectorAll("[data-delivery='d-1']").length).toBe(2);
    expect(text()).toContain("未读 1");
    expect(text()).not.toContain("未读 2");
  });
});

describe("WatchPanel untrusted tool data", () => {
  it("renders watch events as untrusted tool data, not as user messages", async () => {
    api.watches.set(ownerA, [watch({ id: "w-a1" })]);
    mountPanel(ownerA);
    await flushUntil(() => text().includes("w-a1"));
    act(() => api.emit(ownerA, { channel: "tool", untrusted: true, event: event({ deliveryId: "d-1", watchId: "w-a1" }) }));
    await flushUntil(() => text().includes("d-1"));
    expect(text()).toContain("不可信");
    expect(text()).toContain("工具数据");
    expect(mounted!.container.querySelector("[data-role='user-message']")).toBeNull();
  });
});

describe("WatchPanel owner isolation", () => {
  it("shows only the current owner's watches after a switch", async () => {
    api.watches.set(ownerA, [watch({ id: "w-a1" }), watch({ id: "w-a2" })]);
    api.watches.set(ownerB, [watch({ id: "w-b1" })]);
    mountPanel(ownerA);
    await flushUntil(() => text().includes("w-a1"));
    expect(text()).toContain("w-a2");

    renderPanel(ownerB);
    await flushUntil(() => text().includes("w-b1"));
    expect(text()).not.toContain("w-a1");
    expect(text()).not.toContain("w-a2");
  });
});

describe("WatchPanel out-of-order responses", () => {
  it("discards a stale list response that resolves after a newer one", async () => {
    const slow = deferred<WatchSummary[]>();
    api.listWatches = (owner: string) =>
      owner === ownerA ? slow.promise : Promise.resolve([watch({ id: "w-b1" })]);
    mountPanel(ownerA);
    renderPanel(ownerB);
    await flushUntil(() => text().includes("w-b1"));

    slow.resolve([watch({ id: "w-a1" })]);
    await flush();
    expect(text()).not.toContain("w-a1");
    expect(text()).toContain("w-b1");
  });
});
