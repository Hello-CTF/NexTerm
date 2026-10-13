/** @vitest-environment jsdom */
// Desktop auto-sync: a sync://status event wakes the backend sync and invalidates every synced view
// (assets/groups/snippets/credentials/vault-status) so lists show post-sync data without a remount.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createElement } from "react";
import { flush, mount, type MountedView } from "./reactTestUtils";
import { useDesktopAutoSync } from "../../features/settings/useWebAutoSync";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    wake: vi.fn(),
    listenEvent: vi.fn(),
    statusHandlers: [] as (() => void)[],
  };
});

vi.mock("../../ipc/events", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ipc/events")>()),
  listenEvent: mocks.listenEvent,
}));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return { ...actual, syncApi: { ...actual.syncApi, wake: mocks.wake } };
});
vi.mock("../../app/preferences", () => ({
  createHttpPreferenceStore: vi.fn(() => ({})),
  registerAccountPreferenceStore: vi.fn(),
}));

function Probe() {
  useDesktopAutoSync();
  return null;
}

describe("useDesktopAutoSync 同步后刷新视图缓存", () => {
  let view: MountedView | undefined;

  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    mocks.statusHandlers.length = 0;
    mocks.wake.mockResolvedValue(undefined);
    mocks.listenEvent.mockImplementation((event: string, handler: () => void) => {
      if (event === "sync://status") mocks.statusHandlers.push(handler);
      return Promise.resolve(() => {});
    });
  });

  it("sync://status 唤醒后端同步并失效 assets/asset-search/groups/snippets/credentials/vault-status", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    for (const key of ["assets", "asset-search", "groups", "snippets", "credentials", "vault-status"]) {
      qc.setQueryData([key], []);
    }
    view = mount(createElement(QueryClientProvider, { client: qc }, createElement(Probe)));
    await flush();

    const handler = mocks.statusHandlers.at(-1);
    if (!handler) throw new Error("sync://status 未订阅");
    handler();
    await flush();

    expect(mocks.wake).toHaveBeenCalledTimes(1);
    for (const key of ["assets", "asset-search", "groups", "snippets", "credentials", "vault-status"]) {
      expect(qc.getQueryState([key])?.isInvalidated).toBe(true);
    }
    view.unmount();
    view = undefined;
  });
});
