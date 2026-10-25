/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { forceParsing, StreamLanguage } from "@codemirror/language";
import { javascript } from "@codemirror/legacy-modes/mode/javascript";
import type { Extension } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import {
  deferred,
  flushUntil,
  mount,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  read: vi.fn(),
  readRange: vi.fn(),
  write: vi.fn(),
  ask: vi.fn(),
  askChoice: vi.fn(),
  toast: vi.fn(),
  load: vi.fn<(path: string) => Promise<Extension>>(),
}));

vi.mock("../../ipc/commands", () => ({
  fsApi: {
    read: mocks.read,
    readRange: mocks.readRange,
    write: mocks.write,
  },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: {},
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, askChoice: mocks.askChoice }));

vi.mock("../../features/files/fileLanguages", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../features/files/fileLanguages")>()),
  loadFileLanguageExtension: (path: string) => mocks.load(path),
}));

import { FileEditor } from "../../features/files/FileEditor";
import { useUi } from "../../app/store";

function encoded(text: string): { path: string; size: number; contentBase64: string } {
  const bytes = new TextEncoder().encode(text);
  let binary = "";
  bytes.forEach((byte) => {
    binary += String.fromCharCode(byte);
  });
  return { path: "/tmp/file", size: bytes.length, contentBase64: btoa(binary) };
}

let mounted: MountedView | undefined;
let client: QueryClient;

function tree(path: string) {
  return createElement(
    QueryClientProvider,
    { client },
    createElement(FileEditor, { sessionId: "s", path }),
  );
}

function editor(): EditorView {
  const element = mounted?.container.querySelector<HTMLElement>(".cm-editor");
  const view = element ? EditorView.findFromDOM(element) : null;
  if (!view) throw new Error("File editor CodeMirror view not found");
  return view;
}

function hasHighlight(): boolean {
  forceParsing(editor(), editor().state.doc.length, 1000);
  return [...mounted!.container.querySelectorAll(".cm-line span")].some((s) => s.textContent === "const");
}

beforeEach(() => {
  vi.clearAllMocks();
  Object.defineProperty(Range.prototype, "getClientRects", { configurable: true, value: () => [] });
  document.body.replaceChildren();
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mocks.read.mockImplementation(() => Promise.resolve(encoded("const answer = 1;")));
  mocks.readRange.mockResolvedValue({ size: 17 });
  useUi.setState({ pushToast: mocks.toast, sessions: [] });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.restoreAllMocks();
});

describe("FileEditor lazy language packs", () => {
  it("is usable as plain text before the pack arrives, then highlights", async () => {
    const pack = deferred<Extension>();
    mocks.load.mockReturnValue(pack.promise);
    mounted = mount(tree("/tmp/a.js"));
    await flushUntil(() => mounted!.container.querySelector(".cm-editor") !== null);

    expect(editor().state.doc.toString()).toBe("const answer = 1;");
    expect(hasHighlight()).toBe(false);

    await act(async () => {
      pack.resolve(StreamLanguage.define(javascript));
    });
    await flushUntil(hasHighlight);
    expect(mocks.read).toHaveBeenCalledOnce();
  });

  it("keeps plain text when the pack fails to load", async () => {
    mocks.load.mockRejectedValue(new Error("chunk failed"));
    mounted = mount(tree("/tmp/a.js"));
    await flushUntil(() => mocks.load.mock.calls.length > 0 && mounted!.container.querySelector(".cm-editor") !== null);
    await flushUntil(() => mocks.load.mock.results.length > 0);

    expect(editor().state.doc.toString()).toBe("const answer = 1;");
    expect(hasHighlight()).toBe(false);
    expect(mocks.toast).not.toHaveBeenCalled();
  });

  it("drops a stale pack when the path changed meanwhile", async () => {
    const first = deferred<Extension>();
    mocks.load.mockImplementation((path: string) =>
      path.endsWith(".js") ? first.promise : Promise.resolve([]),
    );
    mounted = mount(tree("/tmp/a.js"));
    await flushUntil(() => mounted!.container.querySelector(".cm-editor") !== null);
    act(() => {
      mounted!.root.render(tree("/tmp/a.txt"));
    });
    await flushUntil(() => mocks.load.mock.calls.some(([p]) => p === "/tmp/a.txt"));

    await act(async () => {
      first.resolve(StreamLanguage.define(javascript));
    });
    await flushUntil(() => true);
    expect(hasHighlight()).toBe(false);
  });
});
