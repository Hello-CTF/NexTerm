/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, clickButton, flush, flushUntil, mount, setInputValue, setSelectValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    create: vi.fn(),
    update: vi.fn(),
    groupList: vi.fn(),
    listAssets: vi.fn(),
    listCredentials: vi.fn(),
    setCredential: vi.fn(),
    probe: vi.fn(),
    toast: vi.fn(),
  };
});
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

function advancedToggle(): HTMLButtonElement {
  const button = mounted!.container.querySelector<HTMLButtonElement>(
    "button[aria-expanded][aria-controls]",
  );
  if (!button) throw new Error("advanced options toggle not found");
  return button;
}

function expandAdvanced(): void {
  const toggle = advancedToggle();
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

describe("AssetEditor SSH connectivity", () => {
  it("saves authKind and password credentials for keyboard-interactive authentication", async () => {
    mountEditor();
    await flush();
    setSelectValue(findSelect("密码"), "keyboard-interactive");
    await flush();
    fillName("interactive-01");
    fillHost("10.0.0.8");
    const passwordInput = mounted!.container.querySelector('input[type="password"]') as HTMLInputElement;
    expect(passwordInput).toBeTruthy();
    setInputValue(passwordInput, "s3cret");
    click(saveButton());
    await flushUntil(() => mocks.create.mock.calls.length > 0 && !mounted!.container.textContent?.includes("保存中…"));
    const payload = createdPayload();
    expect(payload.authKind).toBe("keyboard-interactive");
    expect(payload.credId).toBe("cred-1");
    expect(mocks.setCredential).toHaveBeenCalledWith("interactive-01", "password", "s3cret");
    expect(payload.options).toEqual({});
  });

  it("lists SSH assets in the jump host dropdown and saves jumpAssetId", async () => {
    mocks.listAssets.mockResolvedValue([
      { id: "j1", kind: "ssh", name: "jump-host", host: "10.0.0.1", port: 22 },
      { id: "l1", kind: "local", name: "local-machine", host: null, port: null },
      { id: "d1", kind: "docker", name: "docker-host", host: "10.0.0.2", port: 22 },
    ]);
    mountEditor();
    expandAdvanced();
    await flushUntil(() => findSelect("不使用跳板机").options.length > 1);
    const jumpSelect = findSelect("不使用跳板机");
    const labels = [...jumpSelect.options].map((option) => option.textContent);
    expect(labels.some((label) => label?.includes("jump-host"))).toBe(true);
    expect(labels.some((label) => label?.includes("docker-host"))).toBe(true);
    expect(labels.some((label) => label?.includes("local-machine"))).toBe(false);
    setSelectValue(jumpSelect, "j1");
    fillName("via-jump");
    fillHost("10.0.0.8");
    click(saveButton());
    await flushUntil(() => mocks.create.mock.calls.length > 0 && !mounted!.container.textContent?.includes("保存中…"));
    expect(createdPayload().options).toEqual({ jumpAssetId: "j1" });
  });

  it("excludes the current asset from jump host choices when editing and allows clearing the reference", async () => {
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
      { id: "j1", kind: "ssh", name: "jump-host", host: "10.0.0.1", port: 22 },
    ]);
    mountEditor(initial);
    expandAdvanced();
    await flushUntil(() => findSelect("不使用跳板机").options.length > 1);
    const jumpSelect = findSelect("不使用跳板机");
    expect(jumpSelect.value).toBe("j1");
    const labels = [...jumpSelect.options].map((option) => option.textContent);
    expect(labels.some((label) => label?.includes("web-01"))).toBe(false);
    setSelectValue(jumpSelect, "");
    click(saveButton());
    await flushUntil(() => mocks.update.mock.calls.length > 0 && !mounted!.container.textContent?.includes("保存中…"));
    expect(updatedPayload().options).toEqual({});
  });

  it("Agent forwarding is disabled by default and saves forwardAgent and socket when enabled", async () => {
    mountEditor();
    await flush();
    expandAdvanced();
    expect(mounted!.container.textContent).toContain(
      "默认关闭；开启后，远端进程可请求本机 ssh-agent 签名并继续连接其他主机，私钥不会离开本机。仅对可信主机开启。",
    );
    const checkbox = mounted!.container.querySelector('input[aria-label="Agent 转发"]') as HTMLInputElement;
    expect(checkbox.checked).toBe(false);
    click(checkbox);
    await flush();
    expect(checkbox.checked).toBe(true);
    setInputValue(findInput("$SSH_AUTH_SOCK"), "/tmp/agent.sock");
    fillName("forwarder");
    fillHost("10.0.0.8");
    click(saveButton());
    await flushUntil(() => mocks.create.mock.calls.length > 0 && !mounted!.container.textContent?.includes("保存中…"));
    expect(createdPayload().options).toEqual({
      forwardAgent: true,
      agentSocket: "/tmp/agent.sock",
    });
  });

  it("saves the certificate path for private key authentication", async () => {
    mountEditor();
    await flush();
    setSelectValue(findSelect("密码"), "key");
    await flush();
    clickButton(mounted!.container, "引用文件路径");
    expandAdvanced();
    setInputValue(findInput("选择或输入私钥路径"), "/home/u/.ssh/id_ed25519");
    setInputValue(findInput("id_ed25519-cert.pub"), "/home/u/.ssh/id_ed25519-cert.pub");
    fillName("cert-user");
    fillHost("10.0.0.8");
    click(saveButton());
    await flushUntil(() => mocks.create.mock.calls.length > 0 && !mounted!.container.textContent?.includes("保存中…"));
    const payload = createdPayload();
    expect(payload.keyPath).toBe("/home/u/.ssh/id_ed25519");
    expect(payload.options).toEqual({ certPath: "/home/u/.ssh/id_ed25519-cert.pub" });
  });

  it("preserves existing options not managed by the editor when saving", async () => {
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
    expandAdvanced();
    setInputValue(findInput("nc %h %p"), "nc %h %p");
    click(saveButton());
    await flushUntil(() => mocks.update.mock.calls.length > 0 && !mounted!.container.textContent?.includes("保存中…"));
    expect(updatedPayload().options).toEqual({
      proxy: "socks5://127.0.0.1:1080",
      connectTimeout: 30,
      proxyCommand: "nc %h %p",
    });
  });

  it("saves ordinary terminal mode for one SSH asset", async () => {
    mountEditor();
    const mode = findSelect("默认（使用全局设置）");
    expect(mode.value).toBe("auto");
    setSelectValue(mode, "regular");
    expect(mounted?.container.textContent).toContain("仅影响以后新开的终端，已有会话保持不变");
    fillName("ordinary-ssh");
    fillHost("10.0.0.8");
    click(saveButton());
    await flushUntil(() => mocks.create.mock.calls.length > 0 && !mounted!.container.textContent?.includes("保存中…"));
    expect(createdPayload().options).toEqual({ terminalMode: "regular" });
  });

  it("offers persistent terminal mode in the basic form and saves an explicit override", async () => {
    mountEditor();
    const mode = findSelect("默认（使用全局设置）");
    expect(advancedToggle().getAttribute("aria-expanded")).toBe("false");
    expect([...mode.options].map((option) => option.value)).toEqual(["auto", "durable", "regular"]);
    setSelectValue(mode, "durable");
    fillName("durable-ssh");
    fillHost("10.0.0.8");
    click(saveButton());
    await flushUntil(() => mocks.create.mock.calls.length > 0 && !mounted!.container.textContent?.includes("保存中…"));
    expect(createdPayload().options).toEqual({ terminalMode: "durable" });
  });

  it.each(["regular", "durable"])("loads an SSH asset's %s terminal mode and clears its override when switched to default", async (initialMode) => {
    const initial = {
      id: "a-regular",
      kind: "ssh",
      name: "ordinary-ssh",
      host: "10.0.0.8",
      port: 22,
      username: "root",
      authKind: "password",
      credId: null,
      keyPath: null,
      groupId: null,
      options: { terminalMode: initialMode },
      tags: "",
      note: "",
      sort: 0,
      createdAt: 0,
      updatedAt: 0,
      deletedAt: null,
      builtin: false,
    } as Asset;
    mountEditor(initial);
    const mode = findSelect("默认（使用全局设置）");
    expect(mode.value).toBe(initialMode);
    setSelectValue(mode, "auto");
    click(saveButton());
    await flushUntil(() => mocks.update.mock.calls.length > 0 && !mounted!.container.textContent?.includes("保存中…"));
    expect(updatedPayload().options).toEqual({});
  });
});

