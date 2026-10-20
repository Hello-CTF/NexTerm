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
  private listeners = new Map<string, (data: WatchToolData) => void>();

  listWatches(owner: string): Promise<WatchSummary[]> {
    return Promise.resolve(this.watches.get(owner) ?? []);
  }
  listTargets(owner: string): Promise<WatchTargetSummary[]> {
    return Promise.resolve(this.targets.get(owner) ?? []);
  }
  cancelWatch(owner: string, watchId: string, expectedRevision: number): Promise<void> {
    this.cancelCalls.push({ owner, watchId, expectedRevision });
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

function text(): string {
  return mounted?.container.textContent ?? "";
}

describe("WatchPanel owner isolation", () => {
  it("shows only the current owner's watches after an owner switch", async () => {
    api.watches.set(ownerA, [watch({ id: "w-a1" }), watch({ id: "w-a2" })]);
    api.watches.set(ownerB, [watch({ id: "w-b1" })]);
    mountPanel(ownerA);
    await flushUntil(() => text().includes("w-a1"));
    expect(text()).toContain("w-a2");

    act(() => mounted?.root.render(createElement(WatchPanel, { api, owner: ownerB })));
    await flushUntil(() => text().includes("w-b1"));
    expect(text()).not.toContain("w-a1");
    expect(text()).not.toContain("w-a2");
  });

  it("does not leak the previous owner's tool data after a switch", async () => {
    api.watches.set(ownerA, [watch({ id: "w-a1" })]);
    api.watches.set(ownerB, [watch({ id: "w-b1" })]);
    mountPanel(ownerA);
    await flushUntil(() => text().includes("w-a1"));
    act(() => api.emit(ownerA, { channel: "tool", untrusted: true, event: event({ deliveryId: "d-a", watchId: "w-a1" }) }));
    await flushUntil(() => text().includes("d-a"));

    act(() => mounted?.root.render(createElement(WatchPanel, { api, owner: ownerB })));
    await flushUntil(() => text().includes("w-b1"));
    expect(text()).not.toContain("d-a");
  });
});

describe("WatchPanel out-of-order responses", () => {
  it("discards a stale list response that resolves after a newer one", async () => {
    const slow = deferred<WatchSummary[]>();
    api.listWatches = (owner: string) =>
      owner === ownerA ? slow.promise : Promise.resolve([watch({ id: "w-b1" })]);
    mountPanel(ownerA);
    act(() => mounted?.root.render(createElement(WatchPanel, { api, owner: ownerB })));
    await flushUntil(() => text().includes("w-b1"));

    // The stale owner-A response resolves late and must not overwrite B's rows.
    slow.resolve([watch({ id: "w-a1" })]);
    await flush();
    expect(text()).not.toContain("w-a1");
    expect(text()).toContain("w-b1");
  });
});

describe("WatchPanel cancel and recovery", () => {
  it("cancels a watch with its owner and revision", async () => {
    api.watches.set(ownerA, [watch({ id: "w-a1" })]);
    mountPanel(ownerA);
    await flushUntil(() => text().includes("w-a1"));
    const button = [...mounted!.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("取消"),
    );
    expect(button).toBeTruthy();
    await act(async () => button!.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    await flushUntil(() => JSON.stringify(api.cancelCalls) === JSON.stringify([{ owner: ownerA, watchId: "w-a1", expectedRevision: 1 }]));
  });

  it("shows a re-armed repeat watch as armed again (recovery)", async () => {
    api.watches.set(ownerA, [watch({ id: "w-a1", repeat: true, status: "matched" })]);
    mountPanel(ownerA);
    await flushUntil(() => text().includes("已触发"));
    api.watches.set(ownerA, [watch({ id: "w-a1", repeat: true, status: "armed" })]);
    act(() => mounted?.root.render(createElement(WatchPanel, { api, owner: ownerA, key: "v2" })));
    await flushUntil(() => text().includes("等待中"));
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
    // Two visible rows, one logical notification.
    expect(mounted!.container.querySelectorAll("[data-delivery='d-1']").length).toBe(2);
    expect(text()).toContain("未读 1");
    expect(text()).not.toContain("未读 2");
  });
});

describe("WatchPanel state evidence", () => {
  it("shows an evidence-less disconnected target as unknown, never as gone", async () => {
    api.watches.set(ownerA, [watch({ id: "w-a1" })]);
    api.targets.set(ownerA, [target({ targetId: "target-1", state: "disconnected" })]);
    mountPanel(ownerA);
    await flushUntil(() => text().includes("已断连"));
    expect(text()).toContain("无证据");
    expect(text()).not.toContain("已消失");
  });

  it("shows a gone target with its evidence", async () => {
    api.watches.set(ownerA, [watch({ id: "w-a1" })]);
    api.targets.set(ownerA, [target({ targetId: "target-1", state: "gone", evidence: "session exited" })]);
    mountPanel(ownerA);
    await flushUntil(() => text().includes("已消失"));
    expect(text()).toContain("session exited");
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
