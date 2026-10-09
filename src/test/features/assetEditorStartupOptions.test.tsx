/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, flushUntil, mount, setInputValue, setSelectValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  create: vi.fn(),
  update: vi.fn(),
  groupList: vi.fn(),
  listAssets: vi.fn(),
  listCredentials: vi.fn(),
  setCredential: vi.fn(),
  probe: vi.fn(),
  toast: vi.fn(),
}));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      create: mocks.create,
      update: mocks.update,
      groupList: mocks.groupList,
      groupCreate: vi.fn(),
      groupUpdate: vi.fn(),
      groupDelete: vi.fn(),
      list: mocks.listAssets,
      readKeyFile: vi.fn(),
      saveKeyFile: vi.fn(),
      auditQuery: vi.fn(),
    },
    vaultApi: {
      listCredentials: mocks.listCredentials,
      setCredential: mocks.setCredential,
    },
    sessionApi: {
      probe: mocks.probe,
    },
    terminalApi: {},
  };
});

import { AssetEditor } from "../../features/explorer/AssetTree";
import { useUi } from "../../app/store";
import type { Asset } from "../../ipc/commands";

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.groupList.mockResolvedValue([]);
  mocks.listAssets.mockResolvedValue([]);
  mocks.listCredentials.mockResolvedValue([]);
  mocks.create.mockResolvedValue({ id: "a1" });
  mocks.update.mockResolvedValue({ id: "a1" });
  mocks.setCredential.mockResolvedValue({ id: "cred-1" });
  useUi.setState({ pushToast: mocks.toast, sessions: [] });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function withClient(node: React.ReactElement): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function mountEditor(initial?: Asset) {
  const onClose = vi.fn();
  const onSaved = vi.fn();
  mounted = withClient(createElement(AssetEditor, { kind: "asset", initial, onClose, onSaved }));
  return { onClose, onSaved };
}

function findSelect(optionText: string): HTMLSelectElement {
  const selects = [...mounted!.container.querySelectorAll("select")];
  const select = selects.find((candidate) =>
    [...candidate.options].some((option) => option.textContent?.includes(optionText)),
  );
  if (!select) throw new Error(`select containing option ${optionText} not found`);
  return select;
}

function findInput(placeholder: string): HTMLInputElement {
  const input = [...mounted!.container.querySelectorAll("input")].find((candidate) =>
    candidate.placeholder?.includes(placeholder),
  );
  if (!input) throw new Error(`input with placeholder ${placeholder} not found`);
  return input;
}

function envTextarea(): HTMLTextAreaElement {
  const textarea = mounted!.container.querySelector("textarea");
  if (!textarea) throw new Error("env textarea not found");
  return textarea;
}

function fillName(name: string) {
  setInputValue(mounted!.container.querySelector("input.nx-input") as HTMLInputElement, name);
}

function fillHost(host: string) {
  setInputValue(findInput("1.2.3.4"), host);
}

function saveButton(): HTMLButtonElement {
  const button = [...mounted!.container.querySelectorAll("button")].find(
    (candidate) => candidate.textContent?.trim() === "保存",
  );
  if (!button) throw new Error("save button not found");
  return button as HTMLButtonElement;
}

function expandAdvanced(): void {
  const toggle = mounted!.container.querySelector<HTMLButtonElement>(
    "button[aria-expanded][aria-controls]",
  );
  if (!toggle) throw new Error("advanced options toggle not found");
  if (toggle.getAttribute("aria-expanded") !== "true") click(toggle);
}

function createdPayload(): Record<string, unknown> {
  expect(mocks.create).toHaveBeenCalledOnce();
  return mocks.create.mock.calls[0]![0] as Record<string, unknown>;
}

function updatedPayload(): Record<string, unknown> {
  expect(mocks.update).toHaveBeenCalledOnce();
  return mocks.update.mock.calls[0]![0] as Record<string, unknown>;
}

