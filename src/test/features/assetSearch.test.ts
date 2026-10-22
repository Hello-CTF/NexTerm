import { describe, expect, it } from "vitest";
import { assetTagValues, normalizeAssetTags } from "../../features/explorer/assetSearch";

describe("asset tag normalization", () => {
  it("keeps read-side parsing with original casing, trimming and empty entries dropped", () => {
    expect(assetTagValues(" Prod, blue，prod ")).toEqual(["Prod", "blue", "prod"]);
    expect(assetTagValues("a,, b ，,c")).toEqual(["a", "b", "c"]);
    expect(assetTagValues("")).toEqual([]);
  });

  it("lowercases tags before deduplication when committing", () => {
    expect(normalizeAssetTags(" Prod, blue, prod，BLUE ")).toBe("prod, blue");
    expect(normalizeAssetTags("one, One, ONE, two")).toBe("one, two");
    expect(normalizeAssetTags("\tprod ,\nblue\t")).toBe("prod, blue");
  });

  it("canonicalizes environment naming conventions to lowercase", () => {
    expect(normalizeAssetTags("Prod, PROD, prod, Staging, STAGING")).toBe("prod, staging");
    expect(normalizeAssetTags("Dev, dev, DEV, dev")).toBe("dev");
  });

  it("supports ASCII and full-width commas and drops empty entries", () => {
    expect(normalizeAssetTags("alpha，beta,gamma")).toBe("alpha, beta, gamma");
    expect(normalizeAssetTags("alpha,, ，beta, ,")).toBe("alpha, beta");
    expect(normalizeAssetTags(" , ， ")).toBe("");
  });

  it("keeps first-occurrence order of canonical tags", () => {
    expect(normalizeAssetTags("zeta, Alpha, beta, ALPHA, ZETA")).toBe("zeta, alpha, beta");
  });
});
