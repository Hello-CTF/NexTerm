/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  flush,
  mount,
  setInputValue,
  waitForRendered,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    list: vi.fn(),
    snippetList: vi.fn(),
    probeBatch: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: { list: mocks.list, snippetList: mocks.snippetList, probeBatch: mocks.probeBatch },
    sessionApi: {},
    dbApi: {},
    vaultApi: {},
    terminalApi: {},
  };
});

import { JumpPalette } from "../../app/JumpPalette";
import { useUi } from "../../app/store";
import { useAssetReachability } from "../../features/explorer/assetReachability";
import type { Asset } from "../../ipc/commands";

function assetOf(extra: Partial<Asset> & { id: string; name: string }): Asset {
  return {
    groupId: null,
    kind: "ssh",
    host: "10.0.0.8",
    port: 22,
    username: "root",
    authKind: "password",
    keyPath: null,
    credId: null,
    options: {},
    tags: "",
    note: "",
    sort: 0,
    createdAt: 1,
    updatedAt: 1,
    deletedAt: null,
    builtin: false,
    ...extra,
  };
}

const WEB = assetOf({ id: "a-web", name: "web-01" });
const DB = assetOf({ id: "a-db", name: "db-01", kind: "mysql", host: "10.0.0.9", port: 3306 });

function mountWithClient(node: ReactNode): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function optionByText(container: HTMLElement, text: string): HTMLButtonElement {
  const option = [...container.querySelectorAll<HTMLButtonElement>('[role="option"]')].find(
    (o) => o.textContent?.includes(text),
  );
  if (!option) throw new Error(`Palette option not found: ${text}`);
  return option;
}

function searchInput(container: HTMLElement): HTMLInputElement {
  const input = container.querySelector<HTMLInputElement>('[role="combobox"]');
  if (!input) throw new Error("Palette combobox not found");
  return input;
}

const mountedViews: MountedView[] = [];

async function mountPalette(props: { onClose: () => void }): Promise<MountedView> {
  const view = mountWithClient(createElement(JumpPalette, props));
  mountedViews.push(view);
  await waitForRendered(() => {
    expect(optionByText(view.container, "web-01")).toBeTruthy();
    expect(mocks.probeBatch).toHaveBeenCalledTimes(1);
    expect(Object.values(useAssetReachability.getState().entries).every((entry) => entry.state !== "checking")).toBe(true);
  });
  return view;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  useAssetReachability.setState({ entries: {}, batchError: null });
  useUi.setState({
    sessions: [],
    workspaces: [],
    activeWorkspaceId: null,
    connectingAssetIds: [],
    pushToast: mocks.toast,
  });
  mocks.list.mockResolvedValue([{ ...WEB }, { ...DB }]);
  mocks.snippetList.mockResolvedValue([]);
  mocks.probeBatch.mockResolvedValue({ results: [] });
});

afterEach(() => {
  for (const view of mountedViews.splice(0)) view.unmount();
  useAssetReachability.setState({ entries: {}, batchError: null });
});

describe("Command palette reachability and quick connect", () => {
  it("probes assets with hosts in a bounded batch on open", async () => {
    await mountPalette({ onClose: vi.fn() });
    await waitForRendered(() => expect(mocks.probeBatch).toHaveBeenCalledTimes(1));
    const [ids, timeoutMs, maxConcurrent] = mocks.probeBatch.mock.calls[0] as [
      string[],
      number,
      number,
    ];
    expect([...ids].sort()).toEqual(["a-db", "a-web"]);
    expect(timeoutMs).toBe(2500);
    expect(maxConcurrent).toBe(8);
  });

  it("shows reachability dots on connection entries", async () => {
    mocks.probeBatch.mockResolvedValue({
      results: [
        { assetId: "a-web", reachable: true, durationMs: 12 },
        { assetId: "a-db", reachable: false, error: "dial tcp timeout", durationMs: 2500 },
      ],
    });
    const view = await mountPalette({ onClose: vi.fn() });
    await waitForRendered(() => {
      const webRow = optionByText(view.container, "web-01");
      expect(webRow.querySelector('[aria-label="可达"]')).toBeTruthy();
      const dbRow = optionByText(view.container, "db-01");
      expect(dbRow.querySelector('[aria-label="不可达"]')).toBeTruthy();
    });
  });

  it("does not duplicate the manual probe action in asset actions", async () => {
    const view = await mountPalette({ onClose: vi.fn() });
    await waitForRendered(() => expect(mocks.probeBatch).toHaveBeenCalledTimes(1));
    setInputValue(searchInput(view.container), "probe web-01");
    expect(view.container.textContent).not.toContain("probe web-01");
  });

  it("switches to the hosts scope in place from the quick connect action", async () => {
    const onClose = vi.fn();
    const view = await mountPalette({ onClose });
    setInputValue(searchInput(view.container), "快速连接");
    click(optionByText(view.container, "快速连接"));
    await flush();
    expect(onClose).not.toHaveBeenCalled();
    expect(view.container.querySelector('[role="listbox"]')?.getAttribute("aria-label")).toBe("资产连接");
  });

  it("shows a connecting state on entries while a connection is in flight", async () => {
    const view = await mountPalette({ onClose: vi.fn() });
    act(() => useUi.setState({ connectingAssetIds: ["a-web"] }));
    await flush();
    expect(optionByText(view.container, "web-01").textContent).toContain("连接中…");
  });
});
