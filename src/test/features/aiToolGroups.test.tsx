/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, mount, type MountedView } from "./reactTestUtils";
import type { ChatItem, ToolItem } from "../../features/ai/conversation";
import { groupToolRuns, toolGroupSummary, toolGroupFailedText } from "../../features/ai/toolGroups";
import { AiChatMessageStream } from "../../features/ai/panel/AiChatMessageStream";
import type { AiChatCtx } from "../../features/ai/panel/aiChatCtx";

let seq = 0;

function tool(name: string, display: string, partial: Partial<ToolItem> = {}): ToolItem {
  seq++;
  return {
    id: `tool-${seq}`,
    attempt: null,
    role: "tool",
    callId: `call-${seq}`,
    name,
    display,
    summary: `${display} done`,
    text: `${display} output`,
    ok: true,
    exitCode: 0,
    ...partial,
  };
}

function other(role: "assistant" | "reasoning" | "user", text: string): ChatItem {
  seq++;
  return { id: `${role}-${seq}`, attempt: null, role, text } as ChatItem;
}

function diff(path: string): ChatItem {
  seq++;
  return { id: `diff-${seq}`, attempt: null, role: "diff", path, before: "a", after: "b" };
}

let view: MountedView | undefined;

afterEach(() => {
  view?.unmount();
  view = undefined;
});

function renderStream(items: ChatItem[], extra: Partial<AiChatCtx> = {}): HTMLElement {
  const ctx = {
    setScrollEl: () => {},
    messageWindowStart: 0,
    loadOlderMessages: () => {},
    conv: { items, todos: [], attempts: [] },
    conversationError: null,
    conversationIdRef: { current: "conv" },
    restoreConversation: async () => {},
    virtual: {
      range: { active: false, start: 0, end: items.length, padTop: 0, padBottom: 0 },
      measureRef: () => {},
    },
    activeMatchIndex: -1,
    busy: false,
    handleApprovePlan: vi.fn(),
    handleRetryRun: vi.fn(),
    handleEditResend: vi.fn(),
    agentToolCallFor: () => null,
    submitAgentApproval: vi.fn(),
    submittingCardId: null,
    confirmCard: null,
    confirmCardRef: { current: null },
    confirm: vi.fn(),
    productionNames: null,
    handleProductionState: vi.fn(),
    productionBlocked: false,
    questionCard: null,
    answer: vi.fn(),
    questionInput: "",
    setQuestionInput: vi.fn(),
    follow: { newOutput: false, jumpToLatest: vi.fn() },
    setModelPanelOpen: vi.fn(),
    ...extra,
  } as unknown as AiChatCtx;
  view = mount(createElement(AiChatMessageStream, { ctx }));
  return view.container;
}

function groupToggles(container: HTMLElement): HTMLButtonElement[] {
  return [...container.querySelectorAll<HTMLButtonElement>("button[data-tool-group]")];
}

