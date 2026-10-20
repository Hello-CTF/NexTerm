import { describe, expect, it } from "vitest";
import { assetTagValues, normalizeAssetTags } from "../../features/explorer/assetSearch";

describe("asset tag normalization", () => {
  it("trims and deduplicates submitted tags without changing their order", () => {
    expect(assetTagValues(" Prod, blue，prod ")).toEqual(["Prod", "blue", "prod"]);
    expect(normalizeAssetTags(" Prod, blue, prod，BLUE ")).toBe("Prod, blue");
    expect(normalizeAssetTags("one, One, ONE, two")).toBe("one, two");
  });
});
