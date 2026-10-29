/** @vitest-environment jsdom */
import { act, createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { click, clickButton, flush, chooseSelectOption, mountWithClient, setInputValue, type MountedView } from "./reactTestUtils";
import type { StateRuleSaveRequest, StateRuleScreenDTO } from "../../ipc/stateRuleApi";

const mocks = vi.hoisted(() => ({
  snapshot: vi.fn(),
  assetList: vi.fn(),
  validate: vi.fn(),
  draft: vi.fn(),
  save: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return { ...actual, assetApi: { list: mocks.assetList }, terminalApi: { snapshot: mocks.snapshot } };
});
vi.mock("../../ipc/stateRuleApi", () => ({
  stateRuleApi: { validate: mocks.validate, draft: mocks.draft, save: mocks.save },
}));

import { StateMarkDialog } from "../../features/session/StateMarkDialog";
import { RULE_VALIDATE_DELAY_MS } from "../../features/session/StateRuleForm";
import type { StateMarkRequest } from "../../features/session/stateMarkRequests";

const request: StateMarkRequest = {
  appTabId: "app-tab",
  tabId: "tab-1",
  assetId: "A1",
  title: "deploy",
  intent: "this_is_it",
  current: "running",
};

// validateLikeBackend accepts any rule but one with lookahead, which RE2 rejects, and reports a match when the
// pattern's literal text is on the screen.
function validateLikeBackend(rule: StateRuleSaveRequest, screen?: StateRuleScreenDTO) {
  if (rule.pattern.includes("(?=")) {
    return Promise.resolve({ valid: false, field: "pattern", error: "匹配模式无效: invalid or unsupported Perl syntax: `(?=`", tested: false, matched: false });
  }
  const literal = rule.pattern.replace(/\\(.)/g, "$1");
  const text = screen?.lines.join("\n") ?? "";
  return Promise.resolve({ valid: true, lines: rule.lines, tested: Boolean(screen), matched: text.includes(literal), region: text });
}

let mounted: MountedView | undefined;
let onClose: ReturnType<typeof vi.fn<() => void>>;

async function settleValidation() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(RULE_VALIDATE_DELAY_MS);
  });
  await flush();
}

function patternInput(): HTMLTextAreaElement {
  return mounted!.container.querySelector("textarea")!;
}

function saveButton(): HTMLButtonElement {
  return [...mounted!.container.querySelectorAll("button")].find((button) => button.textContent?.trim() === "保存规则")!;
}

async function open() {
  onClose = vi.fn<() => void>();
  mounted = mountWithClient(createElement(StateMarkDialog, { request, onClose }));
  await flush();
  await settleValidation();
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.clearAllMocks();
  mocks.snapshot.mockResolvedValue({
    text: "",
    lines: ["$ ./deploy.sh", "Overwrite app.js? [y/N] ", ""],
    cursorRow: 1,
    cursorCol: 24,
    cols: 80,
    rows: 3,
    altScreen: false,
    lastOutputMsAgo: 0,
  });
  mocks.assetList.mockResolvedValue([{ id: "A1", name: "web-1" }]);
  mocks.validate.mockImplementation(validateLikeBackend);
  mocks.save.mockImplementation((rule: StateRuleSaveRequest) => Promise.resolve({ ...rule, id: "R1", updatedAt: 1 }));
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.useRealTimers();
});

