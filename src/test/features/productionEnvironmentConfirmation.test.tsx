/** @vitest-environment jsdom */

import { describe, expect, it } from "vitest";
import {
  evaluateEnvironmentConfirmation,
  isStaleChallenge,
  matchesEnvironmentName,
  type EnvironmentConfirmationChallenge,
} from "../../features/production/environmentConfirmation";

const challenge: EnvironmentConfirmationChallenge = {
  targetSetHash: "hash-a",
  requiredEnvironmentNames: ["production"],
  productionTargets: [
    {
      sessionId: "session-1",
      tabId: "tab-1",
      assetId: "asset-1",
      hostname: "api.production.example",
      sessionName: "Production API",
      environment: "production",
    },
  ],
};

const mixedChallenge: EnvironmentConfirmationChallenge = {
  targetSetHash: "hash-b",
  requiredEnvironmentNames: ["live", "production"],
  productionTargets: [
    {
      sessionId: "session-1",
      tabId: "tab-1",
      hostname: "api.production.example",
      sessionName: "Production API",
      environment: "production",
    },
    {
      sessionId: "session-2",
      tabId: "tab-2",
      hostname: "api.live.example",
      sessionName: "Live API",
      environment: "live",
    },
  ],
};

describe("matchesEnvironmentName", () => {
  it("matches with surrounding space and letter case ignored", () => {
    expect(matchesEnvironmentName(" Production ", "production")).toBe(true);
    expect(matchesEnvironmentName("production", "Production")).toBe(true);
  });

  it("rejects wrong, empty, and alias names", () => {
    expect(matchesEnvironmentName("staging", "production")).toBe(false);
    expect(matchesEnvironmentName("", "production")).toBe(false);
    expect(matchesEnvironmentName("   ", "production")).toBe(false);
    expect(matchesEnvironmentName("prod", "production")).toBe(false);
  });
});

describe("evaluateEnvironmentConfirmation", () => {
  it("does not confirm with an empty typed name", () => {
    const state = evaluateEnvironmentConfirmation(challenge, { "tab-1": "" }, "hash-a");
    expect(state.canConfirm).toBe(false);
    expect(state.targets[0].missing).toBe(true);
    expect(state.targets[0].matches).toBe(false);
  });

  it("does not confirm with a wrong typed name", () => {
    const state = evaluateEnvironmentConfirmation(challenge, { "tab-1": "staging" }, "hash-a");
    expect(state.canConfirm).toBe(false);
    expect(state.targets[0].missing).toBe(false);
    expect(state.targets[0].matches).toBe(false);
  });

  it("confirms when the typed name matches the actual environment", () => {
    const state = evaluateEnvironmentConfirmation(challenge, { "tab-1": "production" }, "hash-a");
    expect(state.canConfirm).toBe(true);
    expect(state.targets[0].matches).toBe(true);
  });

  it("confirms with surrounding space and letter case ignored", () => {
    const state = evaluateEnvironmentConfirmation(challenge, { "tab-1": " Production " }, "hash-a");
    expect(state.canConfirm).toBe(true);
  });

  it("does not confirm on a cross-target mismatch", () => {
    const state = evaluateEnvironmentConfirmation(mixedChallenge, { "tab-1": "live", "tab-2": "production" }, "hash-b");
    expect(state.canConfirm).toBe(false);
    expect(state.targets[0].matches).toBe(false);
    expect(state.targets[1].matches).toBe(false);
  });

  it("confirms a mixed target set only when every target matches its own environment", () => {
    const state = evaluateEnvironmentConfirmation(mixedChallenge, { "tab-1": "production", "tab-2": "live" }, "hash-b");
    expect(state.canConfirm).toBe(true);
    expect(state.targets.every((target) => target.matches)).toBe(true);
  });

  it("does not confirm when one target is missing", () => {
    const state = evaluateEnvironmentConfirmation(mixedChallenge, { "tab-1": "production" }, "hash-b");
    expect(state.canConfirm).toBe(false);
    expect(state.targets[1].missing).toBe(true);
  });

  it("does not confirm on a stale challenge", () => {
    const state = evaluateEnvironmentConfirmation(challenge, { "tab-1": "production" }, "hash-b");
    expect(state.stale).toBe(true);
    expect(state.canConfirm).toBe(false);
  });

  it("does not confirm with extra typed names beyond the required set", () => {
    const state = evaluateEnvironmentConfirmation(challenge, { "tab-1": "production" }, "hash-a");
    expect(state.canConfirm).toBe(true);
    const extra = evaluateEnvironmentConfirmation(
      { ...challenge, requiredEnvironmentNames: ["production", "staging"] },
      { "tab-1": "production" },
      "hash-a",
    );
    expect(extra.canConfirm).toBe(false);
  });
});

describe("isStaleChallenge", () => {
  it("detects a changed target-set hash", () => {
    expect(isStaleChallenge(challenge, "hash-b")).toBe(true);
    expect(isStaleChallenge(challenge, "hash-a")).toBe(false);
  });
});
