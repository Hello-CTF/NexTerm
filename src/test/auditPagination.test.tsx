/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { clickButton, deferred, flush, flushUntil as flushUntilBase, chooseSelectOption, mount, type MountedView } from "./features/reactTestUtils";

async function flushUntil(predicate: () => boolean, maxTicks = 60): Promise<void> {
  await flush();
  await flushUntilBase(predicate, maxTicks);
  await flush();
}
import type { AuditEntryDto } from "../ipc/types";
import { AuditView } from "../features/settings/AuditView";
import { useAuth } from "../features/auth/store";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    auditQuery: vi.fn(),
    auditCount: vi.fn(),
    fleetDevices: vi.fn(),
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

vi.mock("../ipc/fleetApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/fleetApi")>();
  return {
    ...actual,
    fleetApi: { ...actual.fleetApi, devices: mocks.fleetDevices },
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

function moreButton(container: ParentNode): HTMLButtonElement | undefined {
  return [...container.querySelectorAll<HTMLButtonElement>("button")].find(
    (b) => b.textContent?.trim() === "加载更多",
  );
}

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.fleetDevices.mockResolvedValue({ devices: [] });
});

afterEach(async () => {
  await flush();
  mounted?.unmount();
  mounted = undefined;
  await flush();
  setViewportWidth(1024);
});

describe("AuditView pagination", () => {
  it("does not label the tool's UI ownership as global", async () => {
    mocks.auditCount.mockResolvedValue({ total: 0 });
    mocks.auditQuery.mockResolvedValue([]);
    mounted = mount(createElement(AuditView));
    await flush();
    expect(mounted.container.querySelector(".nx-toolbar")?.textContent).toContain("审计日志");
    expect(
      mounted.container.querySelector('.nx-toolbar span[title="全局面板：内容不随工作区切换"]'),
    ).toBeNull();
  });

  it("loads the first page and appends by offset until all rows are loaded", async () => {
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

  it("deduplicates overlapping pages by ID without rendering loaded rows twice", async () => {
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

  it("keeps loaded rows after load-more fails and resumes appending after retry", async () => {
    mocks.auditCount.mockResolvedValue({ total: 250 });
    mocks.auditQuery
      .mockResolvedValueOnce(makeRows(100, 1))
      .mockRejectedValueOnce(new Error("Network interrupted"))
      .mockResolvedValueOnce(makeRows(100, 101));
    mounted = mount(createElement(AuditView));
    await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length === 100);

    clickButton(mounted!.container, "加载更多");
    await flushUntil(() => text(mounted!.container).includes("未能加载更多：Network interrupted"));
    expect(mounted!.container.querySelectorAll("tbody tr").length).toBe(100);

    clickButton(mounted!.container, "重试");
    await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length === 200);
    expect(text(mounted!.container)).toContain("已显示 200 / 250 条");
    expect(mocks.auditQuery).toHaveBeenLastCalledWith({ source: undefined, limit: 100, offset: 100 });
  });

  it("refresh during load-more does not deadlock pagination and continues with the new offset after the stale response settles", async () => {
    mocks.auditCount.mockResolvedValue({ total: 250 });
    const stale = deferred<AuditEntryDto[]>();
    mocks.auditQuery
      .mockResolvedValueOnce(makeRows(100, 1))
      .mockReturnValueOnce(stale.promise)
      .mockResolvedValueOnce(makeRows(100, 500))
      .mockResolvedValueOnce(makeRows(100, 600));
    mounted = mount(createElement(AuditView));
    await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length === 100);

    clickButton(mounted!.container, "加载更多");
    await flush();
    clickButton(mounted!.container, "刷新");
    await flushUntil(() => rowKinds(mounted!.container)[0] === "k500");

    stale.resolve(makeRows(100, 900));
    await flush();

    const kinds = rowKinds(mounted!.container);
    expect(kinds).toHaveLength(100);
    expect(kinds.some((k) => k.startsWith("k9"))).toBe(false);
    const button = moreButton(mounted!.container);
    expect(button).toBeTruthy();
    expect(button!.disabled).toBe(false);

    clickButton(mounted!.container, "加载更多");
    await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length === 200);
    expect(mocks.auditQuery).toHaveBeenLastCalledWith({ source: undefined, limit: 100, offset: 100 });
    expect(rowKinds(mounted!.container)[199]).toBe("k699");
  });

  it("changing filters during load-more does not deadlock pagination or mix stale responses into the new filter", async () => {
    mocks.auditCount.mockResolvedValue({ total: 250 });
    const stale = deferred<AuditEntryDto[]>();
    mocks.auditQuery
      .mockResolvedValueOnce(makeRows(100, 1))
      .mockReturnValueOnce(stale.promise)
      .mockResolvedValueOnce(makeRows(100, 1001, "ai"))
      .mockResolvedValueOnce(makeRows(100, 1101, "ai"));
    mounted = mount(createElement(AuditView));
    await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length === 100);

    clickButton(mounted!.container, "加载更多");
    await flush();
    clickButton(mounted!.container, "AI");
    await flushUntil(() => rowKinds(mounted!.container)[0] === "k1001");

    stale.resolve(makeRows(100, 2001));
    await flush();

    const kinds = rowKinds(mounted!.container);
    expect(kinds).toHaveLength(100);
    expect(kinds.some((k) => k.startsWith("k2"))).toBe(false);
    const button = moreButton(mounted!.container);
    expect(button).toBeTruthy();
    expect(button!.disabled).toBe(false);

    clickButton(mounted!.container, "加载更多");
    await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length === 200);
    expect(mocks.auditQuery).toHaveBeenLastCalledWith({ source: "ai", limit: 100, offset: 100 });
    expect(rowKinds(mounted!.container)[199]).toBe("k1200");
  });
});

