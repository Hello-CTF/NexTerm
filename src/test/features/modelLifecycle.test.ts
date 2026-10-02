import { describe, expect, it } from "vitest";
import type { ModelProfile, ModelProfilesView } from "../../ipc/commands";
import { selectModelProfileId } from "../../features/ai/modelLifecycle";

function profile(id: string): ModelProfile {
  return {
    id,
    name: id,
    baseUrl: "https://example.test",
    apiKey: "key",
    model: "model",
    temperature: 0.3,
    contextWindow: 32768,
    proxy: null,
    stream: true,
  };
}

function view(activeId: string | null, ids = ["a", "b"]): ModelProfilesView {
  return { activeId, profiles: ids.map(profile) };
}

describe("model profile selection", () => {
  it("keeps a valid preferred profile", () => {
    expect(selectModelProfileId(view("a"), "b")).toBe("b");
  });

  it("falls back from a deleted preferred id to active, then to the first profile", () => {
    expect(selectModelProfileId(view("b"), "deleted")).toBe("b");
    expect(selectModelProfileId(view("deleted"), "also-deleted")).toBe("a");
    expect(selectModelProfileId(view(null), "deleted")).toBe("a");
  });

  it("returns null for an empty profile list", () => {
    expect(selectModelProfileId(view(null, []))).toBeNull();
  });
});
