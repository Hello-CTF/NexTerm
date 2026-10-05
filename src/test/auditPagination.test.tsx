/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { clickButton, deferred, flush, flushUntil, mount, type MountedView } from "./features/reactTestUtils";
import type { AuditEntryDto } from "../ipc/types";
import { AuditView } from "../features/settings/AuditView";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    auditQuery: vi.fn(),
    auditCount: vi.fn(),
  };
});

vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      auditQuery: mocks.auditQuery,
      auditCount: mocks.auditCount,
    },
  };
});

function makeRows(count: number, startId: number, source: "user" | "ai" = "user"): AuditEntryDto[] {
  return Array.from({ length: count }, (_, i) => ({
    id: startId + i,
    ts: 1700000000000 + (startId + i) * 1000,
    sessionId: null,
    assetId: null,
    source,
    kind: `k${startId + i}`,
    payload: {},
    exitCode: 0,
    durationMs: 1,
  }));
}

function setViewportWidth(width: number): void {
  Object.defineProperty(window, "innerWidth", { value: width, configurable: true });
}

function rowKinds(container: ParentNode): string[] {
  return [...container.querySelectorAll("tbody tr")].map(
    (row) => row.querySelectorAll("td")[2]?.textContent ?? "",
  );
}

function text(container: ParentNode): string {
  return container.textContent ?? "";
}

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  setViewportWidth(1024);
});

describe("AuditView 分页", () => {
  it("首屏加载一页，加载更多按 offset 追加直到取完", async () => {
    mocks.auditCount.mockResolvedValue({ total: 250 });
    mocks.auditQuery.mockImplementation(({ offset }: { offset: number }) =>
      Promise.resolve(makeRows(offset === 200 ? 50 : 100, offset + 1)),
    );
    mounted = mount(createElement(AuditView));
    await flushUntil(() => text(mounted!.container).includes("共 250 条"));

    expect(mocks.auditQuery).toHaveBeenCalledWith({ source: undefined, limit: 100, offset: 0 });
    expect(mounted!.container.querySelectorAll("tbody tr").length).toBe(100);

    clickButton(mounted!.container, "加载更多");
    await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length === 200);
    expect(mocks.auditQuery).toHaveBeenLastCalledWith({ source: undefined, limit: 100, offset: 100 });
    expect(text(mounted!.container)).toContain("已显示 200 / 250 条");

    clickButton(mounted!.container, "加载更多");
    await flushUntil(() => text(mounted!.container).includes("已加载全部"));
    expect(mocks.auditQuery).toHaveBeenLastCalledWith({ source: undefined, limit: 100, offset: 200 });
    expect(mounted!.container.querySelectorAll("tbody tr").length).toBe(250);
    expect(rowKinds(mounted!.container)[249]).toBe("k250");
  });

  it("重叠页按 id 去重，已加载行不重复渲染", async () => {
    mocks.auditCount.mockResolvedValue({ total: 150 });
    mocks.auditQuery.mockImplementation(({ offset }: { offset: number }) =>
      Promise.resolve(offset === 0 ? makeRows(100, 1) : makeRows(100, 51)),
    );
    mounted = mount(createElement(AuditView));
    await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length === 100);

    clickButton(mounted!.container, "加载更多");
    await flushUntil(() => text(mounted!.container).includes("已加载全部"));

    const kinds = rowKinds(mounted!.container);
    expect(kinds.length).toBe(150);
    expect(kinds.filter((k) => k === "k100")).toHaveLength(1);
    expect(kinds[0]).toBe("k1");
    expect(kinds[100]).toBe("k101");
    expect(kinds[149]).toBe("k150");
    expect(mocks.auditQuery).toHaveBeenLastCalledWith({ source: undefined, limit: 100, offset: 100 });
  });

  it("加载更多失败保留已有行，重试后继续追加", async () => {
    mocks.auditCount.mockResolvedValue({ total: 250 });
    mocks.auditQuery
      .mockResolvedValueOnce(makeRows(100, 1))
      .mockRejectedValueOnce(new Error("网络中断"))
      .mockResolvedValueOnce(makeRows(100, 101));
    mounted = mount(createElement(AuditView));
    await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length === 100);

    clickButton(mounted!.container, "加载更多");
    await flushUntil(() => text(mounted!.container).includes("加载更多失败 · 网络中断"));
    expect(mounted!.container.querySelectorAll("tbody tr").length).toBe(100);

    clickButton(mounted!.container, "重试");
    await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length === 200);
    expect(text(mounted!.container)).toContain("已显示 200 / 250 条");
    expect(mocks.auditQuery).toHaveBeenLastCalledWith({ source: undefined, limit: 100, offset: 100 });
  });
});

