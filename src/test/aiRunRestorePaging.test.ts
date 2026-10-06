import { describe, expect, it } from "vitest";
import type { AiRunEventDto } from "../ipc/types";
import { fetchRunEventsAfter, RUN_EVENTS_PAGE_SIZE } from "../features/ai/runRestore";
import { createConversationStream } from "../features/ai/conversationStream";

function seqEvent(seq: number): AiRunEventDto {
  return { type: "delta", text: `m${seq} `, seq };
}

function seqEvents(from: number, to: number): AiRunEventDto[] {
  const events: AiRunEventDto[] = [];
  for (let seq = from; seq <= to; seq += 1) events.push(seqEvent(seq));
  return events;
}

function pagedBackend(all: AiRunEventDto[]) {
  const calls: { afterSeq: number; limit: number }[] = [];
  const fetchPage = (afterSeq: number, limit: number) => {
    calls.push({ afterSeq, limit });
    return Promise.resolve(all.filter((event) => (event.seq as number) > afterSeq).slice(0, limit));
  };
  return { calls, fetchPage };
}

function seqsOf(events: AiRunEventDto[]): number[] {
  return events.map((event) => event.seq as number);
}

describe("fetchRunEventsAfter", () => {
  it("traverses multiple pages in order without duplicates or gaps", async () => {
    const all = seqEvents(1, 450);
    const { calls, fetchPage } = pagedBackend(all);

    const result = await fetchRunEventsAfter(fetchPage, 0);

    expect(result.failed).toBe(false);
    expect(seqsOf(result.events)).toEqual(all.map((event) => event.seq));
    expect(calls).toEqual([
      { afterSeq: 0, limit: RUN_EVENTS_PAGE_SIZE },
      { afterSeq: 200, limit: RUN_EVENTS_PAGE_SIZE },
      { afterSeq: 400, limit: RUN_EVENTS_PAGE_SIZE },
    ]);
  });

  it("stops after one short page and refetches with the caller cursor on resume", async () => {
    const { calls, fetchPage } = pagedBackend(seqEvents(251, 260));

    const result = await fetchRunEventsAfter(fetchPage, 250);

    expect(result.failed).toBe(false);
    expect(seqsOf(result.events)).toEqual([251, 252, 253, 254, 255, 256, 257, 258, 259, 260]);
    expect(calls).toEqual([{ afterSeq: 250, limit: RUN_EVENTS_PAGE_SIZE }]);
  });

  it("issues one final empty probe when the total is an exact multiple of the page size", async () => {
    const { calls, fetchPage } = pagedBackend(seqEvents(1, 400));

    const result = await fetchRunEventsAfter(fetchPage, 0);

    expect(result.failed).toBe(false);
    expect(result.events).toHaveLength(400);
    expect(calls.map((call) => call.afterSeq)).toEqual([0, 200, 400]);
  });

  it("skips overlapping seq without duplicating events", async () => {
    const fetchPage = (afterSeq: number) =>
      Promise.resolve(afterSeq === 0 ? seqEvents(1, 200) : seqEvents(200, 210));

    const result = await fetchRunEventsAfter(fetchPage, 0);

    expect(result.failed).toBe(false);
    expect(seqsOf(result.events)).toEqual(seqEvents(1, 210).map((event) => event.seq));
  });

  it("fails on a seq hole instead of silently skipping events", async () => {
    const holed = [...seqEvents(1, 5), seqEvent(7)];
    const { fetchPage } = pagedBackend(holed);

    const result = await fetchRunEventsAfter(fetchPage, 0, 10);

    expect(result.failed).toBe(true);
    expect(seqsOf(result.events)).toEqual([1, 2, 3, 4, 5]);
  });

  it("fails when a full page repeats without advancing the cursor", async () => {
    const fetchPage = () => Promise.resolve(seqEvents(1, 200));

    const result = await fetchRunEventsAfter(fetchPage, 0);

    expect(result.failed).toBe(true);
    expect(result.events).toHaveLength(200);
  });

  it("fails with partial events when a later page fetch rejects", async () => {
    const calls: number[] = [];
    const fetchPage = (afterSeq: number) => {
      calls.push(afterSeq);
      if (afterSeq === 0) return Promise.resolve(seqEvents(1, 200));
      return Promise.reject(new Error("offline"));
    };

    const result = await fetchRunEventsAfter(fetchPage, 0);

    expect(result.failed).toBe(true);
    expect(result.events).toHaveLength(200);
    expect(calls).toEqual([0, 200]);
  });

  it("applies paged reload identically to a single live batch", async () => {
    const all = [...seqEvents(1, 450), { type: "done", answer: "ok", turns: 1, tokensIn: 1, tokensOut: 1, seq: 451 }];
    const scheduler = () => () => undefined;

    const live = createConversationStream(scheduler);
    live.beginRun(1, "chat");
    live.bindJob(1, "job-1");
    for (const event of all) live.pushEvent(1, event);
    live.flush();

    const paged = createConversationStream(scheduler);
    paged.beginRun(1, "chat");
    paged.bindJob(1, "job-1");
    const { events, failed } = await fetchRunEventsAfter(
      (afterSeq, limit) =>
        Promise.resolve(all.filter((event) => (event.seq as number) > afterSeq).slice(0, limit)),
      0,
      100,
    );
    expect(failed).toBe(false);
    for (const event of events) paged.pushEvent(1, event);
    paged.flush();

    expect(paged.lastSeq(1)).toBe(451);
    expect(paged.getState()).toEqual(live.getState());
  });
});
