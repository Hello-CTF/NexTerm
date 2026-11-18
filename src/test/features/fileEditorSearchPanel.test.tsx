/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { EditorView } from "@codemirror/view";
import { click, flush, mount, setInputValue, waitFor, type MountedView } from "./reactTestUtils";

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


function panel(): HTMLElement {
  const element = mounted?.container.querySelector<HTMLElement>(".nx-cm-search");
  if (!element) throw new Error("Search panel not found");
  return element;
}

function field(name: string): HTMLInputElement {
  const input = panel().querySelector<HTMLInputElement>(`input[name="${name}"]`);
  if (!input) throw new Error(`Search field ${name} not found`);
  return input;
}

function button(label: string): HTMLButtonElement {
  const found = [...panel().querySelectorAll<HTMLButtonElement>("button")].find(
    (b) => b.getAttribute("aria-label") === label || b.textContent === label,
  );
  if (!found) throw new Error(`Search panel button ${label} not found`);
  return found;
}

function status(): string {
  return panel().querySelector('[data-testid="editor-search-status"]')?.textContent ?? "";
}

beforeEach(() => {
  vi.clearAllMocks();
  Object.defineProperty(Range.prototype, "getClientRects", {
    configurable: true,
    value: () => [],
  });
  document.body.replaceChildren();
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mocks.read.mockImplementation(() => Promise.resolve(encoded("alpha beta alpha gamma ALPHA")));
  mocks.readRange.mockResolvedValue({ size: 28 });
  mocks.write.mockResolvedValue(undefined);
  mocks.ask.mockResolvedValue(true);
  useUi.setState({ pushToast: mocks.toast, sessions: [] });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.restoreAllMocks();
});

describe("file editor search panel", () => {
  it("opens from the toolbar with NexTerm controls, counts matches and steps through them", async () => {
    await mountEditor("/tmp/file.txt");
    click(mounted!.container.querySelector<HTMLButtonElement>('button[aria-label="查找 / 替换"]')!);
    await waitFor(() => expect(document.activeElement).toBe(field("search")));
    expect(mounted!.container.querySelector(".cm-search")).toBeNull();

    setInputValue(field("search"), "alpha");
    await waitFor(() => expect(status()).toBe("3 个匹配"));

    click(button("下一个匹配"));
    await waitFor(() => expect(status()).toBe("1/3"));
    expect(editor().state.sliceDoc(editor().state.selection.main.from, editor().state.selection.main.to)).toBe("alpha");

    click(button("区分大小写"));
    await waitFor(() => expect(status()).toBe("1/2"));
    expect(button("区分大小写").getAttribute("aria-pressed")).toBe("true");

    click(button("正则表达式"));
    setInputValue(field("search"), "(");
    await waitFor(() => expect(status()).toBe("正则表达式无效"));
  });

  it("replaces all matches from the replace row and closes back to the editor", async () => {
    await mountEditor("/tmp/file.txt");
    click(mounted!.container.querySelector<HTMLButtonElement>('button[aria-label="查找 / 替换"]')!);
    await waitFor(() => expect(field("search")).toBeDefined());
    setInputValue(field("search"), "alpha");
    click(button("切换替换"));
    await waitFor(() => expect(field("replace")).toBeDefined());
    setInputValue(field("replace"), "omega");
    click(button("全部替换"));
    await waitFor(() => expect(editor().state.doc.toString()).toBe("omega beta omega gamma omega"));

    click(button("关闭搜索"));
    await flush();
    expect(mounted!.container.querySelector(".nx-cm-search")).toBeNull();
  });
});
