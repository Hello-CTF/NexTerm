/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { clickButton, flush, flushUntil as flushUntilBase, mount, type MountedView } from "./features/reactTestUtils";
import type { AuditTimelineItem, AuditTimelinePage } from "../ipc/auditTimelineApi";
import { AuditTimelineView } from "../features/settings/AuditTimelineView";

async function flushUntil(predicate: () => boolean, maxTicks = 60): Promise<void> {
  await flush();
  await flushUntilBase(predicate, maxTicks);
  await flush();
}

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    page: vi.fn(),
    assetList: vi.fn(),
    pickSavePath: vi.fn(),
    save: vi.fn(),
    saveBrowserBlob: vi.fn(),
  };
});

vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return { ...actual, assetApi: { ...actual.assetApi, list: mocks.assetList } };
});
vi.mock("../ipc/auditTimelineApi", () => ({ auditTimelineApi: { page: mocks.page } }));
vi.mock("../ipc/logTailApi", () => ({ logTailApi: { save: mocks.save } }));
vi.mock("../ipc/webFiles", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/webFiles")>();
  return { ...actual, saveBrowserBlob: mocks.saveBrowserBlob };
});
vi.mock("../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ui/dialogs")>();
  return { ...actual, pickSavePath: mocks.pickSavePath };
});

const ROW: AuditTimelineItem = {
  stream: "command",
  id: 1,
  ts: 1700000001000,
  origin: "user",
  source: "user",
  kind: "command",
  assetId: null,
  sessionId: null,
  command: "uniq-desktop-cmd",
  payload: {},
  exitCode: 0,
  durationMs: 5,
};
const PAGE: AuditTimelinePage = { items: [ROW], nextCursor: null };

let mounted: MountedView | undefined;
const text = () => mounted!.container.textContent ?? "";

async function openView() {
  mounted = mount(createElement(AuditTimelineView, { onShowAudit: () => {}, onShowCommand: () => {} }));
  await flushUntil(() => mocks.page.mock.calls.length > 0 && text().includes("uniq-desktop-cmd"));
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.assetList.mockResolvedValue([]);
  mocks.page.mockResolvedValue(PAGE);
  mocks.save.mockResolvedValue(undefined);
});

afterEach(async () => {
  await flush();
  mounted?.unmount();
  mounted = undefined;
  await flush();
});

describe("AuditTimelineView desktop export", () => {
  it("describes the local scope on desktop", async () => {
    await openView();
    expect(text()).toContain("显示本机全部记录");
  });

  it("saves the CSV to the picked path", async () => {
    mocks.pickSavePath.mockResolvedValue("/tmp/out.csv");
    await openView();
    clickButton(mounted!.container, "导出 CSV");
    await flushUntil(() => mocks.save.mock.calls.length > 0);
    const [path, content] = mocks.save.mock.calls[0] as [string, string];
    expect(path).toBe("/tmp/out.csv");
    expect(content).toContain("uniq-desktop-cmd");
    expect(mocks.saveBrowserBlob).not.toHaveBeenCalled();
    expect(mocks.pickSavePath.mock.calls[0][0]).toMatch(/\.csv$/);
  });

  it("does nothing visible when the save dialog is cancelled", async () => {
    mocks.pickSavePath.mockResolvedValue(null);
    await openView();
    clickButton(mounted!.container, "导出 CSV");
    await flushUntil(() => mocks.pickSavePath.mock.calls.length > 0);
    await flush();
    expect(mocks.save).not.toHaveBeenCalled();
    expect(text()).not.toContain("导出失败");
    expect(text()).not.toContain("已导出");
  });
});
