// The workspace (terminal) keeps at least this width beside the docks; a session panel that would squeeze it further
// shows as a full-screen sheet instead.
export const WORKSPACE_MAIN_MIN_WIDTH = 360;

// The left rail and each dock's resize handle take this much beside the dock widths.
const RAIL_WIDTH = 48;
const RESIZE_HANDLE_WIDTH = 5;

export interface DockWidths {
  viewportWidth: number;
  leftOpen: boolean;
  leftWidth: number;
  rightOpen: boolean;
  rightWidth: number;
  panelWidth: number;
}

// sessionPanelFits tells whether the session panel can dock beside the open left and AI docks and still leave the
// workspace its minimum width.
export function sessionPanelFits({ viewportWidth, leftOpen, leftWidth, rightOpen, rightWidth, panelWidth }: DockWidths): boolean {
  const docks =
    (leftOpen ? leftWidth + RESIZE_HANDLE_WIDTH : 0) +
    (rightOpen ? rightWidth + RESIZE_HANDLE_WIDTH : 0) +
    panelWidth +
    RESIZE_HANDLE_WIDTH;
  return viewportWidth - RAIL_WIDTH - docks >= WORKSPACE_MAIN_MIN_WIDTH;
}
