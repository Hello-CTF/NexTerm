/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({ preview: vi.fn(), apply: vi.fn(), groupList: vi.fn() }));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: { ...actual.assetApi, groupList: mocks.groupList },
    sshImportApi: { preview: mocks.preview, apply: mocks.apply },
  };
});
vi.mock("../../features/credentials/useVaultInitGate", () => ({
  useVaultInitGate: () => ({
    ensureVaultInit: vi.fn().mockResolvedValue(true),
    vaultInitGate: null,
    vaultStatus: { initialized: true, unlocked: true },
  }),
}));

import { SshImportDialog } from "../../features/explorer/SshImportDialog";
import { useAuth } from "../../features/auth/store";

const REASON = "多用户服务端不支持从服务器路径导入：它会读取服务器上的 SSH 配置和私钥";

let mounted: MountedView | undefined;

function open(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(SshImportDialog, { onClose: () => undefined })));
}

function button(c: HTMLElement, label: string): HTMLButtonElement {
  const found = [...c.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label);
  if (!found) throw new Error(`${label} not found`);
  return found;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.groupList.mockResolvedValue([]);
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  useAuth.setState({ user: null });
});

describe("SSH import dialog on a multi-user server", () => {
  it("disables the server-reading sources with the backend reason and sends no preview", async () => {
    useAuth.setState({ user: { id: "u1", username: "alice" } as never });
    mounted = open();
    await flush();
    const c = mounted.container;
    expect(button(c, "OpenSSH 配置").disabled).toBe(true);
    expect(button(c, "~/.ssh 目录").disabled).toBe(true);
    expect(button(c, "预览").disabled).toBe(true);
    expect(c.querySelector('[role="note"]')?.textContent).toBe(`↳ ${REASON}`);
    click(button(c, "预览"));
    await flush();
    expect(mocks.preview).not.toHaveBeenCalled();
  });

  it("keeps the other-client sources desktop-only in the browser", async () => {
    mounted = open();
    await flush();
    const c = mounted.container;
    for (const label of ["PuTTY", "MobaXterm", "Xshell", "SecureCRT"]) {
      expect(button(c, label).disabled).toBe(true);
    }
    expect(c.textContent).toContain("仅支持桌面版");
  });

  it("keeps the sources usable without an account", async () => {
    mounted = open();
    await flush();
    const c = mounted.container;
    expect(button(c, "OpenSSH 配置").disabled).toBe(false);
    expect(button(c, "~/.ssh 目录").disabled).toBe(false);
    expect(button(c, "预览").disabled).toBe(false);
    expect(c.querySelector('[role="note"]')).toBeNull();
  });
});
