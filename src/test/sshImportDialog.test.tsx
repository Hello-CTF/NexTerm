/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  clickButton,
  flush,
  mount,
  setInputValue,
  setSelectValue,
  waitFor,
  type MountedView,
} from "./features/reactTestUtils";
import type { SshImportPreviewDto } from "../ipc/types";

const mocks = vi.hoisted(() => ({
  preview: vi.fn(),
  apply: vi.fn(),
  toast: vi.fn(),
}));
const gateMocks = vi.hoisted(() => ({
  ensureVaultInit: vi.fn(),
}));
vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    sshImportApi: { preview: mocks.preview, apply: mocks.apply },
  };
});
vi.mock("../features/credentials/useVaultInitGate", () => ({
  useVaultInitGate: () => ({
    ensureVaultInit: gateMocks.ensureVaultInit,
    vaultInitGate: null,
    vaultStatus: { initialized: true, unlocked: true },
  }),
}));

import { SshImportDialog } from "../features/explorer/SshImportDialog";
import { useUi } from "../app/store";

const PREVIEW: SshImportPreviewDto = {
  source: "ssh-config",
  path: "/home/demo/.ssh/config",
  hosts: [
    {
      id: "h0",
      alias: "bastion",
      hostname: "bastion.example.com",
      port: 22,
      username: "admin",
      identityFiles: [],
      keyName: "",
      keyFingerprint: "",
      proxyJump: "",
      authMethod: "agent",
      source: "ssh-config",
      action: "add",
      warnings: [],
    },
    {
      id: "h1",
      alias: "web-01",
      hostname: "127.0.0.1",
      port: 22,
      username: "deploy",
      identityFiles: [],
      keyName: "",
      keyFingerprint: "",
      proxyJump: "",
      authMethod: "agent",
      source: "ssh-config",
      action: "skip-duplicate",
      warnings: ["别名 web-01 已存在且端点相同"],
    },
    {
      id: "h2",
      alias: "nat-01",
      hostname: "nat-new.example.com",
      port: 22,
      username: "root",
      identityFiles: [],
      keyName: "",
      keyFingerprint: "",
      proxyJump: "",
      authMethod: "agent",
      source: "ssh-config",
      action: "conflict-alias",
      warnings: ["别名 nat-01 已被一台端点不同的资产使用"],
    },
  ],
  keys: [
    {
      id: "k0",
      aliases: ["id_rsa_demo"],
      fingerprint: "SHA256:abc123",
      keyType: "ssh-rsa",
      path: "/home/demo/.ssh/id_rsa_demo",
      source: "ssh-config",
      action: "add",
      warnings: [],
    },
    {
      id: "k1",
      aliases: ["id_ed25519_demo"],
      fingerprint: "SHA256:def456",
      keyType: "ssh-ed25519",
      path: "/home/demo/.ssh/id_ed25519_demo",
      source: "ssh-config",
      action: "add",
      warnings: [],
    },
  ],
  diagnostics: [{ code: "host-pattern-skipped", source: "~/.ssh/config:12", message: "通配模式已跳过" }],
  truncated: false,
};

let mounted: MountedView | undefined;

