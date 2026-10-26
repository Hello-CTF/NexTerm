import { describe, expect, it } from "vitest";
import { DROP_EDGE_MIN_PX, dropPreviewRect, dropRegionAt, edgeSplit, type DropRegion } from "../../app/tabDropZone";

const pane = { left: 100, top: 50, width: 800, height: 400 };
const edges = { edges: true };

describe("dropRegionAt", () => {
  it.each<[string, { x: number; y: number }, DropRegion]>([
    ["left quarter", { x: 150, y: 250 }, "left"],
    ["right quarter", { x: 850, y: 250 }, "right"],
    ["top quarter", { x: 500, y: 60 }, "top"],
    ["bottom quarter", { x: 500, y: 440 }, "bottom"],
    ["centre", { x: 500, y: 250 }, "center"],
    ["just inside the left band", { x: 100 + 199, y: 250 }, "left"],
    ["just past the left band", { x: 100 + 201, y: 250 }, "center"],
    ["pane border itself", { x: 100, y: 250 }, "left"],
  ])("maps the %s", (_name, point, region) => {
    expect(dropRegionAt(point, pane, edges)).toBe(region);
  });

  it("picks the edge the point is relatively closest to in a corner", () => {
    // 10% into the width but 20% into the height: the left band is deeper relative to its size.
    expect(dropRegionAt({ x: 100 + 80, y: 50 + 80 }, pane, edges)).toBe("left");
    // 20% into the width but 5% into the height: top wins.
    expect(dropRegionAt({ x: 100 + 160, y: 50 + 20 }, pane, edges)).toBe("top");
  });

  it("keeps a minimum band on small panes and leaves the centre reachable on tiny ones", () => {
    const small = { left: 0, top: 0, width: 80, height: 300 };
    // A quarter would be 20px; the band grows to the minimum.
    expect(dropRegionAt({ x: DROP_EDGE_MIN_PX - 1, y: 150 }, small, edges)).toBe("left");
    expect(dropRegionAt({ x: 40, y: 150 }, small, edges)).toBe("center");
    const tiny = { left: 0, top: 0, width: 60, height: 60 };
    // The minimum is capped at a third, so the middle third stays the centre.
    expect(dropRegionAt({ x: 21, y: 30 }, tiny, edges)).toBe("center");
    expect(dropRegionAt({ x: 19, y: 30 }, tiny, edges)).toBe("left");
  });

  it("treats the whole pane as the centre when edges are off (single-pane layouts)", () => {
    expect(dropRegionAt({ x: 101, y: 250 }, pane, { edges: false })).toBe("center");
    expect(dropRegionAt({ x: 500, y: 449 }, pane, { edges: false })).toBe("center");
  });

  it("returns null outside the pane or for an unmeasured pane", () => {
    expect(dropRegionAt({ x: 99, y: 250 }, pane, edges)).toBeNull();
    expect(dropRegionAt({ x: 500, y: 451 }, pane, edges)).toBeNull();
    expect(dropRegionAt({ x: 0, y: 0 }, { left: 0, top: 0, width: 0, height: 0 }, edges)).toBeNull();
  });
});

describe("edgeSplit and dropPreviewRect", () => {
  it("places the new pane on the dropped edge", () => {
    expect(edgeSplit("left")).toEqual({ dir: "row", side: "before", direction: "right" });
    expect(edgeSplit("right")).toEqual({ dir: "row", side: "after", direction: "right" });
    expect(edgeSplit("top")).toEqual({ dir: "col", side: "before", direction: "down" });
    expect(edgeSplit("bottom")).toEqual({ dir: "col", side: "after", direction: "down" });
  });

  it("previews the half the new pane takes, or the whole pane for the centre", () => {
    expect(dropPreviewRect("left")).toEqual({ x: 0, y: 0, width: 0.5, height: 1 });
    expect(dropPreviewRect("right")).toEqual({ x: 0.5, y: 0, width: 0.5, height: 1 });
    expect(dropPreviewRect("top")).toEqual({ x: 0, y: 0, width: 1, height: 0.5 });
    expect(dropPreviewRect("bottom")).toEqual({ x: 0, y: 0.5, width: 1, height: 0.5 });
    expect(dropPreviewRect("center")).toEqual({ x: 0, y: 0, width: 1, height: 1 });
  });
});
