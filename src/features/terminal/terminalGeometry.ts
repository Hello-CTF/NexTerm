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
  let trimSeen = false;
  let adjusted: {
    start: { x: number; y: number };
    end: { x: number; y: number };
    text: string;
  } | null = null;
  const trimListener =
    lines?.onTrim?.(() => {
      trimSeen = true;
      const current = term.getSelectionPosition();
      const currentText = current ? term.getSelection() : "";
      adjusted =
        current && currentText
          ? { start: current.start, end: current.end, text: currentText }
          : null;
    }) ?? null;
  try {
    resizeTerminalToGrid(term, grid);
  } finally {
    trimListener?.dispose();
  }
  if (!pos || !text || term.hasSelection()) return;
  const target = trimSeen ? adjusted : { start: pos.start, end: pos.end, text };
  if (!target) return;
  restoreSelectionRange(term, target.start, target.end, target.text);
  if (term.getSelection() !== target.text) term.clearSelection();
}

function restoreSelectionRange(
  term: Terminal,
  start: { x: number; y: number },
  end: { x: number; y: number },
  text: string,
): void {
  term.select(start.x, start.y, (end.y - start.y) * term.cols + (end.x - start.x));
  const restored = term.getSelectionPosition();
  if (
    term.getSelection() === text &&
    restored?.start.x === start.x &&
    restored.start.y === start.y &&
    restored.end.x === end.x &&
    restored.end.y === end.y
  ) {
    return;
  }
  const service = internalsOf(term)?._selectionService;
  const model = service?._model;
  if (!model) return;
  model.selectionStart = [start.x, start.y];
  model.selectionEnd = [end.x, end.y];
  model.selectionStartLength = 0;
  service.refresh?.();
  service._fireEventIfSelectionChanged?.();
}
