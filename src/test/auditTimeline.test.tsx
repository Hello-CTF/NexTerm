/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { chooseSelectOption, clickButton, flush, flushUntil as flushUntilBase, mount, type MountedView } from "./features/reactTestUtils";
import type { AuditTimelineItem, AuditTimelinePage } from "../ipc/auditTimelineApi";
import { AuditView } from "../features/settings/AuditView";
import { useAuth } from "../features/auth/store";

async function flushUntil(predicate: () => boolean, maxTicks = 60): Promise<void> {
  await flush();
  await flushUntilBase(predicate, maxTicks);
  await flush();
}

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    page: vi.fn(),
    auditQuery: vi.fn(),
    auditCount: vi.fn(),
    assetList: vi.fn(),
    saveBrowserBlob: vi.fn(),
    pushToast: vi.fn(),
  };
});

vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    assetApi: { auditQuery: mocks.auditQuery, auditCount: mocks.auditCount, list: mocks.assetList },
  };
});
vi.mock("../ipc/fleetApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/fleetApi")>();
  return { ...actual, fleetApi: { ...actual.fleetApi, devices: vi.fn().mockResolvedValue({ devices: [] }) } };
});
vi.mock("../ipc/auditTimelineApi", () => ({ auditTimelineApi: { page: mocks.page } }));
vi.mock("../ipc/webFiles", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/webFiles")>();
  return { ...actual, saveBrowserBlob: mocks.saveBrowserBlob };
});

function item(stream: AuditTimelineItem["stream"], id: number, over: Partial<AuditTimelineItem> = {}): AuditTimelineItem {
  return {
    stream,
    id,
    ts: 1700000000000 + id * 1000,
    origin: stream === "device" ? "device" : "user",
    source: "user",
    kind: stream === "command" ? "command" : stream === "device" ? "device_online" : "file_write",
    assetId: "a-1",
    sessionId: null,
    command: stream === "command" ? `cmd-${id}` : null,
    payload: {},
    exitCode: null,
    durationMs: null,
    ...over,
  };
}

const FIRST: AuditTimelinePage = {
  items: [item("audit", 1), item("command", 2, { exitCode: 0, durationMs: 5 }), item("device", 3)],
  nextCursor: { ts: 1700000003000, stream: "device", id: 3 },
};

let mounted: MountedView | undefined;
const rows = () => [...mounted!.container.querySelectorAll("tbody tr")];
const text = () => mounted!.container.textContent ?? "";

async function openTimeline() {
  mounted = mount(createElement(AuditView));
  await flush();
  clickButton(mounted.container, "时间线");
  await flushUntil(() => mocks.page.mock.calls.length > 0 && rows().length > 0);
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.auditCount.mockResolvedValue({ total: 0 });
  mocks.auditQuery.mockResolvedValue([]);
  mocks.assetList.mockResolvedValue([{ id: "a-1", name: "web-01" }]);
  mocks.page.mockResolvedValue(FIRST);
  useAuth.setState({ user: null });
});

afterEach(async () => {
  await flush();
  mounted?.unmount();
  mounted = undefined;
  await flush();
});

