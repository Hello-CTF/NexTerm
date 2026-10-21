/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, clickButton, flush, flushMacrotask, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";
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
  prepareBroadcast: vi.fn(),
  write: vi.fn(),
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
  assetApi: {}, dbApi: {}, sessionApi: {}, vaultApi: {}, syncApi: {},
  terminalApi: { prepareBroadcast: mocks.prepareBroadcast, write: mocks.write },
}));

import { useUi } from "../../app/store";
import { projectFocusForGroup } from "../../app/projectFocus";
import { ProjectTemplatePanel } from "../../features/explorer/ProjectTemplatePanel";
import { executeProjectTemplateCommands, type ProjectCommandDependencies } from "../../features/terminal/projectTemplateCommands";

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
    sessions: [{ id: "session-old", assetId: "asset-1", name: "host", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }],
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

describe("production confirmation for template startup commands", () => {
  const typedChallenge = {
    code: "needs_confirm",
    detail: {
      kind: "broadcast_production_confirmation",
      targetSetHash: "a".repeat(64),
      inputHash: "b".repeat(64),
      requiredEnvironmentNames: ["production"],
      productionTargets: [
        { tabId: "kernel-old", sessionId: "session-old", sessionName: "one", hostname: "one.prod.internal", environment: "production" },
      ],
    },
  };

  function legacyChallenge() {
    return {
      code: "needs_confirm",
      detail: {
        kind: "broadcast_production_confirmation",
        targetSetHash: "a".repeat(64),
        inputHash: "b".repeat(64),
        productionTargets: [
          { tabId: "kernel-old", sessionId: "session-old", sessionName: "one", hostname: "one.prod.internal", environment: "production" },
        ],
      },
    };
  }

  function commandTemplate(): ProjectTemplate {
    return { ...savedTemplate(), startupCommands: { tab: "docker compose up -d" } };
  }

  // Route the panel's open through the real command pipeline so the dialog's
  // typed names must reach the actual retry confirmation.
  function openThroughRealCommands(): void {
    mocks.open.mockImplementation(async (
      template: ProjectTemplate,
      options?: { commandDependencies?: Partial<ProjectCommandDependencies> },
    ) => {
      const commands = await executeProjectTemplateCommands(template, new Map([["tab", "tab"]]), options?.commandDependencies);
      return { workspaces: {}, forwards: [], commands };
    });
  }

  async function openTemplateWithChallenge(): Promise<void> {
    mocks.list.mockResolvedValue([commandTemplate()]);
    mounted = mount(createElement(ProjectTemplatePanel, { groups }));
    clickButton(mounted.container, "项目模板（0）");
    await flushUntil(() => mounted!.container.textContent?.includes("Daily") ?? false);
    clickButton(mounted.container, "打开");
    await flushUntil(() => document.body.querySelector('[role="dialog"][aria-label="确认生产环境执行"]') !== null);
  }

  function productionDialog(): HTMLElement {
    const dialog = document.body.querySelector<HTMLElement>('[role="dialog"][aria-label="确认生产环境执行"]');
    if (!dialog) throw new Error("production confirmation dialog not found");
    return dialog;
  }

  function environmentInput(): HTMLInputElement {
    const input = productionDialog().querySelector<HTMLInputElement>('input[aria-label="输入环境名 one.prod.internal"]');
    if (!input) throw new Error("environment input not found");
    return input;
  }

  function confirmButton(): HTMLButtonElement {
    const button = [...productionDialog().querySelectorAll<HTMLButtonElement>("button")].find(
      (candidate) => candidate.textContent?.trim() === "确认执行（1）",
    );
    if (!button) throw new Error("confirm button not found");
    return button;
  }

  // Clicks that start an async command continuation: keep an act scope open
  // across a macrotask so its state updates land inside act.
  async function clickAndFlush(element: Element): Promise<void> {
    await act(async () => {
      element.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await flushMacrotask();
    });
  }

  it("passes the typed environment names verbatim to the retried startup command", async () => {
    openThroughRealCommands();
    mocks.prepareBroadcast.mockRejectedValueOnce(typedChallenge).mockResolvedValue(undefined);
    mocks.write.mockResolvedValue(undefined);
    await openTemplateWithChallenge();

    const confirm = confirmButton();
    expect(confirm.disabled).toBe(true);
    setInputValue(environmentInput(), "production");
    expect(confirm.disabled).toBe(false);
    await clickAndFlush(confirm);

    await flushUntil(() => mocks.write.mock.calls.length === 1);
    const productionConfirmation = {
      confirmed: true,
      targetSetHash: "a".repeat(64),
      inputHash: "b".repeat(64),
      environmentNames: ["production"],
    };
    expect(mocks.prepareBroadcast).toHaveBeenCalledTimes(2);
    expect(mocks.prepareBroadcast.mock.calls[1][2]).toEqual(productionConfirmation);
    expect(mocks.write.mock.calls[0][2]).toEqual({ targetTabIds: ["kernel-old"], productionConfirmation });
    await flushUntil(() => useUi.getState().toasts.some((toast) => toast.text.includes("0 条命令失败")));
  });

  it("resolves null on cancel and never writes the startup command", async () => {
    openThroughRealCommands();
    mocks.prepareBroadcast.mockRejectedValueOnce(typedChallenge);
    await openTemplateWithChallenge();

    await clickAndFlush([...productionDialog().querySelectorAll<HTMLButtonElement>("button")].find(
      (candidate) => candidate.textContent?.trim() === "取消",
    )!);
    await flushUntil(() => document.body.querySelector('[role="dialog"][aria-label="确认生产环境执行"]') === null);
    expect(mocks.write).not.toHaveBeenCalled();
    await flushUntil(() => useUi.getState().toasts.some((toast) => toast.text.includes("1 条命令已取消")));
  });

  it("keeps the acknowledge flow for legacy challenges without required names", async () => {
    openThroughRealCommands();
    mocks.prepareBroadcast.mockRejectedValueOnce(legacyChallenge()).mockResolvedValue(undefined);
    mocks.write.mockResolvedValue(undefined);
    await openTemplateWithChallenge();

    const acknowledgement = productionDialog().querySelector<HTMLInputElement>('input[aria-label="确认生产环境风险"]');
    if (!acknowledgement) throw new Error("legacy acknowledgement checkbox not found");
    const confirm = confirmButton();
    expect(confirm.disabled).toBe(true);
    click(acknowledgement);
    expect(confirm.disabled).toBe(false);
    await clickAndFlush(confirm);

    await flushUntil(() => mocks.write.mock.calls.length === 1);
    const productionConfirmation = { confirmed: true, targetSetHash: "a".repeat(64), inputHash: "b".repeat(64) };
    expect(mocks.prepareBroadcast.mock.calls[1][2]).toEqual(productionConfirmation);
    expect(mocks.write.mock.calls[0][2]).toEqual({ targetTabIds: ["kernel-old"], productionConfirmation });
  });
});
