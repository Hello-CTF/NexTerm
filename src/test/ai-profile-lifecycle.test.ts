import { describe, expect, it } from "vitest";
import type { ModelProfile } from "../ipc/commands";
import {
  CIRCUIT_DEFAULT_COOLDOWN_SECONDS,
  CIRCUIT_DEFAULT_THRESHOLD,
  circuitCooldownFromInput,
  circuitCooldownLabel,
  circuitRemainingSeconds,
  circuitRuntimeState,
  circuitStatusText,
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

  it("exposes the backend defaults (5 failures, 300s) for the form placeholders", () => {
    expect(CIRCUIT_DEFAULT_THRESHOLD).toBe(5);
    expect(CIRCUIT_DEFAULT_COOLDOWN_SECONDS).toBe(300);
  });
});

describe("circuit runtime state classification and copy", () => {
  const now = 1_000_000;

  it("classifies zero/closed/open/expired from the DTO", () => {
    expect(circuitRuntimeState({ consecutiveFailures: 0, openUntil: null }, now)).toBe("zero");
    expect(circuitRuntimeState({ consecutiveFailures: 2, openUntil: null }, now)).toBe("closed");
    expect(circuitRuntimeState({ consecutiveFailures: 5, openUntil: now + 60_000 }, now)).toBe("open");
    expect(circuitRuntimeState({ consecutiveFailures: 5, openUntil: now - 1 }, now)).toBe("expired");
    expect(circuitRuntimeState({ consecutiveFailures: 0, openUntil: now - 1 }, now)).toBe("expired");
  });

  it("renders accurate copy per state", () => {
    expect(circuitStatusText({ consecutiveFailures: 0, openUntil: null }, now)).toBe(
      "运行正常：暂无连续失败",
    );
    expect(circuitStatusText({ consecutiveFailures: 2, openUntil: null }, now)).toBe(
      "运行中：最近连续失败 2 次",
    );
    expect(circuitStatusText({ consecutiveFailures: 5, openUntil: now + 60_000 }, now)).toBe(
      "已暂停：连续失败 5 次，60 秒后自动恢复",
    );
    expect(circuitStatusText({ consecutiveFailures: 5, openUntil: now - 1 }, now)).toBe(
      "暂停已结束：连续失败 5 次，下次请求时恢复",
    );
  });

  it("counts remaining whole seconds, never negative", () => {
    expect(circuitRemainingSeconds(5_000, 2_000)).toBe(3);
    expect(circuitRemainingSeconds(2_500, 2_000)).toBe(1);
    expect(circuitRemainingSeconds(2_000, 2_000)).toBe(0);
    expect(circuitRemainingSeconds(1_000, 2_000)).toBe(0);
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
