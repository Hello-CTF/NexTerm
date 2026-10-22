/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  clickButton,
  flush,
  mount,
  setInputValue,
  setSelectValue,
  waitFor,
  type MountedView,
} from "./features/reactTestUtils";
import type { SshImportPreviewDto } from "../ipc/types";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    preview: vi.fn(),
    apply: vi.fn(),
    groupList: vi.fn(),
    sessionConnect: vi.fn(),
    toast: vi.fn(),
  };
});
const platformMocks = vi.hoisted(() => ({ mac: true, mobile: false }));
vi.mock("../app/platform", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../app/platform")>();
  return { ...actual, isMac: () => platformMocks.mac, isMobile: () => platformMocks.mobile };
});
const gateMocks = vi.hoisted(() => ({
  ensureVaultInit: vi.fn(),
}));
vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    assetApi: { ...actual.assetApi, groupList: mocks.groupList },
    sessionApi: { connect: mocks.sessionConnect },
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
      warnings: ["Alias web-01 already exists with the same endpoint"],
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
      warnings: ["Alias nat-01 is already used by an asset with a different endpoint"],
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
  diagnostics: [{ code: "host-pattern-skipped", source: "~/.ssh/config:12", message: "Wildcard pattern skipped" }],
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
  platformMocks.mac = true;
  platformMocks.mobile = false;
  useUi.setState({ pushToast: mocks.toast, appDialog: null });
  gateMocks.ensureVaultInit.mockResolvedValue(true);
  mocks.groupList.mockResolvedValue([{ id: "g1", parentId: null, name: "生产", sort: 0, createdAt: 1, updatedAt: 1 }]);
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

