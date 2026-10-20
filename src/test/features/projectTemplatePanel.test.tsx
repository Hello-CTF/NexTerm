/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { clickButton, flush, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";
import type { ProjectTemplate } from "../../app/projectTemplates";
import type { Workspace } from "../../app/store";
import type { ProjectInventory } from "../../app/projectFocus";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  create: vi.fn(),
  rename: vi.fn(),
  delete: vi.fn(),
  open: vi.fn(),
  close: vi.fn(),
}));

const dialogs = vi.hoisted(() => ({ ask: vi.fn(), promptText: vi.fn() }));
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: dialogs.ask, promptText: dialogs.promptText };
});

vi.mock("../../app/projectTemplateStore", () => ({
  createProjectTemplateRepository: () => ({
    list: mocks.list,
    create: mocks.create,
    rename: mocks.rename,
    delete: mocks.delete,
  }),
}));
vi.mock("../../app/projectTemplateActions", () => ({
  openProjectTemplate: mocks.open,
  closeProject: mocks.close,
}));
vi.mock("../../ipc/commands", () => ({
  assetApi: {}, dbApi: {}, sessionApi: {}, terminalApi: {}, vaultApi: {}, syncApi: {},
}));

import { useUi } from "../../app/store";
import { projectFocusForGroup } from "../../app/projectFocus";
import { ProjectTemplatePanel } from "../../features/explorer/ProjectTemplatePanel";

const groups = [
  { id: "project", parentId: null, name: "Project", sort: 0, createdAt: 1, updatedAt: 1 },
  { id: "other", parentId: null, name: "Other", sort: 0, createdAt: 1, updatedAt: 1 },
];

const inventory: ProjectInventory = {
  assets: [{
    id: "asset-1", groupId: "project", kind: "ssh", name: "host", host: null, port: null,
    username: null, authKind: null, keyPath: null, credId: null, options: {}, tags: "", note: "",
    sort: 0, createdAt: 1, updatedAt: 1, deletedAt: null, builtin: false,
  }],
  groups,
};

function workspace(): Workspace {
  return {
    id: "ws", kind: "session", title: "host", sessionId: "session-old", assetId: "asset-1", assetKind: "ssh",
    panes: [{
      id: "pane",
      tabs: [{ id: "tab", kind: "terminal", title: "logs", sessionId: "session-old", tabId: "kernel-old", closable: true }],
      activeTabId: "tab",
    }],
    activePaneId: "pane", splitRatio: 0.5, closable: true,
  };
}

function savedTemplate(): ProjectTemplate {
  return {
    v: 1, id: "template-1", name: "Daily", project: projectFocusForGroup("other"), createdAt: 1, updatedAt: 1,
    workspaces: [{ ...workspace(), sessionId: undefined, restorePending: true }],
    startupCommands: {},
  };
}

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.list.mockResolvedValue([]);
  mocks.create.mockImplementation(async (template) => template);
  mocks.open.mockResolvedValue({ workspaces: {}, forwards: [], commands: [] });
  mocks.close.mockResolvedValue([]);
  useUi.setState({
    workspaces: [workspace()],
    activeWorkspaceId: "ws",
    activeProject: projectFocusForGroup("project"),
    projectInventory: inventory,
    accountEpoch: 0,
    toasts: [],
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("project template panel", () => {
  it("saves the selected project with per-tab commands and no runtime identifiers", async () => {
    mounted = mount(createElement(ProjectTemplatePanel, { groups }));
    clickButton(mounted.container, "保存当前");
    const command = mounted.container.querySelector<HTMLInputElement>('input[aria-label="启动命令：host / logs"]')!;
    setInputValue(command, "tail -f app.log");
    clickButton(mounted.container, "保存模板");
    await flushUntil(() => {
      const saveCurrent = [...mounted!.container.querySelectorAll("button")]
        .find((button) => button.textContent?.trim() === "保存当前");
      return mocks.create.mock.calls.length === 1 &&
        !mounted!.container.textContent?.includes("模板名称") && saveCurrent?.disabled === false;
    });

    const template = mocks.create.mock.calls[0][0] as ProjectTemplate;
    expect(template.startupCommands).toEqual({ tab: "tail -f app.log" });
    expect(template.workspaces[0].sessionId).toBeUndefined();
    expect(template.workspaces[0].panes[0].tabs[0].tabId).toBeUndefined();
    expect(mounted.container.textContent).not.toContain("模板名称");
  });

  it("lists, opens and closes without forcing focus, with an explicit focus action", async () => {
    mocks.list.mockResolvedValue([savedTemplate()]);
    mounted = mount(createElement(ProjectTemplatePanel, { groups }));
    clickButton(mounted.container, "项目模板（0）");
    await flushUntil(() => mounted!.container.textContent?.includes("Daily") ?? false);

    clickButton(mounted.container, "打开");
    await flushUntil(() => mocks.open.mock.calls.length === 1);
    await flush();
    expect(useUi.getState().activeProject).toBe(projectFocusForGroup("project"));
    clickButton(mounted.container, "关闭项目");
    await flushUntil(() => mocks.close.mock.calls.length === 1);
    await flush();
    expect(mocks.close.mock.calls[0][0]).toBe(projectFocusForGroup("project"));
    clickButton(mounted.container, "聚焦");
    expect(useUi.getState().activeProject).toBe(projectFocusForGroup("other"));
  });

  it("does not delete when A's confirmation resolves after an account switch", async () => {
    mocks.list.mockResolvedValue([savedTemplate()]);
    mounted = mount(createElement(ProjectTemplatePanel, { groups }));
    clickButton(mounted.container, "项目模板（0）");
    await flushUntil(() => mounted!.container.textContent?.includes("Daily") ?? false);

    let resolveAsk!: (value: boolean) => void;
    dialogs.ask.mockImplementation(() => new Promise((resolve) => {
      resolveAsk = resolve;
    }));
    clickButton(mounted.container, "删除");
    await flushUntil(() => dialogs.ask.mock.calls.length === 1);

    // Account switch: A's panel is replaced; A's late confirmation must never reach a repository.
    act(() => {
      useUi.getState().advanceAccountEpoch();
    });
    resolveAsk(true);
    await flush();

    expect(mocks.delete).not.toHaveBeenCalled();
    expect(mocks.rename).not.toHaveBeenCalled();
    expect(mounted.container.textContent).toContain("Daily");
  });

  it("does not rename when A's prompt resolves after an account switch", async () => {
    mocks.list.mockResolvedValue([savedTemplate()]);
    mounted = mount(createElement(ProjectTemplatePanel, { groups }));
    clickButton(mounted.container, "项目模板（0）");
    await flushUntil(() => mounted!.container.textContent?.includes("Daily") ?? false);

    let resolvePrompt!: (value: string | null) => void;
    dialogs.promptText.mockImplementation(() => new Promise((resolve) => {
      resolvePrompt = resolve;
    }));
    clickButton(mounted.container, "重命名");
    await flushUntil(() => dialogs.promptText.mock.calls.length === 1);

    act(() => {
      useUi.getState().advanceAccountEpoch();
    });
    resolvePrompt("Renamed");
    await flush();

    expect(mocks.rename).not.toHaveBeenCalled();
    expect(mounted.container.textContent).toContain("Daily");
  });
});
