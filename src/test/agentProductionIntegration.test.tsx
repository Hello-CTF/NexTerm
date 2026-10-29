/** @vitest-environment jsdom */

// Deterministic production integration: the reviewed panels run against the
// real production adapter (typed IPC only) with the transport mocked at the
// commands seam. Backend-owned paging is proven by exact cursor arguments —
// the UI never slices accumulated results in memory.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { clickButton, flush, mount, setInputValue, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  call: vi.fn(),
  assetList: vi.fn(),
  ask: vi.fn(),
  toast: vi.fn(),
  resolveApproval: vi.fn(),
}));

vi.mock("../ipc/commands", async () => {
  const { createFakeApi } = await import("./helpers/createFakeApi");
  return createFakeApi({ call: mocks.call, assetApi: { list: mocks.assetList } });
});
vi.mock("../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ui/dialogs")>();
  return { ...actual, ask: mocks.ask };
});

import type {
  RosterEntry,
} from "../ipc/generated/types";
import { AgentRosterPanel } from "../features/ai/AgentRosterPanel";
import { createAgentRosterApi } from "../features/ai/agentProduction";
import { useUi } from "../app/store";

let view: MountedView | null = null;

afterEach(() => {
  view?.unmount();
  view = null;
});

describe("production integration: roster panel", () => {
  function rosterEntry(partial: Partial<RosterEntry> = {}): RosterEntry {
    return {
      id: "row-1",
      conversationId: "conv-1",
      handle: "build",
      alias: "build",
      tabId: "tab-1",
      state: "active",
      incarnation: "inc-1",
      jobId: "job-1",
      createdAt: 1_760_000_111_000,
      updatedAt: 1_760_000_222_000,
      releasedAt: null,
      ...partial,
    };
  }

  beforeEach(() => {
    vi.clearAllMocks();
    mocks.ask.mockResolvedValue(true);
    useUi.setState({ pushToast: mocks.toast });
    mocks.assetList.mockResolvedValue([{ id: "asset-1", tags: "env:production" }]);
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") {
        return Promise.resolve([
          { entry: rosterEntry(), presence: "connected", assetId: "asset-1", host: "web-01", cwd: "/srv/app" },
          {
            entry: rosterEntry({ id: "row-2", handle: "deploy", tabId: "tab-2" }),
            presence: "disconnected",
            assetId: "",
            host: "",
            cwd: "",
          },
        ]);
      }
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
  });

  function mountRoster(callbacks: { onMention?: (token: string) => void; onOpened?: (tabId: string) => void } = {}) {
    const api = createAgentRosterApi({
      conversationId: "conv-1",
      identity: { ownerId: "u-1", ownerName: "alice" },
      activeJobId: () => "job-live",
      resolveApproval: mocks.resolveApproval,
    });
    view = mount(
      createElement(AgentRosterPanel, {
        api,
        onMention: callbacks.onMention,
        onOpened: callbacks.onOpened,
      }),
    );
    return api;
  }

  it("renders real rows with the verifiable host/cwd and the production badge", async () => {
    mountRoster();
    await flush();
    const build = view!.container.querySelector('[data-handle="build"]')!;
    const text = build.textContent ?? "";
    // The backend's verifiable target evidence renders as the full identity.
    expect(text).toContain("build@web-01:/srv/app");
    expect(text).toContain("已附加");
    expect(text).toContain("生产环境");
    const deploy = view!.container.querySelector('[data-handle="deploy"]')!;
    // Unverifiable target evidence stays honestly unknown: no fabricated
    // handle@host ever appears.
    expect(deploy.textContent).toContain("deploy");
    expect(deploy.textContent).not.toContain("deploy@");
    expect(deploy.textContent).toContain("已断开");
    expect(deploy.textContent).not.toContain("生产环境");
  });

  it("renames the alias through the real command and shows the alias chip", async () => {
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") {
        return Promise.resolve([
          { entry: rosterEntry(), presence: "connected", assetId: "asset-1", host: "web-01", cwd: "/srv/app" },
        ]);
      }
      if (cmd === "ai_roster_rename") return Promise.resolve(rosterEntry({ alias: "生产构建机" }));
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    mountRoster();
    await flush();
    const build = view!.container.querySelector('[data-handle="build"]')! as HTMLElement;
    clickButton(build, "重命名");
    const input = build.querySelector('input[aria-label="新别名"]') as HTMLInputElement;
    expect(input.value).toBe("build");
    setInputValue(input, "生产构建机");
    clickButton(build, "保存");
    await flush();
    expect(mocks.call).toHaveBeenCalledWith("ai_roster_rename", {
      handle: "build",
      alias: "生产构建机",
      createdAt: 1_760_000_111_000,
      incarnation: "inc-1",
    });
    const row = view!.container.querySelector('[data-handle="build"]')!;
    expect(row.textContent).toContain("别名 生产构建机");
    // The durable identity still renders, and the mention token stays the handle.
    expect(row.textContent).toContain("build@web-01:/srv/app");
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("别名已更新为 生产构建机"));
  });

  it("attaches through the real IPC with the active job and reports the tab", async () => {
    const onOpened = vi.fn();
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") {
        return Promise.resolve([{ entry: rosterEntry(), presence: "connected", assetId: "asset-1" }]);
      }
      if (cmd === "ai_roster_attach") {
        return Promise.resolve({ entry: rosterEntry(), resolution: "connected", assetId: "asset-1" });
      }
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    mountRoster({ onOpened });
    await flush();
    const build = view!.container.querySelector('[data-handle="build"]')!;
    clickButton(build as HTMLElement, "附加");
    await flush();
    expect(mocks.call).toHaveBeenCalledWith("ai_roster_attach", { handle: "build", jobId: "job-live" });
    expect(onOpened).toHaveBeenCalledWith("tab-1", expect.objectContaining({ handle: "build" }));
  });

  it("releases with the exact durable identity and keeps the row release-pending", async () => {
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") {
        return Promise.resolve([{ entry: rosterEntry(), presence: "connected", assetId: "asset-1" }]);
      }
      if (cmd === "ai_roster_release") {
        return Promise.resolve(rosterEntry({ state: "release_pending" }));
      }
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    mountRoster();
    await flush();
    const build = view!.container.querySelector('[data-handle="build"]')!;
    clickButton(build as HTMLElement, "释放");
    await flush();
    // Production target: the confirmation names the production impact.
    const prompt = mocks.ask.mock.calls[0][0] as string;
    expect(prompt).toContain("生产环境");
    expect(mocks.call).toHaveBeenCalledWith("ai_roster_release", {
      handle: "build",
      createdAt: 1_760_000_111_000,
      incarnation: "inc-1",
    });
    expect(build.textContent).toContain("释放待确认");
  });

  it("mentions through the panel callback with the durable handle token", async () => {
    const onMention = vi.fn();
    mountRoster({ onMention });
    await flush();
    const build = view!.container.querySelector('[data-handle="build"]')!;
    clickButton(build as HTMLElement, "提及");
    expect(onMention).toHaveBeenCalledWith("@build", expect.objectContaining({ handle: "build" }));
  });
});
