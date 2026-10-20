/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, mount, type MountedView } from "./reactTestUtils";
import { ProjectWorkspaceStrip } from "../../app/ProjectWorkspaceStrip";
import {
  ALL_PROJECTS,
  UNGROUPED_PROJECT,
  projectFocusForGroup,
  type ProjectFocus,
  type ProjectInventory,
} from "../../app/projectFocus";
import type { SessionRuntimeInfo, Workspace } from "../../app/store";
import type { Asset, AssetGroup } from "../../ipc/commands";

function group(id: string, parentId: string | null = null): AssetGroup {
  return { id, parentId, name: `Project ${id}`, sort: 0, createdAt: 1, updatedAt: 1 };
}

function asset(id: string, groupId: string | null): Asset {
  return {
    id,
    groupId,
    kind: "ssh",
    name: id,
    host: null,
    port: null,
    username: null,
    authKind: null,
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
  };
}

function workspace(id: string, assetId?: string): Workspace {
  const tab = { id: `${id}-tab`, kind: "terminal" as const, title: id, closable: true };
  return {
    id,
    kind: "session",
    title: id,
    sessionId: `session-${id}`,
    assetId,
    assetKind: "ssh",
    panes: [{ id: `${id}-pane`, tabs: [tab], activeTabId: tab.id }],
    activePaneId: `${id}-pane`,
    splitRatio: 0.5,
    closable: true,
  };
}

const inventory: ProjectInventory = {
  groups: [group("p1"), group("child", "p1"), group("p2")],
  assets: [asset("root-asset", "p1"), asset("child-asset", "child"), asset("other-asset", "p2"), asset("free-asset", null)],
};
const workspaces = [
  workspace("ws-root", "root-asset"),
  workspace("ws-child", "child-asset"),
  workspace("ws-other", "other-asset"),
  workspace("ws-free", "free-asset"),
  workspace("ws-quick"),
];
const sessions: SessionRuntimeInfo[] = workspaces.map((item) => ({
  id: item.sessionId as string,
  assetId: item.assetId ?? null,
  name: item.title,
  kind: "ssh",
  status: "connected",
  tabs: [],
  createdAt: 1,
}));

let mounted: MountedView | undefined;

function renderStrip(activeProject: ProjectFocus, onSelect = vi.fn()) {
  mounted = mount(createElement(ProjectWorkspaceStrip, {
    workspaces,
    activeWorkspaceId: "ws-root",
    activeProject,
    projectInventory: inventory,
    groups: inventory.groups,
    sessions,
    onSelect,
  }));
  return onSelect;
}

function stripWorkspaceIds(): string[] {
  return [...(mounted?.container.querySelectorAll("[data-project-workspace-id]") ?? [])]
    .map((node) => node.getAttribute("data-project-workspace-id") as string);
}

beforeEach(() => {
  document.body.replaceChildren();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("ProjectWorkspaceStrip", () => {
  it("keeps the existing all view by rendering nothing", () => {
    renderStrip(ALL_PROJECTS);
    expect(mounted?.container.childElementCount).toBe(0);
  });

  it("shows only the selected top-level project, including nested groups", () => {
    const onSelect = renderStrip(projectFocusForGroup("p1"));
    expect(stripWorkspaceIds()).toEqual(["ws-root", "ws-child"]);
    expect(mounted?.container.textContent).toContain("Project p1");
    expect(mounted?.container.textContent).not.toContain("ws-other");

    const child = mounted?.container.querySelector<HTMLButtonElement>('[data-project-workspace-id="ws-child"]');
    if (!child) throw new Error("Nested project workspace not found");
    click(child);
    expect(onSelect).toHaveBeenCalledWith("ws-child");
  });

  it("collects ungrouped and quick-target workspaces", () => {
    renderStrip(UNGROUPED_PROJECT);
    expect(stripWorkspaceIds()).toEqual(["ws-free", "ws-quick"]);
    expect(mounted?.container.textContent).toContain("未分组");
  });

  it("preserves tab keyboard navigation within the filtered workspaces", () => {
    const onSelect = renderStrip(projectFocusForGroup("p1"));
    const root = mounted?.container.querySelector<HTMLButtonElement>('[data-project-workspace-id="ws-root"]');
    if (!root) throw new Error("Root workspace not found");
    act(() => root.focus());
    act(() => {
      root.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true, cancelable: true }));
    });
    expect(onSelect).toHaveBeenCalledWith("ws-child");
    expect(document.activeElement?.getAttribute("data-project-workspace-id")).toBe("ws-child");
  });
});
