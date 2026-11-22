import { describe, expect, it } from "vitest";
import { keyboardViewShift } from "../../features/terminal/keyboardViewShift";

// A 40-row grid of 20px cells (800px) kept while the keyboard leaves a 400px host: rows 0-19 are clipped.
const base = { hostHeight: 400, terminalHeight: 800, screenTop: 0, screenHeight: 800, rows: 40 };

describe("keyboardViewShift", () => {
  it("keeps the bottom anchor while it shows the cursor row", () => {
    expect(keyboardViewShift({ ...base, cursorRow: 39 })).toBe(0);
    expect(keyboardViewShift({ ...base, cursorRow: 20 })).toBe(0);
  });

  it("moves the grid down so a cursor in the clipped top rows ends at the visible bottom", () => {
    // A fresh screen with the prompt on row 2: the top 400px show instead of 20 empty rows.
    expect(keyboardViewShift({ ...base, cursorRow: 2 })).toBe(400);
    // Row 19 (bottom edge at 400px) is the last clipped row: it becomes the visible bottom row.
    expect(keyboardViewShift({ ...base, cursorRow: 19 })).toBe(400);
  });

  it("puts the cursor row at the visible bottom when the host is shorter than the rows above it", () => {
    // A 200px host clips 600px; row 19 ends at 400px, so the visible area is 200..400.
    expect(keyboardViewShift({ ...base, hostHeight: 200, cursorRow: 19 })).toBe(400);
  });

  it("counts the grid's top padding", () => {
    expect(keyboardViewShift({ ...base, terminalHeight: 810, screenTop: 10, cursorRow: 2 })).toBe(410);
  });

  it("keeps the anchor when nothing is clipped or the view is scrolled back", () => {
    expect(keyboardViewShift({ ...base, hostHeight: 800, cursorRow: 0 })).toBe(0);
    expect(keyboardViewShift({ ...base, cursorRow: 45 })).toBe(0);
    expect(keyboardViewShift({ ...base, cursorRow: -1 })).toBe(0);
    expect(keyboardViewShift({ ...base, rows: 0, cursorRow: 0 })).toBe(0);
  });
});
