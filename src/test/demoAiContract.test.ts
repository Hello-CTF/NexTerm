import { beforeEach, describe, expect, it, vi } from "vitest";
import { mockInvoke } from "../demo/mock";
import type { ModelListDto, ProviderTestResult } from "../ipc/types";

beforeEach(() => {
  vi.stubGlobal("window", globalThis);
});

describe("demo AI 命令契约", () => {
  it("ai_test_provider 返回与线上一致的 nullable 错误字段", async () => {
    const result = (await mockInvoke("ai_test_provider", { id: "p1" })) as ProviderTestResult;
    expect(result).toEqual({
      modelsOk: true,
      modelsError: null,
      chatOk: true,
      chatError: null,
    });
  });

  it("ai_model_refresh 返回 ModelListDto 对象", async () => {
    const result = (await mockInvoke("ai_model_refresh", { profile: { id: "p1" } })) as ModelListDto;
    expect(Array.isArray(result.models)).toBe(true);
    expect(result.models.length).toBeGreaterThan(0);
    expect(result.malformed).toBe(0);
  });
});
