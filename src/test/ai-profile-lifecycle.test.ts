import { describe, expect, it } from "vitest";
import type { ModelProfile } from "../ipc/commands";
import {
  CIRCUIT_DEFAULT_COOLDOWN_SECONDS,
  CIRCUIT_DEFAULT_THRESHOLD,
  circuitCooldownFromInput,
  circuitCooldownLabel,
  circuitEffective,
  circuitThresholdFromInput,
  circuitThresholdLabel,
  maxTokensFromInput,
  maxTokensLabel,
  sameModelProfile,
} from "../features/ai/modelLifecycle";

function profile(overrides: Partial<ModelProfile> = {}): ModelProfile {
  return {
    id: "p1",
    name: "p1",
    baseUrl: "https://example.test",
    apiKey: "key",
    model: "model",
    temperature: 0.3,
    contextWindow: 32768,
    proxy: null,
    stream: true,
    ...overrides,
  };
}

describe("maxTokens input and label", () => {
  it("treats absent and null as unlimited for display", () => {
    expect(maxTokensLabel(profile())).toBe("");
    expect(maxTokensLabel(profile({ maxTokens: null }))).toBe("");
    expect(maxTokensLabel(profile({ maxTokens: 4096 }))).toBe("4096");
  });

  it("parses only positive integers, rounding decimals like the Go int clamp", () => {
    expect(maxTokensFromInput("")).toBeNull();
    expect(maxTokensFromInput("   ")).toBeNull();
    expect(maxTokensFromInput("abc")).toBeNull();
    expect(maxTokensFromInput("0")).toBeNull();
    expect(maxTokensFromInput("-5")).toBeNull();
    expect(maxTokensFromInput("12.5")).toBe(13);
    expect(maxTokensFromInput(" 2048 ")).toBe(2048);
    expect(maxTokensFromInput("4096")).toBe(4096);
  });
});

describe("circuit breaker input, label and effective config", () => {
  it("labels unset circuit fields as empty", () => {
    expect(circuitThresholdLabel(profile())).toBe("");
    expect(circuitThresholdLabel(profile({ circuitFailureThreshold: null }))).toBe("");
    expect(circuitThresholdLabel(profile({ circuitFailureThreshold: 3 }))).toBe("3");
    expect(circuitCooldownLabel(profile())).toBe("");
    expect(circuitCooldownLabel(profile({ circuitCooldownSeconds: null }))).toBe("");
    expect(circuitCooldownLabel(profile({ circuitCooldownSeconds: 60 }))).toBe("60");
  });

  it("parses threshold and cooldown as positive integers", () => {
    expect(circuitThresholdFromInput("")).toBeNull();
    expect(circuitThresholdFromInput("0")).toBeNull();
    expect(circuitThresholdFromInput("abc")).toBeNull();
    expect(circuitThresholdFromInput("7")).toBe(7);
    expect(circuitCooldownFromInput("")).toBeNull();
    expect(circuitCooldownFromInput("-1")).toBeNull();
    expect(circuitCooldownFromInput("600")).toBe(600);
  });

  it("falls back to the backend defaults (5 failures, 300s) only when unset", () => {
    expect(circuitEffective(profile())).toEqual({
      threshold: CIRCUIT_DEFAULT_THRESHOLD,
      cooldownSeconds: CIRCUIT_DEFAULT_COOLDOWN_SECONDS,
    });
    expect(CIRCUIT_DEFAULT_THRESHOLD).toBe(5);
    expect(CIRCUIT_DEFAULT_COOLDOWN_SECONDS).toBe(300);
    expect(circuitEffective(profile({ circuitFailureThreshold: 3 }))).toEqual({
      threshold: 3,
      cooldownSeconds: CIRCUIT_DEFAULT_COOLDOWN_SECONDS,
    });
    expect(circuitEffective(profile({ circuitCooldownSeconds: 60 }))).toEqual({
      threshold: CIRCUIT_DEFAULT_THRESHOLD,
      cooldownSeconds: 60,
    });
  });
});

describe("sameModelProfile covers the nullable AI-1 fields", () => {
  it("treats absent and null as the same unset value", () => {
    expect(sameModelProfile(profile(), profile({ maxTokens: null }))).toBe(true);
    expect(sameModelProfile(profile(), profile({ circuitFailureThreshold: null }))).toBe(true);
    expect(sameModelProfile(profile(), profile({ circuitCooldownSeconds: null }))).toBe(true);
  });

  it("detects edits to maxTokens and circuit fields", () => {
    expect(sameModelProfile(profile({ maxTokens: 4096 }), profile({ maxTokens: 4096 }))).toBe(true);
    expect(sameModelProfile(profile({ maxTokens: 4096 }), profile({ maxTokens: 4097 }))).toBe(false);
    expect(sameModelProfile(profile({ maxTokens: 4096 }), profile())).toBe(false);
    expect(
      sameModelProfile(profile({ circuitFailureThreshold: 3 }), profile({ circuitFailureThreshold: 3 })),
    ).toBe(true);
    expect(
      sameModelProfile(profile({ circuitFailureThreshold: 3 }), profile({ circuitFailureThreshold: 4 })),
    ).toBe(false);
    expect(
      sameModelProfile(profile({ circuitCooldownSeconds: 60 }), profile({ circuitCooldownSeconds: 61 })),
    ).toBe(false);
  });
});
