/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  flush,
  mount,
  setInputValue,
  waitFor,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  snippetList: vi.fn(),
  snippetCreate: vi.fn(),
  snippetUpdate: vi.fn(),
  snippetDelete: vi.fn(),
  write: vi.fn(),
  ask: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask };
});
vi.mock("../../ipc/commands", () => ({
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: { write: mocks.write },
  assetApi: {
    snippetList: mocks.snippetList,
    snippetCreate: mocks.snippetCreate,
    snippetUpdate: mocks.snippetUpdate,
    snippetDelete: mocks.snippetDelete,
  },
}));

import { SnippetsPanel } from "../../features/explorer/SnippetsPanel";
import { useUi } from "../../app/store";

const SNIPPETS = [
  { id: "sn1", name: "看容器状态", body: "docker ps", groupId: null, sort: 1 },
  { id: "sn2", name: "磁盘水位", body: "df -h", groupId: null, sort: 2 },
];

function SnippetsHost({ onClosed }: { onClosed?: () => void }) {
  const [open, setOpen] = useState(true);
  if (!open) return null;
  return (
    <SnippetsPanel
      onClose={() => {
        setOpen(false);
        onClosed?.();
      }}
    />
  );
}

function mountPanel(node: React.ReactNode): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function buttonByTitle(container: ParentNode, title: string): HTMLButtonElement {
  const button = [...container.querySelectorAll("button")].find(
    (candidate) => candidate.title === title,
  );
  if (!button) throw new Error(`Button not found by title: ${title}`);
  return button;
}

function buttonByText(container: ParentNode, text: string): HTMLButtonElement {
  const button = [...container.querySelectorAll("button")].find(
    (candidate) => candidate.textContent?.trim() === text,
  );
  if (!button) throw new Error(`Button not found by text: ${text}`);
  return button;
}

function keyDown(
  target: EventTarget,
  key: string,
  init: KeyboardEventInit = {},
): KeyboardEvent {
  const event = new KeyboardEvent("keydown", {
    key,
    bubbles: true,
    cancelable: true,
    ...init,
  });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

function editorModal(container: ParentNode): HTMLElement {
  const modal = container.querySelectorAll<HTMLElement>(".nx-modal")[1];
  if (!modal) throw new Error("Snippet editor modal not found");
  return modal;
}

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.resetAllMocks();
  document.body.replaceChildren();
  document.body.style.overflow = "";
  mocks.snippetList.mockResolvedValue(SNIPPETS.map((s) => ({ ...s })));
  mocks.ask.mockResolvedValue(true);
  useUi.setState({ toasts: [], pushToast: mocks.toast });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  useUi.setState({ toasts: [] });
  document.body.style.overflow = "";
  document.body.replaceChildren();
});