const baseAsset = {
  id: "a9",
  kind: "ssh",
  name: "web-01",
  host: "10.0.0.8",
  port: 22,
  username: "root",
  authKind: "password",
  credId: null,
  keyPath: null,
  groupId: null,
  options: {},
  tags: "",
  note: "",
  sort: 0,
  createdAt: 0,
  updatedAt: 0,
  deletedAt: null,
  builtin: false,
} as unknown as Asset;

describe("AssetEditor SSH 启动配置", () => {
  it("保存启动命令、终端编码与环境变量", async () => {
    mountEditor();
    await flush();
    expandAdvanced();
    setInputValue(findInput("连接成功后在该终端自动执行"), "echo hello && cd /var/log");
    setSelectValue(findSelect("gb18030"), "gbk");
    setInputValue(envTextarea(), "LANG=en_US.UTF-8\nNEXTERM_TEST=1");
    fillName("startup-01");
    fillHost("10.0.0.8");
    click(saveButton());
    await flushUntil(() => mocks.create.mock.calls.length > 0);
    expect(createdPayload().options).toEqual({
      startupCommand: "echo hello && cd /var/log",
      encoding: "gbk",
      env: { LANG: "en_US.UTF-8", NEXTERM_TEST: "1" },
    });
  });

  it("默认值不写入 options", async () => {
    mountEditor();
    await flush();
    expandAdvanced();
    fillName("plain-01");
    fillHost("10.0.0.8");
    click(saveButton());
    await flushUntil(() => mocks.create.mock.calls.length > 0);
    expect(createdPayload().options).toEqual({});
  });

  it("非法环境变量行阻止保存并提示", async () => {
    mountEditor();
    await flush();
    expandAdvanced();
    setInputValue(envTextarea(), "GOOD=1\nnot a pair");
    fillName("bad-env");
    fillHost("10.0.0.8");
    click(saveButton());
    await flush();
    expect(mocks.create).not.toHaveBeenCalled();
    expect(mounted!.container.textContent).toContain("不是有效的 KEY=VALUE");
  });

  it("编辑含启动命令/编码/环境变量的资产时自动展开", async () => {
    const initial = {
      ...baseAsset,
      options: { startupCommand: "echo hi", encoding: "gbk", env: { A: "1" } },
    } as unknown as Asset;
    mountEditor(initial);
    await flush();
    const toggle = mounted!.container.querySelector<HTMLButtonElement>(
      "button[aria-expanded][aria-controls]",
    )!;
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(findInput("连接成功后在该终端自动执行").value).toBe("echo hi");
    expect(findSelect("gb18030").value).toBe("gbk");
    expect(envTextarea().value).toBe("A=1");
  });

  it("编辑含启动命令的资产自动展开，清空后保存会删除对应 key", async () => {
    const initial = {
      ...baseAsset,
      options: { startupCommand: "echo hi", encoding: "utf-8", env: { A: "1" } },
    } as unknown as Asset;
    mountEditor(initial);
    await flush();
    const toggle = mounted!.container.querySelector<HTMLButtonElement>(
      "button[aria-expanded][aria-controls]",
    )!;
    expect(toggle.getAttribute("aria-expanded")).toBe("true");

    setInputValue(findInput("连接成功后在该终端自动执行"), "");
    setInputValue(envTextarea(), "");
    click(saveButton());
    await flushUntil(() => mocks.update.mock.calls.length > 0);
    expect(updatedPayload().options).toEqual({});
  });

  it("WinRM 与本地资产不渲染 SSH 高级选项", async () => {
    mountEditor({ ...baseAsset, kind: "winrm" } as unknown as Asset);
    await flush();
    expect(
      mounted!.container.querySelector("button[aria-expanded][aria-controls]"),
    ).toBeNull();
    mounted!.unmount();

    mountEditor({ ...baseAsset, kind: "local", host: null, port: null } as unknown as Asset);
    await flush();
    expect(
      mounted!.container.querySelector("button[aria-expanded][aria-controls]"),
    ).toBeNull();
  });
});
