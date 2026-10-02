// 确定性假事件流：聚合 + 合并 + 重放 + 终态持久性的全分支测试。
// 不碰 DOM / IPC / 真定时器 —— 帧调度用手动帧，事件顺序由测试逐条驱动。
import { describe, expect, it } from "vitest";
import {
  applyAiEvent,
  appendUserMessage,
  beginRun,
  createConversation,
  historyToItems,
  pendingInteraction,
  resetConversation,
  resolveInteraction,
  type ChatItem,
  type ConversationState,
  type ToolItem,
} from "../../features/ai/conversation";
import {
  createConversationStream,
  MAX_PENDING_CHARS,
} from "../../features/ai/conversationStream";

function manualFrames() {
  let frames: (() => void)[] = [];
  return {
    scheduler: (cb: () => void) => {
      frames.push(cb);
      return () => {
        frames = frames.filter((f) => f !== cb);
      };
    },
    runFrame() {
      const cbs = frames;
      frames = [];
      for (const cb of cbs) cb();
    },
    scheduled: () => frames.length,
  };
}

function stream() {
  const frames = manualFrames();
  const s = createConversationStream(frames.scheduler);
  return { s, frames };
}

function texts(items: ChatItem[], role: string): string[] {
  return items.filter((i) => i.role === role).map((i) => (i as { text?: string }).text ?? "");
}

function roles(items: ChatItem[]): string[] {
  return items.map((i) => i.role);
}

describe("conversation aggregation: chunking and ordering", () => {
  it("merges consecutive deltas into one bubble, byte-exact, and splits on role change", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    const chunks = ["你好", "，世", "界", "\n第二行"];
    for (const c of chunks) s.pushEvent(1, { type: "delta", text: c });
    s.pushEvent(1, { type: "reasoning", text: "先想" });
    s.pushEvent(1, { type: "reasoning", text: "再想" });
    s.pushEvent(1, { type: "delta", text: "尾巴" });
    frames.runFrame();
    const items = s.getState().items;
    expect(roles(items)).toEqual(["assistant", "reasoning", "assistant"]);
    expect(texts(items, "assistant")).toEqual([chunks.join(""), "尾巴"]);
    expect(texts(items, "reasoning")).toEqual(["先想再想"]);
  });

  it("coalesces many deltas into a single scheduled frame and one publish per frame", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    let publishes = 0;
    s.subscribe(() => {
      publishes += 1;
    });
    for (let i = 0; i < 50; i++) s.pushEvent(1, { type: "delta", text: `t${i}` });
    expect(frames.scheduled()).toBe(1);
    expect(s.getState().items).toHaveLength(0);
    frames.runFrame();
    expect(publishes).toBe(1);
    expect(texts(s.getState().items, "assistant")).toEqual([
      Array.from({ length: 50 }, (_, i) => `t${i}`).join(""),
    ]);
    frames.runFrame();
    expect(publishes).toBe(1);
  });

  it("flushes pending text before tool, interaction and terminal boundaries", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "先" });
    s.pushEvent(1, { type: "toolCall", id: "call-1", name: "exec", display: "ls" });
    expect(roles(s.getState().items)).toEqual(["assistant", "tool"]);
    s.pushEvent(1, { type: "delta", text: "中" });
    s.pushEvent(1, {
      type: "confirmRequired",
      id: "call-2",
      tool: "exec",
      rendered: "$ rm x",
      confirmationNonce: "n2",
    });
    expect(roles(s.getState().items)).toEqual(["assistant", "tool", "assistant", "confirm"]);
    s.pushEvent(1, { type: "delta", text: "后" });
    s.pushEvent(1, { type: "done", answer: "后" });
    expect(frames.scheduled()).toBe(0);
    // 终态前最后一次 flush：「后」落在终态标记之前，而不是被丢掉或排到后面。
    expect(roles(s.getState().items)).toEqual([
      "assistant",
      "tool",
      "assistant",
      "confirm",
      "assistant",
      "outcome",
    ]);
    expect(s.getState().items.at(-2)).toMatchObject({ role: "assistant", text: "后" });
  });

  it("attaches results to the right tool call by id even with interleaved calls", () => {
    const { s } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "toolCall", id: "a", name: "exec", display: "A" });
    s.pushEvent(1, { type: "toolCall", id: "b", name: "exec", display: "B" });
    s.pushEvent(1, { type: "toolResult", id: "a", ok: true, summary: "a ok", text: "a full", exitCode: 0 });
    const items = s.getState().items;
    const a = items.find((i): i is ToolItem => i.role === "tool" && i.callId === "a");
    const b = items.find((i): i is ToolItem => i.role === "tool" && i.callId === "b");
    expect(a).toMatchObject({ summary: "a ok", text: "a full", ok: true, exitCode: 0 });
    expect(b?.summary).toBeUndefined();
  });

  it("keeps model phases, usage and todos as overwrite snapshots and clears status on output", () => {
    const { s } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "status", phase: "thinking", turn: 2 });
    expect(s.getState().status).toMatchObject({ phase: "thinking", turn: 2 });
    s.pushEvent(1, { type: "toolArgs", tool: "write_file", chars: 128 });
    expect(s.getState().status).toMatchObject({ phase: "tool_args", chars: 128 });
    s.pushEvent(1, { type: "toolCall", id: "a", name: "write_file", display: "w" });
    expect(s.getState().status).toBeNull();
    s.pushEvent(1, { type: "status", phase: "thinking" });
    s.pushEvent(1, { type: "delta", text: "x" });
    s.flush();
    expect(s.getState().status).toBeNull();
    s.pushEvent(1, { type: "usage", promptTokens: 10, completionTokens: 5, cachedTokens: 2, contextWindow: 100 });
    s.pushEvent(1, { type: "usage", promptTokens: 20, completionTokens: 6, cachedTokens: 0, contextWindow: 100 });
    expect(s.getState().usage).toMatchObject({ promptTokens: 20, completionTokens: 6 });
    s.pushEvent(1, { type: "todos", items: [{ content: "做", status: "in_progress" }] });
    expect(s.getState().todos).toHaveLength(1);
  });
});

