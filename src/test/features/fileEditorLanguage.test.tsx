/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { redo, undo } from "@codemirror/commands";
import { forceParsing } from "@codemirror/language";
import { EditorView } from "@codemirror/view";
import {
  click,
  flush,
  mount,
  chooseSelectOption,
  waitFor,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  read: vi.fn(),
  readRange: vi.fn(),
  write: vi.fn(),
  ask: vi.fn(),
  askChoice: vi.fn(),
  toast: vi.fn(),
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

async function mountEditor(path: string): Promise<void> {
  mounted = mount(tree(path));
  await waitFor(() => expect(mounted!.container.querySelector(".cm-editor")).not.toBeNull());
}

function editor(): EditorView {
  const element = mounted?.container.querySelector<HTMLElement>(".cm-editor");
  const view = element ? EditorView.findFromDOM(element) : null;
  if (!view) throw new Error("File editor CodeMirror view not found");
  return view;
}

function languageSelect(): HTMLButtonElement {
  const select = mounted?.container.querySelector<HTMLButtonElement>('button[aria-label="语法高亮"]');
  if (!select) throw new Error("Language selector not found");
  return select;
}

function chooseLanguage(optionText: string): void {
  chooseSelectOption(mounted!.container, "语法高亮", optionText);
}

function highlightedSpan(text: string): HTMLSpanElement | null {
  return (
    [...(mounted?.container.querySelectorAll<HTMLSpanElement>(".cm-line span") ?? [])].find(
      (span) => span.textContent === text,
    ) ?? null
  );
}

async function expectHighlight(text: string): Promise<void> {
  await waitFor(() => {
    forceParsing(editor(), editor().state.doc.length, 1000);
    expect(highlightedSpan(text)).not.toBeNull();
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  Object.defineProperty(Range.prototype, "getClientRects", {
    configurable: true,
    value: () => [],
  });
  document.body.replaceChildren();
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mocks.read.mockImplementation(() => Promise.resolve(encoded("const answer = 1;")));
  mocks.readRange.mockResolvedValue({ size: 17 });
  mocks.write.mockResolvedValue(undefined);
  mocks.ask.mockResolvedValue(true);
  useUi.setState({ pushToast: mocks.toast, sessions: [] });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.restoreAllMocks();
});

describe("FileEditor language selection and highlighting", () => {
  it("auto-detects case-insensitively and renders real CodeMirror highlighting", async () => {
    await mountEditor("/tmp/EXAMPLE.JS");
    expect(languageSelect().textContent).toContain("自动（JavaScript）");
    await expectHighlight("const");
    expect(mocks.read).toHaveBeenCalledOnce();
  });

  it("reconfigures highlighting without replacing the document or losing editor state", async () => {
    await mountEditor("/tmp/example.unknown");
    const view = editor();
    expect(view.state.doc.toString()).toBe("const answer = 1;");
    expect(mounted!.container.querySelector(".cm-line span")).toBeNull();

    act(() => {
      view.dispatch({
        changes: { from: view.state.doc.length, insert: "\nconst second = 2;" },
        selection: { anchor: view.state.doc.length + 17 },
      });
    });
    await flush();
    const edited = view.state.doc.toString();
    const cursor = view.state.selection.main.anchor;
    view.scrollDOM.scrollTop = 20;
    const encoding = mounted!.container.querySelector<HTMLButtonElement>('button[aria-label="解码方式"]');
    const eol = mounted!.container.querySelector<HTMLButtonElement>('button[title^="当前换行符"]');
    if (!encoding || !eol) throw new Error("Encoding or EOL control not found");
    const encodingLabel = encoding.textContent;
    // Auto detection shows the detected encoding on the trigger.
    expect(encodingLabel).toContain("UTF-8");
    expect(eol.textContent).toBe("LF");
    expect(mounted!.container.textContent).toContain("未保存");

    chooseLanguage("JavaScript");
    await expectHighlight("const");

    expect(editor()).toBe(view);
    expect(view.dom.isConnected).toBe(true);
    expect(view.state.doc.toString()).toBe(edited);
    expect(view.state.selection.main.anchor).toBe(cursor);
    expect(view.scrollDOM.scrollTop).toBe(20);
    expect(mounted!.container.textContent).toContain("未保存");
    expect(encoding.textContent).toBe(encodingLabel);
    expect(eol.textContent).toBe("LF");
    expect(mocks.read).toHaveBeenCalledOnce();

    act(() => {
      expect(undo(view)).toBe(true);
    });
    expect(view.state.doc.toString()).toBe("const answer = 1;");
    act(() => {
      expect(redo(view)).toBe(true);
    });
    expect(view.state.doc.toString()).toBe(edited);
    expect(mounted!.container.textContent).toContain("未保存");

    chooseLanguage("纯文本");
    await waitFor(() => expect(mounted!.container.querySelector(".cm-line span")).toBeNull());
    expect(view.state.doc.toString()).toBe(edited);
    expect(mounted!.container.textContent).toContain("未保存");

    const save = mounted!.container.querySelector<HTMLButtonElement>('button[title="保存"]');
    if (!save) throw new Error("Save button not found");
    expect(save.disabled).toBe(false);
    click(save);
    await waitFor(() => expect(mocks.write).toHaveBeenCalledOnce());
    // The editor no longer asks the host to keep a .nexterm-bak copy: the backup flag is not passed (default off).
    expect(mocks.write.mock.calls[0]?.[3]).toBeUndefined();
    expect(mounted!.container.textContent).not.toContain("nexterm-bak");
    expect(mocks.read).toHaveBeenCalledOnce();
  });

  it("resets a manual override on path changes and does not carry it into a new editor", async () => {
    await mountEditor("/tmp/example.txt");
    const view = editor();
    chooseLanguage("Python");
    expect(languageSelect().textContent).toBe("Python");

    act(() => {
      mounted!.root.render(tree("/tmp/example.JS"));
    });
    await expectHighlight("const");
    expect(languageSelect().textContent).toContain("自动（JavaScript）");
    expect(editor()).toBe(view);
    expect(view.state.doc.toString()).toBe("const answer = 1;");
    expect(mocks.read).toHaveBeenCalledOnce();

    chooseLanguage("纯文本");
    expect(languageSelect().textContent).toBe("纯文本");
    mounted!.unmount();
    mounted = undefined;
    await mountEditor("/tmp/example.JS");
    expect(languageSelect().textContent).toContain("自动（JavaScript）");
    await expectHighlight("const");
  });
});
