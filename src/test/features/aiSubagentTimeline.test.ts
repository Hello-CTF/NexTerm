import { describe, expect, it } from "vitest";
import type { SubagentTimeline, ToolItem } from "../../features/ai/conversation";
import { createConversationStream } from "../../features/ai/conversationStream";

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

function timelineOf(
  s: ReturnType<typeof createConversationStream>,
  generation: number,
  parentCallId: string,
): SubagentTimeline | undefined {
  const item = s
    .getState()
    .items.find(
      (i): i is ToolItem =>
        i.role === "tool" && i.attempt === generation && i.callId === parentCallId,
    );
  return item?.subagent;
}

function spawnCard(s: ReturnType<typeof createConversationStream>, generation: number, callId: string, seq: number) {
  s.pushEvent(generation, {
    type: "toolCall",
    id: callId,
    name: "spawn_subagent",
    display: `spawn ${callId}`,
    seq,
  });
}

function subagentEvent(extra: Record<string, unknown>) {
  return { parentCallId: "sp-1", subagentId: "sub-1", depth: 1, ...extra };
}

describe("subagent timeline folding", () => {
  it("folds delta, tool and terminal events into the spawn tool card", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    spawnCard(s, 1, "sp-1", 1);
    s.pushEvent(1, subagentEvent({ type: "subagentDelta", text: "正在", seq: 2 }));
    s.pushEvent(1, subagentEvent({ type: "subagentDelta", text: "排查", seq: 3 }));
    s.pushEvent(1, subagentEvent({ type: "subagentToolCall", id: "c1", name: "list_assets", seq: 4 }));
    s.pushEvent(
      1,
      subagentEvent({ type: "subagentToolResult", id: "c1", ok: true, summary: "1 asset", seq: 5 }),
    );
    s.pushEvent(
      1,
      subagentEvent({ type: "subagentDone", status: "completed", summary: "child done", seq: 6 }),
    );
    frames.runFrame();
    const timeline = timelineOf(s, 1, "sp-1");
    expect(timeline).toMatchObject({
      subagentId: "sub-1",
      depth: 1,
      status: "completed",
      text: "正在排查",
      summary: "child done",
    });
    expect(timeline?.tools).toEqual([
      { callId: "c1", name: "list_assets", status: "ok", summary: "1 asset", panic: false },
    ]);
  });

  it("marks panicked subagent tools with the crash flag like main tools", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    spawnCard(s, 1, "sp-1", 1);
    s.pushEvent(1, subagentEvent({ type: "subagentToolCall", id: "c1", name: "list_assets", seq: 2 }));
    s.pushEvent(
      1,
      subagentEvent({ type: "subagentToolResult", id: "c1", ok: false, summary: "工具 list_assets 执行崩溃: 后端不可用", panic: true, seq: 3 }),
    );
    s.pushEvent(1, subagentEvent({ type: "subagentToolCall", id: "c2", name: "read_file", seq: 4 }));
    s.pushEvent(
      1,
      subagentEvent({ type: "subagentToolResult", id: "c2", ok: false, summary: "文件不存在", panic: false, seq: 5 }),
    );
    frames.runFrame();
    const tools = timelineOf(s, 1, "sp-1")?.tools;
    expect(tools).toEqual([
      { callId: "c1", name: "list_assets", status: "error", summary: "工具 list_assets 执行崩溃: 后端不可用", panic: true },
      { callId: "c2", name: "read_file", status: "error", summary: "文件不存在", panic: false },
    ]);
  });

  it("buckets concurrent spawn cards by parentCallId without cross-talk", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    spawnCard(s, 1, "sp-1", 1);
    spawnCard(s, 1, "sp-2", 2);
    s.pushEvent(1, subagentEvent({ type: "subagentDelta", text: "A", seq: 3 }));
    s.pushEvent(
      1,
      { parentCallId: "sp-2", subagentId: "sub-2", depth: 1, type: "subagentDelta", text: "B", seq: 4 },
    );
    frames.runFrame();
    expect(timelineOf(s, 1, "sp-1")?.text).toBe("A");
    expect(timelineOf(s, 1, "sp-2")?.text).toBe("B");
    expect(timelineOf(s, 1, "sp-1")?.subagentId).toBe("sub-1");
    expect(timelineOf(s, 1, "sp-2")?.subagentId).toBe("sub-2");
  });

  it("rejects subagent events from a previous run during a later run", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    spawnCard(s, 1, "sp-1", 1);
    s.pushEvent(1, subagentEvent({ type: "subagentDelta", text: "first", seq: 2 }));
    frames.runFrame();
    s.pushEvent(1, { type: "done", answer: "one", seq: 3 });
    s.beginRun(2);
    spawnCard(s, 2, "sp-2", 1);
    s.pushEvent(2, subagentEvent({ type: "subagentDelta", text: "leak", seq: 4 }));
    frames.runFrame();
    expect(timelineOf(s, 2, "sp-2")).toBeUndefined();
    expect(timelineOf(s, 1, "sp-1")?.text).toBe("first");
  });

  it("seals the timeline on cancellation and drops stale deltas", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    spawnCard(s, 1, "sp-1", 1);
    s.pushEvent(1, subagentEvent({ type: "subagentDelta", text: "partial", seq: 2 }));
    frames.runFrame();
    s.pushEvent(
      1,
      subagentEvent({ type: "subagentDone", status: "canceled", error: "context canceled", seq: 3 }),
    );
    s.pushEvent(1, subagentEvent({ type: "subagentDelta", text: "late", seq: 4 }));
    frames.runFrame();
    const timeline = timelineOf(s, 1, "sp-1");
    expect(timeline?.status).toBe("canceled");
    expect(timeline?.error).toBe("context canceled");
    expect(timeline?.text).toBe("partial");
  });

  it("dedupes replayed subagent events by seq without duplicating text or tools", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    const journaled = [
      { type: "toolCall", id: "sp-1", name: "spawn_subagent", display: "spawn", seq: 1 },
      subagentEvent({ type: "subagentDelta", text: "abc", seq: 2 }),
      subagentEvent({ type: "subagentToolCall", id: "c1", name: "exec_commands", seq: 3 }),
      subagentEvent({ type: "subagentToolResult", id: "c1", ok: true, summary: "ok", seq: 4 }),
      subagentEvent({ type: "subagentDone", status: "completed", summary: "done", seq: 5 }),
    ];
    for (const event of journaled) s.pushEvent(1, event);
    frames.runFrame();
    for (const event of journaled) s.pushEvent(1, event);
    frames.runFrame();
    const timeline = timelineOf(s, 1, "sp-1");
    expect(timeline?.text).toBe("abc");
    expect(timeline?.tools).toHaveLength(1);
    expect(timeline?.status).toBe("completed");
  });

  it("coalesces subagent deltas into one scheduled frame and one publish", () => {
    const { s, frames } = stream();
    s.beginRun(1);
    spawnCard(s, 1, "sp-1", 1);
    let publishes = 0;
    s.subscribe(() => {
      publishes += 1;
    });
    for (let i = 0; i < 20; i++) {
      s.pushEvent(1, subagentEvent({ type: "subagentDelta", text: `t${i}` }));
    }
    expect(frames.scheduled()).toBe(1);
    frames.runFrame();
    expect(publishes).toBe(1);
    expect(timelineOf(s, 1, "sp-1")?.text).toBe(
      Array.from({ length: 20 }, (_, i) => `t${i}`).join(""),
    );
  });

  it("flushes pending subagent text before folding the terminal event", () => {
    const { s } = stream();
    s.beginRun(1);
    spawnCard(s, 1, "sp-1", 1);
    s.pushEvent(1, subagentEvent({ type: "subagentDelta", text: "tail" }));
    s.pushEvent(1, subagentEvent({ type: "subagentDone", status: "completed", summary: "done" }));
    const timeline = timelineOf(s, 1, "sp-1");
    expect(timeline?.text).toBe("tail");
    expect(timeline?.status).toBe("completed");
  });
});
