/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, clickButton, flush, flushUntil, mount, setInputValue, setSelectValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  vaultStatus: vi.fn(),
  initMaster: vi.fn(),
  listCredentials: vi.fn(),
  setCredential: vi.fn(),
  unlock: vi.fn(),
  promptText: vi.fn(),
  assetList: vi.fn(),
  groupList: vi.fn(),
  assetCreate: vi.fn(),
  assetUpdate: vi.fn(),
  toast: vi.fn(),
}));
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, promptText: mocks.promptText };
});
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    vaultApi: {
      status: mocks.vaultStatus,
      initMaster: mocks.initMaster,
      initDpapi: vi.fn(),
      lock: vi.fn(),
      unlock: mocks.unlock,
      changePassword: vi.fn(),
      setAutoLock: vi.fn(),
      listCredentials: mocks.listCredentials,
      setCredential: mocks.setCredential,
      revealCredential: vi.fn(),
      updateCredential: vi.fn(),
      deleteCredential: vi.fn(),
      generateKey: vi.fn(),
    },
    assetApi: {
      list: mocks.assetList,
      groupList: mocks.groupList,
      create: mocks.assetCreate,
      update: mocks.assetUpdate,
      readKeyFile: vi.fn(),
    },
    sessionApi: {},
    terminalApi: {},
    dbApi: {},
  };
});

import { CredentialsSidebar } from "../../features/credentials/CredentialsSidebar";
import { CredentialsPanel } from "../../features/credentials/CredentialsPanel";
import { NewCredentialModal } from "../../features/credentials/NewCredentialModal";
import { AssetEditor } from "../../features/explorer/AssetTree";
import { useUi } from "../../app/store";

const NOT_INIT_VAULT = {
  initialized: false,
  mode: "not_init" as const,
  unlocked: false,
  autoLockMinutes: 30,
};
const DPAPI_VAULT = {
  initialized: true,
  mode: "dpapi" as const,
  unlocked: true,
  autoLockMinutes: 30,
};
const LOCKED_VAULT = {
  initialized: true,
  mode: "master" as const,
  unlocked: false,
  autoLockMinutes: 30,
};
const MASTER_UNLOCKED_VAULT = {
  initialized: true,
  mode: "master" as const,
  unlocked: true,
  autoLockMinutes: 30,
};
const EXISTING_ASSET = {
  id: "a1",
  groupId: null,
  kind: "ssh" as const,
  name: "web-1",
  host: "10.0.0.8",
  port: 22,
  username: "root",
  authKind: "password" as const,
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
};

let mounted: MountedView | undefined;

function withClient(node: React.ReactElement): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function modalHeaders(): string[] {
  return [...(mounted?.container.querySelectorAll(".nx-modal-header") ?? [])].map(
    (el) => el.textContent?.trim() ?? "",
  );
}

function initModal(): HTMLElement | null {
  for (const modal of mounted?.container.querySelectorAll<HTMLElement>(".nx-modal") ?? []) {
    if (modal.querySelector(".nx-modal-header")?.textContent?.includes("初始化凭据保护")) return modal;
  }
  return null;
}

function assetEditorModal(): HTMLElement | null {
  for (const modal of mounted?.container.querySelectorAll<HTMLElement>(".nx-modal") ?? []) {
    const header = modal.querySelector(".nx-modal-header")?.textContent ?? "";
    if (header.includes("新建资产") || header.includes("编辑资产")) return modal;
  }
  return null;
}

function initPasswordInput(): HTMLInputElement {
  const input = initModal()?.querySelector<HTMLInputElement>('input[type="password"]');
  if (!input) throw new Error("init modal password input not found");
  return input;
}