describe("conversation aggregation: reconnect, replay and stale events", () => {
  it("dedupes replayed structured events by kernel id", () => {
    const { s } = stream();
    s.beginRun(1);
    const toolCall = { type: "toolCall", id: "a", name: "exec", display: "A" };
    const toolResult = { type: "toolResult", id: "a", ok: true, summary: "s", text: "full", exitCode: 0 };
    const fileChange = { type: "fileChange", id: "f", path: "/x", before: "1", after: "2" };
    const confirm = { type: "confirmRequired", id: "c", tool: "exec", rendered: "$ x", confirmationNonce: "n" };
    for (const ev of [toolCall, toolResult, fileChange, confirm]) {
      expect(s.pushEvent(1, ev).accepted).toBe(true);
      expect(s.pushEvent(1, ev).accepted).toBe(false);
    }
    const items = s.getState().items;
    expect(roles(items)).toEqual(["tool", "diff", "confirm"]);
    expect(items[0]).toMatchObject({ summary: "s", text: "full" });
  });

  it("done repairs duplicated stream text even when the replay was never signaled", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: " authoritative answer" });
    frames.runFrame();
    // 没有重连信号 ⇒ 入口不去重，同一段 delta 真的落了两遍；
    // 终态仍必须把末段修成权威答案，而不是再补一条重复气泡。
    s.pushEvent(1, { type: "delta", text: " authoritative answer" });
    frames.runFrame();
    s.pushEvent(1, { type: "done", answer: " authoritative answer" });
    const items = s.getState().items;
    expect(texts(items, "assistant")).toEqual([" authoritative answer"]);
    expect(roles(items)).toEqual(["assistant", "outcome"]);
  });

  it("done replaces only the final segment and keeps earlier narration and tool cards", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "先分析一下" });
    frames.runFrame();
    s.pushEvent(1, { type: "toolCall", id: "t", name: "exec", display: "ls" });
    s.pushEvent(1, { type: "toolResult", id: "t", ok: true, summary: "s", text: "full", exitCode: 0 });
    s.pushEvent(1, { type: "delta", text: "最终" });
    frames.runFrame();
    s.pushEvent(1, { type: "done", answer: "最终答案" });
    const items = s.getState().items;
    expect(roles(items)).toEqual(["assistant", "tool", "assistant", "outcome"]);
    expect(texts(items, "assistant")).toEqual(["先分析一下", "最终答案"]);
  });

  it("keeps the first terminal outcome under duplicate done and late error", () => {
    const { s } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "答案" });
    expect(s.pushEvent(1, { type: "done", answer: "答案" }).accepted).toBe(true);
    const settled = s.getState();
    expect(s.pushEvent(1, { type: "done", answer: "答案" }).accepted).toBe(false);
    const lateError = s.pushEvent(1, { type: "error", message: "迟到" });
    expect(lateError.accepted).toBe(false);
    expect(lateError.terminal).toBe("error");
    expect(s.pushEvent(1, { type: "delta", text: "尾巴" }).accepted).toBe(false);
    s.flush();
    expect(s.getState()).toBe(settled);
    expect(roles(settled.items)).toEqual(["assistant", "outcome"]);
    expect(settled.attempts[0].outcome).toBe("done");
  });

  it("isolates stale generations: unknown or previous runs cannot mutate the conversation", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "一" });
    frames.runFrame();
    s.pushEvent(1, { type: "done", answer: "一" });
    s.beginRun(2);
    s.appendUser(2, "第二问");
    const before = s.getState();
    for (const ev of [
      { type: "delta", text: "stale" },
      { type: "toolCall", id: "x", name: "e", display: "e" },
      { type: "done", answer: "stale" },
      { type: "error", message: "stale" },
      { type: "usage", promptTokens: 999 },
    ]) {
      expect(s.pushEvent(1, ev).accepted).toBe(false);
      expect(s.pushEvent(99, ev).accepted).toBe(false);
    }
    s.flush();
    expect(s.getState()).toBe(before);
    // 新代的事件照常工作。
    expect(s.pushEvent(2, { type: "delta", text: "二" }).accepted).toBe(true);
  });

  it("never merges text across attempts or across a terminal boundary", () => {
    let state = createConversation();
    state = beginRun(state, 1);
    state = appendUserMessage(state, 1, "问");
    state = applyAiEvent(state, 1, { type: "delta", text: "答一" }).state;
    state = applyAiEvent(state, 1, { type: "done", answer: "答一" }).state;
    state = beginRun(state, 2);
    state = applyAiEvent(state, 2, { type: "delta", text: "答二" }).state;
    expect(texts(state.items, "assistant")).toEqual(["答一", "答二"]);
  });
});

