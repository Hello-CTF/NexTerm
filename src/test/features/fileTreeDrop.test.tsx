/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { flush, mount, waitForRendered, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  askChoice: vi.fn(),
  promptText: vi.fn(),
  pickLocalFile: vi.fn(),
  pickSavePath: vi.fn(),
  finishSave: vi.fn(),
  discardStaged: vi.fn(),
  stageFile: vi.fn(),
  dropStaged: vi.fn(),
  roots: vi.fn(),
  list: vi.fn(),
  write: vi.fn(),
  rename: vi.fn(),
  chmod: vi.fn(),
  checksum: vi.fn(),
  mkdir: vi.fn(),
  remove: vi.fn(),
  upload: vi.fn(),
  download: vi.fn(),
  packDownload: vi.fn(),
  extract: vi.fn(),
  termWrite: vi.fn(),
  copy: vi.fn(),
  lineExec: vi.fn(),
  connect: vi.fn(),
  connectLocal: vi.fn(),
  connectDevice: vi.fn(),
  knownHostAccept: vi.fn(),
  listenEvent: vi.fn(),
}));

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return {
    ...actual,
    ask: mocks.ask,
    askChoice: mocks.askChoice,
    promptText: mocks.promptText,
    pickLocalFile: mocks.pickLocalFile,
    pickSavePath: mocks.pickSavePath,
    finishSave: mocks.finishSave,
    discardStaged: mocks.discardStaged,
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    fsApi: {
      roots: mocks.roots,
      list: mocks.list,
      write: mocks.write,
      rename: mocks.rename,
      chmod: mocks.chmod,
      checksum: mocks.checksum,
      mkdir: mocks.mkdir,
      delete: mocks.remove,
      upload: mocks.upload,
      download: mocks.download,
      packDownload: mocks.packDownload,
      extract: mocks.extract,
    },
    sessionApi: {
      connect: mocks.connect,
      connectLocal: mocks.connectLocal,
      connectDevice: mocks.connectDevice,
      lineExec: mocks.lineExec,
    },
    terminalApi: { write: mocks.termWrite },
    assetApi: { ...(actual.assetApi ?? {}), knownHostAccept: mocks.knownHostAccept },
  };
});

vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    listenEvent: mocks.listenEvent,
    EVENTS: { ...actual.EVENTS, fsProgress: "fs://progress", filesDropped: "files://dropped" },
  };
});

vi.mock("../../ipc/webFiles", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/webFiles")>();
  return {
    ...actual,
    stageFile: mocks.stageFile,
    dropStaged: mocks.dropStaged,
  };
});

vi.mock("../../ui/clipboard", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/clipboard")>();
  return { ...actual, copyText: mocks.copy };
});

import { FileTree } from "../../features/files/FileTree";
import { resetNativeDropDedupeForTest } from "../../features/files/nativeDrop";
import { resetCwdFollowForTest } from "../../features/files/cwdFollow";
import { useTransferStore } from "../../features/files/transferStore";
import { useUi, type AppTab } from "../../app/store";
import type { FileEntryDto } from "../../ipc/types";

const SID = "s1";

function entry(name: string, kind: string): FileEntryDto {
  return {
    name,
    path: `~/${name}`,
    kind,
    size: kind === "dir" ? 4096 : 10,
    mode: kind === "dir" ? "755" : "644",
    owner: "u",
    group: "u",
    mtime: 1,
    symlinkTarget: null,
  };
}

let mounted: MountedView | undefined;

function seedWorkspace(): void {
  const tab: AppTab = {
    id: "t1",
    kind: "terminal",
    title: "web-01",
    sessionId: SID,
    closable: true,
  };
  useUi.setState({
    workspaces: [
      {
        id: "ws1",
        kind: "session",
        title: "web-01",
        sessionId: SID,
        assetId: "asset-1",
        assetKind: "ssh",
        panes: [{ id: "p1", tabs: [tab], activeTabId: tab.id }],
        activePaneId: "p1",
        closable: true,
      },
    ],
    activeWorkspaceId: "ws1",
    sessions: [
      {
        id: SID,
        assetId: "asset-1",
        name: "web-01",
        kind: "ssh",
        status: "connected",
        tabs: [],
        createdAt: 0,
      },
    ],
    leftOpen: true,
    leftWidth: 260,
    toasts: [],
  });
}

function TreeFromStore() {
  const sessionId = useUi((s) => s.workspaces.find((w) => w.id === "ws1")?.sessionId ?? null);
  return sessionId ? createElement(FileTree, { sessionId }) : null;
}

function mountTree(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(TreeFromStore)));
}

function rowByPath(path: string): HTMLElement {
  const row = [...(mounted?.container.querySelectorAll<HTMLElement>('[role="treeitem"]') ?? [])].find(
    (candidate) => candidate.getAttribute("title") === path,
  );
  if (!row) throw new Error(`tree row not found: ${path}`);
  return row;
}

