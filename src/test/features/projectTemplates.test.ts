/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Asset, AssetGroup } from "../../ipc/commands";
import type { AppTab, QuickWorkspaceTarget, Workspace } from "../../app/store";
import type { AccountPreferenceStore } from "../../app/preferences";
import type { ProjectTemplateStorage } from "../../app/projectTemplateStore";

vi.mock("../../ipc/commands", () => ({
  assetApi: {}, dbApi: {}, sessionApi: {}, terminalApi: {}, vaultApi: {}, syncApi: {},
}));

import {
  captureProjectTemplateDraft,
  createProjectTemplate,
  normalizeProjectTemplates,
  restoreProjectTemplate,
  templateStorageText,
  type ProjectTemplate,
} from "../../app/projectTemplates";
import { projectFocusForGroup, UNGROUPED_PROJECT, type ProjectInventory } from "../../app/projectFocus";
import {
  DeviceProjectTemplateStorage,
  OwnerProjectTemplateStorage,
  PROJECT_TEMPLATES_PREFERENCE_KEY,
  ProjectTemplateRepository,
} from "../../app/projectTemplateStore";

function group(id: string, parentId: string | null = null): AssetGroup {
  return { id, parentId, name: id, sort: 0, createdAt: 1, updatedAt: 1 };
}

function asset(id: string, groupId: string | null): Asset {
  return {
    id, groupId, kind: "ssh", name: id, host: null, port: null, username: null,
    authKind: null, keyPath: null, credId: null, options: {}, tags: "", note: "",
    sort: 0, createdAt: 1, updatedAt: 1, deletedAt: null, builtin: false,
  };
}

function tab(id: string, patch: Partial<AppTab> = {}): AppTab {
  return {
    id, kind: "terminal", title: id, sessionId: `session-${id}`, tabId: `kernel-${id}`,
    credId: "credential-reference", closable: true, ...patch,
  };
}

function workspace(id: string, assetId?: string, patch: Partial<Workspace> = {}): Workspace {
  const first = tab(`${id}-tab`);
  return {
    id, kind: "session", title: id, sessionId: `session-${id}`, assetId, assetKind: "ssh",
    panes: [{ id: `${id}-pane`, tabs: [first], activeTabId: first.id }],
    activePaneId: `${id}-pane`, splitRatio: 0.5, closable: true, ...patch,
  };
}

const inventory: ProjectInventory = {
  assets: [asset("asset-project", "project"), asset("asset-child", "child"), asset("asset-other", "other"), asset("asset-free", null)],
  groups: [group("project"), group("child", "project"), group("other")],
};

function makeTemplate(id = "template-1", name = "Daily"): ProjectTemplate {
  const draft = captureProjectTemplateDraft(name, projectFocusForGroup("project"), [
    workspace("ws-project", "asset-project"),
    workspace("ws-child", "asset-child"),
  ], inventory);
  draft.startupCommands["ws-project-tab"] = "tail -f /var/log/app.log";
  return createProjectTemplate(draft, { id, now: 100 });
}

class MemoryStorage implements ProjectTemplateStorage {
  value: unknown = [];
  writes = 0;
  async read(): Promise<unknown> { return this.value; }
  async write(templates: ProjectTemplate[]): Promise<void> {
    this.value = structuredClone(templates);
    this.writes += 1;
  }
}

beforeEach(() => {
  localStorage.clear();
});