describe("AssetEditor SSH advanced options collapsing", () => {
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

  it("collapses advanced options for a new asset and omits them from the DOM", async () => {
    mountEditor();
    await flush();
    const toggle = advancedToggle();
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(document.getElementById(toggle.getAttribute("aria-controls") ?? "")).toBeNull();
    const hasJumpSelect = [...mounted!.container.querySelectorAll("select")].some((s) =>
      [...s.options].some((o) => o.textContent?.includes("不使用跳板机")),
    );
    expect(hasJumpSelect).toBe(false);
  });

  it("collapses jump host options when editing until they are expanded manually", async () => {
    const initial = { ...baseAsset, options: { jumpAssetId: "j1" } };
    mocks.listAssets.mockResolvedValue([
      { id: "j1", kind: "ssh", name: "jump-host", host: "10.0.0.1", port: 22 },
    ]);
    mountEditor(initial);
    await flush();
    const toggle = advancedToggle();
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(document.getElementById(toggle.getAttribute("aria-controls") ?? "")).toBeNull();

    expandAdvanced();
    await flushUntil(() => findSelect("不使用跳板机").options.length > 1);
    expect(findSelect("不使用跳板机").value).toBe("j1");
  });

  it("collapses Agent forwarding options when editing until they are expanded manually", async () => {
    const initial = { ...baseAsset, options: { forwardAgent: true } };
    mountEditor(initial);
    await flush();
    expect(advancedToggle().getAttribute("aria-expanded")).toBe("false");

    expandAdvanced();
    await flush();
    const checkbox = mounted!.container.querySelector(
      'input[aria-label="Agent 转发"]',
    ) as HTMLInputElement;
    expect(checkbox.checked).toBe(true);
    expect(findInput("$SSH_AUTH_SOCK")).toBeTruthy();
  });

  it("keeps advanced options collapsed when editing an asset without them", async () => {
    mountEditor(baseAsset);
    await flush();
    expect(advancedToggle().getAttribute("aria-expanded")).toBe("false");
  });

  it("preserves form state when toggling advanced options", async () => {
    mountEditor();
    await flush();
    expandAdvanced();
    await flush();
    setInputValue(findInput("nc %h %p"), "nc -X 5 %h %p");
    const checkbox = mounted!.container.querySelector(
      'input[aria-label="Agent 转发"]',
    ) as HTMLInputElement;
    click(checkbox);
    await flush();

    click(advancedToggle());
    await flush();
    expect(advancedToggle().getAttribute("aria-expanded")).toBe("false");

    expandAdvanced();
    await flush();
    expect(advancedToggle().getAttribute("aria-expanded")).toBe("true");
    expect(findInput("nc %h %p").value).toBe("nc -X 5 %h %p");
    const recheck = mounted!.container.querySelector(
      'input[aria-label="Agent 转发"]',
    ) as HTMLInputElement;
    expect(recheck.checked).toBe(true);
    expect(findInput("$SSH_AUTH_SOCK")).toBeTruthy();
  });
});

