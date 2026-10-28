/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { createElement } from "react";
import { act } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  clickButton,
  deferred,
  flush,
  flushUntil,
  mount,
  setInputValue,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return { ask: vi.fn(), call: vi.fn() };
});
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));
vi.mock("../../ipc/commands", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ipc/commands")>()),
  call: mocks.call,
}));
vi.mock("../../features/settings/MemoryCard", () => ({ MemoryCard: () => null }));
vi.mock("../../features/settings/KnownHostsCard", () => ({ KnownHostsCard: () => null }));
vi.mock("../../features/settings/SyncCard", () => ({ SyncCard: () => null }));
vi.mock("../../features/settings/AppearanceCard", () => ({ AppearanceCard: () => null }));
vi.mock("../../features/settings/SyncBundleCard", () => ({ SyncBundleCard: () => null }));
vi.mock("../../features/settings/ShareCard", () => ({ ShareCard: () => null }));
vi.mock("../../features/settings/ShortcutsCard", () => ({ ShortcutsCard: () => null }));
vi.mock("../../features/settings/DiagnosticsCard", () => ({ DiagnosticsCard: () => null }));
vi.mock("../../features/production/ProductionSettingsPanel", () => ({ ProductionSettingsPanel: () => null }));
vi.mock("../../features/ai/ModelPanel", () => ({ ModelManager: () => null }));

import { SkillsCard } from "../../features/settings/SkillsCard";
import { SettingsView } from "../../features/settings/SettingsView";
import {
  isSkillError,
  productionSkillsApi,
  type SkillDispatch,
  type SkillEntry,
  type SkillsApi,
} from "../../features/settings/skillsApi";
import {
  createSkillsMockBackend,
  type SkillSeed,
} from "../../features/settings/skillsMock";
import { useUi } from "../../app/store";

const EMBEDDED: SkillSeed[] = [
  { name: "alpha-skill", description: "Alpha synthetic skill", body: "alpha body\n" },
  { name: "beta-skill", description: "Beta synthetic skill", body: "beta body\n" },
];

// The exact wire shape the generated contract types declare for
// ai_skills_* payloads (see src/ipc/generated/types.ts).
const WIRE_ENTRY = {
  name: "alpha-skill",
  description: "Alpha overridden",
  source: "embedded",
  overridden: true,
  disabled: false,
  digest: `sha256:${"a".repeat(64)}`,
  updatedAt: 1729000000000,
  override: {
    name: "alpha-skill",
    description: "Alpha overridden",
    body: null,
    disabled: false,
    updatedAt: 1729000000000,
  },
};

interface SpiedApi {
  list: Mock<SkillsApi["list"]>;
  saveOverride: Mock<SkillsApi["saveOverride"]>;
  deleteOverride: Mock<SkillsApi["deleteOverride"]>;
  dispatch: Mock<SkillsApi["dispatch"]>;
  getEffective: Mock<SkillsApi["getEffective"]>;
}

function spyApi(api: SkillsApi): SpiedApi {
  return {
    list: vi.fn(api.list),
    saveOverride: vi.fn(api.saveOverride),
    deleteOverride: vi.fn(api.deleteOverride),
    dispatch: vi.fn(api.dispatch),
    getEffective: vi.fn(api.getEffective),
  };
}

function control(container: ParentNode, label: string): HTMLInputElement {
  const input = container.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`);
  if (!input) throw new Error(`control not found: ${label}`);
  return input;
}

function inputByLabel(container: ParentNode, label: string): HTMLInputElement {
  const input = container.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`);
  if (!input) throw new Error(`input not found: ${label}`);
  return input;
}

function textareaByLabel(container: ParentNode, label: string): HTMLTextAreaElement {
  const textarea = container.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${label}"]`);
  if (!textarea) throw new Error(`textarea not found: ${label}`);
  return textarea;
}

