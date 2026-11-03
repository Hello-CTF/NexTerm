/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, clickButton, flush, mount, setInputValue, waitForRendered, type MountedView } from "./reactTestUtils";

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

const MULTI = {
  id: "m1", name: "deploy app", body: "git pull\nsystemctl restart app", groupId: null, sort: 1,
  createdAt: 1, updatedAt: 1, description: "pull and restart",
  steps: [
    { id: "st-1", name: "pull", description: "fetch code", command: "git pull" },
    { id: "st-2", name: "restart", command: "systemctl restart app", confirm: true, interactive: true },
  ],
};
const SINGLE = {
  id: "s1", name: "disk usage", body: "df -h", groupId: null, sort: 2,
  createdAt: 1, updatedAt: 1, description: "", steps: [],
};

let mounted: MountedView | undefined;

function ui(): HTMLElement {
  return mounted!.container;
}

function byLabel<T extends HTMLElement>(label: string): T {
  const el = document.body.querySelector<T>(`[aria-label="${label}"]`);
  if (!el) throw new Error(`not found: ${label}`);
  return el;
}

function seedTerminal() {
  useUi.setState({
    workspaces: [{
      id: "ws-1", kind: "session", title: "W",
      panes: [{ id: "pane-1", tabs: [{ id: "tab-1", kind: "terminal", title: "T", tabId: "term-1", closable: true }], activeTabId: "tab-1" }],
      activePaneId: "pane-1", closable: true,
    }],
    activeWorkspaceId: "ws-1",
  });
}

async function openPanel() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(createElement(QueryClientProvider, { client }, createElement(SnippetsPanel, { onClose: vi.fn() })));
  await waitForRendered(() => expect(ui().textContent).toContain("disk usage"));
}

beforeEach(() => {
  vi.resetAllMocks();
  document.body.replaceChildren();
  mocks.snippetList.mockResolvedValue([structuredClone(MULTI), structuredClone(SINGLE)]);
  mocks.snippetCreate.mockResolvedValue({ id: "new" });
  mocks.snippetUpdate.mockResolvedValue(undefined);
  mocks.ask.mockResolvedValue(true);
  mocks.write.mockResolvedValue(undefined);
  useUi.setState({ workspaces: [], activeWorkspaceId: null, toasts: [], pushToast: mocks.toast });
  seedTerminal();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  document.body.replaceChildren();
});

