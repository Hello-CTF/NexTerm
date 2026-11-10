/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, clickButton, flush, mount, setInputValue, waitFor, waitForRendered, type MountedView } from "./features/reactTestUtils";
import type { SshImportPreviewDto } from "../ipc/types";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    preview: vi.fn(),
    apply: vi.fn(),
    groupList: vi.fn(),
    openFile: vi.fn(),
    openFolder: vi.fn(),
    toast: vi.fn(),
  };
});
const platformMocks = vi.hoisted(() => ({ windows: false, mobile: false }));
vi.mock("../app/platform", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../app/platform")>();
  return { ...actual, isMac: () => false, isWindows: () => platformMocks.windows, isMobile: () => platformMocks.mobile };
});
vi.mock("../ipc/wails", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/wails")>();
  return { ...actual, openWailsFile: mocks.openFile, openWailsFolder: mocks.openFolder };
});
vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    assetApi: { ...actual.assetApi, groupList: mocks.groupList },
    sshImportApi: { preview: mocks.preview, apply: mocks.apply },
  };
});
vi.mock("../features/credentials/useVaultInitGate", () => ({
  useVaultInitGate: () => ({
    ensureVaultInit: vi.fn().mockResolvedValue(true),
    vaultInitGate: null,
    vaultStatus: { initialized: true, unlocked: true },
  }),
}));

import { SshImportDialog } from "../features/explorer/SshImportDialog";
import { useUi } from "../app/store";

const PREVIEW: SshImportPreviewDto = {
  source: "xshell",
  path: "C:/Users/alice/Documents/NetSarang Computer/7/Xshell/Sessions",
  hosts: [
    {
      id: "h0",
      alias: "db01",
      hostname: "db01.example.com",
      port: 2200,
      username: "dba",
      identityFiles: [],
      keyName: "",
      keyFingerprint: "",
      proxyJump: "",
      authMethod: "password",
      group: "生产/数据库",
      source: "db01.xsh",
      action: "add",
      warnings: [],
    },
  ],
  keys: [],
  diagnostics: [{ code: "password-not-imported", source: "Xshell", message: "Xshell passwords are not imported" }],
  truncated: false,
};

let mounted: MountedView | undefined;

function mountDialog(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(
    createElement(QueryClientProvider, { client }, createElement(SshImportDialog, { onClose: () => undefined })),
  );
}

function button(container: ParentNode, label: string): HTMLButtonElement {
  const found = [...container.querySelectorAll<HTMLButtonElement>("button")].find(
    (candidate) => candidate.textContent?.trim() === label,
  );
  if (!found) throw new Error(`Button not found: ${label}`);
  return found;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  platformMocks.windows = false;
  platformMocks.mobile = false;
  useUi.setState({ pushToast: mocks.toast, appDialog: null });
  mocks.groupList.mockResolvedValue([]);
  mocks.preview.mockResolvedValue(PREVIEW);
  mocks.apply.mockResolvedValue({
    assetsCreated: 1,
    assetsUpdated: 0,
    credentialsCreated: 0,
    credentialsUpdated: 0,
    groupsCreated: 2,
    skipped: 0,
    warnings: [],
  });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("SSH import from other clients", () => {
  it("offers every client source on desktop", async () => {
    mounted = mountDialog();
    await flush();
    for (const label of ["PuTTY", "MobaXterm", "Xshell", "SecureCRT"]) {
      expect(button(mounted.container, label).disabled).toBe(false);
    }
  });

  it("picks a session folder for Xshell, previews it and shows the folder group", async () => {
    mocks.openFolder.mockResolvedValue("/home/alice/Xshell/Sessions");
    mounted = mountDialog();
    await flush();
    clickButton(mounted.container, "Xshell");
    expect(button(mounted.container, "预览").disabled).toBe(true);
    clickButton(mounted.container, "选择文件夹…");
    await waitForRendered(() => expect(button(mounted!.container, "预览").disabled).toBe(false));
    expect(mocks.openFolder).toHaveBeenCalledTimes(1);
    expect(mocks.openFile).not.toHaveBeenCalled();
    clickButton(mounted.container, "预览");
    await waitForRendered(() => expect(mounted!.container.textContent).toContain("db01.example.com"));
    expect(mocks.preview).toHaveBeenCalledWith({ source: "xshell", path: "/home/alice/Xshell/Sessions" });
    expect(mounted.container.textContent).toContain("生产/数据库");
    expect(mounted.container.textContent).toContain("密码未导入");

    clickButton(mounted.container, "导入选中项");
    await waitFor(() => expect(mocks.apply).toHaveBeenCalledTimes(1));
    expect(mocks.apply.mock.calls[0][0]).toMatchObject({
      source: "xshell",
      path: "/home/alice/Xshell/Sessions",
      hosts: [{ id: "h0", action: "import" }],
    });
    await waitForRendered(() => expect(mounted!.container.textContent).toContain("新建分组 2"));
  });

  it("picks a .reg file for PuTTY and clears the path when the client changes", async () => {
    mocks.openFile.mockResolvedValue("/home/alice/putty.reg");
    mounted = mountDialog();
    await flush();
    clickButton(mounted.container, "PuTTY");
    clickButton(mounted.container, "选择文件…");
    await waitForRendered(() => expect(button(mounted!.container, "预览").disabled).toBe(false));
    expect(mocks.openFile).toHaveBeenCalledWith([{ name: "PuTTY 会话导出文件（.reg）", extensions: ["reg"] }]);
    const input = mounted.container.querySelector<HTMLInputElement>("input.font-mono");
    expect(input?.value).toBe("/home/alice/putty.reg");

    clickButton(mounted.container, "MobaXterm");
    expect(mounted.container.querySelector<HTMLInputElement>("input.font-mono")?.value).toBe("");
    expect(button(mounted.container, "预览").disabled).toBe(true);
  });

  it("previews the PuTTY registry without a file on Windows", async () => {
    platformMocks.windows = true;
    mounted = mountDialog();
    await flush();
    clickButton(mounted.container, "PuTTY");
    expect(button(mounted.container, "预览").disabled).toBe(false);
    clickButton(mounted.container, "预览");
    await waitFor(() => expect(mocks.preview).toHaveBeenCalledTimes(1));
    expect(mocks.preview).toHaveBeenCalledWith({ source: "putty", path: undefined });
    await waitForRendered(() => expect(mounted!.container.textContent).toContain("db01.example.com"));
  });

  it("accepts a typed path", async () => {
    mounted = mountDialog();
    await flush();
    clickButton(mounted.container, "SecureCRT");
    const input = mounted.container.querySelector<HTMLInputElement>("input.font-mono");
    if (!input) throw new Error("path input missing");
    setInputValue(input, "  /srv/crt/Sessions  ");
    await flush();
    click(button(mounted.container, "预览"));
    await waitFor(() => expect(mocks.preview).toHaveBeenCalledTimes(1));
    expect(mocks.preview).toHaveBeenCalledWith({ source: "securecrt", path: "/srv/crt/Sessions" });
    await waitForRendered(() => expect(mounted!.container.textContent).toContain("db01.example.com"));
  });
});