describe("tool call groups", () => {
  it("groups runs of two or more calls, folding reasoning between calls only", () => {
    const items: ChatItem[] = [
      other("user", "检查服务"),
      tool("exec_commands", "ps"),
      other("reasoning", "想一想"),
      tool("read_file", "/etc/hosts"),
      tool("read_file", "/etc/fstab"),
      other("reasoning", "之后的思考"),
      other("assistant", "中间回答"),
      tool("search_files", "nginx"),
      other("assistant", "单个调用不分组"),
      tool("edit_file", "a.conf"),
      diff("a.conf"),
      tool("edit_file", "b.conf"),
    ];
    const groups = groupToolRuns(items);
    expect(groups).toHaveLength(1);
    expect(groups[0]).toMatchObject({ start: 1, end: 5, calls: 3, failed: 0, key: items[1].id });
    expect(toolGroupSummary(groups[0])).toBe("执行了 1 次命令，读取了 2 个文件");
  });

  it("counts file finds, content searches and the deprecated search_files as searches", () => {
    const items = [tool("find_files", 'find_files "*.go"'), tool("grep_files", 'grep_files "TODO"'), tool("search_files", "search_files")];
    const [group] = groupToolRuns(items);
    expect(toolGroupSummary(group)).toBe("搜索了 3 次");
  });

  it("names every category and counts unknown tools and failures", () => {
    const items = [
      tool("exec_commands", "ls"),
      tool("docker_exec", "ls"),
      tool("read_file", "a"),
      tool("list_dir", "/"),
      tool("write_file", "b"),
      tool("edit_file", "c"),
      tool("search_files", "x"),
      tool("read_screen", "tab"),
      tool("send_keys", "q"),
      tool("wait_for", "ready"),
      tool("db_query", "select 1"),
      tool("mystery_tool", "?"),
      tool("terminal_list", "", { ok: false, summary: "boom" }),
    ];
    const [group] = groupToolRuns(items);
    expect(toolGroupSummary(group)).toBe(
      "执行了 2 次命令，读取了 1 个文件，查看了 1 个目录，修改了 2 个文件，搜索了 1 次，读取了 1 次屏幕，发送了 1 次按键，等待了 1 次输出，查询了 1 次数据库，调用了 2 个工具",
    );
    expect(toolGroupFailedText(group)).toBe("1 个失败");
  });

  it("folds finished calls behind the summary and expands to the unchanged bubbles", () => {
    const container = renderStream([
      other("user", "看看"),
      tool("exec_commands", "uptime"),
      tool("read_file", "/etc/hosts"),
      tool("read_file", "/etc/fstab"),
      other("assistant", "完成"),
    ]);
    const [toggle] = groupToggles(container);
    expect(toggle.textContent).toContain("执行了 1 次命令，读取了 2 个文件");
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(container.textContent).not.toContain("/etc/hosts");
    expect(container.textContent).toContain("完成");

    click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    for (const text of ["uptime", "/etc/hosts", "/etc/fstab"]) expect(container.textContent).toContain(text);
    expect(container.querySelectorAll('[aria-busy="false"]')).toHaveLength(3);

    click(toggle);
    expect(container.textContent).not.toContain("/etc/hosts");
  });

  it("keeps running, failed and confirmation-pending calls visible while folded", () => {
    const pendingCall = tool("write_file", "pending.conf");
    const container = renderStream([
      tool("read_file", "folded.txt"),
      tool("exec_commands", "failing-cmd", { ok: false, exitCode: 1, summary: "exit 1", text: "exit 1" }),
      tool("read_screen", "running-tab", { summary: undefined, text: undefined, ok: undefined }),
      pendingCall,
      {
        id: "confirm-1",
        attempt: null,
        role: "confirm",
        jobId: "job",
        callId: pendingCall.callId,
        tool: "write_file",
        rendered: "pending.conf",
        nonce: "n",
      },
    ]);
    const [toggle] = groupToggles(container);
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(toggle.querySelector(".nx-badge-red")?.textContent).toBe("1 个失败");
    expect(container.textContent).not.toContain("folded.txt");
    expect(container.textContent).toContain("failing-cmd");
    expect(container.textContent).toContain("工具执行失败");
    expect(container.textContent).toContain("running-tab");
    expect(container.textContent).toContain("工具执行中");
    expect(container.textContent).toContain("pending.conf");
  });

  it("keeps the current search match visible inside a folded group", () => {
    const container = renderStream(
      [tool("read_file", "first.txt"), tool("read_file", "match.txt"), tool("read_file", "third.txt")],
      { activeMatchIndex: 1 },
    );
    expect(groupToggles(container)).toHaveLength(1);
    expect(container.textContent).toContain("match.txt");
    expect(container.textContent).not.toContain("first.txt");
  });

  it("leaves a single call unfolded", () => {
    const container = renderStream([other("user", "看"), tool("read_file", "only.txt"), other("assistant", "好")]);
    expect(groupToggles(container)).toHaveLength(0);
    expect(container.textContent).toContain("only.txt");
  });
});
