/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  clickButton,
  flush,
  mount,
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
vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    sshImportApi: { preview: mocks.preview, apply: mocks.apply },
  };
});

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
    expect(text).toContain("host-pattern-skipped");
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
      keys: [{ id: "k0", action: "import" }],
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