describe("snippets panel dialog accessibility", () => {
  it("labels the list dialog, moves focus inside, inerts the background and locks scroll", async () => {
    const trigger = document.body.appendChild(document.createElement("button"));
    trigger.textContent = "打开片段";
    trigger.focus();
    mounted = mountPanel(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("看容器状态"));

    const modal = mounted.container.querySelector<HTMLElement>('.nx-modal[role="dialog"]');
    if (!modal) throw new Error("List dialog not found");
    expect(modal.getAttribute("aria-modal")).toBe("true");
    const title = document.getElementById(modal.getAttribute("aria-labelledby") ?? "");
    expect(title?.textContent).toBe("命令片段");
    expect(document.activeElement).toBe(buttonByTitle(mounted.container, "新建片段"));
    expect(trigger.inert).toBe(true);
    expect(document.body.style.overflow).toBe("hidden");
  });

  it("closes the list dialog on Escape, restores focus and unlocks the page", async () => {
    const trigger = document.body.appendChild(document.createElement("button"));
    trigger.textContent = "打开片段";
    trigger.focus();
    mounted = mountPanel(createElement(SnippetsHost));
    await waitFor(() => expect(mounted!.container.textContent).toContain("看容器状态"));

    const modal = mounted.container.querySelector<HTMLElement>('.nx-modal[role="dialog"]');
    if (!modal) throw new Error("List dialog not found");
    keyDown(modal, "Escape");
    await flush();

    expect(mounted.container.querySelector(".nx-modal")).toBeNull();
    expect(document.activeElement).toBe(trigger);
    expect(trigger.inert).toBe(false);
    expect(trigger.hasAttribute("inert")).toBe(false);
    expect(document.body.style.overflow).toBe("");
  });

  it("traps Tab inside the list dialog", async () => {
    mounted = mountPanel(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("看容器状态"));

    const first = buttonByTitle(mounted.container, "新建片段");
    const last = buttonByText(mounted.container, "关闭");
    last.focus();
    keyDown(last, "Tab");
    expect(document.activeElement).toBe(first);
    keyDown(first, "Tab", { shiftKey: true });
    expect(document.activeElement).toBe(last);
  });

  it("opens the editor with focus on the name input and Escape closes only the editor", async () => {
    const onClose = vi.fn();
    mounted = mountPanel(createElement(SnippetsPanel, { onClose }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("看容器状态"));

    const listModal = mounted.container.querySelector<HTMLElement>(".nx-modal");
    click(buttonByTitle(mounted.container, "新建片段"));
    const editor = editorModal(mounted.container);
    expect(editor.getAttribute("role")).toBe("dialog");
    expect(editor.getAttribute("aria-modal")).toBe("true");
    const editorTitle = document.getElementById(editor.getAttribute("aria-labelledby") ?? "");
    expect(editorTitle?.textContent).toBe("新建片段");
    const nameInput = editor.querySelector<HTMLInputElement>("input.nx-input");
    if (!nameInput) throw new Error("Name input not found");
    expect(document.activeElement).toBe(nameInput);
    expect(listModal?.inert).toBe(true);

    keyDown(nameInput, "Escape");
    await flush();

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mounted.container.querySelectorAll(".nx-modal")).toHaveLength(1);
    expect(onClose).not.toHaveBeenCalled();
    expect(listModal?.inert).toBe(false);
    expect(document.activeElement).toBe(buttonByTitle(mounted.container, "新建片段"));
  });

  it("ignores repeated and IME-composing Escape in the editor", async () => {
    mounted = mountPanel(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("看容器状态"));

    click(buttonByTitle(mounted.container, "新建片段"));
    const nameInput = editorModal(mounted.container).querySelector<HTMLInputElement>("input.nx-input");
    if (!nameInput) throw new Error("Name input not found");

    keyDown(nameInput, "Escape", { repeat: true });
    keyDown(nameInput, "Escape", { isComposing: true });
    await flush();
    expect(mounted.container.querySelectorAll(".nx-modal")).toHaveLength(2);
    expect(mocks.ask).not.toHaveBeenCalled();
  });

  it("asks before discarding a dirty editor on Escape, keeping it open on cancel", async () => {
    const onClose = vi.fn();
    mounted = mountPanel(createElement(SnippetsPanel, { onClose }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("看容器状态"));

    click(buttonByTitle(mounted.container, "新建片段"));
    const editor = editorModal(mounted.container);
    const nameInput = editor.querySelector<HTMLInputElement>("input.nx-input");
    if (!nameInput) throw new Error("Name input not found");
    setInputValue(nameInput, "草稿");

    mocks.ask.mockResolvedValueOnce(false);
    keyDown(nameInput, "Escape");
    await flush();
    expect(mocks.ask).toHaveBeenCalledExactlyOnceWith(
      expect.stringContaining("还没保存"),
      expect.objectContaining({ title: "放弃未保存的修改", kind: "warning" }),
    );
    expect(mounted.container.querySelectorAll(".nx-modal")).toHaveLength(2);
    expect(onClose).not.toHaveBeenCalled();

    mocks.ask.mockResolvedValueOnce(true);
    keyDown(nameInput, "Escape");
    await flush();
    expect(mounted.container.querySelectorAll(".nx-modal")).toHaveLength(1);
    expect(onClose).not.toHaveBeenCalled();
  });

  it("asks before discarding a dirty editor on backdrop click without closing the panel", async () => {
    const onClose = vi.fn();
    mounted = mountPanel(createElement(SnippetsPanel, { onClose }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("看容器状态"));

    click(buttonByTitle(mounted.container, "新建片段"));
    const editor = editorModal(mounted.container);
    const nameInput = editor.querySelector<HTMLInputElement>("input.nx-input");
    if (!nameInput) throw new Error("Name input not found");
    setInputValue(nameInput, "草稿");
    const editorOverlay = mounted.container.querySelectorAll<HTMLElement>(".nx-overlay")[1];
    if (!editorOverlay) throw new Error("Editor overlay not found");

    mocks.ask.mockResolvedValueOnce(false);
    click(editorOverlay);
    await flush();
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mounted.container.querySelectorAll(".nx-modal")).toHaveLength(2);
    expect(onClose).not.toHaveBeenCalled();

    mocks.ask.mockResolvedValueOnce(true);
    click(editorOverlay);
    await flush();
    expect(mounted.container.querySelectorAll(".nx-modal")).toHaveLength(1);
    expect(onClose).not.toHaveBeenCalled();
  });

  it("asks before discarding a dirty editor on the cancel button", async () => {
    mounted = mountPanel(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("看容器状态"));

    click(buttonByTitle(mounted.container, "编辑片段"));
    const editor = editorModal(mounted.container);
    const body = editor.querySelector<HTMLTextAreaElement>("textarea");
    if (!body) throw new Error("Body textarea not found");
    setInputValue(body, "docker ps -a");

    mocks.ask.mockResolvedValueOnce(false);
    click(buttonByText(editor, "取消"));
    await flush();
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mounted.container.querySelectorAll(".nx-modal")).toHaveLength(2);

    mocks.ask.mockResolvedValueOnce(true);
    click(buttonByText(editor, "取消"));
    await flush();
    expect(mounted.container.querySelectorAll(".nx-modal")).toHaveLength(1);
  });

  it("contains key events locally so they never reach window-level handlers", async () => {
    mounted = mountPanel(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("看容器状态"));

    const windowSpy = vi.fn();
    window.addEventListener("keydown", windowSpy);
    try {
      const modal = mounted.container.querySelector<HTMLElement>('.nx-modal[role="dialog"]');
      if (!modal) throw new Error("List dialog not found");
      keyDown(modal, "Escape");
      expect(windowSpy).not.toHaveBeenCalled();

      click(buttonByTitle(mounted.container, "新建片段"));
      const nameInput = editorModal(mounted.container).querySelector<HTMLInputElement>("input.nx-input");
      if (!nameInput) throw new Error("Name input not found");
      keyDown(nameInput, "Escape");
      await flush();
      expect(windowSpy).not.toHaveBeenCalled();
    } finally {
      window.removeEventListener("keydown", windowSpy);
    }
  });

  it("traps Tab inside the dirty editor across its enabled controls", async () => {
    mounted = mountPanel(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("看容器状态"));

    click(buttonByTitle(mounted.container, "新建片段"));
    const editor = editorModal(mounted.container);
    const nameInput = editor.querySelector<HTMLInputElement>("input.nx-input");
    const bodyInput = editor.querySelector<HTMLTextAreaElement>("textarea");
    if (!nameInput || !bodyInput) throw new Error("Editor fields not found");
    setInputValue(nameInput, "草稿");
    setInputValue(bodyInput, "docker ps");

    const save = buttonByText(editor, "保存");
    expect(save.disabled).toBe(false);
    save.focus();
    keyDown(save, "Tab");
    expect(document.activeElement).toBe(nameInput);
    keyDown(nameInput, "Tab", { shiftKey: true });
    expect(document.activeElement).toBe(save);
  });
});
