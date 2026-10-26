/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import {
  click,
  clickButton,
  flush,
  flushUntil,
  mountWithClient,
  setInputValue,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  snippetList: vi.fn(),
  snippetCreate: vi.fn(),
  snippetUpdate: vi.fn(),
  snippetDelete: vi.fn(),
  assetGet: vi.fn(),
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
    get: mocks.assetGet,
  },
}));

import { SnippetsPanel } from "../../features/explorer/SnippetsPanel";
import { useUi } from "../../app/store";

const parameterizedSnippet = {
  id: "sn-vars",
  name: "parameterized",
  body: "run {{service=nginx}} on {{asset.name}} {{asset.host}} {{asset.tags}}: {{script|multiline}}",
  groupId: null,
  sort: 1,
};

let mounted: MountedView | undefined;

function actionButton(action: "插入" | "执行" | "编辑"): HTMLButtonElement {
  const button = mounted?.container.querySelector<HTMLButtonElement>(`button[aria-label="${action}片段「parameterized」"]`);
  if (!button) throw new Error("snippet action button not found");
  return button;
}

function promptDialog(): HTMLElement {
  const dialog = [...document.body.querySelectorAll<HTMLElement>('[role="dialog"]')].find(
    (candidate) => candidate.textContent?.includes("填写片段变量"),
  );
  if (!dialog) throw new Error("snippet variable prompt not found");
  return dialog;
}

function writtenText(): string {
  const data = mocks.write.mock.calls[0]?.[1] as Uint8Array;
  return new TextDecoder().decode(data);
}

beforeEach(() => {
  vi.resetAllMocks();
  document.body.replaceChildren();
  document.body.style.overflow = "";
  mocks.snippetList.mockResolvedValue([{ ...parameterizedSnippet }]);
  mocks.snippetUpdate.mockResolvedValue(undefined);
  mocks.assetGet.mockResolvedValue({
    id: "asset-1",
    name: "api-a",
    host: "10.0.0.1",
    tags: "production, web",
  });
  mocks.ask.mockResolvedValue(true);
  mocks.write.mockResolvedValue(undefined);
  useUi.setState({
    activeWorkspaceId: "ws-1",
    workspaces: [
      {
        id: "ws-1",
        kind: "session",
        title: "Workspace",
        assetId: "asset-1",
        activePaneId: "pane-1",
        panes: [
          {
            id: "pane-1",
            activeTabId: "tab-1",
            tabs: [{ id: "tab-1", kind: "terminal", title: "Terminal", tabId: "term-1", closable: true }],
          },
        ],
        closable: true,
      },
    ],
    toasts: [],
    pushToast: mocks.toast,
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  document.body.replaceChildren();
  document.body.style.overflow = "";
});

describe("snippet variable prompt integration", () => {
  it("collects defaults and multiline values, resolves the active asset, and writes the rendered command", async () => {
    mounted = mountWithClient(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await flushUntil(() => mounted?.container.textContent?.includes("parameterized") ?? false);
    click(actionButton("执行"));
    await flushUntil(() => document.body.textContent?.includes("填写片段变量") ?? false);

    const dialog = promptDialog();
    const service = dialog.querySelector<HTMLInputElement>('input[aria-label="变量 service"]');
    const script = dialog.querySelector<HTMLTextAreaElement>('textarea[aria-label="变量 script"]');
    if (!service || !script) throw new Error("variable fields not found");
    expect(service.value).toBe("nginx");
    expect(dialog.querySelectorAll("input, textarea")).toHaveLength(2);
    expect(mocks.write).not.toHaveBeenCalled();

    clickButton(dialog, "继续");
    await flush();
    expect(dialog.textContent).toContain("必填变量「script」不能为空");
    expect(mocks.write).not.toHaveBeenCalled();

    setInputValue(script, "printf one\nprintf two");
    clickButton(dialog, "继续");
    await flushUntil(() => mocks.write.mock.calls.length === 1);
    expect(mocks.ask).toHaveBeenCalledWith(expect.stringContaining("包含回车或换行"), expect.anything());
    expect(writtenText()).toBe("run nginx on api-a 10.0.0.1 production, web: printf one\nprintf two\r");
    expect(mocks.assetGet).toHaveBeenCalledWith("asset-1");
  });

  it("reports unknown built-ins without opening a prompt or writing to the terminal", async () => {
    mocks.snippetList.mockResolvedValue([{ ...parameterizedSnippet, body: "echo {{asset.environment}}" }]);
    mounted = mountWithClient(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await flushUntil(() => mounted?.container.textContent?.includes("parameterized") ?? false);
    click(actionButton("执行"));
    await flushUntil(() => mocks.toast.mock.calls.length > 0);

    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("未知变量"));
    expect(document.body.textContent).not.toContain("填写片段变量");
    expect(mocks.write).not.toHaveBeenCalled();
  });

  it("uses the same prompt path for insertion without submitting the rendered body", async () => {
    mocks.snippetList.mockResolvedValue([{ ...parameterizedSnippet, body: "echo {{service}}" }]);
    mounted = mountWithClient(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await flushUntil(() => mounted?.container.textContent?.includes("parameterized") ?? false);
    click(actionButton("插入"));
    await flushUntil(() => document.body.textContent?.includes("填写片段变量") ?? false);
    const input = promptDialog().querySelector<HTMLInputElement>('input[aria-label="变量 service"]');
    if (!input) throw new Error("service input not found");
    setInputValue(input, "worker");
    clickButton(promptDialog(), "继续");
    await flushUntil(() => mocks.write.mock.calls.length === 1);

    expect(writtenText()).toBe("echo worker");
    expect(mocks.ask).not.toHaveBeenCalled();
  });

  it("saves the original template rather than a rendered body when editing", async () => {
    mounted = mountWithClient(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await flushUntil(() => mounted?.container.textContent?.includes("parameterized") ?? false);
    click(actionButton("编辑"));
    const editor = mounted.container.querySelectorAll<HTMLElement>(".nx-modal")[1];
    if (!editor) throw new Error("snippet editor not found");
    clickButton(editor, "保存");
    await flushUntil(
      () => mocks.snippetUpdate.mock.calls.length === 1 && mounted?.container.querySelectorAll(".nx-modal").length === 1,
    );
    await flush();

    expect(mocks.snippetUpdate).toHaveBeenCalledWith("sn-vars", "parameterized", parameterizedSnippet.body);
    expect(document.body.textContent).not.toContain("填写片段变量");
  });
});
