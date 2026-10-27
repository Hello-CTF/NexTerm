/** @vitest-environment jsdom */
import { act, createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { click, clickButton, flush, mountWithClient, setInputValue, setSelectValue, type MountedView } from "./reactTestUtils";
import type { StateRuleDTO, StateRuleSaveRequest, StateRuleScreenDTO } from "../../ipc/stateRuleApi";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    list: vi.fn(),
    status: vi.fn(),
    save: vi.fn(),
    remove: vi.fn(),
    reorder: vi.fn(),
    exportRules: vi.fn(),
    importRules: vi.fn(),
    validate: vi.fn(),
    snapshot: vi.fn(),
    assetList: vi.fn(),
    ask: vi.fn(),
    pickFile: vi.fn(),
    saveBlob: vi.fn(),
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return { ...actual, assetApi: { list: mocks.assetList }, terminalApi: { snapshot: mocks.snapshot } };
});
vi.mock("../../ipc/stateRuleApi", () => ({
  stateRuleApi: {
    list: mocks.list,
    status: mocks.status,
    save: mocks.save,
    delete: mocks.remove,
    reorder: mocks.reorder,
    export: mocks.exportRules,
    import: mocks.importRules,
    validate: mocks.validate,
  },
}));
vi.mock("../../ipc/webFiles", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/webFiles")>();
  return { ...actual, pickBrowserFile: mocks.pickFile, saveBrowserBlob: mocks.saveBlob };
});
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask };
});

import { SessionRulesCard } from "../../features/settings/SessionRulesCard";
import { RULE_VALIDATE_DELAY_MS } from "../../features/session/StateRuleForm";
import { useUi } from "../../app/store";

function rule(id: string, name: string, patch: Partial<StateRuleDTO> = {}): StateRuleDTO {
  return { id, name, scope: "global", region: "bottom", lines: 5, pattern: name, state: "needs_me", enabled: true, updatedAt: 1, ...patch };
}

let rules: StateRuleDTO[];
let mounted: MountedView | undefined;

function rows(): string[] {
  return [...mounted!.container.querySelectorAll("[data-rule-id]")].map((row) => row.getAttribute("data-rule-id")!);
}

