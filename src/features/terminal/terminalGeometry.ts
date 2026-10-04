import type { Terminal } from "@xterm/xterm";
import {
  gridForViewport,
  type TerminalCellMetrics,
  type TerminalGrid,
  type TerminalViewport,
} from "./terminalGrid";

interface XtermInternals {
  _renderService?: {
    clear?: () => void;
    dimensions?: {
      css?: {
        cell?: { width?: number; height?: number };
      };
    };
  };
}

export interface TerminalGeometry {
  viewport: TerminalViewport;
  metrics: TerminalCellMetrics;
  grid: TerminalGrid;
}

function internalsOf(term: Terminal): XtermInternals | null {
  return (term as unknown as { _core?: XtermInternals })._core ?? null;
}

function cssPixels(value: string): number {
  const parsed = Number.parseFloat(value);
  return Number.isFinite(parsed) ? parsed : 0;
}

export function measureTerminalGeometry(
  term: Terminal,
  host: HTMLElement,
): TerminalGeometry | null {
  const element = term.element;
  const internals = internalsOf(term);
  const cell = internals?._renderService?.dimensions?.css?.cell;
  if (!element || !cell?.width || !cell.height) return null;

  const style = window.getComputedStyle(element);
  const horizontalPadding = cssPixels(style.paddingLeft) + cssPixels(style.paddingRight);
  const verticalPadding = cssPixels(style.paddingTop) + cssPixels(style.paddingBottom);
  const scrollbar = term.options.scrollback === 0 ? 0 : (term.options.overviewRuler?.width ?? 14);
  const viewport = {
    widthPx: Math.max(0, Math.floor(host.clientWidth - horizontalPadding - scrollbar)),
    heightPx: Math.max(0, Math.floor(host.clientHeight - verticalPadding)),
  };
  const metrics = { widthPx: cell.width, heightPx: cell.height };
  const grid = gridForViewport(viewport, metrics);
  return grid ? { viewport, metrics, grid } : null;
}

export function resizeTerminalToGrid(term: Terminal, grid: TerminalGrid): void {
  if (term.cols === grid.cols && term.rows === grid.rows) return;
  internalsOf(term)?._renderService?.clear?.();
  term.resize(grid.cols, grid.rows);
}