// Async list and form content settles through digest promises and reloads.
// React's act queue only flushes when an act scope exits, so polling
// helpers like vi.waitFor never observe the re-render; flushUntil drives
// explicit act+macrotask cycles until the observable condition holds,
// keeping the suite deterministic under load.
async function waitForButton(container: ParentNode, label: string): Promise<HTMLElement> {
  const find = () => container.querySelector<HTMLElement>(`button[aria-label="${label}"], input[aria-label="${label}"]`);
  await flushUntil(() => find() !== null);
  return find()!;
}

async function waitForTextarea(container: ParentNode, label: string): Promise<HTMLTextAreaElement> {
  await flushUntil(() => {
    try {
      textareaByLabel(container, label);
      return true;
    } catch {
      return false;
    }
  });
  return textareaByLabel(container, label);
}

async function expectText(container: ParentNode, text: string): Promise<void> {
  await flushUntil(() => (container.textContent ?? "").includes(text));
}

async function expectNoText(container: ParentNode, text: string): Promise<void> {
  await flushUntil(() => !(container.textContent ?? "").includes(text));
}

async function expectSkillError(promise: Promise<unknown>, code: string): Promise<void> {
  try {
    await promise;
    throw new Error(`expected SkillError ${code}`);
  } catch (error) {
    if (!isSkillError(error) || error.code !== code) {
      throw new Error(`expected SkillError ${code}, got ${String(error)}`);
    }
  }
}

