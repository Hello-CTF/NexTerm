/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { flush, flushUntil, mount, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  status: vi.fn(),
  lock: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("./helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../ipc/commands")>(), {
    vaultApi: { status: mocks.status, lock: mocks.lock },
  });
});

import { VaultCard } from "../features/settings/VaultCard";
import { useVaultStatus } from "../app/shell/useStatusBarState";
import { useUi } from "../app/store";

let lockVault: (() => Promise<void>) | undefined;
function Harness() {
  lockVault = useVaultStatus().lockVault;
  return null;
}

function render(node: React.ReactNode): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

const MASTER = {
  initialized: true,
  mode: "master",
  unlocked: true,
  autoLockMinutes: 15,
  passwordless: false,
  systemProtection: false,
};

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.lock.mockResolvedValue(undefined);
  useUi.setState({ pushToast: mocks.toast });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("VaultCard on a server-managed vault", () => {
  it("hides the management controls and says the server hosts the vault", async () => {
    mocks.status.mockResolvedValue({ ...MASTER, managed: true });
    mounted = render(createElement(VaultCard));
    await flushUntil(() => mounted!.container.textContent?.includes("服务端凭据库由服务端托管") ?? false);
    expect(mounted.container.querySelector('input[type="checkbox"]')).toBeNull();
    expect(mounted.container.querySelector('input[aria-label="闲置自动锁定分钟数"]')).toBeNull();
    expect(mounted.container.textContent).not.toContain("修改密码");
    expect(mounted.container.textContent).not.toContain("关闭加密");
  });

  it("warns that the server vault failed to unlock instead of only showing the managed text", async () => {
    mocks.status.mockResolvedValue({ ...MASTER, unlocked: false, managed: true });
    mounted = render(createElement(VaultCard));
    await flushUntil(() => mounted!.container.textContent?.includes("服务端凭据库未能解锁，请联系管理员") ?? false);
    expect(mounted.container.querySelector('[role="alert"]')?.textContent).toContain("请联系管理员");
    expect(mounted.container.textContent).toContain("服务端凭据库由服务端托管");
    expect(mounted.container.querySelector('input[type="checkbox"]')).toBeNull();
  });

  it("shows no unlock warning when the managed vault is unlocked", async () => {
    mocks.status.mockResolvedValue({ ...MASTER, unlocked: true, managed: true });
    mounted = render(createElement(VaultCard));
    await flushUntil(() => mounted!.container.textContent?.includes("服务端凭据库由服务端托管") ?? false);
    expect(mounted.container.textContent).not.toContain("未能解锁");
  });

  it.each([false, undefined])("keeps the controls when managed is %s", async (managed) => {
    mocks.status.mockResolvedValue({ ...MASTER, managed });
    mounted = render(createElement(VaultCard));
    await flushUntil(() => !!mounted!.container.querySelector('input[aria-label="闲置自动锁定分钟数"]'));
    expect(mounted.container.textContent).not.toContain("服务端凭据库由服务端托管");
    expect(mounted.container.textContent).toContain("修改密码");
  });
});

describe("lockVault", () => {
  async function ready(status: object) {
    mocks.status.mockResolvedValue(status);
    mounted = render(createElement(Harness));
    await flushUntil(() => mocks.status.mock.calls.length > 0);
    for (let i = 0; i < 5; i++) await flush();
  }

  it("does not call vault_lock for a managed vault and explains why", async () => {
    await ready({ ...MASTER, managed: true });
    await lockVault!();
    expect(mocks.lock).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("服务端凭据库由服务端托管"));
  });

  it("still locks a local password vault", async () => {
    await ready({ ...MASTER, managed: false });
    await lockVault!();
    expect(mocks.lock).toHaveBeenCalledTimes(1);
  });
});
