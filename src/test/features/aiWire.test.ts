import { describe, expect, it } from "vitest";
import {
  answerInput,
  confirmationInput,
  confirmationNonceOf,
  interactionIdentityOf,
  questionFromEvent,
} from "../../features/ai/aiWire";

describe("AI interaction wire mapping", () => {
  it("reads confirmationNonce and preserves the complete confirmation request", () => {
    const event = { confirmationNonce: "nonce-1", nonce: "not-the-event-field" };
    const request = { jobId: "job", callId: "call", nonce: confirmationNonceOf(event) };
    expect(confirmationInput(request, "allow_session")).toEqual({
      jobId: "job",
      callId: "call",
      nonce: "nonce-1",
      decision: "allow_session",
    });
  });

  it("maps question payloads and answers with the same ownership fields", () => {
    expect(
      questionFromEvent({
        question: { question: "继续吗？", options: ["继续", "停止", 42] },
      }),
    ).toEqual({ question: "继续吗？", options: ["继续", "停止"] });
    expect(answerInput({ jobId: "job", callId: "call", nonce: "nonce" }, "继续")).toEqual({
      jobId: "job",
      callId: "call",
      nonce: "nonce",
      text: "继续",
    });
  });

  it("does not invent fields for malformed question or nonce payloads", () => {
    expect(questionFromEvent({ question: null })).toEqual({ question: "", options: [] });
    expect(confirmationNonceOf({ nonce: "wrong-field" })).toBe("");
  });

  it("reads HITL identity (requestId/attempt) from interaction events", () => {
    expect(interactionIdentityOf({ requestId: "req-1", attempt: 3 })).toEqual({
      requestId: "req-1",
      hitlAttempt: 3,
    });
    expect(interactionIdentityOf({ requestId: "req-1" })).toEqual({ requestId: "req-1" });
  });

  it("returns an empty identity for missing or malformed HITL fields", () => {
    expect(interactionIdentityOf({})).toEqual({});
    expect(interactionIdentityOf({ requestId: "", attempt: 0 })).toEqual({});
    expect(interactionIdentityOf({ requestId: 7, attempt: "2" })).toEqual({});
  });

  it("keeps the HITL identity off the confirmation and answer wire payloads", () => {
    expect(
      confirmationInput({ jobId: "job", callId: "call", nonce: "n", requestId: "req", hitlAttempt: 2 }, "deny"),
    ).toEqual({ jobId: "job", callId: "call", nonce: "n", decision: "deny" });
    expect(answerInput({ jobId: "job", callId: "call", nonce: "n", requestId: "req" }, "继续")).toEqual({
      jobId: "job",
      callId: "call",
      nonce: "n",
      text: "继续",
    });
  });
});
