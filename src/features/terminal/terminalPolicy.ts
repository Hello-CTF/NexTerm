export function resolveWinrmMode(explicit: boolean | undefined, sessionKind: string | undefined): boolean {
  return explicit ?? sessionKind === "winrm";
}

export function shouldFitTerminal(width: number, height: number, canResize: boolean): boolean {
  return canResize && width > 0 && height > 0;
}

export function shouldSendTerminalResize(
  hasKernelTab: boolean,
  applyingRemoteDimensions: boolean,
  canResize: boolean,
): boolean {
  return hasKernelTab && !applyingRemoteDimensions && canResize;
}

export interface ControllerViewport {
  fit: () => void;
  dimensions: () => { cols: number; rows: number };
}

export function dimensionsForControllerClaim(
  handle: ControllerViewport | null,
): { cols: number; rows: number } | null {
  if (!handle) return null;
  handle.fit();
  return handle.dimensions();
}
