/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import {
  click,
  flush,
  flushUntil,
  mount,
  setInputValue,
  waitFor,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  transcriptHosts: vi.fn(),
  transcriptList: vi.fn(),
  transcriptRead: vi.fn(),
  transcriptSearch: vi.fn(),
  transcriptRemove: vi.fn(),
  ask: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  assetApi: { list: mocks.assetList },
  transcriptApi: {
    hosts: mocks.transcriptHosts,
    list: mocks.transcriptList,
    read: mocks.transcriptRead,
    search: mocks.transcriptSearch,
    remove: mocks.transcriptRemove,
  },
  sessionApi: {},
  terminalApi: {},
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));
vi.mock("../../app/store", () => ({
  useUi: (selector: (state: { pushToast: unknown }) => unknown) =>
    selector({ pushToast: mocks.toast }),
}));

import { TranscriptHistoryPanel } from "../../features/terminal/TranscriptHistoryPanel";
import {
  chunkToText,
  createTranscriptDecoder,
  formatTranscriptBytes,
  stripAnsi,
} from "../../features/terminal/transcriptText";

const ASSET = {
  id: "01J0NEXTERMLOCALDEVICE0001",
  groupId: null,
  kind: "local" as const,
  name: "当前设备",
  host: null,
  port: null,
  username: null,
  authKind: null,
  keyPath: null,
  credId: null,
  options: {},
  tags: "",
  note: "",
  sort: 0,
  createdAt: 1,
  updatedAt: 1,
  deletedAt: null,
  builtin: true,
};

function summary(overrides: Record<string, unknown> = {}) {
  return {
    id: "01JTRANSCRIPTSEED000000001",
    sessionId: "session-1",
    assetId: ASSET.id,
    assetName: "当前设备",
    assetKind: "local",
    assetDeleted: false,
    startedAt: 1700000000000,
    endedAt: 1700000060000,
    bytes: 42,
    chunks: 2,
    truncated: false,
    active: false,
    ...overrides,
  };
}

function readResult(chunks: { seq: number; dataBase64: string }[], done = true) {
  return {
    chunks: chunks.map((chunk) => ({ seq: chunk.seq, tabId: "tab-1", ts: 1700000000000 + chunk.seq, dataBase64: chunk.dataBase64 })),
    nextSeq: chunks.length ? chunks[chunks.length - 1].seq + 1 : 0,
    done,
    totalBytes: 42,
  };
}