describe("skills mock backend contract", () => {
  it("partially overriding the description inherits the embedded body and changes the digest", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const owner = backend.apiFor("owner-a");
    const baseline = await owner.dispatch("alpha-skill");

    const entry = await owner.saveOverride({
      name: "alpha-skill",
      description: "Alpha overridden",
    });
    expect(entry.overridden).toBe(true);
    expect(entry.source).toBe("embedded");
    expect(entry.override?.description).toBe("Alpha overridden");
    expect(entry.override?.body).toBeUndefined();
    expect(entry.digest).not.toBe(baseline.digest);

    const dispatch = await owner.dispatch("alpha-skill");
    expect(dispatch.description).toBe("Alpha overridden");
    expect(dispatch.body).toBe("alpha body\n");
    expect(dispatch.digest).toBe(entry.digest);
  });

  it("disabling blocks dispatch and deleting falls back to the embedded material and digest", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const owner = backend.apiFor("owner-a");
    const baseline = await owner.dispatch("alpha-skill");

    await owner.saveOverride({ name: "alpha-skill", disabled: true });
    await expectSkillError(owner.dispatch("alpha-skill"), "disabled");
    const listed = await owner.list();
    expect(listed.find((entry) => entry.name === "alpha-skill")?.disabled).toBe(true);

    await owner.deleteOverride("alpha-skill");
    const restored = await owner.dispatch("alpha-skill");
    expect(restored.body).toBe("alpha body\n");
    expect(restored.digest).toBe(baseline.digest);
  });

  it("isolates overrides per owner", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const ownerA = backend.apiFor("owner-a");
    const ownerB = backend.apiFor("owner-b");
    await ownerA.saveOverride({ name: "alpha-skill", body: "body from A\n" });

    const dispatchB = await ownerB.dispatch("alpha-skill");
    expect(dispatchB.body).toBe("alpha body\n");
    const entriesB = await ownerB.list();
    expect(entriesB.find((entry) => entry.name === "alpha-skill")?.overridden).toBe(false);
  });

  it("validates names, custom skills, empty overrides, and deletes of missing overrides", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const owner = backend.apiFor("owner-a");
    await expectSkillError(owner.saveOverride({ name: "Bad_Name", disabled: true }), "invalid_param");
    await expectSkillError(owner.saveOverride({ name: "custom-skill", description: "desc" }), "invalid_param");
    await expectSkillError(owner.saveOverride({ name: "alpha-skill" }), "invalid_param");
    await expectSkillError(owner.deleteOverride("alpha-skill"), "not_found");
  });

  it("runs the custom skill lifecycle end to end", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const owner = backend.apiFor("owner-a");
    const entry = await owner.saveOverride({
      name: "custom-skill",
      description: "Custom skill",
      body: "custom body\n",
    });
    expect(entry.source).toBe("custom");
    expect(entry.overridden).toBe(false);
    expect(entry.digest).toMatch(/^sha256:[0-9a-f]{64}$/);

    const listed = await owner.list();
    expect(listed.map((item) => item.name)).toEqual(["alpha-skill", "beta-skill", "custom-skill"]);

    await owner.deleteOverride("custom-skill");
    await expectSkillError(owner.dispatch("custom-skill"), "not_found");
  });

  it("digests are stable for unchanged material and differ per content", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const owner = backend.apiFor("owner-a");
    const first = await owner.dispatch("alpha-skill");
    const second = await owner.dispatch("alpha-skill");
    expect(first.digest).toBe(second.digest);
    await owner.saveOverride({ name: "alpha-skill", body: "alpha body\n\n" });
    const third = await owner.dispatch("alpha-skill");
    expect(third.digest).not.toBe(first.digest);
  });

  it("frames digests with length prefixes so NUL-containing fields cannot collide", async () => {
    const owner = createSkillsMockBackend(EMBEDDED).apiFor("owner-a");
    const first = await owner.saveOverride({ name: "nul-skill", description: "a\x00b", body: "c" });
    const firstDigest = first.digest;
    const second = await owner.saveOverride({ name: "nul-skill", description: "a", body: "b\x00c" });
    expect(second.digest).not.toBe(firstDigest);
  });

  it("measures override limits in UTF-8 bytes like the Go service", async () => {
    const owner = createSkillsMockBackend(EMBEDDED).apiFor("owner-a");
    const cjk85 = "技".repeat(85); // 255 UTF-8 bytes, within the 256-byte limit
    await expect(owner.saveOverride({ name: "alpha-skill", description: cjk85 })).resolves.toBeDefined();
    const cjk86 = "技".repeat(86); // 258 UTF-8 bytes, beyond the 256-byte limit
    await expectSkillError(owner.saveOverride({ name: "alpha-skill", description: cjk86 }), "invalid_param");
    const oversizedBody = "界".repeat(64 * 1024); // 196608 UTF-8 bytes
    await expectSkillError(owner.saveOverride({ name: "alpha-skill", body: oversizedBody }), "invalid_param");
  });

  it("pins the wire field names to the Go service JSON contract", async () => {
    const owner = createSkillsMockBackend(EMBEDDED).apiFor("owner-a");
    await owner.saveOverride({ name: "alpha-skill", description: "Alpha overridden" });
    const entries = await owner.list();
    const entry = entries.find((item) => item.name === "alpha-skill");
    if (!entry?.override) throw new Error("expected override detail on the listed entry");
    // The production typed adapter deserializes the Go DTOs in
    // internal/ai/skills (Entry/Override/Dispatch json tags); these key sets
    // are the wire contract it will consume. Optional fields that Go omits
    // versus emits as false deserialize identically, so the pin is on names.
    expect(Object.keys(JSON.parse(JSON.stringify(entry))).sort()).toEqual([
      "description",
      "digest",
      "disabled",
      "name",
      "overridden",
      "override",
      "source",
      "updatedAt",
    ]);
    expect(Object.keys(JSON.parse(JSON.stringify(entry.override))).sort()).toEqual([
      "description",
      "disabled",
      "name",
      "updatedAt",
    ]);
    const dispatch = await owner.dispatch("alpha-skill");
    expect(Object.keys(JSON.parse(JSON.stringify(dispatch))).sort()).toEqual([
      "body",
      "description",
      "digest",
      "name",
      "source",
    ]);
  });

  it("getEffective reads disabled material without performing a dispatch", async () => {
    const owner = createSkillsMockBackend(EMBEDDED).apiFor("owner-a");
    await owner.saveOverride({ name: "alpha-skill", disabled: true });
    const material = await owner.getEffective("alpha-skill");
    expect(material.body).toBe("alpha body\n");
    expect(material.source).toBe("embedded");
    await expectSkillError(owner.dispatch("alpha-skill"), "disabled");

    await owner.saveOverride({
      name: "custom-skill",
      description: "d",
      body: "custom body\n",
      disabled: true,
    });
    const custom = await owner.getEffective("custom-skill");
    expect(custom.body).toBe("custom body\n");
    expect(custom.source).toBe("custom");
    await expectSkillError(owner.getEffective("missing-skill"), "not_found");
  });
});