describe("reopen-driven replay suppression (delta / reasoning)", () => {
  it("suppresses the replayed chunk, accepts the diverging tail, and settles on one answer", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "abcdefghij" });
    frames.runFrame();
    s.notifyReopen(1);
    // 重连补帧：同一段再来一遍 ⇒ 丢弃；跟不上的下一帧 ⇒ 新内容，窗口关闭。
    s.pushEvent(1, { type: "delta", text: "abcdefghij" });
    s.pushEvent(1, { type: "delta", text: " XYZ" });
    frames.runFrame();
    expect(texts(s.getState().items, "assistant")).toEqual(["abcdefghij XYZ"]);
    s.pushEvent(1, { type: "done", answer: "abcdefghij XYZ" });
    const items = s.getState().items;
    expect(roles(items)).toEqual(["assistant", "outcome"]);
    expect(texts(items, "assistant")).toEqual(["abcdefghij XYZ"]);
  });

  it("aligns a partial suffix replay across several chunks", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    for (const text of ["a1", "b2", "c3", "d4"]) s.pushEvent(1, { type: "delta", text });
    frames.runFrame();
    s.notifyReopen(1);
    // 只补发后两段：c3 先在 log 里对齐成功，d4 连续对齐，e5 是新内容。
    for (const text of ["c3", "d4", "e5"]) s.pushEvent(1, { type: "delta", text });
    frames.runFrame();
    expect(texts(s.getState().items, "assistant")).toEqual(["a1b2c3d4e5"]);
  });

  it("handles interleaved replay with reasoning and id-bearing tool events", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    const script = () => {
      s.pushEvent(1, { type: "delta", text: "A" });
      s.pushEvent(1, { type: "reasoning", text: "R" });
      s.pushEvent(1, { type: "toolCall", id: "t1", name: "exec", display: "ls" });
      s.pushEvent(1, { type: "toolResult", id: "t1", ok: true, summary: "s", text: "full", exitCode: 0 });
      s.pushEvent(1, { type: "delta", text: "B" });
    };
    script();
    frames.runFrame();
    s.notifyReopen(1);
    script(); // 整段重放：文本帧被对齐丢弃，工具帧由 id 幂等
    s.pushEvent(1, { type: "delta", text: "C" });
    frames.runFrame();
    const items = s.getState().items;
    expect(roles(items)).toEqual(["assistant", "reasoning", "tool", "assistant"]);
    expect(texts(items, "assistant")).toEqual(["A", "BC"]);
    expect(texts(items, "reasoning")).toEqual(["R"]);
    s.pushEvent(1, { type: "done", answer: "BC" });
    expect(roles(s.getState().items)).toEqual(["assistant", "reasoning", "tool", "assistant", "outcome"]);
  });

  it("suppresses reasoning replay and keeps the surviving reasoning exact", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "reasoning", text: "same reasoning chunk" });
    frames.runFrame();
    s.notifyReopen(1);
    s.pushEvent(1, { type: "reasoning", text: "same reasoning chunk" });
    s.pushEvent(1, { type: "reasoning", text: " and more" });
    frames.runFrame();
    expect(texts(s.getState().items, "reasoning")).toEqual(["same reasoning chunk and more"]);
    // done 不带推理内容：推理的修复必须发生在入口，而不是终态。
    s.pushEvent(1, { type: "done", answer: "答案" });
    const items = s.getState().items;
    expect(texts(items, "reasoning")).toEqual(["same reasoning chunk and more"]);
    expect(texts(items, "assistant")).toEqual(["答案"]);
  });

  it("suppresses replay even before the original fragments were flushed", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "x" });
    // 不跑帧：原片段还在 pending，但原始日志已记录，重放依然被识别。
    s.notifyReopen(1);
    s.pushEvent(1, { type: "delta", text: "x" });
    s.pushEvent(1, { type: "delta", text: "y" });
    frames.runFrame();
    expect(texts(s.getState().items, "assistant")).toEqual(["xy"]);
  });

  it("never dedupes without a reopen signal, and accepts repeats after the window closes", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "ha" });
    s.pushEvent(1, { type: "delta", text: "ha" });
    frames.runFrame();
    expect(texts(s.getState().items, "assistant")).toEqual(["haha"]);
    // 窗口被首个对不上的片段关闭后，相同内容照常落账。
    s.notifyReopen(1);
    s.pushEvent(1, { type: "delta", text: "!" });
    s.pushEvent(1, { type: "delta", text: "ha" });
    frames.runFrame();
    expect(texts(s.getState().items, "assistant")).toEqual(["haha!ha"]);
  });

  it("a replayed done still settles exactly once after a replayed text prefix", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "答" });
    frames.runFrame();
    s.notifyReopen(1);
    s.pushEvent(1, { type: "delta", text: "答" });
    expect(s.pushEvent(1, { type: "done", answer: "答" }).accepted).toBe(true);
    expect(s.pushEvent(1, { type: "done", answer: "答" }).accepted).toBe(false);
    const items = s.getState().items;
    expect(roles(items)).toEqual(["assistant", "outcome"]);
    expect(texts(items, "assistant")).toEqual(["答"]);
    // 终态后重连信号与迟到文本一律无效。
    s.notifyReopen(1);
    expect(s.pushEvent(1, { type: "delta", text: "答" }).accepted).toBe(false);
  });
});