function b64(text: string): string {
  return btoa(String.fromCharCode(...new TextEncoder().encode(text)));
}

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.assetList.mockResolvedValue([ASSET]);
  mocks.transcriptHosts.mockResolvedValue([]);
  mocks.transcriptList.mockResolvedValue([]);
  mocks.transcriptRead.mockResolvedValue(readResult([]));
  mocks.transcriptSearch.mockResolvedValue([]);
  mocks.ask.mockResolvedValue(true);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("TranscriptHistoryPanel", () => {
  it("shows empty state when the host has no transcripts", async () => {
    mounted = mount(createElement(TranscriptHistoryPanel));
    await flushUntil(() => mocks.transcriptList.mock.calls.length > 0);
    await flush();
    expect(mounted.container.textContent).toContain("该主机还没有终端历史");
    expect(mocks.transcriptList).toHaveBeenCalledWith(ASSET.id);
  });

  it("shows host error state when asset list fails", async () => {
    mocks.assetList.mockRejectedValue(new Error("boom"));
    mounted = mount(createElement(TranscriptHistoryPanel));
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("读取主机列表失败"));
  });

  it("shows session error state when transcript list fails", async () => {
    mocks.transcriptList.mockRejectedValue(new Error("db down"));
    mounted = mount(createElement(TranscriptHistoryPanel));
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("读取终端历史失败"));
  });

  it("lists sessions chronologically with honest badges", async () => {
    mocks.transcriptList.mockResolvedValue([
      summary({ id: "01JTRANSCRIPTSEED000000002", active: true, endedAt: null }),
      summary({ id: "01JTRANSCRIPTSEED000000003", endedAt: null, active: false }),
      summary({ id: "01JTRANSCRIPTSEED000000004", truncated: true }),
    ]);
    mounted = mount(createElement(TranscriptHistoryPanel));
    await flushUntil(() => mounted!.container.querySelectorAll("tbody tr").length >= 3);
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("进行中");
    expect(text).toContain("异常结束");
    expect(text).toContain("已结束");
    expect(text).toContain("已截断");
  });

  it("marks deleted hosts on session rows", async () => {
    mocks.transcriptList.mockResolvedValue([summary({ assetDeleted: true })]);
    mounted = mount(createElement(TranscriptHistoryPanel));
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("主机已删除"));
  });

  it("reads output and strips ansi escapes for display", async () => {
    mocks.transcriptList.mockResolvedValue([summary()]);
    mocks.transcriptRead.mockResolvedValue(
      readResult([
        { seq: 0, dataBase64: b64("\u001b[31mred text\u001b[0m\r\n") },
        { seq: 1, dataBase64: b64("plain\r\n") },
      ]),
    );
    mounted = mount(createElement(TranscriptHistoryPanel));
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("已结束"));
    const row = mounted.container.querySelector("tbody tr");
    expect(row).not.toBeNull();
    click(row!);
    await waitFor(() => expect(mocks.transcriptRead).toHaveBeenCalledWith(summary().id, 0, 524288));
    const pre = mounted.container.querySelector("pre");
    expect(pre?.textContent).toContain("red text");
    expect(pre?.textContent).toContain("plain");
    expect(pre?.textContent).not.toContain("\u001b[31m");
  });

  it("paginates with load-more until done", async () => {
    mocks.transcriptList.mockResolvedValue([summary({ chunks: 3 })]);
    mocks.transcriptRead
      .mockResolvedValueOnce(readResult([{ seq: 0, dataBase64: b64("one\r\n") }], false))
      .mockResolvedValueOnce(
        readResult([
          { seq: 1, dataBase64: b64("two\r\n") },
          { seq: 2, dataBase64: b64("three\r\n") },
        ]),
      );
    mounted = mount(createElement(TranscriptHistoryPanel));
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("已结束"));
    click(mounted.container.querySelector("tbody tr")!);
    await flushUntil(() => (mounted!.container.querySelector("pre")?.textContent ?? "").includes("one"));
    const more = [...mounted.container.querySelectorAll("button")].find((b) => b.textContent?.includes("加载更多"));
    expect(more).not.toBeUndefined();
    click(more!);
    await flushUntil(() => {
      const text = mounted!.container.querySelector("pre")?.textContent ?? "";
      return text.includes("two") && text.includes("three");
    });
    expect(mocks.transcriptRead).toHaveBeenLastCalledWith(summary().id, 1, 524288);
  });

  it("searches the selected transcript and jumps to matches", async () => {
    mocks.transcriptList.mockResolvedValue([summary()]);
    mocks.transcriptRead.mockResolvedValue(readResult([{ seq: 0, dataBase64: b64("needle here\r\n") }]));
    mocks.transcriptSearch.mockResolvedValue([{ seq: 0, ts: 1700000000000, preview: "needle here" }]);
    mounted = mount(createElement(TranscriptHistoryPanel));
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("已结束"));
    click(mounted.container.querySelector("tbody tr")!);
    await flushUntil(() => mounted!.container.querySelector("pre") !== null);

    const input = mounted.container.querySelector<HTMLInputElement>('input[aria-label="搜索终端记录"]');
    expect(input).not.toBeNull();
    setInputValue(input!, "needle");
    const searchButton = [...mounted.container.querySelectorAll("button")].find((b) => b.textContent?.trim() === "搜索");
    click(searchButton!);
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("needle here"));
    expect(mocks.transcriptSearch).toHaveBeenCalledWith(summary().id, "needle");
  });

  it("shows search error state", async () => {
    mocks.transcriptList.mockResolvedValue([summary()]);
    mocks.transcriptRead.mockResolvedValue(readResult([{ seq: 0, dataBase64: b64("data\r\n") }]));
    mocks.transcriptSearch.mockRejectedValue(new Error("search exploded"));
    mounted = mount(createElement(TranscriptHistoryPanel));
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("已结束"));
    click(mounted.container.querySelector("tbody tr")!);
    await flushUntil(() => mounted!.container.querySelector("pre") !== null);
    const input = mounted.container.querySelector<HTMLInputElement>('input[aria-label="搜索终端记录"]');
    setInputValue(input!, "x");
    const searchButton = [...mounted.container.querySelectorAll("button")].find((b) => b.textContent?.trim() === "搜索");
    click(searchButton!);
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("搜索失败"));
  });

  it("deletes a transcript after confirmation and reloads", async () => {
    mocks.transcriptList.mockResolvedValue([summary()]);
    mocks.transcriptRemove.mockResolvedValue(undefined);
    mounted = mount(createElement(TranscriptHistoryPanel));
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("已结束"));
    const deleteButton = mounted.container.querySelector<HTMLButtonElement>('button[aria-label="删除这条记录"]');
    expect(deleteButton).not.toBeNull();
    click(deleteButton!);
    await waitFor(() => expect(mocks.transcriptRemove).toHaveBeenCalledWith(summary().id));
    await waitFor(() => expect(mocks.transcriptList.mock.calls.length).toBeGreaterThanOrEqual(2));
    expect(mocks.toast).toHaveBeenCalledWith("success", "已删除终端记录");
  });

  it("keeps the transcript when deletion is not confirmed", async () => {
    mocks.ask.mockResolvedValue(false);
    mocks.transcriptList.mockResolvedValue([summary()]);
    mounted = mount(createElement(TranscriptHistoryPanel));
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("已结束"));
    click(mounted.container.querySelector<HTMLButtonElement>('button[aria-label="删除这条记录"]')!);
    await flush();
    expect(mocks.transcriptRemove).not.toHaveBeenCalled();
  });

  it("lists soft-deleted hosts from transcript snapshots", async () => {
    mocks.assetList.mockResolvedValue([]);
    mocks.transcriptHosts.mockResolvedValue([
      {
        assetId: "01J0NEXTERMLOCALDEVICE0001",
        assetName: "当前设备",
        assetKind: "local",
        assetDeleted: true,
        transcripts: 2,
        lastStartedAt: 1700000000000,
      },
    ]);
    mocks.transcriptList.mockResolvedValue([summary({ assetDeleted: true })]);
    mounted = mount(createElement(TranscriptHistoryPanel));
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("主机已删除"));
    const select = mounted.container.querySelector<HTMLSelectElement>('select[aria-label="选择主机"]');
    expect(select?.value).toBe(ASSET.id);
    const options = [...(select?.options ?? [])].map((option) => option.textContent);
    expect(options.some((text) => text?.includes("当前设备") && text?.includes("已删除"))).toBe(true);
    expect(mocks.transcriptList).toHaveBeenCalledWith(ASSET.id);
  });

  it("decodes the reader as a continuous stream across chunks", async () => {
    mocks.transcriptList.mockResolvedValue([summary()]);
    const nihao = new TextEncoder().encode("你好");
    const chunk0 = new Uint8Array([...new TextEncoder().encode("say "), nihao[0]]);
    const chunk1 = new Uint8Array([
      ...[nihao[1], nihao[2]],
      ...new TextEncoder().encode("好\r\nerr\u001b[3"),
    ]);
    const chunk2 = new TextEncoder().encode("1mor\u001b[0m done\r\n");
    mocks.transcriptRead.mockResolvedValue(
      readResult(
        [
          { seq: 0, dataBase64: btoa(String.fromCharCode(...chunk0)) },
          { seq: 1, dataBase64: btoa(String.fromCharCode(...chunk1)) },
          { seq: 2, dataBase64: btoa(String.fromCharCode(...chunk2)) },
        ],
        true,
      ),
    );
    mounted = mount(createElement(TranscriptHistoryPanel));
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("已结束"));
    click(mounted.container.querySelector("tbody tr")!);
    await flushUntil(() => {
      const text = mounted!.container.querySelector("pre")?.textContent ?? "";
      return text.includes("error done");
    });
    const text = mounted.container.querySelector("pre")?.textContent ?? "";
    expect(text).toContain("say 你好");
    expect(text).toContain("error done");
    expect(text).not.toContain("\u001b");
    expect(text).not.toContain("�");
  });
});

