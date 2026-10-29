/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  clickButton,
  flush,
  flushUntil,
  mount,
  setInputValue,
  setSelectValue,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  create: vi.fn(),
  groupList: vi.fn(),
  assetList: vi.fn(),
  listCredentials: vi.fn(),
  probe: vi.fn(),
  promptText: vi.fn(),
  toast: vi.fn(),
}));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      create: mocks.create,
      update: vi.fn(),
      groupList: mocks.groupList,
      groupCreate: vi.fn(),
      groupUpdate: vi.fn(),
      groupDelete: vi.fn(),
      list: mocks.assetList,
      readKeyFile: vi.fn(),
    },
    vaultApi: {
      listCredentials: mocks.listCredentials,
      setCredential: vi.fn(),
    },
    sessionApi: {
      probe: mocks.probe,
    },
    terminalApi: {},
  };
});
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, promptText: mocks.promptText };
});

import { AssetEditor } from "../../features/explorer/AssetTree";
import { useConnectTemplates, type ConnectTemplate } from "../../features/explorer/connectTemplates";
import { useUi } from "../../app/store";
import type { Asset } from "../../ipc/commands";

const KEY = "nexterm.connectTemplates.v1";

const TEMPLATE: ConnectTemplate = {
  id: "t-1",
  name: "production template",
  kind: "ssh",
  port: 2222,
  username: "deploy",
  authKind: "password",
  keyOrigin: "ref",
  keyPath: "",
  vaultCredId: "",
  jumpAssetId: "",
  proxyCommand: "",
  forwardAgent: false,
  agentSocket: "",
  certPath: "",
  startupCommand: "htop",
  encoding: "utf-8",
  envText: "",
};

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  useConnectTemplates.setState({ templates: [] });
  mocks.groupList.mockResolvedValue([]);
  mocks.assetList.mockResolvedValue([]);
  mocks.listCredentials.mockResolvedValue([]);
  mocks.create.mockResolvedValue({ id: "a1" });
  useUi.setState({ pushToast: mocks.toast, sessions: [] });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  useConnectTemplates.setState({ templates: [] });
});

function withClient(node: React.ReactElement): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function mountEditor(initial?: Asset) {
  const onClose = vi.fn();
  const onSaved = vi.fn();
  mounted = withClient(
    initial
      ? createElement(AssetEditor, { kind: "asset", initial, onClose, onSaved })
      : createElement(AssetEditor, { kind: "asset", onClose, onSaved }),
  );
  return { onClose, onSaved };
}

function fieldRow(label: string): HTMLElement {
  const row = [...mounted!.container.querySelectorAll<HTMLElement>(".nx-form-row")].find(
    (r) => r.querySelector(".nx-label")?.textContent?.trim() === label,
  );
  if (!row) throw new Error(`Form row not found: ${label}`);
  return row;
}

function inputOf(label: string): HTMLInputElement {
  const input = fieldRow(label).querySelector<HTMLInputElement>("input");
  if (!input) throw new Error(`Input not found: ${label}`);
  return input;
}

function portInput(): HTMLInputElement {
  const label = [...mounted!.container.querySelectorAll<HTMLLabelElement>("label")].find(
    (l) => l.textContent?.trim() === "端口",
  );
  const input = label?.htmlFor
    ? mounted!.container.querySelector<HTMLInputElement>(`#${CSS.escape(label.htmlFor)}`)
    : null;
  if (!input) throw new Error("port input not found");
  return input;
}

function templateSelect(): HTMLSelectElement | null {
  return mounted!.container.querySelector<HTMLSelectElement>('select[aria-label="从模板创建"]');
}

function fillHost(host: string) {
  const input = mounted!.container.querySelector<HTMLInputElement>('input[placeholder="1.2.3.4"]');
  if (!input) throw new Error("host input not found");
  setInputValue(input, host);
}