describe("conversation terminal reconciliation", () => {
  function doneWith(streamed: string | null, answer: string, plan = false) {
    const { s, frames } = stream();
    s.beginRun(1);
    if (plan) s.pushEvent(1, { type: "planSubmitted", plan: "p" });
    if (streamed !== null) {
      s.pushEvent(1, { type: "delta", text: streamed });
      frames.runFrame();
    }
    s.pushEvent(1, { type: "done", answer });
    return s.getState();
  }

  it("exact match keeps one bubble; non-streamed fallback appends the answer once", () => {
    expect(roles(doneWith("完整答案", "完整答案").items)).toEqual(["assistant", "outcome"]);
    const fallback = doneWith(null, "完整答案");
    expect(roles(fallback.items)).toEqual(["assistant", "outcome"]);
    expect(texts(fallback.items, "assistant")).toEqual(["完整答案"]);
  });

  it("the authoritative answer replaces the final segment: prefix, short prefix, divergence", () => {
    const long = doneWith("0123456789", "0123456789 后续");
    expect(texts(long.items, "assistant")).toEqual(["0123456789 后续"]);
    expect(roles(long.items)).toEqual(["assistant", "outcome"]);
    // 短前缀 / 完全分叉也一样：answer 就是最终消息，流式末段只是它的过渡形态。
    const short = doneWith("好", "好，完整回答");
    expect(texts(short.items, "assistant")).toEqual(["好，完整回答"]);
    expect(roles(short.items)).toEqual(["assistant", "outcome"]);
    const diverged = doneWith("与答案完全不同的流式", "权威答案");
    expect(texts(diverged.items, "assistant")).toEqual(["权威答案"]);
    expect(roles(diverged.items)).toEqual(["assistant", "outcome"]);
  });

  it("empty raw answer does not add a placeholder after streamed text, but does with no output", () => {
    const streamed = doneWith("已经流出的正文", "");
    expect(texts(streamed.items, "assistant")).toEqual(["已经流出的正文"]);
    expect(roles(streamed.items)).toEqual(["assistant", "outcome"]);
    const empty = doneWith(null, "");
    expect(texts(empty.items, "assistant")).toEqual(["(无回答)"]);
  });

  it("planSubmitted upgrades the closing bubble to a plan exactly once", () => {
    const state = doneWith("方案正文", "方案正文", true);
    expect(roles(state.items)).toEqual(["plan", "outcome"]);
    expect(texts(state.items, "plan")).toEqual(["方案正文"]);
  });

  it("error produces a persistent outcome row and wins over later events", () => {
    const { s } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "半截" });
    const result = s.pushEvent(1, { type: "error", message: "网络断开", retryable: true });
    expect(result.terminal).toBe("error");
    const items = s.getState().items;
    expect(roles(items)).toEqual(["assistant", "outcome"]);
    expect(items.at(-1)).toMatchObject({ outcome: "error", text: "网络断开" });
    expect(s.pushEvent(1, { type: "done", answer: "半截" }).accepted).toBe(false);
    expect(s.getState().attempts[0].outcome).toBe("error");
  });

  it("cancel flushes pending text, records canceled once and survives a late done", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "已流出的部分" });
    s.cancelRun(1, true);
    expect(frames.scheduled()).toBe(0);
    const items = s.getState().items;
    expect(roles(items)).toEqual(["assistant", "outcome"]);
    expect(items[0]).toMatchObject({ text: "已流出的部分" });
    expect(items[1]).toMatchObject({ outcome: "canceled" });
    s.cancelRun(1, true);
    expect(roles(s.getState().items)).toEqual(["assistant", "outcome"]);
    expect(s.pushEvent(1, { type: "done", answer: "已流出的部分" }).accepted).toBe(false);
    expect(s.getState().attempts[0].outcome).toBe("canceled");
  });

  it("takeover done appends the closing narration; cancel waits for the real terminal", () => {
    const { s } = stream();
    s.beginRun(7, "takeover");
    s.pushEvent(7, { type: "screen", text: "screen-1" });
    s.pushEvent(7, { type: "delta", text: "观察" });
    s.pushEvent(7, { type: "screen", text: "screen-2" });
    s.cancelRun(7, false);
    expect(s.getState().attempts[0].outcome).toBeNull();
    const done = s.pushEvent(7, { type: "done", answer: "装好了" });
    expect(done.accepted).toBe(true);
    const items = s.getState().items;
    // 读屏卡始终只有最新一屏。
    expect(items.filter((i) => i.role === "tool" && i.name === "read_screen")).toHaveLength(1);
    expect(texts(items, "assistant")).toEqual(["观察", "接管结束：装好了"]);
    expect(items.at(-1)).toMatchObject({ role: "outcome", outcome: "done" });
  });
});

