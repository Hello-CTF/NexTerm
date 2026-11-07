/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { chooseSelectOption, click, clickButton, flushUntil, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  retention: vi.fn(),
  setRetention: vi.fn(),
  usage: vi.fn(),
  purge: vi.fn(),
  ask: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  transcriptApi: { retention: mocks.retention, setRetention: mocks.setRetention, usage: mocks.usage, purge: mocks.purge },
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));
vi.mock("../../app/store", () => {
  const state = { pushToast: mocks.toast };
  const useUi = (selector: (value: typeof state) => unknown) => selector(state);
  return { useUi };
});

import { TranscriptCleanupCard } from "../../features/settings/TranscriptCleanupCard";

const DAY = 24 * 60 * 60 * 1000;
const retention = (extra: Record<string, unknown> = {}) => ({
  enabled: true, maxAgeMs: 30 * DAY, maxCount: 2000, unlimitedCount: false, editable: true, ...extra,
});
const usage = (extra: Record<string, unknown> = {}) => ({
  total: 12, starred: 3, storedBytes: 2048, purgeable: 0, purgeableBytes: 0, ...extra,
});

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.retention.mockResolvedValue(retention());
  mocks.usage.mockResolvedValue(usage());
  mocks.setRetention.mockResolvedValue(undefined);
  mocks.ask.mockResolvedValue(true);
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

const autoCheckbox = () => mounted!.container.querySelector<HTMLInputElement>('input[aria-label="自动清理"]')!;

describe("TranscriptCleanupCard", () => {
  it("shows usage and saves a changed automatic cleanup setting", async () => {
    mounted = mount(createElement(TranscriptCleanupCard));
    await flushUntil(() => mounted!.container.querySelector('[data-testid="transcript-usage"]') !== null);
    expect(mounted.container.querySelector('[data-testid="transcript-usage"]')?.textContent).toContain("12 条");
    expect(mounted.container.querySelector('[data-testid="transcript-usage"]')?.textContent).toContain("3 条星标");

    chooseSelectOption(mounted.container, "保留时长", "3 个月");
    await waitFor(() => expect(mocks.setRetention).toHaveBeenLastCalledWith({ enabled: true, maxAgeMs: 90 * DAY, maxCount: 2000, unlimitedCount: false }));

    chooseSelectOption(mounted.container, "最多保留", "不限数量");
    await waitFor(() => expect(mocks.setRetention).toHaveBeenLastCalledWith(expect.objectContaining({ unlimitedCount: true })));

    click(autoCheckbox());
    await waitFor(() => expect(mocks.setRetention).toHaveBeenLastCalledWith(expect.objectContaining({ enabled: false })));
  });

  it("restores the previous setting when saving fails", async () => {
    mocks.setRetention.mockRejectedValue(new Error("disk full"));
    mounted = mount(createElement(TranscriptCleanupCard));
    await flushUntil(() => autoCheckbox() !== null && !autoCheckbox().disabled);
    click(autoCheckbox());
    await waitFor(() => expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("disk full")));
    await waitFor(() => expect(autoCheckbox().checked).toBe(true));
  });

  it("cannot change the server-wide policy on the multi-user server", async () => {
    mocks.retention.mockResolvedValue(retention({ editable: false }));
    mounted = mount(createElement(TranscriptCleanupCard));
    await flushUntil(() => mounted!.container.textContent?.includes("由服务端统一设置") === true);
    expect(autoCheckbox().disabled).toBe(true);
  });

  it("previews the purge, asks, deletes and reports the count", async () => {
    mocks.usage.mockImplementation(async (endedBefore: number) =>
      endedBefore > Date.now() - DAY ? usage({ purgeable: 5, purgeableBytes: 4096 }) : usage(),
    );
    mocks.purge.mockResolvedValue({ deleted: 5 });
    mounted = mount(createElement(TranscriptCleanupCard));
    await flushUntil(() => mounted!.container.querySelector('[data-testid="transcript-usage"]') !== null);

    chooseSelectOption(mounted.container, "清理范围", "全部");
    clickButton(mounted.container, "清理…");

    await waitFor(() => expect(mocks.ask).toHaveBeenCalledWith(expect.stringContaining("将删除 5 条会话记录"), expect.anything()));
    await waitFor(() => expect(mocks.purge).toHaveBeenCalledTimes(1));
    expect(mocks.purge.mock.calls[0]![0]).toBeGreaterThanOrEqual(Date.now() - 1000);
    await waitFor(() => expect(mocks.toast).toHaveBeenCalledWith("success", "已清理 5 条会话记录"));
  });

  it("does not purge when nothing matches or the confirmation is declined", async () => {
    mounted = mount(createElement(TranscriptCleanupCard));
    await flushUntil(() => mounted!.container.querySelector('[data-testid="transcript-usage"]') !== null);
    clickButton(mounted.container, "清理…");
    await waitFor(() => expect(mocks.toast).toHaveBeenCalledWith("info", "没有可清理的会话记录"));
    expect(mocks.ask).not.toHaveBeenCalled();

    mocks.usage.mockResolvedValue(usage({ purgeable: 2, purgeableBytes: 10 }));
    mocks.ask.mockResolvedValue(false);
    clickButton(mounted.container, "清理…");
    await waitFor(() => expect(mocks.ask).toHaveBeenCalledTimes(1));
    expect(mocks.purge).not.toHaveBeenCalled();
  });
});
