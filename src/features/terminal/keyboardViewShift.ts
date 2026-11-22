export interface KeyboardViewGeometry {
  /** Visible height of the terminal host above the soft keyboard. */
  hostHeight: number;
  /** Height of the terminal element, which keeps the grid it had before the keyboard opened. */
  terminalHeight: number;
  /** Offset of the cell grid inside the terminal element (its top padding). */
  screenTop: number;
  screenHeight: number;
  rows: number;
  /** The cursor's row in the visible grid; outside 0..rows-1 when the user has scrolled back. */
  cursorRow: number;
}

/**
 * While the soft keyboard is open the terminal keeps its grid and is anchored to the bottom of the shorter host, so the
 * top rows are clipped. When the cursor sits in those clipped rows (a fresh or cleared screen, or a full-screen program
 * with its cursor near the top), the visible part would be empty rows below it. This returns how far to move the
 * terminal down from the bottom-anchored position so the cursor row ends at the visible bottom; 0 keeps the anchor.
 */
export function keyboardViewShift(geometry: KeyboardViewGeometry): number {
  const { hostHeight, terminalHeight, screenTop, screenHeight, rows, cursorRow } = geometry;
  const values = [hostHeight, terminalHeight, screenTop, screenHeight, rows, cursorRow];
  if (!values.every(Number.isFinite) || rows <= 0 || hostHeight <= 0) return 0;
  const clipped = terminalHeight - hostHeight;
  // A scrolled-back view keeps the bottom anchor: the user is reading history, not typing at the cursor.
  if (clipped <= 0 || cursorRow < 0 || cursorRow >= rows) return 0;
  const cellHeight = screenHeight / rows;
  const cursorBottom = screenTop + (cursorRow + 1) * cellHeight;
  // A cursor row the bottom anchor already shows keeps it, so the rows below the cursor (an editor's status line) stay.
  if (cursorBottom - cellHeight >= clipped) return 0;
  const visibleTop = Math.max(0, cursorBottom - hostHeight);
  return Math.round(clipped - visibleTop);
}