describe("AuditView 计数与筛选", () => {
  it("总数来自 audit_count 并跟随筛选切换，切换时重置分页", async () => {
    mocks.auditCount.mockImplementation(({ source }: { source?: string }) =>
      Promise.resolve({ total: source === "ai" ? 7 : 250 }),
    );
    mocks.auditQuery.mockImplementation(({ source, offset }: { source?: string; offset: number }) =>
      source === "ai"
        ? Promise.resolve(makeRows(7, 1000, "ai"))
        : Promise.resolve(makeRows(offset === 0 ? 100 : 100, offset + 1)),
    );
    mounted = mount(createElement(AuditView));
    await flushUntil(() => text(mounted!.container).includes("共 250 条"));

    clickButton(mounted!.container, "加载更多");
    await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length === 200);

    clickButton(mounted!.container, "AI");
    await flushUntil(() => text(mounted!.container).includes("共 7 条"));
    expect(mocks.auditCount).toHaveBeenLastCalledWith({ source: "ai" });
    expect(mocks.auditQuery).toHaveBeenLastCalledWith({ source: "ai", limit: 100, offset: 0 });
    const aiKinds = rowKinds(mounted!.container);
    expect(aiKinds).toHaveLength(7);
    expect(aiKinds[0]).toBe("k1000");
    expect(text(mounted!.container)).toContain("已显示 7 / 7 条");
    expect(text(mounted!.container)).toContain("已加载全部");

    clickButton(mounted!.container, "全部");
    await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length === 100);
    expect(text(mounted!.container)).toContain("共 250 条");
    expect(mocks.auditQuery).toHaveBeenLastCalledWith({ source: undefined, limit: 100, offset: 0 });
    expect(text(mounted!.container)).toContain("已显示 100 / 250 条");
  });

  it("audit_count 失败时降级为已加载条数，行仍正常渲染", async () => {
    mocks.auditCount.mockRejectedValue(new Error("count 不可用"));
    mocks.auditQuery.mockResolvedValue(makeRows(3, 1));
    mounted = mount(createElement(AuditView));
    await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length === 3);

    expect(text(mounted!.container)).toContain("共 3 条");
    expect(text(mounted!.container)).toContain("已显示 3 条");
  });
});

describe("AuditView 加载状态", () => {
  it("首屏加载中显示骨架行，数据到达后立即替换且无空态闪烁", async () => {
    const first = deferred<AuditEntryDto[]>();
    mocks.auditCount.mockResolvedValue({ total: 3 });
    mocks.auditQuery.mockReturnValue(first.promise);
    mounted = mount(createElement(AuditView));

    await flushUntil(() => mounted!.container.querySelectorAll('tbody tr[aria-hidden="true"]').length > 0);
    expect(mounted!.container.querySelectorAll('tbody tr[aria-hidden="true"]').length).toBe(8);
    expect(text(mounted!.container)).not.toContain("暂无记录");

    first.resolve(makeRows(3, 1));
    await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length === 3);
    expect(mounted!.container.querySelector('tbody tr[aria-hidden="true"]')).toBeNull();
    expect(text(mounted!.container)).not.toContain("暂无记录");
  });

  it("刷新按钮重新从第一页加载并刷新总数", async () => {
    mocks.auditCount.mockResolvedValue({ total: 3 });
    mocks.auditQuery.mockResolvedValue(makeRows(3, 1));
    mounted = mount(createElement(AuditView));
    await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length === 3);

    mocks.auditCount.mockResolvedValue({ total: 2 });
    mocks.auditQuery.mockResolvedValue(makeRows(2, 1));
    clickButton(mounted!.container, "刷新");
    await flushUntil(() => text(mounted!.container).includes("共 2 条"));
    expect(mocks.auditQuery).toHaveBeenLastCalledWith({ source: undefined, limit: 100, offset: 0 });
    expect(mounted!.container.querySelectorAll("tbody tr").length).toBe(2);
    await flush();
  });
});

describe("AuditView 响应式", () => {
  it("320/390/桌面下骨架条只用百分比或自动宽度，分页条可换行且文本可截断", async () => {
    for (const width of [320, 390, 1280]) {
      setViewportWidth(width);
      const first = deferred<AuditEntryDto[]>();
      mocks.auditCount.mockResolvedValue({ total: 250 });
      mocks.auditQuery.mockReturnValueOnce(first.promise).mockResolvedValueOnce(makeRows(100, 101));
      mounted = mount(createElement(AuditView));

      await flushUntil(() => mounted!.container.querySelectorAll('tbody tr[aria-hidden="true"]').length > 0);
      for (const bar of mounted!.container.querySelectorAll("tbody .nx-skeleton")) {
        expect(bar.className).not.toMatch(/min-w-\[/);
        expect(bar.className).toMatch(/nx-skeleton-chip|w-\d+\/\d+|ml-auto/);
      }

      first.resolve(makeRows(100, 1));
      await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length === 100);
      const paginationBar = [...mounted!.container.querySelectorAll("div")].find(
        (d) => d.className.includes("flex-wrap") && d.textContent?.includes("已显示"),
      );
      expect(paginationBar).toBeTruthy();
      expect(paginationBar!.textContent).toContain("加载更多");
      const status = paginationBar!.querySelector("span");
      expect(status!.className).toContain("min-w-0");
      expect(status!.className).toContain("truncate");

      mounted.unmount();
      mounted = undefined;
    }
  });
});
