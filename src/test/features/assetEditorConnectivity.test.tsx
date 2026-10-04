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

function fillName(name: string) {
  setInputValue(mounted!.container.querySelector("input.nx-input") as HTMLInputElement, name);
}

function saveButton(): HTMLButtonElement {
  const button = [...mounted!.container.querySelectorAll("button")].find(
    (candidate) => candidate.textContent?.trim() === "保存",
  );
  if (!button) throw new Error("save button not found");
  return button as HTMLButtonElement;
}

function createdPayload(): Record<string, unknown> {
  expect(mocks.create).toHaveBeenCalledOnce();
  return mocks.create.mock.calls[0]![0] as Record<string, unknown>;
}

function updatedPayload(): Record<string, unknown> {
  expect(mocks.update).toHaveBeenCalledOnce();
  return mocks.update.mock.calls[0]![0] as Record<string, unknown>;
}

describe("AssetEditor SSH 连接能力", () => {
  it("键盘交互认证保存 authKind 与密码凭据", async () => {
    mountEditor();
    await flush();
    setSelectValue(findSelect("密码"), "keyboard-interactive");
    await flush();
    fillName("interactive-01");
    const passwordInput = mounted!.container.querySelector('input[type="password"]') as HTMLInputElement;
    expect(passwordInput).toBeTruthy();
    setInputValue(passwordInput, "s3cret");
    click(saveButton());
    await flushUntil(() => mocks.create.mock.calls.length > 0);
    const payload = createdPayload();
    expect(payload.authKind).toBe("keyboard-interactive");
    expect(payload.credId).toBe("cred-1");
    expect(mocks.setCredential).toHaveBeenCalledWith("interactive-01", "password", "s3cret");
    expect(payload.options).toEqual({});
  });

  it("跳板机下拉列出 SSH 资产并保存 jumpAssetId", async () => {
    mocks.listAssets.mockResolvedValue([
      { id: "j1", kind: "ssh", name: "跳板", host: "10.0.0.1", port: 22 },
      { id: "l1", kind: "local", name: "本机", host: null, port: null },
      { id: "d1", kind: "docker", name: "容器宿主机", host: "10.0.0.2", port: 22 },
    ]);
    mountEditor();
    await flushUntil(() => findSelect("不使用跳板机").options.length > 1);
    const jumpSelect = findSelect("不使用跳板机");
    const labels = [...jumpSelect.options].map((option) => option.textContent);
    expect(labels.some((label) => label?.includes("跳板"))).toBe(true);
    expect(labels.some((label) => label?.includes("容器宿主机"))).toBe(true);
    expect(labels.some((label) => label?.includes("本机"))).toBe(false);
    setSelectValue(jumpSelect, "j1");
    fillName("via-jump");
    click(saveButton());
    await flushUntil(() => mocks.create.mock.calls.length > 0);
    expect(createdPayload().options).toEqual({ jumpAssetId: "j1" });
  });

  it("编辑时排除自身作为跳板机，并可清除跳板机引用", async () => {
    const initial = {
      id: "self-1",
      kind: "ssh",
      name: "web-01",
      host: "10.0.0.9",
      port: 22,
      username: "root",
      authKind: "password",
      credId: null,
      keyPath: null,
      groupId: null,
      options: { jumpAssetId: "j1" },
      tags: "",
      note: "",
      sort: 0,
      createdAt: 0,
      updatedAt: 0,
      deletedAt: null,
      builtin: false,
    } as unknown as Asset;
    mocks.listAssets.mockResolvedValue([
      { id: "self-1", kind: "ssh", name: "web-01", host: "10.0.0.9", port: 22 },
      { id: "j1", kind: "ssh", name: "跳板", host: "10.0.0.1", port: 22 },
    ]);
    mountEditor(initial);
    await flushUntil(() => findSelect("不使用跳板机").options.length > 1);
    const jumpSelect = findSelect("不使用跳板机");
    expect(jumpSelect.value).toBe("j1");
    const labels = [...jumpSelect.options].map((option) => option.textContent);
    expect(labels.some((label) => label?.includes("web-01"))).toBe(false);
    setSelectValue(jumpSelect, "");
    click(saveButton());
    await flushUntil(() => mocks.update.mock.calls.length > 0);
    expect(updatedPayload().options).toEqual({});
  });

  it("Agent 转发默认关闭，勾选后保存 forwardAgent 与 socket", async () => {
    mountEditor();
    await flush();
    const checkbox = mounted!.container.querySelector('input[aria-label="Agent 转发"]') as HTMLInputElement;
    expect(checkbox.checked).toBe(false);
    click(checkbox);
    await flush();
    expect(checkbox.checked).toBe(true);
    setInputValue(findInput("$SSH_AUTH_SOCK"), "/tmp/agent.sock");
    fillName("forwarder");
    click(saveButton());
    await flushUntil(() => mocks.create.mock.calls.length > 0);
    expect(createdPayload().options).toEqual({
      forwardAgent: true,
      agentSocket: "/tmp/agent.sock",
    });
  });

  it("私钥认证可保存证书路径", async () => {
    mountEditor();
    await flush();
    setSelectValue(findSelect("密码"), "key");
    await flush();
    setInputValue(findInput("选择或输入私钥路径"), "/home/u/.ssh/id_ed25519");
    setInputValue(findInput("id_ed25519-cert.pub"), "/home/u/.ssh/id_ed25519-cert.pub");
    fillName("cert-user");
    click(saveButton());
    await flushUntil(() => mocks.create.mock.calls.length > 0);
    const payload = createdPayload();
    expect(payload.keyPath).toBe("/home/u/.ssh/id_ed25519");
    expect(payload.options).toEqual({ certPath: "/home/u/.ssh/id_ed25519-cert.pub" });
  });

  it("保存时保留界面未管理的既有 options", async () => {
    const initial = {
      id: "a9",
      kind: "ssh",
      name: "legacy",
      host: "10.0.0.8",
      port: 22,
      username: "root",
      authKind: "password",
      credId: null,
      keyPath: null,
      groupId: null,
      options: { proxy: "socks5://127.0.0.1:1080", connectTimeout: 30 },
      tags: "",
      note: "",
      sort: 0,
      createdAt: 0,
      updatedAt: 0,
      deletedAt: null,
      builtin: false,
    } as unknown as Asset;
    mountEditor(initial);
    await flush();
    setInputValue(findInput("nc %h %p"), "nc %h %p");
    click(saveButton());
    await flushUntil(() => mocks.update.mock.calls.length > 0);
    expect(updatedPayload().options).toEqual({
      proxy: "socks5://127.0.0.1:1080",
      connectTimeout: 30,
      proxyCommand: "nc %h %p",
    });
  });
});
