/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount, waitForRendered, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  transcriptHosts: vi.fn(),
  transcriptList: vi.fn(),
  transcriptRead: vi.fn(),
  toast: vi.fn(),
  updateTab: vi.fn(),
  uiState: { pushToast: null as unknown, updateTab: null as unknown },
}));

vi.mock("../../ipc/commands", () => ({
  assetApi: { list: mocks.assetList },
  transcriptApi: {
    hosts: mocks.transcriptHosts,
    list: mocks.transcriptList,
    read: mocks.transcriptRead,
    search: vi.fn(),
    remove: vi.fn(),
    syncOptIn: vi.fn(),
  },
  sessionApi: {},
  terminalApi: {},
}));
vi.mock("../../ui/dialogs", () => ({ ask: vi.fn() }));
vi.mock("../../ui/clipboard", () => ({ copyText: vi.fn() }));
vi.mock("../../app/store", () => {
  mocks.uiState.pushToast = mocks.toast;
  mocks.uiState.updateTab = mocks.updateTab;
  const useUi = (selector: (state: typeof mocks.uiState) => unknown) => selector(mocks.uiState);
  useUi.getState = () => mocks.uiState;
  return { useUi, hostHistoryTab: () => null };
});
vi.mock("../../features/terminal/TranscriptReplayView", () => ({
  TranscriptReplayView: (props: { transcriptId: string; initialTs?: number }) =>
    createElement("div", {
      "data-testid": "replay-stub",
      "data-transcript": props.transcriptId,
      "data-initial-ts": props.initialTs === undefined ? "" : String(props.initialTs),
    }),
}));

import { TranscriptHistoryPanel } from "../../features/terminal/TranscriptHistoryPanel";
import { requestTranscriptFocus, useTranscriptFocus } from "../../features/terminal/transcriptFocus";

const HOST = "asset-a";

function summary(id: string, assetId = HOST) {
  return {
    id,
    sessionId: `s-${id}`,
    assetId,
    assetName: assetId,
    assetKind: "ssh",
    assetDeleted: false,
    startedAt: 1700000000000,
    endedAt: 1700000060000,
    bytes: 10,
    chunks: 4,
    truncated: false,
    active: false,
  };
}

function b64(text: string): string {
  return btoa(text);
}

let mounted: MountedView | undefined;
let scrolled: string[] = [];

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  scrolled = [];
  Element.prototype.scrollIntoView = function scrollIntoView(this: Element) {
    scrolled.push(this.getAttribute("data-chunk-seq") ?? "");
  };
  useTranscriptFocus.setState({ request: null });
  mocks.assetList.mockResolvedValue([]);
  mocks.transcriptHosts.mockResolvedValue([]);
  mocks.transcriptList.mockImplementation(async (assetId: string) =>
    assetId === HOST ? [summary("t1"), summary("t2")] : [summary("t9", assetId)],
  );
  mocks.transcriptRead.mockImplementation(async (id: string) => ({
    chunks: [0, 1, 2, 3].map((seq) => ({
      seq,
      tabId: "tab",
      ts: 1700000000000 + seq * 1000,
      kind: 0,
      dataBase64: b64(`${id}-line${seq}\n`),
    })),
    nextSeq: 4,
    done: true,
    totalBytes: 10,
  }));
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("TranscriptHistoryPanel focus requests", () => {
  it("selects the transcript and scrolls to the requested seq in the text view", async () => {
    mounted = mount(createElement(TranscriptHistoryPanel, { assetId: HOST }));
    await flush();
    await act(async () => requestTranscriptFocus({ assetId: HOST, transcriptId: "t2", ts: 1700000002000, seq: 2 }));
    await waitForRendered(() => expect(scrolled).toContain("2"));
    expect(mocks.transcriptRead).toHaveBeenCalledWith("t2", 0, expect.any(Number));
    expect(useTranscriptFocus.getState().request).toBeNull();
  });

  it("opens the replay view at the timestamp when only ts is given", async () => {
    mounted = mount(createElement(TranscriptHistoryPanel, { assetId: HOST }));
    await flush();
    await act(async () => requestTranscriptFocus({ assetId: HOST, transcriptId: "t1", ts: 1700000003000 }));
    await waitForRendered(() => {
      const stub = mounted!.container.querySelector("[data-testid='replay-stub']");
      expect(stub?.getAttribute("data-transcript")).toBe("t1");
      expect(stub?.getAttribute("data-initial-ts")).toBe("1700000003000");
    });
    // The text reader keeps loading behind the replay view; let it settle before teardown.
    await waitForRendered(() => expect(mocks.transcriptRead).toHaveBeenCalledWith("t1", 0, expect.any(Number)));
    await flush();
  });

  it("applies a second request while the panel stays open", async () => {
    mounted = mount(createElement(TranscriptHistoryPanel, { assetId: HOST }));
    await flush();
    await act(async () => requestTranscriptFocus({ assetId: HOST, transcriptId: "t1", ts: 1, seq: 1 }));
    await waitForRendered(() => expect(scrolled).toContain("1"));
    await act(async () => requestTranscriptFocus({ assetId: HOST, transcriptId: "t2", ts: 1, seq: 3 }));
    await waitForRendered(() => expect(scrolled).toContain("3"));
    expect(mocks.transcriptRead).toHaveBeenCalledWith("t2", 0, expect.any(Number));
  });

  it("switches to another host before selecting", async () => {
    mounted = mount(createElement(TranscriptHistoryPanel, { assetId: HOST }));
    await flush();
    await act(async () => requestTranscriptFocus({ assetId: "asset-b", transcriptId: "t9", ts: 5, seq: 0 }));
    await waitForRendered(() => expect(scrolled).toContain("0"));
    expect(mocks.transcriptList).toHaveBeenCalledWith("asset-b");
    expect(mocks.transcriptRead).toHaveBeenCalledWith("t9", 0, expect.any(Number));
  });

  it("reports a transcript that no longer exists", async () => {
    mounted = mount(createElement(TranscriptHistoryPanel, { assetId: HOST }));
    await flush();
    await act(async () => requestTranscriptFocus({ assetId: HOST, transcriptId: "gone", ts: 1, seq: 0 }));
    await waitForRendered(() => expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("未找到")));
  });
});
