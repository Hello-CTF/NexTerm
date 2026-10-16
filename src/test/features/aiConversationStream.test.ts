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

function perRun(seq: number) {
  return { seq };
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
    s.pushEvent(1, { type: "delta", text: "Before" });
    s.pushEvent(1, { type: "toolCall", id: "call-1", name: "exec", display: "ls" });
    expect(roles(s.getState().items)).toEqual(["assistant", "tool"]);
    s.pushEvent(1, { type: "delta", text: "Middle" });
    s.pushEvent(1, {
      type: "confirmRequired",
      id: "call-2",
      tool: "exec",
      rendered: "$ rm x",
      confirmationNonce: "n2",
    });
    expect(roles(s.getState().items)).toEqual(["assistant", "tool", "assistant", "confirm"]);
    s.pushEvent(1, { type: "delta", text: "After" });
    s.pushEvent(1, { type: "done", answer: "After" });
    expect(frames.scheduled()).toBe(0);
    expect(roles(s.getState().items)).toEqual([
      "assistant",
      "tool",
      "assistant",
      "confirm",
      "assistant",
      "outcome",
    ]);
    expect(s.getState().items.at(-2)).toMatchObject({ role: "assistant", text: "After" });
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

  it("marks tool results that failed with a structured panic", () => {
    const { s } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "toolCall", id: "p", name: "list_assets", display: "list_assets" });
    s.pushEvent(1, { type: "toolCall", id: "q", name: "read_file", display: "read_file /a" });
    s.pushEvent(1, { type: "toolResult", id: "p", ok: false, summary: "Tool list_assets crashed: backend unavailable", text: "Tool list_assets crashed: backend unavailable", exitCode: 1, panic: true });
    s.pushEvent(1, { type: "toolResult", id: "q", ok: false, summary: "File not found", text: "File not found", exitCode: 1, panic: false });
    const items = s.getState().items;
    const crashed = items.find((i): i is ToolItem => i.role === "tool" && i.callId === "p");
    const failed = items.find((i): i is ToolItem => i.role === "tool" && i.callId === "q");
    expect(crashed).toMatchObject({ ok: false, panic: true });
    expect(failed).toMatchObject({ ok: false, panic: false });
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
    s.pushEvent(1, { type: "todos", items: [{ content: "Task", status: "in_progress" }] });
    expect(s.getState().todos).toHaveLength(1);
  });

  it("keeps main usage snapshots separate from deduplicated summary totals", () => {
    const { s } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "usage", channel: "main", callId: "main:agent-1", promptTokens: 100, completionTokens: 20, cachedTokens: 10, contextWindow: 1000 });
    const firstSummary = { type: "usage", channel: "summary", callId: "summary:agent-1", promptTokens: 30, completionTokens: 5, cachedTokens: 3, contextWindow: 1000 };
    s.pushEvent(1, firstSummary);
    s.pushEvent(1, { type: "usage", channel: "summary", callId: "summary:agent-2", promptTokens: 40, completionTokens: 7, cachedTokens: 4, contextWindow: 1000 });
    s.pushEvent(1, firstSummary);
    s.pushEvent(1, { type: "usage", channel: "tool-subagent", callId: "tool-subagent:agent-1", promptTokens: 999, completionTokens: 99, cachedTokens: 99, contextWindow: 9999 });
    expect(s.getState().usage).toMatchObject({
      promptTokens: 100,
      completionTokens: 20,
      cachedTokens: 10,
      contextWindow: 1000,
      summaryPromptTokens: 70,
      summaryCompletionTokens: 12,
    });
  });

  it("keeps unchanneled provider calls with a shared ID and different sequences", () => {
    const { s } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "usage", channel: "main", callId: "main:agent-1", promptTokens: 100, completionTokens: 20, cachedTokens: 10, contextWindow: 1000 });
    s.pushEvent(1, { type: "usage", callId: "summary:provider-shared", promptTokens: 30, completionTokens: 5, cachedTokens: 3, contextWindow: 1000, seq: 1 });
    s.pushEvent(1, { type: "usage", callId: "summary:provider-shared", promptTokens: 40, completionTokens: 7, cachedTokens: 4, contextWindow: 1000, seq: 2 });
    expect(s.getState().usage).toMatchObject({
      promptTokens: 100,
      completionTokens: 20,
      cachedTokens: 10,
      contextWindow: 1000,
      summaryPromptTokens: 70,
      summaryCompletionTokens: 12,
    });
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
    s.pushEvent(1, { type: "delta", text: "Analyze first" });
    frames.runFrame();
    s.pushEvent(1, { type: "toolCall", id: "t", name: "exec", display: "ls" });
    s.pushEvent(1, { type: "toolResult", id: "t", ok: true, summary: "s", text: "full", exitCode: 0 });
    s.pushEvent(1, { type: "delta", text: "Final" });
    frames.runFrame();
    s.pushEvent(1, { type: "done", answer: "Final answer" });
    const items = s.getState().items;
    expect(roles(items)).toEqual(["assistant", "tool", "assistant", "outcome"]);
    expect(texts(items, "assistant")).toEqual(["Analyze first", "Final answer"]);
  });

  it("keeps the first terminal outcome under duplicate done and late error", () => {
    const { s } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "Answer" });
    expect(s.pushEvent(1, { type: "done", answer: "Answer" }).accepted).toBe(true);
    const settled = s.getState();
    expect(s.pushEvent(1, { type: "done", answer: "Answer" }).accepted).toBe(false);
    const lateError = s.pushEvent(1, { type: "error", message: "Late" });
    expect(lateError.accepted).toBe(false);
    expect(lateError.terminal).toBe("error");
    expect(s.pushEvent(1, { type: "delta", text: "Tail" }).accepted).toBe(false);
    s.flush();
    expect(s.getState()).toBe(settled);
    expect(roles(settled.items)).toEqual(["assistant", "outcome"]);
    expect(settled.attempts[0].outcome).toBe("done");
  });

  it("isolates stale generations: unknown or previous runs cannot mutate the conversation", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "One" });
    frames.runFrame();
    s.pushEvent(1, { type: "done", answer: "One" });
    s.beginRun(2);
    s.appendUser(2, "Second question");
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
    expect(s.pushEvent(2, { type: "delta", text: "Two" }).accepted).toBe(true);
  });

  it("never merges text across attempts or across a terminal boundary", () => {
    let state = createConversation();
    state = beginRun(state, 1);
    state = appendUserMessage(state, 1, "Question");
    state = applyAiEvent(state, 1, { type: "delta", text: "Answer one" }).state;
    state = applyAiEvent(state, 1, { type: "done", answer: "Answer one" }).state;
    state = beginRun(state, 2);
    state = applyAiEvent(state, 2, { type: "delta", text: "Answer two" }).state;
    expect(texts(state.items, "assistant")).toEqual(["Answer one", "Answer two"]);
  });
});

