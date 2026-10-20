/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { createElement } from "react";
import { act } from "react";
import { flushSync } from "react-dom";
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

const mocks = vi.hoisted(() => ({ ask: vi.fn() }));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

import { SkillsCard } from "../../features/settings/SkillsCard";
import {
  isSkillError,
  SkillError,
  type SkillDispatch,
  type SkillEntry,
  type SkillsApi,
  type SkillOverrideInput,
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

async function seedEntries(api: SkillsApi): Promise<void> {
  await api.saveOverride({ name: "alpha-skill", description: "Alpha overridden" });
}

function iconButton(container: ParentNode, label: string): HTMLButtonElement {
  const button = [...container.querySelectorAll("button")].find(
    (candidate) => candidate.getAttribute("aria-label") === label,
  );
  if (!button) throw new Error(`button not found: ${label}`);
  return button as HTMLButtonElement;
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
async function waitForButton(container: ParentNode, label: string): Promise<HTMLButtonElement> {
  await flushUntil(() => {
    try {
      iconButton(container, label);
      return true;
    } catch {
      return false;
    }
  });
  return iconButton(container, label);
}

async function waitForInput(container: ParentNode, label: string): Promise<HTMLInputElement> {
  await flushUntil(() => {
    try {
      inputByLabel(container, label);
      return true;
    } catch {
      return false;
    }
  });
  return inputByLabel(container, label);
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

async function expectToast(
  toast: Mock<(kind: "info" | "error" | "success", text: string) => void>,
  kind: "info" | "error" | "success",
  text: string,
): Promise<void> {
  await flushUntil(() =>
    toast.mock.calls.some((call) => call[0] === kind && String(call[1]).includes(text)),
  );
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

  function renderWith(api: SkillsApi): SpiedApi {
    const spied = spyApi(api);
    mounted = mount(createElement(SkillsCard, { api: spied }));
    return spied;
  }

  it("shows loading first, then entries with source badges and digests", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const slow = deferred<SkillEntry[]>();
    const api = spyApi(backend.apiFor("owner-a"));
    api.list.mockReturnValueOnce(slow.promise);
    mounted = mount(createElement(SkillsCard, { api }));
    expect(mounted.container.textContent).toContain("读取中…");

    slow.resolve([
      {
        name: "alpha-skill",
        description: "Alpha synthetic skill",
        source: "embedded",
        overridden: false,
        disabled: false,
        digest: `sha256:${"a".repeat(64)}`,
        updatedAt: 0,
      },
    ]);
    await expectText(mounted.container, "alpha-skill");
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("AI 技能");
    expect(text).toContain("内置");
    expect(text).toContain("1 个");
    expect(text).toContain("摘要");
    const digest = mounted.container.querySelector("code[title]");
    expect(digest?.getAttribute("title")).toBe(`sha256:${"a".repeat(64)}`);
    expect(digest?.textContent).toContain("sha256:aaaaaaaaaaaa…");
    expect(text).toContain("不包含未经验证的具体命令");
    expect(text).toContain("覆盖按账号隔离");
  });

  it("shows an empty hint when no skills exist", async () => {
    const backend = createSkillsMockBackend([]);
    renderWith(backend.apiFor("owner-a"));
    await expectText(mounted!.container, "还没有技能");
  });

  it("surfaces a list failure with a working retry", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const api = spyApi(backend.apiFor("owner-a"));
    api.list.mockRejectedValueOnce(new Error("disk failure"));
    mounted = mount(createElement(SkillsCard, { api }));
    await expectText(mounted.container, "disk failure");

    clickButton(mounted.container, "重试");
    await expectText(mounted.container, "alpha-skill");
    expect(api.list).toHaveBeenCalledTimes(2);
  });

  it("creates a custom skill and reloads the list", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const api = renderWith(backend.apiFor("owner-a"));
    await flush();

    clickButton(mounted!.container, "新建自定义技能");
    setInputValue(inputByLabel(mounted!.container, "技能名称"), "custom-skill");
    setInputValue(inputByLabel(mounted!.container, "技能描述"), "Custom skill");
    setInputValue(textareaByLabel(mounted!.container, "技能正文"), "custom body\n");
    clickButton(mounted!.container, "保存");

    expect(api.saveOverride).toHaveBeenCalledWith({
      name: "custom-skill",
      description: "Custom skill",
      body: "custom body\n",
    });
    await flushUntil(() => api.list.mock.calls.length === 2);
    expect(toast).toHaveBeenCalledWith("success", expect.stringContaining("已保存"));
    expect(mounted!.container.textContent).toContain("自定义");
  });

  it("rejects an invalid custom name client-side without calling the backend", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const api = renderWith(backend.apiFor("owner-a"));
    await flush();

    clickButton(mounted!.container, "新建自定义技能");
    setInputValue(inputByLabel(mounted!.container, "技能名称"), "Bad_Name");
    setInputValue(inputByLabel(mounted!.container, "技能描述"), "desc");
    setInputValue(textareaByLabel(mounted!.container, "技能正文"), "body");
    clickButton(mounted!.container, "保存");
    await flush();

    expect(mounted!.container.textContent).toContain("小写字母、数字与中划线");
    expect(api.saveOverride).not.toHaveBeenCalled();
  });

  it("keeps the form and shows the backend message when saving fails", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const api = renderWith(backend.apiFor("owner-a"));
    await flush();

    clickButton(mounted!.container, "新建自定义技能");
    setInputValue(inputByLabel(mounted!.container, "技能名称"), "custom-skill");
    setInputValue(inputByLabel(mounted!.container, "技能描述"), "desc");
    setInputValue(textareaByLabel(mounted!.container, "技能正文"), "body");
    api.saveOverride.mockRejectedValueOnce(new SkillError("invalid_param", "AI 技能内容无效: boom"));
    clickButton(mounted!.container, "保存");

    await expectText(mounted!.container, "AI 技能内容无效: boom");
    expect(inputByLabel(mounted!.container, "技能名称").value).toBe("custom-skill");
    expect(api.list).toHaveBeenCalledTimes(1);
    expect(toast).not.toHaveBeenCalledWith("success", expect.anything());
  });

  it("edits an embedded skill with a partial description override and omits the body from the payload", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const api = renderWith(backend.apiFor("owner-a"));

    click(await waitForButton(mounted!.container, "编辑技能 alpha-skill"));
    expect(api.getEffective).toHaveBeenCalledWith("alpha-skill");
    expect(api.dispatch).not.toHaveBeenCalled();
    const description = await waitForInput(mounted!.container, "技能描述");
    expect(description.disabled).toBe(true);
    expect(description.value).toBe("Alpha synthetic skill");
    expect((await waitForTextarea(mounted!.container, "技能正文")).value).toBe("alpha body\n");

    click(inputByLabel(mounted!.container, "覆盖描述"));
    setInputValue(inputByLabel(mounted!.container, "技能描述"), "Alpha overridden");
    clickButton(mounted!.container, "保存");

    const call: SkillOverrideInput = api.saveOverride.mock.calls[0]![0];
    expect(call).toMatchObject({ name: "alpha-skill", description: "Alpha overridden", disabled: false });
    expect("body" in call).toBe(false);
    await expectToast(toast, "success", "已保存");
  });

  it("prefills the edit form with the exact overridden fields", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const owner = backend.apiFor("owner-a");
    await owner.saveOverride({ name: "alpha-skill", description: "Alpha overridden" });
    renderWith(owner);

    click(await waitForButton(mounted!.container, "编辑技能 alpha-skill"));
    const overrideDescription = await waitForInput(mounted!.container, "覆盖描述");
    expect(overrideDescription.checked).toBe(true);
    expect(inputByLabel(mounted!.container, "覆盖正文").checked).toBe(false);
    expect(inputByLabel(mounted!.container, "技能描述").value).toBe("Alpha overridden");
  });

  it("disables an embedded skill from the row and enables it again by deleting the disable-only override", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const api = renderWith(backend.apiFor("owner-a"));

    click(await waitForButton(mounted!.container, "禁用技能 alpha-skill"));
    expect(api.saveOverride).toHaveBeenCalledWith({
      name: "alpha-skill",
      description: undefined,
      body: undefined,
      disabled: true,
    });
    await expectText(mounted!.container, "已禁用");

    click(await waitForButton(mounted!.container, "启用技能 alpha-skill"));
    expect(api.deleteOverride).toHaveBeenCalledWith("alpha-skill");
    await expectNoText(mounted!.container, "已禁用");
  });

  it("keeps the overridden fields when toggling disable on an overridden skill", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const owner = backend.apiFor("owner-a");
    await owner.saveOverride({ name: "alpha-skill", description: "Alpha overridden" });
    const api = renderWith(owner);

    click(await waitForButton(mounted!.container, "禁用技能 alpha-skill"));
    expect(api.saveOverride).toHaveBeenCalledWith({
      name: "alpha-skill",
      description: "Alpha overridden",
      body: undefined,
      disabled: true,
    });

    click(await waitForButton(mounted!.container, "启用技能 alpha-skill"));
    expect(api.saveOverride).toHaveBeenLastCalledWith({
      name: "alpha-skill",
      description: "Alpha overridden",
      body: undefined,
      disabled: false,
    });
    await expectText(mounted!.container, "已覆盖");
  });

  it("deletes an override only after confirmation, then reverts the row", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const owner = backend.apiFor("owner-a");
    await seedEntries(owner);
    mocks.ask.mockResolvedValue(true);
    const api = renderWith(owner);
    await expectText(mounted!.container, "已覆盖");

    click(await waitForButton(mounted!.container, "删除覆盖 alpha-skill"));
    await flushUntil(() => api.deleteOverride.mock.calls.length > 0);
    expect(api.deleteOverride).toHaveBeenCalledWith("alpha-skill");
    expect(String(mocks.ask.mock.calls[0]?.[0] ?? "")).toContain("恢复为内置内容");
    expect(toast).toHaveBeenCalledWith("success", expect.stringContaining("已恢复为内置版本"));
    await expectNoText(mounted!.container, "已覆盖");
  });

  it("does nothing when the delete confirmation is declined", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const owner = backend.apiFor("owner-a");
    await seedEntries(owner);
    mocks.ask.mockResolvedValue(false);
    const api = renderWith(owner);
    await expectText(mounted!.container, "已覆盖");

    click(await waitForButton(mounted!.container, "删除覆盖 alpha-skill"));
    await flush();
    expect(api.deleteOverride).not.toHaveBeenCalled();
    expect(mounted!.container.textContent).toContain("已覆盖");
  });

  it("reports a delete failure and keeps the row", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const owner = backend.apiFor("owner-a");
    await seedEntries(owner);
    mocks.ask.mockResolvedValue(true);
    const api = renderWith(owner);

    api.deleteOverride.mockRejectedValueOnce(new Error("read-only database"));
    click(await waitForButton(mounted!.container, "删除覆盖 alpha-skill"));
    await expectToast(toast, "error", "删除失败");
    expect(mounted!.container.textContent).toContain("已覆盖");
  });

  it("swaps to the other owner's state when the api prop changes", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const ownerA = backend.apiFor("owner-a");
    const ownerB = backend.apiFor("owner-b");
    await ownerA.saveOverride({ name: "alpha-skill", description: "Alpha overridden by A" });
    const apiA = spyApi(ownerA);
    const apiB = spyApi(ownerB);
    mounted = mount(createElement(SkillsCard, { api: apiA }));
    await expectText(mounted.container, "Alpha overridden by A");

    act(() => {
      mounted!.root.render(createElement(SkillsCard, { api: apiB }));
    });
    await expectText(mounted.container, "Alpha synthetic skill");
    expect(apiB.list).toHaveBeenCalled();
    expect(mounted.container.textContent ?? "").not.toContain("Alpha overridden by A");
  });

  it("drops a stale list result that lands after a newer reload", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const api = spyApi(backend.apiFor("owner-a"));
    const stale = deferred<SkillEntry[]>();
    api.list.mockReturnValueOnce(stale.promise);
    mounted = mount(createElement(SkillsCard, { api }));

    clickButton(mounted.container, "刷新");
    await expectText(mounted.container, "alpha-skill");

    stale.resolve([]);
    await flush();
    expect(mounted.container.textContent).toContain("alpha-skill");
  });

  it("opens the editor for a disabled embedded skill through the Settings read path", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const owner = backend.apiFor("owner-a");
    await owner.saveOverride({ name: "alpha-skill", disabled: true });
    const api = renderWith(owner);

    click(await waitForButton(mounted!.container, "编辑技能 alpha-skill"));
    expect(api.getEffective).toHaveBeenCalledWith("alpha-skill");
    expect(api.dispatch).not.toHaveBeenCalled();
    expect((await waitForTextarea(mounted!.container, "技能正文")).value).toBe("alpha body\n");
    expect(inputByLabel(mounted!.container, "禁用此技能").checked).toBe(true);
  });

  it("opens the editor for a disabled custom skill and can re-enable it from the form", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const owner = backend.apiFor("owner-a");
    await owner.saveOverride({
      name: "custom-skill",
      description: "Custom skill",
      body: "custom body\n",
      disabled: true,
    });
    const api = renderWith(owner);

    click(await waitForButton(mounted!.container, "编辑技能 custom-skill"));
    expect(api.getEffective).toHaveBeenCalledWith("custom-skill");
    expect((await waitForTextarea(mounted!.container, "技能正文")).value).toBe("custom body\n");

    click(inputByLabel(mounted!.container, "禁用此技能"));
    clickButton(mounted!.container, "保存");
    await flushUntil(() => !(mounted!.container.textContent ?? "").includes("已禁用"));
    expect(api.saveOverride).toHaveBeenCalledWith({
      name: "custom-skill",
      description: "Custom skill",
      body: "custom body\n",
      disabled: false,
    });
    const entries = await owner.list();
    expect(entries.find((entry) => entry.name === "custom-skill")?.disabled).toBe(false);
  });

  it("closes an open owner-A draft on owner swap so B never sees or saves A content", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const ownerA = backend.apiFor("owner-a");
    const ownerB = backend.apiFor("owner-b");
    const apiA = spyApi(ownerA);
    const apiB = spyApi(ownerB);
    mounted = mount(createElement(SkillsCard, { api: apiA }));

    click(await waitForButton(mounted.container, "编辑技能 alpha-skill"));
    expect((await waitForTextarea(mounted.container, "技能正文")).value).toBe("alpha body\n");

    act(() => {
      mounted!.root.render(createElement(SkillsCard, { api: apiB }));
    });
    await flush();

    expect(mounted.container.querySelector('textarea[aria-label="技能正文"]')).toBeNull();
    expect(apiB.saveOverride).not.toHaveBeenCalled();
    const entriesB = await ownerB.list();
    expect(entriesB.find((entry) => entry.name === "alpha-skill")?.overridden).toBe(false);
    await expectText(mounted.container, "Alpha synthetic skill");
  });

  it("drops a pending owner-A read after the swap instead of repopulating the editor", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const apiA = spyApi(backend.apiFor("owner-a"));
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
    slow.resolve({
      name: "alpha-skill",
      description: "Alpha synthetic skill",
      body: "alpha body\n",
      digest: "sha256:aa",
      source: "embedded",
    });
    await flush();

    expect(mounted.container.querySelector('textarea[aria-label="技能正文"]')).toBeNull();
    expect(mounted.container.textContent).not.toContain("读取技能内容…");
    expect(apiB.list).toHaveBeenCalled();
  });

  it("abandons a pending owner-A save completion after the swap", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const apiA = spyApi(backend.apiFor("owner-a"));
    const apiB = spyApi(backend.apiFor("owner-b"));
    mounted = mount(createElement(SkillsCard, { api: apiA }));
    await flush();

    clickButton(mounted.container, "新建自定义技能");
    setInputValue(inputByLabel(mounted.container, "技能名称"), "custom-skill");
    setInputValue(inputByLabel(mounted.container, "技能描述"), "desc");
    setInputValue(textareaByLabel(mounted.container, "技能正文"), "body");
    const slow = deferred<Awaited<ReturnType<SkillsApi["saveOverride"]>>>();
    apiA.saveOverride.mockReturnValueOnce(slow.promise);
    clickButton(mounted.container, "保存");
    await flush();

    act(() => {
      mounted!.root.render(createElement(SkillsCard, { api: apiB }));
    });
    await flush();
    slow.resolve({
      name: "custom-skill",
      description: "desc",
      source: "custom",
      overridden: false,
      disabled: false,
      digest: "sha256:bb",
      updatedAt: 1,
    });
    await flush();

    expect(apiA.list).toHaveBeenCalledTimes(1);
    expect(toast).not.toHaveBeenCalledWith("success", expect.anything());
    expect(mounted.container.querySelector('input[aria-label="技能名称"]')).toBeNull();
    await expectText(mounted.container, "Alpha synthetic skill");
  });

  it("abandons a pending owner-A toggle completion after the swap", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const apiA = spyApi(backend.apiFor("owner-a"));
    const apiB = spyApi(backend.apiFor("owner-b"));
    mounted = mount(createElement(SkillsCard, { api: apiA }));

    const slow = deferred<Awaited<ReturnType<SkillsApi["saveOverride"]>>>();
    apiA.saveOverride.mockReturnValueOnce(slow.promise);
    click(await waitForButton(mounted.container, "禁用技能 alpha-skill"));
    await flush();

    act(() => {
      mounted!.root.render(createElement(SkillsCard, { api: apiB }));
    });
    await flush();
    slow.resolve({
      name: "alpha-skill",
      description: "Alpha synthetic skill",
      source: "embedded",
      overridden: false,
      disabled: true,
      digest: "sha256:cc",
      updatedAt: 1,
    });
    await flush();

    expect(apiA.list).toHaveBeenCalledTimes(1);
    expect(toast).not.toHaveBeenCalledWith("success", expect.anything());
    await expectText(mounted.container, "Alpha synthetic skill");
  });

  it("abandons a pending owner-A delete completion after the swap", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const ownerA = backend.apiFor("owner-a");
    await seedEntries(ownerA);
    const apiA = spyApi(ownerA);
    const apiB = spyApi(backend.apiFor("owner-b"));
    mocks.ask.mockResolvedValue(true);
    mounted = mount(createElement(SkillsCard, { api: apiA }));

    const slow = deferred<void>();
    apiA.deleteOverride.mockReturnValueOnce(slow.promise);
    click(await waitForButton(mounted.container, "删除覆盖 alpha-skill"));
    await flush();

    act(() => {
      mounted!.root.render(createElement(SkillsCard, { api: apiB }));
    });
    await flush();
    slow.resolve();
    await flush();

    expect(apiA.list).toHaveBeenCalledTimes(1);
    expect(toast).not.toHaveBeenCalledWith("success", expect.anything());
    await expectText(mounted.container, "Alpha synthetic skill");
  });

  it("hides owner-A rows while owner B's list is still loading", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const ownerA = backend.apiFor("owner-a");
    await ownerA.saveOverride({ name: "alpha-skill", description: "Alpha overridden by A" });
    const apiA = spyApi(ownerA);
    const apiB = spyApi(backend.apiFor("owner-b"));
    mounted = mount(createElement(SkillsCard, { api: apiA }));
    await expectText(mounted.container, "Alpha overridden by A");

    const slow = deferred<SkillEntry[]>();
    apiB.list.mockReturnValueOnce(slow.promise);
    act(() => {
      mounted!.root.render(createElement(SkillsCard, { api: apiB }));
    });
    await flush();

    const text = mounted.container.textContent ?? "";
    expect(text).not.toContain("Alpha overridden by A");
    expect(text).not.toContain("alpha-skill");
    expect(mounted.container.querySelector('[aria-label="编辑技能 alpha-skill"]')).toBeNull();
    expect(mounted.container.querySelector('[aria-label="禁用技能 alpha-skill"]')).toBeNull();
    expect(text).toContain("读取中…");

    slow.resolve(await backend.apiFor("owner-b").list());
    await expectText(mounted.container, "Alpha synthetic skill");
  });

  it("cannot write or delete through stale owner-A rows while B loads", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const ownerA = backend.apiFor("owner-a");
    await ownerA.saveOverride({
      name: "alpha-skill",
      description: "Alpha overridden by A",
      body: "body from A\n",
    });
    const apiA = spyApi(ownerA);
    const apiB = spyApi(backend.apiFor("owner-b"));
    mounted = mount(createElement(SkillsCard, { api: apiA }));
    await expectText(mounted.container, "已覆盖");

    const slow = deferred<SkillEntry[]>();
    apiB.list.mockReturnValueOnce(slow.promise);
    act(() => {
      mounted!.root.render(createElement(SkillsCard, { api: apiB }));
    });
    await flush();

    expect(mounted.container.querySelector('[aria-label="禁用技能 alpha-skill"]')).toBeNull();
    expect(mounted.container.querySelector('[aria-label="删除覆盖 alpha-skill"]')).toBeNull();
    expect(apiB.saveOverride).not.toHaveBeenCalled();
    expect(apiB.deleteOverride).not.toHaveBeenCalled();

    slow.resolve(await backend.apiFor("owner-b").list());
    await flush();
    const entriesB = await backend.apiFor("owner-b").list();
    expect(entriesB.find((entry) => entry.name === "alpha-skill")?.overridden).toBe(false);
    const entriesA = await ownerA.list();
    expect(entriesA.find((entry) => entry.name === "alpha-skill")?.overridden).toBe(true);
  });

  it("hides an open owner-A editor immediately on swap even before B's list resolves", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const apiA = spyApi(backend.apiFor("owner-a"));
    const apiB = spyApi(backend.apiFor("owner-b"));
    mounted = mount(createElement(SkillsCard, { api: apiA }));

    click(await waitForButton(mounted.container, "编辑技能 alpha-skill"));
    expect((await waitForTextarea(mounted.container, "技能正文")).value).toBe("alpha body\n");

    const slow = deferred<SkillEntry[]>();
    apiB.list.mockReturnValueOnce(slow.promise);
    act(() => {
      mounted!.root.render(createElement(SkillsCard, { api: apiB }));
    });
    await flush();

    expect(mounted.container.querySelector('textarea[aria-label="技能正文"]')).toBeNull();
    expect(mounted.container.querySelector('input[aria-label="覆盖描述"]')).toBeNull();
    expect(apiB.saveOverride).not.toHaveBeenCalled();

    slow.resolve(await backend.apiFor("owner-b").list());
    await expectText(mounted.container, "Alpha synthetic skill");
  });

  it("refuses an immediate save from a detached owner-A editor after a flushSync swap", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const ownerA = backend.apiFor("owner-a");
    await ownerA.saveOverride({
      name: "alpha-skill",
      description: "A private description",
      body: "BODY FROM A",
    });
    const apiA = spyApi(ownerA);
    const apiB = spyApi(backend.apiFor("owner-b"));
    mounted = mount(createElement(SkillsCard, { api: apiA }));

    click(await waitForButton(mounted.container, "编辑技能 alpha-skill"));
    expect((await waitForTextarea(mounted.container, "技能正文")).value).toBe("BODY FROM A");
    const saveButton = [...mounted.container.querySelectorAll("button")].find(
      (candidate) => candidate.textContent?.trim() === "保存",
    ) as HTMLButtonElement;
    expect(saveButton.isConnected).toBe(true);

    // Synchronous owner swap: B's list stays pending and passive effects
    // are not flushed, so only same-render protections can take effect.
    // flushSync outside act is exactly the window under test; silence the
    // act-environment warning for this call only.
    const actFlag = (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean })
      .IS_REACT_ACT_ENVIRONMENT;
    const slow = deferred<SkillEntry[]>();
    apiB.list.mockReturnValueOnce(slow.promise);
    try {
      (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = false;
      flushSync(() => {
        mounted!.root.render(createElement(SkillsCard, { api: apiB }));
      });
    } finally {
      (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = actFlag;
    }

    expect(saveButton.isConnected).toBe(false);
    expect(mounted.container.querySelector('textarea[aria-label="技能正文"]')).toBeNull();
    expect(mounted.container.querySelector('input[aria-label="覆盖描述"]')).toBeNull();
    expect(mounted.container.textContent ?? "").not.toContain("编辑技能 alpha-skill");

    // Even a click delivered to the detached editor must not reach either
    // owner's backend.
    click(saveButton);
    await flush();
    expect(apiB.saveOverride).not.toHaveBeenCalled();
    expect(apiA.saveOverride).not.toHaveBeenCalled();

    slow.resolve(await backend.apiFor("owner-b").list());
    await expectText(mounted.container, "Alpha synthetic skill");
  });
});
