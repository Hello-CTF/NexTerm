/** @vitest-environment jsdom */
//
// AI 长期记忆设置卡片（M46）：opt-in 开关、条目 CRUD、版本冲突、loading/empty/error/retry、
// 密钥策略提示。运行环境固定成服务端模式（web），与 securitySettings 同一套路。
//
// 断言节奏与仓库里其他 React 测试一致：异步推进用 `flush()`（act 退出时 React
// 会冲刷渲染），不在 vi.waitFor 里断言 React DOM。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import {
  click,
  clickButton,
  deferred,
  flush,
  mount,
  setInputValue,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    settings: vi.fn(),
    index: vi.fn(),
    get: vi.fn(),
    create: vi.fn(),
    edit: vi.fn(),
    delete: vi.fn(),
    setSettings: vi.fn(),
    ask: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../../ipc/memory", () => ({
  memoryApi: {
    settings: mocks.settings,
    index: mocks.index,
    get: mocks.get,
    create: mocks.create,
    edit: mocks.edit,
    delete: mocks.delete,
    setSettings: mocks.setSettings,
  },
}));
vi.mock("../../ipc/commands", () => ({
  // toAppError 用与真身一致的形状归一化：测试里的错误都是 AppError 形状的对象
  toAppError: (e: unknown) =>
    e && typeof e === "object" && "code" in e
      ? (e as { code: string })
      : { code: "internal", message: String(e) },
  // store.ts 的顶层命名导入需要这些出口存在（运行期才用到，空对象即可）。
  dbApi: {},
  sessionApi: {},
  terminalApi: {},
  vaultApi: {},
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

import { MemoryCard } from "../../features/settings/MemoryCard";
import { useUi } from "../../app/store";
import type { MemoryTopicIndex } from "../../ipc/memory";

const SCOPE = { tenant: "local", subject: "default" };

const SETTINGS_V3 = { injectionEnabled: false, toolsEnabled: false, version: 3 };

const ENTRY_FULL = {
  id: "m-1",
  topic: "operations",
  content: "restart at 02:00",
  version: 2,
  redacted: false,
  createdAt: 1,
  updatedAt: 2,
};

const INDEX_ONE: MemoryTopicIndex[] = [
  { topic: "operations", entries: [{ id: "m-1", version: 2, redacted: false, updatedAt: 2 }] },
];

const CONFLICT = {
  code: "bad_param",
  message: "version conflict: m-1 (expected 2, actual 5)",
  detail: { id: "m-1", expected: 2, actual: 5 },
};

/** 图标按钮没有文字，按 aria-label 找。 */
function iconButton(container: ParentNode, label: string): HTMLButtonElement {
  const button = [...container.querySelectorAll("button")].find(
    (candidate) => candidate.getAttribute("aria-label") === label,
  );
  if (!button) throw new Error(`Icon button not found: ${label}`);
  return button;
}

function inputByLabel(container: ParentNode, label: string): HTMLInputElement {
  const input = container.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`);
  if (!input) throw new Error(`Input not found: ${label}`);
  return input;
}

describe("MemoryCard", () => {
  let mounted: MountedView | undefined;

  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    useUi.setState({ pushToast: mocks.toast });
    mocks.settings.mockResolvedValue(SETTINGS_V3);
    mocks.index.mockResolvedValue(INDEX_ONE);
  });
  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("shows loading first, then settings toggles and grouped entries", async () => {
    const slow = deferred<MemoryTopicIndex[]>();
    mocks.index.mockReturnValue(slow.promise);
    mounted = mount(createElement(MemoryCard));
    expect(mounted.container.textContent).toContain("读取中…");

    slow.resolve(INDEX_ONE);
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("AI 长期记忆");
    expect(text).toContain("operations");
    expect(text).toContain("m-1");
    expect(text).toContain("1 条");
    // scope 提示：让用户知道这份记忆归谁
    expect(text).toContain("local / default");
    expect(mocks.settings).toHaveBeenCalledWith(SCOPE);
    expect(mocks.index).toHaveBeenCalledWith(SCOPE);
  });

  it("shows an empty hint when nothing is remembered yet", async () => {
    mocks.index.mockResolvedValue([]);
    mounted = mount(createElement(MemoryCard));
    await flush();
    expect(mounted.container.textContent).toContain("还没有记忆");
  });

  it("surfaces a list failure with a working retry", async () => {
    mocks.index.mockRejectedValueOnce(new Error("磁盘炸了")).mockResolvedValueOnce(INDEX_ONE);
    mounted = mount(createElement(MemoryCard));
    await flush();
    expect(mounted.container.textContent).toContain("磁盘炸了");

    clickButton(mounted.container, "重试");
    await flush();
    expect(mounted.container.textContent).toContain("m-1");
    expect(mocks.index).toHaveBeenCalledTimes(2);
  });

  it("toggles the injection opt-in with the read version (CAS)", async () => {
    mocks.setSettings.mockResolvedValue({ injectionEnabled: true, toolsEnabled: false, version: 4 });
    mounted = mount(createElement(MemoryCard));
    await flush();

    click(inputByLabel(mounted.container, "注入到 AI 运行"));
    await flush();
    expect(mocks.setSettings).toHaveBeenCalledWith(SCOPE, 3, { injectionEnabled: true });
    expect((inputByLabel(mounted.container, "注入到 AI 运行") as HTMLInputElement).checked).toBe(true);
  });

  it("reloads and says so when the settings toggle hits a version conflict", async () => {
    mocks.setSettings.mockRejectedValue({
      code: "bad_param",
      message: "version conflict: memory-settings (expected 3, actual 7)",
      detail: { id: "memory-settings", expected: 3, actual: 7 },
    });
    mounted = mount(createElement(MemoryCard));
    await flush();

    click(inputByLabel(mounted.container, "允许模型使用记忆工具"));
    await flush();
    expect(mocks.toast).toHaveBeenCalledWith(
      "error",
      expect.stringContaining("已被其他地方修改"),
    );
    // 冲突后必须重新读取，而不是拿着旧版本硬写
    expect(mocks.settings).toHaveBeenCalledTimes(2);
  });

  it("creates an entry from the inline form with the reject policy by default", async () => {
    mocks.create.mockResolvedValue(ENTRY_FULL);
    mounted = mount(createElement(MemoryCard));
    await flush();

    clickButton(mounted.container, "新建一条记忆");
    const topic = inputByLabel(mounted.container, "记忆主题");
    const content = mounted.container.querySelector<HTMLTextAreaElement>(
      'textarea[aria-label="记忆内容"]',
    );
    if (!content) throw new Error("content textarea not found");
    setInputValue(topic, "operations");
    setInputValue(content, "restart at 03:00");
    clickButton(mounted.container, "保存");
    await flush();

    expect(mocks.create).toHaveBeenCalledWith(SCOPE, "operations", "restart at 03:00", "reject");
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("已保存"));
    expect(mocks.index).toHaveBeenCalledTimes(2);
  });

  it("keeps the form and shows the kernel message when a secret-like write is rejected", async () => {
    mocks.create.mockRejectedValue({
      code: "bad_param",
      message: "semantic memory contains a possible secret",
    });
    mounted = mount(createElement(MemoryCard));
    await flush();

    clickButton(mounted.container, "新建一条记忆");
    setInputValue(inputByLabel(mounted.container, "记忆主题"), "ops");
    setInputValue(
      mounted.container.querySelector<HTMLTextAreaElement>('textarea[aria-label="记忆内容"]')!,
      "api_key=sk-live-123",
    );
    clickButton(mounted.container, "保存");
    await flush();

    const text = mounted.container.textContent ?? "";
    expect(text).toContain("possible secret");
    // 表单内容保留（用户改完策略还能再提交），且没假装成功去刷新列表
    expect(inputByLabel(mounted.container, "记忆主题").value).toBe("ops");
    expect(mocks.index).toHaveBeenCalledTimes(1);
    expect(mocks.toast).not.toHaveBeenCalledWith("success", expect.anything());
  });

  it("edits an entry: loads the body first, saves with the entry version", async () => {
    mocks.get.mockResolvedValue(ENTRY_FULL);
    mocks.edit.mockResolvedValue({ ...ENTRY_FULL, content: "restart at 03:00", version: 3 });
    mounted = mount(createElement(MemoryCard));
    await flush();

    click(iconButton(mounted.container, "编辑记忆 m-1"));
    await flush();
    expect(mocks.get).toHaveBeenCalledWith(SCOPE, "m-1");
    expect(mounted.container.textContent).toContain("编辑记忆");
    expect(inputByLabel(mounted.container, "记忆主题").value).toBe("operations");
    const content = mounted.container.querySelector<HTMLTextAreaElement>(
      'textarea[aria-label="记忆内容"]',
    )!;
    expect(content.value).toBe("restart at 02:00");

    setInputValue(content, "restart at 03:00");
    clickButton(mounted.container, "保存");
    await flush();

    expect(mocks.edit).toHaveBeenCalledWith(SCOPE, "m-1", 2, {
      topic: "operations",
      content: "restart at 03:00",
      secrets: "reject",
    });
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("已更新"));
  });

  it("reports an edit version conflict and reloads instead of overwriting", async () => {
    mocks.get.mockResolvedValue(ENTRY_FULL);
    mocks.edit.mockRejectedValue(CONFLICT);
    mounted = mount(createElement(MemoryCard));
    await flush();

    click(iconButton(mounted.container, "编辑记忆 m-1"));
    await flush();
    setInputValue(
      mounted.container.querySelector<HTMLTextAreaElement>('textarea[aria-label="记忆内容"]')!,
      "stale edit",
    );
    clickButton(mounted.container, "保存");
    await flush();

    const text = mounted.container.textContent ?? "";
    expect(text).toContain("已被其他地方修改");
    expect(text).toContain("2 → 5");
    // 冲突后重新读取列表；旧内容没有硬盖上去（edit 只发了一次）
    expect(mocks.index).toHaveBeenCalledTimes(2);
    expect(mocks.edit).toHaveBeenCalledTimes(1);
    expect(mocks.toast).not.toHaveBeenCalledWith("success", expect.anything());
  });

  it("asks before deleting and does nothing when the confirmation is declined", async () => {
    mocks.ask.mockResolvedValue(false);
    mounted = mount(createElement(MemoryCard));
    await flush();

    click(iconButton(mounted.container, "删除记忆 m-1"));
    await flush();
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    // 确认框必须点名删的是哪条（主题），不能是一句泛泛的「确定吗」
    expect(String(mocks.ask.mock.calls[0]?.[0] ?? "")).toContain("operations");
    expect(mocks.delete).not.toHaveBeenCalled();
    expect(mounted.container.textContent).toContain("m-1");
  });

  it("deletes only after confirmation, then reloads the list", async () => {
    mocks.ask.mockResolvedValue(true);
    mocks.delete.mockResolvedValue(undefined);
    mocks.index.mockResolvedValueOnce(INDEX_ONE).mockResolvedValueOnce([]);
    mounted = mount(createElement(MemoryCard));
    await flush();

    click(iconButton(mounted.container, "删除记忆 m-1"));
    await flush();
    expect(mocks.delete).toHaveBeenCalledWith(SCOPE, "m-1", 2);
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("已删除"));
    expect(mocks.index).toHaveBeenCalledTimes(2);
    expect(mounted.container.textContent).toContain("还没有记忆");
  });

  it("keeps the entry and reports when the delete itself fails", async () => {
    mocks.ask.mockResolvedValue(true);
    mocks.delete.mockRejectedValue(new Error("只读数据库"));
    mounted = mount(createElement(MemoryCard));
    await flush();

    click(iconButton(mounted.container, "删除记忆 m-1"));
    await flush();
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("删除失败"));
    // 失败就不该再拉一次列表（库没变），条目也还在
    expect(mocks.index).toHaveBeenCalledTimes(1);
    expect(mounted.container.textContent).toContain("m-1");
  });

  it("expands an entry to load its body on demand, with a retry on failure", async () => {
    mocks.get.mockRejectedValueOnce(new Error("网络抖动")).mockResolvedValueOnce(ENTRY_FULL);
    mounted = mount(createElement(MemoryCard));
    await flush();

    const expander = mounted.container.querySelector<HTMLButtonElement>("button[aria-expanded]");
    if (!expander) throw new Error("expander not found");
    click(expander);
    await flush();
    expect(mocks.get).toHaveBeenCalledWith(SCOPE, "m-1");
    expect(mounted.container.textContent).toContain("正文读取失败");

    clickButton(mounted.container, "重试");
    await flush();
    expect(mounted.container.textContent).toContain("restart at 02:00");
    expect(mocks.get).toHaveBeenCalledTimes(2);
  });

  it("keeps the edit form unsubmittable while the body loads; a late response never clobbers a newer draft", async () => {
    const slow = deferred<typeof ENTRY_FULL>();
    mocks.get.mockReturnValue(slow.promise);
    mounted = mount(createElement(MemoryCard));
    await flush();

    click(iconButton(mounted.container, "编辑记忆 m-1"));
    await flush();
    // 读取中：显式提示，且结构上就没有可提交的表单
    expect(mounted.container.textContent).toContain("读取记忆正文…");
    expect(mounted.container.querySelector('textarea[aria-label="记忆内容"]')).toBeNull();
    expect(mocks.create).not.toHaveBeenCalled();
    expect(mocks.edit).not.toHaveBeenCalled();

    // 读取期间取消并改为新建；迟到的 get 响应不得把新草稿覆盖成编辑
    clickButton(mounted.container, "取消");
    await flush();
    clickButton(mounted.container, "新建一条记忆");
    setInputValue(inputByLabel(mounted.container, "记忆主题"), "draft-topic");
    slow.resolve(ENTRY_FULL);
    await flush();
    expect(mounted.container.textContent).toContain("新建记忆");
    expect(inputByLabel(mounted.container, "记忆主题").value).toBe("draft-topic");
    expect(mocks.create).not.toHaveBeenCalled();
    expect(mocks.edit).not.toHaveBeenCalled();
  });

  it("closes the form and reports when the edit body fails to load", async () => {
    mocks.get.mockRejectedValue(new Error("网络抖动"));
    mounted = mount(createElement(MemoryCard));
    await flush();

    click(iconButton(mounted.container, "编辑记忆 m-1"));
    await flush();
    expect(mocks.toast).toHaveBeenCalledWith(
      "error",
      expect.stringContaining("读取记忆正文失败"),
    );
    // 表单已关，不会留着一个能误提交的壳
    expect(mounted.container.querySelector('textarea[aria-label="记忆内容"]')).toBeNull();
    expect(mounted.container.textContent).toContain("新建一条记忆");
  });

  it("shows the refresh failure instead of the empty state when a reload fails after an empty list", async () => {
    mocks.index.mockResolvedValueOnce([]).mockRejectedValueOnce(new Error("磁盘炸了"));
    mounted = mount(createElement(MemoryCard));
    await flush();
    expect(mounted.container.textContent).toContain("还没有记忆");

    clickButton(mounted.container, "刷新");
    await flush();
    const text = mounted.container.textContent ?? "";
    // 空态不得在无标注的情况下冒充最新状态
    expect(text).toContain("磁盘炸了");
    expect(text).not.toContain("还没有记忆");

    clickButton(mounted.container, "重试");
    await flush();
    expect(mounted.container.textContent).toContain("m-1");
    expect(mocks.index).toHaveBeenCalledTimes(3);
  });

  it("shows the refresh failure instead of stale entries when a reload fails", async () => {
    mocks.index.mockResolvedValueOnce(INDEX_ONE).mockRejectedValueOnce(new Error("磁盘炸了"));
    mounted = mount(createElement(MemoryCard));
    await flush();
    expect(mounted.container.textContent).toContain("m-1");

    clickButton(mounted.container, "刷新");
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("磁盘炸了");
    expect(text).not.toContain("m-1");
  });

  it("drops a stale list result that lands after a newer reload", async () => {
    const stale = deferred<MemoryTopicIndex[]>();
    mocks.index.mockReturnValueOnce(stale.promise).mockResolvedValue(INDEX_ONE);
    mounted = mount(createElement(MemoryCard));

    clickButton(mounted.container, "刷新");
    await flush();
    expect(mounted.container.textContent).toContain("m-1");

    stale.resolve([]);
    await flush();
    // 迟到的旧结果（空列表）不得覆盖新列表
    expect(mounted.container.textContent).toContain("m-1");
  });
});
