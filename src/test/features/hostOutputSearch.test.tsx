/** @vitest-environment jsdom */
// Host detail "搜索输出" tab: searches recorded terminal output of one host and jumps into the history panel.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import {
  chooseSelectOption,
  click,
  deferred,
  flush,
  mountWithClient,
  setInputValue,
  waitForRendered,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    fetch: vi.fn(),
    assetList: vi.fn(),
    dockerPs: vi.fn(),
    dbSchemas: vi.fn(),
    healthProbe: vi.fn(),
    hostSearch: vi.fn(),
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: { ...actual.assetApi, list: mocks.assetList },
    dockerApi: { ...actual.dockerApi, ps: mocks.dockerPs },
    dbApi: { ...actual.dbApi, schemas: mocks.dbSchemas },
  };
});
vi.mock("../../ipc/execBatchApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/execBatchApi")>();
  return { ...actual, execBatchApi: { ...actual.execBatchApi, healthProbe: mocks.healthProbe, healthCancel: vi.fn(async () => undefined) } };
});
vi.mock("../../ipc/searchApi", () => ({
  commandSearchApi: { search: vi.fn() },
  transcriptHostSearchApi: { search: mocks.hostSearch },
}));

import { HostDetailView } from "../../features/hosts/HostDetailView";
import { useAuth } from "../../features/auth/store";
import { useTranscriptFocus } from "../../features/terminal/transcriptFocus";
import { useUi } from "../../app/store";
import type { Asset } from "../../ipc/commands";

const WEB: Asset = {
  id: "a-web",
  groupId: null,
  kind: "ssh",
  name: "web-01",
  host: "10.0.0.8",
  port: 22,
  username: "root",
  authKind: "password",
  keyPath: null,
  credId: null,
  options: {},
  tags: "",
  note: "",
  sort: 0,
  createdAt: 1,
  updatedAt: 1,
  deletedAt: null,
  builtin: false,
};

function transcript(id: string, extra: Record<string, unknown> = {}) {
  return {
    id,
    sessionId: `s-${id}`,
    assetId: "a-web",
    assetName: "web-01",
    assetKind: "ssh",
    assetDeleted: false,
    startedAt: 1700000000000,
    endedAt: 1700000060000,
    bytes: 10,
    chunks: 3,
    truncated: false,
    active: false,
    ...extra,
  };
}

function result(extra: Record<string, unknown> = {}) {
  return {
    sessions: [
      {
        transcript: transcript("t1"),
        matches: [
          { seq: 4, ts: 1700000004000, preview: "\u001b[31merror: disk full\u001b[0m" },
          { seq: 9, ts: 1700000009000, preview: "error: again" },
        ],
        moreMatches: true,
      },
      { transcript: transcript("t2", { active: true, endedAt: null }), matches: [{ seq: 1, ts: 1700000100000, preview: "error third" }], moreMatches: false },
    ],
    scannedSessions: 2,
    scannedBytes: 2048,
    truncated: false,
    ...extra,
  };
}

let mounted: MountedView | undefined;

function view(): HTMLElement {
  return mounted!.container;
}

function tab(name: string): HTMLElement {
  const el = [...view().querySelectorAll<HTMLElement>('[role="tab"]')].find((item) => item.textContent === name);
  if (!el) throw new Error(`tab ${name} not found`);
  return el;
}

function searchInput(): HTMLInputElement {
  return view().querySelector<HTMLInputElement>('input[aria-label="搜索终端输出"]')!;
}

async function openSearchTab(): Promise<void> {
  mounted = mountWithClient(createElement(HostDetailView, { assetId: "a-web", visible: true }));
  await waitForRendered(() => expect(view().textContent).toContain("web-01"));
  click(tab("搜索输出"));
}