describe("AuditView counts and filters", () => {
  it("gets totals from audit_count, follows filter changes, and resets pagination", async () => {
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

  it("shows an exact total after audit_count fails when a short page proves all rows are loaded", async () => {
    mocks.auditCount.mockRejectedValue(new Error("count unavailable"));
    mocks.auditQuery.mockResolvedValue(makeRows(3, 1));
    mounted = mount(createElement(AuditView));
    await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length === 3);

    expect(text(mounted!.container)).toContain("共 3 条");
    expect(text(mounted!.container)).toContain("已显示 3 / 3 条");
  });

  it("shows the loaded count after audit_count fails on a full page, then switches to an exact total when loading completes", async () => {
    mocks.auditCount.mockRejectedValue(new Error("count unavailable"));
    mocks.auditQuery
      .mockResolvedValueOnce(makeRows(100, 1))
      .mockResolvedValueOnce(makeRows(50, 101));
    mounted = mount(createElement(AuditView));
    await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length === 100);

    expect(text(mounted!.container)).toContain("已加载 100 条");
    expect(text(mounted!.container)).not.toContain("共 100 条");
    expect(text(mounted!.container)).toContain("已显示 100 条");

    clickButton(mounted!.container, "加载更多");
    await flushUntil(() => text(mounted!.container).includes("已加载全部"));
    expect(text(mounted!.container)).toContain("共 150 条");
    expect(text(mounted!.container)).toContain("已显示 150 / 150 条");
    expect(mounted!.container.querySelectorAll("tbody tr").length).toBe(150);
  });
});

