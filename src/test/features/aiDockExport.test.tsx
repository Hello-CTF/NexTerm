/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, flush, flushUntil, mount, waitForRendered, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  getPermission: vi.fn(),
  conversationList: vi.fn(),
  conversationExport: vi.fn(),
  messages: vi.fn(),
  runs: vi.fn(),
  overview: vi.fn(),
  pickSavePath: vi.fn(),
  finishSave: vi.fn(),
  toast: vi.fn(),
}));
vi.mock("../../ipc/commands", () => ({
  aiApi: {
    chat: vi.fn(),
    cancel: vi.fn(),
    confirm: vi.fn(),
    answer: vi.fn(),
    hitlSnapshot: vi.fn(),
    hitlEvents: vi.fn(),
    getPermission: mocks.getPermission,
    conversationList: mocks.conversationList,
    conversationDelete: vi.fn(),
    conversationExport: mocks.conversationExport,
    messages: mocks.messages,
    runs: mocks.runs,
    takeoverEnter: vi.fn(),
    takeoverRun: vi.fn(),
    takeoverExit: vi.fn(),
  },
  modelApi: { overview: mocks.overview, activate: vi.fn() },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: {},
}));
vi.mock("../../ui/dialogs", () => ({
  ask: vi.fn(),
  promptText: vi.fn(),
  pickSavePath: mocks.pickSavePath,
  finishSave: mocks.finishSave,
}));

import { AiSidebar } from "../../features/ai/AiSidebar";
import { useUi, GLOBAL_AI_BOARD_KEY } from "../../app/store";

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
  mocks.conversationList.mockResolvedValue([{ id: "c1", title: "Investigate nginx", updatedAt: 1700000000000 }]);
  mocks.messages.mockResolvedValue([]);
  mocks.runs.mockResolvedValue([]);
  mocks.overview.mockResolvedValue({ profiles: [], activeId: null });
  mocks.conversationExport.mockResolvedValue({ path: "/save/x", matched: 1, exported: 1, skipped: { running: 0 }, files: 1, bytes: 1 });
  mocks.pickSavePath.mockResolvedValue("/save/x");
  mocks.finishSave.mockResolvedValue("/save/x");
  useUi.setState({ rightOpen: true, aiBusy: false, takeover: null, pushToast: mocks.toast, workspaces: [], aiBoards: {}, sessions: [] });
});

afterEach(async () => {
  await flush();
  mounted?.unmount();
  mounted = undefined;
});

const text = () => document.body.textContent ?? "";

describe("AI dock conversation export", () => {
  it("offers the batch export from the history list", async () => {
    mounted = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flush();
    click(mounted.container.querySelector<HTMLButtonElement>('button[title="终端历史"]')!);
    await flushUntil(() => text().includes("Investigate nginx"));
    const exportButton = [...mounted.container.querySelectorAll("button")].find((button) => button.textContent?.trim() === "导出…");
    click(exportButton!);
    await flush();
    expect(document.querySelector('[data-testid="ai-archive-dialog"]')).not.toBeNull();
    await waitForRendered(() => expect(mocks.conversationExport).toHaveBeenCalledWith(expect.objectContaining({ dryRun: true, format: "zip" })));
    await flush();
  });

  it("exports the current conversation from the composer's more menu", async () => {
    useUi.setState({
      aiBoards: { [GLOBAL_AI_BOARD_KEY]: { tabs: [{ id: "tab-1", title: "Investigate nginx", conversationId: "c1" }], activeTabId: "tab-1" } },
    });
    mounted = mount(createElement(AiSidebar, {}));
    await flush();
    click(mounted.container.querySelector<HTMLButtonElement>('button[aria-label="更多"]')!);
    await flush();
    const item = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((candidate) => candidate.textContent?.includes("导出当前对话"));
    click(item!);
    await waitForRendered(() => expect(mocks.conversationExport).toHaveBeenCalled());
    expect(mocks.pickSavePath).toHaveBeenCalledWith("Investigate-nginx.md");
    expect(mocks.conversationExport.mock.calls[0][0]).toMatchObject({ format: "md", ids: ["c1"], path: "/save/x" });
    await flush();
  });
});