describe("SkillsCard", () => {
  let mounted: MountedView | undefined;
  let toast: Mock<(kind: "info" | "error" | "success", text: string) => void>;

  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    toast = vi.fn<(kind: "info" | "error" | "success", text: string) => void>();
    useUi.setState({ pushToast: toast });
  });
  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  // The default backend here is empty so rows are custom skills; built-in cases seed EMBEDDED.
  async function ownerWithSkills(backend: ReturnType<typeof createSkillsMockBackend>, owner: string): Promise<SkillsApi> {
    const api = backend.apiFor(owner);
    await api.saveOverride({ name: "alpha-skill", description: "Alpha synthetic skill", body: "alpha body\n" });
    await api.saveOverride({ name: "beta-skill", description: "Beta synthetic skill", body: "beta body\n" });
    return api;
  }

  async function renderOwner(owner = "owner-a"): Promise<SpiedApi> {
    const api = spyApi(await ownerWithSkills(createSkillsMockBackend([]), owner));
    mounted = mount(createElement(SkillsCard, { api }));
    await expectText(mounted.container, "alpha-skill");
    return api;
  }

  it("lists each skill with its name, when to use it and an on switch", async () => {
    await renderOwner();
    const text = mounted!.container.textContent ?? "";
    expect(text).toContain("AI 技能");
    expect(text).toContain("2 个");
    expect(text).toContain("Alpha synthetic skill");
    expect(text).not.toContain("内置");
    expect(text).not.toContain("sha256");
    expect(control(mounted!.container, "停用技能 alpha-skill").checked).toBe(true);
    expect(mounted!.container.querySelector('[title^="alpha-skill · sha256:"]')).not.toBeNull();
  });

  it("shows an empty hint when no skills exist", async () => {
    mounted = mount(createElement(SkillsCard, { api: createSkillsMockBackend([]).apiFor("owner-a") }));
    await expectText(mounted.container, "暂无技能");
  });

  it("surfaces a list failure with a working retry", async () => {
    const api = spyApi(await ownerWithSkills(createSkillsMockBackend([]), "owner-a"));
    api.list.mockRejectedValueOnce(new Error("disk failure"));
    mounted = mount(createElement(SkillsCard, { api }));
    await expectText(mounted.container, "disk failure");
    clickButton(mounted.container, "重试");
    await expectText(mounted.container, "alpha-skill");
    expect(api.list).toHaveBeenCalledTimes(2);
  });

  it("creates a skill and reloads the list", async () => {
    const api = await renderOwner();
    clickButton(mounted!.container, "新建技能");
    setInputValue(inputByLabel(mounted!.container, "技能名称"), "nginx-check");
    await flush();
    expect(mounted!.container.textContent).toContain("/nginx-check");
    setInputValue(inputByLabel(mounted!.container, "什么时候用"), "检查 nginx 配置");
    setInputValue(textareaByLabel(mounted!.container, "技能内容"), "nginx -t\n");
    clickButton(mounted!.container, "保存");

    expect(api.saveOverride).toHaveBeenCalledWith({ name: "nginx-check", description: "检查 nginx 配置", body: "nginx -t\n" });
    await flushUntil(() => toast.mock.calls.length > 0);
    expect(toast).toHaveBeenCalledWith("success", "技能已创建");
    await expectText(mounted!.container, "检查 nginx 配置");
  });

  it("rejects an invalid name and empty fields without calling the backend", async () => {
    const api = await renderOwner();
    clickButton(mounted!.container, "新建技能");
    setInputValue(inputByLabel(mounted!.container, "技能名称"), "Bad Name");
    setInputValue(inputByLabel(mounted!.container, "什么时候用"), "x");
    setInputValue(textareaByLabel(mounted!.container, "技能内容"), "y");
    clickButton(mounted!.container, "保存");
    await expectText(mounted!.container, "名称只能用小写字母、数字和中划线");

    setInputValue(inputByLabel(mounted!.container, "技能名称"), "good-name");
    setInputValue(inputByLabel(mounted!.container, "什么时候用"), " ");
    clickButton(mounted!.container, "保存");
    await expectText(mounted!.container, "都要填写");
    expect(api.saveOverride).not.toHaveBeenCalled();
  });

  it("keeps the form and shows the backend message when saving fails", async () => {
    const api = await renderOwner();
    api.saveOverride.mockRejectedValueOnce(new Error("quota exceeded"));
    clickButton(mounted!.container, "新建技能");
    setInputValue(inputByLabel(mounted!.container, "技能名称"), "nginx-check");
    setInputValue(inputByLabel(mounted!.container, "什么时候用"), "desc");
    setInputValue(textareaByLabel(mounted!.container, "技能内容"), "body");
    clickButton(mounted!.container, "保存");
    await expectText(mounted!.container, "quota exceeded");
    expect(inputByLabel(mounted!.container, "技能名称").value).toBe("nginx-check");
  });

  it("edits a skill through the Settings read path and saves both fields", async () => {
    const api = await renderOwner();
    click(await waitForButton(mounted!.container, "编辑技能 alpha-skill"));
    expect((await waitForTextarea(mounted!.container, "技能内容")).value).toBe("alpha body\n");
    expect(api.getEffective).toHaveBeenCalledWith("alpha-skill");
    expect(api.dispatch).not.toHaveBeenCalled();
    setInputValue(inputByLabel(mounted!.container, "什么时候用"), "Alpha updated");
    clickButton(mounted!.container, "保存");
    expect(api.saveOverride).toHaveBeenLastCalledWith({ name: "alpha-skill", description: "Alpha updated", body: "alpha body\n", disabled: false });
    await expectText(mounted!.container, "Alpha updated");
  });

  it("reports a failed settings read and closes the editor", async () => {
    const api = await renderOwner();
    api.getEffective.mockRejectedValueOnce(new Error("read failed"));
    click(await waitForButton(mounted!.container, "编辑技能 alpha-skill"));
    await flushUntil(() => toast.mock.calls.length > 0);
    expect(toast).toHaveBeenCalledWith("error", expect.stringContaining("read failed"));
    expect(mounted!.container.querySelector('textarea[aria-label="技能内容"]')).toBeNull();
  });

  it("turns a skill off and on from its switch, keeping its content", async () => {
    const api = await renderOwner();
    click(control(mounted!.container, "停用技能 alpha-skill"));
    await flushUntil(() => api.saveOverride.mock.calls.length === 1);
    expect(api.saveOverride).toHaveBeenLastCalledWith(expect.objectContaining({ name: "alpha-skill", disabled: true, description: "Alpha synthetic skill", body: "alpha body\n" }));
    click(await waitForButton(mounted!.container, "启用技能 alpha-skill"));
    await flushUntil(() => api.saveOverride.mock.calls.length === 2);
    expect(api.saveOverride).toHaveBeenLastCalledWith(expect.objectContaining({ name: "alpha-skill", disabled: false, body: "alpha body\n" }));
  });

  it("deletes a skill only after confirmation", async () => {
    const api = await renderOwner();
    mocks.ask.mockResolvedValueOnce(false);
    click(await waitForButton(mounted!.container, "删除技能 alpha-skill"));
    await flush();
    expect(api.deleteOverride).not.toHaveBeenCalled();

    mocks.ask.mockResolvedValueOnce(true);
    click(await waitForButton(mounted!.container, "删除技能 alpha-skill"));
    await flushUntil(() => api.deleteOverride.mock.calls.length === 1);
    await expectNoText(mounted!.container, "Alpha synthetic skill");
    expect(toast).toHaveBeenCalledWith("success", "已删除技能 alpha-skill");
  });

  it("offers no delete for an untouched built-in skill and restores defaults after an override", async () => {
    const api = spyApi(createSkillsMockBackend(EMBEDDED).apiFor("owner-a"));
    mounted = mount(createElement(SkillsCard, { api }));
    await expectText(mounted.container, "alpha-skill");
    expect(mounted.container.querySelector('[aria-label="删除技能 alpha-skill"]')).toBeNull();
    expect(mounted.container.querySelector('[aria-label="恢复默认技能 alpha-skill"]')).toBeNull();

    mounted.unmount();

    await api.saveOverride({ name: "alpha-skill", description: "Alpha edited" });
    mounted = mount(createElement(SkillsCard, { api }));
    await expectText(mounted.container, "Alpha edited");
    expect(mounted.container.querySelector('[aria-label="删除技能 alpha-skill"]')).toBeNull();
    mocks.ask.mockResolvedValueOnce(true);
    click(await waitForButton(mounted.container, "恢复默认技能 alpha-skill"));
    await flushUntil(() => api.deleteOverride.mock.calls.length === 1);
    await expectText(mounted.container, "Alpha synthetic skill");
    expect(mocks.ask).toHaveBeenLastCalledWith(expect.stringContaining("不会被删除"), expect.objectContaining({ title: "恢复默认" }));
    expect(toast).toHaveBeenCalledWith("success", "已恢复技能 alpha-skill 的默认内容");
    expect(mounted.container.querySelector('[aria-label="恢复默认技能 alpha-skill"]')).toBeNull();
  });

  it("reports a delete failure and keeps the row", async () => {
    const api = await renderOwner();
    mocks.ask.mockResolvedValueOnce(true);
    api.deleteOverride.mockRejectedValueOnce(new Error("locked"));
    click(await waitForButton(mounted!.container, "删除技能 alpha-skill"));
    await flushUntil(() => toast.mock.calls.length > 0);
    expect(toast).toHaveBeenCalledWith("error", expect.stringContaining("locked"));
    expect(mounted!.container.textContent).toContain("Alpha synthetic skill");
  });

  it("closes an open owner-A editor on owner swap so B never sees or saves A content", async () => {
    const backend = createSkillsMockBackend([]);
    const apiA = spyApi(await ownerWithSkills(backend, "owner-a"));
    const apiB = spyApi(backend.apiFor("owner-b"));
    mounted = mount(createElement(SkillsCard, { api: apiA }));
    click(await waitForButton(mounted.container, "编辑技能 alpha-skill"));
    expect((await waitForTextarea(mounted.container, "技能内容")).value).toBe("alpha body\n");

    act(() => {
      mounted!.root.render(createElement(SkillsCard, { api: apiB }));
    });
    await flush();
    expect(mounted.container.querySelector('textarea[aria-label="技能内容"]')).toBeNull();
    expect(apiB.saveOverride).not.toHaveBeenCalled();
    await expectText(mounted.container, "暂无技能");
    expect(mounted.container.textContent).not.toContain("alpha-skill");
  });

  it("drops a pending owner-A read after the swap", async () => {
    const backend = createSkillsMockBackend([]);
    const apiA = spyApi(await ownerWithSkills(backend, "owner-a"));
    const apiB = spyApi(backend.apiFor("owner-b"));
    const slow = deferred<SkillDispatch>();
    apiA.getEffective.mockReturnValueOnce(slow.promise);
    mounted = mount(createElement(SkillsCard, { api: apiA }));
    click(await waitForButton(mounted.container, "编辑技能 alpha-skill"));
    expect(mounted.container.textContent).toContain("读取技能内容…");

    act(() => {
      mounted!.root.render(createElement(SkillsCard, { api: apiB }));
    });
    await flush();
    slow.resolve({ name: "alpha-skill", description: "Alpha synthetic skill", body: "alpha body\n", digest: "sha256:aa", source: "custom" });
    await flush();
    expect(mounted.container.querySelector('textarea[aria-label="技能内容"]')).toBeNull();
    expect(mounted.container.textContent).not.toContain("读取技能内容…");
  });

  it("abandons pending owner-A save, toggle and delete completions after the swap", async () => {
    const backend = createSkillsMockBackend([]);
    const apiA = spyApi(await ownerWithSkills(backend, "owner-a"));
    const apiB = spyApi(backend.apiFor("owner-b"));
    mounted = mount(createElement(SkillsCard, { api: apiA }));
    await expectText(mounted.container, "alpha-skill");

    const saving = deferred<Awaited<ReturnType<SkillsApi["saveOverride"]>>>();
    apiA.saveOverride.mockReturnValueOnce(saving.promise);
    click(control(mounted.container, "停用技能 alpha-skill"));
    await flush();

    act(() => {
      mounted!.root.render(createElement(SkillsCard, { api: apiB }));
    });
    await flush();
    saving.resolve({ name: "alpha-skill", description: "Alpha synthetic skill", source: "custom", overridden: false, disabled: true, digest: "sha256:cc", updatedAt: 1 });
    await flush();

    expect(apiA.list).toHaveBeenCalledTimes(1);
    expect(toast).not.toHaveBeenCalledWith("success", expect.anything());
    await expectText(mounted.container, "暂无技能");
  });

  it("hides owner-A rows while owner B's list is still loading", async () => {
    const backend = createSkillsMockBackend([]);
    const apiA = spyApi(await ownerWithSkills(backend, "owner-a"));
    const apiB = spyApi(backend.apiFor("owner-b"));
    const slow = deferred<SkillEntry[]>();
    apiB.list.mockReturnValueOnce(slow.promise);
    mounted = mount(createElement(SkillsCard, { api: apiA }));
    await expectText(mounted.container, "alpha-skill");

    act(() => {
      mounted!.root.render(createElement(SkillsCard, { api: apiB }));
    });
    expect(mounted.container.textContent).not.toContain("alpha-skill");
    expect(mounted.container.textContent).toContain("读取中…");
    slow.resolve([]);
    await expectText(mounted.container, "暂无技能");
  });
});

