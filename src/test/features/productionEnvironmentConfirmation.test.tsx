/** @vitest-environment jsdom */

import { describe, expect, it } from "vitest";
import {
  evaluateEnvironmentConfirmation,
  goTrimSpace,
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

describe("goTrimSpace", () => {
  it("trims the full Unicode White_Space set like Go's strings.TrimSpace", () => {
    expect(goTrimSpace("\u0085production\u0085")).toBe("production");
    expect(goTrimSpace("\u00a0production\u00a0")).toBe("production");
    expect(goTrimSpace("\u3000production\u3000")).toBe("production");
    expect(goTrimSpace(" \t\n production \r\n")).toBe("production");
  });

  it("does not trim U+FEFF, matching Go (unicode.IsSpace excludes it)", () => {
    expect(goTrimSpace("\uFEFFproduction\uFEFF")).toBe("\uFEFFproduction\uFEFF");
  });
});

describe("matchesEnvironmentName", () => {
  it("matches with Go-trimmed surrounding space", () => {
    expect(matchesEnvironmentName(" production ", "production")).toBe(true);
    expect(matchesEnvironmentName("\u0085production\u0085", "production")).toBe(true);
    expect(matchesEnvironmentName("\u00a0production", "production")).toBe(true);
  });

  it("matches case variants exactly like Go's EqualFold", () => {
    expect(matchesEnvironmentName(" Production ", "production")).toBe(true);
    expect(matchesEnvironmentName("PRODUCTION", "production")).toBe(true);
    // Long s and final sigma fold to s and sigma in Go's simple folding.
    expect(matchesEnvironmentName("ſ", "s")).toBe(true);
    expect(matchesEnvironmentName("ς", "σ")).toBe(true);
    expect(matchesEnvironmentName("Σ", "σ")).toBe(true);
    // U+0130 has no simple case folding in Go; the backend rejects it too.
    expect(matchesEnvironmentName("İ", "i\u0307")).toBe(false);
    expect(matchesEnvironmentName("İ", "i")).toBe(false);
    // U+FEFF is not Go whitespace, so a FEFF-prefixed name must not match.
    expect(matchesEnvironmentName("\uFEFFproduction", "production")).toBe(false);
    expect(matchesEnvironmentName("production\uFEFF", "production")).toBe(false);
  });

  it("rejects names the backend rejects and folds names the backend folds", () => {
    // U+0130 has no simple case folding in Go; the backend rejects it too.
    expect(matchesEnvironmentName("İ", "i\u0307")).toBe(false);
    expect(matchesEnvironmentName("İ", "i")).toBe(false);
    // Long s and final sigma fold to s and sigma in Go's simple folding.
    expect(matchesEnvironmentName("ſ", "s")).toBe(true);
    expect(matchesEnvironmentName("ς", "σ")).toBe(true);
    // U+FEFF is not Go whitespace, so a FEFF-prefixed name must not match.
    expect(matchesEnvironmentName("\uFEFFproduction", "production")).toBe(false);
    expect(matchesEnvironmentName("production\uFEFF", "production")).toBe(false);
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

  it("confirms with Go-trimmed surrounding space", () => {
    const state = evaluateEnvironmentConfirmation(challenge, { "tab-1": " production " }, "hash-a");
    expect(state.canConfirm).toBe(true);
    const nel = evaluateEnvironmentConfirmation(challenge, { "tab-1": "\u0085production\u0085" }, "hash-a");
    expect(nel.canConfirm).toBe(true);
  });

  it("confirms case-variant input exactly like the backend", () => {
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

  it("does not confirm with an extra target key even when the real target is filled", () => {
    const state = evaluateEnvironmentConfirmation(challenge, { "tab-1": "production", "tab-2": "staging" }, "hash-a");
    expect(state.canConfirm).toBe(false);
    expect(state.extraInputs).toEqual([{ key: "tab-2", typed: "staging" }]);
  });

  it("does not confirm with an extra key on a fully matched mixed set", () => {
    const state = evaluateEnvironmentConfirmation(
      mixedChallenge,
      { "tab-1": "production", "tab-2": "live", "tab-3": "production" },
      "hash-b",
    );
    expect(state.canConfirm).toBe(false);
    expect(state.extraInputs).toHaveLength(1);
  });

  it("rejects extra keys even with empty values (out-of-challenge state never slips through)", () => {
    const state = evaluateEnvironmentConfirmation(challenge, { "tab-1": "production", "tab-2": "" }, "hash-a");
    expect(state.canConfirm).toBe(false);
    expect(state.extraInputs).toEqual([{ key: "tab-2", typed: "" }]);
  });

  it("exposes the submitted names array matching the required cardinality and items", () => {
    const state = evaluateEnvironmentConfirmation(mixedChallenge, { "tab-1": "production", "tab-2": "live" }, "hash-b");
    expect(state.submittedNames).toEqual(["production", "live"]);
    expect(state.canConfirm).toBe(true);
    const wrong = evaluateEnvironmentConfirmation(mixedChallenge, { "tab-1": "production" }, "hash-b");
    expect(wrong.submittedNames).toEqual(["production"]);
    expect(wrong.canConfirm).toBe(false);
  });
});

describe("isStaleChallenge", () => {
  it("detects a changed target-set hash", () => {
    expect(isStaleChallenge(challenge, "hash-b")).toBe(true);
    expect(isStaleChallenge(challenge, "hash-a")).toBe(false);
  });
});