function button(label: string): HTMLButtonElement {
  const found = mounted!.container.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`);
  if (!found) throw new Error(`button ${label} not found`);
  return found;
}

function alerts(): string[] {
  return [...mounted!.container.querySelectorAll('[role="alert"]')].map((alert) => alert.textContent ?? "");
}

async function render() {
  mounted = mountWithClient(createElement(SessionRulesCard));
  await flush();
}

beforeEach(() => {
  vi.clearAllMocks();
  rules = [
    rule("R1", "review", { scope: "host", assetId: "A1" }),
    rule("R2", "thinking", { state: "running", region: "title", lines: undefined }),
  ];
  mocks.list.mockImplementation(() => Promise.resolve(rules));
  mocks.status.mockResolvedValue([
    { assetId: "A1", status: "applied", revision: "r", skipped: ["R2"] },
    { assetId: "A2", status: "pending", error: "helper unavailable" },
  ]);
  mocks.assetList.mockResolvedValue([
    { id: "A1", name: "web-1" },
    { id: "A2", name: "db-1" },
  ]);
  mocks.reorder.mockImplementation((ids: string[]) => {
    rules = ids.map((id) => rules.find((item) => item.id === id)!);
    return Promise.resolve(rules);
  });
  mocks.save.mockImplementation((input: StateRuleSaveRequest) => {
    rules = rules.map((item) => (item.id === input.id ? { ...item, ...input } : item));
    return Promise.resolve(rules.find((item) => item.id === input.id));
  });
  mocks.validate.mockImplementation((input: StateRuleSaveRequest, screen?: StateRuleScreenDTO) =>
    Promise.resolve({
      valid: true,
      tested: Boolean(screen),
      matched: Boolean(screen?.lines.some((line) => line.includes(input.pattern))),
      region: screen?.lines.join("\n"),
    }),
  );
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.useRealTimers();
});

describe("state rules settings card", () => {
  it("lists rules in order with their scope and the per-host push status", async () => {
    await render();
    expect(rows()).toEqual(["R1", "R2"]);
    const text = mounted!.container.textContent ?? "";
    expect(text).toContain("仅 web-1");
    expect(text).toContain("窗口标题");
    const statuses = [...mounted!.container.querySelectorAll("[data-status]")].map((item) => item.textContent ?? "");
    expect(statuses[0]).toContain("web-1");
    expect(statuses[0]).toContain("已生效");
    expect(statuses[0]).toContain("跳过无效规则：thinking");
    expect(statuses[1]).toContain("等待连接");
    expect(statuses[1]).toContain("helper unavailable");
  });

  it("reorders, toggles and deletes through the backend", async () => {
    await render();
    click(button("下移 review"));
    await flush();
    expect(mocks.reorder).toHaveBeenCalledWith(["R2", "R1"]);
    expect(rows()).toEqual(["R2", "R1"]);

    click(mounted!.container.querySelector<HTMLInputElement>('input[aria-label="启用 review"]')!);
    await flush();
    expect(mocks.save).toHaveBeenCalledWith(expect.objectContaining({ id: "R1", enabled: false, scope: "host", assetId: "A1" }));

    mocks.ask.mockResolvedValue(false);
    click(button("删除 thinking"));
    await flush();
    expect(mocks.remove).not.toHaveBeenCalled();
    mocks.ask.mockResolvedValue(true);
    mocks.remove.mockImplementation((id: string) => {
      rules = rules.filter((item) => item.id !== id);
      return Promise.resolve();
    });
    click(button("删除 thinking"));
    await flush();
    expect(mocks.remove).toHaveBeenCalledWith("R2");
    expect(rows()).toEqual(["R1"]);
  });

  it("filters by search and holds the order while filtered", async () => {
    await render();
    const search = mounted!.container.querySelector<HTMLInputElement>('input[aria-label="搜索状态规则"]')!;
    setInputValue(search, "think");
    expect(rows()).toEqual(["R2"]);
    expect(button("上移 thinking").disabled).toBe(true);
  });

  it("exports a file that imports back and rejects a broken file before sending it", async () => {
    const exported = { format: "nexterm.state-rules", version: 1, rules };
    mocks.exportRules.mockResolvedValue(exported);
    await render();
    clickButton(mounted!.container, "导出");
    await flush();
    const blob = mocks.saveBlob.mock.calls[0][1] as Blob;
    const text = await blob.text();

    mocks.importRules.mockResolvedValue({ created: 0, updated: 0, unchanged: 2 });
    mocks.pickFile.mockResolvedValue(new File([text], "rules.json"));
    clickButton(mounted!.container, "导入");
    await flush();
    await flush();
    expect(mocks.importRules).toHaveBeenCalledWith(exported);

    mocks.importRules.mockClear();
    mocks.pickFile.mockResolvedValue(new File(['{"format":"other","version":1,"rules":[]}'], "x.json"));
    clickButton(mounted!.container, "导入");
    await flush();
    await flush();
    expect(mocks.importRules).not.toHaveBeenCalled();
    expect(alerts().some((alert) => alert.includes("不是可识别的状态规则文件"))).toBe(true);
  });

  it("tests a rule on the screen of a chosen terminal tab", async () => {
    vi.useFakeTimers();
    useUi.setState({
      workspaces: [
        {
          id: "w1",
          kind: "session",
          title: "web-1",
          closable: true,
          activePaneId: "p1",
          panes: [{ id: "p1", activeTabId: "t1", tabs: [{ id: "t1", kind: "terminal", title: "agent", tabId: "backend-1", closable: true }] }],
        },
      ],
    });
    mocks.snapshot.mockResolvedValue({ text: "", lines: ["working", "Waiting for review"], cursorRow: 1, cursorCol: 0, cols: 80, rows: 2, altScreen: false, lastOutputMsAgo: 0 });
    await render();
    click(button("编辑 review"));
    await flush();
    const editor = mounted!.container.querySelector("[data-rule-editor]")!;
    setSelectValue(editor.querySelector<HTMLSelectElement>('select[aria-label="选择要测试的终端"]')!, "t1");
    clickButton(editor, "读取屏幕并测试");
    await flush();
    expect(mocks.snapshot).toHaveBeenCalledWith("backend-1");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(RULE_VALIDATE_DELAY_MS);
    });
    await flush();
    expect(mocks.validate).toHaveBeenLastCalledWith(expect.objectContaining({ id: "R1", pattern: "review" }), expect.objectContaining({ cursorRow: 1 }));
    expect(editor.querySelector("[data-rule-test]")?.getAttribute("data-rule-test")).toBe("match");
  });
});