async function submit(text: string): Promise<void> {
  setInputValue(searchInput(), text);
  act(() => {
    searchInput().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
  });
  await flush();
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.assetList.mockResolvedValue([WEB]);
  mocks.dockerPs.mockResolvedValue([]);
  mocks.dbSchemas.mockResolvedValue([]);
  mocks.hostSearch.mockResolvedValue(result());
  mocks.fetch.mockImplementation(async () => ({ ok: true, status: 200, text: async () => JSON.stringify({ devices: [] }) }));
  vi.stubGlobal("fetch", mocks.fetch);
  useTranscriptFocus.setState({ request: null });
  useUi.setState({ workspaces: [], activeWorkspaceId: null, sessions: [], toasts: [] });
  useAuth.setState({
    status: { initialized: true, registration_open: false, auth: "on" },
    user: null,
    dek: null,
    gate: "ready",
    pendingRecoveryKey: null,
    error: null,
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
});

describe("host output search tab", () => {
  it("defaults to the overview and switches to the search tab", async () => {
    mounted = mountWithClient(createElement(HostDetailView, { assetId: "a-web", visible: true }));
    await waitForRendered(() => expect(view().textContent).toContain("web-01"));
    expect(tab("概览").getAttribute("aria-selected")).toBe("true");
    expect(view().querySelector('[data-testid="host-metrics"]')).not.toBeNull();
    expect(searchInput()).toBeNull();

    click(tab("搜索输出"));
    expect(tab("搜索输出").getAttribute("aria-selected")).toBe("true");
    expect(searchInput()).not.toBeNull();
  });

  it("does not search an empty query and sends trimmed text with the range filter", async () => {
    await openSearchTab();
    await submit("   ");
    expect(mocks.hostSearch).not.toHaveBeenCalled();

    await submit("error");
    expect(mocks.hostSearch).toHaveBeenLastCalledWith(
      expect.objectContaining({ assetId: "a-web", query: "error", startedFrom: null, startedTo: null }),
    );
    chooseSelectOption(view(), "输出搜索时间范围", "7 天");
    await submit("error");
    const last = mocks.hostSearch.mock.calls.at(-1)?.[0] as { startedFrom: number };
    expect(last.startedFrom).toBeGreaterThan(Date.now() - 8 * 24 * 3600 * 1000);
  });

  it("limits the query to 256 bytes", async () => {
    await openSearchTab();
    setInputValue(searchInput(), "a".repeat(256));
    setInputValue(searchInput(), "a".repeat(256) + "b");
    expect(new TextEncoder().encode(searchInput().value).length).toBeLessThanOrEqual(256);
    setInputValue(searchInput(), "字".repeat(100));
    expect(new TextEncoder().encode(searchInput().value).length).toBeLessThanOrEqual(256);
  });

  it("groups matches by session, strips ANSI and notes when matches are capped", async () => {
    await openSearchTab();
    await submit("error");
    const groups = view().querySelectorAll('[data-testid="output-search-session"]');
    expect(groups).toHaveLength(2);
    expect(groups[0].textContent).toContain("已结束");
    expect(groups[0].textContent).toContain("2 条匹配");
    expect(groups[0].textContent).toContain("仅显示前 50 条");
    expect(groups[0].textContent).toContain("error: disk full");
    expect(groups[0].textContent).not.toContain("\u001b");
    expect(groups[1].textContent).toContain("进行中");
    expect(groups[1].textContent).not.toContain("仅显示前 50 条");
    expect(view().textContent).not.toContain("结果已截断");
  });

  it("warns prominently when the scan was truncated", async () => {
    mocks.hostSearch.mockResolvedValue(result({ truncated: true, scannedSessions: 20, scannedBytes: 64 * 1024 * 1024 }));
    await openSearchTab();
    await submit("error");
    const warning = view().querySelector('[data-testid="output-search-truncated"]');
    expect(warning?.textContent).toContain("已扫描 20 个会话");
    expect(warning?.textContent).toContain("64");
    expect(warning?.textContent).toContain("结果已截断，请缩小时间范围");
  });

  it("shows empty and error states", async () => {
    mocks.hostSearch.mockResolvedValueOnce(result({ sessions: [] }));
    await openSearchTab();
    await submit("nothing");
    expect(view().textContent).toContain("没有匹配的输出");
    mocks.hostSearch.mockRejectedValueOnce(new Error("scan failed"));
    await submit("again");
    expect(view().textContent).toContain("scan failed");
  });

  it("ignores a stale response", async () => {
    const first = deferred<ReturnType<typeof result>>();
    mocks.hostSearch.mockReturnValueOnce(first.promise).mockResolvedValueOnce(result({ sessions: [] }));
    await openSearchTab();
    await submit("one");
    await submit("two");
    await act(async () => first.resolve(result()));
    await flush();
    expect(view().querySelectorAll('[data-testid="output-search-session"]')).toHaveLength(0);
    expect(view().textContent).toContain("没有匹配的输出");
  });

  it("opens the history panel at the clicked match", async () => {
    await openSearchTab();
    await submit("error");
    const match = [...view().querySelectorAll<HTMLElement>('[data-testid="output-search-match"]')][1];
    click(match);
    expect(useTranscriptFocus.getState().request).toMatchObject({
      assetId: "a-web",
      transcriptId: "t1",
      ts: 1700000009000,
      seq: 9,
    });
    const tabs = useUi.getState().workspaces.flatMap((w) => w.panes.flatMap((p) => p.tabs));
    expect(tabs.some((item) => item.kind === "history" && item.assetId === "a-web")).toBe(true);
  });

  it("states that records are stored in plain text and opens the host history", async () => {
    await openSearchTab();
    expect(view().textContent).toContain("终端记录以明文保存在数据库中");
    const button = [...view().querySelectorAll("button")].find((item) => item.textContent === "打开终端历史")!;
    click(button);
    const tabs = useUi.getState().workspaces.flatMap((w) => w.panes.flatMap((p) => p.tabs));
    expect(tabs.some((item) => item.kind === "history" && item.assetId === "a-web")).toBe(true);
  });
});
