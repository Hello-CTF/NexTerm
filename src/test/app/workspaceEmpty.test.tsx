/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, flush, mount, type MountedView } from "../features/reactTestUtils";
import type { Workspace } from "../../app/store";

const mocks = vi.hoisted(() => ({ reopen: vi.fn() }));

vi.mock("../../app/store", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../app/store")>();
  return { ...actual, reopenRemoteDesktopTab: mocks.reopen };
});

import { useUi } from "../../app/store";
import { WorkspaceEmpty } from "../../app/workspace/EmptyStates";

let view: MountedView | null = null;

function workspace(patch: Partial<Workspace> = {}): Workspace {
  return {
    id: "ws",
    kind: "session",
    title: "prod",
    panes: [{ id: "p1", tabs: [], activeTabId: null }],
    activePaneId: "p1",
    closable: true,
    ...patch,
  };
}

function render(onNew = vi.fn()) {
  view = mount(createElement(WorkspaceEmpty, { onNew, workspaceId: "ws", paneId: "p1" }));
  return onNew;
}

function buttons(): string[] {
  return [...view!.container.querySelectorAll("button")].map((b) => b.textContent ?? "");
}

function text(): string {
  return view!.container.textContent ?? "";
}

beforeEach(() => {
  mocks.reopen.mockReset().mockResolvedValue({ ok: true });
  useUi.setState({ toasts: [] });
});

afterEach(() => {
  view?.unmount();
  view = null;
});

describe("WorkspaceEmpty", () => {
  it("keeps the terminal prompt for a session workspace", () => {
    useUi.setState({ workspaces: [workspace()] });
    const onNew = render();
    expect(text()).toContain("暂无终端");
    click(view!.container.querySelector("button")!);
    expect(onNew).toHaveBeenCalledTimes(1);
    expect(mocks.reopen).not.toHaveBeenCalled();
  });

  it("offers reconnect, not a terminal, in a closed remote desktop workspace", async () => {
    useUi.setState({ workspaces: [workspace({ kind: "remote", assetId: "a1", assetKind: "rdp" })] });
    const onNew = render();
    expect(text()).toContain("远程桌面已关闭");
    expect(text()).not.toContain("终端");
    expect(text()).not.toContain("文件树");
    expect(buttons()).toEqual(["重新连接"]);
    click(view!.container.querySelector("button")!);
    await flush();
    expect(mocks.reopen).toHaveBeenCalledWith("ws", "p1");
    expect(onNew).not.toHaveBeenCalled();
  });

  it("shows no reconnect button when the asset was deleted", () => {
    useUi.setState({ workspaces: [workspace({ kind: "remote", assetId: "a1", deletedTarget: true })] });
    render();
    expect(text()).toContain("已删除");
    expect(buttons()).toEqual([]);
  });

  it("shows no reconnect when the desktop is open in another pane", () => {
    useUi.setState({
      workspaces: [
        workspace({
          kind: "remote",
          assetId: "a1",
          panes: [
            { id: "p1", tabs: [], activeTabId: null },
            { id: "p2", tabs: [{ id: "r1", kind: "remote", title: "远程桌面", closable: true }], activeTabId: "r1" },
          ],
        }),
      ],
    });
    render();
    expect(buttons()).toEqual([]);
  });

  it("offers a terminal button in database and tools workspaces", () => {
    for (const kind of ["db", "tools"] as const) {
      useUi.setState({ workspaces: [workspace({ kind })] });
      const onNew = render();
      expect(text()).not.toContain("文件树");
      expect(buttons()).toEqual(["新建终端"]);
      click(view!.container.querySelector("button")!);
      expect(onNew).toHaveBeenCalledTimes(1);
      view!.unmount();
    }
  });
});