describe("transcriptText", () => {
  it("decodes base64 chunks as utf-8", () => {
    expect(chunkToText(b64("你好\r\n"))).toBe("你好\r\n");
  });

  it("strips ansi escape sequences", () => {
    expect(stripAnsi("\u001b[1;31mred\u001b[0m plain")).toBe("red plain");
    expect(stripAnsi("no escapes")).toBe("no escapes");
  });

  it("formats byte sizes", () => {
    expect(formatTranscriptBytes(512)).toBe("512 B");
    expect(formatTranscriptBytes(2048)).toBe("2.0 KB");
    expect(formatTranscriptBytes(3 * 1024 * 1024)).toBe("3.0 MB");
  });

  it("streams utf-8 characters split across chunks", () => {
    const decoder = createTranscriptDecoder();
    const nihao = new TextEncoder().encode("你好");
    const first = new Uint8Array([...new TextEncoder().encode("say "), nihao[0]]);
    const second = new Uint8Array([nihao[1], nihao[2], ...new TextEncoder().encode("好")]);
    let text = decoder.push(btoa(String.fromCharCode(...first)));
    text += decoder.push(btoa(String.fromCharCode(...second)));
    text += decoder.flush();
    expect(text).toBe("say 你好");
    expect(text).not.toContain("�");
  });

  it("strips ansi sequences split across chunks", () => {
    const decoder = createTranscriptDecoder();
    const first = new TextEncoder().encode("err\u001b[3");
    const second = new TextEncoder().encode("1mor\u001b[0m done");
    let text = decoder.push(btoa(String.fromCharCode(...first)));
    text += decoder.push(btoa(String.fromCharCode(...second)));
    text += decoder.flush();
    expect(text).toBe("error done");
  });

  it("consumes charset designation sequences whole", () => {
    const decoder = createTranscriptDecoder();
    const first = new TextEncoder().encode("hel\u001b(");
    const second = new TextEncoder().encode("Blo visible");
    let text = decoder.push(btoa(String.fromCharCode(...first)));
    expect(text).toBe("hel");
    text += decoder.push(btoa(String.fromCharCode(...second)));
    text += decoder.flush();
    expect(text).toBe("hello visible");
  });

  it("strips DCS and APC control strings including split payloads", () => {
    const decoder = createTranscriptDecoder();
    const first = new TextEncoder().encode("hel\u001bP1;2|not-visible-payload");
    const second = new TextEncoder().encode("\u001b\\lo apc\u001b_secret\u001b\\done");
    let text = decoder.push(btoa(String.fromCharCode(...first)));
    expect(text).toBe("hel");
    text += decoder.push(btoa(String.fromCharCode(...second)));
    text += decoder.flush();
    expect(text).toBe("hello apcdone");
    expect(text).not.toContain("not-visible-payload");
    expect(text).not.toContain("secret");
  });

  it("strips SOS PM and C1 control strings", () => {
    const decoder = createTranscriptDecoder();
    const input = new TextEncoder().encode(
      "sos\u001bXsecret-sos\u001b\\done pm\u001b^secret-pm\u001b\\done c1\u0090secret-c1\u009cdone",
    );
    let text = decoder.push(btoa(String.fromCharCode(...input)));
    text += decoder.flush();
    expect(text).toBe("sosdone pmdone c1done");
  });

  it("completes a trailing incomplete escape on flush", () => {
    const decoder = createTranscriptDecoder();
    const first = new TextEncoder().encode("plain\u001b]0;title");
    let text = decoder.push(btoa(String.fromCharCode(...first)));
    expect(text).toBe("plain");
    text += decoder.flush();
    expect(text).toBe("plain");
  });
});