function mountDialog(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(SshImportDialog, { onClose: () => undefined }),
    ),
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: mocks.toast, appDialog: null });
  gateMocks.ensureVaultInit.mockResolvedValue(true);
  mocks.preview.mockResolvedValue(PREVIEW);
  mocks.apply.mockResolvedValue({
    assetsCreated: 2,
    assetsUpdated: 0,
    credentialsCreated: 1,
    credentialsUpdated: 0,
    skipped: 1,
    warnings: [],
  });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("SSH 导入对话框", () => {
  it("Termius 来源需要显式确认后才能预览", async () => {
    mounted = mountDialog();
    clickButton(mounted.container, "Termius");
    const previewButton = [...mounted.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "预览",
    )!;
    expect(previewButton.disabled).toBe(true);

    const confirmBox = [...mounted.container.querySelectorAll<HTMLInputElement>("input[type=checkbox]")][0]!;
    confirmBox.click();
    await flush();
    expect(previewButton.disabled).toBe(false);

    clickButton(mounted.container, "预览");
    await waitFor(() =>
      expect(mocks.preview).toHaveBeenCalledWith({
        source: "termius",
        path: undefined,
        confirmed: true,
      }),
    );
  });

  it("预览展示冲突徽章、诊断与默认勾选", async () => {
    mounted = mountDialog();
    clickButton(mounted.container, "预览");
    await waitFor(() => expect(mocks.preview).toHaveBeenCalled());
    await flush();

    const text = mounted.container.textContent ?? "";
    expect(text).toContain("新增");
    expect(text).toContain("重复 · 跳过");
    expect(text).toContain("别名冲突");
    expect(text).toContain("通配 Host 已跳过");
    expect(text).toContain("3 台主机（1 台可新增）");

    const bastionBox = mounted.container.querySelector<HTMLInputElement>(
      'input[aria-label="导入主机 bastion"]',
    )!;
    expect(bastionBox.checked).toBe(true);
    const dupBox = mounted.container.querySelector<HTMLInputElement>(
      'input[aria-label="导入主机 web-01"]',
    );
    expect(dupBox).toBeNull();
  });

  it("应用导入展示结果并刷新查询", async () => {
    mounted = mountDialog();
    clickButton(mounted.container, "预览");
    await waitFor(() => expect(mocks.preview).toHaveBeenCalled());
    await flush();

    clickButton(mounted.container, "导入选中项");
    await waitFor(() => expect(mocks.apply).toHaveBeenCalled());
    await flush();
    expect(mounted.container.textContent).toContain("新增主机 2");
    expect(mocks.apply).toHaveBeenCalledWith({
      source: "ssh-config",
      path: undefined,
      hosts: [
        { id: "h0", action: "import" },
        { id: "h1", action: "skip" },
        { id: "h2", action: "skip" },
      ],
      keys: [
        { id: "k0", action: "import" },
        { id: "k1", action: "import" },
      ],
    });
    expect(mocks.toast).toHaveBeenCalledWith(
      "success",
      expect.stringContaining("导入完成"),
    );
  });

  it("冲突项可切换为覆盖并传递给 apply", async () => {
    mounted = mountDialog();
    clickButton(mounted.container, "预览");
    await waitFor(() => expect(mocks.preview).toHaveBeenCalled());
    await flush();

    const strategy = mounted.container.querySelector<HTMLSelectElement>(
      'select[aria-label="主机 nat-01 的冲突处理"]',
    )!;
    expect(strategy.value).toBe("skip");
    setSelectValue(strategy, "overwrite");

    clickButton(mounted.container, "导入选中项");
    await waitFor(() => expect(mocks.apply).toHaveBeenCalled());
    const args = mocks.apply.mock.calls[0]![0] as {
      hosts: { id: string; action: string }[];
    };
    expect(args.hosts).toContainEqual({ id: "h2", action: "overwrite" });
  });

  it("预览失败展示错误且可返回", async () => {
    mocks.preview.mockRejectedValue({ code: "bad_param", message: "找不到 SSH 配置文件 /x" });
    mounted = mountDialog();
    clickButton(mounted.container, "预览");
    await waitFor(() => expect(mocks.preview).toHaveBeenCalled());
    await flush();
    expect(mounted.container.querySelector("[role=alert]")?.textContent).toContain(
      "找不到 SSH 配置文件",
    );
    clickButton(mounted.container, "返回");
    await flush();
    expect(mounted.container.querySelector("[role=alert]")).toBeNull();
  });
});

