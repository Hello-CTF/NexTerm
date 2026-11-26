/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { EditorView } from "@codemirror/view";
import { click, mount, waitFor, type MountedView } from "./reactTestUtils";

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

import { FileEditor } from "../../features/files/FileEditor";
import { requestFileReveal } from "../../features/files/fileReveal";
import { useUi } from "../../app/store";

const PATH = "/srv/app/main.go";
const DOC = "package main\n\nfunc main() {\n\tprintln(1)\n}\n";

function encoded(text: string) {
  const bytes = new TextEncoder().encode(text);
  let binary = "";
  bytes.forEach((byte) => {
    binary += String.fromCharCode(byte);
  });
  return { path: PATH, size: bytes.length, contentBase64: btoa(binary) };
}

let mounted: MountedView | undefined;

async function mountEditor(): Promise<EditorView> {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(createElement(QueryClientProvider, { client }, createElement(FileEditor, { sessionId: "s", path: PATH })));
  await waitFor(() => expect(mounted!.container.querySelector(".cm-editor")).not.toBeNull());
  return EditorView.findFromDOM(mounted!.container.querySelector<HTMLElement>(".cm-editor")!)!;
}

function openMenu(): void {
  const content = mounted!.container.querySelector<HTMLElement>(".cm-content")!;
  act(() => {
    content.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 10, clientY: 10 }));
  });
}

function menuItem(label: string): HTMLElement {
  const found = [...document.querySelectorAll<HTMLElement>("[role='menuitem']")].find((item) => item.textContent?.includes(label));
  if (!found) throw new Error(`menu item ${label} not found`);
  return found;
}

describe("FileEditor asks the AI about its content", () => {
  beforeEach(() => {
    Object.defineProperty(Range.prototype, "getClientRects", { configurable: true, value: () => [] });
    useUi.setState({ pushToast: vi.fn(), setRightOpen: vi.fn(), aiPendingRefs: [], sessions: [{ id: "s", name: "web-01", kind: "ssh" }] as never });
    mocks.read.mockImplementation(async () => encoded(DOC));
    mocks.readRange.mockImplementation(async () => ({ ...encoded(DOC), contentBase64: "" }));
    mocks.watch.mockResolvedValue({ sessionId: "s", mode: "push", channel: "helper" });
    mocks.unwatch.mockResolvedValue({});
  });

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
    vi.clearAllMocks();
  });

  it("adds the selected lines from the context menu with a file:line label", async () => {
    const view = await mountEditor();
    const from = view.state.doc.line(3).from;
    // Ending at the start of line 5 does not include line 5.
    act(() => view.dispatch({ selection: { anchor: from, head: view.state.doc.line(5).from } }));
    openMenu();
    click(menuItem("向 AI 询问选中内容"));
    const [ref] = useUi.getState().aiPendingRefs;
    expect(ref).toMatchObject({
      kind: "text",
      label: "main.go:3-4",
      detail: `${PATH} · web-01`,
      path: PATH,
      sessionId: "s",
      text: "func main() {\n\tprintln(1)\n",
      selection: true,
      sourceFile: { sessionId: "s", path: PATH, line: 3, endLine: 4 },
    });
    expect(useUi.getState().setRightOpen).toHaveBeenCalledWith(true);
  });

  it("adds the whole file as a path reference when nothing is selected, from the menu or the shortcut", async () => {
    await mountEditor();
    openMenu();
    expect((menuItem("向 AI 询问选中内容") as HTMLButtonElement).disabled).toBe(true);
    click(menuItem("向 AI 询问当前文件"));
    expect(useUi.getState().aiPendingRefs).toEqual([
      expect.objectContaining({ kind: "file", path: PATH, sessionId: "s", label: "main.go", sourceFile: { sessionId: "s", path: PATH } }),
    ]);
    act(() => useUi.setState({ aiPendingRefs: [] }));
    const content = mounted!.container.querySelector<HTMLElement>(".cm-content")!;
    act(() => {
      content.dispatchEvent(
        new KeyboardEvent("keydown", { key: "a", code: "KeyA", ctrlKey: true, shiftKey: true, altKey: true, bubbles: true, cancelable: true }),
      );
    });
    expect(useUi.getState().aiPendingRefs).toEqual([expect.objectContaining({ kind: "file", path: PATH })]);
  });

  it("selects the referenced line range when a chip jumps back", async () => {
    const view = await mountEditor();
    act(() => requestFileReveal("s", PATH, { line: 3, endLine: 4 }));
    const range = view.state.selection.main;
    expect(view.state.doc.sliceString(range.from, range.to)).toBe("func main() {\n\tprintln(1)");
  });
});