describe("AssetEditor save as template", () => {
  it("persists connection parameters except name and host", async () => {
    mocks.promptText.mockResolvedValue("production template");
    mountEditor();
    await flush();
    setInputValue(inputOf("名称"), "web-01");
    fillHost("10.0.0.8");
    setInputValue(inputOf("用户名"), "deploy");
    setInputValue(portInput(), "2222");

    clickButton(mounted!.container, "存为模板");
    await flushUntil(() => useConnectTemplates.getState().templates.length === 1);

    expect(mocks.promptText).toHaveBeenCalledWith("模板名称", "web-01");
    const saved = useConnectTemplates.getState().templates[0]!;
    expect(saved).toMatchObject({
      name: "production template",
      kind: "ssh",
      port: 2222,
      username: "deploy",
      authKind: "password",
    });
    expect(saved).not.toHaveProperty("host");
    const persisted = JSON.parse(localStorage.getItem(KEY) ?? "[]") as Record<string, unknown>[];
    expect(persisted).toHaveLength(1);
    expect(persisted[0]).toMatchObject({ name: "production template", port: 2222, username: "deploy" });
    expect(mocks.toast).toHaveBeenCalledWith("success", "已存为模板「production template」");
  });

  it("does not save when template name input is cancelled", async () => {
    mocks.promptText.mockResolvedValue(null);
    mountEditor();
    await flush();
    clickButton(mounted!.container, "存为模板");
    await flush();
    expect(useConnectTemplates.getState().templates).toEqual([]);
    expect(localStorage.getItem(KEY)).toBeNull();
  });

  it("hides template controls when editing an existing asset", async () => {
    useConnectTemplates.setState({ templates: [{ ...TEMPLATE }] });
    mountEditor({
      id: "a1",
      groupId: null,
      kind: "ssh",
      name: "web-01",
      host: "10.0.0.8",
      port: 22,
      username: "root",
      authKind: "password",
      keyPath: null,
      credId: null,
      options: {},
      tags: "",
      note: "",
      sort: 0,
      createdAt: 1,
      updatedAt: 1,
      deletedAt: null,
      builtin: false,
    });
    await flush();
    expect(templateSelect()).toBeNull();
    expect(
      [...mounted!.container.querySelectorAll("button")].some(
        (b) => b.textContent?.trim() === "存为模板",
      ),
    ).toBe(false);
  });
});

describe("AssetEditor creation from templates", () => {
  it("hides the template row when no templates exist", async () => {
    mountEditor();
    await flush();
    expect(templateSelect()).toBeNull();
  });

  it("prefills connection parameters from the selected template and leaves name and host blank", async () => {
    useConnectTemplates.setState({ templates: [{ ...TEMPLATE }] });
    mountEditor();
    await flush();
    const select = templateSelect();
    if (!select) throw new Error("template select not found");
    setSelectValue(select, "t-1");
    await flush();

    expect(portInput().value).toBe("2222");
    expect(inputOf("用户名").value).toBe("deploy");
    expect(inputOf("名称").value).toBe("");
    expect(
      mounted!.container.querySelector<HTMLInputElement>('input[placeholder="1.2.3.4"]')?.value,
    ).toBe("");
    const startup = mounted!.container.querySelector<HTMLInputElement>(
      'input[placeholder="连接成功后在该终端自动执行"]',
    );
    expect(startup?.value).toBe("htop");
  });

  it("saves with template parameters after applying a template", async () => {
    useConnectTemplates.setState({ templates: [{ ...TEMPLATE }] });
    const { onSaved } = mountEditor();
    await flush();
    const select = templateSelect();
    if (!select) throw new Error("template select not found");
    setSelectValue(select, "t-1");
    await flush();
    setInputValue(inputOf("名称"), "web-01");
    fillHost("10.0.0.8");

    clickButton(mounted!.container, "保存");
    await flushUntil(() => onSaved.mock.calls.length > 0);
    expect(mocks.create).toHaveBeenCalledWith(
      expect.objectContaining({
        kind: "ssh",
        name: "web-01",
        host: "10.0.0.8",
        port: 2222,
        username: "deploy",
        authKind: "password",
        options: expect.objectContaining({ startupCommand: "htop" }),
      }),
    );
  });

  it("deleting a template hides the controls and clears storage", async () => {
    localStorage.setItem(KEY, JSON.stringify([TEMPLATE]));
    useConnectTemplates.setState({ templates: [{ ...TEMPLATE }] });
    mountEditor();
    await flush();
    const select = templateSelect();
    if (!select) throw new Error("template select not found");
    setSelectValue(select, "t-1");
    await flush();

    clickButton(mounted!.container, "删除模板");
    await flushUntil(() => useConnectTemplates.getState().templates.length === 0);
    expect(JSON.parse(localStorage.getItem(KEY) ?? "[]")).toEqual([]);
    expect(templateSelect()).toBeNull();
    expect(mocks.toast).toHaveBeenCalledWith("info", "已删除该模板");
  });
});
