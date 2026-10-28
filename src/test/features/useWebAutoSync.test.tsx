/** @vitest-environment jsdom */
// Desktop auto-sync: a sync://status event that applied remote changes invalidates every synced view
// (assets/groups/snippets/credentials/vault-status) so lists show post-sync data without a remount; plain status
// events only refresh the health queries.
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
    statusHandlers: [] as ((payload?: { applied?: boolean }) => void)[],
    registerStore: vi.fn(),
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
  registerAccountPreferenceStore: mocks.registerStore,
}));

function Probe() {
  useDesktopAutoSync();
  return null;
}

describe("useDesktopAutoSync cache refresh after sync", () => {
  let view: MountedView | undefined;

  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    mocks.statusHandlers.length = 0;
    mocks.wake.mockResolvedValue(undefined);
    mocks.listenEvent.mockImplementation((event: string, handler: (payload?: { applied?: boolean }) => void) => {
      if (event === "sync://status") mocks.statusHandlers.push(handler);
      return Promise.resolve(() => {});
    });
  });

  it("invalidates assets/asset-search/groups/snippets/credentials/vault-status when sync://status reports applied remote changes", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    for (const key of ["assets", "asset-search", "groups", "snippets", "credentials", "vault-status"]) {
      qc.setQueryData([key], []);
    }
    view = mount(createElement(QueryClientProvider, { client: qc }, createElement(Probe)));
    await flush();

    const handler = mocks.statusHandlers.at(-1);
    if (!handler) throw new Error("sync://status was not subscribed");
    handler({ applied: true });
    await flush();

    expect(mocks.wake).not.toHaveBeenCalled();
    expect(mocks.registerStore).toHaveBeenCalledTimes(1);
    for (const key of ["assets", "asset-search", "groups", "snippets", "credentials", "vault-status"]) {
      expect(qc.getQueryState([key])?.isInvalidated).toBe(true);
    }
    view.unmount();
    view = undefined;
  });

  it("only refreshes health on a plain sync://status event", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    for (const key of ["assets", "groups", "sync-status", "sync-link"]) qc.setQueryData([key], []);
    view = mount(createElement(QueryClientProvider, { client: qc }, createElement(Probe)));
    await flush();

    const handler = mocks.statusHandlers.at(-1);
    if (!handler) throw new Error("sync://status was not subscribed");
    handler({});
    handler(undefined);
    await flush();

    expect(mocks.wake).not.toHaveBeenCalled();
    expect(mocks.registerStore).not.toHaveBeenCalled();
    expect(qc.getQueryState(["assets"])?.isInvalidated).toBe(false);
    expect(qc.getQueryState(["groups"])?.isInvalidated).toBe(false);
    expect(qc.getQueryState(["sync-status"])?.isInvalidated).toBe(true);
    expect(qc.getQueryState(["sync-link"])?.isInvalidated).toBe(true);
    view.unmount();
    view = undefined;
  });
});