describe("SSH import dialog", () => {
  it("requires explicit confirmation before previewing a Termius source", async () => {
    mounted = mountDialog();
    clickButton(mounted.container, "Termius");
    const previewButton = [...mounted.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "预览",
    )!;
    expect(previewButton.disabled).toBe(true);

    const confirmBox = [...mounted.container.querySelectorAll<HTMLInputElement>("input[type=checkbox]")][0]!;
    click(confirmBox);
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

  it("disables Termius with a reason on non-macOS desktop platforms", () => {
    platformMocks.mac = false;
    mounted = mountDialog();
    const button = [...mounted.container.querySelectorAll("button")].find(
      (candidate) => candidate.textContent?.trim() === "Termius",
    ) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    expect(mounted.container.textContent).toContain("仅支持 macOS");
    expect(mocks.preview).not.toHaveBeenCalled();
  });

  it("passes the selected group and normalized common tags to apply", async () => {
    mounted = mountDialog();
    await waitFor(() => expect(mounted!.container.querySelector("#ssh-import-group")?.querySelectorAll("option").length).toBe(2));
    setSelectValue(mounted.container.querySelector<HTMLSelectElement>("#ssh-import-group")!, "g1");
    setInputValue(mounted.container.querySelector<HTMLInputElement>("#ssh-import-tags")!, " prod, PROD, 欧洲 ");
    clickButton(mounted.container, "预览");
    await waitFor(() => expect(mocks.preview).toHaveBeenCalled());
    await flush();
    clickButton(mounted.container, "导入选中项");
    await waitFor(() => expect(mocks.apply).toHaveBeenCalled());
    expect(mocks.apply).toHaveBeenCalledWith(expect.objectContaining({ groupId: "g1", tags: "prod, 欧洲" }));
  });

  it("shows conflict badges, diagnostics, and default selections in preview", async () => {
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

  it("shows import results and refreshes queries after applying", async () => {
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
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
  });

  it("allows conflicts to switch to overwrite and passes them to apply", async () => {
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

  it("clears a stale preview instead of allowing another apply", async () => {
    mocks.apply.mockRejectedValue({ code: "bad_param", message: "导入预览已变化，请重新预览后再导入" });
    mounted = mountDialog();
    clickButton(mounted.container, "预览");
    await waitFor(() => expect(mocks.preview).toHaveBeenCalled());
    await flush();
    clickButton(mounted.container, "导入选中项");
    await waitFor(() => expect(mocks.apply).toHaveBeenCalled());
    await waitFor(() => expect(mounted!.container.textContent).toContain("重新预览"));
    expect(mounted.container.querySelector("button.nx-btn-primary")?.textContent).toBe("预览");
    expect(mounted.container.textContent).not.toContain("导入选中项");
  });

  it("shows preview errors and allows returning", async () => {
    mocks.preview.mockRejectedValue({ code: "bad_param", message: "SSH config file not found /x" });
    mounted = mountDialog();
    clickButton(mounted.container, "预览");
    await waitFor(() => expect(mocks.preview).toHaveBeenCalled());
    await flush();
    expect(mounted.container.querySelector("[role=alert]")?.textContent).toContain(
      "SSH config file not found",
    );
    clickButton(mounted.container, "返回");
    await flush();
    expect(mounted.container.querySelector("[role=alert]")).toBeNull();
  });
});

describe("quick import from the SSH ~/.ssh directory", () => {
  it("hides the path input and omits the old path after switching from a config path to ~/.ssh", async () => {
    mounted = mountDialog();
    const pathInput = mounted.container.querySelector<HTMLInputElement>('input[placeholder="~/.ssh/config"]')!;
    setInputValue(pathInput, "/home/demo/.ssh/config");
    await flush();

    clickButton(mounted.container, "~/.ssh 目录");
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("自动发现受支持的私钥");
    expect(text).toContain("不修改任何原始文件");
    expect(mounted.container.querySelector('input[placeholder="~/.ssh/config"]')).toBeNull();
    expect(mounted.container.querySelector<HTMLInputElement>("#ssh-import-tags")?.value).toBe("");

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

  it("shows counts and selects new items by default; one-click import submits only new items, excluding manual skips and conflict overwrites", async () => {
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

    // Manually uncheck one host and one key; switch the conflicting host to overwrite
    click(bastionBox);
    click(edBox);
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
    clickButton(mounted.container, "导入已勾选的新增项（1）");
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

  it("aborts import when the vault is uninitialized and the user cancels initialization", async () => {
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

// Regression: a Go nil slice marshals as JSON null, and the dialog used to read
// .length on warnings, crashing the whole app UI when importing ~/.ssh on
// Windows ("Cannot read properties of null (reading 'length')").
describe("SSH import dialog tolerates null list fields", () => {
  it("renders the preview when hosts and keys carry null warnings/aliases", async () => {
    const nulled: SshImportPreviewDto = {
      ...PREVIEW,
      hosts: PREVIEW.hosts.map((host) => ({ ...host, warnings: null as unknown as string[] })),
      keys: PREVIEW.keys.map((key) => ({
        ...key,
        aliases: null as unknown as string[],
        warnings: null as unknown as string[],
      })),
      diagnostics: null as unknown as SshImportPreviewDto["diagnostics"],
    };
    mocks.preview.mockResolvedValue(nulled);
    mounted = mountDialog();
    clickButton(mounted.container, "预览");
    await waitFor(() => expect(mocks.preview).toHaveBeenCalled());
    await flush();

    const text = mounted.container.textContent ?? "";
    expect(text).toContain("3 台主机（1 台可新增）");
    expect(text).toContain("2 个密钥（2 个可新增）");
    // key rows fall back to the fingerprint when aliases is null
    expect(text).toContain("SHA256:abc123");
  });

  it("renders the empty state when hosts and keys are null", async () => {
    const nulled = {
      ...PREVIEW,
      hosts: null as unknown as SshImportPreviewDto["hosts"],
      keys: null as unknown as SshImportPreviewDto["keys"],
      diagnostics: null as unknown as SshImportPreviewDto["diagnostics"],
    };
    mocks.preview.mockResolvedValue(nulled);
    mounted = mountDialog();
    clickButton(mounted.container, "预览");
    await waitFor(() => expect(mocks.preview).toHaveBeenCalled());
    await flush();

    expect(mounted.container.textContent).toContain("没有发现可导入的主机或密钥");
  });
});
