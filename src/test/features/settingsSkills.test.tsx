/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { createElement } from "react";
import { act } from "react";
import {
  click,
  clickButton,
  deferred,
  flush,
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
}

function spyApi(api: SkillsApi): SpiedApi {
  return {
    list: vi.fn(api.list),
    saveOverride: vi.fn(api.saveOverride),
    deleteOverride: vi.fn(api.deleteOverride),
    dispatch: vi.fn(api.dispatch),
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
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("AI 技能");
    expect(text).toContain("alpha-skill");
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
    await flush();
    expect(mounted!.container.textContent).toContain("还没有技能");
  });

  it("surfaces a list failure with a working retry", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const api = spyApi(backend.apiFor("owner-a"));
    api.list.mockRejectedValueOnce(new Error("disk failure"));
    mounted = mount(createElement(SkillsCard, { api }));
    await flush();
    expect(mounted.container.textContent).toContain("disk failure");

    clickButton(mounted.container, "重试");
    await flush();
    expect(mounted.container.textContent).toContain("alpha-skill");
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
    await flush();

    expect(api.saveOverride).toHaveBeenCalledWith({
      name: "custom-skill",
      description: "Custom skill",
      body: "custom body\n",
    });
    expect(toast).toHaveBeenCalledWith("success", expect.stringContaining("已保存"));
    expect(api.list).toHaveBeenCalledTimes(2);
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
    await flush();

    expect(mounted!.container.textContent).toContain("AI 技能内容无效: boom");
    expect(inputByLabel(mounted!.container, "技能名称").value).toBe("custom-skill");
    expect(api.list).toHaveBeenCalledTimes(1);
    expect(toast).not.toHaveBeenCalledWith("success", expect.anything());
  });

  it("edits an embedded skill with a partial description override and omits the body from the payload", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const api = renderWith(backend.apiFor("owner-a"));
    await flush();

    click(iconButton(mounted!.container, "编辑技能 alpha-skill"));
    await flush();
    expect(api.dispatch).toHaveBeenCalledWith("alpha-skill");
    const description = inputByLabel(mounted!.container, "技能描述");
    expect(description.disabled).toBe(true);
    expect(description.value).toBe("Alpha synthetic skill");
    expect(textareaByLabel(mounted!.container, "技能正文").value).toBe("alpha body\n");

    click(inputByLabel(mounted!.container, "覆盖描述"));
    setInputValue(inputByLabel(mounted!.container, "技能描述"), "Alpha overridden");
    clickButton(mounted!.container, "保存");
    await flush();

    const call: SkillOverrideInput = api.saveOverride.mock.calls[0]![0];
    expect(call).toMatchObject({ name: "alpha-skill", description: "Alpha overridden", disabled: false });
    expect("body" in call).toBe(false);
    expect(toast).toHaveBeenCalledWith("success", expect.stringContaining("已保存"));
  });

  it("prefills the edit form with the exact overridden fields", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const owner = backend.apiFor("owner-a");
    await owner.saveOverride({ name: "alpha-skill", description: "Alpha overridden" });
    renderWith(owner);
    await flush();

    click(iconButton(mounted!.container, "编辑技能 alpha-skill"));
    await flush();
    expect(inputByLabel(mounted!.container, "覆盖描述")).toBeTruthy();
    expect((inputByLabel(mounted!.container, "覆盖描述") as HTMLInputElement).checked).toBe(true);
    expect((inputByLabel(mounted!.container, "覆盖正文") as HTMLInputElement).checked).toBe(false);
    expect(inputByLabel(mounted!.container, "技能描述").value).toBe("Alpha overridden");
  });

  it("disables an embedded skill from the row and enables it again by deleting the disable-only override", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const api = renderWith(backend.apiFor("owner-a"));
    await flush();

    click(iconButton(mounted!.container, "禁用技能 alpha-skill"));
    await flush();
    expect(api.saveOverride).toHaveBeenCalledWith({
      name: "alpha-skill",
      description: undefined,
      body: undefined,
      disabled: true,
    });
    expect(mounted!.container.textContent).toContain("已禁用");

    click(iconButton(mounted!.container, "启用技能 alpha-skill"));
    await flush();
    expect(api.deleteOverride).toHaveBeenCalledWith("alpha-skill");
    expect(mounted!.container.textContent).not.toContain("已禁用");
  });

  it("keeps the overridden fields when toggling disable on an overridden skill", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const owner = backend.apiFor("owner-a");
    await owner.saveOverride({ name: "alpha-skill", description: "Alpha overridden" });
    const api = renderWith(owner);
    await flush();

    click(iconButton(mounted!.container, "禁用技能 alpha-skill"));
    await flush();
    expect(api.saveOverride).toHaveBeenCalledWith({
      name: "alpha-skill",
      description: "Alpha overridden",
      body: undefined,
      disabled: true,
    });

    click(iconButton(mounted!.container, "启用技能 alpha-skill"));
    await flush();
    expect(api.saveOverride).toHaveBeenLastCalledWith({
      name: "alpha-skill",
      description: "Alpha overridden",
      body: undefined,
      disabled: false,
    });
    expect(mounted!.container.textContent).toContain("已覆盖");
  });

  it("deletes an override only after confirmation, then reverts the row", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const owner = backend.apiFor("owner-a");
    await seedEntries(owner);
    mocks.ask.mockResolvedValue(true);
    const api = renderWith(owner);
    await flush();
    expect(mounted!.container.textContent).toContain("已覆盖");

    click(iconButton(mounted!.container, "删除覆盖 alpha-skill"));
    await flush();
    expect(String(mocks.ask.mock.calls[0]?.[0] ?? "")).toContain("恢复为内置内容");
    expect(api.deleteOverride).toHaveBeenCalledWith("alpha-skill");
    expect(toast).toHaveBeenCalledWith("success", expect.stringContaining("已恢复为内置版本"));
    expect(mounted!.container.textContent).not.toContain("已覆盖");
  });

  it("does nothing when the delete confirmation is declined", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const owner = backend.apiFor("owner-a");
    await seedEntries(owner);
    mocks.ask.mockResolvedValue(false);
    const api = renderWith(owner);
    await flush();

    click(iconButton(mounted!.container, "删除覆盖 alpha-skill"));
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
    await flush();

    api.deleteOverride.mockRejectedValueOnce(new Error("read-only database"));
    click(iconButton(mounted!.container, "删除覆盖 alpha-skill"));
    await flush();
    expect(toast).toHaveBeenCalledWith("error", expect.stringContaining("删除失败"));
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
    await flush();
    expect(mounted.container.textContent).toContain("Alpha overridden by A");

    act(() => {
      mounted!.root.render(createElement(SkillsCard, { api: apiB }));
    });
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(apiB.list).toHaveBeenCalled();
    expect(text).toContain("Alpha synthetic skill");
    expect(text).not.toContain("Alpha overridden by A");
  });

  it("drops a stale list result that lands after a newer reload", async () => {
    const backend = createSkillsMockBackend(EMBEDDED);
    const api = spyApi(backend.apiFor("owner-a"));
    const stale = deferred<SkillEntry[]>();
    api.list.mockReturnValueOnce(stale.promise);
    mounted = mount(createElement(SkillsCard, { api }));

    clickButton(mounted.container, "刷新");
    await flush();
    expect(mounted.container.textContent).toContain("alpha-skill");

    stale.resolve([]);
    await flush();
    expect(mounted.container.textContent).toContain("alpha-skill");
  });
});