describe("production skills API", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("maps wire entries to the UI contract, with null override fields becoming absent", async () => {
    mocks.call.mockResolvedValue([WIRE_ENTRY]);
    const entries = await productionSkillsApi.list();
    expect(mocks.call).toHaveBeenCalledWith("ai_skills_list");
    expect(entries).toEqual([
      {
        name: "alpha-skill",
        description: "Alpha overridden",
        source: "embedded",
        overridden: true,
        disabled: false,
        digest: `sha256:${"a".repeat(64)}`,
        updatedAt: 1729000000000,
        override: {
          name: "alpha-skill",
          description: "Alpha overridden",
          body: undefined,
          disabled: false,
          updatedAt: 1729000000000,
        },
      },
    ]);
  });

  it("sends only the set optional fields in the override payload", async () => {
    mocks.call.mockResolvedValue(WIRE_ENTRY);
    await productionSkillsApi.saveOverride({
      name: "alpha-skill",
      description: "Alpha overridden",
      disabled: false,
    });
    expect(mocks.call).toHaveBeenCalledWith("ai_skills_save_override", {
      name: "alpha-skill",
      description: "Alpha overridden",
      disabled: false,
    });
    await productionSkillsApi.saveOverride({ name: "alpha-skill", body: "body v2\n" });
    expect(mocks.call).toHaveBeenLastCalledWith("ai_skills_save_override", {
      name: "alpha-skill",
      body: "body v2\n",
    });
  });

  it("routes dispatch and settings reads to their distinct commands", async () => {
    mocks.call.mockResolvedValue({
      name: "alpha-skill",
      description: "d",
      body: "b\n",
      digest: "sha256:x",
      source: "custom",
    });
    const dispatch = await productionSkillsApi.dispatch("alpha-skill");
    expect(mocks.call).toHaveBeenCalledWith("ai_skills_dispatch", { name: "alpha-skill" });
    expect(dispatch.source).toBe("custom");
    await productionSkillsApi.getEffective("alpha-skill");
    expect(mocks.call).toHaveBeenLastCalledWith("ai_skills_material", { name: "alpha-skill" });
    await productionSkillsApi.deleteOverride("alpha-skill");
    expect(mocks.call).toHaveBeenLastCalledWith("ai_skills_delete_override", {
      name: "alpha-skill",
    });
  });

  it("maps IPC error codes to SkillError and rethrows foreign failures unchanged", async () => {
    mocks.call.mockRejectedValueOnce({ code: "not_found", message: "AI 技能不存在: x" });
    await expectSkillError(productionSkillsApi.dispatch("x"), "not_found");
    mocks.call.mockRejectedValueOnce({
      code: "bad_param",
      message: "AI 技能已被禁用，请在设置中启用后重试: x",
    });
    await expectSkillError(productionSkillsApi.dispatch("x"), "invalid_param");
    const foreign = { code: "forbidden", message: "AI 请求缺少账号身份" };
    mocks.call.mockRejectedValueOnce(foreign);
    await expect(productionSkillsApi.list()).rejects.toBe(foreign);
  });
});

