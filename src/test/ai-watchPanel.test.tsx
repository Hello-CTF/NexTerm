/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, deferred, flush, flushUntil, mount, type MountedView } from "./features/reactTestUtils";
import type { WatchApi, WatchSummary, WatchTargetSummary } from "../features/ai/watchTypes";
import { WatchPanel } from "../features/ai/WatchPanel";

const owner = "owner-a";

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

class SyntheticApi implements WatchApi {
  watches: WatchSummary[] = [];
  targets: WatchTargetSummary[] = [{ targetId: "target-1", identity: "dev-1", state: "attached" }];
  cancelCalls: Array<{ watchId: string; expectedRevision: number }> = [];
  cancelImpl: () => Promise<void> = () => Promise.resolve();
  listCalls = 0;

  listWatches(): Promise<WatchSummary[]> {
    this.listCalls += 1;
    return Promise.resolve(this.watches.map((w) => ({ ...w })));
  }
  listTargets(): Promise<WatchTargetSummary[]> {
    return Promise.resolve(this.targets);
  }
  cancelWatch(_owner: string, watchId: string, expectedRevision: number): Promise<void> {
    this.cancelCalls.push({ watchId, expectedRevision });
    return this.cancelImpl();
  }
  subscribeWatchToolData(): () => void {
    return () => undefined;
  }
}

let view: MountedView | null = null;

afterEach(() => {
  vi.useRealTimers();
  view?.unmount();
  view = null;
});

function text(): string {
  return view!.container.textContent ?? "";
}

function button(label: string): HTMLButtonElement {
  const found = view!.container.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`);
  if (!found) throw new Error(`button not found: ${label}`);
  return found;
}

describe("WatchPanel", () => {
  it("explains what a watch is in plain Chinese and never shows raw owner ids or English titles", async () => {
    const api = new SyntheticApi();
    api.watches = [watch({ id: "w-1", name: "等待构建" })];
    view = mount(createElement(WatchPanel, { api, owner }));
    await flushUntil(() => text().includes("等待构建"));
    expect(text()).toContain("监视：AI 持续观察指定终端");
    expect(text()).toContain("终端 dev-1出现「ready」时通知 AI");
    expect(text()).toContain("等待中");
    expect(text()).not.toContain("Watch");
    expect(text()).not.toContain(owner);
    expect(text()).not.toContain("锚点");
  });

  it("shows an empty state when nothing is armed", async () => {
    const api = new SyntheticApi();
    view = mount(createElement(WatchPanel, { api, owner }));
    await flushUntil(() => text().includes("暂无进行中的监视"));
  });

  it("shows a spinner on refresh and ignores the response of a superseded load", async () => {
    const api = new SyntheticApi();
    view = mount(createElement(WatchPanel, { api, owner }));
    await flush();
    const gate = deferred<WatchSummary[]>();
    api.listWatches = () => gate.promise;
    click(button("刷新监视列表"));
    await flush();
    expect(button("刷新监视列表").getAttribute("aria-busy")).toBe("true");
    expect(button("刷新监视列表").disabled).toBe(true);
    await act(async () => {
      gate.resolve([]);
    });
    await flush();
    expect(button("刷新监视列表").getAttribute("aria-busy")).toBe("false");
  });

  it("cancels with pressed feedback, blocks double submit, and moves the watch into the collapsed ended section", async () => {
    const api = new SyntheticApi();
    api.watches = [watch({ id: "w-1", name: "等待构建", revision: 7 })];
    const gate = deferred<void>();
    api.cancelImpl = () => gate.promise;
    view = mount(createElement(WatchPanel, { api, owner }));
    await flushUntil(() => text().includes("等待构建"));

    click(button("取消监视「等待构建」"));
    await flush();
    const pending = button("取消监视「等待构建」");
    expect(pending.disabled).toBe(true);
    expect(pending.textContent).toContain("正在取消");
    click(pending);
    await flush();
    expect(api.cancelCalls).toEqual([{ watchId: "w-1", expectedRevision: 7 }]);

    api.watches = [watch({ id: "w-1", name: "等待构建", status: "cancelled", revision: 8 })];
    await act(async () => {
      gate.resolve();
    });
    await flush();
    expect(text()).toContain("已取消监视「等待构建」");
    expect(text()).toContain("暂无进行中的监视");
    const ended = view!.container.querySelector("details");
    expect(ended).not.toBeNull();
    expect(ended!.open).toBe(false);
    expect(ended!.textContent).toContain("已结束（1）");
    expect(view!.container.querySelector('button[aria-label="取消监视「等待构建」"]')).toBeNull();
  });

  it("keeps the watch active and shows a persistent error when cancel fails", async () => {
    const api = new SyntheticApi();
    api.watches = [watch({ id: "w-1", name: "等待构建" })];
    api.cancelImpl = () => Promise.reject(new Error("后端不可用"));
    view = mount(createElement(WatchPanel, { api, owner }));
    await flushUntil(() => text().includes("等待构建"));
    click(button("取消监视「等待构建」"));
    await flushUntil(() => text().includes("未能取消监视：后端不可用"));
    expect(button("取消监视「等待构建」").disabled).toBe(false);
    expect(view!.container.querySelector("details")).toBeNull();
  });

  it("clears the success notice by itself but not while it is hovered", async () => {
    const api = new SyntheticApi();
    api.watches = [watch({ id: "w-1", name: "等待构建" })];
    view = mount(createElement(WatchPanel, { api, owner }));
    await flushUntil(() => text().includes("等待构建"));
    vi.useFakeTimers();
    click(button("取消监视「等待构建」"));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    const notice = view!.container.querySelector('[role="status"]') as HTMLElement;
    expect(notice.textContent).toContain("已取消监视");
    act(() => {
      notice.dispatchEvent(new MouseEvent("mouseover", { bubbles: true }));
      notice.dispatchEvent(new MouseEvent("mouseout", { bubbles: true }));
    });
    // Without a hold the notice clears after the dismiss delay.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(6000);
    });
    expect(view!.container.querySelector('[role="status"]')).toBeNull();
  });

  it("does not reset or remove anything by itself while the user reads", async () => {
    const api = new SyntheticApi();
    api.watches = [watch({ id: "w-1", name: "等待构建" })];
    view = mount(createElement(WatchPanel, { api, owner }));
    await flushUntil(() => text().includes("等待构建"));
    vi.useFakeTimers();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(text()).toContain("等待构建");
    expect(button("取消监视「等待构建」").disabled).toBe(false);
  });
});
