/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  clickButton,
  flush,
  mount,
  setInputValue,
  waitFor,
  type MountedView,
} from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  generateKey: vi.fn(),
  vaultStatus: vi.fn(),
  unlock: vi.fn(),
  promptText: vi.fn(),
  toast: vi.fn(),
}));
vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    vaultApi: { generateKey: mocks.generateKey, status: mocks.vaultStatus, unlock: mocks.unlock },
  };
});
vi.mock("../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ui/dialogs")>();
  return { ...actual, promptText: mocks.promptText };
});

import { GenerateKeyModal } from "../features/credentials/GenerateKeyModal";
import { useUi } from "../app/store";

const GENERATED = {
  id: "cred-1",
  name: "deploy-key",
  algorithm: "ed25519",
  fingerprint: "SHA256:9f8e7d6c5b4a3210",
  publicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeDemoKey demo@deploy",
};

let mounted: MountedView | undefined;
const writeText = vi.fn();

function mountModal(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(GenerateKeyModal, { onClose: () => undefined, onSaved: () => undefined }),
    ),
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: mocks.toast, appDialog: null });
  writeText.mockResolvedValue(undefined);
  Object.assign(navigator, { clipboard: { writeText } });
  mocks.generateKey.mockResolvedValue(GENERATED);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, mode: "master", unlocked: true });
  mocks.promptText.mockResolvedValue(null);
  mocks.unlock.mockResolvedValue(undefined);
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("生成密钥对对话框", () => {
  it("名称为空时禁止生成", () => {
    mounted = mountModal();
    const button = [...mounted.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "生成并入库",
    )!;
    expect(button.disabled).toBe(true);
  });

  it("生成成功展示指纹与 authorized_keys 公钥行", async () => {
    mounted = mountModal();
    const nameInput = mounted.container.querySelector<HTMLInputElement>(
      'input[placeholder="例如：github-deploy"]',
    )!;
    setInputValue(nameInput, "deploy-key");
    clickButton(mounted.container, "生成并入库");

    await waitFor(() =>
      expect(mocks.generateKey).toHaveBeenCalledWith("deploy-key", "ed25519", undefined),
    );
    await waitFor(() => expect(mounted!.container.textContent).toContain("SHA256:9f8e7d6c5b4a3210"));
    expect(mounted.container.textContent).toContain(GENERATED.publicKey);

    clickButton(mounted.container, "复制");
    await flush();
    expect(writeText).toHaveBeenCalledWith(GENERATED.fingerprint);
    expect(mocks.toast).toHaveBeenCalledWith("success", "已复制指纹");
  });

  it("RSA 算法与口令传递给 IPC", async () => {
    mocks.generateKey.mockResolvedValue({ ...GENERATED, algorithm: "rsa", publicKey: "ssh-rsa AAAAB3Fake" });
    mounted = mountModal();
    clickButton(mounted.container, "RSA 4096");
    setInputValue(
      mounted.container.querySelector<HTMLInputElement>('input[placeholder="例如：github-deploy"]')!,
      "legacy-key",
    );
    setInputValue(mounted.container.querySelector<HTMLInputElement>('input[type=password]')!, "hunter2");
    clickButton(mounted.container, "生成并入库");

    await waitFor(() =>
      expect(mocks.generateKey).toHaveBeenCalledWith("legacy-key", "rsa", "hunter2"),
    );
  });

  it("生成失败 toast 错误并保持表单", async () => {
    mocks.generateKey.mockRejectedValue({ code: "bad_param", message: "已存在同名凭据" });
    mounted = mountModal();
    setInputValue(
      mounted.container.querySelector<HTMLInputElement>('input[placeholder="例如：github-deploy"]')!,
      "deploy-key",
    );
    clickButton(mounted.container, "生成并入库");

    await waitFor(() =>
      expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("已存在同名凭据")),
    );
    expect(mounted.container.textContent).toContain("生成并入库");
  });

  it("locked 时先弹解锁再生成，解锁成功才调用 IPC", async () => {
    mocks.vaultStatus.mockResolvedValue({ initialized: true, mode: "master", unlocked: false });
    mocks.promptText.mockResolvedValue("right-password");
    mounted = mountModal();
    setInputValue(
      mounted.container.querySelector<HTMLInputElement>('input[placeholder="例如：github-deploy"]')!,
      "deploy-key",
    );
    clickButton(mounted.container, "生成并入库");

    await waitFor(() =>
      expect(mocks.promptText).toHaveBeenCalledWith("生成密钥前需要解锁凭据库", "", {
        secret: true,
      }),
    );
    await waitFor(() => expect(mocks.generateKey).toHaveBeenCalledWith("deploy-key", "ed25519", undefined));
  });

  it("解锁失败当场报错，不生成且表单保留", async () => {
    mocks.vaultStatus.mockResolvedValue({ initialized: true, mode: "master", unlocked: false });
    mocks.promptText.mockResolvedValue("wrong-password");
    mocks.unlock.mockRejectedValue({ code: "bad_master_password", message: "主密码错误" });
    mounted = mountModal();
    const nameInput = mounted.container.querySelector<HTMLInputElement>(
      'input[placeholder="例如：github-deploy"]',
    )!;
    setInputValue(nameInput, "deploy-key");
    clickButton(mounted.container, "生成并入库");

    await waitFor(() =>
      expect(mocks.toast).toHaveBeenCalledWith("error", "解锁失败：主密码错误"),
    );
    expect(mocks.generateKey).not.toHaveBeenCalled();
    await flush();
    expect(nameInput.value).toBe("deploy-key");
    expect(mounted.container.textContent).toContain("生成并入库");
  });
});