describe("AuditTimelineView", () => {
  it("opens from the audit tab and renders all three streams", async () => {
    await openTimeline();
    expect(mocks.page).toHaveBeenCalledWith({ limit: 100 });
    expect(rows()).toHaveLength(3);
    expect(text()).toContain("cmd-2");
    expect(text()).toContain("设备上线");
    expect(text()).toContain("web-01");
  });

  it("passes nextCursor back when loading more", async () => {
    await openTimeline();
    mocks.page.mockResolvedValueOnce({ items: [item("command", 4)], nextCursor: null });
    clickButton(mounted!.container, "加载更多");
    await flushUntil(() => rows().length === 4);
    expect(mocks.page).toHaveBeenLastCalledWith({ limit: 100, cursor: FIRST.nextCursor });
    expect(text()).toContain("已加载全部");
  });

  it("resets the list and sends origins when the source filter changes", async () => {
    await openTimeline();
    mocks.page.mockResolvedValueOnce({ items: [item("audit", 9, { origin: "ai" })], nextCursor: null });
    clickButton(mounted!.container, "AI");
    await flushUntil(() => rows().length === 1);
    expect(mocks.page).toHaveBeenLastCalledWith({ origins: ["ai"], limit: 100 });
  });

  it("filters by host with assetId", async () => {
    await openTimeline();
    chooseSelectOption(mounted!.container, "按主机过滤", "web-01");
    await flushUntil(() => mocks.page.mock.calls.some(([r]) => (r as { assetId?: string }).assetId === "a-1"));
    expect(mocks.page).toHaveBeenLastCalledWith({ assetId: "a-1", limit: 100 });
  });

  it("exports CSV through saveBrowserBlob and warns visibly when truncated", async () => {
    mocks.saveBrowserBlob.mockResolvedValue(true);
    const big = (start: number): AuditTimelinePage => ({
      items: Array.from({ length: 500 }, (_, i) => item("command", start + i)),
      nextCursor: { ts: 1, stream: "command", id: start + 500 },
    });
    mocks.page.mockImplementation(async (r: { cursor?: { id: number }; limit: number }) =>
      r.limit === 500 ? big(r.cursor?.id ?? 1) : FIRST,
    );
    await openTimeline();
    clickButton(mounted!.container, "导出 CSV");
    await flushUntil(() => mocks.saveBrowserBlob.mock.calls.length > 0 && text().includes("仅导出最新 10000 条"));
    const [name, blob] = mocks.saveBrowserBlob.mock.calls[0] as [string, Blob];
    expect(name).toMatch(/^nexterm-audit-timeline-.+\.csv$/);
    expect(blob.size).toBeGreaterThan(0);
    expect(text()).toContain("请缩小时间范围或筛选条件");
  });

  it("keeps the relative time window fixed while loading more pages", async () => {
    const now = vi.spyOn(Date, "now").mockReturnValue(1_800_000_000_000);
    try {
      await openTimeline();
      clickButton(mounted!.container, "24 小时");
      await flushUntil(() => mocks.page.mock.calls.some(([r]) => (r as { from?: number }).from !== undefined));
      const first = mocks.page.mock.calls.at(-1)![0] as { from: number };
      await flushUntil(() => rows().length > 0);
      now.mockReturnValue(1_800_000_000_000 + 3_600_000);
      mocks.page.mockResolvedValueOnce({ items: [item("command", 4)], nextCursor: null });
      clickButton(mounted!.container, "加载更多");
      await flushUntil(() => rows().length === 4);
      const second = mocks.page.mock.calls.at(-1)![0] as { from: number; cursor?: unknown };
      expect(second.from).toBe(first.from);
      expect(second.cursor).toEqual(FIRST.nextCursor);
    } finally {
      now.mockRestore();
    }
  });

  it("explains that AI-initiated terminal commands show as user", async () => {
    await openTimeline();
    expect(text()).toContain("命令日志没有 AI 来源标记，AI 发起的终端命令显示为用户");
  });

  it("warns that exports may contain sensitive command text", async () => {
    await openTimeline();
    expect(text()).toContain("导出文件可能包含命令行中的敏感内容");
  });

  it("describes the record scope by mode and login state", async () => {
    await openTimeline();
    expect(text()).toContain("显示本服务端的全部记录");
    expect(text()).toContain("时间线合并审计、命令和设备上下线记录");
    await act(async () => {
      useAuth.setState({ user: { id: "u1", username: "alice" } as never });
    });
    await flush();
    expect(text()).toContain("只显示你账户下的记录");
    expect(text()).not.toContain("显示本服务端的全部记录");
  });

  it("keeps the audit tab working", async () => {
    mounted = mount(createElement(AuditView));
    await flushUntil(() => mocks.auditQuery.mock.calls.length > 0);
    expect(mocks.auditQuery).toHaveBeenCalledWith({ source: undefined, limit: 100, offset: 0 });
    expect(mocks.page).not.toHaveBeenCalled();
  });
});
