/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  layoutGet: vi.fn(),
  layoutPut: vi.fn(),
  user: { id: "account-a" } as { id: string } | null,
}));

vi.mock("../../ipc/commands", () => ({
  layoutApi: { get: mocks.layoutGet, put: mocks.layoutPut },
  assetApi: { list: vi.fn(async () => []), get: vi.fn() },
  terminalApi: { listLive: vi.fn(async () => []) },
  sessionApi: { list: vi.fn(async () => []), connect: vi.fn(), disconnect: vi.fn() },
  dbApi: {},
  vaultApi: {},
}));
vi.mock("../../ipc/events", () => ({ listenEvent: vi.fn(async () => () => {}) }));
vi.mock("../../features/auth/store", () => ({
  useAuth: { getState: () => ({ user: mocks.user }) },
}));

async function importModules() {
  const layout = await import("../../app/layout");
  const { useClosedTabs } = await import("../../app/closedTabs");
  const { useGroupCollapse } = await import("../../features/explorer/groupCollapse");
  return { layout, useClosedTabs, useGroupCollapse };
}

beforeEach(() => {
  vi.clearAllMocks();
  window.localStorage.clear();
  mocks.user = { id: "account-a" };
  mocks.layoutGet.mockResolvedValue({ revision: 1, updatedAt: 1, data: null });
  mocks.layoutPut.mockResolvedValue({ saved: true, revision: 2, conflict: false });
});

afterEach(() => {
  vi.resetModules();
});

describe("layout account switching bridges into account-scoped R-09 state", () => {
  it("loads the persisted closed-tab stack and collapsed groups for the signed-in account at boot", async () => {
    window.localStorage.setItem(
      "nexterm:closed-tabs-v1:account-a",
      JSON.stringify({
        v: 1,
        accountId: "account-a",
        generation: 4,
        nextSeq: 2,
        entries: [
          { key: "terminal:tab:t1", kind: "terminal", tabKind: "terminal", id: "t1", title: "终端", seq: 1 },
        ],
      }),
    );
    window.localStorage.setItem(
      "nexterm:collapsed-groups-v1:account-a",
      JSON.stringify({ v: 1, collapsed: ["g1"] }),
    );
    const { layout, useClosedTabs, useGroupCollapse } = await importModules();

    layout.startLayoutSync();
    await layout.layoutBootstrapped;

    expect(useClosedTabs.getState().stack.accountId).toBe("account-a");
    expect(useClosedTabs.getState().stack.entries.map((entry) => entry.id)).toEqual(["t1"]);
    expect(useGroupCollapse.getState().accountId).toBe("account-a");
    expect(useGroupCollapse.getState().collapsedIds).toEqual(["g1"]);
  });

  it("switches the closed-tab stack and collapsed groups on account switch, bumping the generation", async () => {
    const { layout, useClosedTabs, useGroupCollapse } = await importModules();
    layout.startLayoutSync();
    await layout.layoutBootstrapped;
    const generationBefore = useClosedTabs.getState().stack.generation;

    mocks.user = { id: "account-b" };
    layout.advanceLayoutAccount();

    expect(useClosedTabs.getState().stack.accountId).toBe("account-b");
    expect(useClosedTabs.getState().stack.entries).toEqual([]);
    expect(useClosedTabs.getState().stack.generation).toBeGreaterThan(generationBefore);
    expect(useGroupCollapse.getState().accountId).toBe("account-b");
    expect(useGroupCollapse.getState().collapsedIds).toEqual([]);
  });
});