describe("HITL interaction lifecycle", () => {
  function confirmEvent(id: string, nonce: string) {
    return { type: "confirmRequired", id, tool: "exec", rendered: `$ ${id}`, confirmationNonce: nonce };
  }

  it("resolves only the exact card; a question arriving mid-resolution stays pending", () => {
    const { s } = stream();
    s.beginRun(1);
    s.pushEvent(1, confirmEvent("a", "na"));
    const confirm = pendingInteraction(s.getState(), 1, "confirm");
    expect(confirm).not.toBeNull();
    s.pushEvent(1, {
      type: "questionRequired",
      id: "b",
      question: { question: "继续吗", options: ["是"] },
      confirmationNonce: "nb",
    });
    s.resolveInteraction(1, confirm!.id, "na", "已允许一次");
    expect(pendingInteraction(s.getState(), 1, "confirm")).toBeNull();
    const question = pendingInteraction(s.getState(), 1, "question");
    expect(question).toMatchObject({ callId: "b" });
    expect(question?.resolution).toBeUndefined();
    // 重复结算 / 错 nonce / 错代都是空操作。
    const before = s.getState();
    s.resolveInteraction(1, confirm!.id, "na", "已拒绝");
    s.resolveInteraction(1, question!.id, "wrong", "已回答：x");
    s.resolveInteraction(2, question!.id, "nb", "已回答：x");
    expect(s.getState()).toBe(before);
    s.resolveInteraction(1, question!.id, "nb", "已回答：是");
    expect(pendingInteraction(s.getState(), 1, "question")).toBeNull();
    expect(s.getState().items.find((i) => i.id === question!.id)).toMatchObject({
      resolution: "已回答：是",
    });
  });

  it("terminal and cancel close pending cards; replayed confirm does not reopen them", () => {
    const { s } = stream();
    s.beginRun(1);
    const ev = confirmEvent("a", "na");
    s.pushEvent(1, ev);
    s.pushEvent(1, { type: "done", answer: "好" });
    expect(pendingInteraction(s.getState(), 1, "confirm")).toBeNull();
    expect(s.getState().items.find((i) => i.role === "confirm")?.resolution).toContain("已结束");
    expect(s.pushEvent(1, ev).accepted).toBe(false);
    expect(pendingInteraction(s.getState(), 1, "confirm")).toBeNull();

    s.beginRun(2);
    s.pushEvent(2, confirmEvent("b", "nb"));
    s.cancelRun(2, true);
    expect(pendingInteraction(s.getState(), 2, "confirm")).toBeNull();
  });

  it("bindJob backfills jobId on cards created before the RPC returned", () => {
    const { s } = stream();
    s.beginRun(1);
    s.pushEvent(1, confirmEvent("a", "na"));
    expect(pendingInteraction(s.getState(), 1, "confirm")?.jobId).toBe("");
    s.bindJob(1, "job-1");
    expect(pendingInteraction(s.getState(), 1, "confirm")?.jobId).toBe("job-1");
  });

  it("pure reducer mirrors clearInteractionIfMatch identity semantics", () => {
    let state = createConversation();
    state = beginRun(state, 1);
    state = applyAiEvent(state, 1, confirmEvent("a", "na")).state;
    const card = pendingInteraction(state, 1, "confirm")!;
    const otherNonce = resolveInteraction(state, 1, card.id, "other", "x");
    expect(otherNonce).toBe(state);
    const otherItem = resolveInteraction(state, 1, "g1:confirm:nope", "na", "x");
    expect(otherItem).toBe(state);
    expect(pendingInteraction(resolveInteraction(state, 1, card.id, "na", "已拒绝"), 1, "confirm")).toBeNull();
  });
});

