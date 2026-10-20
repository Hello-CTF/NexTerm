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
  passwordless: false,
};
const DPAPI_VAULT = {
  initialized: true,
  mode: "dpapi" as const,
  unlocked: true,
  autoLockMinutes: 30,
  passwordless: false,
};
const LOCKED_VAULT = {
  initialized: true,
  mode: "master" as const,
  unlocked: false,
  autoLockMinutes: 30,
  passwordless: false,
};
const PASSWORDLESS_LOCKED_VAULT = {
  initialized: true,
  mode: "master" as const,
  unlocked: false,
  autoLockMinutes: 30,
  passwordless: true,
};
const MASTER_UNLOCKED_VAULT = {
  initialized: true,
  mode: "master" as const,
  unlocked: true,
  autoLockMinutes: 30,
  passwordless: false,
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

describe("Vault-not-initialized gate (credentials entry)", () => {
  it("sidebar: New Credential with an uninitialized vault opens the init modal first, not the create form", async () => {
    mounted = withClient(createElement(CredentialsSidebar));
    await flushUntil(() => !!mounted!.container.querySelector('button[title="新建凭据"]'));
    expect(mounted!.container.querySelector(".nx-modal")).toBeNull();

    click(mounted!.container.querySelector('button[title="新建凭据"]')!);
    await flushUntil(() => modalHeaders().includes("初始化凭据保护"));

    expect(modalHeaders()).not.toContain("新建凭据");
    expect(mocks.initMaster).not.toHaveBeenCalled();
  });

  it("after init succeeds it resumes the original action: opens the new-credential form", async () => {
    mounted = withClient(createElement(CredentialsSidebar));
    await flushUntil(() => !!mounted!.container.querySelector('button[title="新建凭据"]'));
    click(mounted!.container.querySelector('button[title="新建凭据"]')!);
    await flushUntil(() => !!initModal());

    completeInit();
    await flushUntil(() => modalHeaders().includes("新建凭据"));
    expect(mocks.initMaster).toHaveBeenCalledWith("first-run-password");
    expect(mocks.toast).toHaveBeenCalledWith("success", "已开启密码保护");
    expect(initModal()).toBeNull();
  });

  it("init modal keeps the 8-character minimum; short passwords cannot be submitted", async () => {
    mounted = withClient(createElement(CredentialsSidebar));
    await flushUntil(() => !!mounted!.container.querySelector('button[title="新建凭据"]'));
    click(mounted!.container.querySelector('button[title="新建凭据"]')!);
    await flushUntil(() => !!initModal());

    const submit = [...initModal()!.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "启用保护",
    )!;
    setInputValue(initPasswordInput(), "short");
    expect(submit.disabled).toBe(true);
    expect(mocks.initMaster).not.toHaveBeenCalled();
  });

  it("init modal completes initialization with an empty password", async () => {
    mounted = withClient(createElement(CredentialsSidebar));
    await flushUntil(() => !!mounted!.container.querySelector('button[title="新建凭据"]'));
    click(mounted!.container.querySelector('button[title="新建凭据"]')!);
    await flushUntil(() => !!initModal());

    const submit = [...initModal()!.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "启用保护",
    )!;
    expect(submit.disabled).toBe(false);
    click(submit);
    await flushUntil(() => modalHeaders().includes("新建凭据"));
    expect(mocks.initMaster).toHaveBeenCalledWith("");
    expect(mocks.toast).toHaveBeenCalledWith("success", "已完成初始化，未设置密码");
  });

  it("canceling init neither opens the create form nor calls initialize", async () => {
    mounted = withClient(createElement(CredentialsSidebar));
    await flushUntil(() => !!mounted!.container.querySelector('button[title="新建凭据"]'));
    click(mounted!.container.querySelector('button[title="新建凭据"]')!);
    await flushUntil(() => !!initModal());

    clickButton(initModal()!, "取消");
    await flush();
    expect(mounted!.container.querySelector(".nx-modal")).toBeNull();
    expect(mocks.initMaster).not.toHaveBeenCalled();
  });

  it("initialized vault opens the new-credential form directly, with no init modal", async () => {
    mocks.vaultStatus.mockResolvedValue(DPAPI_VAULT);
    mounted = withClient(createElement(CredentialsSidebar));
    await flushUntil(() => !!mounted!.container.querySelector('button[title="新建凭据"]'));

    click(mounted!.container.querySelector('button[title="新建凭据"]')!);
    await flushUntil(() => modalHeaders().includes("新建凭据"));
    expect(initModal()).toBeNull();
    expect(mocks.initMaster).not.toHaveBeenCalled();
  });

  it("panel toolbar and empty-state New Credential also show the init modal first", async () => {
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

describe("Vault-not-initialized gate (asset flows)", () => {
  it("new asset (password auth) shows the init modal immediately, before any input", async () => {
    mounted = withClient(
      createElement(AssetEditor, { kind: "asset", onClose: () => {}, onSaved: () => {} }),
    );
    await flushUntil(() => !!initModal());

    expect(modalHeaders()).toContain("新建资产");
    expect(mocks.setCredential).not.toHaveBeenCalled();
  });

  it("after init it returns to the asset form: input is preserved and save is no longer rejected as not initialized", async () => {
    const onSaved = vi.fn();
    mounted = withClient(
      createElement(AssetEditor, { kind: "asset", onClose: () => {}, onSaved }),
    );
    await flushUntil(() => !!initModal());

    completeInit();
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
    await flushUntil(() => onSaved.mock.calls.length > 0 && !mounted!.container.textContent?.includes("保存中…"));
    for (const call of mocks.toast.mock.calls) {
      expect(String(call[1])).not.toContain("尚未初始化");
    }
  });

  it("canceling init keeps the asset form open for editing without losing input", async () => {
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

  it("editing an existing asset does not proactively show the init modal", async () => {
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

  it("typing a new password when editing an existing asset shows the init modal first; canceling preserves input", async () => {
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

  it("editing: save after init writes the new password to the vault and updates the asset", async () => {
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
    await flushUntil(() => initModal() === null && !!assetEditorModal());
    await flushUntil(() => initModal() === null);

    clickButton(assetEditorModal()!, "保存");
    await flushUntil(() => mocks.setCredential.mock.calls.length > 0);
    expect(mocks.setCredential).toHaveBeenCalledWith("web-1", "password", "s3cret");
    await flushUntil(() => mocks.assetUpdate.mock.calls.length > 0);
    await flushUntil(() => onSaved.mock.calls.length > 0 && !mounted!.container.textContent?.includes("保存中…"));
    for (const call of mocks.toast.mock.calls) {
      expect(String(call[1])).not.toContain("尚未初始化");
    }
  });

  it("switching to Save-to-vault for a new private key when editing also shows the init modal first", async () => {
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

describe("Locked-vault unlock gate on asset save (M205 finding 5)", () => {
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

  it("when locked, save prompts for unlock first and writes to the vault only after success", async () => {
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
    await flushUntil(() => onSaved.mock.calls.length > 0 && !mounted!.container.textContent?.includes("保存中…"));
  });

  it("failed unlock reports the error and aborts the save, preserving form data", async () => {
    await mountLockedEditor(vi.fn());
    mocks.promptText.mockResolvedValue("wrong-password");
    mocks.unlock.mockRejectedValue({ code: "bad_master_password", message: "保护密码错误" });

    setInputValue(passwordInput(), "s3cret");
    clickButton(assetEditorModal()!, "保存");

    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("error", "解锁失败：保护密码错误");
    expect(mocks.setCredential).not.toHaveBeenCalled();
    expect(mocks.assetUpdate).not.toHaveBeenCalled();
    await flush();
    expect(passwordInput().value).toBe("s3cret");
    expect(assetEditorModal()).not.toBeNull();
  });

  it("canceling unlock aborts the save without calling vault APIs", async () => {
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

describe("Locked-vault unlock gate on new credential (M205 finding 5)", () => {
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

  it("when locked, save prompts for unlock first, then creates with the entered values", async () => {
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
    await flushUntil(() => onSaved.mock.calls.length > 0 && !mounted!.container.textContent?.includes("保存中…"));
    expect(onSaved).toHaveBeenCalledWith("cred-1");
  });

  it("saving with a locked passwordless vault silently unlocks without prompting", async () => {
    mocks.vaultStatus.mockResolvedValue(PASSWORDLESS_LOCKED_VAULT);
    const onSaved = vi.fn();
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

    clickButton(mounted!.container, "保存");
    await flushUntil(() => mocks.setCredential.mock.calls.length > 0);
    expect(mocks.promptText).not.toHaveBeenCalled();
    expect(mocks.unlock).toHaveBeenCalledWith("");
    expect(mocks.setCredential).toHaveBeenCalledWith("db-prod", "password", "s3cret", {
      source: undefined,
    });
    await flushUntil(() => onSaved.mock.calls.length > 0 && !mounted!.container.textContent?.includes("保存中…"));
  });

  it("failed unlock does not create the credential and preserves input", async () => {
    mountLockedModal(vi.fn());
    mocks.promptText.mockResolvedValue("wrong-password");
    mocks.unlock.mockRejectedValue({ code: "bad_master_password", message: "保护密码错误" });

    clickButton(mounted!.container, "保存");
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("error", "解锁失败：保护密码错误");
    expect(mocks.setCredential).not.toHaveBeenCalled();
    await flush();
    expect(mounted!.container.querySelector<HTMLInputElement>('input[type="password"]')?.value).toBe(
      "s3cret",
    );
    expect(mounted!.container.textContent).toContain("新建凭据");
  });
});
