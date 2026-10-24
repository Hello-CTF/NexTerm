/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { clickButton, flush, flushUntil as flushUntilBase, mount, type MountedView } from "./features/reactTestUtils";

async function flushUntil(predicate: () => boolean, maxTicks = 60): Promise<void> {
  await flush();
  await flushUntilBase(predicate, maxTicks);
  await flush();
}
import { AuditView } from "../features/settings/AuditView";
import { useAuth } from "../features/auth/store";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return { auditQuery: vi.fn(), auditCount: vi.fn(), commandQuery: vi.fn(), commandCount: vi.fn(), assetList: vi.fn() };
});

vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      auditQuery: mocks.auditQuery,
      auditCount: mocks.auditCount,
      commandQuery: mocks.commandQuery,
      commandCount: mocks.commandCount,
      list: mocks.assetList,
    },
  };
});

let mounted: MountedView | undefined;
let rectSpy: ReturnType<typeof vi.spyOn> | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useAuth.setState({ user: null });
  mocks.assetList.mockResolvedValue([]);
  mocks.auditCount.mockResolvedValue({ total: 1 });
  mocks.auditQuery.mockResolvedValue([
    { id: 1, ts: 1700000000000, source: "user", kind: "session.open", payload: { host: "web-01" }, exitCode: 2, durationMs: 15 },
  ]);
  mocks.commandCount.mockResolvedValue({ total: 1 });
  mocks.commandQuery.mockResolvedValue([
    { id: 1, sessionId: "s-1", tabId: "t-1", assetId: "a-1", userId: null, command: "ls -la /var/log", source: "terminal", exitCode: 0, startedAt: 1, finishedAt: 2 },
  ]);
});

afterEach(async () => {
  await flush();
  mounted?.unmount();
  mounted = undefined;
  rectSpy?.mockRestore();
  rectSpy = undefined;
  await flush();
});

function narrow(): void {
  rectSpy = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
    x: 0, y: 0, top: 0, left: 0, right: 390, bottom: 600, width: 390, height: 600, toJSON: () => ({}),
  } as DOMRect);
}

describe("audit and command logs on a narrow pane", () => {
  it("lists audit entries as cards that keep action, exit code and duration, without a table", async () => {
    narrow();
    mounted = mount(createElement(AuditView));
    await flushUntil(() => (mounted!.container.querySelector('[data-testid="audit-cards"]')?.textContent ?? "").includes("✗ 2"));
    expect(mounted.container.querySelector("table")).toBeNull();
    const card = mounted.container.querySelector("li")!;
    expect(card.textContent).toContain("✗ 2");
    expect(card.textContent).toContain("15ms");
    expect(card.textContent).toContain("web-01");
  });

  it("lists commands as cards with the full command text reachable", async () => {
    narrow();
    mounted = mount(createElement(AuditView));
    await flushUntil(() => mocks.auditQuery.mock.calls.length > 0);
    await flush();
    clickButton(mounted.container, "命令");
    await flushUntil(() => (mounted!.container.querySelector('[data-testid="command-cards"]')?.textContent ?? "").includes("ls -la"));
    expect(mounted.container.querySelector("table")).toBeNull();
    const cmd = mounted.container.querySelector('[data-testid="command-cards"] [title="ls -la /var/log"]');
    expect(cmd?.textContent).toBe("ls -la /var/log");
  });

  it("keeps the table when the pane is wide", async () => {
    mounted = mount(createElement(AuditView));
    await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length === 1);
    expect(mounted.container.querySelector('[data-testid="audit-cards"]')).toBeNull();
  });
});
