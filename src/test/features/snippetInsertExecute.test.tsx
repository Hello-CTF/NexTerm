/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement, useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  flush,
  mount,
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
import { useUi, type AppTab } from "../../app/store";

const SNIPPETS = [
  { id: "sn1", name: "check container status", body: "docker ps", groupId: null, sort: 1 },
  { id: "sn2", name: "disk usage", body: "df -h", groupId: null, sort: 2 },
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

function buttonByLabel(container: ParentNode, label: string): HTMLButtonElement {
  const button = [...container.querySelectorAll("button")].find(
    (candidate) => candidate.getAttribute("aria-label") === label,
  );
  if (!button) throw new Error(`Button not found by aria-label: ${label}`);
  return button;
}

function seedTerminalTab(tabOverrides: Partial<AppTab> = {}) {
  const tab: AppTab = {
    id: "tab-1",
    kind: "terminal",
    title: "Terminal",
    tabId: "term-1",
    closable: true,
    ...tabOverrides,
  };
  useUi.setState({
    workspaces: [
      {
        id: "ws-1",
        kind: "session",
        title: "Workspace",
        panes: [{ id: "pane-1", tabs: [tab], activeTabId: tab.id }],
        activePaneId: "pane-1",
        closable: true,
      },
    ],
    activeWorkspaceId: "ws-1",
  });
}

function writtenText(callIndex = 0): string {
  const data = mocks.write.mock.calls[callIndex]?.[1] as Uint8Array;
  return new TextDecoder().decode(data);
}

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.resetAllMocks();
  document.body.replaceChildren();
  document.body.style.overflow = "";
  mocks.snippetList.mockResolvedValue(SNIPPETS.map((s) => ({ ...s })));
  mocks.ask.mockResolvedValue(true);
  mocks.write.mockResolvedValue(undefined);
  useUi.setState({
    workspaces: [],
    activeWorkspaceId: null,
    toasts: [],
    pushToast: mocks.toast,
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  useUi.setState({ workspaces: [], activeWorkspaceId: null, toasts: [] });
  document.body.style.overflow = "";
  document.body.replaceChildren();
});

describe("snippet insert and execute actions", () => {
  it("renders per-snippet insert and execute buttons with matching titles and aria labels", async () => {
    mounted = mountPanel(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("check container status"));

    const expectedTitles: Record<string, string> = {
      插入: "插入到当前终端",
      执行: "在当前终端执行",
    };
    for (const name of ["check container status", "disk usage"]) {
      for (const action of ["插入", "执行"]) {
        const button = buttonByLabel(mounted.container, `${action}片段「${name}」`);
        expect(button).toBeInstanceOf(HTMLButtonElement);
        expect(button.title).toBe(expectedTitles[action]);
        button.focus();
        expect(document.activeElement).toBe(button);
        button.blur();
      }
    }
  });

  it("insert writes the body verbatim without submitting", async () => {
    seedTerminalTab();
    const onClosed = vi.fn();
    mounted = mountPanel(createElement(SnippetsHost, { onClosed }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("check container status"));

    click(buttonByLabel(mounted.container, "插入片段「check container status」"));
    await flush();

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.write).toHaveBeenCalledTimes(1);
    expect(mocks.write.mock.calls[0]?.[0]).toBe("term-1");
    expect(writtenText()).toBe("docker ps");
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("已插入"));
    expect(onClosed).toHaveBeenCalledTimes(1);
  });

  it("execute appends a carriage return so the command is submitted", async () => {
    seedTerminalTab();
    mounted = mountPanel(createElement(SnippetsHost));
    await waitFor(() => expect(mounted!.container.textContent).toContain("check container status"));

    click(buttonByLabel(mounted.container, "执行片段「check container status」"));
    await flush();

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(writtenText()).toBe("docker ps\r");
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("已执行"));
  });

  it("execute keeps a body that already ends with a newline instead of adding another", async () => {
    seedTerminalTab();
    mocks.snippetList.mockResolvedValue([
      { id: "sn3", name: "restart service", body: "systemctl restart app\n", groupId: null, sort: 3 },
    ]);
    mounted = mountPanel(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("restart service"));

    click(buttonByLabel(mounted.container, "执行片段「restart service」"));
    await flush();

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(writtenText()).toBe("systemctl restart app\n");
  });

  it("insert asks before writing a multi-line snippet and aborts on cancel", async () => {
    seedTerminalTab();
    mocks.snippetList.mockResolvedValue([
      { id: "sn4", name: "two lines", body: "echo a\necho b", groupId: null, sort: 4 },
    ]);
    const onClosed = vi.fn();
    mounted = mountPanel(createElement(SnippetsHost, { onClosed }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("two lines"));

    mocks.ask.mockResolvedValueOnce(false);
    click(buttonByLabel(mounted.container, "插入片段「two lines」"));
    await flush();

    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("仍要插入吗"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.write).not.toHaveBeenCalled();
    expect(onClosed).not.toHaveBeenCalled();
  });

  it("execute asks with execute wording for control characters and submits after confirm", async () => {
    seedTerminalTab();
    mocks.snippetList.mockResolvedValue([
      { id: "sn5", name: "completion", body: "docker ps\t", groupId: null, sort: 5 },
    ]);
    mounted = mountPanel(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("completion"));

    click(buttonByLabel(mounted.container, "执行片段「completion」"));
    await flush();

    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("仍要执行吗"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(writtenText()).toBe("docker ps\t\r");
  });

  it("hints at opening a terminal first when no writable terminal exists", async () => {
    const onClosed = vi.fn();
    mounted = mountPanel(createElement(SnippetsHost, { onClosed }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("check container status"));

    click(buttonByLabel(mounted.container, "插入片段「check container status」"));
    await flush();

    expect(mocks.write).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith(
      "info",
      expect.stringContaining("请先在当前工作区打开一个终端"),
    );
    expect(onClosed).not.toHaveBeenCalled();
  });

  it("hints that the terminal has ended when the active tab exited", async () => {
    seedTerminalTab({ exited: true });
    mounted = mountPanel(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("check container status"));

    click(buttonByLabel(mounted.container, "执行片段「check container status」"));
    await flush();

    expect(mocks.write).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith(
      "info",
      expect.stringContaining("当前终端已失效或已结束"),
    );
  });

  it("explains missing control when the write is rejected as not_controller", async () => {
    seedTerminalTab();
    mocks.write.mockRejectedValue({ code: "not_controller", message: "not_controller" });
    const onClosed = vi.fn();
    mounted = mountPanel(createElement(SnippetsHost, { onClosed }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("check container status"));

    click(buttonByLabel(mounted.container, "执行片段「check container status」"));
    await flush();

    expect(mocks.toast).toHaveBeenCalledWith(
      "error",
      expect.stringContaining("请先在终端上接管控制"),
    );
    expect(onClosed).not.toHaveBeenCalled();
  });
});
