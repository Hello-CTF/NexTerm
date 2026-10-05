import { describe, expect, it } from "vitest";
import type { ChatItem } from "../features/ai/conversation";
import { findItemMatches, stepMatch } from "../features/ai/conversationSearch";

const user = (text: string): ChatItem => ({ id: "u1", attempt: null, role: "user", text });
const tool = (name: string, summary: string): ChatItem => ({
  id: "t1",
  attempt: null,
  role: "tool",
  callId: "c1",
  name,
  display: name,
  summary,
});
const diff = (path: string): ChatItem => ({ id: "d1", attempt: null, role: "diff", path, before: "", after: "" });
const outcome = (text: string): ChatItem => ({
  id: "o1",
  attempt: 1,
  role: "outcome",
  outcome: "error",
  text,
});

describe("conversation search", () => {
  it("matches user, tool, diff and outcome content case-insensitively", () => {
    const items = [
      user("NGINX 502 排查"),
      tool("exec_commands", "systemctl status nginx"),
      diff("/etc/NGINX/nginx.conf"),
      outcome("本轮出错：超时"),
    ];
    expect(findItemMatches(items, "nginx")).toEqual([0, 1, 2]);
    expect(findItemMatches(items, "超时")).toEqual([3]);
    expect(findItemMatches(items, "  ")).toEqual([]);
    expect(findItemMatches(items, "不存在的关键词")).toEqual([]);
  });

  it("searches tool subagent timelines, confirm cards and questions", () => {
    const withSubagent: ChatItem = {
      id: "t1",
      attempt: null,
      role: "tool",
      callId: "c1",
      name: "dispatch_subagent",
      display: "dispatch_subagent",
      summary: "子代理完成",
      subagent: {
        subagentId: "s1",
        depth: 1,
        status: "completed",
        text: "子代理排查了磁盘占用",
        tools: [],
      },
    };
    const confirm: ChatItem = {
      id: "c1",
      attempt: 1,
      role: "confirm",
      jobId: "j1",
      callId: "c1",
      tool: "write_file",
      rendered: "/etc/hosts",
      nonce: "n1",
    };
    const question: ChatItem = {
      id: "q1",
      attempt: 1,
      role: "question",
      jobId: "j1",
      callId: "c1",
      nonce: "n1",
      question: "继续吗",
      options: ["继续", "停止"],
    };
    expect(findItemMatches([withSubagent], "磁盘")).toEqual([0]);
    expect(findItemMatches([confirm, question], "hosts")).toEqual([0]);
    expect(findItemMatches([confirm, question], "停止")).toEqual([1]);
  });

  it("steps the match cursor with wraparound in both directions", () => {
    expect(stepMatch(-1, 3, 1)).toBe(0);
    expect(stepMatch(-1, 3, -1)).toBe(2);
    expect(stepMatch(0, 3, 1)).toBe(1);
    expect(stepMatch(2, 3, 1)).toBe(0);
    expect(stepMatch(0, 3, -1)).toBe(2);
    expect(stepMatch(1, 0, 1)).toBe(-1);
  });
});