describe("coalescing bounds and dispose", () => {
  it("forces a synchronous flush when pending text exceeds the bound", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "x".repeat(MAX_PENDING_CHARS) });
    expect(frames.scheduled()).toBe(1);
    s.pushEvent(1, { type: "delta", text: "y" });
    expect(frames.scheduled()).toBe(0);
    expect(texts(s.getState().items, "assistant")).toEqual(["x".repeat(MAX_PENDING_CHARS) + "y"]);
  });

  it("flushes on dispose, cancels the frame and rejects everything afterwards", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "收尾" });
    s.dispose();
    expect(frames.scheduled()).toBe(0);
    expect(texts(s.getState().items, "assistant")).toEqual(["收尾"]);
    expect(s.pushEvent(1, { type: "delta", text: "late" }).accepted).toBe(false);
    expect(s.pushEvent(1, { type: "done", answer: "收尾" }).accepted).toBe(false);
    expect(texts(s.getState().items, "assistant")).toEqual(["收尾"]);
    s.dispose();
  });

  it("handles a large stream byte-exactly with bounded renders", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    let publishes = 0;
    s.subscribe(() => {
      publishes += 1;
    });
    const chunks: string[] = [];
    for (let i = 0; i < 20000; i++) {
      const c = `chunk-${i}|`;
      chunks.push(c);
      s.pushEvent(1, { type: "delta", text: c });
      if (i % 100 === 99) frames.runFrame();
    }
    const expected = chunks.join("");
    s.pushEvent(1, { type: "done", answer: expected });
    const items = s.getState().items;
    expect(roles(items)).toEqual(["assistant", "outcome"]);
    expect(texts(items, "assistant")[0]).toBe(expected);
    expect(texts(items, "assistant")[0]).toHaveLength(expected.length);
    expect(publishes).toBeLessThanOrEqual(202);
  });

  it("a large multi-tool stream keeps cards and results aligned under replay", () => {
    const { s } = stream();
    s.beginRun(1);
    for (let i = 0; i < 300; i++) {
      const call = { type: "toolCall", id: `c${i}`, name: "exec", display: `cmd ${i}` };
      const result = { type: "toolResult", id: `c${i}`, ok: i % 2 === 0, summary: `s${i}`, text: `t${i}`, exitCode: i % 2 };
      s.pushEvent(1, call);
      s.pushEvent(1, result);
      // 任意位置的重放都不该改变结构。
      s.pushEvent(1, call);
      s.pushEvent(1, result);
    }
    const items = s.getState().items;
    expect(items).toHaveLength(300);
    for (let i = 0; i < 300; i++) {
      expect(items[i]).toMatchObject({ role: "tool", callId: `c${i}`, summary: `s${i}`, text: `t${i}` });
    }
  });
});

