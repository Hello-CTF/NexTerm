/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { clickButton, mount, type MountedView } from "./features/reactTestUtils";

import { AgentToolCard } from "../features/ai/AgentToolCard";
import type { AgentApprovalDecision, AgentToolCall } from "../features/ai/agentTypes";

function toolCall(partial: Partial<AgentToolCall> = {}): AgentToolCall {
  return {
    callId: "call-1",
    tool: "terminal_send",
    handle: { handle: "build", host: "web-01", cwd: "/srv/app" },
    ownerName: "alice",
    isMine: true,
    production: false,
    persistent: true,
    presence: "attached",
    state: "ok",
    exitCode: 0,
    summary: "已发送 3 行",
    output: "已发送 3 行\n完整输出……",
    run: null,
    approval: null,
    ...partial,
  };
}

let view: MountedView | null = null;

function mountCard(
  call: AgentToolCall,
  onApproval?: (requestId: string, decision: AgentApprovalDecision) => void,
) {
  view = mount(createElement(AgentToolCard, { call, onApproval }));
  return view;
}

function buttons(): string[] {
  return [...view!.container.querySelectorAll("button")].map((b) => b.textContent?.trim() ?? "");
}

function button(text: string): HTMLButtonElement {
  const b = [...view!.container.querySelectorAll("button")].find(
    (candidate) => candidate.textContent?.trim() === text,
  );
  if (!b) throw new Error(`button not found: ${text}`);
  return b as HTMLButtonElement;
}

describe("AgentToolCard", () => {
  afterEach(() => {
    view?.unmount();
    view = null;
  });

  it("renders the tool name and the handle@host:cwd target", () => {
    mountCard(toolCall());
    const text = view!.container.textContent ?? "";
    expect(text).toContain("terminal_send");
    expect(text).toContain("build@web-01:/srv/app");
  });

  it("renders an idle run as 空闲 and never as 已完成", () => {
    mountCard(toolCall({ run: { state: "idle", evidence: null } }));
    const text = view!.container.textContent ?? "";
    expect(text).toContain("空闲");
    expect(text).not.toContain("已完成");
  });

  it("renders done without evidence as unknown instead of completed", () => {
    mountCard(toolCall({ run: { state: "done", evidence: null } }));
    const text = view!.container.textContent ?? "";
    expect(text).toContain("未知");
    expect(text).not.toContain("已完成");
  });

  it("renders done with evidence as 已完成 and shows the evidence line", () => {
    mountCard(
      toolCall({
        run: {
          state: "done",
          evidence: { kind: "output", observedAt: 1_760_000_000_000, summary: "退出码 0，输出锚点匹配" },
        },
      }),
    );
    const text = view!.container.textContent ?? "";
    expect(text).toContain("已完成");
    expect(text).toContain("依据：退出码 0，输出锚点匹配");
  });

  it("pending approval offers approve-once, approve-session and deny actions bound to the request id", () => {
    const onApproval = vi.fn();
    mountCard(
      toolCall({
        approval: { requestId: "req-9", nonce: "n-1", attempt: 2, decision: null, sessionScope: "服务与系统设置变更" },
      }),
      onApproval,
    );
    const text = view!.container.textContent ?? "";
    expect(text).toContain("等待你确认");
    expect(view!.container.querySelector('[title^="请求 req-9"]')).not.toBeNull();

    clickButton(view!.container, "允许一次");
    expect(onApproval).toHaveBeenCalledWith("req-9", "approve_once");
    clickButton(view!.container, "本会话允许");
    expect(onApproval).toHaveBeenCalledWith("req-9", "approve_session");
    clickButton(view!.container, "拒绝");
    expect(onApproval).toHaveBeenCalledWith("req-9", "deny");
  });

  it("resolved approval renders the decision and no action buttons", () => {
    mountCard(
      toolCall({
        approval: { requestId: "req-9", nonce: "n-1", attempt: 2, decision: "approve_once" },
      }),
    );
    const text = view!.container.textContent ?? "";
    expect(text).toContain("已允许一次");
    expect(buttons()).not.toContain("允许一次");
    expect(buttons()).not.toContain("拒绝");
  });

  it("other-owner calls show the owner badge and disable approval actions", () => {
    mountCard(
      toolCall({
        isMine: false,
        ownerName: "bob",
        approval: { requestId: "req-9", nonce: "n-1", attempt: 1, decision: null, sessionScope: "服务与系统设置变更" },
      }),
    );
    const text = view!.container.textContent ?? "";
    expect(text).toContain("所有者：bob");
    expect(button("允许一次").disabled).toBe(true);
    expect(button("本会话允许").disabled).toBe(true);
    expect(button("拒绝").disabled).toBe(true);
  });

  it("production targets keep the production guard visible", () => {
    mountCard(toolCall({ production: true }));
    expect(view!.container.textContent).toContain("生产环境");
  });

  it("unverifiable handles surface the ban instead of collapsing into gone", () => {
    mountCard(toolCall({ presence: "unverifiable" }));
    const text = view!.container.textContent ?? "";
    expect(text).toContain("无法确认");
    expect(text).not.toContain("已不存在");
    expect(text).toContain("自动写入已禁止");
  });

  it("gone handles without evidence render as unknown with the automatic-write ban", () => {
    mountCard(toolCall({ presence: "gone" }));
    const text = view!.container.textContent ?? "";
    expect(text).toContain("未知");
    expect(text).not.toContain("已不存在");
    expect(text).toContain("自动写入已禁止");
  });

  it("unknown tool state is announced instead of being presented as success or failure", () => {
    mountCard(toolCall({ state: "unknown", exitCode: null }));
    const text = view!.container.textContent ?? "";
    expect(text).toContain("工具状态未知");
    expect(text).not.toContain("工具执行成功");
    expect(text).not.toContain("工具执行失败");
  });

  it("long output expands and collapses", () => {
    mountCard(toolCall());
    const expand = button("展开完整输出（14 字）");
    clickButton(view!.container, "展开完整输出（14 字）");
    expect(expand.getAttribute("aria-expanded")).toBe("true");
    expect(view!.container.textContent).toContain("完整输出……");
    clickButton(view!.container, "收起");
    expect(view!.container.textContent).toContain("展开完整输出");
  });
});
