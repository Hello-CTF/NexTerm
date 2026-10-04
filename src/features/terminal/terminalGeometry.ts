import type { Terminal } from "@xterm/xterm";
import {
  gridForViewport,
  type TerminalCellMetrics,
  type TerminalGrid,
  type TerminalViewport,
} from "./terminalGrid";

interface BufferLinesLike {
  onTrim?: (listener: (amount: number) => void) => { dispose: () => void };
}

interface SelectionModelLike {
  selectionStart?: [number, number];
  selectionEnd?: [number, number];
  selectionStartLength?: number;
}

interface SelectionServiceLike {
  _model?: SelectionModelLike;
  refresh?: () => void;
  _fireEventIfSelectionChanged?: () => void;
}

interface XtermInternals {
  _renderService?: {
    clear?: () => void;
    dimensions?: {
      css?: {
        cell?: { width?: number; height?: number };
      };
    };
  };
  _selectionService?: SelectionServiceLike;
  _bufferService?: {
    buffers?: { active?: { lines?: BufferLinesLike } };
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

export function resizeTerminalToGridPreservingSelection(term: Terminal, grid: TerminalGrid): void {
  if (term.cols !== grid.cols) {
    resizeTerminalToGrid(term, grid);
    return;
  }
  const pos = term.getSelectionPosition();
  const text = pos ? term.getSelection() : "";
  const lines = internalsOf(term)?._bufferService?.buffers?.active?.lines;
  let trimmed = 0;
  const trimListener =
    lines?.onTrim?.((amount) => {
      trimmed += amount;
    }) ?? null;
  resizeTerminalToGrid(term, grid);
  trimListener?.dispose();
  if (!pos || !text || term.hasSelection()) return;
  if (pos.start.y < trimmed) return;
  restoreSelectionRange(
    term,
    { x: pos.start.x, y: pos.start.y - trimmed },
    { x: pos.end.x, y: pos.end.y - trimmed },
    text,
  );
  if (term.getSelection() !== text) term.clearSelection();
}

function restoreSelectionRange(
  term: Terminal,
  start: { x: number; y: number },
  end: { x: number; y: number },
  text: string,
): void {
  term.select(start.x, start.y, (end.y - start.y) * term.cols + (end.x - start.x));
  if (term.getSelection() === text) return;
  const service = internalsOf(term)?._selectionService;
  const model = service?._model;
  if (!model) return;
  model.selectionStart = [start.x, start.y];
  model.selectionEnd = [end.x, end.y];
  model.selectionStartLength = 0;
  service.refresh?.();
  service._fireEventIfSelectionChanged?.();
}