describe("SSH ~/.ssh 目录快速导入", () => {
  it("先填配置路径再切换到 ~/.ssh 目录时隐藏输入且请求不带旧路径", async () => {
    mounted = mountDialog();
    const pathInput = mounted.container.querySelector<HTMLInputElement>("input.nx-input")!;
    setInputValue(pathInput, "/home/demo/.ssh/config");
    await flush();

    clickButton(mounted.container, "~/.ssh 目录");
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("自动发现受支持的私钥");
    expect(text).toContain("不修改任何原始文件");
    expect(mounted.container.querySelector("input")).toBeNull();

    clickButton(mounted.container, "预览");
    await waitFor(() =>
      expect(mocks.preview).toHaveBeenCalledWith({
        source: "ssh-home",
        path: undefined,
      }),
    );
    await flush();

    clickButton(mounted.container, "导入选中项");
    await waitFor(() => expect(mocks.apply).toHaveBeenCalled());
    expect(mocks.apply).toHaveBeenCalledWith(
      expect.objectContaining({ source: "ssh-home", path: undefined }),
    );
  });

  it("预览显示数量并默认勾选可新增项；一键导入只提交可新增项，不含手动 skip 与冲突覆盖", async () => {
    mounted = mountDialog();
    clickButton(mounted.container, "~/.ssh 目录");
    clickButton(mounted.container, "预览");
    await waitFor(() => expect(mocks.preview).toHaveBeenCalled());
    await flush();

    const text = mounted.container.textContent ?? "";
    expect(text).toContain("导入 SSH 主机与密钥");
    expect(text).toContain("3 台主机（1 台可新增）");
    expect(text).toContain("2 个密钥（2 个可新增）");
    const bastionBox = mounted.container.querySelector<HTMLInputElement>(
      'input[aria-label="导入主机 bastion"]',
    )!;
    expect(bastionBox.checked).toBe(true);
    const rsaBox = mounted.container.querySelector<HTMLInputElement>(
      'input[aria-label="导入密钥 id_rsa_demo"]',
    )!;
    expect(rsaBox.checked).toBe(true);
    const edBox = mounted.container.querySelector<HTMLInputElement>(
      'input[aria-label="导入密钥 id_ed25519_demo"]',
    )!;
    expect(edBox.checked).toBe(true);

    // 手动取消一台主机与一个密钥，冲突主机改为覆盖
    bastionBox.click();
    edBox.click();
    await flush();
    const strategy = mounted.container.querySelector<HTMLSelectElement>(
      'select[aria-label="主机 nat-01 的冲突处理"]',
    )!;
    setSelectValue(strategy, "overwrite");
    await flush();

    mocks.apply.mockResolvedValue({
      assetsCreated: 0,
      assetsUpdated: 0,
      credentialsCreated: 1,
      credentialsUpdated: 0,
      skipped: 0,
      warnings: [],
    });
    clickButton(mounted.container, "一键导入全部可新增项 (1)");
    await waitFor(() => expect(mocks.apply).toHaveBeenCalled());
    expect(gateMocks.ensureVaultInit).toHaveBeenCalled();
    expect(mocks.apply).toHaveBeenCalledWith({
      source: "ssh-home",
      path: undefined,
      hosts: [],
      keys: [{ id: "k0", action: "import" }],
    });
    await flush();
    expect(mounted.container.textContent).toContain("新增密钥 1");
  });

  it("凭据库未初始化且用户取消初始化时中止导入", async () => {
    gateMocks.ensureVaultInit.mockResolvedValue(false);
    mounted = mountDialog();
    clickButton(mounted.container, "预览");
    await waitFor(() => expect(mocks.preview).toHaveBeenCalled());
    await flush();

    clickButton(mounted.container, "导入选中项");
    await waitFor(() => expect(gateMocks.ensureVaultInit).toHaveBeenCalled());
    await flush();
    expect(mocks.apply).not.toHaveBeenCalled();
    expect(mounted.container.textContent).toContain("导入选中项");
  });
});
