/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { EditorView } from "@codemirror/view";
import { click, mount, waitForRendered as waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  read: vi.fn(),
  readRange: vi.fn(),
  write: vi.fn(),
  watch: vi.fn(),
  unwatch: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  fsApi: { read: mocks.read, readRange: mocks.readRange, write: mocks.write, watch: mocks.watch, unwatch: mocks.unwatch },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: {},
}));
vi.mock("../../ipc/events", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ipc/events")>()),
  listenEvent: () => Promise.resolve(() => undefined),
}));
vi.mock("../../ui/dialogs", () => ({ ask: vi.fn(), askChoice: vi.fn() }));

import { FileEditor, fileVersionOf } from "../../features/files/FileEditor";
import { requestFileReview } from "../../features/files/aiReview";
import { useUi } from "../../app/store";

const PATH = "/srv/app/config.yml";
const BEFORE = "name: app\nport: 80\nworkers: 2\nlog: info\n";
const AFTER = "name: app\nport: 8080\nworkers: 2\nlog: debug\n";

let disk = "";

function encoded(text: string) {
  const bytes = new TextEncoder().encode(text);
  let binary = "";
  bytes.forEach((byte) => {
    binary += String.fromCharCode(byte);
  });
  return { path: PATH, size: bytes.length, contentBase64: btoa(binary) };
}

function decoded(base64: string): string {
  return new TextDecoder().decode(Uint8Array.from(atob(base64), (c) => c.charCodeAt(0)));
}

let mounted: MountedView | undefined;

async function mountReview(): Promise<EditorView> {
  requestFileReview("s", PATH, { before: BEFORE, after: AFTER });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(createElement(QueryClientProvider, { client }, createElement(FileEditor, { sessionId: "s", path: PATH })));
  await waitFor(() => expect(mounted!.container.querySelector('[data-testid="ai-change-review"]')).not.toBeNull());
  return EditorView.findFromDOM(mounted!.container.querySelector<HTMLElement>(".cm-editor")!)!;
}

function reviewBar(): HTMLElement {
  return mounted!.container.querySelector<HTMLElement>('[data-testid="ai-change-review"]')!;
}

function revertButtons(): HTMLButtonElement[] {
  return [...mounted!.container.querySelectorAll<HTMLButtonElement>("[data-review-revert]")];
}

function barButton(label: string): HTMLButtonElement | undefined {
  return [...reviewBar().querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === label);
}

function pressRevert(button: HTMLButtonElement): void {
  act(() => {
    button.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true, button: 0 }));
  });
}

describe("FileEditor reviews an AI change", () => {
  beforeEach(() => {
    Object.defineProperty(Range.prototype, "getClientRects", { configurable: true, value: () => [] });
    useUi.setState({ pushToast: vi.fn(), setRightOpen: vi.fn(), aiPendingRefs: [], aiPendingPrompt: null, sessions: [{ id: "s", name: "web-01", kind: "ssh" }] as never });
    disk = AFTER;
    mocks.read.mockImplementation(async () => encoded(disk));
    mocks.readRange.mockImplementation(async () => ({ ...encoded(disk), contentBase64: "" }));
    mocks.write.mockImplementation(async (_s: string, _p: string, content: string) => {
      disk = decoded(content);
    });
    mocks.watch.mockResolvedValue({ sessionId: "s", mode: "push", channel: "helper" });
    mocks.unwatch.mockResolvedValue({});
  });

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
    vi.clearAllMocks();
  });

  it("shows the change as a diff against the content before it, with a revert per change", async () => {
    const view = await mountReview();
    await waitFor(() => expect(revertButtons()).toHaveLength(2));
    const deleted = [...mounted!.container.querySelectorAll(".cm-deletedChunk")].map((chunk) => chunk.textContent);
    expect(deleted.join("|")).toContain("port: 80");
    expect(deleted.join("|")).toContain("log: info");
    expect(view.state.doc.toString()).toBe(AFTER);
    expect(reviewBar().dataset.conflict).toBeUndefined();
    expect(barButton("全部恢复")).toBeDefined();
  });

  it("reverts one change through the editor's save, replacing only the version the AI wrote", async () => {
    await mountReview();
    await waitFor(() => expect(revertButtons()).toHaveLength(2));
    pressRevert(revertButtons()[0]);
    await waitFor(() => expect(mocks.write).toHaveBeenCalledTimes(1));
    const [sessionId, path, content, , , , expected] = mocks.write.mock.calls[0];
    expect([sessionId, path]).toEqual(["s", PATH]);
    expect(decoded(content)).toBe("name: app\nport: 80\nworkers: 2\nlog: debug\n");
    expect(expected).toEqual(await fileVersionOf(new TextEncoder().encode(AFTER)));
    await waitFor(() => expect(revertButtons()).toHaveLength(1));
  });

  it("restores the whole file to the content before the change", async () => {
    await mountReview();
    await waitFor(() => expect(revertButtons()).toHaveLength(2));
    click(barButton("全部恢复")!);
    await waitFor(() => expect(mocks.write).toHaveBeenCalledTimes(1));
    expect(decoded(mocks.write.mock.calls[0][2])).toBe(BEFORE);
  });

  it("shows a conflict and offers no revert when the file changed after the AI wrote it", async () => {
    disk = "name: app\nport: 9000\nworkers: 4\nlog: debug\n";
    await mountReview();
    await waitFor(() => expect(reviewBar().dataset.conflict).toBe("changed"));
    expect(reviewBar().querySelector('[role="alert"]')?.textContent).toContain("不能逐处撤销");
    await waitFor(() => expect(mounted!.container.querySelector(".cm-deletedChunk")).not.toBeNull());
    expect(revertButtons()).toHaveLength(0);
    expect(barButton("全部恢复")).toBeUndefined();
    expect(mocks.write).not.toHaveBeenCalled();
  });
});
