/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  askChoice: vi.fn(),
  promptText: vi.fn(),
  pickLocalFile: vi.fn(),
  pickSavePath: vi.fn(),
  finishSave: vi.fn(),
  discardStaged: vi.fn(),
  list: vi.fn(),
  toast: vi.fn(),
  listenEvent: vi.fn(),
}));

vi.mock("../../ui/dialogs", () => ({
  ask: mocks.ask,
  askChoice: mocks.askChoice,
  promptText: mocks.promptText,
  pickLocalFile: mocks.pickLocalFile,
  pickSavePath: mocks.pickSavePath,
  finishSave: mocks.finishSave,
  discardStaged: mocks.discardStaged,
}));

vi.mock("../../ipc/commands", () => ({
  fsApi: {
    list: mocks.list,
    read: vi.fn(),
    rename: vi.fn(),
    chmod: vi.fn(),
    checksum: vi.fn(),
    mkdir: vi.fn(),
    delete: vi.fn(),
    upload: vi.fn(),
    download: vi.fn(),
    packDownload: vi.fn(),
    extract: vi.fn(),
  },
  terminalApi: { write: vi.fn() },
  sessionApi: {},
}));

vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    listenEvent: mocks.listenEvent,
    EVENTS: { fsProgress: "fs://progress" },
  };
});

vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getVirtualItems: () =>
      Array.from({ length: count }, (_, i) => ({ index: i, start: i * 30, size: 30, key: i })),
    getTotalSize: () => count * 30,
  }),
}));

import { FileBrowser } from "../../features/files/FileBrowser";
import { reduceFileProgress, type FileProgressMap } from "../../features/files/fileProgress";
import { useUi } from "../../app/store";
import type { FileEntryDto } from "../../ipc/types";

const SID = "s1";

function entry(name: string, kind: string): FileEntryDto {
  return {
    name,
    path: `~/${name}`,
    kind,
    size: 10,
    mode: "644",
    owner: "u",
    group: "u",
    mtime: 1,
    symlinkTarget: null,
  };
}

let mounted: MountedView | undefined;
let progressHandler: ((e: unknown) => void) | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.list.mockResolvedValue([entry("a.txt", "file")]);
  mocks.listenEvent.mockImplementation((t: string, cb: (e: unknown) => void) => {
    if (t === "fs://progress") progressHandler = cb;
    return Promise.resolve(() => {});
  });
  useUi.setState({ pushToast: mocks.toast, workspaces: [], activeWorkspaceId: null, sessions: [] });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(
    createElement(QueryClientProvider, { client }, createElement(FileBrowser, { sessionId: SID })),
  );
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  progressHandler = undefined;
});

describe("fs://progress error field", () => {
  it("reducer: error events remove their task and unknown tasks leave the map unchanged", () => {
    const current: FileProgressMap = {
      t1: { taskId: "t1", transferred: 5, total: 100, done: false },
    };
    const failed = reduceFileProgress(current, {
      taskId: "t1",
      transferred: 50,
      total: 100,
      done: true,
      error: "connection reset",
    });
    expect(failed).toEqual({});
    const untouched = reduceFileProgress(current, {
      taskId: "ghost",
      transferred: 0,
      total: 0,
      done: true,
      error: "boom",
    });
    expect(untouched).toBe(current);
    const progressed = reduceFileProgress(current, {
      taskId: "t1",
      transferred: 60,
      total: 100,
      done: false,
    });
    expect(progressed.t1.transferred).toBe(60);
  });

  it("component: failure events remove the progress bar and show an error toast", async () => {
    await waitFor(() => expect(mocks.list).toHaveBeenCalled());
    act(() => {
      progressHandler?.({ taskId: "t1", transferred: 5, total: 100, done: false });
    });
    expect(mounted?.container.querySelector('[data-task-id="t1"]')).not.toBeNull();

    act(() => {
      progressHandler?.({
        taskId: "t1",
        transferred: 50,
        total: 100,
        done: true,
        error: "connection reset",
      });
    });
    await flush();
    expect(mounted?.container.querySelector('[data-task-id="t1"]')).toBeNull();
    expect(mocks.toast).toHaveBeenCalledWith("error", "文件传输失败：connection reset");
  });

  it("component: successful completion only removes the progress bar without an error toast", async () => {
    await waitFor(() => expect(mocks.list).toHaveBeenCalled());
    act(() => {
      progressHandler?.({ taskId: "t2", transferred: 5, total: 100, done: false });
    });
    act(() => {
      progressHandler?.({ taskId: "t2", transferred: 100, total: 100, done: true });
    });
    await flush();
    expect(mounted?.container.querySelector('[data-task-id="t2"]')).toBeNull();
    expect(mocks.toast).not.toHaveBeenCalledWith("error", expect.anything());
  });
});