describe("multi-step snippets in the snippets panel", () => {
  it("marks a multi-step snippet with its step count and description, and drops the standalone flow entry", async () => {
    await openPanel();
    const rows = [...ui().querySelectorAll("[data-snippet-row]")];
    expect(rows[0].textContent).toContain("2 步");
    expect(rows[0].textContent).toContain("pull and restart");
    expect(rows[1].textContent).not.toContain("步");
    expect(ui().textContent).not.toContain("执行流程");
    expect(byLabel("在多台主机执行片段「disk usage」")).toBeTruthy();
    expect(byLabel("在多台主机执行片段「deploy app」")).toBeTruthy();
  });

  it("disables insert for a multi-step snippet and executes its steps in order", async () => {
    await openPanel();
    const insert = byLabel<HTMLButtonElement>("插入片段「deploy app」");
    expect(insert.disabled).toBe(true);
    expect(insert.title).toContain("多步片段只能执行");
    expect(byLabel<HTMLButtonElement>("插入片段「disk usage」").disabled).toBe(false);

    click(byLabel("执行片段「deploy app」"));
    await waitForRendered(() => expect(mocks.write).toHaveBeenCalledTimes(2));
    const sent = mocks.write.mock.calls.map(([, data]) => new TextDecoder().decode(data as Uint8Array));
    expect(sent).toEqual(["git pull\r", "systemctl restart app\r"]);
    // The second step is marked for confirmation.
    expect(mocks.ask).toHaveBeenCalledTimes(1);
  });

  it("sends the original steps and description back when an existing multi-step snippet is renamed", async () => {
    await openPanel();
    click(byLabel("编辑片段「deploy app」"));
    await flush();
    setInputValue(document.body.querySelector<HTMLInputElement>("#snippet-name")!, "deploy app v2");
    clickButton(document.body, "保存");
    await flush();
    expect(mocks.snippetUpdate).toHaveBeenCalled();
    const [id, name, body, patch] = mocks.snippetUpdate.mock.calls[0];
    expect(id).toBe("m1");
    expect(name).toBe("deploy app v2");
    expect(body).toBe("git pull\nsystemctl restart app");
    expect(patch.description).toBe("pull and restart");
    expect(patch.steps).toEqual(MULTI.steps);
  });

  it("saves a new multi-step snippet with description and per-step flags", async () => {
    await openPanel();
    clickButton(ui(), "新建片段");
    await flush();
    setInputValue(document.body.querySelector<HTMLInputElement>("#snippet-name")!, "release");
    setInputValue(document.body.querySelector<HTMLInputElement>("#snippet-description")!, "ship it");
    setInputValue(document.body.querySelector<HTMLTextAreaElement>("#snippet-body")!, "make build");
    clickButton(document.body, "改为多步");
    await flush();
    // The existing single command becomes step 1.
    expect(byLabel<HTMLTextAreaElement>("步骤 1 命令").value).toBe("make build");
    clickButton(document.body, "添加步骤");
    await flush();
    setInputValue(byLabel<HTMLInputElement>("步骤 2 名称"), "deploy");
    setInputValue(byLabel<HTMLTextAreaElement>("步骤 2 命令"), "make deploy");
    click(byLabel("步骤 2 执行前确认"));
    click(byLabel("步骤 2 启用脚本交互指令"));
    clickButton(document.body, "保存");
    await flush();
    expect(mocks.snippetCreate).toHaveBeenCalled();
    const [name, body, , , content] = mocks.snippetCreate.mock.calls[0];
    expect(name).toBe("release");
    expect(body).toBe("make build\nmake deploy");
    expect(content.description).toBe("ship it");
    expect(content.steps).toHaveLength(2);
    expect(content.steps[0]).toMatchObject({ command: "make build" });
    expect(content.steps[0].confirm).toBeFalsy();
    expect(content.steps[1]).toMatchObject({ name: "deploy", command: "make deploy", confirm: true, interactive: true });
    expect(content.steps[0].id).toBeTruthy();
    expect(content.steps[1].id).not.toBe(content.steps[0].id);
  });

  it("rejects a multi-step snippet with an empty step command", async () => {
    await openPanel();
    click(byLabel("编辑片段「deploy app」"));
    await flush();
    setInputValue(byLabel<HTMLTextAreaElement>("步骤 2 命令"), "  ");
    const save = [...document.body.querySelectorAll("button")].find((b) => b.textContent?.trim() === "保存")!;
    expect(save.disabled).toBe(true);
    expect(mocks.snippetUpdate).not.toHaveBeenCalled();
  });

  it("saves a single-command snippet without steps", async () => {
    await openPanel();
    click(byLabel("编辑片段「disk usage」"));
    await flush();
    setInputValue(document.body.querySelector<HTMLInputElement>("#snippet-name")!, "disk");
    clickButton(document.body, "保存");
    await flush();
    expect(mocks.snippetUpdate).toHaveBeenCalled();
    const patch = mocks.snippetUpdate.mock.calls[0][3] ?? {};
    expect(patch.steps ?? []).toEqual([]);
  });

  it("moves and deletes steps in the editor", async () => {
    await openPanel();
    click(byLabel("编辑片段「deploy app」"));
    await flush();
    click(byLabel("上移步骤 2"));
    await flush();
    expect(byLabel<HTMLTextAreaElement>("步骤 1 命令").value).toBe("systemctl restart app");
    click(byLabel("删除步骤 1"));
    await flush();
    expect(byLabel<HTMLTextAreaElement>("步骤 1 命令").value).toBe("git pull");
    expect(document.body.querySelector('[aria-label="步骤 2 命令"]')).toBeNull();
  });
});
