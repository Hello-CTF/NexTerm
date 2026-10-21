/** @vitest-environment jsdom */

import { describe, expect, it } from "vitest";
import { getProductionChallenge } from "../../features/production/productionChallenge";

const targetSetHash = "a".repeat(64);
const commandHash = "b".repeat(64);
const inputHash = "c".repeat(64);

function execDetail(patch: Record<string, unknown> = {}) {
  return {
    kind: "exec_batch_production_confirmation",
    batchId: "batch-1",
    targetSetHash,
    commandHash,
    requiredEnvironmentNames: ["production"],
    productionTargets: [
      { sessionId: "s1", sessionName: "one", hostname: "one.prod.internal", environment: "production" },
    ],
    ...patch,
  };
}

function broadcastDetail(patch: Record<string, unknown> = {}) {
  return {
    kind: "broadcast_production_confirmation",
    targetSetHash,
    inputHash,
    requiredEnvironmentNames: ["live", "production"],
    productionTargets: [
      { tabId: "tab-1", sessionId: "s1", sessionName: "one", hostname: "one.live.internal", environment: "live" },
      { tabId: "tab-2", sessionId: "s2", sessionName: "two", hostname: "two.prod.internal", environment: "production" },
    ],
    ...patch,
  };
}

describe("getProductionChallenge", () => {
  it("parses an exec-batch challenge with the required environment names", () => {
    const challenge = getProductionChallenge(
      { code: "needs_confirm", detail: execDetail() },
      "exec_batch_production_confirmation",
    );
    expect(challenge).toEqual({
      kind: "exec_batch_production_confirmation",
      batchId: "batch-1",
      targetSetHash,
      commandHash,
      requiredEnvironmentNames: ["production"],
      productionTargets: [
        { sessionId: "s1", sessionName: "one", hostname: "one.prod.internal", environment: "production" },
      ],
    });
  });

  it("parses a broadcast challenge with per-target tab ids and required names", () => {
    const challenge = getProductionChallenge(
      { code: "needs_confirm", detail: broadcastDetail() },
      "broadcast_production_confirmation",
    );
    expect(challenge).toEqual({
      kind: "broadcast_production_confirmation",
      targetSetHash,
      inputHash,
      requiredEnvironmentNames: ["live", "production"],
      productionTargets: [
        { tabId: "tab-1", sessionId: "s1", sessionName: "one", hostname: "one.live.internal", environment: "live" },
        { tabId: "tab-2", sessionId: "s2", sessionName: "two", hostname: "two.prod.internal", environment: "production" },
      ],
    });
  });

  it("rejects challenges without required environment names", () => {
    const { requiredEnvironmentNames: _missing, ...missingNamesDetail } = execDetail();
    expect(getProductionChallenge(
      { code: "needs_confirm", detail: missingNamesDetail },
      "exec_batch_production_confirmation",
    )).toBeNull();
  });

  it("fails closed on the wrong kind, code, or shape", () => {
    expect(getProductionChallenge({ code: "needs_confirm", detail: execDetail() }, "broadcast_production_confirmation")).toBeNull();
    expect(getProductionChallenge({ code: "internal", detail: execDetail() }, "exec_batch_production_confirmation")).toBeNull();
    expect(getProductionChallenge({ code: "needs_confirm", detail: execDetail({ kind: "other" }) }, "exec_batch_production_confirmation")).toBeNull();
    expect(getProductionChallenge({ code: "needs_confirm", detail: execDetail({ targetSetHash: "forged" }) }, "exec_batch_production_confirmation")).toBeNull();
    expect(getProductionChallenge({ code: "needs_confirm", detail: execDetail({ commandHash: "b".repeat(63) }) }, "exec_batch_production_confirmation")).toBeNull();
    expect(getProductionChallenge({ code: "needs_confirm", detail: execDetail({ batchId: 42 }) }, "exec_batch_production_confirmation")).toBeNull();
    expect(getProductionChallenge({ code: "needs_confirm", detail: execDetail({ productionTargets: [] }) }, "exec_batch_production_confirmation")).toBeNull();
    expect(getProductionChallenge({ code: "needs_confirm", detail: broadcastDetail({ inputHash: "c".repeat(63) }) }, "broadcast_production_confirmation")).toBeNull();
    expect(getProductionChallenge({ code: "needs_confirm", detail: broadcastDetail({ productionTargets: [{ sessionId: "s1", sessionName: "one", environment: "production" }] }) }, "broadcast_production_confirmation")).toBeNull();
  });

  it("fails closed on invalid required environment names", () => {
    expect(getProductionChallenge(
      { code: "needs_confirm", detail: execDetail({ requiredEnvironmentNames: [] }) },
      "exec_batch_production_confirmation",
    )).toBeNull();
    expect(getProductionChallenge(
      { code: "needs_confirm", detail: execDetail({ requiredEnvironmentNames: ["production", "  "] }) },
      "exec_batch_production_confirmation",
    )).toBeNull();
    expect(getProductionChallenge(
      { code: "needs_confirm", detail: execDetail({ requiredEnvironmentNames: "production" }) },
      "exec_batch_production_confirmation",
    )).toBeNull();
  });
});