function toastTexts(): string {
  return useUi
    .getState()
    .toasts.map((t) => t.text)
    .join("\n");
}

interface DropOptions {
  types?: string[];
  items?: unknown[];
  files?: File[];
}

function dispatchDrop(target: Element, options: DropOptions): Event {
  const event = new Event("drop", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "dataTransfer", {
    value: {
      types: options.types ?? ["Files"],
      items: options.items ?? [],
      files: options.files ?? [],
      dropEffect: "none",
    },
  });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  window.localStorage.clear();
  resetCwdFollowForTest();
  resetNativeDropDedupeForTest();
  useTransferStore.setState({ tasks: [] });
  mocks.roots.mockResolvedValue({
    pathStyle: "unknown",
    roots: [],
    homePath: null,
    supported: false,
  });
  mocks.list.mockImplementation((_sid: string, path: string) =>
    Promise.resolve(path === "~" ? [entry("a.txt", "file"), entry("sub", "dir")] : []),
  );
  mocks.stageFile.mockImplementation(async (file: File) => ({ id: "b1", path: `b1/${file.name}` }));
  mocks.dropStaged.mockResolvedValue(undefined);
  mocks.discardStaged.mockResolvedValue(undefined);
  mocks.upload.mockResolvedValue(1);
  mocks.listenEvent.mockReturnValue(Promise.resolve(() => {}));
  mocks.ask.mockResolvedValue(false);
  mocks.askChoice.mockResolvedValue(null);
  mocks.promptText.mockResolvedValue(null);
  seedWorkspace();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("FileTree drop targeting", () => {
  it("uploads into the directory row that receives the drop, not the current directory", async () => {
    mounted = mountTree();
    await waitForRendered(() => expect(rowByPath("~/sub")).toBeDefined());

    const event = dispatchDrop(rowByPath("~/sub"), { files: [new File(["x"], "d.txt")] });
    expect(event.defaultPrevented).toBe(true);

    await waitForRendered(() =>
      expect(mocks.upload).toHaveBeenCalledWith(
        SID,
        expect.any(String),
        "~/sub/d.txt",
        false,
        expect.any(String),
        expect.any(String),
      ),
    );
    expect(mocks.upload).not.toHaveBeenCalledWith(
      SID,
      expect.any(String),
      "~/d.txt",
      false,
      expect.any(String),
      expect.any(String),
    );
    expect(mocks.list).toHaveBeenCalledWith(SID, "~/sub");
  });

  it("uploads into the current directory when the drop lands outside any row", async () => {
    mounted = mountTree();
    await waitForRendered(() => expect(rowByPath("~/sub")).toBeDefined());

    const tree = mounted.container.querySelector("[role='tree']");
    if (!tree) throw new Error("tree container not found");
    dispatchDrop(tree, { files: [new File(["x"], "d.txt")] });

    await waitForRendered(() =>
      expect(mocks.upload).toHaveBeenCalledWith(
        SID,
        expect.any(String),
        "~/d.txt",
        false,
        expect.any(String),
        expect.any(String),
      ),
    );
  });
});

describe("FileTree drop with unresolvable entries", () => {
  it("uploads nothing and shows the skip notice when a dropped directory cannot be resolved", async () => {
    mounted = mountTree();
    await waitForRendered(() => expect(rowByPath("~/sub")).toBeDefined());

    // Platforms without webkitGetAsEntry surface a dropped folder as a 0-byte typeless File.
    const pseudo = new File([], "pics");
    const tree = mounted.container.querySelector("[role='tree']");
    if (!tree) throw new Error("tree container not found");
    dispatchDrop(tree, { items: [{ getAsFile: () => pseudo }], files: [pseudo] });

    await waitForRendered(() => expect(toastTexts()).toContain("无法识别的条目"));
    expect(toastTexts()).toContain("pics");
    expect(mocks.upload).not.toHaveBeenCalled();
    expect(mocks.stageFile).not.toHaveBeenCalled();
    expect(useTransferStore.getState().tasks).toEqual([]);
  });
});

describe("FileTree drop with non-file content", () => {
  it("does not preventDefault and starts no upload when the drag carries no Files type", async () => {
    mounted = mountTree();
    await waitForRendered(() => expect(rowByPath("~/sub")).toBeDefined());

    const event = dispatchDrop(rowByPath("~/sub"), { types: ["text/plain"] });
    expect(event.defaultPrevented).toBe(false);

    await flush();
    expect(mocks.upload).not.toHaveBeenCalled();
    expect(mocks.stageFile).not.toHaveBeenCalled();
    expect(useTransferStore.getState().tasks).toEqual([]);
  });
});
