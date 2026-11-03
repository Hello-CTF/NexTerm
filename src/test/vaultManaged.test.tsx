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
    await flushUntil(() => mounted!.container.textContent?.includes("服务端凭据库由服务器托管") ?? false);
    expect(mounted.container.querySelector('input[type="checkbox"]')).toBeNull();
    expect(mounted.container.querySelector('input[aria-label="闲置自动锁定分钟数"]')).toBeNull();
    expect(mounted.container.textContent).not.toContain("修改密码");
    expect(mounted.container.textContent).not.toContain("关闭加密");
  });

  it.each([false, undefined])("keeps the controls when managed is %s", async (managed) => {
    mocks.status.mockResolvedValue({ ...MASTER, managed });
    mounted = render(createElement(VaultCard));
    await flushUntil(() => !!mounted!.container.querySelector('input[aria-label="闲置自动锁定分钟数"]'));
    expect(mounted.container.textContent).not.toContain("服务端凭据库由服务器托管");
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
    expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("服务端凭据库由服务器托管"));
  });

  it("still locks a local password vault", async () => {
    await ready({ ...MASTER, managed: false });
    await lockVault!();
    expect(mocks.lock).toHaveBeenCalledTimes(1);
  });
});