describe("sequence-identity replay suppression (delta / reasoning)", () => {
  it("drops only exact per-run seq replays and settles on one answer", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "abcdefghij", ...perRun(1) });
    frames.runFrame();
    expect(s.pushEvent(1, { type: "delta", text: "abcdefghij", ...perRun(1) }).accepted).toBe(false);
    s.pushEvent(1, { type: "delta", text: " XYZ", ...perRun(2) });
    frames.runFrame();
    expect(texts(s.getState().items, "assistant")).toEqual(["abcdefghij XYZ"]);
    s.pushEvent(1, { type: "done", answer: "abcdefghij XYZ", ...perRun(3) });
    const items = s.getState().items;
    expect(roles(items)).toEqual(["assistant", "outcome"]);
    expect(texts(items, "assistant")).toEqual(["abcdefghij XYZ"]);
  });

  it("drops a replayed suffix across several chunks by identity", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    ["a1", "b2", "c3", "d4"].forEach((text, i) => s.pushEvent(1, { type: "delta", text, ...perRun(i + 1) }));
    frames.runFrame();
    s.pushEvent(1, { type: "delta", text: "c3", ...perRun(3) });
    s.pushEvent(1, { type: "delta", text: "d4", ...perRun(4) });
    s.pushEvent(1, { type: "delta", text: "e5", ...perRun(5) });
    frames.runFrame();
    expect(texts(s.getState().items, "assistant")).toEqual(["a1b2c3d4e5"]);
  });

  it("handles interleaved replay with reasoning and tool events", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    const script = () => {
      s.pushEvent(1, { type: "delta", text: "A", ...perRun(1) });
      s.pushEvent(1, { type: "reasoning", text: "R", ...perRun(2) });
      s.pushEvent(1, { type: "toolCall", id: "t1", name: "exec", display: "ls", ...perRun(3) });
      s.pushEvent(1, { type: "toolResult", id: "t1", ok: true, summary: "s", text: "full", exitCode: 0, ...perRun(4) });
      s.pushEvent(1, { type: "delta", text: "B", ...perRun(5) });
    };
    script();
    frames.runFrame();
    script();
    s.pushEvent(1, { type: "delta", text: "C", ...perRun(6) });
    frames.runFrame();
    const items = s.getState().items;
    expect(roles(items)).toEqual(["assistant", "reasoning", "tool", "assistant"]);
    expect(texts(items, "assistant")).toEqual(["A", "BC"]);
    expect(texts(items, "reasoning")).toEqual(["R"]);
    s.pushEvent(1, { type: "done", answer: "BC", ...perRun(7) });
    expect(roles(s.getState().items)).toEqual(["assistant", "reasoning", "tool", "assistant", "outcome"]);
  });

  it("suppresses reasoning replay and keeps the surviving reasoning exact", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "reasoning", text: "same reasoning chunk", ...perRun(1) });
    frames.runFrame();
    s.pushEvent(1, { type: "reasoning", text: "same reasoning chunk", ...perRun(1) });
    s.pushEvent(1, { type: "reasoning", text: " and more", ...perRun(2) });
    frames.runFrame();
    expect(texts(s.getState().items, "reasoning")).toEqual(["same reasoning chunk and more"]);
    s.pushEvent(1, { type: "done", answer: "Answer", ...perRun(3) });
    const items = s.getState().items;
    expect(texts(items, "reasoning")).toEqual(["same reasoning chunk and more"]);
    expect(texts(items, "assistant")).toEqual(["Answer"]);
  });

  it("suppresses replay even before the original fragments were flushed", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "x", ...perRun(1) });
    s.pushEvent(1, { type: "delta", text: "x", ...perRun(1) });
    s.pushEvent(1, { type: "delta", text: "y", ...perRun(2) });
    frames.runFrame();
    expect(texts(s.getState().items, "assistant")).toEqual(["xy"]);
  });

  it("never drops genuinely new identical reasoning, with or without seq", () => {
    const withSeq = stream();
    withSeq.s.beginRun(1);
    withSeq.s.pushEvent(1, { type: "reasoning", text: "ha", ...perRun(1) });
    withSeq.s.pushEvent(1, { type: "reasoning", text: "ha", ...perRun(2) });
    withSeq.s.pushEvent(1, { type: "reasoning", text: "!", ...perRun(3) });
    withSeq.s.pushEvent(1, { type: "done", answer: "Answer", ...perRun(4) });
    expect(texts(withSeq.s.getState().items, "reasoning")).toEqual(["haha!"]);
    const noSeq = stream();
    noSeq.s.beginRun(1);
    noSeq.s.pushEvent(1, { type: "reasoning", text: "ha" });
    noSeq.s.pushEvent(1, { type: "reasoning", text: "ha" });
    noSeq.s.pushEvent(1, { type: "reasoning", text: "!" });
    noSeq.s.pushEvent(1, { type: "done", answer: "Answer" });
    expect(texts(noSeq.s.getState().items, "reasoning")).toEqual(["haha!"]);
  });

  it("repeated identical chunks survive: only the exact seq replay is dropped", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "reasoning", text: "x", ...perRun(1) });
    s.pushEvent(1, { type: "reasoning", text: "x", ...perRun(2) });
    frames.runFrame();
    s.pushEvent(1, { type: "reasoning", text: "x", ...perRun(2) });
    s.pushEvent(1, { type: "reasoning", text: "x", ...perRun(3) });
    s.pushEvent(1, { type: "reasoning", text: "y", ...perRun(4) });
    frames.runFrame();
    expect(texts(s.getState().items, "reasoning")).toEqual(["xxxy"]);
  });

  it("the per-job counter never resets across model calls; text and reasoning share it", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "a", ...perRun(1) });
    s.pushEvent(1, { type: "reasoning", text: "r", ...perRun(2) });
    s.pushEvent(1, { type: "delta", text: "b", ...perRun(3) });
    frames.runFrame();
    s.pushEvent(1, { type: "reasoning", text: "r", ...perRun(2) });
    s.pushEvent(1, { type: "delta", text: "c", ...perRun(4) });
    s.pushEvent(1, { type: "reasoning", text: "r2", ...perRun(5) });
    frames.runFrame();
    const items = s.getState().items;
    expect(texts(items, "assistant")).toEqual(["a", "bc"]);
    expect(texts(items, "reasoning")).toEqual(["r", "r2"]);
  });

  it("scopes identity per run and treats missing/invalid identity fields as new content", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "One", ...perRun(1) });
    s.pushEvent(1, { type: "done", answer: "One", ...perRun(2) });
    s.beginRun(2);
    s.pushEvent(2, { type: "delta", text: "Two", ...perRun(1) });
    frames.runFrame();
    expect(texts(s.getState().items, "assistant")).toEqual(["One", "Two"]);
    s.beginRun(3);
    const invalid = [{ seq: 0 }, { seq: -2 }, { seq: 1.5 }, { seq: "7" }, {}];
    for (const identity of invalid) {
      s.pushEvent(3, { type: "delta", text: "x", ...identity });
    }
    frames.runFrame();
    expect(texts(s.getState().items, "assistant")).toEqual(["One", "Two", "xxxxx"]);
  });

  it("a replayed done still settles exactly once after a replayed text prefix", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "Answer", ...perRun(1) });
    frames.runFrame();
    s.pushEvent(1, { type: "delta", text: "Answer", ...perRun(1) });
    expect(s.pushEvent(1, { type: "done", answer: "Answer", ...perRun(2) }).accepted).toBe(true);
    const replayedDone = s.pushEvent(1, { type: "done", answer: "Answer", ...perRun(2) });
    expect(replayedDone.accepted).toBe(false);
    expect(replayedDone.terminal).toBe("done");
    const items = s.getState().items;
    expect(roles(items)).toEqual(["assistant", "outcome"]);
    expect(texts(items, "assistant")).toEqual(["Answer"]);
    expect(s.pushEvent(1, { type: "delta", text: "Answer", ...perRun(3) }).accepted).toBe(false);
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
    expect(roles(doneWith("Complete answer", "Complete answer").items)).toEqual(["assistant", "outcome"]);
    const fallback = doneWith(null, "Complete answer");
    expect(roles(fallback.items)).toEqual(["assistant", "outcome"]);
    expect(texts(fallback.items, "assistant")).toEqual(["Complete answer"]);
  });

  it("the authoritative answer replaces the final segment: prefix, short prefix, divergence", () => {
    const long = doneWith("0123456789", "0123456789 follow-up");
    expect(texts(long.items, "assistant")).toEqual(["0123456789 follow-up"]);
    expect(roles(long.items)).toEqual(["assistant", "outcome"]);
    const short = doneWith("Good", "Good, complete answer");
    expect(texts(short.items, "assistant")).toEqual(["Good, complete answer"]);
    expect(roles(short.items)).toEqual(["assistant", "outcome"]);
    const diverged = doneWith("Streaming that differs completely from the answer", "Authoritative answer");
    expect(texts(diverged.items, "assistant")).toEqual(["Authoritative answer"]);
    expect(roles(diverged.items)).toEqual(["assistant", "outcome"]);
  });

  it("empty raw answer does not add a placeholder after streamed text, but does with no output", () => {
    const streamed = doneWith("Previously streamed text", "");
    expect(texts(streamed.items, "assistant")).toEqual(["Previously streamed text"]);
    expect(roles(streamed.items)).toEqual(["assistant", "outcome"]);
    const empty = doneWith(null, "");
    expect(texts(empty.items, "assistant")).toEqual(["（无回答）"]);
  });

  it("replaces the final segment even when reasoning trails it, without touching earlier items", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "Earlier narration" });
    frames.runFrame();
    s.pushEvent(1, { type: "toolCall", id: "t", name: "exec", display: "ls" });
    s.pushEvent(1, { type: "toolResult", id: "t", ok: true, summary: "s", text: "full", exitCode: 0 });
    s.pushEvent(1, { type: "delta", text: "streamed final" });
    s.pushEvent(1, { type: "reasoning", text: "trailing reasoning" });
    frames.runFrame();
    s.pushEvent(1, { type: "done", answer: "authoritative final" });
    const items = s.getState().items;
    expect(roles(items)).toEqual(["assistant", "tool", "assistant", "reasoning", "outcome"]);
    expect(texts(items, "assistant")).toEqual(["Earlier narration", "authoritative final"]);
    expect(texts(items, "reasoning")).toEqual(["trailing reasoning"]);
  });

  it("appends after a tool boundary even with trailing reasoning, and handles empty raw there", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "Narration before the tool" });
    frames.runFrame();
    s.pushEvent(1, { type: "toolCall", id: "t", name: "exec", display: "ls" });
    s.pushEvent(1, { type: "reasoning", text: "Closing reasoning" });
    frames.runFrame();
    s.pushEvent(1, { type: "done", answer: "Answer after the tool" });
    expect(texts(s.getState().items, "assistant")).toEqual(["Narration before the tool", "Answer after the tool"]);
    expect(roles(s.getState().items)).toEqual(["assistant", "tool", "reasoning", "assistant", "outcome"]);

    const empty = stream();
    empty.s.beginRun(1);
    empty.s.pushEvent(1, { type: "delta", text: "streamed final" });
    empty.s.pushEvent(1, { type: "reasoning", text: "trailing reasoning" });
    empty.frames.runFrame();
    empty.s.pushEvent(1, { type: "done", answer: "" });
    expect(texts(empty.s.getState().items, "assistant")).toEqual(["streamed final"]);
    expect(roles(empty.s.getState().items)).toEqual(["assistant", "reasoning", "outcome"]);
  });

  it("planSubmitted upgrades the closing bubble to a plan exactly once", () => {
    const state = doneWith("Plan body", "Plan body", true);
    expect(roles(state.items)).toEqual(["plan", "outcome"]);
    expect(texts(state.items, "plan")).toEqual(["Plan body"]);
  });

  it("error produces a persistent outcome row and wins over later events", () => {
    const { s } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "Partial" });
    const result = s.pushEvent(1, { type: "error", message: "Network disconnected", retryable: true });
    expect(result.terminal).toBe("error");
    const items = s.getState().items;
    expect(roles(items)).toEqual(["assistant", "outcome"]);
    expect(items.at(-1)).toMatchObject({ outcome: "error", text: "Network disconnected" });
    expect(s.pushEvent(1, { type: "done", answer: "Partial" }).accepted).toBe(false);
    expect(s.getState().attempts[0].outcome).toBe("error");
  });

  it("cancel flushes pending text, records canceled once and survives a late done", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "Partially streamed output" });
    s.cancelRun(1, true);
    expect(frames.scheduled()).toBe(0);
    const items = s.getState().items;
    expect(roles(items)).toEqual(["assistant", "outcome"]);
    expect(items[0]).toMatchObject({ text: "Partially streamed output" });
    expect(items[1]).toMatchObject({ outcome: "canceled" });
    s.cancelRun(1, true);
    expect(roles(s.getState().items)).toEqual(["assistant", "outcome"]);
    expect(s.pushEvent(1, { type: "done", answer: "Partially streamed output" }).accepted).toBe(false);
    expect(s.getState().attempts[0].outcome).toBe("canceled");
  });

  it("takeover done appends the closing narration; cancel waits for the real terminal", () => {
    const { s } = stream();
    s.beginRun(7, "takeover");
    s.pushEvent(7, { type: "screen", text: "screen-1" });
    s.pushEvent(7, { type: "delta", text: "Observe" });
    s.pushEvent(7, { type: "screen", text: "screen-2" });
    s.cancelRun(7, false);
    expect(s.getState().attempts[0].outcome).toBeNull();
    const done = s.pushEvent(7, { type: "done", answer: "Installation complete" });
    expect(done.accepted).toBe(true);
    const items = s.getState().items;
    expect(items.filter((i) => i.role === "tool" && i.name === "read_screen")).toHaveLength(1);
    expect(texts(items, "assistant")).toEqual(["Observe", "接管结束：Installation complete"]);
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
      question: { question: "Continue?", options: ["Yes"] },
      confirmationNonce: "nb",
    });
    s.resolveInteraction(1, confirm!.id, "na", "Allowed once");
    expect(pendingInteraction(s.getState(), 1, "confirm")).toBeNull();
    const question = pendingInteraction(s.getState(), 1, "question");
    expect(question).toMatchObject({ callId: "b" });
    expect(question?.resolution).toBeUndefined();
    const before = s.getState();
    s.resolveInteraction(1, confirm!.id, "na", "已拒绝");
    s.resolveInteraction(1, question!.id, "wrong", "Answered: x");
    s.resolveInteraction(2, question!.id, "nb", "Answered: x");
    expect(s.getState()).toBe(before);
    s.resolveInteraction(1, question!.id, "nb", "已回答：Yes");
    expect(pendingInteraction(s.getState(), 1, "question")).toBeNull();
    expect(s.getState().items.find((i) => i.id === question!.id)).toMatchObject({
      resolution: "已回答：Yes",
    });
  });

  it("terminal and cancel close pending cards; replayed confirm does not reopen them", () => {
    const { s } = stream();
    s.beginRun(1);
    const ev = confirmEvent("a", "na");
    s.pushEvent(1, ev);
    s.pushEvent(1, { type: "done", answer: "Good" });
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

  it("resolveInteraction settles only the card matching both id and nonce", () => {
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
    s.pushEvent(1, { type: "delta", text: "Wrap-up" });
    s.dispose();
    expect(frames.scheduled()).toBe(0);
    expect(texts(s.getState().items, "assistant")).toEqual(["Wrap-up"]);
    expect(s.pushEvent(1, { type: "delta", text: "late" }).accepted).toBe(false);
    expect(s.pushEvent(1, { type: "done", answer: "Wrap-up" }).accepted).toBe(false);
    expect(texts(s.getState().items, "assistant")).toEqual(["Wrap-up"]);
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
    s.pushEvent(1, { type: "delta", text: "Old" });
    s.flush();
    const history = historyToItems(s.getState(), [
      { role: "user", content: { content: "With image", imageCount: 2 } },
      { role: "assistant", content: { content: "Historical answer" } },
      { role: "tool", content: { content: "Not in history" } },
      { role: "assistant", content: { content: "" } },
    ]);
    s.reset(history);
    const items = s.getState().items;
    expect(roles(items)).toEqual(["user", "assistant"]);
    expect(items[0]).toMatchObject({ text: "With image", imageCount: 2, attempt: null });
    expect(s.getState().attempts).toHaveLength(0);
    expect(s.pushEvent(1, { type: "delta", text: "stale" }).accepted).toBe(false);
    expect(s.getState().items).toBe(items);
    s.beginRun(2);
    s.pushEvent(2, { type: "delta", text: "New" });
    s.flush();
    const ids = s.getState().items.map((i) => i.id);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("reset with no items yields an empty conversation and clears usage/todos", () => {
    let state: ConversationState = createConversation();
    state = beginRun(state, 1);
    state = applyAiEvent(state, 1, { type: "usage", promptTokens: 3, contextWindow: 9 }).state;
    state = applyAiEvent(state, 1, { type: "todos", items: [{ content: "t", status: "pending" }] }).state;
    state = resetConversation(state);
    expect(state.items).toHaveLength(0);
    expect(state.usage).toBeNull();
    expect(state.todos).toHaveLength(0);
  });
});