describe("AssetEditor tags and note", () => {
  it("loads existing values and saves trimmed deduplicated tags with an unchanged note payload", async () => {
    const initial = {
      id: "asset-tags",
      kind: "ssh",
      name: "tagged",
      host: "10.0.0.8",
      port: 22,
      username: "root",
      authKind: "password",
      credId: null,
      keyPath: null,
      groupId: null,
      options: {},
      tags: "legacy, blue",
      note: "  keep this note\n",
      sort: 0,
      createdAt: 0,
      updatedAt: 0,
      deletedAt: null,
      builtin: false,
    } as unknown as Asset;
    mountEditor(initial);
    await flush();
    const tagsInput = findInput("用逗号分隔");
    const noteInput = mounted!.container.querySelector<HTMLTextAreaElement>('textarea[placeholder="备注（可选）"]')!;
    expect(tagsInput.value).toBe("legacy, blue");
    expect(noteInput.value).toBe("  keep this note\n");
    setInputValue(tagsInput, " Prod, prod, 欧洲，PROD ");
    setInputValue(noteInput, "  updated note\n");
    click(saveButton());
    await flushUntil(() => mocks.update.mock.calls.length > 0 && !mounted!.container.textContent?.includes("保存中…"));
    expect(updatedPayload()).toMatchObject({ tags: "prod, 欧洲", note: "  updated note\n" });
  });
});