describe("state marking dialog", () => {
  it("drafts from the last screen line, tests it on the screen and saves it for the host", async () => {
    await open();
    expect(mocks.snapshot).toHaveBeenCalledWith("tab-1");
    expect(patternInput().value).toBe("Overwrite app\\.js\\? \\[y/N\\]");
    expect(mounted!.container.querySelector("[data-rule-test]")?.getAttribute("data-rule-test")).toBe("match");
    expect(mocks.validate).toHaveBeenLastCalledWith(
      expect.objectContaining({ scope: "host", assetId: "A1", state: "needs_me" }),
      expect.objectContaining({ cursorRow: 1 }),
    );

    clickButton(mounted!.container, "保存规则");
    await flush();
    expect(mocks.save).toHaveBeenCalledWith(
      expect.objectContaining({ pattern: "Overwrite app\\.js\\? \\[y/N\\]", scope: "host", assetId: "A1", state: "needs_me", enabled: true }),
    );
    expect(onClose).toHaveBeenCalled();
  });

  it("shows an invalid AI draft with its error and does not save it", async () => {
    mocks.draft.mockResolvedValue({ name: "bad", region: "bottom", lines: 3, pattern: "(?=Overwrite)", state: "needs_me" });
    await open();
    clickButton(mounted!.container, "AI 起草");
    await flush();
    expect(mocks.draft).toHaveBeenCalledWith("this_is_it", "running", expect.objectContaining({ lines: expect.any(Array) }));
    expect(patternInput().value).toBe("(?=Overwrite)");
    await settleValidation();

    const describedBy = patternInput().getAttribute("aria-describedby")!;
    const error = mounted!.container.querySelector(`#${CSS.escape(describedBy.split(" ")[0])}`)!;
    expect(error.textContent).toContain("匹配模式无效");
    expect(error.getAttribute("role")).toBeNull();
    expect(patternInput().getAttribute("aria-invalid")).toBe("true");
    expect(saveButton().disabled).toBe(true);
    clickButton(mounted!.container, "保存规则");
    await flush();
    expect(mocks.save).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
  });

  it("keeps the manual path when no AI model is configured", async () => {
    mocks.draft.mockRejectedValue({ code: "unsupported", message: "AI 模型不可用: 尚未选择要使用的 AI 模型档案" });
    await open();
    clickButton(mounted!.container, "AI 起草");
    await flush();
    expect(mounted!.container.querySelector("[data-ai-draft-error]")?.textContent).toContain("AI 模型不可用");
    expect(patternInput().value).toBe("Overwrite app\\.js\\? \\[y/N\\]");

    setInputValue(patternInput(), "Overwrite .*\\? \\[y/N\\]");
    await settleValidation();
    expect(saveButton().disabled).toBe(false);
    clickButton(mounted!.container, "保存规则");
    await flush();
    expect(mocks.save).toHaveBeenCalledWith(expect.objectContaining({ pattern: "Overwrite .*\\? \\[y/N\\]" }));
  });

  it("reports a rejected save inline and stays open", async () => {
    mocks.save.mockRejectedValue({ code: "bad_param", message: "状态规则最多 64 条" });
    await open();
    clickButton(mounted!.container, "保存规则");
    await flush();
    const alerts = [...mounted!.container.querySelectorAll('[role="alert"]')].map((alert) => alert.textContent ?? "");
    expect(alerts.some((text) => text.startsWith("保存失败") && text.includes("状态规则最多 64 条"))).toBe(true);
    expect(onClose).not.toHaveBeenCalled();
  });

  it("shows the region the AI receives and says when secrets were masked", async () => {
    mocks.snapshot.mockResolvedValue({
      text: "",
      lines: ["$ export TOKEN=abc123secret", "Overwrite app.js? [y/N] ", ""],
      cursorRow: 1,
      cursorCol: 24,
      cols: 80,
      rows: 3,
      altScreen: false,
      lastOutputMsAgo: 0,
    });
    mocks.draft.mockResolvedValue({ name: "n", region: "bottom", lines: 3, pattern: "Overwrite", state: "needs_me", redacted: true });
    await open();
    expect(mounted!.container.querySelector("[data-ai-sent-rows]")?.textContent).toBe("$ export TOKEN=abc123secret\nOverwrite app.js? [y/N] ");
    expect(mounted!.container.textContent).not.toContain("发送前已对疑似密钥或密码脱敏");
    clickButton(mounted!.container, "AI 起草");
    await flush();
    expect(mounted!.container.textContent).toContain("已对疑似密钥或密码脱敏");
  });

  it("warns when the heuristic draft had to mask a secret", async () => {
    mocks.snapshot.mockResolvedValue({
      text: "",
      lines: ["export TOKEN=abc123secret", ""],
      cursorRow: 1,
      cursorCol: 0,
      cols: 80,
      rows: 2,
      altScreen: false,
      lastOutputMsAgo: 0,
    });
    await open();
    expect(patternInput().value).not.toContain("abc123secret");
    expect(mounted!.container.textContent).toContain("已对疑似密钥或密码脱敏");
  });

  it("keeps the chosen scope and typed keys when the AI drafts", async () => {
    mocks.draft.mockResolvedValue({ name: "n", region: "bottom", lines: 3, pattern: "Overwrite", state: "needs_me" });
    await open();
    chooseSelectOption(mounted!.container, "适用范围", "所有主机");
    const keys = mounted!.container.querySelector<HTMLInputElement>('input[placeholder="例如 y"]')!;
    setInputValue(keys, "y");
    clickButton(mounted!.container, "AI 起草");
    await flush();
    expect(patternInput().value).toBe("Overwrite");
    expect(mounted!.container.querySelector<HTMLInputElement>('input[placeholder="例如 y"]')!.value).toBe("y");
    await settleValidation();
    expect(mocks.validate).toHaveBeenLastCalledWith(expect.objectContaining({ scope: "global", keys: "y" }), expect.anything());
  });

  it("warns about a very short or open pattern", async () => {
    await open();
    const warning = () => mounted!.container.querySelector("[data-pattern-warning]")?.textContent ?? null;
    expect(warning()).toBeNull();
    setInputValue(patternInput(), "ok");
    expect(warning()).toContain("很短");
    setInputValue(patternInput(), ".*");
    expect(warning()).toContain("几乎匹配任何内容");
    setInputValue(patternInput(), "Overwrite");
    expect(warning()).toBeNull();
  });

  it("closes on an overlay click until the form is edited", async () => {
    await open();
    const overlay = mounted!.container.querySelector<HTMLElement>(".nx-overlay")!;
    click(overlay);
    expect(onClose).toHaveBeenCalledTimes(1);
    setInputValue(patternInput(), "changed");
    click(overlay);
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
