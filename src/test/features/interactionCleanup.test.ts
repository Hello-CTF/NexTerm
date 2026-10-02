import { describe, expect, it } from "vitest";
import { clearInteractionIfMatch, type AiInteractionCard } from "../../features/ai/aiWire";

function card(role: "confirm" | "question", callId: string, nonce = `nonce-${callId}`): AiInteractionCard {
  return { role, jobId: "job-1", callId, nonce };
}

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

describe("interaction completion identity", () => {
  it("keeps a question that arrives while ai_confirm is deferred", async () => {
    const rpc = deferred();
    const completed = card("confirm", "call-a");
    const next = card("question", "call-b");
    let current: AiInteractionCard | null = completed;
    const completion = (async () => {
      await rpc.promise;
      current = clearInteractionIfMatch(current, completed, "confirm");
    })();

    current = next;
    rpc.resolve();
    await completion;
    expect(current).toBe(next);
  });

  it("keeps a confirmation that arrives while ai_answer is deferred", async () => {
    const rpc = deferred();
    const completed = card("question", "call-a");
    const next = card("confirm", "call-b");
    let current: AiInteractionCard | null = completed;
    const completion = (async () => {
      await rpc.promise;
      current = clearInteractionIfMatch(current, completed, "question");
    })();

    current = next;
    rpc.resolve();
    await completion;
    expect(current).toBe(next);
  });

  it("keeps a newer question with a different nonce after ai_answer resolves", async () => {
    const rpc = deferred();
    const completed = card("question", "call-a", "nonce-old");
    const next = card("question", "call-b", "nonce-new");
    let current: AiInteractionCard | null = completed;
    const completion = (async () => {
      await rpc.promise;
      current = clearInteractionIfMatch(current, completed, "question");
    })();

    current = next;
    rpc.resolve();
    await completion;
    expect(current).toBe(next);
  });

  it("clears only the exact completed card and never another kind", () => {
    const completed = card("confirm", "call-a");
    expect(clearInteractionIfMatch(completed, completed, "confirm")).toBeNull();
    const question = card("question", "call-a");
    expect(clearInteractionIfMatch(question, completed, "question")).toBe(question);
    const otherJob = { ...completed, jobId: "job-2" };
    expect(clearInteractionIfMatch(otherJob, completed, "confirm")).toBe(otherJob);
    const otherNonce = { ...completed, nonce: "nonce-other" };
    expect(clearInteractionIfMatch(otherNonce, completed, "confirm")).toBe(otherNonce);
  });
});