describe("AuditView loading states", () => {
  it("shows skeleton rows during initial loading and replaces them immediately without flashing an empty state", async () => {
    const first = deferred<AuditEntryDto[]>();
    mocks.auditCount.mockResolvedValue({ total: 3 });
    mocks.auditQuery.mockReturnValue(first.promise);
    mounted = mount(createElement(AuditView));

    await flushUntil(() => mounted!.container.querySelectorAll('tbody tr[aria-hidden="true"]').length > 0);
    expect(mounted!.container.querySelectorAll('tbody tr[aria-hidden="true"]').length).toBeGreaterThan(0);
    expect(text(mounted!.container)).not.toContain("暂无记录");

    first.resolve(makeRows(3, 1));
    await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length === 3);
    expect(mounted!.container.querySelector('tbody tr[aria-hidden="true"]')).toBeNull();
    expect(text(mounted!.container)).not.toContain("暂无记录");
  });

  it("reloads from the first page and refreshes the total from the refresh button", async () => {
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

describe("AuditView responsive layout", () => {
  it("shows real pending skeletons at 320/390/desktop, wraps pagination, and shrinks/wraps long error text", async () => {
    for (const width of [320, 390, 1280]) {
      setViewportWidth(width);
      mocks.auditQuery.mockReset();
      mocks.auditCount.mockReset();
      const first = deferred<AuditEntryDto[]>();
      mocks.auditCount.mockResolvedValue({ total: 250 });
      mocks.auditQuery.mockReturnValue(first.promise);
      mounted = mount(createElement(AuditView));

      await flushUntil(
        () => mounted!.container.querySelectorAll('tbody tr[aria-hidden="true"]').length > 0,
      );
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

      const longError = `E${"x".repeat(200)}`;
      mocks.auditQuery.mockReset();
      mocks.auditQuery.mockRejectedValueOnce(new Error(longError));
      clickButton(mounted!.container, "加载更多");
      await flushUntil(() => text(mounted!.container).includes("未能加载更多"));
      const errorSpan = [...mounted!.container.querySelectorAll("span")].find((s) =>
        s.textContent?.includes("未能加载更多"),
      );
      expect(errorSpan!.className).toContain("min-w-0");
      expect(errorSpan!.className).toContain("break-words");
      expect(errorSpan!.textContent).toContain(longError);

      mounted.unmount();
      mounted = undefined;
    }
  });
});

describe("AuditView device timeline", () => {
  const DEVICE = { id: "d-1", name: "web-01", kind: "agent", created_at: 1, last_seen_at: 1, revoked_at: 0 };

  function seedAdmin() {
    useAuth.setState({
      status: { initialized: true, registration_open: false, auth: "on" },
      user: {
        id: "u-1",
        username: "root",
        display_name: "Root",
        role: "superadmin" as const,
        state: "active" as const,
        must_change_password: false,
        mfa_enabled: false,
        created_at: 1,
        updated_at: 1,
        last_login_at: 1,
      },
      dek: null,
      gate: "ready",
      pendingRecoveryKey: null,
      error: null,
    });
  }

  afterEach(() => {
    act(() => useAuth.setState({ user: null }));
  });

  it("filters by assetId after selecting a device, renders Chinese event labels, and disables source filters", async () => {
    mocks.fleetDevices.mockResolvedValue({ devices: [DEVICE] });
    mocks.auditCount.mockResolvedValue({ total: 2 });
    mocks.auditQuery.mockResolvedValue([
      { id: 2, ts: 2000, sessionId: null, assetId: "d-1", source: "fleet", kind: "device_online", payload: {}, exitCode: null, durationMs: null },
      { id: 1, ts: 1000, sessionId: null, assetId: "d-1", source: "fleet", kind: "device_enroll", payload: {}, exitCode: null, durationMs: null },
    ] satisfies AuditEntryDto[]);
    seedAdmin();
    mounted = mount(createElement(AuditView));

    await flushUntil(() => mounted!.container.querySelector('button[aria-label="按设备过滤"]') !== null);
    // With no device selected, the source filter is kept.
    expect(mocks.auditQuery).toHaveBeenCalledWith({ source: undefined, limit: 100, offset: 0 });

    chooseSelectOption(mounted!.container, "按设备过滤", "web-01");
    await flushUntil(() => {
      const body = text(mounted!.container);
      const refresh = [...mounted!.container.querySelectorAll<HTMLButtonElement>("button")].find(
        (button) => button.textContent?.trim() === "刷新",
      );
      return body.includes("设备上线") && body.includes("共 2 条") && refresh?.disabled === false;
    });
    expect(mocks.auditQuery).toHaveBeenLastCalledWith({ assetId: "d-1", limit: 100, offset: 0 });

    const body = text(mounted!.container);
    expect(body).toContain("设备上线");
    expect(body).toContain("设备接入");
    const badges = [...mounted!.container.querySelectorAll("tbody .nx-badge")].map((b) => b.textContent);
    expect(badges).toContain("设备");
    // During device filtering the source filter is disabled (avoiding an empty set from the "source + device" combination).
    const userSegment = [...mounted!.container.querySelectorAll<HTMLButtonElement>(".nx-segment-item")].find(
      (b) => b.textContent?.trim() === "用户",
    );
    expect(userSegment?.disabled).toBe(true);
  });

  it("does not request devices or show the device filter for logged-out WEB users", async () => {
    mocks.auditCount.mockResolvedValue({ total: 0 });
    mocks.auditQuery.mockResolvedValue([]);
    useAuth.setState({ user: null, gate: "ready", status: { initialized: true, registration_open: false, auth: "on" } });
    mounted = mount(createElement(AuditView));
    await flushUntil(() => mocks.auditQuery.mock.calls.length > 0);

    expect(mocks.fleetDevices).not.toHaveBeenCalled();
    expect(mounted!.container.querySelector('select[aria-label="按设备过滤"]')).toBeNull();
  });
});