describe("history and reset", () => {
  it("maps persisted messages and cuts off every earlier generation", () => {
    const { s } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "旧" });
    s.flush();
    const history = historyToItems(s.getState(), [
      { role: "user", content: { content: "带图", imageCount: 2 } },
      { role: "assistant", content: "历史回答" },
      { role: "tool", content: "不入历史" },
      { role: "assistant", content: "" },
    ]);
    s.reset(history);
    const items = s.getState().items;
    expect(roles(items)).toEqual(["user", "assistant"]);
    expect(items[0]).toMatchObject({ text: "带图", imageCount: 2, attempt: null });
    expect(s.getState().attempts).toHaveLength(0);
    expect(s.pushEvent(1, { type: "delta", text: "stale" }).accepted).toBe(false);
    expect(s.getState().items).toBe(items);
    // 新会话从头开始，条目 id 不与历史冲突。
    s.beginRun(2);
    s.pushEvent(2, { type: "delta", text: "新" });
    s.flush();
    const ids = s.getState().items.map((i) => i.id);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("reset with no items yields an empty conversation but keeps usage/todos", () => {
    let state: ConversationState = createConversation();
    state = beginRun(state, 1);
    state = applyAiEvent(state, 1, { type: "usage", promptTokens: 3, contextWindow: 9 }).state;
    state = applyAiEvent(state, 1, { type: "todos", items: [{ content: "t", status: "pending" }] }).state;
    state = resetConversation(state);
    expect(state.items).toHaveLength(0);
    expect(state.usage).toMatchObject({ promptTokens: 3 });
    expect(state.todos).toHaveLength(1);
  });
});
