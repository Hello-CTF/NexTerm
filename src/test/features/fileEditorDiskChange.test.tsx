/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { EditorView } from "@codemirror/view";
import {
  click,
  mount,
  waitFor,
  waitForRendered,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  read: vi.fn(),
  readRange: vi.fn(),
  write: vi.fn(),
  watch: vi.fn(),
  unwatch: vi.fn(),
  ask: vi.fn(),
  askChoice: vi.fn(),
  handlers: new Map<string, (payload: unknown) => void>(),
}));

vi.mock("../../ipc/commands", () => ({
  fsApi: {
    read: mocks.read,
    readRange: mocks.readRange,
    write: mocks.write,
    watch: mocks.watch,
    unwatch: mocks.unwatch,
  },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: {},
}));
vi.mock("../../ipc/events", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ipc/events")>()),
  listenEvent: (topic: string, handler: (payload: unknown) => void) => {
    mocks.handlers.set(topic, handler);
    return Promise.resolve(() => mocks.handlers.delete(topic));
  },
}));
vi.mock("../../ui/dialogs", () => ({
  ask: mocks.ask,
  askChoice: mocks.askChoice,
}));

import { FileEditor, fileVersionOf } from "../../features/files/FileEditor";
import { useUi } from "../../app/store";

function encoded(text: string): {
  path: string;
  size: number;
  contentBase64: string;
} {
  const bytes = new TextEncoder().encode(text);
  let binary = "";
  bytes.forEach((byte) => {
    binary += String.fromCharCode(byte);
  });
  return {
    path: "/srv/app/config.yml",
    size: bytes.length,
    contentBase64: btoa(binary),
  };
}

let mounted: MountedView | undefined;
let disk = "";

async function mountEditor(): Promise<EditorView> {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  mounted = mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(FileEditor, {
        sessionId: "s",
        path: "/srv/app/config.yml",
      }),
    ),
  );
  await waitFor(() =>
    expect(mounted!.container.querySelector(".cm-editor")).not.toBeNull(),
  );
  await waitFor(() => expect(mocks.watch).toHaveBeenCalled());
  const element = mounted!.container.querySelector<HTMLElement>(".cm-editor")!;
  return EditorView.findFromDOM(element)!;
}

function hostChanged(names?: string[]): void {
  const handler = mocks.handlers.get("fs://changed");
  if (!handler) throw new Error("no fs://changed listener");
  act(() => handler({ sessionId: "s", dir: "/srv/app", names }));
}

function type(view: EditorView, text: string): void {
  act(() => {
    view.dispatch({ changes: { from: view.state.doc.length, insert: text } });
  });
}

function button(label: string): HTMLButtonElement {
  const found = [
    ...mounted!.container.querySelectorAll<HTMLButtonElement>("button"),
  ].find((b) => b.textContent === label);
  if (!found) throw new Error(`button ${label} not found`);
  return found;
}

async function settle(ms: number): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, ms));
  });
}

function banner(): HTMLElement | null {
  return mounted!.container.querySelector<HTMLElement>(
    '[data-testid="file-disk-change"]',
  );
}

describe("FileEditor follows changes made on the host", () => {
  beforeEach(() => {
    Object.defineProperty(Range.prototype, "getClientRects", {
      configurable: true,
      value: () => [],
    });
    useUi.setState({ pushToast: vi.fn(), sessions: [] });
    disk = "port: 80\n";
    mocks.read.mockImplementation(async () => encoded(disk));
    mocks.readRange.mockImplementation(async () => ({
      ...encoded(disk),
      contentBase64: "",
    }));
    mocks.write.mockResolvedValue(undefined);
    mocks.watch.mockResolvedValue({
      sessionId: "s",
      mode: "push",
      channel: "helper",
    });
    mocks.unwatch.mockResolvedValue({});
    mocks.handlers.clear();
  });

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
    vi.clearAllMocks();
  });

  it("watches the file's directory and takes a host change in place when nothing is unsaved", async () => {
    const view = await mountEditor();
    expect(mocks.watch).toHaveBeenCalledWith("s", expect.any(String), [
      "/srv/app",
    ]);
    disk = "port: 8080\n";
    hostChanged(["other.yml"]);
    await settle(500);
    expect(mocks.read).toHaveBeenCalledOnce();
    hostChanged(["config.yml"]);
    await waitFor(() => expect(view.state.doc.toString()).toBe("port: 8080\n"));
    expect(banner()).toBeNull();
    expect(mounted!.container.textContent).not.toContain("未保存");
  });

  it("asks before replacing unsaved edits and can take the host version", async () => {
    const view = await mountEditor();
    type(view, "debug: true\n");
    disk = "port: 9090\n";
    hostChanged(["config.yml"]);
    await waitForRendered(() => expect(banner()?.dataset.kind).toBe("changed"));
    expect(view.state.doc.toString()).toBe("port: 80\ndebug: true\n");
    click(button("对比差异"));
    const diff = mounted!.container.querySelector(
      '[data-testid="file-disk-diff"]',
    )!.textContent!;
    expect(diff).toContain("- port: 9090");
    expect(diff).toContain("+ debug: true");
    click(button("使用主机上的版本"));
    expect(view.state.doc.toString()).toBe("port: 9090\n");
    expect(banner()).toBeNull();
  });

  it("saves only over the version it read and offers to overwrite after a refused save", async () => {
    const view = await mountEditor();
    type(view, "debug: true\n");
    disk = "port: 81\n";
    mocks.write.mockRejectedValueOnce({
      code: "conflict",
      message: "changed",
      detail: { resource: "path", reason: "changed" },
    });
    await act(async () => {
      click(
        mounted!.container.querySelector<HTMLButtonElement>(
          'button[title="保存"]',
        )!,
      );
    });
    // The refused save reads the file again and hashes it; keep act open while that settles.
    await settle(300);
    expect(mocks.write).toHaveBeenCalledOnce();
    expect(mocks.write.mock.calls[0]?.[6]).toEqual(
      await fileVersionOf(new TextEncoder().encode("port: 80\n")),
    );
    expect(banner()?.dataset.kind).toBe("conflict");
    await act(async () => {
      click(button("用我的版本覆盖"));
    });
    await settle(300);
    expect(mocks.write).toHaveBeenCalledTimes(2);
    expect(mocks.write.mock.calls[1]?.[6]).toEqual(
      await fileVersionOf(new TextEncoder().encode("port: 81\n")),
    );
    expect(banner()).toBeNull();
    expect(mounted!.container.textContent).not.toContain("未保存");
  });

  it("tells the user when the file is gone", async () => {
    await mountEditor();
    mocks.read.mockRejectedValueOnce({ code: "not_found", message: "missing" });
    hostChanged([]);
    await waitForRendered(() => expect(banner()?.dataset.kind).toBe("deleted"));
  });
});