describe("project template capture and persistence", () => {
  it("captures nested project workspaces and strips runtime and unexpected credential fields", () => {
    const quickTarget = {
      id: "quick", host: "example.invalid", port: 22, username: "root", authKind: "password",
      password: "quick-plaintext-secret",
    } as QuickWorkspaceTarget;
    const project = workspace("ws-project", "asset-project", { quickTarget });
    const extraSecretTab = { ...tab("secret-tab"), password: "tab-plaintext-secret" } as AppTab;
    project.panes[0].tabs.push(extraSecretTab);
    const draft = captureProjectTemplateDraft("Daily", projectFocusForGroup("project"), [
      project,
      workspace("ws-child", "asset-child"),
      workspace("ws-other", "asset-other"),
      workspace("ws-tools", undefined, { kind: "tools", assetId: undefined }),
    ], inventory);
    draft.startupCommands["ws-project-tab"] = "cd /srv/app && tail -f app.log";
    const template = createProjectTemplate(draft, { id: "template-safe", now: 100 });

    expect(template.workspaces.map((item) => item.id)).toEqual(["ws-project", "ws-child"]);
    expect(template.workspaces[0].sessionId).toBeUndefined();
    expect(template.workspaces[0].panes[0].tabs[0].tabId).toBeUndefined();
    expect(template.workspaces[0].panes[0].tabs[0].credId).toBe("credential-reference");
    const serialized = templateStorageText(template);
    expect(serialized).not.toContain("quick-plaintext-secret");
    expect(serialized).not.toContain("tab-plaintext-secret");
    expect(serialized).not.toContain('"password":');
    expect(serialized).not.toContain("kernel-ws-project-tab");
    expect(normalizeProjectTemplates([JSON.parse(serialized)])).toHaveLength(1);
  });

  it("supports ungrouped projects without absorbing grouped workspaces", () => {
    const draft = captureProjectTemplateDraft("Ungrouped", UNGROUPED_PROJECT, [
      workspace("ws-free", "asset-free"),
      workspace("ws-project", "asset-project"),
    ], inventory);
    expect(draft.workspaces.map((item) => item.id)).toEqual(["ws-free"]);
  });

  it("lists, creates, saves as, renames and deletes through serialized storage", async () => {
    const storage = new MemoryStorage();
    const repository = new ProjectTemplateRepository(storage);
    const created = await repository.create(makeTemplate());
    expect((await repository.list()).map((item) => item.id)).toEqual([created.id]);
    const copy = await repository.saveAs({ ...created, name: "Copy" });
    expect(copy.id).not.toBe(created.id);
    expect((await repository.list()).map((item) => item.name)).toEqual(["Copy", "Daily"]);
    await repository.rename(created.id, "Renamed");
    expect((await repository.list()).find((item) => item.id === created.id)?.name).toBe("Renamed");
    await expect(repository.rename(copy.id, "renamed")).rejects.toThrow("同名");
    await repository.delete(copy.id);
    expect((await repository.list()).map((item) => item.id)).toEqual([created.id]);
    expect(storage.writes).toBe(4);
  });

  it("uses a device-local key on desktop storage", async () => {
    const storage = new DeviceProjectTemplateStorage(localStorage);
    await storage.write([makeTemplate()]);
    expect(localStorage.getItem("nexterm.projectTemplates.v1")).toContain("Daily");
    expect(await storage.read()).toHaveLength(1);
  });

  it("uses only the current owner's preference overrides on server storage", async () => {
    const putOverrides = vi.fn(async () => undefined);
    const preferences: AccountPreferenceStore = {
      getView: vi.fn(async () => ({
        defaults: { [PROJECT_TEMPLATES_PREFERENCE_KEY]: [{ name: "wrong-default" }] },
        overrides: { [PROJECT_TEMPLATES_PREFERENCE_KEY]: [makeTemplate()] },
      })),
      putOverrides,
      deleteOverrides: vi.fn(async () => undefined),
    };
    const storage = new OwnerProjectTemplateStorage(preferences);
    expect(await storage.read()).toHaveLength(1);
    await storage.write([makeTemplate("template-2", "Owner")]);
    expect(putOverrides).toHaveBeenCalledWith({
      [PROJECT_TEMPLATES_PREFERENCE_KEY]: [expect.objectContaining({ name: "Owner" })],
    });
  });
});

describe("project template layout restore", () => {
  it("preserves live workspaces and active runtime while adding missing tabs and workspaces", () => {
    const template = makeTemplate();
    const live = workspace("ws-project", "asset-project", { restorePending: undefined });
    live.panes[0].tabs = [];
    live.panes[0].activeTabId = null;
    const other = workspace("ws-other", "asset-other");
    const restored = restoreProjectTemplate(template, [live, other]);

    expect(restored.workspaces).toHaveLength(3);
    expect(restored.workspaces[0].sessionId).toBe("session-ws-project");
    expect(restored.workspaces[0].restorePending).toBeUndefined();
    expect(restored.workspaces[0].panes[0].tabs[0]).toMatchObject({
      id: "ws-project-tab",
      sessionId: "session-ws-project",
      tabId: undefined,
    });
    expect(restored.addedWorkspaceIds).toEqual(["ws-child"]);
    expect(restored.workspaces[2].restorePending).toBe(true);
    expect(restored.workspaces[2].sessionId).toBeUndefined();
    expect(restored.tabIds.get("ws-project-tab")).toBe("ws-project-tab");
    expect(restored.workspaces[1]).toBe(other);
  });
});