describe("SettingsView skills navigation", () => {
  let mounted: MountedView | undefined;

  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    localStorage.clear();
    mocks.call.mockImplementation(async (cmd: string) => {
      if (cmd === "ai_skills_list") return [WIRE_ENTRY];
      if (cmd === "ai_privacy_status") {
        return {
          config: { enabled: false, precision: "int8", revision: "", version: "" },
          installed: [],
          runtime: { supported: true },
          diskUsage: 0,
          canManage: true,
          download: { running: false, precision: "int8", progress: { file: "", downloaded: 0, total: 0 } },
        };
      }
      if (cmd === "vault_status") {
        return { initialized: true, mode: "master", unlocked: true, autoLockMinutes: 30 };
      }
      if (cmd === "ai_get_permission") return { mode: "read_write", dangerRules: [] };
      throw { code: "internal", message: `unmocked command ${cmd}` };
    });
  });
  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("shows the AI skills section and loads it through the production API", async () => {
    localStorage.setItem("nexterm.settingsGroup.v1", "ai");
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => {
      mounted = mount(createElement(QueryClientProvider, { client }, createElement(SettingsView)));
    });

    const navLabels = [
      ...mounted!.container.querySelectorAll(
        "[data-testid='settings-nav-toc'] button, [data-testid='settings-nav-chips'] button",
      ),
    ].map((button) => button.textContent?.trim());
    expect(navLabels).toContain("技能");
    const section = mounted!.container.querySelector("#settings-ai-skills");
    expect(section).not.toBeNull();

    await flushUntil(() => mocks.call.mock.calls.some((call) => call[0] === "ai_skills_list"));
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("alpha-skill"));
    expect(section!.textContent).toContain("Alpha overridden");
    expect(mocks.call).toHaveBeenCalledWith("ai_skills_list");
  });
});
