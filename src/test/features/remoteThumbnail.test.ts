/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { drawThumbnail } from "../../features/remote/thumbnail";
import {
  MAX_THUMBNAILS,
  acquireThumbnailDemand,
  clearRemoteStatus,
  resetRemoteStatusForTest,
  setRemoteStatus,
  setRemoteThumbnail,
  thumbnailsWanted,
  useRemoteStatus,
} from "../../features/remote/remoteStatus";

function sourceCanvas(width: number, height: number): HTMLCanvasElement {
  const canvas = document.createElement("canvas");
  canvas.width = width;
  canvas.height = height;
  return canvas;
}

describe("drawThumbnail", () => {
  const drawImage = vi.fn();
  const toDataURL = vi.fn(() => "data:image/jpeg;base64,AAAA");
  let context: object | null;

  beforeEach(() => {
    drawImage.mockReset();
    toDataURL.mockClear();
    context = { drawImage };
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation((() => context) as never);
    vi.spyOn(HTMLCanvasElement.prototype, "toDataURL").mockImplementation(toDataURL as never);
  });

  afterEach(() => vi.restoreAllMocks());

  it("returns nothing without a canvas or for a canvas with no picture yet", () => {
    expect(drawThumbnail(null, 240)).toBeNull();
    expect(drawThumbnail(sourceCanvas(0, 0), 240)).toBeNull();
    expect(drawThumbnail(sourceCanvas(1, 600), 240)).toBeNull();
    expect(drawThumbnail(sourceCanvas(800, 1), 240)).toBeNull();
    expect(drawImage).not.toHaveBeenCalled();
  });

  it("scales a large desktop down to the width bound and keeps the aspect ratio", () => {
    expect(drawThumbnail(sourceCanvas(1920, 1080), 240)).toBe("data:image/jpeg;base64,AAAA");
    const [, x, y, width, height] = drawImage.mock.calls[0];
    expect([x, y, width, height]).toEqual([0, 0, 240, 135]);
    expect(toDataURL).toHaveBeenCalledWith("image/jpeg", 0.6);
  });

  it("never enlarges a desktop that is already smaller than the bound", () => {
    drawThumbnail(sourceCanvas(160, 90), 240);
    const [, , , width, height] = drawImage.mock.calls[0];
    expect([width, height]).toEqual([160, 90]);
  });

  it("keeps at least one pixel of height for a very wide desktop", () => {
    drawThumbnail(sourceCanvas(8000, 2), 240);
    const [, , , width, height] = drawImage.mock.calls[0];
    expect(width).toBe(240);
    expect(height).toBeGreaterThanOrEqual(1);
  });

  it("returns null when the canvas is tainted and encoding throws", () => {
    toDataURL.mockImplementationOnce(() => {
      throw new DOMException("tainted", "SecurityError");
    });
    expect(drawThumbnail(sourceCanvas(800, 600), 240)).toBeNull();
  });

  it("returns null when drawing throws", () => {
    drawImage.mockImplementationOnce(() => {
      throw new Error("bad source");
    });
    expect(drawThumbnail(sourceCanvas(800, 600), 240)).toBeNull();
  });

  it("returns null when no 2d context is available", () => {
    context = null;
    expect(drawThumbnail(sourceCanvas(800, 600), 240)).toBeNull();
  });
});

describe("remote thumbnail store", () => {
  beforeEach(() => resetRemoteStatusForTest());

  const entries = () => useRemoteStatus.getState().entries;

  it("ignores a picture for a tab that has no status entry", () => {
    setRemoteThumbnail("ghost", "data:x");
    expect(entries()).toEqual({});
  });

  it("keeps the last picture after the session drops and discards it when the tab closes", () => {
    setRemoteStatus("a", "connected");
    setRemoteThumbnail("a", "data:a", 1);
    setRemoteStatus("a", "disconnected");
    expect(entries().a).toMatchObject({ status: "disconnected", thumb: "data:a" });
    clearRemoteStatus("a");
    expect(entries().a).toBeUndefined();
    const before = entries();
    clearRemoteStatus("a");
    expect(entries()).toBe(before);
  });

  it("does not change state when the status is unchanged", () => {
    setRemoteStatus("a", "connected");
    const before = entries();
    setRemoteStatus("a", "connected");
    expect(entries()).toBe(before);
  });

  it("holds at most MAX_THUMBNAILS pictures and drops the oldest first", () => {
    const total = MAX_THUMBNAILS + 3;
    for (let index = 0; index < total; index += 1) {
      setRemoteStatus(`tab${index}`, "connected");
      setRemoteThumbnail(`tab${index}`, `data:${index}`, index + 1);
    }
    const withPicture = Object.entries(entries()).filter(([, entry]) => entry.thumb);
    expect(withPicture).toHaveLength(MAX_THUMBNAILS);
    for (let index = 0; index < 3; index += 1) {
      expect(entries()[`tab${index}`]).toMatchObject({ status: "connected", thumb: undefined });
    }
    expect(entries()[`tab${total - 1}`].thumb).toBe(`data:${total - 1}`);
  });

  it("tracks thumbnail demand and tolerates a double release", () => {
    expect(thumbnailsWanted()).toBe(false);
    const first = acquireThumbnailDemand();
    const second = acquireThumbnailDemand();
    first();
    first();
    expect(thumbnailsWanted()).toBe(true);
    second();
    expect(thumbnailsWanted()).toBe(false);
  });
});