function completeInit(password = "first-run-password") {
  setInputValue(initPasswordInput(), password);
  clickButton(initModal()!, "启用保护");
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.vaultStatus.mockResolvedValue(NOT_INIT_VAULT);
  mocks.initMaster.mockImplementation(async () => {
    mocks.vaultStatus.mockResolvedValue(MASTER_UNLOCKED_VAULT);
  });
  mocks.listCredentials.mockResolvedValue([]);
  mocks.assetList.mockResolvedValue([]);
  mocks.groupList.mockResolvedValue([]);
  mocks.setCredential.mockResolvedValue({ id: "cred-1" });
  mocks.assetCreate.mockResolvedValue({ id: "a-new" });
  mocks.assetUpdate.mockResolvedValue({ id: "a1" });
  mocks.promptText.mockResolvedValue(null);
  mocks.unlock.mockResolvedValue(undefined);
  useUi.setState({
    pushToast: mocks.toast,
    sessions: [],
    leftOpen: true,
    leftWidth: 260,
    appDialog: null,
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("凭据库未初始化前置弹窗（凭据入口）", () => {
  it("sidebar 未初始化时点「新建凭据」先弹初始化浮层，而不是新建表单", async () => {
    mounted = withClient(createElement(CredentialsSidebar));
    await flushUntil(() => !!mounted!.container.querySelector('button[title="新建凭据"]'));
    expect(mounted!.container.querySelector(".nx-modal")).toBeNull();

    click(mounted!.container.querySelector('button[title="新建凭据"]')!);
    await flushUntil(() => modalHeaders().includes("初始化凭据保护"));

    expect(modalHeaders()).not.toContain("新建凭据");
    expect(mocks.initMaster).not.toHaveBeenCalled();
  });

  it("初始化成功后回到原动作：自动打开新建凭据表单", async () => {
    mounted = withClient(createElement(CredentialsSidebar));
    await flushUntil(() => !!mounted!.container.querySelector('button[title="新建凭据"]'));
    click(mounted!.container.querySelector('button[title="新建凭据"]')!);
    await flushUntil(() => !!initModal());

    completeInit();
    await flushUntil(() => mocks.initMaster.mock.calls.length > 0);
    expect(mocks.initMaster).toHaveBeenCalledWith("first-run-password");
    expect(mocks.toast).toHaveBeenCalledWith("success", "已开启密码保护");

    await flushUntil(() => modalHeaders().includes("新建凭据"));
    expect(initModal()).toBeNull();
  });

  it("初始化浮层保持 8 位密码校验，短密码不能提交", async () => {
    mounted = withClient(createElement(CredentialsSidebar));
    await flushUntil(() => !!mounted!.container.querySelector('button[title="新建凭据"]'));
    click(mounted!.container.querySelector('button[title="新建凭据"]')!);
    await flushUntil(() => !!initModal());

    const submit = [...initModal()!.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "启用保护",
    )!;
    expect(submit.disabled).toBe(true);
    setInputValue(initPasswordInput(), "short");
    expect(submit.disabled).toBe(true);
    expect(mocks.initMaster).not.toHaveBeenCalled();
  });

  it("取消初始化则不打开新建表单，也不调用初始化", async () => {
    mounted = withClient(createElement(CredentialsSidebar));
    await flushUntil(() => !!mounted!.container.querySelector('button[title="新建凭据"]'));
    click(mounted!.container.querySelector('button[title="新建凭据"]')!);
    await flushUntil(() => !!initModal());

    clickButton(initModal()!, "取消");
    await flush();
    expect(mounted!.container.querySelector(".nx-modal")).toBeNull();
    expect(mocks.initMaster).not.toHaveBeenCalled();
  });

  it("已初始化时直接打开新建凭据，不出现初始化浮层", async () => {
    mocks.vaultStatus.mockResolvedValue(DPAPI_VAULT);
    mounted = withClient(createElement(CredentialsSidebar));
    await flushUntil(() => !!mounted!.container.querySelector('button[title="新建凭据"]'));

    click(mounted!.container.querySelector('button[title="新建凭据"]')!);
    await flushUntil(() => modalHeaders().includes("新建凭据"));
    expect(initModal()).toBeNull();
    expect(mocks.initMaster).not.toHaveBeenCalled();
  });

  it("panel 工具栏与空态「新建凭据」同样先弹初始化浮层", async () => {
    mounted = withClient(createElement(CredentialsPanel, { credId: undefined }));
    await flushUntil(() =>
      [...mounted!.container.querySelectorAll("button")].some(
        (b) => b.textContent?.trim() === "新建凭据",
      ),
    );

    clickButton(mounted!.container, "新建凭据");
    await flushUntil(() => !!initModal());
    expect(modalHeaders()).not.toContain("新建凭据");

    clickButton(initModal()!, "取消");
    await flushUntil(() => mounted!.container.querySelector(".nx-modal") === null);

    const emptyNew = [...mounted!.container.querySelectorAll(".nx-empty button")].find(
      (b) => b.textContent?.trim() === "新建凭据",
    )!;
    click(emptyNew);
    await flushUntil(() => !!initModal());
    expect(mocks.initMaster).not.toHaveBeenCalled();
  });
});

describe("凭据库未初始化前置弹窗（资产流程）", () => {
  it("新建资产（密码认证）打开即弹初始化浮层，先于任何填写", async () => {
    mounted = withClient(
      createElement(AssetEditor, { kind: "asset", onClose: () => {}, onSaved: () => {} }),
    );
    await flushUntil(() => !!initModal());

    expect(modalHeaders()).toContain("新建资产");
    expect(mocks.setCredential).not.toHaveBeenCalled();
  });

  it("初始化后回到资产表单：已填内容保留，保存不再被「尚未初始化」拒绝", async () => {
    const onSaved = vi.fn();
    mounted = withClient(
      createElement(AssetEditor, { kind: "asset", onClose: () => {}, onSaved }),
    );
    await flushUntil(() => !!initModal());

    completeInit();
    await flushUntil(() => mocks.initMaster.mock.calls.length > 0);
    await flushUntil(() => initModal() === null && !!assetEditorModal());

    const editor = assetEditorModal()!;
    const nameInput = editor.querySelector<HTMLInputElement>("input.nx-input")!;
    setInputValue(nameInput, "web-01");
    const hostInput = editor.querySelector<HTMLInputElement>('input[placeholder="1.2.3.4"]')!;
    setInputValue(hostInput, "10.0.0.8");
    const passwordInput = editor.querySelector<HTMLInputElement>('input[aria-label="凭据密码"]')!;
    setInputValue(passwordInput, "s3cret");
    clickButton(editor, "保存");

    await flushUntil(() => mocks.setCredential.mock.calls.length > 0);
    expect(mocks.setCredential).toHaveBeenCalledWith("web-01", "password", "s3cret");
    await flushUntil(() => mocks.assetCreate.mock.calls.length > 0);
    await flushUntil(() => onSaved.mock.calls.length > 0);
    for (const call of mocks.toast.mock.calls) {
      expect(String(call[1])).not.toContain("尚未初始化");
    }
  });

  it("取消初始化后资产表单仍在，可继续编辑（不丢已填内容）", async () => {
    mounted = withClient(
      createElement(AssetEditor, { kind: "asset", onClose: () => {}, onSaved: () => {} }),
    );
    await flushUntil(() => !!initModal());

    clickButton(initModal()!, "取消");
    await flushUntil(() => initModal() === null);
    const editor = assetEditorModal();
    expect(editor).not.toBeNull();
    const nameInput = editor!.querySelector<HTMLInputElement>("input.nx-input")!;
    setInputValue(nameInput, "web-02");
    expect(nameInput.value).toBe("web-02");
    expect(mocks.initMaster).not.toHaveBeenCalled();
  });

  it("编辑已有资产不主动弹初始化浮层", async () => {
    mounted = withClient(
      createElement(AssetEditor, {
        kind: "asset",
        initial: EXISTING_ASSET,
        onClose: () => {},
        onSaved: () => {},
      }),
    );
    await flushUntil(() => modalHeaders().includes("编辑资产"));
    await flush();
    expect(modalHeaders()).toEqual(["编辑资产"]);
    expect(mocks.initMaster).not.toHaveBeenCalled();
  });

  it("编辑已有资产并输入新密码时先弹初始化浮层，取消后已填内容保留", async () => {
    mounted = withClient(
      createElement(AssetEditor, {
        kind: "asset",
        initial: EXISTING_ASSET,
        onClose: () => {},
        onSaved: () => {},
      }),
    );
    await flushUntil(() => !!assetEditorModal());
    expect(initModal()).toBeNull();

    const passwordInput = assetEditorModal()!.querySelector<HTMLInputElement>(
      'input[aria-label="凭据密码"]',
    );
    if (!passwordInput) throw new Error("credential password input not found");
    setInputValue(passwordInput, "s3cret");
    await flushUntil(() => !!initModal());
    expect(mocks.setCredential).not.toHaveBeenCalled();

    clickButton(initModal()!, "取消");
    await flushUntil(() => initModal() === null);
    expect(passwordInput.value).toBe("s3cret");
    expect(assetEditorModal()).not.toBeNull();
  });

  it("编辑路径完成初始化后保存：新密码写入凭据库并更新资产", async () => {
    const onSaved = vi.fn();
    mounted = withClient(
      createElement(AssetEditor, {
        kind: "asset",
        initial: EXISTING_ASSET,
        onClose: () => {},
        onSaved,
      }),
    );
    await flushUntil(() => !!assetEditorModal());

    const passwordInput = assetEditorModal()!.querySelector<HTMLInputElement>(
      'input[aria-label="凭据密码"]',
    );
    if (!passwordInput) throw new Error("credential password input not found");
    setInputValue(passwordInput, "s3cret");
    await flushUntil(() => !!initModal());

    completeInit();
    await flushUntil(() => mocks.initMaster.mock.calls.length > 0);
    await flushUntil(() => initModal() === null);

    clickButton(assetEditorModal()!, "保存");
    await flushUntil(() => mocks.setCredential.mock.calls.length > 0);
    expect(mocks.setCredential).toHaveBeenCalledWith("web-1", "password", "s3cret");
    await flushUntil(() => mocks.assetUpdate.mock.calls.length > 0);
    await flushUntil(() => onSaved.mock.calls.length > 0);
    for (const call of mocks.toast.mock.calls) {
      expect(String(call[1])).not.toContain("尚未初始化");
    }
  });

  it("编辑资产改选「存入凭据库」新建私钥时同样先弹初始化浮层", async () => {
    mounted = withClient(
      createElement(AssetEditor, {
        kind: "asset",
        initial: EXISTING_ASSET,
        onClose: () => {},
        onSaved: () => {},
      }),
    );
    await flushUntil(() => !!assetEditorModal());
    const editor = assetEditorModal()!;
    const authSelect = [...editor.querySelectorAll("select")].find(
      (s) => s.value === "password",
    );
    if (!authSelect) throw new Error("auth select not found");
    setSelectValue(authSelect as HTMLSelectElement, "key");
    await flush();
    clickButton(editor, "存入凭据库");
    await flushUntil(() => !!initModal());
    expect(mocks.setCredential).not.toHaveBeenCalled();

    clickButton(initModal()!, "取消");
    await flushUntil(() => initModal() === null);
    expect(assetEditorModal()).not.toBeNull();
  });
});

describe("资产保存的 locked 解锁前置（M205 发现 5）", () => {
  function mountLockedEditor(onSaved: () => void) {
    mocks.vaultStatus.mockResolvedValue(LOCKED_VAULT);
    mounted = withClient(
      createElement(AssetEditor, {
        kind: "asset",
        initial: EXISTING_ASSET,
        onClose: () => {},
        onSaved,
      }),
    );
    return flushUntil(() => !!assetEditorModal());
  }

  function passwordInput(): HTMLInputElement {
    const input = assetEditorModal()!.querySelector<HTMLInputElement>(
      'input[aria-label="凭据密码"]',
    );
    if (!input) throw new Error("credential password input not found");
    return input;
  }

  it("locked 时保存先弹解锁，解锁成功后才写凭据库", async () => {
    const onSaved = vi.fn();
    await mountLockedEditor(onSaved);
    mocks.promptText.mockResolvedValue("right-password");

    setInputValue(passwordInput(), "s3cret");
    clickButton(assetEditorModal()!, "保存");

    await flushUntil(() => mocks.promptText.mock.calls.length > 0);
    expect(mocks.promptText).toHaveBeenCalledWith("保存凭据前需要解锁凭据库", "", {
      secret: true,
    });
    await flushUntil(() => mocks.setCredential.mock.calls.length > 0);
    expect(mocks.setCredential).toHaveBeenCalledWith("web-1", "password", "s3cret");
    await flushUntil(() => onSaved.mock.calls.length > 0);
  });

  it("解锁失败当场报错并中止保存，表单数据保留", async () => {
    await mountLockedEditor(vi.fn());
    mocks.promptText.mockResolvedValue("wrong-password");
    mocks.unlock.mockRejectedValue({ code: "bad_master_password", message: "主密码错误" });

    setInputValue(passwordInput(), "s3cret");
    clickButton(assetEditorModal()!, "保存");

    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("error", "解锁失败：主密码错误");
    expect(mocks.setCredential).not.toHaveBeenCalled();
    expect(mocks.assetUpdate).not.toHaveBeenCalled();
    await flush();
    expect(passwordInput().value).toBe("s3cret");
    expect(assetEditorModal()).not.toBeNull();
  });

  it("取消解锁则中止保存，不调用凭据接口", async () => {
    await mountLockedEditor(vi.fn());
    mocks.promptText.mockResolvedValue(null);

    setInputValue(passwordInput(), "s3cret");
    clickButton(assetEditorModal()!, "保存");

    await flushUntil(() => mocks.promptText.mock.calls.length > 0);
    await flush();
    expect(mocks.unlock).not.toHaveBeenCalled();
    expect(mocks.setCredential).not.toHaveBeenCalled();
    expect(assetEditorModal()).not.toBeNull();
  });
});

describe("新建凭据的 locked 解锁前置（M205 发现 5）", () => {
  function mountLockedModal(onSaved: () => void) {
    mocks.vaultStatus.mockResolvedValue(LOCKED_VAULT);
    mounted = withClient(
      createElement(NewCredentialModal, { onClose: () => {}, onSaved }),
    );
    const nameInput = mounted.container.querySelector<HTMLInputElement>(
      'input[placeholder="例如：db-prod"]',
    );
    if (!nameInput) throw new Error("name input not found");
    setInputValue(nameInput, "db-prod");
    const valueInput = mounted.container.querySelector<HTMLInputElement>('input[type="password"]');
    if (!valueInput) throw new Error("value input not found");
    setInputValue(valueInput, "s3cret");
  }

  it("locked 时保存先弹解锁，成功后按所填内容创建", async () => {
    const onSaved = vi.fn();
    mountLockedModal(onSaved);
    mocks.promptText.mockResolvedValue("right-password");

    clickButton(mounted!.container, "保存");
    await flushUntil(() => mocks.promptText.mock.calls.length > 0);
    expect(mocks.promptText).toHaveBeenCalledWith("保存凭据前需要解锁凭据库", "", {
      secret: true,
    });
    await flushUntil(() => mocks.setCredential.mock.calls.length > 0);
    expect(mocks.setCredential).toHaveBeenCalledWith("db-prod", "password", "s3cret", {
      source: undefined,
    });
    await flushUntil(() => onSaved.mock.calls.length > 0);
    expect(onSaved).toHaveBeenCalledWith("cred-1");
  });

  it("解锁失败不创建凭据，已填内容保留", async () => {
    mountLockedModal(vi.fn());
    mocks.promptText.mockResolvedValue("wrong-password");
    mocks.unlock.mockRejectedValue({ code: "bad_master_password", message: "主密码错误" });

    clickButton(mounted!.container, "保存");
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("error", "解锁失败：主密码错误");
    expect(mocks.setCredential).not.toHaveBeenCalled();
    await flush();
    expect(mounted!.container.querySelector<HTMLInputElement>('input[type="password"]')?.value).toBe(
      "s3cret",
    );
    expect(mounted!.container.textContent).toContain("新建凭据");
  });
});
