/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { flushUntil, mount, type MountedView } from "./reactTestUtils";
import type { ContainerSummary } from "../../ipc/commands";

const mocks = vi.hoisted(() => ({
  ps: vi.fn(),
  images: vi.fn(),
  composeStacks: vi.fn(),
  action: vi.fn(),
  ask: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    dockerApi: {
      ...actual.dockerApi,
      ps: mocks.ps,
      images: mocks.images,
      composeStacks: mocks.composeStacks,
      action: mocks.action,
    },
  };
});
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask };
});

import { DockerPanel } from "../../features/docker/DockerPanel";
import { useUi } from "../../app/store";

const container = (id: string, state: string): ContainerSummary => ({
  id, name: id, image: "example", state, status: state, ports: "", composeProject: null,
});

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.images.mockResolvedValue([]);
  mocks.composeStacks.mockResolvedValue([]);
  mocks.action.mockResolvedValue(undefined);
  mocks.ask.mockResolvedValue(true);
  useUi.setState({ pushToast: mocks.toast, sessions: [] });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function mountPanel() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(createElement(QueryClientProvider, { client }, createElement(DockerPanel, { sessionId: "s1" })));
}

const deleteButtons = () => mounted!.container.querySelectorAll<HTMLButtonElement>('button[title="删除"]');

describe("removing containers", () => {
  it("offers deletion only for containers that are not running", async () => {
    mocks.ps.mockResolvedValue([container("web", "running"), container("old", "exited"), container("busy", "paused")]);
    mountPanel();
    await flushUntil(() => (mounted?.container.textContent ?? "").includes("old"));
    expect(deleteButtons()).toHaveLength(1);
    const row = deleteButtons()[0].closest("tr");
    expect(row?.textContent).toContain("old");
    expect(mounted!.container.querySelectorAll('button[title="停止"]')).toHaveLength(1);
  });
});
