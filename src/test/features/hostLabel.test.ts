import { describe, expect, it } from "vitest";
import { BUILTIN_LOCAL_HOST_ID, resolveHostLabel, type HostLabelSources } from "../../features/hosts/hostLabel";

const base: HostLabelSources = {
  assets: [
    { id: "A1", name: "web-1", deletedAt: null },
    { id: "A2", name: "old-db", deletedAt: 5 },
    { id: "A3", name: " ", deletedAt: 5 },
  ],
  devices: [{ id: "d-1", name: "build-box" }],
  loading: false,
};

describe("resolveHostLabel", () => {
  it("names assets, deleted assets and the built-in local host", () => {
    expect(resolveHostLabel("A1", base)).toBe("web-1");
    expect(resolveHostLabel("A2", base)).toBe("old-db（已删除）");
    expect(resolveHostLabel("A3", base)).toBe("已删除的主机");
    expect(resolveHostLabel(BUILTIN_LOCAL_HOST_ID, base)).toBe("本机");
  });

  it("names managed devices by their device name", () => {
    expect(resolveHostLabel("device/d-1", base)).toBe("build-box");
  });

  it("shows nothing while sources are loading", () => {
    expect(resolveHostLabel("device/d-9", { ...base, devices: undefined, loading: true })).toBe("");
  });

  it("keeps unresolved ids distinguishable", () => {
    const a = resolveHostLabel("device/d-aaa1", base);
    const b = resolveHostLabel("01J0ZZZZ9999b2c3", base);
    expect(a).toBe("未命名主机 …aaa1");
    expect(b).toBe("未命名主机 …b2c3");
    expect(a).not.toBe(b);
  });
});
