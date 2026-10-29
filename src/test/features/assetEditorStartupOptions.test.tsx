/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { chooseSelectOption, click, flush, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

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

describe("AssetEditor SSH startup options", () => {
  it("saves the startup command, terminal encoding, and environment variables", async () => {
    mountEditor();
    await flush();
    expandAdvanced();
    setInputValue(findInput("连接成功后在该终端自动执行"), "echo hello && cd /var/log");
    chooseSelectOption(mounted!.container, "终端编码", "gbk");
    setInputValue(envTextarea(), "LANG=en_US.UTF-8\nNEXTERM_TEST=1");
    fillName("startup-01");
    fillHost("10.0.0.8");
    click(saveButton());
    await flushUntil(() => mocks.create.mock.calls.length > 0 && !mounted!.container.textContent?.includes("正在保存…"));
    expect(createdPayload().options).toEqual({
      startupCommand: "echo hello && cd /var/log",
      encoding: "gbk",
      env: { LANG: "en_US.UTF-8", NEXTERM_TEST: "1" },
    });
  });

  it("does not write default values to options", async () => {
    mountEditor();
    await flush();
    expandAdvanced();
    fillName("plain-01");
    fillHost("10.0.0.8");
    click(saveButton());
    await flushUntil(() => mocks.create.mock.calls.length > 0 && !mounted!.container.textContent?.includes("正在保存…"));
    expect(createdPayload().options).toEqual({});
  });

  it("rejects invalid environment variable lines and shows an error", async () => {
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

  it("collapses advanced options for an asset with startup settings until expanded manually", async () => {
    const initial = {
      ...baseAsset,
      options: { startupCommand: "echo hi", encoding: "gbk", env: { A: "1" } },
    } as unknown as Asset;
    mountEditor(initial);
    await flush();
    const toggle = mounted!.container.querySelector<HTMLButtonElement>(
      "button[aria-expanded][aria-controls]",
    )!;
    expect(toggle.getAttribute("aria-expanded")).toBe("false");

    expandAdvanced();
    await flush();
    expect(findInput("连接成功后在该终端自动执行").value).toBe("echo hi");
    expect(mounted!.container.querySelector<HTMLButtonElement>('button[aria-label="终端编码"][aria-haspopup="listbox"]')?.textContent).toContain("gbk");
    expect(envTextarea().value).toBe("A=1");
  });

  it("starts collapsed for an asset with a startup command and removes its key after clearing and saving", async () => {
    const initial = {
      ...baseAsset,
      options: { startupCommand: "echo hi", encoding: "utf-8", env: { A: "1" } },
    } as unknown as Asset;
    mountEditor(initial);
    await flush();
    const toggle = mounted!.container.querySelector<HTMLButtonElement>(
      "button[aria-expanded][aria-controls]",
    )!;
    expect(toggle.getAttribute("aria-expanded")).toBe("false");

    expandAdvanced();
    await flush();

    setInputValue(findInput("连接成功后在该终端自动执行"), "");
    setInputValue(envTextarea(), "");
    click(saveButton());
    await flushUntil(() => mocks.update.mock.calls.length > 0 && !mounted!.container.textContent?.includes("正在保存…"));
    expect(updatedPayload().options).toEqual({});
  });

  it("saves normalised paste upload directories", async () => {
    mountEditor();
    await flush();
    expandAdvanced();
    setInputValue(findInput("/tmp"), "/ ");
    setInputValue(findInput("与上传目录相同"), "/tmp/");
    fillName("chroot-01");
    fillHost("10.0.0.8");
    click(saveButton());
    await flushUntil(() => mocks.create.mock.calls.length > 0 && !mounted!.container.textContent?.includes("正在保存…"));
    expect(createdPayload().options).toEqual({ pasteUploadDir: "/", pasteTerminalDir: "/tmp" });
  });

  it("loads paste directories on edit and drops a terminal path equal to the upload directory", async () => {
    const initial = {
      ...baseAsset,
      options: { pasteUploadDir: "/upload", pasteTerminalDir: "/home/jail/upload" },
    } as unknown as Asset;
    mountEditor(initial);
    await flush();
    expandAdvanced();
    expect(findInput("/tmp").value).toBe("/upload");
    expect(findInput("与上传目录相同").value).toBe("/home/jail/upload");
    setInputValue(findInput("与上传目录相同"), "/upload/");
    click(saveButton());
    await flushUntil(() => mocks.update.mock.calls.length > 0 && !mounted!.container.textContent?.includes("正在保存…"));
    expect(updatedPayload().options).toEqual({ pasteUploadDir: "/upload" });
  });

  it("keeps the random paste directory by default and saves the opt-out only when it is ticked", async () => {
    mountEditor();
    await flush();
    expandAdvanced();
    const box = () => mounted!.container.querySelector<HTMLInputElement>('input[aria-label="不使用随机目录"]')!;
    expect(box().checked).toBe(false);
    fillName("flat-01");
    fillHost("10.0.0.9");
    click(box());
    expect(box().checked).toBe(true);
    click(saveButton());
    await flushUntil(() => mocks.create.mock.calls.length > 0 && !mounted!.container.textContent?.includes("正在保存…"));
    expect(createdPayload().options).toEqual({ pasteNoRandomDir: true });
  });

  it("loads the opt-out on edit and removes it when cleared", async () => {
    mountEditor({ ...baseAsset, options: { pasteUploadDir: "/", pasteTerminalDir: "/tmp", pasteNoRandomDir: true } } as unknown as Asset);
    await flush();
    expandAdvanced();
    const box = mounted!.container.querySelector<HTMLInputElement>('input[aria-label="不使用随机目录"]')!;
    expect(box.checked).toBe(true);
    click(box);
    click(saveButton());
    await flushUntil(() => mocks.update.mock.calls.length > 0 && !mounted!.container.textContent?.includes("正在保存…"));
    expect(updatedPayload().options).toEqual({ pasteUploadDir: "/", pasteTerminalDir: "/tmp" });
  });

  it("keeps the overwrite backup off by default and saves it only when ticked", async () => {
    mountEditor();
    await flush();
    expandAdvanced();
    const box = () => mounted!.container.querySelector<HTMLInputElement>('input[aria-label="覆盖文件前保留 .nexterm-bak 备份"]')!;
    expect(box().checked).toBe(false);
    expect(mounted!.container.textContent).toContain("覆盖文件前在主机上保留 .nexterm-bak 备份");
    fillName("bak-01");
    fillHost("10.0.0.9");
    click(box());
    expect(box().checked).toBe(true);
    click(saveButton());
    await flushUntil(() => mocks.create.mock.calls.length > 0 && !mounted!.container.textContent?.includes("正在保存…"));
    expect(createdPayload().options).toEqual({ backupBeforeOverwrite: true });
  });

  it("loads the overwrite backup on edit and removes it when cleared", async () => {
    mountEditor({ ...baseAsset, options: { backupBeforeOverwrite: true } } as unknown as Asset);
    await flush();
    expandAdvanced();
    const box = mounted!.container.querySelector<HTMLInputElement>('input[aria-label="覆盖文件前保留 .nexterm-bak 备份"]')!;
    expect(box.checked).toBe(true);
    click(box);
    click(saveButton());
    await flushUntil(() => mocks.update.mock.calls.length > 0 && !mounted!.container.textContent?.includes("正在保存…"));
    expect(updatedPayload().options).toEqual({});
  });

  it("removes paste directories after clearing the upload directory", async () => {
    mountEditor({ ...baseAsset, options: { pasteUploadDir: "/upload" } } as unknown as Asset);
    await flush();
    expandAdvanced();
    setInputValue(findInput("/tmp"), "");
    click(saveButton());
    await flushUntil(() => mocks.update.mock.calls.length > 0 && !mounted!.container.textContent?.includes("正在保存…"));
    expect(updatedPayload().options).toEqual({});
  });

  it.each([
    ["tmp", "", "以 / 开头"],
    ["/srv/../etc", "", ".."],
    ["", "/tmp", "必须同时填写粘贴上传目录"],
  ])("rejects paste directories %j / %j", async (upload, terminal, message) => {
    mountEditor();
    await flush();
    expandAdvanced();
    setInputValue(findInput("/tmp"), upload);
    setInputValue(findInput("与上传目录相同"), terminal);
    fillName("bad-dir");
    fillHost("10.0.0.8");
    click(saveButton());
    await flush();
    expect(mocks.create).not.toHaveBeenCalled();
    expect(mounted!.container.textContent).toContain(message);
  });

  it("does not render SSH advanced options for WinRM and local assets", async () => {
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
